package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	defaultMatchSize = 12
)

type matchTicket struct {
	player   Player
	playerID string
	queuedAt time.Time
	status   string
	instance *GameInstance
	error    string
	partyID  string
}

type matchGroup struct {
	id        string
	ticketIDs []string
	queuedAt  time.Time
}

type MatchmakingManager interface {
	StartGameInstance(ctx context.Context, players []Player, requestedPort string) (GameInstance, error)
	ListInstances() []GameInstance
}

type QueueMember struct {
	PlayerID string `json:"playerId"`
	Username string `json:"username"`
}

type QueueContext struct {
	RequesterPlayerID string        `json:"requesterPlayerId"`
	PartyID           string        `json:"partyId"`
	Members           []QueueMember `json:"members"`
}

type ticketStatus struct {
	PlayerID string        `json:"playerId"`
	Username string        `json:"username"`
	TicketID string        `json:"ticketId"`
	Status   string        `json:"status"`
	PartyID  string        `json:"partyId"`
	Instance *GameInstance `json:"instance,omitempty"`
	Error    string        `json:"error,omitempty"`
}

type MatchmakingAPI struct {
	region              string
	manager             MatchmakingManager
	matchSize           int
	mu                  sync.Mutex
	waiting             []matchGroup
	tickets             map[string]*matchTicket
	startedAt           time.Time
	rng                 *rand.Rand
	resolveQueueContext func(ctx context.Context, sessionToken string) (QueueContext, error)
}

func NewMatchmakingAPI(region string, manager MatchmakingManager) *MatchmakingAPI {
	api := &MatchmakingAPI{
		region:              region,
		manager:             manager,
		matchSize:           defaultMatchSize,
		waiting:             []matchGroup{},
		tickets:             make(map[string]*matchTicket),
		startedAt:           time.Now().UTC(),
		rng:                 rand.New(rand.NewSource(time.Now().UnixNano())),
		resolveQueueContext: resolveQueueContextFromSession,
	}

	go api.matchLoop()
	return api
}

func (api *MatchmakingAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/matchmaking/queue", api.handleQueue)
	mux.HandleFunc("/matchmaking/join", api.handleJoin)
	mux.HandleFunc("/matchmaking/status", api.handleStatus)
	mux.HandleFunc("/matchmaking/cancel", api.handleCancel)
}

func (api *MatchmakingAPI) SetMatchSize(size int) {
	if size <= 0 {
		return
	}
	api.mu.Lock()
	api.matchSize = size
	api.mu.Unlock()
}

func (api *MatchmakingAPI) SetQueueContextResolverForTesting(resolver func(ctx context.Context, sessionToken string) (QueueContext, error)) {
	if resolver == nil {
		return
	}
	api.mu.Lock()
	api.resolveQueueContext = resolver
	api.mu.Unlock()
}

func (api *MatchmakingAPI) handleQueue(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var queueRequest struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.NewDecoder(request.Body).Decode(&queueRequest); err != nil {
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if queueRequest.SessionToken == "" {
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	api.mu.Lock()
	resolver := api.resolveQueueContext
	api.mu.Unlock()
	queueContext, err := resolver(request.Context(), queueRequest.SessionToken)
	if err != nil {
		if errors.Is(err, errInvalidSessionToken) {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	partyID, ticketIDs, playerTicketMap, err := api.enqueueGroup(request.Context(), queueContext.PartyID, queueContext.Members)
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	ticketAssignments := make([]map[string]string, 0, len(queueContext.Members))
	for _, member := range queueContext.Members {
		ticketAssignments = append(ticketAssignments, map[string]string{
			"playerId": member.PlayerID,
			"username": member.Username,
			"ticketId": playerTicketMap[member.PlayerID],
		})
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":            "queued",
		"partyId":           partyID,
		"ticketId":          playerTicketMap[queueContext.RequesterPlayerID],
		"ticketIds":         ticketIDs,
		"ticketAssignments": ticketAssignments,
	})
}

func generateMatchmakingTicket() string {
	uuid, err := uuid.NewRandom()
	if err != nil {
		return "ticket-" + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return "ticket-" + uuid.String()
}

func (api *MatchmakingAPI) handleJoin(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var joinRequest struct {
		TicketID string `json:"ticketId"`
	}
	if err := json.NewDecoder(request.Body).Decode(&joinRequest); err != nil {
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if joinRequest.TicketID == "" {
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	ticketPayload, found, err := loadTicketStatusByIDFromDB(request.Context(), joinRequest.TicketID)
	if err != nil || !found {
		api.mu.Lock()
		ticket, ok := api.tickets[joinRequest.TicketID]
		api.mu.Unlock()
		if !ok {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		ticketPayload = ticketStatus{
			PlayerID: ticket.playerID,
			Username: ticket.player.Username,
			TicketID: joinRequest.TicketID,
			Status:   ticket.status,
			PartyID:  ticket.partyID,
			Instance: ticket.instance,
			Error:    ticket.error,
		}
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":   ticketPayload.Status,
		"ticketId": ticketPayload.TicketID,
		"partyId":  ticketPayload.PartyID,
		"instance": ticketPayload.Instance,
		"error":    ticketPayload.Error,
	})
}

func (api *MatchmakingAPI) handleStatus(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	statusRequest := struct {
		SessionToken string `json:"sessionToken"`
		TicketID     string `json:"ticketId"`
	}{}

	if request.Method == http.MethodGet {
		statusRequest.SessionToken = request.URL.Query().Get("sessionToken")
		statusRequest.TicketID = request.URL.Query().Get("ticketId")
	} else {
		if err := json.NewDecoder(request.Body).Decode(&statusRequest); err != nil {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
	}

	if statusRequest.SessionToken == "" {
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	api.mu.Lock()
	resolver := api.resolveQueueContext
	api.mu.Unlock()
	queueContext, err := resolver(request.Context(), statusRequest.SessionToken)
	if err != nil {
		if errors.Is(err, errInvalidSessionToken) {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	memberSet := make(map[string]struct{}, len(queueContext.Members))
	for _, member := range queueContext.Members {
		memberSet[member.PlayerID] = struct{}{}
	}

	getTicketStatus := func(ticketID string) (ticketStatus, bool, error) {
		dbStatus, found, dbErr := loadTicketStatusByIDFromDB(request.Context(), ticketID)
		if dbErr == nil && found {
			if _, allowed := memberSet[dbStatus.PlayerID]; !allowed {
				return ticketStatus{}, false, nil
			}
			api.mu.Lock()
			if ticket, ok := api.tickets[ticketID]; ok {
				if ticket.instance != nil {
					dbStatus.Instance = ticket.instance
				}
				if ticket.error != "" {
					dbStatus.Error = ticket.error
				}
			}
			api.mu.Unlock()
			return dbStatus, true, nil
		}

		api.mu.Lock()
		ticket, ok := api.tickets[ticketID]
		api.mu.Unlock()
		if !ok {
			return ticketStatus{}, false, nil
		}
		if _, allowed := memberSet[ticket.playerID]; !allowed {
			return ticketStatus{}, false, nil
		}
		return ticketStatus{
			PlayerID: ticket.playerID,
			Username: ticket.player.Username,
			TicketID: ticketID,
			Status:   ticket.status,
			PartyID:  ticket.partyID,
			Instance: ticket.instance,
			Error:    ticket.error,
		}, true, nil
	}

	if statusRequest.TicketID != "" {
		ticketPayload, ok, ticketErr := getTicketStatus(statusRequest.TicketID)
		if ticketErr != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !ok {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		matchedPort := ""
		if ticketPayload.Status == "matched" && ticketPayload.Instance != nil {
			matchedPort = ticketPayload.Instance.Port
		}

		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":   ticketPayload.Status,
			"ticketId": ticketPayload.TicketID,
			"partyId":  ticketPayload.PartyID,
			"playerId": ticketPayload.PlayerID,
			"username": ticketPayload.Username,
			"region":   api.region,
			"port":     matchedPort,
			"error":    ticketPayload.Error,
		})
		return
	}

	collectedStatuses, dbErr := loadLatestTicketStatusesFromDB(request.Context(), queueContext)
	if dbErr != nil {
		api.mu.Lock()
		collectedStatuses = make([]ticketStatus, 0, len(api.tickets))
		for ticketID, ticket := range api.tickets {
			if _, allowed := memberSet[ticket.playerID]; !allowed {
				continue
			}
			collectedStatuses = append(collectedStatuses, ticketStatus{
				PlayerID: ticket.playerID,
				Username: ticket.player.Username,
				TicketID: ticketID,
				Status:   ticket.status,
				PartyID:  ticket.partyID,
				Instance: ticket.instance,
				Error:    ticket.error,
			})
		}
		api.mu.Unlock()
	} else {
		api.mu.Lock()
		for i := range collectedStatuses {
			if ticket, ok := api.tickets[collectedStatuses[i].TicketID]; ok {
				if ticket.instance != nil {
					collectedStatuses[i].Instance = ticket.instance
				}
				if ticket.error != "" {
					collectedStatuses[i].Error = ticket.error
				}
			}
		}
		api.mu.Unlock()
	}

	sort.Slice(collectedStatuses, func(i, j int) bool {
		if collectedStatuses[i].PlayerID == collectedStatuses[j].PlayerID {
			return collectedStatuses[i].TicketID < collectedStatuses[j].TicketID
		}
		return collectedStatuses[i].PlayerID < collectedStatuses[j].PlayerID
	})

	overallStatus := "not_queued"
	for _, status := range collectedStatuses {
		switch status.Status {
		case "error":
			overallStatus = "error"
		case "matched":
			if overallStatus != "error" {
				overallStatus = "matched"
			}
		case "searching":
			if overallStatus != "error" && overallStatus != "matched" {
				overallStatus = "searching"
			}
		}
	}

	ownTicketID := ""
	matchedPort := ""
	for _, status := range collectedStatuses {
		if status.PlayerID == queueContext.RequesterPlayerID {
			ownTicketID = status.TicketID
			if status.Status == "matched" && status.Instance != nil {
				matchedPort = status.Instance.Port
			}
			break
		}
	}

	if matchedPort == "" && overallStatus == "matched" {
		for _, status := range collectedStatuses {
			if status.Status == "matched" && status.Instance != nil {
				matchedPort = status.Instance.Port
				break
			}
		}
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":   overallStatus,
		"ticketId": ownTicketID,
		"port":     matchedPort,
		"partyId":  queueContext.PartyID,
		"playerId": queueContext.RequesterPlayerID,
		"region":   api.region,
		"tickets":  collectedStatuses,
		"members":  queueContext.Members,
	})
}

func (api *MatchmakingAPI) handleCancel(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ticketID := request.URL.Query().Get("ticketId")
	api.mu.Lock()
	if ticket, ok := api.tickets[ticketID]; ok {
		ticket.status = "cancelled"
	}
	api.waiting = removeTicket(api.waiting, ticketID)
	api.mu.Unlock()

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":   "cancelled",
		"ticketId": ticketID,
	})
}

func (api *MatchmakingAPI) enqueueGroup(ctx context.Context, partyID string, members []QueueMember) (string, []string, map[string]string, error) {
	if len(members) == 0 {
		return "", nil, nil, fmt.Errorf("no players found for queue")
	}

	mmDB, err := GetMMDB(ctx)
	if err != nil {
		return "", nil, nil, err
	}

	api.mu.Lock()
	defer api.mu.Unlock()

	if partyID == "" {
		partyID = "party-" + api.randomSuffix()
	}

	ticketIDs := make([]string, 0, len(members))
	playerTicketMap := make(map[string]string, len(members))
	for _, member := range members {
		ticket, err := api.generateTicketLocked(ctx, mmDB)
		if err != nil {
			return "", nil, nil, err
		}
		player := Player{Username: member.Username}
		player.Ticket = ticket
		api.tickets[ticket] = &matchTicket{
			player:   player,
			playerID: member.PlayerID,
			queuedAt: time.Now().UTC(),
			status:   "searching",
			partyID:  partyID,
		}
		ticketIDs = append(ticketIDs, ticket)
		playerTicketMap[member.PlayerID] = ticket

		if err := persistMatchmakingTicket(ctx, mmDB, ticket, member.PlayerID, partyID); err != nil {
			return "", nil, nil, err
		}
	}

	api.waiting = append(api.waiting, matchGroup{
		id:        partyID,
		ticketIDs: ticketIDs,
		queuedAt:  time.Now().UTC(),
	})

	return partyID, ticketIDs, playerTicketMap, nil
}

func (api *MatchmakingAPI) matchLoop() {
	for range time.NewTicker(1 * time.Second).C {
		api.tryCreateMatch()
	}
}

func (api *MatchmakingAPI) tryCreateMatch() {
	ctx := context.Background()
	mmDB, err := GetMMDB(ctx)
	if err != nil {
		return
	}

	api.mu.Lock()
	targetMatchSize := api.matchSize
	api.mu.Unlock()

	groups, err := loadQueueGroupsFromDB(ctx, mmDB)
	if err != nil || len(groups) == 0 {
		return
	}
	selectedRows := selectTicketsForMatch(groups, targetMatchSize)
	if len(selectedRows) == 0 {
		return
	}

	selectedTicketIDs := make([]string, 0, len(selectedRows))
	for _, row := range selectedRows {
		selectedTicketIDs = append(selectedTicketIDs, row.TicketID)
	}

	claimed, claimErr := claimTicketsForMatch(ctx, mmDB, selectedTicketIDs)
	if claimErr != nil || !claimed {
		return
	}

	players, loadErr := loadPlayersForRows(ctx, selectedRows)
	if loadErr != nil {
		_ = updateTicketStatuses(ctx, mmDB, selectedTicketIDs, "error", nil)
		api.setTicketStateForIDs(selectedRows, "error", nil, loadErr.Error())
		return
	}

	matchCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	instance, startErr := api.manager.StartGameInstance(matchCtx, players, "")
	cancel()
	if startErr != nil {
		_ = updateTicketStatuses(ctx, mmDB, selectedTicketIDs, "error", nil)
		api.setTicketStateForIDs(selectedRows, "error", nil, startErr.Error())
		return
	}

	if err := persistMatchResult(ctx, mmDB, instance, selectedRows); err != nil {
		_ = updateTicketStatuses(ctx, mmDB, selectedTicketIDs, "error", nil)
		api.setTicketStateForIDs(selectedRows, "error", nil, err.Error())
		return
	}

	api.setTicketStateForIDs(selectedRows, "matched", &instance, "")
}

func (api *MatchmakingAPI) generateTicketLocked(ctx context.Context, mmDB *Database) (string, error) {
	for i := 0; i < 32; i++ {
		ticket := generateMatchmakingTicket()
		if _, exists := api.tickets[ticket]; exists {
			continue
		}
		exists, err := ticketExistsInDB(ctx, mmDB, ticket)
		if err != nil {
			return "", err
		}
		if exists {
			continue
		}
		return ticket, nil
	}

	return "", fmt.Errorf("failed to generate unique matchmaking ticket")
}

func (api *MatchmakingAPI) randomSuffix() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), api.rng.Intn(100000))
}

func (api *MatchmakingAPI) setTicketStateForIDs(rows []queueTicketRow, status string, instance *GameInstance, ticketErr string) {
	api.mu.Lock()
	defer api.mu.Unlock()

	for _, row := range rows {
		ticket, exists := api.tickets[row.TicketID]
		if !exists {
			ticket = &matchTicket{
				player: Player{
					Username: row.Username,
					Ticket:   row.TicketID,
				},
				playerID: row.PlayerID,
				partyID:  row.PartyID,
			}
			api.tickets[row.TicketID] = ticket
		}

		ticket.status = status
		ticket.error = ticketErr
		if instance != nil {
			copyInstance := *instance
			ticket.instance = &copyInstance
		}
	}
}

func removeTicket(queue []matchGroup, ticketID string) []matchGroup {
	for groupIndex, group := range queue {
		updatedTickets := make([]string, 0, len(group.ticketIDs))
		for _, id := range group.ticketIDs {
			if id != ticketID {
				updatedTickets = append(updatedTickets, id)
			}
		}
		if len(updatedTickets) == len(group.ticketIDs) {
			continue
		}
		if len(updatedTickets) == 0 {
			return append(queue[:groupIndex], queue[groupIndex+1:]...)
		}
		queue[groupIndex].ticketIDs = updatedTickets
		return queue
	}
	return queue
}

var errInvalidSessionToken = errors.New("invalid session token")

func resolveQueueContextFromSession(ctx context.Context, sessionToken string) (QueueContext, error) {
	if sessionToken == "" {
		return QueueContext{}, errInvalidSessionToken
	}

	requesterPlayerID, err := getPlayerIDFromSession(sessionToken)
	if err != nil {
		if err.Error() == "invalid session token" {
			return QueueContext{}, errInvalidSessionToken
		}
		return QueueContext{}, err
	}

	mmDB, err := GetMMDB(ctx)
	if err != nil {
		return QueueContext{}, err
	}

	partyID := ""
	partyQuery := `SELECT p.party_id
		FROM party_players pp
		JOIN parties p ON p.party_id = pp.party_id
		WHERE pp.player_id = $1
		LIMIT 1`
	partyRows, err := submitQuery(ctx, mmDB.DB, partyQuery, requesterPlayerID)
	if err != nil {
		return QueueContext{}, err
	}
	if partyRows.Next() {
		if scanErr := partyRows.Scan(&partyID); scanErr != nil {
			_ = partyRows.Close()
			return QueueContext{}, scanErr
		}
	}
	if closeErr := partyRows.Close(); closeErr != nil {
		return QueueContext{}, closeErr
	}

	memberIDs := []string{requesterPlayerID}
	if partyID != "" {
		memberIDs = make([]string, 0, 4)
		memberQuery := `SELECT player_id FROM party_players WHERE party_id = $1 ORDER BY player_id`
		memberRows, memberErr := submitQuery(ctx, mmDB.DB, memberQuery, partyID)
		if memberErr != nil {
			return QueueContext{}, memberErr
		}
		for memberRows.Next() {
			var memberID string
			if scanErr := memberRows.Scan(&memberID); scanErr != nil {
				_ = memberRows.Close()
				return QueueContext{}, scanErr
			}
			memberIDs = append(memberIDs, memberID)
		}
		if closeErr := memberRows.Close(); closeErr != nil {
			return QueueContext{}, closeErr
		}
		if len(memberIDs) == 0 {
			memberIDs = append(memberIDs, requesterPlayerID)
		}
	}

	playerDB, err := GetDatabase(ctx)
	if err != nil {
		return QueueContext{}, err
	}

	members := make([]QueueMember, 0, len(memberIDs))
	for _, memberID := range memberIDs {
		username := memberID
		usernameQuery := "SELECT account_name FROM players WHERE id = $1"
		usernameRows, queryErr := submitQuery(ctx, playerDB.DB, usernameQuery, memberID)
		if queryErr != nil {
			return QueueContext{}, queryErr
		}
		if usernameRows.Next() {
			if scanErr := usernameRows.Scan(&username); scanErr != nil {
				_ = usernameRows.Close()
				return QueueContext{}, scanErr
			}
		}
		if closeErr := usernameRows.Close(); closeErr != nil {
			return QueueContext{}, closeErr
		}
		members = append(members, QueueMember{PlayerID: memberID, Username: username})
	}

	return QueueContext{
		RequesterPlayerID: requesterPlayerID,
		PartyID:           partyID,
		Members:           members,
	}, nil
}

func ticketExistsInDB(ctx context.Context, mmDB *Database, ticketID string) (bool, error) {
	rows, err := submitQuery(ctx, mmDB.DB, "SELECT COUNT(*) FROM matchmaking_tickets WHERE ticket_id = $1", ticketID)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	var count int
	if rows.Next() {
		if scanErr := rows.Scan(&count); scanErr != nil {
			return false, scanErr
		}
		return count > 0, nil
	}

	if err := rows.Err(); err != nil {
		return false, err
	}

	return false, nil
}

func persistMatchmakingTicket(ctx context.Context, mmDB *Database, ticketID string, playerID string, partyID string) error {
	if mmDB == nil || mmDB.DB == nil {
		return nil
	}

	clearQuery := "DELETE FROM matchmaking_tickets WHERE player_id = $1 AND status IN ('queued', 'searching')"
	if _, err := submitExec(ctx, mmDB.DB, clearQuery, playerID); err != nil {
		return err
	}

	insertQuery := `INSERT INTO matchmaking_tickets (ticket_id, player_id, party_id, status, queued_at)
		VALUES ($1, $2, $3, 'queued', $4)`
	_, err := submitExec(ctx, mmDB.DB, insertQuery, ticketID, playerID, partyID, time.Now().UTC())
	return err
}

func loadLatestTicketStatusesFromDB(ctx context.Context, queueContext QueueContext) ([]ticketStatus, error) {
	mmDB, err := GetMMDB(ctx)
	if err != nil {
		return nil, err
	}

	usernameByPlayerID := make(map[string]string, len(queueContext.Members))
	for _, member := range queueContext.Members {
		usernameByPlayerID[member.PlayerID] = member.Username
	}

	statuses := make([]ticketStatus, 0, len(queueContext.Members))
	for _, member := range queueContext.Members {
		rows, queryErr := submitQuery(
			ctx,
			mmDB.DB,
			`SELECT mt.ticket_id, COALESCE(mt.party_id, ''), mt.status, COALESCE(g.game_id, ''), COALESCE(g.ip_addr, ''), COALESCE(g.port, '')
			 FROM matchmaking_tickets
			 AS mt
			 LEFT JOIN games g ON g.game_id = mt.game_id
			 WHERE player_id = $1
			 ORDER BY mt.queued_at DESC
			 LIMIT 1`,
			member.PlayerID,
		)
		if queryErr != nil {
			return nil, queryErr
		}

		if rows.Next() {
			var dbTicketID string
			var dbPartyID string
			var dbStatus string
			var gameID string
			var gameHost string
			var gamePort string
			if scanErr := rows.Scan(&dbTicketID, &dbPartyID, &dbStatus, &gameID, &gameHost, &gamePort); scanErr != nil {
				_ = rows.Close()
				return nil, scanErr
			}

			statusValue := normalizeTicketStatus(dbStatus)

			if queueContext.PartyID != "" && dbPartyID != "" && dbPartyID != queueContext.PartyID {
				_ = rows.Close()
				continue
			}

			var instance *GameInstance
			if gameID != "" {
				instance = &GameInstance{
					ID:   gameID,
					Host: gameHost,
					Port: gamePort,
				}
			}

			statuses = append(statuses, ticketStatus{
				PlayerID: member.PlayerID,
				Username: usernameByPlayerID[member.PlayerID],
				TicketID: dbTicketID,
				Status:   statusValue,
				PartyID:  dbPartyID,
				Instance: instance,
			})
		}

		if closeErr := rows.Close(); closeErr != nil {
			return nil, closeErr
		}
	}

	return statuses, nil
}

func loadTicketStatusByIDFromDB(ctx context.Context, ticketID string) (ticketStatus, bool, error) {
	mmDB, err := GetMMDB(ctx)
	if err != nil {
		return ticketStatus{}, false, err
	}

	rows, err := submitQuery(
		ctx,
		mmDB.DB,
		`SELECT mt.ticket_id, mt.player_id, COALESCE(mt.party_id, ''), mt.status, COALESCE(g.game_id, ''), COALESCE(g.ip_addr, ''), COALESCE(g.port, '')
		 FROM matchmaking_tickets mt
		 LEFT JOIN games g ON g.game_id = mt.game_id
		 WHERE mt.ticket_id = $1
		 LIMIT 1`,
		ticketID,
	)
	if err != nil {
		return ticketStatus{}, false, err
	}
	defer rows.Close()

	if !rows.Next() {
		return ticketStatus{}, false, nil
	}

	var dbTicketID string
	var playerID string
	var partyID string
	var rawStatus string
	var gameID string
	var gameHost string
	var gamePort string
	if err := rows.Scan(&dbTicketID, &playerID, &partyID, &rawStatus, &gameID, &gameHost, &gamePort); err != nil {
		return ticketStatus{}, false, err
	}

	username := playerID
	playerDB, err := GetDatabase(ctx)
	if err == nil {
		nameRows, queryErr := submitQuery(ctx, playerDB.DB, "SELECT account_name FROM players WHERE id = $1", playerID)
		if queryErr == nil {
			if nameRows.Next() {
				_ = nameRows.Scan(&username)
			}
			_ = nameRows.Close()
		}
	}

	var instance *GameInstance
	if gameID != "" {
		instance = &GameInstance{ID: gameID, Host: gameHost, Port: gamePort}
	}

	return ticketStatus{
		PlayerID: playerID,
		Username: username,
		TicketID: dbTicketID,
		Status:   normalizeTicketStatus(rawStatus),
		PartyID:  partyID,
		Instance: instance,
	}, true, nil
}

type queueTicketRow struct {
	TicketID string
	PlayerID string
	PartyID  string
	QueuedAt time.Time
	Username string
}

type queueTicketGroup struct {
	PartyID string
	Rows    []queueTicketRow
}

func loadQueueGroupsFromDB(ctx context.Context, mmDB *Database) ([]queueTicketGroup, error) {
	rows, err := submitQuery(
		ctx,
		mmDB.DB,
		`SELECT ticket_id, player_id, COALESCE(party_id, ''), queued_at
		 FROM matchmaking_tickets
		 WHERE status IN ('queued', 'searching')
		 ORDER BY queued_at ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groupsByKey := map[string]*queueTicketGroup{}
	orderedKeys := make([]string, 0)
	for rows.Next() {
		var row queueTicketRow
		if scanErr := rows.Scan(&row.TicketID, &row.PlayerID, &row.PartyID, &row.QueuedAt); scanErr != nil {
			return nil, scanErr
		}

		groupKey := row.PartyID
		if groupKey == "" {
			groupKey = "solo-" + row.PlayerID
		}
		group, exists := groupsByKey[groupKey]
		if !exists {
			group = &queueTicketGroup{PartyID: row.PartyID, Rows: make([]queueTicketRow, 0, 4)}
			groupsByKey[groupKey] = group
			orderedKeys = append(orderedKeys, groupKey)
		}
		group.Rows = append(group.Rows, row)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	groups := make([]queueTicketGroup, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		groups = append(groups, *groupsByKey[key])
	}
	return groups, nil
}

func selectTicketsForMatch(groups []queueTicketGroup, matchSize int) []queueTicketRow {
	selected := make([]queueTicketRow, 0, matchSize)
	for _, group := range groups {
		groupSize := len(group.Rows)
		if len(selected) == 0 && groupSize > matchSize {
			selected = append(selected, group.Rows...)
			break
		}
		if len(selected)+groupSize > matchSize {
			break
		}
		selected = append(selected, group.Rows...)
		if len(selected) == matchSize {
			break
		}
	}
	return selected
}

func claimTicketsForMatch(ctx context.Context, mmDB *Database, ticketIDs []string) (bool, error) {
	if len(ticketIDs) == 0 {
		return false, nil
	}

	query := fmt.Sprintf(
		"UPDATE matchmaking_tickets SET status = 'matching' WHERE ticket_id IN (%s) AND status IN ('queued', 'searching')",
		buildPlaceholderList(1, len(ticketIDs)),
	)

	args := make([]any, 0, len(ticketIDs))
	for _, ticketID := range ticketIDs {
		args = append(args, ticketID)
	}

	result, err := submitExec(ctx, mmDB.DB, query, args...)
	if err != nil {
		return false, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}

	return int(affected) == len(ticketIDs), nil
}

func loadPlayersForRows(ctx context.Context, rows []queueTicketRow) ([]Player, error) {
	playerDB, err := GetDatabase(ctx)
	if err != nil {
		return nil, err
	}

	players := make([]Player, 0, len(rows))
	for _, row := range rows {
		username := row.PlayerID
		nameRows, queryErr := submitQuery(ctx, playerDB.DB, "SELECT account_name FROM players WHERE id = $1", row.PlayerID)
		if queryErr != nil {
			return nil, queryErr
		}
		if nameRows.Next() {
			_ = nameRows.Scan(&username)
		}
		if closeErr := nameRows.Close(); closeErr != nil {
			return nil, closeErr
		}

		players = append(players, Player{
			Username: username,
			Ticket:   row.TicketID,
		})
		rowsIndex := len(players) - 1
		rows[rowsIndex].Username = username
	}

	return players, nil
}

func persistMatchResult(ctx context.Context, mmDB *Database, instance GameInstance, rows []queueTicketRow) error {
	gameID := instance.ID
	if gameID == "" {
		gameID = "game-" + fmt.Sprintf("%d", time.Now().UnixNano())
	}

	gameInsert := `INSERT INTO games (game_id, ip_addr, port, status, created_at)
		VALUES ($1, $2, $3, 'ready', $4)
		ON CONFLICT (game_id) DO UPDATE SET ip_addr = EXCLUDED.ip_addr, port = EXCLUDED.port, status = EXCLUDED.status`
	if _, err := submitExec(ctx, mmDB.DB, gameInsert, gameID, instance.Host, instance.Port, time.Now().UTC()); err != nil {
		return err
	}

	for _, row := range rows {
		if _, err := submitExec(ctx, mmDB.DB, "INSERT INTO game_players (player_id, game_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", row.PlayerID, gameID); err != nil {
			return err
		}
	}

	ticketIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		ticketIDs = append(ticketIDs, row.TicketID)
	}

	return updateTicketStatuses(ctx, mmDB, ticketIDs, "matched", &gameID)
}

func updateTicketStatuses(ctx context.Context, mmDB *Database, ticketIDs []string, status string, gameID *string) error {
	if len(ticketIDs) == 0 {
		return nil
	}

	if gameID == nil {
		query := fmt.Sprintf("UPDATE matchmaking_tickets SET status = $1 WHERE ticket_id IN (%s)", buildPlaceholderList(2, len(ticketIDs)))
		args := make([]any, 0, 1+len(ticketIDs))
		args = append(args, status)
		for _, ticketID := range ticketIDs {
			args = append(args, ticketID)
		}
		_, err := submitExec(ctx, mmDB.DB, query, args...)
		return err
	}

	query := fmt.Sprintf("UPDATE matchmaking_tickets SET status = $1, game_id = $2 WHERE ticket_id IN (%s)", buildPlaceholderList(3, len(ticketIDs)))
	args := make([]any, 0, 2+len(ticketIDs))
	args = append(args, status, *gameID)
	for _, ticketID := range ticketIDs {
		args = append(args, ticketID)
	}
	_, err := submitExec(ctx, mmDB.DB, query, args...)
	return err
}

func buildPlaceholderList(start int, count int) string {
	placeholders := make([]string, 0, count)
	for i := 0; i < count; i++ {
		placeholders = append(placeholders, fmt.Sprintf("$%d", start+i))
	}
	return strings.Join(placeholders, ",")
}

func normalizeTicketStatus(status string) string {
	switch status {
	case "queued", "matching":
		return "searching"
	default:
		return status
	}
}

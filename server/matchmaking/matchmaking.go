package matchmaking

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	server "fracturedexodusserver/server"
	ws "fracturedexodusserver/server/ws"
)

const (
	defaultMatchSize     = 12
	maxPlayersPerFaction = 4
	// defaultMatchStartWait = 30 * time.Second
	defaultMatchStartWait = 10 * time.Second
)

type matchTicket struct {
	player   server.Player
	playerID string
	queuedAt time.Time
	status   string
	instance *server.GameInstance
	error    string
	partyID  string
}

type matchGroup struct {
	id        string
	ticketIDs []string
	queuedAt  time.Time
}

// MatchmakingManager is the interface game server managers must implement.
type MatchmakingManager interface {
	StartGameInstance(ctx context.Context, players []server.Player, requestedPort string) (server.GameInstance, error)
	ListInstances() []server.GameInstance
}

// QueueMember represents a player in a matchmaking queue context.
type QueueMember struct {
	PlayerID string `json:"playerId"`
	Username string `json:"username"`
}

// QueueContext holds the resolved party/member info for a queuing session.
type QueueContext struct {
	RequesterPlayerID string        `json:"requesterPlayerId"`
	PartyID           string        `json:"partyId"`
	Members           []QueueMember `json:"members"`
}

type ticketStatus struct {
	PlayerID string               `json:"playerId"`
	Username string               `json:"username"`
	TicketID string               `json:"ticketId"`
	Status   string               `json:"status"`
	PartyID  string               `json:"partyId"`
	Instance *server.GameInstance `json:"instance,omitempty"`
	Error    string               `json:"error,omitempty"`
}

// MatchmakingAPI manages matchmaking queues, parties, and match creation.
type MatchmakingAPI struct {
	region              string
	manager             MatchmakingManager
	matchSize           int
	matchStartWait      time.Duration
	mu                  sync.Mutex
	waiting             []matchGroup
	tickets             map[string]*matchTicket
	startedAt           time.Time
	rng                 *rand.Rand
	resolveQueueContext func(ctx context.Context, sessionToken string) (QueueContext, error)
	stopCh              chan struct{}
	hub                 *ws.Hub
}

// NewMatchmakingAPI creates and starts a MatchmakingAPI.
func NewMatchmakingAPI(region string, manager MatchmakingManager) *MatchmakingAPI {
	api := &MatchmakingAPI{
		region:              region,
		manager:             manager,
		matchSize:           defaultMatchSize,
		matchStartWait:      defaultMatchStartWait,
		waiting:             []matchGroup{},
		tickets:             make(map[string]*matchTicket),
		startedAt:           time.Now().UTC(),
		rng:                 rand.New(rand.NewSource(time.Now().UnixNano())),
		resolveQueueContext: resolveQueueContextFromSession,
		stopCh:              make(chan struct{}),
	}

	go api.matchLoop()
	return api
}

// Close stops the matchmaking loop goroutine.
func (api *MatchmakingAPI) Close() {
	close(api.stopCh)
}

// RegisterRoutes registers all matchmaking HTTP endpoints.
func (api *MatchmakingAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/matchmaking/queue", api.handleQueue)
	mux.HandleFunc("/matchmaking/join", api.handleJoin)
	mux.HandleFunc("/matchmaking/server/register", api.handleRegisterServer)
	mux.HandleFunc("/matchmaking/match/ended", api.handleMatchEnded)
	mux.HandleFunc("/matchmaking/joined", api.handleJoined)
	mux.HandleFunc("/matchmaking/left", api.handleLeft)
	mux.HandleFunc("/matchmaking/heartbeat", api.handleHeartbeat)
	mux.HandleFunc("/matchmaking/status", api.handleStatus)
	mux.HandleFunc("/matchmaking/cancel", api.handleCancel)
	mux.HandleFunc("/matchmaking/party/invite", api.handlePartyInvite)
	mux.HandleFunc("/matchmaking/party/respond", api.handlePartyRespond)
	mux.HandleFunc("/matchmaking/party/leave", api.handlePartyLeave)
	mux.HandleFunc("/matchmaking/party/invites", api.handlePartyInvites)
	mux.HandleFunc("/matchmaking/party/status", api.handlePartyStatus)
}

// SetMatchSize overrides the default match size.
func (api *MatchmakingAPI) SetMatchSize(size int) {
	if size <= 0 {
		return
	}
	api.mu.Lock()
	api.matchSize = size
	api.mu.Unlock()
}

// SetQueueContextResolverForTesting injects a custom resolver for tests.
func (api *MatchmakingAPI) SetQueueContextResolverForTesting(resolver func(ctx context.Context, sessionToken string) (QueueContext, error)) {
	if resolver == nil {
		return
	}
	api.mu.Lock()
	api.resolveQueueContext = resolver
	api.mu.Unlock()
}

// SetMatchStartWaitForTesting overrides the match start wait for tests.
func (api *MatchmakingAPI) SetMatchStartWaitForTesting(wait time.Duration) {
	if wait < 0 {
		return
	}
	api.mu.Lock()
	api.matchStartWait = wait
	api.mu.Unlock()
}

// SetHub wires up the WebSocket push hub. Until this is called, api.hub is nil and every push
// call site is a no-op (see e.g. pushTicketStatuses) — existing callers/tests that construct a
// MatchmakingAPI without a hub keep working unchanged.
func (api *MatchmakingAPI) SetHub(hub *ws.Hub) {
	api.mu.Lock()
	api.hub = hub
	api.mu.Unlock()
}

var errInvalidSessionToken = errors.New("invalid session token")
var errInvalidServerToken = errors.New("invalid server token")

func generateMatchmakingTicket() string {
	id, err := uuid.NewRandom()
	if err != nil {
		return "ticket-" + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return "ticket-" + id.String()
}

func (api *MatchmakingAPI) enqueueGroup(ctx context.Context, partyID string, members []QueueMember) (string, []string, map[string]string, error) {
	if len(members) == 0 {
		return "", nil, nil, fmt.Errorf("no players found for queue")
	}

	mmDB, err := server.GetMMDB(ctx)
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
		player := server.Player{Username: member.Username}
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
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-api.stopCh:
			return
		case <-ticker.C:
			api.tryCreateMatch()
		}
	}
}

func (api *MatchmakingAPI) tryCreateMatch() {
	ctx := context.Background()
	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		return
	}

	api.mu.Lock()
	targetMatchSize := api.matchSize
	matchStartWait := api.matchStartWait
	api.mu.Unlock()

	groups, err := loadQueueGroupsFromDB(ctx, mmDB)
	if err != nil || len(groups) == 0 {
		return
	}
	selectedRows := selectTicketsForMatch(groups, targetMatchSize)
	if len(selectedRows) == 0 {
		return
	}

	if len(selectedRows) < targetMatchSize {
		oldestQueuedAt := selectedRows[0].QueuedAt
		for _, row := range selectedRows[1:] {
			if row.QueuedAt.Before(oldestQueuedAt) {
				oldestQueuedAt = row.QueuedAt
			}
		}

		if time.Since(oldestQueuedAt) < matchStartWait {
			return
		}
	}

	selectedTicketIDs := make([]string, 0, len(selectedRows))
	for _, row := range selectedRows {
		selectedTicketIDs = append(selectedTicketIDs, row.TicketID)
	}

	claimed, claimErr := claimTicketsForMatch(ctx, mmDB, selectedTicketIDs)
	if claimErr != nil || !claimed {
		return
	}

	api.startClaimedMatch(ctx, mmDB, selectedRows)
}

func (api *MatchmakingAPI) startClaimedMatch(ctx context.Context, mmDB *server.Database, selectedRows []queueTicketRow) {
	selectedTicketIDs := make([]string, 0, len(selectedRows))
	for _, row := range selectedRows {
		selectedTicketIDs = append(selectedTicketIDs, row.TicketID)
	}

	players, loadErr := loadPlayersForRows(ctx, selectedRows)
	if loadErr != nil {
		_ = updateTicketStatuses(ctx, mmDB, selectedTicketIDs, "error", nil)
		statuses := api.setTicketStateForIDs(selectedRows, "error", nil, loadErr.Error())
		api.pushTicketStatuses(statuses)
		return
	}

	matchCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	instance, startErr := api.manager.StartGameInstance(matchCtx, players, "")
	cancel()
	if startErr != nil {
		_ = updateTicketStatuses(ctx, mmDB, selectedTicketIDs, "error", nil)
		statuses := api.setTicketStateForIDs(selectedRows, "error", nil, startErr.Error())
		api.pushTicketStatuses(statuses)
		return
	}

	if err := persistMatchResult(ctx, mmDB, instance, selectedRows); err != nil {
		_ = updateTicketStatuses(ctx, mmDB, selectedTicketIDs, "error", nil)
		statuses := api.setTicketStateForIDs(selectedRows, "error", nil, err.Error())
		api.pushTicketStatuses(statuses)
		return
	}

	statuses := api.setTicketStateForIDs(selectedRows, "matched", &instance, "")
	api.pushTicketStatuses(statuses)
}

// pushTicketStatuses pushes a matchmaking.status update to each affected player, mirroring the
// payload shape handleStatus returns for a single ticket. Called by the matchLoop goroutine
// after setTicketStateForIDs has already returned (i.e. after its internal api.mu.Unlock() has
// run) so a slow/stuck client's buffered send channel can never stall matchmaking for everyone.
func (api *MatchmakingAPI) pushTicketStatuses(statuses []ticketStatus) {
	if api.hub == nil {
		return
	}
	for _, status := range statuses {
		matchedPort := ""
		if status.Instance != nil {
			matchedPort = status.Instance.Port
		}
		api.hub.SendTo(status.PlayerID, ws.OutboundMessage{
			Type: "matchmaking.status",
			OK:   true,
			Payload: map[string]any{
				"status":   status.Status,
				"ticketId": status.TicketID,
				"partyId":  status.PartyID,
				"playerId": status.PlayerID,
				"username": status.Username,
				"region":   api.region,
				"port":     matchedPort,
				"error":    status.Error,
			},
		})
	}
}

func (api *MatchmakingAPI) generateTicketLocked(ctx context.Context, mmDB *server.Database) (string, error) {
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

// setTicketStateForIDs updates in-memory ticket state for rows and returns a ticketStatus per
// row describing what was just set, so the caller can push matchmaking.status updates after
// this function returns (i.e. after api.mu is unlocked below).
func (api *MatchmakingAPI) setTicketStateForIDs(rows []queueTicketRow, status string, instance *server.GameInstance, ticketErr string) []ticketStatus {
	api.mu.Lock()
	defer api.mu.Unlock()

	statuses := make([]ticketStatus, 0, len(rows))
	for _, row := range rows {
		ticket, exists := api.tickets[row.TicketID]
		if !exists {
			ticket = &matchTicket{
				player: server.Player{
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

		statuses = append(statuses, ticketStatus{
			PlayerID: ticket.playerID,
			Username: ticket.player.Username,
			TicketID: row.TicketID,
			Status:   ticket.status,
			PartyID:  ticket.partyID,
			Instance: ticket.instance,
			Error:    ticket.error,
		})
	}
	return statuses
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

func selectTicketsForMatch(groups []queueTicketGroup, matchSize int) []queueTicketRow {
	selected := make([]queueTicketRow, 0, matchSize)
	factionCounts := make(map[int]int)

	for _, group := range groups {
		groupSize := len(group.Rows)
		if len(selected) == 0 && groupSize > matchSize {
			selected = append(selected, group.Rows...)
			break
		}
		if len(selected)+groupSize > matchSize {
			continue
		}

		// Check that adding this group won't exceed the per-faction player limit.
		groupFactionCounts := make(map[int]int, groupSize)
		for _, row := range group.Rows {
			groupFactionCounts[row.Faction]++
		}
		factionViolated := false
		for faction, count := range groupFactionCounts {
			if factionCounts[faction]+count > maxPlayersPerFaction {
				factionViolated = true
				break
			}
		}
		if factionViolated {
			continue
		}

		for faction, count := range groupFactionCounts {
			factionCounts[faction] += count
		}
		selected = append(selected, group.Rows...)
		if len(selected) == matchSize {
			break
		}
	}
	return selected
}

package server

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	defaultMatchSize      = 12
	defaultMatchStartWait = 30 * time.Second
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
}

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
	}

	go api.matchLoop()
	return api
}

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

func (api *MatchmakingAPI) SetMatchStartWaitForTesting(wait time.Duration) {
	if wait < 0 {
		return
	}
	api.mu.Lock()
	api.matchStartWait = wait
	api.mu.Unlock()
}

// handleQueue enqueues a player and their party for matchmaking.
// POST /matchmaking/queue
// Request: {"sessionToken": "string"}
// Response: {"status": "queued", "partyId": "uuid", "ticketId": "string", ...}
func (api *MatchmakingAPI) handleQueue(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][queue] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][queue] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var queueRequest struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.NewDecoder(request.Body).Decode(&queueRequest); err != nil {
		fmt.Printf("[DEBUG][queue] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if queueRequest.SessionToken == "" {
		fmt.Printf("[DEBUG][queue] rejected request: missing sessionToken\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	api.mu.Lock()
	resolver := api.resolveQueueContext
	api.mu.Unlock()
	queueContext, err := resolver(request.Context(), queueRequest.SessionToken)
	if err != nil {
		if errors.Is(err, errInvalidSessionToken) {
			fmt.Printf("[DEBUG][queue] session validation failed: %v\n", err)
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Printf("[DEBUG][queue] queue resolution failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	partyID, ticketIDs, playerTicketMap, err := api.enqueueGroup(request.Context(), queueContext.PartyID, queueContext.Members)
	if err != nil {
		fmt.Printf("[DEBUG][queue] enqueueGroup failed partyId=%s memberCount=%d err=%v\n", queueContext.PartyID, len(queueContext.Members), err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("[DEBUG][queue] queue successful partyId=%s ticketCount=%d requesterPlayerId=%s\n", partyID, len(ticketIDs), queueContext.RequesterPlayerID)

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

// handleJoin allows a player to join a matched game instance.
// POST /matchmaking/join
// Request: {"ticketId": "string"}
// Response: {"status": "ok", "message": "...", "instance": {...}}
func (api *MatchmakingAPI) handleJoin(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][join] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][join] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var joinRequest struct {
		TicketID string `json:"ticketId"`
	}
	if err := json.NewDecoder(request.Body).Decode(&joinRequest); err != nil {
		fmt.Printf("[DEBUG][join] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if joinRequest.TicketID == "" {
		fmt.Printf("[DEBUG][join] rejected request: missing ticketId\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	fmt.Printf("[DEBUG][join] decoded request ticketId=%s\n", joinRequest.TicketID)

	ticketPayload, found, err := loadTicketStatusByIDFromDB(request.Context(), joinRequest.TicketID)
	if err != nil || !found {
		fmt.Printf("[DEBUG][join] ticket not found in DB ticketId=%s found=%t err=%v\n", joinRequest.TicketID, found, err)
		api.mu.Lock()
		ticket, ok := api.tickets[joinRequest.TicketID]
		api.mu.Unlock()
		if !ok {
			fmt.Printf("[DEBUG][join] ticket not in memory ticketId=%s\n", joinRequest.TicketID)
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

func (api *MatchmakingAPI) handleRegisterServer(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][registerServer] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][registerServer] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var registerRequest struct {
		ServerName string `json:"serverName"`
	}
	if err := json.NewDecoder(request.Body).Decode(&registerRequest); err != nil {
		fmt.Printf("[DEBUG][registerServer] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	if registerRequest.ServerName == "" {
		fmt.Printf("[DEBUG][registerServer] rejected request: missing serverName\n")
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "serverName is required",
		})
		return
	}

	registrationKey, registrationKeyHash, err := generateServerToken()
	if err != nil {
		fmt.Printf("[DEBUG][registerServer] generateRegistrationKey failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	mmDB, err := GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][registerServer] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	if err := persistRegistrationKey(request.Context(), mmDB, registerRequest.ServerName, registrationKeyHash); err != nil {
		fmt.Printf("[DEBUG][registerServer] persistRegistrationKey failed serverName=%s err=%v\n", registerRequest.ServerName, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	serverToken, tokenHash, err := generateServerToken()
	if err != nil {
		fmt.Printf("[DEBUG][registerServer] generateServerToken failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	tokenID := "srv-token-" + uuid.NewString()
	if err := persistServerToken(request.Context(), mmDB, tokenID, registerRequest.ServerName, tokenHash); err != nil {
		fmt.Printf("[DEBUG][registerServer] persistServerToken failed serverName=%s err=%v\n", registerRequest.ServerName, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	fmt.Printf("[DEBUG][registerServer] server registered serverName=%s\n", registerRequest.ServerName)
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":          "ok",
		"tokenId":         tokenID,
		"serverName":      registerRequest.ServerName,
		"serverToken":     serverToken,
		"registrationKey": registrationKey,
		"issuedAt":        time.Now().UTC().Format(time.RFC3339),
	})
}

// handleStatus retrieves the current status of a matchmaking ticket.
// GET /matchmaking/status?sessionToken=X&ticketId=Y
// POST /matchmaking/status
// Request: {"sessionToken": "string", "ticketId": "string"}
// Response: {"status": "searching|matched|error", "tickets": [...], "members": [...]}
func (api *MatchmakingAPI) handleStatus(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][status] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][status] rejected request: invalid method=%s\n", request.Method)
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
		fmt.Printf("[DEBUG][status] decoded GET request sessionTokenSet=%t ticketId=%s\n", statusRequest.SessionToken != "", statusRequest.TicketID)
	} else {
		if err := json.NewDecoder(request.Body).Decode(&statusRequest); err != nil {
			fmt.Printf("[DEBUG][status] decode failed: %v\n", err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Printf("[DEBUG][status] decoded POST request sessionTokenSet=%t ticketId=%s\n", statusRequest.SessionToken != "", statusRequest.TicketID)
	}

	if statusRequest.SessionToken == "" {
		fmt.Printf("[DEBUG][status] rejected request: missing sessionToken\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	api.mu.Lock()
	resolver := api.resolveQueueContext
	api.mu.Unlock()
	queueContext, err := resolver(request.Context(), statusRequest.SessionToken)
	if err != nil {
		if errors.Is(err, errInvalidSessionToken) {
			fmt.Printf("[DEBUG][status] session validation failed: %v\n", err)
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Printf("[DEBUG][status] queue context resolution failed: %v\n", err)
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
		fmt.Printf("[DEBUG][status] fetching single ticket status ticketId=%s\n", statusRequest.TicketID)
		ticketPayload, ok, ticketErr := getTicketStatus(statusRequest.TicketID)
		if ticketErr != nil {
			fmt.Printf("[DEBUG][status] ticket status lookup failed ticketId=%s err=%v\n", statusRequest.TicketID, ticketErr)
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !ok {
			fmt.Printf("[DEBUG][status] ticket not found ticketId=%s\n", statusRequest.TicketID)
			response.WriteHeader(http.StatusNotFound)
			return
		}
		matchedPort := ""
		if (ticketPayload.Status == "matched" || ticketPayload.Status == "in_match") && ticketPayload.Instance != nil {
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

	fmt.Printf("[DEBUG][status] fetching all party member statuses requesterPlayerId=%s memberCount=%d\n", queueContext.RequesterPlayerID, len(queueContext.Members))
	collectedStatuses, dbErr := loadLatestTicketStatusesFromDB(request.Context(), queueContext)
	if dbErr != nil {
		fmt.Printf("[DEBUG][status] loadLatestTicketStatusesFromDB failed, using in-memory stats err=%v\n", dbErr)
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
		case "in_match":
			if overallStatus != "error" {
				overallStatus = "in_match"
			}
		case "matched":
			if overallStatus != "error" && overallStatus != "in_match" {
				overallStatus = "matched"
			}
		case "searching":
			if overallStatus != "error" && overallStatus != "matched" && overallStatus != "in_match" {
				overallStatus = "searching"
			}
		}
	}

	ownTicketID := ""
	matchedPort := ""
	for _, status := range collectedStatuses {
		if status.PlayerID == queueContext.RequesterPlayerID {
			ownTicketID = status.TicketID
			if (status.Status == "matched" || status.Status == "in_match") && status.Instance != nil {
				matchedPort = status.Instance.Port
			}
			break
		}
	}

	if matchedPort == "" && overallStatus == "matched" {
		for _, status := range collectedStatuses {
			if (status.Status == "matched" || status.Status == "in_match") && status.Instance != nil {
				matchedPort = status.Instance.Port
				break
			}
		}
	}

	fmt.Printf("[DEBUG][status] status response overallStatus=%s ticketCount=%d ownTicketId=%s port=%s\n", overallStatus, len(collectedStatuses), ownTicketID, matchedPort)
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

func (api *MatchmakingAPI) handleJoined(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][joined] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var joinedRequest struct {
		SessionToken string `json:"sessionToken"`
		TicketID     string `json:"ticketId"`
	}
	if err := json.NewDecoder(request.Body).Decode(&joinedRequest); err != nil {
		fmt.Printf("[DEBUG][joined] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if joinedRequest.SessionToken == "" || joinedRequest.TicketID == "" {
		fmt.Printf("[DEBUG][joined] rejected request: missing fields sessionTokenSet=%t ticketId=%s\n", joinedRequest.SessionToken != "", joinedRequest.TicketID)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	fmt.Printf("[DEBUG][joined] decoded request ticketId=%s\n", joinedRequest.TicketID)

	api.mu.Lock()
	resolver := api.resolveQueueContext
	api.mu.Unlock()
	queueContext, err := resolver(request.Context(), joinedRequest.SessionToken)
	if err != nil {
		if errors.Is(err, errInvalidSessionToken) {
			fmt.Printf("[DEBUG][joined] session validation failed: %v\n", err)
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Printf("[DEBUG][joined] queue context resolution failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	memberSet := make(map[string]struct{}, len(queueContext.Members))
	for _, member := range queueContext.Members {
		memberSet[member.PlayerID] = struct{}{}
	}

	ticketPayload, found, err := loadTicketStatusByIDFromDB(request.Context(), joinedRequest.TicketID)
	if err != nil {
		fmt.Printf("[DEBUG][joined] loadTicketStatusByIDFromDB failed ticketId=%s err=%v\n", joinedRequest.TicketID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !found {
		fmt.Printf("[DEBUG][joined] ticket not found ticketId=%s\n", joinedRequest.TicketID)
		response.WriteHeader(http.StatusNotFound)
		return
	}
	if _, ok := memberSet[ticketPayload.PlayerID]; !ok {
		fmt.Printf("[DEBUG][joined] forbidden: player not in requester party ticketId=%s playerId=%s\n", joinedRequest.TicketID, ticketPayload.PlayerID)
		response.WriteHeader(http.StatusForbidden)
		return
	}

	mmDB, err := GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][joined] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	updated, err := markTicketsInMatchByTicketID(request.Context(), mmDB, joinedRequest.TicketID)
	if err != nil {
		fmt.Printf("[DEBUG][joined] markTicketsInMatchByTicketID failed ticketId=%s err=%v\n", joinedRequest.TicketID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("[DEBUG][joined] successfully marked tickets in_match ticketId=%s updatedCount=%d\n", joinedRequest.TicketID, updated)

	api.mu.Lock()
	if ticket, ok := api.tickets[joinedRequest.TicketID]; ok {
		ticket.status = "in_match"
	}
	api.mu.Unlock()

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":         "in_match",
		"ticketId":       joinedRequest.TicketID,
		"partyId":        ticketPayload.PartyID,
		"updatedTickets": updated,
	})
}

func (api *MatchmakingAPI) handleLeft(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][left] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][left] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var leftRequest struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.NewDecoder(request.Body).Decode(&leftRequest); err != nil {
		fmt.Printf("[DEBUG][left] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if leftRequest.SessionToken == "" {
		fmt.Printf("[DEBUG][left] rejected request: missing sessionToken\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	api.mu.Lock()
	resolver := api.resolveQueueContext
	api.mu.Unlock()
	queueContext, err := resolver(request.Context(), leftRequest.SessionToken)
	if err != nil {
		if errors.Is(err, errInvalidSessionToken) {
			fmt.Printf("[DEBUG][left] session validation failed: %v\n", err)
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Printf("[DEBUG][left] queue context resolution failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	memberIDs := make([]string, 0, len(queueContext.Members))
	for _, member := range queueContext.Members {
		memberIDs = append(memberIDs, member.PlayerID)
	}
	fmt.Printf("[DEBUG][left] marking party members as left partyId=%s memberCount=%d\n", queueContext.PartyID, len(memberIDs))

	mmDB, err := GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][left] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	updated, err := markPlayersLeft(request.Context(), mmDB, memberIDs)
	if err != nil {
		fmt.Printf("[DEBUG][left] markPlayersLeft failed memberCount=%d err=%v\n", len(memberIDs), err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("[DEBUG][left] successfully marked players as left memberCount=%d updatedCount=%d\n", len(memberIDs), updated)

	api.mu.Lock()
	for _, ticket := range api.tickets {
		if ticket == nil {
			continue
		}
		for _, playerID := range memberIDs {
			if ticket.playerID == playerID {
				ticket.status = "left"
				ticket.instance = nil
				ticket.error = ""
				break
			}
		}
	}
	api.mu.Unlock()

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":         "not_queued",
		"partyId":        queueContext.PartyID,
		"updatedTickets": updated,
	})
}

func (api *MatchmakingAPI) handleMatchEnded(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][matchEnded] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][matchEnded] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var matchEndedRequest struct {
		ServerToken string `json:"serverToken"`
	}
	if err := json.NewDecoder(request.Body).Decode(&matchEndedRequest); err != nil {
		fmt.Printf("[DEBUG][matchEnded] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if matchEndedRequest.ServerToken == "" {
		fmt.Printf("[DEBUG][matchEnded] rejected request: missing serverToken\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	mmDB, err := GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][matchEnded] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	serverName, err := resolveServerNameFromToken(request.Context(), mmDB, matchEndedRequest.ServerToken)
	if err != nil {
		if errors.Is(err, errInvalidServerToken) {
			fmt.Printf("[DEBUG][matchEnded] server token validation failed\n")
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Printf("[DEBUG][matchEnded] server token lookup failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	fmt.Printf("[DEBUG][matchEnded] authenticated serverName=%s\n", serverName)

	updatedTickets, updatedGames, err := markMatchEndedByServerName(request.Context(), mmDB, serverName)
	if err != nil {
		fmt.Printf("[DEBUG][matchEnded] markMatchEndedByServerName failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	api.mu.Lock()
	for _, ticket := range api.tickets {
		if ticket == nil {
			continue
		}
		if ticket.instance != nil && ticket.instance.ContainerName == serverName {
			ticket.status = "left"
			ticket.instance = nil
			ticket.error = ""
		}
	}
	api.mu.Unlock()

	fmt.Printf("[DEBUG][matchEnded] request succeeded serverName=%s updatedTickets=%d updatedGames=%d\n", serverName, updatedTickets, updatedGames)
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":         "not_queued",
		"serverName":     serverName,
		"updatedTickets": updatedTickets,
		"updatedGames":   updatedGames,
	})
}

func (api *MatchmakingAPI) handleHeartbeat(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][heartbeat] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][heartbeat] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var heartbeatRequest struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.NewDecoder(request.Body).Decode(&heartbeatRequest); err != nil {
		fmt.Printf("[DEBUG][heartbeat] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if heartbeatRequest.SessionToken == "" {
		fmt.Printf("[DEBUG][heartbeat] rejected request: missing sessionToken\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	api.mu.Lock()
	resolver := api.resolveQueueContext
	api.mu.Unlock()
	queueContext, err := resolver(request.Context(), heartbeatRequest.SessionToken)
	if err != nil {
		if errors.Is(err, errInvalidSessionToken) {
			fmt.Printf("[DEBUG][heartbeat] session validation failed: %v\n", err)
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Printf("[DEBUG][heartbeat] queue context resolution failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	memberIDs := make([]string, 0, len(queueContext.Members))
	for _, member := range queueContext.Members {
		memberIDs = append(memberIDs, member.PlayerID)
	}
	fmt.Printf("[DEBUG][heartbeat] updating heartbeat for party members partyId=%s memberCount=%d\n", queueContext.PartyID, len(memberIDs))

	mmDB, err := GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][heartbeat] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	updated, err := touchPlayersHeartbeat(request.Context(), mmDB, memberIDs)
	if err != nil {
		fmt.Printf("[DEBUG][heartbeat] touchPlayersHeartbeat failed memberCount=%d err=%v\n", len(memberIDs), err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("[DEBUG][heartbeat] successfully updated heartbeat memberCount=%d updatedCount=%d\n", len(memberIDs), updated)

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":         "ok",
		"partyId":        queueContext.PartyID,
		"updatedTickets": updated,
		"heartbeatAt":    time.Now().UTC().Format(time.RFC3339),
	})
}

func (api *MatchmakingAPI) handleCancel(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][cancel] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][cancel] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ticketID := request.URL.Query().Get("ticketId")
	if ticketID == "" {
		fmt.Printf("[DEBUG][cancel] rejected request: missing ticketId\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	fmt.Printf("[DEBUG][cancel] cancelling ticket ticketId=%s\n", ticketID)
	api.mu.Lock()
	if ticket, ok := api.tickets[ticketID]; ok {
		ticket.status = "cancelled"
		fmt.Printf("[DEBUG][cancel] marked ticket as cancelled ticketId=%s\n", ticketID)
	}
	api.waiting = removeTicket(api.waiting, ticketID)
	api.mu.Unlock()
	fmt.Printf("[DEBUG][cancel] ticket removed from waiting queue ticketId=%s\n", ticketID)

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":   "cancelled",
		"ticketId": ticketID,
	})
}

// handlePartyInvite sends a party invite to another player.
// POST /matchmaking/party/invite
// Request: {"sessionToken": "string", "targetPlayerId": "string"}
// Response: {"status": "ok", "message": "..."}
func (api *MatchmakingAPI) handlePartyInvite(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][partyInvite] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][partyInvite] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var inviteRequest struct {
		SessionToken string `json:"sessionToken"`
		PlayerID     string `json:"playerId"`
	}
	if err := json.NewDecoder(request.Body).Decode(&inviteRequest); err != nil {
		fmt.Printf("[DEBUG][partyInvite] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if inviteRequest.SessionToken == "" || inviteRequest.PlayerID == "" {
		fmt.Printf("[DEBUG][partyInvite] rejected request: missing fields sessionTokenSet=%t targetPlayerId=%s\n", inviteRequest.SessionToken != "", inviteRequest.PlayerID)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	fmt.Printf("[DEBUG][partyInvite] decoded request targetPlayerId=%s\n", inviteRequest.PlayerID)

	inviterID, err := getPlayerIDFromSession(inviteRequest.SessionToken)
	if err != nil {
		fmt.Printf("[DEBUG][partyInvite] session validation failed: %v\n", err)
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	fmt.Printf("[DEBUG][partyInvite] session validated inviterId=%s targetPlayerId=%s\n", inviterID, inviteRequest.PlayerID)
	if inviterID == inviteRequest.PlayerID {
		fmt.Printf("[DEBUG][partyInvite] rejected request: self invite inviterId=%s\n", inviterID)
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "cannot invite yourself",
		})
		return
	}

	playerDB, err := GetDatabase(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][partyInvite] GetDatabase failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if exists, err := playerExists(request.Context(), playerDB, inviteRequest.PlayerID); err != nil {
		fmt.Printf("[DEBUG][partyInvite] target player lookup failed targetPlayerId=%s err=%v\n", inviteRequest.PlayerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	} else if !exists {
		fmt.Printf("[DEBUG][partyInvite] target player not found targetPlayerId=%s\n", inviteRequest.PlayerID)
		response.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "player not found",
		})
		return
	}

	mmDB, err := GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][partyInvite] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	partyID, err := ensurePlayerParty(request.Context(), mmDB, inviterID)
	if err != nil {
		fmt.Printf("[DEBUG][partyInvite] ensurePlayerParty failed inviterId=%s err=%v\n", inviterID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("[DEBUG][partyInvite] inviter party resolved inviterId=%s partyId=%s\n", inviterID, partyID)

	targetPartyID, err := findPartyForPlayer(request.Context(), mmDB, inviteRequest.PlayerID)
	if err != nil {
		fmt.Printf("[DEBUG][partyInvite] findPartyForPlayer failed targetPlayerId=%s err=%v\n", inviteRequest.PlayerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if targetPartyID != "" && targetPartyID == partyID {
		fmt.Printf("[DEBUG][partyInvite] target already in same party partyId=%s targetPlayerId=%s\n", partyID, inviteRequest.PlayerID)
		response.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "player is already in your party",
		})
		return
	}

	pendingInviteExists, err := hasPendingInvite(request.Context(), mmDB, partyID, inviteRequest.PlayerID)
	if err != nil {
		fmt.Printf("[DEBUG][partyInvite] pending invite check failed partyId=%s targetPlayerId=%s err=%v\n", partyID, inviteRequest.PlayerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if pendingInviteExists {
		fmt.Printf("[DEBUG][partyInvite] pending invite already exists partyId=%s targetPlayerId=%s\n", partyID, inviteRequest.PlayerID)
		response.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invite already pending",
		})
		return
	}

	inviteID := "invite-" + uuid.NewString()
	now := time.Now().UTC()
	expiresAt := now.Add(5 * time.Minute)
	insertInviteQuery := `INSERT INTO party_invites
		(invite_id, party_id, from_player_id, to_player_id, status, created_at, expires_at, seen_by_inviter, seen_by_invitee)
		VALUES ($1, $2, $3, $4, 'pending', $5, $6, TRUE, FALSE)`
	if _, err := submitExec(request.Context(), mmDB.DB, insertInviteQuery, inviteID, partyID, inviterID, inviteRequest.PlayerID, now, expiresAt); err != nil {
		fmt.Printf("[DEBUG][partyInvite] insert invite failed inviteId=%s partyId=%s err=%v\n", inviteID, partyID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("[DEBUG][partyInvite] invite created inviteId=%s partyId=%s inviterId=%s targetPlayerId=%s\n", inviteID, partyID, inviterID, inviteRequest.PlayerID)

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":          "ok",
		"inviteId":        inviteID,
		"partyId":         partyID,
		"fromPlayerId":    inviterID,
		"toPlayerId":      inviteRequest.PlayerID,
		"expiresAt":       expiresAt.Format(time.RFC3339),
		"inviteStatus":    "pending",
		"inviteCreatedAt": now.Format(time.RFC3339),
	})
}

func (api *MatchmakingAPI) handlePartyRespond(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][partyRespond] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][partyRespond] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var respondRequest struct {
		SessionToken string `json:"sessionToken"`
		InviteID     string `json:"inviteId"`
		Accept       bool   `json:"accept"`
	}
	if err := json.NewDecoder(request.Body).Decode(&respondRequest); err != nil {
		fmt.Printf("[DEBUG][partyRespond] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if respondRequest.SessionToken == "" || respondRequest.InviteID == "" {
		fmt.Printf("[DEBUG][partyRespond] rejected request: missing fields sessionTokenSet=%t inviteId=%s\n", respondRequest.SessionToken != "", respondRequest.InviteID)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	fmt.Printf("[DEBUG][partyRespond] decoded request inviteId=%s accept=%t\n", respondRequest.InviteID, respondRequest.Accept)

	inviteeID, err := getPlayerIDFromSession(respondRequest.SessionToken)
	if err != nil {
		fmt.Printf("[DEBUG][partyRespond] session validation failed: %v\n", err)
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	fmt.Printf("[DEBUG][partyRespond] session validated inviteeId=%s inviteId=%s\n", inviteeID, respondRequest.InviteID)

	mmDB, err := GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][partyRespond] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	invite, found, err := getPendingInviteForPlayer(request.Context(), mmDB, respondRequest.InviteID, inviteeID)
	if err != nil {
		fmt.Printf("[DEBUG][partyRespond] getPendingInviteForPlayer failed inviteId=%s inviteeId=%s err=%v\n", respondRequest.InviteID, inviteeID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !found {
		fmt.Printf("[DEBUG][partyRespond] pending invite not found inviteId=%s inviteeId=%s\n", respondRequest.InviteID, inviteeID)
		response.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "pending invite not found",
		})
		return
	}

	nextStatus := "rejected"
	if respondRequest.Accept {
		nextStatus = "accepted"
		fmt.Printf("[DEBUG][partyRespond] accept path move player inviteeId=%s targetPartyId=%s\n", inviteeID, invite.PartyID)
		if err := movePlayerToParty(request.Context(), mmDB, inviteeID, invite.PartyID); err != nil {
			fmt.Printf("[DEBUG][partyRespond] movePlayerToParty failed inviteeId=%s targetPartyId=%s err=%v\n", inviteeID, invite.PartyID, err)
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	updateInviteQuery := `UPDATE party_invites
		SET status = $1, seen_by_inviter = TRUE, seen_by_invitee = TRUE
		WHERE invite_id = $2`
	if _, err := submitExec(request.Context(), mmDB.DB, updateInviteQuery, nextStatus, respondRequest.InviteID); err != nil {
		fmt.Printf("[DEBUG][partyRespond] invite update failed inviteId=%s status=%s err=%v\n", respondRequest.InviteID, nextStatus, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	deletedSeenInviteRows, err := deleteSeenInviteByID(request.Context(), mmDB, respondRequest.InviteID)
	if err != nil {
		fmt.Printf("[DEBUG][partyRespond] invite cleanup failed inviteId=%s err=%v\n", respondRequest.InviteID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if deletedSeenInviteRows > 0 {
		fmt.Printf("[DEBUG][partyRespond] invite removed after both-seen inviteId=%s removed=%d\n", respondRequest.InviteID, deletedSeenInviteRows)
	}
	fmt.Printf("[DEBUG][partyRespond] invite handled inviteId=%s inviteeId=%s status=%s\n", respondRequest.InviteID, inviteeID, nextStatus)

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":       "ok",
		"inviteId":     respondRequest.InviteID,
		"inviteStatus": nextStatus,
		"partyId":      invite.PartyID,
		"playerId":     inviteeID,
		"accepted":     respondRequest.Accept,
	})
}

// handlePartyLeave removes a player from their current party.
// POST /matchmaking/party/leave
// Request: {"sessionToken": "string"}
// Response: {"status": "ok", "message": "..."}
func (api *MatchmakingAPI) handlePartyLeave(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][partyLeave] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][partyLeave] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var leaveRequest struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.NewDecoder(request.Body).Decode(&leaveRequest); err != nil {
		fmt.Printf("[DEBUG][partyLeave] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if leaveRequest.SessionToken == "" {
		fmt.Printf("[DEBUG][partyLeave] rejected request: missing sessionToken\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	playerID, err := getPlayerIDFromSession(leaveRequest.SessionToken)
	if err != nil {
		fmt.Printf("[DEBUG][partyLeave] session validation failed: %v\n", err)
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	fmt.Printf("[DEBUG][partyLeave] session validated playerId=%s\n", playerID)

	mmDB, err := GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][partyLeave] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	partyID, err := findPartyForPlayer(request.Context(), mmDB, playerID)
	if err != nil {
		fmt.Printf("[DEBUG][partyLeave] findPartyForPlayer failed playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if partyID == "" {
		fmt.Printf("[DEBUG][partyLeave] player not in party playerId=%s\n", playerID)
		response.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "player is not in a party",
		})
		return
	}

	if err := leaveParty(request.Context(), mmDB, playerID, partyID); err != nil {
		fmt.Printf("[DEBUG][partyLeave] leaveParty failed playerId=%s partyId=%s err=%v\n", playerID, partyID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("[DEBUG][partyLeave] player left party playerId=%s partyId=%s\n", playerID, partyID)

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":   "ok",
		"message":  "left party",
		"playerId": playerID,
		"partyId":  partyID,
	})
}

func (api *MatchmakingAPI) handlePartyInvites(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][partyInvites] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][partyInvites] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var invitesRequest struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.NewDecoder(request.Body).Decode(&invitesRequest); err != nil {
		fmt.Printf("[DEBUG][partyInvites] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if invitesRequest.SessionToken == "" {
		fmt.Printf("[DEBUG][partyInvites] rejected request: missing sessionToken\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	playerID, err := getPlayerIDFromSession(invitesRequest.SessionToken)
	if err != nil {
		fmt.Printf("[DEBUG][partyInvites] session validation failed: %v\n", err)
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	fmt.Printf("[DEBUG][partyInvites] session validated playerId=%s\n", playerID)

	mmDB, err := GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][partyInvites] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	removedFullySeenInvites, err := deleteFullySeenNonPendingInvitesForPlayer(request.Context(), mmDB, playerID)
	if err != nil {
		fmt.Printf("[DEBUG][partyInvites] cleanup fully-seen invites failed playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if removedFullySeenInvites > 0 {
		fmt.Printf("[DEBUG][partyInvites] removed fully-seen invites playerId=%s removed=%d\n", playerID, removedFullySeenInvites)
	}

	inbound, err := listInvitesForPlayer(request.Context(), mmDB, playerID, true)
	if err != nil {
		fmt.Printf("[DEBUG][partyInvites] inbound invite lookup failed playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	outbound, err := listInvitesForPlayer(request.Context(), mmDB, playerID, false)
	if err != nil {
		fmt.Printf("[DEBUG][partyInvites] outbound invite lookup failed playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("[DEBUG][partyInvites] request succeeded playerId=%s inbound=%d outbound=%d\n", playerID, len(inbound), len(outbound))

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":          "ok",
		"playerId":        playerID,
		"inboundInvites":  inbound,
		"outboundInvites": outbound,
	})
}

// handlePartyStatus returns the current party status including members and faction.
// GET /matchmaking/party/status?sessionToken=X
// POST /matchmaking/party/status
// Request: {"sessionToken": "string"}
// Response: {"partyId": "uuid", "leaderId": "uuid", "partyFaction": 0, "members": [...]}
func (api *MatchmakingAPI) handlePartyStatus(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][partyStatus] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost && request.Method != http.MethodGet {
		fmt.Printf("[DEBUG][partyStatus] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	sessionToken := ""
	if request.Method == http.MethodGet {
		sessionToken = request.URL.Query().Get("sessionToken")
	} else {
		var statusRequest struct {
			SessionToken string `json:"sessionToken"`
		}
		if err := json.NewDecoder(request.Body).Decode(&statusRequest); err != nil {
			fmt.Printf("[DEBUG][partyStatus] decode failed: %v\n", err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		sessionToken = statusRequest.SessionToken
	}

	if sessionToken == "" {
		fmt.Printf("[DEBUG][partyStatus] rejected request: missing sessionToken\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	playerID, err := getPlayerIDFromSession(sessionToken)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] session validation failed: %v\n", err)
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	fmt.Printf("[DEBUG][partyStatus] session validated playerId=%s\n", playerID)

	mmDB, err := GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	removedExpiredInvites, err := deleteExpiredPendingInvitesForPlayer(request.Context(), mmDB, playerID)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] failed deleting expired invites playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if removedExpiredInvites > 0 {
		fmt.Printf("[DEBUG][partyStatus] deleted expired invites playerId=%s removed=%d\n", playerID, removedExpiredInvites)
	}

	removedFullySeenInvites, err := deleteFullySeenNonPendingInvitesForPlayer(request.Context(), mmDB, playerID)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] failed deleting fully-seen invites playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if removedFullySeenInvites > 0 {
		fmt.Printf("[DEBUG][partyStatus] deleted fully-seen invites playerId=%s removed=%d\n", playerID, removedFullySeenInvites)
	}

	inbound, err := listInvitesForPlayer(request.Context(), mmDB, playerID, true)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] inbound invite lookup failed playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	outbound, err := listInvitesForPlayer(request.Context(), mmDB, playerID, false)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] outbound invite lookup failed playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	partyID, err := findPartyForPlayer(request.Context(), mmDB, playerID)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] findPartyForPlayer failed playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if partyID == "" {
		fmt.Printf("[DEBUG][partyStatus] player not in party playerId=%s inbound=%d outbound=%d\n", playerID, len(inbound), len(outbound))
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":          "ok",
			"playerId":        playerID,
			"inParty":         false,
			"inboundInvites":  inbound,
			"outboundInvites": outbound,
		})
		return
	}

	members, err := listPartyMembers(request.Context(), mmDB, partyID)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] listPartyMembers failed partyId=%s err=%v\n", partyID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	playerDB, err := GetDatabase(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] GetDatabase failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	memberPayload := make([]map[string]string, 0, len(members))
	for _, memberID := range members {
		username := memberID
		nameRows, queryErr := submitQuery(request.Context(), playerDB.DB, "SELECT account_name FROM players WHERE id = $1", memberID)
		if queryErr != nil {
			fmt.Printf("[DEBUG][partyStatus] member name lookup failed memberId=%s err=%v\n", memberID, queryErr)
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		if nameRows.Next() {
			_ = nameRows.Scan(&username)
		}
		if closeErr := nameRows.Close(); closeErr != nil {
			fmt.Printf("[DEBUG][partyStatus] member name rows close failed memberId=%s err=%v\n", memberID, closeErr)
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		memberPayload = append(memberPayload, map[string]string{
			"playerId": memberID,
			"username": username,
		})
	}

	primaryPlayerID, err := getPartyPrimaryPlayer(request.Context(), mmDB, partyID)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] getPartyPrimaryPlayer failed partyId=%s err=%v\n", partyID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	partyFaction, err := getPartyFaction(request.Context(), mmDB, partyID)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] getPartyFaction failed partyId=%s err=%v\n", partyID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("[DEBUG][partyStatus] request succeeded playerId=%s partyId=%s members=%d inbound=%d outbound=%d\n", playerID, partyID, len(memberPayload), len(inbound), len(outbound))

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":          "ok",
		"inParty":         true,
		"partyId":         partyID,
		"partyFaction":    partyFaction,
		"playerId":        playerID,
		"primaryPlayerId": primaryPlayerID,
		"members":         memberPayload,
		"inboundInvites":  inbound,
		"outboundInvites": outbound,
	})
}

type partyInviteRecord struct {
	InviteID      string `json:"inviteId"`
	PartyID       string `json:"partyId"`
	FromPlayerID  string `json:"fromPlayerId"`
	ToPlayerID    string `json:"toPlayerId"`
	Status        string `json:"status"`
	CreatedAt     string `json:"createdAt"`
	ExpiresAt     string `json:"expiresAt"`
	Expired       bool   `json:"expired"`
	SeenByInviter bool   `json:"seenByInviter"`
	SeenByInvitee bool   `json:"seenByInvitee"`
}

type pendingInvite struct {
	InviteID     string
	PartyID      string
	FromPlayerID string
	ToPlayerID   string
	ExpiresAt    time.Time
}

func playerExists(ctx context.Context, playerDB *Database, playerID string) (bool, error) {
	rows, err := submitQuery(ctx, playerDB.DB, "SELECT 1 FROM players WHERE id = $1", playerID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), nil
}

func ensurePlayerParty(ctx context.Context, mmDB *Database, playerID string) (string, error) {
	partyID, err := findPartyForPlayer(ctx, mmDB, playerID)
	if err != nil {
		return "", err
	}
	if partyID != "" {
		return partyID, nil
	}

	partyID = "party-" + uuid.NewString()
	activeCharacterID := any(nil)
	activeCharacter, found, err := getActiveCharacterForPlayer(ctx, playerID)
	if err != nil {
		return "", err
	}

	playerFaction, err := getPlayerFaction(ctx, playerID)
	if err != nil {
		return "", err
	}
	if found {
		playerFaction = activeCharacter.Faction
		activeCharacterID = activeCharacter.ID
	}

	createPartyQuery := "INSERT INTO parties (party_id, active_faction, faction, primary_player_id) VALUES ($1, $2, $3, $4)"
	if _, err := submitExec(ctx, mmDB.DB, createPartyQuery, partyID, playerFaction, playerFaction, playerID); err != nil {
		return "", err
	}

	insertMemberQuery := "INSERT INTO party_players (party_id, player_id, active_character_id) VALUES ($1, $2, $3)"
	if _, err := submitExec(ctx, mmDB.DB, insertMemberQuery, partyID, playerID, activeCharacterID); err != nil {
		return "", err
	}

	return partyID, nil
}

func findPartyForPlayer(ctx context.Context, mmDB *Database, playerID string) (string, error) {
	rows, err := submitQuery(ctx, mmDB.DB, "SELECT party_id FROM party_players WHERE player_id = $1 LIMIT 1", playerID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	if !rows.Next() {
		return "", nil
	}

	var partyID string
	if err := rows.Scan(&partyID); err != nil {
		return "", err
	}

	return partyID, nil
}

func hasPendingInvite(ctx context.Context, mmDB *Database, partyID string, toPlayerID string) (bool, error) {
	query := `SELECT 1
		FROM party_invites
		WHERE party_id = $1
			AND to_player_id = $2
			AND status = 'pending'
			AND expires_at > $3
		LIMIT 1`
	rows, err := submitQuery(ctx, mmDB.DB, query, partyID, toPlayerID, time.Now().UTC())
	if err != nil {
		return false, err
	}
	defer rows.Close()

	return rows.Next(), nil
}

func getPartyFaction(ctx context.Context, mmDB *Database, partyID string) (int, error) {
	rows, err := submitQuery(ctx, mmDB.DB, "SELECT faction FROM parties WHERE party_id = $1 LIMIT 1", partyID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, fmt.Errorf("party not found")
	}

	var faction int
	if err := rows.Scan(&faction); err != nil {
		return 0, err
	}

	return faction, nil
}

func getPlayerFaction(ctx context.Context, playerID string) (int, error) {
	activeCharacter, found, err := getActiveCharacterForPlayer(ctx, playerID)
	if err != nil {
		return 0, err
	}
	if found {
		return activeCharacter.Faction, nil
	}

	playerDB, err := GetDatabase(ctx)
	if err != nil {
		return 0, err
	}

	rows, err := submitQuery(ctx, playerDB.DB, "SELECT faction FROM characters WHERE player_id = $1 ORDER BY character_id LIMIT 1", playerID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, nil
	}

	var faction int
	if err := rows.Scan(&faction); err != nil {
		return 0, err
	}

	return faction, nil
}

func getPendingInviteForPlayer(ctx context.Context, mmDB *Database, inviteID string, toPlayerID string) (pendingInvite, bool, error) {
	query := `SELECT invite_id, party_id, from_player_id, to_player_id, expires_at
		FROM party_invites
		WHERE invite_id = $1
			AND to_player_id = $2
			AND status = 'pending'
		LIMIT 1`
	rows, err := submitQuery(ctx, mmDB.DB, query, inviteID, toPlayerID)
	if err != nil {
		return pendingInvite{}, false, err
	}
	defer rows.Close()

	if !rows.Next() {
		return pendingInvite{}, false, nil
	}

	invite := pendingInvite{}
	if err := rows.Scan(&invite.InviteID, &invite.PartyID, &invite.FromPlayerID, &invite.ToPlayerID, &invite.ExpiresAt); err != nil {
		return pendingInvite{}, false, err
	}

	if invite.ExpiresAt.Before(time.Now().UTC()) {
		if _, err := submitExec(ctx, mmDB.DB, "UPDATE party_invites SET status = 'expired' WHERE invite_id = $1", inviteID); err != nil {
			return pendingInvite{}, false, err
		}
		return pendingInvite{}, false, nil
	}

	return invite, true, nil
}

func movePlayerToParty(ctx context.Context, mmDB *Database, playerID string, targetPartyID string) error {
	currentPartyID, err := findPartyForPlayer(ctx, mmDB, playerID)
	if err != nil {
		return err
	}
	if currentPartyID == targetPartyID {
		return nil
	}

	if currentPartyID != "" {
		if err := leaveParty(ctx, mmDB, playerID, currentPartyID); err != nil {
			return err
		}
	}

	activeCharacterID := any(nil)
	activeCharacter, found, err := getActiveCharacterForPlayer(ctx, playerID)
	if err != nil {
		return err
	}
	if found {
		activeCharacterID = activeCharacter.ID
	}

	insertMemberQuery := "INSERT INTO party_players (party_id, player_id, active_character_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING"
	if _, err := submitExec(ctx, mmDB.DB, insertMemberQuery, targetPartyID, playerID, activeCharacterID); err != nil {
		return err
	}

	return nil
}

func syncPartyActiveCharacterSelection(ctx context.Context, playerID string, character CharacterRecord) error {
	fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] start playerId=%s characterId=%s faction=%d\n", playerID, character.ID, character.Faction)
	mmDB, err := GetMMDB(ctx)
	if err != nil {
		fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] GetMMDB failed playerId=%s characterId=%s err=%v\n", playerID, character.ID, err)
		return err
	}

	partyID, err := findPartyForPlayer(ctx, mmDB, playerID)
	if err != nil {
		fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] findPartyForPlayer failed playerId=%s characterId=%s err=%v\n", playerID, character.ID, err)
		return err
	}
	if partyID == "" {
		fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] no party found playerId=%s characterId=%s\n", playerID, character.ID)
		return nil
	}
	fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] party resolved playerId=%s partyId=%s characterId=%s\n", playerID, partyID, character.ID)

	if _, err := submitExec(ctx, mmDB.DB, "UPDATE party_players SET active_character_id = $1 WHERE party_id = $2 AND player_id = $3", character.ID, partyID, playerID); err != nil {
		fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] update party_players failed playerId=%s partyId=%s characterId=%s err=%v\n", playerID, partyID, character.ID, err)
		return err
	}
	fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] party player active character updated playerId=%s partyId=%s characterId=%s\n", playerID, partyID, character.ID)

	primaryPlayerID, err := getPartyPrimaryPlayer(ctx, mmDB, partyID)
	if err != nil {
		fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] getPartyPrimaryPlayer failed playerId=%s partyId=%s err=%v\n", playerID, partyID, err)
		return err
	}
	if primaryPlayerID != playerID {
		fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] player is not primary playerId=%s primaryPlayerId=%s partyId=%s\n", playerID, primaryPlayerID, partyID)
		return nil
	}
	fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] player is primary playerId=%s partyId=%s updating faction=%d\n", playerID, partyID, character.Faction)

	if _, err := submitExec(ctx, mmDB.DB, "UPDATE parties SET active_faction = $1, faction = $1 WHERE party_id = $2", character.Faction, partyID); err != nil {
		fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] update parties failed playerId=%s partyId=%s faction=%d err=%v\n", playerID, partyID, character.Faction, err)
		return err
	}
	fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] request succeeded playerId=%s partyId=%s faction=%d\n", playerID, partyID, character.Faction)

	return nil
}

func leaveParty(ctx context.Context, mmDB *Database, playerID string, partyID string) error {
	deleteMemberQuery := "DELETE FROM party_players WHERE party_id = $1 AND player_id = $2"
	if _, err := submitExec(ctx, mmDB.DB, deleteMemberQuery, partyID, playerID); err != nil {
		return err
	}

	remaining, err := listPartyMembers(ctx, mmDB, partyID)
	if err != nil {
		return err
	}
	if len(remaining) == 0 {
		if _, err := submitExec(ctx, mmDB.DB, "DELETE FROM parties WHERE party_id = $1", partyID); err != nil {
			return err
		}
		return nil
	}

	primaryID, err := getPartyPrimaryPlayer(ctx, mmDB, partyID)
	if err != nil {
		return err
	}

	if primaryID == playerID {
		sort.Strings(remaining)
		nextLeader := remaining[0]
		if _, err := submitExec(ctx, mmDB.DB, "UPDATE parties SET primary_player_id = $1 WHERE party_id = $2", nextLeader, partyID); err != nil {
			return err
		}
	}

	return nil
}

func listPartyMembers(ctx context.Context, mmDB *Database, partyID string) ([]string, error) {
	rows, err := submitQuery(ctx, mmDB.DB, "SELECT player_id FROM party_players WHERE party_id = $1", partyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	members := []string{}
	for rows.Next() {
		var playerID string
		if err := rows.Scan(&playerID); err != nil {
			return nil, err
		}
		members = append(members, playerID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return members, nil
}

func getPartyPrimaryPlayer(ctx context.Context, mmDB *Database, partyID string) (string, error) {
	rows, err := submitQuery(ctx, mmDB.DB, "SELECT primary_player_id FROM parties WHERE party_id = $1", partyID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	if !rows.Next() {
		return "", nil
	}

	var playerID string
	if err := rows.Scan(&playerID); err != nil {
		return "", err
	}

	return playerID, nil
}

func listInvitesForPlayer(ctx context.Context, mmDB *Database, playerID string, inbound bool) ([]partyInviteRecord, error) {
	column := "to_player_id"
	if !inbound {
		column = "from_player_id"
	}

	query := fmt.Sprintf(`SELECT invite_id, party_id, from_player_id, to_player_id, status, created_at, expires_at, seen_by_inviter, seen_by_invitee
		FROM party_invites
		WHERE %s = $1
		ORDER BY created_at DESC`, column)

	rows, err := submitQuery(ctx, mmDB.DB, query, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now().UTC()
	invites := []partyInviteRecord{}
	for rows.Next() {
		var record partyInviteRecord
		var createdAt time.Time
		var expiresAt time.Time
		if err := rows.Scan(&record.InviteID, &record.PartyID, &record.FromPlayerID, &record.ToPlayerID, &record.Status, &createdAt, &expiresAt, &record.SeenByInviter, &record.SeenByInvitee); err != nil {
			return nil, err
		}

		record.CreatedAt = createdAt.Format(time.RFC3339)
		record.ExpiresAt = expiresAt.Format(time.RFC3339)
		record.Expired = expiresAt.Before(now)
		invites = append(invites, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return invites, nil
}

func deleteExpiredPendingInvitesForPlayer(ctx context.Context, mmDB *Database, playerID string) (int64, error) {
	query := `DELETE FROM party_invites
		WHERE status = 'pending'
			AND expires_at <= $1
			AND (to_player_id = $2 OR from_player_id = $2)`
	result, err := submitExec(ctx, mmDB.DB, query, time.Now().UTC(), playerID)
	if err != nil {
		return 0, err
	}

	removed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	return removed, nil
}

func deleteFullySeenNonPendingInvitesForPlayer(ctx context.Context, mmDB *Database, playerID string) (int64, error) {
	query := `DELETE FROM party_invites
		WHERE status <> 'pending'
			AND seen_by_inviter = TRUE
			AND seen_by_invitee = TRUE
			AND (to_player_id = $1 OR from_player_id = $1)`
	result, err := submitExec(ctx, mmDB.DB, query, playerID)
	if err != nil {
		return 0, err
	}

	removed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	return removed, nil
}

func deleteSeenInviteByID(ctx context.Context, mmDB *Database, inviteID string) (int64, error) {
	query := `DELETE FROM party_invites
		WHERE invite_id = $1
			AND status <> 'pending'
			AND seen_by_inviter = TRUE
			AND seen_by_invitee = TRUE`
	result, err := submitExec(ctx, mmDB.DB, query, inviteID)
	if err != nil {
		return 0, err
	}

	removed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	return removed, nil
}

func generateServerToken() (string, string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := cryptorand.Read(tokenBytes); err != nil {
		return "", "", err
	}

	serverToken := hex.EncodeToString(tokenBytes)
	hash := sha256.Sum256([]byte(serverToken))
	return serverToken, hex.EncodeToString(hash[:]), nil
}

func persistServerToken(ctx context.Context, mmDB *Database, tokenID string, serverName string, tokenHash string) error {
	query := `INSERT INTO server_tokens (token_id, server_name, token_hash, created_at, revoked)
		VALUES ($1, $2, $3, $4, FALSE)`
	_, err := submitExec(ctx, mmDB.DB, query, tokenID, serverName, tokenHash, time.Now().UTC())
	return err
}

func persistRegistrationKey(ctx context.Context, mmDB *Database, serverName string, registrationKeyHash string) error {
	query := `INSERT INTO server_registration_keys (server_name, registration_key_hash, created_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (server_name) DO UPDATE SET registration_key_hash = EXCLUDED.registration_key_hash, created_at = EXCLUDED.created_at`
	_, err := submitExec(ctx, mmDB.DB, query, serverName, registrationKeyHash, time.Now().UTC())
	return err
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

func (api *MatchmakingAPI) startClaimedMatch(ctx context.Context, mmDB *Database, selectedRows []queueTicketRow) {
	selectedTicketIDs := make([]string, 0, len(selectedRows))
	for _, row := range selectedRows {
		selectedTicketIDs = append(selectedTicketIDs, row.TicketID)
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
var errInvalidServerToken = errors.New("invalid server token")

func resolveServerNameFromToken(ctx context.Context, mmDB *Database, serverToken string) (string, error) {
	if mmDB == nil || mmDB.DB == nil || serverToken == "" {
		return "", errInvalidServerToken
	}

	hash := sha256.Sum256([]byte(serverToken))
	tokenHash := hex.EncodeToString(hash[:])

	rows, err := submitQuery(ctx, mmDB.DB, "SELECT server_name FROM server_tokens WHERE token_hash = $1 AND revoked = FALSE LIMIT 1", tokenHash)
	if err != nil {
		return "", err
	}

	serverName := ""
	if rows.Next() {
		if scanErr := rows.Scan(&serverName); scanErr != nil {
			_ = rows.Close()
			return "", scanErr
		}
	}
	if closeErr := rows.Close(); closeErr != nil {
		return "", closeErr
	}

	if serverName == "" {
		return "", errInvalidServerToken
	}

	if _, err := submitExec(ctx, mmDB.DB, "UPDATE server_tokens SET last_used_at = $1 WHERE token_hash = $2", time.Now().UTC(), tokenHash); err != nil {
		return "", err
	}

	return serverName, nil
}

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

func markTicketsInMatchByTicketID(ctx context.Context, mmDB *Database, ticketID string) (int64, error) {
	if mmDB == nil || mmDB.DB == nil {
		return 0, nil
	}

	now := time.Now().UTC()
	query := `WITH selected_ticket AS (
		SELECT ticket_id, COALESCE(party_id, '') AS party_id, game_id
		FROM matchmaking_tickets
		WHERE ticket_id = $1
		LIMIT 1
	), target_tickets AS (
		SELECT mt.ticket_id
		FROM matchmaking_tickets mt
		JOIN selected_ticket st
			ON mt.game_id = st.game_id
			AND (mt.party_id = st.party_id OR st.party_id = '')
		WHERE mt.status IN ('matched', 'in_match')
	)
	UPDATE matchmaking_tickets mt
	SET status = 'in_match', joined_at = COALESCE(joined_at, $2), last_heartbeat_at = $2
	WHERE mt.ticket_id IN (SELECT ticket_id FROM target_tickets)`

	result, err := submitExec(ctx, mmDB.DB, query, ticketID, now)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

func markPlayersLeft(ctx context.Context, mmDB *Database, playerIDs []string) (int64, error) {
	if mmDB == nil || mmDB.DB == nil || len(playerIDs) == 0 {
		return 0, nil
	}

	now := time.Now().UTC()
	query := fmt.Sprintf(`WITH latest_active_tickets AS (
		SELECT DISTINCT ON (player_id) ticket_id
		FROM matchmaking_tickets
		WHERE player_id IN (%s)
			AND status IN ('queued', 'searching', 'matching', 'matched', 'in_match')
		ORDER BY player_id, queued_at DESC
	)
	UPDATE matchmaking_tickets mt
	SET status = 'left', game_id = NULL, left_at = $1, last_heartbeat_at = $1
	WHERE mt.ticket_id IN (SELECT ticket_id FROM latest_active_tickets)`, buildPlaceholderList(2, len(playerIDs)))

	args := make([]any, 0, 1+len(playerIDs))
	args = append(args, now)
	for _, playerID := range playerIDs {
		args = append(args, playerID)
	}

	result, err := submitExec(ctx, mmDB.DB, query, args...)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

func touchPlayersHeartbeat(ctx context.Context, mmDB *Database, playerIDs []string) (int64, error) {
	if mmDB == nil || mmDB.DB == nil || len(playerIDs) == 0 {
		return 0, nil
	}

	now := time.Now().UTC()
	query := fmt.Sprintf(`WITH latest_active_tickets AS (
		SELECT DISTINCT ON (player_id) ticket_id
		FROM matchmaking_tickets
		WHERE player_id IN (%s)
			AND status IN ('matched', 'in_match')
		ORDER BY player_id, queued_at DESC
	)
	UPDATE matchmaking_tickets mt
	SET last_heartbeat_at = $1
	WHERE mt.ticket_id IN (SELECT ticket_id FROM latest_active_tickets)`, buildPlaceholderList(2, len(playerIDs)))

	args := make([]any, 0, 1+len(playerIDs))
	args = append(args, now)
	for _, playerID := range playerIDs {
		args = append(args, playerID)
	}

	result, err := submitExec(ctx, mmDB.DB, query, args...)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

func markMatchEndedByServerName(ctx context.Context, mmDB *Database, serverName string) (int64, int64, error) {
	if mmDB == nil || mmDB.DB == nil || strings.TrimSpace(serverName) == "" {
		return 0, 0, nil
	}

	now := time.Now().UTC()

	updateTicketsQuery := `UPDATE matchmaking_tickets mt
		SET status = 'left', game_id = NULL, left_at = $1, last_heartbeat_at = $1
		WHERE mt.game_id IN (
			SELECT game_id
			FROM games
			WHERE server_name = $2 AND status <> 'ended'
		)
		AND mt.status IN ('matched', 'in_match')`
	ticketResult, err := submitExec(ctx, mmDB.DB, updateTicketsQuery, now, serverName)
	if err != nil {
		return 0, 0, err
	}
	updatedTickets, err := ticketResult.RowsAffected()
	if err != nil {
		return 0, 0, err
	}

	updateGamesQuery := `UPDATE games
		SET status = 'ended'
		WHERE server_name = $1
			AND status <> 'ended'`
	gameResult, err := submitExec(ctx, mmDB.DB, updateGamesQuery, serverName)
	if err != nil {
		return 0, 0, err
	}
	updatedGames, err := gameResult.RowsAffected()
	if err != nil {
		return 0, 0, err
	}

	return updatedTickets, updatedGames, nil
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
	case "left":
		return "not_queued"
	default:
		return status
	}
}

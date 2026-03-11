package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

const (
	defaultMatchSize = 12
)

type matchTicket struct {
	player   Player
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

type MatchmakingAPI struct {
	region    string
	manager   MatchmakingManager
	matchSize int
	mu        sync.Mutex
	waiting   []matchGroup
	tickets   map[string]*matchTicket
	startedAt time.Time
	rng       *rand.Rand
}

func NewMatchmakingAPI(region string, manager MatchmakingManager) *MatchmakingAPI {
	api := &MatchmakingAPI{
		region:    region,
		manager:   manager,
		matchSize: defaultMatchSize,
		waiting:   []matchGroup{},
		tickets:   make(map[string]*matchTicket),
		startedAt: time.Now().UTC(),
		rng:       rand.New(rand.NewSource(time.Now().UnixNano())),
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

func (api *MatchmakingAPI) handleQueue(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var queueRequest struct {
		PartyID  string   `json:"partyId"`
		Players  []Player `json:"players"`
		ID       int64    `json:"id"`
		Username string   `json:"username"`
	}
	if err := json.NewDecoder(request.Body).Decode(&queueRequest); err != nil {
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	players := queueRequest.Players
	if len(players) == 0 {
		if queueRequest.ID == 0 || queueRequest.Username == "" {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		players = []Player{{ID: queueRequest.ID, Username: queueRequest.Username}}
	}

	partyID, tickets := api.enqueueGroup(queueRequest.PartyID, players)
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":    "queued",
		"ticketIds": tickets,
		"partyId":   partyID,
		"region":    api.region,
	})
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

	api.mu.Lock()
	ticket, ok := api.tickets[joinRequest.TicketID]
	api.mu.Unlock()
	if !ok {
		response.WriteHeader(http.StatusNotFound)
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":   ticket.status,
		"ticketId": joinRequest.TicketID,
		"partyId":  ticket.partyID,
		"instance": ticket.instance,
	})
}

func (api *MatchmakingAPI) handleStatus(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ticketID := request.URL.Query().Get("ticketId")
	api.mu.Lock()
	ticket, ok := api.tickets[ticketID]
	api.mu.Unlock()
	if !ok {
		response.WriteHeader(http.StatusNotFound)
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	payload := map[string]any{
		"status":   ticket.status,
		"ticketId": ticketID,
		"region":   api.region,
	}
	if ticket.instance != nil {
		payload["instance"] = ticket.instance
	}
	if ticket.error != "" {
		payload["error"] = ticket.error
	}
	_ = json.NewEncoder(response).Encode(payload)
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

func (api *MatchmakingAPI) enqueueGroup(partyID string, players []Player) (string, []string) {
	api.mu.Lock()
	defer api.mu.Unlock()

	if partyID == "" {
		partyID = "party-" + api.randomSuffix()
	}

	ticketIDs := make([]string, 0, len(players))
	for _, player := range players {
		ticket := api.generateTicketLocked()
		player.Ticket = ticket
		api.tickets[ticket] = &matchTicket{
			player:   player,
			queuedAt: time.Now().UTC(),
			status:   "searching",
			partyID:  partyID,
		}
		ticketIDs = append(ticketIDs, ticket)
	}

	api.waiting = append(api.waiting, matchGroup{
		id:        partyID,
		ticketIDs: ticketIDs,
		queuedAt:  time.Now().UTC(),
	})

	return partyID, ticketIDs
}

func (api *MatchmakingAPI) matchLoop() {
	for range time.NewTicker(1 * time.Second).C {
		api.tryCreateMatch()
	}
}

func (api *MatchmakingAPI) tryCreateMatch() {
	api.mu.Lock()
	if len(api.waiting) == 0 {
		api.mu.Unlock()
		return
	}

	totalPlayers := 0
	for _, group := range api.waiting {
		totalPlayers += len(group.ticketIDs)
	}
	if totalPlayers < api.matchSize {
		api.mu.Unlock()
		return
	}

	selectedTickets := make([]string, 0, api.matchSize)
	selectedGroups := 0
	for _, group := range api.waiting {
		groupSize := len(group.ticketIDs)
		if len(selectedTickets) == 0 && groupSize > api.matchSize {
			selectedGroups++
			selectedTickets = append(selectedTickets, group.ticketIDs...)
			break
		}
		if len(selectedTickets)+groupSize > api.matchSize {
			break
		}
		selectedGroups++
		selectedTickets = append(selectedTickets, group.ticketIDs...)
		if len(selectedTickets) == api.matchSize {
			break
		}
	}
	if len(selectedTickets) == 0 {
		api.mu.Unlock()
		return
	}
	api.waiting = api.waiting[selectedGroups:]

	players := make([]Player, 0, len(selectedTickets))
	for _, ticketID := range selectedTickets {
		if ticket, ok := api.tickets[ticketID]; ok {
			players = append(players, ticket.player)
		}
	}
	api.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	instance, err := api.manager.StartGameInstance(ctx, players, "")
	cancel()

	api.mu.Lock()
	defer api.mu.Unlock()
	for _, ticketID := range selectedTickets {
		ticket, ok := api.tickets[ticketID]
		if !ok {
			continue
		}
		if err != nil {
			ticket.status = "error"
			ticket.error = err.Error()
			continue
		}
		copyInstance := instance
		ticket.status = "matched"
		ticket.instance = &copyInstance
	}
}

func (api *MatchmakingAPI) generateTicketLocked() string {
	for {
		ticket := "ticket-" + api.randomSuffix()
		if _, exists := api.tickets[ticket]; !exists {
			return ticket
		}
	}
}

func (api *MatchmakingAPI) randomSuffix() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), api.rng.Intn(100000))
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

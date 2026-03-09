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
	defaultMaxWait   = 30 * time.Second
)

type matchTicket struct {
	player   Player
	queuedAt time.Time
	status   string
	instance *GameInstance
	error    string
}

type MatchmakingAPI struct {
	region    string
	manager   *GameServerManager
	matchSize int
	maxWait   time.Duration
	mu        sync.Mutex
	waiting   []string
	tickets   map[string]*matchTicket
	startedAt time.Time
	rng       *rand.Rand
}

func NewMatchmakingAPI(region string, manager *GameServerManager) *MatchmakingAPI {
	api := &MatchmakingAPI{
		region:    region,
		manager:   manager,
		matchSize: defaultMatchSize,
		maxWait:   defaultMaxWait,
		waiting:   []string{},
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

func (api *MatchmakingAPI) handleQueue(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var playerData struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(request.Body).Decode(&playerData); err != nil {
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	player := Player{
		ID:       playerData.ID,
		Username: playerData.Username,
	}

	ticket := api.enqueuePlayer(player)
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":   "queued",
		"ticketId": ticket,
		"region":   api.region,
	})
}

func (api *MatchmakingAPI) handleJoin(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var joinRequest struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(request.Body).Decode(&joinRequest); err != nil {
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	instances := api.manager.ListInstances()
	if len(instances) == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		instance, err := api.manager.StartGameInstance(ctx, nil, "")
		if err != nil {
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "failed to start server",
				"error":   err.Error(),
			})
			return
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":   "ready",
			"instance": instance,
		})
		return
	}

	instance := instances[0]
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":   "ready",
		"instance": instance,
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

func (api *MatchmakingAPI) enqueuePlayer(player Player) string {
	api.mu.Lock()
	defer api.mu.Unlock()

	ticket := api.generateTicketLocked()
	player.Ticket = ticket
	api.tickets[ticket] = &matchTicket{
		player:   player,
		queuedAt: time.Now().UTC(),
		status:   "searching",
	}
	api.waiting = append(api.waiting, ticket)
	return ticket
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

	shouldStart := len(api.waiting) >= api.matchSize
	if !shouldStart {
		oldestTicket := api.waiting[0]
		oldest := api.tickets[oldestTicket]
		if oldest != nil && time.Since(oldest.queuedAt) >= api.maxWait {
			shouldStart = true
		}
	}

	if !shouldStart {
		api.mu.Unlock()
		return
	}

	count := api.matchSize
	if len(api.waiting) < count {
		count = len(api.waiting)
	}

	selectedTickets := append([]string(nil), api.waiting[:count]...)
	api.waiting = api.waiting[count:]
	players := make([]Player, 0, count)
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

func removeTicket(queue []string, ticketID string) []string {
	for index, ticket := range queue {
		if ticket == ticketID {
			return append(queue[:index], queue[index+1:]...)
		}
	}
	return queue
}

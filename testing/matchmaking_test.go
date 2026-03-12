package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	server "fracturedexodusserver/src"
)

type fakeMatchmakingManager struct {
	mu        sync.Mutex
	instances []server.GameInstance
	calls     int
}

func (manager *fakeMatchmakingManager) StartGameInstance(ctx context.Context, players []server.Player, requestedPort string) (server.GameInstance, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.calls++
	instance := server.GameInstance{
		ID:       "instance-1",
		Host:     "127.0.0.1",
		Port:     "7777",
		Protocol: "udp",
		JoinKey:  "join-key",
	}
	manager.instances = append(manager.instances, instance)
	return instance, nil
}

func (manager *fakeMatchmakingManager) ListInstances() []server.GameInstance {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	instances := make([]server.GameInstance, len(manager.instances))
	copy(instances, manager.instances)
	return instances
}

func TestMatchmakingQueueAndStatus(t *testing.T) {
	manager := &fakeMatchmakingManager{}
	api := server.NewMatchmakingAPI("NA", manager)
	api.SetQueueContextResolverForTesting(func(ctx context.Context, sessionToken string) (server.QueueContext, error) {
		return server.QueueContext{
			RequesterPlayerID: "player-1",
			Members: []server.QueueMember{
				{PlayerID: "player-1", Username: "pilot"},
			},
		}, nil
	})
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	queueRequest := httptest.NewRequest(http.MethodPost, "/matchmaking/queue", strings.NewReader(`{"sessionToken":"session-1"}`))
	queueResponse := httptest.NewRecorder()
	mux.ServeHTTP(queueResponse, queueRequest)

	if queueResponse.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, queueResponse.Code)
	}

	var queuePayload map[string]any
	if err := json.NewDecoder(queueResponse.Body).Decode(&queuePayload); err != nil {
		t.Fatalf("decode queue response: %v", err)
	}
	ids, ok := queuePayload["ticketIds"].([]any)
	if !ok || len(ids) == 0 {
		t.Fatalf("expected ticketIds array")
	}
	ticketID, ok := ids[0].(string)
	if !ok || ticketID == "" {
		t.Fatalf("expected ticketId string")
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/matchmaking/status?sessionToken=session-1", nil)
	statusResponse := httptest.NewRecorder()
	mux.ServeHTTP(statusResponse, statusRequest)

	if statusResponse.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, statusResponse.Code)
	}
}

func TestMatchmakingJoinStartsInstance(t *testing.T) {
	manager := &fakeMatchmakingManager{}
	api := server.NewMatchmakingAPI("NA", manager)
	api.SetQueueContextResolverForTesting(func(ctx context.Context, sessionToken string) (server.QueueContext, error) {
		return server.QueueContext{
			RequesterPlayerID: "player-1",
			Members: []server.QueueMember{
				{PlayerID: "player-1", Username: "pilot"},
			},
		}, nil
	})
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	queueRequest := httptest.NewRequest(http.MethodPost, "/matchmaking/queue", strings.NewReader(`{"sessionToken":"session-1"}`))
	queueResponse := httptest.NewRecorder()
	mux.ServeHTTP(queueResponse, queueRequest)

	var queuePayload map[string]any
	if err := json.NewDecoder(queueResponse.Body).Decode(&queuePayload); err != nil {
		t.Fatalf("decode queue response: %v", err)
	}
	ids, ok := queuePayload["ticketIds"].([]any)
	if !ok || len(ids) == 0 {
		t.Fatalf("expected ticketIds array")
	}
	ticketID, ok := ids[0].(string)
	if !ok || ticketID == "" {
		t.Fatalf("expected ticketId string")
	}

	joinRequest := httptest.NewRequest(http.MethodPost, "/matchmaking/join", strings.NewReader(`{"ticketId":"`+ticketID+`"}`))
	joinResponse := httptest.NewRecorder()
	mux.ServeHTTP(joinResponse, joinRequest)

	if joinResponse.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, joinResponse.Code)
	}

	var payload map[string]any
	if err := json.NewDecoder(joinResponse.Body).Decode(&payload); err != nil {
		t.Fatalf("decode join response: %v", err)
	}

	if payload["status"] != "searching" {
		t.Fatalf("expected status searching, got %v", payload["status"])
	}
}

func TestMatchmakingStatusNoTicketNotQueued(t *testing.T) {
	manager := &fakeMatchmakingManager{}
	api := server.NewMatchmakingAPI("NA", manager)
	uniqueSuffix := fmt.Sprintf("%d", time.Now().UnixNano())
	playerID := "player-not-queued-" + uniqueSuffix
	partyID := "party-not-queued-" + uniqueSuffix
	api.SetQueueContextResolverForTesting(func(ctx context.Context, sessionToken string) (server.QueueContext, error) {
		return server.QueueContext{
			RequesterPlayerID: playerID,
			PartyID:           partyID,
			Members: []server.QueueMember{
				{PlayerID: playerID, Username: "pilot-not-queued"},
			},
		}, nil
	})

	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	statusRequest := httptest.NewRequest(http.MethodGet, "/matchmaking/status?sessionToken=session-1", nil)
	statusResponse := httptest.NewRecorder()
	mux.ServeHTTP(statusResponse, statusRequest)

	if statusResponse.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, statusResponse.Code)
	}

	var statusPayload map[string]any
	if err := json.NewDecoder(statusResponse.Body).Decode(&statusPayload); err != nil {
		t.Fatalf("decode status response: %v", err)
	}

	if statusPayload["status"] != "not_queued" {
		t.Fatalf("expected status not_queued, got %v", statusPayload["status"])
	}
}

func TestMatchmakingStatusNoTicketReturnsOwnTicketWhenQueued(t *testing.T) {
	manager := &fakeMatchmakingManager{}
	api := server.NewMatchmakingAPI("NA", manager)
	api.SetQueueContextResolverForTesting(func(ctx context.Context, sessionToken string) (server.QueueContext, error) {
		return server.QueueContext{
			RequesterPlayerID: "player-1",
			Members: []server.QueueMember{
				{PlayerID: "player-1", Username: "pilot"},
			},
		}, nil
	})

	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	queueRequest := httptest.NewRequest(http.MethodPost, "/matchmaking/queue", strings.NewReader(`{"sessionToken":"session-1"}`))
	queueResponse := httptest.NewRecorder()
	mux.ServeHTTP(queueResponse, queueRequest)

	if queueResponse.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, queueResponse.Code)
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/matchmaking/status?sessionToken=session-1", nil)
	statusResponse := httptest.NewRecorder()
	mux.ServeHTTP(statusResponse, statusRequest)

	if statusResponse.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, statusResponse.Code)
	}

	var statusPayload map[string]any
	if err := json.NewDecoder(statusResponse.Body).Decode(&statusPayload); err != nil {
		t.Fatalf("decode status response: %v", err)
	}

	ticketID, ok := statusPayload["ticketId"].(string)
	if !ok || ticketID == "" {
		t.Fatalf("expected non-empty ticketId, got %v", statusPayload["ticketId"])
	}
}

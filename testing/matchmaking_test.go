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
	lastSizes []int
}

func (manager *fakeMatchmakingManager) StartGameInstance(ctx context.Context, players []server.Player, requestedPort string) (server.GameInstance, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.calls++
	manager.lastSizes = append(manager.lastSizes, len(players))
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

func (manager *fakeMatchmakingManager) CallCount() int {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.calls
}

func (manager *fakeMatchmakingManager) LastPlayerCount() int {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if len(manager.lastSizes) == 0 {
		return 0
	}
	return manager.lastSizes[len(manager.lastSizes)-1]
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

func TestMatchmakingQueuesSecondsApartJoinSameMatch(t *testing.T) {
	mmDB, err := server.GetMMDB(context.Background())
	if err != nil {
		t.Fatalf("get mm db: %v", err)
	}
	if err := server.ResetMMDB(context.Background(), mmDB); err != nil {
		t.Fatalf("reset mm db: %v", err)
	}
	if err := server.InitMMDB(context.Background(), mmDB); err != nil {
		t.Fatalf("init mm db: %v", err)
	}

	manager := &fakeMatchmakingManager{}
	api := server.NewMatchmakingAPI("NA", manager)
	api.SetMatchSize(2)
	api.SetMatchStartWaitForTesting(30 * time.Second)

	uniqueSuffix := fmt.Sprintf("%d", time.Now().UnixNano())
	playerOne := "player-one-" + uniqueSuffix
	playerTwo := "player-two-" + uniqueSuffix

	api.SetQueueContextResolverForTesting(func(ctx context.Context, sessionToken string) (server.QueueContext, error) {
		switch sessionToken {
		case "session-1":
			return server.QueueContext{
				RequesterPlayerID: playerOne,
				Members: []server.QueueMember{
					{PlayerID: playerOne, Username: "pilot-one"},
				},
			}, nil
		case "session-2":
			return server.QueueContext{
				RequesterPlayerID: playerTwo,
				Members: []server.QueueMember{
					{PlayerID: playerTwo, Username: "pilot-two"},
				},
			}, nil
		default:
			return server.QueueContext{}, fmt.Errorf("invalid session token")
		}
	})

	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	queueOne := httptest.NewRequest(http.MethodPost, "/matchmaking/queue", strings.NewReader(`{"sessionToken":"session-1"}`))
	queueOneResponse := httptest.NewRecorder()
	mux.ServeHTTP(queueOneResponse, queueOne)
	if queueOneResponse.Code != http.StatusAccepted {
		t.Fatalf("expected first queue status %d, got %d", http.StatusAccepted, queueOneResponse.Code)
	}

	time.Sleep(1200 * time.Millisecond)
	if manager.CallCount() != 0 {
		t.Fatalf("expected no match start before threshold, got %d starts", manager.CallCount())
	}

	queueTwo := httptest.NewRequest(http.MethodPost, "/matchmaking/queue", strings.NewReader(`{"sessionToken":"session-2"}`))
	queueTwoResponse := httptest.NewRecorder()
	mux.ServeHTTP(queueTwoResponse, queueTwo)
	if queueTwoResponse.Code != http.StatusAccepted {
		t.Fatalf("expected second queue status %d, got %d", http.StatusAccepted, queueTwoResponse.Code)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if manager.CallCount() > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if manager.CallCount() != 1 {
		t.Fatalf("expected exactly one match start, got %d", manager.CallCount())
	}

	if manager.LastPlayerCount() != 2 {
		t.Fatalf("expected combined match size 2, got %d", manager.LastPlayerCount())
	}
}

func TestMatchmakingMatchEndedMethodNotAllowed(t *testing.T) {
	manager := &fakeMatchmakingManager{}
	api := server.NewMatchmakingAPI("NA", manager)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodGet, "/matchmaking/match/ended", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
}

func TestMatchmakingMatchEndedBadRequest(t *testing.T) {
	manager := &fakeMatchmakingManager{}
	api := server.NewMatchmakingAPI("NA", manager)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodPost, "/matchmaking/match/ended", strings.NewReader(`{"serverToken":""}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}
}

func TestMatchmakingRegisterServerMethodNotAllowed(t *testing.T) {
	manager := &fakeMatchmakingManager{}
	api := server.NewMatchmakingAPI("NA", manager)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodGet, "/matchmaking/server/register", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
}

func TestMatchmakingRegisterServerBadRequest(t *testing.T) {
	manager := &fakeMatchmakingManager{}
	api := server.NewMatchmakingAPI("NA", manager)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodPost, "/matchmaking/server/register", strings.NewReader(`{"serverName":""}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}
}

func TestPartyInviteAllowsDifferentFaction(t *testing.T) {
	ctx := context.Background()

	playerDB, err := server.GetDatabase(ctx)
	if err != nil {
		t.Fatalf("get player db: %v", err)
	}
	if err := server.InitDB(ctx, playerDB.DB); err != nil {
		t.Fatalf("init player db: %v", err)
	}

	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		t.Fatalf("get mm db: %v", err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		err = server.InitMMDB(ctx, mmDB)
		if err == nil {
			break
		}
		if strings.Contains(err.Error(), "deadlock detected") {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		t.Fatalf("init mm db: %v", err)
	}
	if err != nil {
		t.Fatalf("init mm db after retries: %v", err)
	}

	uniqueSuffix := fmt.Sprintf("%d", time.Now().UnixNano())
	inviterID := "party-inviter-" + uniqueSuffix
	inviteeID := "party-invitee-" + uniqueSuffix
	inviterToken := "session-inviter-" + uniqueSuffix

	if _, err := playerDB.DB.ExecContext(ctx,
		"INSERT INTO players (id, password, account_name) VALUES ($1, $2, $3)",
		inviterID,
		"pw",
		"inviter",
	); err != nil {
		t.Fatalf("insert inviter player: %v", err)
	}
	if _, err := playerDB.DB.ExecContext(ctx,
		"INSERT INTO players (id, password, account_name) VALUES ($1, $2, $3)",
		inviteeID,
		"pw",
		"invitee",
	); err != nil {
		t.Fatalf("insert invitee player: %v", err)
	}

	if _, err := playerDB.DB.ExecContext(ctx,
		"INSERT INTO session_tokens (player_id, session_token, expiration) VALUES ($1, $2, $3)",
		inviterID,
		inviterToken,
		time.Now().UTC().Add(30*time.Minute),
	); err != nil {
		t.Fatalf("insert inviter session: %v", err)
	}

	if _, err := playerDB.DB.ExecContext(ctx,
		"INSERT INTO characters (character_id, player_id, name, skin_key, weapon_1, weapon_2, weapon_3, equipment_1, equipment_2, class_type, faction) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)",
		"char-inviter-"+uniqueSuffix,
		inviterID,
		"inviter-char",
		"skin",
		"w1",
		"w2",
		"w3",
		"e1",
		"e2",
		0,
		1,
	); err != nil {
		t.Fatalf("insert inviter character: %v", err)
	}
	if _, err := playerDB.DB.ExecContext(ctx,
		"INSERT INTO characters (character_id, player_id, name, skin_key, weapon_1, weapon_2, weapon_3, equipment_1, equipment_2, class_type, faction) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)",
		"char-invitee-"+uniqueSuffix,
		inviteeID,
		"invitee-char",
		"skin",
		"w1",
		"w2",
		"w3",
		"e1",
		"e2",
		0,
		2,
	); err != nil {
		t.Fatalf("insert invitee character: %v", err)
	}

	manager := &fakeMatchmakingManager{}
	api := server.NewMatchmakingAPI("NA", manager)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	inviteBody := fmt.Sprintf(`{"sessionToken":"%s","playerId":"%s"}`, inviterToken, inviteeID)
	inviteRequest := httptest.NewRequest(http.MethodPost, "/matchmaking/party/invite", strings.NewReader(inviteBody))
	inviteResponse := httptest.NewRecorder()
	mux.ServeHTTP(inviteResponse, inviteRequest)

	if inviteResponse.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, inviteResponse.Code)
	}

	var payload map[string]any
	if err := json.NewDecoder(inviteResponse.Body).Decode(&payload); err != nil {
		t.Fatalf("decode invite response: %v", err)
	}

	if payload["status"] != "ok" {
		t.Fatalf("expected ok status, got %v", payload["status"])
	}
}

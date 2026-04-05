package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	server "fracturedexodusserver/src"
	playerhandling "fracturedexodusserver/src/playerhandling"
)

func TestPlayerLoginMethodNotAllowed(t *testing.T) {
	api := playerhandling.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodGet, "/player/login", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
}

func TestPlayerLoginPost(t *testing.T) {
	api := playerhandling.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodPost, "/player/login", strings.NewReader("{}"))
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized && response.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d or %d, got %d", http.StatusUnauthorized, http.StatusInternalServerError, response.Code)
	}

	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload["status"] == "ok" {
		t.Fatalf("expected non-ok status, got %v", payload["status"])
	}
}

func TestPlayerAccountInfo(t *testing.T) {
	api := playerhandling.NewPlayerAPI("dev-build")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodPost, "/player/account/info", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}

	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload["status"] != "error" {
		t.Fatalf("expected error status, got %v", payload["status"])
	}

	if payload["message"] != "invalid request body" {
		t.Fatalf("expected invalid request body message, got %v", payload["message"])
	}
}

func TestPlayerAccountInfoMethodNotAllowed(t *testing.T) {
	api := playerhandling.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodGet, "/player/account/info", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
}

func TestPlayerEquipmentAndCharacters(t *testing.T) {
	api := playerhandling.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodPost, "/player/characters", strings.NewReader(`{"sessionToken":""}`))
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}

	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload["status"] != "error" {
		t.Fatalf("expected error status, got %v", payload["status"])
	}
}

func TestPlayerEquipmentAndCharactersMethodNotAllowed(t *testing.T) {
	api := playerhandling.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodGet, "/player/characters", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
}

func TestSetActiveCharacterMethodNotAllowed(t *testing.T) {
	api := playerhandling.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodGet, "/player/character/set", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
}

func TestSetActiveCharacterPersistsSelection(t *testing.T) {
	ctx := context.Background()
	db, err := server.GetDatabase(ctx)
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	if err := server.ResetDB(ctx, db.DB); err != nil {
		t.Fatalf("reset db: %v", err)
	}
	if err := server.InitDB(ctx, db.DB); err != nil {
		t.Fatalf("init db: %v", err)
	}

	playerID := "player-active-character"
	characterID := "character-active-character"
	sessionToken := "session-active-character"

	if _, err := db.DB.ExecContext(ctx,
		"INSERT INTO players (id, password, account_name) VALUES ($1, $2, $3)",
		playerID,
		"pw",
		"pilot",
	); err != nil {
		t.Fatalf("insert player: %v", err)
	}
	if _, err := db.DB.ExecContext(ctx,
		"INSERT INTO session_tokens (player_id, session_token, expiration) VALUES ($1, $2, $3)",
		playerID,
		sessionToken,
		time.Now().UTC().Add(30*time.Minute),
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := db.DB.ExecContext(ctx,
		"INSERT INTO characters (character_id, player_id, name, skin_key, weapon_1, weapon_2, weapon_3, equipment_1, equipment_2, class_type, faction) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)",
		characterID,
		playerID,
		"pilot-char",
		"skin",
		"w1",
		"w2",
		"w3",
		"e1",
		"e2",
		0,
		1,
	); err != nil {
		t.Fatalf("insert character: %v", err)
	}

	api := playerhandling.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodPost, "/player/character/set", strings.NewReader(`{"sessionToken":"`+sessionToken+`","characterId":"`+characterID+`"}`))
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}

	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload["status"] != "ok" {
		t.Fatalf("expected ok status, got %v", payload["status"])
	}

	rows, err := db.DB.QueryContext(ctx, "SELECT character_id FROM active_characters WHERE player_id = $1", playerID)
	if err != nil {
		t.Fatalf("query active character: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatalf("expected active character row for player")
	}

	var storedCharacterID string
	if err := rows.Scan(&storedCharacterID); err != nil {
		t.Fatalf("scan active character row: %v", err)
	}

	if storedCharacterID != characterID {
		t.Fatalf("expected active character %s, got %s", characterID, storedCharacterID)
	}
}

func TestSetActiveCharacterUpdatesPrimaryPartyFaction(t *testing.T) {
	ctx := context.Background()
	playerDB, err := server.GetDatabase(ctx)
	if err != nil {
		t.Fatalf("get player db: %v", err)
	}
	if err := server.ResetDB(ctx, playerDB.DB); err != nil {
		t.Fatalf("reset player db: %v", err)
	}
	if err := server.InitDB(ctx, playerDB.DB); err != nil {
		t.Fatalf("init player db: %v", err)
	}

	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		t.Fatalf("get matchmaking db: %v", err)
	}
	if err := server.ResetMMDB(ctx, mmDB); err != nil {
		t.Fatalf("reset matchmaking db: %v", err)
	}
	if err := server.InitMMDB(ctx, mmDB); err != nil {
		t.Fatalf("init matchmaking db: %v", err)
	}

	playerID := "party-leader"
	characterID := "party-leader-character"
	sessionToken := "party-leader-session"
	partyID := "party-1"

	if _, err := playerDB.DB.ExecContext(ctx,
		"INSERT INTO players (id, password, account_name) VALUES ($1, $2, $3)",
		playerID,
		"pw",
		"leader",
	); err != nil {
		t.Fatalf("insert player: %v", err)
	}
	if _, err := playerDB.DB.ExecContext(ctx,
		"INSERT INTO session_tokens (player_id, session_token, expiration) VALUES ($1, $2, $3)",
		playerID,
		sessionToken,
		time.Now().UTC().Add(30*time.Minute),
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := playerDB.DB.ExecContext(ctx,
		"INSERT INTO characters (character_id, player_id, name, skin_key, weapon_1, weapon_2, weapon_3, equipment_1, equipment_2, class_type, faction) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)",
		characterID,
		playerID,
		"leader-char",
		"skin",
		"w1",
		"w2",
		"w3",
		"e1",
		"e2",
		0,
		2,
	); err != nil {
		t.Fatalf("insert character: %v", err)
	}

	if _, err := mmDB.DB.ExecContext(ctx,
		"INSERT INTO parties (party_id, active_faction, faction, primary_player_id) VALUES ($1, $2, $3, $4)",
		partyID,
		0,
		0,
		playerID,
	); err != nil {
		t.Fatalf("insert party: %v", err)
	}
	if _, err := mmDB.DB.ExecContext(ctx,
		"INSERT INTO party_players (party_id, player_id, active_character_id) VALUES ($1, $2, $3)",
		partyID,
		playerID,
		nil,
	); err != nil {
		t.Fatalf("insert party player: %v", err)
	}

	api := playerhandling.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodPost, "/player/character/set", strings.NewReader(`{"sessionToken":"`+sessionToken+`","characterId":"`+characterID+`"}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}

	partyRows, err := mmDB.DB.QueryContext(ctx, "SELECT faction FROM parties WHERE party_id = $1", partyID)
	if err != nil {
		t.Fatalf("query party faction: %v", err)
	}
	defer partyRows.Close()
	if !partyRows.Next() {
		t.Fatalf("expected party row")
	}

	var faction int
	if err := partyRows.Scan(&faction); err != nil {
		t.Fatalf("scan party faction: %v", err)
	}
	if faction != 2 {
		t.Fatalf("expected party faction 2, got %d", faction)
	}

	memberRows, err := mmDB.DB.QueryContext(ctx, "SELECT active_character_id FROM party_players WHERE party_id = $1 AND player_id = $2", partyID, playerID)
	if err != nil {
		t.Fatalf("query party player active character: %v", err)
	}
	defer memberRows.Close()
	if !memberRows.Next() {
		t.Fatalf("expected party player row")
	}

	var activeCharacterID string
	if err := memberRows.Scan(&activeCharacterID); err != nil {
		t.Fatalf("scan party player active character: %v", err)
	}
	if activeCharacterID != characterID {
		t.Fatalf("expected party player active character %s, got %s", characterID, activeCharacterID)
	}
}

func TestFriendRequestMethodNotAllowed(t *testing.T) {
	api := playerhandling.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodGet, "/player/friend/request", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
}

func TestFriendRequestInvalidBody(t *testing.T) {
	api := playerhandling.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodPost, "/player/friend/request", strings.NewReader("not-json"))
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}

	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload["status"] != "error" {
		t.Fatalf("expected error status, got %v", payload["status"])
	}
}

func TestAcceptRejectFriendRequestMethodNotAllowed(t *testing.T) {
	api := playerhandling.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodGet, "/player/friend/accept", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
}

func TestAcceptRejectFriendRequestMissingFields(t *testing.T) {
	api := playerhandling.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodPost, "/player/friend/accept", strings.NewReader(`{"accept":true}`))
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}

	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload["status"] != "error" {
		t.Fatalf("expected error status, got %v", payload["status"])
	}
}

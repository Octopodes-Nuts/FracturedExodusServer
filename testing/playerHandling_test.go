package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	server "fracturedexodusserver/src"
)

func TestPlayerLoginMethodNotAllowed(t *testing.T) {
	api := server.NewPlayerAPI("test")
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
	api := server.NewPlayerAPI("test")
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
	api := server.NewPlayerAPI("dev-build")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodPost, "/player/accountInfo", nil)
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
	api := server.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodGet, "/player/accountInfo", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
}

func TestPlayerEquipmentAndCharacters(t *testing.T) {
	api := server.NewPlayerAPI("test")
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
	api := server.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodGet, "/player/characters", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
}

func TestFriendRequestMethodNotAllowed(t *testing.T) {
	api := server.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodGet, "/player/friendRequest", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
}

func TestFriendRequestInvalidBody(t *testing.T) {
	api := server.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodPost, "/player/friendRequest", strings.NewReader("not-json"))
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
	api := server.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodGet, "/player/acceptRejectFriendRequest", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
}

func TestAcceptRejectFriendRequestMissingFields(t *testing.T) {
	api := server.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodPost, "/player/acceptRejectFriendRequest", strings.NewReader(`{"accept":true}`))
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

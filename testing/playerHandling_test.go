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

	request := httptest.NewRequest(http.MethodGet, "/player/accountInfo", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}

	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload["version"] != "dev-build" {
		t.Fatalf("expected version dev-build, got %v", payload["version"])
	}
}

func TestPlayerAccountInfoMethodNotAllowed(t *testing.T) {
	api := server.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodPost, "/player/accountInfo", nil)
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

	request := httptest.NewRequest(http.MethodGet, "/player/equipmentAndCharacters", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}

	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if _, ok := payload["equipment"]; !ok {
		t.Fatalf("expected equipment field")
	}
	if _, ok := payload["characters"]; !ok {
		t.Fatalf("expected characters field")
	}
}

func TestPlayerEquipmentAndCharactersMethodNotAllowed(t *testing.T) {
	api := server.NewPlayerAPI("test")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodPost, "/player/equipmentAndCharacters", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
}

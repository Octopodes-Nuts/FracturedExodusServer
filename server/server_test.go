package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"fracturedexodusserver/server"
)

func TestServerHealth(t *testing.T) {
	api := server.NewServerAPI("test-service")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}

	var payload map[string]string
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", payload["status"])
	}
}

func TestServerInfo(t *testing.T) {
	api := server.NewServerAPI("test-service")
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	request := httptest.NewRequest(http.MethodGet, "/info", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}

	var payload map[string]string
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload["service"] != "test-service" {
		t.Fatalf("expected service test-service, got %v", payload["service"])
	}
	if payload["startedAt"] == "" {
		t.Fatalf("expected startedAt to be set")
	}
}

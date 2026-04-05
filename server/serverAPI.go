package server

import (
	"encoding/json"
	"net/http"
	"time"
)

// API is the interface for all API services
type API interface {
	// RegisterRoutes registers all HTTP routes for this API
	RegisterRoutes(mux *http.ServeMux)
}

// ServerAPI provides health and info endpoints for the server.
type ServerAPI struct {
	serviceName string
	startedAt   time.Time
}

func NewServerAPI(serviceName string) *ServerAPI {
	return &ServerAPI{serviceName: serviceName, startedAt: time.Now().UTC()}
}

func (api *ServerAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", api.handleHealth)
	mux.HandleFunc("/info", api.handleInfo)
}

// handleHealth returns server health status.
// GET /health
// Returns: {"status": "ok"}
func (api *ServerAPI) handleHealth(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]string{
		"status": "ok",
	})
}

// handleInfo returns server information including service name and startup timestamp.
// GET /info
// Returns: {"service": "string", "startedAt": "timestamp"}
func (api *ServerAPI) handleInfo(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]string{
		"service":   api.serviceName,
		"startedAt": api.startedAt.Format(time.RFC3339),
	})
}

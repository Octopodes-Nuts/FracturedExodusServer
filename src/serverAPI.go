package server

import (
	"encoding/json"
	"net/http"
	"time"
)

type API interface {
	RegisterRoutes(mux *http.ServeMux)
}

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

func (api *ServerAPI) handleHealth(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]string{
		"status": "ok",
	})
}

func (api *ServerAPI) handleInfo(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]string{
		"service":   api.serviceName,
		"startedAt": api.startedAt.Format(time.RFC3339),
	})
}

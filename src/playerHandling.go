package server

import (
	"encoding/json"
	"net/http"
	"time"
)

type PlayerAPI struct {
	buildVersion string
}

func NewPlayerAPI(buildVersion string) *PlayerAPI {
	return &PlayerAPI{buildVersion: buildVersion}
}

func (api *PlayerAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/player/login", api.handleLogin)
	mux.HandleFunc("/player/accountInfo", api.handleAccountInfo)
	mux.HandleFunc("/player/equipmentAndCharacters", api.handleEquipmentAndCharacters)
}

func (api *PlayerAPI) handleLogin(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":   "ok",
		"message":  "login accepted",
		"issuedAt": time.Now().UTC().Format(time.RFC3339),
	})
}

func (api *PlayerAPI) handleAccountInfo(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"accountId":   "demo-account",
		"displayName": "Pilot",
		"region":      "NA",
		"version":     api.buildVersion,
	})
}

func (api *PlayerAPI) handleEquipmentAndCharacters(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"equipment":  []string{},
		"characters": []string{},
	})
}

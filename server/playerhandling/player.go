package playerhandling

import (
	"net/http"

	ws "fracturedexodusserver/server/ws"
)

// PlayerAPI provides endpoints for account management, character progression, and friend relationships.
type PlayerAPI struct {
	buildVersion string
	hub          *ws.Hub
}

// NewPlayerAPI creates a new PlayerAPI instance.
func NewPlayerAPI(buildVersion string) *PlayerAPI {
	return &PlayerAPI{buildVersion: buildVersion}
}

// SetHub wires up the WebSocket push hub. Until this is called, api.hub is nil and every push
// call site (e.g. pushAccountInfoUpdated) is a no-op — existing callers/tests that construct a
// PlayerAPI without a hub keep working unchanged.
func (api *PlayerAPI) SetHub(hub *ws.Hub) {
	api.hub = hub
}

func (api *PlayerAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/player/login", api.handleLogin)
	mux.HandleFunc("/player/account/info", api.handleAccountInfo)
	mux.HandleFunc("/player/characters", api.handleCharacters)
	mux.HandleFunc("/player/character/set", api.handleSetActiveCharacter)
	mux.HandleFunc("/player/friend/request", api.handleFriendRequest)
	mux.HandleFunc("/player/friend/respond", api.handleAcceptRejectFriendRequest)
	mux.HandleFunc("/player/account/create", handleCreateAccount)
	mux.HandleFunc("/player/logout", handleLogout)
	mux.HandleFunc("/player/character/new", api.handleNewCharacter)
	mux.HandleFunc("/player/character/delete", api.handleDeleteCharacter)
	mux.HandleFunc("/player/character/update", api.handleUpdateCharacter)
	mux.HandleFunc("/player/character/get", api.handleGetCharacter)
}

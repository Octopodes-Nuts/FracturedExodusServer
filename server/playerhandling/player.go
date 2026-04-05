package playerhandling

import "net/http"

// PlayerAPI provides endpoints for account management, character progression, and friend relationships.
type PlayerAPI struct {
	buildVersion string
}

// NewPlayerAPI creates a new PlayerAPI instance.
func NewPlayerAPI(buildVersion string) *PlayerAPI {
	return &PlayerAPI{buildVersion: buildVersion}
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

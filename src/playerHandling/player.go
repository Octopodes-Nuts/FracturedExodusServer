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
	mux.HandleFunc("/player/accountInfo", api.handleAccountInfo)
	mux.HandleFunc("/player/characters", api.handleCharacters)
	mux.HandleFunc("/player/setActiveCharacter", api.handleSetActiveCharacter)
	mux.HandleFunc("/player/friendRequest", api.handleFriendRequest)
	mux.HandleFunc("/player/acceptRejectFriendRequest", api.handleAcceptRejectFriendRequest)
	mux.HandleFunc("/player/createAccount", handleCreateAccount)
	mux.HandleFunc("/player/logout", handleLogout)
	mux.HandleFunc("/player/newCharacter", api.handleNewCharacter)
	mux.HandleFunc("/player/deleteCharacter", api.handleDeleteCharacter)
	mux.HandleFunc("/player/updateCharacter", api.handleUpdateCharacter)
	mux.HandleFunc("/player/getCharacter", api.handleGetCharacter)
}

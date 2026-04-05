package playerhandling

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	server "fracturedexodusserver/server"
	"fracturedexodusserver/server/matchmaking"

	"github.com/google/uuid"
)

// handleCharacters returns all characters for an authenticated player.
// POST /player/characters
// Request: {"sessionToken": "string"}
// Response: {"characters": [{...}, ...]}
func (api *PlayerAPI) handleCharacters(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	type CharactersRequest struct {
		SessionToken string `json:"sessionToken"`
	}

	var charactersRequest CharactersRequest
	if err := json.NewDecoder(request.Body).Decode(&charactersRequest); err != nil {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid request body",
			"error":   err.Error(),
		})
		return
	}

	if charactersRequest.SessionToken == "" {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "sessionToken is required",
		})
		return
	}

	playerID, err := server.GetPlayerIDFromSession(charactersRequest.SessionToken)
	if err != nil {
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid session token",
			"error":   err.Error(),
		})
		return
	}

	db, err := server.GetDatabase(context.Background())
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	query := "SELECT character_id, name, skin_key, weapon_1, weapon_2, weapon_3, equipment_1, equipment_2, xp, devotion, class_type, faction FROM characters WHERE player_id = $1"
	rows, err := server.SubmitQuery(context.Background(), db.DB, query, playerID)
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database query failed",
			"error":   err.Error(),
		})
		return
	}
	defer rows.Close()

	type Character struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		SkinKey    string `json:"skinKey"`
		Weapon1    string `json:"weapon1"`
		Weapon2    string `json:"weapon2"`
		Weapon3    string `json:"weapon3"`
		Equipment1 string `json:"equipment1"`
		Equipment2 string `json:"equipment2"`
		XP         int    `json:"xp"`
		Devotion   int    `json:"devotion"`
		ClassType  int    `json:"classType"`
		Faction    int    `json:"faction"`
	}

	characters := []Character{}
	for rows.Next() {
		var character Character
		if err := rows.Scan(&character.ID, &character.Name, &character.SkinKey, &character.Weapon1, &character.Weapon2, &character.Weapon3, &character.Equipment1, &character.Equipment2, &character.XP, &character.Devotion, &character.ClassType, &character.Faction); err != nil {
			fmt.Printf("[DEBUG][characters] scan failed playerId=%s err=%v\n", playerID, err)
			response.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "database scan failed",
				"error":   err.Error(),
			})
			return
		}
		characters = append(characters, character)
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"characters": characters,
	})
}

// handleSetActiveCharacter sets the active character for a player. Active character determines party faction.
// POST /player/setActiveCharacter
// Request: {"sessionToken": "string", "characterId": "string"}
// Response: {"status": "ok", "message": "...", "characterId": "uuid", "faction": 0}
func (api *PlayerAPI) handleSetActiveCharacter(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][setActiveCharacter] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][setActiveCharacter] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var setActiveCharacterRequest struct {
		SessionToken string `json:"sessionToken"`
		CharacterID  string `json:"characterId"`
	}

	if err := json.NewDecoder(request.Body).Decode(&setActiveCharacterRequest); err != nil {
		fmt.Printf("[DEBUG][setActiveCharacter] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid request body",
			"error":   err.Error(),
		})
		return
	}

	if setActiveCharacterRequest.SessionToken == "" {
		fmt.Printf("[DEBUG][setActiveCharacter] rejected request: missing sessionToken\n")
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "sessionToken is required",
		})
		return
	}

	playerID, err := server.GetPlayerIDFromSession(setActiveCharacterRequest.SessionToken)
	if err != nil {
		fmt.Printf("[DEBUG][setActiveCharacter] session validation failed characterId=%s err=%v\n", setActiveCharacterRequest.CharacterID, err)
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid session token",
			"error":   err.Error(),
		})
		return
	}
	fmt.Printf("[DEBUG][setActiveCharacter] session validated playerId=%s characterId=%s\n", playerID, setActiveCharacterRequest.CharacterID)

	// Empty characterId clears the active character.
	if setActiveCharacterRequest.CharacterID == "" {
		db, err := server.GetDatabase(context.Background())
		if err != nil {
			fmt.Printf("[DEBUG][setActiveCharacter] GetDatabase failed playerId=%s err=%v\n", playerID, err)
			response.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "database error",
				"error":   err.Error(),
			})
			return
		}
		if _, err := server.SubmitExec(context.Background(), db.DB, "DELETE FROM active_characters WHERE player_id = $1", playerID); err != nil {
			fmt.Printf("[DEBUG][setActiveCharacter] delete active_characters failed playerId=%s err=%v\n", playerID, err)
			response.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "database error",
				"error":   err.Error(),
			})
			return
		}
		if err := server.ClearPartyActiveCharacterSelection(request.Context(), playerID); err != nil {
			fmt.Printf("[DEBUG][setActiveCharacter] clearPartyActiveCharacterSelection failed playerId=%s err=%v\n", playerID, err)
			response.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "failed to sync party active character",
				"error":   err.Error(),
			})
			return
		}
		fmt.Printf("[DEBUG][setActiveCharacter] active character cleared playerId=%s\n", playerID)
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":      "ok",
			"playerId":    playerID,
			"characterId": "",
			"message":     "active character cleared",
		})
		return
	}

	character, found, err := server.GetCharacterByID(request.Context(), setActiveCharacterRequest.CharacterID)
	if err != nil {
		fmt.Printf("[DEBUG][setActiveCharacter] getCharacterByID failed playerId=%s characterId=%s err=%v\n", playerID, setActiveCharacterRequest.CharacterID, err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database query failed",
			"error":   err.Error(),
		})
		return
	}
	if !found || character.PlayerID != playerID {
		fmt.Printf("[DEBUG][setActiveCharacter] character not found or not owned playerId=%s characterId=%s found=%t ownerId=%s\n", playerID, setActiveCharacterRequest.CharacterID, found, character.PlayerID)
		response.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "character not found",
		})
		return
	}

	db, err := server.GetDatabase(context.Background())
	if err != nil {
		fmt.Printf("[DEBUG][setActiveCharacter] GetDatabase failed playerId=%s characterId=%s err=%v\n", playerID, character.ID, err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	query := `INSERT INTO active_characters (player_id, character_id, updated_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (player_id) DO UPDATE SET character_id = EXCLUDED.character_id, updated_at = EXCLUDED.updated_at`
	if _, err := server.SubmitExec(context.Background(), db.DB, query, playerID, setActiveCharacterRequest.CharacterID, time.Now().UTC()); err != nil {
		fmt.Printf("[DEBUG][setActiveCharacter] upsert active_characters failed playerId=%s characterId=%s err=%v\n", playerID, setActiveCharacterRequest.CharacterID, err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}
	fmt.Printf("[DEBUG][setActiveCharacter] active character persisted playerId=%s characterId=%s faction=%d\n", playerID, character.ID, character.Faction)

	if err := server.SyncPartyActiveCharacterSelection(request.Context(), playerID, character); err != nil {
		fmt.Printf("[DEBUG][setActiveCharacter] syncPartyActiveCharacterSelection failed playerId=%s characterId=%s err=%v\n", playerID, character.ID, err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "failed to sync party active character",
			"error":   err.Error(),
		})
		return
	}
	fmt.Printf("[DEBUG][setActiveCharacter] request succeeded playerId=%s characterId=%s faction=%d\n", playerID, character.ID, character.Faction)

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":      "ok",
		"playerId":    playerID,
		"characterId": character.ID,
		"faction":     character.Faction,
		"message":     "active character updated",
	})
}

func (api *PlayerAPI) handleGetCharacter(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	type GetCharacterRequest struct {
		SessionToken string `json:"sessionToken"`
		CharacterID  string `json:"characterId"`
	}

	var getRequest GetCharacterRequest
	if err := json.NewDecoder(request.Body).Decode(&getRequest); err != nil {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid request body",
			"error":   err.Error(),
		})
		return
	}

	if getRequest.SessionToken == "" || getRequest.CharacterID == "" {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "sessionToken and characterId are required",
		})
		return
	}

	if err := server.ValidateSessionToken(getRequest.SessionToken); err != nil {
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid session token",
			"error":   err.Error(),
		})
		return
	}

	db, err := server.GetDatabase(context.Background())
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	query := "SELECT character_id, name, skin_key, weapon_1, weapon_2, weapon_3, equipment_1, equipment_2, xp, devotion, class_type, faction FROM characters WHERE character_id = $1"
	rows, err := server.SubmitQuery(context.Background(), db.DB, query, getRequest.CharacterID)
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database query failed",
			"error":   err.Error(),
		})
		return
	}
	defer rows.Close()

	if !rows.Next() {
		response.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "character not found",
		})
		return
	}

	var character struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		SkinKey    string `json:"skinKey"`
		Weapon1    string `json:"weapon1"`
		Weapon2    string `json:"weapon2"`
		Weapon3    string `json:"weapon3"`
		Equipment1 string `json:"equipment1"`
		Equipment2 string `json:"equipment2"`
		XP         int    `json:"xp"`
		Devotion   int    `json:"devotion"`
		ClassType  int    `json:"classType"`
		Faction    int    `json:"faction"`
	}

	if err := rows.Scan(&character.ID, &character.Name, &character.SkinKey, &character.Weapon1, &character.Weapon2, &character.Weapon3, &character.Equipment1, &character.Equipment2, &character.XP, &character.Devotion, &character.ClassType, &character.Faction); err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database scan failed",
			"error":   err.Error(),
		})
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"character": character,
	})
}

// handleNewCharacter creates a new character for an authenticated player.
// POST /player/newCharacter
// Request: {"sessionToken": "string", "name": "string", "skinKey": "string", "weapon1": "string", ...}
// Response: {"status": "ok", "message": "...", "characterId": "uuid"}
func (api *PlayerAPI) handleNewCharacter(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][newCharacter] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][newCharacter] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	const initialDevotion = 10

	type NewCharacterRequest struct {
		SessionToken string `json:"sessionToken"`
		Name         string `json:"name"`
		SkinKey      string `json:"skinKey"`
		Weapon1      string `json:"weapon1"`
		Weapon2      string `json:"weapon2"`
		Weapon3      string `json:"weapon3"`
		Equipment1   string `json:"equipment1"`
		Equipment2   string `json:"equipment2"`
		ClassType    int    `json:"classType"`
		Faction      int    `json:"faction"`
	}

	var newCharacterRequest NewCharacterRequest
	if err := json.NewDecoder(request.Body).Decode(&newCharacterRequest); err != nil {
		fmt.Printf("[DEBUG][newCharacter] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid request body",
			"error":   err.Error(),
		})
		return
	}

	fmt.Printf("[DEBUG][newCharacter] decoded request name=%s classType=%d faction=%d sessionTokenSet=%t\n", newCharacterRequest.Name, newCharacterRequest.ClassType, newCharacterRequest.Faction, newCharacterRequest.SessionToken != "")

	if newCharacterRequest.SessionToken == "" {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "sessionToken is required",
		})
		return
	}

	if newCharacterRequest.Name == "" {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "name is required",
		})
		return
	}

	playerID, err := server.GetPlayerIDFromSession(newCharacterRequest.SessionToken)
	if err != nil {
		fmt.Printf("[DEBUG][newCharacter] session validation failed sessionTokenSet=%t err=%v\n", newCharacterRequest.SessionToken != "", err)
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid session token",
			"error":   err.Error(),
		})
		return
	}
	fmt.Printf("[DEBUG][newCharacter] session validated playerId=%s\n", playerID)

	db, err := server.GetDatabase(context.Background())
	if err != nil {
		fmt.Printf("[DEBUG][newCharacter] GetDatabase failed playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}
	const maxCharactersPerFaction = 5
	countQuery := "SELECT COUNT(*) FROM characters WHERE player_id = $1 AND faction = $2"
	countRows, err := server.SubmitQuery(context.Background(), db.DB, countQuery, playerID, newCharacterRequest.Faction)
	if err != nil {
		fmt.Printf("[DEBUG][newCharacter] faction count query failed playerId=%s faction=%d err=%v\n", playerID, newCharacterRequest.Faction, err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database query failed",
			"error":   err.Error(),
		})
		return
	}
	defer countRows.Close()

	var factionCount int
	if countRows.Next() {
		if err := countRows.Scan(&factionCount); err != nil {
			fmt.Printf("[DEBUG][newCharacter] faction count scan failed playerId=%s err=%v\n", playerID, err)
			response.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "database scan failed",
				"error":   err.Error(),
			})
			return
		}
	}
	if factionCount >= maxCharactersPerFaction {
		fmt.Printf("[DEBUG][newCharacter] faction character limit reached playerId=%s faction=%d count=%d\n", playerID, newCharacterRequest.Faction, factionCount)
		response.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "character limit reached for this faction",
		})
		return
	}

	id, err := createCharacterID()
	if err != nil {
		fmt.Printf("[DEBUG][newCharacter] createCharacterID failed playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "failed to create character ID",
			"error":   err.Error(),
		})
		return
	}

	query := "INSERT INTO characters (player_id, character_id, name, skin_key, weapon_1, weapon_2, weapon_3, equipment_1, equipment_2, xp, devotion, class_type, faction) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)"
	if _, err := server.SubmitExec(context.Background(), db.DB, query, playerID, id, newCharacterRequest.Name, newCharacterRequest.SkinKey, newCharacterRequest.Weapon1, newCharacterRequest.Weapon2, newCharacterRequest.Weapon3, newCharacterRequest.Equipment1, newCharacterRequest.Equipment2, 0, initialDevotion, newCharacterRequest.ClassType, newCharacterRequest.Faction); err != nil {
		fmt.Printf("[DEBUG][newCharacter] character insert failed playerId=%s characterId=%s name=%s faction=%d err=%v\n", playerID, id, newCharacterRequest.Name, newCharacterRequest.Faction, err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}
	fmt.Printf("[DEBUG][newCharacter] request succeeded playerId=%s characterId=%s name=%s faction=%d\n", playerID, id, newCharacterRequest.Name, newCharacterRequest.Faction)

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":      "ok",
		"characterId": id,
		"message":     fmt.Sprintf("character %s created", newCharacterRequest.Name),
	})
}

func (api *PlayerAPI) handleUpdateCharacter(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	type UpdateCharacterRequest struct {
		SessionToken string `json:"sessionToken"`
		CharacterID  string `json:"characterId"`
		Name         string `json:"name"`
		SkinKey      string `json:"skinKey"`
		Weapon1      string `json:"weapon1"`
		Weapon2      string `json:"weapon2"`
		Weapon3      string `json:"weapon3"`
		Equipment1   string `json:"equipment1"`
		Equipment2   string `json:"equipment2"`
		ClassType    int    `json:"classType"`
		Faction      int    `json:"faction"`
	}

	var updateRequest UpdateCharacterRequest
	if err := json.NewDecoder(request.Body).Decode(&updateRequest); err != nil {
		fmt.Printf("Error decoding request body: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid request body",
			"error":   err.Error(),
		})
		return
	}

	if updateRequest.SessionToken == "" || updateRequest.CharacterID == "" {
		fmt.Printf("Missing sessionToken or characterId\n")
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "sessionToken and characterId are required",
		})
		return
	}

	if updateRequest.Name == "" {
		fmt.Printf("Missing character fields\n")
		fmt.Printf("UpdateCharacterRequest: %+v\n", updateRequest)
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "all character fields are required",
		})
		return
	}

	playerID, err := server.GetPlayerIDFromSession(updateRequest.SessionToken)
	if err != nil {
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid session token",
			"error":   err.Error(),
		})
		return
	}

	db, err := server.GetDatabase(context.Background())
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	query := "UPDATE characters SET name = $1, skin_key = $2, weapon_1 = $3, weapon_2 = $4, weapon_3 = $5, equipment_1 = $6, equipment_2 = $7, class_type = $8, faction = $9 WHERE character_id = $10 AND player_id = $11"
	result, err := server.SubmitExec(context.Background(), db.DB, query, updateRequest.Name, updateRequest.SkinKey, updateRequest.Weapon1, updateRequest.Weapon2, updateRequest.Weapon3, updateRequest.Equipment1, updateRequest.Equipment2, updateRequest.ClassType, updateRequest.Faction, updateRequest.CharacterID, playerID)
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		response.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "character not found",
		})
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":      "ok",
		"characterId": updateRequest.CharacterID,
		"message":     "character updated",
	})
}

func (api *PlayerAPI) handleDeleteCharacter(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	type DeleteCharacterRequest struct {
		SessionToken string `json:"sessionToken"`
		ServerToken  string `json:"serverToken"`
		CharacterID  string `json:"characterId"`
	}

	var deleteRequest DeleteCharacterRequest
	if err := json.NewDecoder(request.Body).Decode(&deleteRequest); err != nil {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid request body",
			"error":   err.Error(),
		})
		return
	}

	if deleteRequest.CharacterID == "" {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "characterId is required",
		})
		return
	}

	// Authenticate using either session token or server token
	useServerToken := false
	var playerID string
	var err error

	if deleteRequest.SessionToken != "" {
		playerID, err = server.GetPlayerIDFromSession(deleteRequest.SessionToken)
		if err != nil {
			response.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "invalid session token",
				"error":   err.Error(),
			})
			return
		}
	} else if deleteRequest.ServerToken != "" {
		mmDB, err := server.GetMMDB(request.Context())
		if err != nil {
			response.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "database error",
				"error":   err.Error(),
			})
			return
		}

		_, tokenErr := matchmaking.ValidateServerToken(request.Context(), mmDB, deleteRequest.ServerToken)
		if tokenErr != nil {
			response.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "invalid server token",
				"error":   tokenErr.Error(),
			})
			return
		}

		useServerToken = true
	} else {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "either sessionToken or serverToken is required",
		})
		return
	}

	db, err := server.GetDatabase(context.Background())
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	var result interface{ RowsAffected() (int64, error) }
	if useServerToken {
		result, err = server.SubmitExec(context.Background(), db.DB, "DELETE FROM characters WHERE character_id = $1", deleteRequest.CharacterID)
	} else {
		result, err = server.SubmitExec(context.Background(), db.DB, "DELETE FROM characters WHERE character_id = $1 AND player_id = $2", deleteRequest.CharacterID, playerID)
	}
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		response.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "character not found",
		})
		return
	}

	if _, err := server.SubmitExec(context.Background(), db.DB, "DELETE FROM active_characters WHERE character_id = $1", deleteRequest.CharacterID); err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	if !useServerToken {
		if err := server.ClearPartyActiveCharacterSelection(request.Context(), playerID); err != nil {
			response.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "failed to sync party active character",
				"error":   err.Error(),
			})
			return
		}
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":      "ok",
		"characterId": deleteRequest.CharacterID,
		"message":     "character deleted",
	})
}

func createCharacterID() (string, error) {
	id := uuid.New().String()

	db, err := server.GetDatabase(context.Background())
	if err != nil {
		return "", err
	}
	query := "SELECT COUNT(*) FROM characters WHERE character_id = $1"

	rows, err := server.SubmitQuery(context.Background(), db.DB, query, id)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var count int
	if rows.Next() {
		if err := rows.Scan(&count); err != nil {
			return "", err
		}
	} else {
		return "", fmt.Errorf("Query returned no rows")
	}

	if count > 0 {
		return "", fmt.Errorf("UUID already exists")
	}

	return id, nil
}

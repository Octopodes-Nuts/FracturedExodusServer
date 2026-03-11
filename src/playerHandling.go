package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
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
	mux.HandleFunc("/player/characters", api.handleCharacters)
	mux.HandleFunc("/player/createAccount", handleCreateAccount)
	mux.HandleFunc("/player/logout", handleLogout)
	mux.HandleFunc("/player/newCharacter", api.handleNewCharacter)
	mux.HandleFunc("/player/deleteCharacter", api.handleDeleteCharacter)
	mux.HandleFunc("/player/updateCharacter", api.handleUpdateCharacter)
	mux.HandleFunc("/player/getCharacter", api.handleGetCharacter)
}

func (api *PlayerAPI) handleLogin(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	type LoginRequest struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	sessionToken, err := createSessionToken()
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "failed to create session token",
			"error":   err.Error(),
		})
		return
	}

	var loginRequest LoginRequest
	if err := json.NewDecoder(request.Body).Decode(&loginRequest); err != nil {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid request body",
			"error":   err.Error(),
		})
		return
	}

	db, _ := GetDatabase(context.Background())
	// check credentials against database

	query := "SELECT id FROM players WHERE account_name = $1 AND password = $2"
	rows, err := submitQuery(context.Background(), db.DB, query, loginRequest.Username, loginRequest.Password)
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

	var id string
	if rows.Next() {
		if err := rows.Scan(&id); err != nil {
			fmt.Printf("Error scanning database row: %v\n", err)
			return
		}
	} else {
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid credentials",
		})
		return
	}

	// insert session token into database with expiration time, associated with player account
	query = "INSERT INTO session_tokens (player_id, session_token, expiration) VALUES ($1, $2, $3)"
	if _, err := submitExec(context.Background(), db.DB, query, id, sessionToken, time.Now().Add(24*time.Hour).UTC()); err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "failed to create session",
			"error":   err.Error(),
		})
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)

	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":       "ok",
		"message":      "login accepted",
		"issuedAt":     time.Now().UTC().Format(time.RFC3339),
		"accountId":    id,
		"sessionToken": sessionToken,
	})
}

func createSessionToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func (api *PlayerAPI) handleAccountInfo(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":       "ok",
		"message":      "account info retrieved",
		"buildVersion": api.buildVersion,
	})
}

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

	db, err := GetDatabase(context.Background())
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	query := "SELECT player_id FROM session_tokens WHERE session_token = $1 AND expiration > $2"
	rows, err := submitQuery(context.Background(), db.DB, query, charactersRequest.SessionToken, time.Now().UTC())
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

	var playerID string
	if rows.Next() {
		if err := rows.Scan(&playerID); err != nil {
			fmt.Printf("Error scanning database row: %v\n", err)
			return
		}
	} else {
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid session token",
		})
		return
	}

	// query database for characters associated with playerID, return in response
	query = "SELECT character_id, name, skin_key, weapon_1, weapon_2, weapon_3, equipment_1, equipment_2, class_type, faction FROM characters WHERE player_id = $1"
	rows, err = submitQuery(context.Background(), db.DB, query, playerID)
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
		ClassType  int    `json:"classType"`
		Faction    int    `json:"faction"`
	}

	characters := []Character{}
	for rows.Next() {
		var character Character
		if err := rows.Scan(&character.ID, &character.Name, &character.SkinKey, &character.Weapon1, &character.Weapon2, &character.Weapon3, &character.Equipment1, &character.Equipment2, &character.ClassType, &character.Faction); err != nil {
			fmt.Printf("Error scanning database row: %v\n", err)
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

	if err := validateSessionToken(getRequest.SessionToken); err != nil {
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid session token",
			"error":   err.Error(),
		})
		return
	}

	query := "SELECT character_id, name, skin_key, weapon_1, weapon_2, weapon_3, equipment_1, equipment_2, class_type, faction FROM characters WHERE character_id = $1"
	rows, err := submitQuery(context.Background(), databaseInstance.DB, query, getRequest.CharacterID)
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
		ClassType  int    `json:"classType"`
		Faction    int    `json:"faction"`
	}

	if err := rows.Scan(&character.ID, &character.Name, &character.SkinKey, &character.Weapon1, &character.Weapon2, &character.Weapon3, &character.Equipment1, &character.Equipment2, &character.ClassType, &character.Faction); err != nil {
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

func (api *PlayerAPI) handleNewCharacter(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	type NewCharacterRequest struct {
		SessionToken   string `json:"sessionToken"`
		Name           string `json:"name"`
		SkinKey        string `json:"skinKey"`
		Weapon1        string `json:"weapon1"`
		Weapon2        string `json:"weapon2"`
		Weapon3        string `json:"weapon3"`
		Equipment1     string `json:"equipment1"`
		Equipment2     string `json:"equipment2"`
		DevotionPoints int    `json:"devotionPoints"`
		ClassType      int    `json:"classType"`
		Faction        int    `json:"faction"`
	}

	var newCharacterRequest NewCharacterRequest
	if err := json.NewDecoder(request.Body).Decode(&newCharacterRequest); err != nil {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid request body",
			"error":   err.Error(),
		})
		return
	}

	player_id := ""

	db, err := GetDatabase(context.Background())
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	query := "SELECT player_id FROM session_tokens WHERE session_token = $1 AND expiration > $2"
	rows, err := submitQuery(context.Background(), db.DB, query, newCharacterRequest.SessionToken, time.Now().UTC())
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

	if rows.Next() {
		if err := rows.Scan(&player_id); err != nil {
			fmt.Printf("Error scanning database row: %v\n", err)
			return
		}
	} else {
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid session token",
		})
		return
	}

	id, err := createCharacterID()
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "failed to create character ID",
			"error":   err.Error(),
		})
		return
	}

	db, err = GetDatabase(context.Background())
	if err != nil {
		fmt.Printf("Error getting database: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	query = "INSERT INTO characters (player_id, character_id, name, skin_key, weapon_1, weapon_2, weapon_3, equipment_1, equipment_2, class_type, faction) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)"
	if _, err := submitExec(context.Background(), db.DB, query, player_id, id, newCharacterRequest.Name, newCharacterRequest.SkinKey, newCharacterRequest.Weapon1, newCharacterRequest.Weapon2, newCharacterRequest.Weapon3, newCharacterRequest.Equipment1, newCharacterRequest.Equipment2, newCharacterRequest.ClassType, newCharacterRequest.Faction); err != nil {
		fmt.Printf("Error inserting character into database: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":      "ok",
		"characterId": id,
		"message":     fmt.Sprintf("character %s created", newCharacterRequest.Name),
	})
}

func createCharacterID() (string, error) {
	id := uuid.New().String()

	db, err := GetDatabase(context.Background())
	if err != nil {
		return "", err
	}
	query := "SELECT COUNT(*) FROM characters WHERE character_id = $1"

	rows, err := submitQuery(context.Background(), db.DB, query, id)
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
		//print object
		fmt.Printf("UpdateCharacterRequest: %+v\n", updateRequest)
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "all character fields are required",
		})
		return
	}

	playerID, err := getPlayerIDFromSession(updateRequest.SessionToken)
	if err != nil {
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid session token",
			"error":   err.Error(),
		})
		return
	}

	query := "UPDATE characters SET name = $1, skin_key = $2, weapon_1 = $3, weapon_2 = $4, weapon_3 = $5, equipment_1 = $6, equipment_2 = $7, class_type = $8, faction = $9 WHERE character_id = $10 AND player_id = $11"
	result, err := submitExec(context.Background(), databaseInstance.DB, query, updateRequest.Name, updateRequest.SkinKey, updateRequest.Weapon1, updateRequest.Weapon2, updateRequest.Weapon3, updateRequest.Equipment1, updateRequest.Equipment2, updateRequest.ClassType, updateRequest.Faction, updateRequest.CharacterID, playerID)
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

	if deleteRequest.SessionToken == "" || deleteRequest.CharacterID == "" {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "sessionToken and characterId are required",
		})
		return
	}

	playerID, err := getPlayerIDFromSession(deleteRequest.SessionToken)
	if err != nil {
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid session token",
			"error":   err.Error(),
		})
		return
	}

	query := "DELETE FROM characters WHERE character_id = $1 AND player_id = $2"
	result, err := submitExec(context.Background(), databaseInstance.DB, query, deleteRequest.CharacterID, playerID)
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
		"characterId": deleteRequest.CharacterID,
		"message":     "character deleted",
	})
}

func handleCreateAccount(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// get account name from request body
	accountName := struct {
		AccountName string `json:"accountName"`
		Password    string `json:"password"`
	}{}
	if err := json.NewDecoder(request.Body).Decode(&accountName); err != nil {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid request body",
			"error":   err.Error(),
		})
		return
	}

	if accountName.AccountName == "" {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "account name is required",
		})
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)

	playerID, err := createPlayerID()
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "failed to create player ID",
			"error":   err.Error(),
		})
		return
	}

	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":   "ok",
		"message":  "account created",
		"playerId": playerID,
	})

	db, err := GetDatabase(context.Background())
	if err != nil {
		fmt.Printf("Error getting database: %v\n", err)
		return
	}

	// insert new player into database with playerID and accountName, along with any other default values (empty equipment/characters, etc)
	query := "INSERT INTO players (id, account_name, password) VALUES ($1, $2, $3)"
	if _, err := submitExec(context.Background(), db.DB, query, playerID, accountName.AccountName, accountName.Password); err != nil {
		fmt.Printf("Error inserting player into database: %v\n", err)
		return
	}
}

func createPlayerID() (string, error) {
	// create a unique player ID, possibly using a database sequence or UUID. Ensure ID not in database, retry if it is.
	id := uuid.New().String()

	db, err := GetDatabase(context.Background())
	if err != nil {
		return "", err
	}
	query := "SELECT COUNT(*) FROM players WHERE id = $1"

	//query database to check if ID exists, return error if it does (extremely unlikely with UUIDs, but good to check)
	rows, err := submitQuery(context.Background(), db.DB, query, id)
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

func handleLogout(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	type Logout struct {
		SessionToken string `json:"sessionToken"`
	}

	var logoutData Logout
	if err := json.NewDecoder(request.Body).Decode(&logoutData); err != nil {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid request body",
			"error":   err.Error(),
		})
		return
	}

	db, err := GetDatabase(context.Background())
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	query := "DELETE FROM session_tokens WHERE session_token = $1"
	if _, err := submitExec(context.Background(), db.DB, query, logoutData.SessionToken); err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "failed to invalidate session token",
			"error":   err.Error(),
		})
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":  "ok",
		"message": "logout successful",
	})
}

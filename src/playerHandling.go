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
	mux.HandleFunc("/player/equipmentAndCharacters", api.handleEquipmentAndCharacters)
	mux.HandleFunc("/player/createAccount", handleCreateAccount)
	mux.HandleFunc("/player/logout", handleLogout)
}

func (api *PlayerAPI) handleLogin(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	type LoginRequest struct {
		ID       string `json:"id"`
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

	query := "SELECT account_name FROM players WHERE id = $1 AND password = $2"
	rows, err := submitQuery(context.Background(), db.DB, query, loginRequest.ID, loginRequest.Password)
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

	var accountName string
	if rows.Next() {
		if err := rows.Scan(&accountName); err != nil {
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
	if _, err := submitExec(context.Background(), db.DB, query, loginRequest.ID, sessionToken, time.Now().Add(24*time.Hour).UTC()); err != nil {
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

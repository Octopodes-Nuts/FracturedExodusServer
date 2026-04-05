package playerhandling

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	server "fracturedexodusserver/server"

	"github.com/google/uuid"
)

// handleLogin authenticates a player with username and password, returning a session token.
// POST /player/login
// Request: {"username": "string", "password": "string"}
// Response: {"status": "ok", "message": "...", "sessionToken": "string", "issuedAt": "timestamp", "friendCode": "string"}
func (api *PlayerAPI) handleLogin(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][login] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][login] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	type LoginRequest struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	sessionToken, err := createSessionToken()
	if err != nil {
		fmt.Printf("[DEBUG][login] session token generation failed: %v\n", err)
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
		fmt.Printf("[DEBUG][login] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid request body",
			"error":   err.Error(),
		})
		return
	}
	fmt.Printf("[DEBUG][login] decoded request username=%s passwordSet=%t\n", loginRequest.Username, loginRequest.Password != "")

	db, err := server.GetDatabase(context.Background())
	if err != nil {
		fmt.Printf("[DEBUG][login] GetDatabase failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}
	fmt.Printf("[DEBUG][login] database handle acquired\n")

	query := "SELECT id FROM players WHERE account_name = $1 AND password = $2"
	fmt.Printf("[DEBUG][login] querying credentials for username=%s\n", loginRequest.Username)
	rows, err := server.SubmitQuery(context.Background(), db.DB, query, loginRequest.Username, loginRequest.Password)
	if err != nil {
		fmt.Printf("[DEBUG][login] credential query failed: %v\n", err)
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
			fmt.Printf("[DEBUG][login] failed scanning credential row: %v\n", err)
			response.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "database scan failed",
				"error":   err.Error(),
			})
			return
		}
		fmt.Printf("[DEBUG][login] credentials validated accountId=%s\n", id)
	} else {
		fmt.Printf("[DEBUG][login] credentials rejected username=%s\n", loginRequest.Username)
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid credentials",
		})
		return
	}

	query = "INSERT INTO session_tokens (player_id, session_token, expiration) VALUES ($1, $2, $3)"
	fmt.Printf("[DEBUG][login] creating session for accountId=%s\n", id)
	if _, err := server.SubmitExec(context.Background(), db.DB, query, id, sessionToken, time.Now().Add(24*time.Hour).UTC()); err != nil {
		fmt.Printf("[DEBUG][login] session insert failed accountId=%s err=%v\n", id, err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "failed to create session",
			"error":   err.Error(),
		})
		return
	}

	friendCode, err := ensureFriendCode(context.Background(), db, id)
	if err != nil {
		fmt.Printf("[DEBUG][login] ensureFriendCode failed accountId=%s err=%v\n", id, err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "failed to assign friend code",
			"error":   err.Error(),
		})
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	fmt.Printf("[DEBUG][login] request succeeded accountId=%s friendCode=%s\n", id, friendCode)

	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":       "ok",
		"message":      "login accepted",
		"issuedAt":     time.Now().UTC().Format(time.RFC3339),
		"accountId":    id,
		"sessionToken": sessionToken,
		"friendCode":   friendCode,
	})
}

// handleAccountInfo returns account information for an authenticated player.
// POST /player/accountInfo
// Request: {"playerId": "string", "sessionToken": "string"}
// Response: {"accountId": "uuid", "displayName": "string", "region": "string", "version": "string"}
func (api *PlayerAPI) handleAccountInfo(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][accountInfo] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][accountInfo] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	type AccountInfoRequest struct {
		SessionToken string `json:"sessionToken"`
		PlayerID     string `json:"playerId"`
	}

	var accountInfoRequest AccountInfoRequest
	if err := json.NewDecoder(request.Body).Decode(&accountInfoRequest); err != nil {
		fmt.Printf("[DEBUG][accountInfo] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid request body",
			"error":   err.Error(),
		})
		return
	}
	fmt.Printf("[DEBUG][accountInfo] decoded request playerId=%s sessionTokenSet=%t\n", accountInfoRequest.PlayerID, accountInfoRequest.SessionToken != "")

	db, err := server.GetDatabase(context.Background())
	if err != nil {
		fmt.Printf("[DEBUG][accountInfo] GetDatabase failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}
	query := "SELECT 1 FROM session_tokens WHERE session_token = $1 AND player_id = $2 AND expiration > $3"
	fmt.Printf("[DEBUG][accountInfo] validating session token for playerId=%s\n", accountInfoRequest.PlayerID)
	rows, err := server.SubmitQuery(context.Background(), db.DB, query, accountInfoRequest.SessionToken, accountInfoRequest.PlayerID, time.Now().UTC())
	if err != nil {
		fmt.Printf("[DEBUG][accountInfo] session validation query failed: %v\n", err)
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
		fmt.Printf("[DEBUG][accountInfo] session validation failed for playerId=%s\n", accountInfoRequest.PlayerID)
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid session token",
		})
		return
	}
	fmt.Printf("[DEBUG][accountInfo] session validated for playerId=%s\n", accountInfoRequest.PlayerID)

	query = "SELECT account_level, account_experience FROM players WHERE id = $1"
	fmt.Printf("[DEBUG][accountInfo] querying account stats for playerId=%s\n", accountInfoRequest.PlayerID)
	rows, err = server.SubmitQuery(context.Background(), db.DB, query, accountInfoRequest.PlayerID)
	if err != nil {
		fmt.Printf("[DEBUG][accountInfo] account stats query failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database query failed",
			"error":   err.Error(),
		})
		return
	}
	defer rows.Close()
	var accountLevel int
	var experience int
	if rows.Next() {
		if err := rows.Scan(&accountLevel, &experience); err != nil {
			fmt.Printf("[DEBUG][accountInfo] failed scanning account stats: %v\n", err)
			return
		}
	} else {
		fmt.Printf("[DEBUG][accountInfo] player not found for playerId=%s\n", accountInfoRequest.PlayerID)
		response.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "player not found",
		})
		return
	}
	fmt.Printf("[DEBUG][accountInfo] account stats loaded level=%d experience=%d\n", accountLevel, experience)

	query = `SELECT p.id, p.account_name FROM friend_connections fc
		JOIN players p ON p.id = CASE WHEN fc.player_one_id = $1 THEN fc.player_two_id ELSE fc.player_one_id END
		WHERE (fc.player_one_id = $1 OR fc.player_two_id = $1) AND fc.status = 'accepted'`
	fmt.Printf("[DEBUG][accountInfo] querying accepted friends for playerId=%s\n", accountInfoRequest.PlayerID)
	rows, err = server.SubmitQuery(context.Background(), db.DB, query, accountInfoRequest.PlayerID)
	if err != nil {
		fmt.Printf("[DEBUG][accountInfo] accepted friends query failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database query failed",
			"error":   err.Error(),
		})
		return
	}
	defer rows.Close()
	friends := []map[string]string{}
	for rows.Next() {
		var friendID, friendUsername string
		if err := rows.Scan(&friendID, &friendUsername); err != nil {
			fmt.Printf("[DEBUG][accountInfo] failed scanning accepted friend row: %v\n", err)
			return
		}
		friends = append(friends, map[string]string{"accountId": friendID, "username": friendUsername})
	}
	fmt.Printf("[DEBUG][accountInfo] accepted friends count=%d\n", len(friends))

	friendRequests := []string{}
	query = "SELECT player_one_id FROM friend_connections WHERE player_two_id = $1 AND status = 'pending'"
	fmt.Printf("[DEBUG][accountInfo] querying inbound friend requests for playerId=%s\n", accountInfoRequest.PlayerID)
	rows, err = server.SubmitQuery(context.Background(), db.DB, query, accountInfoRequest.PlayerID)
	if err != nil {
		fmt.Printf("[DEBUG][accountInfo] inbound friend requests query failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database query failed",
			"error":   err.Error(),
		})
		return
	}
	defer rows.Close()
	for rows.Next() {
		var friendRequestID string
		if err := rows.Scan(&friendRequestID); err != nil {
			fmt.Printf("[DEBUG][accountInfo] failed scanning inbound friend request row: %v\n", err)
			return
		}
		friendRequests = append(friendRequests, friendRequestID)
	}
	fmt.Printf("[DEBUG][accountInfo] inbound friend requests count=%d\n", len(friendRequests))

	pendingFriendRequests := []string{}
	query = "SELECT player_two_id FROM friend_connections WHERE player_one_id = $1 AND status = 'pending'"
	fmt.Printf("[DEBUG][accountInfo] querying outbound friend requests for playerId=%s\n", accountInfoRequest.PlayerID)
	rows, err = server.SubmitQuery(context.Background(), db.DB, query, accountInfoRequest.PlayerID)
	if err != nil {
		fmt.Printf("[DEBUG][accountInfo] outbound friend requests query failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database query failed",
			"error":   err.Error(),
		})
		return
	}
	defer rows.Close()
	for rows.Next() {
		var pendingFriendRequestID string
		if err := rows.Scan(&pendingFriendRequestID); err != nil {
			fmt.Printf("[DEBUG][accountInfo] failed scanning outbound friend request row: %v\n", err)
			return
		}
		pendingFriendRequests = append(pendingFriendRequests, pendingFriendRequestID)
	}
	fmt.Printf("[DEBUG][accountInfo] outbound friend requests count=%d\n", len(pendingFriendRequests))

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":                "ok",
		"accountId":             accountInfoRequest.PlayerID,
		"accountLevel":          accountLevel,
		"accountExperience":     experience,
		"buildVersion":          api.buildVersion,
		"friends":               friends,
		"friendRequests":        friendRequests,
		"pendingFriendRequests": pendingFriendRequests,
	})
}

func handleCreateAccount(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

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

	query := "INSERT INTO players (id, account_name, password) VALUES ($1, $2, $3)"
	if _, err := server.SubmitExec(context.Background(), db.DB, query, playerID, accountName.AccountName, accountName.Password); err != nil {
		response.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "account name already taken",
			"error":   err.Error(),
		})
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":   "ok",
		"message":  "account created",
		"playerId": playerID,
	})
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

	query := "DELETE FROM session_tokens WHERE session_token = $1"
	if _, err := server.SubmitExec(context.Background(), db.DB, query, logoutData.SessionToken); err != nil {
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

func createSessionToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func createPlayerID() (string, error) {
	id := uuid.New().String()

	db, err := server.GetDatabase(context.Background())
	if err != nil {
		return "", err
	}
	query := "SELECT COUNT(*) FROM players WHERE id = $1"

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

const friendCodeAlphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

func generateFriendCodeSuffix() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	result := make([]byte, 4)
	for i, v := range b {
		result[i] = friendCodeAlphabet[int(v)%len(friendCodeAlphabet)]
	}
	return string(result), nil
}

// ensureFriendCode returns the player's friend code (username#XXXX), creating the suffix if needed.
func ensureFriendCode(ctx context.Context, db *server.Database, playerID string) (string, error) {
	rows, err := server.SubmitQuery(ctx, db.DB, "SELECT account_name, COALESCE(friend_code_suffix, '') FROM players WHERE id = $1", playerID)
	if err != nil {
		return "", err
	}
	var accountName, suffix string
	if rows.Next() {
		if scanErr := rows.Scan(&accountName, &suffix); scanErr != nil {
			_ = rows.Close()
			return "", scanErr
		}
	}
	if closeErr := rows.Close(); closeErr != nil {
		return "", closeErr
	}

	if suffix != "" {
		return accountName + "#" + suffix, nil
	}

	for range 10 {
		newSuffix, err := generateFriendCodeSuffix()
		if err != nil {
			return "", err
		}
		result, err := server.SubmitExec(ctx, db.DB,
			"UPDATE players SET friend_code_suffix = $1 WHERE id = $2 AND friend_code_suffix IS NULL",
			newSuffix, playerID)
		if err != nil {
			continue
		}
		affected, _ := result.RowsAffected()
		if affected == 1 {
			return accountName + "#" + newSuffix, nil
		}
		rows, err = server.SubmitQuery(ctx, db.DB, "SELECT COALESCE(friend_code_suffix, '') FROM players WHERE id = $1", playerID)
		if err != nil {
			return "", err
		}
		if rows.Next() {
			_ = rows.Scan(&suffix)
		}
		_ = rows.Close()
		if suffix != "" {
			return accountName + "#" + suffix, nil
		}
	}
	return "", fmt.Errorf("failed to assign friend code after multiple attempts")
}

// lookupPlayerByFriendCode resolves a friend code (username#XXXX) to a player ID.
// Returns ("", nil) if not found.
func lookupPlayerByFriendCode(ctx context.Context, db *server.Database, friendCode string) (string, error) {
	idx := strings.LastIndex(friendCode, "#")
	if idx < 0 || idx == len(friendCode)-1 {
		return "", nil
	}
	username := friendCode[:idx]
	suffix := friendCode[idx+1:]
	rows, err := server.SubmitQuery(ctx, db.DB,
		"SELECT id FROM players WHERE account_name = $1 AND friend_code_suffix = $2 LIMIT 1",
		username, suffix)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", nil
	}
	var playerID string
	if err := rows.Scan(&playerID); err != nil {
		return "", err
	}
	return playerID, nil
}

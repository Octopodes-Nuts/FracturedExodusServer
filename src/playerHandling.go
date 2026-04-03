package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

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

// handleLogin authenticates a player with username and password, returning a session token.
// POST /player/login
// Request: {"username": "string", "password": "string"}
// Response: {"status": "ok", "message": "...", "sessionToken": "string", "issuedAt": "timestamp"}
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

	db, err := GetDatabase(context.Background())
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
	// check credentials against database

	query := "SELECT id FROM players WHERE account_name = $1 AND password = $2"
	fmt.Printf("[DEBUG][login] querying credentials for username=%s\n", loginRequest.Username)
	rows, err := submitQuery(context.Background(), db.DB, query, loginRequest.Username, loginRequest.Password)
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

	// insert session token into database with expiration time, associated with player account
	query = "INSERT INTO session_tokens (player_id, session_token, expiration) VALUES ($1, $2, $3)"
	fmt.Printf("[DEBUG][login] creating session for accountId=%s\n", id)
	if _, err := submitExec(context.Background(), db.DB, query, id, sessionToken, time.Now().Add(24*time.Hour).UTC()); err != nil {
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

func createSessionToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
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
func ensureFriendCode(ctx context.Context, db *Database, playerID string) (string, error) {
	rows, err := submitQuery(ctx, db.DB, "SELECT account_name, COALESCE(friend_code_suffix, '') FROM players WHERE id = $1", playerID)
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
		result, err := submitExec(ctx, db.DB,
			"UPDATE players SET friend_code_suffix = $1 WHERE id = $2 AND friend_code_suffix IS NULL",
			newSuffix, playerID)
		if err != nil {
			// likely unique constraint collision — retry
			continue
		}
		affected, _ := result.RowsAffected()
		if affected == 1 {
			return accountName + "#" + newSuffix, nil
		}
		// 0 rows: concurrent update already set it — re-read
		rows, err = submitQuery(ctx, db.DB, "SELECT COALESCE(friend_code_suffix, '') FROM players WHERE id = $1", playerID)
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
func lookupPlayerByFriendCode(ctx context.Context, db *Database, friendCode string) (string, error) {
	idx := strings.LastIndex(friendCode, "#")
	if idx < 0 || idx == len(friendCode)-1 {
		return "", nil
	}
	username := friendCode[:idx]
	suffix := friendCode[idx+1:]
	rows, err := submitQuery(ctx, db.DB,
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

	db, err := GetDatabase(context.Background())
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
	rows, err := submitQuery(context.Background(), db.DB, query, accountInfoRequest.SessionToken, accountInfoRequest.PlayerID, time.Now().UTC())
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
	// session token is valid, return account level, experience, and freind connections in response
	// get information from database
	query = "SELECT account_level, account_experience FROM players WHERE id = $1"
	fmt.Printf("[DEBUG][accountInfo] querying account stats for playerId=%s\n", accountInfoRequest.PlayerID)
	rows, err = submitQuery(context.Background(), db.DB, query, accountInfoRequest.PlayerID)
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
	rows, err = submitQuery(context.Background(), db.DB, query, accountInfoRequest.PlayerID)
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
	rows, err = submitQuery(context.Background(), db.DB, query, accountInfoRequest.PlayerID)
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
	rows, err = submitQuery(context.Background(), db.DB, query, accountInfoRequest.PlayerID)
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
	fmt.Printf("[DEBUG][accountInfo] request succeeded for playerId=%s\n", accountInfoRequest.PlayerID)

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":                "ok",
		"accountLevel":          accountLevel,
		"experience":            experience,
		"friends":               friends,
		"friendRequests":        friendRequests,
		"pendingFriendRequests": pendingFriendRequests,
	})
}

// handleFriendRequest sends a friend request to another player.
// POST /player/friendRequest
// Request: {"sessionToken": "string", "playerId": "string"} OR {"sessionToken": "string", "friendCode": "username#XXXX"}
// Response: {"status": "ok", "message": "..."}
func (api *PlayerAPI) handleFriendRequest(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][friendRequest] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][friendRequest] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	type FriendRequest struct {
		SessionToken string `json:"sessionToken"`
		PlayerID     string `json:"playerId"`
		FriendCode   string `json:"friendCode"`
	}

	var friendRequest FriendRequest
	if err := json.NewDecoder(request.Body).Decode(&friendRequest); err != nil {
		fmt.Printf("[DEBUG][friendRequest] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid request body",
			"error":   err.Error(),
		})
		return
	}

	if friendRequest.SessionToken == "" || (friendRequest.PlayerID == "" && friendRequest.FriendCode == "") {
		fmt.Printf("[DEBUG][friendRequest] rejected request: missing fields sessionTokenSet=%t playerId=%s friendCode=%s\n", friendRequest.SessionToken != "", friendRequest.PlayerID, friendRequest.FriendCode)
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "sessionToken and one of playerId or friendCode are required",
		})
		return
	}

	senderID, err := getPlayerIDFromSession(friendRequest.SessionToken)
	if err != nil {
		fmt.Printf("[DEBUG][friendRequest] session validation failed: %v\n", err)
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid session token",
			"error":   err.Error(),
		})
		return
	}

	if friendRequest.FriendCode != "" {
		db, err := GetDatabase(context.Background())
		if err != nil {
			fmt.Printf("[DEBUG][friendRequest] GetDatabase failed resolving friendCode: %v\n", err)
			response.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "database error",
				"error":   err.Error(),
			})
			return
		}
		resolved, err := lookupPlayerByFriendCode(context.Background(), db, friendRequest.FriendCode)
		if err != nil {
			fmt.Printf("[DEBUG][friendRequest] friendCode lookup failed friendCode=%s err=%v\n", friendRequest.FriendCode, err)
			response.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "database error",
				"error":   err.Error(),
			})
			return
		}
		if resolved == "" {
			fmt.Printf("[DEBUG][friendRequest] friendCode not found friendCode=%s\n", friendRequest.FriendCode)
			response.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "player not found",
			})
			return
		}
		friendRequest.PlayerID = resolved
	}

	fmt.Printf("[DEBUG][friendRequest] session validated senderId=%s targetId=%s\n", senderID, friendRequest.PlayerID)

	if senderID == friendRequest.PlayerID {
		fmt.Printf("[DEBUG][friendRequest] rejected request: sender attempted self-request senderId=%s\n", senderID)
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "cannot send a friend request to yourself",
		})
		return
	}

	db, err := GetDatabase(context.Background())
	if err != nil {
		fmt.Printf("[DEBUG][friendRequest] GetDatabase failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	query := "SELECT 1 FROM players WHERE id = $1"
	rows, err := submitQuery(context.Background(), db.DB, query, friendRequest.PlayerID)
	if err != nil {
		fmt.Printf("[DEBUG][friendRequest] target player lookup failed targetId=%s err=%v\n", friendRequest.PlayerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database query failed",
			"error":   err.Error(),
		})
		return
	}

	if !rows.Next() {
		rows.Close()
		fmt.Printf("[DEBUG][friendRequest] target player not found targetId=%s\n", friendRequest.PlayerID)
		response.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "target player not found",
		})
		return
	}
	rows.Close()
	fmt.Printf("[DEBUG][friendRequest] target player exists targetId=%s\n", friendRequest.PlayerID)

	query = "SELECT connection_id, player_one_id, player_two_id, status FROM friend_connections WHERE (player_one_id = $1 AND player_two_id = $2) OR (player_one_id = $2 AND player_two_id = $1)"
	rows, err = submitQuery(context.Background(), db.DB, query, senderID, friendRequest.PlayerID)
	if err != nil {
		fmt.Printf("[DEBUG][friendRequest] existing connection lookup failed senderId=%s targetId=%s err=%v\n", senderID, friendRequest.PlayerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database query failed",
			"error":   err.Error(),
		})
		return
	}
	defer rows.Close()

	var connectionID string
	var playerOneID string
	var playerTwoID string
	var status string
	if rows.Next() {
		if err := rows.Scan(&connectionID, &playerOneID, &playerTwoID, &status); err != nil {
			fmt.Printf("[DEBUG][friendRequest] failed scanning existing connection row: %v\n", err)
			response.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "database scan failed",
				"error":   err.Error(),
			})
			return
		}

		fmt.Printf("[DEBUG][friendRequest] existing connection found connectionId=%s status=%s playerOne=%s playerTwo=%s\n", connectionID, status, playerOneID, playerTwoID)

		switch status {
		case "pending":
			if playerOneID == senderID && playerTwoID == friendRequest.PlayerID {
				fmt.Printf("[DEBUG][friendRequest] pending request already exists senderId=%s targetId=%s\n", senderID, friendRequest.PlayerID)
				response.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(response).Encode(map[string]any{
					"status":  "error",
					"message": "friend request already pending",
				})
				return
			}

			if playerOneID == friendRequest.PlayerID && playerTwoID == senderID {
				fmt.Printf("[DEBUG][friendRequest] inverse pending request exists senderId=%s targetId=%s\n", senderID, friendRequest.PlayerID)
				response.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(response).Encode(map[string]any{
					"status":  "error",
					"message": "incoming friend request already exists",
				})
				return
			}
		case "accepted":
			fmt.Printf("[DEBUG][friendRequest] players already friends senderId=%s targetId=%s\n", senderID, friendRequest.PlayerID)
			response.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "players are already friends",
			})
			return
		case "blocked":
			fmt.Printf("[DEBUG][friendRequest] blocked relationship prevents request senderId=%s targetId=%s\n", senderID, friendRequest.PlayerID)
			response.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "friend request blocked",
			})
			return
		case "removed":
			fmt.Printf("[DEBUG][friendRequest] reactivating removed connection as pending senderId=%s targetId=%s connectionId=%s\n", senderID, friendRequest.PlayerID, connectionID)
			updateQuery := "UPDATE friend_connections SET player_one_id = $1, player_two_id = $2, status = 'pending', updated_at = $3 WHERE connection_id = $4"
			if _, err := submitExec(context.Background(), db.DB, updateQuery, senderID, friendRequest.PlayerID, time.Now().UTC(), connectionID); err != nil {
				fmt.Printf("[DEBUG][friendRequest] failed reactivating removed connection connectionId=%s err=%v\n", connectionID, err)
				response.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(response).Encode(map[string]any{
					"status":  "error",
					"message": "database error",
					"error":   err.Error(),
				})
				return
			}

			fmt.Printf("[DEBUG][friendRequest] request accepted for processing by reactivating connectionId=%s\n", connectionID)
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":         "ok",
				"message":        "friend request sent",
				"senderPlayerId": senderID,
				"targetPlayerId": friendRequest.PlayerID,
			})
			return
		default:
			fmt.Printf("[DEBUG][friendRequest] unsupported connection status=%s connectionId=%s\n", status, connectionID)
			response.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "friend connection state is not actionable",
			})
			return
		}
	}

	connectionID, err = createFriendConnectionID()
	if err != nil {
		fmt.Printf("[DEBUG][friendRequest] failed to create connection ID: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "failed to create connection ID",
			"error":   err.Error(),
		})
		return
	}

	query = "INSERT INTO friend_connections (connection_id, player_one_id, player_two_id, status, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6)"
	if _, err := submitExec(context.Background(), db.DB, query, connectionID, senderID, friendRequest.PlayerID, "pending", time.Now().UTC(), time.Now().UTC()); err != nil {
		fmt.Printf("[DEBUG][friendRequest] failed inserting friend request senderId=%s targetId=%s connectionId=%s err=%v\n", senderID, friendRequest.PlayerID, connectionID, err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	fmt.Printf("[DEBUG][friendRequest] friend request created senderId=%s targetId=%s connectionId=%s\n", senderID, friendRequest.PlayerID, connectionID)
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":         "ok",
		"message":        "friend request sent",
		"connectionId":   connectionID,
		"senderPlayerId": senderID,
		"targetPlayerId": friendRequest.PlayerID,
	})
}

func (api *PlayerAPI) handleAcceptRejectFriendRequest(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][acceptRejectFriendRequest] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][acceptRejectFriendRequest] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	type AcceptRejectFriendRequest struct {
		Accept       bool   `json:"accept"`
		SessionToken string `json:"sessionToken"`
		PlayerID     string `json:"playerId"`
	}

	var acceptRejectRequest AcceptRejectFriendRequest
	if err := json.NewDecoder(request.Body).Decode(&acceptRejectRequest); err != nil {
		fmt.Printf("[DEBUG][acceptRejectFriendRequest] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid request body",
			"error":   err.Error(),
		})
		return
	}

	if acceptRejectRequest.SessionToken == "" || acceptRejectRequest.PlayerID == "" {
		fmt.Printf("[DEBUG][acceptRejectFriendRequest] rejected request: missing fields sessionTokenSet=%t senderPlayerId=%s\n", acceptRejectRequest.SessionToken != "", acceptRejectRequest.PlayerID)
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "sessionToken and playerId are required",
		})
		return
	}

	accepterID, err := getPlayerIDFromSession(acceptRejectRequest.SessionToken)
	if err != nil {
		fmt.Printf("[DEBUG][acceptRejectFriendRequest] session validation failed: %v\n", err)
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid session token",
			"error":   err.Error(),
		})
		return
	}
	fmt.Printf("[DEBUG][acceptRejectFriendRequest] session validated accepterId=%s senderId=%s accept=%t\n", accepterID, acceptRejectRequest.PlayerID, acceptRejectRequest.Accept)

	if accepterID == acceptRejectRequest.PlayerID {
		fmt.Printf("[DEBUG][acceptRejectFriendRequest] rejected request: sender and accepter are same playerId=%s\n", accepterID)
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "playerId must be the initial sender, not the accepter",
		})
		return
	}

	db, err := GetDatabase(context.Background())
	if err != nil {
		fmt.Printf("[DEBUG][acceptRejectFriendRequest] GetDatabase failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	query := "SELECT connection_id FROM friend_connections WHERE player_one_id = $1 AND player_two_id = $2 AND status = 'pending'"
	rows, err := submitQuery(context.Background(), db.DB, query, acceptRejectRequest.PlayerID, accepterID)
	if err != nil {
		fmt.Printf("[DEBUG][acceptRejectFriendRequest] pending request lookup failed senderId=%s accepterId=%s err=%v\n", acceptRejectRequest.PlayerID, accepterID, err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database query failed",
			"error":   err.Error(),
		})
		return
	}
	defer rows.Close()

	var connectionID string
	if !rows.Next() {
		fmt.Printf("[DEBUG][acceptRejectFriendRequest] no pending request found senderId=%s accepterId=%s\n", acceptRejectRequest.PlayerID, accepterID)
		response.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "pending friend request not found",
		})
		return
	}

	if err := rows.Scan(&connectionID); err != nil {
		fmt.Printf("[DEBUG][acceptRejectFriendRequest] failed scanning pending request row: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database scan failed",
			"error":   err.Error(),
		})
		return
	}

	nextStatus := "removed"
	if acceptRejectRequest.Accept {
		nextStatus = "accepted"
	}

	query = "UPDATE friend_connections SET status = $1, updated_at = $2 WHERE connection_id = $3"
	if _, err := submitExec(context.Background(), db.DB, query, nextStatus, time.Now().UTC(), connectionID); err != nil {
		fmt.Printf("[DEBUG][acceptRejectFriendRequest] failed updating request status connectionId=%s status=%s err=%v\n", connectionID, nextStatus, err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}

	fmt.Printf("[DEBUG][acceptRejectFriendRequest] request updated connectionId=%s senderId=%s accepterId=%s newStatus=%s\n", connectionID, acceptRejectRequest.PlayerID, accepterID, nextStatus)
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":           "ok",
		"message":          "friend request updated",
		"friendStatus":     nextStatus,
		"connectionId":     connectionID,
		"senderPlayerId":   acceptRejectRequest.PlayerID,
		"accepterPlayerId": accepterID,
		"accepted":         acceptRejectRequest.Accept,
	})
}

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

	playerID, err := getPlayerIDFromSession(setActiveCharacterRequest.SessionToken)
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
		db, err := GetDatabase(context.Background())
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
		if _, err := submitExec(context.Background(), db.DB, "DELETE FROM active_characters WHERE player_id = $1", playerID); err != nil {
			fmt.Printf("[DEBUG][setActiveCharacter] delete active_characters failed playerId=%s err=%v\n", playerID, err)
			response.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"status":  "error",
				"message": "database error",
				"error":   err.Error(),
			})
			return
		}
		if err := clearPartyActiveCharacterSelection(request.Context(), playerID); err != nil {
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

	character, found, err := getCharacterByID(request.Context(), setActiveCharacterRequest.CharacterID)
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

	db, err := GetDatabase(context.Background())
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
	if _, err := submitExec(context.Background(), db.DB, query, playerID, setActiveCharacterRequest.CharacterID, time.Now().UTC()); err != nil {
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

	if err := syncPartyActiveCharacterSelection(request.Context(), playerID, character); err != nil {
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
		fmt.Printf("[DEBUG][newCharacter] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid request body",
			"error":   err.Error(),
		})
		return
	}

	player_id := ""
	fmt.Printf("[DEBUG][newCharacter] decoded request name=%s classType=%d faction=%d sessionTokenSet=%t\n", newCharacterRequest.Name, newCharacterRequest.ClassType, newCharacterRequest.Faction, newCharacterRequest.SessionToken != "")

	db, err := GetDatabase(context.Background())
	if err != nil {
		fmt.Printf("[DEBUG][newCharacter] GetDatabase failed during session lookup err=%v\n", err)
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
		fmt.Printf("[DEBUG][newCharacter] session lookup failed err=%v\n", err)
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
		fmt.Printf("[DEBUG][newCharacter] session validation failed sessionTokenSet=%t\n", newCharacterRequest.SessionToken != "")
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invalid session token",
		})
		return
	}
	fmt.Printf("[DEBUG][newCharacter] session validated playerId=%s\n", player_id)

	id, err := createCharacterID()
	if err != nil {
		fmt.Printf("[DEBUG][newCharacter] createCharacterID failed playerId=%s err=%v\n", player_id, err)
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
		fmt.Printf("[DEBUG][newCharacter] GetDatabase failed during insert playerId=%s characterId=%s err=%v\n", player_id, id, err)
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
		fmt.Printf("[DEBUG][newCharacter] character insert failed playerId=%s characterId=%s name=%s faction=%d err=%v\n", player_id, id, newCharacterRequest.Name, newCharacterRequest.Faction, err)
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "database error",
			"error":   err.Error(),
		})
		return
	}
	fmt.Printf("[DEBUG][newCharacter] request succeeded playerId=%s characterId=%s name=%s faction=%d\n", player_id, id, newCharacterRequest.Name, newCharacterRequest.Faction)

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

func createFriendConnectionID() (string, error) {
	id := uuid.New().String()

	db, err := GetDatabase(context.Background())
	if err != nil {
		return "", err
	}

	query := "SELECT COUNT(*) FROM friend_connections WHERE connection_id = $1"
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

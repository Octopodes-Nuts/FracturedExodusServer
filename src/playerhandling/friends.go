package playerhandling

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	server "fracturedexodusserver/src"

	"github.com/google/uuid"
)

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

	senderID, err := server.GetPlayerIDFromSession(friendRequest.SessionToken)
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
		db, err := server.GetDatabase(context.Background())
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

	db, err := server.GetDatabase(context.Background())
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
	rows, err := server.SubmitQuery(context.Background(), db.DB, query, friendRequest.PlayerID)
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
	rows, err = server.SubmitQuery(context.Background(), db.DB, query, senderID, friendRequest.PlayerID)
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
			if _, err := server.SubmitExec(context.Background(), db.DB, updateQuery, senderID, friendRequest.PlayerID, time.Now().UTC(), connectionID); err != nil {
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
	if _, err := server.SubmitExec(context.Background(), db.DB, query, connectionID, senderID, friendRequest.PlayerID, "pending", time.Now().UTC(), time.Now().UTC()); err != nil {
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

	accepterID, err := server.GetPlayerIDFromSession(acceptRejectRequest.SessionToken)
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

	db, err := server.GetDatabase(context.Background())
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
	rows, err := server.SubmitQuery(context.Background(), db.DB, query, acceptRejectRequest.PlayerID, accepterID)
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
	if _, err := server.SubmitExec(context.Background(), db.DB, query, nextStatus, time.Now().UTC(), connectionID); err != nil {
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

func createFriendConnectionID() (string, error) {
	id := uuid.New().String()

	db, err := server.GetDatabase(context.Background())
	if err != nil {
		return "", err
	}

	query := "SELECT COUNT(*) FROM friend_connections WHERE connection_id = $1"
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

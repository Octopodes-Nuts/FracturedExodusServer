package matchmaking

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"

	server "fracturedexodusserver/server"
)

type partyInviteRecord struct {
	InviteID      string `json:"inviteId"`
	PartyID       string `json:"partyId"`
	FromPlayerID  string `json:"fromPlayerId"`
	ToPlayerID    string `json:"toPlayerId"`
	Status        string `json:"status"`
	CreatedAt     string `json:"createdAt"`
	ExpiresAt     string `json:"expiresAt"`
	Expired       bool   `json:"expired"`
	SeenByInviter bool   `json:"seenByInviter"`
	SeenByInvitee bool   `json:"seenByInvitee"`
}

type pendingInvite struct {
	InviteID     string
	PartyID      string
	FromPlayerID string
	ToPlayerID   string
	ExpiresAt    time.Time
}

// handlePartyInvite sends a party invite to another player.
// POST /matchmaking/party/invite
// Request: {"sessionToken": "string", "targetPlayerId": "string"}
// Response: {"status": "ok", "message": "..."}
func (api *MatchmakingAPI) handlePartyInvite(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][partyInvite] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][partyInvite] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var inviteRequest struct {
		SessionToken string `json:"sessionToken"`
		PlayerID     string `json:"playerId"`
	}
	if err := json.NewDecoder(request.Body).Decode(&inviteRequest); err != nil {
		fmt.Printf("[DEBUG][partyInvite] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if inviteRequest.SessionToken == "" || inviteRequest.PlayerID == "" {
		fmt.Printf("[DEBUG][partyInvite] rejected request: missing fields sessionTokenSet=%t targetPlayerId=%s\n", inviteRequest.SessionToken != "", inviteRequest.PlayerID)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	fmt.Printf("[DEBUG][partyInvite] decoded request targetPlayerId=%s\n", inviteRequest.PlayerID)

	inviterID, err := server.GetPlayerIDFromSession(inviteRequest.SessionToken)
	if err != nil {
		fmt.Printf("[DEBUG][partyInvite] session validation failed: %v\n", err)
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	fmt.Printf("[DEBUG][partyInvite] session validated inviterId=%s targetPlayerId=%s\n", inviterID, inviteRequest.PlayerID)
	if inviterID == inviteRequest.PlayerID {
		fmt.Printf("[DEBUG][partyInvite] rejected request: self invite inviterId=%s\n", inviterID)
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "cannot invite yourself",
		})
		return
	}

	playerDB, err := server.GetDatabase(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][partyInvite] GetDatabase failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if exists, err := playerExists(request.Context(), playerDB, inviteRequest.PlayerID); err != nil {
		fmt.Printf("[DEBUG][partyInvite] target player lookup failed targetPlayerId=%s err=%v\n", inviteRequest.PlayerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	} else if !exists {
		fmt.Printf("[DEBUG][partyInvite] target player not found targetPlayerId=%s\n", inviteRequest.PlayerID)
		response.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "player not found",
		})
		return
	}

	mmDB, err := server.GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][partyInvite] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	partyID, err := ensurePlayerParty(request.Context(), mmDB, inviterID)
	if err != nil {
		fmt.Printf("[DEBUG][partyInvite] ensurePlayerParty failed inviterId=%s err=%v\n", inviterID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("[DEBUG][partyInvite] inviter party resolved inviterId=%s partyId=%s\n", inviterID, partyID)

	targetPartyID, err := findPartyForPlayer(request.Context(), mmDB, inviteRequest.PlayerID)
	if err != nil {
		fmt.Printf("[DEBUG][partyInvite] findPartyForPlayer failed targetPlayerId=%s err=%v\n", inviteRequest.PlayerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if targetPartyID != "" && targetPartyID == partyID {
		fmt.Printf("[DEBUG][partyInvite] target already in same party partyId=%s targetPlayerId=%s\n", partyID, inviteRequest.PlayerID)
		response.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "player is already in your party",
		})
		return
	}

	pendingInviteExists, err := hasPendingInvite(request.Context(), mmDB, partyID, inviteRequest.PlayerID)
	if err != nil {
		fmt.Printf("[DEBUG][partyInvite] pending invite check failed partyId=%s targetPlayerId=%s err=%v\n", partyID, inviteRequest.PlayerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if pendingInviteExists {
		fmt.Printf("[DEBUG][partyInvite] pending invite already exists partyId=%s targetPlayerId=%s\n", partyID, inviteRequest.PlayerID)
		response.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "invite already pending",
		})
		return
	}

	if _, err := server.SubmitExec(request.Context(), mmDB.DB,
		"DELETE FROM party_invites WHERE party_id = $1 AND to_player_id = $2 AND status = 'pending' AND expires_at <= $3",
		partyID, inviteRequest.PlayerID, time.Now().UTC()); err != nil {
		fmt.Printf("[DEBUG][partyInvite] delete expired invite failed partyId=%s targetPlayerId=%s err=%v\n", partyID, inviteRequest.PlayerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	inviteID := "invite-" + uuid.NewString()
	now := time.Now().UTC()
	expiresAt := now.Add(5 * time.Minute)
	insertInviteQuery := `INSERT INTO party_invites
		(invite_id, party_id, from_player_id, to_player_id, status, created_at, expires_at, seen_by_inviter, seen_by_invitee)
		VALUES ($1, $2, $3, $4, 'pending', $5, $6, TRUE, FALSE)`
	if _, err := server.SubmitExec(request.Context(), mmDB.DB, insertInviteQuery, inviteID, partyID, inviterID, inviteRequest.PlayerID, now, expiresAt); err != nil {
		fmt.Printf("[DEBUG][partyInvite] insert invite failed inviteId=%s partyId=%s err=%v\n", inviteID, partyID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("[DEBUG][partyInvite] invite created inviteId=%s partyId=%s inviterId=%s targetPlayerId=%s\n", inviteID, partyID, inviterID, inviteRequest.PlayerID)

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":          "ok",
		"inviteId":        inviteID,
		"partyId":         partyID,
		"fromPlayerId":    inviterID,
		"toPlayerId":      inviteRequest.PlayerID,
		"expiresAt":       expiresAt.Format(time.RFC3339),
		"inviteStatus":    "pending",
		"inviteCreatedAt": now.Format(time.RFC3339),
	})
}

func (api *MatchmakingAPI) handlePartyRespond(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][partyRespond] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][partyRespond] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var respondRequest struct {
		SessionToken string `json:"sessionToken"`
		InviteID     string `json:"inviteId"`
		Accept       bool   `json:"accept"`
	}
	if err := json.NewDecoder(request.Body).Decode(&respondRequest); err != nil {
		fmt.Printf("[DEBUG][partyRespond] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if respondRequest.SessionToken == "" || respondRequest.InviteID == "" {
		fmt.Printf("[DEBUG][partyRespond] rejected request: missing fields sessionTokenSet=%t inviteId=%s\n", respondRequest.SessionToken != "", respondRequest.InviteID)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	fmt.Printf("[DEBUG][partyRespond] decoded request inviteId=%s accept=%t\n", respondRequest.InviteID, respondRequest.Accept)

	inviteeID, err := server.GetPlayerIDFromSession(respondRequest.SessionToken)
	if err != nil {
		fmt.Printf("[DEBUG][partyRespond] session validation failed: %v\n", err)
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	fmt.Printf("[DEBUG][partyRespond] session validated inviteeId=%s inviteId=%s\n", inviteeID, respondRequest.InviteID)

	mmDB, err := server.GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][partyRespond] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	invite, found, err := getPendingInviteForPlayer(request.Context(), mmDB, respondRequest.InviteID, inviteeID)
	if err != nil {
		fmt.Printf("[DEBUG][partyRespond] getPendingInviteForPlayer failed inviteId=%s inviteeId=%s err=%v\n", respondRequest.InviteID, inviteeID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !found {
		fmt.Printf("[DEBUG][partyRespond] pending invite not found inviteId=%s inviteeId=%s\n", respondRequest.InviteID, inviteeID)
		response.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "pending invite not found",
		})
		return
	}

	nextStatus := "rejected"
	if respondRequest.Accept {
		nextStatus = "accepted"
		fmt.Printf("[DEBUG][partyRespond] accept path move player inviteeId=%s targetPartyId=%s\n", inviteeID, invite.PartyID)
		if err := movePlayerToParty(request.Context(), mmDB, inviteeID, invite.PartyID); err != nil {
			fmt.Printf("[DEBUG][partyRespond] movePlayerToParty failed inviteeId=%s targetPartyId=%s err=%v\n", inviteeID, invite.PartyID, err)
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	updateInviteQuery := `UPDATE party_invites
		SET status = $1, seen_by_inviter = TRUE, seen_by_invitee = TRUE
		WHERE invite_id = $2`
	if _, err := server.SubmitExec(request.Context(), mmDB.DB, updateInviteQuery, nextStatus, respondRequest.InviteID); err != nil {
		fmt.Printf("[DEBUG][partyRespond] invite update failed inviteId=%s status=%s err=%v\n", respondRequest.InviteID, nextStatus, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	deletedSeenInviteRows, err := deleteSeenInviteByID(request.Context(), mmDB, respondRequest.InviteID)
	if err != nil {
		fmt.Printf("[DEBUG][partyRespond] invite cleanup failed inviteId=%s err=%v\n", respondRequest.InviteID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if deletedSeenInviteRows > 0 {
		fmt.Printf("[DEBUG][partyRespond] invite removed after both-seen inviteId=%s removed=%d\n", respondRequest.InviteID, deletedSeenInviteRows)
	}
	fmt.Printf("[DEBUG][partyRespond] invite handled inviteId=%s inviteeId=%s status=%s\n", respondRequest.InviteID, inviteeID, nextStatus)

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":       "ok",
		"inviteId":     respondRequest.InviteID,
		"inviteStatus": nextStatus,
		"partyId":      invite.PartyID,
		"playerId":     inviteeID,
		"accepted":     respondRequest.Accept,
	})
}

// handlePartyLeave removes a player from their current party.
// POST /matchmaking/party/leave
// Request: {"sessionToken": "string"}
// Response: {"status": "ok", "message": "..."}
func (api *MatchmakingAPI) handlePartyLeave(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][partyLeave] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][partyLeave] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var leaveRequest struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.NewDecoder(request.Body).Decode(&leaveRequest); err != nil {
		fmt.Printf("[DEBUG][partyLeave] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if leaveRequest.SessionToken == "" {
		fmt.Printf("[DEBUG][partyLeave] rejected request: missing sessionToken\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	playerID, err := server.GetPlayerIDFromSession(leaveRequest.SessionToken)
	if err != nil {
		fmt.Printf("[DEBUG][partyLeave] session validation failed: %v\n", err)
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	fmt.Printf("[DEBUG][partyLeave] session validated playerId=%s\n", playerID)

	mmDB, err := server.GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][partyLeave] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	partyID, err := findPartyForPlayer(request.Context(), mmDB, playerID)
	if err != nil {
		fmt.Printf("[DEBUG][partyLeave] findPartyForPlayer failed playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if partyID == "" {
		fmt.Printf("[DEBUG][partyLeave] player not in party playerId=%s\n", playerID)
		response.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "player is not in a party",
		})
		return
	}

	if err := leaveParty(request.Context(), mmDB, playerID, partyID); err != nil {
		fmt.Printf("[DEBUG][partyLeave] leaveParty failed playerId=%s partyId=%s err=%v\n", playerID, partyID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("[DEBUG][partyLeave] player left party playerId=%s partyId=%s\n", playerID, partyID)

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":   "ok",
		"message":  "left party",
		"playerId": playerID,
		"partyId":  partyID,
	})
}

func (api *MatchmakingAPI) handlePartyInvites(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][partyInvites] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][partyInvites] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var invitesRequest struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.NewDecoder(request.Body).Decode(&invitesRequest); err != nil {
		fmt.Printf("[DEBUG][partyInvites] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if invitesRequest.SessionToken == "" {
		fmt.Printf("[DEBUG][partyInvites] rejected request: missing sessionToken\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	playerID, err := server.GetPlayerIDFromSession(invitesRequest.SessionToken)
	if err != nil {
		fmt.Printf("[DEBUG][partyInvites] session validation failed: %v\n", err)
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	fmt.Printf("[DEBUG][partyInvites] session validated playerId=%s\n", playerID)

	mmDB, err := server.GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][partyInvites] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	removedFullySeenInvites, err := deleteFullySeenNonPendingInvitesForPlayer(request.Context(), mmDB, playerID)
	if err != nil {
		fmt.Printf("[DEBUG][partyInvites] cleanup fully-seen invites failed playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if removedFullySeenInvites > 0 {
		fmt.Printf("[DEBUG][partyInvites] removed fully-seen invites playerId=%s removed=%d\n", playerID, removedFullySeenInvites)
	}

	inbound, err := listInvitesForPlayer(request.Context(), mmDB, playerID, true)
	if err != nil {
		fmt.Printf("[DEBUG][partyInvites] inbound invite lookup failed playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	outbound, err := listInvitesForPlayer(request.Context(), mmDB, playerID, false)
	if err != nil {
		fmt.Printf("[DEBUG][partyInvites] outbound invite lookup failed playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("[DEBUG][partyInvites] request succeeded playerId=%s inbound=%d outbound=%d\n", playerID, len(inbound), len(outbound))

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":          "ok",
		"playerId":        playerID,
		"inboundInvites":  inbound,
		"outboundInvites": outbound,
	})
}

// handlePartyStatus returns the current party status including members and faction.
// GET /matchmaking/party/status?sessionToken=X
// POST /matchmaking/party/status
// Request: {"sessionToken": "string"}
// Response: {"partyId": "uuid", "leaderId": "uuid", "partyFaction": 0, "allMembersHaveActiveCharacter": bool, "members": [...]}
func (api *MatchmakingAPI) handlePartyStatus(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][partyStatus] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost && request.Method != http.MethodGet {
		fmt.Printf("[DEBUG][partyStatus] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	sessionToken := ""
	if request.Method == http.MethodGet {
		sessionToken = request.URL.Query().Get("sessionToken")
	} else {
		var statusRequest struct {
			SessionToken string `json:"sessionToken"`
		}
		if err := json.NewDecoder(request.Body).Decode(&statusRequest); err != nil {
			fmt.Printf("[DEBUG][partyStatus] decode failed: %v\n", err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		sessionToken = statusRequest.SessionToken
	}

	if sessionToken == "" {
		fmt.Printf("[DEBUG][partyStatus] rejected request: missing sessionToken\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	playerID, err := server.GetPlayerIDFromSession(sessionToken)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] session validation failed: %v\n", err)
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	fmt.Printf("[DEBUG][partyStatus] session validated playerId=%s\n", playerID)

	mmDB, err := server.GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	removedExpiredInvites, err := deleteExpiredPendingInvitesForPlayer(request.Context(), mmDB, playerID)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] failed deleting expired invites playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if removedExpiredInvites > 0 {
		fmt.Printf("[DEBUG][partyStatus] deleted expired invites playerId=%s removed=%d\n", playerID, removedExpiredInvites)
	}

	removedFullySeenInvites, err := deleteFullySeenNonPendingInvitesForPlayer(request.Context(), mmDB, playerID)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] failed deleting fully-seen invites playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if removedFullySeenInvites > 0 {
		fmt.Printf("[DEBUG][partyStatus] deleted fully-seen invites playerId=%s removed=%d\n", playerID, removedFullySeenInvites)
	}

	inbound, err := listInvitesForPlayer(request.Context(), mmDB, playerID, true)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] inbound invite lookup failed playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	outbound, err := listInvitesForPlayer(request.Context(), mmDB, playerID, false)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] outbound invite lookup failed playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	partyID, err := findPartyForPlayer(request.Context(), mmDB, playerID)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] findPartyForPlayer failed playerId=%s err=%v\n", playerID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if partyID == "" {
		fmt.Printf("[DEBUG][partyStatus] player not in party playerId=%s inbound=%d outbound=%d\n", playerID, len(inbound), len(outbound))
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":          "ok",
			"playerId":        playerID,
			"inParty":         false,
			"inboundInvites":  inbound,
			"outboundInvites": outbound,
		})
		return
	}

	members, err := listPartyMembers(request.Context(), mmDB, partyID)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] listPartyMembers failed partyId=%s err=%v\n", partyID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	playerDB, err := server.GetDatabase(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] GetDatabase failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	memberPayload := make([]map[string]string, 0, len(members))
	for _, memberID := range members {
		username := memberID
		nameRows, queryErr := server.SubmitQuery(request.Context(), playerDB.DB, "SELECT account_name FROM players WHERE id = $1", memberID)
		if queryErr != nil {
			fmt.Printf("[DEBUG][partyStatus] member name lookup failed memberId=%s err=%v\n", memberID, queryErr)
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		if nameRows.Next() {
			_ = nameRows.Scan(&username)
		}
		if closeErr := nameRows.Close(); closeErr != nil {
			fmt.Printf("[DEBUG][partyStatus] member name rows close failed memberId=%s err=%v\n", memberID, closeErr)
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		memberPayload = append(memberPayload, map[string]string{
			"playerId": memberID,
			"username": username,
		})
	}

	primaryPlayerID, err := getPartyPrimaryPlayer(request.Context(), mmDB, partyID)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] getPartyPrimaryPlayer failed partyId=%s err=%v\n", partyID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	partyFaction, err := getPartyFaction(request.Context(), mmDB, partyID)
	if err != nil {
		fmt.Printf("[DEBUG][partyStatus] getPartyFaction failed partyId=%s err=%v\n", partyID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	allMembersHaveActiveCharacter := false
	{
		rows, queryErr := server.SubmitQuery(request.Context(), mmDB.DB,
			"SELECT COUNT(*) FROM party_players WHERE party_id = $1 AND active_character_id IS NULL", partyID)
		if queryErr != nil {
			fmt.Printf("[DEBUG][partyStatus] allMembersHaveActiveCharacter query failed partyId=%s err=%v\n", partyID, queryErr)
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		var missingCount int
		if rows.Next() {
			if scanErr := rows.Scan(&missingCount); scanErr != nil {
				_ = rows.Close()
				fmt.Printf("[DEBUG][partyStatus] allMembersHaveActiveCharacter scan failed partyId=%s err=%v\n", partyID, scanErr)
				response.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		if closeErr := rows.Close(); closeErr != nil {
			fmt.Printf("[DEBUG][partyStatus] allMembersHaveActiveCharacter rows close failed partyId=%s err=%v\n", partyID, closeErr)
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		allMembersHaveActiveCharacter = missingCount == 0
	}

	fmt.Printf("[DEBUG][partyStatus] request succeeded playerId=%s partyId=%s members=%d inbound=%d outbound=%d allMembersHaveActiveCharacter=%t\n", playerID, partyID, len(memberPayload), len(inbound), len(outbound), allMembersHaveActiveCharacter)

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":                        "ok",
		"inParty":                       true,
		"partyId":                       partyID,
		"partyFaction":                  partyFaction,
		"playerId":                      playerID,
		"primaryPlayerId":               primaryPlayerID,
		"allMembersHaveActiveCharacter": allMembersHaveActiveCharacter,
		"members":                       memberPayload,
		"inboundInvites":                inbound,
		"outboundInvites":               outbound,
	})
}

func playerExists(ctx context.Context, playerDB *server.Database, playerID string) (bool, error) {
	rows, err := server.SubmitQuery(ctx, playerDB.DB, "SELECT 1 FROM players WHERE id = $1", playerID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), nil
}

func ensurePlayerParty(ctx context.Context, mmDB *server.Database, playerID string) (string, error) {
	partyID, err := findPartyForPlayer(ctx, mmDB, playerID)
	if err != nil {
		return "", err
	}
	if partyID != "" {
		return partyID, nil
	}

	partyID = "party-" + uuid.NewString()
	activeCharacterID := any(nil)
	activeCharacter, found, err := server.GetActiveCharacterForPlayer(ctx, playerID)
	if err != nil {
		return "", err
	}

	playerFaction, err := getPlayerFaction(ctx, playerID)
	if err != nil {
		return "", err
	}
	if found {
		playerFaction = activeCharacter.Faction
		activeCharacterID = activeCharacter.ID
	}

	createPartyQuery := "INSERT INTO parties (party_id, active_faction, faction, primary_player_id) VALUES ($1, $2, $3, $4)"
	if _, err := server.SubmitExec(ctx, mmDB.DB, createPartyQuery, partyID, playerFaction, playerFaction, playerID); err != nil {
		return "", err
	}

	insertMemberQuery := "INSERT INTO party_players (party_id, player_id, active_character_id) VALUES ($1, $2, $3)"
	if _, err := server.SubmitExec(ctx, mmDB.DB, insertMemberQuery, partyID, playerID, activeCharacterID); err != nil {
		return "", err
	}

	return partyID, nil
}

func findPartyForPlayer(ctx context.Context, mmDB *server.Database, playerID string) (string, error) {
	rows, err := server.SubmitQuery(ctx, mmDB.DB, "SELECT party_id FROM party_players WHERE player_id = $1 LIMIT 1", playerID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	if !rows.Next() {
		return "", nil
	}

	var partyID string
	if err := rows.Scan(&partyID); err != nil {
		return "", err
	}

	return partyID, nil
}

func hasPendingInvite(ctx context.Context, mmDB *server.Database, partyID string, toPlayerID string) (bool, error) {
	query := `SELECT 1
		FROM party_invites
		WHERE party_id = $1
			AND to_player_id = $2
			AND status = 'pending'
			AND expires_at > $3
		LIMIT 1`
	rows, err := server.SubmitQuery(ctx, mmDB.DB, query, partyID, toPlayerID, time.Now().UTC())
	if err != nil {
		return false, err
	}
	defer rows.Close()

	return rows.Next(), nil
}

func getPartyFaction(ctx context.Context, mmDB *server.Database, partyID string) (int, error) {
	rows, err := server.SubmitQuery(ctx, mmDB.DB, "SELECT faction FROM parties WHERE party_id = $1 LIMIT 1", partyID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, fmt.Errorf("party not found")
	}

	var faction int
	if err := rows.Scan(&faction); err != nil {
		return 0, err
	}

	return faction, nil
}

func getPlayerFaction(ctx context.Context, playerID string) (int, error) {
	activeCharacter, found, err := server.GetActiveCharacterForPlayer(ctx, playerID)
	if err != nil {
		return 0, err
	}
	if found {
		return activeCharacter.Faction, nil
	}

	playerDB, err := server.GetDatabase(ctx)
	if err != nil {
		return 0, err
	}

	rows, err := server.SubmitQuery(ctx, playerDB.DB, "SELECT faction FROM characters WHERE player_id = $1 ORDER BY character_id LIMIT 1", playerID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, nil
	}

	var faction int
	if err := rows.Scan(&faction); err != nil {
		return 0, err
	}

	return faction, nil
}

func getPendingInviteForPlayer(ctx context.Context, mmDB *server.Database, inviteID string, toPlayerID string) (pendingInvite, bool, error) {
	query := `SELECT invite_id, party_id, from_player_id, to_player_id, expires_at
		FROM party_invites
		WHERE invite_id = $1
			AND to_player_id = $2
			AND status = 'pending'
		LIMIT 1`
	rows, err := server.SubmitQuery(ctx, mmDB.DB, query, inviteID, toPlayerID)
	if err != nil {
		return pendingInvite{}, false, err
	}
	defer rows.Close()

	if !rows.Next() {
		return pendingInvite{}, false, nil
	}

	invite := pendingInvite{}
	if err := rows.Scan(&invite.InviteID, &invite.PartyID, &invite.FromPlayerID, &invite.ToPlayerID, &invite.ExpiresAt); err != nil {
		return pendingInvite{}, false, err
	}

	if invite.ExpiresAt.Before(time.Now().UTC()) {
		if _, err := server.SubmitExec(ctx, mmDB.DB, "UPDATE party_invites SET status = 'expired' WHERE invite_id = $1", inviteID); err != nil {
			return pendingInvite{}, false, err
		}
		return pendingInvite{}, false, nil
	}

	return invite, true, nil
}

func movePlayerToParty(ctx context.Context, mmDB *server.Database, playerID string, targetPartyID string) error {
	currentPartyID, err := findPartyForPlayer(ctx, mmDB, playerID)
	if err != nil {
		return err
	}
	if currentPartyID == targetPartyID {
		return nil
	}

	if currentPartyID != "" {
		if err := leaveParty(ctx, mmDB, playerID, currentPartyID); err != nil {
			return err
		}
	}

	activeCharacterID := any(nil)
	activeCharacter, found, err := server.GetActiveCharacterForPlayer(ctx, playerID)
	if err != nil {
		return err
	}
	if found {
		activeCharacterID = activeCharacter.ID
	}

	insertMemberQuery := "INSERT INTO party_players (party_id, player_id, active_character_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING"
	if _, err := server.SubmitExec(ctx, mmDB.DB, insertMemberQuery, targetPartyID, playerID, activeCharacterID); err != nil {
		return err
	}

	return nil
}

func leaveParty(ctx context.Context, mmDB *server.Database, playerID string, partyID string) error {
	deleteMemberQuery := "DELETE FROM party_players WHERE party_id = $1 AND player_id = $2"
	if _, err := server.SubmitExec(ctx, mmDB.DB, deleteMemberQuery, partyID, playerID); err != nil {
		return err
	}

	remaining, err := listPartyMembers(ctx, mmDB, partyID)
	if err != nil {
		return err
	}
	if len(remaining) == 0 {
		if _, err := server.SubmitExec(ctx, mmDB.DB, "DELETE FROM parties WHERE party_id = $1", partyID); err != nil {
			return err
		}
		return nil
	}

	primaryID, err := getPartyPrimaryPlayer(ctx, mmDB, partyID)
	if err != nil {
		return err
	}

	if primaryID == playerID {
		sort.Strings(remaining)
		nextLeader := remaining[0]
		if _, err := server.SubmitExec(ctx, mmDB.DB, "UPDATE parties SET primary_player_id = $1 WHERE party_id = $2", nextLeader, partyID); err != nil {
			return err
		}
	}

	return nil
}

func listPartyMembers(ctx context.Context, mmDB *server.Database, partyID string) ([]string, error) {
	rows, err := server.SubmitQuery(ctx, mmDB.DB, "SELECT player_id FROM party_players WHERE party_id = $1", partyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	members := []string{}
	for rows.Next() {
		var playerID string
		if err := rows.Scan(&playerID); err != nil {
			return nil, err
		}
		members = append(members, playerID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return members, nil
}

func getPartyPrimaryPlayer(ctx context.Context, mmDB *server.Database, partyID string) (string, error) {
	rows, err := server.SubmitQuery(ctx, mmDB.DB, "SELECT primary_player_id FROM parties WHERE party_id = $1", partyID)
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

func listInvitesForPlayer(ctx context.Context, mmDB *server.Database, playerID string, inbound bool) ([]partyInviteRecord, error) {
	column := "to_player_id"
	if !inbound {
		column = "from_player_id"
	}

	query := fmt.Sprintf(`SELECT invite_id, party_id, from_player_id, to_player_id, status, created_at, expires_at, seen_by_inviter, seen_by_invitee
		FROM party_invites
		WHERE %s = $1
		ORDER BY created_at DESC`, column)

	rows, err := server.SubmitQuery(ctx, mmDB.DB, query, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now().UTC()
	invites := []partyInviteRecord{}
	for rows.Next() {
		var record partyInviteRecord
		var createdAt time.Time
		var expiresAt time.Time
		if err := rows.Scan(&record.InviteID, &record.PartyID, &record.FromPlayerID, &record.ToPlayerID, &record.Status, &createdAt, &expiresAt, &record.SeenByInviter, &record.SeenByInvitee); err != nil {
			return nil, err
		}

		record.CreatedAt = createdAt.Format(time.RFC3339)
		record.ExpiresAt = expiresAt.Format(time.RFC3339)
		record.Expired = expiresAt.Before(now)
		invites = append(invites, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return invites, nil
}

func deleteExpiredPendingInvitesForPlayer(ctx context.Context, mmDB *server.Database, playerID string) (int64, error) {
	query := `DELETE FROM party_invites
		WHERE status = 'pending'
			AND expires_at <= $1
			AND (to_player_id = $2 OR from_player_id = $2)`
	result, err := server.SubmitExec(ctx, mmDB.DB, query, time.Now().UTC(), playerID)
	if err != nil {
		return 0, err
	}

	removed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	return removed, nil
}

func deleteFullySeenNonPendingInvitesForPlayer(ctx context.Context, mmDB *server.Database, playerID string) (int64, error) {
	query := `DELETE FROM party_invites
		WHERE status <> 'pending'
			AND seen_by_inviter = TRUE
			AND seen_by_invitee = TRUE
			AND (to_player_id = $1 OR from_player_id = $1)`
	result, err := server.SubmitExec(ctx, mmDB.DB, query, playerID)
	if err != nil {
		return 0, err
	}

	removed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	return removed, nil
}

func deleteSeenInviteByID(ctx context.Context, mmDB *server.Database, inviteID string) (int64, error) {
	query := `DELETE FROM party_invites
		WHERE invite_id = $1
			AND status <> 'pending'
			AND seen_by_inviter = TRUE
			AND seen_by_invitee = TRUE`
	result, err := server.SubmitExec(ctx, mmDB.DB, query, inviteID)
	if err != nil {
		return 0, err
	}

	removed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	return removed, nil
}

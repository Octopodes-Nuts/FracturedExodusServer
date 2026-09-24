package matchmaking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	server "fracturedexodusserver/server"
	ws "fracturedexodusserver/server/ws"
)

// WSHandlers returns the WebSocket handler map for every matchmaking.* message type. Each
// handler is a thin decode -> call the same core function the HTTP handler calls -> encode
// wrapper; none of them reimplement business logic that already exists as a plain function
// (resolveQueueContext/enqueueGroup/movePlayerToParty/leaveParty/buildPartyStatusPayload, the
// matchmaking_db.go DB-layer helpers, etc).
func (api *MatchmakingAPI) WSHandlers() map[string]ws.HandlerFunc {
	return map[string]ws.HandlerFunc{
		"matchmaking.queue":         api.wsQueue,
		"matchmaking.cancel":        api.wsCancel,
		"matchmaking.join":          api.wsJoin,
		"matchmaking.joined":        api.wsJoined,
		"matchmaking.left":          api.wsLeft,
		"matchmaking.heartbeat":     api.wsHeartbeat,
		"matchmaking.status":        api.wsStatus,
		"matchmaking.party.invite":  api.wsPartyInvite,
		"matchmaking.party.respond": api.wsPartyRespond,
		"matchmaking.party.leave":   api.wsPartyLeave,
		"matchmaking.party.invites": api.wsPartyInvites,
		"matchmaking.party.status":  api.wsPartyStatus,
	}
}

// resolveQueueContextForConn resolves the QueueContext for c's bound session token, mapping
// errInvalidSessionToken onto a WS-friendly "unauthenticated" error message (parity with the
// {"ok": false, "error": "unauthenticated"} shape used elsewhere).
func (api *MatchmakingAPI) resolveQueueContextForConn(ctx context.Context, c *ws.Conn) (QueueContext, error) {
	api.mu.Lock()
	resolver := api.resolveQueueContext
	api.mu.Unlock()

	queueContext, err := resolver(ctx, c.SessionToken())
	if err != nil {
		if errors.Is(err, errInvalidSessionToken) {
			return QueueContext{}, fmt.Errorf("unauthenticated")
		}
		return QueueContext{}, err
	}
	return queueContext, nil
}

// mirrors handleQueue (POST /matchmaking/queue).
func (api *MatchmakingAPI) wsQueue(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	queueContext, err := api.resolveQueueContextForConn(ctx, c)
	if err != nil {
		return nil, err
	}

	partyID, ticketIDs, playerTicketMap, err := api.enqueueGroup(ctx, queueContext.PartyID, queueContext.Members)
	if err != nil {
		return nil, err
	}

	ticketAssignments := make([]map[string]string, 0, len(queueContext.Members))
	for _, member := range queueContext.Members {
		ticketAssignments = append(ticketAssignments, map[string]string{
			"playerId": member.PlayerID,
			"username": member.Username,
			"ticketId": playerTicketMap[member.PlayerID],
		})
	}

	// Push points: notify every queued member they're now searching, mirroring the shape
	// handleStatus returns for an unmatched ticket.
	if api.hub != nil {
		for _, member := range queueContext.Members {
			api.hub.SendTo(member.PlayerID, ws.OutboundMessage{
				Type: "matchmaking.status",
				OK:   true,
				Payload: map[string]any{
					"status":   "searching",
					"ticketId": playerTicketMap[member.PlayerID],
					"partyId":  partyID,
					"playerId": member.PlayerID,
					"username": member.Username,
					"region":   api.region,
					"port":     "",
				},
			})
		}
	}

	return map[string]any{
		"status":            "queued",
		"partyId":           partyID,
		"ticketId":          playerTicketMap[queueContext.RequesterPlayerID],
		"ticketIds":         ticketIDs,
		"ticketAssignments": ticketAssignments,
	}, nil
}

// mirrors handleCancel (POST /matchmaking/cancel?ticketId=X). The HTTP endpoint has no auth
// check at all today (any caller holding a ticketId can cancel it); this WS twin preserves
// that lack of *ownership* checking — it does not verify the ticket belongs to c's bound
// player. The connection itself must still be bound (matchmaking.cancel is not in the
// unbound-allowed list), since that gate is enforced uniformly by the router for every message
// type other than the three login/auth ones.
func (api *MatchmakingAPI) wsCancel(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	var req struct {
		TicketID string `json:"ticketId"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}
	if req.TicketID == "" {
		return nil, fmt.Errorf("ticketId is required")
	}

	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		return nil, err
	}

	if err := updateTicketStatuses(ctx, mmDB, []string{req.TicketID}, "left", nil); err != nil {
		return nil, err
	}

	api.mu.Lock()
	if ticket, ok := api.tickets[req.TicketID]; ok {
		ticket.status = "cancelled"
	}
	api.waiting = removeTicket(api.waiting, req.TicketID)
	api.mu.Unlock()

	return map[string]any{
		"status":   "cancelled",
		"ticketId": req.TicketID,
	}, nil
}

// mirrors handleJoin (POST /matchmaking/join).
func (api *MatchmakingAPI) wsJoin(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	var req struct {
		TicketID string `json:"ticketId"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}
	if req.TicketID == "" {
		return nil, fmt.Errorf("ticketId is required")
	}

	ticketPayload, found, err := loadTicketStatusByIDFromDB(ctx, req.TicketID)
	if err != nil || !found {
		api.mu.Lock()
		ticket, ok := api.tickets[req.TicketID]
		api.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("ticket not found")
		}
		ticketPayload = ticketStatus{
			PlayerID: ticket.playerID,
			Username: ticket.player.Username,
			TicketID: req.TicketID,
			Status:   ticket.status,
			PartyID:  ticket.partyID,
			Instance: ticket.instance,
			Error:    ticket.error,
		}
	}

	return map[string]any{
		"status":   ticketPayload.Status,
		"ticketId": ticketPayload.TicketID,
		"partyId":  ticketPayload.PartyID,
		"instance": ticketPayload.Instance,
		"error":    ticketPayload.Error,
	}, nil
}

// mirrors handleJoined (POST /matchmaking/joined).
func (api *MatchmakingAPI) wsJoined(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	var req struct {
		TicketID string `json:"ticketId"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}
	if req.TicketID == "" {
		return nil, fmt.Errorf("ticketId is required")
	}

	queueContext, err := api.resolveQueueContextForConn(ctx, c)
	if err != nil {
		return nil, err
	}

	memberSet := make(map[string]struct{}, len(queueContext.Members))
	for _, member := range queueContext.Members {
		memberSet[member.PlayerID] = struct{}{}
	}

	ticketPayload, found, err := loadTicketStatusByIDFromDB(ctx, req.TicketID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("ticket not found")
	}
	if _, ok := memberSet[ticketPayload.PlayerID]; !ok {
		return nil, fmt.Errorf("forbidden")
	}

	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		return nil, err
	}

	updated, err := markTicketsInMatchByTicketID(ctx, mmDB, req.TicketID)
	if err != nil {
		return nil, err
	}

	api.mu.Lock()
	if ticket, ok := api.tickets[req.TicketID]; ok {
		ticket.status = "in_match"
	}
	api.mu.Unlock()

	return map[string]any{
		"status":         "in_match",
		"ticketId":       req.TicketID,
		"partyId":        ticketPayload.PartyID,
		"updatedTickets": updated,
	}, nil
}

// mirrors handleLeft (POST /matchmaking/left).
func (api *MatchmakingAPI) wsLeft(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	queueContext, err := api.resolveQueueContextForConn(ctx, c)
	if err != nil {
		return nil, err
	}

	memberIDs := make([]string, 0, len(queueContext.Members))
	for _, member := range queueContext.Members {
		memberIDs = append(memberIDs, member.PlayerID)
	}

	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		return nil, err
	}

	updated, err := markPlayersLeft(ctx, mmDB, memberIDs)
	if err != nil {
		return nil, err
	}

	api.mu.Lock()
	for _, ticket := range api.tickets {
		if ticket == nil {
			continue
		}
		for _, playerID := range memberIDs {
			if ticket.playerID == playerID {
				ticket.status = "left"
				ticket.instance = nil
				ticket.error = ""
				break
			}
		}
	}
	api.mu.Unlock()

	return map[string]any{
		"status":         "not_queued",
		"partyId":        queueContext.PartyID,
		"updatedTickets": updated,
	}, nil
}

// mirrors handleHeartbeat (POST /matchmaking/heartbeat).
func (api *MatchmakingAPI) wsHeartbeat(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	queueContext, err := api.resolveQueueContextForConn(ctx, c)
	if err != nil {
		return nil, err
	}

	memberIDs := make([]string, 0, len(queueContext.Members))
	for _, member := range queueContext.Members {
		memberIDs = append(memberIDs, member.PlayerID)
	}

	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		return nil, err
	}

	updated, err := touchPlayersHeartbeat(ctx, mmDB, memberIDs)
	if err != nil {
		return nil, err
	}
	if updated == 0 {
		return nil, fmt.Errorf("no active match found")
	}

	return map[string]any{
		"status":         "ok",
		"partyId":        queueContext.PartyID,
		"updatedTickets": updated,
		"heartbeatAt":    time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// mirrors handleStatus (GET/POST /matchmaking/status), both the single-ticket and
// all-party-members code paths. ticketId is optional in the payload, same as today.
func (api *MatchmakingAPI) wsStatus(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	var req struct {
		TicketID string `json:"ticketId"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid request body")
		}
	}

	queueContext, err := api.resolveQueueContextForConn(ctx, c)
	if err != nil {
		return nil, err
	}

	memberSet := make(map[string]struct{}, len(queueContext.Members))
	for _, member := range queueContext.Members {
		memberSet[member.PlayerID] = struct{}{}
	}

	getTicketStatus := func(ticketID string) (ticketStatus, bool, error) {
		dbStatus, found, dbErr := loadTicketStatusByIDFromDB(ctx, ticketID)
		if dbErr == nil && found {
			if _, allowed := memberSet[dbStatus.PlayerID]; !allowed {
				return ticketStatus{}, false, nil
			}
			api.mu.Lock()
			if ticket, ok := api.tickets[ticketID]; ok {
				if ticket.instance != nil {
					dbStatus.Instance = ticket.instance
				}
				if ticket.error != "" {
					dbStatus.Error = ticket.error
				}
			}
			api.mu.Unlock()
			return dbStatus, true, nil
		}

		api.mu.Lock()
		ticket, ok := api.tickets[ticketID]
		api.mu.Unlock()
		if !ok {
			return ticketStatus{}, false, nil
		}
		if _, allowed := memberSet[ticket.playerID]; !allowed {
			return ticketStatus{}, false, nil
		}
		return ticketStatus{
			PlayerID: ticket.playerID,
			Username: ticket.player.Username,
			TicketID: ticketID,
			Status:   ticket.status,
			PartyID:  ticket.partyID,
			Instance: ticket.instance,
			Error:    ticket.error,
		}, true, nil
	}

	if req.TicketID != "" {
		ticketPayload, ok, ticketErr := getTicketStatus(req.TicketID)
		if ticketErr != nil {
			return nil, ticketErr
		}
		if !ok {
			return nil, fmt.Errorf("ticket not found")
		}
		matchedPort := ""
		if (ticketPayload.Status == "matched" || ticketPayload.Status == "in_match") && ticketPayload.Instance != nil {
			matchedPort = ticketPayload.Instance.Port
		}

		return map[string]any{
			"status":   ticketPayload.Status,
			"ticketId": ticketPayload.TicketID,
			"partyId":  ticketPayload.PartyID,
			"playerId": ticketPayload.PlayerID,
			"username": ticketPayload.Username,
			"region":   api.region,
			"port":     matchedPort,
			"error":    ticketPayload.Error,
		}, nil
	}

	collectedStatuses, dbErr := loadLatestTicketStatusesFromDB(ctx, queueContext)
	if dbErr != nil {
		api.mu.Lock()
		collectedStatuses = make([]ticketStatus, 0, len(api.tickets))
		for ticketID, ticket := range api.tickets {
			if _, allowed := memberSet[ticket.playerID]; !allowed {
				continue
			}
			collectedStatuses = append(collectedStatuses, ticketStatus{
				PlayerID: ticket.playerID,
				Username: ticket.player.Username,
				TicketID: ticketID,
				Status:   ticket.status,
				PartyID:  ticket.partyID,
				Instance: ticket.instance,
				Error:    ticket.error,
			})
		}
		api.mu.Unlock()
	} else {
		api.mu.Lock()
		for i := range collectedStatuses {
			if ticket, ok := api.tickets[collectedStatuses[i].TicketID]; ok {
				if ticket.instance != nil {
					collectedStatuses[i].Instance = ticket.instance
				}
				if ticket.error != "" {
					collectedStatuses[i].Error = ticket.error
				}
			}
		}
		api.mu.Unlock()
	}

	sort.Slice(collectedStatuses, func(i, j int) bool {
		if collectedStatuses[i].PlayerID == collectedStatuses[j].PlayerID {
			return collectedStatuses[i].TicketID < collectedStatuses[j].TicketID
		}
		return collectedStatuses[i].PlayerID < collectedStatuses[j].PlayerID
	})

	overallStatus := "not_queued"
	for _, status := range collectedStatuses {
		switch status.Status {
		case "error":
			overallStatus = "error"
		case "in_match":
			if overallStatus != "error" {
				overallStatus = "in_match"
			}
		case "matched":
			if overallStatus != "error" && overallStatus != "in_match" {
				overallStatus = "matched"
			}
		case "searching":
			if overallStatus != "error" && overallStatus != "matched" && overallStatus != "in_match" {
				overallStatus = "searching"
			}
		}
	}

	ownTicketID := ""
	matchedPort := ""
	for _, status := range collectedStatuses {
		if status.PlayerID == queueContext.RequesterPlayerID {
			ownTicketID = status.TicketID
			if (status.Status == "matched" || status.Status == "in_match") && status.Instance != nil {
				matchedPort = status.Instance.Port
			}
			break
		}
	}

	if matchedPort == "" && overallStatus == "matched" {
		for _, status := range collectedStatuses {
			if (status.Status == "matched" || status.Status == "in_match") && status.Instance != nil {
				matchedPort = status.Instance.Port
				break
			}
		}
	}

	return map[string]any{
		"status":   overallStatus,
		"ticketId": ownTicketID,
		"port":     matchedPort,
		"partyId":  queueContext.PartyID,
		"playerId": queueContext.RequesterPlayerID,
		"region":   api.region,
		"tickets":  collectedStatuses,
		"members":  queueContext.Members,
	}, nil
}

// mirrors handlePartyInvite (POST /matchmaking/party/invite), using c's bound player identity
// instead of resolving it from a payload sessionToken.
func (api *MatchmakingAPI) wsPartyInvite(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	var req struct {
		PlayerID string `json:"playerId"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}
	if req.PlayerID == "" {
		return nil, fmt.Errorf("playerId is required")
	}

	inviterID := c.PlayerID()
	if inviterID == req.PlayerID {
		return nil, fmt.Errorf("cannot invite yourself")
	}

	playerDB, err := server.GetDatabase(ctx)
	if err != nil {
		return nil, err
	}
	if exists, err := playerExists(ctx, playerDB, req.PlayerID); err != nil {
		return nil, err
	} else if !exists {
		return nil, fmt.Errorf("player not found")
	}

	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		return nil, err
	}

	partyID, err := ensurePlayerParty(ctx, mmDB, inviterID)
	if err != nil {
		return nil, err
	}

	targetPartyID, err := findPartyForPlayer(ctx, mmDB, req.PlayerID)
	if err != nil {
		return nil, err
	}
	if targetPartyID != "" && targetPartyID == partyID {
		return nil, fmt.Errorf("player is already in your party")
	}

	pendingInviteExists, err := hasPendingInvite(ctx, mmDB, partyID, req.PlayerID)
	if err != nil {
		return nil, err
	}
	if pendingInviteExists {
		return nil, fmt.Errorf("invite already pending")
	}

	if _, err := server.SubmitExec(ctx, mmDB.DB,
		"DELETE FROM party_invites WHERE party_id = $1 AND to_player_id = $2 AND status = 'pending' AND expires_at <= $3",
		partyID, req.PlayerID, time.Now().UTC()); err != nil {
		return nil, err
	}

	inviteID := "invite-" + uuid.NewString()
	now := time.Now().UTC()
	expiresAt := now.Add(5 * time.Minute)
	insertInviteQuery := `INSERT INTO party_invites
		(invite_id, party_id, from_player_id, to_player_id, status, created_at, expires_at, seen_by_inviter, seen_by_invitee)
		VALUES ($1, $2, $3, $4, 'pending', $5, $6, TRUE, FALSE)`
	if _, err := server.SubmitExec(ctx, mmDB.DB, insertInviteQuery, inviteID, partyID, inviterID, req.PlayerID, now, expiresAt); err != nil {
		return nil, err
	}

	api.pushPartyInviteCreated(ctx, mmDB, playerDB, req.PlayerID)

	return map[string]any{
		"status":          "ok",
		"inviteId":        inviteID,
		"partyId":         partyID,
		"fromPlayerId":    inviterID,
		"toPlayerId":      req.PlayerID,
		"expiresAt":       expiresAt.Format(time.RFC3339),
		"inviteStatus":    "pending",
		"inviteCreatedAt": now.Format(time.RFC3339),
	}, nil
}

// mirrors handlePartyRespond (POST /matchmaking/party/respond).
func (api *MatchmakingAPI) wsPartyRespond(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	var req struct {
		InviteID string `json:"inviteId"`
		Accept   bool   `json:"accept"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}
	if req.InviteID == "" {
		return nil, fmt.Errorf("inviteId is required")
	}

	inviteeID := c.PlayerID()

	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		return nil, err
	}

	invite, found, err := getPendingInviteForPlayer(ctx, mmDB, req.InviteID, inviteeID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("pending invite not found")
	}

	nextStatus := "rejected"
	if req.Accept {
		nextStatus = "accepted"
		oldPartyID, _ := findPartyForPlayer(ctx, mmDB, inviteeID)
		if err := movePlayerToParty(ctx, mmDB, inviteeID, invite.PartyID); err != nil {
			return nil, err
		}
		if playerDB, dbErr := server.GetDatabase(ctx); dbErr == nil {
			if oldPartyID != "" && oldPartyID != invite.PartyID {
				api.pushPartyStatusToMembers(ctx, mmDB, playerDB, oldPartyID)
			}
			api.pushPartyStatusToMembers(ctx, mmDB, playerDB, invite.PartyID)
		}
	}

	updateInviteQuery := `UPDATE party_invites
		SET status = $1, seen_by_inviter = TRUE, seen_by_invitee = TRUE
		WHERE invite_id = $2`
	if _, err := server.SubmitExec(ctx, mmDB.DB, updateInviteQuery, nextStatus, req.InviteID); err != nil {
		return nil, err
	}

	if _, err := deleteSeenInviteByID(ctx, mmDB, req.InviteID); err != nil {
		return nil, err
	}

	return map[string]any{
		"status":       "ok",
		"inviteId":     req.InviteID,
		"inviteStatus": nextStatus,
		"partyId":      invite.PartyID,
		"playerId":     inviteeID,
		"accepted":     req.Accept,
	}, nil
}

// mirrors handlePartyLeave (POST /matchmaking/party/leave).
func (api *MatchmakingAPI) wsPartyLeave(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	playerID := c.PlayerID()

	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		return nil, err
	}

	partyID, err := findPartyForPlayer(ctx, mmDB, playerID)
	if err != nil {
		return nil, err
	}
	if partyID == "" {
		return nil, fmt.Errorf("player is not in a party")
	}

	if err := leaveParty(ctx, mmDB, playerID, partyID); err != nil {
		return nil, err
	}

	if playerDB, dbErr := server.GetDatabase(ctx); dbErr == nil {
		api.pushPartyStatusToMembers(ctx, mmDB, playerDB, partyID)
	}

	return map[string]any{
		"status":   "ok",
		"message":  "left party",
		"playerId": playerID,
		"partyId":  partyID,
	}, nil
}

// mirrors handlePartyInvites (POST /matchmaking/party/invites).
func (api *MatchmakingAPI) wsPartyInvites(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	playerID := c.PlayerID()

	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		return nil, err
	}
	playerDB, err := server.GetDatabase(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := deleteFullySeenNonPendingInvitesForPlayer(ctx, mmDB, playerID); err != nil {
		return nil, err
	}

	inbound, err := listInvitesForPlayer(ctx, mmDB, playerDB, playerID, true)
	if err != nil {
		return nil, err
	}
	outbound, err := listInvitesForPlayer(ctx, mmDB, playerDB, playerID, false)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"status":          "ok",
		"playerId":        playerID,
		"inboundInvites":  inbound,
		"outboundInvites": outbound,
	}, nil
}

// mirrors handlePartyStatus (GET/POST /matchmaking/party/status), via buildPartyStatusPayload.
func (api *MatchmakingAPI) wsPartyStatus(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	playerID := c.PlayerID()

	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := deleteExpiredPendingInvitesForPlayer(ctx, mmDB, playerID); err != nil {
		return nil, err
	}
	if _, err := deleteFullySeenNonPendingInvitesForPlayer(ctx, mmDB, playerID); err != nil {
		return nil, err
	}

	playerDB, err := server.GetDatabase(ctx)
	if err != nil {
		return nil, err
	}

	return buildPartyStatusPayload(ctx, mmDB, playerDB, playerID)
}

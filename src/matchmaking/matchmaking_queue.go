package matchmaking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	server "fracturedexodusserver/src"
)

// handleQueue enqueues a player and their party for matchmaking.
// POST /matchmaking/queue
// Request: {"sessionToken": "string"}
// Response: {"status": "queued", "partyId": "uuid", "ticketId": "string", ...}
func (api *MatchmakingAPI) handleQueue(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][queue] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][queue] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var queueRequest struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.NewDecoder(request.Body).Decode(&queueRequest); err != nil {
		fmt.Printf("[DEBUG][queue] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if queueRequest.SessionToken == "" {
		fmt.Printf("[DEBUG][queue] rejected request: missing sessionToken\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	api.mu.Lock()
	resolver := api.resolveQueueContext
	api.mu.Unlock()
	queueContext, err := resolver(request.Context(), queueRequest.SessionToken)
	if err != nil {
		if errors.Is(err, errInvalidSessionToken) {
			fmt.Printf("[DEBUG][queue] session validation failed: %v\n", err)
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Printf("[DEBUG][queue] queue resolution failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	partyID, ticketIDs, playerTicketMap, err := api.enqueueGroup(request.Context(), queueContext.PartyID, queueContext.Members)
	if err != nil {
		fmt.Printf("[DEBUG][queue] enqueueGroup failed partyId=%s memberCount=%d err=%v\n", queueContext.PartyID, len(queueContext.Members), err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("[DEBUG][queue] queue successful partyId=%s ticketCount=%d requesterPlayerId=%s\n", partyID, len(ticketIDs), queueContext.RequesterPlayerID)

	ticketAssignments := make([]map[string]string, 0, len(queueContext.Members))
	for _, member := range queueContext.Members {
		ticketAssignments = append(ticketAssignments, map[string]string{
			"playerId": member.PlayerID,
			"username": member.Username,
			"ticketId": playerTicketMap[member.PlayerID],
		})
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":            "queued",
		"partyId":           partyID,
		"ticketId":          playerTicketMap[queueContext.RequesterPlayerID],
		"ticketIds":         ticketIDs,
		"ticketAssignments": ticketAssignments,
	})
}

// handleJoin allows a player to join a matched game instance.
// POST /matchmaking/join
// Request: {"ticketId": "string"}
// Response: {"status": "ok", "message": "...", "instance": {...}}
func (api *MatchmakingAPI) handleJoin(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][join] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][join] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var joinRequest struct {
		TicketID string `json:"ticketId"`
	}
	if err := json.NewDecoder(request.Body).Decode(&joinRequest); err != nil {
		fmt.Printf("[DEBUG][join] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if joinRequest.TicketID == "" {
		fmt.Printf("[DEBUG][join] rejected request: missing ticketId\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	fmt.Printf("[DEBUG][join] decoded request ticketId=%s\n", joinRequest.TicketID)

	ticketPayload, found, err := loadTicketStatusByIDFromDB(request.Context(), joinRequest.TicketID)
	if err != nil || !found {
		fmt.Printf("[DEBUG][join] ticket not found in DB ticketId=%s found=%t err=%v\n", joinRequest.TicketID, found, err)
		api.mu.Lock()
		ticket, ok := api.tickets[joinRequest.TicketID]
		api.mu.Unlock()
		if !ok {
			fmt.Printf("[DEBUG][join] ticket not in memory ticketId=%s\n", joinRequest.TicketID)
			response.WriteHeader(http.StatusNotFound)
			return
		}
		ticketPayload = ticketStatus{
			PlayerID: ticket.playerID,
			Username: ticket.player.Username,
			TicketID: joinRequest.TicketID,
			Status:   ticket.status,
			PartyID:  ticket.partyID,
			Instance: ticket.instance,
			Error:    ticket.error,
		}
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":   ticketPayload.Status,
		"ticketId": ticketPayload.TicketID,
		"partyId":  ticketPayload.PartyID,
		"instance": ticketPayload.Instance,
		"error":    ticketPayload.Error,
	})
}

// handleStatus retrieves the current status of a matchmaking ticket.
// GET /matchmaking/status?sessionToken=X&ticketId=Y
// POST /matchmaking/status
// Request: {"sessionToken": "string", "ticketId": "string"}
// Response: {"status": "searching|matched|error", "tickets": [...], "members": [...]}
func (api *MatchmakingAPI) handleStatus(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][status] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][status] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	statusRequest := struct {
		SessionToken string `json:"sessionToken"`
		TicketID     string `json:"ticketId"`
	}{}

	if request.Method == http.MethodGet {
		statusRequest.SessionToken = request.URL.Query().Get("sessionToken")
		statusRequest.TicketID = request.URL.Query().Get("ticketId")
		fmt.Printf("[DEBUG][status] decoded GET request sessionTokenSet=%t ticketId=%s\n", statusRequest.SessionToken != "", statusRequest.TicketID)
	} else {
		if err := json.NewDecoder(request.Body).Decode(&statusRequest); err != nil {
			fmt.Printf("[DEBUG][status] decode failed: %v\n", err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Printf("[DEBUG][status] decoded POST request sessionTokenSet=%t ticketId=%s\n", statusRequest.SessionToken != "", statusRequest.TicketID)
	}

	if statusRequest.SessionToken == "" {
		fmt.Printf("[DEBUG][status] rejected request: missing sessionToken\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	api.mu.Lock()
	resolver := api.resolveQueueContext
	api.mu.Unlock()
	queueContext, err := resolver(request.Context(), statusRequest.SessionToken)
	if err != nil {
		if errors.Is(err, errInvalidSessionToken) {
			fmt.Printf("[DEBUG][status] session validation failed: %v\n", err)
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Printf("[DEBUG][status] queue context resolution failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	memberSet := make(map[string]struct{}, len(queueContext.Members))
	for _, member := range queueContext.Members {
		memberSet[member.PlayerID] = struct{}{}
	}

	getTicketStatus := func(ticketID string) (ticketStatus, bool, error) {
		dbStatus, found, dbErr := loadTicketStatusByIDFromDB(request.Context(), ticketID)
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

	if statusRequest.TicketID != "" {
		fmt.Printf("[DEBUG][status] fetching single ticket status ticketId=%s\n", statusRequest.TicketID)
		ticketPayload, ok, ticketErr := getTicketStatus(statusRequest.TicketID)
		if ticketErr != nil {
			fmt.Printf("[DEBUG][status] ticket status lookup failed ticketId=%s err=%v\n", statusRequest.TicketID, ticketErr)
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !ok {
			fmt.Printf("[DEBUG][status] ticket not found ticketId=%s\n", statusRequest.TicketID)
			response.WriteHeader(http.StatusNotFound)
			return
		}
		matchedPort := ""
		if (ticketPayload.Status == "matched" || ticketPayload.Status == "in_match") && ticketPayload.Instance != nil {
			matchedPort = ticketPayload.Instance.Port
		}

		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":   ticketPayload.Status,
			"ticketId": ticketPayload.TicketID,
			"partyId":  ticketPayload.PartyID,
			"playerId": ticketPayload.PlayerID,
			"username": ticketPayload.Username,
			"region":   api.region,
			"port":     matchedPort,
			"error":    ticketPayload.Error,
		})
		return
	}

	fmt.Printf("[DEBUG][status] fetching all party member statuses requesterPlayerId=%s memberCount=%d\n", queueContext.RequesterPlayerID, len(queueContext.Members))
	collectedStatuses, dbErr := loadLatestTicketStatusesFromDB(request.Context(), queueContext)
	if dbErr != nil {
		fmt.Printf("[DEBUG][status] loadLatestTicketStatusesFromDB failed, using in-memory stats err=%v\n", dbErr)
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

	fmt.Printf("[DEBUG][status] status response overallStatus=%s ticketCount=%d ownTicketId=%s port=%s\n", overallStatus, len(collectedStatuses), ownTicketID, matchedPort)
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":   overallStatus,
		"ticketId": ownTicketID,
		"port":     matchedPort,
		"partyId":  queueContext.PartyID,
		"playerId": queueContext.RequesterPlayerID,
		"region":   api.region,
		"tickets":  collectedStatuses,
		"members":  queueContext.Members,
	})
}

func (api *MatchmakingAPI) handleJoined(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][joined] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var joinedRequest struct {
		SessionToken string `json:"sessionToken"`
		TicketID     string `json:"ticketId"`
	}
	if err := json.NewDecoder(request.Body).Decode(&joinedRequest); err != nil {
		fmt.Printf("[DEBUG][joined] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if joinedRequest.SessionToken == "" || joinedRequest.TicketID == "" {
		fmt.Printf("[DEBUG][joined] rejected request: missing fields sessionTokenSet=%t ticketId=%s\n", joinedRequest.SessionToken != "", joinedRequest.TicketID)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	fmt.Printf("[DEBUG][joined] decoded request ticketId=%s\n", joinedRequest.TicketID)

	api.mu.Lock()
	resolver := api.resolveQueueContext
	api.mu.Unlock()
	queueContext, err := resolver(request.Context(), joinedRequest.SessionToken)
	if err != nil {
		if errors.Is(err, errInvalidSessionToken) {
			fmt.Printf("[DEBUG][joined] session validation failed: %v\n", err)
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Printf("[DEBUG][joined] queue context resolution failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	memberSet := make(map[string]struct{}, len(queueContext.Members))
	for _, member := range queueContext.Members {
		memberSet[member.PlayerID] = struct{}{}
	}

	ticketPayload, found, err := loadTicketStatusByIDFromDB(request.Context(), joinedRequest.TicketID)
	if err != nil {
		fmt.Printf("[DEBUG][joined] loadTicketStatusByIDFromDB failed ticketId=%s err=%v\n", joinedRequest.TicketID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !found {
		fmt.Printf("[DEBUG][joined] ticket not found ticketId=%s\n", joinedRequest.TicketID)
		response.WriteHeader(http.StatusNotFound)
		return
	}
	if _, ok := memberSet[ticketPayload.PlayerID]; !ok {
		fmt.Printf("[DEBUG][joined] forbidden: player not in requester party ticketId=%s playerId=%s\n", joinedRequest.TicketID, ticketPayload.PlayerID)
		response.WriteHeader(http.StatusForbidden)
		return
	}

	mmDB, err := server.GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][joined] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	updated, err := markTicketsInMatchByTicketID(request.Context(), mmDB, joinedRequest.TicketID)
	if err != nil {
		fmt.Printf("[DEBUG][joined] markTicketsInMatchByTicketID failed ticketId=%s err=%v\n", joinedRequest.TicketID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("[DEBUG][joined] successfully marked tickets in_match ticketId=%s updatedCount=%d\n", joinedRequest.TicketID, updated)

	api.mu.Lock()
	if ticket, ok := api.tickets[joinedRequest.TicketID]; ok {
		ticket.status = "in_match"
	}
	api.mu.Unlock()

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":         "in_match",
		"ticketId":       joinedRequest.TicketID,
		"partyId":        ticketPayload.PartyID,
		"updatedTickets": updated,
	})
}

func (api *MatchmakingAPI) handleLeft(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][left] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][left] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var leftRequest struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.NewDecoder(request.Body).Decode(&leftRequest); err != nil {
		fmt.Printf("[DEBUG][left] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if leftRequest.SessionToken == "" {
		fmt.Printf("[DEBUG][left] rejected request: missing sessionToken\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	api.mu.Lock()
	resolver := api.resolveQueueContext
	api.mu.Unlock()
	queueContext, err := resolver(request.Context(), leftRequest.SessionToken)
	if err != nil {
		if errors.Is(err, errInvalidSessionToken) {
			fmt.Printf("[DEBUG][left] session validation failed: %v\n", err)
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Printf("[DEBUG][left] queue context resolution failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	memberIDs := make([]string, 0, len(queueContext.Members))
	for _, member := range queueContext.Members {
		memberIDs = append(memberIDs, member.PlayerID)
	}
	fmt.Printf("[DEBUG][left] marking party members as left partyId=%s memberCount=%d\n", queueContext.PartyID, len(memberIDs))

	mmDB, err := server.GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][left] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	updated, err := markPlayersLeft(request.Context(), mmDB, memberIDs)
	if err != nil {
		fmt.Printf("[DEBUG][left] markPlayersLeft failed memberCount=%d err=%v\n", len(memberIDs), err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("[DEBUG][left] successfully marked players as left memberCount=%d updatedCount=%d\n", len(memberIDs), updated)

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

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":         "not_queued",
		"partyId":        queueContext.PartyID,
		"updatedTickets": updated,
	})
}

func (api *MatchmakingAPI) handleHeartbeat(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][heartbeat] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][heartbeat] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var heartbeatRequest struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.NewDecoder(request.Body).Decode(&heartbeatRequest); err != nil {
		fmt.Printf("[DEBUG][heartbeat] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if heartbeatRequest.SessionToken == "" {
		fmt.Printf("[DEBUG][heartbeat] rejected request: missing sessionToken\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	api.mu.Lock()
	resolver := api.resolveQueueContext
	api.mu.Unlock()
	queueContext, err := resolver(request.Context(), heartbeatRequest.SessionToken)
	if err != nil {
		if errors.Is(err, errInvalidSessionToken) {
			fmt.Printf("[DEBUG][heartbeat] session validation failed: %v\n", err)
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Printf("[DEBUG][heartbeat] queue context resolution failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	memberIDs := make([]string, 0, len(queueContext.Members))
	for _, member := range queueContext.Members {
		memberIDs = append(memberIDs, member.PlayerID)
	}
	fmt.Printf("[DEBUG][heartbeat] updating heartbeat for party members partyId=%s memberCount=%d\n", queueContext.PartyID, len(memberIDs))

	mmDB, err := server.GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][heartbeat] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	updated, err := touchPlayersHeartbeat(request.Context(), mmDB, memberIDs)
	if err != nil {
		fmt.Printf("[DEBUG][heartbeat] touchPlayersHeartbeat failed memberCount=%d err=%v\n", len(memberIDs), err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	if updated == 0 {
		fmt.Printf("[DEBUG][heartbeat] no matched tickets found for heartbeat partyId=%s memberCount=%d\n", queueContext.PartyID, len(memberIDs))
		response.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "no active match found",
		})
		return
	}
	fmt.Printf("[DEBUG][heartbeat] successfully updated heartbeat memberCount=%d updatedCount=%d\n", len(memberIDs), updated)

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":         "ok",
		"partyId":        queueContext.PartyID,
		"updatedTickets": updated,
		"heartbeatAt":    time.Now().UTC().Format(time.RFC3339),
	})
}

func (api *MatchmakingAPI) handleCancel(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][cancel] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][cancel] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ticketID := request.URL.Query().Get("ticketId")
	if ticketID == "" {
		fmt.Printf("[DEBUG][cancel] rejected request: missing ticketId\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	fmt.Printf("[DEBUG][cancel] cancelling ticket ticketId=%s\n", ticketID)

	mmDB, err := server.GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][cancel] GetMMDB failed ticketId=%s err=%v\n", ticketID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	if err := updateTicketStatuses(request.Context(), mmDB, []string{ticketID}, "left", nil); err != nil {
		fmt.Printf("[DEBUG][cancel] updateTicketStatuses failed ticketId=%s err=%v\n", ticketID, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	api.mu.Lock()
	if ticket, ok := api.tickets[ticketID]; ok {
		ticket.status = "cancelled"
		fmt.Printf("[DEBUG][cancel] marked ticket as cancelled ticketId=%s\n", ticketID)
	}
	api.waiting = removeTicket(api.waiting, ticketID)
	api.mu.Unlock()
	fmt.Printf("[DEBUG][cancel] ticket removed from waiting queue ticketId=%s\n", ticketID)

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":   "cancelled",
		"ticketId": ticketID,
	})
}

func resolveQueueContextFromSession(ctx context.Context, sessionToken string) (QueueContext, error) {
	if sessionToken == "" {
		return QueueContext{}, errInvalidSessionToken
	}

	requesterPlayerID, err := server.GetPlayerIDFromSession(sessionToken)
	if err != nil {
		if err.Error() == "invalid session token" {
			return QueueContext{}, errInvalidSessionToken
		}
		return QueueContext{}, err
	}

	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		return QueueContext{}, err
	}

	partyID := ""
	partyQuery := `SELECT p.party_id
		FROM party_players pp
		JOIN parties p ON p.party_id = pp.party_id
		WHERE pp.player_id = $1
		LIMIT 1`
	partyRows, err := server.SubmitQuery(ctx, mmDB.DB, partyQuery, requesterPlayerID)
	if err != nil {
		return QueueContext{}, err
	}
	if partyRows.Next() {
		if scanErr := partyRows.Scan(&partyID); scanErr != nil {
			_ = partyRows.Close()
			return QueueContext{}, scanErr
		}
	}
	if closeErr := partyRows.Close(); closeErr != nil {
		return QueueContext{}, closeErr
	}

	memberIDs := []string{requesterPlayerID}
	if partyID != "" {
		memberIDs = make([]string, 0, 4)
		memberQuery := `SELECT player_id FROM party_players WHERE party_id = $1 ORDER BY player_id`
		memberRows, memberErr := server.SubmitQuery(ctx, mmDB.DB, memberQuery, partyID)
		if memberErr != nil {
			return QueueContext{}, memberErr
		}
		for memberRows.Next() {
			var memberID string
			if scanErr := memberRows.Scan(&memberID); scanErr != nil {
				_ = memberRows.Close()
				return QueueContext{}, scanErr
			}
			memberIDs = append(memberIDs, memberID)
		}
		if closeErr := memberRows.Close(); closeErr != nil {
			return QueueContext{}, closeErr
		}
		if len(memberIDs) == 0 {
			memberIDs = append(memberIDs, requesterPlayerID)
		}
	}

	playerDB, err := server.GetDatabase(ctx)
	if err != nil {
		return QueueContext{}, err
	}

	members := make([]QueueMember, 0, len(memberIDs))
	for _, memberID := range memberIDs {
		username := memberID
		usernameRows, queryErr := server.SubmitQuery(ctx, playerDB.DB, "SELECT account_name FROM players WHERE id = $1", memberID)
		if queryErr != nil {
			return QueueContext{}, queryErr
		}
		if usernameRows.Next() {
			if scanErr := usernameRows.Scan(&username); scanErr != nil {
				_ = usernameRows.Close()
				return QueueContext{}, scanErr
			}
		}
		if closeErr := usernameRows.Close(); closeErr != nil {
			return QueueContext{}, closeErr
		}
		members = append(members, QueueMember{PlayerID: memberID, Username: username})
	}

	return QueueContext{
		RequesterPlayerID: requesterPlayerID,
		PartyID:           partyID,
		Members:           members,
	}, nil
}

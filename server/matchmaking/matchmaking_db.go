package matchmaking

import (
	"context"
	"fmt"
	"strings"
	"time"

	server "fracturedexodusserver/server"
)

type queueTicketRow struct {
	TicketID string
	PlayerID string
	PartyID  string
	QueuedAt time.Time
	Username string
	Faction  int
}

type queueTicketGroup struct {
	PartyID string
	Rows    []queueTicketRow
}

func ticketExistsInDB(ctx context.Context, mmDB *server.Database, ticketID string) (bool, error) {
	rows, err := server.SubmitQuery(ctx, mmDB.DB, "SELECT COUNT(*) FROM matchmaking_tickets WHERE ticket_id = $1", ticketID)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	var count int
	if rows.Next() {
		if scanErr := rows.Scan(&count); scanErr != nil {
			return false, scanErr
		}
		return count > 0, nil
	}

	if err := rows.Err(); err != nil {
		return false, err
	}

	return false, nil
}

func persistMatchmakingTicket(ctx context.Context, mmDB *server.Database, ticketID string, playerID string, partyID string) error {
	if mmDB == nil || mmDB.DB == nil {
		return nil
	}

	clearQuery := "DELETE FROM matchmaking_tickets WHERE player_id = $1 AND status IN ('queued', 'searching')"
	if _, err := server.SubmitExec(ctx, mmDB.DB, clearQuery, playerID); err != nil {
		return err
	}

	insertQuery := `INSERT INTO matchmaking_tickets (ticket_id, player_id, party_id, status, queued_at)
		VALUES ($1, $2, $3, 'queued', $4)`
	_, err := server.SubmitExec(ctx, mmDB.DB, insertQuery, ticketID, playerID, partyID, time.Now().UTC())
	return err
}

func loadLatestTicketStatusesFromDB(ctx context.Context, queueContext QueueContext) ([]ticketStatus, error) {
	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		return nil, err
	}

	usernameByPlayerID := make(map[string]string, len(queueContext.Members))
	for _, member := range queueContext.Members {
		usernameByPlayerID[member.PlayerID] = member.Username
	}

	statuses := make([]ticketStatus, 0, len(queueContext.Members))
	for _, member := range queueContext.Members {
		rows, queryErr := server.SubmitQuery(
			ctx,
			mmDB.DB,
			`SELECT mt.ticket_id, COALESCE(mt.party_id, ''), mt.status, COALESCE(g.game_id, ''), COALESCE(g.ip_addr, ''), COALESCE(g.port, '')
			 FROM matchmaking_tickets
			 AS mt
			 LEFT JOIN games g ON g.game_id = mt.game_id
			 WHERE player_id = $1
			 ORDER BY mt.queued_at DESC
			 LIMIT 1`,
			member.PlayerID,
		)
		if queryErr != nil {
			return nil, queryErr
		}

		if rows.Next() {
			var dbTicketID string
			var dbPartyID string
			var dbStatus string
			var gameID string
			var gameHost string
			var gamePort string
			if scanErr := rows.Scan(&dbTicketID, &dbPartyID, &dbStatus, &gameID, &gameHost, &gamePort); scanErr != nil {
				_ = rows.Close()
				return nil, scanErr
			}

			statusValue := normalizeTicketStatus(dbStatus)

			if queueContext.PartyID != "" && dbPartyID != "" && dbPartyID != queueContext.PartyID {
				_ = rows.Close()
				continue
			}

			var instance *server.GameInstance
			if gameID != "" {
				instance = &server.GameInstance{
					ID:   gameID,
					Host: gameHost,
					Port: gamePort,
				}
			}

			statuses = append(statuses, ticketStatus{
				PlayerID: member.PlayerID,
				Username: usernameByPlayerID[member.PlayerID],
				TicketID: dbTicketID,
				Status:   statusValue,
				PartyID:  dbPartyID,
				Instance: instance,
			})
		}

		if closeErr := rows.Close(); closeErr != nil {
			return nil, closeErr
		}
	}

	return statuses, nil
}

func loadTicketStatusByIDFromDB(ctx context.Context, ticketID string) (ticketStatus, bool, error) {
	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		return ticketStatus{}, false, err
	}

	rows, err := server.SubmitQuery(
		ctx,
		mmDB.DB,
		`SELECT mt.ticket_id, mt.player_id, COALESCE(mt.party_id, ''), mt.status, COALESCE(g.game_id, ''), COALESCE(g.ip_addr, ''), COALESCE(g.port, '')
		 FROM matchmaking_tickets mt
		 LEFT JOIN games g ON g.game_id = mt.game_id
		 WHERE mt.ticket_id = $1
		 LIMIT 1`,
		ticketID,
	)
	if err != nil {
		return ticketStatus{}, false, err
	}
	defer rows.Close()

	if !rows.Next() {
		return ticketStatus{}, false, nil
	}

	var dbTicketID string
	var playerID string
	var partyID string
	var rawStatus string
	var gameID string
	var gameHost string
	var gamePort string
	if err := rows.Scan(&dbTicketID, &playerID, &partyID, &rawStatus, &gameID, &gameHost, &gamePort); err != nil {
		return ticketStatus{}, false, err
	}

	username := playerID
	playerDB, err := server.GetDatabase(ctx)
	if err == nil {
		nameRows, queryErr := server.SubmitQuery(ctx, playerDB.DB, "SELECT account_name FROM players WHERE id = $1", playerID)
		if queryErr == nil {
			if nameRows.Next() {
				_ = nameRows.Scan(&username)
			}
			_ = nameRows.Close()
		}
	}

	var instance *server.GameInstance
	if gameID != "" {
		instance = &server.GameInstance{ID: gameID, Host: gameHost, Port: gamePort}
	}

	return ticketStatus{
		PlayerID: playerID,
		Username: username,
		TicketID: dbTicketID,
		Status:   normalizeTicketStatus(rawStatus),
		PartyID:  partyID,
		Instance: instance,
	}, true, nil
}

func loadQueueGroupsFromDB(ctx context.Context, mmDB *server.Database) ([]queueTicketGroup, error) {
	rows, err := server.SubmitQuery(
		ctx,
		mmDB.DB,
		`SELECT ticket_id, player_id, COALESCE(party_id, ''), queued_at
		 FROM matchmaking_tickets
		 WHERE status IN ('queued', 'searching')
		 ORDER BY queued_at ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groupsByKey := map[string]*queueTicketGroup{}
	orderedKeys := make([]string, 0)
	for rows.Next() {
		var row queueTicketRow
		if scanErr := rows.Scan(&row.TicketID, &row.PlayerID, &row.PartyID, &row.QueuedAt); scanErr != nil {
			return nil, scanErr
		}

		groupKey := row.PartyID
		if groupKey == "" {
			groupKey = "solo-" + row.PlayerID
		}
		group, exists := groupsByKey[groupKey]
		if !exists {
			group = &queueTicketGroup{PartyID: row.PartyID, Rows: make([]queueTicketRow, 0, 4)}
			groupsByKey[groupKey] = group
			orderedKeys = append(orderedKeys, groupKey)
		}
		group.Rows = append(group.Rows, row)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	groups := make([]queueTicketGroup, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		groups = append(groups, *groupsByKey[key])
	}

	// Resolve each player's active character faction from the player DB.
	allPlayerIDs := make([]string, 0)
	for _, group := range groups {
		for _, row := range group.Rows {
			allPlayerIDs = append(allPlayerIDs, row.PlayerID)
		}
	}
	if len(allPlayerIDs) > 0 {
		playerDB, playerDBErr := server.GetDatabase(ctx)
		if playerDBErr == nil {
			factionQuery := fmt.Sprintf(
				`SELECT ac.player_id, c.faction FROM active_characters ac
				 JOIN characters c ON c.character_id = ac.character_id
				 WHERE ac.player_id IN (%s)`,
				buildPlaceholderList(1, len(allPlayerIDs)),
			)
			args := make([]any, len(allPlayerIDs))
			for i, id := range allPlayerIDs {
				args[i] = id
			}
			factionRows, factionErr := server.SubmitQuery(ctx, playerDB.DB, factionQuery, args...)
			if factionErr == nil {
				factionByPlayerID := make(map[string]int, len(allPlayerIDs))
				for factionRows.Next() {
					var pid string
					var faction int
					if scanErr := factionRows.Scan(&pid, &faction); scanErr == nil {
						factionByPlayerID[pid] = faction
					}
				}
				_ = factionRows.Close()
				for gi := range groups {
					for ri := range groups[gi].Rows {
						groups[gi].Rows[ri].Faction = factionByPlayerID[groups[gi].Rows[ri].PlayerID]
					}
				}
			}
		}
	}

	return groups, nil
}

func claimTicketsForMatch(ctx context.Context, mmDB *server.Database, ticketIDs []string) (bool, error) {
	if len(ticketIDs) == 0 {
		return false, nil
	}

	query := fmt.Sprintf(
		"UPDATE matchmaking_tickets SET status = 'matching' WHERE ticket_id IN (%s) AND status IN ('queued', 'searching')",
		buildPlaceholderList(1, len(ticketIDs)),
	)

	args := make([]any, 0, len(ticketIDs))
	for _, ticketID := range ticketIDs {
		args = append(args, ticketID)
	}

	result, err := server.SubmitExec(ctx, mmDB.DB, query, args...)
	if err != nil {
		return false, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}

	return int(affected) == len(ticketIDs), nil
}

func loadPlayersForRows(ctx context.Context, rows []queueTicketRow) ([]server.Player, error) {
	playerDB, err := server.GetDatabase(ctx)
	if err != nil {
		return nil, err
	}

	players := make([]server.Player, 0, len(rows))
	for i, row := range rows {
		username := row.PlayerID
		nameRows, queryErr := server.SubmitQuery(ctx, playerDB.DB, "SELECT account_name FROM players WHERE id = $1", row.PlayerID)
		if queryErr != nil {
			return nil, queryErr
		}
		if nameRows.Next() {
			_ = nameRows.Scan(&username)
		}
		if closeErr := nameRows.Close(); closeErr != nil {
			return nil, closeErr
		}

		players = append(players, server.Player{
			Username: username,
			Ticket:   row.TicketID,
		})
		rows[i].Username = username
	}

	return players, nil
}

func persistMatchResult(ctx context.Context, mmDB *server.Database, instance server.GameInstance, rows []queueTicketRow) error {
	gameID := instance.ID
	if gameID == "" {
		gameID = "game-" + fmt.Sprintf("%d", time.Now().UnixNano())
	}

	gameInsert := `INSERT INTO games (game_id, ip_addr, port, status, created_at)
		VALUES ($1, $2, $3, 'ready', $4)
		ON CONFLICT (game_id) DO UPDATE SET ip_addr = EXCLUDED.ip_addr, port = EXCLUDED.port, status = EXCLUDED.status`
	if _, err := server.SubmitExec(ctx, mmDB.DB, gameInsert, gameID, instance.Host, instance.Port, time.Now().UTC()); err != nil {
		return err
	}

	for _, row := range rows {
		if _, err := server.SubmitExec(ctx, mmDB.DB, "INSERT INTO game_players (player_id, game_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", row.PlayerID, gameID); err != nil {
			return err
		}
	}

	ticketIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		ticketIDs = append(ticketIDs, row.TicketID)
	}

	return updateTicketStatuses(ctx, mmDB, ticketIDs, "matched", &gameID)
}

func updateTicketStatuses(ctx context.Context, mmDB *server.Database, ticketIDs []string, status string, gameID *string) error {
	if len(ticketIDs) == 0 {
		return nil
	}

	if gameID == nil {
		query := fmt.Sprintf("UPDATE matchmaking_tickets SET status = $1 WHERE ticket_id IN (%s)", buildPlaceholderList(2, len(ticketIDs)))
		args := make([]any, 0, 1+len(ticketIDs))
		args = append(args, status)
		for _, ticketID := range ticketIDs {
			args = append(args, ticketID)
		}
		_, err := server.SubmitExec(ctx, mmDB.DB, query, args...)
		return err
	}

	query := fmt.Sprintf("UPDATE matchmaking_tickets SET status = $1, game_id = $2 WHERE ticket_id IN (%s)", buildPlaceholderList(3, len(ticketIDs)))
	args := make([]any, 0, 2+len(ticketIDs))
	args = append(args, status, *gameID)
	for _, ticketID := range ticketIDs {
		args = append(args, ticketID)
	}
	_, err := server.SubmitExec(ctx, mmDB.DB, query, args...)
	return err
}

func markTicketsInMatchByTicketID(ctx context.Context, mmDB *server.Database, ticketID string) (int64, error) {
	if mmDB == nil || mmDB.DB == nil {
		return 0, nil
	}

	now := time.Now().UTC()
	query := `WITH selected_ticket AS (
		SELECT ticket_id, COALESCE(party_id, '') AS party_id, game_id
		FROM matchmaking_tickets
		WHERE ticket_id = $1
		LIMIT 1
	), target_tickets AS (
		SELECT mt.ticket_id
		FROM matchmaking_tickets mt
		JOIN selected_ticket st
			ON mt.game_id = st.game_id
			AND (mt.party_id = st.party_id OR st.party_id = '')
		WHERE mt.status IN ('matched', 'in_match')
	)
	UPDATE matchmaking_tickets mt
	SET status = 'in_match', joined_at = COALESCE(joined_at, $2), last_heartbeat_at = $2
	WHERE mt.ticket_id IN (SELECT ticket_id FROM target_tickets)`

	result, err := server.SubmitExec(ctx, mmDB.DB, query, ticketID, now)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

func markPlayersLeft(ctx context.Context, mmDB *server.Database, playerIDs []string) (int64, error) {
	if mmDB == nil || mmDB.DB == nil || len(playerIDs) == 0 {
		return 0, nil
	}

	now := time.Now().UTC()
	query := fmt.Sprintf(`WITH latest_active_tickets AS (
		SELECT DISTINCT ON (player_id) ticket_id
		FROM matchmaking_tickets
		WHERE player_id IN (%s)
			AND status IN ('queued', 'searching', 'matching', 'matched', 'in_match')
		ORDER BY player_id, queued_at DESC
	)
	UPDATE matchmaking_tickets mt
	SET status = 'left', game_id = NULL, left_at = $1, last_heartbeat_at = $1
	WHERE mt.ticket_id IN (SELECT ticket_id FROM latest_active_tickets)`, buildPlaceholderList(2, len(playerIDs)))

	args := make([]any, 0, 1+len(playerIDs))
	args = append(args, now)
	for _, playerID := range playerIDs {
		args = append(args, playerID)
	}

	result, err := server.SubmitExec(ctx, mmDB.DB, query, args...)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

func touchPlayersHeartbeat(ctx context.Context, mmDB *server.Database, playerIDs []string) (int64, error) {
	if mmDB == nil || mmDB.DB == nil || len(playerIDs) == 0 {
		return 0, nil
	}

	now := time.Now().UTC()
	query := fmt.Sprintf(`WITH latest_active_tickets AS (
		SELECT DISTINCT ON (player_id) ticket_id
		FROM matchmaking_tickets
		WHERE player_id IN (%s)
			AND status IN ('matched', 'in_match')
		ORDER BY player_id, queued_at DESC
	)
	UPDATE matchmaking_tickets mt
	SET last_heartbeat_at = $1
	WHERE mt.ticket_id IN (SELECT ticket_id FROM latest_active_tickets)`, buildPlaceholderList(2, len(playerIDs)))

	args := make([]any, 0, 1+len(playerIDs))
	args = append(args, now)
	for _, playerID := range playerIDs {
		args = append(args, playerID)
	}

	result, err := server.SubmitExec(ctx, mmDB.DB, query, args...)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

func markMatchEndedByServerName(ctx context.Context, mmDB *server.Database, serverName string) (int64, int64, error) {
	if mmDB == nil || mmDB.DB == nil || strings.TrimSpace(serverName) == "" {
		return 0, 0, nil
	}

	now := time.Now().UTC()

	updateTicketsQuery := `UPDATE matchmaking_tickets mt
		SET status = 'left', game_id = NULL, left_at = $1, last_heartbeat_at = $1
		WHERE mt.game_id IN (
			SELECT game_id
			FROM games
			WHERE server_name = $2 AND status <> 'ended'
		)
		AND mt.status IN ('matched', 'in_match')`
	ticketResult, err := server.SubmitExec(ctx, mmDB.DB, updateTicketsQuery, now, serverName)
	if err != nil {
		return 0, 0, err
	}
	updatedTickets, err := ticketResult.RowsAffected()
	if err != nil {
		return 0, 0, err
	}

	updateGamesQuery := `UPDATE games
		SET status = 'ended'
		WHERE server_name = $1
			AND status <> 'ended'`
	gameResult, err := server.SubmitExec(ctx, mmDB.DB, updateGamesQuery, serverName)
	if err != nil {
		return 0, 0, err
	}
	updatedGames, err := gameResult.RowsAffected()
	if err != nil {
		return 0, 0, err
	}

	return updatedTickets, updatedGames, nil
}

func buildPlaceholderList(start int, count int) string {
	placeholders := make([]string, 0, count)
	for i := 0; i < count; i++ {
		placeholders = append(placeholders, fmt.Sprintf("$%d", start+i))
	}
	return strings.Join(placeholders, ",")
}

func normalizeTicketStatus(status string) string {
	switch status {
	case "queued", "matching":
		return "searching"
	case "left":
		return "not_queued"
	default:
		return status
	}
}

package playerhandling

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	server "fracturedexodusserver/server"
	ws "fracturedexodusserver/server/ws"
)

// WSHandlers returns the WebSocket handler map for every account.* message type. Each handler
// is a thin decode -> call the same core function/queries the HTTP handler uses -> encode
// wrapper, reusing the package's existing helpers (createSessionToken, createPlayerID,
// ensureFriendCode, lookupPlayerByFriendCode, createCharacterID, buildAccountInfoPayload,
// deleteCharacterBySessionToken) rather than reimplementing business logic.
func (api *PlayerAPI) WSHandlers() map[string]ws.HandlerFunc {
	return map[string]ws.HandlerFunc{
		"account.login":                api.wsLogin,
		"account.createAccount":        api.wsCreateAccount,
		"account.authenticate":         api.wsAuthenticate,
		"account.logout":               api.wsLogout,
		"account.getCharacters":        api.wsGetCharacters,
		"account.createCharacter":      api.wsCreateCharacter,
		"account.updateCharacter":      api.wsUpdateCharacter,
		"account.deleteCharacter":      api.wsDeleteCharacter,
		"account.setActiveCharacter":   api.wsSetActiveCharacter,
		"account.getInfo":              api.wsAccountInfo,
		"account.getInfoUpdate":        api.wsAccountInfo,
		"account.sendFriendRequest":    api.wsFriendRequest,
		"account.respondFriendRequest": api.wsRespondFriendRequest,
	}
}

// bindConn binds c to playerID/sessionToken and (re)registers it with the hub, unregistering
// any previous binding first if this conn was already bound under a different playerID (e.g.
// account.authenticate called again on an already-bound conn).
func (api *PlayerAPI) bindConn(c *ws.Conn, playerID string, sessionToken string) {
	previous := c.PlayerID()
	c.Bind(playerID, sessionToken)
	if api.hub == nil {
		return
	}
	if previous != "" && previous != playerID {
		api.hub.Unregister(previous, c)
	}
	api.hub.Register(playerID, c)
}

// mirrors handleLogin (POST /player/login). Allowed on an unbound conn.
func (api *PlayerAPI) wsLogin(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}

	sessionToken, err := createSessionToken()
	if err != nil {
		return nil, fmt.Errorf("failed to create session token: %w", err)
	}

	db, err := server.GetDatabase(ctx)
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	rows, err := server.SubmitQuery(ctx, db.DB, "SELECT id FROM players WHERE account_name = $1 AND password = $2", req.Username, req.Password)
	if err != nil {
		return nil, fmt.Errorf("database query failed: %w", err)
	}
	defer rows.Close()

	var id string
	if rows.Next() {
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("database scan failed: %w", err)
		}
	} else {
		return nil, fmt.Errorf("invalid credentials")
	}

	if _, err := server.SubmitExec(ctx, db.DB, "INSERT INTO session_tokens (player_id, session_token, expiration) VALUES ($1, $2, $3)", id, sessionToken, time.Now().Add(24*time.Hour).UTC()); err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	friendCode, err := ensureFriendCode(ctx, db, id)
	if err != nil {
		return nil, fmt.Errorf("failed to assign friend code: %w", err)
	}

	api.bindConn(c, id, sessionToken)

	return map[string]any{
		"status":       "ok",
		"message":      "login accepted",
		"issuedAt":     time.Now().UTC().Format(time.RFC3339),
		"accountId":    id,
		"sessionToken": sessionToken,
		"friendCode":   friendCode,
	}, nil
}

// mirrors handleCreateAccount (POST /player/account/create). Allowed on an unbound conn. Does
// not bind the conn itself (the HTTP endpoint doesn't log the account in either) — the client
// is expected to follow up with account.login.
func (api *PlayerAPI) wsCreateAccount(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	var req struct {
		AccountName string `json:"accountName"`
		Password    string `json:"password"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}
	if req.AccountName == "" {
		return nil, fmt.Errorf("account name is required")
	}

	playerID, err := createPlayerID()
	if err != nil {
		return nil, fmt.Errorf("failed to create player ID: %w", err)
	}

	db, err := server.GetDatabase(ctx)
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	if _, err := server.SubmitExec(ctx, db.DB, "INSERT INTO players (id, account_name, password) VALUES ($1, $2, $3)", playerID, req.AccountName, req.Password); err != nil {
		return nil, fmt.Errorf("account name already taken: %w", err)
	}

	return map[string]any{
		"status":   "ok",
		"message":  "account created",
		"playerId": playerID,
	}, nil
}

// account.authenticate is new: it binds a conn (or re-binds a fresh conn from a prior session)
// using an existing sessionToken, resolving the playerID via server.GetPlayerIDFromSession
// (server/auth.go), the same lookup used elsewhere. Allowed on an unbound conn.
func (api *PlayerAPI) wsAuthenticate(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	var req struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}
	if req.SessionToken == "" {
		return nil, fmt.Errorf("sessionToken is required")
	}

	playerID, err := server.GetPlayerIDFromSession(req.SessionToken)
	if err != nil {
		return nil, fmt.Errorf("invalid session token")
	}

	api.bindConn(c, playerID, req.SessionToken)

	return map[string]any{
		"status":   "ok",
		"playerId": playerID,
	}, nil
}

// mirrors handleLogout (POST /player/logout), using the conn's bound sessionToken instead of a
// payload field. Unbinds the conn on success so subsequent messages require re-auth.
func (api *PlayerAPI) wsLogout(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	sessionToken := c.SessionToken()

	db, err := server.GetDatabase(ctx)
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	if _, err := server.SubmitExec(ctx, db.DB, "DELETE FROM session_tokens WHERE session_token = $1", sessionToken); err != nil {
		return nil, fmt.Errorf("failed to invalidate session token: %w", err)
	}

	playerID := c.PlayerID()
	c.Bind("", "")
	if api.hub != nil && playerID != "" {
		api.hub.Unregister(playerID, c)
	}

	return map[string]any{
		"status":  "ok",
		"message": "logout successful",
	}, nil
}

// mirrors handleCharacters (POST /player/characters), using the conn's bound identity instead
// of resolving playerID from a payload sessionToken.
func (api *PlayerAPI) wsGetCharacters(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	playerID := c.PlayerID()

	db, err := server.GetDatabase(ctx)
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	query := "SELECT character_id, name, skin_key, weapon_1, weapon_2, weapon_3, equipment_1, equipment_2, xp, devotion, class_type, faction FROM characters WHERE player_id = $1"
	rows, err := server.SubmitQuery(ctx, db.DB, query, playerID)
	if err != nil {
		return nil, fmt.Errorf("database query failed: %w", err)
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
			return nil, fmt.Errorf("database scan failed: %w", err)
		}
		characters = append(characters, character)
	}

	return map[string]any{"characters": characters}, nil
}

// mirrors handleNewCharacter (POST /player/character/new), using the conn's bound identity.
func (api *PlayerAPI) wsCreateCharacter(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	const initialDevotion = 10

	var req struct {
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
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	playerID := c.PlayerID()

	db, err := server.GetDatabase(ctx)
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	const maxCharactersPerFaction = 5
	countRows, err := server.SubmitQuery(ctx, db.DB, "SELECT COUNT(*) FROM characters WHERE player_id = $1 AND faction = $2", playerID, req.Faction)
	if err != nil {
		return nil, fmt.Errorf("database query failed: %w", err)
	}
	var factionCount int
	if countRows.Next() {
		if err := countRows.Scan(&factionCount); err != nil {
			_ = countRows.Close()
			return nil, fmt.Errorf("database scan failed: %w", err)
		}
	}
	if err := countRows.Close(); err != nil {
		return nil, err
	}
	if factionCount >= maxCharactersPerFaction {
		return nil, fmt.Errorf("character limit reached for this faction")
	}

	id, err := createCharacterID()
	if err != nil {
		return nil, fmt.Errorf("failed to create character ID: %w", err)
	}

	query := "INSERT INTO characters (player_id, character_id, name, skin_key, weapon_1, weapon_2, weapon_3, equipment_1, equipment_2, xp, devotion, class_type, faction) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)"
	if _, err := server.SubmitExec(ctx, db.DB, query, playerID, id, req.Name, req.SkinKey, req.Weapon1, req.Weapon2, req.Weapon3, req.Equipment1, req.Equipment2, 0, initialDevotion, req.ClassType, req.Faction); err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	return map[string]any{
		"status":      "ok",
		"characterId": id,
		"message":     fmt.Sprintf("character %s created", req.Name),
	}, nil
}

// mirrors handleUpdateCharacter (POST /player/character/update), using the conn's bound
// identity.
func (api *PlayerAPI) wsUpdateCharacter(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	var req struct {
		CharacterID string `json:"characterId"`
		Name        string `json:"name"`
		SkinKey     string `json:"skinKey"`
		Weapon1     string `json:"weapon1"`
		Weapon2     string `json:"weapon2"`
		Weapon3     string `json:"weapon3"`
		Equipment1  string `json:"equipment1"`
		Equipment2  string `json:"equipment2"`
		ClassType   int    `json:"classType"`
		Faction     int    `json:"faction"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}
	if req.CharacterID == "" {
		return nil, fmt.Errorf("characterId is required")
	}
	if req.Name == "" {
		return nil, fmt.Errorf("all character fields are required")
	}

	playerID := c.PlayerID()

	db, err := server.GetDatabase(ctx)
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	query := "UPDATE characters SET name = $1, skin_key = $2, weapon_1 = $3, weapon_2 = $4, weapon_3 = $5, equipment_1 = $6, equipment_2 = $7, class_type = $8, faction = $9 WHERE character_id = $10 AND player_id = $11"
	result, err := server.SubmitExec(ctx, db.DB, query, req.Name, req.SkinKey, req.Weapon1, req.Weapon2, req.Weapon3, req.Equipment1, req.Equipment2, req.ClassType, req.Faction, req.CharacterID, playerID)
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return nil, fmt.Errorf("character not found")
	}

	return map[string]any{
		"status":      "ok",
		"characterId": req.CharacterID,
		"message":     "character updated",
	}, nil
}

// mirrors ONLY the sessionToken branch of handleDeleteCharacter (POST /player/character/delete)
// via the shared deleteCharacterBySessionToken (server/playerhandling/characters.go), using the
// conn's bound sessionToken. The serverToken branch is HTTP-only (dedicated game server binary)
// and has no WS equivalent.
func (api *PlayerAPI) wsDeleteCharacter(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	var req struct {
		CharacterID string `json:"characterId"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}
	if req.CharacterID == "" {
		return nil, fmt.Errorf("characterId is required")
	}

	if _, err := deleteCharacterBySessionToken(ctx, c.SessionToken(), req.CharacterID); err != nil {
		return nil, err
	}

	return map[string]any{
		"status":      "ok",
		"characterId": req.CharacterID,
		"message":     "character deleted",
	}, nil
}

// mirrors handleSetActiveCharacter (POST /player/character/set), using the conn's bound
// identity.
func (api *PlayerAPI) wsSetActiveCharacter(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	var req struct {
		CharacterID string `json:"characterId"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}

	playerID := c.PlayerID()

	if req.CharacterID == "" {
		db, err := server.GetDatabase(ctx)
		if err != nil {
			return nil, fmt.Errorf("database error: %w", err)
		}
		if _, err := server.SubmitExec(ctx, db.DB, "DELETE FROM active_characters WHERE player_id = $1", playerID); err != nil {
			return nil, fmt.Errorf("database error: %w", err)
		}
		if err := server.ClearPartyActiveCharacterSelection(ctx, playerID); err != nil {
			return nil, fmt.Errorf("failed to sync party active character: %w", err)
		}
		return map[string]any{
			"status":      "ok",
			"playerId":    playerID,
			"characterId": "",
			"message":     "active character cleared",
		}, nil
	}

	character, found, err := server.GetCharacterByID(ctx, req.CharacterID)
	if err != nil {
		return nil, fmt.Errorf("database query failed: %w", err)
	}
	if !found || character.PlayerID != playerID {
		return nil, fmt.Errorf("character not found")
	}

	db, err := server.GetDatabase(ctx)
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	query := `INSERT INTO active_characters (player_id, character_id, updated_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (player_id) DO UPDATE SET character_id = EXCLUDED.character_id, updated_at = EXCLUDED.updated_at`
	if _, err := server.SubmitExec(ctx, db.DB, query, playerID, req.CharacterID, time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	if err := server.SyncPartyActiveCharacterSelection(ctx, playerID, character); err != nil {
		return nil, fmt.Errorf("failed to sync party active character: %w", err)
	}

	return map[string]any{
		"status":      "ok",
		"playerId":    playerID,
		"characterId": character.ID,
		"faction":     character.Faction,
		"message":     "active character updated",
	}, nil
}

// mirrors handleAccountInfo (POST /player/account/info) via buildAccountInfoPayload, using the
// conn's bound identity rather than an explicit payload playerId. Registered for both
// account.getInfo and account.getInfoUpdate: the HTTP endpoint only ever validates a playerId
// that matches the caller's own sessionToken (the query requires session_token AND player_id to
// match), so there is no "look up a different player's info" capability to preserve — using the
// bound identity for both is a behavior-preserving simplification, not a semantic change.
func (api *PlayerAPI) wsAccountInfo(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	return api.buildAccountInfoPayload(ctx, c.PlayerID())
}

// mirrors handleFriendRequest (POST /player/friend/request), using the conn's bound identity as
// the sender.
func (api *PlayerAPI) wsFriendRequest(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	var req struct {
		PlayerID   string `json:"playerId"`
		FriendCode string `json:"friendCode"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}
	if req.PlayerID == "" && req.FriendCode == "" {
		return nil, fmt.Errorf("one of playerId or friendCode is required")
	}

	senderID := c.PlayerID()

	db, err := server.GetDatabase(ctx)
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	if req.FriendCode != "" {
		resolved, err := lookupPlayerByFriendCode(ctx, db, req.FriendCode)
		if err != nil {
			return nil, fmt.Errorf("database error: %w", err)
		}
		if resolved == "" {
			return nil, fmt.Errorf("player not found")
		}
		req.PlayerID = resolved
	}

	if senderID == req.PlayerID {
		return nil, fmt.Errorf("cannot send a friend request to yourself")
	}

	existsRows, err := server.SubmitQuery(ctx, db.DB, "SELECT 1 FROM players WHERE id = $1", req.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("database query failed: %w", err)
	}
	if !existsRows.Next() {
		_ = existsRows.Close()
		return nil, fmt.Errorf("target player not found")
	}
	_ = existsRows.Close()

	query := "SELECT connection_id, player_one_id, player_two_id, status FROM friend_connections WHERE (player_one_id = $1 AND player_two_id = $2) OR (player_one_id = $2 AND player_two_id = $1)"
	rows, err := server.SubmitQuery(ctx, db.DB, query, senderID, req.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("database query failed: %w", err)
	}
	defer rows.Close()

	var connectionID, playerOneID, playerTwoID, status string
	if rows.Next() {
		if err := rows.Scan(&connectionID, &playerOneID, &playerTwoID, &status); err != nil {
			return nil, fmt.Errorf("database scan failed: %w", err)
		}

		switch status {
		case "pending":
			if playerOneID == senderID && playerTwoID == req.PlayerID {
				return nil, fmt.Errorf("friend request already pending")
			}
			if playerOneID == req.PlayerID && playerTwoID == senderID {
				return nil, fmt.Errorf("incoming friend request already exists")
			}
		case "accepted":
			return nil, fmt.Errorf("players are already friends")
		case "blocked":
			return nil, fmt.Errorf("friend request blocked")
		case "removed":
			updateQuery := "UPDATE friend_connections SET player_one_id = $1, player_two_id = $2, status = 'pending', updated_at = $3 WHERE connection_id = $4"
			if _, err := server.SubmitExec(ctx, db.DB, updateQuery, senderID, req.PlayerID, time.Now().UTC(), connectionID); err != nil {
				return nil, fmt.Errorf("database error: %w", err)
			}
			api.pushAccountInfoUpdated(ctx, req.PlayerID)
			api.pushAccountInfoUpdated(ctx, senderID)
			return map[string]any{
				"status":         "ok",
				"message":        "friend request sent",
				"senderPlayerId": senderID,
				"targetPlayerId": req.PlayerID,
			}, nil
		default:
			return nil, fmt.Errorf("friend connection state is not actionable")
		}
	}

	connectionID, err = createFriendConnectionID()
	if err != nil {
		return nil, fmt.Errorf("failed to create connection ID: %w", err)
	}

	insertQuery := "INSERT INTO friend_connections (connection_id, player_one_id, player_two_id, status, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6)"
	if _, err := server.SubmitExec(ctx, db.DB, insertQuery, connectionID, senderID, req.PlayerID, "pending", time.Now().UTC(), time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	api.pushAccountInfoUpdated(ctx, req.PlayerID)
	api.pushAccountInfoUpdated(ctx, senderID)

	return map[string]any{
		"status":         "ok",
		"message":        "friend request sent",
		"connectionId":   connectionID,
		"senderPlayerId": senderID,
		"targetPlayerId": req.PlayerID,
	}, nil
}

// mirrors handleAcceptRejectFriendRequest (POST /player/friend/respond), using the conn's bound
// identity as the accepter.
func (api *PlayerAPI) wsRespondFriendRequest(ctx context.Context, c *ws.Conn, payload json.RawMessage) (any, error) {
	var req struct {
		Accept   bool   `json:"accept"`
		PlayerID string `json:"playerId"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}
	if req.PlayerID == "" {
		return nil, fmt.Errorf("playerId is required")
	}

	accepterID := c.PlayerID()
	if accepterID == req.PlayerID {
		return nil, fmt.Errorf("playerId must be the initial sender, not the accepter")
	}

	db, err := server.GetDatabase(ctx)
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	query := "SELECT connection_id FROM friend_connections WHERE player_one_id = $1 AND player_two_id = $2 AND status = 'pending'"
	rows, err := server.SubmitQuery(ctx, db.DB, query, req.PlayerID, accepterID)
	if err != nil {
		return nil, fmt.Errorf("database query failed: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, fmt.Errorf("pending friend request not found")
	}
	var connectionID string
	if err := rows.Scan(&connectionID); err != nil {
		return nil, fmt.Errorf("database scan failed: %w", err)
	}

	nextStatus := "removed"
	if req.Accept {
		nextStatus = "accepted"
	}

	updateQuery := "UPDATE friend_connections SET status = $1, updated_at = $2 WHERE connection_id = $3"
	if _, err := server.SubmitExec(ctx, db.DB, updateQuery, nextStatus, time.Now().UTC(), connectionID); err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	api.pushAccountInfoUpdated(ctx, req.PlayerID)
	api.pushAccountInfoUpdated(ctx, accepterID)

	return map[string]any{
		"status":           "ok",
		"message":          "friend request updated",
		"friendStatus":     nextStatus,
		"connectionId":     connectionID,
		"senderPlayerId":   req.PlayerID,
		"accepterPlayerId": accepterID,
		"accepted":         req.Accept,
	}, nil
}

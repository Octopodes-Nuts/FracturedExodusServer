package server

import (
	"context"
	"fmt"
)

// SyncPartyActiveCharacterSelection is an exported wrapper for use by sub-packages.
func SyncPartyActiveCharacterSelection(ctx context.Context, playerID string, character CharacterRecord) error {
	return syncPartyActiveCharacterSelection(ctx, playerID, character)
}

// ClearPartyActiveCharacterSelection is an exported wrapper for use by sub-packages.
func ClearPartyActiveCharacterSelection(ctx context.Context, playerID string) error {
	return clearPartyActiveCharacterSelection(ctx, playerID)
}

// syncPartyActiveCharacterSelection updates a player's active character in their party
// and, if they are the party leader, updates the party's faction to match.
func syncPartyActiveCharacterSelection(ctx context.Context, playerID string, character CharacterRecord) error {
	fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] start playerId=%s characterId=%s faction=%d\n", playerID, character.ID, character.Faction)
	mmDB, err := GetMMDB(ctx)
	if err != nil {
		fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] GetMMDB failed playerId=%s characterId=%s err=%v\n", playerID, character.ID, err)
		return err
	}

	partyID := ""
	{
		rows, queryErr := submitQuery(ctx, mmDB.DB, "SELECT party_id FROM party_players WHERE player_id = $1 LIMIT 1", playerID)
		if queryErr != nil {
			fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] findPartyForPlayer failed playerId=%s characterId=%s err=%v\n", playerID, character.ID, queryErr)
			return queryErr
		}
		if rows.Next() {
			if scanErr := rows.Scan(&partyID); scanErr != nil {
				_ = rows.Close()
				return scanErr
			}
		}
		if closeErr := rows.Close(); closeErr != nil {
			return closeErr
		}
	}

	if partyID == "" {
		fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] no party found playerId=%s characterId=%s\n", playerID, character.ID)
		return nil
	}
	fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] party resolved playerId=%s partyId=%s characterId=%s\n", playerID, partyID, character.ID)

	if _, execErr := submitExec(ctx, mmDB.DB, "UPDATE party_players SET active_character_id = $1 WHERE party_id = $2 AND player_id = $3", character.ID, partyID, playerID); execErr != nil {
		fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] update party_players failed playerId=%s partyId=%s characterId=%s err=%v\n", playerID, partyID, character.ID, execErr)
		return execErr
	}
	fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] party player active character updated playerId=%s partyId=%s characterId=%s\n", playerID, partyID, character.ID)

	primaryPlayerID := ""
	{
		rows, queryErr := submitQuery(ctx, mmDB.DB, "SELECT primary_player_id FROM parties WHERE party_id = $1", partyID)
		if queryErr != nil {
			fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] getPartyPrimaryPlayer failed playerId=%s partyId=%s err=%v\n", playerID, partyID, queryErr)
			return queryErr
		}
		if rows.Next() {
			if scanErr := rows.Scan(&primaryPlayerID); scanErr != nil {
				_ = rows.Close()
				return scanErr
			}
		}
		if closeErr := rows.Close(); closeErr != nil {
			return closeErr
		}
	}

	if primaryPlayerID != playerID {
		fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] player is not primary playerId=%s primaryPlayerId=%s partyId=%s\n", playerID, primaryPlayerID, partyID)
		return nil
	}
	fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] player is primary playerId=%s partyId=%s updating faction=%d\n", playerID, partyID, character.Faction)

	if _, execErr := submitExec(ctx, mmDB.DB, "UPDATE parties SET active_faction = $1, faction = $1 WHERE party_id = $2", character.Faction, partyID); execErr != nil {
		fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] update parties failed playerId=%s partyId=%s faction=%d err=%v\n", playerID, partyID, character.Faction, execErr)
		return execErr
	}
	fmt.Printf("[DEBUG][syncPartyActiveCharacterSelection] request succeeded playerId=%s partyId=%s faction=%d\n", playerID, partyID, character.Faction)

	return nil
}

// clearPartyActiveCharacterSelection clears a player's active character in their party
// and, if they are the party leader, resets the party's faction to 0.
func clearPartyActiveCharacterSelection(ctx context.Context, playerID string) error {
	fmt.Printf("[DEBUG][clearPartyActiveCharacterSelection] start playerId=%s\n", playerID)
	mmDB, err := GetMMDB(ctx)
	if err != nil {
		fmt.Printf("[DEBUG][clearPartyActiveCharacterSelection] GetMMDB failed playerId=%s err=%v\n", playerID, err)
		return err
	}

	partyID := ""
	{
		rows, queryErr := submitQuery(ctx, mmDB.DB, "SELECT party_id FROM party_players WHERE player_id = $1 LIMIT 1", playerID)
		if queryErr != nil {
			fmt.Printf("[DEBUG][clearPartyActiveCharacterSelection] findPartyForPlayer failed playerId=%s err=%v\n", playerID, queryErr)
			return queryErr
		}
		if rows.Next() {
			if scanErr := rows.Scan(&partyID); scanErr != nil {
				_ = rows.Close()
				return scanErr
			}
		}
		if closeErr := rows.Close(); closeErr != nil {
			return closeErr
		}
	}

	if partyID == "" {
		fmt.Printf("[DEBUG][clearPartyActiveCharacterSelection] no party found playerId=%s\n", playerID)
		return nil
	}
	fmt.Printf("[DEBUG][clearPartyActiveCharacterSelection] party resolved playerId=%s partyId=%s\n", playerID, partyID)

	if _, execErr := submitExec(ctx, mmDB.DB, "UPDATE party_players SET active_character_id = NULL WHERE party_id = $1 AND player_id = $2", partyID, playerID); execErr != nil {
		fmt.Printf("[DEBUG][clearPartyActiveCharacterSelection] update party_players failed playerId=%s partyId=%s err=%v\n", playerID, partyID, execErr)
		return execErr
	}
	fmt.Printf("[DEBUG][clearPartyActiveCharacterSelection] party player active character cleared playerId=%s partyId=%s\n", playerID, partyID)

	primaryPlayerID := ""
	{
		rows, queryErr := submitQuery(ctx, mmDB.DB, "SELECT primary_player_id FROM parties WHERE party_id = $1", partyID)
		if queryErr != nil {
			fmt.Printf("[DEBUG][clearPartyActiveCharacterSelection] getPartyPrimaryPlayer failed playerId=%s partyId=%s err=%v\n", playerID, partyID, queryErr)
			return queryErr
		}
		if rows.Next() {
			if scanErr := rows.Scan(&primaryPlayerID); scanErr != nil {
				_ = rows.Close()
				return scanErr
			}
		}
		if closeErr := rows.Close(); closeErr != nil {
			return closeErr
		}
	}

	if primaryPlayerID != playerID {
		fmt.Printf("[DEBUG][clearPartyActiveCharacterSelection] player is not primary playerId=%s primaryPlayerId=%s partyId=%s\n", playerID, primaryPlayerID, partyID)
		return nil
	}
	fmt.Printf("[DEBUG][clearPartyActiveCharacterSelection] player is primary playerId=%s partyId=%s resetting faction\n", playerID, partyID)

	if _, execErr := submitExec(ctx, mmDB.DB, "UPDATE parties SET active_faction = 0, faction = 0 WHERE party_id = $1", partyID); execErr != nil {
		fmt.Printf("[DEBUG][clearPartyActiveCharacterSelection] update parties failed playerId=%s partyId=%s err=%v\n", playerID, partyID, execErr)
		return execErr
	}
	fmt.Printf("[DEBUG][clearPartyActiveCharacterSelection] request succeeded playerId=%s partyId=%s\n", playerID, partyID)

	return nil
}

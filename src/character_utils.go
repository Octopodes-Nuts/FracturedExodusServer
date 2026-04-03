package server

import "context"

type CharacterRecord struct {
	ID         string
	PlayerID   string
	Name       string
	SkinKey    string
	Weapon1    string
	Weapon2    string
	Weapon3    string
	Equipment1 string
	Equipment2 string
	ClassType  int
	Faction    int
}

func getCharacterByID(ctx context.Context, characterID string) (CharacterRecord, bool, error) {
	playerDB, err := GetDatabase(ctx)
	if err != nil {
		return CharacterRecord{}, false, err
	}

	rows, err := submitQuery(
		ctx,
		playerDB.DB,
		`SELECT character_id, player_id, name, skin_key, weapon_1, weapon_2, weapon_3, equipment_1, equipment_2, class_type, faction
		 FROM characters
		 WHERE character_id = $1
		 LIMIT 1`,
		characterID,
	)
	if err != nil {
		return CharacterRecord{}, false, err
	}
	defer rows.Close()

	if !rows.Next() {
		return CharacterRecord{}, false, nil
	}

	character := CharacterRecord{}
	if err := rows.Scan(
		&character.ID,
		&character.PlayerID,
		&character.Name,
		&character.SkinKey,
		&character.Weapon1,
		&character.Weapon2,
		&character.Weapon3,
		&character.Equipment1,
		&character.Equipment2,
		&character.ClassType,
		&character.Faction,
	); err != nil {
		return CharacterRecord{}, false, err
	}

	return character, true, nil
}

// GetActiveCharacterForPlayer is an exported wrapper for use by sub-packages.
func GetActiveCharacterForPlayer(ctx context.Context, playerID string) (CharacterRecord, bool, error) {
	return getActiveCharacterForPlayer(ctx, playerID)
}

func getActiveCharacterForPlayer(ctx context.Context, playerID string) (CharacterRecord, bool, error) {
	playerDB, err := GetDatabase(ctx)
	if err != nil {
		return CharacterRecord{}, false, err
	}

	rows, err := submitQuery(
		ctx,
		playerDB.DB,
		`SELECT c.character_id, c.player_id, c.name, c.skin_key, c.weapon_1, c.weapon_2, c.weapon_3, c.equipment_1, c.equipment_2, c.class_type, c.faction
		 FROM active_characters ac
		 JOIN characters c ON c.character_id = ac.character_id
		 WHERE ac.player_id = $1
		 LIMIT 1`,
		playerID,
	)
	if err != nil {
		return CharacterRecord{}, false, err
	}
	defer rows.Close()

	if !rows.Next() {
		return CharacterRecord{}, false, nil
	}

	character := CharacterRecord{}
	if err := rows.Scan(
		&character.ID,
		&character.PlayerID,
		&character.Name,
		&character.SkinKey,
		&character.Weapon1,
		&character.Weapon2,
		&character.Weapon3,
		&character.Equipment1,
		&character.Equipment2,
		&character.ClassType,
		&character.Faction,
	); err != nil {
		return CharacterRecord{}, false, err
	}

	return character, true, nil
}

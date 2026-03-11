package server

import "context"

const (
	mmDBUser     = "fractured_exodus"
	mmDBPassword = "fe"
	mmDBHost     = "localhost"
	mmDBPort     = "5432"
	mmDBName     = "fractured_exodus_matchmaking"
)

var initMMDBStatements = []string{
	`CREATE TABLE IF NOT EXISTS games (
		game_id TEXT PRIMARY KEY,
		ip_addr TEXT NOT NULL,
		port TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'starting',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);`,
	`CREATE TABLE IF NOT EXISTS matchmaking_tickets (
		ticket_id TEXT PRIMARY KEY,
		player_id TEXT NOT NULL,
		party_id TEXT,
		status TEXT NOT NULL DEFAULT 'queued',
		game_id TEXT REFERENCES games(game_id) ON DELETE SET NULL,
		queued_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);`,
	`CREATE TABLE IF NOT EXISTS party_invites (
		invite_id TEXT PRIMARY KEY,
		party_id TEXT NOT NULL,
		from_player_id TEXT NOT NULL,
		to_player_id TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at TIMESTAMPTZ NOT NULL,
		seen_by_inviter BOOLEAN NOT NULL DEFAULT FALSE,
		seen_by_invitee BOOLEAN NOT NULL DEFAULT FALSE
	);`,
	`CREATE TABLE IF NOT EXISTS parties (
		party_id TEXT PRIMARY KEY,
		active_faction INTEGER NOT NULL DEFAULT 0,
		primary_player_id TEXT NOT NULL
	);`,
	`CREATE TABLE IF NOT EXISTS party_players (
		party_id TEXT NOT NULL REFERENCES parties(party_id) ON DELETE CASCADE,
		player_id TEXT NOT NULL,
		active_character_id TEXT,
		PRIMARY KEY (party_id, player_id)
	);`,
	`CREATE TABLE IF NOT EXISTS game_players (
		player_id TEXT NOT NULL,
		game_id TEXT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,
		PRIMARY KEY (player_id, game_id)
	);`,
}

var resetMMDBStatements = []string{
	`DROP TABLE IF EXISTS game_players CASCADE;`,
	`DROP TABLE IF EXISTS party_players CASCADE;`,
	`DROP TABLE IF EXISTS parties CASCADE;`,
	`DROP TABLE IF EXISTS party_invites CASCADE;`,
	`DROP TABLE IF EXISTS matchmaking_tickets CASCADE;`,
	`DROP TABLE IF EXISTS games CASCADE;`,
}

func DefaultMMDBConfig() DBConfig {
	return DBConfig{
		User:     getEnvOrDefault("MM_DB_USER", mmDBUser),
		Password: getEnvOrDefault("MM_DB_PASSWORD", mmDBPassword),
		Host:     getEnvOrDefault("MM_DB_HOST", mmDBHost),
		Port:     getEnvOrDefault("MM_DB_PORT", mmDBPort),
		Name:     getEnvOrDefault("MM_DB_NAME", mmDBName),
	}
}

func InitMMDB(ctx context.Context, database *Database) error {
	return execStatements(ctx, database.DB, initMMDBStatements)
}

func ResetMMDB(ctx context.Context, database *Database) error {
	return execStatements(ctx, database.DB, resetMMDBStatements)
}

package server

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	defaultDBUser     = "fractured_exodus"
	defaultDBPassword = "fe"
	defaultDBHost     = "localhost"
	defaultDBPort     = "5432"
	defaultDBName     = "fractured_exodus_player_data"
)

type DBConfig struct {
	User     string
	Password string
	Host     string
	Port     string
	Name     string
}

var initStatements = []string{
	`CREATE TABLE IF NOT EXISTS players (
		id TEXT PRIMARY KEY,
		password TEXT NOT NULL,
		account_name TEXT NOT NULL,
		account_level INTEGER NOT NULL DEFAULT 1,
		account_experience INTEGER NOT NULL DEFAULT 0
	);`,
	`CREATE TABLE IF NOT EXISTS session_tokens (
		id BIGSERIAL PRIMARY KEY,
		player_id TEXT NOT NULL REFERENCES players(id) ON DELETE CASCADE,
		session_token TEXT NOT NULL,
		expiration TIMESTAMPTZ NOT NULL,
		UNIQUE(player_id, session_token)
	);`,
	`CREATE TABLE IF NOT EXISTS characters (
		character_id TEXT PRIMARY KEY,
		player_id TEXT NOT NULL REFERENCES players(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		skin_key TEXT NOT NULL,
		weapon_1 TEXT NOT NULL,
		weapon_2 TEXT NOT NULL,
		weapon_3 TEXT NOT NULL,
		equipment_1 TEXT NOT NULL,
		equipment_2 TEXT NOT NULL,
		devotion_points INTEGER NOT NULL DEFAULT 0,
		class_type INTEGER NOT NULL DEFAULT 0,
		faction INTEGER NOT NULL DEFAULT 0
	);`,
	`CREATE TABLE IF NOT EXISTS active_characters (
		player_id TEXT PRIMARY KEY REFERENCES players(id) ON DELETE CASCADE,
		character_id TEXT NOT NULL REFERENCES characters(character_id) ON DELETE CASCADE,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);`,
	`CREATE TABLE IF NOT EXISTS friend_connections (
		connection_id TEXT PRIMARY KEY,
		player_one_id TEXT NOT NULL REFERENCES players(id) ON DELETE CASCADE,
		player_two_id TEXT NOT NULL REFERENCES players(id) ON DELETE CASCADE,
		status TEXT NOT NULL CHECK (status IN ('pending', 'accepted', 'blocked', 'removed')),
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		CHECK (player_one_id <> player_two_id),
		UNIQUE(player_one_id, player_two_id)
	);`,
}

var resetStatements = []string{
	`DROP TABLE IF EXISTS friend_connections CASCADE;`,
	`DROP TABLE IF EXISTS active_characters CASCADE;`,
	`DROP TABLE IF EXISTS characters CASCADE;`,
	`DROP TABLE IF EXISTS session_tokens CASCADE;`,
	`DROP TABLE IF EXISTS players CASCADE;`,
}

func DefaultDBConfig() DBConfig {
	return DBConfig{
		User:     getEnvOrDefault("DB_USER", defaultDBUser),
		Password: getEnvOrDefault("DB_PASSWORD", defaultDBPassword),
		Host:     getEnvOrDefault("DB_HOST", defaultDBHost),
		Port:     getEnvOrDefault("DB_PORT", defaultDBPort),
		Name:     getEnvOrDefault("DB_NAME", defaultDBName),
	}
}

func (cfg DBConfig) DSN() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable",
		cfg.User,
		cfg.Password,
		cfg.Host,
		cfg.Port,
		cfg.Name,
	)
}

func OpenDB(ctx context.Context, cfg DBConfig) (*sql.DB, error) {
	database, err := sql.Open("pgx", cfg.DSN())
	if err != nil {
		return nil, err
	}

	database.SetMaxOpenConns(10)
	database.SetMaxIdleConns(5)
	database.SetConnMaxLifetime(30 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := database.PingContext(pingCtx); err != nil {
		_ = database.Close()
		return nil, err
	}

	return database, nil
}

type Database struct {
	DB *sql.DB
}

var databaseInstance *Database

func GetDatabase(ctx context.Context) (*Database, error) {
	if databaseInstance != nil {
		return databaseInstance, nil
	}

	cfg := DefaultDBConfig()
	db, err := OpenDB(ctx, cfg)
	if err != nil {
		return nil, err
	}

	databaseInstance = &Database{DB: db}
	return databaseInstance, nil
}

func InitDB(ctx context.Context, database *sql.DB) error {
	return execStatements(ctx, database, initStatements)
}

func ResetDB(ctx context.Context, database *sql.DB) error {
	return execStatements(ctx, database, resetStatements)
}

func execStatements(ctx context.Context, database *sql.DB, statements []string) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	for _, statement := range statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			_ = transaction.Rollback()
			return err
		}
	}

	return transaction.Commit()
}

// get database response for query
func submitQuery(ctx context.Context, database *sql.DB, query string, args ...interface{}) (*sql.Rows, error) {
	return database.QueryContext(ctx, query, args...)
}

func submitExec(ctx context.Context, database *sql.DB, query string, args ...interface{}) (sql.Result, error) {
	return database.ExecContext(ctx, query, args...)
}

func getEnvOrDefault(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	return value
}

// SubmitQuery is an exported wrapper for use by sub-packages.
func SubmitQuery(ctx context.Context, database *sql.DB, query string, args ...interface{}) (*sql.Rows, error) {
	return submitQuery(ctx, database, query, args...)
}

// SubmitExec is an exported wrapper for use by sub-packages.
func SubmitExec(ctx context.Context, database *sql.DB, query string, args ...interface{}) (sql.Result, error) {
	return submitExec(ctx, database, query, args...)
}

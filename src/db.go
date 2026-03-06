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
		session_token TEXT
	);`,
	`CREATE TABLE IF NOT EXISTS equipment (
		id BIGSERIAL PRIMARY KEY,
		player_id TEXT NOT NULL REFERENCES players(id) ON DELETE CASCADE,
		equipment_key TEXT NOT NULL,
		quantity INTEGER NOT NULL DEFAULT 0,
		faction INTEGER NOT NULL DEFAULT 0
	);`,
	`CREATE TABLE IF NOT EXISTS characters (
		id BIGSERIAL PRIMARY KEY,
		player_id TEXT NOT NULL REFERENCES players(id) ON DELETE CASCADE,
		character_id TEXT NOT NULL,
		skin_key TEXT NOT NULL,
		weapon_1 TEXT NOT NULL,
		weapon_2 TEXT NOT NULL,
		weapon_3 TEXT NOT NULL,
		equipment_1 TEXT NOT NULL,
		equipment_2 TEXT NOT NULL,
		faction INTEGER NOT NULL DEFAULT 0
	);`,
}

var resetStatements = []string{
	`DROP TABLE IF EXISTS characters;`,
	`DROP TABLE IF EXISTS equipment;`,
	`DROP TABLE IF EXISTS players;`,
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

func getEnvOrDefault(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	return value
}

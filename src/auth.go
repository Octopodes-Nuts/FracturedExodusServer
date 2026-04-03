package server

import (
	"context"
	"fmt"
	"time"
)

func validateSessionToken(sessionToken string) error {
	db, err := GetDatabase(context.Background())
	if err != nil {
		return err
	}

	query := "SELECT 1 FROM session_tokens WHERE session_token = $1 AND expiration > $2"
	rows, err := submitQuery(context.Background(), db.DB, query, sessionToken, time.Now().UTC())
	if err != nil {
		return err
	}
	defer rows.Close()

	if rows.Next() {
		return nil
	}

	return fmt.Errorf("invalid session token")
}

// ValidateSessionToken is an exported wrapper for use by sub-packages.
func ValidateSessionToken(sessionToken string) error {
	return validateSessionToken(sessionToken)
}

// GetPlayerIDFromSession is an exported wrapper for use by sub-packages.
func GetPlayerIDFromSession(sessionToken string) (string, error) {
	return getPlayerIDFromSession(sessionToken)
}

func getPlayerIDFromSession(sessionToken string) (string, error) {
	db, err := GetDatabase(context.Background())
	if err != nil {
		return "", err
	}

	query := "SELECT player_id FROM session_tokens WHERE session_token = $1 AND expiration > $2"
	rows, err := submitQuery(context.Background(), db.DB, query, sessionToken, time.Now().UTC())
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var playerID string
	if rows.Next() {
		if err := rows.Scan(&playerID); err != nil {
			return "", err
		}
		return playerID, nil
	}

	return "", fmt.Errorf("invalid session token")
}

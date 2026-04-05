package matchmaking

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	server "fracturedexodusserver/src"
)

func (api *MatchmakingAPI) handleRegisterServer(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][registerServer] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][registerServer] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var registerRequest struct {
		ServerName string `json:"serverName"`
	}
	if err := json.NewDecoder(request.Body).Decode(&registerRequest); err != nil {
		fmt.Printf("[DEBUG][registerServer] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	if registerRequest.ServerName == "" {
		fmt.Printf("[DEBUG][registerServer] rejected request: missing serverName\n")
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status":  "error",
			"message": "serverName is required",
		})
		return
	}

	registrationKey, registrationKeyHash, err := generateServerToken()
	if err != nil {
		fmt.Printf("[DEBUG][registerServer] generateRegistrationKey failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	mmDB, err := server.GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][registerServer] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	if err := persistRegistrationKey(request.Context(), mmDB, registerRequest.ServerName, registrationKeyHash); err != nil {
		fmt.Printf("[DEBUG][registerServer] persistRegistrationKey failed serverName=%s err=%v\n", registerRequest.ServerName, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	serverToken, tokenHash, err := generateServerToken()
	if err != nil {
		fmt.Printf("[DEBUG][registerServer] generateServerToken failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	tokenID := "srv-token-" + uuid.NewString()
	if err := persistServerToken(request.Context(), mmDB, tokenID, registerRequest.ServerName, tokenHash); err != nil {
		fmt.Printf("[DEBUG][registerServer] persistServerToken failed serverName=%s err=%v\n", registerRequest.ServerName, err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	fmt.Printf("[DEBUG][registerServer] server registered serverName=%s\n", registerRequest.ServerName)
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":          "ok",
		"tokenId":         tokenID,
		"serverName":      registerRequest.ServerName,
		"serverToken":     serverToken,
		"registrationKey": registrationKey,
		"issuedAt":        time.Now().UTC().Format(time.RFC3339),
	})
}

func (api *MatchmakingAPI) handleMatchEnded(response http.ResponseWriter, request *http.Request) {
	fmt.Printf("[DEBUG][matchEnded] request received method=%s path=%s\n", request.Method, request.URL.Path)
	if request.Method != http.MethodPost {
		fmt.Printf("[DEBUG][matchEnded] rejected request: invalid method=%s\n", request.Method)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var matchEndedRequest struct {
		ServerToken string `json:"serverToken"`
	}
	if err := json.NewDecoder(request.Body).Decode(&matchEndedRequest); err != nil {
		fmt.Printf("[DEBUG][matchEnded] decode failed: %v\n", err)
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if matchEndedRequest.ServerToken == "" {
		fmt.Printf("[DEBUG][matchEnded] rejected request: missing serverToken\n")
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	mmDB, err := server.GetMMDB(request.Context())
	if err != nil {
		fmt.Printf("[DEBUG][matchEnded] GetMMDB failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	serverName, err := resolveServerNameFromToken(request.Context(), mmDB, matchEndedRequest.ServerToken)
	if err != nil {
		if errors.Is(err, errInvalidServerToken) {
			fmt.Printf("[DEBUG][matchEnded] server token validation failed\n")
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Printf("[DEBUG][matchEnded] server token lookup failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	fmt.Printf("[DEBUG][matchEnded] authenticated serverName=%s\n", serverName)

	updatedTickets, updatedGames, err := markMatchEndedByServerName(request.Context(), mmDB, serverName)
	if err != nil {
		fmt.Printf("[DEBUG][matchEnded] markMatchEndedByServerName failed: %v\n", err)
		response.WriteHeader(http.StatusInternalServerError)
		return
	}

	api.mu.Lock()
	for _, ticket := range api.tickets {
		if ticket == nil {
			continue
		}
		if ticket.instance != nil && ticket.instance.ContainerName == serverName {
			ticket.status = "left"
			ticket.instance = nil
			ticket.error = ""
		}
	}
	api.mu.Unlock()

	fmt.Printf("[DEBUG][matchEnded] request succeeded serverName=%s updatedTickets=%d updatedGames=%d\n", serverName, updatedTickets, updatedGames)
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":         "not_queued",
		"serverName":     serverName,
		"updatedTickets": updatedTickets,
		"updatedGames":   updatedGames,
	})
}

func generateServerToken() (string, string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := cryptorand.Read(tokenBytes); err != nil {
		return "", "", err
	}

	serverToken := hex.EncodeToString(tokenBytes)
	hash := sha256.Sum256([]byte(serverToken))
	return serverToken, hex.EncodeToString(hash[:]), nil
}

func persistServerToken(ctx context.Context, mmDB *server.Database, tokenID string, serverName string, tokenHash string) error {
	query := `INSERT INTO server_tokens (token_id, server_name, token_hash, created_at, revoked)
		VALUES ($1, $2, $3, $4, FALSE)`
	_, err := server.SubmitExec(ctx, mmDB.DB, query, tokenID, serverName, tokenHash, time.Now().UTC())
	return err
}

func persistRegistrationKey(ctx context.Context, mmDB *server.Database, serverName string, registrationKeyHash string) error {
	query := `INSERT INTO server_registration_keys (server_name, registration_key_hash, created_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (server_name) DO UPDATE SET registration_key_hash = EXCLUDED.registration_key_hash, created_at = EXCLUDED.created_at`
	_, err := server.SubmitExec(ctx, mmDB.DB, query, serverName, registrationKeyHash, time.Now().UTC())
	return err
}

func resolveServerNameFromToken(ctx context.Context, mmDB *server.Database, serverToken string) (string, error) {
	if mmDB == nil || mmDB.DB == nil || serverToken == "" {
		return "", errInvalidServerToken
	}

	hash := sha256.Sum256([]byte(serverToken))
	tokenHash := hex.EncodeToString(hash[:])

	rows, err := server.SubmitQuery(ctx, mmDB.DB, "SELECT server_name FROM server_tokens WHERE token_hash = $1 AND revoked = FALSE LIMIT 1", tokenHash)
	if err != nil {
		return "", err
	}

	serverName := ""
	if rows.Next() {
		if scanErr := rows.Scan(&serverName); scanErr != nil {
			_ = rows.Close()
			return "", scanErr
		}
	}
	if closeErr := rows.Close(); closeErr != nil {
		return "", closeErr
	}

	if serverName == "" {
		return "", errInvalidServerToken
	}

	if _, err := server.SubmitExec(ctx, mmDB.DB, "UPDATE server_tokens SET last_used_at = $1 WHERE token_hash = $2", time.Now().UTC(), tokenHash); err != nil {
		return "", err
	}

	return serverName, nil
}

// ValidateServerToken validates a server token and returns the server name if valid.
// Returns an error if the token is invalid or empty.
func ValidateServerToken(ctx context.Context, mmDB *server.Database, serverToken string) (string, error) {
	return resolveServerNameFromToken(ctx, mmDB, serverToken)
}

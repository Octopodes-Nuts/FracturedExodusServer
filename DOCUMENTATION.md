# API Documentation Overview

This directory contains comprehensive API documentation for the Fractured Exodus Server.

## Documentation Files

### 1. **API_DOCS.md** - Detailed Endpoint Reference
Complete human-readable documentation of all endpoints, organized by service:
- **Server API** - Health and info endpoints
- **Player API** - Account management, character progression, friends
- **Matchmaking API** - Queue management, party system, match creation
- **Game Server API** - Server instance management

Each endpoint includes:
- Description of functionality
- HTTP method and path
- Request body schema
- Response schema with examples
- HTTP status codes
- Error handling information

### 2. **openapi.yaml** - OpenAPI 3.0 Specification
Machine-readable API specification in OpenAPI 3.0 format. Can be used with:
- **Swagger UI**: View interactive documentation
  ```bash
  # With swagger-ui-express for Node.js
  # With go-swagger for Go
  # Or hosted at https://editor.swagger.io/
  ```
- **Code Generation**: Generate client SDKs in multiple languages
  ```bash
  openapi-generator-cli generate -i openapi.yaml -g typescript-fetch -o client/
  ```
- **API Testing**: Import into Postman, Insomnia, or other REST clients
- **Documentation Sites**: Deploy to ReadTheDocs, GitBook, etc.

### 3. **In-Code Documentation** - JSDoc-style Comments
Handler functions include documentation comments showing:
- Endpoint path and HTTP method
- Request payload structure
- Response payload structure
- Example usage patterns

Example:
```go
// handleLogin authenticates a player with username and password, returning a session token.
// POST /player/login
// Request: {"username": "string", "password": "string"}
// Response: {"status": "ok", "message": "...", "sessionToken": "string", "issuedAt": "timestamp"}
func (api *PlayerAPI) handleLogin(response http.ResponseWriter, request *http.Request) {
```

---

## Quick Start

### For API Consumers
1. Start with **API_DOCS.md** to understand what endpoints exist and how to use them
2. Look at the specific endpoint section for request/response examples
3. Test endpoints using cURL, Postman, or your client library

**Example:** Login flow
```bash
curl -X POST http://localhost:8000/player/login \
  -H "Content-Type: application/json" \
  -d '{"username": "testuser", "password": "testpass"}'
```

### For API Developers
1. Add code comments to new handlers using the documented pattern
2. Update `openapi.yaml` with new endpoint definitions
3. Add endpoint documentation to `API_DOCS.md` in the appropriate section
4. Keep request/response schemas in sync across all documentation

### For Documentation Generation
```bash
# Generate Swagger UI from openapi.yaml
docker run -p 8081:8080 -e SWAGGER_JSON=/specs/openapi.yaml \
  -v $(pwd)/openapi.yaml:/specs/openapi.yaml \
  swaggerapi/swagger-ui

# Generate TypeScript client
openapi-generator-cli generate \
  -i openapi.yaml \
  -g typescript-fetch \
  -o generated-client/

# Generate Go client
openapi-generator-cli generate \
  -i openapi.yaml \
  -g go \
  -o generated-client-go/
```

---

## API Organization

### Authentication
- Most endpoints require `sessionToken` obtained from `/player/login`
- Game server endpoints use `registrationKey` for server-to-server auth
- Session tokens are validated against the player database

### Response Format
**Success (200/201/202):**
```json
{
  "status": "ok",
  "message": "descriptive message",
  "data": {...}
}
```

**Error (4xx/5xx):**
```json
{
  "status": "error",
  "message": "human readable error",
  "error": "technical error details"
}
```

### Database Schema
- **Player DB** - Accounts, sessions, characters, friends
- **Matchmaking DB** - Parties, queues, game instances, servers

---

## Common Workflows

### Player Login & Character Selection
```
1. POST /player/login → get sessionToken
2. POST /player/characters → list player's characters
3. POST /player/setActiveCharacter → set active character (determines party faction)
```

### Party & Queue
```
1. POST /matchmaking/party/invite → invite friend to party
2. GET /matchmaking/party/invites → check pending invites
3. POST /matchmaking/party/respond → accept invite
4. POST /matchmaking/queue → queue party for match
5. GET /matchmaking/status → monitor queue status
```

### Matchmaking Flow
```
1. Player queues with party (faction determined by leader's active character)
2. Server groups players into matches when queue is full
3. Players join via /matchmaking/join
4. Game server notifies when match ends via /matchmaking/match/ended
5. Player stats/rewards recorded
```

---

## Testing Endpoints

### Using cURL
```bash
# Create account
curl -X POST http://localhost:8000/player/createAccount \
  -H "Content-Type: application/json" \
  -d '{"username": "testuser", "password": "testpass"}'

# Login
SESSION=$(curl -X POST http://localhost:8000/player/login \
  -H "Content-Type: application/json" \
  -d '{"username": "testuser", "password": "testpass"}' | jq -r .sessionToken)

# Get characters
curl -X POST http://localhost:8000/player/characters \
  -H "Content-Type: application/json" \
  -d "{\"sessionToken\": \"$SESSION\"}"
```

### Using Postman
1. Import `openapi.yaml` into Postman
2. Set up environment variables:
   - `{{baseUrl}}` = http://localhost:8000
   - `{{sessionToken}}` = value from login response
3. Use pre-built request templates

### Using the test suite
```bash
cd testing
go test -v ./...
```

---

## Maintenance

### Updating Documentation
When adding/modifying endpoints:
1. Update in-code comments (handler functions)
2. Update `openapi.yaml` following OpenAPI 3.0 spec
3. Update `API_DOCS.md` with endpoint details
4. Run tests to ensure consistency
5. Update this README if workflows changed

### Documentation Validation
```bash
# Validate OpenAPI schema
npm install -g swagger-cli
swagger-cli validate openapi.yaml

# Generate docs and check for broken links
# (if using documentation generator)
```

---

## Current Endpoints Summary

| Service | Count | Examples |
|---------|-------|----------|
| **Server** | 2 | GET /health, GET /info |
| **Player** | 12 | POST /player/login, POST /player/characters, POST /player/setActiveCharacter |
| **Matchmaking** | 14 | POST /matchmaking/queue, POST /matchmaking/party/invite, GET /matchmaking/status |
| **Game Server** | 1 | POST /game/server/start |
| **Total** | **29** | |

---

## Related Documentation

- [README.md](./README.md) - Project overview and setup
- [src/schema_account.txt](./src/schema_account.txt) - Player database schema
- [src/schema_matchmaking.txt](./src/schema_matchmaking.txt) - Matchmaking database schema
- [testing/README.md](./testing/README.md) - Test documentation

---

## Support

For API questions:
1. Check `API_DOCS.md` for endpoint details
2. Look at `openapi.yaml` for specification details
3. Review testing examples in `testing/playerHandling_test.go` and `testing/matchmaking_test.go`
4. Add DEBUG logging to handlers for troubleshooting (see `fmt.Printf` patterns in code)

For issues:
1. Check HTTP status codes and error messages in response body
2. Verify session token is valid (from `/player/login`)
3. Verify request payload matches schema in documentation
4. Check database connectivity with `/health` endpoint

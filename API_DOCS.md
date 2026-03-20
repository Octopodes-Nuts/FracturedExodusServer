# Fractured Exodus Server API Documentation

## Overview

The Fractured Exodus Server provides endpoints for account management, player progression, matchmaking, and game server coordination. The API uses JSON for request/response payloads and standard HTTP status codes.

### Base URL
```
http://localhost:8000
```

### Common Response Format
All error responses follow this format:
```json
{
  "status": "error",
  "message": "human readable error message",
  "error": "detailed error information"
}
```

---

## Health & Info Endpoints

### GET /health
Server health check endpoint.

**Response:**
```json
{
  "status": "ok"
}
```

**Status Codes:** 
- `200 OK` - Server is healthy

---

### GET /info
Retrieve server information.

**Response:**
```json
{
  "service": "FracturedExodusServer",
  "startedAt": "2026-03-20T14:30:00Z"
}
```

**Status Codes:**
- `200 OK` - Server info retrieved successfully

---

## Player API Endpoints

### POST /player/createAccount
Create a new player account.

**Request Body:**
```json
{
  "username": "string",
  "password": "string"
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "account created",
  "playerId": "uuid-string"
}
```

**Status Codes:**
- `201 Created` - Account created successfully
- `400 Bad Request` - Invalid request body or missing fields
- `409 Conflict` - Username already exists
- `500 Internal Server Error` - Database error

---

### POST /player/login
Authenticate player and receive session token.

**Request Body:**
```json
{
  "username": "string",
  "password": "string"
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "login accepted",
  "issuedAt": "2026-03-20T14:30:00Z",
  "sessionToken": "hex-encoded-session-token"
}
```

**Status Codes:**
- `200 OK` - Login successful
- `400 Bad Request` - Invalid request body
- `401 Unauthorized` - Invalid credentials
- `500 Internal Server Error` - Database error

---

### POST /player/logout
Invalidate current session token.

**Request Body:**
```json
{
  "sessionToken": "string"
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "logout successful"
}
```

**Status Codes:**
- `200 OK` - Logout successful
- `400 Bad Request` - Missing sessionToken
- `401 Unauthorized` - Invalid session token

---

### POST /player/accountInfo
Retrieve authenticated player's account information.

**Request Body:**
```json
{
  "playerId": "string",
  "sessionToken": "string"
}
```

**Response:**
```json
{
  "accountId": "uuid",
  "displayName": "string",
  "region": "NA|EU|APAC",
  "version": "dev|release"
}
```

**Status Codes:**
- `200 OK` - Account info retrieved
- `400 Bad Request` - Missing playerId or sessionToken
- `401 Unauthorized` - Invalid session token
- `500 Internal Server Error` - Database error

---

### POST /player/characters
Retrieve all characters belonging to the authenticated player.

**Request Body:**
```json
{
  "sessionToken": "string"
}
```

**Response:**
```json
{
  "characters": [
    {
      "id": "uuid",
      "name": "string",
      "skinKey": "string",
      "weapon1": "string",
      "weapon2": "string",
      "weapon3": "string",
      "equipment1": "string",
      "equipment2": "string",
      "classType": 0,
      "faction": 0
    }
  ]
}
```

**Status Codes:**
- `200 OK` - Characters retrieved
- `400 Bad Request` - Missing sessionToken
- `401 Unauthorized` - Invalid session token
- `500 Internal Server Error` - Database error

---

### POST /player/getCharacter
Retrieve a specific character by ID.

**Request Body:**
```json
{
  "sessionToken": "string",
  "characterId": "string"
}
```

**Response:**
```json
{
  "id": "uuid",
  "name": "string",
  "skinKey": "string",
  "weapon1": "string",
  "weapon2": "string",
  "weapon3": "string",
  "equipment1": "string",
  "equipment2": "string",
  "devotionPoints": 0,
  "classType": 0,
  "faction": 0
}
```

**Status Codes:**
- `200 OK` - Character retrieved
- `400 Bad Request` - Missing characterId or sessionToken
- `401 Unauthorized` - Invalid session token
- `404 Not Found` - Character not found or not owned by player
- `500 Internal Server Error` - Database error

---

### POST /player/newCharacter
Create a new character for the authenticated player.

**Request Body:**
```json
{
  "sessionToken": "string",
  "name": "string",
  "skinKey": "string",
  "weapon1": "string",
  "weapon2": "string",
  "weapon3": "string",
  "equipment1": "string",
  "equipment2": "string",
  "devotionPoints": 0,
  "classType": 0,
  "faction": 0
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "character created",
  "characterId": "uuid"
}
```

**Status Codes:**
- `201 Created` - Character created successfully
- `400 Bad Request` - Missing required fields
- `401 Unauthorized` - Invalid session token
- `500 Internal Server Error` - Database error

---

### POST /player/updateCharacter
Update an existing character.

**Request Body:**
```json
{
  "sessionToken": "string",
  "characterId": "string",
  "name": "string",
  "weapon1": "string",
  "weapon2": "string",
  "weapon3": "string",
  "equipment1": "string",
  "equipment2": "string"
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "character updated"
}
```

**Status Codes:**
- `200 OK` - Character updated
- `400 Bad Request` - Missing required fields
- `401 Unauthorized` - Invalid session token
- `404 Not Found` - Character not found or not owned by player
- `500 Internal Server Error` - Database error

---

### POST /player/deleteCharacter
Delete a character permanently.

**Request Body:**
```json
{
  "sessionToken": "string",
  "characterId": "string"
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "character deleted"
}
```

**Status Codes:**
- `200 OK` - Character deleted
- `400 Bad Request` - Missing characterId or sessionToken
- `401 Unauthorized` - Invalid session token
- `404 Not Found` - Character not found or not owned by player
- `500 Internal Server Error` - Database error

---

### POST /player/setActiveCharacter
Set the active character for the authenticated player. The active character determines the faction for any party the player creates or joins.

**Request Body:**
```json
{
  "sessionToken": "string",
  "characterId": "string"
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "active character set",
  "characterId": "uuid",
  "faction": 0
}
```

**Status Codes:**
- `200 OK` - Active character set successfully
- `400 Bad Request` - Missing characterId or sessionToken
- `401 Unauthorized` - Invalid session token
- `404 Not Found` - Character not found or not owned by player
- `500 Internal Server Error` - Database error

---

### POST /player/friendRequest
Send a friend request to another player.

**Request Body:**
```json
{
  "sessionToken": "string",
  "targetPlayerId": "string"
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "friend request sent"
}
```

**Status Codes:**
- `200 OK` - Friend request sent
- `400 Bad Request` - Missing fields
- `401 Unauthorized` - Invalid session token
- `404 Not Found` - Target player not found
- `409 Conflict` - Already friends or duplicate request
- `500 Internal Server Error` - Database error

---

### POST /player/acceptRejectFriendRequest
Accept or reject a friend request.

**Request Body:**
```json
{
  "sessionToken": "string",
  "connectionId": "string",
  "action": "accept|reject"
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "action accepted"
}
```

**Status Codes:**
- `200 OK` - Action processed
- `400 Bad Request` - Missing fields or invalid action
- `401 Unauthorized` - Invalid session token
- `404 Not Found` - Connection not found
- `500 Internal Server Error` - Database error

---

## Matchmaking API Endpoints

### POST /matchmaking/queue
Join a matchmaking queue with a party or as a solo player.

**Request Body:**
```json
{
  "sessionToken": "string"
}
```

**Response:**
```json
{
  "status": "queued",
  "partyId": "uuid",
  "ticketId": "hex-string",
  "ticketIds": ["hex-string1", "hex-string2"],
  "ticketAssignments": [
    {
      "playerId": "uuid",
      "username": "string",
      "ticketId": "hex-string"
    }
  ]
}
```

**Status Codes:**
- `202 Accepted` - Successfully queued
- `400 Bad Request` - Missing sessionToken
- `401 Unauthorized` - Invalid session token
- `500 Internal Server Error` - Server error

---

### GET /matchmaking/status?sessionToken=X&ticketId=Y
### POST /matchmaking/status
Check the status of a matchmaking ticket.

**Query Parameters (GET):**
- `sessionToken` - Player's session token
- `ticketId` - Matchmaking ticket ID

**Request Body (POST):**
```json
{
  "sessionToken": "string",
  "ticketId": "string"
}
```

**Response:**
```json
{
  "status": "searching|matched|error",
  "tickets": [
    {
      "playerId": "uuid",
      "username": "string",
      "ticketId": "hex-string",
      "status": "searching|matched",
      "partyId": "uuid",
      "instance": null
    }
  ],
  "members": [
    {
      "playerId": "uuid",
      "username": "string"
    }
  ]
}
```

**Status Codes:**
- `200 OK` - Status retrieved
- `400 Bad Request` - Missing parameters
- `401 Unauthorized` - Invalid session token
- `404 Not Found` - Ticket not found

---

### POST /matchmaking/join
Join a matched game instance.

**Request Body:**
```json
{
  "ticketId": "string"
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "join accepted",
  "instance": {
    "id": "uuid",
    "address": "container-id",
    "port": "5000",
    "joinKey": "hex-string"
  }
}
```

**Status Codes:**
- `200 OK` - Successfully joined instance
- `400 Bad Request` - Missing ticketId
- `404 Not Found` - Ticket or instance not found
- `500 Internal Server Error` - Server error

---

### POST /matchmaking/cancel
Cancel a queued ticket and leave the queue.

**Request Body:**
```json
{
  "sessionToken": "string"
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "ticket cancelled"
}
```

**Status Codes:**
- `200 OK` - Ticket cancelled
- `400 Bad Request` - Missing sessionToken
- `401 Unauthorized` - Invalid session token

---

### POST /matchmaking/heartbeat
Send a keep-alive heartbeat for an active queue entry.

**Request Body:**
```json
{
  "sessionToken": "string"
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "heartbeat received"
}
```

**Status Codes:**
- `200 OK` - Heartbeat accepted
- `400 Bad Request` - Missing sessionToken
- `401 Unauthorized` - Invalid session token
- `404 Not Found` - No active queue entry

---

### POST /matchmaking/joined
Notify server that player successfully joined a game instance.

**Request Body:**
```json
{
  "sessionToken": "string"
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "joined acknowledged"
}
```

**Status Codes:**
- `200 OK` - Joined acknowledged
- `400 Bad Request` - Missing sessionToken
- `401 Unauthorized` - Invalid session token

---

### POST /matchmaking/left
Notify server that player left a game instance.

**Request Body:**
```json
{
  "sessionToken": "string"
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "left acknowledged"
}
```

**Status Codes:**
- `200 OK` - Left acknowledged
- `400 Bad Request` - Missing sessionToken
- `401 Unauthorized` - Invalid session token

---

### POST /matchmaking/match/ended
Notify server that a match has ended (called by game server).

**Request Body:**
```json
{
  "registrationKey": "string",
  "instanceId": "string",
  "winners": ["playerId1", "playerId2"],
  "losers": ["playerId3"]
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "match ended"
}
```

**Status Codes:**
- `200 OK` - Match end recorded
- `400 Bad Request` - Missing fields
- `401 Unauthorized` - Invalid registration key

---

### POST /matchmaking/server/register
Register a new game server instance (called by game server).

**Request Body:**
```json
{
  "serverName": "string",
  "region": "NA|EU|APAC",
  "port": "string"
}
```

**Response:**
```json
{
  "status": "ok",
  "registrationKey": "hex-string"
}
```

**Status Codes:**
- `201 Created` - Server registered successfully
- `400 Bad Request` - Missing fields
- `500 Internal Server Error` - Server error

---

### POST /matchmaking/party/invite
Send a party invite to another player.

**Request Body:**
```json
{
  "sessionToken": "string",
  "targetPlayerId": "string"
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "party invite sent"
}
```

**Status Codes:**
- `200 OK` - Invite sent
- `400 Bad Request` - Missing fields
- `401 Unauthorized` - Invalid session token
- `404 Not Found` - Target player not found

---

### GET /matchmaking/party/invites
Retrieve pending party invites for the authenticated player.

**Query Parameters:**
- `sessionToken` - Player's session token

**Response:**
```json
{
  "invites": [
    {
      "inviteId": "uuid",
      "fromPlayerId": "uuid",
      "fromPlayerName": "string",
      "sentAt": "2026-03-20T14:30:00Z"
    }
  ]
}
```

**Status Codes:**
- `200 OK` - Invites retrieved
- `401 Unauthorized` - Invalid session token

---

### POST /matchmaking/party/respond
Accept or reject a party invite.

**Request Body:**
```json
{
  "sessionToken": "string",
  "inviteId": "string",
  "action": "accept|reject"
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "action accepted"
}
```

**Status Codes:**
- `200 OK` - Action processed
- `400 Bad Request` - Invalid action
- `401 Unauthorized` - Invalid session token
- `404 Not Found` - Invite not found

---

### POST /matchmaking/party/leave
Leave the current party.

**Request Body:**
```json
{
  "sessionToken": "string"
}
```

**Response:**
```json
{
  "status": "ok",
  "message": "left party"
}
```

**Status Codes:**
- `200 OK` - Left party successfully
- `400 Bad Request` - Missing sessionToken
- `401 Unauthorized` - Invalid session token

---

### GET /matchmaking/party/status
### POST /matchmaking/party/status
Retrieve current party status and member information.

**Query Parameters (GET):**
- `sessionToken` - Player's session token

**Request Body (POST):**
```json
{
  "sessionToken": "string"
}
```

**Response:**
```json
{
  "partyId": "uuid",
  "leaderId": "uuid",
  "leaderName": "string",
  "partyFaction": 0,
  "members": [
    {
      "playerId": "uuid",
      "username": "string",
      "characterId": "uuid",
      "faction": 0
    }
  ],
  "createdAt": "2026-03-20T14:30:00Z"
}
```

**Status Codes:**
- `200 OK` - Party status retrieved
- `400 Bad Request` - Missing sessionToken
- `401 Unauthorized` - Invalid session token
- `404 Not Found` - Player has no party

---

## Game Server API Endpoints

### POST /game/server/start
Start a new game server instance with players (called by matchmaking service).

**Request Body:**
```json
{
  "players": [
    {
      "id": "uuid",
      "displayName": "string"
    }
  ],
  "port": "5000"
}
```

**Response:**
```json
{
  "id": "uuid",
  "address": "container-id",
  "port": "5000",
  "joinKey": "hex-string",
  "createdAt": "2026-03-20T14:30:00Z"
}
```

**Status Codes:**
- `201 Created` - Instance started successfully
- `400 Bad Request` - Invalid request body
- `500 Internal Server Error` - Failed to start instance

---

## Error Handling

All errors follow the standard error response format with appropriate HTTP status codes:

| Status Code | Meaning |
|-------------|---------|
| 200 | Request succeeded |
| 201 | Resource created |
| 202 | Request accepted (async operation) |
| 400 | Bad request (invalid data or missing fields) |
| 401 | Unauthorized (invalid session token) |
| 404 | Not found (resource doesn't exist) |
| 405 | Method not allowed (wrong HTTP method) |
| 409 | Conflict (duplicate data or incompatible state) |
| 500 | Internal server error |

---

## Authentication

Most endpoints require a `sessionToken` obtained from `/player/login`. Include it in the request body or query parameters. Session tokens expire after a configured duration.

---

## Rate Limiting

No rate limiting is currently implemented. Each endpoint processes requests immediately.

---

## Versioning

API version is tracked in the `/info` endpoint's `version` field (e.g., "dev" or "release").

---

## Database Schema

### Player Database
- **players** - Account credentials and account level
- **session_tokens** - Active session tokens with expiration
- **characters** - Character data (weapons, equipment, faction, class)
- **active_characters** - Tracks which character is currently active per player
- **friend_connections** - Friend relationships and pending requests

### Matchmaking Database
- **parties** - Party metadata and faction tracking
- **party_players** - Party membership with active character tracking
- **queue_tickets** - Matchmaking queue entries
- **game_instances** - Active game server instances
- **server_registrations** - Registered game servers

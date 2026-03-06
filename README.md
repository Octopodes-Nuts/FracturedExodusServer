# Fractured Exodus Server

Minimal HTTP API scaffold with a pluggable interface in `main.go`.

## Endpoints
- `GET /health` → `{ "status": "ok" }`
- `GET /info` → `{ "service": "FracturedExodusServer", "startedAt": "..." }`
- `POST /player/login` → `{ "status": "ok", "message": "login accepted", "issuedAt": "..." }`
- `GET /player/accountInfo` → `{ "accountId": "demo-account", "displayName": "Pilot", "region": "NA", "version": "dev" }`
- `GET /player/equipmentAndCharacters` → `{ "equipment": [], "characters": [] }`
- `POST /matchmaking/queue` → `{ "status": "queued", "ticketId": "...", "region": "NA" }`
- `GET /matchmaking/status?ticketId=...` → `{ "status": "searching|matched|error", "ticketId": "...", "region": "NA", "instance": { ... } }`
- `POST /matchmaking/cancel?ticketId=...` → `{ "status": "cancelled", "ticketId": "..." }`
- `POST /game/server/start` → `{ "id": "...", "containerId": "...", "ports": { ... }, "players": [ ... ] }`

## Run
```bash
go run ./cmd/server --run
```

## Configure
- `PORT` (default `8080`)
- `GAME_IMAGE` (default `fractured-exodus-game:dev`)
- `GAME_DOCKERFILE` (default `docker/dev.Dockerfile`)
- `GAME_BUILD_CONTEXT` (default `.`)

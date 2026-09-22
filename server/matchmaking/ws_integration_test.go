package matchmaking_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"

	"fracturedexodusserver/server"
	mm "fracturedexodusserver/server/matchmaking"
	"fracturedexodusserver/server/playerhandling"
	ws "fracturedexodusserver/server/ws"
)

// fakeIntegrationManager is a minimal MatchmakingManager double, mirroring
// fakeMatchmakingManager in matchmaking_test.go, used so match creation in these WS tests
// doesn't need a real game server binary.
type fakeIntegrationManager struct {
	mu    sync.Mutex
	calls int
}

func (m *fakeIntegrationManager) StartGameInstance(ctx context.Context, players []server.Player, requestedPort string) (server.GameInstance, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return server.GameInstance{
		ID:       "ws-integration-instance",
		Host:     "127.0.0.1",
		Port:     "7788",
		Protocol: "udp",
		JoinKey:  "join-key",
	}, nil
}

func (m *fakeIntegrationManager) ListInstances() []server.GameInstance {
	return nil
}

// wsTestServer bundles the httptest server, the MatchmakingAPI and the PlayerAPI, wired up the
// same way cmd/server/main.go wires them (hub shared between both APIs, /ws routed on the same
// mux as the HTTP endpoints).
type wsTestServer struct {
	httpServer *httptest.Server
	mmAPI      *mm.MatchmakingAPI
	playerAPI  *playerhandling.PlayerAPI
}

func newWSTestServer(t *testing.T) *wsTestServer {
	t.Helper()

	ctx := context.Background()

	playerDB, err := server.GetDatabase(ctx)
	if err != nil {
		t.Fatalf("get player db: %v", err)
	}
	if err := server.ResetDB(ctx, playerDB.DB); err != nil {
		t.Fatalf("reset player db: %v", err)
	}
	if err := server.InitDB(ctx, playerDB.DB); err != nil {
		t.Fatalf("init player db: %v", err)
	}

	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		t.Fatalf("get mm db: %v", err)
	}
	var initErr error
	for attempt := 0; attempt < 3; attempt++ {
		initErr = server.InitMMDB(ctx, mmDB)
		if initErr == nil {
			break
		}
		if strings.Contains(initErr.Error(), "deadlock detected") {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		t.Fatalf("init mm db: %v", initErr)
	}
	if initErr != nil {
		t.Fatalf("init mm db after retries: %v", initErr)
	}

	manager := &fakeIntegrationManager{}
	mmAPI := mm.NewMatchmakingAPI("NA", manager)
	mmAPI.SetMatchSize(2)
	mmAPI.SetMatchStartWaitForTesting(200 * time.Millisecond)

	playerAPI := playerhandling.NewPlayerAPI("ws-integration-test")

	hub := ws.NewHub()
	mmAPI.SetHub(hub)
	playerAPI.SetHub(hub)

	router := ws.Router{}
	for msgType, handler := range playerAPI.WSHandlers() {
		router[msgType] = handler
	}
	for msgType, handler := range mmAPI.WSHandlers() {
		router[msgType] = handler
	}

	mux := http.NewServeMux()
	playerAPI.RegisterRoutes(mux)
	mmAPI.RegisterRoutes(mux)
	upgrader := gorillaws.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	ws.RegisterHandlers(mux, upgrader, router)

	httpServer := httptest.NewServer(mux)

	t.Cleanup(func() {
		httpServer.Close()
		mmAPI.Close()
	})

	return &wsTestServer{httpServer: httpServer, mmAPI: mmAPI, playerAPI: playerAPI}
}

// wsEnvelope mirrors ws.OutboundMessage for decoding replies/pushes in tests.
type wsEnvelope struct {
	Type    string          `json:"type"`
	ReqID   string          `json:"reqId"`
	OK      bool            `json:"ok"`
	Payload json.RawMessage `json:"payload"`
	Error   string          `json:"error"`
}

// wsClient wraps a raw websocket connection plus a local FIFO of messages that were read off
// the wire but didn't match what a prior expectType call was looking for. Pushes can arrive
// interleaved with replies on the same conn (e.g. matchmaking.queue's own "searching" push to
// the requester races the reply to that same request), so a naive "skip anything that doesn't
// match" reader would silently discard messages a later assertion still needs. Buffering keeps
// every message available to be consumed exactly once, in arrival order.
type wsClient struct {
	t        *testing.T
	conn     *gorillaws.Conn
	buffered []wsEnvelope
}

func (s *wsTestServer) dial(t *testing.T) *wsClient {
	t.Helper()
	url := "ws" + strings.TrimPrefix(s.httpServer.URL, "http") + "/ws"
	conn, _, err := gorillaws.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &wsClient{t: t, conn: conn}
}

func (c *wsClient) send(msgType string, reqID string, payload any) {
	c.t.Helper()
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		c.t.Fatalf("marshal payload: %v", err)
	}
	msg := map[string]any{
		"type":    msgType,
		"reqId":   reqID,
		"payload": json.RawMessage(payloadBytes),
	}
	if err := c.conn.WriteJSON(msg); err != nil {
		c.t.Fatalf("write ws message: %v", err)
	}
}

// readRaw returns the next message for this conn: from the local buffer first (oldest first),
// falling back to a blocking socket read.
func (c *wsClient) readRaw() wsEnvelope {
	c.t.Helper()
	if len(c.buffered) > 0 {
		env := c.buffered[0]
		c.buffered = c.buffered[1:]
		return env
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var env wsEnvelope
	if err := c.conn.ReadJSON(&env); err != nil {
		c.t.Fatalf("read ws message: %v", err)
	}
	return env
}

// recv reads exactly the next message (buffered or off the wire), with no type filtering.
func (c *wsClient) recv() wsEnvelope {
	c.t.Helper()
	return c.readRaw()
}

// expectType reads messages until one of msgType is found, re-buffering any others in their
// original relative order (ahead of whatever is already buffered) so a later expectType/recv
// call can still consume them.
func (c *wsClient) expectType(msgType string) wsEnvelope {
	c.t.Helper()
	var skipped []wsEnvelope
	for i := 0; i < 20; i++ {
		env := c.readRaw()
		if env.Type == msgType {
			c.buffered = append(skipped, c.buffered...)
			return env
		}
		skipped = append(skipped, env)
	}
	c.t.Fatalf("gave up waiting for message type %q after 20 messages", msgType)
	return wsEnvelope{}
}

// expectNType collects the next n messages of msgType, in arrival order, via repeated
// expectType calls (each of which preserves any interleaved non-matching messages for later).
func (c *wsClient) expectNType(msgType string, n int) []wsEnvelope {
	c.t.Helper()
	envs := make([]wsEnvelope, 0, n)
	for len(envs) < n {
		envs = append(envs, c.expectType(msgType))
	}
	return envs
}

func insertPlayerWithSession(t *testing.T, ctx context.Context, playerDB *server.Database, playerID string, sessionToken string) {
	t.Helper()
	if _, err := playerDB.DB.ExecContext(ctx,
		"INSERT INTO players (id, password, account_name) VALUES ($1, $2, $3)",
		playerID, "pw", playerID,
	); err != nil {
		t.Fatalf("insert player %s: %v", playerID, err)
	}
	if _, err := playerDB.DB.ExecContext(ctx,
		"INSERT INTO session_tokens (player_id, session_token, expiration) VALUES ($1, $2, $3)",
		playerID, sessionToken, time.Now().UTC().Add(30*time.Minute),
	); err != nil {
		t.Fatalf("insert session for %s: %v", playerID, err)
	}
}

func assertStatusField(t *testing.T, env wsEnvelope, key string, want string) {
	t.Helper()
	if env.Type != "matchmaking.status" {
		t.Fatalf("expected matchmaking.status push, got type %q", env.Type)
	}
	var payload map[string]any
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("decode push payload: %v", err)
	}
	if got, _ := payload[key].(string); got != want {
		t.Fatalf("expected push %s=%q, got %+v", key, want, payload)
	}
}

// Covers: "unauthenticated conn gets rejected on matchmaking.queue" — every message type other
// than account.login/account.createAccount/account.authenticate must get {"ok": false, "error":
// "unauthenticated"} on an unbound conn, never a silent drop.
func TestWSUnauthenticatedConnRejectedOnMatchmakingQueue(t *testing.T) {
	srv := newWSTestServer(t)
	conn := srv.dial(t)

	conn.send("matchmaking.queue", "req-1", map[string]any{})
	env := conn.recv()

	if env.Type != "matchmaking.queue" || env.ReqID != "req-1" {
		t.Fatalf("expected echoed type/reqId, got %+v", env)
	}
	if env.OK {
		t.Fatalf("expected ok=false for unauthenticated conn, got %+v", env)
	}
	if env.Error != "unauthenticated" {
		t.Fatalf("expected error 'unauthenticated', got %q", env.Error)
	}
}

// Covers: "account.login binds a conn" — after a successful login reply, a subsequent
// bound-only message (account.getInfoUpdate) must succeed rather than being rejected.
func TestWSAccountLoginBindsConn(t *testing.T) {
	ctx := context.Background()
	srv := newWSTestServer(t)

	playerDB, err := server.GetDatabase(ctx)
	if err != nil {
		t.Fatalf("get player db: %v", err)
	}
	playerID := "ws-login-player"
	if _, err := playerDB.DB.ExecContext(ctx,
		"INSERT INTO players (id, password, account_name) VALUES ($1, $2, $3)",
		playerID, "s3cret", "ws-login-user",
	); err != nil {
		t.Fatalf("insert player: %v", err)
	}

	conn := srv.dial(t)
	conn.send("account.login", "login-1", map[string]any{
		"username": "ws-login-user",
		"password": "s3cret",
	})
	env := conn.recv()
	if !env.OK {
		t.Fatalf("expected login to succeed, got %+v", env)
	}
	var loginPayload map[string]any
	if err := json.Unmarshal(env.Payload, &loginPayload); err != nil {
		t.Fatalf("decode login payload: %v", err)
	}
	if loginPayload["accountId"] != playerID {
		t.Fatalf("expected accountId %q, got %v", playerID, loginPayload["accountId"])
	}
	if sessionToken, _ := loginPayload["sessionToken"].(string); sessionToken == "" {
		t.Fatalf("expected non-empty sessionToken in login payload")
	}

	// The conn should now be bound: a bound-only message must succeed.
	conn.send("account.getInfoUpdate", "info-1", map[string]any{})
	infoEnv := conn.recv()
	if !infoEnv.OK {
		t.Fatalf("expected account.getInfoUpdate to succeed after login, got %+v", infoEnv)
	}
}

// Covers: "account.authenticate rebinds a fresh conn from a prior session token" — a session
// token minted independently of this conn (e.g. from a prior HTTP/WS login) can bind a brand
// new conn via account.authenticate.
func TestWSAccountAuthenticateRebindsFreshConn(t *testing.T) {
	ctx := context.Background()
	srv := newWSTestServer(t)

	playerDB, err := server.GetDatabase(ctx)
	if err != nil {
		t.Fatalf("get player db: %v", err)
	}
	playerID := "ws-authenticate-player"
	sessionToken := "ws-authenticate-session"
	insertPlayerWithSession(t, ctx, playerDB, playerID, sessionToken)

	// A brand new conn that never logged in on this connection.
	conn := srv.dial(t)
	conn.send("account.authenticate", "auth-1", map[string]any{
		"sessionToken": sessionToken,
	})
	env := conn.recv()
	if !env.OK {
		t.Fatalf("expected account.authenticate to succeed, got %+v", env)
	}
	var payload map[string]any
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("decode authenticate payload: %v", err)
	}
	if payload["playerId"] != playerID {
		t.Fatalf("expected playerId %q, got %v", playerID, payload["playerId"])
	}

	conn.send("account.getInfoUpdate", "info-1", map[string]any{})
	infoEnv := conn.recv()
	if !infoEnv.OK {
		t.Fatalf("expected account.getInfoUpdate to succeed after authenticate, got %+v", infoEnv)
	}
}

// Covers: "two party members queueing both receive matchmaking.status 'matched' pushes with no
// polling involved" — only one party member sends matchmaking.queue over WS; both members'
// conns (bound via account.authenticate) must receive unsolicited matchmaking.status pushes,
// first "searching" then "matched" once the match loop forms a match, without either of them
// ever sending matchmaking.status themselves.
func TestWSPartyMembersReceiveMatchedPushesWithoutPolling(t *testing.T) {
	ctx := context.Background()
	srv := newWSTestServer(t)

	playerDB, err := server.GetDatabase(ctx)
	if err != nil {
		t.Fatalf("get player db: %v", err)
	}
	mmDB, err := server.GetMMDB(ctx)
	if err != nil {
		t.Fatalf("get mm db: %v", err)
	}

	uniqueSuffix := fmt.Sprintf("%d", time.Now().UnixNano())
	playerOne := "ws-party-one-" + uniqueSuffix
	playerTwo := "ws-party-two-" + uniqueSuffix
	sessionOne := "ws-party-session-one-" + uniqueSuffix
	sessionTwo := "ws-party-session-two-" + uniqueSuffix
	partyID := "ws-party-" + uniqueSuffix

	insertPlayerWithSession(t, ctx, playerDB, playerOne, sessionOne)
	insertPlayerWithSession(t, ctx, playerDB, playerTwo, sessionTwo)

	if _, err := mmDB.DB.ExecContext(ctx,
		"INSERT INTO parties (party_id, active_faction, faction, primary_player_id) VALUES ($1, $2, $3, $4)",
		partyID, 0, 0, playerOne,
	); err != nil {
		t.Fatalf("insert party: %v", err)
	}
	if _, err := mmDB.DB.ExecContext(ctx,
		"INSERT INTO party_players (party_id, player_id, active_character_id) VALUES ($1, $2, $3)",
		partyID, playerOne, nil,
	); err != nil {
		t.Fatalf("insert party player one: %v", err)
	}
	if _, err := mmDB.DB.ExecContext(ctx,
		"INSERT INTO party_players (party_id, player_id, active_character_id) VALUES ($1, $2, $3)",
		partyID, playerTwo, nil,
	); err != nil {
		t.Fatalf("insert party player two: %v", err)
	}

	connOne := srv.dial(t)
	connOne.send("account.authenticate", "auth-1", map[string]any{"sessionToken": sessionOne})
	if env := connOne.recv(); !env.OK {
		t.Fatalf("expected player one authenticate to succeed, got %+v", env)
	}

	connTwo := srv.dial(t)
	connTwo.send("account.authenticate", "auth-1", map[string]any{"sessionToken": sessionTwo})
	if env := connTwo.recv(); !env.OK {
		t.Fatalf("expected player two authenticate to succeed, got %+v", env)
	}

	// Only player one sends matchmaking.queue; player two never sends anything else. The
	// requester's own conn also gets the "searching" push as a side effect of the same handler
	// call, so its reply (type "matchmaking.queue") and that push can arrive in either order —
	// expectType looks past (and preserves) whichever comes first.
	connOne.send("matchmaking.queue", "queue-1", map[string]any{})
	queueEnv := connOne.expectType("matchmaking.queue")
	if !queueEnv.OK {
		t.Fatalf("expected matchmaking.queue to succeed, got %+v", queueEnv)
	}

	// Both conns should receive an unsolicited "searching" push, then a "matched" push once the
	// match loop fires, without either of them polling matchmaking.status.
	pushesOne := connOne.expectNType("matchmaking.status", 2)
	assertStatusField(t, pushesOne[0], "status", "searching")
	assertStatusField(t, pushesOne[1], "status", "matched")

	pushesTwo := connTwo.expectNType("matchmaking.status", 2)
	assertStatusField(t, pushesTwo[0], "status", "searching")
	assertStatusField(t, pushesTwo[1], "status", "matched")
}

// Covers: "a party invite delivers an unsolicited matchmaking.party.status push to the invitee
// without it asking" — the invitee never sends matchmaking.party.status (or anything else)
// themselves; the push must still arrive after the inviter sends matchmaking.party.invite.
func TestWSPartyInviteDeliversUnsolicitedPushToInvitee(t *testing.T) {
	ctx := context.Background()
	srv := newWSTestServer(t)

	playerDB, err := server.GetDatabase(ctx)
	if err != nil {
		t.Fatalf("get player db: %v", err)
	}

	uniqueSuffix := fmt.Sprintf("%d", time.Now().UnixNano())
	inviterID := "ws-invite-inviter-" + uniqueSuffix
	inviteeID := "ws-invite-invitee-" + uniqueSuffix
	inviterSession := "ws-invite-inviter-session-" + uniqueSuffix
	inviteeSession := "ws-invite-invitee-session-" + uniqueSuffix

	insertPlayerWithSession(t, ctx, playerDB, inviterID, inviterSession)
	insertPlayerWithSession(t, ctx, playerDB, inviteeID, inviteeSession)

	inviterConn := srv.dial(t)
	inviterConn.send("account.authenticate", "auth-1", map[string]any{"sessionToken": inviterSession})
	if env := inviterConn.recv(); !env.OK {
		t.Fatalf("expected inviter authenticate to succeed, got %+v", env)
	}

	inviteeConn := srv.dial(t)
	inviteeConn.send("account.authenticate", "auth-1", map[string]any{"sessionToken": inviteeSession})
	if env := inviteeConn.recv(); !env.OK {
		t.Fatalf("expected invitee authenticate to succeed, got %+v", env)
	}

	// Inviter sends the invite; invitee sends nothing at all from here on.
	inviterConn.send("matchmaking.party.invite", "invite-1", map[string]any{"playerId": inviteeID})
	inviteEnv := inviterConn.expectType("matchmaking.party.invite")
	if !inviteEnv.OK {
		t.Fatalf("expected matchmaking.party.invite to succeed, got %+v", inviteEnv)
	}

	pushEnv := inviteeConn.expectType("matchmaking.party.status")
	var payload map[string]any
	if err := json.Unmarshal(pushEnv.Payload, &payload); err != nil {
		t.Fatalf("decode push payload: %v", err)
	}
	if pushEnv.ReqID != "" {
		t.Fatalf("expected an unsolicited push with no reqId, got reqId=%q", pushEnv.ReqID)
	}
	inbound, ok := payload["inboundInvites"].([]any)
	if !ok || len(inbound) != 1 {
		t.Fatalf("expected exactly one inbound invite in the push, got %+v", payload["inboundInvites"])
	}
}

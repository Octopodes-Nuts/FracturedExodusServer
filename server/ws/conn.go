package ws

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	sendBufferSize = 32
	writeWait      = 10 * time.Second
)

// Conn wraps a single *websocket.Conn plus the auth-binding and outbound queue needed to route
// messages for it. A Conn starts unbound (playerID == ""); only the unbound-allowed message
// types (see Router) may be handled before it is bound via Bind.
type Conn struct {
	ws *websocket.Conn

	mu           sync.RWMutex
	playerID     string
	sessionToken string
	hub          *Hub
	closed       bool

	send chan OutboundMessage
}

// NewConn wraps an already-upgraded websocket connection.
func NewConn(wsConn *websocket.Conn) *Conn {
	return &Conn{
		ws:   wsConn,
		send: make(chan OutboundMessage, sendBufferSize),
	}
}

// PlayerID returns the currently bound player ID, or "" if the conn is unbound.
func (c *Conn) PlayerID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.playerID
}

// SessionToken returns the raw session token bound to this conn, or "" if unbound. Several
// existing core functions (e.g. resolveQueueContextFromSession) take a session token rather
// than a player ID, so handlers pass this straight through instead of rederiving one.
func (c *Conn) SessionToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionToken
}

// Bind associates this conn with playerID/sessionToken (following a successful
// account.login/account.createAccount+login/account.authenticate), or clears the binding when
// called with empty strings (following account.logout).
func (c *Conn) Bind(playerID string, sessionToken string) {
	c.mu.Lock()
	c.playerID = playerID
	c.sessionToken = sessionToken
	c.mu.Unlock()
}

// attachHub records the Hub a conn was last registered with, purely so the conn can
// unregister itself from that Hub when its connection closes (readPump's defer calls
// detachFromHub). This is an internal bookkeeping detail; callers never need to touch it.
func (c *Conn) attachHub(h *Hub) {
	c.mu.Lock()
	c.hub = h
	c.mu.Unlock()
}

// detachFromHub unregisters the conn from whatever Hub it was last attached to, under
// whatever playerID it was bound to at the time. Safe to call multiple times or on a conn
// that was never registered.
func (c *Conn) detachFromHub() {
	c.mu.RLock()
	h := c.hub
	playerID := c.playerID
	c.mu.RUnlock()
	if h != nil && playerID != "" {
		h.Unregister(playerID, c)
	}
}

// Send queues msg for delivery on the write pump. It never blocks: if the send buffer is full
// (a stuck/slow client) the message is dropped rather than stalling the caller — this matters
// because pushes originate from matchmaking/party code paths that must never block on a
// client's network behavior.
func (c *Conn) Send(msg OutboundMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.send <- msg:
	default:
		log.Printf("[ws] dropping outbound message, send buffer full: type=%s", msg.Type)
	}
}

// closeSend marks the conn closed and closes the send channel exactly once. Closing under the
// same mutex Send() uses guarantees no send-on-closed-channel panic can race with Send.
func (c *Conn) closeSend() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	close(c.send)
}

// writePump is the only goroutine allowed to call WriteMessage/WriteJSON on the underlying
// websocket connection, as required by gorilla/websocket (concurrent writers are not safe).
func (c *Conn) writePump() {
	defer func() {
		_ = c.ws.Close()
	}()
	for msg := range c.send {
		_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
		if err := c.ws.WriteJSON(msg); err != nil {
			return
		}
	}
}

// readPump reads inbound frames, decodes them as InboundMessage, dispatches to the matching
// handler in router (enforcing the unbound-allowlist), and queues a reply on send. It returns
// when the connection is closed by the client or errors out, at which point it unregisters the
// conn from its Hub (if any) and closes the send channel so writePump can exit.
func (c *Conn) readPump(router Router) {
	defer func() {
		c.detachFromHub()
		c.closeSend()
	}()

	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}

		var inbound InboundMessage
		if err := json.Unmarshal(data, &inbound); err != nil {
			c.Send(OutboundMessage{OK: false, Error: "invalid message"})
			continue
		}

		handler, ok := router[inbound.Type]
		if !ok {
			c.Send(OutboundMessage{Type: inbound.Type, ReqID: inbound.ReqID, OK: false, Error: "unknown message type"})
			continue
		}

		if c.PlayerID() == "" && !unboundAllowed[inbound.Type] {
			c.Send(OutboundMessage{Type: inbound.Type, ReqID: inbound.ReqID, OK: false, Error: "unauthenticated"})
			continue
		}

		payload, err := handler(context.Background(), c, inbound.Payload)
		if err != nil {
			c.Send(OutboundMessage{Type: inbound.Type, ReqID: inbound.ReqID, OK: false, Error: err.Error()})
			continue
		}

		c.Send(OutboundMessage{Type: inbound.Type, ReqID: inbound.ReqID, OK: true, Payload: payload})
	}
}

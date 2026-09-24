package ws

import (
	"testing"
	"time"
)

// newTestConn builds a Conn with no underlying websocket connection, suitable for exercising
// Hub/Conn plumbing (Send/attachHub/detachFromHub) without a real network connection.
func newTestConn() *Conn {
	return &Conn{send: make(chan OutboundMessage, sendBufferSize)}
}

func drain(t *testing.T, c *Conn) OutboundMessage {
	t.Helper()
	select {
	case msg := <-c.send:
		return msg
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for message")
		return OutboundMessage{}
	}
}

func assertEmpty(t *testing.T, c *Conn) {
	t.Helper()
	select {
	case msg := <-c.send:
		t.Fatalf("expected no message, got %+v", msg)
	default:
	}
}

func TestHubSendToReachesRegisteredConn(t *testing.T) {
	hub := NewHub()
	conn := newTestConn()

	hub.Register("player-1", conn)
	hub.SendTo("player-1", OutboundMessage{Type: "matchmaking.status", OK: true})

	msg := drain(t, conn)
	if msg.Type != "matchmaking.status" {
		t.Fatalf("expected matchmaking.status, got %q", msg.Type)
	}
}

func TestHubSendToUnregisteredPlayerIsNoop(t *testing.T) {
	hub := NewHub()
	conn := newTestConn()
	hub.Register("player-1", conn)

	// Sending to a playerID nobody registered under must not panic and must not reach conn.
	hub.SendTo("player-nobody", OutboundMessage{Type: "matchmaking.status"})
	assertEmpty(t, conn)
}

func TestHubUnregisterStopsDelivery(t *testing.T) {
	hub := NewHub()
	conn := newTestConn()

	hub.Register("player-1", conn)
	hub.Unregister("player-1", conn)
	hub.SendTo("player-1", OutboundMessage{Type: "matchmaking.status"})

	assertEmpty(t, conn)
}

func TestHubMultiConnPerPlayerFanOut(t *testing.T) {
	hub := NewHub()
	connA := newTestConn()
	connB := newTestConn()

	hub.Register("player-1", connA)
	hub.Register("player-1", connB)

	hub.SendTo("player-1", OutboundMessage{Type: "matchmaking.party.status"})

	msgA := drain(t, connA)
	msgB := drain(t, connB)
	if msgA.Type != "matchmaking.party.status" || msgB.Type != "matchmaking.party.status" {
		t.Fatalf("expected both conns to receive the push, got %+v and %+v", msgA, msgB)
	}
}

func TestHubNoCrossTalkBetweenPlayers(t *testing.T) {
	hub := NewHub()
	connA := newTestConn()
	connB := newTestConn()

	hub.Register("player-a", connA)
	hub.Register("player-b", connB)

	hub.SendTo("player-a", OutboundMessage{Type: "account.infoUpdated"})

	drain(t, connA)
	assertEmpty(t, connB)
}

func TestHubSendToManyFansOutToDistinctPlayers(t *testing.T) {
	hub := NewHub()
	connA := newTestConn()
	connB := newTestConn()
	connC := newTestConn()

	hub.Register("player-a", connA)
	hub.Register("player-b", connB)
	hub.Register("player-c", connC)

	hub.SendToMany([]string{"player-a", "player-b"}, OutboundMessage{Type: "matchmaking.status"})

	drain(t, connA)
	drain(t, connB)
	assertEmpty(t, connC)
}

func TestHubRegisterUnregisterDifferentConnsSamePlayer(t *testing.T) {
	hub := NewHub()
	connA := newTestConn()
	connB := newTestConn()

	hub.Register("player-1", connA)
	hub.Register("player-1", connB)

	// Unregistering connA must not affect delivery to connB.
	hub.Unregister("player-1", connA)
	hub.SendTo("player-1", OutboundMessage{Type: "matchmaking.status"})

	assertEmpty(t, connA)
	drain(t, connB)
}

func TestHubNilHubMethodsAreNoops(t *testing.T) {
	var hub *Hub
	conn := newTestConn()

	// Call sites are expected to nil-check, but these must not panic either way.
	hub.Register("player-1", conn)
	hub.Unregister("player-1", conn)
	hub.SendTo("player-1", OutboundMessage{Type: "x"})
	hub.SendToMany([]string{"player-1"}, OutboundMessage{Type: "x"})
}

func TestConnSendAfterCloseIsNoop(t *testing.T) {
	conn := newTestConn()
	conn.closeSend()

	// Must not panic (send on a closed channel would panic if not guarded).
	conn.Send(OutboundMessage{Type: "matchmaking.status"})
}

func TestConnDetachFromHubUnregisters(t *testing.T) {
	hub := NewHub()
	conn := newTestConn()
	conn.Bind("player-1", "session-1")
	hub.Register("player-1", conn)

	conn.detachFromHub()
	hub.SendTo("player-1", OutboundMessage{Type: "matchmaking.status"})

	assertEmpty(t, conn)
}

package ws

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gorilla/websocket"
)

// HandlerFunc handles one decoded inbound message for a bound (or, for the unbound-allowed
// types, still-unbound) Conn. The returned value becomes the reply's Payload; a non-nil error
// becomes {"ok": false, "error": err.Error()}.
type HandlerFunc func(ctx context.Context, c *Conn, payload json.RawMessage) (any, error)

// Router maps a message "type" string to the handler that serves it.
type Router map[string]HandlerFunc

// unboundAllowed lists the only message types a Conn may send before it has authenticated
// (conn.playerID == ""). Every other type gets {"ok": false, "error": "unauthenticated"} —
// never a silent drop.
var unboundAllowed = map[string]bool{
	"account.login":         true,
	"account.createAccount": true,
	"account.authenticate":  true,
}

// RegisterHandlers registers the GET /ws upgrade endpoint on mux. Each accepted connection
// gets its own Conn, with writePump and readPump running on dedicated goroutines.
func RegisterHandlers(mux *http.ServeMux, upgrader websocket.Upgrader, router Router) {
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		wsConn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		conn := NewConn(wsConn)
		go conn.writePump()
		go conn.readPump(router)
	})
}

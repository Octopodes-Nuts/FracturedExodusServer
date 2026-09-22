package ws

import "sync"

// Hub tracks live WebSocket connections by playerID so server-side code can push unsolicited
// messages to a player (or a set of players) without them polling. A single player can have
// more than one live Conn (e.g. multiple devices/tabs), so each playerID maps to a set of
// conns and a push fans out to all of them.
type Hub struct {
	mu    sync.RWMutex
	conns map[string]map[*Conn]struct{}
}

// NewHub creates an empty Hub.
func NewHub() *Hub {
	return &Hub{conns: make(map[string]map[*Conn]struct{})}
}

// Register associates c with playerID so future pushes to playerID reach it.
func (h *Hub) Register(playerID string, c *Conn) {
	if h == nil || c == nil || playerID == "" {
		return
	}
	h.mu.Lock()
	set, ok := h.conns[playerID]
	if !ok {
		set = make(map[*Conn]struct{})
		h.conns[playerID] = set
	}
	set[c] = struct{}{}
	h.mu.Unlock()

	c.attachHub(h)
}

// Unregister removes the association between c and playerID. It is safe to call even if c was
// never registered, or was registered under a different playerID.
func (h *Hub) Unregister(playerID string, c *Conn) {
	if h == nil || c == nil {
		return
	}
	h.mu.Lock()
	if set, ok := h.conns[playerID]; ok {
		delete(set, c)
		if len(set) == 0 {
			delete(h.conns, playerID)
		}
	}
	h.mu.Unlock()
}

// SendTo pushes msg to every conn currently registered for playerID. Delivery is best-effort
// and non-blocking (see Conn.Send); a slow or dead client can never stall the caller.
func (h *Hub) SendTo(playerID string, msg OutboundMessage) {
	if h == nil || playerID == "" {
		return
	}
	h.mu.RLock()
	set := h.conns[playerID]
	conns := make([]*Conn, 0, len(set))
	for c := range set {
		conns = append(conns, c)
	}
	h.mu.RUnlock()

	for _, c := range conns {
		c.Send(msg)
	}
}

// SendToMany pushes msg to every conn registered for any of playerIDs.
func (h *Hub) SendToMany(playerIDs []string, msg OutboundMessage) {
	if h == nil {
		return
	}
	for _, playerID := range playerIDs {
		h.SendTo(playerID, msg)
	}
}

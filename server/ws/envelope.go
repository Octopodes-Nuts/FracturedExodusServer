package ws

import "encoding/json"

// InboundMessage is a client -> server message envelope.
//
//	{"type": "account.login", "reqId": "c-1", "payload": {...}}
type InboundMessage struct {
	Type    string          `json:"type"`
	ReqID   string          `json:"reqId"`
	Payload json.RawMessage `json:"payload"`
}

// OutboundMessage is a server -> client message envelope, used both for replies to a
// specific InboundMessage (ReqID echoed back, OK/Error/Payload populated accordingly) and for
// unsolicited pushes (ReqID left empty, which is omitted from the wire payload).
//
//	{"type": "account.login", "reqId": "c-1", "ok": true, "payload": {...}}
//	{"type": "account.login", "reqId": "c-1", "ok": false, "error": "invalid credentials"}
type OutboundMessage struct {
	Type    string `json:"type"`
	ReqID   string `json:"reqId,omitempty"`
	OK      bool   `json:"ok"`
	Payload any    `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
}

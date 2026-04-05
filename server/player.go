package server

type Player struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Ticket   string `json:"ticket"`
}

package protocol

import "time"

// The agent is reachable only through a shared Unix socket. It never accepts
// executable paths, shell commands or arbitrary filenames from the panel.
type Status struct {
	Running       bool      `json:"running"`
	PID           int       `json:"pid"`
	StartedAt     time.Time `json:"started_at"`
	Version       string    `json:"version"`
	Pending       bool      `json:"pending"`
	PublicAddress string    `json:"public_address"`
	LastError     string    `json:"last_error,omitempty"`
}

type User struct {
	Username      string  `json:"username"`
	Enabled       bool    `json:"enabled"`
	MaxHTTP2Conns *uint32 `json:"max_http2_conns,omitempty"`
	MaxHTTP3Conns *uint32 `json:"max_http3_conns,omitempty"`
}

type Users struct {
	Users   []User `json:"users"`
	Pending bool   `json:"pending"`
}

type UserChange struct {
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
	Enabled  *bool  `json:"enabled,omitempty"`
}

type Export struct {
	Username string `json:"username"`
	TOML     string `json:"toml"`
	DeepLink string `json:"deeplink"`
}

type ClientMetric struct {
	Username string  `json:"username"`
	IP       *string `json:"ip"`
	Sessions uint64  `json:"sessions"`
	Inbound  uint64  `json:"inbound"`
	Outbound uint64  `json:"outbound"`
}

type Certificate struct {
	Hostname string    `json:"hostname"`
	NotAfter time.Time `json:"not_after"`
	Error    string    `json:"error,omitempty"`
}

type LogResponse struct {
	Lines []string `json:"lines"`
}
type Result struct {
	Message string `json:"message"`
}
type Error struct {
	Error string `json:"error"`
}

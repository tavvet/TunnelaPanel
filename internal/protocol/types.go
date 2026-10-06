// Package protocol defines the JSON messages exchanged by the panel and agent.
// The agent is reachable only through a shared Unix socket. Requests never carry
// executable paths, shell commands, or arbitrary filenames from the panel.
package protocol

import "time"

// Status combines the supervised process state with pending configuration changes.
type Status struct {
	Running       bool      `json:"running"`
	PID           int       `json:"pid"`
	StartedAt     time.Time `json:"started_at"`
	Version       string    `json:"version"`
	Pending       bool      `json:"pending"`
	PublicAddress string    `json:"public_address"`
	LastError     string    `json:"last_error,omitempty"`
}

// User is a draft user shown by the panel; passwords are never included.
// Connection limit pointers distinguish an absent setting from an explicit zero.
type User struct {
	Username      string  `json:"username"`
	Enabled       bool    `json:"enabled"`
	MaxHTTP2Conns *uint32 `json:"max_http2_conns,omitempty"`
	MaxHTTP3Conns *uint32 `json:"max_http3_conns,omitempty"`
}

// Users lists draft users, including disabled ones. Pending reports whether the
// draft differs from the applied configuration.
type Users struct {
	Users   []User `json:"users"`
	Pending bool   `json:"pending"`
}

// UserChange adds, edits, or deletes a user in the draft, depending on the HTTP method.
type UserChange struct {
	Username string `json:"username"`

	// An empty Password leaves the existing password unchanged on edit.
	Password string `json:"password,omitempty"`

	// Enabled is optional: nil preserves the enabled state on edit and enables
	// a newly added user. A pointer to false explicitly disables the user.
	Enabled *bool `json:"enabled,omitempty"`
}

// Export contains connection credentials for an enabled user in the applied
// configuration. Both formats contain secrets and must not be logged.
type Export struct {
	Username string `json:"username"`
	TOML     string `json:"toml"`
	DeepLink string `json:"deeplink"`
}

// ClientMetric is a per-client record from TrustTunnel's private metrics endpoint.
type ClientMetric struct {
	Username string  `json:"username"`
	IP       *string `json:"ip"`
	Sessions uint64  `json:"sessions"`
	Inbound  uint64  `json:"inbound"`
	Outbound uint64  `json:"outbound"`
}

// Certificate reports a configured host's certificate expiry or a read/parse error.
type Certificate struct {
	Hostname string    `json:"hostname"`
	NotAfter time.Time `json:"not_after"`
	Error    string    `json:"error,omitempty"`
}

// LogResponse contains a bounded set of process log lines with secrets redacted.
type LogResponse struct {
	Lines []string `json:"lines"`
}

// Result acknowledges a successful agent operation.
type Result struct {
	Message string `json:"message"`
}

// Error carries the agent's message for an unsuccessful request.
type Error struct {
	Error string `json:"error"`
}

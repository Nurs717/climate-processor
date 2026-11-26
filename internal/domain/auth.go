package domain

import "time"

// Role says what a client may do. The server decides it for every request: it is never taken from the client.
type Role string

const (
	// RoleDemo is read-only access for everybody. An anonymous request has it too.
	RoleDemo Role = "demo"
	// RoleOwner is the owner of the sensor: signed in with Telegram, with the higher limits and the private features.
	RoleOwner Role = "owner"
)

// Session is a sign-in of the owner. The client holds a secret token; the server keeps only ID, the hash of
// that token, so ID is safe to log and to show in a list of sessions.
type Session struct {
	ID         string // hex SHA-256 of the token
	UserID     int64  // Telegram user id of the owner
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt time.Time
	UserAgent  string // the browser or app that signed in, to tell sessions apart
}

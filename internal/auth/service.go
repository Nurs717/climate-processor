// Package auth tells who makes a request. The owner signs in once (with Telegram) and gets a session;
// everybody else is an anonymous client with the read-only demo role.
//
// A session is an opaque random token. The client keeps the token (in a cookie, or in the keychain of an app);
// the server keeps only its SHA-256, so a leaked database cannot be used to sign in. Unlike a self-contained
// token (JWT), a session can be revoked at any moment: by signing out, by "sign out everywhere", or by
// removing the owner from the access list.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"climate-processor/internal/apperr"
	"climate-processor/internal/domain"
)

const (
	// DefaultSessionTTL is how long a session lives when Options.TTL is not set.
	DefaultSessionTTL = 30 * 24 * time.Hour

	tokenBytes      = 32               // 256 bits of randomness
	maxTokenLen     = 128              // a real token has 43 characters; anything longer is not even hashed
	maxUserAgentLen = 200              // runes
	touchEvery      = 10 * time.Minute // how often a session in use gets its last-used time written
)

// SessionStore keeps the sessions. It is declared here, next to its consumer, so the service depends on
// behaviour and not on a concrete database.
type SessionStore interface {
	CreateSession(ctx context.Context, s domain.Session) error
	// Session returns the session with the given id; ok is false when there is none.
	Session(ctx context.Context, id string) (s domain.Session, ok bool, err error)
	// TouchSession moves the last-used time of a session forward (never back).
	TouchSession(ctx context.Context, id string, at time.Time) error
	// DeleteSession removes a session. A session that does not exist is not an error.
	DeleteSession(ctx context.Context, id string) error
	// DeleteUserSessions removes all sessions of a user and reports how many there were.
	DeleteUserSessions(ctx context.Context, userID int64) (int64, error)
	// DeleteExpiredSessions removes the sessions that expire at or before now and reports how many there were.
	DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error)
}

// Owners tells who is an owner. The access list of the Telegram bot implements it.
type Owners interface {
	IsAdmin(userID int64) bool
}

// Options are the optional settings of the service.
type Options struct {
	TTL time.Duration    // lifetime of a session (default DefaultSessionTTL)
	Now func() time.Time // clock, replaced in tests (default time.Now)
}

// Identity is who makes a request.
type Identity struct {
	Role      domain.Role
	UserID    int64  // Telegram user id; 0 for an anonymous client
	SessionID string // empty for an anonymous client
}

// Anonymous is the identity of a request without credentials.
var Anonymous = Identity{Role: domain.RoleDemo}

// Service creates, checks and revokes sessions. It is safe for concurrent use.
type Service struct {
	log    *slog.Logger
	store  SessionStore
	owners Owners
	ttl    time.Duration
	now    func() time.Time
}

// NewService returns a service that keeps its sessions in store and lets only the owners sign in.
func NewService(log *slog.Logger, store SessionStore, owners Owners, opts Options) *Service {
	if log == nil {
		log = slog.Default()
	}
	if opts.TTL <= 0 {
		opts.TTL = DefaultSessionTTL
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{log: log, store: store, owners: owners, ttl: opts.TTL, now: opts.Now}
}

// Create signs an owner in: it stores a new session and returns its secret token, which is shown only here
// (the server keeps just a hash). An account that is not an owner gets an apperr.CodeForbidden error.
func (s *Service) Create(ctx context.Context, userID int64, userAgent string) (token string, session domain.Session, err error) {
	const op = "auth.Create"
	if !s.owners.IsAdmin(userID) {
		return "", domain.Session{}, apperr.New(apperr.CodeForbidden, op, "this account is not the owner")
	}

	raw := make([]byte, tokenBytes)
	_, _ = rand.Read(raw) // never fails: Go stops the program if the system has no randomness
	token = base64.RawURLEncoding.EncodeToString(raw)

	now := s.now().UTC().Truncate(time.Second)
	session = domain.Session{
		ID:         sessionID(token),
		UserID:     userID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.ttl),
		LastUsedAt: now,
		UserAgent:  cleanUserAgent(userAgent),
	}
	if err = s.store.CreateSession(ctx, session); err != nil {
		return "", domain.Session{}, apperr.Wrap(err, apperr.CodeUnavailable, op, "sign-in is temporarily unavailable")
	}
	s.log.Info("session created", "session_id", session.ID, "user_id", userID, "expires_at", session.ExpiresAt)
	return token, session, nil
}

// Authenticate finds who a token belongs to. An unknown, expired or revoked token (and a token of somebody who is
// no longer an owner) gives an apperr.CodeUnauthorized error; a broken store gives apperr.CodeUnavailable, so
// that a database outage is not mistaken for a wrong token. A request without a token is Anonymous: that is
// decided by the caller, before it gets here.
func (s *Service) Authenticate(ctx context.Context, token string) (Identity, error) {
	const op = "auth.Authenticate"
	if token == "" || len(token) > maxTokenLen {
		return Identity{}, errInvalidSession(op)
	}
	session, ok, err := s.store.Session(ctx, sessionID(token))
	if err != nil {
		return Identity{}, apperr.Wrap(err, apperr.CodeUnavailable, op, "sign-in is temporarily unavailable")
	}
	now := s.now()
	switch {
	case !ok:
		return Identity{}, errInvalidSession(op)
	case !now.Before(session.ExpiresAt):
		return Identity{}, errInvalidSession(op)
	case !s.owners.IsAdmin(session.UserID):
		// The owner was removed from the access list. The session must stop working and must not come back
		// to life if the account is added again later.
		if delErr := s.store.DeleteSession(ctx, session.ID); delErr != nil {
			s.log.Warn("deleting the session of a removed owner failed", "session_id", session.ID, "error", delErr)
		}
		return Identity{}, errInvalidSession(op)
	}

	if now.Sub(session.LastUsedAt) >= touchEvery {
		if touchErr := s.store.TouchSession(ctx, session.ID, now.UTC().Truncate(time.Second)); touchErr != nil {
			// The session is valid: a failed bookkeeping write must not sign the owner out.
			s.log.Warn("updating the last-used time of a session failed", "session_id", session.ID, "error", touchErr)
		}
	}
	return Identity{Role: domain.RoleOwner, UserID: session.UserID, SessionID: session.ID}, nil
}

// Revoke signs a token out. A token that is empty or unknown is not an error: signing out twice is fine.
func (s *Service) Revoke(ctx context.Context, token string) error {
	const op = "auth.Revoke"
	if token == "" || len(token) > maxTokenLen {
		return nil
	}
	id := sessionID(token)
	if err := s.store.DeleteSession(ctx, id); err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, op, "sign-out is temporarily unavailable")
	}
	s.log.Info("session revoked", "session_id", id)
	return nil
}

// RevokeAll signs a user out everywhere and reports how many sessions there were.
func (s *Service) RevokeAll(ctx context.Context, userID int64) (int64, error) {
	const op = "auth.RevokeAll"
	n, err := s.store.DeleteUserSessions(ctx, userID)
	if err != nil {
		return 0, apperr.Wrap(err, apperr.CodeUnavailable, op, "sign-out is temporarily unavailable")
	}
	s.log.Info("all sessions of a user revoked", "user_id", userID, "count", n)
	return n, nil
}

// DeleteExpired removes the sessions that have expired and reports how many there were.
func (s *Service) DeleteExpired(ctx context.Context) (int64, error) {
	const op = "auth.DeleteExpired"
	n, err := s.store.DeleteExpiredSessions(ctx, s.now().UTC().Truncate(time.Second))
	if err != nil {
		return 0, apperr.Wrap(err, apperr.CodeUnavailable, op, "cleaning up sessions failed")
	}
	return n, nil
}

// RunCleanup deletes the expired sessions now and then every `every` until ctx is cancelled. Expired sessions
// are refused anyway: the cleanup only keeps the table small.
func (s *Service) RunCleanup(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Hour
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		n, err := s.DeleteExpired(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			s.log.Warn("cleaning up expired sessions failed", "error", err)
		case n > 0:
			s.log.Info("expired sessions deleted", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func errInvalidSession(op string) error {
	return apperr.New(apperr.CodeUnauthorized, op, "invalid or expired session")
}

// sessionID is what the server stores instead of the token.
func sessionID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// cleanUserAgent keeps a header chosen by the client within a sane size and valid UTF-8.
func cleanUserAgent(ua string) string {
	ua = strings.ToValidUTF8(ua, "")
	if utf8.RuneCountInString(ua) > maxUserAgentLen {
		ua = string([]rune(ua)[:maxUserAgentLen])
	}
	return ua
}

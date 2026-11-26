package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"climate-processor/internal/domain"
)

// CreateSession stores a new session. A second session with the same id is an error.
func (r *Repository) CreateSession(ctx context.Context, s domain.Session) error {
	_, err := r.writer.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, created_at, expires_at, last_used_at, user_agent)
		VALUES (?, ?, ?, ?, ?, ?)`,
		s.ID, s.UserID, s.CreatedAt.Unix(), s.ExpiresAt.Unix(), s.LastUsedAt.Unix(), s.UserAgent)
	if err != nil {
		return fmt.Errorf("sqlite: create session: %w", err)
	}
	return nil
}

// Session returns the session with the given id; ok is false when there is none. Times are in UTC.
func (r *Repository) Session(ctx context.Context, id string) (s domain.Session, ok bool, err error) {
	var created, expires, lastUsed int64
	err = r.reader.QueryRowContext(ctx, `
		SELECT user_id, created_at, expires_at, last_used_at, user_agent
		FROM sessions WHERE id = ?`, id).
		Scan(&s.UserID, &created, &expires, &lastUsed, &s.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Session{}, false, nil
	}
	if err != nil {
		return domain.Session{}, false, fmt.Errorf("sqlite: query session: %w", err)
	}
	s.ID = id
	s.CreatedAt = time.Unix(created, 0).UTC()
	s.ExpiresAt = time.Unix(expires, 0).UTC()
	s.LastUsedAt = time.Unix(lastUsed, 0).UTC()
	return s, true, nil
}

// TouchSession moves the last-used time of a session forward. An earlier time is ignored, so two requests
// that finish out of order cannot move it back.
func (r *Repository) TouchSession(ctx context.Context, id string, at time.Time) error {
	_, err := r.writer.ExecContext(ctx,
		`UPDATE sessions SET last_used_at = ? WHERE id = ? AND last_used_at < ?`,
		at.Unix(), id, at.Unix())
	if err != nil {
		return fmt.Errorf("sqlite: touch session: %w", err)
	}
	return nil
}

// DeleteSession removes a session. A session that does not exist is not an error.
func (r *Repository) DeleteSession(ctx context.Context, id string) error {
	if _, err := r.writer.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("sqlite: delete session: %w", err)
	}
	return nil
}

// DeleteUserSessions removes all sessions of a user and reports how many there were.
func (r *Repository) DeleteUserSessions(ctx context.Context, userID int64) (int64, error) {
	return r.deleteSessions(ctx, "delete user sessions", `DELETE FROM sessions WHERE user_id = ?`, userID)
}

// DeleteExpiredSessions removes the sessions that expire at or before now and reports how many there were.
func (r *Repository) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	return r.deleteSessions(ctx, "delete expired sessions", `DELETE FROM sessions WHERE expires_at <= ?`, now.Unix())
}

func (r *Repository) deleteSessions(ctx context.Context, what, query string, arg any) (int64, error) {
	res, err := r.writer.ExecContext(ctx, query, arg)
	if err != nil {
		return 0, fmt.Errorf("sqlite: %s: %w", what, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlite: %s: %w", what, err)
	}
	return n, nil
}

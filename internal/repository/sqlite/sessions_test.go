package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"climate-processor/internal/auth"
	"climate-processor/internal/domain"
)

// The repository must satisfy what the auth service needs; checked at compile time.
var _ auth.SessionStore = (*Repository)(nil)

var sessionT0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

// testSession is a valid session of a user; the digit makes its 64-character id unique.
func testSession(digit string, userID int64) domain.Session {
	return domain.Session{
		ID:         strings.Repeat(digit, 64),
		UserID:     userID,
		CreatedAt:  sessionT0,
		ExpiresAt:  sessionT0.Add(time.Hour),
		LastUsedAt: sessionT0,
		UserAgent:  "Mozilla/5.0 (test)",
	}
}

func mustCreate(t *testing.T, r *Repository, s domain.Session) {
	t.Helper()
	if err := r.CreateSession(context.Background(), s); err != nil {
		t.Fatalf("CreateSession(%s...): %v", s.ID[:4], err)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := newTestRepository(t)
	want := testSession("a", 1001)
	mustCreate(t, r, want)

	got, ok, err := r.Session(ctx, want.ID)

	if err != nil || !ok {
		t.Fatalf("Session = %v, %v; want it found", ok, err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got.CreatedAt.Location() != time.UTC || got.ExpiresAt.Location() != time.UTC || got.LastUsedAt.Location() != time.UTC {
		t.Error("times must be in UTC")
	}
}

func TestUnknownSessionIsNotAnError(t *testing.T) {
	r := newTestRepository(t)

	_, ok, err := r.Session(context.Background(), strings.Repeat("f", 64))

	if err != nil || ok {
		t.Errorf("Session = %v, %v; want not found and no error", ok, err)
	}
}

func TestSessionIDMustBeUnique(t *testing.T) {
	r := newTestRepository(t)
	mustCreate(t, r, testSession("a", 1001))

	if err := r.CreateSession(context.Background(), testSession("a", 2002)); err == nil {
		t.Fatal("a second session with the same id must be refused")
	}
}

func TestSessionIDMustBeAHash(t *testing.T) {
	r := newTestRepository(t)
	s := testSession("a", 1001)
	s.ID = "the-raw-token" // not 64 characters: something that is not a SHA-256 must not be stored

	if err := r.CreateSession(context.Background(), s); err == nil {
		t.Fatal("an id that is not 64 characters long must be refused")
	}
}

func TestSessionsSurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s := testSession("a", 1001)
	r, err := New(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, r, s)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	r, err = New(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	got, ok, err := r.Session(ctx, s.ID)
	if err != nil || !ok || got != s {
		t.Errorf("after reopen: %+v, %v, %v; want %+v", got, ok, err, s)
	}
}

func TestTouchMovesTheLastUsedTimeForwardOnly(t *testing.T) {
	ctx := context.Background()
	r := newTestRepository(t)
	s := testSession("a", 1001)
	mustCreate(t, r, s)
	lastUsed := func() time.Time {
		t.Helper()
		got, ok, err := r.Session(ctx, s.ID)
		if err != nil || !ok {
			t.Fatalf("Session: %v, %v", ok, err)
		}
		return got.LastUsedAt
	}

	later := sessionT0.Add(10 * time.Minute)
	if err := r.TouchSession(ctx, s.ID, later); err != nil {
		t.Fatal(err)
	}
	if got := lastUsed(); !got.Equal(later) {
		t.Errorf("last used = %s, want %s", got, later)
	}

	if err := r.TouchSession(ctx, s.ID, sessionT0.Add(5*time.Minute)); err != nil { // an older request finishes late
		t.Fatal(err)
	}
	if got := lastUsed(); !got.Equal(later) {
		t.Errorf("last used moved back to %s, want %s", got, later)
	}
}

func TestTouchOfUnknownSessionIsHarmless(t *testing.T) {
	r := newTestRepository(t)

	if err := r.TouchSession(context.Background(), strings.Repeat("f", 64), sessionT0); err != nil {
		t.Errorf("TouchSession = %v, want nil", err)
	}
}

func TestDeleteSession(t *testing.T) {
	ctx := context.Background()
	r := newTestRepository(t)
	a, b := testSession("a", 1001), testSession("b", 1001)
	mustCreate(t, r, a)
	mustCreate(t, r, b)

	if err := r.DeleteSession(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteSession(ctx, a.ID); err != nil { // already gone
		t.Fatalf("deleting a missing session must not fail: %v", err)
	}

	if _, ok, _ := r.Session(ctx, a.ID); ok {
		t.Error("the deleted session is still there")
	}
	if _, ok, _ := r.Session(ctx, b.ID); !ok {
		t.Error("another session must stay")
	}
}

func TestDeleteUserSessions(t *testing.T) {
	ctx := context.Background()
	r := newTestRepository(t)
	mustCreate(t, r, testSession("a", 1001))
	mustCreate(t, r, testSession("b", 1001))
	mustCreate(t, r, testSession("c", 2002))

	n, err := r.DeleteUserSessions(ctx, 1001)

	if err != nil || n != 2 {
		t.Fatalf("DeleteUserSessions = %d, %v; want 2, nil", n, err)
	}
	if _, ok, _ := r.Session(ctx, strings.Repeat("c", 64)); !ok {
		t.Error("the session of another user must stay")
	}
	if n, err := r.DeleteUserSessions(ctx, 1001); err != nil || n != 0 {
		t.Errorf("second call = %d, %v; want 0, nil", n, err)
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	ctx := context.Background()
	r := newTestRepository(t)
	now := sessionT0.Add(time.Hour) // the session "a" expires exactly now
	expired, expiring, alive := testSession("a", 1001), testSession("b", 1001), testSession("c", 1001)
	expired.ExpiresAt = now.Add(-time.Second)
	expiring.ExpiresAt = now // the boundary: at or before now is gone
	alive.ExpiresAt = now.Add(time.Second)
	mustCreate(t, r, expired)
	mustCreate(t, r, expiring)
	mustCreate(t, r, alive)

	n, err := r.DeleteExpiredSessions(ctx, now)

	if err != nil || n != 2 {
		t.Fatalf("DeleteExpiredSessions = %d, %v; want 2, nil", n, err)
	}
	for _, s := range []domain.Session{expired, expiring} {
		if _, ok, _ := r.Session(ctx, s.ID); ok {
			t.Errorf("session %s... must be deleted", s.ID[:1])
		}
	}
	if _, ok, _ := r.Session(ctx, alive.ID); !ok {
		t.Error("a session that has not expired must stay")
	}
}

// The migration is a script of several statements: the indexes must be created together with the table.
func TestSessionsTableHasItsIndexes(t *testing.T) {
	r := newTestRepository(t)

	rows, err := r.writer.QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'sessions' AND name LIKE 'sessions_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !got["sessions_user_id"] || !got["sessions_expires_at"] {
		t.Errorf("indexes = %v, want sessions_user_id and sessions_expires_at", got)
	}
}

func TestReaderPoolCannotModifySessions(t *testing.T) {
	r := newTestRepository(t)

	if _, err := r.reader.ExecContext(context.Background(), "DELETE FROM sessions"); err == nil {
		t.Fatal("the reader pool must not be able to modify sessions")
	}
}

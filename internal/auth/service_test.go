package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"climate-processor/internal/apperr"
	"climate-processor/internal/domain"
)

const (
	ownerID   int64 = 1001
	strangeID int64 = 2002
)

// fakeStore is an in-memory SessionStore that counts calls and fails on demand.
type fakeStore struct {
	mu       sync.Mutex
	sessions map[string]domain.Session
	calls    map[string]int   // method name -> number of calls
	fail     map[string]error // method name -> the error it returns
}

func newFakeStore() *fakeStore {
	return &fakeStore{sessions: map[string]domain.Session{}, calls: map[string]int{}, fail: map[string]error{}}
}

// enter counts the call and returns the programmed failure. The caller must hold no lock.
func (f *fakeStore) enter(method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[method]++
	return f.fail[method]
}

func (f *fakeStore) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func (f *fakeStore) CreateSession(_ context.Context, s domain.Session) error {
	if err := f.enter("CreateSession"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[s.ID] = s
	return nil
}

func (f *fakeStore) Session(_ context.Context, id string) (domain.Session, bool, error) {
	if err := f.enter("Session"); err != nil {
		return domain.Session{}, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	return s, ok, nil
}

func (f *fakeStore) TouchSession(_ context.Context, id string, at time.Time) error {
	if err := f.enter("TouchSession"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[id]; ok && at.After(s.LastUsedAt) {
		s.LastUsedAt = at
		f.sessions[id] = s
	}
	return nil
}

func (f *fakeStore) DeleteSession(_ context.Context, id string) error {
	if err := f.enter("DeleteSession"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, id)
	return nil
}

func (f *fakeStore) DeleteUserSessions(_ context.Context, userID int64) (int64, error) {
	if err := f.enter("DeleteUserSessions"); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for id, s := range f.sessions {
		if s.UserID == userID {
			delete(f.sessions, id)
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) DeleteExpiredSessions(_ context.Context, now time.Time) (int64, error) {
	if err := f.enter("DeleteExpiredSessions"); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for id, s := range f.sessions {
		if !s.ExpiresAt.After(now) {
			delete(f.sessions, id)
			n++
		}
	}
	return n, nil
}

// stored returns the session as the store holds it.
func (f *fakeStore) stored(id string) (domain.Session, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	return s, ok
}

func (f *fakeStore) size() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sessions)
}

// fakeOwners is the set of owners; the test may change it.
type fakeOwners struct {
	mu  sync.Mutex
	ids map[int64]bool
}

func (o *fakeOwners) IsAdmin(id int64) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ids[id]
}

func (o *fakeOwners) remove(id int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.ids, id)
}

func (o *fakeOwners) add(id int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ids[id] = true
}

// testClock is a clock the test moves by hand.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type testEnv struct {
	svc    *Service
	store  *fakeStore
	owners *fakeOwners
	clock  *testClock
	logs   *syncBuffer
}

// syncBuffer is a log sink that can be read while a goroutine still writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestEnv(t *testing.T, opts Options) *testEnv {
	t.Helper()
	e := &testEnv{
		store:  newFakeStore(),
		owners: &fakeOwners{ids: map[int64]bool{ownerID: true}},
		clock:  &testClock{t: time.Date(2026, 11, 26, 12, 0, 0, 500_000_000, time.UTC)},
		logs:   &syncBuffer{},
	}
	opts.Now = e.clock.now
	e.svc = NewService(slog.New(slog.NewJSONHandler(e.logs, nil)), e.store, e.owners, opts)
	return e
}

// signIn creates a session of the owner and returns its token.
func (e *testEnv) signIn(t *testing.T) string {
	t.Helper()
	token, _, err := e.svc.Create(context.Background(), ownerID, "test")
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func wantCode(t *testing.T, err error, want apperr.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("got no error, want a %s error", want)
	}
	if got := apperr.CodeOf(err); got != want {
		t.Fatalf("error code = %s, want %s (err: %v)", got, want, err)
	}
}

func TestSignedInOwnerIsAuthenticated(t *testing.T) {
	e := newTestEnv(t, Options{})
	token, session, err := e.svc.Create(context.Background(), ownerID, "Mozilla/5.0")
	if err != nil {
		t.Fatal(err)
	}

	id, err := e.svc.Authenticate(context.Background(), token)

	if err != nil {
		t.Fatal(err)
	}
	if want := (Identity{Role: domain.RoleOwner, UserID: ownerID, SessionID: session.ID}); id != want {
		t.Errorf("identity = %+v, want %+v", id, want)
	}
}

func TestOnlyTheHashOfTheTokenIsStored(t *testing.T) {
	e := newTestEnv(t, Options{})
	token, session, err := e.svc.Create(context.Background(), ownerID, "ua")
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256([]byte(token))
	if want := hex.EncodeToString(sum[:]); session.ID != want {
		t.Errorf("session id = %q, want the SHA-256 of the token %q", session.ID, want)
	}
	if session.ID == token {
		t.Error("the token itself must not be the stored id")
	}
	stored, ok := e.store.stored(session.ID)
	if !ok || stored != session {
		t.Errorf("stored %+v (found: %v), want %+v", stored, ok, session)
	}
	if _, found := e.store.stored(token); found {
		t.Error("a session must not be findable by its raw token")
	}
}

func TestTokensAreLongRandomAndDifferent(t *testing.T) {
	e := newTestEnv(t, Options{})
	seen := map[string]bool{}

	for range 50 {
		token := e.signIn(t)
		if len(token) != 43 || strings.Trim(token, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
			t.Fatalf("token %q: want 43 URL-safe characters (256 bits)", token)
		}
		if seen[token] {
			t.Fatalf("token %q was issued twice", token)
		}
		seen[token] = true
	}
}

func TestSessionLifetime(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want time.Duration
	}{
		{"default", Options{}, DefaultSessionTTL},
		{"custom", Options{TTL: 2 * time.Hour}, 2 * time.Hour},
		{"a non-positive value falls back to the default", Options{TTL: -time.Hour}, DefaultSessionTTL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t, tt.opts)

			_, s, err := e.svc.Create(context.Background(), ownerID, "")

			if err != nil {
				t.Fatal(err)
			}
			created := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC) // the clock without its fraction of a second
			if !s.CreatedAt.Equal(created) || !s.LastUsedAt.Equal(created) || !s.ExpiresAt.Equal(created.Add(tt.want)) {
				t.Errorf("session times = %s / %s / %s, want %s / %s / %s",
					s.CreatedAt, s.LastUsedAt, s.ExpiresAt, created, created, created.Add(tt.want))
			}
			if s.CreatedAt.Location() != time.UTC {
				t.Errorf("times must be in UTC, got %s", s.CreatedAt.Location())
			}
		})
	}
}

func TestSessionTimesAreInUTCWhateverTheClockZone(t *testing.T) {
	e := newTestEnv(t, Options{})
	e.clock.t = e.clock.t.In(time.FixedZone("UTC+5", 5*60*60)) // Astana

	_, s, err := e.svc.Create(context.Background(), ownerID, "")

	if err != nil {
		t.Fatal(err)
	}
	for name, at := range map[string]time.Time{"created": s.CreatedAt, "expires": s.ExpiresAt, "last used": s.LastUsedAt} {
		if at.Location() != time.UTC {
			t.Errorf("%s time is in %s, want UTC", name, at.Location())
		}
	}
}

func TestOnlyOwnersCanSignIn(t *testing.T) {
	e := newTestEnv(t, Options{})

	token, _, err := e.svc.Create(context.Background(), strangeID, "ua")

	wantCode(t, err, apperr.CodeForbidden)
	if token != "" || e.store.size() != 0 || e.store.count("CreateSession") != 0 {
		t.Errorf("nothing may be created for a stranger: token %q, %d sessions", token, e.store.size())
	}
}

func TestUserAgentIsKeptSmallAndValid(t *testing.T) {
	e := newTestEnv(t, Options{})
	long := strings.Repeat("ж", 300) // two bytes per rune: cutting by bytes would break a character

	_, s, err := e.svc.Create(context.Background(), ownerID, long+"\xff")

	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Repeat("ж", maxUserAgentLen); s.UserAgent != want {
		t.Errorf("user agent has %d runes, want %d valid ones", len([]rune(s.UserAgent)), maxUserAgentLen)
	}
	_, s, err = e.svc.Create(context.Background(), ownerID, "a\xffb")
	if err != nil || s.UserAgent != "ab" {
		t.Errorf("invalid UTF-8 must be dropped: %q, %v", s.UserAgent, err)
	}
}

func TestStoreFailureOnCreateIsUnavailable(t *testing.T) {
	e := newTestEnv(t, Options{})
	boom := errors.New("disk is full")
	e.store.fail["CreateSession"] = boom

	_, _, err := e.svc.Create(context.Background(), ownerID, "")

	wantCode(t, err, apperr.CodeUnavailable)
	if !errors.Is(err, boom) {
		t.Error("the cause must stay in the error chain for the logs")
	}
}

func TestInvalidTokensAreUnauthorized(t *testing.T) {
	e := newTestEnv(t, Options{})
	e.signIn(t)

	for name, token := range map[string]string{
		"empty":   "",
		"unknown": strings.Repeat("A", 43),
		"garbage": "!!! not a token !!!",
	} {
		_, err := e.svc.Authenticate(context.Background(), token)
		if apperr.CodeOf(err) != apperr.CodeUnauthorized {
			t.Errorf("%s: code = %s, want unauthorized (err: %v)", name, apperr.CodeOf(err), err)
		}
	}
}

func TestAnEmptyOrOverlongTokenNeverReachesTheStore(t *testing.T) {
	for name, token := range map[string]string{"empty": "", "overlong": strings.Repeat("A", maxTokenLen+1)} {
		e := newTestEnv(t, Options{})

		_, err := e.svc.Authenticate(context.Background(), token)

		wantCode(t, err, apperr.CodeUnauthorized)
		if n := e.store.count("Session"); n != 0 {
			t.Errorf("%s token: the store was asked %d times, want 0", name, n)
		}
	}
}

func TestTokenOfMaximumLengthIsStillLookedUp(t *testing.T) {
	e := newTestEnv(t, Options{})

	_, err := e.svc.Authenticate(context.Background(), strings.Repeat("A", maxTokenLen))

	wantCode(t, err, apperr.CodeUnauthorized)
	if n := e.store.count("Session"); n != 1 {
		t.Errorf("the store was asked %d times, want 1", n)
	}
}

func TestSessionExpiresExactlyAtItsExpiryTime(t *testing.T) {
	e := newTestEnv(t, Options{TTL: time.Hour})
	token := e.signIn(t) // expires at 13:00:00; the clock reads 12:00:00.5

	e.clock.advance(time.Hour - time.Second) // 12:59:59.5
	if _, err := e.svc.Authenticate(context.Background(), token); err != nil {
		t.Fatalf("one second before the expiry: %v", err)
	}

	e.clock.advance(500 * time.Millisecond) // 13:00:00.0
	_, err := e.svc.Authenticate(context.Background(), token)
	wantCode(t, err, apperr.CodeUnauthorized)
}

func TestRemovedOwnerLosesTheSessionForGood(t *testing.T) {
	e := newTestEnv(t, Options{})
	token := e.signIn(t)
	e.owners.remove(ownerID)

	_, err := e.svc.Authenticate(context.Background(), token)

	wantCode(t, err, apperr.CodeUnauthorized)
	if e.store.size() != 0 {
		t.Error("the session of a removed owner must be deleted")
	}
	e.owners.add(ownerID) // the account becomes an owner again
	if _, err := e.svc.Authenticate(context.Background(), token); err == nil {
		t.Error("the old session must not come back to life")
	}
}

func TestStoreFailureOnLookupIsUnavailableNotUnauthorized(t *testing.T) {
	e := newTestEnv(t, Options{})
	token := e.signIn(t)
	boom := errors.New("database is locked")
	e.store.fail["Session"] = boom

	_, err := e.svc.Authenticate(context.Background(), token)

	wantCode(t, err, apperr.CodeUnavailable) // a database outage must not look like a wrong password
	if !errors.Is(err, boom) {
		t.Error("the cause must stay in the error chain")
	}
}

func TestLastUsedTimeIsWrittenAtMostEveryTenMinutes(t *testing.T) {
	e := newTestEnv(t, Options{})
	e.clock.t = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC) // a whole second: the interval is measured exactly
	token := e.signIn(t)
	ctx := context.Background()

	e.clock.advance(touchEvery - time.Second)
	if _, err := e.svc.Authenticate(ctx, token); err != nil {
		t.Fatal(err)
	}
	if n := e.store.count("TouchSession"); n != 0 {
		t.Fatalf("touched %d times within %s, want 0", n, touchEvery)
	}

	e.clock.advance(time.Second) // exactly touchEvery since the sign-in
	id, err := e.svc.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if n := e.store.count("TouchSession"); n != 1 {
		t.Fatalf("touched %d times, want 1", n)
	}
	stored, _ := e.store.stored(id.SessionID)
	if want := time.Date(2026, 11, 26, 12, 10, 0, 0, time.UTC); !stored.LastUsedAt.Equal(want) {
		t.Errorf("last used = %s, want %s", stored.LastUsedAt, want)
	}

	e.clock.advance(time.Minute) // used again right away: no second write
	if _, err := e.svc.Authenticate(ctx, token); err != nil {
		t.Fatal(err)
	}
	if n := e.store.count("TouchSession"); n != 1 {
		t.Errorf("touched %d times, want still 1", n)
	}
}

func TestFailedTouchDoesNotSignTheOwnerOut(t *testing.T) {
	e := newTestEnv(t, Options{})
	token := e.signIn(t)
	e.store.fail["TouchSession"] = errors.New("database is locked")
	e.clock.advance(time.Hour)

	if _, err := e.svc.Authenticate(context.Background(), token); err != nil {
		t.Fatalf("a failed bookkeeping write must not fail the request: %v", err)
	}
	if !strings.Contains(e.logs.String(), "last-used time") {
		t.Errorf("the failure must be logged: %s", e.logs.String())
	}
}

func TestRevokeSignsOutOnlyThatSession(t *testing.T) {
	e := newTestEnv(t, Options{})
	laptop, phone := e.signIn(t), e.signIn(t)
	ctx := context.Background()

	if err := e.svc.Revoke(ctx, laptop); err != nil {
		t.Fatal(err)
	}

	_, err := e.svc.Authenticate(ctx, laptop)
	wantCode(t, err, apperr.CodeUnauthorized)
	if _, err := e.svc.Authenticate(ctx, phone); err != nil {
		t.Errorf("the other session must stay valid: %v", err)
	}
}

func TestRevokeIsIdempotent(t *testing.T) {
	e := newTestEnv(t, Options{})
	token := e.signIn(t)
	ctx := context.Background()

	for _, tok := range []string{token, token, strings.Repeat("A", 43), "", strings.Repeat("A", maxTokenLen+1)} {
		if err := e.svc.Revoke(ctx, tok); err != nil {
			t.Errorf("Revoke(%q) = %v, want nil", tok, err)
		}
	}
	if n := e.store.count("DeleteSession"); n != 3 { // the empty and the overlong token are not looked up
		t.Errorf("the store was asked %d times, want 3", n)
	}
}

func TestStoreFailureOnRevokeIsUnavailable(t *testing.T) {
	e := newTestEnv(t, Options{})
	e.store.fail["DeleteSession"] = errors.New("boom")

	wantCode(t, e.svc.Revoke(context.Background(), strings.Repeat("A", 43)), apperr.CodeUnavailable)
}

func TestRevokeAllSignsTheUserOutEverywhere(t *testing.T) {
	e := newTestEnv(t, Options{})
	e.owners.add(strangeID)
	a, b := e.signIn(t), e.signIn(t)
	other, _, err := e.svc.Create(context.Background(), strangeID, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	n, err := e.svc.RevokeAll(ctx, ownerID)

	if err != nil || n != 2 {
		t.Fatalf("RevokeAll = %d, %v; want 2, nil", n, err)
	}
	for _, token := range []string{a, b} {
		if _, err := e.svc.Authenticate(ctx, token); err == nil {
			t.Error("a session of the user must be gone")
		}
	}
	if _, err := e.svc.Authenticate(ctx, other); err != nil {
		t.Errorf("the session of another user must stay: %v", err)
	}
}

func TestStoreFailureOnRevokeAllIsUnavailable(t *testing.T) {
	e := newTestEnv(t, Options{})
	e.store.fail["DeleteUserSessions"] = errors.New("boom")

	_, err := e.svc.RevokeAll(context.Background(), ownerID)

	wantCode(t, err, apperr.CodeUnavailable)
}

func TestDeleteExpiredRemovesOnlyExpiredSessions(t *testing.T) {
	e := newTestEnv(t, Options{TTL: time.Hour})
	old := e.signIn(t) // expires 13:00:00
	e.clock.advance(30 * time.Minute)
	fresh := e.signIn(t)              // expires 13:30:00
	e.clock.advance(30 * time.Minute) // 13:00:00.5: the first one has just expired
	ctx := context.Background()

	n, err := e.svc.DeleteExpired(ctx)

	if err != nil || n != 1 {
		t.Fatalf("DeleteExpired = %d, %v; want 1, nil", n, err)
	}
	if _, ok := e.store.stored(sessionID(old)); ok {
		t.Error("the expired session must be deleted")
	}
	if _, err := e.svc.Authenticate(ctx, fresh); err != nil {
		t.Errorf("the fresh session must stay: %v", err)
	}
}

func TestStoreFailureOnDeleteExpiredIsUnavailable(t *testing.T) {
	e := newTestEnv(t, Options{})
	e.store.fail["DeleteExpiredSessions"] = errors.New("boom")

	_, err := e.svc.DeleteExpired(context.Background())

	wantCode(t, err, apperr.CodeUnavailable)
}

func TestRunCleanupSweepsAtStartAndWhenAskedToStopReturns(t *testing.T) {
	e := newTestEnv(t, Options{TTL: time.Second})
	e.signIn(t)
	e.clock.advance(time.Minute) // the session has expired
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		e.svc.RunCleanup(ctx, 5*time.Millisecond)
		close(done)
	}()

	deadline := time.After(5 * time.Second)
	for e.store.size() != 0 {
		select {
		case <-deadline:
			t.Fatal("the expired session was not cleaned up")
		case <-time.After(time.Millisecond):
		}
	}
	for e.store.count("DeleteExpiredSessions") < 2 { // it keeps sweeping, not only at the start
		select {
		case <-deadline:
			t.Fatal("the cleanup did not repeat")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunCleanup did not return after the context was cancelled")
	}
}

func TestRunCleanupWaitsBetweenSweeps(t *testing.T) {
	e := newTestEnv(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		e.svc.RunCleanup(ctx, time.Hour)
		close(done)
	}()

	for e.store.count("DeleteExpiredSessions") < 1 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := e.store.count("DeleteExpiredSessions"); n != 1 {
		t.Errorf("swept %d times, want 1: with a one hour interval the next sweep is an hour away", n)
	}
	cancel()
	<-done
}

func TestRunCleanupSurvivesStoreErrors(t *testing.T) {
	e := newTestEnv(t, Options{})
	e.store.fail["DeleteExpiredSessions"] = errors.New("boom")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		e.svc.RunCleanup(ctx, time.Millisecond)
		close(done)
	}()

	deadline := time.After(5 * time.Second)
	for e.store.count("DeleteExpiredSessions") < 3 {
		select {
		case <-deadline:
			t.Fatal("the cleanup stopped after an error")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
	if !strings.Contains(e.logs.String(), "cleaning up expired sessions failed") {
		t.Errorf("the failure must be logged: %s", e.logs.String())
	}
}

func TestRunCleanupWithoutAnIntervalUsesTheDefault(t *testing.T) {
	e := newTestEnv(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		e.svc.RunCleanup(ctx, 0) // a zero interval would make time.NewTicker panic
		close(done)
	}()

	for e.store.count("DeleteExpiredSessions") < 1 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunCleanup did not return after the context was cancelled")
	}
}

func TestTokenIsNeverLogged(t *testing.T) {
	e := newTestEnv(t, Options{})
	ctx := context.Background()
	token, _, err := e.svc.Create(ctx, ownerID, "ua")
	if err != nil {
		t.Fatal(err)
	}
	e.clock.advance(time.Hour)
	_, _ = e.svc.Authenticate(ctx, token)
	_ = e.svc.Revoke(ctx, token)
	_, _ = e.svc.Authenticate(ctx, token)

	if strings.Contains(e.logs.String(), token) {
		t.Errorf("the secret token is in the logs: %s", e.logs.String())
	}
	if !strings.Contains(e.logs.String(), sessionID(token)) {
		t.Error("the session id (a hash) should be logged, so that sessions can be told apart")
	}
}

func TestAnonymousIsTheReadOnlyDemo(t *testing.T) {
	if Anonymous.Role != domain.RoleDemo || Anonymous.UserID != 0 || Anonymous.SessionID != "" {
		t.Errorf("Anonymous = %+v", Anonymous)
	}
}

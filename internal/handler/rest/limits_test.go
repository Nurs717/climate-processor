package rest

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"climate-processor/internal/auth"
	"climate-processor/internal/domain"
	"climate-processor/internal/ratelimit"
)

// refusingLimiter refuses the keys it is told to refuse, allows the others and records every call.
type refusingLimiter struct {
	refuse map[string]time.Duration // key -> how long the refused client has to wait
	calls  []limiterCall
}

func (f *refusingLimiter) Allow(key string, cost int, now time.Time) (bool, time.Duration) {
	f.calls = append(f.calls, limiterCall{key: key, cost: cost, now: now})
	if wait, ok := f.refuse[key]; ok {
		return false, wait
	}
	return true, 0
}

// charges lists what a limiter was asked as "key/cost", in order.
func charges(calls []limiterCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, fmt.Sprintf("%s/%d", c.key, c.cost))
	}
	return out
}

func withOwnerLimiter(l Limiter) func(*Options) { return func(o *Options) { o.OwnerLimiter = l } }

func withPeer(addr string) func(*http.Request) {
	return func(r *http.Request) { r.RemoteAddr = addr }
}

func ownerCookie() func(*http.Request) { return withCookie(sessionCookie, ownerToken) }

func mustLimiter(t *testing.T, perMinute, burst int) *ratelimit.Limiter {
	t.Helper()
	l, err := ratelimit.New(perMinute, burst, 1000) // the router's clock is fixed in tests: nothing is refilled
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func wantCharges(t *testing.T, who string, calls []limiterCall, want ...string) {
	t.Helper()
	if got := charges(calls); !slices.Equal(got, want) {
		t.Errorf("%s was charged %v, want %v", who, got, want)
	}
}

func TestAnonymousClientsHaveTheDemoLimit(t *testing.T) {
	demo, owner := &fakeLimiter{ok: true}, &fakeLimiter{ok: true}
	e := newAuthEnv(t, withLimiter(demo), withOwnerLimiter(owner))
	e.reader.hasLatest = true

	e.get("/api/v1/latest")
	e.get("/api/v1/readings")
	e.get("/api/v1/auth/me")

	wantCharges(t, "the demo limit", demo.calls, "192.0.2.1/1", "192.0.2.1/5", "192.0.2.1/1")
	wantCharges(t, "the owner limit", owner.calls)
}

func TestOwnerPaysOneTokenPerAddressBeforeTheSessionCheckAndItsCostPerUserAfter(t *testing.T) {
	demo, owner := &fakeLimiter{ok: true}, &fakeLimiter{ok: true}
	e := newAuthEnv(t, withLimiter(demo), withOwnerLimiter(owner))
	e.reader.hasLatest = true

	for _, target := range []string{"/api/v1/latest", "/api/v1/readings"} {
		if rec := e.do(http.MethodGet, target, ownerCookie()); rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body: %s", target, rec.Code, rec.Body)
		}
	}

	wantCharges(t, "the owner limit", owner.calls,
		"ip:192.0.2.1/1", "user:42/1",
		"ip:192.0.2.1/1", "user:42/5")
	for _, c := range owner.calls {
		if !c.now.Equal(testNow) {
			t.Errorf("%s was charged at %s, want the router clock", c.key, c.now)
		}
	}
	wantCharges(t, "the demo limit", demo.calls)
}

func TestBearerTokenHasTheOwnerLimitToo(t *testing.T) {
	owner := &fakeLimiter{ok: true}
	e := newAuthEnv(t, withOwnerLimiter(owner))
	e.reader.hasLatest = true

	rec := e.do(http.MethodGet, "/api/v1/latest", withHeader("Authorization", "Bearer "+ownerToken))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body)
	}
	wantCharges(t, "the owner limit", owner.calls, "ip:192.0.2.1/1", "user:42/1")
}

// The owner is not held to the limit of the demo: the demo budget of this very address is empty.
func TestOwnerIsNotHeldToTheDemoLimit(t *testing.T) {
	e := newAuthEnv(t, withLimiter(mustLimiter(t, 60, 2)), withOwnerLimiter(mustLimiter(t, 600, 50)))
	e.reader.hasLatest = true

	for i := range 2 {
		if rec := e.get("/api/v1/latest"); rec.Code != http.StatusOK {
			t.Fatalf("anonymous request %d within the budget: status = %d", i+1, rec.Code)
		}
	}
	if rec := e.get("/api/v1/latest"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("anonymous request over the budget: status = %d, want 429", rec.Code)
	}

	for i := range 40 {
		if rec := e.do(http.MethodGet, "/api/v1/latest", ownerCookie()); rec.Code != http.StatusOK {
			t.Fatalf("owner request %d from the same address: status = %d, want 200", i+1, rec.Code)
		}
	}
}

// The budget of the owner follows the user, not the address, and every user has a budget of their own.
func TestOwnerBudgetIsPerUserAndNotPerAddress(t *testing.T) {
	e := newAuthEnv(t, withOwnerLimiter(mustLimiter(t, 60, 3)))
	e.reader.hasLatest = true
	e.sessions.owners["second-token"] = auth.Identity{Role: domain.RoleOwner, UserID: 43, SessionID: "sid-2"}

	// Three requests from three addresses use up the burst of user 42.
	for _, peer := range []string{"192.0.2.11:1", "192.0.2.12:1", "192.0.2.13:1"} {
		if rec := e.do(http.MethodGet, "/api/v1/latest", ownerCookie(), withPeer(peer)); rec.Code != http.StatusOK {
			t.Fatalf("request from %s within the budget: status = %d", peer, rec.Code)
		}
	}

	if rec := e.do(http.MethodGet, "/api/v1/latest", ownerCookie(), withPeer("192.0.2.14:1")); rec.Code != http.StatusTooManyRequests {
		t.Errorf("a fourth address of the same user: status = %d, want 429: the budget goes with the user", rec.Code)
	}
	if rec := e.do(http.MethodGet, "/api/v1/latest", withCookie(sessionCookie, "second-token"), withPeer("192.0.2.11:1")); rec.Code != http.StatusOK {
		t.Errorf("another user from an address the first one used: status = %d, want 200", rec.Code)
	}
}

func TestRefusedOwnerGets429AfterTheSessionCheck(t *testing.T) {
	owner := &refusingLimiter{refuse: map[string]time.Duration{"user:42": 4 * time.Second}}
	e := newAuthEnv(t, withOwnerLimiter(owner))

	rec := e.do(http.MethodGet, "/api/v1/readings", ownerCookie())

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body: %s)", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Retry-After"); got != "4" {
		t.Errorf("Retry-After = %q, want 4", got)
	}
	if e.reader.queries != 0 {
		t.Error("a refused request must not reach the service")
	}
	if len(e.sessions.authCalls) != 1 {
		t.Errorf("the session was checked %d times, want once: the user is known only after that", len(e.sessions.authCalls))
	}
	if r := e.record(t, "request done"); r["status"] != float64(http.StatusTooManyRequests) || r["role"] != "owner" {
		t.Errorf("the refusal must be logged with the role of the client: %v", r)
	}
}

// The limit that keeps made-up tokens away from the session store: it is spent per address, before the check.
func TestFloodOfMadeUpTokensStopsBeforeTheSessionStore(t *testing.T) {
	demo := &fakeLimiter{ok: true}
	e := newAuthEnv(t, withLimiter(demo), withOwnerLimiter(mustLimiter(t, 60, 5)))
	const flooder, other = "192.0.2.50:1", "192.0.2.60:1"

	for i := range 5 {
		rec := e.do(http.MethodGet, "/api/v1/latest", withCookie(sessionCookie, "made-up"), withPeer(flooder))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("made-up token %d: status = %d, want 401", i+1, rec.Code)
		}
	}
	checked := len(e.sessions.authCalls)

	if rec := e.do(http.MethodGet, "/api/v1/latest", withCookie(sessionCookie, "made-up"), withPeer(flooder)); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the flood after the budget: status = %d, want 429", rec.Code)
	}
	if got := len(e.sessions.authCalls); got != checked {
		t.Errorf("the session store was asked %d times, want %d: a refused request must not reach it", got, checked)
	}
	if rec := e.do(http.MethodGet, "/api/v1/latest", withCookie(sessionCookie, "made-up"), withPeer(other)); rec.Code != http.StatusUnauthorized {
		t.Errorf("another address: status = %d, want 401 (its own budget)", rec.Code)
	}
	wantCharges(t, "the demo limit", demo.calls)
}

func TestRefusedAddressDoesNotReachTheSessionStore(t *testing.T) {
	owner := &refusingLimiter{refuse: map[string]time.Duration{"ip:192.0.2.1": time.Second}}
	e := newAuthEnv(t, withOwnerLimiter(owner))

	rec := e.do(http.MethodGet, "/api/v1/latest", ownerCookie())

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if len(e.sessions.authCalls) != 0 {
		t.Error("a request refused in front of the session check must not reach the session store")
	}
	wantCharges(t, "the owner limit", owner.calls, "ip:192.0.2.1/1")
}

func TestInvalidTokenPaysOnlyTheGuard(t *testing.T) {
	demo, owner := &fakeLimiter{ok: true}, &fakeLimiter{ok: true}
	e := newAuthEnv(t, withLimiter(demo), withOwnerLimiter(owner))

	rec := e.do(http.MethodGet, "/api/v1/readings", withCookie(sessionCookie, "stale"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	wantCharges(t, "the owner limit", owner.calls, "ip:192.0.2.1/1")
	wantCharges(t, "the demo limit", demo.calls)
}

func TestIdentityWithoutTheOwnerRoleHasTheDemoLimit(t *testing.T) {
	demo, owner := &fakeLimiter{ok: true}, &fakeLimiter{ok: true}
	e := newAuthEnv(t, withLimiter(demo), withOwnerLimiter(owner))
	e.reader.hasLatest = true
	e.sessions.owners["viewer-token"] = auth.Identity{Role: "viewer", UserID: 7, SessionID: "sid-v"}

	rec := e.do(http.MethodGet, "/api/v1/readings", withCookie(sessionCookie, "viewer-token"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body)
	}
	wantCharges(t, "the owner limit", owner.calls, "ip:192.0.2.1/1")
	wantCharges(t, "the demo limit", demo.calls, "192.0.2.1/5")
}

// Without a limit of its own the owner is limited like everybody else, as before the owner limit existed.
func TestWithoutAnOwnerLimitTheOwnerIsChargedLikeAnybodyElse(t *testing.T) {
	demo := &fakeLimiter{ok: true}
	e := newAuthEnv(t, withLimiter(demo))
	e.reader.hasLatest = true

	e.do(http.MethodGet, "/api/v1/latest", ownerCookie())
	e.do(http.MethodGet, "/api/v1/readings", withHeader("Authorization", "Bearer "+ownerToken))

	wantCharges(t, "the demo limit", demo.calls, "192.0.2.1/1", "192.0.2.1/5")
}

// A token must not buy a cheaper price for the routes that make the server remember something or answer a 404.
func TestSignInRoutesAndUnknownRoutesIgnoreTheOwnerLimit(t *testing.T) {
	demo, owner := &fakeLimiter{ok: true}, &fakeLimiter{ok: true}
	e := newAuthEnv(t, withLimiter(demo), withOwnerLimiter(owner))

	e.do(http.MethodGet, "/api/v1/auth/telegram/login", withCookie(sessionCookie, "junk"))
	e.do(http.MethodGet, TelegramCallbackPath, withCookie(sessionCookie, "junk"))
	e.do(http.MethodPost, "/api/v1/auth/logout", ownerCookie())
	e.do(http.MethodGet, "/api/v1/nowhere", ownerCookie())

	wantCharges(t, "the demo limit", demo.calls, "192.0.2.1/5", "192.0.2.1/5", "192.0.2.1/1", "192.0.2.1/1")
	wantCharges(t, "the owner limit", owner.calls)
}

func TestNewRouterRejectsTheOwnerLimitWithoutSessions(t *testing.T) {
	_, err := NewRouter(nil, &fakeReader{}, Options{OwnerLimiter: &fakeLimiter{ok: true}})
	if err == nil {
		t.Error("an owner limit without sessions has nobody to apply to: it must fail the start")
	}
}

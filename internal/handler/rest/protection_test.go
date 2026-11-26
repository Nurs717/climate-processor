package rest

import (
	"net/http"
	"testing"
	"time"

	"climate-processor/internal/ratelimit"
)

// fakeLimiter records what the router asks it and answers as programmed.
type fakeLimiter struct {
	ok    bool
	wait  time.Duration
	calls []limiterCall
}

type limiterCall struct {
	key  string
	cost int
	now  time.Time
}

func (f *fakeLimiter) Allow(key string, cost int, now time.Time) (bool, time.Duration) {
	f.calls = append(f.calls, limiterCall{key: key, cost: cost, now: now})
	return f.ok, f.wait
}

func withLimiter(l Limiter) func(*Options) { return func(o *Options) { o.Limiter = l } }

func withTrustedProxies(cidrs ...string) func(*Options) {
	return func(o *Options) { o.TrustedProxies = cidrs }
}

func TestRefusedRequestGets429WithRetryAfter(t *testing.T) {
	lim := &fakeLimiter{ok: false, wait: 2500 * time.Millisecond}
	e := newTestEnv(t, withLimiter(lim))

	rec := e.get("/api/v1/readings?range=1h")

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body: %s)", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Retry-After"); got != "3" {
		t.Errorf("Retry-After = %q, want 3 (2.5s rounded up)", got)
	}
	body := decode[errorBody](t, rec)
	if body.Error.Code != "too_many_requests" || body.Error.Message != "too many requests, retry later" {
		t.Errorf("error = %+v", body.Error)
	}
	if id := rec.Header().Get("X-Request-ID"); id == "" || body.Error.RequestID != id {
		t.Errorf("the error body must carry the request id of the response header: %q vs %q", body.Error.RequestID, id)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q: a refusal must not be cached", cc)
	}
	if e.reader.queries != 0 {
		t.Error("a refused request must not reach the service")
	}
	if r := e.record(t, "request done"); r["status"] != float64(http.StatusTooManyRequests) {
		t.Errorf("the refusal must be in the access log: %v", r)
	}
}

func TestRetryAfterIsWholeSecondsAndAtLeastOne(t *testing.T) {
	tests := []struct {
		wait time.Duration
		want string
	}{
		{0, "1"}, // a limiter that refuses without a wait must still not produce "Retry-After: 0"
		{10 * time.Millisecond, "1"},
		{time.Second, "1"},
		{1001 * time.Millisecond, "2"},
		{2500 * time.Millisecond, "3"},
		{time.Hour, "3600"},
	}
	for _, tt := range tests {
		e := newTestEnv(t, withLimiter(&fakeLimiter{ok: false, wait: tt.wait}))

		rec := e.get("/api/v1/latest")

		if got := rec.Header().Get("Retry-After"); got != tt.want {
			t.Errorf("wait %s: Retry-After = %q, want %q", tt.wait, got, tt.want)
		}
	}
}

func TestRequestsAreChargedByTheirCost(t *testing.T) {
	lim := &fakeLimiter{ok: true}
	e := newTestEnv(t, withLimiter(lim))
	e.reader.hasLatest = true

	targets := []string{"/api/v1/latest", "/api/v1/readings", "/api/v1/stats", "/api/v1/nowhere"}
	wantCost := []int{1, 5, 5, 1}
	wantStatus := []int{http.StatusOK, http.StatusOK, http.StatusOK, http.StatusNotFound}
	for i, target := range targets {
		if rec := e.get(target); rec.Code != wantStatus[i] {
			t.Errorf("%s: status = %d, want %d", target, rec.Code, wantStatus[i])
		}
	}

	if len(lim.calls) != len(targets) {
		t.Fatalf("the limiter was asked %d times, want %d (unknown routes are limited too)", len(lim.calls), len(targets))
	}
	for i, call := range lim.calls {
		if call.cost != wantCost[i] {
			t.Errorf("%s costs %d, want %d", targets[i], call.cost, wantCost[i])
		}
		if call.key != "192.0.2.1" || !call.now.Equal(testNow) {
			t.Errorf("%s: limiter asked with key %q at %s, want the client address and the router clock", targets[i], call.key, call.now)
		}
	}
}

func TestClientAddressBehindProxies(t *testing.T) {
	private := []string{"10.0.0.0/8"}
	tests := []struct {
		name    string
		proxies []string
		peer    string
		xff     string
		want    string
	}{
		{"no proxies configured: the header is ignored", nil, "198.51.100.9:4000", "203.0.113.7", "198.51.100.9"},
		{"peer is not a trusted proxy: the header is ignored", private, "198.51.100.9:4000", "203.0.113.7", "198.51.100.9"},
		{"peer is a trusted proxy: the header is used", private, "10.1.2.3:4000", "203.0.113.7", "203.0.113.7"},
		{"a single address can be trusted", []string{"10.1.2.3"}, "10.1.2.3:4000", "203.0.113.7", "203.0.113.7"},
		{"a forged first entry is skipped, the proxy appended the real one", private, "10.1.2.3:4000", "1.2.3.4, 203.0.113.7", "203.0.113.7"},
		{"a chain of trusted proxies is walked from the right", private, "10.1.2.3:4000", "203.0.113.7, 10.9.9.9", "203.0.113.7"},
		{"a malformed header falls back to the peer", private, "10.1.2.3:4000", "not-an-ip", "10.1.2.3"},
		{"no header: the peer", private, "10.1.2.3:4000", "", "10.1.2.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lim := &fakeLimiter{ok: true}
			e := newTestEnv(t, withLimiter(lim), withTrustedProxies(tt.proxies...))
			var headers []string
			if tt.xff != "" {
				headers = []string{"X-Forwarded-For", tt.xff}
			}

			e.getFrom(tt.peer, "/api/v1/latest", headers...)

			if got := lim.calls[0].key; got != tt.want {
				t.Errorf("the limiter saw the client %q, want %q", got, tt.want)
			}
			if got := e.record(t, "request done")["client_ip"]; got != tt.want {
				t.Errorf("the access log has client_ip %v, want %q", got, tt.want)
			}
		})
	}
}

func TestNewRouterRejectsAnInvalidTrustedProxy(t *testing.T) {
	for _, bad := range []string{"10.0.0.0/33", "not-an-ip"} {
		if _, err := NewRouter(nil, &fakeReader{}, Options{TrustedProxies: []string{bad}}); err == nil {
			t.Errorf("TrustedProxies %q must be rejected", bad)
		}
	}
}

// The real limiter behind the real router: a client that spends its tokens is refused, another one is not.
func TestEachClientHasItsOwnBudget(t *testing.T) {
	limiter, err := ratelimit.New(60, 12, 100) // the fixed test clock means no refill
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, withLimiter(limiter))
	e.reader.hasLatest = true
	const busy, other = "192.0.2.10:5000", "192.0.2.20:5000"

	// 2 history queries (5 tokens each) and 2 cheap requests use up the burst of 12.
	for _, target := range []string{"/api/v1/readings", "/api/v1/stats", "/api/v1/latest", "/api/v1/latest"} {
		if rec := e.getFrom(busy, target); rec.Code != http.StatusOK {
			t.Fatalf("%s within the budget: status = %d", target, rec.Code)
		}
	}

	if rec := e.getFrom(busy, "/api/v1/latest"); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "1" {
		t.Errorf("cheap request over the budget: status = %d, Retry-After = %q; want 429 and 1", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := e.getFrom(busy, "/api/v1/readings"); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "5" {
		t.Errorf("history query over the budget: status = %d, Retry-After = %q; want 429 and 5", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := e.getFrom(other, "/api/v1/readings"); rec.Code != http.StatusOK {
		t.Errorf("another client must not be affected: status = %d", rec.Code)
	}
}

package ratelimit

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func mustNew(t *testing.T, perMinute, burst, maxKeys int) *Limiter {
	t.Helper()
	l, err := New(perMinute, burst, maxKeys)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestNewRejectsBadArguments(t *testing.T) {
	for _, args := range [][3]int{{0, 1, 1}, {1, 0, 1}, {1, 1, 0}, {-1, 1, 1}, {1, -1, 1}, {1, 1, -1}} {
		if _, err := New(args[0], args[1], args[2]); err == nil {
			t.Errorf("New(%d, %d, %d) must fail", args[0], args[1], args[2])
		}
	}
}

func TestBurstIsAllowedThenRefused(t *testing.T) {
	l := mustNew(t, 60, 3, 100) // one token per second

	for i := range 3 {
		if ok, _ := l.Allow("a", 1, t0); !ok {
			t.Fatalf("request %d of the burst was refused", i+1)
		}
	}
	if ok, wait := l.Allow("a", 1, t0); ok || wait != time.Second {
		t.Errorf("4th request: ok=%v, wait=%s; want refused and 1s", ok, wait)
	}
}

func TestBucketRefillsSteadilyAndNeverAboveTheBurst(t *testing.T) {
	l := mustNew(t, 60, 3, 100)
	for range 3 {
		l.Allow("a", 1, t0)
	}

	at := t0.Add(1500 * time.Millisecond)
	if ok, _ := l.Allow("a", 1, at); !ok {
		t.Error("after 1.5s one token must be back")
	}
	if ok, wait := l.Allow("a", 1, at); ok || wait != 500*time.Millisecond {
		t.Errorf("half a token is left: ok=%v, wait=%s; want refused and 500ms", ok, wait)
	}

	// 55s is shorter than the sweep interval, so only the cap on the bucket keeps it from overflowing.
	allowed := 0
	for range 100 {
		if ok, _ := l.Allow("a", 1, t0.Add(55*time.Second)); ok {
			allowed++
		}
	}
	if allowed != 3 {
		t.Errorf("a long pause allowed %d requests, want exactly the burst (3)", allowed)
	}
}

func TestExpensiveRequestsCostMore(t *testing.T) {
	l := mustNew(t, 60, 10, 100)

	for i := range 2 {
		if ok, _ := l.Allow("a", 5, t0); !ok {
			t.Fatalf("expensive request %d was refused", i+1)
		}
	}
	if ok, wait := l.Allow("a", 5, t0); ok || wait != 5*time.Second {
		t.Errorf("3rd expensive request: ok=%v, wait=%s; want refused and 5s", ok, wait)
	}
	if ok, wait := l.Allow("a", 1, t0); ok || wait != time.Second {
		t.Errorf("a cheap request with an empty bucket: ok=%v, wait=%s; want refused and 1s", ok, wait)
	}
}

func TestWaitingForRetryAfterIsEnough(t *testing.T) {
	// A partial refill first, so that the bucket holds a fraction of a token. 8 per minute with a burst of 1
	// is a case where plain floating point arithmetic comes up a hair short after the advertised wait.
	const pause = 137 * time.Millisecond

	for _, tc := range []struct{ perMinute, burst, cost int }{
		{60, 3, 1}, {7, 5, 5}, {1, 4, 3}, {600, 20, 5}, {13, 9, 2}, {100, 7, 3}, {8, 1, 1},
	} {
		drained := func() (l *Limiter, wait time.Duration) {
			l = mustNew(t, tc.perMinute, tc.burst, 10)
			for {
				if ok, _ := l.Allow("a", tc.cost, t0); !ok {
					break
				}
			}
			_, wait = l.Allow("a", tc.cost, t0.Add(pause))
			return l, wait
		}

		l, wait := drained()
		if ok, _ := l.Allow("a", tc.cost, t0.Add(pause+wait-time.Millisecond)); ok {
			t.Errorf("%+v: allowed 1ms before the advertised wait (%s)", tc, wait)
		}
		l, wait = drained()
		if ok, _ := l.Allow("a", tc.cost, t0.Add(pause+wait)); !ok {
			t.Errorf("%+v: still refused after waiting the advertised %s", tc, wait)
		}
	}
}

func TestClientsDoNotShareABucket(t *testing.T) {
	l := mustNew(t, 60, 2, 100)
	l.Allow("a", 1, t0)
	l.Allow("a", 1, t0)

	if ok, _ := l.Allow("a", 1, t0); ok {
		t.Error("client a should be out of tokens")
	}
	if ok, _ := l.Allow("b", 1, t0); !ok {
		t.Error("client b must not be affected by client a")
	}
}

func TestOddCostsAreNormalised(t *testing.T) {
	l := mustNew(t, 60, 3, 100)
	if ok, _ := l.Allow("big", 10, t0); !ok {
		t.Error("a cost above the burst is cut to the burst, so the first request is allowed")
	}
	if ok, wait := l.Allow("big", 10, t0); ok || wait != 3*time.Second {
		t.Errorf("second such request: ok=%v, wait=%s; want refused and 3s", ok, wait)
	}

	l = mustNew(t, 60, 2, 100)
	l.Allow("small", 0, t0)
	l.Allow("small", -5, t0)
	if ok, _ := l.Allow("small", 1, t0); ok {
		t.Error("a cost of 0 or less must still take one token")
	}
}

func TestIdleClientsAreForgotten(t *testing.T) {
	l := mustNew(t, 60, 5, 1000)
	for i := range 100 {
		l.Allow(fmt.Sprintf("idle-%d", i), 1, t0)
	}
	if got := len(l.buckets); got != 100 {
		t.Fatalf("tracking %d clients, want 100", got)
	}

	// busy empties its bucket just before the next sweep, so it still has missing tokens when the sweep runs.
	for range 5 {
		l.Allow("busy", 1, t0.Add(59*time.Second))
	}
	l.Allow("fresh", 1, t0.Add(61*time.Second))

	if got := len(l.buckets); got != 2 {
		t.Errorf("tracking %d clients, want 2 (busy and fresh): idle clients with full buckets must be forgotten", got)
	}
	if _, ok := l.buckets["busy"]; !ok {
		t.Error("a client that is still short of tokens must be kept")
	}
	if _, ok := l.buckets["idle-0"]; ok {
		t.Error("a client whose bucket is full again must be forgotten")
	}
}

func TestTableStaysBounded(t *testing.T) {
	l := mustNew(t, 60, 5, 10)
	for i := range 1000 {
		l.Allow(fmt.Sprintf("ip-%d", i), 1, t0)
		if got := len(l.buckets); got > 10 {
			t.Fatalf("after %d clients the table holds %d entries, want at most 10", i+1, got)
		}
	}
}

func TestClockGoingBackwardsAddsNothing(t *testing.T) {
	l := mustNew(t, 60, 2, 100)
	now := t0.Add(10 * time.Second)
	l.Allow("a", 1, now)
	l.Allow("a", 1, now)

	if ok, wait := l.Allow("a", 1, t0); ok || wait != time.Second {
		t.Errorf("a request stamped in the past: ok=%v, wait=%s; want refused and the bucket untouched (1s)", ok, wait)
	}
	if ok, _ := l.Allow("a", 1, now.Add(time.Second)); !ok {
		t.Error("one second after the last real refill one token must be back")
	}
}

func TestConcurrentUse(t *testing.T) {
	l := mustNew(t, 60, 5, 100)
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for g := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				if ok, _ := l.Allow(fmt.Sprintf("ip-%d", (g+i)%10), 1, t0); ok {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	// The time is fixed, so there is no refill: every one of the 10 clients gets exactly its burst.
	if got := allowed.Load(); got != 50 {
		t.Errorf("allowed %d requests, want exactly 10 clients x burst 5 = 50", got)
	}
}

// Package ratelimit limits how often a client may call the API.
//
// It is a token bucket per key (the client's IP address): a bucket holds up to burst tokens and refills at a
// steady rate, a request takes tokens (its cost) and is refused when there are not enough. Cheap requests cost
// one token, expensive ones (database aggregation) several.
//
// The package is small on purpose: no dependency, no background goroutine and no clock of its own, so tests
// run instantly and deterministically.
package ratelimit

import (
	"errors"
	"math"
	"sync"
	"time"
)

const (
	// sweepInterval is how often idle clients are forgotten.
	sweepInterval = time.Minute
	// epsilon absorbs floating point error: a client that waits exactly as long as it was told must get in.
	epsilon = 1e-9
)

// Limiter keeps one token bucket per key. It is safe for concurrent use; create it with New.
type Limiter struct {
	mu        sync.Mutex
	rate      float64 // tokens per second
	burst     float64
	maxKeys   int
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens  float64
	updated time.Time // when tokens was last brought up to date
}

// New returns a limiter whose buckets refill perMinute tokens per minute up to burst. It remembers at most
// maxKeys clients at a time, which bounds its memory.
func New(perMinute, burst, maxKeys int) (*Limiter, error) {
	if perMinute <= 0 || burst <= 0 || maxKeys <= 0 {
		return nil, errors.New("ratelimit: perMinute, burst and maxKeys must be positive")
	}
	return &Limiter{
		rate:    float64(perMinute) / 60,
		burst:   float64(burst),
		maxKeys: maxKeys,
		buckets: make(map[string]*bucket),
	}, nil
}

// Allow takes cost tokens from the bucket of key at time now. A new client starts with a full bucket.
// When the bucket has too few tokens nothing is taken, ok is false and retryAfter says how long to wait.
// A cost below one counts as one; a cost above burst is cut to burst, so no request is impossible forever.
func (l *Limiter) Allow(key string, cost int, now time.Time) (ok bool, retryAfter time.Duration) {
	need := math.Min(float64(max(cost, 1)), l.burst)

	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweepIfDue(now)
	b := l.buckets[key]
	if b == nil {
		l.makeRoom()
		b = &bucket{tokens: l.burst, updated: now}
		l.buckets[key] = b
	} else {
		l.refill(b, now)
	}

	if b.tokens >= need-epsilon {
		b.tokens = math.Max(0, b.tokens-need)
		return true, 0
	}
	return false, time.Duration(math.Ceil((need-b.tokens)/l.rate*1000)) * time.Millisecond
}

// refill brings the bucket up to date. A clock that went backwards adds nothing.
func (l *Limiter) refill(b *bucket, now time.Time) {
	if elapsed := now.Sub(b.updated).Seconds(); elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+elapsed*l.rate)
		b.updated = now
	}
}

// sweepIfDue forgets the clients whose bucket is full again: a missing bucket behaves exactly like a full one.
func (l *Limiter) sweepIfDue(now time.Time) {
	if now.Sub(l.lastSweep) < sweepInterval {
		return
	}
	l.lastSweep = now
	for key, b := range l.buckets {
		l.refill(b, now)
		if b.tokens >= l.burst {
			delete(l.buckets, key)
		}
	}
}

// makeRoom keeps the table bounded: when it is full, an arbitrary client is forgotten and starts with a full
// bucket next time. Bounded memory matters more than exact accounting when thousands of addresses show up at once.
func (l *Limiter) makeRoom() {
	if len(l.buckets) < l.maxKeys {
		return
	}
	for key := range l.buckets { // map order is random: this picks an arbitrary entry
		delete(l.buckets, key)
		return
	}
}

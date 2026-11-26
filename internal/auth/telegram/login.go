// Package telegram signs the owner in with "Log In With Telegram": the OpenID Connect authorization code flow
// with PKCE (https://core.telegram.org/bots/telegram-login).
//
// The flow has two steps. Begin prepares a sign-in and gives the address of the Telegram page where the user
// confirms it. Telegram then sends the browser back with a one-time code, and Finish trades the code for an ID
// token, checks it and says which Telegram account signed in. Whether that account is the owner, and the session,
// are the business of package auth.
package telegram

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"log/slog"
	"sync"
	"time"

	"climate-processor/internal/apperr"
)

const (
	// DefaultFlowTTL is how long the user has to confirm a sign-in in Telegram.
	DefaultFlowTTL = 10 * time.Minute
	// DefaultMaxPending bounds the sign-ins that were started and not yet finished (memory on a 1 GB VM).
	DefaultMaxPending = 1000

	secretBytes = 32 // 256 bits; encoded as 43 characters, within the 43..128 that PKCE allows for a verifier
)

// User is the Telegram account that signed in.
type User struct {
	ID       int64  // Telegram user id
	Name     string // display name
	Username string // @username without the @; may be empty
}

// IDToken is what the sign-in needs from a verified ID token.
type IDToken struct {
	Nonce string
	User  User
}

// Provider is the OpenID Connect side of the sign-in. It is declared here, next to its consumer: the real
// one (NewProvider) talks to Telegram, tests use a fake.
type Provider interface {
	// AuthURL is the Telegram page where the user confirms the sign-in.
	AuthURL(state, nonce, codeVerifier string) string
	// Exchange trades the one-time code for an ID token and verifies it: signature, issuer, audience, expiry.
	Exchange(ctx context.Context, code, codeVerifier string) (IDToken, error)
}

// Options are the optional settings of Login.
type Options struct {
	FlowTTL    time.Duration    // how long a started sign-in stays valid (default DefaultFlowTTL)
	MaxPending int              // how many started sign-ins are remembered at most (default DefaultMaxPending)
	Now        func() time.Time // clock, replaced in tests (default time.Now)
}

// flow is a started sign-in. Its secrets never leave the server: the browser only gets the state.
type flow struct {
	verifier string // PKCE code verifier, sent to Telegram with the code
	nonce    string // must come back inside the ID token
	expires  time.Time
}

// Login keeps the sign-ins in progress. It is safe for concurrent use.
//
// The flows live in memory: a restart of the service (a deployment) cancels the sign-ins in progress, and the
// user simply starts again.
type Login struct {
	log      *slog.Logger
	provider Provider
	ttl      time.Duration
	max      int
	now      func() time.Time

	mu      sync.Mutex
	pending map[string]flow // by state
}

// NewLogin returns a Login that works through the given provider.
func NewLogin(log *slog.Logger, provider Provider, opts Options) *Login {
	if log == nil {
		log = slog.Default()
	}
	if opts.FlowTTL <= 0 {
		opts.FlowTTL = DefaultFlowTTL
	}
	if opts.MaxPending <= 0 {
		opts.MaxPending = DefaultMaxPending
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Login{
		log:      log,
		provider: provider,
		ttl:      opts.FlowTTL,
		max:      opts.MaxPending,
		now:      opts.Now,
		pending:  make(map[string]flow),
	}
}

// FlowTTL is how long a started sign-in stays valid: the lifetime for the cookie that binds it to the browser.
func (l *Login) FlowTTL() time.Duration { return l.ttl }

// Begin starts a sign-in. It returns the address to send the browser to and the state. The caller must also
// hand the state to the same browser (in a cookie) and pass it back to Finish: that binds the sign-in to the
// browser that started it. Too many sign-ins in progress give an apperr.CodeTooManyRequests error.
func (l *Login) Begin() (redirectURL, state string, err error) {
	const op = "telegram.Begin"
	state, verifier, nonce := randomToken(), randomToken(), randomToken()

	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for s, f := range l.pending { // forget the sign-ins nobody finished
		if !now.Before(f.expires) {
			delete(l.pending, s)
		}
	}
	if len(l.pending) >= l.max {
		return "", "", apperr.New(apperr.CodeTooManyRequests, op, "too many sign-ins in progress, try again later")
	}
	l.pending[state] = flow{verifier: verifier, nonce: nonce, expires: now.Add(l.ttl)}
	return l.provider.AuthURL(state, nonce, verifier), state, nil
}

// Finish completes a sign-in. state and code are what Telegram sent to the callback, browserState is the state
// that Begin returned, read back from the browser (its cookie). A sign-in can be finished once.
//
// A state that is unknown, used, expired or not the browser's gives an apperr.CodeBadRequest error. A code that
// Telegram does not accept, a token that does not verify and a token that belongs to another sign-in give an
// apperr.CodeUnauthorized error; the reason is in the log, not in the error text.
func (l *Login) Finish(ctx context.Context, state, browserState, code string) (User, error) {
	const op = "telegram.Finish"
	if state == "" || code == "" || !equal(state, browserState) { // an empty browserState never equals a real state
		return User{}, apperr.New(apperr.CodeBadRequest, op, "this sign-in was not started in this browser, start again")
	}
	f, ok := l.take(state)
	if !ok {
		return User{}, apperr.New(apperr.CodeBadRequest, op, "the sign-in has expired or was already used, start again")
	}

	token, err := l.provider.Exchange(ctx, code, f.verifier)
	if err != nil {
		l.log.Warn("Telegram sign-in failed", "error", err)
		return User{}, apperr.Wrap(err, apperr.CodeUnauthorized, op, "Telegram sign-in failed")
	}
	if !equal(token.Nonce, f.nonce) || token.User.ID <= 0 {
		l.log.Warn("Telegram sign-in refused: the ID token does not belong to this sign-in")
		return User{}, apperr.New(apperr.CodeUnauthorized, op, "Telegram sign-in failed")
	}
	return token.User, nil
}

// take removes the flow of a state and returns it, unless it has expired.
func (l *Login) take(state string) (flow, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.pending[state]
	if !ok {
		return flow{}, false
	}
	delete(l.pending, state) // single use, even if the exchange fails: the code is single use too
	if !l.now().Before(f.expires) {
		return flow{}, false
	}
	return f, true
}

// randomToken returns 256 random bits as 43 URL-safe characters.
func randomToken() string {
	b := make([]byte, secretBytes)
	_, _ = rand.Read(b) // never fails: Go stops the program if the system has no randomness
	return base64.RawURLEncoding.EncodeToString(b)
}

// equal compares two secrets without leaking, through timing, how much of them matches.
func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

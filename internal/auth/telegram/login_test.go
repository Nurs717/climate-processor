package telegram

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"climate-processor/internal/apperr"
)

var owner = User{ID: 1001, Name: "Nursultan", Username: "nurs"}

// fakeProvider is a Provider that records what it was asked and answers as programmed.
type fakeProvider struct {
	mu sync.Mutex

	authURLs  []authURLCall
	exchanges []exchangeCall

	token      IDToken // answer of Exchange; a zero Nonce is replaced by the nonce of the sign-in
	emptyNonce bool    // return the token with no nonce at all
	err        error   // error of Exchange
}

type authURLCall struct{ state, nonce, verifier string }
type exchangeCall struct{ code, verifier string }

func (f *fakeProvider) AuthURL(state, nonce, codeVerifier string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authURLs = append(f.authURLs, authURLCall{state, nonce, codeVerifier})
	return "https://telegram.test/auth?state=" + state
}

func (f *fakeProvider) Exchange(_ context.Context, code, codeVerifier string) (IDToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exchanges = append(f.exchanges, exchangeCall{code, codeVerifier})
	if f.err != nil {
		return IDToken{}, f.err
	}
	token := f.token
	if token.Nonce == "" && !f.emptyNonce {
		// A well-behaved Telegram returns the nonce of the sign-in that the code belongs to; the verifier tells which.
		for _, c := range f.authURLs {
			if c.verifier == codeVerifier {
				token.Nonce = c.nonce
			}
		}
	}
	return token, nil
}

func (f *fakeProvider) exchangeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.exchanges)
}

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

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
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

type testEnv struct {
	login    *Login
	provider *fakeProvider
	clock    *testClock
	logs     *syncBuffer
}

func newTestEnv(t *testing.T, opts Options) *testEnv {
	t.Helper()
	e := &testEnv{
		provider: &fakeProvider{token: IDToken{User: owner}},
		clock:    &testClock{t: time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)},
		logs:     &syncBuffer{},
	}
	opts.Now = e.clock.now
	e.login = NewLogin(slog.New(slog.NewJSONHandler(e.logs, nil)), e.provider, opts)
	return e
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

func TestBeginSendsTheBrowserToTelegram(t *testing.T) {
	e := newTestEnv(t, Options{})

	url, state, err := e.login.Begin()

	if err != nil {
		t.Fatal(err)
	}
	if want := "https://telegram.test/auth?state=" + state; url != want {
		t.Errorf("url = %q, want %q", url, want)
	}
	call := e.provider.authURLs[0]
	if call.state != state {
		t.Errorf("the provider got state %q, Begin returned %q", call.state, state)
	}
	for name, secret := range map[string]string{"state": call.state, "nonce": call.nonce, "verifier": call.verifier} {
		if len(secret) != 43 {
			t.Errorf("%s %q has %d characters, want 43 (256 bits, valid for PKCE)", name, secret, len(secret))
		}
	}
	if call.state == call.nonce || call.nonce == call.verifier || call.state == call.verifier {
		t.Error("state, nonce and verifier must be three different secrets")
	}
}

func TestEverySignInHasItsOwnSecrets(t *testing.T) {
	e := newTestEnv(t, Options{})
	seen := map[string]bool{}

	for range 20 {
		if _, _, err := e.login.Begin(); err != nil {
			t.Fatal(err)
		}
	}

	for _, c := range e.provider.authURLs {
		for _, secret := range []string{c.state, c.nonce, c.verifier} {
			if seen[secret] {
				t.Fatalf("secret %q was used twice", secret)
			}
			seen[secret] = true
		}
	}
}

func TestFinishReturnsTheTelegramUser(t *testing.T) {
	e := newTestEnv(t, Options{})
	_, state, _ := e.login.Begin()

	user, err := e.login.Finish(context.Background(), state, state, "the-code")

	if err != nil {
		t.Fatal(err)
	}
	if user != owner {
		t.Errorf("user = %+v, want %+v", user, owner)
	}
	if got := e.provider.exchanges[0]; got.code != "the-code" || got.verifier != e.provider.authURLs[0].verifier {
		t.Errorf("exchange = %+v: the code of the callback and the verifier of this sign-in are expected", got)
	}
}

func TestSignInCannotBeFinishedFromAnotherBrowser(t *testing.T) {
	e := newTestEnv(t, Options{})
	_, state, _ := e.login.Begin()

	for name, browserState := range map[string]string{"another state": strings.Repeat("A", 43), "no cookie": ""} {
		_, err := e.login.Finish(context.Background(), state, browserState, "code")

		wantCode(t, err, apperr.CodeBadRequest)
		if n := e.provider.exchangeCount(); n != 0 {
			t.Errorf("%s: Telegram was asked %d times, want 0", name, n)
		}
	}
	// The failed attempts must not have used up the sign-in of the real browser.
	if _, err := e.login.Finish(context.Background(), state, state, "code"); err != nil {
		t.Errorf("the right browser can still finish: %v", err)
	}
}

func TestIncompleteCallbackIsRefused(t *testing.T) {
	e := newTestEnv(t, Options{})
	_, state, _ := e.login.Begin()

	for name, args := range map[string][3]string{
		"no state": {"", state, "code"},
		"no code":  {state, state, ""},
		"nothing":  {"", "", ""},
	} {
		_, err := e.login.Finish(context.Background(), args[0], args[1], args[2])
		if apperr.CodeOf(err) != apperr.CodeBadRequest {
			t.Errorf("%s: code = %s, want bad_request (err: %v)", name, apperr.CodeOf(err), err)
		}
	}
	if n := e.provider.exchangeCount(); n != 0 {
		t.Errorf("Telegram was asked %d times, want 0", n)
	}
}

func TestUnknownStateIsRefused(t *testing.T) {
	e := newTestEnv(t, Options{})
	forged := strings.Repeat("B", 43)

	_, err := e.login.Finish(context.Background(), forged, forged, "code")

	wantCode(t, err, apperr.CodeBadRequest)
	if n := e.provider.exchangeCount(); n != 0 {
		t.Errorf("Telegram was asked %d times, want 0", n)
	}
}

func TestSignInCanBeFinishedOnlyOnce(t *testing.T) {
	e := newTestEnv(t, Options{})
	_, state, _ := e.login.Begin()
	if _, err := e.login.Finish(context.Background(), state, state, "code"); err != nil {
		t.Fatal(err)
	}

	_, err := e.login.Finish(context.Background(), state, state, "code") // the callback URL is replayed

	wantCode(t, err, apperr.CodeBadRequest)
	if n := e.provider.exchangeCount(); n != 1 {
		t.Errorf("Telegram was asked %d times, want 1", n)
	}
}

func TestSignInExpires(t *testing.T) {
	e := newTestEnv(t, Options{FlowTTL: 5 * time.Minute})

	_, early, _ := e.login.Begin()
	e.clock.advance(5*time.Minute - time.Second)
	if _, err := e.login.Finish(context.Background(), early, early, "code"); err != nil {
		t.Fatalf("one second before the end: %v", err)
	}

	_, late, _ := e.login.Begin()
	e.clock.advance(5 * time.Minute) // exactly the lifetime
	_, err := e.login.Finish(context.Background(), late, late, "code")

	wantCode(t, err, apperr.CodeBadRequest)
	if n := e.provider.exchangeCount(); n != 1 {
		t.Errorf("Telegram was asked %d times, want 1 (not for the expired one)", n)
	}
}

func TestFlowTTLDefaultAndOption(t *testing.T) {
	if got := newTestEnv(t, Options{}).login.FlowTTL(); got != DefaultFlowTTL {
		t.Errorf("default = %s, want %s", got, DefaultFlowTTL)
	}
	if got := newTestEnv(t, Options{FlowTTL: time.Minute}).login.FlowTTL(); got != time.Minute {
		t.Errorf("custom = %s, want 1m", got)
	}
	if got := newTestEnv(t, Options{FlowTTL: -time.Minute}).login.FlowTTL(); got != DefaultFlowTTL {
		t.Errorf("a negative value = %s, want the default", got)
	}
}

func TestTelegramRefusalIsUnauthorizedAndUsesUpTheSignIn(t *testing.T) {
	e := newTestEnv(t, Options{})
	_, state, _ := e.login.Begin()
	boom := errors.New("invalid_grant")
	e.provider.err = boom

	_, err := e.login.Finish(context.Background(), state, state, "bad-code")

	wantCode(t, err, apperr.CodeUnauthorized)
	if !errors.Is(err, boom) {
		t.Error("the cause must stay in the error chain for the logs")
	}
	if got := apperr.ReasonOf(err); strings.Contains(got, "invalid_grant") {
		t.Errorf("the reason shown to the client leaks the cause: %q", got)
	}
	if !strings.Contains(e.logs.String(), "invalid_grant") {
		t.Errorf("the failure must be logged: %s", e.logs.String())
	}
	e.provider.err = nil
	if _, err := e.login.Finish(context.Background(), state, state, "good-code"); err == nil {
		t.Error("a failed attempt must use up the sign-in: the code is single use anyway")
	}
}

func TestTokenOfAnotherSignInIsRefused(t *testing.T) {
	e := newTestEnv(t, Options{})
	_, state, _ := e.login.Begin()
	e.provider.token = IDToken{Nonce: strings.Repeat("C", 43), User: owner} // an ID token issued for another nonce

	_, err := e.login.Finish(context.Background(), state, state, "code")

	wantCode(t, err, apperr.CodeUnauthorized)
}

func TestTokenWithoutNonceIsRefused(t *testing.T) {
	e := newTestEnv(t, Options{})
	_, state, _ := e.login.Begin()
	e.provider.emptyNonce = true

	_, err := e.login.Finish(context.Background(), state, state, "code")

	wantCode(t, err, apperr.CodeUnauthorized)
}

func TestTokenWithoutAUserIsRefused(t *testing.T) {
	for _, id := range []int64{0, -5} {
		e := newTestEnv(t, Options{})
		_, state, _ := e.login.Begin()
		e.provider.token = IDToken{User: User{ID: id, Name: "nobody"}}

		_, err := e.login.Finish(context.Background(), state, state, "code")

		wantCode(t, err, apperr.CodeUnauthorized)
	}
}

func TestTooManySignInsInProgress(t *testing.T) {
	e := newTestEnv(t, Options{MaxPending: 3, FlowTTL: time.Minute})
	var first string
	for i := range 3 {
		_, state, err := e.login.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = state
		}
	}

	_, _, err := e.login.Begin()
	wantCode(t, err, apperr.CodeTooManyRequests)

	// Finishing one frees a place.
	if _, err := e.login.Finish(context.Background(), first, first, "code"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.login.Begin(); err != nil {
		t.Errorf("a place was freed: %v", err)
	}

	// Expired ones free places too: nobody has to finish them.
	_, _, err = e.login.Begin()
	wantCode(t, err, apperr.CodeTooManyRequests)
	e.clock.advance(time.Minute)
	if _, _, err := e.login.Begin(); err != nil {
		t.Errorf("expired sign-ins must be forgotten: %v", err)
	}
}

func TestMemoryIsReleasedForExpiredSignIns(t *testing.T) {
	e := newTestEnv(t, Options{FlowTTL: time.Minute})
	for range 10 {
		if _, _, err := e.login.Begin(); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.advance(time.Minute)

	if _, _, err := e.login.Begin(); err != nil {
		t.Fatal(err)
	}

	e.login.mu.Lock()
	defer e.login.mu.Unlock()
	if n := len(e.login.pending); n != 1 {
		t.Errorf("%d sign-ins are remembered, want 1: the expired ones must be dropped", n)
	}
}

func TestSecretsAreNeverLogged(t *testing.T) {
	e := newTestEnv(t, Options{})
	_, state, _ := e.login.Begin()
	e.provider.err = errors.New("invalid_grant")
	_, _ = e.login.Finish(context.Background(), state, state, "super-secret-code")
	call := e.provider.authURLs[0]

	logs := e.logs.String()

	for name, secret := range map[string]string{"state": call.state, "nonce": call.nonce, "verifier": call.verifier, "code": "super-secret-code"} {
		if strings.Contains(logs, secret) {
			t.Errorf("the %s is in the logs: %s", name, logs)
		}
	}
}

func TestConcurrentSignIns(t *testing.T) {
	e := newTestEnv(t, Options{})
	var wg sync.WaitGroup
	errs := make(chan error, 50)

	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, state, err := e.login.Begin()
			if err != nil {
				errs <- err
				return
			}
			if _, err := e.login.Finish(context.Background(), state, state, "code"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

func TestOneStateIsFinishedByExactlyOneOfManyRacers(t *testing.T) {
	e := newTestEnv(t, Options{})
	_, state, _ := e.login.Begin()
	var wg sync.WaitGroup
	var mu sync.Mutex
	succeeded := 0

	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.login.Finish(context.Background(), state, state, "code"); err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if succeeded != 1 {
		t.Errorf("%d callbacks succeeded, want exactly 1", succeeded)
	}
}

package rest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"climate-processor/internal/apperr"
	"climate-processor/internal/auth"
	"climate-processor/internal/auth/telegram"
	"climate-processor/internal/domain"
)

const (
	ownerToken = "owner-token"
	ownerID    = int64(42)
	ownerAgent = "TestBrowser/1.0"
)

// fakeSessions is a Sessions whose answers the tests control; it records its calls.
type fakeSessions struct {
	owners    map[string]auth.Identity // token -> who it belongs to; any other token is unauthorized
	authErr   error                    // returned by Authenticate for every token
	createErr error
	revokeErr error

	authCalls     []string
	created       []createCall
	createHadTime bool // the context of Create had a deadline
	revoked       []string
	sessionTime   time.Duration // lifetime of a created session
}

type createCall struct {
	userID    int64
	userAgent string
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{
		owners:      map[string]auth.Identity{ownerToken: {Role: domain.RoleOwner, UserID: ownerID, SessionID: "sid"}},
		sessionTime: 24 * time.Hour,
	}
}

func (f *fakeSessions) Authenticate(_ context.Context, token string) (auth.Identity, error) {
	f.authCalls = append(f.authCalls, token)
	if f.authErr != nil {
		return auth.Identity{}, f.authErr
	}
	if id, ok := f.owners[token]; ok {
		return id, nil
	}
	return auth.Identity{}, apperr.New(apperr.CodeUnauthorized, "fake.Authenticate", "invalid or expired session")
}

func (f *fakeSessions) Create(ctx context.Context, userID int64, userAgent string) (string, domain.Session, error) {
	f.created = append(f.created, createCall{userID, userAgent})
	_, f.createHadTime = ctx.Deadline()
	if f.createErr != nil {
		return "", domain.Session{}, f.createErr
	}
	return "new-token", domain.Session{ID: "new-sid", UserID: userID, ExpiresAt: testNow.Add(f.sessionTime)}, nil
}

func (f *fakeSessions) Revoke(_ context.Context, token string) error {
	f.revoked = append(f.revoked, token)
	return f.revokeErr
}

// fakeSignIn is a SignIn whose answers the tests control; it records its calls.
type fakeSignIn struct {
	beginErr  error
	finishErr error
	user      telegram.User

	begins        int
	finishes      []finishCall
	finishHadTime bool          // the context of Finish had a deadline
	finishWithin  time.Duration // how far away it was
}

type finishCall struct{ state, browserState, code string }

const (
	telegramURL = "https://oauth.telegram.org/auth?state=st-1"
	flowState   = "st-1"
)

func (f *fakeSignIn) Begin() (string, string, error) {
	f.begins++
	if f.beginErr != nil {
		return "", "", f.beginErr
	}
	return telegramURL, flowState, nil
}

func (f *fakeSignIn) Finish(ctx context.Context, state, browserState, code string) (telegram.User, error) {
	f.finishes = append(f.finishes, finishCall{state, browserState, code})
	if deadline, ok := ctx.Deadline(); ok {
		f.finishHadTime, f.finishWithin = true, time.Until(deadline)
	}
	return f.user, f.finishErr
}

func (f *fakeSignIn) FlowTTL() time.Duration { return 10 * time.Minute }

type authEnv struct {
	*testEnv
	sessions *fakeSessions
	signIn   *fakeSignIn
}

// newAuthEnv is a router with sessions and the sign-in; tweaks change the options after those are set.
func newAuthEnv(t *testing.T, tweaks ...func(*Options)) *authEnv {
	t.Helper()
	s, si := newFakeSessions(), &fakeSignIn{user: telegram.User{ID: ownerID, Name: "Owner", Username: "owner"}}
	all := append([]func(*Options){func(o *Options) { o.Sessions, o.SignIn = s, si }}, tweaks...)
	return &authEnv{testEnv: newTestEnv(t, all...), sessions: s, signIn: si}
}

// do sends a request; mods set the headers, cookies and the peer address.
func (e *testEnv) do(method, target string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for _, mod := range mods {
		mod(req)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func withCookie(name, value string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: name, Value: value}) }
}

func withHeader(key, value string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

func withSecureCookies(o *Options) { o.SecureCookies = true }

// cookie returns the cookie of that name that the response sets, or nil.
func cookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// wantRemoved checks that the response tells the browser to forget the cookie.
func wantRemoved(t *testing.T, rec *httptest.ResponseRecorder, name, path string) {
	t.Helper()
	c := cookie(rec, name)
	if c == nil {
		t.Fatalf("cookie %s is not set; want it removed. Set-Cookie: %q", name, rec.Header().Values("Set-Cookie"))
	}
	if c.MaxAge >= 0 || c.Value != "" || c.Path != path {
		t.Errorf("cookie %s = %+v, want an empty value, Path=%s and Max-Age=0", name, c, path)
	}
}

func wantNoCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) {
	t.Helper()
	if c := cookie(rec, name); c != nil {
		t.Errorf("cookie %s must not be set, got %+v", name, c)
	}
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	return decode[errorBody](t, rec).Error.Code
}

func TestMeIsDemoWithoutCredentials(t *testing.T) {
	e := newAuthEnv(t)

	rec := e.get("/api/v1/auth/me")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body)
	}
	got := decode[meDTO](t, rec)
	if got.Role != domain.RoleDemo || got.UserID != 0 || !got.SignInEnabled {
		t.Errorf("got %+v, want demo, no user, sign-in enabled", got)
	}
	if strings.Contains(rec.Body.String(), "user_id") {
		t.Errorf("an anonymous client has no user id in the response: %s", rec.Body)
	}
	if len(e.sessions.authCalls) != 0 {
		t.Error("a request without a token must not touch the session store")
	}
}

func TestMeWithoutSignInSaysSoAndStaysDemo(t *testing.T) {
	e := newTestEnv(t) // neither sessions nor sign-in: owner sign-in is switched off

	rec := e.do(http.MethodGet, "/api/v1/auth/me", withCookie(sessionCookie, ownerToken))

	got := decode[meDTO](t, rec)
	if rec.Code != http.StatusOK || got.Role != domain.RoleDemo || got.SignInEnabled {
		t.Errorf("status %d, got %+v; want 200, demo, sign-in disabled", rec.Code, got)
	}
}

func TestOwnerIsRecognisedByCookieOrBearer(t *testing.T) {
	tests := []struct {
		name      string
		mods      []func(*http.Request)
		wantToken string
	}{
		{"cookie", []func(*http.Request){withCookie(sessionCookie, ownerToken)}, ownerToken},
		{"bearer", []func(*http.Request){withHeader("Authorization", "Bearer "+ownerToken)}, ownerToken},
		{"bearer in any case", []func(*http.Request){withHeader("Authorization", "bEaReR "+ownerToken)}, ownerToken},
		{"bearer wins over the cookie", []func(*http.Request){
			withCookie(sessionCookie, "stale-cookie"), withHeader("Authorization", "Bearer "+ownerToken),
		}, ownerToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAuthEnv(t)

			rec := e.do(http.MethodGet, "/api/v1/auth/me", tt.mods...)

			got := decode[meDTO](t, rec)
			if rec.Code != http.StatusOK || got.Role != domain.RoleOwner || got.UserID != ownerID {
				t.Fatalf("status %d, got %+v; want 200, owner %d", rec.Code, got, ownerID)
			}
			if len(e.sessions.authCalls) != 1 || e.sessions.authCalls[0] != tt.wantToken {
				t.Errorf("Authenticate calls = %q, want exactly [%q]", e.sessions.authCalls, tt.wantToken)
			}
		})
	}
}

func TestAuthorizationHeadersOfOtherKindsAreIgnored(t *testing.T) {
	for _, header := range []string{"Basic dXNlcjpwYXNz", "Bearer", "Bearer   ", "Token abc", "Bearerabc"} {
		e := newAuthEnv(t)

		rec := e.do(http.MethodGet, "/api/v1/auth/me", withHeader("Authorization", header))

		if got := decode[meDTO](t, rec); rec.Code != http.StatusOK || got.Role != domain.RoleDemo {
			t.Errorf("Authorization %q: status %d, role %q; want 200, demo", header, rec.Code, got.Role)
		}
		if len(e.sessions.authCalls) != 0 {
			t.Errorf("Authorization %q must not reach the session store: %q", header, e.sessions.authCalls)
		}
	}
}

func TestAnotherKindOfAuthorizationDoesNotHideTheCookie(t *testing.T) {
	// A site behind HTTP Basic authentication (a proxy, say) still has its session cookie.
	for _, header := range []string{"Basic dXNlcjpwYXNz", "Bearer", "Bearer   "} {
		e := newAuthEnv(t)

		rec := e.do(http.MethodGet, "/api/v1/auth/me", withHeader("Authorization", header), withCookie(sessionCookie, ownerToken))

		if got := decode[meDTO](t, rec); rec.Code != http.StatusOK || got.Role != domain.RoleOwner {
			t.Errorf("Authorization %q: status %d, role %q; want the owner from the cookie", header, rec.Code, got.Role)
		}
	}
}

func TestInvalidCookieIsRefusedAndRemoved(t *testing.T) {
	e := newAuthEnv(t)

	rec := e.do(http.MethodGet, "/api/v1/latest", withCookie(sessionCookie, "expired-token"))

	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "unauthorized" {
		t.Fatalf("status = %d (%s), want 401 unauthorized", rec.Code, rec.Body)
	}
	wantRemoved(t, rec, sessionCookie, "/")
	if e.reader.queries != 0 {
		t.Error("a refused request must not reach the service")
	}
}

func TestInvalidBearerIsRefusedWithoutTouchingCookies(t *testing.T) {
	e := newAuthEnv(t)

	rec := e.do(http.MethodGet, "/api/v1/auth/me", withHeader("Authorization", "Bearer nope"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("an app has no cookie to remove: %q", got)
	}
}

func TestSessionStoreOutageIsNotMistakenForABadToken(t *testing.T) {
	e := newAuthEnv(t)
	e.sessions.authErr = apperr.New(apperr.CodeUnavailable, "fake", "sign-in is temporarily unavailable")

	rec := e.do(http.MethodGet, "/api/v1/auth/me", withCookie(sessionCookie, ownerToken))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	wantNoCookie(t, rec, sessionCookie) // the session may be fine: the owner must not be signed out by an outage
}

func TestDataEndpointsIdentifyTheClient(t *testing.T) {
	for _, path := range []string{"/api/v1/latest", "/api/v1/readings", "/api/v1/stats"} {
		e := newAuthEnv(t)
		e.reader.hasLatest = true

		owner := e.do(http.MethodGet, path, withCookie(sessionCookie, ownerToken))
		demo := e.get(path)
		stale := e.do(http.MethodGet, path, withCookie(sessionCookie, "stale"))

		if owner.Code != http.StatusOK || demo.Code != http.StatusOK {
			t.Errorf("%s: owner %d, anonymous %d; want 200 for both", path, owner.Code, demo.Code)
		}
		if stale.Code != http.StatusUnauthorized {
			t.Errorf("%s with a stale cookie: %d, want 401", path, stale.Code)
		}
	}
}

func TestRateLimitComesBeforeTheSessionLookup(t *testing.T) {
	e := newAuthEnv(t, withLimiter(&fakeLimiter{ok: false, wait: time.Second}))

	rec := e.do(http.MethodGet, "/api/v1/auth/me", withCookie(sessionCookie, "made-up"))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if len(e.sessions.authCalls) != 0 {
		t.Error("a request the limiter refuses must not reach the session store")
	}
}

func TestAuthRoutesAreChargedByTheirCost(t *testing.T) {
	lim := &fakeLimiter{ok: true}
	e := newAuthEnv(t, withLimiter(lim))

	e.get("/api/v1/auth/me")
	e.get("/api/v1/auth/telegram/login")
	e.get(TelegramCallbackPath)
	e.do(http.MethodPost, "/api/v1/auth/logout")

	var costs []int
	for _, c := range lim.calls {
		costs = append(costs, c.cost)
	}
	// Starting a sign-in is priced like a history query: it takes room in the server's memory.
	if want := []int{1, 5, 5, 1}; !slices.Equal(costs, want) {
		t.Errorf("costs of me, login, callback, logout = %v, want %v", costs, want)
	}
}

func TestSignInRoutesAreAbsentWithoutSignIn(t *testing.T) {
	e := newTestEnv(t, func(o *Options) { o.Sessions = newFakeSessions() }) // sessions, but no sign-in

	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/auth/telegram/login"},
		{http.MethodGet, TelegramCallbackPath},
		{http.MethodPost, "/api/v1/auth/logout"},
	} {
		rec := e.do(tt.method, tt.path)
		if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
			t.Errorf("%s %s: status %d, want the JSON 404", tt.method, tt.path, rec.Code)
		}
	}
}

func TestNewRouterRejectsSignInWithoutSessions(t *testing.T) {
	_, err := NewRouter(nil, &fakeReader{}, Options{SignIn: &fakeSignIn{}})
	if err == nil {
		t.Error("a sign-in that cannot start a session must fail the start")
	}
}

func TestLoginSendsTheBrowserToTelegramAndRemembersIt(t *testing.T) {
	e := newAuthEnv(t)

	rec := e.get("/api/v1/auth/telegram/login")

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != telegramURL {
		t.Fatalf("status %d, Location %q; want 302 to %s", rec.Code, rec.Header().Get("Location"), telegramURL)
	}
	c := cookie(rec, flowCookie)
	if c == nil {
		t.Fatal("the state of the sign-in must be handed to the browser")
	}
	if c.Value != flowState || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/api/v1/auth/telegram" ||
		c.MaxAge != 600 || c.Secure {
		t.Errorf("flow cookie = %+v; want the state, HttpOnly, Lax, Path=/api/v1/auth/telegram, Max-Age=600, not Secure", c)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
}

func TestLoginCookieIsSecureWhenAsked(t *testing.T) {
	e := newAuthEnv(t, withSecureCookies)

	if c := cookie(e.get("/api/v1/auth/telegram/login"), flowCookie); c == nil || !c.Secure {
		t.Errorf("flow cookie = %+v, want Secure", c)
	}
}

func TestLoginFailureSendsTheBrowserBackWithTheReasonAndWithoutACookie(t *testing.T) {
	e := newAuthEnv(t)
	e.signIn.beginErr = apperr.New(apperr.CodeTooManyRequests, "fake", "too many sign-ins in progress, try again later")

	rec := e.get("/api/v1/auth/telegram/login")

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/?signin=busy" {
		t.Errorf("status %d, Location %q; want 302 to /?signin=busy", rec.Code, rec.Header().Get("Location"))
	}
	wantNoCookie(t, rec, flowCookie)
}

// callback sends the browser back from Telegram: the flow cookie and the query that Telegram adds.
func (e *authEnv) callback(query string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	return e.do(http.MethodGet, TelegramCallbackPath+"?"+query, mods...)
}

func TestCallbackSignsTheOwnerIn(t *testing.T) {
	e := newAuthEnv(t)

	rec := e.callback("code=the-code&state=url-state",
		withCookie(flowCookie, "browser-state"), withHeader("User-Agent", ownerAgent))

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("status %d, Location %q; want 302 to / (body: %s)", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	if len(e.signIn.finishes) != 1 || e.signIn.finishes[0] != (finishCall{"url-state", "browser-state", "the-code"}) {
		t.Errorf("Finish got %+v: the state of the address, the state of the cookie and the code are three different things", e.signIn.finishes)
	}
	if len(e.sessions.created) != 1 || e.sessions.created[0] != (createCall{ownerID, ownerAgent}) {
		t.Errorf("Create got %+v, want the Telegram id %d and the browser's User-Agent", e.sessions.created, ownerID)
	}
	c := cookie(rec, sessionCookie)
	if c == nil {
		t.Fatal("the session cookie is missing")
	}
	if c.Value != "new-token" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" ||
		c.MaxAge != 24*60*60 || c.Secure {
		t.Errorf("session cookie = %+v; want the token, HttpOnly, Lax, Path=/, Max-Age=86400 (the session lifetime), not Secure", c)
	}
	wantRemoved(t, rec, flowCookie, "/api/v1/auth/telegram")
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q: the one-time code must not leak in a Referer header", got)
	}
}

func TestCallbackCookiesAreSecureWhenAsked(t *testing.T) {
	e := newAuthEnv(t, withSecureCookies)

	rec := e.callback("code=c&state=s", withCookie(flowCookie, "s"))

	for _, name := range []string{sessionCookie, flowCookie} {
		if c := cookie(rec, name); c == nil || !c.Secure {
			t.Errorf("cookie %s = %+v, want Secure", name, c)
		}
	}
}

func TestCallbackGivesTheSignInLimitedTime(t *testing.T) {
	e := newAuthEnv(t)

	e.callback("code=c&state=s", withCookie(flowCookie, "s"))

	if !e.signIn.finishHadTime || e.signIn.finishWithin <= 0 || e.signIn.finishWithin > signInTimeout {
		t.Errorf("Finish deadline: set=%v, in %s; want within %s", e.signIn.finishHadTime, e.signIn.finishWithin, signInTimeout)
	}
	if !e.sessions.createHadTime {
		t.Error("Create must run under the same deadline")
	}
}

func TestCallbackWithoutTheFlowCookieAsksFinishWithAnEmptyBrowserState(t *testing.T) {
	e := newAuthEnv(t)
	e.signIn.finishErr = apperr.New(apperr.CodeBadRequest, "fake", "this sign-in was not started in this browser, start again")

	rec := e.callback("code=c&state=s")

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/?signin=expired" {
		t.Errorf("status %d, Location %q; want 302 to /?signin=expired", rec.Code, rec.Header().Get("Location"))
	}
	if len(e.signIn.finishes) != 1 || e.signIn.finishes[0].browserState != "" {
		t.Errorf("Finish got %+v, want an empty browser state", e.signIn.finishes)
	}
}

func TestCallbackFailures(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*authEnv)
		query      string
		wantReason string // after /?signin=
		wantFinish int    // how many times Finish must be called
		wantLoud   bool   // a failure of ours: it is also logged at ERROR
	}{
		{
			name: "Telegram does not accept the sign-in",
			setup: func(e *authEnv) {
				e.signIn.finishErr = apperr.New(apperr.CodeUnauthorized, "fake", "Telegram sign-in failed")
			},
			query: "code=c&state=s", wantReason: "failed", wantFinish: 1,
		},
		{
			name:  "the state is unknown or used",
			setup: func(e *authEnv) { e.signIn.finishErr = apperr.New(apperr.CodeBadRequest, "fake", "expired") },
			query: "code=c&state=s", wantReason: "expired", wantFinish: 1,
		},
		{
			name: "the account is not an owner",
			setup: func(e *authEnv) {
				e.sessions.createErr = apperr.New(apperr.CodeForbidden, "fake", "this account is not the owner")
			},
			query: "code=c&state=s", wantReason: "not_owner", wantFinish: 1,
		},
		{
			name: "the session cannot be stored",
			setup: func(e *authEnv) {
				e.sessions.createErr = apperr.New(apperr.CodeUnavailable, "fake", "sign-in is temporarily unavailable")
			},
			query: "code=c&state=s", wantReason: "unavailable", wantFinish: 1, wantLoud: true,
		},
		{
			name:  "an unexpected failure",
			setup: func(e *authEnv) { e.signIn.finishErr = errors.New("disk on fire") },
			query: "code=c&state=s", wantReason: "failed", wantFinish: 1, wantLoud: true,
		},
		{
			name:  "the user pressed Cancel in Telegram",
			query: "error=access_denied&error_description=The+user+denied+access&state=s", wantReason: "cancelled", wantFinish: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAuthEnv(t)
			if tt.setup != nil {
				tt.setup(e)
			}

			rec := e.callback(tt.query, withCookie(flowCookie, "s"))

			// The browser is sent home, not left on a page of JSON: the home page says what happened.
			if want := "/?signin=" + tt.wantReason; rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
				t.Errorf("status %d, Location %q (%s); want 302 to %s", rec.Code, rec.Header().Get("Location"), rec.Body, want)
			}
			wantNoCookie(t, rec, sessionCookie)
			wantRemoved(t, rec, flowCookie, "/api/v1/auth/telegram") // a sign-in is never retried with the same state
			if len(e.signIn.finishes) != tt.wantFinish {
				t.Errorf("Finish was called %d times, want %d", len(e.signIn.finishes), tt.wantFinish)
			}
			if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
				t.Errorf("Referrer-Policy = %q: it must be set on errors too", got)
			}
			if r := e.record(t, "request done"); r["error"] == nil || r["status"] != float64(http.StatusFound) {
				t.Errorf("the access log must keep the cause of a failed sign-in; record = %v", r)
			}
			loud := false
			for _, r := range e.records(t) {
				if r["message"] == "sign-in failed" {
					loud = true
					if r["level"] != "ERROR" || r["reason"] != tt.wantReason || r["request_id"] == nil {
						t.Errorf("record = %v, want ERROR with the reason and the request id", r)
					}
				}
			}
			if loud != tt.wantLoud {
				t.Errorf("logged at ERROR = %v, want %v: only a failure of ours is an error", loud, tt.wantLoud)
			}
		})
	}
}

func TestCallbackOfABrowserThatLeftIsNotRedirected(t *testing.T) {
	e := newAuthEnv(t)
	e.signIn.finishErr = fmt.Errorf("telegram: %w", context.Canceled)

	rec := e.callback("code=c&state=s", withCookie(flowCookie, "s"))

	if rec.Code != 499 || rec.Header().Get("Location") != "" {
		t.Errorf("status %d, Location %q; want 499 and no redirect: nobody is there to follow it", rec.Code, rec.Header().Get("Location"))
	}
}

func TestReasonFor(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{apperr.New(apperr.CodeForbidden, "x", "no"), "not_owner"},
		{apperr.New(apperr.CodeBadRequest, "x", "no"), "expired"},
		{apperr.New(apperr.CodeTooManyRequests, "x", "no"), "busy"},
		{apperr.New(apperr.CodeUnavailable, "x", "no"), "unavailable"},
		{apperr.New(apperr.CodeUnauthorized, "x", "no"), "failed"},
		{apperr.New(apperr.CodeUnknown, "x", "no"), "failed"},
		{apperr.New(apperr.CodeNotFound, "x", "no"), "failed"},
		{errors.New("not an application error"), "failed"},
		{fmt.Errorf("wrapped: %w", apperr.New(apperr.CodeForbidden, "x", "no")), "not_owner"},
	}
	for _, tt := range tests {
		if got := reasonFor(tt.err); got != tt.want {
			t.Errorf("reasonFor(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

// The web app shows a sentence for the reason it finds in the address, and nothing for a reason it does not know:
// a reason that is missing there would send the person home without a word.
func TestTheWebAppHasASentenceForEveryReason(t *testing.T) {
	source, err := os.ReadFile("../../web/static/account.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{reasonCancelled, reasonNotOwner, reasonExpired, reasonBusy, reasonUnavailable, reasonFailed} {
		if !regexp.MustCompile(`\b` + reason + `\s*:`).Match(source) {
			t.Errorf("static/account.js has no sentence for the reason %q", reason)
		}
	}
}

func TestRefusedStrangerIsLoggedWithTheirTelegramId(t *testing.T) {
	e := newAuthEnv(t)
	e.signIn.user = telegram.User{ID: 777, Name: "Stranger", Username: "stranger"}
	e.sessions.createErr = apperr.New(apperr.CodeForbidden, "fake", "this account is not the owner")

	e.callback("code=c&state=s", withCookie(flowCookie, "s"))

	r := e.record(t, "sign-in refused: the account is not an owner")
	if r["level"] != "WARN" || r["telegram_id"] != float64(777) || r["username"] != "stranger" {
		t.Errorf("record = %v", r)
	}
}

func TestCallbackDoesNotEchoWhatTelegramSent(t *testing.T) {
	e := newAuthEnv(t)
	long := strings.Repeat("x", 500)

	rec := e.callback("error=<script>"+long+"</script>", withCookie(flowCookie, "s"))

	if strings.Contains(rec.Body.String(), "script") || strings.Contains(rec.Header().Get("Location"), "script") {
		t.Errorf("the response repeats the text of the address: %s (Location %q)", rec.Body, rec.Header().Get("Location"))
	}
	if got := rec.Header().Get("Location"); got != "/?signin=cancelled" {
		t.Errorf("Location = %q, want /?signin=cancelled whatever Telegram sent", got)
	}
	if r := e.record(t, "request done"); len(r["error"].(string)) > 300 {
		t.Errorf("the log line must keep an attacker's text short: %d characters", len(r["error"].(string)))
	}
}

func TestLogout(t *testing.T) {
	tests := []struct {
		name        string
		mods        []func(*http.Request)
		wantRevoked []string
	}{
		{"cookie", []func(*http.Request){withCookie(sessionCookie, ownerToken)}, []string{ownerToken}},
		{"bearer", []func(*http.Request){withHeader("Authorization", "Bearer app-token")}, []string{"app-token"}},
		{"a session that is already dead is revoked too", []func(*http.Request){withCookie(sessionCookie, "dead")}, []string{"dead"}},
		{"nothing to sign out", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAuthEnv(t)

			rec := e.do(http.MethodPost, "/api/v1/auth/logout", tt.mods...)

			if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Errorf("status = %d, body %q; want 204 and no body", rec.Code, rec.Body)
			}
			wantRemoved(t, rec, sessionCookie, "/")
			if !slices.Equal(e.sessions.revoked, tt.wantRevoked) {
				t.Errorf("revoked %q, want %q", e.sessions.revoked, tt.wantRevoked)
			}
			if len(e.sessions.authCalls) != 0 {
				t.Error("signing out must not need a valid session: a stale cookie has to be sign-out-able")
			}
		})
	}
}

func TestLogoutKeepsTheCookieWhenTheSessionCannotBeRevoked(t *testing.T) {
	e := newAuthEnv(t)
	e.sessions.revokeErr = apperr.New(apperr.CodeUnavailable, "fake", "sign-out is temporarily unavailable")

	rec := e.do(http.MethodPost, "/api/v1/auth/logout", withCookie(sessionCookie, ownerToken))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	wantNoCookie(t, rec, sessionCookie) // the session is still alive on the server: let the user try again
}

func TestLogoutIsPostOnly(t *testing.T) {
	e := newAuthEnv(t)

	// A link or an <img> on another site must not be able to sign the owner out.
	rec := e.do(http.MethodGet, "/api/v1/auth/logout", withCookie(sessionCookie, ownerToken))

	if rec.Code != http.StatusNotFound || len(e.sessions.revoked) != 0 {
		t.Errorf("GET logout: status %d, revoked %q; want 404 and nothing revoked", rec.Code, e.sessions.revoked)
	}
}

func TestSecretsNeverReachTheLog(t *testing.T) {
	e := newAuthEnv(t)

	e.do(http.MethodGet, "/api/v1/auth/me", withCookie(sessionCookie, ownerToken))
	e.do(http.MethodGet, "/api/v1/auth/me", withHeader("Authorization", "Bearer app-secret-token"))
	e.do(http.MethodGet, "/api/v1/latest", withCookie(sessionCookie, "stale-secret-token"))
	e.callback("code=secret-code&state=secret-state", withCookie(flowCookie, "secret-browser-state"))
	e.do(http.MethodPost, "/api/v1/auth/logout", withCookie(sessionCookie, "logout-secret-token"))

	for _, secret := range []string{ownerToken, "app-secret-token", "stale-secret-token", "secret-code", "secret-state", "secret-browser-state", "logout-secret-token", "new-token"} {
		if strings.Contains(e.logs.String(), secret) {
			t.Errorf("the log contains %q:\n%s", secret, e.logs)
		}
	}
}

func TestAccessLogTellsTheRole(t *testing.T) {
	e := newAuthEnv(t)
	e.reader.hasLatest = true

	e.get("/api/v1/latest")
	e.do(http.MethodGet, "/api/v1/latest", withCookie(sessionCookie, ownerToken))

	var roles []any
	for _, r := range e.records(t) {
		if r["message"] == "request done" {
			roles = append(roles, r["role"])
		}
	}
	if len(roles) != 2 || roles[0] != "demo" || roles[1] != "owner" {
		t.Errorf("roles in the access log = %v, want [demo owner]", roles)
	}
}

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"climate-processor/internal/access"
	"climate-processor/internal/auth"
	"climate-processor/internal/config"
	"climate-processor/internal/domain"
	"climate-processor/internal/service"
)

type fakeReader struct{}

func (fakeReader) Latest() (service.LatestReading, bool) {
	return service.LatestReading{ClimateData: domain.ClimateData{Timestamp: time.Now()}}, true
}
func (fakeReader) History(context.Context, time.Time, time.Time, time.Duration) ([]domain.Bucket, error) {
	return nil, nil
}
func (fakeReader) Summary(context.Context, time.Time, time.Time) (domain.Summary, error) {
	return domain.Summary{}, nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func call(h http.Handler, peer, xff string) int {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/latest", nil)
	req.RemoteAddr = peer
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestNewAPIServerLimitsPerRealClient(t *testing.T) {
	cfg := &config.Config{HTTPAddr: ":8080", TrustedProxies: []string{"10.0.0.0/8"}, APIRatePerMin: 60, APIRateBurst: 2}
	srv, err := newAPIServer(quiet(), cfg, fakeReader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if srv.Addr != ":8080" || srv.ReadHeaderTimeout == 0 {
		t.Errorf("server = %+v", srv)
	}
	// Two clients behind the same proxy: each has its own budget of 2.
	for i, want := range []int{200, 200, 429} {
		if got := call(srv.Handler, "10.0.0.5:1", "203.0.113.1"); got != want {
			t.Errorf("client A request %d: %d, want %d", i+1, got, want)
		}
	}
	if got := call(srv.Handler, "10.0.0.5:1", "203.0.113.2"); got != 200 {
		t.Errorf("client B: %d, want 200", got)
	}
}

func TestNewAPIServerRateZeroSwitchesTheLimitOff(t *testing.T) {
	cfg := &config.Config{HTTPAddr: ":8080", APIRatePerMin: 0, APIRateBurst: 1}
	srv, err := newAPIServer(quiet(), cfg, fakeReader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if got := call(srv.Handler, "192.0.2.1:1", ""); got != 200 {
			t.Fatalf("request %d: %d, want 200 (no limit)", i+1, got)
		}
	}
}

func TestNewAPIServerErrors(t *testing.T) {
	bad := &config.Config{HTTPAddr: ":8080", TrustedProxies: []string{"10.0.0.0/33"}, APIRatePerMin: 60, APIRateBurst: 1}
	if _, err := newAPIServer(quiet(), bad, fakeReader{}, nil); err == nil {
		t.Error("an invalid trusted proxy must fail the start")
	}
	noBurst := &config.Config{HTTPAddr: ":8080", APIRatePerMin: 60, APIRateBurst: 0}
	if _, err := newAPIServer(quiet(), noBurst, fakeReader{}, nil); err == nil {
		t.Error("a limiter without a burst must fail the start")
	}
}

func TestNewAPIServerServesTheWebAppNextToTheAPI(t *testing.T) {
	cfg := &config.Config{HTTPAddr: ":8080", APIRatePerMin: 60, APIRateBurst: 2}
	srv, err := newAPIServer(quiet(), cfg, fakeReader{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	page := apiRequest(srv.Handler, http.MethodGet, "/")
	api := apiRequest(srv.Handler, http.MethodGet, "/api/v1/latest")
	missing := apiRequest(srv.Handler, http.MethodGet, "/no-such-file.js")

	if page.Code != http.StatusOK || !strings.HasPrefix(page.Header().Get("Content-Type"), "text/html") {
		t.Errorf("/: status %d, type %q; want the page", page.Code, page.Header().Get("Content-Type"))
	}
	if csp := page.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("/: Content-Security-Policy = %q", csp)
	}
	if got := page.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("/: Cache-Control = %q, want no-cache (revalidate, keep the ETag)", got)
	}
	if api.Code != http.StatusOK || !strings.HasPrefix(api.Header().Get("Content-Type"), "application/json") {
		t.Errorf("/api/v1/latest: status %d, type %q; want JSON", api.Code, api.Header().Get("Content-Type"))
	}
	if got := api.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("/api/v1/latest: Cache-Control = %q, want no-store", got)
	}
	if missing.Code != http.StatusNotFound || !strings.HasPrefix(missing.Header().Get("Content-Type"), "application/json") {
		t.Errorf("a missing file: status %d, type %q; want the JSON 404", missing.Code, missing.Header().Get("Content-Type"))
	}
}

func TestNewAPIServerGivesTheFilesALimitApartFromTheAPI(t *testing.T) {
	log, out := capture()
	cfg := &config.Config{HTTPAddr: ":8080", APIRatePerMin: 60, APIRateBurst: 2, WebRatePerMin: 60, WebRateBurst: 3}
	srv, err := newAPIServer(log, cfg, fakeReader{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for i, want := range []int{http.StatusOK, http.StatusOK, http.StatusOK, http.StatusTooManyRequests} {
		if got := apiRequest(srv.Handler, http.MethodGet, "/app.css").Code; got != want {
			t.Errorf("file request %d: %d, want %d (a burst of 3)", i+1, got, want)
		}
	}

	// The API has its own burst of 2, which the files have not touched.
	for i, want := range []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests} {
		if got := call(srv.Handler, "192.0.2.1:1", ""); got != want {
			t.Errorf("API request %d: %d, want %d (a burst of 2)", i+1, got, want)
		}
	}
	if got := out.String(); !strings.Contains(got, "web_rate_per_minute=60") || !strings.Contains(got, "web_rate_burst=3") {
		t.Errorf("the settings log must show the limit of the files: %q", got)
	}
}

func TestNewAPIServerDoesNotLimitTheFilesWithoutAWebLimit(t *testing.T) {
	log, out := capture()
	cfg := &config.Config{HTTPAddr: ":8080", APIRatePerMin: 60, APIRateBurst: 1, WebRatePerMin: 0, WebRateBurst: 1}
	srv, err := newAPIServer(log, cfg, fakeReader{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 50; i++ {
		if got := apiRequest(srv.Handler, http.MethodGet, "/app.css").Code; got != http.StatusOK {
			t.Fatalf("file request %d: %d, want 200 (the files are not limited)", i+1, got)
		}
	}
	if got := out.String(); !strings.Contains(got, "web_rate_per_minute=0") {
		t.Errorf("the settings log must show that the files are not limited: %q", got)
	}
}

func TestNewAPIServerRateZeroSwitchesTheFilesLimitOffToo(t *testing.T) {
	cfg := &config.Config{HTTPAddr: ":8080", APIRatePerMin: 0, APIRateBurst: 1, WebRatePerMin: 60, WebRateBurst: 1}
	srv, err := newAPIServer(quiet(), cfg, fakeReader{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 50; i++ {
		if got := apiRequest(srv.Handler, http.MethodGet, "/app.css").Code; got != http.StatusOK {
			t.Fatalf("file request %d: %d, want 200: API_RATE_PER_MIN=0 switches every limit off", i+1, got)
		}
	}
}

func TestNewAPIServerRejectsAWebLimitWithoutABurst(t *testing.T) {
	cfg := &config.Config{HTTPAddr: ":8080", APIRatePerMin: 60, APIRateBurst: 1, WebRatePerMin: 60, WebRateBurst: 0}
	if _, err := newAPIServer(quiet(), cfg, fakeReader{}, nil); err == nil {
		t.Error("a limiter of the files without a burst must fail the start")
	}
}

// noSessions is a session store that keeps nothing: the tests below never get further than the redirect to Telegram.
type noSessions struct{}

func (noSessions) CreateSession(context.Context, domain.Session) error { return nil }
func (noSessions) Session(context.Context, string) (domain.Session, bool, error) {
	return domain.Session{}, false, nil
}
func (noSessions) TouchSession(context.Context, string, time.Time) error { return nil }
func (noSessions) DeleteSession(context.Context, string) error           { return nil }
func (noSessions) DeleteUserSessions(context.Context, int64) (int64, error) {
	return 0, nil
}
func (noSessions) DeleteExpiredSessions(context.Context, time.Time) (int64, error) {
	return 0, nil
}

const redirectURI = "https://climate.example.com/api/v1/auth/telegram/callback"

// signInConfig is a configuration with the owner sign-in fully set up.
func signInConfig() *config.Config {
	return &config.Config{
		HTTPAddr:            ":8080",
		PublicURL:           "https://climate.example.com",
		TGLoginClientID:     "123456",
		TGLoginClientSecret: "s3cret",
		SessionTTL:          24 * time.Hour,
		AdminChatIDs:        []int64{42},
	}
}

// capture returns a logger and what it has written so far.
func capture() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func newOwner(t *testing.T, cfg *config.Config) *ownerAuth {
	t.Helper()
	owners, err := access.NewStore("", cfg.AdminChatIDs)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := newOwnerAuth(context.Background(), quiet(), cfg, noSessions{}, owners)
	if err != nil || owner == nil {
		t.Fatalf("newOwnerAuth = %v, %v; want the sign-in", owner, err)
	}
	return owner
}

func TestNewOwnerAuthIsOffUntilFullyConfigured(t *testing.T) {
	tests := []struct {
		name      string
		tweak     func(*config.Config)
		noStore   bool
		wantLevel string // level of the log record
		wantText  string // what the record says
	}{
		{"nothing is configured", func(c *config.Config) { c.PublicURL, c.TGLoginClientID, c.TGLoginClientSecret = "", "", "" },
			false, "level=INFO", "owner sign-in is off"},
		{"no public address", func(c *config.Config) { c.PublicURL = "" }, false, "level=WARN", "PUBLIC_URL"},
		{"no client id", func(c *config.Config) { c.TGLoginClientID = "" }, false, "level=WARN", "TELEGRAM_OIDC_CLIENT_ID"},
		{"no client secret", func(c *config.Config) { c.TGLoginClientSecret = "" }, false, "level=WARN", "TELEGRAM_OIDC_CLIENT_SECRET"},
		{"no database for the sessions", func(*config.Config) {}, true, "level=WARN", "DB_PATH"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := signInConfig()
			tt.tweak(cfg)
			log, out := capture()
			owners, _ := access.NewStore("", cfg.AdminChatIDs)
			var store auth.SessionStore = noSessions{}
			if tt.noStore {
				store = nil
			}

			owner, err := newOwnerAuth(context.Background(), log, cfg, store, owners)

			if owner != nil || err != nil {
				t.Errorf("newOwnerAuth = %v, %v; want it off without an error", owner, err)
			}
			if got := out.String(); !strings.Contains(got, tt.wantLevel) || !strings.Contains(got, tt.wantText) {
				t.Errorf("log = %q; want a %s record that mentions %q", got, tt.wantLevel, tt.wantText)
			}
			if strings.Contains(out.String(), "s3cret") {
				t.Errorf("the client secret must never be logged: %q", out)
			}
		})
	}
}

func TestNewOwnerAuthSendsTelegramBackToThePublicAddress(t *testing.T) {
	cfg := signInConfig()
	log, out := capture()
	owners, _ := access.NewStore("", cfg.AdminChatIDs)

	owner, err := newOwnerAuth(context.Background(), log, cfg, noSessions{}, owners)
	if err != nil || owner == nil {
		t.Fatalf("newOwnerAuth = %v, %v", owner, err)
	}

	authURL, _, err := owner.login.Begin()
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	// This address must match, character for character, the Redirect URI registered in @BotFather.
	if got := u.Query().Get("redirect_uri"); got != redirectURI {
		t.Errorf("redirect_uri = %q, want %q", got, redirectURI)
	}
	if got := u.Query().Get("client_id"); got != "123456" {
		t.Errorf("client_id = %q, want 123456", got)
	}
	if got := out.String(); !strings.Contains(got, redirectURI) || strings.Contains(got, "s3cret") {
		t.Errorf("the log must show the redirect address and never the secret: %q", got)
	}
}

func TestNewOwnerAuthUsesTheConfiguredLifetimeAndOwners(t *testing.T) {
	cfg := signInConfig()
	cfg.SessionTTL = 36 * time.Hour
	owner := newOwner(t, cfg)

	_, session, err := owner.service.Create(context.Background(), 42, "browser")
	if err != nil {
		t.Fatalf("the admin of the bot must be able to sign in: %v", err)
	}
	if got := session.ExpiresAt.Sub(session.CreatedAt); got != 36*time.Hour {
		t.Errorf("session lives %s, want the configured 36h", got)
	}
	if _, _, err := owner.service.Create(context.Background(), 99, "browser"); err == nil {
		t.Error("an account that is not an admin of the bot must not be able to sign in")
	}
}

func TestNewOwnerAuthWarnsWhenNobodyCanSignIn(t *testing.T) {
	cfg := signInConfig()
	cfg.AdminChatIDs = nil
	log, out := capture()
	owners, _ := access.NewStore("", nil)

	owner, err := newOwnerAuth(context.Background(), log, cfg, noSessions{}, owners)

	if err != nil || owner == nil {
		t.Fatalf("newOwnerAuth = %v, %v; the sign-in is configured, so it must be on", owner, err)
	}
	if !strings.Contains(out.String(), "TELEGRAM_ADMIN_CHAT_IDS is empty") {
		t.Errorf("log = %q; want a warning about the empty owner list", out)
	}
}

func apiRequest(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestNewAPIServerServesTheSignInOverHTTPS(t *testing.T) {
	cfg := signInConfig()
	srv, err := newAPIServer(quiet(), cfg, fakeReader{}, newOwner(t, cfg))
	if err != nil {
		t.Fatal(err)
	}

	login := apiRequest(srv.Handler, http.MethodGet, "/api/v1/auth/telegram/login")

	if login.Code != http.StatusFound || !strings.HasPrefix(login.Header().Get("Location"), "https://oauth.telegram.org/") {
		t.Fatalf("login: status %d, Location %q; want a redirect to Telegram", login.Code, login.Header().Get("Location"))
	}
	if cookies := login.Result().Cookies(); len(cookies) != 1 || !cookies[0].Secure {
		t.Errorf("cookies = %+v; want one, Secure (the address is https)", cookies)
	}
	if me := apiRequest(srv.Handler, http.MethodGet, "/api/v1/auth/me"); !strings.Contains(me.Body.String(), `"sign_in_enabled":true`) {
		t.Errorf("me = %s; want sign_in_enabled", me.Body)
	}
}

func TestNewAPIServerCookiesAreNotSecureOnLocalHTTP(t *testing.T) {
	cfg := signInConfig()
	cfg.PublicURL = "http://localhost:8080" // the only plain-http address the configuration accepts
	srv, err := newAPIServer(quiet(), cfg, fakeReader{}, newOwner(t, cfg))
	if err != nil {
		t.Fatal(err)
	}

	login := apiRequest(srv.Handler, http.MethodGet, "/api/v1/auth/telegram/login")

	if cookies := login.Result().Cookies(); len(cookies) != 1 || cookies[0].Secure {
		t.Errorf("cookies = %+v; want one that is not Secure: a browser would drop it on http", cookies)
	}
}

// memorySessions keeps the sessions in a map, so that a test can sign the owner in.
type memorySessions struct {
	noSessions
	sessions map[string]domain.Session
}

func (m *memorySessions) CreateSession(_ context.Context, s domain.Session) error {
	m.sessions[s.ID] = s
	return nil
}

func (m *memorySessions) Session(_ context.Context, id string) (domain.Session, bool, error) {
	s, ok := m.sessions[id]
	return s, ok, nil
}

// limitedConfig has the sign-in and the rate limits: a burst of 2 for everybody, a burst of 20 for the owner.
func limitedConfig() *config.Config {
	cfg := signInConfig()
	cfg.APIRatePerMin, cfg.APIRateBurst = 60, 2
	cfg.APIOwnerRatePerMin, cfg.APIOwnerRateBurst = 600, 20
	return cfg
}

// signedInServer is the API server with the owner sign-in, and the session token of the owner (Telegram id 42).
func signedInServer(t *testing.T, cfg *config.Config, log *slog.Logger) (*http.Server, string) {
	t.Helper()
	owners, err := access.NewStore("", cfg.AdminChatIDs)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := newOwnerAuth(context.Background(), quiet(), cfg, &memorySessions{sessions: map[string]domain.Session{}}, owners)
	if err != nil || owner == nil {
		t.Fatalf("newOwnerAuth = %v, %v; want the sign-in", owner, err)
	}
	token, _, err := owner.service.Create(context.Background(), 42, "test")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := newAPIServer(log, cfg, fakeReader{}, owner)
	if err != nil {
		t.Fatal(err)
	}
	return srv, token
}

// callAs makes a request from one address, as the owner when a session token is given.
func callAs(h http.Handler, token string) int {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/latest", nil)
	req.RemoteAddr = "192.0.2.1:1"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestNewAPIServerGivesTheOwnerAHigherLimit(t *testing.T) {
	log, out := capture()
	srv, token := signedInServer(t, limitedConfig(), log)

	// Everybody has a burst of 2: this address has used it up.
	for i, want := range []int{200, 200, 429} {
		if got := callAs(srv.Handler, ""); got != want {
			t.Errorf("anonymous request %d: %d, want %d", i+1, got, want)
		}
	}
	// The owner has a burst of 20 of their own, from the very same address.
	for i := range 20 {
		if got := callAs(srv.Handler, token); got != 200 {
			t.Fatalf("owner request %d: %d, want 200", i+1, got)
		}
	}
	if got := callAs(srv.Handler, token); got != 429 {
		t.Errorf("owner request 21: %d, want 429: the owner is limited too", got)
	}
	if got := out.String(); !strings.Contains(got, "owner_rate_per_minute=600") || !strings.Contains(got, "owner_rate_burst=20") {
		t.Errorf("log = %q; want the owner limit in the settings", got)
	}
}

// 600 requests a minute are 10 a second: a spent budget is worth a request again after a fraction of a second, which
// the 60 a minute of everybody (1 a second) would not give.
func TestNewAPIServerOwnerLimitRefillsAtTheOwnerRate(t *testing.T) {
	srv, token := signedInServer(t, limitedConfig(), quiet())
	spent := false
	for range 100 {
		if callAs(srv.Handler, token) == http.StatusTooManyRequests {
			spent = true
			break
		}
	}
	if !spent {
		t.Fatal("the owner budget was never used up")
	}

	time.Sleep(300 * time.Millisecond) // 3 requests at 10 a second, a third of one at 1 a second

	if got := callAs(srv.Handler, token); got != http.StatusOK {
		t.Errorf("owner request after 300ms: %d, want 200: the budget refills at the owner rate", got)
	}
}

func TestNewAPIServerOwnerSharesTheLimitWhenTheOwnerLimitIsOff(t *testing.T) {
	cfg := limitedConfig()
	cfg.APIOwnerRatePerMin = 0
	log, out := capture()
	srv, token := signedInServer(t, cfg, log)

	for i, want := range []int{200, 200, 429} {
		if got := callAs(srv.Handler, token); got != want {
			t.Errorf("owner request %d: %d, want %d (the limit of everybody)", i+1, got, want)
		}
	}
	if !strings.Contains(out.String(), "owner_rate_per_minute=0") {
		t.Errorf("log = %q; want the owner limit reported as off", out)
	}
}

func TestNewAPIServerRateZeroSwitchesTheOwnerLimitOffToo(t *testing.T) {
	cfg := limitedConfig()
	cfg.APIRatePerMin = 0 // nobody is limited, whatever the owner limit says
	srv, token := signedInServer(t, cfg, quiet())

	for i := range 50 {
		if got := callAs(srv.Handler, token); got != 200 {
			t.Fatalf("owner request %d: %d, want 200 (no limit)", i+1, got)
		}
	}
}

func TestNewAPIServerOwnerLimitNeedsTheSignIn(t *testing.T) {
	cfg := &config.Config{HTTPAddr: ":8080", APIRatePerMin: 60, APIRateBurst: 2, APIOwnerRatePerMin: 600, APIOwnerRateBurst: 20}
	log, out := capture()
	srv, err := newAPIServer(log, cfg, fakeReader{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Without the sign-in a token means nothing: everybody has the limit of everybody.
	for i, want := range []int{200, 200, 429} {
		if got := callAs(srv.Handler, "some-token"); got != want {
			t.Errorf("request %d: %d, want %d", i+1, got, want)
		}
	}
	if !strings.Contains(out.String(), "owner_rate_per_minute=0") {
		t.Errorf("log = %q; want the owner limit reported as off", out)
	}
}

func TestNewAPIServerRejectsAnOwnerLimitWithoutABurst(t *testing.T) {
	cfg := limitedConfig()
	cfg.APIOwnerRateBurst = 0
	if _, err := newAPIServer(quiet(), cfg, fakeReader{}, newOwner(t, cfg)); err == nil {
		t.Error("an owner limit without a burst must fail the start")
	}
}

func TestNewAPIServerWithoutTheSignInHasNoSignInRoutes(t *testing.T) {
	srv, err := newAPIServer(quiet(), &config.Config{HTTPAddr: ":8080"}, fakeReader{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if login := apiRequest(srv.Handler, http.MethodGet, "/api/v1/auth/telegram/login"); login.Code != http.StatusNotFound {
		t.Errorf("login: status %d, want 404", login.Code)
	}
	if me := apiRequest(srv.Handler, http.MethodGet, "/api/v1/auth/me"); !strings.Contains(me.Body.String(), `"sign_in_enabled":false`) {
		t.Errorf("me = %s; want sign_in_enabled false", me.Body)
	}
}

func TestOpenRepository(t *testing.T) {
	t.Run("without a path there is no history and no sessions", func(t *testing.T) {
		readings, sessions, closeRepo, err := openRepository(context.Background(), quiet(), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := readings.(service.NopRepository); !ok || sessions != nil {
			t.Errorf("readings %T, sessions %v; want NopRepository and nil", readings, sessions)
		}
		if err := closeRepo(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	t.Run("with a path the database keeps both", func(t *testing.T) {
		readings, sessions, closeRepo, err := openRepository(context.Background(), quiet(), filepath.Join(t.TempDir(), "climate.db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, nop := readings.(service.NopRepository); nop || sessions == nil {
			t.Errorf("readings %T, sessions %v; want the database for both", readings, sessions)
		}
		if err := closeRepo(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	t.Run("a path that cannot be opened is an error", func(t *testing.T) {
		if _, _, _, err := openRepository(context.Background(), quiet(), "bad?path"); err == nil {
			t.Error("want an error")
		}
	})
}

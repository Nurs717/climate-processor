package rest

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"climate-processor/internal/web"
)

// fakeUI holds some paths and answers them with a page; it records what it was asked to serve.
type fakeUI struct {
	paths  map[string]bool
	served []string // "METHOD path"
}

func newFakeUI(paths ...string) *fakeUI {
	u := &fakeUI{paths: map[string]bool{}}
	for _, p := range paths {
		u.paths[p] = true
	}
	return u
}

func (u *fakeUI) Has(path string) bool { return u.paths[path] }

func (u *fakeUI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.served = append(u.served, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte("<h1>page</h1>"))
	}
}

func withUI(u UI) func(*Options) { return func(o *Options) { o.UI = u } }

func TestAppFilesAnswerTheRequestsNoRouteTakes(t *testing.T) {
	ui := newFakeUI("/", "/app.css")
	e := newTestEnv(t, withUI(ui))

	for _, path := range []string{"/", "/app.css"} {
		rec := e.get(path)

		if rec.Code != http.StatusOK || rec.Body.String() != "<h1>page</h1>" {
			t.Errorf("%s: status %d, body %q", path, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control = %q: the API's no-store must not leak onto the files", path, got)
		}
	}
	if len(ui.served) != 2 {
		t.Errorf("the app served %v, want the two requests", ui.served)
	}
}

func TestAppFilesAreLoggedLikeAnyRequest(t *testing.T) {
	e := newTestEnv(t, withUI(newFakeUI("/")))

	e.get("/")

	rec := e.record(t, "request done")
	if rec["path"] != "/" || rec["status"] != float64(http.StatusOK) || rec["method"] != "GET" {
		t.Errorf("record = %v", rec)
	}
}

func TestAppFilesHaveABudgetOfTheirOwn(t *testing.T) {
	demo := &fakeLimiter{ok: true}
	e := newTestEnv(t, withUI(newFakeUI("/", "/app.js")), withLimiter(demo))
	e.reader.hasLatest = true

	e.get("/")
	e.get("/app.js")
	e.get("/api/v1/latest")

	wantCharges(t, "the demo limit", demo.calls, "static:192.0.2.1/1", "static:192.0.2.1/1", "192.0.2.1/1")
}

func TestRefusedFileIsAJSON429AndTheAppIsNotAsked(t *testing.T) {
	demo := &refusingLimiter{refuse: map[string]time.Duration{"static:192.0.2.1": 3 * time.Second}}
	ui := newFakeUI("/")
	e := newTestEnv(t, withUI(ui), withLimiter(demo))

	rec := e.get("/")

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "3" {
		t.Errorf("Retry-After = %q, want 3", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", got)
	}
	if len(ui.served) != 0 {
		t.Errorf("a refused request reached the app: %v", ui.served)
	}
	wantCharges(t, "the demo limit", demo.calls, "static:192.0.2.1/1") // and not once more for the 404 it is not
}

func TestUnknownPathIsAJSON404ChargedAsACheapRequest(t *testing.T) {
	demo := &fakeLimiter{ok: true}
	ui := newFakeUI("/")
	e := newTestEnv(t, withUI(ui), withLimiter(demo))

	rec := e.get("/nowhere.js")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := decode[errorBody](t, rec).Error.Code; got != "not_found" {
		t.Errorf("error code = %q, want not_found", got)
	}
	if len(ui.served) != 0 {
		t.Errorf("the app served %v for a path it does not hold", ui.served)
	}
	wantCharges(t, "the demo limit", demo.calls, "192.0.2.1/1")
}

func TestOnlyGETAndHEADReachTheApp(t *testing.T) {
	ui := newFakeUI("/", "/app.css")
	e := newTestEnv(t, withUI(ui))

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := e.do(method, "/")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s /: status = %d, want 404 of the API", method, rec.Code)
		}
	}
	if len(ui.served) != 0 {
		t.Errorf("the app served %v", ui.served)
	}

	head := e.do(http.MethodHead, "/app.css")

	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Errorf("HEAD: status %d, body %q", head.Code, head.Body)
	}
	if len(ui.served) != 1 || ui.served[0] != "HEAD /app.css" {
		t.Errorf("the app served %v, want HEAD /app.css", ui.served)
	}
}

func TestAPIPathsStayJSONWhateverTheAppHolds(t *testing.T) {
	ui := newFakeUI("/api/v1/nowhere", "/api/v1/latest/", "/api/")
	e := newTestEnv(t, withUI(ui))

	for _, path := range []string{"/api/v1/nowhere", "/api/", "/api/other"} {
		rec := e.get(path)

		if rec.Code != http.StatusNotFound || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s: status %d, type %q; want a JSON 404", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	if len(ui.served) != 0 {
		t.Errorf("the app served %v under /api/", ui.served)
	}
}

func TestAPIRoutesWinOverTheApp(t *testing.T) {
	ui := newFakeUI("/", "/api/v1/latest")
	e := newTestEnv(t, withUI(ui))
	e.reader.hasLatest = true

	rec := e.get("/api/v1/latest")

	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("status %d, type %q; want the API answer", rec.Code, rec.Header().Get("Content-Type"))
	}
	if len(ui.served) != 0 {
		t.Errorf("the app served %v for an API route", ui.served)
	}
}

// Opening the page must not use up what the dashboard needs to load its data, nor the other way round.
func TestFilesAndAPIDoNotShareABucket(t *testing.T) {
	t.Run("a busy page leaves the API alone", func(t *testing.T) {
		e := newTestEnv(t, withUI(newFakeUI("/")), withLimiter(mustLimiter(t, 60, 3)))
		e.reader.hasLatest = true

		for range 3 {
			if rec := e.get("/"); rec.Code != http.StatusOK {
				t.Fatalf("/: status = %d", rec.Code)
			}
		}
		if rec := e.get("/"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("a 4th file within the burst: status = %d, want 429", rec.Code)
		}

		if rec := e.get("/api/v1/latest"); rec.Code != http.StatusOK {
			t.Errorf("the API after a flood of files: status = %d, want 200", rec.Code)
		}
	})
	t.Run("a busy API leaves the page alone", func(t *testing.T) {
		e := newTestEnv(t, withUI(newFakeUI("/")), withLimiter(mustLimiter(t, 60, 3)))
		e.reader.hasLatest = true

		for range 3 {
			e.get("/api/v1/latest")
		}
		if rec := e.get("/api/v1/latest"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("a 4th API call within the burst: status = %d, want 429", rec.Code)
		}

		if rec := e.get("/"); rec.Code != http.StatusOK {
			t.Errorf("the page after a flood of API calls: status = %d, want 200", rec.Code)
		}
	})
	t.Run("another address has its own", func(t *testing.T) {
		e := newTestEnv(t, withUI(newFakeUI("/")), withLimiter(mustLimiter(t, 60, 1)))

		e.get("/")

		if rec := e.getFrom("192.0.2.9:1234", "/"); rec.Code != http.StatusOK {
			t.Errorf("another address: status = %d, want 200", rec.Code)
		}
	})
}

// Files are public: a session token neither buys a cheaper price for them nor reaches the session store.
func TestFilesIgnoreTheSession(t *testing.T) {
	demo, owner := &fakeLimiter{ok: true}, &fakeLimiter{ok: true}
	e := newAuthEnv(t, withUI(newFakeUI("/", "/app.css")), withLimiter(demo), withOwnerLimiter(owner))

	e.do(http.MethodGet, "/", ownerCookie())
	e.do(http.MethodGet, "/app.css", withCookie(sessionCookie, "junk"))

	wantCharges(t, "the demo limit", demo.calls, "static:192.0.2.1/1", "static:192.0.2.1/1")
	wantCharges(t, "the owner limit", owner.calls)
	if len(e.sessions.authCalls) != 0 {
		t.Errorf("a file request reached the session store: %v", e.sessions.authCalls)
	}
}

func TestWithoutALimitTheAppIsServed(t *testing.T) {
	e := newTestEnv(t, withUI(newFakeUI("/")))

	for range 50 {
		if rec := e.get("/"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}
}

func TestWithoutAnAppEveryUnknownPathIsAJSON404(t *testing.T) {
	demo := &fakeLimiter{ok: true}
	e := newTestEnv(t, withLimiter(demo))

	rec := e.get("/")

	if rec.Code != http.StatusNotFound || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("status %d, type %q; want the JSON 404 of before the app", rec.Code, rec.Header().Get("Content-Type"))
	}
	wantCharges(t, "the demo limit", demo.calls, "192.0.2.1/1")
}

// A page, a 304 and the answer to HEAD are all chosen by the real app. Gin starts the answer to an unknown route as a
// 404, so each of them has to reach the client with the status the app gave it.
func TestRealAppKeepsItsStatusesThroughTheRouter(t *testing.T) {
	app, err := web.New()
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, withUI(app))

	page := e.get("/")
	etag := page.Header().Get("ETag")

	if page.Code != http.StatusOK || page.Body.Len() == 0 || etag == "" {
		t.Fatalf("GET /: status %d, %d bytes, ETag %q; want the page", page.Code, page.Body.Len(), etag)
	}
	if again := e.get("/", "If-None-Match", etag); again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Errorf("GET / with its ETag: status %d, %d bytes; want an empty 304", again.Code, again.Body.Len())
	}
	head := e.do(http.MethodHead, "/")
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD /: status %d, %d bytes, Content-Length %q; want 200 and no body",
			head.Code, head.Body.Len(), head.Header().Get("Content-Length"))
	}
}

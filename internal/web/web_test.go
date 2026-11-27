package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func testApp(t *testing.T) *App {
	t.Helper()
	app, err := newApp(fstest.MapFS{
		"index.html":           {Data: []byte("<h1>index</h1>")},
		"app.css":              {Data: []byte("body{}")},
		"app.js":               {Data: []byte("console.log(1)")},
		"manifest.webmanifest": {Data: []byte("{}")},
		"icons/icon.png":       {Data: []byte("\x89PNG")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func do(app *App, method, target string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}

func TestAppServesItsFilesWithTheirTypes(t *testing.T) {
	app := testApp(t)
	tests := []struct {
		path, contentType, body string
	}{
		{"/", "text/html; charset=utf-8", "<h1>index</h1>"},
		{"/index.html", "text/html; charset=utf-8", "<h1>index</h1>"},
		{"/app.css", "text/css; charset=utf-8", "body{}"},
		{"/app.js", "text/javascript; charset=utf-8", "console.log(1)"},
		{"/manifest.webmanifest", "application/manifest+json", "{}"},
		{"/icons/icon.png", "image/png", "\x89PNG"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := do(app, http.MethodGet, tt.path)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != tt.contentType {
				t.Errorf("Content-Type = %q, want %q", got, tt.contentType)
			}
			if got := rec.Body.String(); got != tt.body {
				t.Errorf("body = %q, want %q", got, tt.body)
			}
		})
	}
}

func TestAppSendsTheSecurityHeadersWithEveryFile(t *testing.T) {
	app := testApp(t)
	want := map[string]string{
		"Content-Security-Policy":    policy,
		"X-Content-Type-Options":     "nosniff",
		"X-Frame-Options":            "DENY",
		"Referrer-Policy":            "same-origin",
		"Permissions-Policy":         "camera=(), microphone=(), geolocation=()",
		"Cross-Origin-Opener-Policy": "same-origin",
		"Cache-Control":              "no-cache",
	}
	for _, path := range []string{"/", "/app.css", "/app.js", "/manifest.webmanifest", "/icons/icon.png"} {
		rec := do(app, http.MethodGet, path)
		for name, value := range want {
			if got := rec.Header().Get(name); got != value {
				t.Errorf("%s: %s = %q, want %q", path, name, got, value)
			}
		}
	}
}

func TestPolicyAllowsOnlyTheOwnFiles(t *testing.T) {
	for _, directive := range []string{
		"default-src 'none'", "script-src 'self'", "style-src 'self'", "connect-src 'self'",
		"worker-src 'self'", "manifest-src 'self'", "frame-ancestors 'none'", "base-uri 'none'",
	} {
		if !strings.Contains(policy, directive) {
			t.Errorf("the policy lacks %q: %s", directive, policy)
		}
	}
	for _, loose := range []string{"unsafe-inline", "unsafe-eval", "*", "data:", "http:"} {
		if strings.Contains(policy, loose) {
			t.Errorf("the policy must not contain %q: %s", loose, policy)
		}
	}
}

func TestAppAnswersNotModifiedWhenTheBrowserHasTheFile(t *testing.T) {
	app := testApp(t)
	first := do(app, http.MethodGet, "/app.css")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	again := do(app, http.MethodGet, "/app.css", "If-None-Match", etag)

	if again.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", again.Code)
	}
	if again.Body.Len() != 0 {
		t.Errorf("a 304 has no body, got %q", again.Body)
	}
	if got := again.Header().Get("ETag"); got != etag {
		t.Errorf("ETag of the 304 = %q, want %q", got, etag)
	}
	other := do(app, http.MethodGet, "/app.css", "If-None-Match", `"something-else"`)
	if other.Code != http.StatusOK {
		t.Errorf("another ETag: status = %d, want 200 with the file", other.Code)
	}
}

func TestETagFollowsTheContentOfTheFile(t *testing.T) {
	app := testApp(t)
	same, err := newApp(fstest.MapFS{
		"index.html": {Data: []byte("<h1>index</h1>")},
		"app.css":    {Data: []byte("body{}")},
	})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := newApp(fstest.MapFS{
		"index.html": {Data: []byte("<h1>index</h1>")},
		"app.css":    {Data: []byte("body{margin:0}")},
	})
	if err != nil {
		t.Fatal(err)
	}

	etag := func(a *App) string { return do(a, http.MethodGet, "/app.css").Header().Get("ETag") }

	if etag(app) != etag(same) {
		t.Error("the same content must give the same ETag, also after a restart")
	}
	if etag(app) == etag(changed) {
		t.Error("another content must give another ETag, or browsers keep the old file")
	}
	if do(app, http.MethodGet, "/app.js").Header().Get("ETag") == etag(app) {
		t.Error("two different files share an ETag")
	}
}

func TestAppAnswersHEADWithoutABody(t *testing.T) {
	app := testApp(t)

	rec := do(app, http.MethodHead, "/app.js")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD must not have a body, got %q", rec.Body)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len("console.log(1)")) {
		t.Errorf("Content-Length = %q, want the size of the file", got)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("HEAD must carry the same headers as GET")
	}
}

func TestAppOnlyReadsFiles(t *testing.T) {
	app := testApp(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := do(app, method, "/app.css")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", method, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s: Allow = %q", method, got)
		}
	}
}

func TestHasIsAnExactMatch(t *testing.T) {
	app := testApp(t)
	for _, path := range []string{"/", "/index.html", "/app.css", "/icons/icon.png"} {
		if !app.Has(path) {
			t.Errorf("Has(%q) = false", path)
		}
	}
	for _, path := range []string{
		"", "index.html", "/static/index.html", "/app.css/", "//app.css", "/./app.css", "/icons/../app.css",
		"/../app.css", "/APP.CSS", "/icons", "/icons/", "/missing.js", "/api/v1/latest",
	} {
		if app.Has(path) {
			t.Errorf("Has(%q) = true: only the exact path of a file is a file", path)
		}
	}
}

func TestAppRefusesAPathItDoesNotHold(t *testing.T) {
	rec := do(testApp(t), http.MethodGet, "/missing.js")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestNewAppFailsOnWhatItCannotServe(t *testing.T) {
	tests := []struct {
		name string
		fsys fstest.MapFS
		want string
	}{
		{"unknown file type", fstest.MapFS{"index.html": {Data: []byte("x")}, "tool.exe": {Data: []byte("x")}}, "tool.exe"},
		{"no index", fstest.MapFS{"app.css": {Data: []byte("x")}}, "index.html"},
		{"index only in a directory", fstest.MapFS{"sub/index.html": {Data: []byte("x")}}, "index.html"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newApp(tt.fsys)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want one that mentions %q", err, tt.want)
			}
		})
	}
}

func TestContentTypeOfAnExtensionIgnoresItsCase(t *testing.T) {
	app, err := newApp(fstest.MapFS{"index.html": {Data: []byte("x")}, "Logo.PNG": {Data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	if got := do(app, http.MethodGet, "/Logo.PNG").Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
}

// The embedded app is the one that ships: it has to start, and it has to work under its own policy.
func TestEmbeddedApp(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	index := do(app, http.MethodGet, "/")
	if index.Code != http.StatusOK || !strings.Contains(index.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("/: status %d, type %q", index.Code, index.Header().Get("Content-Type"))
	}

	t.Run("every file it refers to is there", func(t *testing.T) {
		ref := regexp.MustCompile(`(?:href|src)="(/[^"]*)"`)
		for path, f := range app.files {
			if !strings.Contains(f.contentType, "html") {
				continue
			}
			for _, m := range ref.FindAllStringSubmatch(string(f.body), -1) {
				target := m[1]
				if strings.HasPrefix(target, "/api/") {
					continue // the API, not a file
				}
				if !app.Has(target) {
					t.Errorf("%s refers to %s, which the app does not hold", path, target)
				}
			}
		}
	})

	t.Run("pages need nothing its policy forbids", func(t *testing.T) {
		forbidden := []struct{ name, pattern string }{
			{"an inline script", `<script(?:\s[^>]*)?>`},
			{"an inline style", `<style`},
			{"a style attribute", `\sstyle\s*=`},
			{"an event handler", `\son[a-z]+\s*=`},
			{"a javascript: link", `javascript:`},
		}
		for path, f := range app.files {
			if !strings.Contains(f.contentType, "html") {
				continue
			}
			for _, bad := range forbidden {
				re := regexp.MustCompile(`(?i)` + bad.pattern)
				for _, m := range re.FindAllString(string(f.body), -1) {
					// A script with a src is a file of the app, not an inline one.
					if bad.name == "an inline script" && strings.Contains(m, "src=") {
						continue
					}
					t.Errorf("%s has %s (%q), which its policy blocks", path, bad.name, m)
				}
			}
		}
	})

	t.Run("its files are not empty", func(t *testing.T) {
		for path, f := range app.files {
			if len(bytes.TrimSpace(f.body)) == 0 {
				t.Errorf("%s is empty", path)
			}
		}
	})

	t.Run("body of a response is the file", func(t *testing.T) {
		body, err := io.ReadAll(index.Result().Body)
		if err != nil || !strings.Contains(string(body), "<title>") {
			t.Errorf("index body = %q, %v", body, err)
		}
	})
}

package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"regexp"
	"slices"
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

	// A browser finds the modules of a script by their URL, and only when the script runs. A typo in one name
	// would otherwise show up as a blank page, not as a failing build.
	t.Run("every module it imports is there", func(t *testing.T) {
		from := regexp.MustCompile(`(?m)^\s*(?:import|export)\s[^;'"]*?\bfrom\s*['"]([^'"]+)['"]`)
		bare := regexp.MustCompile(`(?m)^\s*import\s*['"]([^'"]+)['"]`)
		found := 0
		for name, f := range app.files {
			if !strings.Contains(f.contentType, "javascript") {
				continue
			}
			var targets []string
			for _, re := range []*regexp.Regexp{from, bare} {
				for _, m := range re.FindAllStringSubmatch(string(f.body), -1) {
					targets = append(targets, m[1])
				}
			}
			for _, target := range targets {
				found++
				// There is no import map or bundler: only a path to a file will do ("lodash" would not).
				if !strings.HasPrefix(target, "./") && !strings.HasPrefix(target, "../") && !strings.HasPrefix(target, "/") {
					t.Errorf("%s imports %q, which a browser cannot find (it is not a path)", name, target)
					continue
				}
				resolved := target
				if !strings.HasPrefix(target, "/") {
					resolved = path.Join(path.Dir(name), target)
				}
				if !app.Has(resolved) {
					t.Errorf("%s imports %s, which the app does not hold", name, resolved)
				}
			}
		}
		if found == 0 {
			t.Fatal("found no imports at all: the pattern no longer matches how the scripts are written")
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

// What the worker is: its file is a template, and the server puts the shell and the build in front of it.

const workerTemplate = "self.addEventListener('fetch', () => {});\n"

// workerApp is an app with a worker. change edits its files before it starts.
func workerApp(t *testing.T, change func(fstest.MapFS)) *App {
	t.Helper()
	files := fstest.MapFS{
		"index.html":     {Data: []byte("<h1>index</h1>")},
		"app.css":        {Data: []byte("body{}")},
		"icons/icon.png": {Data: []byte("\x89PNG")},
		"sw.js":          {Data: []byte(workerTemplate)},
	}
	if change != nil {
		change(files)
	}
	app, err := newApp(files)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

var workerHead = regexp.MustCompile(`^const SHELL_VERSION = "([0-9a-f]{16})";\nconst SHELL_FILES = (\[.*\]);\n`)

// shellOf reads what the server put in front of the worker: the build, and the paths of the files it keeps.
func shellOf(t *testing.T, app *App) (version string, files []string) {
	t.Helper()
	body := do(app, http.MethodGet, "/sw.js").Body.String()
	m := workerHead.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the worker does not start with its constants:\n%.200s", body)
	}
	if err := json.Unmarshal([]byte(m[2]), &files); err != nil {
		t.Fatalf("SHELL_FILES is not a list of paths: %v", err)
	}
	return m[1], files
}

func TestWorkerIsTheTemplateWithTheShellAndTheBuildInFront(t *testing.T) {
	app := workerApp(t, nil)

	rec := do(app, http.MethodGet, "/sw.js")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Errorf("Content-Type = %q: a browser refuses a worker of another type", got)
	}
	if !strings.HasSuffix(rec.Body.String(), "\n"+workerTemplate) {
		t.Errorf("the template is not served after the constants:\n%s", rec.Body)
	}
	_, files := shellOf(t, app)
	want := []string{"/", "/app.css", "/icons/icon.png", "/index.html"}
	if !slices.Equal(files, want) {
		t.Errorf("SHELL_FILES = %q, want every file of the app but the worker: %q", files, want)
	}
}

func TestWorkerIsSentLikeEveryFileAndAlwaysChecked(t *testing.T) {
	rec := do(workerApp(t, nil), http.MethodGet, "/sw.js")

	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q: a worker that is kept without asking is a build that never ends", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != policy {
		t.Errorf("Content-Security-Policy = %q, want the policy of the app", got)
	}
}

func TestBuildFollowsEverythingTheAppIs(t *testing.T) {
	version := func(change func(fstest.MapFS)) string {
		v, _ := shellOf(t, workerApp(t, change))
		return v
	}
	base := version(nil)
	tests := []struct {
		name   string
		change func(fstest.MapFS)
		same   bool
	}{
		{"nothing changed", func(fstest.MapFS) {}, true},
		{"a file changed", func(f fstest.MapFS) { f["app.css"] = &fstest.MapFile{Data: []byte("body{margin:0}")} }, false},
		{"the worker changed", func(f fstest.MapFS) { f["sw.js"] = &fstest.MapFile{Data: []byte(workerTemplate + "//")} }, false},
		{"a file was added", func(f fstest.MapFS) { f["app.js"] = &fstest.MapFile{Data: []byte("1")} }, false},
		{"a file was removed", func(f fstest.MapFS) { delete(f, "icons/icon.png") }, false},
		{"a file was renamed", func(f fstest.MapFS) {
			f["icons/other.png"] = f["icons/icon.png"]
			delete(f, "icons/icon.png")
		}, false},
		{"text moved from one file to the next", func(f fstest.MapFS) {
			f["index.html"] = &fstest.MapFile{Data: []byte("<h1>index</h1>body{}")}
			f["app.css"] = &fstest.MapFile{Data: nil}
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := version(tt.change); (got == base) != tt.same {
				t.Errorf("build %s, was %s: same = %v, want %v", got, base, got == base, tt.same)
			}
		})
	}
}

func TestBuildFollowsTheHeadersOfTheFiles(t *testing.T) {
	files := workerApp(t, nil).files
	build := shellVersion(files, "a")

	if build == shellVersion(files, "b") {
		t.Error("another policy gave the same build: an installed app would keep the files with the old one")
	}
	if again := shellVersion(files, "a"); build != again {
		t.Errorf("the same files and headers gave two builds: %s and %s", build, again)
	}
}

func TestBuildTellsWhereOneFileEndsAndTheNextBegins(t *testing.T) {
	one := map[string]file{"/a": {body: []byte("bc")}}
	two := map[string]file{"/ab": {body: []byte("c")}}

	if shellVersion(one, "") == shellVersion(two, "") {
		t.Error(`"/a" with "bc" and "/ab" with "c" gave the same build`)
	}
}

func TestAnotherBuildIsAnotherWorkerToTheBrowser(t *testing.T) {
	one := workerApp(t, nil)
	two := workerApp(t, func(f fstest.MapFS) { f["app.css"] = &fstest.MapFile{Data: []byte("body{margin:0}")} })

	etag := func(a *App) string { return do(a, http.MethodGet, "/sw.js").Header().Get("ETag") }

	if etag(one) == "" || etag(one) == etag(two) {
		t.Errorf("ETags %q and %q: the browser would keep the worker of the old build", etag(one), etag(two))
	}
	if etag(one) != etag(workerApp(t, nil)) {
		t.Error("the same build must give the same worker, also after a restart")
	}
	if got := do(one, http.MethodGet, "/sw.js", "If-None-Match", etag(one)).Code; got != http.StatusNotModified {
		t.Errorf("status = %d, want 304 for a browser that has the worker", got)
	}
}

func TestAppWithoutAWorkerHasNone(t *testing.T) {
	app := testApp(t)

	if app.Has("/sw.js") {
		t.Error("Has(/sw.js) = true for an app that has no worker")
	}
	if got := do(app, http.MethodGet, "/sw.js").Code; got != http.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}

// What a browser needs from the embedded app to offer it for installing, and what the files have to say about each
// other. The icons are checked by what they hold: a size that is not the declared one, or a hole in an icon that an
// operating system fills with black, is found by looking at them and not by the person who installs the app.
func TestEmbeddedAppCanBeInstalled(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	read := func(urlPath string) []byte {
		t.Helper()
		f, ok := app.files[urlPath]
		if !ok {
			t.Fatalf("the app does not hold %s", urlPath)
		}
		return f.body
	}
	// icon checks that the PNG at urlPath is size by size, and (opaque) has no transparent corner.
	icon := func(t *testing.T, urlPath string, size int, opaque bool) {
		t.Helper()
		img, err := png.Decode(bytes.NewReader(read(urlPath)))
		if err != nil {
			t.Fatalf("%s is not a PNG: %v", urlPath, err)
		}
		b := img.Bounds()
		if b.Dx() != size || b.Dy() != size {
			t.Errorf("%s is %dx%d, want %dx%d", urlPath, b.Dx(), b.Dy(), size, size)
		}
		if !opaque {
			return
		}
		for _, p := range [][2]int{{b.Min.X, b.Min.Y}, {b.Max.X - 1, b.Min.Y}, {b.Min.X, b.Max.Y - 1}, {b.Max.X - 1, b.Max.Y - 1}} {
			if _, _, _, a := img.At(p[0], p[1]).RGBA(); a != 0xffff {
				t.Errorf("%s is transparent at its corner %v: it is cut into a shape by the system, which fills a hole with black", urlPath, p)
			}
		}
	}

	t.Run("the manifest", func(t *testing.T) {
		var m struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			ShortName string `json:"short_name"`
			StartURL  string `json:"start_url"`
			Scope     string `json:"scope"`
			Display   string `json:"display"`
			Icons     []struct {
				Src     string `json:"src"`
				Sizes   string `json:"sizes"`
				Type    string `json:"type"`
				Purpose string `json:"purpose"`
			} `json:"icons"`
		}
		if err := json.Unmarshal(read("/manifest.webmanifest"), &m); err != nil {
			t.Fatalf("the manifest is not JSON: %v", err)
		}
		if m.Name == "" || m.ShortName == "" || m.ID == "" {
			t.Errorf("name %q, short_name %q, id %q: all three are needed", m.Name, m.ShortName, m.ID)
		}
		if m.Display != "standalone" {
			t.Errorf("display = %q: the app is meant to open without the address bar", m.Display)
		}
		start, err := url.Parse(m.StartURL)
		if err != nil || !app.Has(start.Path) || !strings.HasPrefix(start.Path, m.Scope) {
			t.Errorf("start_url %q is not a file of the app inside the scope %q", m.StartURL, m.Scope)
		}
		if m.Scope != "/" {
			t.Errorf("scope = %q: the worker is at /sw.js and the app is the whole site", m.Scope)
		}

		have := map[string]bool{}
		for _, ic := range m.Icons {
			if f, ok := app.files[ic.Src]; !ok || f.contentType != ic.Type {
				t.Errorf("icon %s: the app holds it as %q, the manifest says %q", ic.Src, f.contentType, ic.Type)
				continue
			}
			var w, h int
			if _, err := fmt.Sscanf(ic.Sizes, "%dx%d", &w, &h); err != nil || w != h {
				t.Errorf("icon %s: sizes %q is not a square", ic.Src, ic.Sizes)
				continue
			}
			icon(t, ic.Src, w, ic.Purpose == "maskable")
			have[ic.Purpose+" "+strconv.Itoa(w)] = true
		}
		// What a browser asks for before it offers the app for installing, and what Android wants for a round icon.
		for _, want := range []string{"any 192", "any 512", "maskable 512"} {
			if !have[want] {
				t.Errorf("the manifest has no %q icon (it has %v)", want, have)
			}
		}
	})

	t.Run("the icon of the home screen of an iPhone", func(t *testing.T) {
		m := regexp.MustCompile(`<link rel="apple-touch-icon" href="([^"]+)">`).FindSubmatch(read("/index.html"))
		if m == nil {
			t.Fatal("index.html has no apple-touch-icon: an iPhone would put a screenshot of the page there")
		}
		icon(t, string(m[1]), 180, true)
	})

	t.Run("the worker", func(t *testing.T) {
		version, files := shellOf(t, app)
		if version == "" || len(files) < 10 {
			t.Fatalf("build %q, shell %q: too little", version, files)
		}
		for _, want := range []string{"/", "/index.html", "/app.js", "/app.css", "/manifest.webmanifest"} {
			if !slices.Contains(files, want) {
				t.Errorf("%s is not in the shell: the app would not open offline", want)
			}
		}
		for _, p := range files {
			if !app.Has(p) || strings.HasPrefix(p, "/api/") || p == workerPath {
				t.Errorf("%s must not be in the shell", p)
			}
		}
		// The template and the server have to agree on the names, or the worker fails when it starts.
		template, err := fs.ReadFile(embedded, "static/sw.js")
		if err != nil {
			t.Fatal(err)
		}
		if register, err := fs.ReadFile(embedded, "static/pwa.js"); err != nil || !bytes.Contains(register, []byte(".register('"+workerPath+"')")) {
			t.Errorf("pwa.js does not register %s, the worker the server makes (%v)", workerPath, err)
		}
		for _, name := range []string{"SHELL_VERSION", "SHELL_FILES"} {
			if !bytes.Contains(template, []byte(name)) {
				t.Errorf("sw.js does not use %s, which the server puts in front of it", name)
			}
			if regexp.MustCompile(`(?m)^\s*(?:const|let|var)\s+` + name + `\b`).Match(template) {
				t.Errorf("sw.js declares %s: the server does, and a second declaration stops the worker", name)
			}
		}
	})
}

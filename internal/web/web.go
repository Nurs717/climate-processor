// Package web serves the web app (a PWA). Its files are embedded into the binary and answered from memory.
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

//go:embed static
var embedded embed.FS

// contentTypes are the types of the files the app may hold. A file of another kind fails the start: a wrong type is
// a bug to meet at once and not in a browser, which would guess (and the responses forbid guessing).
var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".json":        "application/json",
	".webmanifest": "application/manifest+json",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".ico":         "image/x-icon",
}

// policy is the Content-Security-Policy of every file of the app: only its own files, no inline scripts or styles,
// no framing. The app talks to nobody but its own server.
const policy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; " +
	"manifest-src 'self'; worker-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// workerPath is the service worker. Its file in static/ is a template: see addShellToWorker.
const workerPath = "/sw.js"

type file struct {
	body        []byte
	contentType string
	etag        string
}

func newFile(contentType string, body []byte) file {
	sum := sha256.Sum256(body)
	return file{body: body, contentType: contentType, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
}

// App serves the files of the web app.
type App struct {
	files map[string]file // by URL path
}

// New serves the files embedded into the binary (the static directory).
func New() (*App, error) {
	root, err := fs.Sub(embedded, "static")
	if err != nil {
		return nil, fmt.Errorf("web: %w", err)
	}
	return newApp(root)
}

// newApp reads every file of root. Its index.html answers both / and /index.html.
func newApp(root fs.FS) (*App, error) {
	app := &App{files: make(map[string]file)}
	err := fs.WalkDir(root, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		contentType, ok := contentTypes[strings.ToLower(path.Ext(name))]
		if !ok {
			return fmt.Errorf("no content type for %q", name)
		}
		body, err := fs.ReadFile(root, name)
		if err != nil {
			return err
		}
		f := newFile(contentType, body)
		app.files["/"+name] = f
		if name == "index.html" {
			app.files["/"] = f
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("web: %w", err)
	}
	if _, ok := app.files["/"]; !ok {
		return nil, errors.New("web: index.html is missing")
	}
	if err := app.addShellToWorker(); err != nil {
		return nil, fmt.Errorf("web: %w", err)
	}
	return app, nil
}

// addShellToWorker turns the template static/sw.js into the worker that is served: two constants are put in front of
// it, which it cannot know itself. SHELL_FILES are the paths of the files it keeps on the device (all of the app but
// itself), and SHELL_VERSION is the build they belong to. A browser looks at the bytes of the worker to see whether
// there is a new one, so any change to any file of the app gives it a new worker, which makes a new cache and then
// removes the old one. An app without a worker is served as it is: it only does not open offline.
func (a *App) addShellToWorker() error {
	worker, ok := a.files[workerPath]
	if !ok {
		return nil
	}
	paths := slices.Sorted(maps.Keys(a.files))
	paths = slices.DeleteFunc(paths, func(p string) bool { return p == workerPath })
	list, err := json.Marshal(paths)
	if err != nil {
		return err
	}
	head := fmt.Appendf(nil, "const SHELL_VERSION = %q;\nconst SHELL_FILES = %s;\n", shellVersion(a.files, policy), list)
	a.files[workerPath] = newFile(worker.contentType, append(head, worker.body...))
	return nil
}

// shellVersion tells one build of the app from another. It changes with any file, the worker's own included, and with
// the headers the files are sent with (the policy), which a browser keeps along with every file it has kept.
func shellVersion(files map[string]file, headers string) string {
	all := appendField(nil, []byte(headers))
	for _, p := range slices.Sorted(maps.Keys(files)) {
		all = appendField(all, []byte(p))
		all = appendField(all, files[p].body)
	}
	sum := sha256.Sum256(all)
	return hex.EncodeToString(sum[:8])
}

// appendField adds a piece of data with its length in front of it, so that two lists of pieces give the same bytes
// only when they are the same pieces: "a"+"bc" is not "ab"+"c".
func appendField(dst, field []byte) []byte {
	dst = strconv.AppendInt(dst, int64(len(field)), 10)
	dst = append(dst, ':')
	return append(dst, field...)
}

// Has tells whether urlPath is a file of the app. Only exact matches count, so nothing can climb out of the files.
func (a *App) Has(urlPath string) bool {
	_, ok := a.files[urlPath]
	return ok
}

// ServeHTTP answers GET and HEAD requests for the files of the app.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f, ok := a.files[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h := w.Header()
	h.Set("Content-Type", f.contentType)
	h.Set("ETag", f.etag)
	// The files have no fingerprint in their names, so a browser may keep them but has to ask whether they changed:
	// the answer is a cheap 304.
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Security-Policy", policy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	// "same-origin" and not "no-referrer": with the latter a browser sends "Origin: null" with its POST requests.
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	// ServeContent answers If-None-Match with 304 and sends no body to HEAD.
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.body))
}

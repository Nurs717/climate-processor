package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"climate-processor/internal/apperr"
	"climate-processor/internal/domain"
	"climate-processor/internal/logger"
	"climate-processor/internal/service"
)

var (
	testNow = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	testEnd = testNow.Add(time.Second) // where an "up to now" interval ends: the end of the current second
)

// fakeReader is a Reader whose answers the tests control; it records the arguments of history queries.
type fakeReader struct {
	latest    service.LatestReading
	hasLatest bool
	buckets   []domain.Bucket
	summary   domain.Summary
	err       error // returned by History and Summary
	boom      bool  // makes Latest panic

	queries  int
	from, to time.Time
	step     time.Duration
}

func (f *fakeReader) Latest() (service.LatestReading, bool) {
	if f.boom {
		panic("boom")
	}
	return f.latest, f.hasLatest
}

func (f *fakeReader) History(_ context.Context, from, to time.Time, step time.Duration) ([]domain.Bucket, error) {
	f.queries++
	f.from, f.to, f.step = from, to, step
	return f.buckets, f.err
}

func (f *fakeReader) Summary(_ context.Context, from, to time.Time) (domain.Summary, error) {
	f.queries++
	f.from, f.to = from, to
	return f.summary, f.err
}

type testEnv struct {
	router http.Handler
	reader *fakeReader
	logs   *bytes.Buffer
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	logs := &bytes.Buffer{}
	reader := &fakeReader{}
	router := NewRouter(logger.New(logs, slog.LevelDebug, logger.FormatJSON), reader, Options{
		Now: func() time.Time { return testNow },
	})
	return &testEnv{router: router, reader: reader, logs: logs}
}

func (e *testEnv) get(target string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

// records returns the log lines written so far as JSON objects.
func (e *testEnv) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(e.logs.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q (%v)", line, err)
		}
		out = append(out, m)
	}
	return out
}

func (e *testEnv) record(t *testing.T, message string) map[string]any {
	t.Helper()
	for _, r := range e.records(t) {
		if r["message"] == message {
			return r
		}
	}
	t.Fatalf("no %q record in the log:\n%s", message, e.logs.String())
	return nil
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("response is not the expected JSON: %v\n%s", err, rec.Body.String())
	}
	return v
}

func TestLatest(t *testing.T) {
	e := newTestEnv(t)
	e.reader.hasLatest = true
	e.reader.latest = service.LatestReading{
		ClimateData: domain.ClimateData{
			DeviceID: "room", Temperature: 21.5, Humidity: 45.2, Pressure: 101325, Timestamp: testNow.Add(-3 * time.Second),
		},
		Age: 3 * time.Second,
	}

	rec := e.get("/api/v1/latest")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	got := decode[latestDTO](t, rec)
	want := latestDTO{DeviceID: "room", Time: testNow.Add(-3 * time.Second), AgeSeconds: 3, Temperature: 21.5, Humidity: 45.2, Pressure: 101325}
	if got.DeviceID != want.DeviceID || !got.Time.Equal(want.Time) || got.AgeSeconds != 3 || got.Stale ||
		got.Temperature != want.Temperature || got.Humidity != want.Humidity || got.Pressure != want.Pressure {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestLatestBeforeTheFirstReading(t *testing.T) {
	e := newTestEnv(t)

	rec := e.get("/api/v1/latest")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	body := decode[errorBody](t, rec)
	if body.Error.Code != "not_found" || body.Error.Message != "no readings yet" {
		t.Errorf("error = %+v", body.Error)
	}
	if id := rec.Header().Get("X-Request-ID"); id == "" || body.Error.RequestID != id {
		t.Errorf("the error body must carry the request id of the response header: %q vs %q", body.Error.RequestID, id)
	}
}

func TestReadingsUsesRangeAndStep(t *testing.T) {
	e := newTestEnv(t)
	e.reader.buckets = []domain.Bucket{
		{Start: testNow.Add(-2 * time.Minute), Summary: domain.Summary{
			Count:       12,
			Temperature: domain.Stat{Avg: 21.456, Min: 21.1, Max: 21.9},
			Humidity:    domain.Stat{Avg: 45, Min: 44, Max: 46},
			Pressure:    domain.Stat{Avg: 101325.004, Min: 101300, Max: 101350},
		}},
		{Start: testNow.Add(-time.Minute), Summary: domain.Summary{Count: 12}},
	}

	rec := e.get("/api/v1/readings?range=1h&step=1m")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body)
	}
	if !e.reader.from.Equal(testEnd.Add(-time.Hour)) || !e.reader.to.Equal(testEnd) || e.reader.step != time.Minute {
		t.Errorf("service asked for %s .. %s step %s", e.reader.from, e.reader.to, e.reader.step)
	}
	got := decode[readingsDTO](t, rec)
	if got.StepSeconds != 60 || len(got.Points) != 2 {
		t.Fatalf("got %+v", got)
	}
	first := got.Points[0]
	if !first.Time.Equal(testNow.Add(-2*time.Minute)) || first.Count != 12 {
		t.Errorf("first point = %+v", first)
	}
	if first.Temperature != (statDTO{Avg: 21.46, Min: 21.1, Max: 21.9}) {
		t.Errorf("values must be rounded to two decimals: %+v", first.Temperature)
	}
	if first.Pressure.Avg != 101325 {
		t.Errorf("pressure avg = %v, want 101325", first.Pressure.Avg)
	}
}

func TestReadingsChoosesTheStepAndReturnsAnEmptyList(t *testing.T) {
	e := newTestEnv(t)

	rec := e.get("/api/v1/readings") // no parameters at all

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body)
	}
	if !e.reader.from.Equal(testEnd.Add(-24*time.Hour)) || e.reader.step != 5*time.Minute {
		t.Errorf("default query: from %s step %s, want the last 24h in 5m windows", e.reader.from, e.reader.step)
	}
	if !strings.Contains(rec.Body.String(), `"points":[]`) {
		t.Errorf("an empty series must be [] and not null: %s", rec.Body)
	}
	if got := decode[readingsDTO](t, rec); got.StepSeconds != 300 {
		t.Errorf("step_seconds = %d, want the chosen step (300)", got.StepSeconds)
	}
}

func TestReadingsWithAnExplicitInterval(t *testing.T) {
	e := newTestEnv(t)

	// %2B is an encoded "+": the offset must be converted to UTC.
	rec := e.get("/api/v1/readings?from=2026-11-26T10:00:00%2B05:00&to=2026-11-26T11:00:00Z&step=30s")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body)
	}
	wantFrom := time.Date(2026, 11, 26, 5, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, 11, 26, 11, 0, 0, 0, time.UTC)
	if !e.reader.from.Equal(wantFrom) || !e.reader.to.Equal(wantTo) || e.reader.step != 30*time.Second {
		t.Errorf("service asked for %s .. %s step %s", e.reader.from, e.reader.to, e.reader.step)
	}
}

func TestBadQueryParametersAreRejectedBeforeTheService(t *testing.T) {
	tests := []struct {
		target string
		reason string
	}{
		{"/api/v1/readings?range=abc", `"range"`},
		{"/api/v1/readings?range=0h", `"range"`},
		{"/api/v1/readings?range=24h&from=2026-11-26T10:00:00Z", "not both"},
		{"/api/v1/readings?from=yesterday", `"from"`},
		{"/api/v1/readings?to=2026-11-26T10:00:00Z", `"from" is required`},
		{"/api/v1/readings?step=fast", `"step"`},
		{"/api/v1/readings?step=0s", `"step"`},
		{"/api/v1/stats?range=abc", `"range"`},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			e := newTestEnv(t)

			rec := e.get(tt.target)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body)
			}
			body := decode[errorBody](t, rec)
			if body.Error.Code != "bad_request" || !strings.Contains(body.Error.Message, tt.reason) {
				t.Errorf("error = %+v, want a bad_request about %s", body.Error, tt.reason)
			}
			if e.reader.queries != 0 {
				t.Error("the service must not be called for an invalid request")
			}
		})
	}
}

func TestServiceErrorsAreMappedToStatuses(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    string
		wantMessage string
	}{
		{
			"bad request from the service",
			apperr.New(apperr.CodeBadRequest, "service.History", "from must be before to"),
			http.StatusBadRequest, "bad_request", "from must be before to",
		},
		{
			"history is disabled",
			apperr.New(apperr.CodeUnavailable, "service.History", "history is not enabled"),
			http.StatusServiceUnavailable, "unavailable", "history is not enabled",
		},
		{
			"wrapped application error",
			fmt.Errorf("handler: %w", apperr.New(apperr.CodeTooManyRequests, "x", "slow down")),
			http.StatusTooManyRequests, "too_many_requests", "slow down",
		},
		{
			"unexpected error",
			errors.New("pq: password authentication failed for user admin"),
			http.StatusInternalServerError, "unknown", "internal error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.reader.err = tt.err

			rec := e.get("/api/v1/stats")

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			body := decode[errorBody](t, rec)
			if body.Error.Code != tt.wantCode || body.Error.Message != tt.wantMessage {
				t.Errorf("error = %+v", body.Error)
			}
			if strings.Contains(rec.Body.String(), "password") {
				t.Errorf("internal details leaked to the client: %s", rec.Body)
			}
		})
	}
}

func TestStats(t *testing.T) {
	e := newTestEnv(t)
	e.reader.summary = domain.Summary{
		Count:       17280,
		Temperature: domain.Stat{Avg: 21.456, Min: 19.9, Max: 23.1},
		Humidity:    domain.Stat{Avg: 45, Min: 40, Max: 52},
		Pressure:    domain.Stat{Avg: 101325, Min: 100900, Max: 101700},
	}

	rec := e.get("/api/v1/stats") // default range: the last 24 hours

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body)
	}
	if !e.reader.from.Equal(testEnd.Add(-24*time.Hour)) || !e.reader.to.Equal(testEnd) {
		t.Errorf("service asked for %s .. %s", e.reader.from, e.reader.to)
	}
	got := decode[statsDTO](t, rec)
	if got.Count != 17280 || got.Temperature != (statDTO{Avg: 21.46, Min: 19.9, Max: 23.1}) || !got.To.Equal(testEnd) {
		t.Errorf("got %+v", got)
	}
}

func TestUnknownRouteGetsTheJSONError(t *testing.T) {
	e := newTestEnv(t)

	rec := e.get("/api/v1/nothing")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := decode[errorBody](t, rec); body.Error.Code != "not_found" {
		t.Errorf("error = %+v", body.Error)
	}
}

func TestRequestID(t *testing.T) {
	hex16 := regexp.MustCompile(`^[0-9a-f]{16}$`)

	t.Run("generated when absent", func(t *testing.T) {
		e := newTestEnv(t)
		if id := e.get("/api/v1/latest").Header().Get("X-Request-ID"); !hex16.MatchString(id) {
			t.Errorf("generated id = %q, want 16 hex characters", id)
		}
	})
	t.Run("taken from a sane header", func(t *testing.T) {
		e := newTestEnv(t)
		rec := e.get("/api/v1/latest", "X-Request-ID", "abc-123_DEF.4")
		if id := rec.Header().Get("X-Request-ID"); id != "abc-123_DEF.4" {
			t.Errorf("id = %q, want the caller's id", id)
		}
		if got := e.record(t, "request done")["request_id"]; got != "abc-123_DEF.4" {
			t.Errorf("logged request_id = %v", got)
		}
	})
	t.Run("replaced when the header is not sane", func(t *testing.T) {
		e := newTestEnv(t)
		for _, bad := range []string{"has spaces", strings.Repeat("a", 65), `"quoted"`} {
			if id := e.get("/api/v1/latest", "X-Request-ID", bad).Header().Get("X-Request-ID"); !hex16.MatchString(id) {
				t.Errorf("id for %q = %q, want a generated one", bad, id)
			}
		}
	})
}

func TestAccessLog(t *testing.T) {
	e := newTestEnv(t)
	e.reader.hasLatest = true

	rec := e.get("/api/v1/latest", "X-Request-ID", "req-1")

	r := e.record(t, "request done")
	if r["level"] != "INFO" || r["method"] != "GET" || r["path"] != "/api/v1/latest" || r["request_id"] != "req-1" {
		t.Errorf("record = %v", r)
	}
	if r["status"] != float64(http.StatusOK) || r["bytes"] != float64(rec.Body.Len()) {
		t.Errorf("status/bytes = %v/%v, want 200/%d", r["status"], r["bytes"], rec.Body.Len())
	}
	if _, ok := r["duration_ms"]; !ok {
		t.Error("duration_ms is missing")
	}
	if _, ok := r["error"]; ok {
		t.Errorf("a successful request must not have an error: %v", r)
	}
}

func TestAccessLogReportsTheCauseOfFailures(t *testing.T) {
	t.Run("client error is INFO with the reason", func(t *testing.T) {
		e := newTestEnv(t)
		e.get("/api/v1/readings?range=abc")

		r := e.record(t, "request done")
		if r["level"] != "INFO" || r["status"] != float64(400) || !strings.Contains(fmt.Sprint(r["error"]), `invalid "range"`) {
			t.Errorf("record = %v", r)
		}
	})
	t.Run("server error is ERROR with the cause", func(t *testing.T) {
		e := newTestEnv(t)
		e.reader.err = errors.New("disk I/O error")
		e.get("/api/v1/stats")

		r := e.record(t, "request done")
		if r["level"] != "ERROR" || r["status"] != float64(500) || !strings.Contains(fmt.Sprint(r["error"]), "disk I/O error") {
			t.Errorf("record = %v", r)
		}
	})
	t.Run("an abandoned request is not a server error", func(t *testing.T) {
		e := newTestEnv(t)
		e.reader.err = fmt.Errorf("sqlite: aggregate: %w", context.Canceled)

		rec := e.get("/api/v1/stats")

		if rec.Code != 499 {
			t.Errorf("status = %d, want 499", rec.Code)
		}
		if r := e.record(t, "request done"); r["level"] != "INFO" {
			t.Errorf("record = %v, want INFO", r)
		}
	})
}

func TestPanicIsRecovered(t *testing.T) {
	e := newTestEnv(t)
	e.reader.boom = true

	rec := e.get("/api/v1/latest", "X-Request-ID", "req-panic")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := decode[errorBody](t, rec)
	if body.Error.Code != "unknown" || body.Error.Message != "internal error" || body.Error.RequestID != "req-panic" {
		t.Errorf("error = %+v", body.Error)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("the panic value leaked to the client: %s", rec.Body)
	}

	p := e.record(t, "panic in a request handler")
	if p["level"] != "ERROR" || p["panic"] != "boom" || p["request_id"] != "req-panic" || !strings.Contains(fmt.Sprint(p["stack"]), "goroutine") {
		t.Errorf("panic record = %v", p)
	}
	if r := e.record(t, "request done"); r["level"] != "ERROR" || r["status"] != float64(500) {
		t.Errorf("the request must still be logged as finished: %v", r)
	}

	// The server survives: the next request is served normally.
	e.reader.boom = false
	if rec := e.get("/api/v1/latest"); rec.Code != http.StatusNotFound {
		t.Errorf("next request: status = %d, want 404 (no readings yet)", rec.Code)
	}
}

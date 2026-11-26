package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	srv, err := newAPIServer(quiet(), cfg, fakeReader{})
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
	srv, err := newAPIServer(quiet(), cfg, fakeReader{})
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
	if _, err := newAPIServer(quiet(), bad, fakeReader{}); err == nil {
		t.Error("an invalid trusted proxy must fail the start")
	}
	noBurst := &config.Config{HTTPAddr: ":8080", APIRatePerMin: 60, APIRateBurst: 0}
	if _, err := newAPIServer(quiet(), noBurst, fakeReader{}); err == nil {
		t.Error("a limiter without a burst must fail the start")
	}
}

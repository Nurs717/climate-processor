package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"climate-processor/internal/domain"
	"climate-processor/internal/logger"
	"climate-processor/internal/metrics"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const validPayload = `{"temperature":21.5,"humidity":45.2,"pressure":101325}`

// fakeRepo is an in-memory ReadingRepository; err, when set, makes every Save fail.
type fakeRepo struct {
	saved []domain.ClimateData
	err   error
}

func (f *fakeRepo) Save(_ context.Context, d domain.ClimateData) error {
	if f.err != nil {
		return f.err
	}
	f.saved = append(f.saved, d)
	return nil
}

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}

// newTestService returns a service that does not store readings.
func newTestService(t *testing.T) (*ClimateService, *time.Time) {
	t.Helper()
	return newTestServiceWithRepo(t, NopRepository{})
}

// newTestServiceWithRepo returns a service with a controllable clock (UTC+5 for display).
func newTestServiceWithRepo(t *testing.T, repo ReadingRepository) (*ClimateService, *time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	s := NewClimateService(
		slog.New(slog.DiscardHandler),
		metrics.NewClimateMetrics(prometheus.NewRegistry()),
		repo,
		Options{
			Location:   time.FixedZone("UTC+5", 5*60*60),
			StaleAfter: 5 * time.Minute,
			Now:        func() time.Time { return now },
		},
	)
	return s, &now
}

func TestProcessMessageValid(t *testing.T) {
	s, _ := newTestService(t)

	if err := s.ProcessMessage([]byte(validPayload)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	status := s.GetCurrentStatus()
	for _, want := range []string{"21.5 °C", "45.2%", "760.0 mmHg", "15:00:00"} {
		if !strings.Contains(status, want) {
			t.Errorf("status does not contain %q:\n%s", want, status)
		}
	}
	if strings.Contains(status, "stale") {
		t.Errorf("fresh data must not be marked stale:\n%s", status)
	}
}

func TestProcessMessageRejectsInvalid(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"not json", `hello`},
		{"missing humidity", `{"temperature":21.5,"pressure":101325}`},
		{"missing pressure", `{"temperature":21.5,"humidity":45}`},
		{"temperature too high", `{"temperature":120,"humidity":45,"pressure":101325}`},
		{"temperature too low", `{"temperature":-50,"humidity":45,"pressure":101325}`},
		{"humidity over 100", `{"temperature":21,"humidity":101,"pressure":101325}`},
		{"pressure zero", `{"temperature":21,"humidity":45,"pressure":0}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newTestService(t)

			err := s.ProcessMessage([]byte(tt.payload))
			if !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("expected ErrInvalidPayload, got %v", err)
			}
			if got := s.GetCurrentStatus(); !strings.Contains(got, "No sensor data") {
				t.Errorf("rejected message must not be stored, status:\n%s", got)
			}
		})
	}
}

func TestProcessMessageStoresReading(t *testing.T) {
	repo := &fakeRepo{}
	s, now := newTestServiceWithRepo(t, repo)

	if err := s.ProcessMessage([]byte(validPayload)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := domain.ClimateData{DeviceID: "room", Temperature: 21.5, Humidity: 45.2, Pressure: 101325, Timestamp: *now}
	if len(repo.saved) != 1 || repo.saved[0] != want {
		t.Fatalf("saved = %+v, want [%+v]", repo.saved, want)
	}
}

func TestRejectedMessageIsNotStored(t *testing.T) {
	repo := &fakeRepo{}
	s, _ := newTestServiceWithRepo(t, repo)

	if err := s.ProcessMessage([]byte(`{"temperature":120,"humidity":45,"pressure":101325}`)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("expected ErrInvalidPayload, got %v", err)
	}
	if len(repo.saved) != 0 {
		t.Errorf("rejected message must not be stored, got %+v", repo.saved)
	}
}

func TestStorageFailureDoesNotRejectReading(t *testing.T) {
	repo := &fakeRepo{err: errors.New("disk is full")}
	s, _ := newTestServiceWithRepo(t, repo)

	if err := s.ProcessMessage([]byte(validPayload)); err != nil {
		t.Fatalf("a storage failure must not reject the reading, got %v", err)
	}
	if got := s.GetCurrentStatus(); !strings.Contains(got, "21.5 °C") {
		t.Errorf("reading must still be served to the bot:\n%s", got)
	}
	if got := counterValue(t, s.metrics.StoreErrors); got != 1 {
		t.Errorf("StoreErrors = %v, want 1", got)
	}
}

func TestAcceptedReadingIsNotLoggedAtInfo(t *testing.T) {
	var buf bytes.Buffer
	s := NewClimateService(
		logger.New(&buf, slog.LevelInfo, logger.FormatJSON),
		metrics.NewClimateMetrics(prometheus.NewRegistry()),
		NopRepository{},
		Options{},
	)

	if err := s.ProcessMessage([]byte(validPayload)); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("an accepted reading must not produce INFO logs (one line per reading floods the log), got: %s", buf.String())
	}
}

func TestRejectedMessageIsLoggedWithReason(t *testing.T) {
	var buf bytes.Buffer
	s := NewClimateService(
		logger.New(&buf, slog.LevelInfo, logger.FormatJSON),
		metrics.NewClimateMetrics(prometheus.NewRegistry()),
		NopRepository{},
		Options{},
	)

	_ = s.ProcessMessage([]byte(`hello`))

	got := buf.String()
	if !strings.Contains(got, `"level":"WARN"`) || !strings.Contains(got, `"reason":"parse"`) {
		t.Errorf("expected a WARN record with reason=parse, got: %s", got)
	}
}

func TestStatusMarksStaleData(t *testing.T) {
	s, now := newTestService(t)
	if err := s.ProcessMessage([]byte(validPayload)); err != nil {
		t.Fatal(err)
	}

	*now = now.Add(6 * time.Minute)

	if got := s.GetCurrentStatus(); !strings.Contains(got, "stale") || !strings.Contains(got, "6m") {
		t.Errorf("expected a stale warning with age, got:\n%s", got)
	}
}

func TestCheckStalenessNotifiesOncePerChange(t *testing.T) {
	s, now := newTestService(t)

	if msg := s.checkStaleness(); msg != "" {
		t.Fatalf("no notification expected right after start, got %q", msg)
	}

	// Sensor never sent anything.
	*now = now.Add(6 * time.Minute)
	if msg := s.checkStaleness(); !strings.Contains(msg, "since the service started") {
		t.Fatalf("expected a 'no data since start' notification, got %q", msg)
	}
	if msg := s.checkStaleness(); msg != "" {
		t.Fatalf("must not repeat the notification, got %q", msg)
	}

	// Sensor comes back.
	if err := s.ProcessMessage([]byte(validPayload)); err != nil {
		t.Fatal(err)
	}
	if msg := s.checkStaleness(); !strings.Contains(msg, "flowing again") {
		t.Fatalf("expected a 'back online' notification, got %q", msg)
	}
	if msg := s.checkStaleness(); msg != "" {
		t.Fatalf("must not repeat the notification, got %q", msg)
	}

	// Sensor goes silent again.
	*now = now.Add(10 * time.Minute)
	if msg := s.checkStaleness(); !strings.Contains(msg, "Last update at 15:06:00") {
		t.Fatalf("expected a 'went silent' notification with last update time, got %q", msg)
	}
}

func TestFormatAge(t *testing.T) {
	tests := map[time.Duration]string{
		42 * time.Second:               "42s",
		5*time.Minute + 20*time.Second: "5m",
		2*time.Hour + 7*time.Minute:    "2h07m",
	}
	for d, want := range tests {
		if got := formatAge(d); got != want {
			t.Errorf("formatAge(%s) = %q, want %q", d, got, want)
		}
	}
}

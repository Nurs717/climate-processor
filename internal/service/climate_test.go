package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"climate-processor/internal/metrics"

	"github.com/prometheus/client_golang/prometheus"
)

const validPayload = `{"temperature":21.5,"humidity":45.2,"pressure":101325}`

// newTestService returns a service with a controllable clock (UTC+5 for display).
func newTestService(t *testing.T) (*ClimateService, *time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	s := NewClimateService(
		metrics.NewClimateMetrics(prometheus.NewRegistry()),
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

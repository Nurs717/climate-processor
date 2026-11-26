package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"climate-processor/internal/domain"
	"climate-processor/internal/metrics"
)

// Plausible ranges of the BME280 sensor; anything outside is treated as a faulty reading.
const (
	minTempC      = -40.0
	maxTempC      = 85.0
	minHumidity   = 0.0
	maxHumidity   = 100.0
	minPressurePa = 30000.0
	maxPressurePa = 110000.0

	defaultStaleAfter = 5 * time.Minute
	defaultDeviceID   = "room"
	maxLoggedPayload  = 256             // bytes of a rejected message written to the log
	saveTimeout       = 2 * time.Second // upper bound for one database write
	paToMmHg          = 0.00750062
)

// ErrInvalidPayload is wrapped by every error returned for a rejected message.
var ErrInvalidPayload = errors.New("invalid payload")

// Options tunes the service. All fields are optional.
type Options struct {
	Location   *time.Location   // used to format times for the user (default UTC)
	StaleAfter time.Duration    // data older than this is reported as stale (default 5m)
	DeviceID   string           // id stored with every reading (default "room")
	Now        func() time.Time // clock, replaced in tests (default time.Now)
}

type ClimateService struct {
	log        *slog.Logger
	metrics    *metrics.ClimateMetrics // Metrics layer
	repo       ReadingRepository       // History storage
	deviceID   string
	loc        *time.Location
	staleAfter time.Duration
	now        func() time.Time
	startedAt  time.Time

	mu          sync.RWMutex
	currentData domain.ClimateData
	hasData     bool
	offline     bool // true after a "no data" notification was produced
}

// Dependencies (metrics, repository) are injected via the constructor.
// A nil repo disables storing readings.
func NewClimateService(log *slog.Logger, m *metrics.ClimateMetrics, repo ReadingRepository, opts Options) *ClimateService {
	if log == nil {
		log = slog.Default()
	}
	if repo == nil {
		repo = NopRepository{}
	}
	if opts.DeviceID == "" {
		opts.DeviceID = defaultDeviceID
	}
	if opts.Location == nil {
		opts.Location = time.UTC
	}
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = defaultStaleAfter
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &ClimateService{
		log:        log,
		metrics:    m,
		repo:       repo,
		deviceID:   opts.DeviceID,
		loc:        opts.Location,
		staleAfter: opts.StaleAfter,
		now:        opts.Now,
		startedAt:  opts.Now(),
	}
}

// ProcessMessage validates a raw MQTT payload and stores it. Rejected messages are logged,
// counted in Prometheus and returned as an error wrapping ErrInvalidPayload.
func (s *ClimateService) ProcessMessage(payload []byte) error {
	var p domain.Payload
	if err := json.Unmarshal(payload, &p); err != nil {
		s.metrics.Rejected.WithLabelValues("parse").Inc()
		s.log.Warn("message rejected", "reason", "parse", "error", err)
		return fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}

	if p.Temperature == nil || p.Humidity == nil || p.Pressure == nil {
		s.metrics.Rejected.WithLabelValues("missing_field").Inc()
		s.log.Warn("message rejected", "reason", "missing_field", "payload", loggedPayload(payload))
		return fmt.Errorf("%w: missing field", ErrInvalidPayload)
	}

	t, h, pr := *p.Temperature, *p.Humidity, *p.Pressure
	if t < minTempC || t > maxTempC || h < minHumidity || h > maxHumidity || pr < minPressurePa || pr > maxPressurePa {
		s.metrics.Rejected.WithLabelValues("out_of_range").Inc()
		s.log.Warn("message rejected", "reason", "out_of_range",
			"temperature_c", t, "humidity_percent", h, "pressure_pa", pr)
		return fmt.Errorf("%w: value out of range", ErrInvalidPayload)
	}

	now := s.now()
	data := domain.ClimateData{DeviceID: s.deviceID, Temperature: t, Humidity: h, Pressure: pr, Timestamp: now}

	// Store the state for the Telegram bot
	s.mu.Lock()
	s.currentData = data
	s.hasData = true
	s.mu.Unlock()

	// === UPDATE PROMETHEUS METRICS ===
	s.metrics.Temperature.Set(t)
	s.metrics.Humidity.Set(h)
	s.metrics.Pressure.Set(pr)
	s.metrics.LastUpdate.Set(float64(now.Unix()))

	s.save(data)

	s.log.Debug("reading accepted", "device_id", s.deviceID,
		"temperature_c", t, "humidity_percent", h, "pressure_pa", pr)
	return nil
}

// save persists a reading. A storage failure must not reject a valid reading:
// the bot and Prometheus keep working, the failure is logged and counted.
func (s *ClimateService) save(d domain.ClimateData) {
	ctx, cancel := context.WithTimeout(context.Background(), saveTimeout)
	defer cancel()
	if err := s.repo.Save(ctx, d); err != nil {
		s.metrics.StoreErrors.Inc()
		s.log.Error("failed to store reading", "device_id", d.DeviceID, "error", err)
	}
}

// loggedPayload shortens a rejected message so that a huge payload cannot flood the log.
func loggedPayload(p []byte) string {
	if len(p) > maxLoggedPayload {
		return string(p[:maxLoggedPayload]) + "…"
	}
	return string(p)
}

// GetCurrentStatus formats the data for sending to Telegram
func (s *ClimateService) GetCurrentStatus() string {
	latest, ok := s.Latest()
	if !ok {
		return "⚠️ No sensor data received yet."
	}

	// Convert Pascals to mmHg for convenience
	mmHg := latest.Pressure * paToMmHg

	text := fmt.Sprintf(
		"🏠 *Apartment climate:*\n\n"+
			"🌡 *Temperature:* `%.1f °C`\n"+
			"💧 *Humidity:* `%.1f%%`\n"+
			"🧭 *Pressure:* `%.1f mmHg`\n\n"+
			"🕒 _Updated: %s_",
		latest.Temperature,
		latest.Humidity,
		mmHg,
		latest.Timestamp.In(s.loc).Format("15:04:05"),
	)

	if latest.Stale {
		text += fmt.Sprintf("\n\n⚠️ _Data is stale: last update %s ago._", formatAge(latest.Age))
	}
	return text
}

// WatchStaleness checks every interval whether readings stopped arriving and calls notify
// once when the sensor goes silent and once when it comes back. It returns when ctx is done.
func (s *ClimateService) WatchStaleness(ctx context.Context, interval time.Duration, notify func(text string)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if msg := s.checkStaleness(); msg != "" {
				notify(msg)
			}
		}
	}
}

// checkStaleness returns a notification text when the sensor state changed
// (went silent / came back) and an empty string otherwise.
func (s *ClimateService) checkStaleness() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Before the first reading, count silence from the service start.
	ref := s.startedAt
	if s.hasData {
		ref = s.currentData.Timestamp
	}
	age := s.now().Sub(ref)
	stale := age > s.staleAfter

	switch {
	case stale && !s.offline:
		s.offline = true
		if !s.hasData {
			return fmt.Sprintf("⚠️ No sensor data since the service started (%s ago).", formatAge(age))
		}
		return fmt.Sprintf("⚠️ No data from the sensor for %s. Last update at %s.",
			formatAge(age), ref.In(s.loc).Format("15:04:05"))
	case !stale && s.offline:
		s.offline = false
		return "✅ Sensor data is flowing again."
	}
	return ""
}

func formatAge(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

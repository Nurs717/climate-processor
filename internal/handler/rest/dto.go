package rest

import (
	"math"
	"time"

	"climate-processor/internal/auth"
	"climate-processor/internal/domain"
	"climate-processor/internal/service"
)

// The JSON field names carry the unit, so a client never has to guess it.

type statDTO struct {
	Avg float64 `json:"avg"`
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

func newStat(s domain.Stat) statDTO {
	return statDTO{Avg: round2(s.Avg), Min: round2(s.Min), Max: round2(s.Max)}
}

// round2 keeps two decimals: AVG over thousands of readings yields digits no sensor can resolve.
func round2(v float64) float64 { return math.Round(v*100) / 100 }

type latestDTO struct {
	DeviceID    string    `json:"device_id"`
	Time        time.Time `json:"time"`        // when the reading was received, in whole seconds like the history
	AgeSeconds  int64     `json:"age_seconds"` // how old it is now
	Stale       bool      `json:"stale"`       // the sensor has been silent longer than STALE_AFTER
	Temperature float64   `json:"temperature_c"`
	Humidity    float64   `json:"humidity_percent"`
	Pressure    float64   `json:"pressure_pa"`
}

func newLatest(l service.LatestReading) latestDTO {
	return latestDTO{
		DeviceID:    l.DeviceID,
		Time:        l.Timestamp.UTC().Truncate(time.Second),
		AgeSeconds:  int64(l.Age.Seconds()),
		Stale:       l.Stale,
		Temperature: l.Temperature,
		Humidity:    l.Humidity,
		Pressure:    l.Pressure,
	}
}

type pointDTO struct {
	Time        time.Time `json:"time"`  // start of the window
	Count       int       `json:"count"` // readings in the window
	Temperature statDTO   `json:"temperature_c"`
	Humidity    statDTO   `json:"humidity_percent"`
	Pressure    statDTO   `json:"pressure_pa"`
}

type readingsDTO struct {
	From        time.Time  `json:"from"`
	To          time.Time  `json:"to"`
	StepSeconds int64      `json:"step_seconds"`
	Points      []pointDTO `json:"points"` // windows without readings are absent: a gap is a gap in the data
}

func newReadings(from, to time.Time, step time.Duration, buckets []domain.Bucket) readingsDTO {
	points := make([]pointDTO, 0, len(buckets)) // never null in JSON
	for _, b := range buckets {
		points = append(points, pointDTO{
			Time:        b.Start.UTC(),
			Count:       b.Count,
			Temperature: newStat(b.Temperature),
			Humidity:    newStat(b.Humidity),
			Pressure:    newStat(b.Pressure),
		})
	}
	return readingsDTO{From: from, To: to, StepSeconds: int64(step / time.Second), Points: points}
}

type statsDTO struct {
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	Count       int       `json:"count"`
	Temperature statDTO   `json:"temperature_c"`
	Humidity    statDTO   `json:"humidity_percent"`
	Pressure    statDTO   `json:"pressure_pa"`
}

func newStats(from, to time.Time, s domain.Summary) statsDTO {
	return statsDTO{
		From:        from,
		To:          to,
		Count:       s.Count,
		Temperature: newStat(s.Temperature),
		Humidity:    newStat(s.Humidity),
		Pressure:    newStat(s.Pressure),
	}
}

// meDTO answers "who am I": the role the server gives the client, and whether the owner can sign in at all,
// so that a client can show or hide its sign-in button.
type meDTO struct {
	Role          domain.Role `json:"role"`
	UserID        int64       `json:"user_id,omitempty"` // Telegram user id of the owner
	SignInEnabled bool        `json:"sign_in_enabled"`
}

func newMe(id auth.Identity, signInEnabled bool) meDTO {
	return meDTO{Role: id.Role, UserID: id.UserID, SignInEnabled: signInEnabled}
}

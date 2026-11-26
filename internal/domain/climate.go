package domain

import "time"

// Payload is the JSON message published by the ESP32-C3.
// Pointers let us tell a missing field apart from a real zero value.
type Payload struct {
	Temperature *float64 `json:"temperature"` // °C
	Humidity    *float64 `json:"humidity"`    // %
	Pressure    *float64 `json:"pressure"`    // Pa
}

// ClimateData is a validated reading, stamped with the backend receive time.
type ClimateData struct {
	DeviceID    string // which sensor produced the reading
	Temperature float64
	Humidity    float64
	Pressure    float64
	Timestamp   time.Time
}

// Stat is the average, minimum and maximum of one quantity.
type Stat struct {
	Avg float64
	Min float64
	Max float64
}

// Summary aggregates a set of readings. Count == 0 means there were none, and the stats are then zero.
type Summary struct {
	Count       int
	Temperature Stat // °C
	Humidity    Stat // %
	Pressure    Stat // Pa
}

// Bucket is the Summary of the readings that fall into one time window of a series.
// Start is the beginning of the window (UTC).
type Bucket struct {
	Start time.Time
	Summary
}

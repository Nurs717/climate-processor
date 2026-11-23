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
	Temperature float64
	Humidity    float64
	Pressure    float64
	Timestamp   time.Time
}

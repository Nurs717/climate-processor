package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type ClimateMetrics struct {
	Temperature prometheus.Gauge
	Humidity    prometheus.Gauge
	Pressure    prometheus.Gauge

	// LastUpdate is the Unix time of the last accepted reading.
	// Alert on it, e.g.: time() - room_last_update_timestamp_seconds > 300
	LastUpdate prometheus.Gauge
	// Rejected counts discarded messages by reason: parse, missing_field, out_of_range.
	Rejected *prometheus.CounterVec
}

// NewClimateMetrics registers the metrics in reg (use prometheus.DefaultRegisterer in the app,
// a fresh prometheus.NewRegistry() in tests).
func NewClimateMetrics(reg prometheus.Registerer) *ClimateMetrics {
	f := promauto.With(reg)
	return &ClimateMetrics{
		Temperature: f.NewGauge(prometheus.GaugeOpts{
			Name: "room_temperature_celsius",
			Help: "Current room temperature from the BME280 sensor",
		}),
		Humidity: f.NewGauge(prometheus.GaugeOpts{
			Name: "room_humidity_percent",
			Help: "Current relative humidity in the room",
		}),
		Pressure: f.NewGauge(prometheus.GaugeOpts{
			Name: "room_pressure_pascal",
			Help: "Current atmospheric pressure in Pascals",
		}),
		LastUpdate: f.NewGauge(prometheus.GaugeOpts{
			Name: "room_last_update_timestamp_seconds",
			Help: "Unix time of the last accepted sensor reading",
		}),
		Rejected: f.NewCounterVec(prometheus.CounterOpts{
			Name: "climate_messages_rejected_total",
			Help: "Discarded MQTT messages by reason",
		}, []string{"reason"}),
	}
}

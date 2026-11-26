package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BrokerURL string
	ClientID  string
	Topic     string

	// Optional MQTT authentication and TLS (required for a public broker).
	MQTTUsername      string
	MQTTPassword      string
	MQTTCAFile        string // PEM file with the CA that signed the broker certificate
	MQTTTLSServerName string // name expected in the broker certificate (SAN)

	MetricsPort string // listen address of the HTTP server, e.g. ":2112"

	TGToken string
	// Telegram chat IDs of the administrators: they always have access and approve or remove other users
	// in the bot. Empty = nobody can approve requests.
	AdminChatIDs []int64
	// File where users approved in the bot are stored (JSON). Empty = kept in memory, lost on restart.
	AccessFile string

	// SQLite file with the reading history. Empty = history is not stored (only the latest reading lives in memory).
	DBPath string
	// Id stored with every reading; lets one database hold several sensors.
	DeviceID string

	Timezone   string        // IANA name used when formatting time in messages
	StaleAfter time.Duration // how long without readings until the data is considered stale
}

func Load() *Config {
	return &Config{
		// If the MQTT_BROKER_URL env variable is set, use it; otherwise fall back to localhost
		BrokerURL: getEnv("MQTT_BROKER_URL", "tcp://127.0.0.1:1883"),
		ClientID:  getEnv("MQTT_CLIENT_ID", "go_climate_worker"),
		Topic:     getEnv("MQTT_TOPIC", "home/room/climate"),

		MQTTUsername:      getEnv("MQTT_USERNAME", ""),
		MQTTPassword:      getEnv("MQTT_PASSWORD", ""),
		MQTTCAFile:        getEnv("MQTT_CA_FILE", ""),
		MQTTTLSServerName: getEnv("MQTT_TLS_SERVER_NAME", ""),

		MetricsPort: getEnv("METRICS_PORT", ":2112"),

		TGToken:      getEnv("TELEGRAM_BOT_TOKEN", ""),
		AdminChatIDs: parseChatIDs(getEnv("TELEGRAM_ADMIN_CHAT_IDS", "")),
		AccessFile:   getEnv("ACCESS_FILE", ""),

		DBPath:   getEnv("DB_PATH", ""),
		DeviceID: getEnv("DEVICE_ID", "room"),

		Timezone:   getEnv("TIMEZONE", "Asia/Almaty"),
		StaleAfter: getEnvDuration("STALE_AFTER", 5*time.Minute),
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		log.Printf("[WARN] Invalid %s=%q, using %s", key, value, fallback)
		return fallback
	}
	return d
}

// parseChatIDs parses a comma-separated list of Telegram chat IDs (group IDs are negative).
// Invalid items are skipped with a warning.
func parseChatIDs(raw string) []int64 {
	var ids []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			log.Printf("[WARN] Ignoring invalid chat id %q in TELEGRAM_ADMIN_CHAT_IDS", part)
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

package config

import (
	"log/slog"
	"net/netip"
	"net/url"
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

	MetricsPort string // listen address of the internal HTTP server (/metrics, /healthz), e.g. ":2112"
	// Listen address of the REST API (internal/handler/rest), e.g. ":8080". Empty = the API is disabled.
	HTTPAddr string
	// The REST API is meant to sit behind a reverse proxy (Caddy) that puts the real client address into
	// X-Forwarded-For. The header is believed only when the request comes from one of these IPs or CIDR ranges.
	// Empty = no proxy is trusted: the client is whoever connects.
	TrustedProxies []string
	// How many requests per minute and how large a burst one client address may make. 0 per minute = no limit.
	APIRatePerMin int
	APIRateBurst  int

	// Owner sign-in with Telegram ("Log In With Telegram", OpenID Connect). It works only when PUBLIC_URL, the
	// client id and secret and DB_PATH (the sessions are kept there) are all set; without it everybody is an
	// anonymous client with the read-only demo role.
	// PublicURL is the address of the service as the browser sees it, without a path, e.g. https://34-28-22-93.sslip.io.
	// Telegram sends the browser back to PublicURL + /api/v1/auth/telegram/callback: register exactly that address
	// as a Redirect URI in @BotFather. An invalid value is ignored (with a warning).
	PublicURL string
	// Client id and secret of the bot's OpenID Connect settings in @BotFather (the id is the bot id). The secret is
	// confidential: it comes from Secret Manager, never from git.
	TGLoginClientID     string
	TGLoginClientSecret string
	// How long a sign-in lasts until the owner has to sign in again, e.g. 720h (30 days).
	SessionTTL time.Duration

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

	LogLevel  string // debug, info, warn or error
	LogFormat string // json (production) or text (readable, for local development)
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
		HTTPAddr:    getEnv("HTTP_ADDR", ":8080"),

		TrustedProxies: parseTrustedProxies(getEnv("TRUSTED_PROXIES", "")),
		APIRatePerMin:  getEnvInt("API_RATE_PER_MIN", 60, 0),
		APIRateBurst:   getEnvInt("API_RATE_BURST", 20, 1),

		PublicURL:           parsePublicURL(getEnv("PUBLIC_URL", "")),
		TGLoginClientID:     strings.TrimSpace(getEnv("TELEGRAM_OIDC_CLIENT_ID", "")),
		TGLoginClientSecret: strings.TrimSpace(getEnv("TELEGRAM_OIDC_CLIENT_SECRET", "")),
		SessionTTL:          getEnvDuration("SESSION_TTL", 30*24*time.Hour),

		TGToken:      getEnv("TELEGRAM_BOT_TOKEN", ""),
		AdminChatIDs: parseChatIDs(getEnv("TELEGRAM_ADMIN_CHAT_IDS", "")),
		AccessFile:   getEnv("ACCESS_FILE", ""),

		DBPath:   getEnv("DB_PATH", ""),
		DeviceID: getEnv("DEVICE_ID", "room"),

		Timezone:   getEnv("TIMEZONE", "Asia/Almaty"),
		StaleAfter: getEnvDuration("STALE_AFTER", 5*time.Minute),

		LogLevel:  getEnv("LOG_LEVEL", "info"),
		LogFormat: getEnv("LOG_FORMAT", "json"),
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
		slog.Warn("invalid environment variable, using the default", "key", key, "value", value, "default", fallback.String())
		return fallback
	}
	return d
}

// getEnvInt reads a whole number that must be at least minValue; anything else falls back to the default.
func getEnvInt(key string, fallback, minValue int) int {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < minValue {
		slog.Warn("invalid environment variable, using the default", "key", key, "value", value, "default", fallback)
		return fallback
	}
	return n
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
			slog.Warn("ignoring invalid chat id", "env", "TELEGRAM_ADMIN_CHAT_IDS", "value", part)
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// parseTrustedProxies parses a comma-separated list of IP addresses and CIDR ranges. Invalid items are skipped
// with a warning: that only makes the service trust fewer proxies, which is the safe direction.
func parseTrustedProxies(raw string) []string {
	var list []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !validProxy(part) {
			slog.Warn("ignoring invalid trusted proxy", "env", "TRUSTED_PROXIES", "value", part)
			continue
		}
		list = append(list, part)
	}
	return list
}

func validProxy(s string) bool {
	if strings.Contains(s, "/") {
		_, err := netip.ParsePrefix(s)
		return err == nil
	}
	addr, err := netip.ParseAddr(s)
	return err == nil && addr.Zone() == "" // the router does not understand IPv6 zones
}

// parsePublicURL checks the public address of the service and returns it as scheme://host[:port], without a
// trailing slash. Anything but a bare origin gives "" with a warning, which switches the owner sign-in off: the
// safe direction. Plain http is accepted only for a loopback host (local development): the session cookie of an
// http address cannot be Secure, so on the internet it would travel in clear text.
func parsePublicURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || !isOrigin(u) || !schemeAllowed(u) {
		slog.Warn("ignoring invalid PUBLIC_URL: use an https address without a path, e.g. https://example.com", "value", raw)
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// isOrigin reports whether u is a host (with an optional port) and nothing more: no credentials, path, query or fragment.
func isOrigin(u *url.URL) bool {
	return u.Host != "" && u.User == nil && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == ""
}

// schemeAllowed accepts https, and http only for a loopback host.
func schemeAllowed(u *url.URL) bool {
	return u.Scheme == "https" || (u.Scheme == "http" && isLoopback(u.Hostname()))
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

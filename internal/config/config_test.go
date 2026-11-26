package config

import (
	"os"
	"slices"
	"testing"
	"time"
)

func TestParseChatIDs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []int64
	}{
		{"empty", "", nil},
		{"single", "123", []int64{123}},
		{"mixed with spaces, group id and junk", "123, -456 ,abc,,789", []int64{123, -456, 789}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseChatIDs(tt.raw)
			if !slices.Equal(got, tt.want) {
				t.Errorf("parseChatIDs(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestLoadTelegramAccessSettings(t *testing.T) {
	t.Setenv("TELEGRAM_ADMIN_CHAT_IDS", "398427165, 111")
	t.Setenv("ACCESS_FILE", "/data/access.json")

	cfg := Load()

	if want := []int64{398427165, 111}; !slices.Equal(cfg.AdminChatIDs, want) {
		t.Errorf("AdminChatIDs = %v, want %v", cfg.AdminChatIDs, want)
	}
	if cfg.AccessFile != "/data/access.json" {
		t.Errorf("AccessFile = %q", cfg.AccessFile)
	}
}

func TestLoadTelegramAccessDefaults(t *testing.T) {
	t.Setenv("TELEGRAM_ADMIN_CHAT_IDS", "")
	t.Setenv("ACCESS_FILE", "")

	cfg := Load()

	if len(cfg.AdminChatIDs) != 0 || cfg.AccessFile != "" {
		t.Errorf("defaults = %v, %q", cfg.AdminChatIDs, cfg.AccessFile)
	}
}

func TestGetEnvDuration(t *testing.T) {
	const key = "TEST_STALE_AFTER"
	fallback := 5 * time.Minute

	t.Setenv(key, "90s")
	if got := getEnvDuration(key, fallback); got != 90*time.Second {
		t.Errorf("valid value: got %s", got)
	}

	t.Setenv(key, "not-a-duration")
	if got := getEnvDuration(key, fallback); got != fallback {
		t.Errorf("invalid value should fall back, got %s", got)
	}

	t.Setenv(key, "-1m")
	if got := getEnvDuration(key, fallback); got != fallback {
		t.Errorf("non-positive value should fall back, got %s", got)
	}
}

func TestLoadLogSettings(t *testing.T) {
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_FORMAT", "text")

	cfg := Load()

	if cfg.LogLevel != "debug" || cfg.LogFormat != "text" {
		t.Errorf("log settings = %q, %q", cfg.LogLevel, cfg.LogFormat)
	}
}

func TestLoadLogDefaults(t *testing.T) {
	// t.Setenv restores the original values after the test; Unsetenv makes the variables absent meanwhile.
	for _, key := range []string{"LOG_LEVEL", "LOG_FORMAT"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}

	cfg := Load()

	if cfg.LogLevel != "info" || cfg.LogFormat != "json" {
		t.Errorf("defaults = %q, %q; want info, json", cfg.LogLevel, cfg.LogFormat)
	}
}

func TestLoadHTTPAddr(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:9000")
	if got := Load().HTTPAddr; got != "127.0.0.1:9000" {
		t.Errorf("HTTPAddr = %q", got)
	}

	// An explicitly empty value disables the API; only an absent variable selects the default.
	t.Setenv("HTTP_ADDR", "")
	if got := Load().HTTPAddr; got != "" {
		t.Errorf("empty HTTP_ADDR must disable the API, got %q", got)
	}
}

func TestLoadHTTPAddrDefault(t *testing.T) {
	t.Setenv("HTTP_ADDR", "") // restored after the test; Unsetenv makes the variable absent meanwhile
	if err := os.Unsetenv("HTTP_ADDR"); err != nil {
		t.Fatal(err)
	}

	if got := Load().HTTPAddr; got != ":8080" {
		t.Errorf("default HTTPAddr = %q, want :8080", got)
	}
}

// unsetenv makes the variables absent for the test (t.Setenv restores the original values afterwards).
func unsetenv(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseTrustedProxies(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", nil},
		{"one range", "10.0.0.0/8", []string{"10.0.0.0/8"}},
		{"several, with spaces", "10.0.0.0/8, 172.16.0.0/12 ,192.168.0.0/16", []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}},
		{"single addresses", "203.0.113.5,2001:db8::1", []string{"203.0.113.5", "2001:db8::1"}},
		{"IPv6 range", "fd00::/8", []string{"fd00::/8"}},
		{"junk is skipped", "10.0.0.0/33, abc, ,10.0.0.1/8x,172.16.0.0/12,fe80::1%eth0", []string{"172.16.0.0/12"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseTrustedProxies(tt.raw); !slices.Equal(got, tt.want) {
				t.Errorf("parseTrustedProxies(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestLoadAPIProtection(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES", "172.16.0.0/12")
	t.Setenv("API_RATE_PER_MIN", " 120 ") // stray spaces are tolerated
	t.Setenv("API_RATE_BURST", "40")

	cfg := Load()

	if !slices.Equal(cfg.TrustedProxies, []string{"172.16.0.0/12"}) || cfg.APIRatePerMin != 120 || cfg.APIRateBurst != 40 {
		t.Errorf("got %v, %d, %d", cfg.TrustedProxies, cfg.APIRatePerMin, cfg.APIRateBurst)
	}
}

func TestLoadAPIProtectionDefaults(t *testing.T) {
	unsetenv(t, "TRUSTED_PROXIES", "API_RATE_PER_MIN", "API_RATE_BURST")

	cfg := Load()

	if len(cfg.TrustedProxies) != 0 || cfg.APIRatePerMin != 60 || cfg.APIRateBurst != 20 {
		t.Errorf("defaults = %v, %d, %d; want no proxies, 60, 20", cfg.TrustedProxies, cfg.APIRatePerMin, cfg.APIRateBurst)
	}
}

func TestLoadAPIRateLimitCanBeSwitchedOff(t *testing.T) {
	t.Setenv("API_RATE_PER_MIN", "0")

	if got := Load().APIRatePerMin; got != 0 {
		t.Errorf("API_RATE_PER_MIN=0 must switch the limit off, got %d", got)
	}
}

func TestLoadAPIRateLimitFallsBackOnBadValues(t *testing.T) {
	for _, bad := range []string{"abc", "-1", "1.5"} {
		t.Setenv("API_RATE_PER_MIN", bad)
		if got := Load().APIRatePerMin; got != 60 {
			t.Errorf("API_RATE_PER_MIN=%q: got %d, want the default 60", bad, got)
		}
	}
	for _, bad := range []string{"abc", "0", "-3"} {
		t.Setenv("API_RATE_BURST", bad)
		if got := Load().APIRateBurst; got != 20 {
			t.Errorf("API_RATE_BURST=%q: got %d, want the default 20", bad, got)
		}
	}
}

func TestLoadOwnerRateLimit(t *testing.T) {
	t.Setenv("API_OWNER_RATE_PER_MIN", " 300 ")
	t.Setenv("API_OWNER_RATE_BURST", "50")

	cfg := Load()

	if cfg.APIOwnerRatePerMin != 300 || cfg.APIOwnerRateBurst != 50 {
		t.Errorf("got %d, %d; want 300, 50", cfg.APIOwnerRatePerMin, cfg.APIOwnerRateBurst)
	}
}

func TestLoadOwnerRateLimitDefaultsToTenTimesTheDemoBudget(t *testing.T) {
	unsetenv(t, "API_OWNER_RATE_PER_MIN", "API_OWNER_RATE_BURST")

	cfg := Load()

	if cfg.APIOwnerRatePerMin != 600 || cfg.APIOwnerRateBurst != 100 {
		t.Errorf("defaults = %d, %d; want 600, 100", cfg.APIOwnerRatePerMin, cfg.APIOwnerRateBurst)
	}
}

func TestLoadOwnerRateLimitCanBeSwitchedOff(t *testing.T) {
	t.Setenv("API_OWNER_RATE_PER_MIN", "0")

	if got := Load().APIOwnerRatePerMin; got != 0 {
		t.Errorf("API_OWNER_RATE_PER_MIN=0 must give the owner the limit of everybody, got %d", got)
	}
}

func TestLoadOwnerRateLimitFallsBackOnBadValues(t *testing.T) {
	for _, bad := range []string{"abc", "-1", "1.5"} {
		t.Setenv("API_OWNER_RATE_PER_MIN", bad)
		if got := Load().APIOwnerRatePerMin; got != 600 {
			t.Errorf("API_OWNER_RATE_PER_MIN=%q: got %d, want the default 600", bad, got)
		}
	}
	for _, bad := range []string{"abc", "0", "-3"} {
		t.Setenv("API_OWNER_RATE_BURST", bad)
		if got := Load().APIOwnerRateBurst; got != 100 {
			t.Errorf("API_OWNER_RATE_BURST=%q: got %d, want the default 100", bad, got)
		}
	}
}

func TestParsePublicURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", ""},
		{"https origin", "https://34-28-22-93.sslip.io", "https://34-28-22-93.sslip.io"},
		{"trailing slash is cut", "https://climate.example.com/", "https://climate.example.com"},
		{"spaces are trimmed", "  https://climate.example.com  ", "https://climate.example.com"},
		{"port is kept", "https://climate.example.com:8443", "https://climate.example.com:8443"},
		{"http on localhost", "http://localhost:8080", "http://localhost:8080"},
		{"http on loopback IPv4", "http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"http on loopback IPv6", "http://[::1]:8080", "http://[::1]:8080"},
		{"http on a public host is refused", "http://climate.example.com", ""},
		{"http on a LAN address is refused", "http://192.168.1.10:8080", ""},
		{"no scheme", "climate.example.com", ""},
		{"host and port without a scheme", "localhost:8080", ""},
		{"other scheme", "ftp://climate.example.com", ""},
		{"a path", "https://climate.example.com/app", ""},
		{"a query", "https://climate.example.com/?a=1", ""},
		{"a fragment", "https://climate.example.com/#x", ""},
		{"credentials", "https://user:pass@climate.example.com", ""},
		{"no host", "https://", ""},
		{"not a URL", "https://exa mple.com", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsePublicURL(tt.raw); got != tt.want {
				t.Errorf("parsePublicURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestLoadOwnerSignInSettings(t *testing.T) {
	t.Setenv("PUBLIC_URL", "https://34-28-22-93.sslip.io/")
	t.Setenv("TELEGRAM_OIDC_CLIENT_ID", " 8835632852 ")
	t.Setenv("TELEGRAM_OIDC_CLIENT_SECRET", "s3cret\n") // a secret read from a file often ends with a newline
	t.Setenv("SESSION_TTL", "720h")

	cfg := Load()

	if cfg.PublicURL != "https://34-28-22-93.sslip.io" || cfg.TGLoginClientID != "8835632852" ||
		cfg.TGLoginClientSecret != "s3cret" || cfg.SessionTTL != 720*time.Hour {
		t.Errorf("got %q, %q, %q, %s", cfg.PublicURL, cfg.TGLoginClientID, cfg.TGLoginClientSecret, cfg.SessionTTL)
	}
}

func TestLoadOwnerSignInDefaults(t *testing.T) {
	unsetenv(t, "PUBLIC_URL", "TELEGRAM_OIDC_CLIENT_ID", "TELEGRAM_OIDC_CLIENT_SECRET", "SESSION_TTL")

	cfg := Load()

	if cfg.PublicURL != "" || cfg.TGLoginClientID != "" || cfg.TGLoginClientSecret != "" {
		t.Errorf("the sign-in must be unconfigured by default: %q, %q, %q", cfg.PublicURL, cfg.TGLoginClientID, cfg.TGLoginClientSecret)
	}
	if cfg.SessionTTL != 30*24*time.Hour {
		t.Errorf("SessionTTL = %s, want 30 days", cfg.SessionTTL)
	}
}

func TestLoadSessionTTLFallsBackOnBadValues(t *testing.T) {
	for _, bad := range []string{"abc", "0", "-1h", "30d"} { // Go durations have no "d"
		t.Setenv("SESSION_TTL", bad)
		if got := Load().SessionTTL; got != 30*24*time.Hour {
			t.Errorf("SESSION_TTL=%q: got %s, want the default 30 days", bad, got)
		}
	}
}

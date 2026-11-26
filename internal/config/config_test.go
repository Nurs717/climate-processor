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

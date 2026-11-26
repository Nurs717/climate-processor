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

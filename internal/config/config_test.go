package config

import (
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

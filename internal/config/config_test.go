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

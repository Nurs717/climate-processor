package rest

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"climate-processor/internal/apperr"
)

func TestParseDuration(t *testing.T) {
	valid := map[string]time.Duration{
		"30s":   30 * time.Second,
		"90m":   90 * time.Minute,
		"24h":   24 * time.Hour,
		"1h30m": 90 * time.Minute,
		"7d":    7 * 24 * time.Hour,
	}
	for in, want := range valid {
		if got, err := parseDuration(in); err != nil || got != want {
			t.Errorf("parseDuration(%q) = %s, %v; want %s", in, got, err, want)
		}
	}

	for _, in := range []string{"", "d", "0d", "-1d", "1.5d", "3651d", "0s", "-5m", "abc", "10"} {
		if got, err := parseDuration(in); err == nil {
			t.Errorf("parseDuration(%q) = %s, want an error", in, got)
		}
	}
}

func TestTimeRange(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 30, 500_000_000, time.UTC)
	at := func(h, m, s int) time.Time { return time.Date(2026, 11, 26, h, m, s, 0, time.UTC) }
	end := at(12, 0, 31) // "now" is the end of the current second: the fraction is cut off and one second is added

	tests := []struct {
		name     string
		query    url.Values
		wantFrom time.Time
		wantTo   time.Time
	}{
		{"defaults to the last 24 hours", url.Values{}, end.Add(-24 * time.Hour), end},
		{"range in hours", url.Values{"range": {"6h"}}, end.Add(-6 * time.Hour), end},
		{"range in days", url.Values{"range": {"7d"}}, end.Add(-7 * 24 * time.Hour), end},
		{"from only", url.Values{"from": {"2026-11-26T10:00:00Z"}}, at(10, 0, 0), end},
		{
			"from and to with an offset",
			url.Values{"from": {"2026-11-26T10:00:00+05:00"}, "to": {"2026-11-26T11:00:00+05:00"}},
			at(5, 0, 0), at(6, 0, 0),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			from, to, err := timeRange(tt.query, now, "test")
			if err != nil {
				t.Fatal(err)
			}
			if !from.Equal(tt.wantFrom) || !to.Equal(tt.wantTo) || from.Location() != time.UTC || to.Location() != time.UTC {
				t.Errorf("got %s .. %s, want %s .. %s (UTC)", from, to, tt.wantFrom, tt.wantTo)
			}
		})
	}
}

// Readings are stored with the whole second they arrived in and the repository query is half-open
// (from <= ts < to), so the interval must end after the current second: otherwise the reading that has
// just arrived is missing from the answer until the next second starts.
func TestTimeRangeIncludesTheCurrentSecond(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 30, 999_000_000, time.UTC)
	justArrived := now.Truncate(time.Second) // the timestamp stored for a reading received at "now"

	for name, query := range map[string]url.Values{
		"no parameters": {},
		"range":         {"range": {"1h"}},
		"from only":     {"from": {"2026-11-26T10:00:00Z"}},
	} {
		t.Run(name, func(t *testing.T) {
			from, to, err := timeRange(query, now, "test")
			if err != nil {
				t.Fatal(err)
			}
			if justArrived.Before(from) || !justArrived.Before(to) {
				t.Errorf("a reading stored at %s is outside %s <= ts < %s", justArrived, from, to)
			}
		})
	}
}

func TestTimeRangeRejectsBadInput(t *testing.T) {
	tests := []struct {
		name   string
		query  url.Values
		reason string // part of the text shown to the client
	}{
		{"range together with from", url.Values{"range": {"1h"}, "from": {"2026-11-26T10:00:00Z"}}, "not both"},
		{"range together with to", url.Values{"range": {"1h"}, "to": {"2026-11-26T10:00:00Z"}}, "not both"},
		{"bad range", url.Values{"range": {"abc"}}, `"range"`},
		{"negative range", url.Values{"range": {"-1h"}}, `"range"`},
		{"bad from", url.Values{"from": {"yesterday"}}, `"from"`},
		{"bad to", url.Values{"from": {"2026-11-26T10:00:00Z"}, "to": {"tomorrow"}}, `"to"`},
		{"to without from", url.Values{"to": {"2026-11-26T10:00:00Z"}}, `"from" is required`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := timeRange(tt.query, time.Now(), "test")

			if apperr.CodeOf(err) != apperr.CodeBadRequest {
				t.Fatalf("code = %v, want bad_request (err: %v)", apperr.CodeOf(err), err)
			}
			if got := apperr.ReasonOf(err); !strings.Contains(got, tt.reason) {
				t.Errorf("reason = %q, want it to contain %q", got, tt.reason)
			}
		})
	}
}

func TestParseStep(t *testing.T) {
	if got, err := parseStep(url.Values{}, "test"); err != nil || got != 0 {
		t.Errorf("no step: got %s, %v; want 0 (the server chooses)", got, err)
	}
	if got, err := parseStep(url.Values{"step": {"5m"}}, "test"); err != nil || got != 5*time.Minute {
		t.Errorf("step=5m: got %s, %v", got, err)
	}
	for _, bad := range []string{"fast", "0s", "-1m", "1.5d"} {
		_, err := parseStep(url.Values{"step": {bad}}, "test")
		if apperr.CodeOf(err) != apperr.CodeBadRequest || !strings.Contains(apperr.ReasonOf(err), `"step"`) {
			t.Errorf("step=%q: err = %v, want a bad_request error about the step", bad, err)
		}
	}
}

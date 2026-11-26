package rest

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRange = 24 * time.Hour
	maxDays      = 3650 // keeps "<n>d" far away from overflowing time.Duration
)

// parseDuration accepts Go durations ("90m", "24h") and whole days ("7d").
func parseDuration(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 || n > maxDays {
			return 0, errors.New("invalid number of days")
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, errors.New("invalid duration")
	}
	return d, nil
}

// timeRange reads the interval a request asks for: range=24h (the last 24 hours up to now), or
// from=<RFC 3339> with an optional to=<RFC 3339> (default: now). Without parameters it is the last 24 hours.
// The result is in UTC. "Now" means the end of the current second: readings are stored with whole-second
// timestamps and the interval is half-open, so this way a reading that has just arrived is included.
// As a consequence the reported "to" can be up to a second ahead of the clock.
func timeRange(q url.Values, now time.Time, op string) (from, to time.Time, err error) {
	end := now.UTC().Truncate(time.Second).Add(time.Second)
	rng, fromRaw, toRaw := q.Get("range"), q.Get("from"), q.Get("to")

	switch {
	case rng != "" && (fromRaw != "" || toRaw != ""):
		return from, to, badRequest(op, `use either "range" or "from"/"to", not both`)
	case rng != "":
		d, parseErr := parseDuration(rng)
		if parseErr != nil {
			return from, to, badRequest(op, `invalid "range": use a duration such as 6h, 24h or 7d`)
		}
		return end.Add(-d), end, nil
	case fromRaw == "" && toRaw == "":
		return end.Add(-defaultRange), end, nil
	case fromRaw == "":
		return from, to, badRequest(op, `"from" is required when "to" is given`)
	}

	from, err = time.Parse(time.RFC3339, fromRaw)
	if err != nil {
		return from, to, badRequest(op, `invalid "from": use RFC 3339, e.g. 2026-11-26T10:00:00Z`)
	}
	to = end
	if toRaw != "" {
		if to, err = time.Parse(time.RFC3339, toRaw); err != nil {
			return from, to, badRequest(op, `invalid "to": use RFC 3339, e.g. 2026-11-26T12:00:00Z`)
		}
	}
	return from.UTC(), to.UTC(), nil
}

// parseStep reads the optional step parameter ("30s", "5m", "1h"). Zero means "choose a sensible step".
func parseStep(q url.Values, op string) (time.Duration, error) {
	raw := q.Get("step")
	if raw == "" {
		return 0, nil
	}
	d, err := parseDuration(raw)
	if err != nil {
		return 0, badRequest(op, `invalid "step": use a duration such as 30s, 5m or 1h`)
	}
	return d, nil
}

package rest

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"climate-processor/internal/domain"
	"climate-processor/internal/service"
)

func TestNewLatestCutsTheTimeToWholeSecondsInUTC(t *testing.T) {
	almaty := time.FixedZone("UTC+5", 5*60*60)
	received := time.Date(2026, 11, 26, 21, 40, 6, 470_753_552, almaty)

	got := newLatest(service.LatestReading{ClimateData: domain.ClimateData{Timestamp: received}})

	if want := time.Date(2026, 11, 26, 16, 40, 6, 0, time.UTC); !got.Time.Equal(want) || got.Time.Location() != time.UTC {
		t.Errorf("time = %s, want %s (UTC)", got.Time, want)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"time":"2026-11-26T16:40:06Z"`) {
		t.Errorf("the JSON time must have no fraction: %s", body)
	}
}

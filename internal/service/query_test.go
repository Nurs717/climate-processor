package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"climate-processor/internal/apperr"
	"climate-processor/internal/domain"
)

var queryFrom = time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)

func TestHistoryPassesArgumentsToTheRepository(t *testing.T) {
	repo := &fakeRepo{buckets: []domain.Bucket{{Start: queryFrom, Summary: domain.Summary{Count: 12}}}}
	s, _ := newTestServiceWithRepo(t, repo)
	to := queryFrom.Add(time.Hour)

	got, err := s.History(context.Background(), queryFrom, to, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Count != 12 {
		t.Errorf("buckets = %+v, want the repository's result", got)
	}
	want := historyCall{deviceID: "room", from: queryFrom, to: to, step: time.Minute}
	if repo.calls != 1 || repo.call != want {
		t.Errorf("repository call = %+v (calls: %d), want %+v", repo.call, repo.calls, want)
	}
}

func TestHistoryChoosesStepWhenNoneIsGiven(t *testing.T) {
	repo := &fakeRepo{}
	s, _ := newTestServiceWithRepo(t, repo)

	if _, err := s.History(context.Background(), queryFrom, queryFrom.Add(24*time.Hour), 0); err != nil {
		t.Fatal(err)
	}
	if repo.call.step != 5*time.Minute {
		t.Errorf("step = %s, want 5m0s for a 24h range", repo.call.step)
	}
}

func TestHistoryRejectsInvalidArguments(t *testing.T) {
	const day = 24 * time.Hour
	tests := []struct {
		name   string
		from   time.Time
		to     time.Time
		step   time.Duration
		reason string // part of the text shown to the client
	}{
		{"empty range", queryFrom, queryFrom, time.Minute, "from must be before to"},
		{"reversed range", queryFrom.Add(time.Hour), queryFrom, time.Minute, "from must be before to"},
		{"range too long", queryFrom, queryFrom.Add(91 * day), time.Hour, "90 days"},
		{"step below one second", queryFrom, queryFrom.Add(time.Hour), 500 * time.Millisecond, "whole number of seconds"},
		{"fractional step", queryFrom, queryFrom.Add(time.Hour), 1500 * time.Millisecond, "whole number of seconds"},
		{"negative step", queryFrom, queryFrom.Add(time.Hour), -time.Minute, "whole number of seconds"},
		{"too many points", queryFrom, queryFrom.Add(day), time.Second, "at least 44s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepo{}
			s, _ := newTestServiceWithRepo(t, repo)

			_, err := s.History(context.Background(), tt.from, tt.to, tt.step)

			if apperr.CodeOf(err) != apperr.CodeBadRequest {
				t.Fatalf("code = %v, want bad_request (err: %v)", apperr.CodeOf(err), err)
			}
			if got := apperr.ReasonOf(err); !strings.Contains(got, tt.reason) {
				t.Errorf("reason = %q, want it to contain %q", got, tt.reason)
			}
			if repo.calls != 0 {
				t.Error("an invalid query must not reach the repository")
			}
		})
	}
}

func TestHistoryAndSummaryTurnRepositoryFailuresIntoUnavailable(t *testing.T) {
	boom := errors.New("disk I/O error")
	failures := []struct {
		name   string
		err    error
		reason string
	}{
		{"database error", boom, "history is temporarily unavailable"},
		{"timeout", context.DeadlineExceeded, "history query timed out"},
		{"history disabled", ErrHistoryDisabled, "history is not enabled"},
	}
	queries := map[string]func(s *ClimateService) error{
		"History": func(s *ClimateService) error {
			_, err := s.History(context.Background(), queryFrom, queryFrom.Add(time.Hour), 0)
			return err
		},
		"Summary": func(s *ClimateService) error {
			_, err := s.Summary(context.Background(), queryFrom, queryFrom.Add(time.Hour))
			return err
		},
	}
	for name, query := range queries {
		for _, f := range failures {
			t.Run(name+"/"+f.name, func(t *testing.T) {
				s, _ := newTestServiceWithRepo(t, &fakeRepo{err: f.err})

				err := query(s)

				if apperr.CodeOf(err) != apperr.CodeUnavailable {
					t.Fatalf("code = %v, want unavailable (err: %v)", apperr.CodeOf(err), err)
				}
				if got := apperr.ReasonOf(err); got != f.reason {
					t.Errorf("reason = %q, want %q", got, f.reason)
				}
				if !errors.Is(err, f.err) {
					t.Errorf("the cause must stay in the error chain for the logs: %v", err)
				}
			})
		}
	}
}

func TestHistoryWithoutDatabaseSaysSo(t *testing.T) {
	s, _ := newTestService(t) // NopRepository

	_, err := s.History(context.Background(), queryFrom, queryFrom.Add(time.Hour), 0)

	if got := apperr.ReasonOf(err); got != "history is not enabled" {
		t.Errorf("reason = %q, err: %v", got, err)
	}
}

func TestSummaryPassesArgumentsToTheRepository(t *testing.T) {
	repo := &fakeRepo{summary: domain.Summary{Count: 3, Temperature: domain.Stat{Avg: 21, Min: 20, Max: 22}}}
	s, _ := newTestServiceWithRepo(t, repo)
	to := queryFrom.Add(time.Hour)

	got, err := s.Summary(context.Background(), queryFrom, to)
	if err != nil {
		t.Fatal(err)
	}
	if got != repo.summary {
		t.Errorf("summary = %+v, want %+v", got, repo.summary)
	}
	if want := (historyCall{deviceID: "room", from: queryFrom, to: to}); repo.call != want {
		t.Errorf("repository call = %+v, want %+v", repo.call, want)
	}
}

func TestSummaryRejectsInvalidRange(t *testing.T) {
	repo := &fakeRepo{}
	s, _ := newTestServiceWithRepo(t, repo)

	_, err := s.Summary(context.Background(), queryFrom, queryFrom.Add(-time.Hour))

	if apperr.CodeOf(err) != apperr.CodeBadRequest || repo.calls != 0 {
		t.Errorf("code = %v, repository calls = %d, err: %v", apperr.CodeOf(err), repo.calls, err)
	}
}

func TestDefaultStep(t *testing.T) {
	const day = 24 * time.Hour
	tests := []struct {
		span time.Duration
		want time.Duration
	}{
		{time.Second, 5 * time.Second},
		{5 * time.Minute, 5 * time.Second},
		{time.Hour, 10 * time.Second},
		{day, 5 * time.Minute},
		{7 * day, 30 * time.Minute},
		{30 * day, 3 * time.Hour},
		{90 * day, 6 * time.Hour},
		{1000 * day, 24 * time.Hour}, // beyond every window: the largest step
	}
	for _, tt := range tests {
		if got := DefaultStep(tt.span); got != tt.want {
			t.Errorf("DefaultStep(%s) = %s, want %s", tt.span, got, tt.want)
		}
	}
}

// Whatever range a client asks for without a step, the default must be accepted by History.
func TestDefaultStepStaysWithinTheLimit(t *testing.T) {
	for span := time.Second; span <= maxHistoryRange; span = span*3/2 + time.Second {
		step := DefaultStep(span)
		if step < time.Second || step%time.Second != 0 || span/step > maxHistoryPoints {
			t.Fatalf("DefaultStep(%s) = %s is not usable (%d points)", span, step, span/step)
		}
	}
}

func TestLatest(t *testing.T) {
	s, now := newTestService(t)

	if _, ok := s.Latest(); ok {
		t.Fatal("there must be no reading before the first message")
	}

	if err := s.ProcessMessage([]byte(validPayload)); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Latest()
	if !ok || got.Temperature != 21.5 || got.DeviceID != "room" || got.Age != 0 || got.Stale {
		t.Fatalf("fresh reading: %+v, ok = %v", got, ok)
	}

	*now = now.Add(6 * time.Minute)
	got, _ = s.Latest()
	if !got.Stale || got.Age != 6*time.Minute {
		t.Errorf("after 6 minutes: stale = %v, age = %s", got.Stale, got.Age)
	}
}

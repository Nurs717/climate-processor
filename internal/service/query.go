package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"climate-processor/internal/apperr"
	"climate-processor/internal/domain"
)

// Limits of the history queries. They protect the database (and the 1 GB VM) from one expensive request.
const (
	maxHistoryRange  = 90 * 24 * time.Hour // ~1.5M readings at one per 5s; longer ranges need downsampled storage
	maxHistoryPoints = 2000                // windows in one response
	targetPoints     = 400                 // what DefaultStep aims for: enough for a chart, small for a phone
	queryTimeout     = 5 * time.Second     // upper bound for one history query
)

// niceSteps are the window sizes DefaultStep chooses from.
var niceSteps = []time.Duration{
	5 * time.Second, 10 * time.Second, 30 * time.Second,
	time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

// DefaultStep returns the smallest round window size that keeps a series of the given span within targetPoints.
func DefaultStep(span time.Duration) time.Duration {
	for _, step := range niceSteps {
		if span/step <= targetPoints {
			return step
		}
	}
	return niceSteps[len(niceSteps)-1]
}

// LatestReading is the newest accepted reading and how fresh it is.
type LatestReading struct {
	domain.ClimateData
	Age   time.Duration // time since the reading was received
	Stale bool          // Age exceeds the configured threshold
}

// Latest returns the newest reading received since the service started. ok is false before the first one.
func (s *ClimateService) Latest() (latest LatestReading, ok bool) {
	s.mu.RLock()
	data, hasData := s.currentData, s.hasData
	s.mu.RUnlock()

	if !hasData {
		return LatestReading{}, false
	}
	age := s.now().Sub(data.Timestamp)
	return LatestReading{ClimateData: data, Age: age, Stale: age > s.staleAfter}, true
}

// History returns the readings with from <= time < to grouped into windows of step. A zero step selects
// DefaultStep. Windows are aligned to the Unix epoch (so the first one may start before from) and windows
// without readings are omitted. Invalid arguments yield an apperr.CodeBadRequest error.
func (s *ClimateService) History(ctx context.Context, from, to time.Time, step time.Duration) ([]domain.Bucket, error) {
	const op = "service.History"
	if err := validateRange(op, from, to); err != nil {
		return nil, err
	}
	span := to.Sub(from)
	if step == 0 {
		step = DefaultStep(span)
	}
	if step < time.Second || step%time.Second != 0 {
		return nil, apperr.New(apperr.CodeBadRequest, op, "step must be a whole number of seconds, at least 1s")
	}
	if span/step > maxHistoryPoints {
		return nil, apperr.New(apperr.CodeBadRequest, op, fmt.Sprintf(
			"step is too small for this range: at most %d points, use a step of at least %ds",
			maxHistoryPoints, int64(minStep(span)/time.Second)))
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	buckets, err := s.repo.Aggregate(ctx, s.deviceID, from, to, step)
	if err != nil {
		return nil, historyError(op, err)
	}
	return buckets, nil
}

// Summary aggregates the readings with from <= time < to. An empty interval gives Count == 0.
func (s *ClimateService) Summary(ctx context.Context, from, to time.Time) (domain.Summary, error) {
	const op = "service.Summary"
	if err := validateRange(op, from, to); err != nil {
		return domain.Summary{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	sum, err := s.repo.Summary(ctx, s.deviceID, from, to)
	if err != nil {
		return domain.Summary{}, historyError(op, err)
	}
	return sum, nil
}

func validateRange(op string, from, to time.Time) error {
	switch {
	case !from.Before(to):
		return apperr.New(apperr.CodeBadRequest, op, "from must be before to")
	case to.Sub(from) > maxHistoryRange:
		return apperr.New(apperr.CodeBadRequest, op,
			fmt.Sprintf("the range must not exceed %d days", int(maxHistoryRange.Hours()/24)))
	}
	return nil
}

// minStep is the smallest whole-second step that keeps a series of the given span within maxHistoryPoints.
func minStep(span time.Duration) time.Duration {
	seconds := int64(span / time.Second)
	return time.Duration((seconds+maxHistoryPoints-1)/maxHistoryPoints) * time.Second
}

// historyError turns a repository failure into a "service unavailable" error; the cause stays in the chain for logs.
func historyError(op string, err error) error {
	reason := "history is temporarily unavailable"
	switch {
	case errors.Is(err, ErrHistoryDisabled):
		reason = "history is not enabled"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "history query timed out"
	}
	return apperr.Wrap(err, apperr.CodeUnavailable, op, reason)
}

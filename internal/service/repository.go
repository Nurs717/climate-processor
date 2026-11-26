package service

import (
	"context"
	"errors"
	"time"

	"climate-processor/internal/domain"
)

// ErrHistoryDisabled is returned by NopRepository: no database is configured.
var ErrHistoryDisabled = errors.New("history is not enabled")

// ReadingRepository stores readings and answers history queries. It is declared here, next to its
// consumer, so the service depends on behaviour and not on a concrete database.
// Every query uses the half-open interval from <= time < to.
type ReadingRepository interface {
	Save(ctx context.Context, d domain.ClimateData) error

	// Aggregate groups the readings of a device into windows of step (whole seconds, at least 1s),
	// aligned to the Unix epoch, oldest first. Windows without readings are omitted.
	Aggregate(ctx context.Context, deviceID string, from, to time.Time, step time.Duration) ([]domain.Bucket, error)

	// Summary aggregates all readings of a device in the interval.
	Summary(ctx context.Context, deviceID string, from, to time.Time) (domain.Summary, error)
}

// NopRepository drops readings and has no history. It is used when no database is configured.
type NopRepository struct{}

func (NopRepository) Save(context.Context, domain.ClimateData) error { return nil }

func (NopRepository) Aggregate(context.Context, string, time.Time, time.Time, time.Duration) ([]domain.Bucket, error) {
	return nil, ErrHistoryDisabled
}

func (NopRepository) Summary(context.Context, string, time.Time, time.Time) (domain.Summary, error) {
	return domain.Summary{}, ErrHistoryDisabled
}

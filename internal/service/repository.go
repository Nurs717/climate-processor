package service

import (
	"context"

	"climate-processor/internal/domain"
)

// ReadingRepository persists accepted readings. It is declared here, next to its consumer,
// so the service depends on behaviour and not on a concrete database.
// Range(...) will be added when the bot needs history (/stats, /chart).
type ReadingRepository interface {
	Save(ctx context.Context, d domain.ClimateData) error
}

// NopRepository drops readings. It is used when no database is configured.
type NopRepository struct{}

func (NopRepository) Save(context.Context, domain.ClimateData) error { return nil }

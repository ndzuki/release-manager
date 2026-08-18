package sqlite

import (
	"context"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

var errPostgresCleanupIdempotencyRequired = store.ErrCleanupIdempotencyUnavailable

type unsupportedCleanupIdempotencyStore struct{}

func (unsupportedCleanupIdempotencyStore) TryCreate(context.Context, string, time.Duration) error {
	return errPostgresCleanupIdempotencyRequired
}

func (unsupportedCleanupIdempotencyStore) DeleteExpiredBefore(context.Context, time.Time, int) (int64, error) {
	return 0, errPostgresCleanupIdempotencyRequired
}

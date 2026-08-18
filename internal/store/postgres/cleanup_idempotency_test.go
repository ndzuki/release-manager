package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanupIdempotencyTryCreate(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectExec(`INSERT INTO cleanup_idempotency`).
		WithArgs("cleanup-request", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	cleanupStore := &cleanupIdempotencyStore{db: db}
	require.NoError(t, cleanupStore.TryCreate(context.Background(), "cleanup-request", 24*time.Hour))

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCleanupIdempotencyTryCreateMapsUniqueViolation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectExec(`INSERT INTO cleanup_idempotency`).
		WithArgs("cleanup-request", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnError(&pgconn.PgError{Code: "23505"})

	cleanupStore := &cleanupIdempotencyStore{db: db}
	err = cleanupStore.TryCreate(context.Background(), "cleanup-request", 24*time.Hour)
	assert.ErrorIs(t, err, store.ErrCleanupAlreadyRequested)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCleanupIdempotencyDeleteExpiredBeforeCapsBatchAtOneHundred(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	cutoff := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.FixedZone("test", 2*60*60))
	mock.ExpectExec(`(?s)WITH expired AS .*DELETE FROM cleanup_idempotency WHERE ctid IN \(SELECT ctid FROM expired\)`).
		WithArgs(cutoff.UTC(), cleanupIdempotencyBatchLimit).
		WillReturnResult(sqlmock.NewResult(0, cleanupIdempotencyBatchLimit))

	cleanupStore := &cleanupIdempotencyStore{db: db}
	deleted, err := cleanupStore.DeleteExpiredBefore(context.Background(), cutoff, 500)
	require.NoError(t, err)
	assert.Equal(t, int64(cleanupIdempotencyBatchLimit), deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCleanupIdempotencyDeleteExpiredBeforeChecksRowsAffected(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectExec(`WITH expired AS`).
		WillReturnError(errors.New("database unavailable"))

	cleanupStore := &cleanupIdempotencyStore{db: db}
	_, err = cleanupStore.DeleteExpiredBefore(context.Background(), time.Now(), cleanupIdempotencyBatchLimit)
	assert.ErrorContains(t, err, "delete expired cleanup idempotency keys")
	require.NoError(t, mock.ExpectationsWereMet())
}

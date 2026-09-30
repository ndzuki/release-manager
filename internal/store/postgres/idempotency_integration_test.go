//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TASK-233: the PostgreSQL half of the bounded collector. The statement deletes through the
// primary-key tuple, so it works without PostgreSQL-only row identity.
func TestIdempotencyDeleteExpiredIsBounded(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, st.GORM().Exec(`
		INSERT INTO idempotency_records (scope, text_key, request_hash, response_ref, expires_at)
		SELECT 'bounded', 'expired-' || g, 'hash', ?, ?
		FROM generate_series(1, 250) AS g
	`, []byte("{}"), now.Add(-time.Hour)).Error)
	require.NoError(t, st.GORM().Exec(`
		INSERT INTO idempotency_records (scope, text_key, request_hash, response_ref, expires_at)
		VALUES ('bounded', 'live', 'hash-live', ?, ?)
	`, []byte("{}"), now.Add(time.Hour)).Error)

	for batch, want := range []int64{100, 100, 50, 0} {
		deleted, err := st.Idempotency().DeleteExpired(ctx, now, 100)
		require.NoError(t, err)
		assert.Equal(t, want, deleted, "batch %d must delete at most one batch's worth", batch+1)
	}

	var remaining int64
	require.NoError(t, st.GORM().Raw(`SELECT COUNT(*) FROM idempotency_records`).Scan(&remaining).Error)
	assert.EqualValues(t, 1, remaining, "the unexpired record survives")
}

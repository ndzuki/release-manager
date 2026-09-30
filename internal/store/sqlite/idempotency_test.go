package sqlite

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// These tests drive createOrGetIdempotencyRecord, the function the operation
// creation unit of work and CreateIdempotent run inside their transactions
// (uow.go, operations.go, values_approval.go). The exported
// IdempotencyStore.CreateOrGet wrapper had no shipping caller (TASK-226).
func TestIdempotencyStore_CreateOrGet(t *testing.T) {
	st := OpenTest(t)
	ctx := t.Context()
	now := time.Now().UTC()

	first := &store.IdempotencyRecord{
		Scope: "user-1:/test.Service/Create", Key: "idem-key-1", RequestHash: "abc123",
		ResponseRef: []byte(`{"operation_id":"op-001"}`), ExpiresAt: now.Add(time.Hour),
	}
	createdRecord, created, err := createOrGetIdempotencyRecord(ctx, st.DB(), first, now)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, first.Scope, createdRecord.Scope)
	assert.Equal(t, first.ResponseRef, createdRecord.ResponseRef)

	replay, created, err := createOrGetIdempotencyRecord(ctx, st.DB(), &store.IdempotencyRecord{
		Scope: first.Scope, Key: first.Key, RequestHash: first.RequestHash,
		ResponseRef: []byte(`{"operation_id":"op-002"}`), ExpiresAt: now.Add(2 * time.Hour),
	}, now)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, []byte(`{"operation_id":"op-001"}`), []byte(replay.ResponseRef))

	_, created, err = createOrGetIdempotencyRecord(ctx, st.DB(), &store.IdempotencyRecord{
		Scope: first.Scope, Key: first.Key, RequestHash: "different",
		ExpiresAt: now.Add(time.Hour),
	}, now)
	assert.False(t, created)
	assert.ErrorIs(t, err, store.ErrIdempotencyConflict)

	otherScope, created, err := createOrGetIdempotencyRecord(ctx, st.DB(), &store.IdempotencyRecord{
		Scope: "user-2:/test.Service/Create", Key: first.Key, RequestHash: first.RequestHash,
		ExpiresAt: now.Add(time.Hour),
	}, now)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "user-2:/test.Service/Create", otherScope.Scope)
}

func TestIdempotencyStore_ExpiredRecordsCanBeReplacedAndPurged(t *testing.T) {
	st := OpenTest(t)
	ctx := t.Context()
	idem := st.Idempotency()
	now := time.Now().UTC()

	_, _, err := createOrGetIdempotencyRecord(ctx, st.DB(), &store.IdempotencyRecord{
		Scope: "scope", Key: "expired", RequestHash: "old", ExpiresAt: now.Add(-time.Hour),
	}, now)
	require.NoError(t, err)

	replacement, created, err := createOrGetIdempotencyRecord(ctx, st.DB(), &store.IdempotencyRecord{
		Scope: "scope", Key: "expired", RequestHash: "new", ExpiresAt: now.Add(time.Hour),
	}, now)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "new", replacement.RequestHash)

	_, _, err = createOrGetIdempotencyRecord(ctx, st.DB(), &store.IdempotencyRecord{
		Scope: "scope", Key: "purge", RequestHash: "old", ExpiresAt: now.Add(-time.Minute),
	}, now)
	require.NoError(t, err)

	deleted, err := idem.DeleteExpired(ctx, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)

	// The purge removes exactly the expired row; the replaced, still-live record survives.
	var remaining int
	require.NoError(t, st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM idempotency_records`).Scan(&remaining))
	assert.Equal(t, 1, remaining)
}

// TASK-233: the collector is bounded. The garbage collector loops until a batch comes back
// short, so a first run over an accumulated backlog makes progress in fixed-size transactions
// instead of one long statement that a timeout would roll back entirely.
func TestIdempotencyDeleteExpiredIsBounded(t *testing.T) {
	st := OpenTest(t)
	ctx := t.Context()
	now := time.Now().UTC()

	placeholders := make([]string, 0, 251)
	args := make([]any, 0, 251*5)
	for i := range 250 {
		placeholders = append(placeholders, "(?, ?, ?, ?, ?)")
		args = append(args, "bounded", fmt.Sprintf("expired-%03d", i), "hash", []byte(`{}`),
			now.Add(-time.Hour).Format(time.RFC3339Nano))
	}
	placeholders = append(placeholders, "(?, ?, ?, ?, ?)")
	args = append(args, "bounded", "live", "hash-live", []byte(`{}`), now.Add(time.Hour).Format(time.RFC3339Nano))
	_, err := st.DB().ExecContext(ctx,
		`INSERT INTO idempotency_records (scope, text_key, request_hash, response_ref, expires_at) VALUES `+
			strings.Join(placeholders, ", "), args...)
	require.NoError(t, err)

	for batch, want := range []int64{100, 100, 50, 0} {
		deleted, err := st.Idempotency().DeleteExpired(ctx, now, 100)
		require.NoError(t, err)
		assert.Equal(t, want, deleted, "batch %d must delete at most one batch's worth", batch+1)
	}

	var remaining int
	require.NoError(t, st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM idempotency_records`).Scan(&remaining))
	assert.Equal(t, 1, remaining, "the unexpired record survives")
}

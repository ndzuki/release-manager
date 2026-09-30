package sqlite

import (
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

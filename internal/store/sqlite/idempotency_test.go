package sqlite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestIdempotencyStore_CreateOrGet(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	ctx := t.Context()
	idem := st.Idempotency()
	now := time.Now().UTC()

	first := &store.IdempotencyRecord{
		Scope: "user-1:/test.Service/Create", Key: "idem-key-1", RequestHash: "abc123",
		ResponseRef: []byte(`{"operation_id":"op-001"}`), ExpiresAt: now.Add(time.Hour),
	}
	createdRecord, created, err := idem.CreateOrGet(ctx, first)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, first.Scope, createdRecord.Scope)
	assert.Equal(t, first.ResponseRef, createdRecord.ResponseRef)

	replay, created, err := idem.CreateOrGet(ctx, &store.IdempotencyRecord{
		Scope: first.Scope, Key: first.Key, RequestHash: first.RequestHash,
		ResponseRef: []byte(`{"operation_id":"op-002"}`), ExpiresAt: now.Add(2 * time.Hour),
	})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, []byte(`{"operation_id":"op-001"}`), []byte(replay.ResponseRef))

	_, created, err = idem.CreateOrGet(ctx, &store.IdempotencyRecord{
		Scope: first.Scope, Key: first.Key, RequestHash: "different",
		ExpiresAt: now.Add(time.Hour),
	})
	assert.False(t, created)
	assert.ErrorIs(t, err, store.ErrIdempotencyConflict)

	otherScope, created, err := idem.CreateOrGet(ctx, &store.IdempotencyRecord{
		Scope: "user-2:/test.Service/Create", Key: first.Key, RequestHash: first.RequestHash,
		ExpiresAt: now.Add(time.Hour),
	})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "user-2:/test.Service/Create", otherScope.Scope)
}

func TestIdempotencyStore_ExpiredRecordsCanBeReplacedAndPurged(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	ctx := t.Context()
	idem := st.Idempotency()
	now := time.Now().UTC()

	_, _, err := idem.CreateOrGet(ctx, &store.IdempotencyRecord{
		Scope: "scope", Key: "expired", RequestHash: "old", ExpiresAt: now.Add(-time.Hour),
	})
	require.NoError(t, err)

	replacement, created, err := idem.CreateOrGet(ctx, &store.IdempotencyRecord{
		Scope: "scope", Key: "expired", RequestHash: "new", ExpiresAt: now.Add(time.Hour),
	})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "new", replacement.RequestHash)

	_, _, err = idem.CreateOrGet(ctx, &store.IdempotencyRecord{
		Scope: "scope", Key: "purge", RequestHash: "old", ExpiresAt: now.Add(-time.Minute),
	})
	require.NoError(t, err)
	expired, err := idem.GetExpired(ctx, now, 10)
	require.NoError(t, err)
	require.Len(t, expired, 1)
	assert.Equal(t, "purge", expired[0].Key)

	deleted, err := idem.DeleteExpired(ctx, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)
}

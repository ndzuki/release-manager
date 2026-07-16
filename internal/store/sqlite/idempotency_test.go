package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

func TestIdempotencyStore_CreateOrGet(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	idem := st.Idempotency()

	record := &store.IdempotencyRecord{
		ID:          "idem-001",
		Scope:       "user-1:/test.Service/Create",
		Key:         "idem-key-1",
		RequestHash: "abc123",
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(1 * time.Hour),
	}

	t.Run("creates new record", func(t *testing.T) {
		got, created, err := idem.CreateOrGet(ctx, record)
		require.NoError(t, err)
		assert.True(t, created)
		assert.Equal(t, record.ID, got.ID)
		assert.Equal(t, record.Scope, got.Scope)
		assert.Equal(t, record.Key, got.Key)
	})

	t.Run("returns existing on same scope+key+hash", func(t *testing.T) {
		dup := &store.IdempotencyRecord{
			ID:          "idem-002",
			Scope:       "user-1:/test.Service/Create",
			Key:         "idem-key-1",
			RequestHash: "abc123",
			CreatedAt:   time.Now().UTC(),
			ExpiresAt:   time.Now().UTC().Add(1 * time.Hour),
		}

		got, created, err := idem.CreateOrGet(ctx, dup)
		require.NoError(t, err)
		assert.False(t, created)
		assert.Equal(t, "idem-001", got.ID)
	})

	t.Run("returns conflict on same scope+key but different hash", func(t *testing.T) {
		conflict := &store.IdempotencyRecord{
			ID:          "idem-003",
			Scope:       "user-1:/test.Service/Create",
			Key:         "idem-key-1",
			RequestHash: "xyz789",
			CreatedAt:   time.Now().UTC(),
			ExpiresAt:   time.Now().UTC().Add(1 * time.Hour),
		}

		_, created, err := idem.CreateOrGet(ctx, conflict)
		require.Error(t, err)
		assert.False(t, created)
		assert.ErrorIs(t, err, store.ErrIdempotencyConflict)
	})

	t.Run("different scope — no conflict", func(t *testing.T) {
		diff := &store.IdempotencyRecord{
			ID:          "idem-004",
			Scope:       "user-2:/test.Service/Create",
			Key:         "idem-key-1",
			RequestHash: "abc123",
			CreatedAt:   time.Now().UTC(),
			ExpiresAt:   time.Now().UTC().Add(1 * time.Hour),
		}

		got, created, err := idem.CreateOrGet(ctx, diff)
		require.NoError(t, err)
		assert.True(t, created)
		assert.Equal(t, "idem-004", got.ID)
	})
}

func TestIdempotencyStore_DeleteExpired(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	idem := st.Idempotency()

	expired1 := &store.IdempotencyRecord{
		ID:          "exp-1",
		Scope:       "s",
		Key:         "k1",
		RequestHash: "h",
		CreatedAt:   time.Now().UTC().Add(-2 * time.Hour),
		ExpiresAt:   time.Now().UTC().Add(-1 * time.Hour),
	}
	expired2 := &store.IdempotencyRecord{
		ID:          "exp-2",
		Scope:       "s",
		Key:         "k2",
		RequestHash: "h",
		CreatedAt:   time.Now().UTC().Add(-2 * time.Hour),
		ExpiresAt:   time.Now().UTC().Add(-30 * time.Minute),
	}
	active := &store.IdempotencyRecord{
		ID:          "act-1",
		Scope:       "s",
		Key:         "k3",
		RequestHash: "h",
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(1 * time.Hour),
	}

	_, _, err := idem.CreateOrGet(ctx, expired1)
	require.NoError(t, err)
	_, _, err = idem.CreateOrGet(ctx, expired2)
	require.NoError(t, err)
	_, _, err = idem.CreateOrGet(ctx, active)
	require.NoError(t, err)

	t.Run("deletes only expired", func(t *testing.T) {
		n, err := idem.DeleteExpired(ctx, time.Now().UTC())
		require.NoError(t, err)
		assert.Equal(t, int64(2), n)
	})

	t.Run("active record still exists", func(t *testing.T) {
		_, _, err := idem.CreateOrGet(ctx, &store.IdempotencyRecord{
			ID:          "dup",
			Scope:       "s",
			Key:         "k3",
			RequestHash: "h",
			CreatedAt:   time.Now().UTC(),
			ExpiresAt:   time.Now().UTC().Add(1 * time.Hour),
		})
		require.NoError(t, err)
	})
}

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

func TestListOlderThanFiltersByCutoff(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	old := []string{"old-1", "old-2", "old-3"}
	recent := []string{"recent-1", "recent-2"}
	for i, id := range old {
		require.NoError(t, st.AuditEvents().Create(ctx, &store.AuditEvent{
			ID:             id,
			OrganizationID: "org-001",
			Metadata:       map[string]string{},
			CreatedAt:      base.Add(time.Duration(i) * time.Hour),
		}))
	}
	for i, id := range recent {
		require.NoError(t, st.AuditEvents().Create(ctx, &store.AuditEvent{
			ID:             id,
			OrganizationID: "org-001",
			Metadata:       map[string]string{},
			CreatedAt:      base.Add(100*24*time.Hour + time.Duration(i)*time.Hour),
		}))
	}

	cutoff := base.Add(50 * 24 * time.Hour)
	events, err := st.AuditEvents().ListOlderThan(ctx, cutoff, 10)
	require.NoError(t, err)
	assert.Len(t, events, 3)

	ids := make([]string, len(events))
	for i, e := range events {
		ids[i] = e.ID
		assert.True(t, e.CreatedAt.Before(cutoff), "event %s at %s not before cutoff %s", e.ID, e.CreatedAt, cutoff)
	}
	assert.ElementsMatch(t, old, ids)
}

func TestListOlderThanAscendingOrder(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	createAt := func(id string, offset time.Duration) {
		require.NoError(t, st.AuditEvents().Create(ctx, &store.AuditEvent{
			ID:             id,
			OrganizationID: "org-001",
			Metadata:       map[string]string{},
			CreatedAt:      base.Add(offset),
		}))
	}
	createAt("c", 3*time.Hour)
	createAt("a", 1*time.Hour)
	createAt("b", 2*time.Hour)

	cutoff := base.Add(10 * 24 * time.Hour)
	events, err := st.AuditEvents().ListOlderThan(ctx, cutoff, 10)
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, "a", events[0].ID)
	assert.Equal(t, "b", events[1].ID)
	assert.Equal(t, "c", events[2].ID)
}

func TestListOlderThanRespectsBatchSize(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	for i := range 5 {
		require.NoError(t, st.AuditEvents().Create(ctx, &store.AuditEvent{
			ID:             string(rune('a' + i)),
			OrganizationID: "org-001",
			Metadata:       map[string]string{},
			CreatedAt:      base.Add(time.Duration(i) * time.Hour),
		}))
	}

	cutoff := base.Add(10 * 24 * time.Hour)
	events, err := st.AuditEvents().ListOlderThan(ctx, cutoff, 2)
	require.NoError(t, err)
	assert.Len(t, events, 2)
}

func TestDeleteByIDsRemovesOnlySpecified(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	ids := []string{"del-1", "del-2"}
	for i, id := range ids {
		require.NoError(t, st.AuditEvents().Create(ctx, &store.AuditEvent{
			ID:             id,
			OrganizationID: "org-001",
			Metadata:       map[string]string{},
			CreatedAt:      base.Add(time.Duration(i) * time.Hour),
		}))
	}
	require.NoError(t, st.AuditEvents().Create(ctx, &store.AuditEvent{
		ID:             "keep-1",
		OrganizationID: "org-001",
		Metadata:       map[string]string{},
		CreatedAt:      base,
	}))

	n, err := st.AuditEvents().DeleteByIDs(ctx, ids)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	// Deleted events should not be found.
	for _, id := range ids {
		_, err := st.AuditEvents().GetByID(ctx, id)
		assert.ErrorIs(t, err, store.ErrNotFound, "event %s should be deleted", id)
	}
	// Kept event should still exist.
	kept, err := st.AuditEvents().GetByID(ctx, "keep-1")
	require.NoError(t, err)
	assert.Equal(t, "keep-1", kept.ID)
}

func TestDeleteByIDsEmptySliceIsNoop(t *testing.T) {
	st := setupStore(t)
	n, err := st.AuditEvents().DeleteByIDs(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

func TestListOlderThanEmptyCutoffReturnsAll(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	for _, id := range []string{"ev-1", "ev-2"} {
		require.NoError(t, st.AuditEvents().Create(ctx, &store.AuditEvent{
			ID:             id,
			OrganizationID: "org-001",
			Metadata:       map[string]string{},
		}))
	}

	farFuture := time.Now().Add(365 * 24 * time.Hour)
	events, err := st.AuditEvents().ListOlderThan(ctx, farFuture, 10)
	require.NoError(t, err)
	assert.Len(t, events, 2)
}

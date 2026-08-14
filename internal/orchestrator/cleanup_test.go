package orchestrator

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestCleanupServiceDeletesOnlyPrepareSessionMetadataPastRetention(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	ctx := context.Background()
	now := time.Now().UTC()

	oldDefinition := createCleanupDefinition(t, st, "cleanup-old")
	recentDefinition := createCleanupDefinition(t, st, "cleanup-recent")
	for _, session := range []*store.PrepareSession{
		{
			TokenHash: "cleanup-expired-old", ActorUserID: "user", OrganizationID: "org",
			ReleaseDefinitionID: oldDefinition.ID, TaskIDs: []string{"task-old"},
			LockedPaths: []string{"/replicas"}, LockedPathHash: "old",
			ExpiresAt: now.Add(-25 * time.Hour), CreatedAt: now.Add(-26 * time.Hour),
		},
		{
			TokenHash: "cleanup-expired-recent", ActorUserID: "user", OrganizationID: "org",
			ReleaseDefinitionID: recentDefinition.ID, TaskIDs: []string{"task-recent"},
			LockedPaths: []string{"/replicas"}, LockedPathHash: "recent",
			ExpiresAt: now.Add(-time.Hour), CreatedAt: now.Add(-2 * time.Hour),
		},
	} {
		require.NoError(t, st.PrepareSessions().Create(ctx, session, 0))
	}

	service := NewCleanupService(st, DefaultRetentionConfig(), slog.New(slog.DiscardHandler))
	_, errs := service.runGC(ctx)
	require.Empty(t, errs)

	_, err := st.PrepareSessions().Get(ctx, "cleanup-expired-old")
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.PrepareSessions().Get(ctx, "cleanup-expired-recent")
	require.NoError(t, err)
	_, err = st.Definitions().Get(ctx, oldDefinition.ID)
	require.NoError(t, err)
}

func TestDefaultRetentionConfigValidates(t *testing.T) {
	require.NoError(t, DefaultRetentionConfig().Validate())
	assert.Equal(t, 6, DefaultRetentionConfig().GCIntervalHours)
	assert.Equal(t, 24, DefaultRetentionConfig().PrepareSessionHours)
	assert.Equal(t, 10, DefaultRetentionConfig().PrepareSessionGCIntervalMinutes)
}

func createCleanupDefinition(t *testing.T, st *sqlitestore.Store, id string) *store.ReleaseDefinition {
	t.Helper()
	definition := &store.ReleaseDefinition{
		ID: id, Name: id, CustomerID: "customer-" + id, ClusterID: "cluster-" + id,
		ReleaseName: id, Status: store.DefStatusActive,
	}
	require.NoError(t, st.Definitions().Create(context.Background(), definition, nil))
	return definition
}

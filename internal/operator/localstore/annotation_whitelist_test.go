package localstore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TASK-244: the annotation whitelist is persisted in its own bucket, keyed by
// release key, and must survive closing/reopening the DB (the operator restart
// case) independently of the command bucket.
func TestBoltStore_AnnotationWhitelistRoundTripAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")

	store, err := OpenBolt(path)
	require.NoError(t, err)
	alpha := []ApprovedAnnotationKey{
		{Key: "team", Scope: "WORKLOAD_METADATA"},
		{Key: "prometheus.io/scrape", Scope: "POD_TEMPLATE_METADATA", PromotionValuesPath: "podAnnotations"},
	}
	beta := []ApprovedAnnotationKey{{Key: "tier", Scope: "WORKLOAD_METADATA"}}
	require.NoError(t, store.SaveAnnotationWhitelist(ctx, "apps/alpha", alpha))
	require.NoError(t, store.SaveAnnotationWhitelist(ctx, "apps/beta", beta))
	// A command in the commands bucket must not leak into the whitelist load.
	require.NoError(t, store.Save(ctx, &CommandEntry{CommandID: "cmd-1", Status: StatusPending}))
	require.NoError(t, store.Close())

	reopened, err := OpenBolt(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	loaded, err := reopened.LoadAnnotationWhitelists(ctx)
	require.NoError(t, err)
	require.Len(t, loaded, 2)
	assert.Equal(t, alpha, loaded["apps/alpha"])
	assert.Equal(t, beta, loaded["apps/beta"])
}

// TASK-244: SaveAnnotationWhitelist overwrites the release's previous list
// (the release write replaces the definition-derived whitelist) and Delete
// removes it, so a cleared whitelist cannot be revived by the next load.
func TestBoltStore_AnnotationWhitelistSaveOverwritesAndDeleteClears(t *testing.T) {
	ctx := context.Background()
	store, err := OpenBolt(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	require.NoError(t, store.SaveAnnotationWhitelist(ctx, "apps/alpha", []ApprovedAnnotationKey{
		{Key: "team", Scope: "WORKLOAD_METADATA"},
	}))
	require.NoError(t, store.SaveAnnotationWhitelist(ctx, "apps/alpha", []ApprovedAnnotationKey{
		{Key: "tier", Scope: "WORKLOAD_METADATA"},
	}))

	loaded, err := store.LoadAnnotationWhitelists(ctx)
	require.NoError(t, err)
	assert.Equal(t, []ApprovedAnnotationKey{{Key: "tier", Scope: "WORKLOAD_METADATA"}}, loaded["apps/alpha"],
		"a second save must overwrite, not append")

	// Deleting an absent key is idempotent.
	require.NoError(t, store.DeleteAnnotationWhitelist(ctx, "apps/missing"))
	require.NoError(t, store.DeleteAnnotationWhitelist(ctx, "apps/alpha"))
	loaded, err = store.LoadAnnotationWhitelists(ctx)
	require.NoError(t, err)
	assert.Empty(t, loaded, "the deleted whitelist must be gone")
}

// TASK-244: an empty release key is rejected instead of producing an
// unreachable entry, and the initial load of an untouched store is empty
// rather than nil.
func TestBoltStore_AnnotationWhitelistRejectsEmptyKeyAndLoadsEmpty(t *testing.T) {
	ctx := context.Background()
	store, err := OpenBolt(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	assert.Error(t, store.SaveAnnotationWhitelist(ctx, "", nil))
	assert.Error(t, store.DeleteAnnotationWhitelist(ctx, ""))

	loaded, err := store.LoadAnnotationWhitelists(ctx)
	require.NoError(t, err)
	assert.NotNil(t, loaded, "an empty store must yield an empty map, not nil")
	assert.Empty(t, loaded)
}

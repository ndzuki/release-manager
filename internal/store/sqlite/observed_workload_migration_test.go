package sqlite

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// observedWorkloadColumns are the TASK-168 (REQ-058 C1/R1) observed field
// projection columns migrationStatements must add to release_inventory.
var observedWorkloadColumns = []string{
	"observed_containers",
	"observed_image_refs",
	"observed_replicas",
	"observed_at",
}

// TestLegacyMigrationAddsObservedWorkloadColumns is the W3 mutation gate: a
// pre-existing (legacy) database must gain the observed workload columns from
// migrationStatements, not just a freshly created one. Removing any of the four
// ALTER statements makes this test fail ("no such column") — the fresh path
// alone would not catch it, which is exactly the drift the gate exists for.
func TestLegacyMigrationAddsObservedWorkloadColumns(t *testing.T) {
	legacyPath := t.TempDir() + "/legacy.db"
	raw, err := sql.Open("sqlite", legacyPath)
	require.NoError(t, err)
	// Any schema object routes Open into the incremental legacy migration loop.
	_, err = raw.ExecContext(context.Background(), "CREATE TABLE throwaway_marker (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	st, err := Open(legacyPath)
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	for _, column := range observedWorkloadColumns {
		require.Contains(t, releaseInventoryColumns(t, st.db), column,
			"a legacy database must gain %s from migrationStatements", column)
	}

	// Shape is not enough: the legacy path must actually persist and read the
	// observation through the real store seam.
	ctx := t.Context()
	require.NoError(t, st.Inventories().Upsert(ctx, &store.ReleaseInventory{
		CustomerID:      "customer-1",
		ClusterID:       "cluster-1",
		Namespace:       "apps",
		ReleaseName:     "example",
		Chart:           "example-chart",
		Revision:        1,
		InventoryStatus: store.InventoryActive,
	}))

	replicas := int32(2)
	observedAt := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, st.Inventories().UpdateWorkloadObservation(ctx, "customer-1", "cluster-1", "apps", "example", store.WorkloadObservation{
		Containers: []string{"api"},
		ImageRefs:  map[string]string{"api": "registry.example.com/api:1.0.0"},
		Replicas:   &replicas,
		ObservedAt: observedAt,
	}))

	got, err := st.Inventories().GetByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "example")
	require.NoError(t, err)
	assert.Equal(t, []string{"api"}, got.ObservedContainers)
	assert.Equal(t, map[string]string{"api": "registry.example.com/api:1.0.0"}, got.ObservedImageRefs)
	require.NotNil(t, got.ObservedReplicas)
	assert.Equal(t, int32(2), *got.ObservedReplicas)
	assert.True(t, got.ObservedAt.Equal(observedAt))
}

// TestFreshSchemaCarriesObservedWorkloadColumns is the fresh-path twin of the
// legacy gate: a brand-new database (built from the same migrationStatements
// snapshot) must expose the same columns.
func TestFreshSchemaCarriesObservedWorkloadColumns(t *testing.T) {
	st, err := Open("file:fresh-observed-workload?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	for _, column := range observedWorkloadColumns {
		require.Contains(t, releaseInventoryColumns(t, st.db), column,
			"a fresh database must carry %s", column)
	}
}

// releaseInventoryColumns reads the release_inventory column set from
// sqlite_master's declared schema via PRAGMA table_info.
func releaseInventoryColumns(t *testing.T, db *sql.DB) map[string]struct{} {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `PRAGMA table_info(release_inventory)`)
	require.NoError(t, err)
	defer rows.Close()

	columns := make(map[string]struct{})
	for rows.Next() {
		var position, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		require.NoError(t, rows.Scan(&position, &name, &columnType, &notNull, &defaultValue, &primaryKey))
		columns[name] = struct{}{}
	}
	require.NoError(t, rows.Err())
	return columns
}

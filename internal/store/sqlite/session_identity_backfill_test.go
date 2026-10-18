package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// TestLegacyMigrationBackfillsSessionIdentity is the TASK-361 migration gate:
// rows the pre-fix CommandStream writer left with empty customer_id/cluster_id
// must be repaired from their operator when an existing (legacy) database is
// opened. A fresh database never shows the bug (the row is written correctly),
// so only this path proves the in-place upgrade a deployment performs.
func TestLegacyMigrationBackfillsSessionIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-sessions.db")
	ctx := context.Background()

	seed, err := sqlitestore.Open(path)
	require.NoError(t, err)
	require.NoError(t, createCustomerViaManagement(ctx, seed, &store.Customer{ID: "cust-1", Name: "Legacy customer", Slug: "legacy-customer"}))
	require.NoError(t, seed.Clusters().Create(ctx, &store.Cluster{ID: "clus-1", Name: "Legacy cluster", CustomerID: "cust-1"}))
	require.NoError(t, seed.Operators().Create(ctx, &store.Operator{ID: "op-1", CustomerID: "cust-1", ClusterID: "clus-1", CertSerial: "serial-1"}))
	require.NoError(t, seed.Sessions().Establish(ctx, &store.Session{
		ID: "sess-1", OperatorID: "op-1", CustomerID: "cust-1", ClusterID: "clus-1", Status: store.SessionOnline,
	}))
	require.NoError(t, seed.Close())

	// Rewrite the row the way the pre-fix writer left it.
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, `UPDATE sessions SET customer_id = '', cluster_id = '' WHERE id = 'sess-1'`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	// Reopening an existing database takes the legacy migration loop, where the
	// backfill statement runs.
	repaired, err := sqlitestore.Open(path)
	require.NoError(t, err)
	sess, err := repaired.Sessions().Get(ctx, "sess-1")
	require.NoError(t, err)
	require.Equal(t, "cust-1", sess.CustomerID)
	require.Equal(t, "clus-1", sess.ClusterID)
	require.NoError(t, repaired.Close())

	// Idempotent: a second open leaves the repaired values untouched.
	again, err := sqlitestore.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { again.Close() })
	sess, err = again.Sessions().Get(ctx, "sess-1")
	require.NoError(t, err)
	require.Equal(t, "cust-1", sess.CustomerID)
	require.Equal(t, "clus-1", sess.ClusterID)
}

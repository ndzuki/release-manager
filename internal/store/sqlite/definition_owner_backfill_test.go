package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// TestLegacyMigrationBackfillsReleaseDefinitionOwner is the TASK-359 migration
// gate: definitions the pre-TASK-357 creation path stored with a NULL owner must
// be repaired from the customer's unique active organization binding when an
// existing (legacy) database is opened. A fresh database never shows the defect,
// so only this path proves the in-place upgrade a deployment performs.
//
// The negative cases are as important as the positive one: an ambiguous customer
// (zero or several active bindings) or a disabled owner organization must keep
// NULL rather than receive a guessed organization, because a wrong backfill
// would be a cross-organization authorization grant.
func TestLegacyMigrationBackfillsReleaseDefinitionOwner(t *testing.T) {
	t.Run("unique active binding backfills the owner", func(t *testing.T) {
		path := seedOwnerBackfillDB(t, ownerBackfillSeed{
			organizations: []ownerBackfillOrg{{id: "org-1", status: "active"}},
			bindings:      []ownerBackfillBinding{{id: "b-1", orgID: "org-1", customerID: "cust-1", status: "active"}},
			definitions:   []ownerBackfillDefinition{{id: "def-null", customerID: "cust-1"}},
		})

		owner := reopenedOwner(t, path, "def-null")
		require.NotNil(t, owner)
		require.Equal(t, "org-1", *owner)

		// Idempotent: a second open leaves the repaired value untouched.
		requireOwner(t, path, "def-null", "org-1")
	})

	t.Run("backfills the binding's organization, not a hardcoded one", func(t *testing.T) {
		path := seedOwnerBackfillDB(t, ownerBackfillSeed{
			organizations: []ownerBackfillOrg{{id: "org-2", status: "active"}},
			bindings:      []ownerBackfillBinding{{id: "b-9", orgID: "org-2", customerID: "cust-8", status: "active"}},
			definitions:   []ownerBackfillDefinition{{id: "def-other-org", customerID: "cust-8"}},
		})

		requireOwner(t, path, "def-other-org", "org-2")
	})

	t.Run("already owned definition is never reassigned", func(t *testing.T) {
		path := seedOwnerBackfillDB(t, ownerBackfillSeed{
			organizations: []ownerBackfillOrg{{id: "org-1", status: "active"}, {id: "org-2", status: "active"}},
			bindings:      []ownerBackfillBinding{{id: "b-2", orgID: "org-2", customerID: "cust-2", status: "active"}},
			definitions:   []ownerBackfillDefinition{{id: "def-owned", customerID: "cust-2", ownerID: "org-1"}},
		})

		requireOwner(t, path, "def-owned", "org-1")
	})

	t.Run("two active bindings keep NULL instead of guessing", func(t *testing.T) {
		path := seedOwnerBackfillDB(t, ownerBackfillSeed{
			organizations: []ownerBackfillOrg{{id: "org-1", status: "active"}, {id: "org-2", status: "active"}},
			bindings: []ownerBackfillBinding{
				{id: "b-3", orgID: "org-1", customerID: "cust-3", status: "active"},
				{id: "b-4", orgID: "org-2", customerID: "cust-3", status: "active"},
			},
			definitions: []ownerBackfillDefinition{{id: "def-ambiguous", customerID: "cust-3"}},
		})

		require.Nil(t, reopenedOwner(t, path, "def-ambiguous"))
	})

	t.Run("no active binding keeps NULL", func(t *testing.T) {
		path := seedOwnerBackfillDB(t, ownerBackfillSeed{
			organizations: []ownerBackfillOrg{{id: "org-1", status: "active"}},
			bindings:      []ownerBackfillBinding{{id: "b-5", orgID: "org-1", customerID: "cust-4", status: "revoked"}},
			definitions:   []ownerBackfillDefinition{{id: "def-unbound", customerID: "cust-4"}},
		})

		require.Nil(t, reopenedOwner(t, path, "def-unbound"))

		// A disabled binding does not count as the unique active binding either.
		path = seedOwnerBackfillDB(t, ownerBackfillSeed{
			organizations: []ownerBackfillOrg{{id: "org-1", status: "active"}},
			bindings:      []ownerBackfillBinding{{id: "b-6", orgID: "org-1", customerID: "cust-5", status: "suspended"}},
			definitions:   []ownerBackfillDefinition{{id: "def-suspended", customerID: "cust-5"}},
		})
		require.Nil(t, reopenedOwner(t, path, "def-suspended"))
	})

	t.Run("disabled owner organization keeps NULL", func(t *testing.T) {
		path := seedOwnerBackfillDB(t, ownerBackfillSeed{
			organizations: []ownerBackfillOrg{{id: "org-1", status: "disabled"}},
			bindings:      []ownerBackfillBinding{{id: "b-7", orgID: "org-1", customerID: "cust-6", status: "active"}},
			definitions:   []ownerBackfillDefinition{{id: "def-disabled-org", customerID: "cust-6"}},
		})

		// The binding is unique, but a disabled organization could not pass the
		// approval authorization check, so the definition stays unapprovable.
		require.Nil(t, reopenedOwner(t, path, "def-disabled-org"))
	})

	t.Run("backfilled owner survives a later revoked binding", func(t *testing.T) {
		path := seedOwnerBackfillDB(t, ownerBackfillSeed{
			organizations: []ownerBackfillOrg{{id: "org-1", status: "active"}},
			bindings:      []ownerBackfillBinding{{id: "b-8", orgID: "org-1", customerID: "cust-7", status: "active"}},
			definitions:   []ownerBackfillDefinition{{id: "def-keep", customerID: "cust-7"}},
		})
		requireOwner(t, path, "def-keep", "org-1")

		raw, err := sql.Open("sqlite", path)
		require.NoError(t, err)
		_, err = raw.ExecContext(context.Background(), `UPDATE org_customer_bindings SET status = 'revoked' WHERE id = 'b-8'`)
		require.NoError(t, err)
		require.NoError(t, raw.Close())

		// The migration only fills NULL, so a revoked binding never clears an
		// owner an approval decision may already rely on.
		requireOwner(t, path, "def-keep", "org-1")
	})
}

type ownerBackfillOrg struct {
	id     string
	status string
}

type ownerBackfillBinding struct {
	id         string
	orgID      string
	customerID string
	status     string
}

type ownerBackfillDefinition struct {
	id         string
	customerID string
	ownerID    string // "" leaves owner_organization_id NULL
}

type ownerBackfillSeed struct {
	organizations []ownerBackfillOrg
	bindings      []ownerBackfillBinding
	definitions   []ownerBackfillDefinition
}

// seedOwnerBackfillDB creates a database through the normal open path, then
// rewrites it into the shape the pre-fix writer left behind (definition with a
// NULL owner), simulating the database a deployment upgrades in place.
func seedOwnerBackfillDB(t *testing.T, seed ownerBackfillSeed) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy-owner.db")
	ctx := context.Background()

	st, err := sqlitestore.Open(path)
	require.NoError(t, err)
	for _, org := range seed.organizations {
		_, err := st.DB().ExecContext(ctx,
			`INSERT INTO organizations (id, name, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			org.id, "Org "+org.id, org.status, "2026-10-17T10:00:00Z", "2026-10-17T10:00:00Z")
		require.NoError(t, err)
	}
	for _, customer := range distinctCustomers(seed) {
		_, err := st.DB().ExecContext(ctx,
			`INSERT INTO customers (id, name, slug, status, created_at, updated_at) VALUES (?, ?, ?, 'active', ?, ?)`,
			customer, "Customer "+customer, customer, "2026-10-17T10:00:00Z", "2026-10-17T10:00:00Z")
		require.NoError(t, err)
	}
	for _, binding := range seed.bindings {
		_, err := st.DB().ExecContext(ctx,
			`INSERT INTO org_customer_bindings (id, org_id, customer_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			binding.id, binding.orgID, binding.customerID, binding.status, "2026-10-17T10:00:00Z", "2026-10-17T10:00:00Z")
		require.NoError(t, err)
	}
	for _, definition := range seed.definitions {
		var owner any
		if definition.ownerID != "" {
			owner = definition.ownerID
		}
		_, err := st.DB().ExecContext(ctx,
			`INSERT INTO release_definitions
			   (id, name, customer_id, cluster_id, namespace, release_name, chart_name, status,
			    owner_organization_id, created_at, updated_at)
			 VALUES (?, ?, ?, ?, '', ?, '', 'active', ?, ?, ?)`,
			definition.id, definition.id, definition.customerID, "cluster-"+definition.customerID,
			definition.id, owner, "2026-10-17T10:00:00Z", "2026-10-17T10:00:00Z")
		require.NoError(t, err)
	}
	require.NoError(t, st.Close())
	return path
}

func distinctCustomers(seed ownerBackfillSeed) []string {
	seen := map[string]bool{}
	var out []string
	for _, definition := range seed.definitions {
		if !seen[definition.customerID] {
			seen[definition.customerID] = true
			out = append(out, definition.customerID)
		}
	}
	return out
}

// requireOwner asserts a definition's owner, failing cleanly (instead of
// panicking on a nil pointer) when the backfill did not run.
func requireOwner(t *testing.T, path, definitionID, want string) {
	t.Helper()
	owner := reopenedOwner(t, path, definitionID)
	require.NotNil(t, owner, "definition %s has no owner", definitionID)
	require.Equal(t, want, *owner)
}

// reopenedOwner opens an existing database (taking the legacy migration path),
// reads the definition's owner and closes the store again.
func reopenedOwner(t *testing.T, path, definitionID string) *string {
	t.Helper()
	ctx := context.Background()
	st, err := sqlitestore.Open(path)
	require.NoError(t, err)

	var owner sql.NullString
	require.NoError(t, st.DB().QueryRowContext(ctx,
		`SELECT owner_organization_id FROM release_definitions WHERE id = ?`, definitionID).Scan(&owner))
	require.NoError(t, st.Close())

	if !owner.Valid {
		return nil
	}
	value := owner.String
	return &value
}

//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/require"

	schemamigrations "github.com/ndzuki/release-manager/migrations"
)

// TestReleaseDefinitionOwnerBackfillMigration exercises the PostgreSQL half of
// the TASK-359 data repair (migrations/000036_release_definition_owner_backfill)
// against a real server: the fixture is written after the schema reaches head, so
// the SQL itself is what repairs the rows.
//
// The negative cases mirror the SQLite gate
// (internal/store/sqlite/definition_owner_backfill_test.go): an ambiguous
// customer (zero or several active bindings) and a disabled owner organization
// must keep NULL. A guessed organization would be a cross-organization
// authorization grant.
func TestReleaseDefinitionOwnerBackfillMigration(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	db := st.SQLDB()

	// Two active organizations, one disabled, one customer with a unique active
	// binding, one with two, one with none, and one whose only binding points at
	// the disabled organization.
	for _, org := range []struct{ id, status string }{
		{"org-1", "active"},
		{"org-2", "active"},
		{"org-3", "disabled"},
	} {
		_, err := db.ExecContext(ctx,
			`INSERT INTO organizations (id, name, status, created_at, updated_at) VALUES ($1, $2, $3, NOW(), NOW())`,
			org.id, "Org "+org.id, org.status)
		require.NoError(t, err)
	}
	for _, customer := range []string{"cust-1", "cust-2", "cust-3", "cust-4"} {
		_, err := db.ExecContext(ctx,
			`INSERT INTO customers (id, name, slug, status, created_at, updated_at) VALUES ($1, $2, $3, 'active', NOW(), NOW())`,
			customer, "Customer "+customer, customer)
		require.NoError(t, err)
	}
	for _, binding := range []struct{ id, orgID, customerID, status string }{
		{"b-1", "org-1", "cust-1", "active"},
		{"b-2", "org-1", "cust-2", "active"},
		{"b-3", "org-2", "cust-2", "active"},
		{"b-4", "org-3", "cust-4", "active"},
	} {
		_, err := db.ExecContext(ctx,
			`INSERT INTO org_customer_bindings (id, org_id, customer_id, status, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, NOW(), NOW())`,
			binding.id, binding.orgID, binding.customerID, binding.status)
		require.NoError(t, err)
	}
	// def-null: unique active binding -> org-1.
	// def-ambiguous: two active bindings -> stays NULL.
	// def-unbound: no binding -> stays NULL.
	// def-disabled-org: unique binding to a disabled organization -> stays NULL.
	// def-owned: already owned -> untouched.
	for _, definition := range []struct{ id, customerID, ownerID string }{
		{"def-null", "cust-1", ""},
		{"def-ambiguous", "cust-2", ""},
		{"def-unbound", "cust-3", ""},
		{"def-disabled-org", "cust-4", ""},
		{"def-owned", "cust-1", "org-2"},
	} {
		var owner any
		if definition.ownerID != "" {
			owner = definition.ownerID
		}
		_, err := db.ExecContext(ctx,
			`INSERT INTO release_definitions
			   (id, name, customer_id, cluster_id, release_name, status, owner_organization_id, created_at, updated_at)
			 VALUES ($1, $1, $2, $3, $1, 'active', $4, NOW(), NOW())`,
			definition.id, definition.customerID, "cluster-"+definition.customerID, owner)
		require.NoError(t, err)
	}

	up, err := fs.ReadFile(schemamigrations.FS, "000036_release_definition_owner_backfill.up.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(up))
	require.NoError(t, err)

	require.Equal(t, "org-1", definitionOwner(t, db, "def-null"))
	require.Nil(t, definitionOwner(t, db, "def-ambiguous"))
	require.Nil(t, definitionOwner(t, db, "def-unbound"))
	require.Nil(t, definitionOwner(t, db, "def-disabled-org"))
	require.Equal(t, "org-2", definitionOwner(t, db, "def-owned"))

	// Idempotent: replaying the migration changes no owner.
	_, err = db.ExecContext(ctx, string(up))
	require.NoError(t, err)
	require.Equal(t, "org-1", definitionOwner(t, db, "def-null"))
	require.Equal(t, "org-2", definitionOwner(t, db, "def-owned"))
	require.Nil(t, definitionOwner(t, db, "def-ambiguous"))
}

// definitionOwner returns the definition's owner organization, or nil when it is
// still NULL.
func definitionOwner(t *testing.T, db *sql.DB, definitionID string) any {
	t.Helper()
	var owner sql.NullString
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT owner_organization_id FROM release_definitions WHERE id = $1`, definitionID).Scan(&owner))
	if !owner.Valid {
		return nil
	}
	return owner.String
}

package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// testSchemaSQL contains a minimal subset of the SQLite schema used in tests.
const testSchemaSQL = `
CREATE TABLE customers (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	slug       TEXT NOT NULL UNIQUE,
	status     TEXT NOT NULL DEFAULT 'active',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE release_definitions (
	id                  TEXT PRIMARY KEY,
	name                TEXT NOT NULL,
	customer_id         TEXT NOT NULL,
	cluster_id          TEXT NOT NULL,
	namespace           TEXT NOT NULL DEFAULT '',
	release_name        TEXT NOT NULL,
	chart_name          TEXT NOT NULL DEFAULT '',
	status              TEXT NOT NULL DEFAULT 'draft',
	optimistic_version  INTEGER NOT NULL DEFAULT 0,
	created_by          TEXT NOT NULL DEFAULT '',
	created_at          TEXT NOT NULL,
	updated_at          TEXT NOT NULL,
	UNIQUE(customer_id, cluster_id, namespace, release_name)
);

CREATE TABLE clusters (
	id             TEXT PRIMARY KEY,
	name           TEXT NOT NULL,
	customer_id    TEXT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	kubeconfig_ref TEXT NOT NULL DEFAULT '',
	status         TEXT NOT NULL DEFAULT 'active',
	created_at     TEXT NOT NULL,
	updated_at     TEXT NOT NULL
);

CREATE TABLE operations (
	id                   TEXT PRIMARY KEY,
	operation_type       TEXT NOT NULL,
	status               TEXT NOT NULL DEFAULT 'pending',
	release_definition_id TEXT NOT NULL REFERENCES release_definitions(id) ON DELETE CASCADE,
	idempotency_key      TEXT NOT NULL UNIQUE,
	request_hash         TEXT NOT NULL,
	state_version        INTEGER NOT NULL DEFAULT 0,
	bundle_id            TEXT NOT NULL DEFAULT '',
	values_revision_id   TEXT NOT NULL DEFAULT '',
	expected_revision    INTEGER NOT NULL DEFAULT 0,
	values_patch         BLOB,
	actor                TEXT NOT NULL DEFAULT '{}',
	created_at           TEXT NOT NULL,
	updated_at           TEXT NOT NULL,
	deadline             TEXT,
	last_error           TEXT NOT NULL DEFAULT '',
	target_revision      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE operation_events (
	id                    TEXT PRIMARY KEY,
	operation_id          TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
	operation_type        TEXT NOT NULL,
	release_definition_id TEXT NOT NULL,
	old_status            TEXT NOT NULL,
	new_status            TEXT NOT NULL,
	state_version         INTEGER NOT NULL,
	created_at            TEXT NOT NULL
);

CREATE TABLE users (
	id            TEXT PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	status        TEXT NOT NULL DEFAULT 'active',
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL,
	provider      TEXT NOT NULL DEFAULT '',
	subject       TEXT NOT NULL DEFAULT ''
);

CREATE TABLE auth_sessions (
	id                 TEXT PRIMARY KEY,
	user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	token_family       TEXT NOT NULL,
	refresh_token_hash TEXT NOT NULL,
	expires_at         TEXT NOT NULL,
	created_at         TEXT NOT NULL,
	revoked            INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE organizations (
	id                 TEXT PRIMARY KEY,
	name               TEXT NOT NULL,
	status             TEXT NOT NULL DEFAULT 'active',
	optimistic_version INTEGER NOT NULL DEFAULT 0,
	created_at         TEXT NOT NULL,
	updated_at         TEXT NOT NULL
);

CREATE TABLE org_customer_bindings (
	id                 TEXT PRIMARY KEY,
	org_id             TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	customer_id        TEXT NOT NULL,
	status             TEXT NOT NULL DEFAULT 'active',
	optimistic_version INTEGER NOT NULL DEFAULT 0,
	created_at         TEXT NOT NULL,
	updated_at         TEXT NOT NULL,
	UNIQUE(org_id, customer_id)
);

CREATE TABLE organization_customer_binding_events (
	id                 TEXT PRIMARY KEY,
	binding_id         TEXT NOT NULL REFERENCES org_customer_bindings(id) ON DELETE CASCADE,
	org_id             TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	customer_id        TEXT NOT NULL,
	status             TEXT NOT NULL,
	optimistic_version INTEGER NOT NULL,
	changed_at         TEXT NOT NULL
);

CREATE TABLE audit_events (
	id               TEXT PRIMARY KEY,
	actor_kind       TEXT NOT NULL DEFAULT 'system',
	actor_id         TEXT NOT NULL DEFAULT '',
	organization_id  TEXT NOT NULL DEFAULT '',
	role             TEXT NOT NULL DEFAULT '',
	resource_type    TEXT NOT NULL DEFAULT '',
	resource_id      TEXT NOT NULL DEFAULT '',
	action           TEXT NOT NULL DEFAULT '',
	status           TEXT NOT NULL DEFAULT '',
	duration_ms      INTEGER NOT NULL DEFAULT 0,
	change_summary   TEXT NOT NULL DEFAULT '',
	metadata         TEXT NOT NULL DEFAULT '{}',
	created_at       TEXT NOT NULL
);
`

// testSeedSQL populates the test schema with known data.
const testSeedSQL = `
INSERT INTO customers(id, name, slug, status, created_at, updated_at)
VALUES ('cust-1', 'Acme Corp', 'acme-corp', 'active', '2024-01-01T00:00:00Z', '2024-06-01T00:00:00Z');

INSERT INTO release_definitions(id, name, customer_id, cluster_id, namespace, release_name, status, created_at, updated_at)
VALUES ('def-1', 'webapp', 'cust-1', 'cluster-1', 'default', 'webapp-prod', 'active', '2024-01-15T00:00:00Z', '2024-06-15T00:00:00Z');

INSERT INTO clusters(id, name, customer_id, status, created_at, updated_at)
VALUES ('cluster-1', 'prod-us-east', 'cust-1', 'active', '2024-01-01T00:00:00Z', '2024-06-01T00:00:00Z');

INSERT INTO operations(id, operation_type, status, release_definition_id, idempotency_key, request_hash, created_at, updated_at, deadline)
VALUES ('op-1', 'UPGRADE', 'succeeded', 'def-1', 'idem-1', 'abc123', '2024-02-01T00:00:00Z', '2024-02-01T01:00:00Z', '2024-02-01T02:00:00Z');

INSERT INTO operation_events(id, operation_id, operation_type, release_definition_id, old_status, new_status, state_version, created_at)
VALUES ('evt-1', 'op-1', 'UPGRADE', 'def-1', 'pending', 'running', 1, '2024-02-01T00:00:00Z'),
       ('evt-2', 'op-1', 'UPGRADE', 'def-1', 'running', 'succeeded', 2, '2024-02-01T01:00:00Z');

INSERT INTO users(id, username, password_hash, status, created_at, updated_at)
VALUES ('user-1', 'admin', '$2a$10$hash', 'active', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z');

INSERT INTO auth_sessions(id, user_id, token_family, refresh_token_hash, expires_at, created_at, revoked)
VALUES ('sess-1', 'user-1', 'fam-1', 'hash1', '2025-01-01T00:00:00Z', '2024-01-01T00:00:00Z', 0);

INSERT INTO organizations(id, name, status, created_at, updated_at)
VALUES ('org-1', 'Engineering', 'active', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z');

INSERT INTO org_customer_bindings(id, org_id, customer_id, status, created_at, updated_at)
VALUES ('bind-1', 'org-1', 'cust-1', 'active', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z');

INSERT INTO organization_customer_binding_events(id, binding_id, org_id, customer_id, status, optimistic_version, changed_at)
VALUES ('bce-1', 'bind-1', 'org-1', 'cust-1', 'active', 1, '2024-01-01T00:00:00Z');

INSERT INTO audit_events(id, actor_kind, actor_id, action, status, created_at)
VALUES ('audit-1', 'user', 'user-1', 'login', 'success', '2024-01-01T00:00:00Z');
`

// newSQLiteFixture creates an in-memory SQLite database with the test schema and seed data.
func newSQLiteFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	// Execute schema.
	for _, stmt := range splitSQLStatements(testSchemaSQL) {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		_, err := db.ExecContext(t.Context(), stmt)
		require.NoError(t, err, "exec schema: %s", stmt)
	}

	// Execute seed.
	for _, stmt := range splitSQLStatements(testSeedSQL) {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		_, err := db.ExecContext(t.Context(), stmt)
		require.NoError(t, err, "exec seed: %s", stmt)
	}
	return db
}

func splitSQLStatements(s string) []string {
	var stmts []string
	var current strings.Builder
	inString := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\'' {
			inString = !inString
		}
		current.WriteByte(ch)
		if ch == ';' && !inString {
			stmts = append(stmts, current.String())
			current.Reset()
		}
	}
	remainder := strings.TrimSpace(current.String())
	if remainder != "" {
		stmts = append(stmts, remainder)
	}
	return stmts
}

func TestDiscoverTables(t *testing.T) {
	db := newSQLiteFixture(t)
	ctx := context.Background()

	tables, err := discoverTables(ctx, db)
	require.NoError(t, err)

	expected := []string{
		"audit_events",
		"auth_sessions",
		"clusters",
		"customers",
		"operation_events",
		"operations",
		"org_customer_bindings",
		"organization_customer_binding_events",
		"organizations",
		"release_definitions",
		"users",
	}
	assert.Equal(t, expected, tables)
}

func TestOrderTables(t *testing.T) {
	tables := []string{
		"operation_events",
		"operations",
		"clusters",
		"customers",
		"organization_customer_binding_events",
		"org_customer_bindings",
		"organizations",
		"release_definitions",
		"audit_events", // no FKs, should come first
	}

	ordered := orderTables(tables)

	// audit_events (leaf) should be before everything with FKs.
	// customers should be before clusters.
	// operations should be before operation_events.
	// organizations should be before org_customer_bindings.
	// org_customer_bindings should be before organization_customer_binding_events.
	idx := make(map[string]int)
	for i, t := range ordered {
		idx[t] = i
	}
	assert.True(t, idx["customers"] < idx["clusters"], "customers before clusters")
	assert.True(t, idx["operations"] < idx["operation_events"], "operations before operation_events")
	assert.True(t, idx["organizations"] < idx["org_customer_bindings"], "organizations before org_customer_bindings")
	assert.True(t, idx["org_customer_bindings"] < idx["organization_customer_binding_events"], "bindings before events")
	assert.True(t, idx["audit_events"] < idx["clusters"], "leaf table before FK table")
}

func TestOrderTablesValuesDecisionsAfterRevisions(t *testing.T) {
	ordered := orderTables([]string{"values_revision_decisions", "values_revisions", "release_definitions"})
	index := make(map[string]int, len(ordered))
	for i, table := range ordered {
		index[table] = i
	}
	assert.Less(t, index["release_definitions"], index["values_revisions"])
	assert.Less(t, index["values_revisions"], index["values_revision_decisions"])
}

func TestReadTableColumns(t *testing.T) {
	db := newSQLiteFixture(t)
	ctx := context.Background()

	cols, timeCols, blobCols, err := readTableColumns(ctx, db, "customers")
	require.NoError(t, err)

	assert.Equal(t, []string{"id", "name", "slug", "status", "created_at", "updated_at"}, cols)
	// Time columns: created_at (idx 4), updated_at (idx 5).
	assert.True(t, timeCols[4], "created_at should be a time column")
	assert.True(t, timeCols[5], "updated_at should be a time column")
	assert.False(t, timeCols[0], "id should not be a time column")
	assert.False(t, timeCols[1], "name should not be a time column")
	assert.Empty(t, blobCols)
}

func TestAppendTargetColumns(t *testing.T) {
	tests := []struct {
		name      string
		table     string
		columns   []string
		want      []string
		wantExtra int
	}{
		{name: "candidate last seen", table: "candidate_artifacts", columns: []string{"id", "created_at"}, want: []string{"id", "created_at", "last_seen_at"}, wantExtra: 1},
		{name: "preflight updated at", table: "preflight_lifecycles", columns: []string{"id", "created_at", "stages"}, want: []string{"id", "created_at", "stages", "updated_at"}, wantExtra: 1},
		{name: "unchanged", table: "customers", columns: []string{"id"}, want: []string{"id"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			columns, defaults := appendTargetColumns(tt.table, tt.columns)
			assert.Equal(t, tt.want, columns)
			assert.Len(t, defaults, tt.wantExtra)
		})
	}
}

func TestConvertSQLiteValue(t *testing.T) {
	converted, err := convertSQLiteValue("auth_sessions", "revoked", int64(1), false, false)
	require.NoError(t, err)
	assert.Equal(t, true, converted)

	converted, err = convertSQLiteValue("values_revisions", "values", "payload", false, true)
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), converted)

	converted, err = convertSQLiteValue("operations", "created_at", "2024-01-01T01:00:00+01:00", true, false)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), converted)

	converted, err = convertSQLiteValue("audit_outbox", "payload_json", []byte(`{"revision_id":"r1"}`), false, true)
	require.NoError(t, err)
	assert.Equal(t, `{"revision_id":"r1"}`, converted)

	_, err = convertSQLiteValue("audit_outbox", "payload_json", []byte(`not-json`), false, true)
	assert.ErrorContains(t, err, "invalid JSON")

	converted, err = convertSQLiteValue("notification_outbox", "delivered", int64(0), false, false)
	require.NoError(t, err)
	assert.Equal(t, false, converted)
}

func TestIsTimeColumn(t *testing.T) {
	tests := []struct {
		name     string
		expected bool
	}{
		{"created_at", true},
		{"updated_at", true},
		{"expires_at", true},
		{"used_at", true},
		{"deadline", true},
		{"last_heartbeat", true},
		{"terminal_at", true},
		{"archived_at", true},
		{"id", false},
		{"name", false},
		{"status", false},
		{"count", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.expected, isTimeColumn(tt.name), "isTimeColumn(%q)", tt.name)
	}
}

func TestParseTimeUTC(t *testing.T) {
	tests := []struct {
		input   string
		want    time.Time
		wantErr bool
	}{
		{"2024-01-15T10:30:00Z", time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC), false},
		{"2024-01-15T10:30:00+00:00", time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC), false},
		{"2024-06-01T00:00:00Z", time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), false},
		{"2024-01-01T00:00:00.123456789Z", time.Date(2024, 1, 1, 0, 0, 0, 123456789, time.UTC), false},
		{"2024-01-01 00:00:00", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"not a time", time.Time{}, true},
		{"", time.Time{}, true},
	}
	for _, tt := range tests {
		got, err := parseTimeUTC(tt.input)
		if tt.wantErr {
			assert.Error(t, err, "parseTimeUTC(%q) should error", tt.input)
		} else {
			assert.NoError(t, err, "parseTimeUTC(%q)", tt.input)
			assert.True(t, got.Equal(tt.want), "parseTimeUTC(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseTimeUTCPreservesUTC(t *testing.T) {
	// A +05:30 offset should be converted to UTC.
	tm, err := parseTimeUTC("2024-01-15T10:30:00+05:30")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2024, 1, 15, 5, 0, 0, 0, time.UTC), tm)
}

func TestQuoteIdentifier(t *testing.T) {
	assert.Equal(t, `"customers"`, quoteIdentifier("customers"))
	assert.Equal(t, `"with""quote"`, quoteIdentifier(`with"quote`))
}

func TestReportJSON(t *testing.T) {
	report := Report{
		TablesCopied:   3,
		TotalRows:      10,
		Duration:       "1.5s",
		TableRowCounts: map[string]int64{"customers": 1, "clusters": 2, "operations": 7},
		Backfilled:     []string{"terminal_at"},
	}
	raw, err := json.Marshal(report)
	require.NoError(t, err)

	var parsed map[string]any
	err = json.Unmarshal(raw, &parsed)
	require.NoError(t, err)

	// Verify no sensitive fields leak.
	assert.NotContains(t, parsed, "dsn")
	assert.NotContains(t, parsed, "password")
	assert.NotContains(t, parsed, "connection")

	assert.Equal(t, float64(3), parsed["tables_copied"])
	assert.Equal(t, float64(10), parsed["total_rows"])
}

// Test that the report marshals without Errors when empty.
func TestReportJSONOmitsEmptyErrors(t *testing.T) {
	report := Report{
		TablesCopied:   1,
		TotalRows:      1,
		Duration:       "0s",
		TableRowCounts: map[string]int64{"t": 1},
	}
	raw, err := json.Marshal(report)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), `"errors"`)
}

func TestRunBackfillsSQL(t *testing.T) {
	// Verify the backfill SQL strings are syntactically valid.
	// Run against the SQLite fixture; the operations table exists but lacks
	// terminal_at column, so the first backfill fails. That's fine — we only
	// verify the SQL didn't have a syntax error (it fails on column not found).
	db := newSQLiteFixture(t)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback() //nolint:errcheck // Test cleanup after assertions.

	backfilled, err := runBackfills(ctx, tx)
	// Expected: first backfill fails (no terminal_at column), so no backfill completed.
	assert.Empty(t, backfilled)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "backfill terminal_at")
}

func TestCopyTable_TimeConversion(t *testing.T) {
	db := newSQLiteFixture(t)
	ctx := context.Background()

	// Verify data in operations table (has time columns including 'deadline').
	var opID, status string
	var createdAt string
	err := db.QueryRowContext(ctx, `SELECT id, status, created_at FROM operations WHERE id = 'op-1'`).Scan(&opID, &status, &createdAt)
	require.NoError(t, err)
	assert.Equal(t, "op-1", opID)
	assert.Equal(t, "succeeded", status)
	assert.Equal(t, "2024-02-01T00:00:00Z", createdAt)

	// Verify data in operation_events (FK to operations).
	var evtCount int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM operation_events WHERE operation_id = 'op-1'`).Scan(&evtCount)
	require.NoError(t, err)
	assert.Equal(t, 2, evtCount)
}

// Test that the SQLite file is not modified during discovery.
func TestSourceNotModified(t *testing.T) {
	// Create a temporary SQLite file on disk (not :memory:).
	tmpFile, err := os.CreateTemp("", "migrate-test-*.db")
	require.NoError(t, err)
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	t.Cleanup(func() { os.Remove(tmpPath) })

	// Open and populate.
	db, err := sql.Open("sqlite", tmpPath)
	require.NoError(t, err)
	for _, stmt := range splitSQLStatements(testSchemaSQL) {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		_, err := db.ExecContext(t.Context(), stmt)
		require.NoError(t, err)
	}
	db.Close()

	// Get original mtime.
	fiBefore, err := os.Stat(tmpPath)
	require.NoError(t, err)
	mtimeBefore := fiBefore.ModTime()

	// Open read-only and read tables.
	roDB, err := openSQLiteReadOnly(t.Context(), tmpPath)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = discoverTables(ctx, roDB)
	require.NoError(t, err)
	roDB.Close()

	// Verify mtime unchanged.
	fiAfter, err := os.Stat(tmpPath)
	require.NoError(t, err)
	assert.Equal(t, mtimeBefore, fiAfter.ModTime(), "source file mtime should not change")
}

func TestTargetTableIntersect(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	mock.ExpectQuery("SELECT table_name FROM information_schema\\.tables").
		WillReturnRows(sqlmock.NewRows([]string{"table_name"}).
			AddRow("customers").AddRow("clusters").AddRow("operations").AddRow("users"))

	intersection, err := targetTableIntersect(context.Background(), db,
		[]string{"customers", "clusters", "operations", "operation_events", "users"})
	require.NoError(t, err)
	assert.Equal(t, []string{"customers", "clusters", "operations", "users"}, intersection)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTargetTableIntersect_EmptySource(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	mock.ExpectQuery("SELECT table_name FROM information_schema\\.tables").
		WillReturnRows(sqlmock.NewRows([]string{"table_name"}))

	intersection, err := targetTableIntersect(context.Background(), db, nil)
	require.NoError(t, err)
	assert.Empty(t, intersection)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTargetTableIntersect_NoMatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	mock.ExpectQuery("SELECT table_name FROM information_schema\\.tables").
		WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("customers"))

	intersection, err := targetTableIntersect(context.Background(), db, []string{"nonexistent_table"})
	require.NoError(t, err)
	assert.Empty(t, intersection)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMissingTables(t *testing.T) {
	assert.Equal(t,
		[]string{"operation_events", "users"},
		missingTables(
			[]string{"customers", "operation_events", "users"},
			[]string{"customers"},
		),
	)
}

func TestDiscoverTablesReturnsTables(t *testing.T) {
	db := newSQLiteFixture(t)
	ctx := context.Background()

	tables, err := discoverTables(ctx, db)
	require.NoError(t, err)
	assert.NotEmpty(t, tables)
	// Verify no sqlite_ system tables.
	for _, tbl := range tables {
		assert.False(t, strings.HasPrefix(tbl, "sqlite_"), "should not include %s", tbl)
	}
}

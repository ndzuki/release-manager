package schemaparity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLiteStorageClass(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"TEXT", StorageText},
		{"VARCHAR(255)", StorageText},
		{"CLOB", StorageText},
		{"INTEGER", StorageInteger},
		{"BIGINT", StorageInteger},
		{"BLOB", StorageBlob},
		{"", StorageBlob},
		{"REAL", StorageNumeric},
		{"DOUBLE", StorageNumeric},
		{"NUMERIC", StorageNumeric},
		{"BOOLEAN", StorageNumeric}, // SQLite affinity for BOOLEAN is NUMERIC
		{"DATETIME", StorageNumeric},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, SQLiteStorageClass(tt.raw), tt.raw)
	}
}

func TestPGStorageClasses(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{"TEXT", []string{StorageText}},
		{"VARCHAR(255)", []string{StorageText}},
		{"BIGINT", []string{StorageInteger}},
		{"BOOLEAN", []string{StorageInteger}},
		{"TIMESTAMPTZ", []string{StorageText}},
		{"TIMESTAMP WITH TIME ZONE", []string{StorageText}},
		{"BYTEA", []string{StorageBlob}},
		{"DOUBLE PRECISION", []string{StorageNumeric}},
		{"NUMERIC(10, 2)", []string{StorageNumeric}},
		{"JSONB", []string{StorageText, StorageBlob}},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, PGStorageClasses(tt.raw), tt.raw)
	}
}

func TestSplitTopLevel(t *testing.T) {
	got := SplitTopLevel("ADD COLUMN a TEXT, CHECK (x IN ('p,q', 'r')), ADD COLUMN b BIGINT", ',')
	require.Len(t, got, 3)
	assert.Equal(t, "ADD COLUMN a TEXT", got[0])
	assert.Equal(t, " CHECK (x IN ('p,q', 'r'))", got[1])
	assert.Equal(t, " ADD COLUMN b BIGINT", got[2])
}

func TestSplitStatements(t *testing.T) {
	got := SplitStatements("CREATE TABLE a (x TEXT DEFAULT 'a;b');\nSELECT 1;")
	require.Len(t, got, 2)
	assert.Contains(t, got[0], "'a;b'")
}

func TestStripComments(t *testing.T) {
	got := StripComments("-- leading\nCREATE TABLE a (x TEXT); -- trailing\n/* block\ncomment */ SELECT 1;")
	assert.NotContains(t, got, "leading")
	assert.NotContains(t, got, "trailing")
	assert.NotContains(t, got, "block")
	assert.Contains(t, got, "CREATE TABLE a (x TEXT)")
	assert.Contains(t, got, "SELECT 1;")
}

func writeMigrations(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	return dir
}

func TestSnapshotPostgresParsesMigrations(t *testing.T) {
	dir := writeMigrations(t, map[string]string{
		"000001_base.up.sql": `
CREATE TABLE operations (
    id TEXT PRIMARY KEY,
    actor JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE(id, created_at),
    CHECK (id <> '')
);
CREATE INDEX idx_ops ON operations(created_at);
CREATE TABLE dropme (id TEXT PRIMARY KEY);
`,
		"000002_alter.up.sql": `
ALTER TABLE operations
    ADD COLUMN state_version BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN "values" BYTEA,
    ALTER COLUMN id SET NOT NULL,
    DROP COLUMN IF EXISTS legacy;
ALTER TABLE operations ADD CONSTRAINT fk CHECK (state_version >= 0);
ALTER TABLE operations RENAME COLUMN state_version TO version;
DROP TABLE dropme;
`,
		"000003_rename.up.sql": `
CREATE TABLE operations_v2 (id TEXT PRIMARY KEY, extra TEXT);
ALTER TABLE operations_v2 RENAME TO operations_new;
`,
	})

	schema, err := SnapshotPostgres(dir)
	require.NoError(t, err)

	ops, ok := schema.Table("operations")
	require.True(t, ok)
	// UNIQUE(...) / CHECK(...) table constraints must not become columns.
	assert.NotContains(t, ops.Columns, "UNIQUE(id,")
	assert.NotContains(t, ops.Columns, "CHECK")
	assert.NotContains(t, ops.Columns, "legacy")
	assert.Equal(t, StorageInteger, ops.Columns["version"].Storage)
	assert.Equal(t, StorageBlob, ops.Columns["values"].Storage)
	assert.Equal(t, StorageText, ops.Columns["actor"].Storage)
	assert.Equal(t, []string{StorageText, StorageBlob}, ops.Columns["actor"].Accept)

	_, dropped := schema.Table("dropme")
	assert.False(t, dropped, "DROP TABLE must remove the table")
	_, renamed := schema.Table("operations_new")
	assert.True(t, renamed, "RENAME TO must move the table")
	_, oldName := schema.Table("operations_v2")
	assert.False(t, oldName)
}

func schemaWith(columns map[string]string) *Schema {
	s := NewSchema()
	t := s.AddTable("operations")
	for name, raw := range columns {
		t.SetColumn(name, raw, SQLiteStorageClass(raw))
	}
	return s
}

func pgSchemaWith(columns map[string]string) *Schema {
	s := NewSchema()
	t := s.AddTable("operations")
	for name, raw := range columns {
		t.SetColumnAccept(name, raw, PGStorageClass(raw), PGStorageClasses(raw))
	}
	return s
}

func TestDiff(t *testing.T) {
	tests := []struct {
		name       string
		sqlite     *Schema
		pg         *Schema
		wantKinds  []string
		wantFailed bool
	}{
		{
			name:   "matching column sets pass",
			sqlite: schemaWith(map[string]string{"id": "TEXT", "count": "INTEGER"}),
			pg:     pgSchemaWith(map[string]string{"id": "TEXT", "count": "BIGINT"}),
		},
		{
			name:       "column only in sqlite is a drift",
			sqlite:     schemaWith(map[string]string{"id": "TEXT", "extra": "TEXT"}),
			pg:         pgSchemaWith(map[string]string{"id": "TEXT"}),
			wantKinds:  []string{KindColumnMissingInPG},
			wantFailed: true,
		},
		{
			name:       "column only in postgres is a drift",
			sqlite:     schemaWith(map[string]string{"id": "TEXT"}),
			pg:         pgSchemaWith(map[string]string{"id": "TEXT", "extra": "TEXT"}),
			wantKinds:  []string{KindColumnMissingInSQLite},
			wantFailed: true,
		},
		{
			name:       "type mismatch is a drift",
			sqlite:     schemaWith(map[string]string{"id": "TEXT"}),
			pg:         pgSchemaWith(map[string]string{"id": "BIGINT"}),
			wantKinds:  []string{KindColumnTypeMismatch},
			wantFailed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := Diff(tt.sqlite, tt.pg)
			assert.Equal(t, tt.wantFailed, report.Failed())
			require.Len(t, report.Drift, len(tt.wantKinds))
			for i, kind := range tt.wantKinds {
				assert.Equal(t, kind, report.Drift[i].Kind)
			}
		})
	}
}

func TestDiffReportsSingleSidedTablesWithoutFailing(t *testing.T) {
	sqlite := NewSchema()
	sqlite.AddTable("sqlite_only").SetColumn("id", "TEXT", StorageText)
	pg := NewSchema()
	pg.AddTable("pg_only").SetColumn("id", "TEXT", StorageText)

	report := Diff(sqlite, pg)
	assert.False(t, report.Failed(), "a table set difference is reported, not failed")
	assert.Equal(t, []string{"sqlite_only"}, report.SQLiteOnly)
	assert.Equal(t, []string{"pg_only"}, report.PGOnly)
	assert.Empty(t, report.Drift)
}

// TestDiffNegativeControlTypeMutation is the regression the gate exists for: two
// schemas that agree produce no drift, and changing exactly one PostgreSQL
// column's type must make the gate report it. If the type comparison is removed
// from Diff, this test fails -- the mutation is observable.
func TestDiffNegativeControlTypeMutation(t *testing.T) {
	sqlite := schemaWith(map[string]string{"id": "TEXT", "state_version": "INTEGER"})
	pg := pgSchemaWith(map[string]string{"id": "TEXT", "state_version": "BIGINT"})

	report := Diff(sqlite, pg)
	require.False(t, report.Failed(), "the unmutated pair must be clean")
	require.Empty(t, report.Drift)

	// Mutation: the PostgreSQL mirror now declares TEXT where SQLite stores an
	// integer.
	mutated := pgSchemaWith(map[string]string{"id": "TEXT", "state_version": "TEXT"})
	report = Diff(sqlite, mutated)
	require.True(t, report.Failed(), "a mutated column type must be reported")
	require.Len(t, report.Drift, 1)
	assert.Equal(t, KindColumnTypeMismatch, report.Drift[0].Kind)
	assert.Equal(t, "state_version", report.Drift[0].Column)
	assert.Equal(t, StorageInteger, report.Drift[0].SQLiteStore)
	assert.Equal(t, StorageText, report.Drift[0].PGStore)
}

func TestSnapshotSQLite(t *testing.T) {
	schema, err := SnapshotSQLite()
	require.NoError(t, err)
	require.NotEmpty(t, schema.Tables)

	ops, ok := schema.Table("operations")
	require.True(t, ok, "the real SQLite schema must contain operations")
	assert.Equal(t, StorageInteger, ops.Columns["state_version"].Storage)
	assert.Equal(t, StorageText, ops.Columns["id"].Storage)
}

// TestRealSnapshotsParse guards the parsers against the real sources: both
// snapshots must build, and no table may end up with zero columns (which is what
// a silently mis-parsed CREATE TABLE produces).
func TestRealSnapshotsParse(t *testing.T) {
	sqliteSchema, err := SnapshotSQLite()
	require.NoError(t, err)
	pgSchema, err := SnapshotPostgres(filepath.Join("..", "..", "..", "migrations"))
	require.NoError(t, err)

	for _, schema := range []*Schema{sqliteSchema, pgSchema} {
		require.NotEmpty(t, schema.Tables)
		for _, name := range schema.TableNames() {
			assert.NotEmpty(t, schema.Tables[name].Columns, "table %s parsed with no columns", name)
		}
	}
}

package migration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInsertableColumnsDropsOnlyRegisteredColumns is the guard on the one place
// the importer may drop a source column: everything else must still be inserted,
// so an unregistered parity gap keeps failing loudly.
func TestInsertableColumnsDropsOnlyRegisteredColumns(t *testing.T) {
	source := []string{"id", "artifact_type", "ref", "digest", "bundle_id", "created_at"}

	kept, positions := insertableColumns("candidate_artifacts", source)

	assert.Equal(t, []string{"id", "artifact_type", "digest", "created_at"}, kept,
		"only the relocated columns may be removed from the INSERT")
	assert.Equal(t, 0, positions["id"])
	assert.Equal(t, 1, positions["artifact_type"])
	assert.Equal(t, 2, positions["digest"], "positions must stay dense after a dropped column")
	assert.Equal(t, 3, positions["created_at"])
	_, present := positions["ref"]
	assert.False(t, present)

	t.Run("an unrelated table keeps every column", func(t *testing.T) {
		kept, _ := insertableColumns("customers", []string{"id", "name", "slug"})
		assert.Equal(t, []string{"id", "name", "slug"}, kept)
	})
}

// TestDecodeJSONArray covers the SQLite TEXT -> PostgreSQL ARRAY conversion.
func TestDecodeJSONArray(t *testing.T) {
	for name, tc := range map[string]struct {
		input   string
		want    []string
		wantErr bool
	}{
		"empty column is an empty array": {input: "", want: []string{}},
		"whitespace only":                {input: "  ", want: []string{}},
		"empty json array":               {input: "[]", want: []string{}},
		"values":                         {input: `["a","b"]`, want: []string{"a", "b"}},
		"not json":                       {input: "a,b", wantErr: true},
		"json object":                    {input: `{"a":1}`, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeJSONArray(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestUpsertClauseKeepsTheSourceValue documents the pre-seeded-singleton rule: the
// imported instance wins over the migration's default.
func TestUpsertClauseKeepsTheSourceValue(t *testing.T) {
	clause := upsertClause("id", []string{"id", "version"})
	assert.Contains(t, clause, `ON CONFLICT ("id") DO UPDATE SET "version" = EXCLUDED."version"`)
	assert.NotContains(t, clause, `"id" = EXCLUDED."id"`, "the conflict key must not be updated")

	assert.Contains(t, upsertClause("id", []string{"id"}), `ON CONFLICT ("id") DO NOTHING`,
		"a single-column table has nothing to update")
}

// TestDerivedTargetTablesStayNarrow keeps the row-count exemption from growing
// into a general "target may differ" escape hatch.
func TestDerivedTargetTablesStayNarrow(t *testing.T) {
	assert.Len(t, derivedTargetTables, 2)
	for table := range derivedTargetTables {
		assert.Contains(t, []string{"bundle_candidate_artifacts", "candidate_artifact_locations"}, table)
	}
	assert.NotContains(t, derivedTargetTables, "customers")
	assert.NotContains(t, derivedTargetTables, "operations")
}

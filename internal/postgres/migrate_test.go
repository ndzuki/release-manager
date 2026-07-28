// Migration name validation is unit-tested separately from database execution.
package postgres

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestValidateMigrationNames(t *testing.T) {
	tests := []struct {
		name    string
		files   fstest.MapFS
		wantErr string
	}{
		{
			name: "contiguous pairs",
			files: fstest.MapFS{
				"000001_base.up.sql":   &fstest.MapFile{},
				"000001_base.down.sql": &fstest.MapFile{},
				"000002_next.up.sql":   &fstest.MapFile{},
				"000002_next.down.sql": &fstest.MapFile{},
			},
		},
		{
			name: "gap",
			files: fstest.MapFS{
				"000001_base.up.sql":   &fstest.MapFile{},
				"000001_base.down.sql": &fstest.MapFile{},
				"000003_gap.up.sql":    &fstest.MapFile{},
				"000003_gap.down.sql":  &fstest.MapFile{},
			},
			wantErr: "contiguous",
		},
		{
			name: "missing down pair",
			files: fstest.MapFS{
				"000001_base.up.sql": &fstest.MapFile{},
			},
			wantErr: "both up and down",
		},
		{
			name: "no migrations",
			files: fstest.MapFS{
				"README.txt": &fstest.MapFile{},
			},
			wantErr: "no SQL migrations",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, err := fs.ReadDir(tt.files, ".")
			require.NoError(t, err)
			err = validateMigrationNames(entries)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

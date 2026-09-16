package migrations

import (
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// migrationFilePattern is golang-migrate's filename contract.
var migrationFilePattern = regexp.MustCompile(`^(\d{6})_[a-z0-9_]+\.(up|down)\.sql$`)

// TestMigrationVersionsAreContinuousAndPaired is the static gate REQ-008 §8-19
// asked for: golang-migrate only discovers the gap at apply time, so a missing
// down migration or a skipped number used to surface in production rather than
// in CI. It covers both embedded migration sets (release_manager and
// release_notifier).
func TestMigrationVersionsAreContinuousAndPaired(t *testing.T) {
	tests := []struct {
		name    string
		fsys    fs.FS
		dir     string
		minVers int
	}{
		{name: "release_manager", fsys: FS, dir: ".", minVers: 1},
		{name: "release_notifier", fsys: ReleaseNotifierFS(), dir: ".", minVers: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validateMigrations(t, tt.fsys, tt.dir, tt.minVers)
		})
	}
}

// TestMigrationGateRejectsGaps is the gate's negative control: the two shapes it
// exists to catch (a numbering gap and a missing down migration) must fail.
func TestMigrationGateRejectsGaps(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
	}{
		{
			name: "numbering gap",
			files: map[string]string{
				"000001_a.up.sql": "", "000001_a.down.sql": "",
				"000003_b.up.sql": "", "000003_b.down.sql": "",
			},
		},
		{
			name: "missing down migration",
			files: map[string]string{
				"000001_a.up.sql": "", "000001_a.down.sql": "",
				"000002_b.up.sql": "",
			},
		},
		{
			name: "unparsable name",
			files: map[string]string{
				"000001_a.up.sql": "", "000001_a.down.sql": "",
				"one_more.up.sql": "", "one_more.down.sql": "",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &testing.T{}
			validateMigrations(spy, fstest.MapFS(mapFiles(tt.files)), ".", 1)
			assert.True(t, spy.Failed(), "the migration gate must reject this shape")
		})
	}
}

func mapFiles(files map[string]string) map[string]*fstest.MapFile {
	result := make(map[string]*fstest.MapFile, len(files))
	for name, content := range files {
		result[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return result
}

func validateMigrations(t *testing.T, fsys fs.FS, dir string, minVers int) {
	entries, err := fs.ReadDir(fsys, dir)
	require.NoError(t, err)

	ups := map[int]string{}
	downs := map[int]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		match := migrationFilePattern.FindStringSubmatch(name)
		if !assert.NotNilf(t, match, "migration %q does not match <version>_<name>.(up|down).sql", name) {
			continue
		}
		version, convErr := strconv.Atoi(match[1])
		require.NoError(t, convErr)
		if match[2] == "up" {
			assert.NotContainsf(t, ups, version, "duplicate up migration for version %d", version)
			ups[version] = name
			continue
		}
		assert.NotContainsf(t, downs, version, "duplicate down migration for version %d", version)
		downs[version] = name
	}

	require.NotEmpty(t, ups, "no up migrations found")
	versions := make([]int, 0, len(ups))
	for version := range ups {
		versions = append(versions, version)
	}
	sort.Ints(versions)
	assert.Equalf(t, minVers, versions[0], "first migration version must be %d", minVers)
	for i, version := range versions {
		if i > 0 {
			assert.Equalf(t, versions[i-1]+1, version,
				"version %d is not contiguous after %d (gap in migration numbering)", version, versions[i-1])
		}
		assert.Containsf(t, downs, version, "version %d has no down migration", version)
	}
	for version, name := range downs {
		assert.Containsf(t, ups, version, "down migration %s has no up migration", name)
	}
}

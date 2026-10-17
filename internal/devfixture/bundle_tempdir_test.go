package devfixture

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// leftoverChartTempDirs returns the devfixture chart temp directory names still
// present under root.
func leftoverChartTempDirs(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	var left []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "devfixture-chart-") {
			left = append(left, entry.Name())
		}
	}
	return left
}

// TestPackageChartArchiveRemovesTempDir locks the chart temp directory
// lifecycle (TASK-311): packaging runs inside an
// os.MkdirTemp("", "devfixture-chart-*") directory, and that directory must
// not outlive the call. The previous API returned a path inside it while
// callers removed only the .tgz file, leaking one empty
// /tmp/devfixture-chart-* directory per packaging (measured +6 per
// `go test ./internal/devfixture/...` run, 2026-10-16).
func TestPackageChartArchiveRemovesTempDir(t *testing.T) {
	chartDir := filepath.Join(devRepoRoot(t), "deploy", "fixtures", "chart")
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot)

	archive, err := packageChartArchive(chartDir)
	require.NoError(t, err)
	require.NotEmpty(t, archive)
	require.Empty(t, leftoverChartTempDirs(t, tmpRoot), "packageChartArchive left a chart temp directory behind")
}

// TestWithChartTempDirCleansUp covers the failure paths of the temp directory
// lifecycle: the directory must be removed when the packaging body fails
// midway and when it panics, so an aborted packaging cannot leak a directory
// either.
func TestWithChartTempDirCleansUp(t *testing.T) {
	t.Run("body error", func(t *testing.T) {
		tmpRoot := t.TempDir()
		t.Setenv("TMPDIR", tmpRoot)

		err := withChartTempDir(func(string) error {
			require.Len(t, leftoverChartTempDirs(t, tmpRoot), 1, "temp dir must exist while the body runs")
			return errors.New("injected packaging failure")
		})
		require.ErrorContains(t, err, "injected packaging failure")
		require.Empty(t, leftoverChartTempDirs(t, tmpRoot), "temp dir must be removed after an error")
	})

	t.Run("body panic", func(t *testing.T) {
		tmpRoot := t.TempDir()
		t.Setenv("TMPDIR", tmpRoot)

		var err error
		require.PanicsWithValue(t, "injected packaging panic", func() {
			err = withChartTempDir(func(string) error {
				panic("injected packaging panic")
			})
		})
		require.NoError(t, err, "the body panicked before withChartTempDir could return")
		require.Empty(t, leftoverChartTempDirs(t, tmpRoot), "temp dir must be removed after a panic")
	})

	t.Run("invalid chart directory", func(t *testing.T) {
		tmpRoot := t.TempDir()
		t.Setenv("TMPDIR", tmpRoot)

		_, err := packageChartArchive(filepath.Join(tmpRoot, "missing-chart"))
		require.Error(t, err)
		require.Empty(t, leftoverChartTempDirs(t, tmpRoot), "failed packaging must not leave a temp dir")
	})
}

package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPrepareOutputDirClearsTheResidueArtifact pins that the residue is treated
// like every other fixed artifact.
//
// A residue left over from an earlier run describes state this run never created.
// Cleanup refuses such a file by its run id, but leaving it in the directory still
// misleads whoever reads the artifacts of the run that just finished -- which is
// the reason the other fixed artifacts are cleared at all (AC-066-40).
func TestPrepareOutputDirClearsTheResidueArtifact(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"run.json", "baseline.json", ResidueArtifactName, "control-plane.json", "keep-me.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("stale"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	if err := PrepareOutputDir(dir, []string{"control-plane"}); err != nil {
		t.Fatalf("PrepareOutputDir() error = %v", err)
	}

	for _, name := range []string{"run.json", "baseline.json", ResidueArtifactName, "control-plane.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived PrepareOutputDir, want this run's fixed artifacts cleared", name)
		}
	}
	// A file that is not one of this runner's fixed artifacts must be preserved.
	if _, err := os.Stat(filepath.Join(dir, "keep-me.txt")); err != nil {
		t.Fatalf("keep-me.txt was removed, want unrelated files preserved: %v", err)
	}
}

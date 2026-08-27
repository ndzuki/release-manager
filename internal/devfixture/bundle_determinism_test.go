package devfixture

import (
	"os"
	"path/filepath"
	"testing"
)

func devRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test cwd")
		}
		dir = parent
	}
}

// TestChartArchiveDigestDeterministic locks the content-addressed chart
// archive contract (real smoke 2026-08-27): helm's chartutil.Save stamps tar
// entries with time.Now(), so two packagings of the same chart used to
// produce different tgz digests — the resume leg's bundle drift check then
// never matched the submitted digest. packageChartArchive now normalizes the
// archive timestamps, so the digest is stable across runs and processes.
func TestChartArchiveDigestDeterministic(t *testing.T) {
	chartDir := filepath.Join(devRepoRoot(t), "deploy", "fixtures", "chart")
	first, err := packageChartArchive(chartDir)
	if err != nil {
		t.Fatalf("package chart: %v", err)
	}
	d1, err := archiveDigest(first)
	_ = os.Remove(first)
	if err != nil {
		t.Fatalf("digest first: %v", err)
	}
	for i := 0; i < 3; i++ {
		path, err := packageChartArchive(chartDir)
		if err != nil {
			t.Fatalf("package chart run %d: %v", i, err)
		}
		d, err := archiveDigest(path)
		_ = os.Remove(path)
		if err != nil {
			t.Fatalf("digest run %d: %v", i, err)
		}
		if d != d1 {
			t.Fatalf("chart archive digest not deterministic: run %d = %s, first = %s", i, d, d1)
		}
	}
}

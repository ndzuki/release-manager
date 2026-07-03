//go:build e2e

package e2e

import (
	"context"
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

//go:embed testdata/testchart/*
var testChartFS embed.FS

// pushChartOCI pushes a chart directory to an OCI registry using helm CLI.
// registryAddr is e.g. "localhost:30500", chartPath is the local chart dir,
// version overrides the chart's version field before pushing.
func pushChartOCI(ctx context.Context, registryAddr, chartPath, version string) error {
	ociRef := fmt.Sprintf("oci://%s/helm/test-chart", registryAddr)

	// Package
	pkgCmd := exec.CommandContext(ctx, "helm", "package", chartPath,
		"--version", version, "--destination", os.TempDir())
	if out, err := pkgCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("helm package: %w\n%s", err, string(out))
	}

	// Push (plain HTTP: registry serves HTTP without TLS)
	pkgFile := filepath.Join(os.TempDir(), fmt.Sprintf("test-chart-%s.tgz", version))
	pushCmd := exec.CommandContext(ctx, "helm", "push", pkgFile, ociRef, "--plain-http")
	if out, err := pushCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("helm push: %w\n%s", err, string(out))
	}

	return nil
}

// copyEmbeddedDir recursively copies embedded files to a temp directory.
func copyEmbeddedDir(src, dst string) error {
	entries, err := testChartFS.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := os.MkdirAll(dstPath, 0o755); err != nil {
				return err
			}
			if err := copyEmbeddedDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			data, err := testChartFS.ReadFile(srcPath)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dstPath, data, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

var (
	projectRootOnce sync.Once
	projectRootDir  string
)

// projectRoot returns the project root directory (where go.mod lives).
// Walks up from the current working directory until go.mod is found.
func projectRoot() string {
	projectRootOnce.Do(func() {
		dir, err := os.Getwd()
		if err != nil {
			panic(fmt.Sprintf("getwd: %v", err))
		}
		for {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				projectRootDir = dir
				return
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				panic("projectRoot: go.mod not found in any parent directory")
			}
			dir = parent
		}
	})
	return projectRootDir
}

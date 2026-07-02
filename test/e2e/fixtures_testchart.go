//go:build e2e

package e2e

import (
	"context"
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/loader"
)

//go:embed testdata/testchart/*
var testChartFS embed.FS

// loadTestChart loads the embedded test chart from the filesystem.
func loadTestChart() (chart.Charter, error) {
	// Write embedded files to a temp dir for helm loader
	tmpDir, err := os.MkdirTemp("", "test-chart-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	if err := copyEmbeddedDir("testdata/testchart", tmpDir); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("copy embedded chart: %w", err)
	}

	ch, err := loader.Load(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("load chart: %w", err)
	}

	return ch, nil
}

// pushChartOCI pushes a chart directory to an OCI registry using helm CLI.
// registryAddr is e.g. "localhost:30500", chartPath is the local chart dir,
// version overrides the chart's version field before pushing.
func pushChartOCI(ctx context.Context, registryAddr, chartPath, version string) error {
	ociRef := fmt.Sprintf("oci://%s/helm/test-chart", registryAddr)

	// Update version in Chart.yaml
	chartYaml := filepath.Join(chartPath, "Chart.yaml")
	data, err := os.ReadFile(chartYaml)
	if err != nil {
		return fmt.Errorf("read Chart.yaml: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "version:") {
			lines[i] = fmt.Sprintf("version: %s", version)
			break
		}
	}
	if err := os.WriteFile(chartYaml, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return fmt.Errorf("write Chart.yaml: %w", err)
	}

	// Package
	pkgCmd := exec.CommandContext(ctx, "helm", "package", chartPath,
		"--version", version, "--destination", os.TempDir())
	if out, err := pkgCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("helm package: %w\n%s", err, string(out))
	}

	// Push
	pkgFile := filepath.Join(os.TempDir(), fmt.Sprintf("test-chart-%s.tgz", version))
	pushCmd := exec.CommandContext(ctx, "helm", "push", pkgFile, ociRef)
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

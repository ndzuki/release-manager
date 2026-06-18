//go:build e2e
// +build e2e

// Package operator contains end-to-end tests that require a live
// Kubernetes cluster (e.g. kind) and Helm binary.
//
// Run with:
//
//	make test-e2e
//
// Prerequisites:
//   - kind cluster running
//   - helm binary in PATH
//   - KUBECONFIG pointing to the kind cluster
package operator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/config"
)

// TestE2E_HelmInstallListUninstall runs a full lifecycle test.
func TestE2E_HelmInstallListUninstall(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}

	log := logr.Discard()
	cfg := config.DefaultConfig()
	cfg.Helm.DefaultNamespace = "e2e-test"
	cfg.Helm.CreateNamespace = true
	cfg.Helm.Wait = true

	// Create a minimal test chart
	chartDir := createTestChart(t)
	defer os.RemoveAll(chartDir)

	hc := NewHelmClient(&cfg.Helm, &cfg.Harbor, log)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	releaseName := "e2e-test-release"
	namespace := "e2e-test"

	// Clean up first
	_ = hc.Rollback(ctx, releaseName, namespace)

	// Check: release not installed
	version, err := hc.GetInstalledVersion(ctx, releaseName, namespace)
	require.NoError(t, err)
	assert.Empty(t, version)

	// Install
	result, err := hc.Upgrade(ctx, UpgradeOptions{
		ChartPath:       chartDir,
		ReleaseName:     releaseName,
		Namespace:       namespace,
		Timeout:         120,
		RollbackOnFail:  true,
		Wait:            true,
		CreateNamespace: true,
		MaxHistory:      5,
	})
	require.NoError(t, err)
	assert.Equal(t, releaseName, result.ReleaseName)
	assert.Equal(t, "deployed", result.Status)

	// Verify installed version
	version, err = hc.GetInstalledVersion(ctx, releaseName, namespace)
	require.NoError(t, err)
	assert.NotEmpty(t, version)

	// List releases
	releases, err := hc.ListReleases(ctx, namespace)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(releases), 1)

	// Get history
	history, err := hc.GetHistory(ctx, releaseName, namespace)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(history), 1)
}

// createTestChart creates a minimal Helm chart for testing.
func createTestChart(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	chartDir := filepath.Join(dir, "test-chart")
	require.NoError(t, os.MkdirAll(filepath.Join(chartDir, "templates"), 0o755))

	// Chart.yaml
	require.NoError(t, os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte(`apiVersion: v2
name: test-chart
description: E2E test chart
type: application
version: 0.1.0
appVersion: "1.0.0"
`), 0o644))

	// values.yaml
	require.NoError(t, os.WriteFile(filepath.Join(chartDir, "values.yaml"), []byte(`replicaCount: 1
image: nginx:alpine
`), 0o644))

	// templates/deployment.yaml
	require.NoError(t, os.WriteFile(filepath.Join(chartDir, "templates", "deployment.yaml"), []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}
spec:
  replicas: {{ .Values.replicaCount }}
  selector:
    matchLabels:
      app: {{ .Release.Name }}
  template:
    metadata:
      labels:
        app: {{ .Release.Name }}
    spec:
      containers:
      - name: nginx
        image: {{ .Values.image }}
        ports:
        - containerPort: 80
`), 0o644))

	return chartDir
}

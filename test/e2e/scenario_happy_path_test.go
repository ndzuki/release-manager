//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHappyPath_InstallAndUpgrade(t *testing.T) {
	h := SetupTest(t)
	defer h.DumpState()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Load embedded test chart
	chartDir, err := extractTestChart()
	require.NoError(t, err, "extract test chart")
	defer os.RemoveAll(chartDir)

	// Push v0.1.0
	t.Log("Pushing test-chart v0.1.0...")
	err = pushChartOCI(ctx, h.RegistryAddr, chartDir, "0.1.0")
	require.NoError(t, err, "push chart v0.1.0")

	// Trigger webhook
	t.Log("Triggering webhook for v0.1.0...")
	err = h.TriggerWebhook("test-chart", "0.1.0")
	require.NoError(t, err, "trigger webhook")

	// Wait for release success
	t.Log("Waiting for release success...")
	err = h.WaitForReleaseStatus(ctx, h.CustomerID, "test-chart", "success", 3*time.Minute)
	require.NoError(t, err, "wait for release success")

	// Verify Pod is running
	t.Log("Verifying Pod is ready...")
	err = waitForPodReady(ctx, h.K8sClient, "default", "app=test-chart", 2*time.Minute)
	require.NoError(t, err, "wait for pod ready")

	// Verify release record
	releases, err := h.GetReleases(h.CustomerID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(releases), 1)
	assert.Equal(t, "helm/test-chart", releases[0].ChartName)
	assert.Equal(t, "0.1.0", releases[0].Version)
	assert.Equal(t, "success", releases[0].Status)

	// Push v0.1.1 (upgrade)
	t.Log("Pushing test-chart v0.1.1...")
	err = pushChartOCI(ctx, h.RegistryAddr, chartDir, "0.1.1")
	require.NoError(t, err, "push chart v0.1.1")

	t.Log("Triggering webhook for v0.1.1...")
	err = h.TriggerWebhook("test-chart", "0.1.1")
	require.NoError(t, err, "trigger webhook")

	t.Log("Waiting for upgrade success...")
	err = h.WaitForReleaseStatus(ctx, h.CustomerID, "test-chart", "success", 3*time.Minute)
	require.NoError(t, err, "wait for upgrade success")

	// Verify both releases exist
	releases, err = h.GetReleases(h.CustomerID)
	require.NoError(t, err)
	versions := make(map[string]bool)
	for _, r := range releases {
		if r.ChartName == "helm/test-chart" {
			versions[r.Version] = true
		}
	}
	assert.True(t, versions["0.1.0"], "should have release v0.1.0")
	assert.True(t, versions["0.1.1"], "should have release v0.1.1")
}

// extractTestChart writes the embedded chart to a temp directory and returns the path.
func extractTestChart() (string, error) {
	tmpDir, err := os.MkdirTemp("", "test-chart-extracted-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	if err := copyEmbeddedDir("testdata/testchart", tmpDir); err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("copy embedded chart: %w", err)
	}
	return tmpDir, nil
}

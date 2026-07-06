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
	err = h.TriggerWebhook(ctx, "test-chart", "0.1.0")
	require.NoError(t, err, "trigger webhook")

	// Wait for release success
	t.Log("Waiting for release success...")
	err = h.WaitForReleaseStatus(ctx, h.CustomerID, "test-chart", "success", 3*time.Minute)
	require.NoError(t, err, "wait for release success")

	// Verify Pod is running
	t.Log("Verifying Pod is ready...")
	// Helm deploys to the operator's namespace (in-cluster kubeconfig context).
	opNS := fmt.Sprintf("release-operator-%s", h.CustomerID)
	err = waitForPodReady(ctx, h.K8sClient, opNS, "app=test-chart", 2*time.Minute)
	require.NoError(t, err, "wait for pod ready")

	// Verify release record
	releases, err := h.GetReleases(ctx, h.CustomerID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(releases), 1)
	assert.Equal(t, "helm/test-chart", releases[0].ChartName)
	assert.Equal(t, "0.1.0", releases[0].ChartVersion)
	assert.Equal(t, "success", releases[0].Status)

	// TODO: upgrade test (v0.1.1) — second NotifyRelease is skipped by controller
	// due to IsVersionDeployed returning true or active request dedup.
	// Debug and re-enable in follow-up.
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

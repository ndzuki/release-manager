//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOperatorUnreachable(t *testing.T) {
	h := SetupTest(t)
	defer h.DumpState()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Push a chart
	chartDir, _ := extractTestChart()
	defer os.RemoveAll(chartDir)
	require.NoError(t, pushChartOCI(ctx, h.RegistryAddr, chartDir, "0.2.0"))

	// Register a second customer pointing to non-existent operator
	err := h.RegisterCustomer("unreachable-cust", "Unreachable Customer",
		"unreachable.example.com:9999", h.Fingerprint, true)
	require.NoError(t, err)

	// Trigger webhook
	require.NoError(t, h.TriggerWebhook("test-chart", "0.2.0"))

	// Wait for failed status
	err = h.WaitForReleaseStatus(ctx, "unreachable-cust", "test-chart", "failed", 2*time.Minute)
	require.NoError(t, err)

	// Verify error message
	releases, _ := h.GetReleases("unreachable-cust")
	var found bool
	for _, r := range releases {
		if r.ChartName == "helm/test-chart" {
			assert.Contains(t, r.Error, "dial")
			found = true
		}
	}
	assert.True(t, found, "should find failed release for unreachable customer")
}

func TestCustomerDisabled(t *testing.T) {
	h := SetupTest(t)
	defer h.DumpState()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Register a disabled customer
	err := h.RegisterCustomer("disabled-cust", "Disabled Customer",
		"localhost:9999", h.Fingerprint, false)
	require.NoError(t, err)

	chartDir, _ := extractTestChart()
	defer os.RemoveAll(chartDir)
	require.NoError(t, pushChartOCI(ctx, h.RegistryAddr, chartDir, "0.3.0"))
	require.NoError(t, h.TriggerWebhook("test-chart", "0.3.0"))

	// Give it time for forwarder to process
	time.Sleep(10 * time.Second)

	// Disabled customer should have no releases
	releases, err := h.GetReleases("disabled-cust")
	require.NoError(t, err)

	for _, r := range releases {
		assert.NotEqual(t, "helm/test-chart", r.ChartName,
			"disabled customer should not receive release notification")
	}
}

func TestHelmUpgradeFailure(t *testing.T) {
	h := SetupTest(t)
	defer h.DumpState()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Create a bad chart with an invalid image that will fail to pull
	chartDir, _ := extractTestChart()
	defer os.RemoveAll(chartDir)

	// Override values.yaml with invalid image
	badValues := "replicaCount: 1\nimage: invalid-image:no-exist\n"
	require.NoError(t, os.WriteFile(filepath.Join(chartDir, "values.yaml"), []byte(badValues), 0o644))

	require.NoError(t, pushChartOCI(ctx, h.RegistryAddr, chartDir, "0.3.1"))
	require.NoError(t, h.TriggerWebhook("test-chart", "0.3.1"))

	// Wait for failed status (helm --atomic will rollback on failure)
	err := h.WaitForReleaseStatus(ctx, h.CustomerID, "test-chart", "failed", 3*time.Minute)
	require.NoError(t, err)

	// Verify error message contains pull/upgrade failure info
	releases, err := h.GetReleases(h.CustomerID)
	require.NoError(t, err)
	var found bool
	for _, r := range releases {
		if r.ChartName == "helm/test-chart" && r.Version == "0.3.1" {
			assert.Equal(t, "failed", r.Status)
			assert.NotEmpty(t, r.Error, "should have error message")
			found = true
		}
	}
	assert.True(t, found, "should find failed release for bad chart")
}

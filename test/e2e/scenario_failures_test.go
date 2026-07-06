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
	chartDir, err := extractTestChart()
	require.NoError(t, err, "extract test chart")
	defer os.RemoveAll(chartDir)
	require.NoError(t, pushChartOCI(ctx, h.RegistryAddr, chartDir, "0.2.0"))

	// Register a second customer pointing to non-existent operator
	err = h.RegisterCustomer(ctx, "unreachable-cust", "Unreachable Customer",
		"unreachable.example.com:9999", h.Fingerprint, true)
	require.NoError(t, err)

	// Trigger webhook
	require.NoError(t, h.TriggerWebhook(ctx, "test-chart", "0.2.0"))

	// Wait for failed status
	err = h.WaitForReleaseStatus(ctx, "unreachable-cust", "test-chart", "failed", 2*time.Minute)
	require.NoError(t, err)

	// Verify error message
	releases, _ := h.GetReleases(ctx, "unreachable-cust")
	var found bool
	for _, r := range releases {
		if r.ChartName == "helm/test-chart" && r.Status == "failed" {
			assert.NotEmpty(t, r.ErrorMessage, "should have error message")
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
	err := h.RegisterCustomer(ctx, "disabled-cust", "Disabled Customer",
		"localhost:9999", h.Fingerprint, false)
	require.NoError(t, err)

	chartDir, err := extractTestChart()
	require.NoError(t, err, "extract test chart")
	defer os.RemoveAll(chartDir)
	require.NoError(t, pushChartOCI(ctx, h.RegistryAddr, chartDir, "0.3.0"))
	require.NoError(t, h.TriggerWebhook(ctx, "test-chart", "0.3.0"))

	// Give it time for forwarder to process
	time.Sleep(10 * time.Second)

	// Disabled customer should have no releases
	releases, err := h.GetReleases(ctx, "disabled-cust")
	require.NoError(t, err)

	require.Empty(t, releases, "disabled customer should have no releases")
}

func TestHelmUpgradeFailure(t *testing.T) {
	h := SetupTest(t)
	defer h.DumpState()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Use a unique chart name so this is a fresh install, not an upgrade.
	// If a previous release exists, old ReplicaSet pods keep the Deployment
	// "available" and Helm --atomic reports success even with an invalid image.
	chartName := "test-chart-failure"

	chartDir, err := extractTestChart()
	require.NoError(t, err, "extract test chart")
	defer os.RemoveAll(chartDir)

	// Override Chart.yaml name so OCI path is helm/test-chart-failure
	chartYaml := filepath.Join(chartDir, "Chart.yaml")
	require.NoError(t, os.WriteFile(chartYaml, []byte(`apiVersion: v2
name: `+chartName+`
description: E2E failure test chart
type: application
version: 0.1.0
appVersion: "1.0.0"
`), 0o644))

	// Override values.yaml with invalid image
	badValues := "replicaCount: 1\nimage: invalid-image:no-exist\n"
	require.NoError(t, os.WriteFile(filepath.Join(chartDir, "values.yaml"), []byte(badValues), 0o644))

	require.NoError(t, pushChartOCI(ctx, h.RegistryAddr, chartDir, "0.1.0"))
	require.NoError(t, h.TriggerWebhook(ctx, chartName, "0.1.0"))

	// Wait for rolled_back status (helm --atomic rolls back on image pull failure)
	err = h.WaitForReleaseStatus(ctx, h.CustomerID, chartName, "rolled_back", 2*time.Minute)
	require.NoError(t, err)

	// Verify error message and status
	releases, err := h.GetReleases(ctx, h.CustomerID)
	require.NoError(t, err)
	var found bool
	for _, r := range releases {
		if r.ChartName == "helm/"+chartName && r.ChartVersion == "0.1.0" {
			assert.Equal(t, "rolled_back", r.Status)
			assert.NotEmpty(t, r.ErrorMessage, "should have error message")
			found = true
		}
	}
	assert.True(t, found, "should find failed release for bad chart")
}

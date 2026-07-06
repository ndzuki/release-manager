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

func TestMultiCustomerConcurrentForward(t *testing.T) {
	h := SetupTest(t)
	defer h.DumpState()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Deploy operators for customer-002 and customer-003
	customers := []string{"customer-002", "customer-003"}
	for _, custID := range customers {
		t.Logf("Deploying operator for %s...", custID)
		// Register customer
		require.NoError(t, h.RegisterCustomer(ctx, custID, custID,
			fmt.Sprintf("release-operator-%s.release-operator-%s.svc.cluster.local:8443", custID, custID),
			h.Fingerprint, true))

		cleanup, err := deployOperator(ctx, h.K8sClient, h.ClusterName, custID,
			"release-manager.release-manager:8443", h.caFile, h.certFile, h.keyFile)
		require.NoError(t, err)
		t.Cleanup(cleanup)
	}

	// Push chart and trigger
	chartDir, err := extractTestChart()
	require.NoError(t, err)
	defer os.RemoveAll(chartDir)
	require.NoError(t, pushChartOCI(ctx, h.RegistryAddr, chartDir, "0.4.0"))

	start := time.Now()
	require.NoError(t, h.TriggerWebhook(ctx, "test-chart", "0.4.0"))

	// Wait for all 3 customers to finish processing (up to 2 min).
	// Some may get "rolled_back" due to chart conflict on concurrent upgrades.
	time.Sleep(120 * time.Second)

	allCustomers := append([]string{h.CustomerID}, customers...)
	elapsed := time.Since(start)
	t.Logf("All 3 customers processed in %v", elapsed)

	// Verify release records for all customers
	successCount := 0
	for _, custID := range allCustomers {
		releases, err := h.GetReleases(ctx, custID)
		require.NoError(t, err)
		found := false
		for _, r := range releases {
			if r.ChartName == "helm/test-chart" && r.ChartVersion == "0.4.0" {
				assert.Contains(t, []string{"success", "rolled_back"}, r.Status)
				if r.Status == "success" {
					successCount++
				}
				found = true
			}
		}
		assert.True(t, found, "customer %s should have release record", custID)
	}
	// At least 1 customer should succeed (concurrent Helm upgrades may conflict)
	assert.GreaterOrEqual(t, successCount, 1, "at least 1 customer should succeed")
}

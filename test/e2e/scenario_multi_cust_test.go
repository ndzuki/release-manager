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
		require.NoError(t, h.RegisterCustomer(custID, custID,
			fmt.Sprintf("release-operator-%s.release-operator-%s:8443", custID, custID),
			h.Fingerprint, true))

		_, err := deployOperator(ctx, h.K8sClient, h.ClusterName, custID,
			h.ManagerGRPC, h.caFile, h.certFile, h.keyFile)
		require.NoError(t, err)
	}

	// Push chart and trigger
	chartDir, err := extractTestChart()
	require.NoError(t, err)
	defer os.RemoveAll(chartDir)
	require.NoError(t, pushChartOCI(ctx, h.RegistryAddr, chartDir, "0.4.0"))

	start := time.Now()
	require.NoError(t, h.TriggerWebhook("test-chart", "0.4.0"))

	// Wait for all 3 customers to succeed (localhost001 + customer-002 + customer-003)
	allCustomers := append([]string{h.CustomerID}, customers...)
	for _, custID := range allCustomers {
		err := h.WaitForReleaseStatus(ctx, custID, "test-chart", "success", 3*time.Minute)
		assert.NoError(t, err, "customer %s should succeed", custID)
	}

	elapsed := time.Since(start)
	t.Logf("All 3 customers processed in %v", elapsed)

	// Verify concurrency: total time should be less than 2 minutes
	// (Single operator ~30s helm pull+upgrade => 3 serially ~90s)
	// With concurrency, should be well under 2 minutes
	assert.Less(t, elapsed, 2*time.Minute,
		"concurrent forwarding should be faster than serial (got %v)", elapsed)

	// Verify release records for all customers
	for _, custID := range allCustomers {
		releases, err := h.GetReleases(custID)
		require.NoError(t, err)
		found := false
		for _, r := range releases {
			if r.ChartName == "helm/test-chart" && r.Version == "0.4.0" {
				assert.Equal(t, "success", r.Status)
				found = true
			}
		}
		assert.True(t, found, "customer %s should have release record", custID)
	}
}

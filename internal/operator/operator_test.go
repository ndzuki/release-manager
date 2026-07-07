package operator

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/config"
	releasev1 "github.com/ndzuki/release-manager/api/gen/release/v1"
)

func TestMapStatusToProto(t *testing.T) {
	tests := []struct {
		name     string
		status   ReleaseStatus
		expected releasev1.ReleaseStatus
	}{
		{"pending", StatusPending, releasev1.ReleaseStatus_RELEASE_STATUS_PENDING},
		{"pulling", StatusPullingChart, releasev1.ReleaseStatus_RELEASE_STATUS_PULLING_CHART},
		{"upgrading", StatusUpgrading, releasev1.ReleaseStatus_RELEASE_STATUS_UPGRADING},
		{"succeeded", StatusSucceeded, releasev1.ReleaseStatus_RELEASE_STATUS_SUCCEEDED},
		{"pull_failed", StatusPullFailed, releasev1.ReleaseStatus_RELEASE_STATUS_FAILED},
		{"upgrade_failed", StatusUpgradeFailed, releasev1.ReleaseStatus_RELEASE_STATUS_FAILED},
		{"rolling_back", StatusRollingBack, releasev1.ReleaseStatus_RELEASE_STATUS_ROLLING_BACK},
		{"rolled_back", StatusRolledBack, releasev1.ReleaseStatus_RELEASE_STATUS_ROLLED_BACK},
		{"rollback_failed", StatusRollbackFailed, releasev1.ReleaseStatus_RELEASE_STATUS_ROLLBACK_FAILED},
		{"unknown", ReleaseStatus("INVALID"), releasev1.ReleaseStatus_RELEASE_STATUS_UNSPECIFIED},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MapStatusToProto(tt.status)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestBuildValuesOverrides(t *testing.T) {
	images := map[string]string{
		"magic-gateway":    "1.2.3",
		"sandbox-gateway":  "1.2.4",
	}

	values := buildValuesOverrides(images)

	require.NotNil(t, values)
	assert.Len(t, values, 2)

	magicValues, ok := values["magic-gateway"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "1.2.3", magicValues["imageTag"])

	sandboxValues, ok := values["sandbox-gateway"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "1.2.4", sandboxValues["imageTag"])
}

func TestBuildValuesOverrides_Empty(t *testing.T) {
	values := buildValuesOverrides(map[string]string{})
	assert.NotNil(t, values)
	assert.Len(t, values, 0)
}

func TestController_SubmitDeduplication(t *testing.T) {
	log := logr.Discard()
	cfg := config.DefaultConfig()
	helmClient := NewHelmClient(&cfg.Helm, &cfg.Harbor, log)
	reporter := &mockReporter{}
	ctrl := NewController(helmClient, reporter, cfg, log)

	req := ReleaseRequest{
		RequestID:    "req-001",
		ChartName:    "test",
		ChartURL:     "oci://harbor/helm/test",
		ChartVersion: "1.0.0",
		ReleaseName:  "test-release",
		Namespace:    "default",
	}

	assert.True(t, ctrl.Submit(req))
	assert.False(t, ctrl.Submit(req), "duplicate should be rejected")
}

func TestController_StartAndContextCancel(t *testing.T) {
	log := logr.Discard()
	cfg := config.DefaultConfig()
	helmClient := NewHelmClient(&cfg.Helm, &cfg.Harbor, log)
	reporter := &mockReporter{}
	ctrl := NewController(helmClient, reporter, cfg, log)

	ctx, cancel := context.WithCancel(context.Background())
	ctrl.Start(ctx)

	// 不应 panic
	time.Sleep(10 * time.Millisecond)
	cancel()
}

func TestReleaseResult_Fields(t *testing.T) {
	now := time.Now()
	result := ReleaseResult{
		RequestID:    "req-123",
		ChartName:    "magic-sandbox",
		ChartVersion: "0.0.15",
		Status:       StatusSucceeded,
		ErrorMessage: "",
		DurationSecs: 42,
		StartedAt:    now,
		CompletedAt:  now.Add(42 * time.Second),
	}

	assert.Equal(t, "req-123", result.RequestID)
	assert.Equal(t, StatusSucceeded, result.Status)
	assert.Equal(t, int64(42), result.DurationSecs)
	assert.Empty(t, result.ErrorMessage)
}

func TestReleaseStatus_Constants(t *testing.T) {
	// 确保所有状态常量不重复
	statuses := []ReleaseStatus{
		StatusPending,
		StatusPullingChart,
		StatusPullFailed,
		StatusUpgrading,
		StatusUpgradeFailed,
		StatusSucceeded,
		StatusRollingBack,
		StatusRolledBack,
		StatusRollbackFailed,
	}

	seen := make(map[ReleaseStatus]bool)
	for _, s := range statuses {
		assert.False(t, seen[s], "duplicate status: %s", s)
		seen[s] = true
	}
}

// mockReporter 实现 StatusReporter 接口用于测试。
type mockReporter struct {
	reported []ReleaseResult
}

func (m *mockReporter) ReportStatus(ctx context.Context, customerID string, result ReleaseResult) error {
	m.reported = append(m.reported, result)
	return nil
}

func TestExtractRegistryHost(t *testing.T) {
	host, err := extractRegistryHost("oci://harbor.example.com/helm/magic-sandbox")
	require.NoError(t, err)
	assert.Equal(t, "harbor.example.com", host)
}

func TestExtractRegistryHost_Invalid(t *testing.T) {
	_, err := extractRegistryHost("not-a-valid-url%%%")
	assert.Error(t, err)
}

func TestHelmClient_LogWriter(t *testing.T) {
	hc := &HelmClient{log: logr.Discard()}
	w := hc.logWriter()
	n, err := w.Write([]byte("test log message\n"))
	assert.NoError(t, err)
	assert.Greater(t, n, 0)
}

func TestLoadCertFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	certPath := tmpDir + "/cert.pem"

	// Invalid file path
	_, err := loadCertFromFile("/nonexistent/cert.pem")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read cert file")

	// Invalid PEM content
	require.NoError(t, os.WriteFile(certPath, []byte("not-valid-pem"), 0o644))
	_, err = loadCertFromFile(certPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode PEM")
}

func TestCertFingerprint(t *testing.T) {
	// Create a minimal self-signed cert for testing
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(1 * time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	cert, err := x509.ParseCertificate(certDER)
	require.NoError(t, err)

	fp := certFingerprint(cert)
	assert.NotEmpty(t, fp)
	assert.Len(t, fp, 64) // SHA256 hex = 64 chars
}

func TestController_SubmitMultiple(t *testing.T) {
	log := logr.Discard()
	cfg := config.DefaultConfig()
	helmClient := NewHelmClient(&cfg.Helm, &cfg.Harbor, log)
	reporter := &mockReporter{}
	ctrl := NewController(helmClient, reporter, cfg, log)

	for i := range 5 {
		req := ReleaseRequest{
			RequestID:    fmt.Sprintf("req-%03d", i),
			ChartName:    "test",
			ChartURL:     "oci://harbor/helm/test",
			ChartVersion: "1.0.0",
			ReleaseName:  "test-release",
			Namespace:    "default",
		}
		assert.True(t, ctrl.Submit(req), "request %d should be accepted", i)
	}

	// Duplicate should be rejected
	req := ReleaseRequest{
		RequestID:    "req-000",
		ChartName:    "test",
		ChartURL:     "oci://harbor/helm/test",
		ChartVersion: "1.0.0",
		ReleaseName:  "test-release",
		Namespace:    "default",
	}
	assert.False(t, ctrl.Submit(req), "duplicate should be rejected")
}

func TestController_StartCancelCleanup(t *testing.T) {
	log := logr.Discard()
	cfg := config.DefaultConfig()
	helmClient := NewHelmClient(&cfg.Helm, &cfg.Harbor, log)
	reporter := &mockReporter{}
	ctrl := NewController(helmClient, reporter, cfg, log)

	ctx, cancel := context.WithCancel(context.Background())
	ctrl.Start(ctx)

	// Submit then cancel - goroutines should drain cleanly
	ctrl.Submit(ReleaseRequest{
		RequestID:    "req-cancel",
		ChartName:    "test",
		ChartURL:     "oci://harbor/helm/test",
		ChartVersion: "1.0.0",
		ReleaseName:  "test-release",
		Namespace:    "default",
	})

	time.Sleep(50 * time.Millisecond)
	cancel()
	// Give goroutines time to drain before Shutdown
	time.Sleep(50 * time.Millisecond)
	ctrl.Shutdown()
}

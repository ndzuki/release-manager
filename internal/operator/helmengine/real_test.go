package helmengine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chartutil"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
)

func TestRealEngine_Install(t *testing.T) {
	engine, _ := newTestRealEngine(t, &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
	})
	chartPath := writeTestChart(t)

	installed, err := engine.Install(t.Context(), InstallOptions{
		Namespace:   "default",
		ReleaseName: "example",
		ChartPath:   chartPath,
		Values:      map[string]interface{}{"message": "hello"},
	})
	require.NoError(t, err)
	assert.Equal(t, "example", installed.Name)
	assert.Equal(t, "default", installed.Namespace)
	assert.Equal(t, 1, installed.Revision)
	assert.Equal(t, "deployed", installed.Status)
	assert.Equal(t, "example-chart-0.1.0", installed.Chart)
	assert.NotEmpty(t, installed.ManifestDigest)
}

func TestRealEngine_InstallAlreadyExists(t *testing.T) {
	engine, _ := newTestRealEngine(t, &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
	})
	opts := InstallOptions{
		Namespace:   "default",
		ReleaseName: "example",
		ChartPath:   writeTestChart(t),
	}

	_, err := engine.Install(t.Context(), opts)
	require.NoError(t, err)

	_, err = engine.Install(t.Context(), opts)
	assert.ErrorIs(t, err, ErrAlreadyExists)
}
func TestRealEngine_UpgradeExpectedRevisionConflictDoesNotWrite(t *testing.T) {
	engine, releases := newTestRealEngine(t, &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
	})
	chartPath := writeTestChart(t)
	_, err := engine.Install(t.Context(), InstallOptions{
		Namespace:   "default",
		ReleaseName: "upgrade-conflict",
		ChartPath:   chartPath,
	})
	require.NoError(t, err)

	_, err = engine.Upgrade(t.Context(), UpgradeOptions{
		Namespace:        "default",
		ReleaseName:      "upgrade-conflict",
		ChartPath:        chartPath,
		ExpectedRevision: 9,
	})
	require.ErrorIs(t, err, ErrConflict)

	stored, getErr := releases.Get("upgrade-conflict", 1)
	require.NoError(t, getErr)
	assert.Equal(t, 1, stored.Version)
}

func TestRealEngine_UpgradeAtomicFailureRestoresRelease(t *testing.T) {
	kubeClient := &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
	}
	engine, releases := newTestRealEngine(t, kubeClient)
	chartPath := writeTestChart(t)
	_, err := engine.Install(t.Context(), InstallOptions{
		Namespace:   "default",
		ReleaseName: "upgrade-atomic",
		ChartPath:   chartPath,
	})
	require.NoError(t, err)

	kubeClient.WaitError = errors.New("upgrade hook failed")
	_, err = engine.Upgrade(t.Context(), UpgradeOptions{
		Namespace:        "default",
		ReleaseName:      "upgrade-atomic",
		ChartPath:        chartPath,
		ExpectedRevision: 1,
		Atomic:           true,
		Timeout:          time.Second,
	})
	require.Error(t, err)

	stored, getErr := releases.Get("upgrade-atomic", 1)
	require.NoError(t, getErr)
	assert.Equal(t, 1, stored.Version)
}

func TestRealEngine_InstallAtomicFailureRemovesRelease(t *testing.T) {
	engine, releases := newTestRealEngine(t, &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
		WaitError:          errors.New("hook failed"),
	})

	_, err := engine.Install(t.Context(), InstallOptions{
		Namespace:   "default",
		ReleaseName: "atomic-example",
		ChartPath:   writeTestChart(t),
		Atomic:      true,
		Timeout:     time.Second,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrActionFailed)

	_, storageErr := releases.Get("atomic-example", 1)
	assert.ErrorIs(t, storageErr, driver.ErrReleaseNotFound)
}

func TestRealEngine_InstallContextErrors(t *testing.T) {
	engine, _ := newTestRealEngine(t, &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
	})

	tests := []struct {
		name    string
		ctx     context.Context
		wantErr error
	}{
		{
			name:    "cancelled",
			ctx:     cancelledContext(),
			wantErr: ErrCancelled,
		},
		{
			name:    "deadline exceeded",
			ctx:     expiredContext(t),
			wantErr: ErrTimeout,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := engine.Install(test.ctx, InstallOptions{
				Namespace:   "default",
				ReleaseName: "context-example",
				ChartPath:   writeTestChart(t),
			})
			assert.ErrorIs(t, err, test.wantErr)
		})
	}
}

func TestManifestGate(t *testing.T) {
	manifest := []byte("apiVersion: v1\nkind: ConfigMap\n")
	gate := &manifestGate{expectedDigest: fmt.Sprintf("%x", sha256.Sum256(manifest))}

	output, err := gate.Run(bytes.NewBuffer(manifest))
	require.NoError(t, err)
	assert.Equal(t, manifest, output.Bytes())
}

func TestManifestGateRejectsDrift(t *testing.T) {
	gate := &manifestGate{expectedDigest: "different"}

	_, err := gate.Run(bytes.NewBufferString("manifest"))
	require.ErrorIs(t, err, ErrRenderDrift)
}

func TestRealEngine_UpgradeRejectsRevisionConflict(t *testing.T) {
	engine, _ := newTestRealEngine(t, &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
	})
	chartPath := writeTestChart(t)
	_, err := engine.Install(t.Context(), InstallOptions{
		Namespace: "default", ReleaseName: "upgrade-example", ChartPath: chartPath,
	})
	require.NoError(t, err)

	_, err = engine.Upgrade(t.Context(), UpgradeOptions{
		Namespace: "default", ReleaseName: "upgrade-example", ChartPath: chartPath, ExpectedRevision: 2,
	})
	require.ErrorIs(t, err, ErrConflict)
}
func TestDigestValuesDeterministic(t *testing.T) {
	left := map[string]interface{}{
		"replicas": 2,
		"image": map[string]interface{}{
			"repository": "example/app",
			"tag":        "1.0.0",
		},
	}
	right := map[string]interface{}{
		"image": map[string]interface{}{
			"tag":        "1.0.0",
			"repository": "example/app",
		},
		"replicas": 2,
	}

	assert.Equal(t, digestValues(left), digestValues(right))
}

func newTestRealEngine(t *testing.T, kubeClient *kubefake.FailingKubeClient) (*RealEngine, *storage.Storage) {
	t.Helper()

	releases := storage.Init(driver.NewMemory())
	registryClient, err := registry.NewClient()
	require.NoError(t, err)

	engine := NewRealEngine("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	engine.releaseStorage = releases
	engine.configFactory = func(namespace string) (*action.Configuration, error) {
		driver, ok := releases.Driver.(*driver.Memory)
		if ok {
			driver.SetNamespace(namespace)
		}

		return &action.Configuration{
			Releases:       releases,
			KubeClient:     kubeClient,
			Capabilities:   chartutil.DefaultCapabilities.Copy(),
			RegistryClient: registryClient,
			Log:            engine.helmLog,
		}, nil
	}

	return engine, releases
}

func writeTestChart(t *testing.T) string {
	t.Helper()

	chartDir := filepath.Join(t.TempDir(), "example-chart")

	require.NoError(t, os.MkdirAll(filepath.Join(chartDir, "templates"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte(`apiVersion: v2
name: example-chart
version: 0.1.0
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(chartDir, "values.yaml"), []byte("message: default\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(chartDir, "templates", "configmap.yaml"), []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  message: {{ .Values.message | quote }}
`), 0o644))

	return chartDir
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func expiredContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)
	return ctx
}

// ── Rollback Tests ──

// AC-063-01: successful rollback creates a new revision.
func TestRealEngine_Rollback(t *testing.T) {
	kubeClient := &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
	}
	engine, releases := newTestRealEngine(t, kubeClient)
	chartPath := writeTestChart(t)
	ctx := context.Background()

	// Install at revision 1.
	_, err := engine.Install(ctx, InstallOptions{
		Namespace:   "default",
		ReleaseName: "rollback-test",
		ChartPath:   chartPath,
	})
	require.NoError(t, err)

	// Upgrade to revision 2 with different values.
	_, err = engine.Upgrade(ctx, UpgradeOptions{
		Namespace:        "default",
		ReleaseName:      "rollback-test",
		ChartPath:        chartPath,
		Values:           map[string]interface{}{"message": "v2"},
		ExpectedRevision: 1,
	})
	require.NoError(t, err)

	// Rollback to revision 1.
	rel, err := engine.Rollback(ctx, RollbackOptions{
		Namespace:      "default",
		ReleaseName:    "rollback-test",
		TargetRevision: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, 3, rel.Revision, "rollback creates a new revision")
	assert.Equal(t, "deployed", rel.Status)

	// Verify revision 3 exists in storage and is deployed.
	stored, getErr := releases.Get("rollback-test", 3)
	require.NoError(t, getErr)
	assert.Equal(t, release.StatusDeployed, stored.Info.Status)
	assert.Contains(t, stored.Info.Description, "Rollback to 1")
}

// AC-063-02: rollback to non-existent revision returns ErrRevisionNotFound.
func TestRealEngine_RollbackRevisionNotFound(t *testing.T) {
	engine, _ := newTestRealEngine(t, &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
	})
	chartPath := writeTestChart(t)
	ctx := context.Background()

	_, err := engine.Install(ctx, InstallOptions{
		Namespace:   "default",
		ReleaseName: "rb-rev-nf",
		ChartPath:   chartPath,
	})
	require.NoError(t, err)

	// Revision 99 does not exist in history.
	_, err = engine.Rollback(ctx, RollbackOptions{
		Namespace:      "default",
		ReleaseName:    "rb-rev-nf",
		TargetRevision: 99,
	})
	require.ErrorIs(t, err, ErrRevisionNotFound)
}

// AC-063-02: rollback for non-existent release returns ErrNotFound.
func TestRealEngine_RollbackReleaseNotFound(t *testing.T) {
	engine, _ := newTestRealEngine(t, &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
	})

	_, err := engine.Rollback(context.Background(), RollbackOptions{
		Namespace:      "default",
		ReleaseName:    "nonexistent",
		TargetRevision: 1,
	})
	require.ErrorIs(t, err, ErrNotFound)
}

// AC-063-03: rollback failure preserves the original release.
func TestRealEngine_RollbackFailurePreservesRelease(t *testing.T) {
	kubeClient := &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
	}
	engine, releases := newTestRealEngine(t, kubeClient)
	chartPath := writeTestChart(t)
	ctx := context.Background()

	_, err := engine.Install(ctx, InstallOptions{
		Namespace:   "default",
		ReleaseName: "rb-fail",
		ChartPath:   chartPath,
	})
	require.NoError(t, err)

	// Upgrade to build history.
	_, err = engine.Upgrade(ctx, UpgradeOptions{
		Namespace:        "default",
		ReleaseName:      "rb-fail",
		ChartPath:        chartPath,
		Values:           map[string]interface{}{"message": "v2"},
		ExpectedRevision: 1,
	})
	require.NoError(t, err)

	// Inject a WaitError to cause the rollback to fail during wait.
	// The rollback will run but when Wait is set it will fail, and Helm SDK
	// records the failed target and superseded current release.
	kubeClient.WaitError = errors.New("rollback wait hook failed")

	_, err = engine.Rollback(ctx, RollbackOptions{
		Namespace:      "default",
		ReleaseName:    "rb-fail",
		TargetRevision: 1,
		Timeout:        time.Second,
	})
	require.Error(t, err)

	// Verify original release (revision 2) still exists.
	stored, getErr := releases.Get("rb-fail", 2)
	require.NoError(t, getErr)
	// After Helm SDK rollback Wait failure, original release remains deployed
	// (the Helm SDK only supersedes on Update failure, not Wait failure).
	assert.Equal(t, release.StatusDeployed, stored.Info.Status,
		"original release should remain deployed after failed rollback")

	// Verify the failed rollback revision (3) was created but is failed.
	failedRel, getErr2 := releases.Get("rb-fail", 3)
	require.NoError(t, getErr2)
	assert.Equal(t, release.StatusFailed, failedRel.Info.Status,
		"rollback target should be failed")
}

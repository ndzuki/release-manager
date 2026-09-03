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
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/kube"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	// TASK-084 AC-084-04 negative (real SDK): the persistent WaitError also
	// fails the atomic rollback itself, so the outcome must report Restored=
	// false with the failed active revision — never a fabricated restore.
	outcome := OutcomeOf(err)
	assert.False(t, outcome.Restored, "failed rollback cascade must never report restored")
	require.NotNil(t, outcome.Active)
	assert.Equal(t, "failed", outcome.Active.Status)
	require.NotNil(t, outcome.Attempted)
	assert.Equal(t, 2, outcome.Attempted.Revision)

	stored, getErr := releases.Get("upgrade-atomic", 1)
	require.NoError(t, getErr)
	assert.Equal(t, 1, stored.Version)
}

// onceFailKubeClient fails exactly N Wait/WaitWithJobs calls and then lets the
// wait succeed — one shot for the upgrade wait, success for the atomic
// rollback wait (TASK-084 real-SDK restore scenario).
type onceFailKubeClient struct {
	*kubefake.FailingKubeClient
	remaining int
}

func (c *onceFailKubeClient) Wait(resources kube.ResourceList, d time.Duration) error {
	if c.remaining > 0 {
		c.remaining--
		return c.FailingKubeClient.Wait(resources, d)
	}
	return nil
}

func (c *onceFailKubeClient) WaitWithJobs(resources kube.ResourceList, d time.Duration) error {
	if c.remaining > 0 {
		c.remaining--
		return c.FailingKubeClient.WaitWithJobs(resources, d)
	}
	return nil
}

// TASK-084 AC-084-04: real SDK positive — the upgrade wait fails once, the
// atomic rollback succeeds, and the engine reports the authoritative restore
// signal. The restored active lands on the NEW rollback revision (3), which is
// why the agent must trust Restored instead of comparing revisions.
func TestRealEngine_UpgradeAtomicRollbackRestoredOutcome(t *testing.T) {
	kubeClient := &onceFailKubeClient{
		FailingKubeClient: &kubefake.FailingKubeClient{
			PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
		},
		remaining: 1,
	}
	kubeClient.WaitError = errors.New("upgrade hook failed")
	engine, releases := newTestRealEngine(t, kubeClient)
	chartPath := writeTestChart(t)
	_, err := engine.Install(t.Context(), InstallOptions{
		Namespace: "default", ReleaseName: "upgrade-restored", ChartPath: chartPath,
	})
	require.NoError(t, err)

	_, err = engine.Upgrade(t.Context(), UpgradeOptions{
		Namespace:        "default",
		ReleaseName:      "upgrade-restored",
		ChartPath:        chartPath,
		ExpectedRevision: 1,
		Atomic:           true,
		Timeout:          time.Second,
	})
	require.Error(t, err)
	outcome := OutcomeOf(err)
	assert.True(t, outcome.Restored, "real SDK restore must report the authoritative signal")
	require.NotNil(t, outcome.Active)
	assert.Equal(t, 3, outcome.Active.Revision, "restored config lands on the new rollback revision")
	assert.Equal(t, "deployed", outcome.Active.Status)
	require.NotNil(t, outcome.Attempted)
	assert.Equal(t, 2, outcome.Attempted.Revision)

	// Revision 1 remains the canonical pre-upgrade config source.
	stored, getErr := releases.Get("upgrade-restored", 1)
	require.NoError(t, getErr)
	assert.Equal(t, 1, stored.Version)
}

// TASK-084 AC-084-04 negative: real SDK preparation failures never decorate an
// outcome — the zero outcome is the documented fail-closed "no rollback
// signal", which the agent must never map to rollback_succeeded.
func TestRealEngine_UpgradePreparationFailureHasNoOutcome(t *testing.T) {
	engine, _ := newTestRealEngine(t, &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
	})
	chartPath := writeTestChart(t)
	_, err := engine.Install(t.Context(), InstallOptions{
		Namespace: "default", ReleaseName: "upgrade-prep-fail", ChartPath: chartPath,
	})
	require.NoError(t, err)

	// A manifest drift is detected before the Helm action writes anything:
	// rel == nil, so the plain preparation error carries no outcome.
	_, err = engine.Upgrade(t.Context(), UpgradeOptions{
		Namespace: "default", ReleaseName: "upgrade-prep-fail", ChartPath: chartPath,
		ExpectedRevision:       1,
		EffectiveValuesDigest:  "sha256:never-matched",
		ExpectedManifestDigest: "sha256:never-matched",
	})
	require.Error(t, err)
	assert.False(t, OutcomeOf(err).Restored, "preparation failure must fail closed")
	assert.Nil(t, OutcomeOf(err).Active)
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

func TestMapActionError_Forbidden(t *testing.T) {
	wrapped := fmt.Errorf(
		"validate install resources: %w",
		apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "example", errors.New("denied")),
	)

	err := mapActionError(t.Context(), "install Helm release", wrapped)
	assert.ErrorIs(t, err, ErrForbidden)
	assert.Contains(t, err.Error(), "configmaps \"example\" is forbidden")
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

func TestRealEngine_UpgradeManifestGateRejectsBeforeWrite(t *testing.T) {
	engine, releases := newTestRealEngine(t, &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
	})
	chartPath := writeTestChart(t)
	_, err := engine.Install(t.Context(), InstallOptions{
		Namespace: "default", ReleaseName: "upgrade-drift", ChartPath: chartPath,
	})
	require.NoError(t, err)

	rel, err := engine.Upgrade(t.Context(), UpgradeOptions{
		Namespace:        "default",
		ReleaseName:      "upgrade-drift",
		ChartPath:        chartPath,
		Values:           map[string]interface{}{"message": "v2"},
		ExpectedRevision: 1,
		Atomic:           true,
		OperationID:      "operation-drift",
		CommandID:        "command-drift",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, rel.Revision)
	assert.Equal(t, "release-manager operation=operation-drift command=command-drift", rel.Description)
	assert.NotEmpty(t, rel.Labels["rm_input_digest"])

	history, err := releases.History("upgrade-drift")
	require.NoError(t, err)
	assert.Len(t, history, 2)
}

func TestRealEngine_UpgradeRejectsManifestDriftWithoutWrite(t *testing.T) {
	engine, releases := newTestRealEngine(t, &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
	})
	chartPath := writeTestChart(t)
	_, err := engine.Install(t.Context(), InstallOptions{
		Namespace: "default", ReleaseName: "upgrade-manifest-drift", ChartPath: chartPath,
	})
	require.NoError(t, err)

	realDigest, err := renderUpgradeManifestDigest(t.Context(), mustActionConfig(t, engine, "default"), UpgradeOptions{
		Namespace: "default", ReleaseName: "upgrade-manifest-drift", ChartPath: chartPath,
		Values: map[string]interface{}{"message": "v2"}, ExpectedRevision: 1,
	}, "", "", mustLoadChart(t, chartPath))
	require.NoError(t, err)

	_, err = engine.Upgrade(t.Context(), UpgradeOptions{
		Namespace: "default", ReleaseName: "upgrade-manifest-drift", ChartPath: chartPath,
		Values: map[string]interface{}{"message": "v2"}, ExpectedRevision: 1,
		ExpectedManifestDigest: realDigest + "-drift",
	})
	require.ErrorIs(t, err, ErrRenderDrift)
	history, err := releases.History("upgrade-manifest-drift")
	require.NoError(t, err)
	assert.Len(t, history, 1)
}

func mustActionConfig(t *testing.T, engine *RealEngine, namespace string) *action.Configuration {
	t.Helper()
	cfg, err := engine.actionConfig(namespace)
	require.NoError(t, err)
	return cfg
}

func mustLoadChart(t *testing.T, chartPath string) *chart.Chart {
	t.Helper()
	chrt, err := loader.Load(chartPath)
	require.NoError(t, err)
	return chrt
}

func TestRealEngine_UpgradeCrashReplayUsesFrozenCommand(t *testing.T) {
	engine, releases := newTestRealEngine(t, &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
	})
	chartPath := writeTestChart(t)
	_, err := engine.Install(t.Context(), InstallOptions{
		Namespace: "default", ReleaseName: "upgrade-replay", ChartPath: chartPath,
	})
	require.NoError(t, err)
	opts := UpgradeOptions{
		Namespace: "default", ReleaseName: "upgrade-replay", ChartPath: chartPath,
		Values: map[string]interface{}{"message": "v2"}, ExpectedRevision: 1, Atomic: true,
		OperationID: "operation-replay", CommandID: "command-replay", BundleDigest: "sha256:bundle",
		ChartDigest: "", EffectiveValuesDigest: "sha256:values", SecretSnapshotDigest: "sha256:secret",
	}

	first, err := engine.Upgrade(t.Context(), opts)
	require.NoError(t, err)
	replayed, err := engine.Upgrade(t.Context(), opts)
	require.NoError(t, err)
	assert.Equal(t, first.Revision, replayed.Revision)
	assert.Equal(t, first.Description, replayed.Description)
	assert.Equal(t, first.Labels["rm_input_digest"], replayed.Labels["rm_input_digest"])
	history, err := releases.History("upgrade-replay")
	require.NoError(t, err)
	assert.Len(t, history, 2)
}

func TestRealEngine_UpgradeStatusErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  release.Status
		wantErr error
	}{
		{name: "busy", status: release.StatusPendingUpgrade, wantErr: ErrReleaseBusy},
		{name: "not deployed", status: release.StatusFailed, wantErr: ErrReleaseNotDeployed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, releases := newTestRealEngine(t, &kubefake.FailingKubeClient{
				PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
			})
			chartPath := writeTestChart(t)
			_, err := engine.Install(t.Context(), InstallOptions{
				Namespace: "default", ReleaseName: "upgrade-status", ChartPath: chartPath,
			})
			require.NoError(t, err)
			stored, err := releases.Get("upgrade-status", 1)
			require.NoError(t, err)
			stored.Info.Status = test.status
			require.NoError(t, releases.Update(stored))

			_, err = engine.Upgrade(t.Context(), UpgradeOptions{
				Namespace: "default", ReleaseName: "upgrade-status", ChartPath: chartPath, ExpectedRevision: 1,
			})
			assert.ErrorIs(t, err, test.wantErr)
		})
	}
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

func newTestRealEngine(t *testing.T, kubeClient kube.Interface) (*RealEngine, *storage.Storage) {
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

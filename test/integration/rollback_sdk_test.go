//go:build integration

// Package integration contains end-to-end SDK quality gate tests.
// These tests exercise the full Helm SDK pipeline (helm.sh/helm/v3/pkg/action)
// with in-memory storage — no Helm CLI, kubectl, or real cluster required.
package integration

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chartutil"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"

	"github.com/ndzuki/release-manager/internal/operator/helmengine"
)

// AC-063-01: rollback from revision 2 to revision 1 produces revision 3.
func TestRollbackSDK_CreatesNewRevision(t *testing.T) {
	engine, releases := newRollbackTestEngine(t)
	chartPath := writeRollbackTestChart(t)
	ctx := context.Background()

	// Install revision 1.
	_, err := engine.Install(ctx, helmengine.InstallOptions{
		Namespace:   "default",
		ReleaseName: "rollback-sdk",
		ChartPath:   chartPath,
	})
	require.NoError(t, err)

	// Upgrade to revision 2.
	_, err = engine.Upgrade(ctx, helmengine.UpgradeOptions{
		Namespace:        "default",
		ReleaseName:     "rollback-sdk",
		ChartPath:       chartPath,
		Values:          map[string]interface{}{"message": "v2"},
		ExpectedRevision: 1,
	})
	require.NoError(t, err)

	// Rollback to revision 1.
	rel, err := engine.Rollback(ctx, helmengine.RollbackOptions{
		Namespace:      "default",
		ReleaseName:    "rollback-sdk",
		TargetRevision: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, 3, rel.Revision, "rollback should create revision 3")
	assert.Equal(t, "deployed", rel.Status)

	// Verify history contains rollback entry.
	history, err := engine.History(ctx, helmengine.HistoryOptions{
		Namespace:   "default",
		ReleaseName: "rollback-sdk",
	})
	require.NoError(t, err)
	assert.Len(t, history, 3)
	assert.Equal(t, "Install complete", history[0].Description)
	assert.Equal(t, "Upgrade complete", history[1].Description)
	assert.Contains(t, history[2].Description, "Rollback to 1")

	// Verify Helm storage has revision 3.
	stored, getErr := releases.Get("rollback-sdk", 3)
	require.NoError(t, getErr)
	assert.Equal(t, release.StatusDeployed, stored.Info.Status)
}

// AC-063-02: rollback to non-existent revision returns ErrRevisionNotFound.
func TestRollbackSDK_TargetRevisionNotFound(t *testing.T) {
	engine, _ := newRollbackTestEngine(t)
	chartPath := writeRollbackTestChart(t)
	ctx := context.Background()

	_, err := engine.Install(ctx, helmengine.InstallOptions{
		Namespace:   "default",
		ReleaseName: "rb-rev-nf",
		ChartPath:   chartPath,
	})
	require.NoError(t, err)

	_, err = engine.Rollback(ctx, helmengine.RollbackOptions{
		Namespace:      "default",
		ReleaseName:    "rb-rev-nf",
		TargetRevision: 99,
	})
	require.ErrorIs(t, err, helmengine.ErrRevisionNotFound)

	// Verify release state is unchanged.
	rel, err := engine.Status(ctx, helmengine.StatusOptions{
		Namespace:   "default",
		ReleaseName: "rb-rev-nf",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, rel.Revision, "original revision should be unchanged")
	assert.Equal(t, "deployed", rel.Status)
}

// AC-063-02: rollback when release does not exist returns ErrNotFound.
func TestRollbackSDK_ReleaseNotFound(t *testing.T) {
	engine, _ := newRollbackTestEngine(t)

	_, err := engine.Rollback(context.Background(), helmengine.RollbackOptions{
		Namespace:      "default",
		ReleaseName:    "nonexistent",
		TargetRevision: 1,
	})
	require.ErrorIs(t, err, helmengine.ErrNotFound)
}

// AC-063-03: rollback failure preserves the original release.
func TestRollbackSDK_FailurePreservesRelease(t *testing.T) {
	kubeClient := &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
	}
	engine, releases := newRollbackTestEngine(t, kubeClient)
	chartPath := writeRollbackTestChart(t)
	ctx := context.Background()

	_, err := engine.Install(ctx, helmengine.InstallOptions{
		Namespace:   "default",
		ReleaseName: "rb-fail",
		ChartPath:   chartPath,
	})
	require.NoError(t, err)

	_, err = engine.Upgrade(ctx, helmengine.UpgradeOptions{
		Namespace:        "default",
		ReleaseName:     "rb-fail",
		ChartPath:       chartPath,
		Values:          map[string]interface{}{"message": "v2"},
		ExpectedRevision: 1,
	})
	require.NoError(t, err)

	// Inject WaitError to cause rollback to fail during wait phase.
	kubeClient.WaitError = assert.AnError

	_, err = engine.Rollback(ctx, helmengine.RollbackOptions{
		Namespace:      "default",
		ReleaseName:    "rb-fail",
		TargetRevision: 1,
	})
	require.Error(t, err)

	// Verify original release (revision 2) remains deployed.
	stored, getErr := releases.Get("rb-fail", 2)
	require.NoError(t, getErr)
	assert.Equal(t, release.StatusDeployed, stored.Info.Status,
		"original release should remain deployed after failed rollback")

	// Verify rollback revision (3) was created but is failed.
	failedRel, getErr2 := releases.Get("rb-fail", 3)
	require.NoError(t, getErr2)
	assert.Equal(t, release.StatusFailed, failedRel.Info.Status,
		"rollback target should be in failed state")
}

func newRollbackTestEngine(t *testing.T, kubeClient ...*kubefake.FailingKubeClient) (*helmengine.RealEngine, *storage.Storage) {
	t.Helper()

	var kc *kubefake.FailingKubeClient
	if len(kubeClient) > 0 {
		kc = kubeClient[0]
	} else {
		kc = &kubefake.FailingKubeClient{
			PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
		}
	}

	releases := storage.Init(driver.NewMemory())

	engine := helmengine.NewRealEngine("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	engine.SetReleaseStorage(releases)

	engine.SetConfigFactory(func(namespace string) (*action.Configuration, error) {
		d, ok := releases.Driver.(*driver.Memory)
		if ok {
			d.SetNamespace(namespace)
		}
		return &action.Configuration{
			Releases:       releases,
			KubeClient:     kc,
			Capabilities:   chartutil.DefaultCapabilities.Copy(),
			Log:            func(_ string, _ ...interface{}) {},
		}, nil
	})

	return engine, releases
}

func writeRollbackTestChart(t *testing.T) string {
	t.Helper()

	chartDir := filepath.Join(t.TempDir(), "rollback-chart")
	require.NoError(t, os.MkdirAll(filepath.Join(chartDir, "templates"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte(`apiVersion: v2
name: rollback-chart
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

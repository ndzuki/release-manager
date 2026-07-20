package helmengine

import (
	"context"
	"errors"
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
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
)

func TestRealEngine_Install(t *testing.T) {
	engine, _ := newTestRealEngine(t, &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard},
	})
	chartPath := writeTestChart(t)

	release, err := engine.Install(t.Context(), InstallOptions{
		Namespace:   "default",
		ReleaseName: "example",
		ChartPath:   chartPath,
		Values:      map[string]interface{}{"message": "hello"},
	})
	require.NoError(t, err)
	assert.Equal(t, "example", release.Name)
	assert.Equal(t, "default", release.Namespace)
	assert.Equal(t, 1, release.Revision)
	assert.Equal(t, "deployed", release.Status)
	assert.Equal(t, "example-chart-0.1.0", release.Chart)
	assert.NotEmpty(t, release.ManifestDigest)
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

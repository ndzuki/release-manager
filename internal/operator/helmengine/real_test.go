package helmengine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/kube"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/client-go/rest"
)

func TestRealEngine_Upgrade(t *testing.T) {
	tests := []struct {
		name             string
		expectedRevision int
		configure        func(*action.Configuration, *kubefake.FailingKubeClient)
		wantRevision     int
		wantErr          error
		wantHistory      int
	}{
		{
			name:             "release not found",
			expectedRevision: 1,
			wantErr:          ErrNotFound,
			wantHistory:      0,
		},
		{
			name:             "revision conflict has no write",
			expectedRevision: 2,
			wantErr:          ErrConflict,
			wantHistory:      1,
		},
		{
			name:             "successful upgrade",
			expectedRevision: 1,
			wantRevision:     2,
			wantHistory:      2,
		},
		{
			name:             "atomic failure rolls back",
			expectedRevision: 1,
			configure: func(cfg *action.Configuration, client *kubefake.FailingKubeClient) {
				cfg.KubeClient = &failOnceUpdateClient{
					FailingKubeClient: client,
					err:               fmt.Errorf("upgrade failed"),
				}
			},
			wantErr:     ErrActionFailed,
			wantHistory: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chartPath := writeTestChart(t)
			cfg, kubeClient := newTestActionConfiguration(t)
			if tt.configure != nil {
				tt.configure(cfg, kubeClient)
			}
			if tt.name != "release not found" {
				require.NoError(t, cfg.Releases.Create(testRelease("my-release", "default", chartPath)))
			}

			engine := newRealEngineWithConfig("default", func(string) (*action.Configuration, error) {
				return cfg, nil
			})
			rel, err := engine.Upgrade(context.Background(), UpgradeOptions{
				Namespace:        "default",
				ReleaseName:      "my-release",
				ChartPath:        chartPath,
				Values:           map[string]interface{}{"replicas": 2},
				ExpectedRevision: tt.expectedRevision,
				Atomic:           tt.name == "atomic failure rolls back",
				Timeout:          1,
			})

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				require.NotNil(t, rel)
				assert.Equal(t, tt.wantRevision, rel.Revision)
			}

			history, historyErr := cfg.Releases.History("my-release")
			if tt.wantHistory == 0 {
				require.ErrorIs(t, historyErr, driver.ErrReleaseNotFound)
				return
			}
			require.NoError(t, historyErr)
			assert.Len(t, history, tt.wantHistory)
			if tt.name == "revision conflict has no write" {
				assert.Equal(t, 1, history[0].Version)
				assert.Equal(t, release.StatusDeployed, history[0].Info.Status)
			}
			if tt.name == "atomic failure rolls back" {
				rolledBack, getErr := cfg.Releases.Get("my-release", 3)
				require.NoError(t, getErr)
				assert.Equal(t, release.StatusDeployed, rolledBack.Info.Status)
				original, getErr := cfg.Releases.Get("my-release", 1)
				require.NoError(t, getErr)
				assert.Equal(t, release.StatusSuperseded, original.Info.Status)
			}
		})
	}
}

func TestNewActionConfiguration_RequiresRESTConfig(t *testing.T) {
	_, err := newActionConfiguration("default", func() *rest.Config { return nil })
	require.ErrorContains(t, err, "kubernetes rest config is required")
}

type failOnceUpdateClient struct {
	*kubefake.FailingKubeClient
	err error
}

func (c *failOnceUpdateClient) Update(
	original kube.ResourceList,
	modified kube.ResourceList,
	force bool,
) (*kube.Result, error) {
	if c.err != nil {
		err := c.err
		c.err = nil
		return &kube.Result{}, err
	}
	return c.FailingKubeClient.Update(original, modified, force)
}

func newTestActionConfiguration(t *testing.T) (*action.Configuration, *kubefake.FailingKubeClient) {
	t.Helper()

	registryClient, err := registry.NewClient()
	require.NoError(t, err)
	kubeClient := &kubefake.FailingKubeClient{
		PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard, LogOutput: io.Discard},
	}
	return &action.Configuration{
		Releases:       storage.Init(driver.NewMemory()),
		KubeClient:     kubeClient,
		Capabilities:   chartutil.DefaultCapabilities,
		RegistryClient: registryClient,
		Log:            func(string, ...interface{}) {},
	}, kubeClient
}

func testRelease(name, namespace, chartPath string) *release.Release {
	return &release.Release{
		Name:      name,
		Namespace: namespace,
		Chart: &chart.Chart{Metadata: &chart.Metadata{
			APIVersion: "v2",
			Name:       filepath.Base(chartPath),
			Version:    "0.1.0",
		}},
		Config:   map[string]interface{}{"replicas": 1},
		Manifest: "",
		Info: &release.Info{
			Status:      release.StatusDeployed,
			Description: "seed release",
		},
		Version: 1,
	}
}

func writeTestChart(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	chartYAML := []byte("apiVersion: v2\nname: upgrade-test\nversion: 0.1.0\n")
	require.NoError(t, writeFile(filepath.Join(dir, "Chart.yaml"), chartYAML))
	require.NoError(t, writeFile(filepath.Join(dir, "values.yaml"), []byte("replicas: 1\n")))
	return dir
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

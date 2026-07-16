package helmengine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chart"
)

func TestFake_AllMethodsUseSDKContract(t *testing.T) {
	engine := NewFake()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	validatedChart := testChart("example")
	_, err := engine.Install(ctx, InstallOptions{
		Namespace:   "default",
		ReleaseName: "release",
		Chart:       validatedChart,
		Values:      map[string]interface{}{"replicas": 2},
	})
	require.NoError(t, err)

	status, err := engine.Status(ctx, StatusOptions{Namespace: "default", ReleaseName: "release"})
	require.NoError(t, err)
	assert.Equal(t, 1, status.Revision)

	values, err := engine.GetValues(ctx, GetValuesOptions{Namespace: "default", ReleaseName: "release"})
	require.NoError(t, err)
	assert.Empty(t, values)

	_, err = engine.Upgrade(ctx, UpgradeOptions{
		Namespace:   "default",
		ReleaseName: "release",
		Chart:       validatedChart,
		Values:      map[string]interface{}{"replicas": 3},
	})
	require.NoError(t, err)

	history, err := engine.History(ctx, HistoryOptions{Namespace: "default", ReleaseName: "release"})
	require.NoError(t, err)
	assert.Len(t, history, 2)

	_, err = engine.Rollback(ctx, RollbackOptions{
		Namespace:      "default",
		ReleaseName:    "release",
		TargetRevision: 1,
	})
	require.NoError(t, err)

	items, err := engine.List(ctx, "default")
	require.NoError(t, err)
	assert.Len(t, items, 1)
}

func TestFake_ContextCancellation(t *testing.T) {
	engine := NewFake()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := engine.Install(ctx, InstallOptions{
		Namespace:   "default",
		ReleaseName: "release",
		Chart:       testChart("example"),
	})
	assert.ErrorIs(t, err, ErrCancelled)
}

func TestFake_ParallelOperationsAreIsolated(t *testing.T) {
	engine := NewFake()

	type operation struct {
		namespace string
		name      string
		chart     string
	}
	operations := []operation{
		{namespace: "team-a", name: "release-a", chart: "chart-a"},
		{namespace: "team-b", name: "release-b", chart: "chart-b"},
	}

	var wait sync.WaitGroup
	errors := make(chan error, len(operations))
	for _, op := range operations {
		wait.Go(func() {
			_, err := engine.Install(t.Context(), InstallOptions{
				Namespace:   op.namespace,
				ReleaseName: op.name,
				Chart:       testChart(op.chart),
			})
			errors <- err
		})
	}
	wait.Wait()
	close(errors)

	for err := range errors {
		require.NoError(t, err)
	}

	for _, op := range operations {
		release, err := engine.Status(t.Context(), StatusOptions{
			Namespace:   op.namespace,
			ReleaseName: op.name,
		})
		require.NoError(t, err)
		assert.Equal(t, op.chart, release.Chart)
	}
}

func TestFake_StableErrors(t *testing.T) {
	engine := NewFake()
	ctx := t.Context()

	_, err := engine.Status(ctx, StatusOptions{Namespace: "default", ReleaseName: "missing"})
	assert.ErrorIs(t, err, ErrNotFound)

	opts := InstallOptions{
		Namespace:   "default",
		ReleaseName: "release",
		Chart:       testChart("example"),
	}
	_, err = engine.Install(ctx, opts)
	require.NoError(t, err)
	_, err = engine.Install(ctx, opts)
	assert.ErrorIs(t, err, ErrAlreadyExists)
}

func testChart(name string) *chart.Chart {
	return &chart.Chart{Metadata: &chart.Metadata{Name: name, Version: "1.0.0"}}
}

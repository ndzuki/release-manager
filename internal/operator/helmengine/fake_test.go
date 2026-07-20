package helmengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFake_Install(t *testing.T) {
	eng := NewFake()
	ctx := context.Background()

	rel, err := eng.Install(ctx, InstallOptions{
		Namespace:   "default",
		ReleaseName: "my-release",
		ChartPath:   "nginx",
		Values:      map[string]interface{}{"replicas": 3},
	})
	require.NoError(t, err)
	assert.Equal(t, "my-release", rel.Name)
	assert.Equal(t, "default", rel.Namespace)
	assert.Equal(t, 1, rel.Revision)
	assert.Equal(t, "deployed", rel.Status)
	assert.NotEmpty(t, rel.ManifestDigest)
}

func TestFake_InstallAlreadyExists(t *testing.T) {
	eng := NewFake()
	ctx := context.Background()
	opts := InstallOptions{Namespace: "default", ReleaseName: "my-release", ChartPath: "nginx"}

	_, err := eng.Install(ctx, opts)
	require.NoError(t, err)

	_, err = eng.Install(ctx, opts)
	assert.ErrorIs(t, err, ErrAlreadyExists)
}

func TestFake_Upgrade(t *testing.T) {
	eng := NewFake()
	ctx := context.Background()

	_, err := eng.Install(ctx, InstallOptions{Namespace: "default", ReleaseName: "my-release", ChartPath: "nginx"})
	require.NoError(t, err)

	rel, err := eng.Upgrade(ctx, UpgradeOptions{
		Namespace:   "default",
		ReleaseName: "my-release",
		ChartPath:   "nginx",
		Values:      map[string]interface{}{"replicas": 5},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, rel.Revision)
	assert.Equal(t, "deployed", rel.Status)
}

func TestFake_UpgradeNotFound(t *testing.T) {
	eng := NewFake()
	ctx := context.Background()

	_, err := eng.Upgrade(ctx, UpgradeOptions{Namespace: "default", ReleaseName: "nonexistent", ChartPath: "nginx"})
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestFake_Rollback(t *testing.T) {
	eng := NewFake()
	ctx := context.Background()

	_, err := eng.Install(ctx, InstallOptions{Namespace: "default", ReleaseName: "my-release", ChartPath: "nginx"})
	require.NoError(t, err)

	_, err = eng.Upgrade(ctx, UpgradeOptions{Namespace: "default", ReleaseName: "my-release", ChartPath: "nginx"})
	require.NoError(t, err)

	rel, err := eng.Rollback(ctx, RollbackOptions{Namespace: "default", ReleaseName: "my-release", TargetRevision: 1})
	require.NoError(t, err)
	assert.Equal(t, 3, rel.Revision) // rollback creates a new revision
}

// AC-063-01: valid historical revision → new revision and operation recorded.
func TestFake_RollbackCreatesNewOperation(t *testing.T) {
	eng := NewFake()
	ctx := context.Background()

	_, err := eng.Install(ctx, InstallOptions{Namespace: "ns1", ReleaseName: "rel-a", ChartPath: "nginx"})
	require.NoError(t, err)

	_, err = eng.Upgrade(ctx, UpgradeOptions{Namespace: "ns1", ReleaseName: "rel-a", ChartPath: "nginx"})
	require.NoError(t, err)

	// Rollback to revision 1.
	rel, err := eng.Rollback(ctx, RollbackOptions{Namespace: "ns1", ReleaseName: "rel-a", TargetRevision: 1})
	require.NoError(t, err)
	assert.Equal(t, 3, rel.Revision, "rollback creates a new revision")
	assert.Equal(t, "deployed", rel.Status)

	// Verify history contains the rollback entry.
	history, err := eng.History(ctx, HistoryOptions{Namespace: "ns1", ReleaseName: "rel-a"})
	require.NoError(t, err)
	assert.Len(t, history, 3)
	assert.Contains(t, history[2].Description, "Rollback")
}

// AC-063-02: missing historical artifact → rollback not executed.
func TestFake_RollbackReleaseNotFound(t *testing.T) {
	eng := NewFake()
	ctx := context.Background()

	_, err := eng.Rollback(ctx, RollbackOptions{Namespace: "ns1", ReleaseName: "nonexistent", TargetRevision: 1})
	require.ErrorIs(t, err, ErrNotFound)
}

// AC-063-03: rollback failure → original release state preserved.
func TestFake_RollbackFailurePreservesRelease(t *testing.T) {
	eng := NewFake()
	ctx := context.Background()

	_, err := eng.Install(ctx, InstallOptions{Namespace: "ns1", ReleaseName: "rel-a", ChartPath: "nginx"})
	require.NoError(t, err)

	// Capture pre-rollback state.
	before, err := eng.Status(ctx, StatusOptions{Namespace: "ns1", ReleaseName: "rel-a"})
	require.NoError(t, err)
	beforeRevision := before.Revision

	// Inject failure.
	eng.RollbackError = errors.New("helm rollback failed")

	_, err = eng.Rollback(ctx, RollbackOptions{Namespace: "ns1", ReleaseName: "rel-a", TargetRevision: 1})
	require.Error(t, err)

	// Verify release state is unchanged.
	after, err := eng.Status(ctx, StatusOptions{Namespace: "ns1", ReleaseName: "rel-a"})
	require.NoError(t, err)
	assert.Equal(t, beforeRevision, after.Revision, "original release revision preserved after failed rollback")
	assert.Equal(t, "deployed", after.Status)
}

func TestFake_Status(t *testing.T) {
	eng := NewFake()
	ctx := context.Background()

	_, err := eng.Install(ctx, InstallOptions{Namespace: "default", ReleaseName: "my-release", ChartPath: "nginx"})
	require.NoError(t, err)

	rel, err := eng.Status(ctx, StatusOptions{Namespace: "default", ReleaseName: "my-release"})
	require.NoError(t, err)
	assert.Equal(t, "my-release", rel.Name)
	assert.Equal(t, "deployed", rel.Status)
}

func TestFake_StatusNotFound(t *testing.T) {
	eng := NewFake()
	ctx := context.Background()

	_, err := eng.Status(ctx, StatusOptions{Namespace: "default", ReleaseName: "nonexistent"})
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestFake_History(t *testing.T) {
	eng := NewFake()
	ctx := context.Background()

	_, err := eng.Install(ctx, InstallOptions{Namespace: "default", ReleaseName: "my-release", ChartPath: "nginx"})
	require.NoError(t, err)
	_, err = eng.Upgrade(ctx, UpgradeOptions{Namespace: "default", ReleaseName: "my-release", ChartPath: "nginx"})
	require.NoError(t, err)

	entries, err := eng.History(ctx, HistoryOptions{Namespace: "default", ReleaseName: "my-release"})
	require.NoError(t, err)
	assert.Len(t, entries, 2)
	assert.Equal(t, 1, entries[0].Revision)
	assert.Equal(t, 2, entries[1].Revision)
}

func TestFake_ContextCancellation(t *testing.T) {
	// AC-041-02: context cancel returns cancelled error
	eng := NewFake()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := eng.Install(ctx, InstallOptions{Namespace: "default", ReleaseName: "my-release", ChartPath: "nginx"})
	assert.ErrorIs(t, err, ErrCancelled)
}

func TestFake_ConcurrentAccess(t *testing.T) {
	// AC-041-04: parallel operations don't pollute each other
	eng := NewFake()
	ctx := context.Background()

	// Install two different releases concurrently
	errCh := make(chan error, 2)

	go func() {
		_, err := eng.Install(ctx, InstallOptions{Namespace: "ns1", ReleaseName: "rel1", ChartPath: "chart1"})
		errCh <- err
	}()
	go func() {
		_, err := eng.Install(ctx, InstallOptions{Namespace: "ns2", ReleaseName: "rel2", ChartPath: "chart2"})
		errCh <- err
	}()

	for i := 0; i < 2; i++ {
		require.NoError(t, <-errCh)
	}

	// Verify they don't leak into each other
	rel1, err := eng.Status(ctx, StatusOptions{Namespace: "ns1", ReleaseName: "rel1"})
	require.NoError(t, err)
	assert.Equal(t, "chart1", rel1.Chart)

	rel2, err := eng.Status(ctx, StatusOptions{Namespace: "ns2", ReleaseName: "rel2"})
	require.NoError(t, err)
	assert.Equal(t, "chart2", rel2.Chart)
}

func TestFake_UpgradeIsolatesNamespaceAndRelease(t *testing.T) {
	eng := NewFake()
	ctx := context.Background()

	for _, opts := range []InstallOptions{
		{Namespace: "customer-a", ReleaseName: "release-a", ChartPath: "chart-a"},
		{Namespace: "customer-b", ReleaseName: "release-b", ChartPath: "chart-b"},
		{Namespace: "customer-a", ReleaseName: "other-release", ChartPath: "chart-other"},
	} {
		_, err := eng.Install(ctx, opts)
		require.NoError(t, err)
	}

	_, err := eng.Upgrade(ctx, UpgradeOptions{
		Namespace:        "customer-a",
		ReleaseName:      "release-a",
		ChartPath:        "chart-a-v2",
		ExpectedRevision: 1,
	})
	require.NoError(t, err)

	unchanged := []StatusOptions{
		{Namespace: "customer-b", ReleaseName: "release-b"},
		{Namespace: "customer-a", ReleaseName: "other-release"},
	}
	for _, opts := range unchanged {
		rel, statusErr := eng.Status(ctx, opts)
		require.NoError(t, statusErr)
		assert.Equal(t, 1, rel.Revision)
		assert.NotEqual(t, "chart-a-v2", rel.Chart)
	}

	updated, err := eng.Status(ctx, StatusOptions{Namespace: "customer-a", ReleaseName: "release-a"})
	require.NoError(t, err)
	assert.Equal(t, 2, updated.Revision)
	assert.Equal(t, "chart-a-v2", updated.Chart)
}

func TestFake_AllMethods(t *testing.T) {
	// AC-041-01: all interface methods work on fake without subprocess
	eng := NewFake()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Install
	_, err := eng.Install(ctx, InstallOptions{Namespace: "default", ReleaseName: "full-test", ChartPath: "nginx"})
	require.NoError(t, err)

	// Status
	rel, err := eng.Status(ctx, StatusOptions{Namespace: "default", ReleaseName: "full-test"})
	require.NoError(t, err)
	assert.Equal(t, 1, rel.Revision)

	// GetValues
	vals, err := eng.GetValues(ctx, GetValuesOptions{Namespace: "default", ReleaseName: "full-test"})
	require.NoError(t, err)
	assert.NotNil(t, vals)

	// Upgrade
	_, err = eng.Upgrade(ctx, UpgradeOptions{Namespace: "default", ReleaseName: "full-test", ChartPath: "nginx"})
	require.NoError(t, err)

	// History
	entries, err := eng.History(ctx, HistoryOptions{Namespace: "default", ReleaseName: "full-test"})
	require.NoError(t, err)
	assert.Len(t, entries, 2)

	// Rollback
	_, err = eng.Rollback(ctx, RollbackOptions{Namespace: "default", ReleaseName: "full-test", TargetRevision: 1})
	require.NoError(t, err)
}

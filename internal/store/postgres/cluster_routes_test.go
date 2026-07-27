//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
)

//nolint:unparam // customerID returned for consistency; callers may use it in future tests
func setupClusterRouteFixture(t *testing.T, st *postgresstore.Store) (customerID, clusterID string) {
	t.Helper()
	ctx := context.Background()

	cust := &store.Customer{ID: uuid.New().String(), Name: "RouteOwner", Slug: "route-owner"}
	require.NoError(t, st.Customers().Create(ctx, cust))

	cl := &store.Cluster{ID: uuid.New().String(), Name: "target-cluster", CustomerID: cust.ID}
	require.NoError(t, st.Clusters().Create(ctx, cl))

	return cust.ID, cl.ID
}

func TestClusterRouteCreateAndGet(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	_, clusterID := setupClusterRouteFixture(t, st)

	r := &store.ClusterRoute{
		ID:           uuid.New().String(),
		ClusterID:    clusterID,
		ArtifactType: store.ArtifactImage,
		Mode:         store.ModeDirect,
		SourcePrefix: "docker.io/myorg",
		TargetPrefix: "registry.internal/myorg",
	}
	require.NoError(t, st.ClusterRoutes().Create(ctx, r))

	got, err := st.ClusterRoutes().Get(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, r.ID, got.ID)
	assert.Equal(t, r.ClusterID, got.ClusterID)
	assert.Equal(t, r.ArtifactType, got.ArtifactType)
	assert.Equal(t, r.Mode, got.Mode)
	assert.Equal(t, r.SourcePrefix, got.SourcePrefix)
	assert.Equal(t, r.TargetPrefix, got.TargetPrefix)
}

func TestClusterRouteDelete(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	_, clusterID := setupClusterRouteFixture(t, st)

	r := &store.ClusterRoute{
		ID:           uuid.New().String(),
		ClusterID:    clusterID,
		ArtifactType: store.ArtifactImage,
		Mode:         store.ModeDirect,
		SourcePrefix: "docker.io/app",
		TargetPrefix: "registry.internal/app",
	}
	require.NoError(t, st.ClusterRoutes().Create(ctx, r))

	require.NoError(t, st.ClusterRoutes().Delete(ctx, r.ID))

	_, err := st.ClusterRoutes().Get(ctx, r.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestClusterRouteDeleteNotFound(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	err := st.ClusterRoutes().Delete(ctx, "nonexistent")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestClusterRouteListByCluster(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	_, clusterID := setupClusterRouteFixture(t, st)

	// Create routes of different types.
	r1 := &store.ClusterRoute{
		ID:           uuid.New().String(),
		ClusterID:    clusterID,
		ArtifactType: store.ArtifactImage,
		Mode:         store.ModeDirect,
		SourcePrefix: "docker.io/a",
		TargetPrefix: "reg/a",
	}
	r2 := &store.ClusterRoute{
		ID:           uuid.New().String(),
		ClusterID:    clusterID,
		ArtifactType: store.ArtifactChart,
		Mode:         store.ModeReplicated,
		SourcePrefix: "charts.helm.sh/stable",
		TargetPrefix: "reg/charts",
	}
	require.NoError(t, st.ClusterRoutes().Create(ctx, r1))
	require.NoError(t, st.ClusterRoutes().Create(ctx, r2))

	routes, err := st.ClusterRoutes().ListByCluster(ctx, clusterID)
	require.NoError(t, err)
	assert.Len(t, routes, 2)
}

func TestClusterRouteListByClusterAndType(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	_, clusterID := setupClusterRouteFixture(t, st)

	rImage := &store.ClusterRoute{
		ID:           uuid.New().String(),
		ClusterID:    clusterID,
		ArtifactType: store.ArtifactImage,
		Mode:         store.ModeDirect,
		SourcePrefix: "docker.io/img",
		TargetPrefix: "reg/img",
	}
	rChart := &store.ClusterRoute{
		ID:           uuid.New().String(),
		ClusterID:    clusterID,
		ArtifactType: store.ArtifactChart,
		Mode:         store.ModeDirect,
		SourcePrefix: "charts.helm.sh/stable",
		TargetPrefix: "reg/charts",
	}
	require.NoError(t, st.ClusterRoutes().Create(ctx, rImage))
	require.NoError(t, st.ClusterRoutes().Create(ctx, rChart))

	// List only image routes.
	imageRoutes, err := st.ClusterRoutes().ListByClusterAndType(ctx, clusterID, store.ArtifactImage)
	require.NoError(t, err)
	assert.Len(t, imageRoutes, 1)
	assert.Equal(t, store.ArtifactImage, imageRoutes[0].ArtifactType)

	// List only chart routes.
	chartRoutes, err := st.ClusterRoutes().ListByClusterAndType(ctx, clusterID, store.ArtifactChart)
	require.NoError(t, err)
	assert.Len(t, chartRoutes, 1)
	assert.Equal(t, store.ArtifactChart, chartRoutes[0].ArtifactType)
}

func TestClusterRouteUniqueConstraint(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	_, clusterID := setupClusterRouteFixture(t, st)

	r := &store.ClusterRoute{
		ID:           uuid.New().String(),
		ClusterID:    clusterID,
		ArtifactType: store.ArtifactImage,
		Mode:         store.ModeDirect,
		SourcePrefix: "docker.io/unique",
		TargetPrefix: "reg/unique",
	}
	require.NoError(t, st.ClusterRoutes().Create(ctx, r))

	// Duplicate (same cluster + artifact_type + source_prefix) should fail.
	r2 := &store.ClusterRoute{
		ID:           uuid.New().String(),
		ClusterID:    clusterID,
		ArtifactType: store.ArtifactImage,
		Mode:         store.ModeReplicated,
		SourcePrefix: "docker.io/unique",
		TargetPrefix: "reg/other",
	}
	err := st.ClusterRoutes().Create(ctx, r2)
	assert.Error(t, err)
}

func TestClusterRouteCascadeDelete(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	_, clusterID := setupClusterRouteFixture(t, st)

	r := &store.ClusterRoute{
		ID:           uuid.New().String(),
		ClusterID:    clusterID,
		ArtifactType: store.ArtifactImage,
		Mode:         store.ModeDirect,
		SourcePrefix: "docker.io/cascade",
		TargetPrefix: "reg/cascade",
	}
	require.NoError(t, st.ClusterRoutes().Create(ctx, r))

	// Disable then get the cluster — verify FK still works.
	cl, err := st.Clusters().Get(ctx, clusterID)
	require.NoError(t, err)
	cl.Status = store.ClusterDisabled
	require.NoError(t, st.Clusters().Update(ctx, cl, cl.Version))

	// Route should still exist on disabled cluster.
	got, err := st.ClusterRoutes().Get(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, r.ID, got.ID)
}

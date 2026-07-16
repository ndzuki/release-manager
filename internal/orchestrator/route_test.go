package orchestrator

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

//nolint:unparam // customerID returned for consistency; callers may use it in future tests
func seedCustomerAndCluster(t *testing.T, st store.Store) (customerID, clusterID string) {
	t.Helper()
	ctx := context.Background()

	cust := &store.Customer{ID: "cust-route", Name: "Route Test", Slug: "route-test"}
	require.NoError(t, st.Customers().Create(ctx, cust))

	cl := &store.Cluster{ID: "cls-route", Name: "route-target", CustomerID: cust.ID, KubeconfigRef: "kref"}
	require.NoError(t, st.Clusters().Create(ctx, cl))

	return cust.ID, cl.ID
}

func TestConfigureClusterRoute(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	_, clusterID := seedCustomerAndCluster(t, st)

	resp, err := svc.ConfigureClusterRoute(context.Background(), connect.NewRequest(&orchestratorv1.ConfigureClusterRouteRequest{
		ClusterId:    clusterID,
		ArtifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE,
		Mode:         orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT,
		SourcePrefix: "docker.io/myorg",
		TargetPrefix: "registry.internal/myorg",
	}))
	require.NoError(t, err)

	route := resp.Msg.GetRoute()
	assert.NotEmpty(t, route.GetId())
	assert.Equal(t, clusterID, route.GetClusterId())
	assert.Equal(t, orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE, route.GetArtifactType())
	assert.Equal(t, orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT, route.GetMode())
	assert.Equal(t, "docker.io/myorg", route.GetSourcePrefix())
	assert.Equal(t, "registry.internal/myorg", route.GetTargetPrefix())
}

func TestConfigureClusterRoute_InvalidArtifactType(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	_, clusterID := seedCustomerAndCluster(t, st)

	_, err := svc.ConfigureClusterRoute(context.Background(), connect.NewRequest(&orchestratorv1.ConfigureClusterRouteRequest{
		ClusterId:    clusterID,
		ArtifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_UNSPECIFIED,
		Mode:         orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT,
		SourcePrefix: "docker.io/myorg",
		TargetPrefix: "registry.internal/myorg",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestConfigureClusterRoute_ChartPullThroughCacheRejected(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	_, clusterID := seedCustomerAndCluster(t, st)

	_, err := svc.ConfigureClusterRoute(context.Background(), connect.NewRequest(&orchestratorv1.ConfigureClusterRouteRequest{
		ClusterId:    clusterID,
		ArtifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_CHART,
		Mode:         orchestratorv1.ArtifactMode_ARTIFACT_MODE_PULL_THROUGH_CACHE,
		SourcePrefix: "charts.helm.sh",
		TargetPrefix: "cache.internal",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestConfigureClusterRoute_ConflictingPrefix(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	_, clusterID := seedCustomerAndCluster(t, st)

	// First route.
	_, err := svc.ConfigureClusterRoute(context.Background(), connect.NewRequest(&orchestratorv1.ConfigureClusterRouteRequest{
		ClusterId:    clusterID,
		ArtifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE,
		Mode:         orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT,
		SourcePrefix: "docker.io/myorg",
		TargetPrefix: "reg1/myorg",
	}))
	require.NoError(t, err)

	// Conflicting route — same cluster, type, and source_prefix.
	_, err = svc.ConfigureClusterRoute(context.Background(), connect.NewRequest(&orchestratorv1.ConfigureClusterRouteRequest{
		ClusterId:    clusterID,
		ArtifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE,
		Mode:         orchestratorv1.ArtifactMode_ARTIFACT_MODE_REPLICATED,
		SourcePrefix: "docker.io/myorg",
		TargetPrefix: "reg2/myorg",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func TestConfigureClusterRoute_DisabledCluster(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	_, clusterID := seedCustomerAndCluster(t, st)

	ctx := context.Background()

	// Disable the cluster.
	cl, err := st.Clusters().Get(ctx, clusterID)
	require.NoError(t, err)
	cl.Status = store.ClusterDisabled
	require.NoError(t, st.Clusters().Update(ctx, cl))

	_, err = svc.ConfigureClusterRoute(ctx, connect.NewRequest(&orchestratorv1.ConfigureClusterRouteRequest{
		ClusterId:    clusterID,
		ArtifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE,
		Mode:         orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT,
		SourcePrefix: "docker.io/test",
		TargetPrefix: "reg/test",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestGetClusterRoutes(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	_, clusterID := seedCustomerAndCluster(t, st)

	// Create two routes.
	_, err := svc.ConfigureClusterRoute(context.Background(), connect.NewRequest(&orchestratorv1.ConfigureClusterRouteRequest{
		ClusterId:    clusterID,
		ArtifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE,
		Mode:         orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT,
		SourcePrefix: "docker.io/a",
		TargetPrefix: "reg/a",
	}))
	require.NoError(t, err)

	_, err = svc.ConfigureClusterRoute(context.Background(), connect.NewRequest(&orchestratorv1.ConfigureClusterRouteRequest{
		ClusterId:    clusterID,
		ArtifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_CHART,
		Mode:         orchestratorv1.ArtifactMode_ARTIFACT_MODE_REPLICATED,
		SourcePrefix: "charts.helm.sh/stable",
		TargetPrefix: "reg/charts",
	}))
	require.NoError(t, err)

	resp, err := svc.GetClusterRoutes(context.Background(), connect.NewRequest(&orchestratorv1.GetClusterRoutesRequest{
		ClusterId: clusterID,
	}))
	require.NoError(t, err)
	assert.Len(t, resp.Msg.GetRoutes(), 2)
}

func TestDeleteClusterRoute(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	_, clusterID := seedCustomerAndCluster(t, st)

	createResp, err := svc.ConfigureClusterRoute(context.Background(), connect.NewRequest(&orchestratorv1.ConfigureClusterRouteRequest{
		ClusterId:    clusterID,
		ArtifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE,
		Mode:         orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT,
		SourcePrefix: "docker.io/del",
		TargetPrefix: "reg/del",
	}))
	require.NoError(t, err)

	_, err = svc.DeleteClusterRoute(context.Background(), connect.NewRequest(&orchestratorv1.DeleteClusterRouteRequest{
		RouteId: createResp.Msg.GetRoute().GetId(),
	}))
	require.NoError(t, err)

	// Verify it's gone.
	_, err = st.ClusterRoutes().Get(context.Background(), createResp.Msg.GetRoute().GetId())
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestDeleteClusterRouteNotFound(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	_, err := svc.DeleteClusterRoute(context.Background(), connect.NewRequest(&orchestratorv1.DeleteClusterRouteRequest{
		RouteId: "nonexistent",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

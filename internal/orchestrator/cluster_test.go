package orchestrator

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

func clusterUpdateFixture(t *testing.T) (*Service, store.Store, *store.Cluster, func()) {
	t.Helper()
	svc, st, cleanup := setupService(t)
	ctx := context.Background()
	customer := &store.Customer{ID: "cust-cluster-update", Name: "Cluster Update", Slug: "cluster-update"}
	require.NoError(t, st.Customers().Create(ctx, customer))
	cluster := &store.Cluster{ID: "cluster-update", Name: "staging", CustomerID: customer.ID}
	require.NoError(t, st.Clusters().Create(ctx, cluster))
	return svc, st, cluster, cleanup
}

func routeInput(id string, artifactType orchestratorv1.ArtifactType, mode orchestratorv1.ArtifactMode, source string) *orchestratorv1.ClusterRouteInput {
	return &orchestratorv1.ClusterRouteInput{
		Id:           id,
		ArtifactType: artifactType,
		Mode:         mode,
		SourcePrefix: source,
		TargetPrefix: "harbor.example.com/proxy/",
	}
}

func TestUpdateCluster_RoutingConflictIdentifiesRule(t *testing.T) {
	svc, _, cluster, cleanup := clusterUpdateFixture(t)
	defer cleanup()

	_, err := svc.UpdateCluster(context.Background(), connect.NewRequest(&orchestratorv1.UpdateClusterRequest{
		ClusterId: cluster.ID,
		Name:      cluster.Name,
		Enabled:   true,
		Version:   cluster.Version,
		Routes: []*orchestratorv1.ClusterRouteInput{
			routeInput("rule-1", orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE, orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT, "docker.io/library/"),
			routeInput("rule-2", orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE, orchestratorv1.ArtifactMode_ARTIFACT_MODE_REPLICATED, "docker.io/library/"),
		},
	}))

	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	detail, detailErr := connectErr.Details()[0].Value()
	require.NoError(t, detailErr)
	validation, ok := detail.(*orchestratorv1.RouteValidationDetail)
	require.True(t, ok)
	assert.Equal(t, "routing_conflict", validation.GetErrorCode())
	assert.Equal(t, "routes[1].sourcePrefix", validation.GetField())
	assert.Equal(t, "rule-1", validation.GetConflictingRuleId())
}

func TestUpdateCluster_RejectsChartPullThroughCache(t *testing.T) {
	svc, _, cluster, cleanup := clusterUpdateFixture(t)
	defer cleanup()

	_, err := svc.UpdateCluster(context.Background(), connect.NewRequest(&orchestratorv1.UpdateClusterRequest{
		ClusterId: cluster.ID,
		Name:      cluster.Name,
		Enabled:   true,
		Version:   cluster.Version,
		Routes: []*orchestratorv1.ClusterRouteInput{
			routeInput("chart-rule", orchestratorv1.ArtifactType_ARTIFACT_TYPE_CHART, orchestratorv1.ArtifactMode_ARTIFACT_MODE_PULL_THROUGH_CACHE, "charts.example.com/"),
		},
	}))

	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "mode_not_supported")
}

func TestUpdateCluster_RejectsUnknownCredentialField(t *testing.T) {
	svc, _, cluster, cleanup := clusterUpdateFixture(t)
	defer cleanup()

	msg := &orchestratorv1.UpdateClusterRequest{
		ClusterId: cluster.ID,
		Name:      cluster.Name,
		Enabled:   true,
		Version:   cluster.Version,
	}
	unknown := protowire.AppendTag(nil, 99, protowire.BytesType)
	unknown = protowire.AppendString(unknown, "bearer-secret")
	msg.ProtoReflect().SetUnknown(unknown)

	_, err := svc.UpdateCluster(context.Background(), connect.NewRequest(msg))

	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "credential_not_allowed")
}

func TestUpdateCluster_IdempotentAndOptimisticLock(t *testing.T) {
	svc, st, cluster, cleanup := clusterUpdateFixture(t)
	defer cleanup()
	request := &orchestratorv1.UpdateClusterRequest{
		ClusterId: cluster.ID,
		Name:      "production",
		Enabled:   true,
		Version:   cluster.Version,
		Routes: []*orchestratorv1.ClusterRouteInput{
			routeInput("rule-1", orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE, orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT, "docker.io/library/"),
		},
	}

	first, err := svc.UpdateCluster(context.Background(), connect.NewRequest(request))
	require.NoError(t, err)
	assert.Equal(t, int64(2), first.Msg.GetCluster().GetVersion())

	request.Version = first.Msg.GetCluster().GetVersion()
	second, err := svc.UpdateCluster(context.Background(), connect.NewRequest(request))
	require.NoError(t, err)
	assert.Equal(t, int64(2), second.Msg.GetCluster().GetVersion())

	stored, err := st.Clusters().Get(context.Background(), cluster.ID)
	require.NoError(t, err)
	stored.Name = "concurrent-change"
	require.NoError(t, st.Clusters().Update(context.Background(), stored, stored.Version))

	_, err = svc.UpdateCluster(context.Background(), connect.NewRequest(request))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "optimistic_lock_conflict")
}

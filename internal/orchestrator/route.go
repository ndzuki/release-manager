package orchestrator

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// ConfigureClusterRoute creates or updates an artifact routing rule.
func (s *Service) ConfigureClusterRoute(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ConfigureClusterRouteRequest],
) (*connect.Response[orchestratorv1.ConfigureClusterRouteResponse], error) {
	msg := req.Msg

	// Verify cluster exists and is active.
	cluster, err := s.store.Clusters().Get(ctx, msg.GetClusterId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound,
				fmt.Errorf("cluster %q not found", msg.GetClusterId()))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if cluster.Status == store.ClusterDisabled {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("cluster %q is disabled", msg.GetClusterId()))
	}

	artifactType := artifactTypeFromProto(msg.GetArtifactType())
	mode := modeFromProto(msg.GetMode())
	sourcePrefix := msg.GetSourcePrefix()
	targetPrefix := msg.GetTargetPrefix()

	if err := ValidateRouteConfig(artifactType, mode, sourcePrefix, targetPrefix); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Check for conflicting routes.
	existing, err := s.store.ClusterRoutes().ListByClusterAndType(ctx, msg.GetClusterId(), artifactType)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("list existing routes: %w", err))
	}

	newRoute := &store.ClusterRoute{
		ClusterID:    msg.GetClusterId(),
		ArtifactType: artifactType,
		Mode:         mode,
		SourcePrefix: sourcePrefix,
		TargetPrefix: targetPrefix,
	}

	isUpdate := false
	id := msg.GetId()
	if id != "" {
		newRoute.ID = id
		// Fetch existing route if updating.
		if existingRoute, err := s.store.ClusterRoutes().Get(ctx, id); err == nil {
			newRoute.CreatedAt = existingRoute.CreatedAt
			isUpdate = true
		}
	} else {
		newRoute.ID = uuid.New().String()
	}

	if err := DetectConflictingRoutes(existing, newRoute); err != nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, err)
	}

	if isUpdate {
		if err := s.store.ClusterRoutes().Update(ctx, newRoute); err != nil {
			s.logger.Error("update cluster route failed", "error", err)
			return nil, connect.NewError(connect.CodeInternal,
				fmt.Errorf("update cluster route: %w", err))
		}
		s.logger.Info("cluster route updated",
			"id", newRoute.ID,
			"cluster_id", newRoute.ClusterID,
			"artifact_type", newRoute.ArtifactType,
			"source_prefix", newRoute.SourcePrefix,
		)
	} else {
		if err := s.store.ClusterRoutes().Create(ctx, newRoute); err != nil {
			s.logger.Error("configure cluster route failed", "error", err)
			return nil, connect.NewError(connect.CodeInternal,
				fmt.Errorf("configure cluster route: %w", err))
		}
		s.logger.Info("cluster route configured",
			"id", newRoute.ID,
			"cluster_id", newRoute.ClusterID,
			"artifact_type", newRoute.ArtifactType,
			"source_prefix", newRoute.SourcePrefix,
		)
	}

	return connect.NewResponse(&orchestratorv1.ConfigureClusterRouteResponse{
		Route: toProtoRoute(newRoute),
	}), nil
}

// GetClusterRoutes lists all routing rules for a cluster.
func (s *Service) GetClusterRoutes(
	ctx context.Context,
	req *connect.Request[orchestratorv1.GetClusterRoutesRequest],
) (*connect.Response[orchestratorv1.GetClusterRoutesResponse], error) {
	routes, err := s.store.ClusterRoutes().ListByCluster(ctx, req.Msg.GetClusterId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("list cluster routes: %w", err))
	}

	protoRoutes := make([]*orchestratorv1.ClusterRoute, len(routes))
	for i, r := range routes {
		protoRoutes[i] = toProtoRoute(r)
	}

	return connect.NewResponse(&orchestratorv1.GetClusterRoutesResponse{
		Routes: protoRoutes,
	}), nil
}

// DeleteClusterRoute removes an artifact routing rule.
func (s *Service) DeleteClusterRoute(
	ctx context.Context,
	req *connect.Request[orchestratorv1.DeleteClusterRouteRequest],
) (*connect.Response[orchestratorv1.DeleteClusterRouteResponse], error) {
	if err := s.store.ClusterRoutes().Delete(ctx, req.Msg.GetRouteId()); err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound,
				fmt.Errorf("route %q not found", req.Msg.GetRouteId()))
		}
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("delete cluster route: %w", err))
	}

	s.logger.Info("cluster route deleted", "route_id", req.Msg.GetRouteId())
	return connect.NewResponse(&orchestratorv1.DeleteClusterRouteResponse{}), nil
}

// artifactTypeFromProto converts a proto ArtifactType to store.ArtifactType.
func artifactTypeFromProto(t orchestratorv1.ArtifactType) store.ArtifactType {
	switch t {
	case orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE:
		return store.ArtifactImage
	case orchestratorv1.ArtifactType_ARTIFACT_TYPE_CHART:
		return store.ArtifactChart
	default:
		return store.ArtifactType("")
	}
}

// artifactTypeToProto converts a store.ArtifactType to proto ArtifactType.
func artifactTypeToProto(t store.ArtifactType) orchestratorv1.ArtifactType {
	switch t {
	case store.ArtifactImage:
		return orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE
	case store.ArtifactChart:
		return orchestratorv1.ArtifactType_ARTIFACT_TYPE_CHART
	default:
		return orchestratorv1.ArtifactType_ARTIFACT_TYPE_UNSPECIFIED
	}
}

// modeFromProto converts a proto ArtifactMode to store.ArtifactMode.
func modeFromProto(m orchestratorv1.ArtifactMode) store.ArtifactMode {
	switch m {
	case orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT:
		return store.ModeDirect
	case orchestratorv1.ArtifactMode_ARTIFACT_MODE_PULL_THROUGH_CACHE:
		return store.ModePullThroughCache
	case orchestratorv1.ArtifactMode_ARTIFACT_MODE_REPLICATED:
		return store.ModeReplicated
	default:
		return store.ArtifactMode("")
	}
}

// modeToProto converts a store.ArtifactMode to proto ArtifactMode.
func modeToProto(m store.ArtifactMode) orchestratorv1.ArtifactMode {
	switch m {
	case store.ModeDirect:
		return orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT
	case store.ModePullThroughCache:
		return orchestratorv1.ArtifactMode_ARTIFACT_MODE_PULL_THROUGH_CACHE
	case store.ModeReplicated:
		return orchestratorv1.ArtifactMode_ARTIFACT_MODE_REPLICATED
	default:
		return orchestratorv1.ArtifactMode_ARTIFACT_MODE_UNSPECIFIED
	}
}

func toProtoRoute(r *store.ClusterRoute) *orchestratorv1.ClusterRoute {
	return &orchestratorv1.ClusterRoute{
		Id:           r.ID,
		ClusterId:    r.ClusterID,
		ArtifactType: artifactTypeToProto(r.ArtifactType),
		Mode:         modeToProto(r.Mode),
		SourcePrefix: r.SourcePrefix,
		TargetPrefix: r.TargetPrefix,
	}
}

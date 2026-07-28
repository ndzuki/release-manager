//nolint:dupl // Customer and Cluster handlers share the same CRUD pattern
package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// CreateCluster creates a new cluster under a customer.
func (s *Service) CreateCluster(
	ctx context.Context,
	req *connect.Request[orchestratorv1.CreateClusterRequest],
) (*connect.Response[orchestratorv1.CreateClusterResponse], error) {
	msg := req.Msg

	// Verify customer exists and is active.
	cust, err := s.store.Customers().Get(ctx, msg.GetCustomerId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("customer %q not found", msg.GetCustomerId()))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if cust.Status == store.CustomerDisabled {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("customer %q is disabled", msg.GetCustomerId()))
	}

	id := msg.GetId()
	if id == "" {
		id = uuid.New().String()
	}

	c := &store.Cluster{
		ID:            id,
		Name:          msg.GetName(),
		CustomerID:    msg.GetCustomerId(),
		KubeconfigRef: msg.GetKubeconfigRef(),
		Status:        store.ClusterActive,
	}

	if err := s.store.Clusters().Create(ctx, c); err != nil {
		s.logger.Error("create cluster failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create cluster: %w", err))
	}

	s.logger.Info("cluster created", "id", c.ID, "customer_id", c.CustomerID)
	return connect.NewResponse(&orchestratorv1.CreateClusterResponse{
		Cluster: toProtoCluster(c),
	}), nil
}

// UpdateCluster atomically validates and saves cluster metadata and routes.
func (s *Service) UpdateCluster(
	ctx context.Context,
	req *connect.Request[orchestratorv1.UpdateClusterRequest],
) (*connect.Response[orchestratorv1.UpdateClusterResponse], error) {
	msg := req.Msg
	if unknown := msg.ProtoReflect().GetUnknown(); len(unknown) > 0 {
		return nil, newRouteValidationError(
			connect.CodeInvalidArgument,
			"credential_not_allowed",
			"credential",
			"registry credentials are not accepted",
			"",
		)
	}

	c, err := s.store.Clusters().Get(ctx, msg.GetClusterId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("cluster %q not found", msg.GetClusterId()))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get cluster: %w", err))
	}
	if msg.GetVersion() != c.Version {
		return nil, newRouteValidationError(
			connect.CodeAborted,
			"optimistic_lock_conflict",
			"version",
			"data was modified by another user",
			"",
		)
	}

	name := msg.GetName()
	if len(name) == 0 || len(name) > 253 {
		return nil, newRouteValidationError(
			connect.CodeInvalidArgument,
			"invalid_name",
			"name",
			"cluster name must contain 1 to 253 characters",
			"",
		)
	}

	existingRoutes, err := s.store.ClusterRoutes().ListByCluster(ctx, c.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list cluster routes: %w", err))
	}
	if requestMatchesCluster(c, existingRoutes, msg) {
		return connect.NewResponse(&orchestratorv1.UpdateClusterResponse{
			Cluster: toProtoClusterWithRouteCount(c, len(existingRoutes)),
			Routes:  routesToProto(existingRoutes),
		}), nil
	}

	validatedRoutes := make([]*store.ClusterRoute, 0, len(msg.GetRoutes()))
	seenPrefixes := make(map[string]string, len(msg.GetRoutes()))
	for index, input := range msg.GetRoutes() {
		artifactType := artifactTypeFromProto(input.GetArtifactType())
		mode := modeFromProto(input.GetMode())
		fieldPrefix := fmt.Sprintf("routes[%d]", index)
		if err := ValidateRouteConfig(artifactType, mode, input.GetSourcePrefix(), input.GetTargetPrefix()); err != nil {
			errorCode := "invalid_uri"
			field := fieldPrefix + ".sourcePrefix"
			if mode == store.ModePullThroughCache && artifactType == store.ArtifactChart {
				errorCode = "mode_not_supported"
				field = fieldPrefix + ".mode"
			}
			return nil, newRouteValidationError(connect.CodeInvalidArgument, errorCode, field, err.Error(), input.GetId())
		}
		key := string(artifactType) + "\x00" + input.GetSourcePrefix()
		if conflictingRuleID, exists := seenPrefixes[key]; exists {
			if conflictingRuleID == "" {
				conflictingRuleID = input.GetId()
			}
			return nil, newRouteValidationError(
				connect.CodeAlreadyExists,
				"routing_conflict",
				fieldPrefix+".sourcePrefix",
				"route source prefix conflicts with another rule",
				conflictingRuleID,
			)
		}
		seenPrefixes[key] = input.GetId()

		routeID := input.GetId()
		if routeID == "" {
			routeID = uuid.New().String()
		}
		validatedRoutes = append(validatedRoutes, &store.ClusterRoute{
			ID:           routeID,
			ClusterID:    c.ID,
			ArtifactType: artifactType,
			Mode:         mode,
			SourcePrefix: input.GetSourcePrefix(),
			TargetPrefix: input.GetTargetPrefix(),
		})
	}

	c.Name = name
	if msg.GetEnabled() {
		c.Status = store.ClusterActive
	} else {
		c.Status = store.ClusterDisabled
	}
	if err := s.store.Clusters().Update(ctx, c, msg.GetVersion()); err != nil {
		if errors.Is(err, store.ErrOptimisticLock) {
			return nil, newRouteValidationError(connect.CodeAborted, "optimistic_lock_conflict", "version", "data was modified by another user", "")
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update cluster: %w", err))
	}

	incomingIDs := make(map[string]struct{}, len(validatedRoutes))
	for _, route := range validatedRoutes {
		incomingIDs[route.ID] = struct{}{}
		if existingRouteByID(existingRoutes, route.ID) != nil {
			if err := s.store.ClusterRoutes().Update(ctx, route); err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update cluster route: %w", err))
			}
			continue
		}
		if err := s.store.ClusterRoutes().Create(ctx, route); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create cluster route: %w", err))
		}
	}
	for _, route := range existingRoutes {
		if _, keep := incomingIDs[route.ID]; keep {
			continue
		}
		if err := s.store.ClusterRoutes().Delete(ctx, route.ID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete cluster route: %w", err))
		}
	}

	return connect.NewResponse(&orchestratorv1.UpdateClusterResponse{
		Cluster: toProtoClusterWithRouteCount(c, len(validatedRoutes)),
		Routes:  routesToProto(validatedRoutes),
	}), nil
}

// GetCluster retrieves a cluster by ID.
func (s *Service) GetCluster(
	ctx context.Context,
	req *connect.Request[orchestratorv1.GetClusterRequest],
) (*connect.Response[orchestratorv1.GetClusterResponse], error) {
	c, err := s.store.Clusters().Get(ctx, req.Msg.GetClusterId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("cluster %q not found", req.Msg.GetClusterId()))
		}
		s.logger.Error("get cluster failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	routes, err := s.store.ClusterRoutes().ListByCluster(ctx, c.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list cluster routes: %w", err))
	}
	return connect.NewResponse(&orchestratorv1.GetClusterResponse{
		Cluster: toProtoClusterWithRouteCount(c, len(routes)),
	}), nil
}

// ListClusters lists all clusters, optionally filtered by customer_id.
func (s *Service) ListClusters(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ListClustersRequest],
) (*connect.Response[orchestratorv1.ListClustersResponse], error) {
	customerID := req.Msg.GetCustomerId()

	var clusters []*store.Cluster
	var err error
	if customerID != "" {
		clusters, err = s.store.Clusters().List(ctx, customerID)
	} else {
		clusters, err = s.store.Clusters().ListAll(ctx)
	}
	if err != nil {
		s.logger.Error("list clusters failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	protoClusters := make([]*commonv1.Cluster, 0, len(clusters))
	for _, c := range clusters {
		routes, routeErr := s.store.ClusterRoutes().ListByCluster(ctx, c.ID)
		if routeErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list cluster routes: %w", routeErr))
		}
		protoClusters = append(protoClusters, toProtoClusterWithRouteCount(c, len(routes)))
	}

	return connect.NewResponse(&orchestratorv1.ListClustersResponse{
		Clusters: protoClusters,
	}), nil
}

// DisableCluster disables a cluster. Disabled clusters cannot be release targets.
func (s *Service) DisableCluster(
	ctx context.Context,
	req *connect.Request[orchestratorv1.DisableClusterRequest],
) (*connect.Response[orchestratorv1.DisableClusterResponse], error) {
	c, err := s.store.Clusters().Get(ctx, req.Msg.GetClusterId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("cluster %q not found", req.Msg.GetClusterId()))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	c.Status = store.ClusterDisabled
	if err := s.store.Clusters().Update(ctx, c, c.Version); err != nil {
		s.logger.Error("disable cluster failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("disable cluster: %w", err))
	}

	// Cascade: revoke all enrollment tokens for this cluster (AC-015-04).
	tokens, err := s.store.EnrollmentTokens().ListByCluster(ctx, c.ID)
	if err != nil {
		s.logger.Warn("listing tokens for cascade revoke", "error", err)
	}
	for _, t := range tokens {
		if !t.Used {
			if err := s.store.EnrollmentTokens().Revoke(ctx, t.ID); err != nil {
				s.logger.Warn("cascade revoke token", "token_id", t.ID, "error", err)
			}
		}
	}

	// Cascade: revoke all active operators for this cluster.
	operators, err := s.store.Operators().ListByCluster(ctx, c.ID)
	if err != nil {
		s.logger.Warn("listing operators for cascade revoke", "error", err)
	}
	for _, op := range operators {
		if op.Status == store.OperatorActive {
			if err := s.store.Operators().Revoke(ctx, op.ID); err != nil {
				s.logger.Warn("cascade revoke operator", "operator_id", op.ID, "error", err)
			}
			// Close active sessions.
			if sess, err := s.store.Sessions().GetActiveByOperator(ctx, op.ID); err == nil {
				if err := s.store.Sessions().UpdateStatus(ctx, sess.ID, store.SessionOffline); err != nil {
					s.logger.Warn("cascade close session", "session_id", sess.ID, "error", err)
				}
			}
		}
	}

	s.logger.Warn("cluster disabled", "id", c.ID, "customer_id", c.CustomerID)
	return connect.NewResponse(&orchestratorv1.DisableClusterResponse{}), nil
}

// toProtoCluster converts a store.Cluster to a commonv1.Cluster proto message.
func toProtoCluster(c *store.Cluster) *commonv1.Cluster {
	return toProtoClusterWithRouteCount(c, 0)
}

func toProtoClusterWithRouteCount(c *store.Cluster, routeCount int) *commonv1.Cluster {
	return &commonv1.Cluster{
		Id:            c.ID,
		Name:          c.Name,
		CustomerId:    c.CustomerID,
		KubeconfigRef: c.KubeconfigRef,
		Status:        clusterStatusToProto(c.Status),
		CreatedAt:     timestamppb.New(c.CreatedAt),
		UpdatedAt:     timestamppb.New(c.UpdatedAt),
		Version:       c.Version,
		RouteCount:    int32(routeCount),
	}
}

func clusterStatusToProto(s store.ClusterStatus) commonv1.ClusterStatus {
	switch s {
	case store.ClusterActive:
		return commonv1.ClusterStatus_CLUSTER_STATUS_ACTIVE
	case store.ClusterDisabled:
		return commonv1.ClusterStatus_CLUSTER_STATUS_DISABLED
	default:
		return commonv1.ClusterStatus_CLUSTER_STATUS_UNSPECIFIED
	}
}

func newRouteValidationError(code connect.Code, errorCode, field, description, conflictingRuleID string) error {
	err := connect.NewError(code, errors.New(errorCode+": "+description))
	detail, detailErr := connect.NewErrorDetail(&orchestratorv1.RouteValidationDetail{
		ErrorCode:         errorCode,
		Field:             field,
		Description:       description,
		ConflictingRuleId: conflictingRuleID,
	})
	if detailErr == nil {
		err.AddDetail(detail)
	}
	return err
}

func requestMatchesCluster(c *store.Cluster, existing []*store.ClusterRoute, msg *orchestratorv1.UpdateClusterRequest) bool {
	wantStatus := store.ClusterDisabled
	if msg.GetEnabled() {
		wantStatus = store.ClusterActive
	}
	if c.Name != msg.GetName() || c.Status != wantStatus || len(existing) != len(msg.GetRoutes()) {
		return false
	}

	for _, input := range msg.GetRoutes() {
		route := existingRouteByID(existing, input.GetId())
		if route == nil ||
			route.ArtifactType != artifactTypeFromProto(input.GetArtifactType()) ||
			route.Mode != modeFromProto(input.GetMode()) ||
			route.SourcePrefix != input.GetSourcePrefix() ||
			route.TargetPrefix != input.GetTargetPrefix() {
			return false
		}
	}
	return true
}

func existingRouteByID(routes []*store.ClusterRoute, id string) *store.ClusterRoute {
	for _, route := range routes {
		if route.ID == id {
			return route
		}
	}
	return nil
}

func routesToProto(routes []*store.ClusterRoute) []*orchestratorv1.ClusterRoute {
	result := make([]*orchestratorv1.ClusterRoute, len(routes))
	for index, route := range routes {
		result[index] = toProtoRoute(route)
	}
	return result
}


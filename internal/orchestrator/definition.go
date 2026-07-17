package orchestrator

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// ── Release Definition Lifecycle ───────────────────────────────

// CreateReleaseDefinition creates a new release definition for a customer cluster.
func (s *Service) CreateReleaseDefinition(
	ctx context.Context,
	req *connect.Request[orchestratorv1.CreateReleaseDefinitionRequest],
) (*connect.Response[orchestratorv1.CreateReleaseDefinitionResponse], error) {
	msg := req.Msg

	if msg.GetCustomerId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("customer_id is required"))
	}
	if msg.GetClusterId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cluster_id is required"))
	}

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

	// Verify cluster exists, belongs to customer, and is active.
	cls, err := s.store.Clusters().Get(ctx, msg.GetClusterId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("cluster %q not found", msg.GetClusterId()))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if cls.CustomerID != msg.GetCustomerId() {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("cluster %q does not belong to customer %q", msg.GetClusterId(), msg.GetCustomerId()))
	}
	if cls.Status == store.ClusterDisabled {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("cluster %q is disabled", msg.GetClusterId()))
	}

	now := time.Now().UTC()
	def := &store.ReleaseDefinition{
		ID:                uuid.New().String(),
		Name:              msg.GetReleaseName(),
		CustomerID:        msg.GetCustomerId(),
		ClusterID:         msg.GetClusterId(),
		Namespace:         msg.GetNamespace(),
		ReleaseName:       msg.GetReleaseName(),
		ChartName:         msg.GetChartName(),
		Status:            store.DefStatusActive,
		OptimisticVersion: 1,
		CreatedBy:         msg.GetActor().GetUserId(),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if !msg.GetEnabled() {
		def.Status = store.DefStatusDraft
	}

	event := &store.ReleaseDefinitionEvent{
		ID:           uuid.New().String(),
		DefinitionID: def.ID,
		EventType:    "definition_created",
		CreatedAt:    now,
	}

	if err := s.store.Definitions().Create(ctx, def, event); err != nil {
		if err == store.ErrDuplicateKey {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("definition already exists for customer=%s cluster=%s namespace=%s release=%s",
					msg.GetCustomerId(), msg.GetClusterId(), msg.GetNamespace(), msg.GetReleaseName()))
		}
		s.logger.Error("create definition failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create definition: %w", err))
	}

	s.logger.Info("definition created",
		"id", def.ID,
		"customer", def.CustomerID,
		"cluster", def.ClusterID,
		"status", def.Status,
	)

	return connect.NewResponse(&orchestratorv1.CreateReleaseDefinitionResponse{
		Definition: toProtoDefinition(def),
	}), nil
}

// GetReleaseDefinition retrieves a release definition by ID.
func (s *Service) GetReleaseDefinition(
	ctx context.Context,
	req *connect.Request[orchestratorv1.GetReleaseDefinitionRequest],
) (*connect.Response[orchestratorv1.GetReleaseDefinitionResponse], error) {
	def, err := s.store.Definitions().Get(ctx, req.Msg.GetDefinitionId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("definition %q not found", req.Msg.GetDefinitionId()))
		}
		s.logger.Error("get definition failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&orchestratorv1.GetReleaseDefinitionResponse{
		Definition: toProtoDefinition(def),
	}), nil
}

// ListReleaseDefinitions lists definitions, optionally filtered by customer and cluster.
func (s *Service) ListReleaseDefinitions(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ListReleaseDefinitionsRequest],
) (*connect.Response[orchestratorv1.ListReleaseDefinitionsResponse], error) {
	defs, err := s.store.Definitions().List(ctx,
		req.Msg.GetCustomerId(), req.Msg.GetClusterId(),
		req.Msg.GetIncludeDisabled(),
	)
	if err != nil {
		s.logger.Error("list definitions failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	protoDefs := make([]*commonv1.ReleaseDefinition, 0, len(defs))
	for _, d := range defs {
		protoDefs = append(protoDefs, toProtoDefinition(d))
	}

	return connect.NewResponse(&orchestratorv1.ListReleaseDefinitionsResponse{
		Definitions: protoDefs,
	}), nil
}

// UpdateReleaseDefinition updates mutable fields of a definition with optimistic locking.
func (s *Service) UpdateReleaseDefinition(
	ctx context.Context,
	req *connect.Request[orchestratorv1.UpdateReleaseDefinitionRequest],
) (*connect.Response[orchestratorv1.UpdateReleaseDefinitionResponse], error) {
	msg := req.Msg

	def, err := s.store.Definitions().Get(ctx, msg.GetDefinitionId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("definition %q not found", msg.GetDefinitionId()))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if expectedVer := msg.GetExpectedVersion(); expectedVer > 0 && int64(def.OptimisticVersion) != expectedVer {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("optimistic_lock_conflict: expected version %d, current %d", expectedVer, def.OptimisticVersion))
	}

	if ns := msg.GetNamespace(); ns != "" {
		def.Namespace = ns
	}
	if rn := msg.GetReleaseName(); rn != "" {
		def.ReleaseName = rn
	}
	if cn := msg.GetChartName(); cn != "" {
		def.ChartName = cn
	}

	updated, err := s.store.Definitions().Update(ctx, def, nil)
	if err != nil {
		if err == store.ErrDuplicateKey {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("unique constraint conflict: another definition already has that key"))
		}
		if err == store.ErrOptimisticLock {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("optimistic_lock_conflict: definition version changed"))
		}
		s.logger.Error("update definition failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update definition: %w", err))
	}

	s.logger.Info("definition updated", "id", updated.ID, "version", updated.OptimisticVersion)
	return connect.NewResponse(&orchestratorv1.UpdateReleaseDefinitionResponse{
		Definition: toProtoDefinition(updated),
	}), nil
}

// DisableReleaseDefinition disables a definition with optimistic locking and emits a domain event.
func (s *Service) DisableReleaseDefinition(
	ctx context.Context,
	req *connect.Request[orchestratorv1.DisableReleaseDefinitionRequest],
) (*connect.Response[orchestratorv1.DisableReleaseDefinitionResponse], error) {
	msg := req.Msg

	def, err := s.store.Definitions().Get(ctx, msg.GetDefinitionId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("definition %q not found", msg.GetDefinitionId()))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if def.Status == store.DefStatusDisabled {
		s.logger.Info("definition already disabled", "id", def.ID)
		return connect.NewResponse(&orchestratorv1.DisableReleaseDefinitionResponse{
			Definition: toProtoDefinition(def),
		}), nil
	}

	def.Status = store.DefStatusDisabled
	now := time.Now().UTC()
	event := &store.ReleaseDefinitionEvent{
		ID:           uuid.New().String(),
		DefinitionID: def.ID,
		EventType:    "definition_disabled",
		CreatedAt:    now,
	}

	updated, err := s.store.Definitions().Update(ctx, def, event)
	if err != nil {
		if err == store.ErrOptimisticLock {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("optimistic_lock_conflict: definition version changed"))
		}
		s.logger.Error("disable definition failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("disable definition: %w", err))
	}

	s.logger.Warn("definition disabled", "id", updated.ID)
	return connect.NewResponse(&orchestratorv1.DisableReleaseDefinitionResponse{
		Definition: toProtoDefinition(updated),
	}), nil
}

// toProtoDefinition converts a store.ReleaseDefinition to a commonv1.ReleaseDefinition proto message.
func toProtoDefinition(def *store.ReleaseDefinition) *commonv1.ReleaseDefinition {
	return &commonv1.ReleaseDefinition{
		Id:          def.ID,
		Name:        def.Name,
		CustomerId:  def.CustomerID,
		ClusterId:   def.ClusterID,
		Namespace:   def.Namespace,
		ReleaseName: def.ReleaseName,
		ChartName:   def.ChartName,
		Status:      string(def.Status),
		Version:     int64(def.OptimisticVersion),
		CreatedBy:   def.CreatedBy,
		CreatedAt:   timestamppb.New(def.CreatedAt),
		UpdatedAt:   timestamppb.New(def.UpdatedAt),
	}
}

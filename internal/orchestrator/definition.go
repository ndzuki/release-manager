package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
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

	// The creating actor's organization owns the definition; it must come from the
	// verified request identity (TASK-357, see releaseDefinitionOwner).
	actor, err := releaseDefinitionOwner(ctx, msg)
	if err != nil {
		return nil, err
	}

	ownerOrg := actor.OrganizationID
	now := time.Now().UTC()
	def := &store.ReleaseDefinition{
		ID:                     uuid.New().String(),
		Name:                   msg.GetReleaseName(),
		CustomerID:             msg.GetCustomerId(),
		ClusterID:              msg.GetClusterId(),
		Namespace:              msg.GetNamespace(),
		ReleaseName:            msg.GetReleaseName(),
		ChartName:              msg.GetChartName(),
		Status:                 store.DefStatusActive,
		OptimisticVersion:      1,
		OwnerOrganizationID:    &ownerOrg,
		CreatedBy:              actor.UserID,
		CreatedAt:              now,
		UpdatedAt:              now,
		HPAManaged:             msg.GetHpaManaged(),
		MaxEmergencyReplicas:   msg.GetMaxEmergencyReplicas(),
		ApprovedAnnotationKeys: approvedAnnotationKeysFromProto(msg.GetApprovedAnnotationKeys()),
		PromotionMappings:      promotionMappingsFromProto(msg.GetPromotionMappings()),
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

// releaseDefinitionOwner resolves the organization that owns a new definition and
// the user recorded as its creator.
//
// The owner MUST come from the verified request identity, never from the request
// body (REQ-018 §关联需求影响 → REQ-040): Submit/Approve/Reject, values writes and
// prepare sessions authorize against OwnerOrganizationID (values_approval.go,
// values_revision.go, prepare_sessions.go). The console never populates the request
// `actor`, so trusting the body silently persisted a NULL owner and created a
// definition that could never be approved (`release_definition_owner_unresolved`,
// TASK-348 found the dead row in a customer cluster). Fail the call instead of
// storing an unusable object.
//
// The request `actor` stays accepted for compatibility but is advisory: a caller
// that needs another organization must switch (SwitchOrganization) and retry with a
// token scoped there. Storing the requested organization would let the body choose
// the owner; ignoring a contradiction would hide the client bug.
func releaseDefinitionOwner(
	ctx context.Context,
	msg *orchestratorv1.CreateReleaseDefinitionRequest,
) (authctx.Actor, error) {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok || actor.OrganizationID == "" {
		return authctx.Actor{}, connect.NewError(connect.CodeUnauthenticated,
			fmt.Errorf("actor organization is required to create a release definition"))
	}
	if requestedOrg := msg.GetActor().GetOrganization(); requestedOrg != "" && requestedOrg != actor.OrganizationID {
		return authctx.Actor{}, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("actor.organization %q does not match the authenticated organization %q",
				requestedOrg, actor.OrganizationID))
	}
	if requestedUser := msg.GetActor().GetUserId(); requestedUser != "" && requestedUser != actor.UserID {
		return authctx.Actor{}, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("actor.user_id %q does not match the authenticated user %q",
				requestedUser, actor.UserID))
	}
	return actor, nil
}

// GetReleaseDefinition retrieves a release definition by ID.
//
//nolint:dupl // Resource Get handlers intentionally share the project-wide Connect error mapping.
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

	applyDefinitionUpdate(def, msg)

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

// applyDefinitionUpdate copies the presence-aware fields of an update request onto def.
//
// Presence decides, and that is the whole point of TASK-214: an ABSENT field leaves the
// stored value alone, while a field that is PRESENT replaces it even when it is empty
// (which is how a caller clears a value). The two list fields cannot carry presence in
// proto3, so they have explicit replacement wrappers: a present wrapper replaces the list,
// including with an empty one, and it wins over the legacy field. The legacy fields stay
// supported for callers that predate the wrappers, replacing only when non-empty.
//
// Note the precision: on the WIRE an empty repeated field decodes to a nil slice, so the
// old `!= nil` test could never fire for a clear attempt (which is how the silent false
// success happened). An in-process Go caller that passed a non-nil empty slice did clear
// the list before this change and no longer does; no such caller exists in the tree.
func applyDefinitionUpdate(def *store.ReleaseDefinition, msg *orchestratorv1.UpdateReleaseDefinitionRequest) {
	if msg.Namespace != nil {
		def.Namespace = msg.GetNamespace()
	}
	if msg.ReleaseName != nil {
		def.ReleaseName = msg.GetReleaseName()
	}
	if msg.ChartName != nil {
		def.ChartName = msg.GetChartName()
	}
	if msg.HpaManaged != nil {
		def.HPAManaged = msg.GetHpaManaged()
	}
	if msg.MaxEmergencyReplicas != nil {
		def.MaxEmergencyReplicas = msg.GetMaxEmergencyReplicas()
	}
	if replace := msg.GetApprovedAnnotationKeysReplace(); replace != nil {
		def.ApprovedAnnotationKeys = approvedAnnotationKeysFromProto(replace.GetItems())
	} else if len(msg.GetApprovedAnnotationKeys()) > 0 {
		def.ApprovedAnnotationKeys = approvedAnnotationKeysFromProto(msg.GetApprovedAnnotationKeys())
	}
	if replace := msg.GetPromotionMappingsReplace(); replace != nil {
		def.PromotionMappings = promotionMappingsFromProto(replace.GetItems())
	} else if len(msg.GetPromotionMappings()) > 0 {
		def.PromotionMappings = promotionMappingsFromProto(msg.GetPromotionMappings())
	}
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
		Id:                     def.ID,
		Name:                   def.Name,
		CustomerId:             def.CustomerID,
		ClusterId:              def.ClusterID,
		Namespace:              def.Namespace,
		ReleaseName:            def.ReleaseName,
		ChartName:              def.ChartName,
		Status:                 string(def.Status),
		Version:                int64(def.OptimisticVersion),
		CreatedBy:              def.CreatedBy,
		CreatedAt:              timestamppb.New(def.CreatedAt),
		UpdatedAt:              timestamppb.New(def.UpdatedAt),
		HpaManaged:             def.HPAManaged,
		MaxEmergencyReplicas:   def.MaxEmergencyReplicas,
		ApprovedAnnotationKeys: mustJSON(def.ApprovedAnnotationKeys),
		PromotionMappings:      mustJSON(def.PromotionMappings),
	}
}

func approvedAnnotationKeysFromProto(values []*orchestratorv1.ApprovedAnnotationKey) []store.ApprovedAnnotationKey {
	result := make([]store.ApprovedAnnotationKey, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		result = append(result, store.ApprovedAnnotationKey{
			Key:                 value.GetKey(),
			Scope:               value.GetScope(),
			PromotionValuesPath: value.GetPromotionValuesPath(),
		})
	}
	return result
}

func promotionMappingsFromProto(values []*orchestratorv1.PromotionMapping) []store.PromotionMapping {
	result := make([]store.PromotionMapping, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		result = append(result, store.PromotionMapping{
			WorkloadKind: value.GetWorkloadKind(),
			WorkloadName: value.GetWorkloadName(),
			Container:    value.GetContainer(),
			Field:        value.GetField(),
			ValuesPath:   value.GetValuesPath(),
		})
	}
	return result
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("[]")
	}
	return encoded
}

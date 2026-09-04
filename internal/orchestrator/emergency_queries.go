package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

func (s *Service) authorizeEmergencyRead(ctx context.Context, definitionID, requestedOrganizationID string) error {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return emergencyError(connect.CodeUnauthenticated, "authentication_required", "authentication required")
	}
	definition, err := s.store.Definitions().Get(ctx, definitionID)
	if errors.Is(err, store.ErrNotFound) {
		return emergencyError(connect.CodeNotFound, "definition_not_found", "release definition not found")
	}
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("load emergency definition: %w", err))
	}
	if requestedOrganizationID != "" && requestedOrganizationID != actor.OrganizationID {
		return emergencyError(connect.CodePermissionDenied, "permission_denied", "organization scope does not match actor")
	}
	if err := s.store.Bindings().RequireActive(ctx, actor.OrganizationID, definition.CustomerID); err != nil {
		return emergencyError(connect.CodePermissionDenied, "permission_denied", "actor is not authorized for customer")
	}
	return nil
}

// ListEmergencyTargets derives one EmergencyTarget per release definition
// from the cached release inventory snapshot and the definition's emergency
// configuration (REQ-081 D1=B). When the inventory row carries the
// authoritative workload identity reported by the operator (REQ-085
// D-110 ②), all four WorkloadRef fields come from it; otherwise name/namespace
// keep the D1=B derivation and kind/uid stay empty (downstream fail-closed).
// Other fields release_inventory does not carry (containers, image refs,
// current replicas/annotations) keep the D7=A unavailable sentinels:
// current_replicas=-1, empty containers/annotations/image refs.
// A definition without an inventory row yields an empty target list (not an
// error).
func (s *Service) ListEmergencyTargets(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ListEmergencyTargetsRequest],
) (*connect.Response[orchestratorv1.ListEmergencyTargetsResponse], error) {
	if req.Msg.GetReleaseDefinitionId() == "" {
		return nil, emergencyError(connect.CodeInvalidArgument, "release_definition_id_required", "release_definition_id is required")
	}
	if err := s.authorizeEmergencyRead(ctx, req.Msg.GetReleaseDefinitionId(), ""); err != nil {
		return nil, err
	}
	definition, err := s.store.Definitions().Get(ctx, req.Msg.GetReleaseDefinitionId())
	if errors.Is(err, store.ErrNotFound) {
		// authorizeEmergencyRead already loaded the definition successfully,
		// so this is a defensive mapping for the concurrent-deletion window.
		return nil, emergencyError(connect.CodeNotFound, "definition_not_found", "release definition not found")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load emergency definition: %w", err))
	}
	inventory, err := s.store.Inventories().GetByDefinition(ctx, req.Msg.GetReleaseDefinitionId())
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewResponse(&orchestratorv1.ListEmergencyTargetsResponse{Targets: []*orchestratorv1.EmergencyTarget{}}), nil
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load emergency inventory: %w", err))
	}

	// REQ-085: the authoritative identity wins when complete (all four
	// fields — same completeness gate as resolveEmergencyWorkloadIdentity);
	// otherwise fall back to D1=B (name/namespace from the inventory row,
	// kind/uid empty, downstream fail-closed).
	workloadRef := &orchestratorv1.WorkloadRef{
		Name:      inventory.ReleaseName,
		Namespace: inventory.Namespace,
	}
	if inventory.WorkloadKind != "" && inventory.WorkloadName != "" && inventory.WorkloadNamespace != "" && inventory.WorkloadUID != "" {
		workloadRef = &orchestratorv1.WorkloadRef{
			Kind:      inventory.WorkloadKind,
			Name:      inventory.WorkloadName,
			Namespace: inventory.WorkloadNamespace,
			Uid:       inventory.WorkloadUID,
		}
	}

	target := &orchestratorv1.EmergencyTarget{
		WorkloadRef: workloadRef,
		// D7=A unavailable sentinels.
		CurrentReplicas:      -1,
		Containers:           []string{},
		CurrentImageRefs:     map[string]string{},
		CurrentAnnotations:   map[string]string{},
		HpaManaged:           definition.HPAManaged,
		MaxEmergencyReplicas: definition.MaxEmergencyReplicas,
		Promotions:           promotionsToProto(definition.PromotionMappings),
		SupportedOperations:  deriveSupportedOperations(definition),
	}
	return connect.NewResponse(&orchestratorv1.ListEmergencyTargetsResponse{Targets: []*orchestratorv1.EmergencyTarget{target}}), nil
}

// promotionsToProto projects stored promotion mappings onto the wire contract.
func promotionsToProto(mappings []store.PromotionMapping) []*orchestratorv1.PromotionMapping {
	result := make([]*orchestratorv1.PromotionMapping, 0, len(mappings))
	for _, mapping := range mappings {
		result = append(result, &orchestratorv1.PromotionMapping{
			WorkloadKind: mapping.WorkloadKind,
			WorkloadName: mapping.WorkloadName,
			Container:    mapping.Container,
			Field:        mapping.Field,
			ValuesPath:   mapping.ValuesPath,
		})
	}
	return result
}

// deriveSupportedOperations computes the emergency actions available under
// the D1=B derived model (REQ-081, REQ-032 §226): the workload kind is
// derived from the promotion mappings (the only kind source) and replicas
// changes are supported for DEPLOYMENT/STATEFUL_SET targets without live HPA
// and with a positive replicas ceiling. Image/annotation operations stay
// degraded — no container or annotation data exists in release_inventory.
func deriveSupportedOperations(definition *store.ReleaseDefinition) []orchestratorv1.EmergencyAction {
	replicasEligible := false
	for _, mapping := range definition.PromotionMappings {
		if mapping.WorkloadKind == workloadDeployment || mapping.WorkloadKind == workloadStatefulSet {
			replicasEligible = true
			break
		}
	}
	if !replicasEligible || definition.HPAManaged || definition.MaxEmergencyReplicas <= 0 {
		return []orchestratorv1.EmergencyAction{}
	}
	return []orchestratorv1.EmergencyAction{orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_REPLICAS}
}

// CheckEmergencyConflict reports a running standard operation for one definition.
func (s *Service) CheckEmergencyConflict(
	ctx context.Context,
	req *connect.Request[orchestratorv1.CheckEmergencyConflictRequest],
) (*connect.Response[orchestratorv1.CheckEmergencyConflictResponse], error) {
	definitionID := req.Msg.GetReleaseDefinitionId()
	if definitionID == "" {
		return nil, emergencyError(connect.CodeInvalidArgument, "release_definition_id_required", "release_definition_id is required")
	}
	if err := s.authorizeEmergencyRead(ctx, definitionID, ""); err != nil {
		return nil, err
	}
	operations, err := s.store.Operations().List(ctx, definitionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list emergency conflicts: %w", err))
	}
	response := &orchestratorv1.CheckEmergencyConflictResponse{}
	for _, operation := range operations {
		if operation.OperationType.IsStandard() && !operation.Status.IsTerminal() {
			response.HasConflict = true
			response.RunningOperation = &orchestratorv1.RunningOperationDetail{
				OperationId: operation.ID, Type: string(operation.OperationType), Status: string(operation.Status),
				StartedAt: timestamppb.New(operation.CreatedAt),
			}
			break
		}
	}
	return connect.NewResponse(response), nil
}

// ListCandidateArtifacts returns validated image artifacts visible to emergency change.
func (s *Service) ListCandidateArtifacts(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ListCandidateArtifactsRequest],
) (*connect.Response[orchestratorv1.ListCandidateArtifactsResponse], error) {
	if req.Msg.GetReleaseDefinitionId() == "" {
		return nil, emergencyError(connect.CodeInvalidArgument, "release_definition_id_required", "release_definition_id is required")
	}
	// AC-079-G5 / D11: container/operation_version cascade parameters require
	// workload_ref.
	if (req.Msg.GetContainer() != "" || req.Msg.GetOperationVersion() != "") && strings.TrimSpace(req.Msg.GetWorkloadRef()) == "" {
		return nil, emergencyError(connect.CodeInvalidArgument, "workload_ref_required", "workload_ref is required when container or operation_version is provided")
	}
	if err := s.authorizeEmergencyRead(ctx, req.Msg.GetReleaseDefinitionId(), req.Msg.GetOrganizationId()); err != nil {
		return nil, err
	}
	artifacts, err := s.store.CandidateArtifacts().ListValidated(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list validated candidate artifacts: %w", err))
	}
	response := &orchestratorv1.ListCandidateArtifactsResponse{Artifacts: make([]*orchestratorv1.CandidateArtifactSummary, 0, len(artifacts))}
	for _, artifact := range artifacts {
		if artifact.ArtifactType != store.ArtifactImage || artifact.ValidatedAt == nil {
			continue
		}
		response.Artifacts = append(response.Artifacts, &orchestratorv1.CandidateArtifactSummary{
			Id: artifact.ID, Repository: emergencyRepository(artifact.Ref), Digest: artifact.Digest,
			Ref: artifact.Ref, ValidatedAt: timestamppb.New(*artifact.ValidatedAt), SourceId: artifact.SourceID,
		})
	}
	return connect.NewResponse(response), nil
}

// ListConvergenceTasks returns persisted convergence work for one definition.
func (s *Service) ListConvergenceTasks(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ListConvergenceTasksRequest],
) (*connect.Response[orchestratorv1.ListConvergenceTasksResponse], error) {
	definitionID := req.Msg.GetReleaseDefinitionId()
	if definitionID == "" {
		return nil, emergencyError(connect.CodeInvalidArgument, "release_definition_id_required", "release_definition_id is required")
	}
	if filter := req.Msg.GetStatusFilter(); filter != "" && filter != "pending_promotion" && filter != "converged" {
		return nil, emergencyError(connect.CodeInvalidArgument, "invalid_status_filter", "status_filter is invalid")
	}
	if err := s.authorizeEmergencyRead(ctx, definitionID, ""); err != nil {
		return nil, err
	}
	tasks, err := s.store.ConvergenceTasks().ListByDefinition(ctx, definitionID, req.Msg.GetStatusFilter())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list convergence tasks: %w", err))
	}
	response := &orchestratorv1.ListConvergenceTasksResponse{Tasks: make([]*orchestratorv1.ConvergenceTaskDetail, 0, len(tasks))}
	for _, task := range tasks {
		var promotionPaths []string
		if len(task.PromotionPaths) > 0 {
			if err := json.Unmarshal(task.PromotionPaths, &promotionPaths); err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode convergence promotion paths: %w", err))
			}
		}
		detail := &orchestratorv1.ConvergenceTaskDetail{
			TaskId: task.ID, OperationId: task.OperationID, OpType: emergencyActionToProto(task.Action),
			TargetSummary: task.TargetSummary, SubmittedAt: timestamppb.New(task.SubmittedAt), Reason: task.Reason,
			PromotionPaths: promotionPaths, Selectable: task.Status == "pending_promotion",
		}
		if task.ActiveRevisionID != nil {
			detail.ActiveRevisionId = *task.ActiveRevisionID
		}
		if task.ActiveRevisionStatus != nil {
			detail.ActiveRevisionStatus = *task.ActiveRevisionStatus
		}
		if !detail.Selectable {
			detail.IncompatibilityReason = "task is already converged"
		}
		response.Tasks = append(response.Tasks, detail)
	}
	return connect.NewResponse(response), nil
}

func emergencyRepository(ref string) string {
	withoutDigest, _, _ := strings.Cut(ref, "@")
	lastSlash := strings.LastIndex(withoutDigest, "/")
	lastColon := strings.LastIndex(withoutDigest, ":")
	if lastColon > lastSlash {
		return withoutDigest[:lastColon]
	}
	return withoutDigest
}

func emergencyActionToProto(action store.EmergencyAction) orchestratorv1.EmergencyAction {
	switch action {
	case store.EmergencySetContainerImage:
		return orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_CONTAINER_IMAGE
	case store.EmergencySetReplicas:
		return orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_REPLICAS
	case store.EmergencySetApprovedAnnotations:
		return orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_APPROVED_ANNOTATION
	default:
		return orchestratorv1.EmergencyAction_EMERGENCY_ACTION_UNSPECIFIED
	}
}

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

// ListEmergencyTargets returns unavailable until the workload manifest snapshot contract is implemented.
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
	return nil, emergencyError(connect.CodeUnavailable, "manifest_inventory_unavailable", "workload manifest inventory is unavailable")
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

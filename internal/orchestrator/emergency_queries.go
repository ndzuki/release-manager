package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

// emergencyObservationMaxAge is the staleness window for an operator-reported
// workload observation (TASK-168 U3, same order of magnitude as the existing
// operator probe TTL). An observation older than this is treated as absent so
// the read model fails closed instead of advertising stale values as current.
const emergencyObservationMaxAge = 15 * time.Minute

// emergencyObservation is the operator-reported mutable workload state for one
// release: the container list, each container's current image ref and the
// observed replica count, stamped with the observation time. Annotations are
// deliberately absent this round (their values may carry sensitive data), so
// annotation operations stay degraded.
//
// The zero value means "not observed": consumers must fail closed.
type emergencyObservation struct {
	Containers       []string
	CurrentImageRefs map[string]string
	CurrentReplicas  int32
	// ReplicasObserved distinguishes an observed 0 from "not observed" (a
	// DaemonSet reports no replica count at all).
	ReplicasObserved bool
	ObservedAt       time.Time
}

// observedFresh reports whether the observation is present and within the
// staleness window at now. A zero ObservedAt is never fresh.
func (o emergencyObservation) observedFresh(now time.Time) bool {
	if o.ObservedAt.IsZero() {
		return false
	}
	return now.Sub(o.ObservedAt) <= emergencyObservationMaxAge
}

// emergencyObservedWorkload projects the operator-reported mutable workload
// state off one release inventory row (TASK-168 W3 columns, written by the
// operator observation report). A zero observed_at means the row carries no
// observation at all — an older operator, or a release that never reported —
// and yields observed=false so every read model path fails closed.
//
// Per-field sentinels are preserved: an observation carrying only a subset of
// the fields still projects that subset, and the missing fields keep their
// unavailable sentinel.
func emergencyObservedWorkload(inventory *store.ReleaseInventory) (emergencyObservation, bool) {
	if inventory == nil || inventory.ObservedAt.IsZero() {
		return emergencyObservation{}, false
	}
	observation := emergencyObservation{
		Containers:       inventory.ObservedContainers,
		CurrentImageRefs: inventory.ObservedImageRefs,
		ObservedAt:       inventory.ObservedAt,
	}
	if inventory.ObservedReplicas != nil {
		observation.CurrentReplicas = *inventory.ObservedReplicas
		observation.ReplicasObserved = true
	}
	return observation, true
}

// emergencyWorkloadView is the fail-closed projection of an observation onto
// the target read model. When no fresh observation exists every field keeps the
// D7=A unavailable sentinel: an empty container/image map and replicas -1.
type emergencyWorkloadView struct {
	Containers       []string
	CurrentImageRefs map[string]string
	CurrentReplicas  int32
	// Observed reports whether a fresh observation backed these values.
	Observed bool
}

// imageSelectable reports whether a fresh observation supplies enough data to
// target a container image change. Annotation operations are not represented
// here: the annotation data plane is out of scope this round.
func (w emergencyWorkloadView) imageSelectable() bool {
	return w.Observed && len(w.Containers) > 0 && len(w.CurrentImageRefs) > 0
}

// projectEmergencyWorkload turns an observation into the fail-closed view used
// by the read model. A missing, zero-timestamp or stale observation (older than
// emergencyObservationMaxAge) yields the sentinels; stale values are never
// projected as current.
func projectEmergencyWorkload(observation emergencyObservation, observed bool, now time.Time) emergencyWorkloadView {
	view := emergencyWorkloadView{
		Containers:       []string{},
		CurrentImageRefs: map[string]string{},
		CurrentReplicas:  -1,
	}
	if !observed || !observation.observedFresh(now) {
		return view
	}
	view.Observed = true
	if len(observation.Containers) > 0 {
		view.Containers = append([]string{}, observation.Containers...)
	}
	if len(observation.CurrentImageRefs) > 0 {
		view.CurrentImageRefs = make(map[string]string, len(observation.CurrentImageRefs))
		for container, ref := range observation.CurrentImageRefs {
			view.CurrentImageRefs[container] = ref
		}
	}
	if observation.ReplicasObserved {
		view.CurrentReplicas = observation.CurrentReplicas
	}
	return view
}

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
// The mutable current values (containers, image refs, replicas) come from the
// operator-reported observation only while it is fresh; a missing or stale
// observation keeps the D7=A unavailable sentinels (current_replicas=-1, empty
// containers/image refs) and the corresponding operation stays unavailable.
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

	observation, observed := emergencyObservedWorkload(inventory)
	workload := projectEmergencyWorkload(observation, observed, time.Now().UTC())

	target := &orchestratorv1.EmergencyTarget{
		WorkloadRef: workloadRef,
		// The workload lives in the definition's customer cluster, so a caller
		// that has to read the workload itself must know which cluster to read.
		Cluster:              inventory.ClusterID,
		CurrentReplicas:      workload.CurrentReplicas,
		Containers:           workload.Containers,
		CurrentImageRefs:     workload.CurrentImageRefs,
		CurrentAnnotations:   map[string]string{},
		HpaManaged:           definition.HPAManaged,
		MaxEmergencyReplicas: definition.MaxEmergencyReplicas,
		Promotions:           promotionsToProto(definition.PromotionMappings),
		SupportedOperations:  deriveSupportedOperations(definition, workload),
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

// deriveSupportedOperations computes the emergency actions available for one
// target. The two dimensions are independent (AC-058-09: image may be
// selectable while replicas is disabled with a stable reason):
//   - SET_REPLICAS requires a DEPLOYMENT/STATEFUL_SET promotion mapping
//     (REQ-032 §226), no live HPA and a positive replicas ceiling;
//   - SET_CONTAINER_IMAGE requires a fresh observation carrying containers and
//     their current image refs (W4). Without one the operation stays degraded.
//
// Annotation operations always stay degraded: the annotation data plane is out
// of scope this round, so no annotation value is ever projected.
func deriveSupportedOperations(definition *store.ReleaseDefinition, workload emergencyWorkloadView) []orchestratorv1.EmergencyAction {
	operations := make([]orchestratorv1.EmergencyAction, 0, 2)
	if emergencyReplicasEligible(definition) {
		operations = append(operations, orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_REPLICAS)
	}
	if workload.imageSelectable() {
		operations = append(operations, orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_CONTAINER_IMAGE)
	}
	return operations
}

// emergencyReplicasEligible reports whether a replicas change is offered for
// the definition (REQ-032 §226): a DEPLOYMENT/STATEFUL_SET promotion mapping
// exists, no HPA manages the workload and the replicas ceiling is positive.
func emergencyReplicasEligible(definition *store.ReleaseDefinition) bool {
	if definition.HPAManaged || definition.MaxEmergencyReplicas <= 0 {
		return false
	}
	for _, mapping := range definition.PromotionMappings {
		if mapping.WorkloadKind == workloadDeployment || mapping.WorkloadKind == workloadStatefulSet {
			return true
		}
	}
	return false
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

// ListCandidateArtifacts returns validated image artifacts visible to emergency
// change. When the request scopes the list to a container, the server derives
// the logical repository from that container's *current* image ref (E3 /
// AC-058-10) and only returns artifacts from the same repository. The current
// ref can only come from a fresh operator observation; without one the scope
// cannot be proven, so the response fails closed with no artifacts. A request
// without a container keeps the unscoped validated-image browse.
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
	workload, err := s.emergencyWorkloadView(ctx, req.Msg.GetReleaseDefinitionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	repository, scoped := emergencyArtifactRepositoryScope(req.Msg.GetContainer(), workload)

	artifacts, err := s.store.CandidateArtifacts().ListValidated(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list validated candidate artifacts: %w", err))
	}
	return connect.NewResponse(&orchestratorv1.ListCandidateArtifactsResponse{
		Artifacts: candidateArtifactSummaries(artifacts, repository, scoped),
	}), nil
}

// candidateArtifactSummaries projects the stored candidates onto the wire
// contract: IMAGE artifacts that were validated, narrowed to one logical
// repository when the request scoped the list to a container.
//
// G11 ruling ② (Lead): candidate selection means "was this artifact
// validated?", never "can it be pulled right now?". A validated artifact
// without a candidate_artifact_locations row is still a candidate — a missing
// location is a pull-time concern for the operator, and the central read model
// must not report it as "does not exist". Both engines therefore select by
// validated_at alone; the validated_at guard below keeps the answer
// engine-independent even for an engine whose query does not enforce it. The
// parity is pinned by TestListCandidateArtifactsEngineParity
// (emergency_queries_parity_integration_test.go) and by the store-level gate in
// internal/store/postgres/candidate_artifacts_parity_integration_test.go — do
// not re-introduce a location-table requirement.
func candidateArtifactSummaries(artifacts []*store.CandidateArtifact, repository string, scoped bool) []*orchestratorv1.CandidateArtifactSummary {
	summaries := make([]*orchestratorv1.CandidateArtifactSummary, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.ArtifactType != store.ArtifactImage || artifact.ValidatedAt == nil {
			continue
		}
		if scoped && emergencyRepository(artifact.Ref) != repository {
			continue
		}
		summaries = append(summaries, &orchestratorv1.CandidateArtifactSummary{
			Id: artifact.ID, Repository: emergencyRepository(artifact.Ref), Digest: artifact.Digest,
			Ref: artifact.Ref, ValidatedAt: timestamppb.New(*artifact.ValidatedAt), SourceId: artifact.SourceID,
		})
	}
	return summaries
}

// emergencyWorkloadView loads the release inventory row for one definition and
// projects it onto the fail-closed workload view. A definition without an
// inventory row has no observation, so it yields the unavailable sentinels.
func (s *Service) emergencyWorkloadView(ctx context.Context, definitionID string) (emergencyWorkloadView, error) {
	inventory, err := s.store.Inventories().GetByDefinition(ctx, definitionID)
	if errors.Is(err, store.ErrNotFound) {
		return projectEmergencyWorkload(emergencyObservation{}, false, time.Now().UTC()), nil
	}
	if err != nil {
		return emergencyWorkloadView{}, fmt.Errorf("load emergency inventory: %w", err)
	}
	observation, observed := emergencyObservedWorkload(inventory)
	return projectEmergencyWorkload(observation, observed, time.Now().UTC()), nil
}

// emergencyArtifactRepositoryScope derives the logical repository that scopes
// the candidate artifact list (E3 / AC-058-10) and whether a filter is
// required. A container request always requires the filter; when the target's
// current image ref for that container is unknown — no fresh observation, or
// the container is not part of it — the returned repository is empty, which
// filters every artifact out. The list never silently falls back to an
// unscoped response.
func emergencyArtifactRepositoryScope(container string, workload emergencyWorkloadView) (string, bool) {
	if strings.TrimSpace(container) == "" {
		return "", false
	}
	if !workload.Observed {
		return "", true
	}
	return emergencyRepository(workload.CurrentImageRefs[container]), true
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

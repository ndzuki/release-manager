package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/store"
)

const (
	workloadDeployment  = "DEPLOYMENT"
	workloadStatefulSet = "STATEFUL_SET"
	workloadDaemonSet   = "DAEMON_SET"
)

// operationVersionPattern is the semver-style contract accepted for
// operation_version (REQ-079 D4/D17; OperationVersionSchema carries a plain
// string, so the server enforces the shape).
var operationVersionPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

// workloadGVRResources maps the operator control-stream GVR form
// ("<gvr.resource>/<namespace>/<name>", per api/proto/operator/v1/operator.proto
// RolloutProgress.workload_ref) to the store workload kinds accepted for
// emergency changes.
var workloadGVRResources = map[string]string{
	"deployments":  workloadDeployment,
	"statefulsets": workloadStatefulSet,
	"daemonsets":   workloadDaemonSet,
}

// parsedWorkloadRef is the decoded canonical workload_ref string.
type parsedWorkloadRef struct {
	Kind      string // store workload kind (DEPLOYMENT/STATEFUL_SET/DAEMON_SET)
	Namespace string
	Name      string
}

type emergencyDispatcher interface {
	DispatchEmergency(context.Context, string, *operatorv1.EmergencyCommand) error
}

type emergencyResolvedChange struct {
	action         store.EmergencyAction
	workload       parsedWorkloadRef
	container      string
	artifactID     string
	imageReference string
	promotionPaths []string
	targetSummary  string
}

// ExecuteEmergencyChange validates and persists a canonical emergency
// operation before immediate Operator delivery (REQ-079, D-94 decision A).
// It replaces the legacy REQ-032 call surface. Creation runs through the
// shared OperationCreationUnitOfWork transaction seam (ADR-009/ADR-014,
// REQ-079 plan Step 3 design comparison option A); authorization precedes the
// idempotency hit (ADR-009).
//
//nolint:gocyclo // Emergency handler runs sequentially ordered validation rules per REQ-079.
func (s *Service) ExecuteEmergencyChange(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ExecuteEmergencyChangeRequest],
) (*connect.Response[orchestratorv1.ExecuteEmergencyChangeResponse], error) {
	ctx = authorization.WithFenceCapture(ctx)
	started := time.Now()
	msg := req.Msg
	strategy := msg.GetConvergenceStrategy()
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		err := emergencyError(connect.CodeUnauthenticated, "authentication_required", "authentication required")
		s.emitEmergencyAttempt(nil, msg.GetReleaseDefinitionId(), "", strategy, err, time.Since(started))
		return nil, err
	}
	if err := validateExecuteEmergencyRequest(msg); err != nil {
		s.emitEmergencyAttempt(&actor, msg.GetReleaseDefinitionId(), "", strategy, err, time.Since(started))
		return nil, err
	}

	// AC-079-G2: the kill switch is the highest-priority gate (REQ-079 D6).
	// Missing configuration fails closed to false (store default).
	emergencyConfig, err := s.store.EmergencyConfig().GetEmergencyConfig(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load emergency config: %w", err))
	}
	if !emergencyConfig.Enabled {
		rpcErr := emergencyDetailError(
			connect.CodeFailedPrecondition,
			orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_KILL_SWITCH_DISABLED,
			"emergency change is disabled by the kill switch", false,
		)
		s.emitEmergencyAttempt(&actor, msg.GetReleaseDefinitionId(), "", strategy, rpcErr, time.Since(started))
		return nil, rpcErr
	}

	definition, err := s.loadEmergencyDefinition(ctx, msg.GetReleaseDefinitionId())
	if err != nil {
		s.emitEmergencyAttempt(&actor, msg.GetReleaseDefinitionId(), "", strategy, err, time.Since(started))
		return nil, err
	}
	customer, err := s.store.Customers().Get(ctx, definition.CustomerID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load emergency customer: %w", err))
	}
	if customer.Status != store.CustomerActive {
		err := emergencyError(connect.CodePermissionDenied, "customer_disabled", "customer is disabled")
		s.emitEmergencyAttempt(&actor, msg.GetReleaseDefinitionId(), "", strategy, err, time.Since(started))
		return nil, err
	}

	requestHash, err := hashExecuteEmergencyRequest(msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hash emergency request: %w", err))
	}
	keyHash := hashEmergencyIdempotencyKey(msg.GetIdempotencyKey())
	resolved, err := s.resolveExecuteEmergencyChange(ctx, msg, definition)
	if err != nil {
		s.emitEmergencyAttempt(&actor, msg.GetReleaseDefinitionId(), "", strategy, err, time.Since(started))
		return nil, err
	}
	if s.authorizer == nil {
		err := emergencyError(connect.CodeUnavailable, "authorization_snapshot_stale", "authorization snapshot is unavailable")
		s.emitEmergencyAttempt(&actor, msg.GetReleaseDefinitionId(), "", strategy, err, time.Since(started))
		return nil, err
	}
	// ADR-009: authorization precedes the idempotency hit.
	if err := s.authorizer.AuthorizeWrite(ctx, actor, definition.CustomerID, store.AuthorizationExecuteEmergency); err != nil {
		s.emitEmergencyAttempt(&actor, msg.GetReleaseDefinitionId(), "", strategy, err, time.Since(started))
		return nil, err
	}
	replay, err := s.store.EmergencyIntents().GetReplay(ctx, actor.OrganizationID+":"+definition.ID, keyHash, requestHash)
	if err == nil {
		return connect.NewResponse(executeEmergencyResponse(replay.Operation, replay.Intent, replay.ConvergenceTask, msg.GetConvergenceStrategy())), nil
	}
	if errors.Is(err, store.ErrIdempotencyConflict) {
		rpcErr := emergencyStoreError(err)
		s.emitEmergencyAttempt(&actor, msg.GetReleaseDefinitionId(), "", strategy, rpcErr, time.Since(started))
		return nil, rpcErr
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("lookup emergency replay: %w", err))
	}
	if msg.GetConvergenceStrategy() == orchestratorv1.ConvergenceStrategy_REQUIRE_PROMOTION {
		blocked, checkErr := s.store.ConvergenceTasks().HasPendingPromotionPath(ctx, definition.ID, resolved.promotionPaths)
		if checkErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check pending promotion paths: %w", checkErr))
		}
		if blocked {
			rpcErr := emergencyDetailError(
				connect.CodeFailedPrecondition,
				orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_LOCKED_PATH,
				"emergency target is locked", false,
			)
			s.emitEmergencyAttempt(&actor, msg.GetReleaseDefinitionId(), "", strategy, rpcErr, time.Since(started))
			return nil, rpcErr
		}
	}

	now := time.Now().UTC()
	// D16: the emergency deadline comes from the configurable
	// emergency.operation_timeout setting (default 30s).
	deadline := now.Add(emergencyConfig.OperationTimeout)
	opID := uuid.NewString()
	commandID := uuid.NewString()
	convergence := convergenceStrategyFromProto(msg.GetConvergenceStrategy())
	promotionPaths, err := json.Marshal(resolved.promotionPaths)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode promotion paths: %w", err))
	}
	intent := &store.EmergencyIntent{
		ID: uuid.NewString(), ReleaseDefinitionID: definition.ID, OperationID: opID, CommandID: commandID,
		Action: resolved.action, WorkloadKind: resolved.workload.Kind, WorkloadName: resolved.workload.Name,
		WorkloadNamespace: resolved.workload.Namespace, WorkloadUID: "",
		Container: &resolved.container, ArtifactID: &resolved.artifactID, ImageReference: &resolved.imageReference,
		Convergence: convergence, PromotionPaths: promotionPaths,
		DeliveryStatus: "pending", CreatedAt: now, UpdatedAt: now,
	}
	op := &store.Operation{
		ID: opID, OperationType: store.OperationEmergency, Status: store.StatusPending,
		ReleaseDefinitionID: definition.ID, IdempotencyKey: keyHash, IdempotencyScope: actor.OrganizationID + ":" + definition.ID,
		RequestHash: requestHash,
		Actor:       store.ActorContext{UserID: actor.UserID, Organization: actor.OrganizationID},
		CreatedAt:   now, UpdatedAt: now, Deadline: &deadline,
	}
	var convergenceTask *store.ConvergenceTask
	if convergence == store.EmergencyRequirePromotion {
		convergenceTask = &store.ConvergenceTask{
			ID: uuid.NewString(), OperationID: opID, ReleaseDefinitionID: definition.ID,
			Action: resolved.action, TargetSummary: resolved.targetSummary, Reason: "",
			PromotionPaths: promotionPaths, Status: "pending_promotion", SubmittedAt: now, CreatedAt: now,
		}
	}
	expectedAuthorizationVersion, ok := authorization.SourceVersionFromContext(ctx)
	if !ok {
		return nil, emergencyError(connect.CodeUnavailable, "authorization_snapshot_stale", "authorization snapshot is unavailable")
	}
	if s.createOperation == nil {
		return nil, emergencyDetailError(
			connect.CodeUnavailable,
			orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_INTERNAL,
			"operation creation unit of work is unavailable", false,
		)
	}
	created, err := s.createOperation(ctx, store.OperationCreationRequest{
		Operation:                    op,
		ExpectedAuthorizationVersion: expectedAuthorizationVersion,
		Emergency: &store.EmergencyCreateCommand{
			Operation:                    op,
			Intent:                       intent,
			ConvergenceTask:              convergenceTask,
			ExpectedAuthorizationVersion: expectedAuthorizationVersion,
			IdempotencyScope:             actor.OrganizationID + ":" + definition.ID,
			IdempotencyKeyHash:           keyHash,
			RequestHash:                  requestHash,
			IdempotencyExpiresAt:         now.Add(24 * time.Hour),
		},
	})
	if err != nil {
		rpcErr := emergencyStoreError(err)
		s.emitEmergencyAttempt(&actor, msg.GetReleaseDefinitionId(), "", strategy, rpcErr, time.Since(started))
		return nil, rpcErr
	}
	if !created.Replayed {
		operatorID, onlineErr := s.onlineEmergencyOperator(ctx, definition)
		if onlineErr != nil {
			return nil, s.failEmergencyDelivery(ctx, created, &actor, msg.GetReleaseDefinitionId(), strategy, onlineErr, time.Since(started))
		}
		command := emergencyCommandFromIntent(created.Intent)
		if s.emergencyDispatcher == nil {
			deliveryErr := emergencyError(connect.CodeUnavailable, "delivery_failed", "emergency dispatcher is unavailable")
			return nil, s.failEmergencyDelivery(ctx, created, &actor, msg.GetReleaseDefinitionId(), strategy, deliveryErr, time.Since(started))
		}
		if err := s.emergencyDispatcher.DispatchEmergency(ctx, operatorID, command); err != nil {
			deliveryErr := emergencyError(connect.CodeUnavailable, "delivery_failed", "emergency command delivery failed")
			return nil, s.failEmergencyDelivery(ctx, created, &actor, msg.GetReleaseDefinitionId(), strategy, deliveryErr, time.Since(started))
		}
		if err := s.store.EmergencyIntents().UpdateDeliveryStatus(ctx, created.Intent.ID, "queued"); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("mark emergency queued: %w", err))
		}
		transitioned, transitionErr := s.store.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, created.Operation.StateVersion, "")
		if transitionErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("mark emergency operation queued: %w", transitionErr))
		}
		created.Operation = transitioned
	}

	s.emitEmergencyAttempt(&actor, msg.GetReleaseDefinitionId(), created.Operation.ID, strategy, nil, time.Since(started))
	s.logger.Info("emergency change accepted", "operation_id", created.Operation.ID, "definition_id", definition.ID,
		"action", resolved.action, "convergence", convergence)
	return connect.NewResponse(executeEmergencyResponse(created.Operation, created.Intent, created.ConvergenceTask, msg.GetConvergenceStrategy())), nil
}

// ExpireEmergencyOperations moves overdue emergency operations to timeout and emits audit evidence.
func (s *Service) ExpireEmergencyOperations(ctx context.Context) int {
	operations, err := s.store.Operations().ListNonTerminal(ctx)
	if err != nil {
		s.logger.Warn("failed to list emergency operations for timeout", "error", err)
		return 0
	}
	now := time.Now().UTC()
	expired := 0
	for _, operation := range operations {
		if operation.OperationType != store.OperationEmergency || operation.Deadline == nil || now.Before(*operation.Deadline) {
			continue
		}
		intent, getErr := s.store.EmergencyIntents().GetByOperationID(ctx, operation.ID)
		if getErr != nil {
			s.logger.Warn("failed to load timed out emergency intent", "operation_id", operation.ID, "error", getErr)
			continue
		}
		finished, finishErr := s.store.EmergencyIntents().Finish(
			ctx, intent.ID, operation.ID, operation.StateVersion, store.StatusTimeout,
			store.EmergencyEffectUnknown, "operation_timeout", nil, nil,
		)
		if finishErr != nil {
			if !errors.Is(finishErr, store.ErrOptimisticLock) && !errors.Is(finishErr, store.ErrInvalidState) {
				s.logger.Warn("failed to time out emergency operation", "operation_id", operation.ID, "error", finishErr)
			}
			continue
		}
		s.emitEmergencyTimeoutAudit(finished, intent)
		expired++
	}
	return expired
}

func (s *Service) emitEmergencyTimeoutAudit(operation *store.Operation, intent *store.EmergencyIntent) {
	if s.auditEmitter == nil || operation == nil || intent == nil {
		return
	}
	event := audit.NewEvent(
		store.AuditActorUser,
		operation.Actor.UserID,
		operation.Actor.Organization,
		"",
		"operation",
		operation.ID,
		"emergency_change",
		"timeout",
		fmt.Sprintf("action=%s convergence=%s", intent.Action, intent.Convergence),
		map[string]string{
			"definition_id": intent.ReleaseDefinitionID,
			"payload_hash":  operation.RequestHash,
			"error_code":    "operation_timeout",
		},
	)
	if result := s.auditEmitter.Emit(event); !result.Accepted {
		s.logger.Warn("emergency timeout audit event rejected", "operation_id", operation.ID, "code", result.Code)
	}
}

// executeEmergencyResponse projects one accepted (or idempotently replayed)
// emergency creation onto the canonical response contract.
func executeEmergencyResponse(
	operation *store.Operation,
	intent *store.EmergencyIntent,
	convergenceTask *store.ConvergenceTask,
	strategy orchestratorv1.ConvergenceStrategy,
) *orchestratorv1.ExecuteEmergencyChangeResponse {
	response := &orchestratorv1.ExecuteEmergencyChangeResponse{
		Result: &orchestratorv1.EmergencyResult{
			Requested:         true, // AC-079-G1: accepted and queued; execution is asynchronous
			ConvergencePolicy: emergencyConvergenceToProto(convergenceStrategyFromProto(strategy)),
		},
	}
	if operation == nil || intent == nil {
		return response
	}
	response.OperationId = operation.ID
	// D17: the authoritative version derives from the Operation state version.
	response.OperationVersion = operationVersionFromStateVersion(operation.StateVersion)
	if convergenceTask != nil {
		response.Result.ConvergenceTasks = []*orchestratorv1.ConvergenceTaskSummary{{
			TaskId: convergenceTask.ID, Status: convergenceTask.Status,
		}}
	}
	return response
}

func operationVersionFromStateVersion(stateVersion int) string {
	if stateVersion < 1 {
		stateVersion = 1
	}
	return fmt.Sprintf("v%d.0.0", stateVersion)
}

func (s *Service) failEmergencyDelivery(
	ctx context.Context,
	created *store.OperationCreationResult,
	actor *authctx.Actor,
	definitionID string,
	strategy orchestratorv1.ConvergenceStrategy,
	deliveryErr error,
	duration time.Duration,
) error {
	operationID := ""
	if created != nil && created.Operation != nil && created.Intent != nil {
		operationID = created.Operation.ID
		if _, err := s.store.EmergencyIntents().Finish(
			ctx,
			created.Intent.ID,
			created.Operation.ID,
			created.Operation.StateVersion,
			store.StatusFailed,
			store.EmergencyEffectNotApplied,
			connectErrorReasonCode(deliveryErr),
			nil,
			nil,
		); err != nil {
			s.logger.Error("failed to mark emergency delivery terminal", "operation_id", operationID, "error", err)
		}
	}
	s.emitEmergencyAttempt(actor, definitionID, operationID, strategy, deliveryErr, duration)
	return deliveryErr
}

func connectErrorReasonCode(err error) string {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		if reason := connectErr.Meta().Get("X-Reason-Code"); reason != "" {
			return reason
		}
	}
	return connect.CodeOf(err).String()
}

// validateExecuteEmergencyRequest enforces the REQ-079 §4 field contract:
// required identifiers, cascade dependencies (D11), convergence strategy
// (D12/D13), target locks (D12) and the operation version shape (D4/D17).
func validateExecuteEmergencyRequest(msg *orchestratorv1.ExecuteEmergencyChangeRequest) error {
	if msg == nil || strings.TrimSpace(msg.GetReleaseDefinitionId()) == "" {
		return emergencyError(connect.CodeInvalidArgument, "release_definition_id_required", "release_definition_id is required")
	}
	if _, err := parseWorkloadRef(msg.GetWorkloadRef()); err != nil {
		return emergencyError(connect.CodeInvalidArgument, "invalid_workload_ref", "workload_ref is invalid")
	}
	if strings.TrimSpace(msg.GetIdempotencyKey()) == "" {
		return emergencyError(connect.CodeInvalidArgument, "idempotency_key_required", "idempotency_key is required")
	}
	// AC-079-G7 / D13: UNSPECIFIED is rejected server-side.
	if msg.GetConvergenceStrategy() == orchestratorv1.ConvergenceStrategy_CONVERGENCE_STRATEGY_UNSPECIFIED {
		return emergencyError(connect.CodeInvalidArgument, "convergence_strategy_required", "convergence strategy is required")
	}
	// AC-079-G8: artifact_ref is mandatory (D14).
	if strings.TrimSpace(msg.GetArtifactRef()) == "" {
		return emergencyError(connect.CodeInvalidArgument, "artifact_ref_required", "artifact_ref is required")
	}
	// AC-079-G9 / D12: REQUIRE_PROMOTION requires target locks.
	if msg.GetConvergenceStrategy() == orchestratorv1.ConvergenceStrategy_REQUIRE_PROMOTION && len(msg.GetTargetLocks()) == 0 {
		return emergencyError(connect.CodeInvalidArgument, "target_locks_required", "target_locks are required for REQUIRE_PROMOTION")
	}
	// D4/D17: operation_version must match the OperationVersionSchema shape
	// (semver-style string).
	if msg.GetOperationVersion() != "" && !operationVersionPattern.MatchString(strings.TrimSpace(msg.GetOperationVersion())) {
		return emergencyDetailError(
			connect.CodeInvalidArgument,
			orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_VERSION_INVALID,
			"operation_version is invalid", false,
		)
	}
	return nil
}

// parseWorkloadRef decodes the canonical "<gvr.resource>/<namespace>/<name>"
// workload_ref string form shared with the operator control stream
// (api/proto/operator/v1/operator.proto).
func parseWorkloadRef(value string) (parsedWorkloadRef, error) {
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) != 3 {
		return parsedWorkloadRef{}, errors.New("workload_ref must be <gvr.resource>/<namespace>/<name>")
	}
	kind, ok := workloadGVRResources[parts[0]]
	if !ok {
		return parsedWorkloadRef{}, fmt.Errorf("unsupported workload GVR %q", parts[0])
	}
	if parts[1] == "" || parts[2] == "" {
		return parsedWorkloadRef{}, errors.New("workload_ref namespace and name are required")
	}
	return parsedWorkloadRef{Kind: kind, Namespace: parts[1], Name: parts[2]}, nil
}

func (s *Service) loadEmergencyDefinition(ctx context.Context, definitionID string) (*store.ReleaseDefinition, error) {
	definition, err := s.store.Definitions().Get(ctx, definitionID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, emergencyError(connect.CodeNotFound, "definition_not_found", "release definition not found")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load emergency definition: %w", err))
	}
	if definition.Status != store.DefStatusActive {
		return nil, emergencyError(connect.CodeFailedPrecondition, "release_definition_disabled", "release definition is disabled")
	}
	return definition, nil
}

func (s *Service) onlineEmergencyOperator(ctx context.Context, definition *store.ReleaseDefinition) (string, error) {
	operator, err := s.store.Operators().GetByClusterID(ctx, definition.ClusterID)
	if err != nil || operator.CustomerID != definition.CustomerID || operator.Status != store.OperatorActive {
		return "", emergencyError(connect.CodeUnavailable, "operator_offline", "operator is offline")
	}
	session, err := s.store.Sessions().GetActiveByOperator(ctx, operator.ID)
	if err != nil || session.Status != store.SessionOnline {
		return "", emergencyError(connect.CodeUnavailable, "operator_offline", "operator is offline")
	}
	return operator.ID, nil
}

// resolveExecuteEmergencyChange resolves the workload_ref string, candidate
// artifact reference and promotion mapping paths (D14).
func (s *Service) resolveExecuteEmergencyChange(
	ctx context.Context,
	msg *orchestratorv1.ExecuteEmergencyChangeRequest,
	definition *store.ReleaseDefinition,
) (emergencyResolvedChange, error) {
	workload, err := parseWorkloadRef(msg.GetWorkloadRef())
	if err != nil {
		return emergencyResolvedChange{}, emergencyError(connect.CodeInvalidArgument, "invalid_workload_ref", "workload_ref is invalid")
	}
	artifact, err := s.resolveEmergencyArtifact(ctx, msg.GetArtifactRef())
	if err != nil {
		return emergencyResolvedChange{}, err
	}
	container := strings.TrimSpace(msg.GetContainer())
	// §4 declares container optional; the image action still requires a
	// concrete target container, so the requirement is enforced here at
	// resolution (same as the legacy SET_CONTAINER_IMAGE resolution).
	if container == "" {
		return emergencyResolvedChange{}, emergencyError(connect.CodeInvalidArgument, "container_required", "container is required for the image change")
	}
	paths := emergencyPromotionPathsForRef(definition, workload, container, "image_digest")
	if msg.GetConvergenceStrategy() == orchestratorv1.ConvergenceStrategy_REQUIRE_PROMOTION && len(paths) == 0 {
		return emergencyResolvedChange{}, emergencyError(connect.CodeFailedPrecondition, "promotion_not_supported", "target has no promotion mapping")
	}
	return emergencyResolvedChange{
		action:         store.EmergencySetContainerImage,
		workload:       workload,
		container:      container,
		artifactID:     artifact.ID,
		imageReference: artifact.Ref,
		promotionPaths: paths,
		targetSummary:  fmt.Sprintf("%s/%s, container=%s", workload.Kind, workload.Name, container),
	}, nil
}

func (s *Service) resolveEmergencyArtifact(ctx context.Context, artifactID string) (*store.CandidateArtifact, error) {
	artifact, err := s.store.CandidateArtifacts().Get(ctx, strings.TrimSpace(artifactID))
	if errors.Is(err, store.ErrNotFound) {
		return nil, emergencyDetailError(
			connect.CodeNotFound,
			orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_ARTIFACT_NOT_FOUND,
			"candidate artifact not found", false,
		)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load emergency artifact: %w", err))
	}
	if artifact.ArtifactType != store.ArtifactImage || artifact.ValidatedAt == nil {
		return nil, emergencyDetailError(
			connect.CodeNotFound,
			orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_NO_CANDIDATE_ARTIFACT,
			"candidate artifact is not validated", true,
		)
	}
	policy := trustPolicyVersion(s.targetEnv)
	verification, err := s.store.Verifications().GetByDigestAndPolicy(ctx, artifact.Digest, policy)
	if err != nil || verification.Status != store.VerificationTrusted {
		return nil, emergencyError(connect.CodeFailedPrecondition, "artifact_not_trusted", "candidate artifact is not trusted")
	}
	meta, err := s.store.TrustRoots().GetPolicy(ctx, s.targetEnv)
	if err != nil || verification.RevocationEpoch < meta.RevocationEpoch {
		return nil, emergencyError(connect.CodeFailedPrecondition, "artifact_not_trusted", "candidate artifact verification is revoked")
	}
	if strings.Contains(artifact.Ref, "@sha256:") {
		return artifact, nil
	}
	artifact.Ref = strings.TrimSuffix(artifact.Ref, ":") + "@" + artifact.Digest
	return artifact, nil
}

func trustPolicyVersion(_ string) string { return "v1" }

func emergencyPromotionPathsForRef(definition *store.ReleaseDefinition, workload parsedWorkloadRef, container, field string) []string {
	paths := make([]string, 0, 1)
	for _, mapping := range definition.PromotionMappings {
		if strings.EqualFold(mapping.WorkloadKind, workload.Kind) && mapping.WorkloadName == workload.Name &&
			mapping.Field == field && mapping.Container == container && mapping.ValuesPath != "" {
			paths = append(paths, mapping.ValuesPath)
		}
	}
	return paths
}

func hashExecuteEmergencyRequest(msg *orchestratorv1.ExecuteEmergencyChangeRequest) (string, error) {
	encoded, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(msg)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func hashEmergencyIdempotencyKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func emergencyStoreError(err error) error {
	switch {
	case errors.Is(err, store.ErrAuthorizationStale):
		return emergencyError(connect.CodeUnavailable, "authorization_snapshot_stale", "authorization snapshot is stale")
	case errors.Is(err, store.ErrReleaseBusy):
		return emergencyError(connect.CodeFailedPrecondition, "release_busy", "release definition has a running standard operation")
	case errors.Is(err, store.ErrEmergencyOperationInProgress):
		// AC-079-G3 / D18: release-global emergency mutex.
		return emergencyDetailError(
			connect.CodeAborted,
			orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_OPERATION_IN_PROGRESS,
			"another emergency operation is in progress for this release", true,
		)
	case errors.Is(err, store.ErrEmergencyConflict):
		return emergencyDetailError(
			connect.CodeFailedPrecondition,
			orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_LOCKED_PATH,
			"emergency target is locked", false,
		)
	case errors.Is(err, store.ErrIdempotencyConflict):
		return emergencyError(connect.CodeAlreadyExists, "idempotency_conflict", "idempotency key was used with different parameters")
	default:
		return connect.NewError(connect.CodeInternal, fmt.Errorf("create emergency operation: %w", err))
	}
}

// emergencyError builds a Connect error carrying the legacy X-Reason-Code
// metadata contract.
func emergencyError(code connect.Code, reason, message string) error {
	err := connect.NewError(code, errors.New(message))
	err.Meta().Set("X-Reason-Code", reason)
	return err
}

// emergencyDetailError builds a Connect error carrying the typed
// EmergencyErrorDetail contract (REQ-079 D2/D3) as a connect error detail and
// the legacy X-Reason-Code metadata.
func emergencyDetailError(code connect.Code, reasonCode orchestratorv1.EmergencyReasonCode, message string, retryable bool) error {
	err := connect.NewError(code, errors.New(message))
	err.Meta().Set("X-Reason-Code", emergencyReasonCodeName(reasonCode))
	detail, detailErr := connect.NewErrorDetail(&orchestratorv1.EmergencyErrorDetail{
		ReasonCode: reasonCode,
		Message:    message,
		Retryable:  retryable,
	})
	if detailErr == nil {
		err.AddDetail(detail)
	}
	return err
}

func emergencyReasonCodeName(code orchestratorv1.EmergencyReasonCode) string {
	name := code.String()
	return strings.TrimPrefix(name, "EMERGENCY_REASON_CODE_")
}

func convergenceStrategyFromProto(value orchestratorv1.ConvergenceStrategy) store.EmergencyConvergence {
	if value == orchestratorv1.ConvergenceStrategy_REVERT_ON_NEXT_RECONCILE {
		return store.EmergencyRevertOnNextReconcile
	}
	return store.EmergencyRequirePromotion
}

func emergencyCommandFromIntent(intent *store.EmergencyIntent) *operatorv1.EmergencyCommand {
	command := &operatorv1.EmergencyCommand{
		CommandId: intent.CommandID, OperationId: intent.OperationID, Action: string(intent.Action),
		WorkloadKind: intent.WorkloadKind, WorkloadName: intent.WorkloadName,
		WorkloadNamespace: intent.WorkloadNamespace, WorkloadUid: intent.WorkloadUID,
	}
	switch intent.Action {
	case store.EmergencySetContainerImage:
		command.Change = &operatorv1.EmergencyCommand_SetContainerImage{SetContainerImage: &operatorv1.EmergencySetContainerImage{
			Container: valueOrEmpty(intent.Container), ImageReference: valueOrEmpty(intent.ImageReference),
		}}
	case store.EmergencySetReplicas:
		command.Change = &operatorv1.EmergencyCommand_SetReplicas{SetReplicas: &operatorv1.EmergencySetReplicas{Replicas: valueOrZero(intent.TargetReplicas)}}
	case store.EmergencySetApprovedAnnotations:
		var entries []struct{ Key, Value string }
		//nolint:errcheck // best-effort proto conversion; Unmarshal failure yields empty entries
		_ = json.Unmarshal(intent.AnnotationEntries, &entries)
		protoEntries := make([]*operatorv1.EmergencyAnnotationEntry, 0, len(entries))
		for _, entry := range entries {
			protoEntries = append(protoEntries, &operatorv1.EmergencyAnnotationEntry{Key: entry.Key, Value: entry.Value})
		}
		command.Change = &operatorv1.EmergencyCommand_SetApprovedAnnotations{SetApprovedAnnotations: &operatorv1.EmergencySetApprovedAnnotations{
			Entries: protoEntries, Scope: valueOrEmpty(intent.AnnotationScope),
		}}
	}
	return command
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func valueOrZero(value *int32) int32 {
	if value == nil {
		return 0
	}
	return *value
}

func (s *Service) emitEmergencyAttempt(actor *authctx.Actor, definitionID, operationID string, strategy orchestratorv1.ConvergenceStrategy, operationErr error, duration time.Duration) {
	if definitionID == "" {
		return
	}
	status := "succeeded"
	errorCode := ""
	if operationErr != nil {
		status = "failed"
		errorCode = connect.CodeOf(operationErr).String()
	}
	actorKind := store.AuditActorAnonymous
	actorID := "anonymous"
	organizationID := ""
	role := ""
	if actor != nil {
		actorKind = store.AuditActorUser
		actorID = actor.UserID
		organizationID = actor.OrganizationID
		if len(actor.Roles) > 0 {
			role = actor.Roles[0]
		}
	}
	resourceID := operationID
	if resourceID == "" {
		resourceID = definitionID
	}
	event := audit.NewEvent(actorKind, actorID, organizationID, role, "operation", resourceID,
		"emergency_change", status,
		fmt.Sprintf("action=%s convergence=%s", store.EmergencySetContainerImage, convergenceStrategyFromProto(strategy)),
		map[string]string{"definition_id": definitionID, "error_code": errorCode})
	event.DurationMs = duration.Milliseconds()
	s.emitAudit(event)
}

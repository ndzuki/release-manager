package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/store"
)

const (
	maxEmergencyReasonBytes = 1000
	maxAnnotationEntries    = 50
	emergencyOperationTTL   = 30 * time.Second
	workloadDeployment      = "DEPLOYMENT"
	workloadStatefulSet     = "STATEFUL_SET"
	workloadDaemonSet       = "DAEMON_SET"
)

type emergencyDispatcher interface {
	DispatchEmergency(context.Context, string, *operatorv1.EmergencyCommand) error
}

type emergencyResolvedChange struct {
	action            store.EmergencyAction
	container         *string
	artifactID        *string
	imageReference    *string
	targetReplicas    *int32
	annotationScope   *string
	annotationEntries json.RawMessage
	promotionPaths    []string
	targetSummary     string
}

// EmergencyChange validates and persists a typed emergency operation before immediate Operator delivery.
// Implements REQ-032.
//
//nolint:gocyclo // Emergency handler runs 16 sequentially ordered validation rules per REQ-032 spec.
func (s *Service) EmergencyChange(
	ctx context.Context,
	req *connect.Request[orchestratorv1.EmergencyChangeRequest],
) (*connect.Response[orchestratorv1.EmergencyChangeResponse], error) {
	ctx = authorization.WithFenceCapture(ctx)
	started := time.Now()
	msg := req.Msg
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		err := emergencyError(connect.CodeUnauthenticated, "authentication_required", "authentication required")
		s.emitEmergencyAttempt(nil, msg, "", err, time.Since(started))
		return nil, err
	}
	if err := validateEmergencyBase(msg); err != nil {
		s.emitEmergencyAttempt(&actor, msg, "", err, time.Since(started))
		return nil, err
	}

	definition, err := s.loadEmergencyDefinition(ctx, msg.GetReleaseDefinitionId())
	if err != nil {
		s.emitEmergencyAttempt(&actor, msg, "", err, time.Since(started))
		return nil, err
	}
	customer, err := s.store.Customers().Get(ctx, definition.CustomerID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load emergency customer: %w", err))
	}
	if customer.Status != store.CustomerActive {
		err := emergencyError(connect.CodePermissionDenied, "customer_disabled", "customer is disabled")
		s.emitEmergencyAttempt(&actor, msg, "", err, time.Since(started))
		return nil, err
	}
	requestHash, err := hashEmergencyRequest(msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hash emergency request: %w", err))
	}
	keyHash := hashEmergencyIdempotencyKey(msg.GetIdempotencyKey())
	resolved, err := s.resolveEmergencyChange(ctx, msg, definition)
	if err != nil {
		s.emitEmergencyAttempt(&actor, msg, "", err, time.Since(started))
		return nil, err
	}
	if s.authorizer == nil {
		err := emergencyError(connect.CodeUnavailable, "authorization_snapshot_stale", "authorization snapshot is unavailable")
		s.emitEmergencyAttempt(&actor, msg, "", err, time.Since(started))
		return nil, err
	}
	if err := s.authorizer.AuthorizeWrite(ctx, actor, definition.CustomerID, store.AuthorizationExecuteEmergency); err != nil {
		s.emitEmergencyAttempt(&actor, msg, "", err, time.Since(started))
		return nil, err
	}
	replay, err := s.store.EmergencyIntents().GetReplay(ctx, actor.OrganizationID+":"+definition.ID, keyHash, requestHash)
	if err == nil {
		return connect.NewResponse(emergencyResponse(replay, msg.GetConvergence())), nil
	}
	if errors.Is(err, store.ErrIdempotencyConflict) {
		rpcErr := emergencyStoreError(err)
		s.emitEmergencyAttempt(&actor, msg, "", rpcErr, time.Since(started))
		return nil, rpcErr
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("lookup emergency replay: %w", err))
	}
	if msg.GetConvergence() == orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REQUIRE_PROMOTION {
		blocked, checkErr := s.store.ConvergenceTasks().HasPendingPromotionPath(ctx, definition.ID, resolved.promotionPaths)
		if checkErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check pending promotion paths: %w", checkErr))
		}
		if blocked {
			rpcErr := emergencyError(connect.CodeFailedPrecondition, "promotion_path_blocked", "emergency target is locked")
			s.emitEmergencyAttempt(&actor, msg, "", rpcErr, time.Since(started))
			return nil, rpcErr
		}
	}

	now := time.Now().UTC()
	deadline := now.Add(emergencyOperationTTL)
	opID := uuid.NewString()
	commandID := uuid.NewString()
	convergence := emergencyConvergenceFromProto(msg.GetConvergence())
	promotionPaths, err := json.Marshal(resolved.promotionPaths)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode promotion paths: %w", err))
	}
	intent := &store.EmergencyIntent{
		ID: uuid.NewString(), ReleaseDefinitionID: definition.ID, OperationID: opID, CommandID: commandID,
		Action: resolved.action, WorkloadKind: msg.GetWorkloadRef().GetKind(), WorkloadName: msg.GetWorkloadRef().GetName(),
		WorkloadNamespace: msg.GetWorkloadRef().GetNamespace(), WorkloadUID: msg.GetWorkloadRef().GetUid(),
		Container: resolved.container, ArtifactID: resolved.artifactID, ImageReference: resolved.imageReference,
		TargetReplicas: resolved.targetReplicas, AnnotationScope: resolved.annotationScope,
		AnnotationEntries: resolved.annotationEntries, Convergence: convergence, PromotionPaths: promotionPaths,
		DeliveryStatus: "pending", CreatedAt: now, UpdatedAt: now,
	}
	op := &store.Operation{
		ID: opID, OperationType: store.OperationEmergency, Status: store.StatusPending,
		ReleaseDefinitionID: definition.ID, IdempotencyKey: keyHash, RequestHash: requestHash,
		Actor:     store.ActorContext{UserID: actor.UserID, Organization: actor.OrganizationID},
		CreatedAt: now, UpdatedAt: now, Deadline: &deadline,
	}
	var convergenceTask *store.ConvergenceTask
	if convergence == store.EmergencyRequirePromotion {
		convergenceTask = &store.ConvergenceTask{
			ID: uuid.NewString(), OperationID: opID, ReleaseDefinitionID: definition.ID,
			Action: resolved.action, TargetSummary: resolved.targetSummary, Reason: strings.TrimSpace(msg.GetReason()),
			PromotionPaths: promotionPaths, Status: "pending_promotion", SubmittedAt: now, CreatedAt: now,
		}
	}
	expectedAuthorizationVersion, ok := authorization.SourceVersionFromContext(ctx)
	if !ok {
		return nil, emergencyError(connect.CodeUnavailable, "authorization_snapshot_stale", "authorization snapshot is unavailable")
	}
	created, err := s.store.EmergencyIntents().CreateIfAvailable(ctx, store.EmergencyCreateCommand{
		Operation: op, Intent: intent, ConvergenceTask: convergenceTask,
		ExpectedAuthorizationVersion: expectedAuthorizationVersion,
		IdempotencyScope:             actor.OrganizationID + ":" + definition.ID,
		IdempotencyKeyHash:           keyHash,
		RequestHash:                  requestHash,
		IdempotencyExpiresAt:         now.Add(24 * time.Hour),
	})
	if err != nil {
		rpcErr := emergencyStoreError(err)
		s.emitEmergencyAttempt(&actor, msg, "", rpcErr, time.Since(started))
		return nil, rpcErr
	}
	if !created.Replayed {
		operatorID, onlineErr := s.onlineEmergencyOperator(ctx, definition)
		if onlineErr != nil {
			return nil, s.failEmergencyDelivery(ctx, created, &actor, msg, onlineErr, time.Since(started))
		}
		command := emergencyCommandFromIntent(created.Intent)
		if s.emergencyDispatcher == nil {
			deliveryErr := emergencyError(connect.CodeUnavailable, "delivery_failed", "emergency dispatcher is unavailable")
			return nil, s.failEmergencyDelivery(ctx, created, &actor, msg, deliveryErr, time.Since(started))
		}
		if err := s.emergencyDispatcher.DispatchEmergency(ctx, operatorID, command); err != nil {
			deliveryErr := emergencyError(connect.CodeUnavailable, "delivery_failed", "emergency command delivery failed")
			return nil, s.failEmergencyDelivery(ctx, created, &actor, msg, deliveryErr, time.Since(started))
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

	s.emitEmergencyAttempt(&actor, msg, created.Operation.ID, nil, time.Since(started))
	s.logger.Info("emergency change accepted", "operation_id", created.Operation.ID, "definition_id", definition.ID,
		"action", resolved.action, "convergence", convergence)
	return connect.NewResponse(emergencyResponse(created, msg.GetConvergence())), nil
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

func emergencyResponse(
	created *store.EmergencyCreateResult,
	convergence orchestratorv1.EmergencyConvergence,
) *orchestratorv1.EmergencyChangeResponse {
	response := &orchestratorv1.EmergencyChangeResponse{Convergence: convergence}
	if created == nil || created.Operation == nil || created.Intent == nil {
		return response
	}
	response.OperationId = created.Operation.ID
	response.Status = string(created.Operation.Status)
	response.ImageReference = valueOrEmpty(created.Intent.ImageReference)
	response.AcceptedAt = timestamppb.New(created.Operation.CreatedAt)
	if created.ConvergenceTask != nil {
		response.ConvergenceTaskId = created.ConvergenceTask.ID
	}
	return response
}

func (s *Service) failEmergencyDelivery(
	ctx context.Context,
	created *store.EmergencyCreateResult,
	actor *authctx.Actor,
	msg *orchestratorv1.EmergencyChangeRequest,
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
	s.emitEmergencyAttempt(actor, msg, operationID, deliveryErr, duration)
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

//nolint:gocyclo // Validation rules must execute in REQ-defined order.
func validateEmergencyBase(msg *orchestratorv1.EmergencyChangeRequest) error {
	if msg == nil || strings.TrimSpace(msg.GetReleaseDefinitionId()) == "" {
		return emergencyError(connect.CodeInvalidArgument, "release_definition_id_required", "release_definition_id is required")
	}
	if strings.TrimSpace(msg.GetIdempotencyKey()) == "" {
		return emergencyError(connect.CodeInvalidArgument, "idempotency_key_required", "idempotency_key is required")
	}
	ref := msg.GetWorkloadRef()
	if ref == nil || !validEmergencyWorkloadKind(ref.GetKind()) || ref.GetName() == "" || ref.GetNamespace() == "" || ref.GetUid() == "" {
		return emergencyError(connect.CodeInvalidArgument, "invalid_workload_ref", "workload_ref is invalid")
	}
	reason := strings.TrimSpace(msg.GetReason())
	if reason == "" {
		return emergencyError(connect.CodeInvalidArgument, "reason_required", "reason is required")
	}
	if !utf8.ValidString(reason) || len([]byte(reason)) > maxEmergencyReasonBytes || strings.ContainsRune(reason, '\x00') || strings.ContainsRune(reason, '\uFFFE') || strings.ContainsRune(reason, '\uFFFF') {
		return emergencyError(connect.CodeInvalidArgument, "invalid_reason", "reason is invalid")
	}
	if msg.GetConvergence() == orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_UNSPECIFIED {
		return emergencyError(connect.CodeInvalidArgument, "convergence_required", "convergence is required")
	}
	if msg.GetChange() == nil {
		return emergencyError(connect.CodeInvalidArgument, "unsupported_emergency_action", "emergency action is required")
	}
	return nil
}

func validEmergencyWorkloadKind(kind string) bool {
	return kind == workloadDeployment || kind == workloadStatefulSet || kind == workloadDaemonSet
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

func (s *Service) resolveEmergencyChange(ctx context.Context, msg *orchestratorv1.EmergencyChangeRequest, definition *store.ReleaseDefinition) (emergencyResolvedChange, error) {
	ref := msg.GetWorkloadRef()
	switch change := msg.GetChange().(type) {
	case *orchestratorv1.EmergencyChangeRequest_SetContainerImage:
		container := strings.TrimSpace(change.SetContainerImage.GetContainer())
		if container == "" {
			return emergencyResolvedChange{}, emergencyError(connect.CodeInvalidArgument, "container_required", "container is required")
		}
		artifact, err := s.resolveEmergencyArtifact(ctx, change.SetContainerImage.GetArtifactId())
		if err != nil {
			return emergencyResolvedChange{}, err
		}
		paths := emergencyPromotionPaths(definition, ref, container, "image_digest")
		if err := requirePromotionPaths(msg, paths); err != nil {
			return emergencyResolvedChange{}, err
		}
		return emergencyResolvedChange{action: store.EmergencySetContainerImage, container: &container,
			artifactID: &artifact.ID, imageReference: &artifact.Ref, promotionPaths: paths,
			targetSummary: fmt.Sprintf("%s/%s, container=%s", ref.GetKind(), ref.GetName(), container)}, nil
	case *orchestratorv1.EmergencyChangeRequest_SetReplicas:
		if ref.GetKind() == workloadDaemonSet {
			return emergencyResolvedChange{}, emergencyError(connect.CodeInvalidArgument, "workload_kind_not_supported", "DaemonSet replicas are not supported")
		}
		if definition.HPAManaged {
			return emergencyResolvedChange{}, emergencyError(connect.CodeFailedPrecondition, "hpa_managed", "workload is managed by HPA")
		}
		replicas := change.SetReplicas.GetReplicas()
		limit := definition.MaxEmergencyReplicas
		if limit <= 0 {
			limit = 100
		}
		if replicas < 0 || replicas > limit {
			return emergencyResolvedChange{}, emergencyError(connect.CodeInvalidArgument, "invalid_replicas", "replicas exceed the emergency limit")
		}
		paths := emergencyPromotionPaths(definition, ref, "", "replicas")
		if err := requirePromotionPaths(msg, paths); err != nil {
			return emergencyResolvedChange{}, err
		}
		return emergencyResolvedChange{action: store.EmergencySetReplicas, targetReplicas: &replicas,
			promotionPaths: paths, targetSummary: fmt.Sprintf("%s/%s, replicas", ref.GetKind(), ref.GetName())}, nil
	case *orchestratorv1.EmergencyChangeRequest_SetApprovedAnnotations:
		return resolveEmergencyAnnotations(msg, definition)
	default:
		return emergencyResolvedChange{}, emergencyError(connect.CodeInvalidArgument, "unsupported_emergency_action", "emergency action is unsupported")
	}
}

func (s *Service) resolveEmergencyArtifact(ctx context.Context, artifactID string) (*store.CandidateArtifact, error) {
	if strings.TrimSpace(artifactID) == "" {
		return nil, emergencyError(connect.CodeInvalidArgument, "invalid_image_reference", "artifact_id is required")
	}
	artifact, err := s.store.CandidateArtifacts().Get(ctx, artifactID)
	if err != nil || artifact.ArtifactType != store.ArtifactImage || artifact.ValidatedAt == nil {
		return nil, emergencyError(connect.CodeInvalidArgument, "invalid_image_reference", "candidate artifact is not validated")
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

func emergencyPromotionPaths(definition *store.ReleaseDefinition, ref *orchestratorv1.WorkloadRef, container, field string) []string {
	paths := make([]string, 0, 1)
	for _, mapping := range definition.PromotionMappings {
		if strings.EqualFold(mapping.WorkloadKind, ref.GetKind()) && mapping.WorkloadName == ref.GetName() && mapping.Field == field && mapping.Container == container && mapping.ValuesPath != "" {
			paths = append(paths, mapping.ValuesPath)
		}
	}
	return paths
}

func requirePromotionPaths(msg *orchestratorv1.EmergencyChangeRequest, paths []string) error {
	if msg.GetConvergence() == orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REQUIRE_PROMOTION && len(paths) == 0 {
		return emergencyError(connect.CodeFailedPrecondition, "promotion_not_supported", "target has no promotion mapping")
	}
	return nil
}

//nolint:gocyclo // Annotation resolution rules are naturally sequential.
func resolveEmergencyAnnotations(msg *orchestratorv1.EmergencyChangeRequest, definition *store.ReleaseDefinition) (emergencyResolvedChange, error) {
	change := msg.GetSetApprovedAnnotations()
	if change == nil || len(change.GetEntries()) == 0 || len(change.GetEntries()) > maxAnnotationEntries {
		return emergencyResolvedChange{}, emergencyError(connect.CodeInvalidArgument, "invalid_annotation_entries", "annotation entries are invalid")
	}
	allowed := make(map[string]store.ApprovedAnnotationKey, len(definition.ApprovedAnnotationKeys))
	for _, entry := range definition.ApprovedAnnotationKeys {
		allowed[entry.Key] = entry
	}
	seen := make(map[string]struct{}, len(change.GetEntries()))
	entries := make([]map[string]string, 0, len(change.GetEntries()))
	paths := make([]string, 0, len(change.GetEntries()))
	scope := ""
	for _, entry := range change.GetEntries() {
		if entry == nil || entry.GetKey() == "" || entry.GetValue() == "" || len(entry.GetKey()) > 253 || len(entry.GetValue()) > 2048 {
			return emergencyResolvedChange{}, emergencyError(connect.CodeInvalidArgument, "invalid_annotation_entries", "annotation entry is invalid")
		}
		if _, ok := seen[entry.GetKey()]; ok {
			return emergencyResolvedChange{}, emergencyError(connect.CodeInvalidArgument, "duplicate_annotation_key", "annotation key is duplicated")
		}
		seen[entry.GetKey()] = struct{}{}
		approved, ok := allowed[entry.GetKey()]
		if !ok {
			return emergencyResolvedChange{}, emergencyError(connect.CodeInvalidArgument, "annotation_key_not_allowed", "annotation key is not allowed")
		}
		if scope == "" {
			scope = approved.Scope
		} else if scope != approved.Scope {
			return emergencyResolvedChange{}, emergencyError(connect.CodeInvalidArgument, "annotation_scope_mismatch", "annotation scopes must match")
		}
		if approved.PromotionValuesPath != "" {
			paths = append(paths, approved.PromotionValuesPath)
		}
		entries = append(entries, map[string]string{"key": entry.GetKey(), "value": entry.GetValue()})
	}
	if err := requirePromotionPaths(msg, paths); err != nil {
		return emergencyResolvedChange{}, err
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return emergencyResolvedChange{}, connect.NewError(connect.CodeInternal, fmt.Errorf("encode annotations: %w", err))
	}
	return emergencyResolvedChange{action: store.EmergencySetApprovedAnnotations, annotationScope: &scope,
		annotationEntries: encoded, promotionPaths: paths,
		targetSummary: fmt.Sprintf("%s/%s, annotations", msg.GetWorkloadRef().GetKind(), msg.GetWorkloadRef().GetName())}, nil
}

func hashEmergencyRequest(msg *orchestratorv1.EmergencyChangeRequest) (string, error) {
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
	case errors.Is(err, store.ErrEmergencyConflict):
		return emergencyError(connect.CodeFailedPrecondition, "conflicting_emergency", "emergency target is locked")
	case errors.Is(err, store.ErrIdempotencyConflict):
		return emergencyError(connect.CodeAlreadyExists, "idempotency_conflict", "idempotency key was used with different parameters")
	default:
		return connect.NewError(connect.CodeInternal, fmt.Errorf("create emergency operation: %w", err))
	}
}

func emergencyError(code connect.Code, reason, message string) error {
	err := connect.NewError(code, errors.New(message))
	err.Meta().Set("X-Reason-Code", reason)
	return err
}

func emergencyActionFromRequest(msg *orchestratorv1.EmergencyChangeRequest) store.EmergencyAction {
	if msg == nil {
		return ""
	}
	switch msg.GetChange().(type) {
	case *orchestratorv1.EmergencyChangeRequest_SetContainerImage:
		return store.EmergencySetContainerImage
	case *orchestratorv1.EmergencyChangeRequest_SetReplicas:
		return store.EmergencySetReplicas
	case *orchestratorv1.EmergencyChangeRequest_SetApprovedAnnotations:
		return store.EmergencySetApprovedAnnotations
	default:
		return ""
	}
}

func emergencyConvergenceFromProto(value orchestratorv1.EmergencyConvergence) store.EmergencyConvergence {
	if value == orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REVERT_ON_NEXT_RECONCILE {
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

func (s *Service) emitEmergencyAttempt(actor *authctx.Actor, msg *orchestratorv1.EmergencyChangeRequest, operationID string, operationErr error, duration time.Duration) {
	if msg == nil {
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
		resourceID = msg.GetReleaseDefinitionId()
	}
	reason, _ := audit.Sanitize(strings.TrimSpace(msg.GetReason()))
	event := audit.NewEvent(actorKind, actorID, organizationID, role, "operation", resourceID,
		"emergency_change", status,
		fmt.Sprintf("action=%s convergence=%s", emergencyActionFromRequest(msg), emergencyConvergenceFromProto(msg.GetConvergence())),
		map[string]string{"definition_id": msg.GetReleaseDefinitionId(), "reason": reason, "error_code": errorCode})
	event.DurationMs = duration.Milliseconds()
	s.emitAudit(event)
}

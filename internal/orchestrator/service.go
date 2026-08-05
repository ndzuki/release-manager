// Package orchestrator implements the release orchestration Connect service.
package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/audit"
	authctx "github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/orchestrator/operation"
	"github.com/ndzuki/release-manager/internal/orchestrator/preflight"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/trust"
	"github.com/ndzuki/release-manager/internal/vulnerability"
)

// Service implements the OrchestratorServiceHandler Connect interface.
type Service struct {
	store               store.Store
	verifier            trust.Verifier
	targetEnv           string
	coordinator         *preflight.Coordinator
	vulnEval            *vulnerability.Evaluator
	auditEmitter        audit.Sink
	emergencyDispatcher emergencyDispatcher
	logger              *slog.Logger
	authorizer          authorization.Authorizer
}

func NewService(st store.Store, verifier trust.Verifier, targetEnv string, args ...any) *Service {
	var auditEmitter audit.Sink
	var dispatcher emergencyDispatcher
	logger := slog.Default()
	var authorizer authorization.Authorizer
	for _, arg := range args {
		switch value := arg.(type) {
		case audit.Sink:
			auditEmitter = value
		case emergencyDispatcher:
			dispatcher = value
		case authorization.Authorizer:
			authorizer = value
		case *slog.Logger:
			logger = value
		}
	}
	return &Service{
		store:               st,
		verifier:            verifier,
		emergencyDispatcher: dispatcher,
		targetEnv:           targetEnv,
		coordinator:         preflight.NewCoordinator(st.Outbox(), st.Operations(), st.Operators(), st.Definitions(), st.Values(), st.Bundles(), st.PreflightLifecycles(), st.Inventories(), logger),
		auditEmitter:        auditEmitter,
		logger:              logger,
		authorizer:          authorizer,
	}
}

// CreateOperation creates a new release operation from the given request.
//
//nolint:gocyclo // operation creation validates multiple independent policy gates
func (s *Service) CreateOperation(
	ctx context.Context,
	req *connect.Request[orchestratorv1.CreateOperationRequest],
) (*connect.Response[orchestratorv1.CreateOperationResponse], error) {
	msg := req.Msg
	existing, err := s.findIdempotentOperation(ctx, msg)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		s.logger.Info("idempotent operation found", "key", msg.IdempotencyKey, "op_id", existing.ID)
		return connect.NewResponse(s.toResponse(existing, nil)), nil
	}

	opType := store.OperationType(msg.OperationType)
	if !opType.Valid() {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("invalid operation_type: %s", msg.OperationType))
	}

	// 3. Lookup release definition
	def, err := s.store.Definitions().Get(ctx, msg.ReleaseDefinitionId)
	if err == store.ErrNotFound {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("release_definition not found: %s", msg.ReleaseDefinitionId))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("definition lookup: %w", err))
	}

	// Validate definition is active (AC-040-03).
	if err := checkDefinitionOperable(def); err != nil {
		return nil, err
	}

	// AC-013-02: Reject operations for disabled customers.
	if err := s.checkCustomerNotDisabled(ctx, def.CustomerID); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}

	// When a caller supplies an organization, it must have an active customer binding.
	// Legacy in-process callers without organization context remain compatible.
	if organizationID := msg.Actor.GetOrganization(); organizationID != "" {
		if err := s.store.Bindings().RequireActive(ctx, organizationID, def.CustomerID); err != nil {
			if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrBindingRevoked) {
				return nil, connect.NewError(connect.CodePermissionDenied,
					errors.New("customer binding is not active"))
			}
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("binding check: %w", err))
		}
	}
	// EMERGENCY ↔ standard mutual exclusion (REQ-023 AC-023-06, AC-023-07).
	if opType.IsStandard() {
		hasEmergency, err := s.store.Operations().HasActiveEmergencyForDefinition(ctx, msg.ReleaseDefinitionId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("emergency check: %w", err))
		}
		if hasEmergency {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("release_busy: definition %s has a running EMERGENCY", msg.ReleaseDefinitionId))
		}
	}
	if opType == store.OperationEmergency {
		hasStandard, err := s.store.Operations().HasActiveForDefinition(ctx, msg.ReleaseDefinitionId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("standard check: %w", err))
		}
		if hasStandard {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("release_busy: definition %s has an active standard operation", msg.ReleaseDefinitionId))
		}
	}
	if opType.IsStandard() {
		hasPendingPromotion, err := s.store.ConvergenceTasks().HasPendingPromotionForDefinition(ctx, msg.ReleaseDefinitionId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("emergency convergence check: %w", err))
		}
		if hasPendingPromotion {
			promotionErr := connect.NewError(connect.CodeFailedPrecondition, errors.New("promotion_required: emergency change must be promoted before standard release"))
			promotionErr.Meta().Set("X-Reason-Code", "promotion_required")
			promotionErr.Meta().Set("X-Remediation", "create and approve a ValuesRevision that absorbs the pending emergency change")
			return nil, promotionErr
		}
	}
	// AC-021-02: UPGRADE requires a positive expected revision and an approved values revision.
	if opType == store.OperationUpgrade {
		if msg.ExpectedCurrentRevision < 1 {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("expected_current_revision must be >= 1 for %s, got %d", opType, msg.ExpectedCurrentRevision))
		}

		vr, err := s.store.Values().Get(ctx, msg.ValuesRevisionId)
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound,
				fmt.Errorf("values_revision not found: %s", msg.ValuesRevisionId))
		}
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("values_revision lookup: %w", err))
		}
		if vr.ReleaseDefinitionID != def.ID {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("values_revision %s belongs to release_definition %s, not %s", vr.ID, vr.ReleaseDefinitionID, def.ID))
		}
		if vr.Status != store.ValuesStatusApproved {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("values_revision %s is %s, must be approved", vr.ID, vr.Status))
		}
	}

	if opType == store.OperationRollback && msg.ExpectedCurrentRevision < 1 {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("expected_current_revision must be >= 1 for %s, got %d", opType, msg.ExpectedCurrentRevision))
	}

	// 4.5. Trust verification (REQ-012).
	policy := trust.DefaultPolicy(s.targetEnv)
	bundle, err := s.store.Bundles().Get(ctx, msg.BundleId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("bundle not found: %s", msg.BundleId))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("bundle lookup: %w", err))
	}
	digest := bundle.DigestValue
	if bundle.DigestAlg != "" {
		digest = bundle.DigestAlg + ":" + bundle.DigestValue
	}

	var out *trust.Output
	if s.verifier == nil {
		out = &trust.Output{
			Status:  store.VerificationVerificationUnavailable,
			Summary: "verification_unavailable: verifier is not configured",
		}
	} else {
		out, err = s.verifier.Verify(ctx, trust.Input{
			Digest:       digest,
			SignatureRef: msg.SignatureRef,
			Policy:       policy,
			Environment:  s.targetEnv,
		})
		if err != nil {
			out = &trust.Output{
				Status:  store.VerificationVerificationUnavailable,
				Summary: fmt.Sprintf("verification_unavailable: %v", err),
			}
		} else if out == nil {
			out = &trust.Output{
				Status:  store.VerificationVerificationUnavailable,
				Summary: "verification_unavailable: verifier returned no result",
			}
		}
	}
	if out.Status != store.VerificationTrusted {
		s.emitTrustVerificationAudit(ctx, msg, digest, out)
	}
	responseStatus := out.Status
	switch out.Status {
	case store.VerificationTrusted:
	case store.VerificationRejected:
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(out.Summary))
	case store.VerificationSignatureMissing:
		if policy.FailClosed {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(out.Summary))
		}
		responseStatus = store.VerificationPolicyWarning
	case store.VerificationVerificationUnavailable:
		if policy.FailClosed {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New(out.Summary))
		}
		responseStatus = store.VerificationPolicyWarning
	default:
		if policy.FailClosed {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("artifact trust rejected: %s", out.Summary))
		}
		responseStatus = store.VerificationPolicyWarning
	}
	verifyResult := trust.StatusToProto(responseStatus)

	if opType == store.OperationInstall {
		if msg.GetValuesRevisionId() == "" {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("revision_not_approved: values_revision_id is required"))
		}
		revision, err := s.store.Values().Get(ctx, msg.GetValuesRevisionId())
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("revision_not_approved: values revision not found"))
		}
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("values revision lookup: %w", err))
		}
		if revision.ReleaseDefinitionID != def.ID || revision.Status != store.ValuesStatusApproved {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("revision_not_approved: values revision must be approved for the target definition"))
		}
	}

	// 5. Build operation request hash for idempotency
	reqHash := hashRequest(msg)

	// 6. Build domain Operation
	now := time.Now().UTC()
	op := &store.Operation{
		ID:                  uuid.New().String(),
		OperationType:       opType,
		Status:              operation.InitialStatus(),
		ReleaseDefinitionID: msg.ReleaseDefinitionId,
		IdempotencyKey:      msg.IdempotencyKey,
		RequestHash:         reqHash,
		BundleID:            msg.BundleId,
		ValuesRevisionID:    msg.ValuesRevisionId,
		ExpectedRevision:    int(msg.ExpectedCurrentRevision),
		ValuesPatch:         []byte(msg.ValuesPatch),
		Actor: store.ActorContext{
			UserID:       msg.Actor.GetUserId(),
			Organization: msg.Actor.GetOrganization(),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	// 7. Persist with atomic availability check (AC-062-01).
	if err := s.store.Operations().CreateIfAvailable(ctx, op); err != nil {
		if errors.Is(err, store.ErrReleaseBusy) {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("release_busy: definition %s has active operation", msg.ReleaseDefinitionId))
		}
		if errors.Is(err, store.ErrDuplicateKey) {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("idempotency_key %s already used", msg.IdempotencyKey))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create operation: %w", err))
	}

	// 8. Trigger preflight transition and launch coordinator
	//    Standard ops go pending→preflight, EMERGENCY goes pending→queued
	if opType.IsStandard() {
		next, err := operation.Transition(op.Status, operation.EventStartPreflight)
		if err != nil {
			s.logger.Error("preflight transition failed", "op_id", op.ID, "err", err)
		} else {
			_, err = s.store.Operations().UpdateStatus(ctx, op.ID, next, op.StateVersion, "")
			if err != nil {
				s.logger.Error("preflight status update failed", "op_id", op.ID, "err", err)
			} else {
				op.Status = next
				op.StateVersion++

				// Launch preflight coordinator in background.
				// Use WithoutCancel so the coordinator outlives the HTTP request.
				bgCtx := context.WithoutCancel(ctx)
				go s.coordinator.Run(bgCtx, op)
				s.logger.Info("preflight coordinator launched", "op_id", op.ID)
			}
		}
	}
	s.logger.Info("operation created",
		"op_id", op.ID,
		"type", op.OperationType,
		"definition", op.ReleaseDefinitionID,
	)

	return connect.NewResponse(s.toResponse(op, &verifyResult)), nil
}

func (s *Service) findIdempotentOperation(
	ctx context.Context,
	msg *orchestratorv1.CreateOperationRequest,
) (*store.Operation, error) {
	if msg.IdempotencyKey == "" {
		return nil, nil
	}

	existing, err := s.store.Operations().GetByIdempotencyKey(ctx, msg.IdempotencyKey)
	if err == store.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("idempotency lookup: %w", err))
	}
	if existing.RequestHash != hashRequest(msg) {
		return nil, connect.NewError(connect.CodeAlreadyExists,
			fmt.Errorf("idempotency_conflict: key %s already used with different request", msg.IdempotencyKey))
	}
	return existing, nil
}

// PublishRelease triggers the release pipeline for a definition (skeleton).
func (s *Service) PublishRelease(
	ctx context.Context,
	req *connect.Request[orchestratorv1.PublishReleaseRequest],
) (*connect.Response[orchestratorv1.PublishReleaseResponse], error) {
	msg := req.Msg

	// Verify the definition exists and both customer and cluster are active.
	def, err := s.store.Definitions().Get(ctx, msg.ReleaseDefinitionId)
	if err == store.ErrNotFound {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("release_definition not found: %s", msg.ReleaseDefinitionId))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("definition lookup: %w", err))
	}
	if err := checkDefinitionOperable(def); err != nil {
		return nil, err
	}

	if err := s.checkCustomerNotDisabled(ctx, def.CustomerID); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}

	// AC-014-04: disabled cluster cannot be a release target.
	cluster, err := s.store.Clusters().Get(ctx, def.ClusterID)
	if err == store.ErrNotFound {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("cluster %q not found for definition %s", def.ClusterID, msg.ReleaseDefinitionId))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cluster lookup: %w", err))
	}
	if cluster.Status == store.ClusterDisabled {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("cluster %q is disabled, cannot publish", cluster.ID))
	}

	s.logger.Info("publish release requested (skeleton)", "definition", msg.ReleaseDefinitionId)
	return connect.NewResponse(&orchestratorv1.PublishReleaseResponse{
		OperationId: "",
		Status:      "not_implemented",
	}), nil
}

// GetOperation returns the safe public fields of an operation.
// It intentionally excludes values_patch, idempotency_key, and request_hash.
func (s *Service) GetOperation(
	ctx context.Context,
	req *connect.Request[orchestratorv1.GetOperationRequest],
) (*connect.Response[orchestratorv1.GetOperationResponse], error) {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
	}
	op, err := s.store.Operations().Get(ctx, req.Msg.OperationId)
	if err == store.ErrNotFound {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("operation not found: %s", req.Msg.OperationId))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("operation lookup: %w", err))
	}
	if err := s.authorizeReadOperation(ctx, op, actor); err != nil {
		return nil, err
	}
	response := &orchestratorv1.GetOperationResponse{Operation: toProtoOperation(op)}
	if op.OperationType == store.OperationEmergency {
		emergencyResult, resultErr := s.emergencyOperationResult(ctx, op)
		if resultErr != nil {
			return nil, resultErr
		}
		response.EmergencyResult = emergencyResult
	}
	return connect.NewResponse(response), nil
}

const (
	operationWatchPollInterval = 50 * time.Millisecond
	operationWatchHeartbeat    = 30 * time.Second
)

// WatchOperation streams a consistent snapshot, retained replay, live entries, and heartbeats.
func (s *Service) WatchOperation(
	ctx context.Context,
	req *connect.Request[orchestratorv1.WatchOperationRequest],
	stream *connect.ServerStream[orchestratorv1.WatchOperationResponse],
) error {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
	}
	if req.Msg.GetOperationId() == "" || req.Msg.GetAfterSequence() < 0 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("operation_id is required and after_sequence must be non-negative"))
	}
	snapshot, err := s.store.Timeline().Snapshot(ctx, req.Msg.GetOperationId())
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("operation not found: %s", req.Msg.GetOperationId()))
	}
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("operation timeline snapshot: %w", err))
	}
	if err := s.authorizeReadOperation(ctx, snapshot.Operation, actor); err != nil {
		return err
	}
	if snapshot.RetainedFromSequence > 0 && req.Msg.GetAfterSequence() < snapshot.RetainedFromSequence-1 {
		return operationCursorExpiredError(snapshot)
	}
	requestID := uuid.NewString()
	if err := stream.Send(&orchestratorv1.WatchOperationResponse{
		Payload: &orchestratorv1.WatchOperationResponse_Snapshot{Snapshot: toProtoOperationSnapshot(snapshot)},
	}); err != nil {
		return err
	}
	entries, err := s.store.Timeline().List(ctx, snapshot.Operation.ID, req.Msg.GetAfterSequence(), snapshot.SnapshotSequence)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("operation timeline replay: %w", err))
	}
	lastSequence := req.Msg.GetAfterSequence()
	for _, entry := range entries {
		if err := stream.Send(&orchestratorv1.WatchOperationResponse{
			Payload: &orchestratorv1.WatchOperationResponse_Entry{Entry: toProtoTimelineEntry(entry)},
		}); err != nil {
			return err
		}
		lastSequence = entry.Sequence
	}
	if lastSequence < snapshot.SnapshotSequence {
		lastSequence = snapshot.SnapshotSequence
	}

	pollTicker := time.NewTicker(operationWatchPollInterval)
	defer pollTicker.Stop()
	heartbeatTicker := time.NewTicker(operationWatchHeartbeat)
	defer heartbeatTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-pollTicker.C:
			latest, listErr := s.store.Timeline().List(ctx, snapshot.Operation.ID, lastSequence, 0)
			if listErr != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("operation timeline live read: %w", listErr))
			}
			for _, entry := range latest {
				if err := stream.Send(&orchestratorv1.WatchOperationResponse{
					Payload: &orchestratorv1.WatchOperationResponse_Entry{Entry: toProtoTimelineEntry(entry)},
				}); err != nil {
					return err
				}
				lastSequence = entry.Sequence
			}
		case sentAt := <-heartbeatTicker.C:
			latestSequence, latestErr := s.store.Timeline().LatestSequence(ctx, snapshot.Operation.ID)
			if latestErr != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("operation timeline heartbeat: %w", latestErr))
			}
			if err := stream.Send(&orchestratorv1.WatchOperationResponse{
				Payload: &orchestratorv1.WatchOperationResponse_Heartbeat{Heartbeat: &orchestratorv1.Heartbeat{
					LatestSequence: latestSequence, RequestId: requestID, SentAt: timestamppb.New(sentAt.UTC()),
				}},
			}); err != nil {
				return err
			}
		}
	}
}

func operationCursorExpiredError(snapshot *store.TimelineSnapshot) error {
	err := connect.NewError(connect.CodeOutOfRange, errors.New("cursor_expired: retained timeline no longer includes the requested sequence"))
	err.Meta().Set("X-Reason-Code", "cursor_expired")
	err.Meta().Set("X-Snapshot-Sequence", fmt.Sprintf("%d", snapshot.SnapshotSequence))
	err.Meta().Set("X-Retained-From-Sequence", fmt.Sprintf("%d", snapshot.RetainedFromSequence))
	return err
}

func toProtoOperationSnapshot(snapshot *store.TimelineSnapshot) *orchestratorv1.OperationSnapshot {
	return &orchestratorv1.OperationSnapshot{
		Operation: toProtoOperation(snapshot.Operation), SnapshotSequence: snapshot.SnapshotSequence,
		RetainedFromSequence: snapshot.RetainedFromSequence,
	}
}

func toProtoTimelineEntry(entry *store.OperationTimelineEntry) *orchestratorv1.TimelineEntry {
	result := &orchestratorv1.TimelineEntry{
		Id: entry.ID, OperationId: entry.OperationID, Sequence: entry.Sequence,
		OperationStateVersion: int64(entry.OperationStateVersion), Timestamp: timestamppb.New(entry.CreatedAt),
	}
	switch store.OperationTimelineEntryKind(entry.Kind) {
	case store.TimelineEntryStateTransition:
		result.Kind = orchestratorv1.TimelineEntryKind_TIMELINE_ENTRY_KIND_STATE_TRANSITION
		var data store.StateTransitionTimelineData
		if json.Unmarshal(entry.Data, &data) == nil {
			result.RequestId = data.RequestID
			result.FromState = data.FromState
			result.ToState = data.ToState
			result.ErrorCode = data.ErrorCode
		}
	case store.TimelineEntryEmergencyEffectResolved:
		result.Kind = orchestratorv1.TimelineEntryKind_TIMELINE_ENTRY_KIND_EMERGENCY_EFFECT_RESOLVED
		var data store.EmergencyEffectTimelineData
		if json.Unmarshal(entry.Data, &data) == nil {
			result.RequestId = data.RequestID
			result.EffectFrom = data.EffectFrom
			result.EffectTo = data.EffectTo
		}
	default:
		result.Kind = orchestratorv1.TimelineEntryKind_TIMELINE_ENTRY_KIND_UNSPECIFIED
	}
	return result
}

func emergencyEffectToProto(value store.EmergencyEffectStatus) orchestratorv1.EmergencyEffectStatus {
	switch value {
	case store.EmergencyEffectApplied:
		return orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_APPLIED
	case store.EmergencyEffectNotApplied:
		return orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_NOT_APPLIED
	case store.EmergencyEffectUnknown:
		return orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_UNKNOWN
	default:
		return orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_UNSPECIFIED
	}
}

func (s *Service) emergencyOperationResult(ctx context.Context, op *store.Operation) (*orchestratorv1.EmergencyResult, error) {
	intent, err := s.store.EmergencyIntents().GetByOperationID(ctx, op.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load emergency operation result: %w", err))
	}
	result := &orchestratorv1.EmergencyResult{
		OpType:            emergencyActionToProto(intent.Action),
		ConvergencePolicy: emergencyConvergenceToProto(intent.Convergence),
	}
	result.EffectStatus = emergencyEffectToProto(intent.EffectStatus)
	result.Before, err = emergencyTypedValues(intent.Action, intent.BeforeSnapshot)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode emergency before snapshot: %w", err))
	}
	result.After, err = emergencyTypedValues(intent.Action, intent.AfterSnapshot)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode emergency after snapshot: %w", err))
	}
	if intent.Convergence == store.EmergencyRequirePromotion {
		task, taskErr := s.store.ConvergenceTasks().GetByOperationID(ctx, op.ID)
		if taskErr != nil && !errors.Is(taskErr, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load emergency convergence task: %w", taskErr))
		}
		if task != nil {
			result.ConvergenceTasks = []*orchestratorv1.ConvergenceTaskSummary{{TaskId: task.ID, Status: task.Status}}
		}
	} else {
		result.RevertStatus = "awaiting_standard_release"
	}
	return result, nil
}

func emergencyTypedValues(action store.EmergencyAction, snapshot json.RawMessage) (*orchestratorv1.EmergencyTypedValues, error) {
	if len(snapshot) == 0 {
		return nil, nil
	}
	switch action {
	case store.EmergencySetContainerImage:
		var value struct {
			Container      string `json:"container"`
			ImageReference string `json:"image_reference"`
		}
		if err := json.Unmarshal(snapshot, &value); err != nil {
			return nil, err
		}
		return &orchestratorv1.EmergencyTypedValues{Values: &orchestratorv1.EmergencyTypedValues_ImageRefValues{
			ImageRefValues: &orchestratorv1.ImageRefValues{Container: value.Container, ImageReference: value.ImageReference},
		}}, nil
	case store.EmergencySetReplicas:
		var value struct {
			Replicas int32 `json:"replicas"`
		}
		if err := json.Unmarshal(snapshot, &value); err != nil {
			return nil, err
		}
		return &orchestratorv1.EmergencyTypedValues{Values: &orchestratorv1.EmergencyTypedValues_ReplicasValues{
			ReplicasValues: &orchestratorv1.ReplicasValues{Replicas: value.Replicas},
		}}, nil
	case store.EmergencySetApprovedAnnotations:
		var value struct {
			Annotations []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"annotations"`
		}
		if err := json.Unmarshal(snapshot, &value); err != nil {
			return nil, err
		}
		entries := make([]*orchestratorv1.AnnotationEntry, 0, len(value.Annotations))
		for _, annotation := range value.Annotations {
			entries = append(entries, &orchestratorv1.AnnotationEntry{Key: annotation.Key, Value: annotation.Value})
		}
		return &orchestratorv1.EmergencyTypedValues{Values: &orchestratorv1.EmergencyTypedValues_AnnotationValues{
			AnnotationValues: &orchestratorv1.AnnotationValues{Annotations: entries},
		}}, nil
	default:
		return nil, nil
	}
}

func emergencyConvergenceToProto(value store.EmergencyConvergence) orchestratorv1.EmergencyConvergence {
	if value == store.EmergencyRevertOnNextReconcile {
		return orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REVERT_ON_NEXT_RECONCILE
	}
	return orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REQUIRE_PROMOTION
}

// CancelOperation requests cancellation of a non-terminal operation.
// It checks CanCancel, authorizes via Casbin + definition→customer→binding chain,
// applies CAS on state_version, and persists the transition with idempotency.
//
//nolint:gocyclo // Cancellation orchestrates state machine, authorization, and idempotency gates.
func (s *Service) CancelOperation(
	ctx context.Context,
	req *connect.Request[orchestratorv1.CancelOperationRequest],
) (*connect.Response[orchestratorv1.CancelOperationResponse], error) {
	msg := req.Msg
	idempotencyKey := req.Header().Get("Idempotency-Key")

	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
	}

	if err := validateCancelInput(msg, idempotencyKey); err != nil {
		return nil, err
	}

	op, err := s.store.Operations().Get(ctx, msg.OperationId)
	if err == store.ErrNotFound {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("operation not found: %s", msg.OperationId))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("operation lookup: %w", err))
	}

	if err := s.authorizeCancelOperation(ctx, op, actor); err != nil {
		return nil, err
	}

	if replayed, err := s.replayCancel(ctx, op, actor, msg, idempotencyKey); err != nil {
		return nil, err
	} else if replayed != nil {
		return replayed, nil
	}
	if op.OperationType == store.OperationEmergency && op.Status == store.StatusRunning {
		return nil, cancelOperationError(connect.CodeFailedPrecondition, "cancel_not_allowed",
			fmt.Errorf("running EMERGENCY operation %s cannot be cancelled", op.ID))
	}
	if op.Status == store.StatusCancelling {
		return nil, cancelOperationError(connect.CodeFailedPrecondition, "cancel_not_allowed",
			fmt.Errorf("operation %s is cancelling and awaiting operator acknowledgment", op.ID))
	}

	if !operation.CanCancel(op.Status) {
		return nil, cancelOperationError(connect.CodeFailedPrecondition, "cancel_not_allowed",
			fmt.Errorf("operation %s is %s, cannot be cancelled", op.ID, op.Status))
	}

	targetStatus, err := operation.Transition(op.Status, operation.EventCancel)
	if err != nil {
		return nil, cancelOperationError(connect.CodeFailedPrecondition, "cancel_not_allowed", err)
	}

	// Use the computed target status for the cancel command.
	return s.finishCancelWithTarget(ctx, op, actor, msg, idempotencyKey, targetStatus)
}

func (s *Service) finishCancelWithTarget(
	ctx context.Context,
	op *store.Operation,
	actor authctx.Actor,
	msg *orchestratorv1.CancelOperationRequest,
	idempotencyKey string,
	targetStatus store.OperationStatus,
) (*connect.Response[orchestratorv1.CancelOperationResponse], error) {
	requestID := uuid.New().String()
	scope := operationCancelScope(op.ID, actor.UserID)
	reqHash := hashCancelRequest(msg.OperationId, int(msg.ExpectedStateVersion), msg.Reason)
	keyHash := hashIdempotencyKey(idempotencyKey)

	deliveryStatus, err := s.cancelDeliveryStatus(ctx, op)
	if err != nil {
		return nil, err
	}
	result, err := s.store.Operations().Cancel(ctx, store.OperationCancelCommand{
		OperationID:          op.ID,
		ExpectedStateVersion: int(msg.ExpectedStateVersion),
		TargetStatus:         targetStatus,
		ActorUserID:          actor.UserID,
		Reason:               msg.Reason,
		RequestID:            requestID,
		IdempotencyScope:     scope,
		IdempotencyKeyHash:   keyHash,
		RequestHash:          reqHash,
		DeliveryStatus:       deliveryStatus,
	})
	if err != nil {
		var versionErr *store.OperationStateVersionConflictError
		switch {
		case errors.As(err, &versionErr):
			return nil, cancelOperationError(connect.CodeAborted, "optimistic_lock_conflict",
				fmt.Errorf("state version conflict: expected %d, current %d", versionErr.Expected, versionErr.Current))
		case errors.Is(err, store.ErrIdempotencyConflict):
			return nil, cancelOperationError(connect.CodeAlreadyExists, "idempotency_conflict",
				errors.New("idempotency key conflict: different request for same scope and key"))
		default:
			return nil, cancelOperationError(connect.CodeInternal, "internal_error", fmt.Errorf("cancel operation: %w", err))
		}
	}

	protoOp := toProtoOperation(result.Operation)
	return connect.NewResponse(&orchestratorv1.CancelOperationResponse{
		Operation: protoOp,
		RequestId: result.RequestID,
	}), nil
}

func (s *Service) replayCancel(
	ctx context.Context,
	op *store.Operation,
	actor authctx.Actor,
	msg *orchestratorv1.CancelOperationRequest,
	idempotencyKey string,
) (*connect.Response[orchestratorv1.CancelOperationResponse], error) {
	result, err := s.store.Operations().GetCancelReplay(ctx, store.OperationCancelReplayQuery{
		OperationID:        op.ID,
		ActorUserID:        actor.UserID,
		IdempotencyKeyHash: hashIdempotencyKey(idempotencyKey),
		RequestHash:        hashCancelRequest(msg.OperationId, int(msg.ExpectedStateVersion), msg.Reason),
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if errors.Is(err, store.ErrIdempotencyConflict) {
		return nil, cancelOperationError(connect.CodeAlreadyExists, "idempotency_conflict",
			errors.New("idempotency key conflict: different request for same scope and key"))
	}
	if err != nil {
		return nil, cancelOperationError(connect.CodeInternal, "internal_error", fmt.Errorf("replay cancel operation: %w", err))
	}
	return connect.NewResponse(&orchestratorv1.CancelOperationResponse{
		Operation: toProtoOperation(result.Operation),
		RequestId: result.RequestID,
	}), nil
}

func (s *Service) cancelDeliveryStatus(ctx context.Context, op *store.Operation) (store.OperationDeliveryStatus, error) {
	if op.OperationType != store.OperationEmergency {
		return "", nil
	}
	intent, err := s.store.EmergencyIntents().GetByOperationID(ctx, op.ID)
	if err != nil {
		return "", cancelOperationError(connect.CodeInternal, "internal_error", fmt.Errorf("load emergency delivery status: %w", err))
	}
	switch intent.DeliveryStatus {
	case "pending":
		return store.DeliveryUndelivered, nil
	case "queued":
		return store.DeliveryUnknown, nil
	case "delivered", "persisted":
		return store.DeliveryDelivered, nil
	default:
		return "", cancelOperationError(connect.CodeInternal, "internal_error",
			fmt.Errorf("unknown emergency delivery status %q", intent.DeliveryStatus))
	}
}

// authorizeCancelOperation verifies that the actor has a valid membership,
// binding, and role for the operation's target customer.
func (s *Service) authorizeReadOperation(ctx context.Context, op *store.Operation, actor authctx.Actor) error {
	def, err := s.store.Definitions().Get(ctx, op.ReleaseDefinitionID)
	if err != nil {
		return cancelOperationError(connect.CodeInternal, "internal_error", fmt.Errorf("definition lookup: %w", err))
	}
	if err := s.store.Bindings().RequireActive(ctx, actor.OrganizationID, def.CustomerID); err != nil {
		return cancelOperationError(connect.CodePermissionDenied, "binding_revoked", errors.New("organization-customer binding is revoked"))
	}
	if _, err := s.store.OrgMembers().Get(ctx, actor.OrganizationID, actor.UserID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return cancelOperationError(connect.CodePermissionDenied, "membership_inactive", errors.New("actor has no active membership"))
		}
		return cancelOperationError(connect.CodeInternal, "internal_error", fmt.Errorf("membership lookup: %w", err))
	}
	return nil
}

func (s *Service) authorizeCancelOperation(ctx context.Context, op *store.Operation, actor authctx.Actor) error {
	def, err := s.store.Definitions().Get(ctx, op.ReleaseDefinitionID)
	if err != nil {
		return cancelOperationError(connect.CodeInternal, "internal_error", fmt.Errorf("definition lookup: %w", err))
	}

	customer, err := s.store.Customers().Get(ctx, def.CustomerID)
	if err != nil {
		return cancelOperationError(connect.CodeInternal, "internal_error", fmt.Errorf("customer lookup: %w", err))
	}
	if customer.Status != store.CustomerActive {
		return cancelOperationError(connect.CodeFailedPrecondition, "customer_disabled", errors.New("customer is disabled"))
	}

	if err := s.store.Bindings().RequireActive(ctx, actor.OrganizationID, def.CustomerID); err != nil {
		return cancelOperationError(connect.CodePermissionDenied, "binding_revoked", errors.New("organization-customer binding is revoked"))
	}

	member, err := s.store.OrgMembers().Get(ctx, actor.OrganizationID, actor.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return cancelOperationError(connect.CodePermissionDenied, "membership_inactive", errors.New("actor has no active membership"))
		}
		return cancelOperationError(connect.CodeInternal, "internal_error", fmt.Errorf("membership lookup: %w", err))
	}
	if !canCancelOperation(member.Role) {
		return cancelOperationError(connect.CodePermissionDenied, "role_insufficient",
			fmt.Errorf("actor role %s is insufficient for cancel", member.Role))
	}
	return nil
}

func canCancelOperation(role store.Role) bool {
	return role == store.RoleDeployer || role == store.RoleReleaseAdmin || role == store.RolePlatformAdmin
}

func validateCancelInput(msg *orchestratorv1.CancelOperationRequest, idempotencyKey string) error {
	if msg.OperationId == "" {
		return cancelOperationError(connect.CodeInvalidArgument, "invalid_argument", errors.New("operation_id is required"))
	}
	if msg.ExpectedStateVersion < 1 {
		return cancelOperationError(connect.CodeInvalidArgument, "invalid_argument", errors.New("expected_state_version must be >= 1"))
	}
	if idempotencyKey == "" {
		return cancelOperationError(connect.CodeInvalidArgument, "invalid_argument", errors.New("Idempotency-Key header is required"))
	}
	if len(idempotencyKey) > 64 {
		return cancelOperationError(connect.CodeInvalidArgument, "invalid_argument", errors.New("idempotency key too large"))
	}
	if utf8.RuneCountInString(msg.Reason) > 500 {
		return cancelOperationError(connect.CodeInvalidArgument, "invalid_argument", errors.New("reason exceeds 500 characters"))
	}
	if strings.TrimSpace(msg.Reason) == "" {
		return cancelOperationError(connect.CodeInvalidArgument, "invalid_argument", errors.New("reason is required"))
	}
	return nil
}

func cancelOperationError(code connect.Code, reason string, err error) error {
	connectErr := connect.NewError(code, fmt.Errorf("%s: %w", reason, err))
	connectErr.Meta().Set("X-Reason-Code", reason)
	return connectErr
}

// toProtoOperation converts a store.Operation to the safe public proto Operation.
// It intentionally excludes values_patch, idempotency_key, and request_hash.
func toProtoOperation(op *store.Operation) *orchestratorv1.Operation {
	result := &orchestratorv1.Operation{
		OperationId:         op.ID,
		ReleaseDefinitionId: op.ReleaseDefinitionID,
		OperationType:       string(op.OperationType),
		State:               storeStatusToProto(op.Status),
		StateVersion:        int64(op.StateVersion),
		BundleId:            op.BundleID,
		ValuesRevisionId:    op.ValuesRevisionID,
		ExpectedRevision:    int32(op.ExpectedRevision), //nolint:gosec // Helm revisions bounded in practice
		TargetRevision:      int32(op.TargetRevision),   //nolint:gosec // Helm revisions bounded in practice
		Actor: &commonv1.ActorContext{
			UserId:       op.Actor.UserID,
			Organization: op.Actor.Organization,
		},
		CreatedAt: timestamppb.New(op.CreatedAt),
		UpdatedAt: timestamppb.New(op.UpdatedAt),
		LastError: op.LastError,
	}
	if op.TerminalAt != nil {
		result.TerminalAt = timestamppb.New(*op.TerminalAt)
	}
	if op.Deadline != nil {
		result.Deadline = timestamppb.New(*op.Deadline)
	}
	return result
}

func storeStatusToProto(s store.OperationStatus) orchestratorv1.OperationStatus {
	switch s {
	case store.StatusPending:
		return orchestratorv1.OperationStatus_OPERATION_STATUS_PENDING
	case store.StatusPreflight:
		return orchestratorv1.OperationStatus_OPERATION_STATUS_PREFLIGHT
	case store.StatusQueued:
		return orchestratorv1.OperationStatus_OPERATION_STATUS_QUEUED
	case store.StatusRunning:
		return orchestratorv1.OperationStatus_OPERATION_STATUS_RUNNING
	case store.StatusCancelling:
		return orchestratorv1.OperationStatus_OPERATION_STATUS_CANCELLING
	case store.StatusSucceeded:
		return orchestratorv1.OperationStatus_OPERATION_STATUS_SUCCEEDED
	case store.StatusFailed:
		return orchestratorv1.OperationStatus_OPERATION_STATUS_FAILED
	case store.StatusCancelled:
		return orchestratorv1.OperationStatus_OPERATION_STATUS_CANCELLED
	case store.StatusTimeout:
		return orchestratorv1.OperationStatus_OPERATION_STATUS_TIMEOUT
	default:
		return orchestratorv1.OperationStatus_OPERATION_STATUS_UNSPECIFIED
	}
}

func hashIdempotencyKey(key string) string {
	if key == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}

func operationCancelScope(operationID, actorUserID string) string {
	return operationID + ":" + actorUserID
}

func hashCancelRequest(operationID string, expectedStateVersion int, reason string) string {
	payload := fmt.Sprintf("%s|%d|%s", operationID, expectedStateVersion, reason)
	hash := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(hash[:])
}

func (s *Service) toResponse(op *store.Operation, verificationResult *commonv1.VerificationResult) *orchestratorv1.CreateOperationResponse {
	response := &orchestratorv1.CreateOperationResponse{
		OperationId: op.ID,
		State:       string(op.Status),
		PreflightId: op.ID, // preflight_id = operation_id for initial phase
		AcceptedAt:  timestamppb.New(op.CreatedAt),
	}
	if verificationResult != nil {
		response.VerificationResult = *verificationResult
	}
	return response
}

// hashRequest computes a deterministic hash of the request for idempotency.
func hashRequest(req *orchestratorv1.CreateOperationRequest) string {
	payload := fmt.Sprintf("%s|%s|%s|%s|%s|%d|%s|%s",
		req.OperationType,
		req.BundleId,
		req.ReleaseDefinitionId,
		req.ValuesRevisionId,
		req.ValuesPatch,
		req.ExpectedCurrentRevision,
		req.Actor.GetUserId(),
		req.Actor.GetOrganization(),
	)
	h := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", h)
}

// checkCustomerNotDisabled verifies the customer is not disabled.
// Returns PermissionDenied if the customer is disabled.
func (s *Service) checkCustomerNotDisabled(ctx context.Context, customerID string) error {
	cust, err := s.store.Customers().Get(ctx, customerID)
	if err != nil {
		return fmt.Errorf("customer lookup: %w", err)
	}
	if cust.Status == store.CustomerDisabled {
		return fmt.Errorf("customer %s is disabled", customerID)
	}
	return nil
}

// Compile-time check: Service implements the Connect handler interface.
var _ orchestratorv1connect.OrchestratorServiceHandler = (*Service)(nil)

// checkDefinitionOperable returns a stable release_definition_disabled error
// if the definition is not in active state. Uses FailedPrecondition for both
// draft and disabled; callers that want to differentiate can check the status
// before calling this function.
func checkDefinitionOperable(def *store.ReleaseDefinition) error {
	if def.Status == store.DefStatusActive {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("release_definition_disabled: definition %s is %s", def.ID, def.Status))
}

func (s *Service) emitTrustVerificationAudit(
	ctx context.Context,
	msg *orchestratorv1.CreateOperationRequest,
	digest string,
	out *trust.Output,
) {
	if out == nil {
		return
	}
	actor, ok := authctx.ActorFromContext(ctx)
	actorKind := store.AuditActorUser
	actorID := msg.GetActor().GetUserId()
	organizationID := msg.GetActor().GetOrganization()
	role := ""
	if ok {
		actorID = actor.UserID
		organizationID = actor.OrganizationID
		if actor.Service != "" {
			actorKind = store.AuditActorService
			actorID = actor.Service
		}
		if len(actor.Roles) > 0 {
			role = actor.Roles[0]
		}
	}
	policyVersion := trust.DefaultPolicy(s.targetEnv).PolicyVersion
	if out.Record != nil && out.Record.PolicyVersion != "" {
		policyVersion = out.Record.PolicyVersion
	}
	s.emitAudit(audit.NewEvent(
		actorKind,
		actorID,
		organizationID,
		role,
		"release_bundle",
		msg.GetBundleId(),
		"verify_trust",
		string(out.Status),
		out.Summary,
		map[string]string{
			"digest":         digest,
			"policy_version": policyVersion,
			"result":         string(out.Status),
		},
	))
}

// emitAudit emits an audit event through the configured sink, if any.
func (s *Service) emitAudit(ev *store.AuditEvent) {
	if s.auditEmitter == nil {
		return
	}
	result := s.auditEmitter.Emit(ev)
	if !result.Accepted {
		s.logger.Warn("audit event rejected",
			"code", string(result.Code),
			"resource_type", ev.ResourceType,
			"action", ev.Action,
		)
	}
}

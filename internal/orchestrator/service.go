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
	"google.golang.org/protobuf/types/known/structpb"
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
	valueutil "github.com/ndzuki/release-manager/internal/values"
	"github.com/ndzuki/release-manager/internal/vulnerability"
)

// Service implements the OrchestratorServiceHandler Connect interface.
type Service struct {
	store               store.Store
	createOperation     OperationCreationUnitOfWork
	verifier            trust.Verifier
	targetEnv           string
	coordinator         *preflight.Coordinator
	preflightRunner     *preflight.Runner
	vulnEval            *vulnerability.Evaluator
	auditEmitter        audit.Sink
	emergencyDispatcher emergencyDispatcher
	streamRevoker       OperatorStreamRevoker
	operatorEndpoint    string
	logger              *slog.Logger
	authorizer          authorization.Authorizer
	valuesConfig        ValuesConfig
}

func NewService(st store.Store, verifier trust.Verifier, targetEnv string, args ...any) *Service {
	var auditEmitter audit.Sink
	var dispatcher emergencyDispatcher
	var streamRevoker OperatorStreamRevoker
	var createOperation OperationCreationUnitOfWork
	operatorEndpoint := "http://operator:8084"
	logger := slog.Default()
	valuesConfig := DefaultValuesConfig()
	var authorizer authorization.Authorizer
	for _, arg := range args {
		switch value := arg.(type) {
		case audit.Sink:
			auditEmitter = value
		case emergencyDispatcher:
			dispatcher = value
		case OperatorStreamRevoker:
			streamRevoker = value
		case OperationCreationUnitOfWork:
			createOperation = value
		case string:
			if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
				operatorEndpoint = strings.TrimRight(value, "/")
			}
		case authorization.Authorizer:
			authorizer = value
		case ValuesConfig:
			valuesConfig = value.WithDefaults()
		case *slog.Logger:
			logger = value
		}
	}
	coordinator := preflight.NewCoordinator(st.Outbox(), st.Operations(), st.Operators(), st.Definitions(), st.Values(), st.Bundles(), st.PreflightLifecycles(), st.Inventories(), logger)
	return &Service{
		store:               st,
		createOperation:     createOperation,
		verifier:            verifier,
		emergencyDispatcher: dispatcher,
		targetEnv:           targetEnv,
		coordinator:         coordinator,
		preflightRunner:     preflight.NewRunner(coordinator.Run, logger),
		auditEmitter:        auditEmitter,
		streamRevoker:       streamRevoker,
		operatorEndpoint:    operatorEndpoint,
		logger:              logger,
		authorizer:          authorizer,
		valuesConfig:        valuesConfig,
	}
}

// CreateOperation creates a new release operation from the given request.
//
//nolint:gocyclo // operation creation validates multiple independent policy gates
func (s *Service) CreateOperation(
	ctx context.Context,
	req *connect.Request[orchestratorv1.CreateOperationRequest],
) (*connect.Response[orchestratorv1.CreateOperationResponse], error) {
	ctx = authorization.WithFenceCapture(ctx)
	msg := req.Msg
	idempotencyKey := req.Header().Get("Idempotency-Key")

	// REQ-067 rule 1: only INSTALL and UPGRADE are accepted here; ROLLBACK
	// uses RollbackRelease and EMERGENCY uses EmergencyChange.
	opType := store.OperationType(msg.OperationType)
	if opType != store.OperationInstall && opType != store.OperationUpgrade {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("invalid_operation_type: only INSTALL and UPGRADE are accepted"))
	}

	// REQ-067 rule 2: actor comes from the auth interceptor context.
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
	}

	// Definition lookup feeds authorization, gates, and validation below.
	def, err := s.store.Definitions().Get(ctx, msg.ReleaseDefinitionId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("definition_not_found: %s", msg.ReleaseDefinitionId))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("definition lookup: %w", err))
	}

	// REQ-067 rule 2: authorization (binding + role) runs before idempotency
	// and gates (AC-067-22 priority 1, ADR-009).
	if err := s.authorizeOperationActor(ctx, actor, def.CustomerID); err != nil {
		return nil, err
	}

	// REQ-067 rule 3: unresolved emergency effect gate (AC-067-20).
	unresolved, unresolvedOperationIDs, err := s.store.EmergencyIntents().HasUnresolvedForDefinition(ctx, def.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("emergency effect gate: %w", err))
	}
	if unresolved {
		// AC-067-22: the typed detail may carry both ID arrays even though the
		// top-level reason only reflects the highest-priority gate.
		detail := &orchestratorv1.CreateOperationGateDetail{UnresolvedOperationIds: unresolvedOperationIDs}
		pendingTasks, listErr := s.store.ConvergenceTasks().ListByDefinition(ctx, def.ID, "pending_promotion")
		if listErr == nil && len(pendingTasks) > 0 {
			detail.ConvergenceTaskIds = taskIDs(pendingTasks)
		}
		return nil, operationGateError("emergency_effect_unresolved", detail)
	}

	// REQ-067 rule 4: pending promotion convergence gate (AC-067-21).
	pendingTasks, err := s.store.ConvergenceTasks().ListByDefinition(ctx, def.ID, "pending_promotion")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("convergence gate: %w", err))
	}
	if len(pendingTasks) > 0 {
		return nil, operationGateError("release_convergence_pending",
			&orchestratorv1.CreateOperationGateDetail{ConvergenceTaskIds: taskIDs(pendingTasks)})
	}

	// REQ-067 rule 5: idempotent replay or conflict (same scope + key).

	// REQ-067 rule 5: the idempotency key is mandatory and travels via the
	// HTTP Idempotency-Key header (AC-067-06/07). Emptiness is checked with
	// the idempotency step, after authorization and gates (rule order 2-5,
	// ADR-009: unauthorized actors never learn anything about keys).
	if idempotencyKey == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("idempotency_key is required"))
	}

	scope := idempotencyScope(actor.OrganizationID, def.ID)
	scopedKey := operationIdempotencyKey(scope, idempotencyKey)
	existing, err := s.store.Operations().GetByIdempotencyScopeAndKey(ctx, scope, scopedKey)
	if err == nil {
		if existing.RequestHash != hashRequest(msg, canonicalPatchForHash(msg.GetValuesPatch())) {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				errors.New("idempotency_conflict: key already used with different request"))
		}
		s.logger.Info("idempotent operation found", "key", idempotencyKey, "op_id", existing.ID)
		return connect.NewResponse(s.toResponse(existing, nil)), nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("idempotency lookup: %w", err))
	}

	// REQ-067 rule 6: definition must be active (AC-040-03).
	if err := checkDefinitionOperable(def); err != nil {
		return nil, err
	}
	// REQ-067 rule 7: customer must not be disabled (AC-013-02).
	if err := s.checkCustomerNotDisabled(ctx, def.CustomerID); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("customer_disabled: %w", err))
	}
	// REQ-067 rule 8: no other non-terminal operation for the definition
	// (standard/EMERGENCY mutual exclusion, REQ-023 AC-023-06/07).
	if err := s.checkNoActiveOperation(ctx, def.ID); err != nil {
		return nil, err
	}

	// REQ-067 rule 9: bundle state, including archived CAS pre-check
	// (AC-067-17/18; the UOW re-checks atomically inside the transaction).
	bundle, err := s.store.Bundles().Get(ctx, msg.BundleId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("bundle_not_found: %s", msg.BundleId))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("bundle lookup: %w", err))
	}
	switch bundle.Status {
	case store.BundleReceived:
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle_not_ready"))
	case store.BundleRejected:
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle_rejected"))
	case store.BundleValidated:
	case store.BundleArchived:
		// archived_from_status nil/validated continues; received/rejected
		// are rejected per the REQ-067 SetCurrentBundle decision table.
		switch {
		case bundle.ArchivedFromStatus == nil || *bundle.ArchivedFromStatus == store.BundleValidated:
		case *bundle.ArchivedFromStatus == store.BundleRejected:
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle_rejected"))
		default:
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle_not_ready"))
		}
	default:
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle_not_ready"))
	}
	// REQ-067 rule 10: chart match (AC-067-03).
	if !chartNameMatches(bundle.ChartRef, def.ChartName) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("chart_mismatch: bundle chart_ref %q does not match definition chart_name %q", bundle.ChartRef, def.ChartName))
	}

	// Trust verification (REQ-012): merged main flow. The signature reference
	// stays part of the request contract; the preflight Artifact stage owns
	// the synchronous trust checks per REQ-067 non-goals.
	policy := trust.DefaultPolicy(s.targetEnv)
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

	// REQ-067 rule 11: values revision approved and bound to the definition.
	revision, err := s.checkValuesRevision(ctx, def, msg.GetValuesRevisionId())
	if err != nil {
		return nil, err
	}
	// REQ-067 rules 12/13: inventory preconditions (AC-067-02).
	if err := s.checkReleaseState(ctx, def, opType, int(msg.GetExpectedCurrentRevision())); err != nil {
		return nil, err
	}
	// REQ-067 rule 14: canonical merge and secret scan (AC-067-12).
	merged, err := prepareValues(revision, msg.GetValuesPatch())
	if err != nil {
		if errors.Is(err, errSecretLiteralForbidden) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("secret_literal_forbidden"))
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Build the domain Operation with the canonical request hash.
	now := time.Now().UTC()
	policyVersion := trust.DefaultPolicy(s.targetEnv).PolicyVersion
	imageRefsJSON, imageDigestsJSON, err := bundleImageDigests(bundle.Images)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal bundle image digests: %w", err))
	}
	op := &store.Operation{
		ID:                    uuid.New().String(),
		OperationType:         opType,
		Status:                operation.InitialStatus(),
		ReleaseDefinitionID:   def.ID,
		IdempotencyKey:        scopedKey,
		IdempotencyScope:      scope,
		RequestHash:           hashRequest(msg, string(merged.patch)),
		BundleID:              bundle.ID,
		BundleChartRef:        bundle.ChartRef,
		BundleChartDigest:     bundle.ChartDigest,
		ImageRefsJSON:         imageRefsJSON,
		ImageDigestsJSON:      imageDigestsJSON,
		PolicyVersion:         policyVersion,
		ValuesRevisionID:      msg.GetValuesRevisionId(),
		ExpectedRevision:      int(msg.GetExpectedCurrentRevision()),
		ValuesPatch:           merged.patch,
		PatchDigest:           merged.patchDigest,
		EffectiveValuesDigest: merged.effectiveDigest,
		Actor: store.ActorContext{
			UserID:       actor.UserID,
			Organization: actor.OrganizationID,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Preflight dispatch: coordinator publishes in-process; a coordinator
	// failure still persists a deferred outbox entry (AC-067-13).
	dispatch, dispatchErr := s.coordinator.Dispatch(ctx, op, bundleToProto(bundle), merged.effective)
	if dispatchErr != nil {
		payload, marshalErr := (&preflight.CommandPayload{
			Stage: preflight.StageArtifact, OperationID: op.ID, BundleID: op.BundleID, DefinitionID: def.ID,
			Bundle: bundleToProto(bundle), Namespace: def.Namespace, ReleaseName: def.ReleaseName, Values: merged.effective,
			ValuesRevisionID: op.ValuesRevisionID, ValuesPatch: op.ValuesPatch,
			ExpectedCurrentRevision: int64(op.ExpectedRevision), TargetRevision: int64(op.TargetRevision),
		}).Marshal()
		if marshalErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal deferred dispatch: %w", marshalErr))
		}
		dispatch = &store.OutboxEntry{
			ID: uuid.New().String(), CommandID: fmt.Sprintf("%s:artifact", op.ID),
			OperationID: op.ID, OperationType: string(op.OperationType), Payload: payload,
		}
	}
	if s.createOperation == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("operation creation unit of work is not configured"))
	}
	expectedAuthorizationVersion, ok := authorization.SourceVersionFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnavailable,
			errors.New("authorization_snapshot_stale: authorization snapshot is unavailable"))
	}
	artifactDigests := make([]string, 0, len(bundle.Images)+1)
	if bundle.ChartDigest != "" {
		artifactDigests = append(artifactDigests, bundle.ChartDigest)
	}
	for _, image := range bundle.Images {
		if image.Digest != "" {
			artifactDigests = append(artifactDigests, image.Digest)
		}
	}
	// Atomically commit operation, dispatch, bundle CAS, artifact links, and
	// definition current_bundle_id; the fence re-checks the authorization
	// snapshot inside the transaction (AC-067-19/22).
	if _, err := s.createOperation(ctx, CreateOperationRequest{
		Operation:                    op,
		Dispatch:                     dispatch,
		CandidateArtifactDigests:     artifactDigests,
		ExpectedAuthorizationVersion: expectedAuthorizationVersion,
	}); err != nil {
		switch {
		case errors.Is(err, store.ErrReleaseBusy):
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("release_busy"))
		case errors.Is(err, store.ErrDuplicateKey):
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("idempotency_conflict"))
		case errors.Is(err, store.ErrBundleNotReady):
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle_not_ready"))
		case errors.Is(err, store.ErrBundleRejected):
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle_rejected"))
		case errors.Is(err, store.ErrAuthorizationStale):
			return nil, connect.NewError(connect.CodeUnavailable,
				errors.New("authorization_snapshot_stale: authorization snapshot is stale"))
		default:
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create operation: %w", err))
		}
	}
	if dispatchErr != nil {
		s.logger.Warn("preflight dispatch deferred", "op_id", op.ID, "err", dispatchErr)
	}

	// Transition pending → preflight and launch the coordinator.
	next, err := operation.Transition(op.Status, operation.EventStartPreflight)
	if err != nil {
		s.logger.Error("preflight transition failed", "op_id", op.ID, "err", err)
	} else {
		updated, updateErr := s.store.Operations().UpdateStatus(ctx, op.ID, next, op.StateVersion, "")
		if updateErr != nil {
			s.logger.Error("preflight status update failed", "op_id", op.ID, "err", updateErr)
		} else {
			op.Status = updated.Status
			op.StateVersion = updated.StateVersion
			//nolint:contextcheck // preflight must outlive the request context; Runner.Start detaches deliberately (AC-019-03).
			s.startPreflight(op)
			s.logger.Info("preflight coordinator launched", "op_id", op.ID)
		}
	}
	s.logger.Info("operation created",
		"op_id", op.ID,
		"type", op.OperationType,
		"definition", op.ReleaseDefinitionID,
	)

	return connect.NewResponse(s.toResponse(op, &verifyResult)), nil
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
	requestID := requestIDOrNew(ctx)
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
	case store.EmergencyEffectNotStarted:
		return orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_NOT_STARTED
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
	requestID := requestIDOrNew(ctx)
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
	if result.Operation.Status == store.StatusCancelled || result.Operation.Status == store.StatusCancelling {
		// AC-019-03: a successfully cancelled operation terminates its running
		// preflight; the coordinator finalizes the lifecycle as cancelled.
		s.CancelPreflight(op.ID)
	}
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
		LastError: op.LastError,
		// effect_status is the authoritative cluster effect projection for
		// EMERGENCY operations; non-EMERGENCY operations always project
		// NOT_STARTED (AC-077-04).
		EffectStatus: emergencyEffectToProto(op.EffectStatus),
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

// idempotencyScope scopes idempotency by organization and definition
// (REQ-067: scope = organization_id + ":" + release_definition_id).
func idempotencyScope(orgID, definitionID string) string {
	return orgID + ":" + definitionID
}

func scopedOperationKey(scope, key string) string {
	if key == "" {
		return ""
	}
	return scope + ":" + hashIdempotencyKey(key)
}

// operationIdempotencyKey returns the value persisted in the globally UNIQUE
// operations.idempotency_key column. When the client sends no key, idempotency
// is disabled and the record is never written, so the column needs a unique
// placeholder instead of the empty string shared by every keyless request
// (AC-010-05).
func operationIdempotencyKey(scope, key string) string {
	if key == "" {
		return scope + ":" + uuid.NewString()
	}
	return scopedOperationKey(scope, key)
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

// hashRequest computes a deterministic hash of the canonical request payload
// (REQ-067 idempotency model). The canonical patch is produced by
// canonicalPatchForHash so replays with equivalent patches hash identically.
func hashRequest(req *orchestratorv1.CreateOperationRequest, canonicalPatch string) string {
	return hashOperationRequest(store.OperationType(req.GetOperationType()), req.GetBundleId(), req.GetReleaseDefinitionId(),
		req.GetValuesRevisionId(), canonicalPatch, int(req.GetExpectedCurrentRevision()), 0, "")
}

// hashOperationRequest canonicalizes the REQ-067 hash input into one JSON
// document: operation_type, bundle_id, release_definition_id,
// values_revision_id, values_patch, expected_current_revision,
// target_revision, reason (0/"" for non-ROLLBACK).
func hashOperationRequest(opType store.OperationType, bundleID, definitionID, valuesRevisionID, canonicalPatch string, expectedRevision, targetRevision int, reason string) string {
	payload := fmt.Sprintf(`{"operation_type":%q,"bundle_id":%q,"release_definition_id":%q,"values_revision_id":%q,"values_patch":%s,"expected_current_revision":%d,"target_revision":%d,"reason":%q}`,
		string(opType), bundleID, definitionID, valuesRevisionID, canonicalPatch, expectedRevision, targetRevision, reason)
	h := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", h)
}

// canonicalPatchForHash returns the canonical JSON of a Struct merge patch,
// matching prepareValues output so stored request hashes stay stable.
func canonicalPatchForHash(patch *structpb.Struct) string {
	if patch == nil {
		return "{}"
	}
	raw, err := json.Marshal(patch.AsMap())
	if err != nil {
		return "{}"
	}
	canonical, err := valueutil.Canonicalize(raw)
	if err != nil {
		return "{}"
	}
	return string(canonical)
}

// authorizeOperationActor verifies the actor's binding and role via the
// authorization snapshot (REQ-067 rule 2, ADR-006). The successful decision
// records the fence source version into the request context; a missing or
// stale snapshot fails closed before idempotency and gates (AC-067-22).
func (s *Service) authorizeOperationActor(ctx context.Context, actor authctx.Actor, customerID string) error {
	if s.authorizer == nil {
		return connect.NewError(connect.CodeUnavailable,
			errors.New("authorization_snapshot_stale: authorization snapshot is unavailable"))
	}
	if err := s.authorizer.AuthorizeWrite(ctx, actor, customerID, store.AuthorizationCreateOperation); err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return connect.NewError(connect.CodePermissionDenied, errors.New("permission_denied"))
		}
		return err
	}
	return nil
}

// checkNoActiveOperation enforces REQ-067 rule 8: no other non-terminal
// standard or EMERGENCY operation for the definition (AC-067-04, REQ-023
// AC-023-06/07). The UOW re-checks availability atomically inside the
// transaction.
func (s *Service) checkNoActiveOperation(ctx context.Context, defID string) error {
	active, err := s.store.Operations().HasActiveForDefinition(ctx, defID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("active check: %w", err))
	}
	if active {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("release_busy: definition %s has active operation", defID))
	}
	activeEmergency, err := s.store.Operations().HasActiveEmergencyForDefinition(ctx, defID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("emergency check: %w", err))
	}
	if activeEmergency {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("release_busy: definition %s has a running EMERGENCY", defID))
	}
	return nil
}

// operationGateError builds a CodeFailedPrecondition error with an optional
// typed CreateOperationGateDetail (AC-067-20/21/22).
func operationGateError(reason string, detail *orchestratorv1.CreateOperationGateDetail) error {
	connectErr := connect.NewError(connect.CodeFailedPrecondition, errors.New(reason))

	if detail != nil {
		if errorDetail, err := connect.NewErrorDetail(detail); err == nil {
			connectErr.AddDetail(errorDetail)
		}
	}
	return connectErr
}

// taskIDs extracts convergence task IDs for the typed gate detail
// (AC-067-21/22).
func taskIDs(tasks []*store.ConvergenceTask) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

func (s *Service) checkValuesRevision(ctx context.Context, def *store.ReleaseDefinition, revisionID string) (*store.ValuesRevision, error) {
	if revisionID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("revision_not_approved"))
	}
	revision, err := s.store.Values().Get(ctx, revisionID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("revision_mismatch: values_revision not found: %s", revisionID))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("values revision lookup: %w", err))
	}
	if revision.ReleaseDefinitionID != def.ID {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("values_revision %s belongs to release_definition %s", revision.ID, revision.ReleaseDefinitionID))
	}
	if revision.Status != store.ValuesStatusApproved {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("revision_not_approved: values_revision %s must be approved", revision.ID))
	}
	return revision, nil
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

// startPreflight begins the preflight pipeline for an operation, detached from
// the request context (AC-019-03). Runs are tracked by the runner for
// operation-scoped cancellation and graceful shutdown.
func (s *Service) startPreflight(op *store.Operation) {
	s.preflightRunner.Start(op)
}

// CancelPreflight propagates cancellation to a running preflight after the
// operation has been CASed to cancelled (AC-019-03/07).
func (s *Service) CancelPreflight(operationID string) {
	s.preflightRunner.Cancel(operationID)
}

// ResumePreflights restarts preflight coordination for operations left in the
// preflight state after a service restart (ADR-009 recovery). Returns the
// number of operations resumed.
func (s *Service) ResumePreflights(ctx context.Context) (int, error) {
	ops, err := s.store.Operations().ListNonTerminal(ctx)
	if err != nil {
		return 0, err
	}
	//nolint:contextcheck // resumed runs use the runner's detached background context.
	started := s.preflightRunner.Resume(ops)
	if started > 0 {
		s.logger.Info("resumed preflight operations after restart", "count", started)
	}
	return started, nil
}

// Shutdown cancels and joins all preflight coordinators before the Store closes.
func (s *Service) Shutdown(ctx context.Context) error {
	return s.preflightRunner.Shutdown(ctx)
}

func (s *Service) checkReleaseState(
	ctx context.Context,
	def *store.ReleaseDefinition,
	opType store.OperationType,
	expected int,
) error {
	installed, err := s.store.Inventories().GetByDefinition(ctx, def.ID)
	if opType == store.OperationInstall {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("inventory lookup: %w", err))
		}
		if installed.InventoryStatus == store.InventoryActive {
			return connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("release_already_exists: installed release exists for definition %s", def.ID))
		}
		return nil
	}

	if expected < 1 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("expected_current_revision must be >= 1"))
	}
	if errors.Is(err, store.ErrNotFound) || (err == nil && installed.InventoryStatus != store.InventoryActive) {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("release_not_found: no installed release for definition %s", def.ID))
	}
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("inventory lookup: %w", err))
	}
	if installed.Revision != expected {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("revision_conflict: expected revision %d, but current revision is %d", expected, installed.Revision))
	}
	return nil
}

// chartNameMatches performs a loose match between a chart reference and a chart name.
// Registry host prefixes (e.g., "registry.example.com/") are stripped from both
// before comparison (AC-067-03).
func chartNameMatches(chartRef, chartName string) bool {
	ref := extractChartName(chartRef)
	name := extractChartName(chartName)
	return ref == name
}

// extractChartName strips the registry host prefix from a chart reference.
// "registry.example.com/nginx" → "nginx"
// "nginx" → "nginx"
func extractChartName(ref string) string {
	if idx := strings.LastIndex(ref, "/"); idx >= 0 {
		return ref[idx+1:]
	}
	return ref
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
	actorID := ""
	organizationID := ""
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

// ListOperations returns operations for a release definition.
func (s *Service) ListOperations(_ context.Context, _ *connect.Request[orchestratorv1.ListOperationsRequest]) (*connect.Response[orchestratorv1.ListOperationsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("ListOperations is not implemented"))
}

// bundleToProto converts a store bundle into the common proto shape used by
// preflight dispatch payloads.
func bundleToProto(bundle *store.ReleaseBundle) *commonv1.ReleaseBundle {
	images := make([]*commonv1.BundleImage, 0, len(bundle.Images))
	for _, image := range bundle.Images {
		images = append(images, &commonv1.BundleImage{
			Ref: image.Ref, Digest: image.Digest, ValuesPath: image.ValuesPath,
		})
	}
	return &commonv1.ReleaseBundle{
		Id: bundle.ID, Name: bundle.Name,
		Digest:   &commonv1.ReleaseDigest{Algorithm: bundle.DigestAlg, Value: bundle.DigestValue},
		Status:   commonv1.BundleStatus(commonv1.BundleStatus_value["BUNDLE_STATUS_"+strings.ToUpper(string(bundle.Status))]),
		ChartRef: bundle.ChartRef, ChartVersion: bundle.ChartVersion, ChartDigest: bundle.ChartDigest,
		Images: images, GitCommit: bundle.GitCommit, PipelineId: bundle.PipelineID,
		SignatureRef: bundle.SignatureRef, SbomRef: bundle.SBOMRef, ProvenanceRef: bundle.ProvenanceRef,
		CreatedAt: timestamppb.New(bundle.CreatedAt),
	}
}

// bundleImageDigests converts bundle images to JSON byte arrays for storage.
func bundleImageDigests(images []store.BundleImage) (refsJSON, digestsJSON []byte, err error) {
	refs := make([]string, 0, len(images))
	digests := make([]string, 0, len(images))
	for _, image := range images {
		if image.Ref != "" {
			refs = append(refs, image.Ref)
		}
		if image.Digest != "" {
			digests = append(digests, image.Digest)
		}
	}
	refsJSON, err = json.Marshal(refs)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal image refs: %w", err)
	}
	digestsJSON, err = json.Marshal(digests)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal image digests: %w", err)
	}
	return refsJSON, digestsJSON, nil
}

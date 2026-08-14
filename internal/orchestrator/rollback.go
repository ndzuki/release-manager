package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	authctx "github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/orchestrator/operation"
	"github.com/ndzuki/release-manager/internal/store"
)

// RollbackRelease creates a ROLLBACK operation targeting a specific Helm revision.
// Implements REQ-022: the handler validates the request, creates an Operation, and
// transitions it to preflight. The operator agent executes the actual Helm rollback.
//
//nolint:gocyclo // validation cascade is intentional — each check gates the next.
func (s *Service) RollbackRelease(
	ctx context.Context,
	req *connect.Request[orchestratorv1.RollbackReleaseRequest],
) (*connect.Response[orchestratorv1.RollbackReleaseResponse], error) {
	ctx = authorization.WithFenceCapture(ctx)
	msg := req.Msg
	idempotencyKey := req.Header().Get("Idempotency-Key")

	// REQ-067 rule 2: actor comes from the auth interceptor context.
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
	}

	// AC-067-16: ROLLBACK must not carry values.
	//nolint:staticcheck // AC-067-16: the deprecated fields remain the server-side rejection detection point.
	if msg.ValuesRevisionId != "" || msg.ValuesPatch != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("rollback_values_not_allowed"))
	}

	// Field validation (REQ-022).
	if msg.Reason == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("reason is required for rollback"))
	}
	if msg.TargetRevision <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("target_revision must be >= 1, got %d", msg.TargetRevision))
	}
	if msg.ExpectedCurrentRevision <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("expected_current_revision must be >= 1, got %d", msg.ExpectedCurrentRevision))
	}
	if msg.TargetRevision >= msg.ExpectedCurrentRevision {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("target_revision %d must be < expected_current_revision %d",
				msg.TargetRevision, msg.ExpectedCurrentRevision))
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

	// REQ-067 rule 2: authorization before idempotency and gates (AC-067-22).
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

	// REQ-067 rule 5: idempotency key is mandatory and travels via the header;
	// emptiness is checked with the idempotency step, after authorization and
	// gates (rule order 2-5, ADR-009).
	if idempotencyKey == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("idempotency_key is required"))
	}


	// REQ-067 rule 5: idempotent replay or conflict (same scope + key).
	scope := idempotencyScope(actor.OrganizationID, def.ID)
	scopedKey := operationIdempotencyKey(scope, idempotencyKey)
	requestHash := hashRollbackRequest(msg)
	existing, err := s.store.Operations().GetByIdempotencyScopeAndKey(ctx, scope, scopedKey)
	if err == nil {
		if existing.RequestHash != requestHash {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				errors.New("idempotency_conflict: key already used with different request"))
		}
		s.logger.Info("idempotent rollback found", "key", idempotencyKey, "op_id", existing.ID)
		return rollbackResponse(existing), nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("idempotency lookup: %w", err))
	}

	// REQ-067 rule 6: definition must be active.
	if err := checkDefinitionOperable(def); err != nil {
		return nil, err
	}
	// REQ-067 rule 7: customer must not be disabled.
	if err := s.checkCustomerNotDisabled(ctx, def.CustomerID); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("customer_disabled: %w", err))
	}
	// REQ-067 rule 8: no other non-terminal operation (AC-067-04).
	if err := s.checkNoActiveOperation(ctx, def.ID); err != nil {
		return nil, err
	}
	// REQ-067 rule 13: ROLLBACK requires an active inventory entry.
	if err := s.checkReleaseState(ctx, def, store.OperationRollback, int(msg.GetExpectedCurrentRevision())); err != nil {
		return nil, err
	}

	// Build the rollback operation.
	now := time.Now().UTC()
	op := &store.Operation{
		ID:                  uuid.New().String(),
		OperationType:       store.OperationRollback,
		Status:              operation.InitialStatus(),
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      scopedKey,
		IdempotencyScope:    scope,
		RequestHash:         requestHash,
		ExpectedRevision:    int(msg.ExpectedCurrentRevision),
		TargetRevision:      int(msg.TargetRevision),
		Reason:              msg.Reason,
		Actor: store.ActorContext{
			UserID:       actor.UserID,
			Organization: actor.OrganizationID,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	expectedAuthorizationVersion, ok := authorization.SourceVersionFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnavailable,
			errors.New("authorization_snapshot_stale: authorization snapshot is unavailable"))
	}
	createResult, err := s.store.Operations().CreateIdempotent(ctx, store.OperationCreateCommand{
		Operation: op,
		Idempotency: &store.IdempotencyRecord{
			Scope: scope, Key: hashIdempotencyKey(idempotencyKey), RequestHash: requestHash,
			ExpiresAt: now.Add(24 * time.Hour),
		},
		CheckAvailable:             true,
		ExpectedAuthorizationVersion: expectedAuthorizationVersion,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrReleaseBusy):
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("release_busy: definition %s has active operation", msg.ReleaseDefinitionId))
		case errors.Is(err, store.ErrIdempotencyConflict):
			return nil, connect.NewError(connect.CodeAlreadyExists,
				errors.New("idempotency_conflict: key already used with different request"))
		case errors.Is(err, store.ErrAuthorizationStale):
			return nil, connect.NewError(connect.CodeUnavailable,
				errors.New("authorization_snapshot_stale: authorization snapshot is stale"))
		default:
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create rollback operation: %w", err))
		}
	}
	op = createResult.Operation
	if createResult.Replayed {
		return rollbackResponse(op), nil
	}

	// Transition to preflight (AC-022-02: historical artifact check happens during preflight in operator).
	next, err := operation.Transition(op.Status, operation.EventStartPreflight)
	if err != nil {
		s.logger.Error("rollback preflight transition failed", "op_id", op.ID, "err", err)
	} else {
		updated, updateErr := s.store.Operations().UpdateStatus(ctx, op.ID, next, op.StateVersion, "")
		if updateErr != nil {
			s.logger.Error("rollback preflight status update failed", "op_id", op.ID, "err", updateErr)
		} else {
			op.Status = updated.Status
			op.StateVersion = updated.StateVersion
		}
	}

	s.logger.Info("rollback operation created",
		"op_id", op.ID, "definition", op.ReleaseDefinitionID,
		"from_rev", op.ExpectedRevision, "to_rev", op.TargetRevision,
	)
	return rollbackResponse(op), nil
}

func hashRollbackRequest(msg *orchestratorv1.RollbackReleaseRequest) string {
	return hashOperationRequest(store.OperationRollback, "", msg.GetReleaseDefinitionId(), "", "{}",
		int(msg.GetExpectedCurrentRevision()), int(msg.GetTargetRevision()), msg.GetReason())
}

func rollbackResponse(op *store.Operation) *connect.Response[orchestratorv1.RollbackReleaseResponse] {
	return connect.NewResponse(&orchestratorv1.RollbackReleaseResponse{
		OperationId:  op.ID,
		FromRevision: int32(op.ExpectedRevision),     //nolint:gosec // Helm revisions are bounded well below int32 max.
		ToRevision:   int32(op.ExpectedRevision + 1), //nolint:gosec // Helm revisions are bounded well below int32 max.
		State:        string(op.Status),
	})
}

package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
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
	msg := req.Msg

	// 1. Validate actor before any idempotency lookup.
	if msg.Actor.GetOrganization() == "" || msg.Actor.GetUserId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("actor organization and user_id are required"))
	}
	if msg.IdempotencyKey == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("idempotency_key is required"))
	}

	// 2. Validate required fields
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

	operationScope := operationIdempotencyScope(
		msg.Actor.GetUserId(), msg.Actor.GetOrganization(), msg.ReleaseDefinitionId,
	)
	requestHash := hashRollbackRequest(msg)
	keyHash := hashIdempotencyKey(msg.IdempotencyKey)

	// 2. Lookup release definition and authorization before idempotency replay.
	def, err := s.store.Definitions().Get(ctx, msg.ReleaseDefinitionId)
	if err == store.ErrNotFound {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("release_definition not found: %s", msg.ReleaseDefinitionId))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("definition lookup: %w", err))
	}
	if def.Status != store.DefStatusActive {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("release_definition %s is %s", def.ID, def.Status))
	}

	// 4. Authorize the actor against the customer binding and organization role.
	if err := s.checkCustomerNotDisabled(ctx, def.CustomerID); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("customer_disabled: %w", err))
	}
	if organizationID := msg.Actor.GetOrganization(); organizationID != "" {
		if err := s.store.Bindings().RequireActive(ctx, organizationID, def.CustomerID); err != nil {
			if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrBindingRevoked) {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("customer binding is not active"))
			}
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("binding check: %w", err))
		}
	}

	// 3. Build and persist through the same atomic idempotency gate as create.
	now := time.Now().UTC()
	op := &store.Operation{
	ID: uuid.New().String(), OperationType: store.OperationRollback, Status: operation.InitialStatus(),
	ReleaseDefinitionID: msg.ReleaseDefinitionId, IdempotencyKey: msg.IdempotencyKey,
	IdempotencyScope: operationScope, RequestHash: requestHash,
	ExpectedRevision: int(msg.ExpectedCurrentRevision), TargetRevision: int(msg.TargetRevision), Reason: msg.Reason,
	Actor:     store.ActorContext{UserID: msg.Actor.GetUserId(), Organization: msg.Actor.GetOrganization()},
	CreatedAt: now, UpdatedAt: now,
	}
	createResult, err := s.store.Operations().CreateIdempotent(ctx, store.OperationCreateCommand{
		Operation: op,
		Idempotency: &store.IdempotencyRecord{
			Scope: operationScope, Key: keyHash, RequestHash: requestHash,
			ExpiresAt: now.Add(24 * time.Hour),
		},
		CheckAvailable: true,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrReleaseBusy):
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("release_busy: definition %s has active operation", msg.ReleaseDefinitionId))
		case errors.Is(err, store.ErrIdempotencyConflict):
			return nil, connect.NewError(connect.CodeAlreadyExists,
				errors.New("idempotency_conflict: key already used with different request"))
		default:
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create rollback operation: %w", err))
		}
	}
	op = createResult.Operation
	if createResult.Replayed {
		return rollbackResponse(op), nil
	}

	// 4. Transition to preflight (AC-022-02: historical artifact check happens during preflight in operator).
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

func hashRollbackRequest(req *orchestratorv1.RollbackReleaseRequest) string {
	payload := fmt.Sprintf("%s|%d|%d|%s|%s|%s",
		req.ReleaseDefinitionId,
		req.TargetRevision,
		req.ExpectedCurrentRevision,
		req.Reason,
		req.Actor.GetUserId(),
		req.Actor.GetOrganization(),
	)
	hash := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(hash[:])
}

func rollbackResponse(op *store.Operation) *connect.Response[orchestratorv1.RollbackReleaseResponse] {
	return connect.NewResponse(&orchestratorv1.RollbackReleaseResponse{
		OperationId:  op.ID,
		FromRevision: int32(op.ExpectedRevision),     //nolint:gosec // Helm revisions are bounded well below int32 max.
		ToRevision:   int32(op.ExpectedRevision + 1), //nolint:gosec // Helm revisions are bounded well below int32 max.
		State:        string(op.Status),
	})
}

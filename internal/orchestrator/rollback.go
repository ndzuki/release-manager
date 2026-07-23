package orchestrator

import (
	"context"
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

	if msg.GetValuesRevisionId() != "" || msg.GetValuesPatch() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("rollback_values_not_allowed"))
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
	// AC-022-01: target_revision must be less than current — you can't rollback
	// to the same or a future revision.
	if msg.TargetRevision >= msg.ExpectedCurrentRevision {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("target_revision %d must be < expected_current_revision %d",
				msg.TargetRevision, msg.ExpectedCurrentRevision))
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
	if def.Status != store.DefStatusActive {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("release_definition %s is %s", def.ID, def.Status))
	}

	// 4. Authorize the actor against the customer binding and organization role.
	if err := s.checkCustomerNotDisabled(ctx, def.CustomerID); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("customer_disabled: %w", err))
	}
	if err := s.authorizeOperationActor(ctx, msg.Actor.GetOrganization(), msg.Actor.GetUserId(), def.CustomerID); err != nil {
		return nil, err
	}

	requestHash := s.rollbackRequestHash(msg)
	scope := idempotencyScope(msg.Actor.GetOrganization(), msg.ReleaseDefinitionId)
	existing, err := s.store.Operations().GetByIdempotencyScopeAndKey(ctx, scope, msg.IdempotencyKey)
	if err == nil {
		if existing.RequestHash != requestHash {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("idempotency_conflict"))
		}
		return connect.NewResponse(s.rollbackResponse(existing)), nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("idempotency lookup: %w", err))
	}

	if err := s.checkReleaseState(ctx, def, store.OperationRollback, int(msg.ExpectedCurrentRevision)); err != nil {
		return nil, err
	}

	// 5. Build and persist the rollback operation.
	now := time.Now().UTC()
	op := &store.Operation{
		ID: uuid.New().String(), OperationType: store.OperationRollback, Status: operation.InitialStatus(),
		ReleaseDefinitionID: msg.ReleaseDefinitionId, IdempotencyKey: msg.IdempotencyKey,
		IdempotencyScope: scope, RequestHash: requestHash,
		ExpectedRevision: int(msg.ExpectedCurrentRevision), TargetRevision: int(msg.TargetRevision), Reason: msg.Reason,
		Actor:     store.ActorContext{UserID: msg.Actor.GetUserId(), Organization: msg.Actor.GetOrganization()},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Operations().CreateIfAvailable(ctx, op); err != nil {
		if errors.Is(err, store.ErrReleaseBusy) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("release_busy"))
		}
		if errors.Is(err, store.ErrDuplicateKey) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("idempotency_conflict"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create rollback operation: %w", err))
	}

	if err := s.coordinator.Enqueue(ctx, op); err != nil {
		s.logger.Warn("rollback preflight dispatch deferred", "op_id", op.ID, "err", err)
	}

	return connect.NewResponse(s.rollbackResponse(op)), nil
}

func (s *Service) rollbackResponse(op *store.Operation) *orchestratorv1.RollbackReleaseResponse {
	return &orchestratorv1.RollbackReleaseResponse{
		OperationId: op.ID,
		//nolint:gosec // Helm revisions are bounded well below int32 max
		FromRevision: int32(op.ExpectedRevision),
		//nolint:gosec // Helm revisions are bounded well below int32 max
		ToRevision: int32(op.ExpectedRevision + 1),
		State:      string(op.Status),
	}
}

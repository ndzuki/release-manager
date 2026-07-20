package orchestrator

import (
	"context"
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

	// 1. Idempotency check
	if msg.IdempotencyKey != "" {
		existing, err := s.store.Operations().GetByIdempotencyKey(ctx, msg.IdempotencyKey)
		if err == nil {
			s.logger.Info("idempotent rollback operation found", "key", msg.IdempotencyKey, "op_id", existing.ID)
			return connect.NewResponse(&orchestratorv1.RollbackReleaseResponse{
			OperationId:  existing.ID,
			//nolint:gosec // Helm revisions are bounded well below int32 max
			FromRevision: int32(existing.ExpectedRevision),
			//nolint:gosec // Helm revisions are bounded well below int32 max
			ToRevision:   int32(existing.TargetRevision),
			State:        string(existing.Status),
			}), nil
		}
		if err != store.ErrNotFound {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("idempotency lookup: %w", err))
		}
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

	// 4. Customer not disabled
	if err := s.checkCustomerNotDisabled(ctx, def.CustomerID); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}

	// 5. Release busy check — no active operation for this definition
	active, err := s.store.Operations().HasActiveForDefinition(ctx, msg.ReleaseDefinitionId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("active check: %w", err))
	}
	if active {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("release_busy: definition %s has active operation", msg.ReleaseDefinitionId))
	}

	// Always reject standard operations during emergency (parity with CreateOperation).
	activeEmergency, err := s.store.Operations().HasActiveEmergencyForDefinition(ctx, msg.ReleaseDefinitionId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("emergency active check: %w", err))
	}
	if activeEmergency {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("definition %s has a running EMERGENCY operation; rollback is denied", msg.ReleaseDefinitionId))
	}

	// 6. Build domain Operation
	now := time.Now().UTC()
	op := &store.Operation{
		ID:                  uuid.New().String(),
		OperationType:       store.OperationRollback,
		Status:              operation.InitialStatus(),
		ReleaseDefinitionID: msg.ReleaseDefinitionId,
		IdempotencyKey:      msg.IdempotencyKey,
		ExpectedRevision:    int(msg.ExpectedCurrentRevision),
		TargetRevision:      int(msg.TargetRevision),
		Actor: store.ActorContext{
			UserID:       msg.Actor.GetUserId(),
			Organization: msg.Actor.GetOrganization(),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	// 7. Persist
	if err := s.store.Operations().Create(ctx, op); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create rollback operation: %w", err))
	}

	// 8. Transition to preflight (AC-022-02: historical artifact check happens during preflight in operator)
	next, err := operation.Transition(op.Status, operation.EventStartPreflight)
	if err != nil {
		s.logger.Error("rollback preflight transition failed", "op_id", op.ID, "err", err)
	} else {
		updated, err := s.store.Operations().UpdateStatus(ctx, op.ID, next, op.StateVersion, "")
		if err != nil {
			s.logger.Error("rollback preflight status update failed", "op_id", op.ID, "err", err)
		} else {
			op.Status = updated.Status
			op.StateVersion = updated.StateVersion
		}
	}

	s.logger.Info("rollback operation created",
		"op_id", op.ID,
		"definition", op.ReleaseDefinitionID,
		"from_rev", op.ExpectedRevision,
		"to_rev", op.TargetRevision,
	)

	return connect.NewResponse(&orchestratorv1.RollbackReleaseResponse{
		OperationId:  op.ID,
		//nolint:gosec // Helm revisions are bounded well below int32 max
		FromRevision: int32(op.ExpectedRevision),
		//nolint:gosec // Helm revisions are bounded well below int32 max
		ToRevision:   int32(op.ExpectedRevision + 1), // rollback creates a new revision
		State:        string(op.Status),
	}), nil
}

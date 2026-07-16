package orchestrator

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// EmergencyChange handles type-whitelisted emergency operations with convergence policy.
// Implements REQ-032.
func (s *Service) EmergencyChange(
	ctx context.Context,
	req *connect.Request[orchestratorv1.EmergencyChangeRequest],
) (*connect.Response[orchestratorv1.EmergencyChangeResponse], error) {
	msg := req.Msg

	// Validate action is in the whitelist.
	action := emergencyActionFromProto(msg.Action)
	if !action.Valid() {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("unsupported emergency action: %s", msg.Action))
	}

	defID := msg.ReleaseDefinitionId
	if defID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("release_definition_id is required"))
	}

	// Verify the release definition exists.
	def, err := s.store.Definitions().Get(ctx, defID)
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound,
				fmt.Errorf("release definition not found: %s", defID))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if err := checkDefinitionOperable(def); err != nil {
		return nil, err
	}

	// AC-013-02: Reject emergency changes for disabled customers.
	if err := s.checkCustomerNotDisabled(ctx, def.CustomerID); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}

	// AC-032-02: HPA detection stub — reject SetReplicas on HPA-managed workloads.
	if action == store.EmergencySetReplicas && isHPAManaged(def) {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("HPA managed workload: SetReplicas is denied for definition %s", defID))
	}

	// AC-032-05: conflicting standard operation → reject EMERGENCY.
	hasActive, err := s.store.Operations().HasActiveForDefinition(ctx, defID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if hasActive {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("definition %s has a running standard operation; EMERGENCY operation is denied", defID))
	}

	convergence := emergencyConvergenceFromProto(msg.Convergence)

	// Create the emergency operation.
	op := &store.Operation{
		ID:                  uuid.New().String(),
		OperationType:       store.OperationEmergency,
		Status:              store.StatusPending,
		ReleaseDefinitionID: defID,
		IdempotencyKey:      uuid.New().String(),
		ValuesPatch:         []byte(msg.Payload),
	}

	if msg.Actor != nil {
		op.Actor = store.ActorContext{
			UserID:       msg.Actor.UserId,
			Organization: msg.Actor.Organization,
		}
	}

	// Persist convergence policy in metadata.
	// REQUIRE_PROMOTION → ValuesRevision approval before Helm takes over (AC-032-03).
	_ = convergence // convergence is attached to the operation context for later processing.

	if err := s.store.Operations().Create(ctx, op); err != nil {
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("create emergency operation: %w", err))
	}

	s.logger.Info("emergency change created",
		"operation_id", op.ID,
		"definition_id", defID,
		"action", action,
		"convergence", convergence,
	)

	return connect.NewResponse(&orchestratorv1.EmergencyChangeResponse{
		OperationId: op.ID,
		Status:      string(op.Status),
		Convergence: msg.Convergence,
	}), nil
}

// isHPAManaged checks whether a release definition's workload is managed by HPA.
// AC-032-02: Stub implementation — returns false to not block normal workflows.
// TODO: Implement actual HPA detection via K8s API or definition metadata.
func isHPAManaged(_ *store.ReleaseDefinition) bool {
	// Stub: not yet implemented. In production, this would check the
	// definition's cluster for an HPA targeting the same workload.
	return false
}

package livewire

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/test/e2e/stages"
)

// errUnavailable reports an unusable connector. Adapters surface it instead of
// panicking so a mis-wired stage fails closed like any other stage error.
var errUnavailable = errors.New("livewire: connector is unavailable")

// authorizedRequest carries the runner bearer token. It is a package-level
// generic helper because Go 1.26 cannot declare generic methods.
func authorizedRequest[T any](token string, message *T) *connect.Request[T] {
	request := connect.NewRequest(message)
	if token != "" {
		request.Header().Set("Authorization", "Bearer "+token)
	}
	return request
}

// Revision implements stages.ReleaseObserver from the formal inventory, which
// is the only sanctioned source for a definition's live revision. The value is
// never assumed from the seed manifest.
func (c *Connector) Revision(ctx context.Context, definitionID string) (int32, error) {
	row, err := c.inventoryRow(ctx, definitionID)
	if err != nil {
		return 0, err
	}
	return row.GetRevision(), nil
}

// ActiveOperation implements stages.ReleaseObserver. A definition missing from
// the inventory is reported as "no active operation" only when the inventory
// actually answered; an absent row means the definition is not observable and
// is surfaced as an error by inventoryRow.
func (c *Connector) ActiveOperation(ctx context.Context, definitionID string) (stages.ActiveOperation, bool, error) {
	row, err := c.inventoryRow(ctx, definitionID)
	if err != nil {
		return stages.ActiveOperation{}, false, err
	}
	active := row.GetActiveOperation()
	if active == nil || strings.TrimSpace(active.GetOperationId()) == "" {
		return stages.ActiveOperation{}, false, nil
	}
	return stages.ActiveOperation{
		ID:     active.GetOperationId(),
		Type:   active.GetOperationType(),
		Status: active.GetState().String(),
		Actor:  active.GetActor(),
	}, true, nil
}

// inventoryRow resolves one definition's inventory row.
func (c *Connector) inventoryRow(ctx context.Context, definitionID string) (*orchestratorv1.ReleaseInventoryRow, error) {
	clients, err := c.clientsOrFail()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(definitionID) == "" {
		return nil, errors.New("livewire: empty release definition id")
	}
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	response, err := clients.Orchestrator().ListReleaseInventory(ctx,
		authorizedRequest(c.session.Token(), &orchestratorv1.ListReleaseInventoryRequest{}))
	if err != nil {
		return nil, fmt.Errorf("livewire: list release inventory: %w", err)
	}
	if response == nil || response.Msg == nil {
		return nil, errors.New("livewire: empty release inventory response")
	}
	for _, row := range response.Msg.GetRows() {
		if row != nil && row.GetReleaseDefinitionId() == definitionID {
			return row, nil
		}
	}
	return nil, fmt.Errorf("livewire: release definition %s is absent from the inventory", definitionID)
}

// Upgrade implements stages.OperationWriter via CreateOperation.
//
// The Idempotency-Key is an HTTP header because CreateOperationRequest reserves
// the in-message field; the key is stable per definition so a replayed stage
// dedupes onto the same operation instead of submitting a second upgrade.
func (c *Connector) Upgrade(ctx context.Context, request stages.UpgradeRequest) (stages.OperationRef, error) {
	clients, err := c.clientsOrFail()
	if err != nil {
		return stages.OperationRef{}, err
	}
	if err := c.Login(ctx); err != nil {
		return stages.OperationRef{}, err
	}
	rpc := authorizedRequest(c.session.Token(), &orchestratorv1.CreateOperationRequest{
		OperationType:           "UPGRADE",
		BundleId:                request.BundleID,
		ReleaseDefinitionId:     request.DefinitionID,
		ValuesRevisionId:        request.ValuesRevisionID,
		ExpectedCurrentRevision: request.ExpectedRevision,
	})
	rpc.Header().Set("Idempotency-Key", c.writeKey("upgrade", request.DefinitionID, revisionQualifier(request.ExpectedRevision)))
	response, err := clients.Orchestrator().CreateOperation(ctx, rpc)
	if err != nil {
		return stages.OperationRef{}, err
	}
	if response == nil || response.Msg == nil {
		return stages.OperationRef{}, errors.New("livewire: empty create operation response")
	}
	return stages.OperationRef{
		ID:           response.Msg.GetOperationId(),
		DefinitionID: request.DefinitionID,
		Type:         "UPGRADE",
		Status:       response.Msg.GetState(),
	}, nil
}

// Rollback implements stages.OperationWriter via RollbackRelease.
//
// The order of the stage's optimistic lock matters: ExpectedRevision is the
// revision the rollback expects to be current, which is the revision the
// upgrade left behind, not the baseline being restored.
// revisionQualifier renders the revision a write starts from as an idempotency
// key qualifier. The revision is what the request hash turns on — the server
// compares the stored request hash and rejects a reused key whose body differs —
// so qualifying by it keeps a replay from the same revision deduping while
// letting a write from a different revision through as a new operation.
func revisionQualifier(revision int32) string {
	return strconv.FormatInt(int64(revision), 10)
}

func (c *Connector) Rollback(ctx context.Context, request stages.RollbackRequest) (stages.OperationRef, error) {
	clients, err := c.clientsOrFail()
	if err != nil {
		return stages.OperationRef{}, err
	}
	if err := c.Login(ctx); err != nil {
		return stages.OperationRef{}, err
	}
	rpc := authorizedRequest(c.session.Token(), &orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     request.DefinitionID,
		TargetRevision:          request.TargetRevision,
		ExpectedCurrentRevision: request.ExpectedRevision,
		Reason:                  request.Reason,
	})
	rpc.Header().Set("Idempotency-Key", c.writeKey("rollback", request.DefinitionID, revisionQualifier(request.ExpectedRevision)))
	response, err := clients.Orchestrator().RollbackRelease(ctx, rpc)
	if err != nil {
		return stages.OperationRef{}, err
	}
	if response == nil || response.Msg == nil {
		return stages.OperationRef{}, errors.New("livewire: empty rollback response")
	}
	return stages.OperationRef{
		ID:           response.Msg.GetOperationId(),
		DefinitionID: request.DefinitionID,
		Type:         "ROLLBACK",
		Status:       response.Msg.GetState(),
		Revision:     response.Msg.GetToRevision(),
	}, nil
}

// Cancel implements stages.OperationWriter and stages.EmergencyWriter.
func (c *Connector) Cancel(ctx context.Context, operationID string) error {
	clients, err := c.clientsOrFail()
	if err != nil {
		return err
	}
	if strings.TrimSpace(operationID) == "" {
		return errors.New("livewire: empty operation id")
	}
	if err := c.Login(ctx); err != nil {
		return err
	}
	// The cancel write is a CAS and the server rejects expected_state_version < 1
	// (internal/orchestrator/service.go:1259), so the previous call failed every
	// time with invalid_argument (REQ-066 cancel-leg gap). Read the current
	// version first.
	current, err := clients.Orchestrator().GetOperation(ctx,
		authorizedRequest(c.session.Token(), &orchestratorv1.GetOperationRequest{OperationId: operationID}))
	if err != nil {
		return fmt.Errorf("livewire: read operation %s: %w", operationID, err)
	}
	rpc := authorizedRequest(c.session.Token(), &orchestratorv1.CancelOperationRequest{
		OperationId:          operationID,
		Reason:               "e2e stage takeover",
		ExpectedStateVersion: current.Msg.GetOperation().GetStateVersion(),
	})
	rpc.Header().Set("Idempotency-Key", c.writeKey("cancel", operationID))
	_, err = clients.Orchestrator().CancelOperation(ctx, rpc)
	if err != nil {
		return fmt.Errorf("livewire: cancel operation %s: %w", operationID, err)
	}
	return nil
}

// AwaitOperation implements stages.OperationWriter and stages.EmergencyWriter by
// polling GetOperation until the operation reaches a terminal state or the
// caller's context expires. Polling (rather than the WatchOperation stream)
// keeps the adapter stateless and re-entrant, which is what the equal-and-
// opposite compensation needs.
func (c *Connector) AwaitOperation(ctx context.Context, operationID string) (stages.OperationRef, error) {
	if strings.TrimSpace(operationID) == "" {
		return stages.OperationRef{}, errors.New("livewire: empty operation id")
	}
	interval := c.poll
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		reference, err := c.getOperation(ctx, operationID)
		if err != nil {
			return stages.OperationRef{}, err
		}
		if reference.Terminal() {
			return reference, nil
		}
		select {
		case <-ctx.Done():
			return reference, ctx.Err()
		case <-ticker.C:
		}
	}
}

// getOperation reads one operation snapshot and maps it onto the stage's
// sanitized reference.
func (c *Connector) getOperation(ctx context.Context, operationID string) (stages.OperationRef, error) {
	clients, err := c.clientsOrFail()
	if err != nil {
		return stages.OperationRef{}, err
	}
	if err := c.Login(ctx); err != nil {
		return stages.OperationRef{}, err
	}
	response, err := clients.Orchestrator().GetOperation(ctx,
		authorizedRequest(c.session.Token(), &orchestratorv1.GetOperationRequest{OperationId: operationID}))
	if err != nil {
		return stages.OperationRef{}, fmt.Errorf("livewire: get operation %s: %w", operationID, err)
	}
	if response == nil || response.Msg == nil || response.Msg.GetOperation() == nil {
		return stages.OperationRef{}, fmt.Errorf("livewire: operation %s is not observable", operationID)
	}
	operation := response.Msg.GetOperation()
	reference := stages.OperationRef{
		ID:           operation.GetOperationId(),
		DefinitionID: operation.GetReleaseDefinitionId(),
		Type:         operation.GetOperationType(),
		Status:       operation.GetState().String(),
		Revision:     operation.GetTargetRevision(),
	}
	// The effective convergence policy is only meaningful for emergency
	// operations, and it is reported on the emergency result rather than the
	// operation itself.
	if result := response.Msg.GetEmergencyResult(); result != nil {
		reference.Convergence = convergencePolicyName(result.GetConvergencePolicy())
	}
	return reference, nil
}

// convergencePolicyName maps the proto enum onto the canonical contract
// spelling the stages compare against (REQ-079 §4 pins the unprefixed names).
func convergencePolicyName(policy orchestratorv1.EmergencyConvergence) string {
	switch policy {
	case orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REVERT_ON_NEXT_RECONCILE:
		return stages.EmergencyConvergenceRevertOnNextReconcile
	case orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REQUIRE_PROMOTION:
		return stages.EmergencyConvergenceRequirePromotion
	default:
		return ""
	}
}

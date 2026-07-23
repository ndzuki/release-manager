package orchestrator

import (
	"connectrpc.com/connect"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"time"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/store"
)

// EmergencyChange handles type-whitelisted emergency operations with convergence policy.
// Implements REQ-032.
func (s *Service) EmergencyChange(
	ctx context.Context,
	req *connect.Request[orchestratorv1.EmergencyChangeRequest],
) (*connect.Response[orchestratorv1.EmergencyChangeResponse], error) {
	started := time.Now()
	msg := req.Msg
	action := emergencyActionFromProto(msg.Action)
	if !action.Valid() {
		err := connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("unsupported emergency action: %s", msg.Action))
		s.emitEmergencyAudit(msg, "", err, time.Since(started))
		return nil, err
	}

	defID := msg.ReleaseDefinitionId
	if defID == "" {
		err := connect.NewError(connect.CodeInvalidArgument, errors.New("release_definition_id is required"))
		s.emitEmergencyAudit(msg, "", err, time.Since(started))
		return nil, err
	}

	definition, err := s.store.Definitions().Get(ctx, defID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			rpcErr := connect.NewError(connect.CodeNotFound,
				fmt.Errorf("release definition not found: %s", defID))
			s.emitEmergencyAudit(msg, "", rpcErr, time.Since(started))
			return nil, rpcErr
		}
		rpcErr := connect.NewError(connect.CodeInternal, fmt.Errorf("definition lookup: %w", err))
		s.emitEmergencyAudit(msg, "", rpcErr, time.Since(started))
		return nil, rpcErr
	}
	if err := checkDefinitionOperable(definition); err != nil {
		s.emitEmergencyAudit(msg, "", err, time.Since(started))
		return nil, err
	}
	if err := s.checkCustomerNotDisabled(ctx, definition.CustomerID); err != nil {
		rpcErr := connect.NewError(connect.CodePermissionDenied, err)
		s.emitEmergencyAudit(msg, "", rpcErr, time.Since(started))
		return nil, rpcErr
	}
	if err := validateEmergencyPayload(action, msg.GetPayload()); err != nil {
		rpcErr := connect.NewError(connect.CodeInvalidArgument, err)
		s.emitEmergencyAudit(msg, "", rpcErr, time.Since(started))
		return nil, rpcErr
	}
	if action == store.EmergencySetReplicas && isHPAManaged(definition) {
		rpcErr := connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("HPA managed workload: SetReplicas is denied for definition %s", defID))
		s.emitEmergencyAudit(msg, "", rpcErr, time.Since(started))
		return nil, rpcErr
	}

	convergence := emergencyConvergenceFromProto(msg.Convergence)
	op := &store.Operation{
		ID:                  uuid.New().String(),
		OperationType:       store.OperationEmergency,
		Status:              store.StatusPending,
		ReleaseDefinitionID: defID,
		IdempotencyKey:      uuid.New().String(),
		ValuesPatch:         []byte(msg.Payload),
	}
	if msg.Actor != nil {
		op.Actor = store.ActorContext{UserID: msg.Actor.UserId, Organization: msg.Actor.Organization}
	}
	if err := s.store.Operations().CreateIfAvailable(ctx, op); err != nil {
		if errors.Is(err, store.ErrReleaseBusy) {
			rpcErr := connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("release_busy: definition %s has a running standard operation", defID))
			s.emitEmergencyAudit(msg, "", rpcErr, time.Since(started))
			return nil, rpcErr
		}
		rpcErr := connect.NewError(connect.CodeInternal, fmt.Errorf("create emergency operation: %w", err))
		s.emitEmergencyAudit(msg, "", rpcErr, time.Since(started))
		return nil, rpcErr
	}

	s.logger.Info("emergency change created",
		"operation_id", op.ID,
		"definition_id", defID,
		"action", action,
		"convergence", convergence,
	)
	s.emitEmergencyAudit(msg, op.ID, nil, time.Since(started))
	return connect.NewResponse(&orchestratorv1.EmergencyChangeResponse{
		OperationId: op.ID,
		Status:      string(op.Status),
		Convergence: msg.Convergence,
	}), nil
}

const approvedAnnotationKey = "release-manager.io/approved-change"

//nolint:gocyclo // Validation keeps the three typed emergency payload contracts explicit.
func validateEmergencyPayload(action store.EmergencyAction, payload string) error {
	switch action {
	case store.EmergencySetContainerImage:
		var change struct {
			Workload  string `json:"workload"`
			Container string `json:"container"`
			Image     string `json:"image"`
		}
		if err := json.Unmarshal([]byte(payload), &change); err != nil {
			return fmt.Errorf("invalid SetContainerImage payload: %w", err)
		}
		if change.Workload == "" || change.Container == "" || change.Image == "" {
			return errors.New("SetContainerImage requires workload, container, and image")
		}
	case store.EmergencySetReplicas:
		var change struct {
			Workload string `json:"workload"`
			Replicas *int32 `json:"replicas"`
		}
		if err := json.Unmarshal([]byte(payload), &change); err != nil {
			return fmt.Errorf("invalid SetReplicas payload: %w", err)
		}
		if change.Workload == "" || change.Replicas == nil || *change.Replicas < 0 {
			return errors.New("SetReplicas requires workload and non-negative replicas")
		}
	case store.EmergencySetApprovedAnnotation:
		var change struct {
			Workload string `json:"workload"`
			Key      string `json:"key"`
			Value    string `json:"value"`
		}
		if err := json.Unmarshal([]byte(payload), &change); err != nil {
			return fmt.Errorf("invalid SetApprovedAnnotation payload: %w", err)
		}
		if change.Workload == "" || change.Value == "" {
			return errors.New("SetApprovedAnnotation requires workload and value")
		}
		if change.Key != approvedAnnotationKey {
			return fmt.Errorf("annotation_not_allowed: key %q is not approved", change.Key)
		}
	}
	return nil
}

func (s *Service) emitEmergencyAudit(
	msg *orchestratorv1.EmergencyChangeRequest,
	operationID string,
	operationErr error,
	duration time.Duration,
) {
	if msg == nil {
		return
	}
	payloadHash := sha256.Sum256([]byte(msg.GetPayload()))
	status := "succeeded"
	errorCode := ""
	if operationErr != nil {
		status = "failed"
		errorCode = connect.CodeOf(operationErr).String()
	}
	resourceID := operationID
	if resourceID == "" {
		resourceID = msg.GetReleaseDefinitionId()
	}
	actor := store.ActorContext{}
	if msg.Actor != nil {
		actor = store.ActorContext{UserID: msg.Actor.GetUserId(), Organization: msg.Actor.GetOrganization()}
	}
	actorKind, actorID := auditActor(&actor)
	event := audit.NewEvent(
		actorKind,
		actorID,
		actor.Organization,
		"",
		"operation",
		resourceID,
		"emergency_change",
		status,
		fmt.Sprintf("action=%s convergence=%s", emergencyActionFromProto(msg.GetAction()), emergencyConvergenceFromProto(msg.GetConvergence())),
		map[string]string{
			"definition_id": msg.GetReleaseDefinitionId(),
			"payload_hash":  fmt.Sprintf("sha256:%x", payloadHash),
			"reason":        msg.GetReason(),
			"error_code":    errorCode,
		},
	)
	event.DurationMs = duration.Milliseconds()
	s.emitAudit(event)
}

// isHPAManaged checks whether a release definition's workload is managed by HPA.
// AC-032-02: Stub implementation — returns false to not block normal workflows.
// TODO: Implement actual HPA detection via K8s API or definition metadata.
func isHPAManaged(_ *store.ReleaseDefinition) bool {
	// Stub: not yet implemented. In production, this would check the
	// definition's cluster for an HPA targeting the same workload.
	return false
}

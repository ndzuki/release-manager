package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/store"
)

// EmergencyChange handles type-whitelisted emergency operations with convergence policy.
// Implements REQ-032.
func (s *Service) EmergencyChange(
	ctx context.Context,
	req *connect.Request[orchestratorv1.EmergencyChangeRequest],
) (*connect.Response[orchestratorv1.EmergencyChangeResponse], error) {
	started := time.Now()
	var operationID string
	defer func() {
		if r := recover(); r != nil {
			s.emitEmergencyAudit(ctx, req.Msg, operationID,
				connect.NewError(connect.CodeInternal, fmt.Errorf("panic: %v", r)), time.Since(started))
			panic(r)
		}
	}()
	msg := req.Msg

	// Validate action is in the whitelist.
	action := emergencyActionFromProto(msg.Action)
	if !action.Valid() {
		err := connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("unsupported emergency action: %s", msg.Action))
		s.emitEmergencyAudit(ctx, msg, operationID, err, time.Since(started))
		return nil, err
	}

	defID := msg.ReleaseDefinitionId
	if defID == "" {
		err := connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("release_definition_id is required"))
		s.emitEmergencyAudit(ctx, msg, operationID, err, time.Since(started))
		return nil, err
	}

	def, err := s.store.Definitions().Get(ctx, defID)
	if err != nil {
		if err == store.ErrNotFound {
			rpcErr := connect.NewError(connect.CodeNotFound,
				fmt.Errorf("release definition not found: %s", defID))
			s.emitEmergencyAudit(ctx, msg, operationID, rpcErr, time.Since(started))
			return nil, rpcErr
		}
		rpcErr := connect.NewError(connect.CodeInternal, fmt.Errorf("definition lookup: %w", err))
		s.emitEmergencyAudit(ctx, msg, operationID, rpcErr, time.Since(started))
		return nil, rpcErr
	}

	if err := s.checkCustomerNotDisabled(ctx, def.CustomerID); err != nil {
		rpcErr := connect.NewError(connect.CodePermissionDenied, err)
		s.emitEmergencyAudit(ctx, msg, operationID, rpcErr, time.Since(started))
		return nil, rpcErr
	}
	if err := validateEmergencyPayload(action, msg.GetPayload()); err != nil {
		rpcErr := connect.NewError(connect.CodeInvalidArgument, err)
		s.emitEmergencyAudit(ctx, msg, operationID, rpcErr, time.Since(started))
		return nil, rpcErr
	}

	if action == store.EmergencySetReplicas && isHPAManaged(def) {
		rpcErr := connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("HPA managed workload: SetReplicas is denied for definition %s", defID))
		s.emitEmergencyAudit(ctx, msg, operationID, rpcErr, time.Since(started))
		return nil, rpcErr
	}

	hasActive, err := s.store.Operations().HasActiveStandardForDefinition(ctx, defID)
	if err != nil {
		rpcErr := connect.NewError(connect.CodeInternal, fmt.Errorf("standard operation active check: %w", err))
		s.emitEmergencyAudit(ctx, msg, operationID, rpcErr, time.Since(started))
		return nil, rpcErr
	}
	if hasActive {
		rpcErr := connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("definition %s has a running standard operation; EMERGENCY operation is denied", defID))
		s.emitEmergencyAudit(ctx, msg, operationID, rpcErr, time.Since(started))
		return nil, rpcErr
	}

	convergence := emergencyConvergenceFromProto(msg.Convergence)
	actorUserID := auth.UserIDFromContext(ctx)
	actorOrganizationID := auth.OrganizationIDFromContext(ctx)
	if actorUserID == "" && msg.Actor != nil {
		actorUserID = msg.Actor.GetUserId()
		actorOrganizationID = msg.Actor.GetOrganization()
	}
	op := &store.Operation{
		ID:                  uuid.New().String(),
		OperationType:       store.OperationEmergency,
		Status:              store.StatusPending,
		ReleaseDefinitionID: defID,
		IdempotencyKey:      uuid.New().String(),
		ValuesPatch:         []byte(msg.Payload),
		EmergencyAction:     action,
		Convergence:         convergence,
		Actor: store.ActorContext{
			UserID:       actorUserID,
			Organization: actorOrganizationID,
		},
	}

	if err := s.store.Operations().Create(ctx, op); err != nil {
		rpcErr := connect.NewError(connect.CodeInternal,
			fmt.Errorf("create emergency operation: %w", err))
		s.emitEmergencyAudit(ctx, msg, operationID, rpcErr, time.Since(started))
		return nil, rpcErr
	}
	operationID = op.ID

	s.logger.Info("emergency change created",
		"operation_id", op.ID,
		"definition_id", defID,
		"action", action,
		"convergence", convergence,
	)
	s.emitEmergencyAudit(ctx, msg, operationID, nil, time.Since(started))
	return connect.NewResponse(&orchestratorv1.EmergencyChangeResponse{
		OperationId: op.ID,
		Status:      string(op.Status),
		Convergence: msg.Convergence,
	}), nil
}

// isHPAManaged checks the definition metadata maintained by release config.
func isHPAManaged(definition *store.ReleaseDefinition) bool {
	return definition.HPAManaged
}

const approvedAnnotationKey = "release-manager.io/approved-change"

func validateEmergencyPayload(action store.EmergencyAction, payload string) error {
	switch action {
	case store.EmergencySetContainerImage:
		return validateSetContainerImagePayload(payload)
	case store.EmergencySetReplicas:
		return validateSetReplicasPayload(payload)
	case store.EmergencySetApprovedAnnotation:
		return validateSetApprovedAnnotationPayload(payload)
	default:
		return fmt.Errorf("unsupported emergency action: %s", action)
	}
}

func validateSetContainerImagePayload(payload string) error {
	var change struct {
		Workload  string `json:"workload"`
		Container string `json:"container"`
		Image     string `json:"image"`
	}
	if err := json.Unmarshal([]byte(payload), &change); err != nil {
		return fmt.Errorf("invalid SetContainerImage payload: %w", err)
	}
	if change.Workload == "" || change.Container == "" || change.Image == "" {
		return fmt.Errorf("SetContainerImage requires workload, container, and image")
	}
	return nil
}

func validateSetReplicasPayload(payload string) error {
	var change struct {
		Workload string `json:"workload"`
		Replicas *int32 `json:"replicas"`
	}
	if err := json.Unmarshal([]byte(payload), &change); err != nil {
		return fmt.Errorf("invalid SetReplicas payload: %w", err)
	}
	if change.Workload == "" || change.Replicas == nil || *change.Replicas < 0 {
		return fmt.Errorf("SetReplicas requires workload and non-negative replicas")
	}
	return nil
}

func validateSetApprovedAnnotationPayload(payload string) error {
	var change struct {
		Workload string `json:"workload"`
		Key      string `json:"key"`
		Value    string `json:"value"`
	}
	if err := json.Unmarshal([]byte(payload), &change); err != nil {
		return fmt.Errorf("invalid SetApprovedAnnotation payload: %w", err)
	}
	if change.Workload == "" || change.Value == "" {
		return fmt.Errorf("SetApprovedAnnotation requires workload and value")
	}
	if change.Key != approvedAnnotationKey {
		return fmt.Errorf("annotation_not_allowed: key %q is not approved", change.Key)
	}
	return nil
}

func (s *Service) emitEmergencyAudit(
	ctx context.Context,
	msg *orchestratorv1.EmergencyChangeRequest,
	operationID string,
	operationErr error,
	duration time.Duration,
) {
	if s.audit == nil || msg == nil {
		return
	}

	action := emergencyActionFromProto(msg.GetAction())
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

	actorID := auth.UserIDFromContext(ctx)
	organizationID := auth.OrganizationIDFromContext(ctx)
	if actorID == "" && msg.Actor != nil {
		actorID = msg.Actor.GetUserId()
		organizationID = msg.Actor.GetOrganization()
	}
	event := &store.AuditEvent{
		ActorKind:      store.AuditActorUser,
		ActorID:        actorID,
		OrganizationID: organizationID,
		ResourceType:   "operation",
		ResourceID:     resourceID,
		Action:         "emergency_change",
		Status:         status,
		DurationMs:     duration.Milliseconds(),
		ChangeSummary: fmt.Sprintf(
			"action=%s convergence=%s",
			action,
			emergencyConvergenceFromProto(msg.GetConvergence()),
		),
		Metadata: map[string]string{
			"definition_id": msg.GetReleaseDefinitionId(),
			"payload_hash":  fmt.Sprintf("sha256:%x", payloadHash),
			"reason":        msg.GetReason(),
			"error_code":    errorCode,
		},
	}
	if !s.audit.Emit(event) {
		s.logger.Error("emergency audit event rejected",
			"operation_id", operationID,
			"definition_id", msg.GetReleaseDefinitionId(),
			"status", status,
		)
	}
}

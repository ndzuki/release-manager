package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

// emergencyLockManagementError maps ReleaseEmergencyLock store/safety failures
// onto the REQ-087 §10 error model.
func emergencyLockManagementError(code connect.Code, reason, message string) error {
	return emergencyError(code, reason, message)
}

// authorizeEmergencyLockScope verifies the actor may manage emergency target
// locks (REQ-087 §11: release_admin/platform_admin via the
// release.emergency.execute capability on an active organization-customer
// binding) and returns the customer scope it may operate on. With a
// definition id the scope is that definition's customer; without one the
// scope is every customer the actor's organization has an active binding to
// AND the actor may execute emergency changes for — an actor authorized for
// none of them is denied (AC-087-10).
func (s *Service) authorizeEmergencyLockScope(ctx context.Context, actor authctx.Actor, definitionID string) ([]string, error) {
	if s.authorizer == nil {
		return nil, emergencyLockManagementError(connect.CodeUnavailable, "authorization_snapshot_stale", "authorization snapshot is unavailable")
	}
	if definitionID != "" {
		definition, err := s.store.Definitions().Get(ctx, definitionID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, emergencyLockManagementError(connect.CodeNotFound, "definition_not_found", "release definition not found")
		}
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load stuck-lock definition: %w", err))
		}
		if err := s.store.Bindings().RequireActive(ctx, actor.OrganizationID, definition.CustomerID); err != nil {
			return nil, emergencyLockManagementError(connect.CodePermissionDenied, "permission_denied", "actor is not authorized for customer")
		}
		if err := s.authorizer.AuthorizeWrite(ctx, actor, definition.CustomerID, store.AuthorizationExecuteEmergency); err != nil {
			return nil, emergencyLockManagementError(connect.CodePermissionDenied, "permission_denied", "actor is not authorized to release emergency locks")
		}
		return []string{definition.CustomerID}, nil
	}

	bindings, err := s.store.Bindings().ListByOrg(ctx, actor.OrganizationID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list stuck-lock bindings: %w", err))
	}
	scope := make([]string, 0)
	for _, binding := range bindings {
		if binding.Status != store.BindingActive {
			continue
		}
		if err := s.authorizer.AuthorizeWrite(ctx, actor, binding.CustomerID, store.AuthorizationExecuteEmergency); err == nil {
			scope = append(scope, binding.CustomerID)
		}
	}
	if len(scope) == 0 {
		return nil, emergencyLockManagementError(connect.CodePermissionDenied, "permission_denied", "actor is not authorized for any customer")
	}
	return scope, nil
}

// ListStuckLocks lists stuck emergency target locks (REQ-087 §4.1/§4.2,
// AC-087-07): terminal EMERGENCY operations whose effect is still UNKNOWN
// past emergency.effect_observe_timeout, not explicitly released. The result
// is a derived projection — listing never releases or mutates anything.
func (s *Service) ListStuckLocks(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ListStuckLocksRequest],
) (*connect.Response[orchestratorv1.ListStuckLocksResponse], error) {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return nil, emergencyLockManagementError(connect.CodeUnauthenticated, "authentication_required", "authentication required")
	}
	cfg, err := s.store.EmergencyConfig().GetEmergencyConfig(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load emergency config: %w", err))
	}
	definitionID := req.Msg.GetReleaseDefinitionId()
	customerScope, err := s.authorizeEmergencyLockScope(ctx, actor, definitionID)
	if err != nil {
		return nil, err
	}
	locks, err := s.store.EmergencyIntents().ListStuckLocks(ctx, store.StuckLockFilter{
		DefinitionID:   definitionID,
		CustomerIDs:    customerScope,
		ObserveTimeout: cfg.EffectObserveTimeout,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list stuck locks: %w", err))
	}
	protoLocks := make([]*orchestratorv1.StuckLock, 0, len(locks))
	for _, lock := range locks {
		protoLocks = append(protoLocks, stuckLockToProto(lock, cfg.EffectObserveTimeout))
	}
	return connect.NewResponse(&orchestratorv1.ListStuckLocksResponse{Locks: protoLocks}), nil
}

func stuckLockToProto(lock *store.StuckLock, observeTimeout time.Duration) *orchestratorv1.StuckLock {
	return &orchestratorv1.StuckLock{
		IntentId:              lock.Intent.ID,
		OperationId:           lock.Intent.OperationID,
		ReleaseDefinitionId:   lock.Intent.ReleaseDefinitionID,
		Action:                emergencyActionToProto(lock.Intent.Action),
		LockPathSummary:       lock.LockPathSummary,
		EffectStatus:          emergencyEffectToProto(lock.Intent.EffectStatus),
		TerminalAt:            timestamppb.New(lock.TerminalAt),
		StuckSince:            timestamppb.New(lock.TerminalAt.Add(observeTimeout)),
		ObserveTimeoutDisplay: observeTimeout.String(),
	}
}

// ReleaseEmergencyLock releases one stuck emergency target lock (REQ-087
// §4.2, AC-087-08/09/10): NOT_APPLIED_PROVEN records NOT_APPLIED + releases;
// AUDITED_OVERRIDE releases while the effect stays UNKNOWN. Every release is
// audited with who/when/mode/evidence/reason and the before/after effect.
func (s *Service) ReleaseEmergencyLock(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ReleaseEmergencyLockRequest],
) (*connect.Response[orchestratorv1.ReleaseEmergencyLockResponse], error) {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return nil, emergencyLockManagementError(connect.CodeUnauthenticated, "authentication_required", "authentication required")
	}
	msg := req.Msg
	if strings.TrimSpace(msg.GetReason()) == "" {
		return nil, emergencyLockManagementError(connect.CodeInvalidArgument, "reason_required", "reason is required")
	}
	if len([]rune(strings.TrimSpace(msg.GetReason()))) > 1000 {
		return nil, emergencyLockManagementError(connect.CodeInvalidArgument, "reason_required", "reason exceeds 1000 characters")
	}
	if len([]rune(msg.GetEvidence())) > 500 {
		return nil, emergencyLockManagementError(connect.CodeInvalidArgument, "reason_required", "evidence exceeds 500 characters")
	}
	mode, ok := releaseModeFromProto(msg.GetMode())
	if !ok {
		return nil, emergencyLockManagementError(connect.CodeInvalidArgument, "release_mode_unspecified", "release mode is required")
	}

	intent, err := s.store.EmergencyIntents().GetByID(ctx, msg.GetIntentId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, emergencyLockManagementError(connect.CodeNotFound, "intent_not_found", "emergency intent not found")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load stuck-lock intent: %w", err))
	}
	if _, err := s.authorizeEmergencyLockScope(ctx, actor, intent.ReleaseDefinitionID); err != nil {
		return nil, err
	}
	cfg, err := s.store.EmergencyConfig().GetEmergencyConfig(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load emergency config: %w", err))
	}

	released, err := s.store.EmergencyIntents().ReleaseLock(ctx, store.ReleaseLockCommand{
		IntentID:       intent.ID,
		Mode:           mode,
		Reason:         strings.TrimSpace(msg.GetReason()),
		Evidence:       msg.GetEvidence(),
		ObserveTimeout: cfg.EffectObserveTimeout,
	})
	if err != nil {
		return nil, mapReleaseLockError(err)
	}

	// Exactly one audited release event with before/after effect + lock state.
	auditEventID := s.emitEmergencyLockReleaseAudit(actor, intent, released, msg.GetReason(), msg.GetEvidence(), string(mode))
	return connect.NewResponse(&orchestratorv1.ReleaseEmergencyLockResponse{
		IntentId:     released.Intent.ID,
		LockReleased: true,
		EffectStatus: emergencyEffectToProto(released.Intent.EffectStatus),
		AuditEventId: auditEventID,
	}), nil
}

func releaseModeFromProto(mode orchestratorv1.ReleaseMode) (store.EmergencyReleaseMode, bool) {
	switch mode {
	case orchestratorv1.ReleaseMode_NOT_APPLIED_PROVEN:
		return store.EmergencyReleaseNotAppliedProven, true
	case orchestratorv1.ReleaseMode_AUDITED_OVERRIDE:
		return store.EmergencyReleaseAuditedOverride, true
	default:
		return "", false
	}
}

func mapReleaseLockError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return emergencyLockManagementError(connect.CodeNotFound, "intent_not_found", "emergency intent not found")
	case errors.Is(err, store.ErrInvalidState):
		return emergencyLockManagementError(connect.CodeFailedPrecondition, "operation_not_terminal", "operation is not terminal; use CancelOperation")
	case errors.Is(err, store.ErrLockNotStuck):
		return emergencyLockManagementError(connect.CodeFailedPrecondition, "lock_not_stuck_or_already_released", "lock is not stuck or already released")
	case errors.Is(err, store.ErrConvergencePending):
		return emergencyLockManagementError(connect.CodeFailedPrecondition, "convergence_pending", "convergence task is pending promotion; converge it before releasing")
	case errors.Is(err, store.ErrOptimisticLock):
		return emergencyLockManagementError(connect.CodeAborted, "optimistic_lock_conflict", "lock state changed concurrently; retry")
	default:
		return connect.NewError(connect.CodeInternal, fmt.Errorf("release emergency lock: %w", err))
	}
}

// emitEmergencyLockReleaseAudit records one audited lock release (REQ-087 §5
// / ADR-010): actor, intent/operation/definition ids, mode, sanitized reason
// and evidence, and before/after effect status. Returns the audit event id.
func (s *Service) emitEmergencyLockReleaseAudit(actor authctx.Actor, before *store.EmergencyIntent, released *store.ReleaseLockResult, reason, evidence, mode string) string {
	if s.auditEmitter == nil {
		return ""
	}
	event := audit.NewEvent(
		store.AuditActorUser,
		actor.UserID,
		actor.OrganizationID,
		firstRole(actor.Roles),
		"operation",
		released.Operation.ID,
		"emergency_lock_release",
		mode,
		fmt.Sprintf("intent=%s action=%s reason=%s evidence=%s", released.Intent.ID, released.Intent.Action, reason, evidence),
		map[string]string{
			"definition_id": released.Intent.ReleaseDefinitionID,
			"intent_id":     released.Intent.ID,
			"mode":          mode,
			"effect_before": string(before.EffectStatus),
			"effect_after":  string(released.Intent.EffectStatus),
		},
	)
	result := s.auditEmitter.Emit(event)
	if !result.Accepted {
		s.logger.Warn("emergency lock release audit rejected", "intent_id", released.Intent.ID, "code", result.Code)
		return ""
	}
	return result.EventID
}

func firstRole(roles []string) string {
	if len(roles) == 0 {
		return ""
	}
	return roles[0]
}

// alertedStuckLocks deduplicates stuck-lock alerts within one process: each
// intent_id is alerted (slog + audit) once and forgotten once it no longer
// appears in the derived stuck set (resolved or released).
type AlertedStuckLocks struct {
	intentIDs map[string]struct{}
}

func NewAlertedStuckLocks() *AlertedStuckLocks {
	return &AlertedStuckLocks{intentIDs: make(map[string]struct{})}
}

// ScanStuckEmergencyLocks is the background stuck-lock observer (REQ-087
// D5=B / AC-087-07): it lists all stuck locks across the whole system (the
// scan is system-level — no actor, no authorization) and, for each lock not
// previously alerted, logs a warning and records an audit event. It never
// releases or mutates locks (no automatic TTL unlock).
func (s *Service) ScanStuckEmergencyLocks(ctx context.Context, alerted *AlertedStuckLocks) int {
	cfg, err := s.store.EmergencyConfig().GetEmergencyConfig(ctx)
	if err != nil {
		s.logger.Warn("failed to load emergency config for stuck-lock scan", "error", err)
		return 0
	}
	locks, err := s.store.EmergencyIntents().ListStuckLocks(ctx, store.StuckLockFilter{
		ObserveTimeout: cfg.EffectObserveTimeout,
	})
	if err != nil {
		s.logger.Warn("failed to list stuck emergency locks", "error", err)
		return 0
	}
	now := time.Now().UTC()
	current := make(map[string]struct{}, len(locks))
	for _, lock := range locks {
		current[lock.Intent.ID] = struct{}{}
		if _, ok := alerted.intentIDs[lock.Intent.ID]; ok {
			continue
		}
		s.logger.Warn("emergency target lock is stuck",
			"intent_id", lock.Intent.ID,
			"operation_id", lock.Intent.OperationID,
			"definition_id", lock.Intent.ReleaseDefinitionID,
			"action", lock.Intent.Action,
			"lock_path", lock.LockPathSummary,
			"terminal_at", lock.TerminalAt.Format(time.RFC3339),
		)
		s.emitEmergencyLockStuckAudit(lock)
		alerted.intentIDs[lock.Intent.ID] = struct{}{}
	}
	// Forget intents that are no longer stuck (resolved/released) so a later
	// re-stuck lock can alert again.
	for id := range alerted.intentIDs {
		if _, ok := current[id]; !ok {
			delete(alerted.intentIDs, id)
		}
	}
	_ = now
	return len(current)
}

func (s *Service) emitEmergencyLockStuckAudit(lock *store.StuckLock) {
	if s.auditEmitter == nil {
		return
	}
	event := audit.NewEvent(
		store.AuditActorSystem,
		"",
		"",
		"",
		"operation",
		lock.Intent.OperationID,
		"emergency_lock_stuck",
		"stuck",
		fmt.Sprintf("intent=%s action=%s lock_path=%s", lock.Intent.ID, lock.Intent.Action, lock.LockPathSummary),
		map[string]string{
			"definition_id": lock.Intent.ReleaseDefinitionID,
			"intent_id":     lock.Intent.ID,
			"effect_status": string(lock.Intent.EffectStatus),
			"terminal_at":   lock.TerminalAt.Format(time.RFC3339),
		},
	)
	result := s.auditEmitter.Emit(event)
	if !result.Accepted {
		s.logger.Warn("emergency lock stuck audit rejected", "intent_id", lock.Intent.ID, "code", result.Code)
	}
}

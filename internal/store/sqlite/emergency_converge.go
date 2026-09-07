package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ndzuki/release-manager/internal/store"
)

// ConvergeEmergencyResult atomically converges one authoritative terminal
// EMERGENCY result (REQ-087 D1=D2, AC-087-01/02/03). See
// store.EmergencyConvergeCommand for the full ordering semantics. The whole
// advance+terminal sequence runs inside a single CAS transaction so a result
// that beats the queued→running migration never produces an illegal
// queued→succeeded/failed row or timeline entry (ADR-009 / REQ-032 §475-487).
//
//nolint:gocyclo // ConvergeEmergencyResult mirrors the full EMERGENCY state-machine/lock-release decision matrix (hop advance, terminal resolution, safety guards); branching is inherent.
func (s *emergencyIntentStore) ConvergeEmergencyResult(ctx context.Context, command store.EmergencyConvergeCommand) (*store.EmergencyConvergeResult, error) {
	if command.OperationID == "" || command.IntentID == "" {
		return nil, fmt.Errorf("converge emergency result: operation_id and intent_id are required")
	}
	if !command.Status.IsTerminal() {
		return nil, fmt.Errorf("converge emergency result: status %q is not terminal", command.Status)
	}
	if command.EffectStatus != store.EmergencyEffectApplied && command.EffectStatus != store.EmergencyEffectNotApplied {
		return nil, fmt.Errorf("converge emergency result: invalid effect status %q", command.EffectStatus)
	}
	if command.ExpectedStateVersion < 1 {
		return nil, fmt.Errorf("converge emergency result: invalid expected state version %d", command.ExpectedStateVersion)
	}

	var result *store.EmergencyConvergeResult
	err := retryBusy(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin converge emergency result: %w", err)
		}
		defer tx.Rollback() //nolint:errcheck // no-op after commit

		current, err := getOperation(ctx, tx, command.OperationID)
		if err != nil {
			return err
		}
		if current.OperationType != store.OperationEmergency {
			return store.ErrInvalidState
		}
		intent, err := getEmergencyIntentByOperation(ctx, tx, command.OperationID)
		if err != nil {
			return err
		}

		// ── Terminal operation ──
		if current.Status.IsTerminal() {
			// Idempotent replay of the same effect: no-op.
			if intent.EffectStatus == command.EffectStatus {
				result = &store.EmergencyConvergeResult{Operation: current, Intent: intent}
				return nil
			}
			// A resolved-but-different effect is a protocol conflict.
			if intent.EffectStatus != store.EmergencyEffectUnknown {
				return store.ErrInvalidState
			}
			// UNKNOWN → APPLIED/NOT_APPLIED: exactly one resolution allowed.
			if current.StateVersion != command.ExpectedStateVersion {
				return store.ErrOptimisticLock
			}
			updatedOp, updatedIntent, err := resolveUnknownEffectTx(ctx, tx, current, intent, command)
			if err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit converge emergency result: %w", err)
			}
			result = &store.EmergencyConvergeResult{Operation: updatedOp, Intent: updatedIntent, Resolved: true}
			return nil
		}

		// ── Non-terminal: advance along the legal EMERGENCY chain ──
		if current.StateVersion != command.ExpectedStateVersion {
			return store.ErrOptimisticLock
		}
		version := command.ExpectedStateVersion
		step := current
		// Advance through legal intermediate hops until running
		// (pending→queued→running as needed). Only EMERGENCY ops reach this
		// method; cancelling is never reachable for EMERGENCY (Cancel rejects
		// running EMERGENCY ops and direct-cancels queued/pending ones).
		for step.Status != store.StatusRunning {
			target := nextEmergencyHop(step.Status)
			if target == "" || !step.Status.CanTransitionTo(target) {
				return store.ErrInvalidState
			}
			advanced, hopErr := emergencyHopTx(ctx, tx, step, target, version, false, "", nil, nil, "")
			if hopErr != nil {
				return hopErr
			}
			step = advanced
			version = advanced.StateVersion
		}
		// Apply the terminal hop running→terminal.
		finished, err := emergencyHopTx(ctx, tx, step, command.Status, version, true,
			command.EffectStatus, command.BeforeSnapshot, command.AfterSnapshot, command.LastError)
		if err != nil {
			return err
		}
		updatedIntent, err := getEmergencyIntentByOperation(ctx, tx, command.OperationID)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit converge emergency result: %w", err)
		}
		result = &store.EmergencyConvergeResult{Operation: finished, Intent: updatedIntent, Resolved: true}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// nextEmergencyHop returns the next legal intermediate hop for a non-running
// EMERGENCY operation, or "" when there is none.
func nextEmergencyHop(status store.OperationStatus) store.OperationStatus {
	switch status {
	case store.StatusPending, store.StatusPreflight:
		return store.StatusQueued
	case store.StatusQueued:
		return store.StatusRunning
	default:
		return ""
	}
}

// emergencyHopTx advances one operation along its state machine inside the
// caller's transaction: it CAS-updates the operations row (state_version+1,
// terminal_at only on the terminal hop), writes the state-change event and
// timeline entry, and — on the terminal hop — writes the intent effect +
// snapshots (and an ERROR timeline entry for failed results with a reason).
//
//nolint:gocyclo // emergencyHopTx mirrors the full EMERGENCY state-machine/lock-release decision matrix (hop advance, terminal resolution, safety guards); branching is inherent.
func emergencyHopTx(
	ctx context.Context,
	tx *sql.Tx,
	current *store.Operation,
	target store.OperationStatus,
	expectedStateVersion int,
	terminal bool,
	effectStatus store.EmergencyEffectStatus,
	beforeSnapshot, afterSnapshot json.RawMessage,
	lastError string,
) (*store.Operation, error) {
	now := nowUTC()
	result, err := tx.ExecContext(ctx, `
		UPDATE operations
		SET status = ?, state_version = state_version + 1, last_error = ?, updated_at = ?,
		    terminal_at = CASE WHEN ? THEN ? ELSE terminal_at END
		WHERE id = ? AND state_version = ?
	`, string(target), lastError, now, terminal, now, current.ID, expectedStateVersion)
	if err != nil {
		return nil, fmt.Errorf("converge emergency operation hop: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("converge emergency operation hop rows: %w", err)
	}
	if rows == 0 {
		return nil, store.ErrOptimisticLock
	}

	updated := *current
	updated.Status = target
	updated.StateVersion++
	updated.LastError = lastError
	updated.UpdatedAt, err = time.Parse(time.RFC3339, now)
	if err != nil {
		return nil, fmt.Errorf("parse converge emergency time: %w", err)
	}
	if terminal {
		terminalAt := updated.UpdatedAt
		updated.TerminalAt = &terminalAt
		// The returned op must carry the authoritative projected effect for
		// its (now terminal) state; the pre-read projection is stale.
		updated.EffectStatus = store.ProjectEffectStatus(updated.OperationType, updated.Status, "", effectStatus)
	}

	if err := insertOperationEvent(ctx, tx, &store.OperationStateChangedEvent{
		ID: uuid.NewString(), OperationID: updated.ID, OperationType: updated.OperationType,
		DefinitionID: updated.ReleaseDefinitionID, OldStatus: current.Status, NewStatus: updated.Status,
		StateVersion: updated.StateVersion, CreatedAt: updated.UpdatedAt,
	}); err != nil {
		return nil, err
	}
	timelineData, err := json.Marshal(store.StateTransitionTimelineData{
		FromState: string(current.Status), ToState: string(updated.Status), ErrorCode: lastError,
	})
	if err != nil {
		return nil, fmt.Errorf("encode converge emergency timeline: %w", err)
	}
	if _, err := appendTimelineEntry(ctx, tx, &store.OperationTimelineEntry{
		OperationID: updated.ID, OperationStateVersion: updated.StateVersion,
		Kind: string(store.TimelineEntryStateTransition), Data: timelineData, CreatedAt: updated.UpdatedAt,
	}); err != nil {
		return nil, err
	}

	if terminal {
		if err := writeEmergencyIntentEffect(ctx, tx, updated.ID, effectStatus, beforeSnapshot, afterSnapshot, now); err != nil {
			return nil, err
		}
		if (updated.Status == store.StatusFailed || updated.Status == store.StatusTimeout) && lastError != "" {
			errorEntry, err := store.ErrorTimelineEntry(updated.ID, updated.StateVersion, lastError)
			if err != nil {
				return nil, err
			}
			errorEntry.CreatedAt = updated.UpdatedAt
			if _, err := appendTimelineEntry(ctx, tx, errorEntry); err != nil {
				return nil, err
			}
		}
	}
	return &updated, nil
}

// resolveUnknownEffectTx resolves a terminal op's UNKNOWN effect to
// APPLIED/NOT_APPLIED exactly once (REQ-032 §465 EMERGENCY_EFFECT_RESOLVED +
// stateVersion+1, REQ-087 AC-087-03). Caller holds the transaction and has
// verified state_version == command.ExpectedStateVersion.
func resolveUnknownEffectTx(
	ctx context.Context,
	tx *sql.Tx,
	current *store.Operation,
	intent *store.EmergencyIntent,
	command store.EmergencyConvergeCommand,
) (*store.Operation, *store.EmergencyIntent, error) {
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `
		UPDATE operations SET state_version = state_version + 1, updated_at = ?
		WHERE id = ? AND state_version = ?
	`, now.Format(time.RFC3339Nano), current.ID, command.ExpectedStateVersion)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve converge emergency operation effect: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve converge emergency operation rows: %w", err)
	}
	if rows == 0 {
		return nil, nil, store.ErrOptimisticLock
	}
	if err := writeEmergencyIntentEffect(ctx, tx, current.ID, command.EffectStatus, command.BeforeSnapshot, command.AfterSnapshot, now.Format(time.RFC3339Nano)); err != nil {
		return nil, nil, err
	}
	updatedOperation := *current
	updatedOperation.StateVersion++
	updatedOperation.UpdatedAt = now
	updatedOperation.EffectStatus = store.ProjectEffectStatus(updatedOperation.OperationType, updatedOperation.Status, "", command.EffectStatus)
	updatedIntent := *intent
	updatedIntent.EffectStatus = command.EffectStatus
	updatedIntent.BeforeSnapshot = append(json.RawMessage(nil), command.BeforeSnapshot...)
	updatedIntent.AfterSnapshot = append(json.RawMessage(nil), command.AfterSnapshot...)
	updatedIntent.UpdatedAt = now
	data, err := json.Marshal(store.EmergencyEffectTimelineData{
		RequestID: command.RequestID, EffectFrom: string(store.EmergencyEffectUnknown), EffectTo: string(command.EffectStatus),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("encode converge emergency effect timeline: %w", err)
	}
	if _, err := appendTimelineEntry(ctx, tx, &store.OperationTimelineEntry{
		OperationID: updatedOperation.ID, OperationStateVersion: updatedOperation.StateVersion,
		Kind: string(store.TimelineEntryEmergencyEffectResolved), Data: data, CreatedAt: now,
	}); err != nil {
		return nil, nil, err
	}
	return &updatedOperation, &updatedIntent, nil
}

// writeEmergencyIntentEffect writes the intent effect + snapshots for an
// operation inside the caller's transaction.
func writeEmergencyIntentEffect(ctx context.Context, execer operationExecer, operationID string, effectStatus store.EmergencyEffectStatus, beforeSnapshot, afterSnapshot json.RawMessage, now string) error {
	result, err := execer.ExecContext(ctx, `
		UPDATE emergency_intents
		SET before_snapshot = ?, after_snapshot = ?, effect_status = ?, updated_at = ?
		WHERE operation_id = ?
	`, nullableJSON(beforeSnapshot), nullableJSON(afterSnapshot), string(effectStatus), now, operationID)
	if err != nil {
		return fmt.Errorf("write converge emergency intent effect: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("converge emergency intent rows: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

// GetByID loads one emergency intent by primary key.
func (s *emergencyIntentStore) GetByID(ctx context.Context, id string) (*store.EmergencyIntent, error) {
	return scanEmergencyIntent(s.db.QueryRowContext(ctx, emergencyIntentSelect+` WHERE emergency_intents.id = ?`, id))
}

// ListStuckLocks derives stuck locks for the optional definition/customer
// scope (REQ-087 D4/D5, AC-087-07): a stuck lock is a terminal EMERGENCY
// intent whose effect is still UNKNOWN, was not explicitly released, and has
// been terminal longer than the observe window. The result is a derived
// projection — no stuck column is persisted.
func (s *emergencyIntentStore) ListStuckLocks(ctx context.Context, filter store.StuckLockFilter) ([]*store.StuckLock, error) {
	if filter.ObserveTimeout <= 0 {
		filter.ObserveTimeout = store.DefaultEmergencyEffectObserveTimeout
	}
	cutoff := time.Now().UTC().Add(-filter.ObserveTimeout)
	query := `
		SELECT ei.id, o.terminal_at
		FROM emergency_intents AS ei
		JOIN operations AS o ON o.id = ei.operation_id
		WHERE ei.effect_status = 'UNKNOWN'
		  AND ei.lock_released_at IS NULL
		  AND o.status IN ('succeeded','failed','cancelled','timeout')
		  AND o.terminal_at IS NOT NULL
		  AND o.terminal_at < ?`
	args := []any{cutoff.Format(time.RFC3339)}
	if filter.DefinitionID != "" {
		query += ` AND ei.release_definition_id = ?`
		args = append(args, filter.DefinitionID)
	}
	if len(filter.CustomerIDs) > 0 {
		//nolint:gosec // only generated placeholders are concatenated; customer IDs remain bound parameters
		query += ` AND ei.release_definition_id IN (
			SELECT id FROM release_definitions WHERE customer_id IN (` + placeholders(len(filter.CustomerIDs)) + `)
		)`
		for _, id := range filter.CustomerIDs {
			args = append(args, id)
		}
	}
	query += ` ORDER BY o.terminal_at ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list stuck emergency locks: %w", err)
	}
	defer rows.Close()
	type pair struct {
		id string
		at time.Time
	}
	pairs := make([]pair, 0)
	for rows.Next() {
		var id string
		var terminalAt string
		if err := rows.Scan(&id, &terminalAt); err != nil {
			return nil, fmt.Errorf("scan stuck emergency lock: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, terminalAt)
		if err != nil {
			return nil, fmt.Errorf("parse stuck emergency terminal_at: %w", err)
		}
		pairs = append(pairs, pair{id: id, at: parsed})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stuck emergency locks: %w", err)
	}
	locks := make([]*store.StuckLock, 0, len(pairs))
	for _, p := range pairs {
		intent, err := s.GetByID(ctx, p.id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue // deleted concurrently
			}
			return nil, fmt.Errorf("load stuck emergency lock %s: %w", p.id, err)
		}
		locks = append(locks, &store.StuckLock{
			Intent:          intent,
			TerminalAt:      p.at,
			LockPathSummary: emergencyLockPathSummary(intent),
		})
	}
	return locks, nil
}

// ReleaseLock releases one stuck emergency target lock (REQ-087 D4=A,
// AC-087-08/09). Both modes require a terminal operation whose effect is
// still UNKNOWN, an unreleased intent and a lock that has been stuck past the
// observe window; a pending_promotion ConvergenceTask bound to the operation
// blocks the release. NOT_APPLIED_PROVEN records NOT_APPLIED (an effect
// resolution: state_version+1 + EMERGENCY_EFFECT_RESOLVED) and releases;
// AUDITED_OVERRIDE only stamps lock_released_at and keeps the effect UNKNOWN
// so a late result can still resolve it (REQ-032 AC-032-31).
//
//nolint:gocyclo // ReleaseLock mirrors the full EMERGENCY state-machine/lock-release decision matrix (hop advance, terminal resolution, safety guards); branching is inherent.
func (s *emergencyIntentStore) ReleaseLock(ctx context.Context, command store.ReleaseLockCommand) (*store.ReleaseLockResult, error) {
	if command.IntentID == "" || command.Mode == "" || strings.TrimSpace(command.Reason) == "" {
		return nil, fmt.Errorf("release emergency lock: intent_id, mode and reason are required")
	}
	if command.Mode != store.EmergencyReleaseNotAppliedProven && command.Mode != store.EmergencyReleaseAuditedOverride {
		return nil, fmt.Errorf("release emergency lock: invalid mode %q", command.Mode)
	}
	if command.ObserveTimeout <= 0 {
		command.ObserveTimeout = store.DefaultEmergencyEffectObserveTimeout
	}

	var result *store.ReleaseLockResult
	err := retryBusy(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin release emergency lock: %w", err)
		}
		defer tx.Rollback() //nolint:errcheck // no-op after commit

		intent, err := getEmergencyIntentByID(ctx, tx, command.IntentID)
		if err != nil {
			return err
		}
		current, err := getOperation(ctx, tx, intent.OperationID)
		if err != nil {
			return err
		}
		if !current.Status.IsTerminal() {
			return store.ErrInvalidState
		}
		if intent.EffectStatus != store.EmergencyEffectUnknown {
			return store.ErrLockNotStuck
		}
		if intent.LockReleasedAt != nil {
			return store.ErrLockNotStuck
		}
		if current.TerminalAt == nil || time.Since(current.TerminalAt.UTC()) <= command.ObserveTimeout {
			return store.ErrLockNotStuck
		}
		// An unresolved promotion obligation must converge before release.
		task, taskErr := getConvergenceTaskByOperation(ctx, tx, intent.OperationID)
		if taskErr != nil && !errors.Is(taskErr, store.ErrNotFound) {
			return taskErr
		}
		if taskErr == nil && task.Status == "pending_promotion" {
			return store.ErrConvergencePending
		}

		now := time.Now().UTC()
		nowValue := now.Format(time.RFC3339Nano)
		if command.Mode == store.EmergencyReleaseNotAppliedProven {
			opResult, err := tx.ExecContext(ctx, `
				UPDATE operations SET state_version = state_version + 1, updated_at = ?
				WHERE id = ? AND state_version = ?
			`, nowValue, current.ID, current.StateVersion)
			if err != nil {
				return fmt.Errorf("release emergency lock operation effect: %w", err)
			}
			opRows, err := opResult.RowsAffected()
			if err != nil {
				return fmt.Errorf("release emergency lock operation rows: %w", err)
			}
			if opRows == 0 {
				return store.ErrOptimisticLock
			}
			data, err := json.Marshal(store.EmergencyEffectTimelineData{
				EffectFrom: string(store.EmergencyEffectUnknown), EffectTo: string(store.EmergencyEffectNotApplied),
			})
			if err != nil {
				return fmt.Errorf("encode release emergency lock timeline: %w", err)
			}
			if _, err := appendTimelineEntry(ctx, tx, &store.OperationTimelineEntry{
				OperationID: current.ID, OperationStateVersion: current.StateVersion + 1,
				Kind: string(store.TimelineEntryEmergencyEffectResolved), Data: data, CreatedAt: now,
			}); err != nil {
				return err
			}
		}
		effectResult, err := tx.ExecContext(ctx, `
			UPDATE emergency_intents
			SET effect_status = ?, lock_released_at = ?, updated_at = ?
			WHERE id = ?
		`, string(releaseEffectStatus(command.Mode)), nowValue, nowValue, command.IntentID)
		if err != nil {
			return fmt.Errorf("release emergency lock: %w", err)
		}
		effectRows, err := effectResult.RowsAffected()
		if err != nil {
			return fmt.Errorf("release emergency lock rows: %w", err)
		}
		if effectRows == 0 {
			return store.ErrLockNotStuck
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit release emergency lock: %w", err)
		}
		updatedIntent := *intent
		updatedIntent.EffectStatus = releaseEffectStatus(command.Mode)
		updatedIntent.LockReleasedAt = &now
		updatedIntent.UpdatedAt = now
		updatedOp := *current
		if command.Mode == store.EmergencyReleaseNotAppliedProven {
			updatedOp.StateVersion++
			updatedOp.EffectStatus = store.ProjectEffectStatus(updatedOp.OperationType, updatedOp.Status, "", store.EmergencyEffectNotApplied)
		}
		result = &store.ReleaseLockResult{Intent: &updatedIntent, Operation: &updatedOp}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func releaseEffectStatus(mode store.EmergencyReleaseMode) store.EmergencyEffectStatus {
	if mode == store.EmergencyReleaseNotAppliedProven {
		return store.EmergencyEffectNotApplied
	}
	return store.EmergencyEffectUnknown
}

func getEmergencyIntentByID(ctx context.Context, queryer operationQueryer, id string) (*store.EmergencyIntent, error) {
	return scanEmergencyIntent(queryer.QueryRowContext(ctx, emergencyIntentSelect+` WHERE emergency_intents.id = ?`, id))
}

// emergencyLockPathSummary builds the sanitized lock path summary displayed
// by ListStuckLocks (REQ-087 §4.2, e.g. "Deployment/api, container=app").
func emergencyLockPathSummary(intent *store.EmergencyIntent) string {
	summary := fmt.Sprintf("%s/%s", intent.WorkloadKind, intent.WorkloadName)
	switch {
	case intent.Container != nil && *intent.Container != "":
		summary += ", container=" + *intent.Container
	case intent.TargetReplicas != nil:
		summary += fmt.Sprintf(", replicas=%d", *intent.TargetReplicas)
	}
	return summary
}

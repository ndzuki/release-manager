package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ndzuki/release-manager/internal/store"
)

type operationStore struct{ gorm *DB }

func (s *operationStore) Create(ctx context.Context, op *store.Operation) error {
	return createOperation(ctx, s.gorm, op)
}

func (s *operationStore) CreateIfAvailable(ctx context.Context, op *store.Operation) error {
	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create operation: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback is a no-op after successful Commit

	query := `
		SELECT COUNT(*) FROM operations
		WHERE release_definition_id = ?
		  AND status NOT IN ('succeeded','failed','cancelled','timeout')
	`
	if op.OperationType == store.OperationEmergency {
		query += " AND operation_type != 'EMERGENCY'"
	}

	var count int
	if err := tx.QueryRowContext(ctx, query, op.ReleaseDefinitionID).Scan(&count); err != nil {
		return fmt.Errorf("count conflicting operations: %w", err)
	}
	if count > 0 {
		return store.ErrReleaseBusy
	}

	if err := createOperation(ctx, tx, op); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create operation: %w", err)
	}
	return nil
}

type operationExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func createOperation(ctx context.Context, execer operationExecer, op *store.Operation) error {
	actorJSON, err := json.Marshal(op.Actor)
	if err != nil {
		return fmt.Errorf("marshal actor: %w", err)
	}

	if op.CreatedAt.IsZero() {
		op.CreatedAt = time.Now().UTC()
	}
	if op.UpdatedAt.IsZero() {
		op.UpdatedAt = op.CreatedAt
	}
	if op.StateVersion == 0 {
		op.StateVersion = 1
	}

	var deadline *string
	if op.Deadline != nil {
		d := op.Deadline.UTC().Format(time.RFC3339)
		deadline = &d
	}
	var terminalAt *string
	if op.TerminalAt != nil {
		t := op.TerminalAt.UTC().Format(time.RFC3339)
		terminalAt = &t
	}

	_, err = execer.ExecContext(ctx, `
		INSERT INTO operations (
			id, operation_type, status, release_definition_id,
			idempotency_key, request_hash, state_version,
			bundle_id, values_revision_id, expected_revision, target_revision, values_patch,
			actor, created_at, updated_at, terminal_at, deadline, last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		op.ID, string(op.OperationType), string(op.Status), op.ReleaseDefinitionID,
		op.IdempotencyKey, op.RequestHash, op.StateVersion,
		op.BundleID, op.ValuesRevisionID, op.ExpectedRevision, op.TargetRevision, op.ValuesPatch,
		string(actorJSON), op.CreatedAt.UTC().Format(time.RFC3339), op.UpdatedAt.UTC().Format(time.RFC3339),
		terminalAt, deadline, op.LastError,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return store.ErrDuplicateKey
		}
		return fmt.Errorf("insert operation: %w", err)
	}
	return nil
}

func (s *operationStore) Get(ctx context.Context, id string) (*store.Operation, error) {
	row := s.gorm.QueryRowContext(ctx, `
		SELECT id, operation_type, status, release_definition_id,
			idempotency_key, request_hash, state_version,
			bundle_id, values_revision_id, expected_revision, target_revision, values_patch,
			actor, created_at, updated_at, terminal_at, deadline, last_error
		FROM operations WHERE id = ?
	`, id)
	return scanOperation(row)
}

func (s *operationStore) GetByIdempotencyKey(ctx context.Context, key string) (*store.Operation, error) {
	row := s.gorm.QueryRowContext(ctx, `
		SELECT id, operation_type, status, release_definition_id,
			idempotency_key, request_hash, state_version,
			bundle_id, values_revision_id, expected_revision, target_revision, values_patch,
			actor, created_at, updated_at, terminal_at, deadline, last_error
		FROM operations WHERE idempotency_key = ?
	`, key)
	return scanOperation(row)
}

func (s *operationStore) GetCancelReplay(
	ctx context.Context,
	query store.OperationCancelReplayQuery,
) (*store.OperationCancelResult, error) {
	if query.IdempotencyKeyHash == "" {
		return nil, store.ErrNotFound
	}
	scope := operationCancelIdempotencyScope(query.OperationID, query.ActorUserID)
	var requestHash string
	var responseRef []byte
	err := s.gorm.QueryRowContext(ctx, `
		SELECT request_hash, response_ref FROM idempotency_records
		WHERE scope = ? AND text_key = ? AND expires_at > ?
	`, scope, query.IdempotencyKeyHash, time.Now().UTC()).Scan(
		&requestHash,
		&responseRef,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup operation cancel replay: %w", err)
	}
	if requestHash != query.RequestHash {
		return nil, store.ErrIdempotencyConflict
	}
	var result store.OperationCancelResult
	if err := json.Unmarshal(responseRef, &result); err != nil {
		return nil, fmt.Errorf("decode operation cancel replay: %w", err)
	}
	result.Replayed = true
	return &result, nil
}

func operationCancelIdempotencyScope(operationID, actorUserID string) string {
	return operationID + ":" + actorUserID
}

const operationCancelIdempotencyTTL = 24 * time.Hour

//nolint:gocyclo // Cancel transaction keeps authorization-independent CAS, event, lifecycle, and idempotency writes atomic.
func (s *operationStore) Cancel(ctx context.Context, command store.OperationCancelCommand) (*store.OperationCancelResult, error) {
	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin cancel operation: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback is a no-op after successful Commit.

	replay, err := lookupOperationCancelIdempotency(ctx, tx, command)
	if err != nil {
		return nil, err
	}
	if replay != nil {
		replay.Replayed = true
		return replay, nil
	}

	current, err := getOperation(ctx, tx, command.OperationID)
	if err != nil {
		return nil, err
	}
	if current.StateVersion != command.ExpectedStateVersion {
		return nil, &store.OperationStateVersionConflictError{
			Expected: command.ExpectedStateVersion,
			Current:  current.StateVersion,
		}
	}
	if current.Status.IsTerminal() {
		return nil, store.ErrInvalidState
	}
	if !current.Status.CanTransitionTo(command.TargetStatus) {
		return nil, store.ErrInvalidState
	}
	if current.OperationType == store.OperationEmergency && current.Status == store.StatusRunning {
		return nil, store.ErrInvalidState
	}

	now := nowUTC()
	terminal := command.TargetStatus.IsTerminal()
	result, err := tx.ExecContext(ctx, `
		UPDATE operations
		SET status = ?, state_version = state_version + 1, last_error = ?, updated_at = ?,
		    terminal_at = CASE WHEN ? THEN ? ELSE terminal_at END
		WHERE id = ? AND state_version = ?
	`, string(command.TargetStatus), command.Reason, now, terminal, now, command.OperationID, command.ExpectedStateVersion)
	if err != nil {
		return nil, fmt.Errorf("cancel operation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("cancel rows affected: %w", err)
	}
	if rows == 0 {
		return nil, store.ErrOptimisticLock
	}

	updated := *current
	updated.Status = command.TargetStatus
	updated.StateVersion++
	updated.LastError = command.Reason
	updated.UpdatedAt, err = time.Parse(time.RFC3339, now)
	if err != nil {
		return nil, fmt.Errorf("parse cancel time: %w", err)
	}
	if terminal {
		terminalAt := updated.UpdatedAt
		updated.TerminalAt = &terminalAt
	}

	event := &store.OperationStateChangedEvent{
		ID:            uuid.NewString(),
		OperationID:   updated.ID,
		OperationType: updated.OperationType,
		DefinitionID:  updated.ReleaseDefinitionID,
		OldStatus:     current.Status,
		NewStatus:     updated.Status,
		StateVersion:  updated.StateVersion,
		CreatedAt:     updated.UpdatedAt,
	}
	if err := insertOperationEvent(ctx, tx, event); err != nil {
		return nil, err
	}
	if current.OperationType == store.OperationEmergency && terminal {
		effectStatus := store.EmergencyEffectUnknown
		if command.DeliveryStatus == store.DeliveryUndelivered {
			effectStatus = store.EmergencyEffectNotApplied
		}
		effectResult, err := tx.ExecContext(ctx, `
			UPDATE emergency_intents SET effect_status = ?, updated_at = ? WHERE operation_id = ?
		`, string(effectStatus), now, command.OperationID)
		if err != nil {
			return nil, fmt.Errorf("set cancelled emergency effect: %w", err)
		}
		effectRows, rowsErr := effectResult.RowsAffected()
		if rowsErr != nil {
			return nil, fmt.Errorf("cancelled emergency effect rows: %w", rowsErr)
		}
		if effectRows != 1 {
			return nil, store.ErrNotFound
		}
	}
	timelineData, err := json.Marshal(store.StateTransitionTimelineData{
		RequestID: command.RequestID, FromState: string(current.Status), ToState: string(updated.Status),
	})
	if err != nil {
		return nil, fmt.Errorf("encode cancel timeline: %w", err)
	}
	if _, err := appendTimelineEntry(ctx, tx, &store.OperationTimelineEntry{
		OperationID: updated.ID, OperationStateVersion: updated.StateVersion,
		Kind: string(store.TimelineEntryStateTransition), Data: timelineData, CreatedAt: updated.UpdatedAt,
	}); err != nil {
		return nil, err
	}
	if terminal {
		if _, err := tx.ExecContext(ctx, `
			UPDATE preflight_lifecycles
			SET operation_terminal_at = ?
			WHERE operation_id = ? AND operation_terminal_at IS NULL
		`, now, command.OperationID); err != nil {
			return nil, fmt.Errorf("set preflight operation terminal: %w", err)
		}
	}

	cancelResult := &store.OperationCancelResult{Operation: &updated, RequestID: command.RequestID}
	if err := insertOperationCancelIdempotency(ctx, tx, command, cancelResult, time.Now().UTC().Add(operationCancelIdempotencyTTL)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit cancel operation: %w", err)
	}
	return cancelResult, nil
}

//nolint:dupl // Operation cancellation mirrors the shared transactional idempotency record protocol.
func lookupOperationCancelIdempotency(
	ctx context.Context,
	tx *Tx,
	command store.OperationCancelCommand,
) (*store.OperationCancelResult, error) {
	if command.IdempotencyKeyHash == "" {
		return nil, nil
	}
	var requestHash string
	var responseRef []byte
	err := tx.QueryRowContext(ctx, `
		SELECT request_hash, response_ref FROM idempotency_records
		WHERE scope = ? AND text_key = ? AND expires_at > ?
	`, command.IdempotencyScope, command.IdempotencyKeyHash, time.Now().UTC()).Scan(
		&requestHash,
		&responseRef,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup operation cancel idempotency: %w", err)
	}
	if requestHash != command.RequestHash {
		return nil, store.ErrIdempotencyConflict
	}
	var result store.OperationCancelResult
	if err := json.Unmarshal(responseRef, &result); err != nil {
		return nil, fmt.Errorf("decode operation cancel replay: %w", err)
	}
	return &result, nil
}

//nolint:dupl // Operation cancellation mirrors the shared transactional idempotency record protocol.
func insertOperationCancelIdempotency(
	ctx context.Context,
	tx *Tx,
	command store.OperationCancelCommand,
	result *store.OperationCancelResult,
	expiresAt time.Time,
) error {
	if command.IdempotencyKeyHash == "" {
		return nil
	}
	responseRef, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode operation cancel response: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO idempotency_records (scope, text_key, request_hash, response_ref, expires_at)
		VALUES (?, ?, ?, ?, ?)
	`, command.IdempotencyScope, command.IdempotencyKeyHash, command.RequestHash, responseRef, expiresAt.UTC())
	if err != nil {
		if isUniqueConstraint(err) {
			return store.ErrIdempotencyConflict
		}
		return fmt.Errorf("insert operation cancel idempotency: %w", err)
	}
	return nil
}

func (s *operationStore) UpdateStatus(ctx context.Context, id string, status store.OperationStatus, stateVersion int, lastError string) (*store.Operation, error) {
	return s.transition(ctx, id, status, stateVersion, lastError)
}

func (s *operationStore) Transition(
	ctx context.Context,
	id string,
	status store.OperationStatus,
	stateVersion int,
	lastError string,
) (*store.Operation, error) {
	return s.transition(ctx, id, status, stateVersion, lastError)
}

func (s *operationStore) transition(
	ctx context.Context,
	id string,
	status store.OperationStatus,
	stateVersion int,
	lastError string,
) (*store.Operation, error) {
	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin operation transition: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback is a no-op after successful Commit.

	current, err := getOperation(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if current.StateVersion != stateVersion {
		return nil, store.ErrOptimisticLock
	}
	if !current.Status.CanTransitionTo(status) {
		return nil, store.ErrInvalidState
	}

	now := nowUTC()
	result, err := tx.ExecContext(ctx, `
		UPDATE operations
		SET status = ?, state_version = state_version + 1, last_error = ?, updated_at = ?,
		    terminal_at = CASE WHEN ? THEN ? ELSE terminal_at END
		WHERE id = ? AND state_version = ?
	`, string(status), lastError, now, status.IsTerminal(), now, id, stateVersion)
	if err != nil {
		return nil, fmt.Errorf("update operation status: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return nil, store.ErrOptimisticLock
	}

	updated := *current
	updated.Status = status
	updated.StateVersion++
	updated.LastError = lastError
	updated.UpdatedAt, err = time.Parse(time.RFC3339, now)
	if err != nil {
		return nil, fmt.Errorf("parse transition time: %w", err)
	}
	if status.IsTerminal() {
		terminalAt := updated.UpdatedAt
		updated.TerminalAt = &terminalAt
	}

	ev := &store.OperationStateChangedEvent{
		ID:            uuid.New().String(),
		OperationID:   updated.ID,
		OperationType: updated.OperationType,
		DefinitionID:  updated.ReleaseDefinitionID,
		OldStatus:     current.Status,
		NewStatus:     updated.Status,
		StateVersion:  updated.StateVersion,
		CreatedAt:     updated.UpdatedAt,
	}
	if err := insertOperationEvent(ctx, tx, ev); err != nil {
		return nil, err
	}

	if status.IsTerminal() {
		if _, err := tx.ExecContext(ctx, `
			UPDATE preflight_lifecycles
			SET operation_terminal_at = ?
			WHERE operation_id = ? AND operation_terminal_at IS NULL
		`, now, id); err != nil {
			return nil, fmt.Errorf("set preflight operation terminal: %w", err)
		}
	}
	timelineData, err := json.Marshal(store.StateTransitionTimelineData{
		FromState: string(current.Status), ToState: string(updated.Status), ErrorCode: lastError,
	})
	if err != nil {
		return nil, fmt.Errorf("encode operation transition timeline: %w", err)
	}
	if _, err := appendTimelineEntry(ctx, tx, &store.OperationTimelineEntry{
		OperationID: updated.ID, OperationStateVersion: updated.StateVersion,
		Kind: string(store.TimelineEntryStateTransition), Data: timelineData, CreatedAt: updated.UpdatedAt,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit operation transition: %w", err)
	}
	return &updated, nil
}

func (s *operationStore) HasActiveForDefinition(ctx context.Context, definitionID string) (bool, error) {
	row := s.gorm.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM operations
		WHERE release_definition_id = ?
		  AND operation_type != 'EMERGENCY'
		  AND status NOT IN ('succeeded','failed','cancelled','timeout')
	`, definitionID)
	var count int
	if err := row.Scan(&count); err != nil {
		return false, fmt.Errorf("count active operations: %w", err)
	}
	return count > 0, nil
}

// HasActiveEmergencyForDefinition returns true if there is an active EMERGENCY operation
// for the given definition. Used for AC-032-06 conflict detection.
func (s *operationStore) HasActiveEmergencyForDefinition(ctx context.Context, definitionID string) (bool, error) {
	row := s.gorm.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM operations
		WHERE release_definition_id = ?
		  AND operation_type = 'EMERGENCY'
		  AND status NOT IN ('succeeded','failed','cancelled','timeout')
	`, definitionID)
	var count int
	if err := row.Scan(&count); err != nil {
		return false, fmt.Errorf("count active emergency operations: %w", err)
	}
	return count > 0, nil
}

func (s *operationStore) List(ctx context.Context, definitionID string) ([]*store.Operation, error) {
	rows, err := s.gorm.QueryContext(ctx, `
		SELECT id, operation_type, status, release_definition_id,
			idempotency_key, request_hash, state_version,
			bundle_id, values_revision_id, expected_revision, target_revision, values_patch,
			actor, created_at, updated_at, terminal_at, deadline, last_error
		FROM operations
		WHERE release_definition_id = ?
		ORDER BY created_at DESC
	`, definitionID)
	if err != nil {
		return nil, fmt.Errorf("list operations: %w", err)
	}
	defer rows.Close()

	var ops []*store.Operation
	for rows.Next() {
		op, err := scanOperationFromRows(rows)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

// ListNonTerminal returns all operations that are not in a terminal state.
// Used for recovery on service restart (REQ-023 AC-023-05).
func (s *operationStore) ListNonTerminal(ctx context.Context) ([]*store.Operation, error) {
	rows, err := s.gorm.QueryContext(ctx, `
		SELECT id, operation_type, status, release_definition_id,
			idempotency_key, request_hash, state_version,
			bundle_id, values_revision_id, expected_revision, target_revision, values_patch,
			actor, created_at, updated_at, terminal_at, deadline, last_error
		FROM operations
		WHERE status NOT IN ('succeeded','failed','cancelled','timeout')
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list non-terminal operations: %w", err)
	}
	defer rows.Close()

	var ops []*store.Operation
	for rows.Next() {
		op, err := scanOperationFromRows(rows)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

type operationQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getOperation(ctx context.Context, queryer operationQueryer, id string) (*store.Operation, error) {
	row := queryer.QueryRowContext(ctx, `
		SELECT id, operation_type, status, release_definition_id,
			idempotency_key, request_hash, state_version,
			bundle_id, values_revision_id, expected_revision, target_revision, values_patch,
			actor, created_at, updated_at, terminal_at, deadline, last_error
		FROM operations WHERE id = ?
	`, id)
	return scanOperation(row)
}
func scanOperation(row interface{ Scan(...interface{}) error }) (*store.Operation, error) {
	var (
		id, opType, status, defID, idemKey, reqHash string
		stateVer, expectedRev, targetRev            int
		bundleID, valuesRevID                       string
		valuesPatch                                 []byte
		actorJSON                                   string
		createdAt, updatedAt                        string
		terminalAt, deadline                        *string
		lastError                                   string
	)

	err := row.Scan(
		&id, &opType, &status, &defID,
		&idemKey, &reqHash, &stateVer,
		&bundleID, &valuesRevID, &expectedRev, &targetRev, &valuesPatch,
		&actorJSON, &createdAt, &updatedAt, &terminalAt, &deadline, &lastError,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan operation: %w", err)
	}

	return buildOperation(id, opType, status, defID, idemKey, reqHash,
		stateVer, bundleID, valuesRevID, expectedRev, targetRev, valuesPatch,
		actorJSON, createdAt, updatedAt, terminalAt, deadline, lastError)
}

func scanOperationFromRows(rows *sql.Rows) (*store.Operation, error) {
	var (
		id, opType, status, defID, idemKey, reqHash string
		stateVer, expectedRev, targetRev            int
		bundleID, valuesRevID                       string
		valuesPatch                                 []byte
		actorJSON                                   string
		createdAt, updatedAt                        string
		terminalAt, deadline                        *string
		lastError                                   string
	)

	err := rows.Scan(
		&id, &opType, &status, &defID,
		&idemKey, &reqHash, &stateVer,
		&bundleID, &valuesRevID, &expectedRev, &targetRev, &valuesPatch,
		&actorJSON, &createdAt, &updatedAt, &terminalAt, &deadline, &lastError,
	)
	if err != nil {
		return nil, fmt.Errorf("scan operation row: %w", err)
	}

	return buildOperation(id, opType, status, defID, idemKey, reqHash,
		stateVer, bundleID, valuesRevID, expectedRev, targetRev, valuesPatch,
		actorJSON, createdAt, updatedAt, terminalAt, deadline, lastError)
}

func buildOperation(id, opType, status, defID, idemKey, reqHash string,
	stateVer int, bundleID, valuesRevID string, expectedRev, targetRev int,
	valuesPatch []byte, actorJSON, createdAt, updatedAt string,
	terminalAt, deadline *string, lastError string,
) (*store.Operation, error) {
	var actor store.ActorContext
	if err := json.Unmarshal([]byte(actorJSON), &actor); err != nil {
		return nil, fmt.Errorf("unmarshal actor: %w", err)
	}

	ct, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	ut, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}

	var terminal *time.Time
	if terminalAt != nil && *terminalAt != "" {
		t, err := time.Parse(time.RFC3339, *terminalAt)
		if err != nil {
			return nil, fmt.Errorf("parse terminal_at: %w", err)
		}
		terminal = &t
	}

	var dl *time.Time
	if deadline != nil && *deadline != "" {
		t, err := time.Parse(time.RFC3339, *deadline)
		if err != nil {
			return nil, fmt.Errorf("parse deadline: %w", err)
		}
		dl = &t
	}

	return &store.Operation{
		ID:                  id,
		OperationType:       store.OperationType(opType),
		Status:              store.OperationStatus(status),
		ReleaseDefinitionID: defID,
		IdempotencyKey:      idemKey,
		RequestHash:         reqHash,
		StateVersion:        stateVer,
		BundleID:            bundleID,
		ValuesRevisionID:    valuesRevID,
		ExpectedRevision:    expectedRev,
		TargetRevision:      targetRev,
		ValuesPatch:         valuesPatch,
		Actor:               actor,
		CreatedAt:           ct,
		UpdatedAt:           ut,
		Deadline:            dl,
		TerminalAt:          terminal,
		LastError:           lastError,
	}, nil
}

package sqlite

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

const emergencyIdempotencyTTL = 24 * time.Hour

type emergencyIntentStore struct{ db *sql.DB }

type emergencyReplayRef struct {
	OperationID       string `json:"operation_id"`
	IntentID          string `json:"intent_id"`
	ConvergenceTaskID string `json:"convergence_task_id,omitempty"`
}

func (s *emergencyIntentStore) GetReplay(ctx context.Context, scope, keyHash, requestHash string) (*store.EmergencyCreateResult, error) {
	command := store.EmergencyCreateCommand{
		IdempotencyScope: scope, IdempotencyKeyHash: keyHash, RequestHash: requestHash,
	}
	result, err := lookupEmergencyReplay(ctx, s.db, command)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, store.ErrNotFound
	}
	return result, nil
}

func (s *emergencyIntentStore) CreateIfAvailable(ctx context.Context, command store.EmergencyCreateCommand) (*store.EmergencyCreateResult, error) {
	if command.Operation == nil || command.Intent == nil {
		return nil, fmt.Errorf("emergency create requires operation and intent")
	}
	var result *store.EmergencyCreateResult
	err := retryBusy(ctx, func() error {
		created, err := s.createIfAvailable(ctx, command)
		if err != nil {
			return err
		}
		result = created
		return nil
	})
	return result, err
}

//nolint:gocyclo // Emergency create orchestrates idempotency, conflict check, operation, intent, convergence, and idempotency-record writes atomically.
func (s *emergencyIntentStore) createIfAvailable(ctx context.Context, command store.EmergencyCreateCommand) (*store.EmergencyCreateResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create emergency: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback is a no-op after successful Commit.

	if err := checkAuthorizationFence(ctx, tx, command.ExpectedAuthorizationVersion); err != nil {
		return nil, err
	}
	replayed, err := lookupEmergencyReplay(ctx, tx, command)
	if err != nil || replayed != nil {
		return replayed, err
	}

	var standardCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM operations
		WHERE release_definition_id = ?
		  AND operation_type != 'EMERGENCY'
		  AND status NOT IN ('succeeded','failed','cancelled','timeout')
	`, command.Operation.ReleaseDefinitionID).Scan(&standardCount); err != nil {
		return nil, fmt.Errorf("count standard operation conflicts: %w", err)
	}
	if standardCount > 0 {
		return nil, store.ErrReleaseBusy
	}

	active, err := listActiveEmergencyIntents(ctx, tx, command.Operation.ReleaseDefinitionID)
	if err != nil {
		return nil, err
	}
	for _, existing := range active {
		if emergencyIntentsConflict(existing, command.Intent) {
			return nil, store.ErrEmergencyConflict
		}
	}

	if err := createOperation(ctx, tx, command.Operation); err != nil {
		return nil, err
	}
	if err := insertEmergencyIntent(ctx, tx, command.Intent); err != nil {
		return nil, err
	}
	if command.ConvergenceTask != nil {
		if err := insertConvergenceTask(ctx, tx, command.ConvergenceTask); err != nil {
			return nil, err
		}
	}

	reference := emergencyReplayRef{
		OperationID: command.Operation.ID,
		IntentID:    command.Intent.ID,
	}
	if command.ConvergenceTask != nil {
		reference.ConvergenceTaskID = command.ConvergenceTask.ID
	}
	responseRef, err := json.Marshal(reference)
	if err != nil {
		return nil, fmt.Errorf("marshal emergency replay reference: %w", err)
	}
	expiresAt := command.IdempotencyExpiresAt
	if expiresAt.IsZero() {
		expiresAt = time.Now().UTC().Add(emergencyIdempotencyTTL)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO idempotency_records (scope, text_key, request_hash, response_ref, expires_at)
		VALUES (?, ?, ?, ?, ?)
	`, command.IdempotencyScope, command.IdempotencyKeyHash, command.RequestHash, responseRef, expiresAt.UTC().Format(time.RFC3339Nano)); err != nil {
		if isUniqueConstraint(err) {
			return nil, store.ErrIdempotencyConflict
		}
		return nil, fmt.Errorf("insert emergency idempotency record: %w", err)
	}

	if err := checkAuthorizationFence(ctx, tx, command.ExpectedAuthorizationVersion); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create emergency: %w", err)
	}
	return &store.EmergencyCreateResult{
		Operation:       command.Operation,
		Intent:          command.Intent,
		ConvergenceTask: command.ConvergenceTask,
	}, nil
}

func checkAuthorizationFence(ctx context.Context, execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, expected uint64) error {
	if expected == 0 {
		return nil
	}
	result, err := execer.ExecContext(ctx,
		`UPDATE authorization_source_version SET version = version WHERE id = 1 AND version = ?`, expected)
	if err != nil {
		return fmt.Errorf("check authorization source version: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check authorization source version rows affected: %w", err)
	}
	if rows != 1 {
		return store.ErrAuthorizationStale
	}
	return nil
}

func lookupEmergencyReplay(ctx context.Context, queryer operationQueryer, command store.EmergencyCreateCommand) (*store.EmergencyCreateResult, error) {
	var requestHash string
	var responseRef []byte
	err := queryer.QueryRowContext(ctx, `
		SELECT request_hash, response_ref FROM idempotency_records
		WHERE scope = ? AND text_key = ? AND expires_at > ?
	`, command.IdempotencyScope, command.IdempotencyKeyHash, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&requestHash, &responseRef)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup emergency idempotency: %w", err)
	}
	if requestHash != command.RequestHash {
		return nil, store.ErrIdempotencyConflict
	}
	var reference emergencyReplayRef
	if err := json.Unmarshal(responseRef, &reference); err != nil {
		return nil, fmt.Errorf("decode emergency replay reference: %w", err)
	}
	op, err := getOperation(ctx, queryer, reference.OperationID)
	if err != nil {
		return nil, fmt.Errorf("load replay operation: %w", err)
	}
	intent, err := getEmergencyIntentByOperation(ctx, queryer, reference.OperationID)
	if err != nil {
		return nil, fmt.Errorf("load replay emergency intent: %w", err)
	}
	var task *store.ConvergenceTask
	if reference.ConvergenceTaskID != "" {
		task, err = getConvergenceTaskByOperation(ctx, queryer, reference.OperationID)
		if err != nil {
			return nil, fmt.Errorf("load replay convergence task: %w", err)
		}
	}
	return &store.EmergencyCreateResult{Operation: op, Intent: intent, ConvergenceTask: task, Replayed: true}, nil
}

func insertEmergencyIntent(ctx context.Context, execer operationExecer, intent *store.EmergencyIntent) error {
	if intent.CreatedAt.IsZero() {
		intent.CreatedAt = time.Now().UTC()
	}
	if intent.UpdatedAt.IsZero() {
		intent.UpdatedAt = intent.CreatedAt
	}
	if intent.DeliveryStatus == "" {
		intent.DeliveryStatus = "pending"
	}
	var lastDeliveryAt *string
	if intent.LastDeliveryAt != nil {
		value := intent.LastDeliveryAt.UTC().Format(time.RFC3339Nano)
		lastDeliveryAt = &value
	}
	_, err := execer.ExecContext(ctx, `
		INSERT INTO emergency_intents (
			id, release_definition_id, operation_id, command_id, action,
			workload_kind, workload_name, workload_namespace, workload_uid,
			container, artifact_id, image_reference, target_replicas,
			annotation_scope, annotation_entries, convergence, promotion_paths,
			before_snapshot, after_snapshot, delivery_status, last_delivery_at,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, intent.ID, intent.ReleaseDefinitionID, intent.OperationID, intent.CommandID, string(intent.Action),
		intent.WorkloadKind, intent.WorkloadName, intent.WorkloadNamespace, intent.WorkloadUID,
		intent.Container, intent.ArtifactID, intent.ImageReference, intent.TargetReplicas,
		intent.AnnotationScope, nullableJSON(intent.AnnotationEntries), string(intent.Convergence), nullableJSON(intent.PromotionPaths),
		nullableJSON(intent.BeforeSnapshot), nullableJSON(intent.AfterSnapshot), intent.DeliveryStatus, lastDeliveryAt,
		intent.CreatedAt.UTC().Format(time.RFC3339Nano), intent.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		if isUniqueConstraint(err) {
			return store.ErrDuplicateKey
		}
		return fmt.Errorf("insert emergency intent: %w", err)
	}
	return nil
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return []byte(value)
}

func (s *emergencyIntentStore) GetByOperationID(ctx context.Context, operationID string) (*store.EmergencyIntent, error) {
	return getEmergencyIntentByOperation(ctx, s.db, operationID)
}

func getEmergencyIntentByOperation(ctx context.Context, queryer operationQueryer, operationID string) (*store.EmergencyIntent, error) {
	return scanEmergencyIntent(queryer.QueryRowContext(ctx, emergencyIntentSelect+` WHERE operation_id = ?`, operationID))
}

func (s *emergencyIntentStore) GetByCommandID(ctx context.Context, commandID string) (*store.EmergencyIntent, error) {
	return scanEmergencyIntent(s.db.QueryRowContext(ctx, emergencyIntentSelect+` WHERE command_id = ?`, commandID))
}

func (s *emergencyIntentStore) GetActiveLocksForDefinition(ctx context.Context, definitionID string) ([]*store.EmergencyIntent, error) {
	return listActiveEmergencyIntents(ctx, s.db, definitionID)
}

func listActiveEmergencyIntents(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, definitionID string) ([]*store.EmergencyIntent, error) {
	rows, err := queryer.QueryContext(ctx, emergencyIntentSelect+`
		JOIN operations ON operations.id = emergency_intents.operation_id
		WHERE emergency_intents.release_definition_id = ?
		  AND operations.status NOT IN ('succeeded','failed','cancelled','timeout')
		ORDER BY emergency_intents.created_at ASC
	`, definitionID)
	if err != nil {
		return nil, fmt.Errorf("list active emergency intents: %w", err)
	}
	defer rows.Close()
	return scanEmergencyIntentRows(rows)
}

func (s *emergencyIntentStore) ListPendingDeliveryByDefinition(ctx context.Context, definitionID string) ([]*store.EmergencyIntent, error) {
	rows, err := s.db.QueryContext(ctx, emergencyIntentSelect+`
		WHERE release_definition_id = ? AND delivery_status != 'persisted'
		ORDER BY created_at ASC
	`, definitionID)
	if err != nil {
		return nil, fmt.Errorf("list pending emergency delivery: %w", err)
	}
	defer rows.Close()
	return scanEmergencyIntentRows(rows)
}

func (s *emergencyIntentStore) UpdateDeliveryStatus(ctx context.Context, id, status string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE emergency_intents
		SET delivery_status = ?, last_delivery_at = ?, updated_at = ?
		WHERE id = ?
	`, status, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("update emergency delivery status: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("emergency delivery rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *emergencyIntentStore) Finish(
	ctx context.Context,
	intentID, operationID string,
	expectedStateVersion int,
	status store.OperationStatus,
	lastError string,
	beforeSnapshot, afterSnapshot json.RawMessage,
) (*store.Operation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin finish emergency: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback is a no-op after successful Commit.

	current, err := getOperation(ctx, tx, operationID)
	if err != nil {
		return nil, err
	}
	if current.StateVersion != expectedStateVersion {
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
	`, string(status), lastError, now, status.IsTerminal(), now, operationID, expectedStateVersion)
	if err != nil {
		return nil, fmt.Errorf("finish emergency operation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("finish emergency rows affected: %w", err)
	}
	if rows == 0 {
		return nil, store.ErrOptimisticLock
	}

	result, err = tx.ExecContext(ctx, `
		UPDATE emergency_intents
		SET before_snapshot = ?, after_snapshot = ?, updated_at = ?
		WHERE id = ? AND operation_id = ?
	`, nullableJSON(beforeSnapshot), nullableJSON(afterSnapshot), now, intentID, operationID)
	if err != nil {
		return nil, fmt.Errorf("finish emergency snapshots: %w", err)
	}
	rows, err = result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("finish emergency snapshot rows affected: %w", err)
	}
	if rows == 0 {
		return nil, store.ErrNotFound
	}

	updated := *current
	updated.Status = status
	updated.StateVersion++
	updated.LastError = lastError
	updated.UpdatedAt, err = time.Parse(time.RFC3339, now)
	if err != nil {
		return nil, fmt.Errorf("parse emergency finish time: %w", err)
	}
	if status.IsTerminal() {
		terminalAt := updated.UpdatedAt
		updated.TerminalAt = &terminalAt
	}
	event := &store.OperationStateChangedEvent{
		ID: uuid.NewString(), OperationID: updated.ID, OperationType: updated.OperationType,
		DefinitionID: updated.ReleaseDefinitionID, OldStatus: current.Status, NewStatus: updated.Status,
		StateVersion: updated.StateVersion, CreatedAt: updated.UpdatedAt,
	}
	if err := insertOperationEvent(ctx, tx, event); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit finish emergency: %w", err)
	}
	return &updated, nil
}

const emergencyIntentSelect = `
	SELECT emergency_intents.id, emergency_intents.release_definition_id,
		emergency_intents.operation_id, emergency_intents.command_id, emergency_intents.action,
		emergency_intents.workload_kind, emergency_intents.workload_name,
		emergency_intents.workload_namespace, emergency_intents.workload_uid,
		emergency_intents.container, emergency_intents.artifact_id, emergency_intents.image_reference,
		emergency_intents.target_replicas, emergency_intents.annotation_scope,
		emergency_intents.annotation_entries, emergency_intents.convergence,
		emergency_intents.promotion_paths, emergency_intents.before_snapshot,
		emergency_intents.after_snapshot, emergency_intents.delivery_status,
		emergency_intents.last_delivery_at, emergency_intents.created_at, emergency_intents.updated_at
	FROM emergency_intents`

func scanEmergencyIntent(row interface{ Scan(...any) error }) (*store.EmergencyIntent, error) {
	var intent store.EmergencyIntent
	var action, convergence string
	var container, artifactID, imageReference, annotationScope, lastDeliveryAt sql.NullString
	var targetReplicas sql.NullInt64
	var annotationEntries, promotionPaths, beforeSnapshot, afterSnapshot []byte
	var createdAt, updatedAt string
	if err := row.Scan(
		&intent.ID, &intent.ReleaseDefinitionID, &intent.OperationID, &intent.CommandID, &action,
		&intent.WorkloadKind, &intent.WorkloadName, &intent.WorkloadNamespace, &intent.WorkloadUID,
		&container, &artifactID, &imageReference, &targetReplicas, &annotationScope,
		&annotationEntries, &convergence, &promotionPaths, &beforeSnapshot, &afterSnapshot,
		&intent.DeliveryStatus, &lastDeliveryAt, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan emergency intent: %w", err)
	}
	intent.Action = store.EmergencyAction(action)
	intent.Convergence = store.EmergencyConvergence(convergence)
	intent.AnnotationEntries = append(json.RawMessage(nil), annotationEntries...)
	intent.PromotionPaths = append(json.RawMessage(nil), promotionPaths...)
	intent.BeforeSnapshot = append(json.RawMessage(nil), beforeSnapshot...)
	intent.AfterSnapshot = append(json.RawMessage(nil), afterSnapshot...)
	assignOptionalEmergencyFields(&intent, container, artifactID, imageReference, targetReplicas, annotationScope)
	var err error
	intent.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse emergency created_at: %w", err)
	}
	intent.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse emergency updated_at: %w", err)
	}
	if lastDeliveryAt.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, lastDeliveryAt.String)
		if parseErr != nil {
			return nil, fmt.Errorf("parse emergency last_delivery_at: %w", parseErr)
		}
		intent.LastDeliveryAt = &parsed
	}
	return &intent, nil
}

func assignOptionalEmergencyFields(intent *store.EmergencyIntent, container, artifactID, imageReference sql.NullString, targetReplicas sql.NullInt64, annotationScope sql.NullString) {
	if container.Valid {
		intent.Container = &container.String
	}
	if artifactID.Valid {
		intent.ArtifactID = &artifactID.String
	}
	if imageReference.Valid {
		intent.ImageReference = &imageReference.String
	}
	if targetReplicas.Valid {
		value := int32(targetReplicas.Int64) //nolint:gosec // validated by the handler before persistence.
		intent.TargetReplicas = &value
	}
	if annotationScope.Valid {
		intent.AnnotationScope = &annotationScope.String
	}
}

func scanEmergencyIntentRows(rows *sql.Rows) ([]*store.EmergencyIntent, error) {
	intents := make([]*store.EmergencyIntent, 0)
	for rows.Next() {
		intent, err := scanEmergencyIntent(rows)
		if err != nil {
			return nil, err
		}
		intents = append(intents, intent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate emergency intents: %w", err)
	}
	return intents, nil
}

type emergencyAnnotationLock struct {
	Key string `json:"key"`
}

//nolint:gocyclo // Conflict checks across three action types are inherently branched.
func emergencyIntentsConflict(left, right *store.EmergencyIntent) bool {
	if left.WorkloadKind != right.WorkloadKind || left.WorkloadName != right.WorkloadName {
		return false
	}
	if left.Action == store.EmergencySetReplicas && right.Action == store.EmergencySetReplicas {
		return true
	}
	if left.Action == store.EmergencySetContainerImage && right.Action == store.EmergencySetContainerImage {
		return left.Container != nil && right.Container != nil && *left.Container == *right.Container
	}
	if left.Action != store.EmergencySetApprovedAnnotations || right.Action != store.EmergencySetApprovedAnnotations {
		return false
	}
	var leftEntries, rightEntries []emergencyAnnotationLock
	if json.Unmarshal(left.AnnotationEntries, &leftEntries) != nil || json.Unmarshal(right.AnnotationEntries, &rightEntries) != nil {
		return true
	}
	keys := make(map[string]struct{}, len(leftEntries))
	for _, entry := range leftEntries {
		keys[entry.Key] = struct{}{}
	}
	for _, entry := range rightEntries {
		if _, ok := keys[entry.Key]; ok {
			return true
		}
	}
	return false
}

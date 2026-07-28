package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/store"
)

type operationStore struct{ db *sql.DB }

func (s *operationStore) Create(ctx context.Context, op *store.Operation) error {
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

	var deadline *string
	if op.Deadline != nil {
		d := op.Deadline.UTC().Format(time.RFC3339)
		deadline = &d
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO operations (
			id, operation_type, status, release_definition_id,
			idempotency_key, request_hash, state_version,
			bundle_id, values_revision_id, expected_revision, values_patch,
			actor, created_at, updated_at, deadline, last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		op.ID, string(op.OperationType), string(op.Status), op.ReleaseDefinitionID,
		op.IdempotencyKey, op.RequestHash, op.StateVersion,
		op.BundleID, op.ValuesRevisionID, op.ExpectedRevision, op.ValuesPatch,
		string(actorJSON), op.CreatedAt.UTC().Format(time.RFC3339), op.UpdatedAt.UTC().Format(time.RFC3339),
		deadline, op.LastError,
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
	row := s.db.QueryRowContext(ctx, `
		SELECT id, operation_type, status, release_definition_id,
			idempotency_key, request_hash, state_version,
			bundle_id, values_revision_id, expected_revision, values_patch,
			actor, created_at, updated_at, deadline, last_error, terminal_at
		FROM operations WHERE id = ?
	`, id)
	return scanOperation(row)
}

func (s *operationStore) GetByIdempotencyKey(ctx context.Context, key string) (*store.Operation, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, operation_type, status, release_definition_id,
			idempotency_key, request_hash, state_version,
			bundle_id, values_revision_id, expected_revision, values_patch,
			actor, created_at, updated_at, deadline, last_error, terminal_at
		FROM operations WHERE idempotency_key = ?
	`, key)
	return scanOperation(row)
}

func (s *operationStore) UpdateStatus(ctx context.Context, id string, status store.OperationStatus, stateVersion int, lastError string) (*store.Operation, error) {
	now := nowUTC()
	var terminalAt *string
	if status.IsTerminal() {
		terminalAt = &now
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE operations
		SET status = ?, state_version = state_version + 1, last_error = ?, updated_at = ?, terminal_at = COALESCE(?, terminal_at)
		WHERE id = ? AND state_version = ?
	`, string(status), lastError, now, terminalAt, id, stateVersion)
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
	return s.Get(ctx, id)
}

// FinalizeUpgrade atomically persists a typed result, terminal CAS, inventory, rollout, and timeline event.
//
//nolint:gocyclo // One transaction intentionally applies every terminal projection atomically.
func (s *operationStore) FinalizeUpgrade(ctx context.Context, input *store.UpgradeTerminalInput) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin upgrade terminal transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	var existing int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM operation_execution_results WHERE operation_id = ?`,
		input.OperationID,
	).Scan(&existing); err != nil {
		return fmt.Errorf("check upgrade result idempotency: %w", err)
	}
	if existing > 0 {
		return nil
	}

	now := nowUTC()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO operation_execution_results (operation_id, result_type, result_payload, created_at)
		VALUES (?, 'upgrade', ?, ?)
	`, input.OperationID, input.ResultPayload, now); err != nil {
		return fmt.Errorf("insert upgrade result: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE operations
		SET status = ?, state_version = state_version + 1, terminal_at = ?, last_error = ?, updated_at = ?
		WHERE id = ? AND state_version = ?
	`, string(input.Status), now, input.LastError, now, input.OperationID, input.ExpectedStateVersion)
	if err != nil {
		return fmt.Errorf("terminal operation CAS: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("terminal operation rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrOptimisticLock
	}

	if input.UpdateInventory {
		result, err = tx.ExecContext(ctx, `
			UPDATE release_inventory
			SET revision = ?, values_digest = ?, observed_bundle_digest = ?, observed_chart_digest = ?,
			    observed_effective_values_digest = ?, observed_manifest_digest = ?, last_operation_id = ?,
			    inventory_status = ?, updated_at = ?
			WHERE release_definition_id = ?
		`, input.Revision, input.ObservedEffectiveValuesDigest, input.ObservedBundleDigest,
			input.ObservedChartDigest, input.ObservedEffectiveValuesDigest, input.ObservedManifestDigest,
			input.OperationID, string(input.InventoryStatus), now, input.ReleaseDefinitionID)
		if err != nil {
			return fmt.Errorf("update upgrade inventory: %w", err)
		}
		rows, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("upgrade inventory rows affected: %w", err)
		}
		if rows == 0 {
			return fmt.Errorf("update upgrade inventory: %w", store.ErrNotFound)
		}
	}

	if input.Status == store.StatusSucceeded {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO rollout_trackings
				(operation_id, status, resource_count, ready_count, failed_count, last_error, created_at, updated_at)
			VALUES (?, 'pending', ?, 0, 0, '', ?, ?)
			ON CONFLICT(operation_id) DO NOTHING
		`, input.OperationID, input.ResourceCount, now, now); err != nil {
			return fmt.Errorf("insert rollout tracking: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO operation_events (id, operation_id, event_type, payload, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, uuid.NewString(), input.OperationID, "operation."+string(input.Status), input.EventPayload, now); err != nil {
		return fmt.Errorf("insert operation event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upgrade terminal transaction: %w", err)
	}
	return nil
}

func (s *operationStore) HasActiveForDefinition(ctx context.Context, definitionID string) (bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM operations
		WHERE release_definition_id = ? AND status NOT IN ('succeeded','failed','cancelled','timeout')
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
	row := s.db.QueryRowContext(ctx, `
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
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, operation_type, status, release_definition_id,
			idempotency_key, request_hash, state_version,
			bundle_id, values_revision_id, expected_revision, values_patch,
			actor, created_at, updated_at, deadline, last_error, terminal_at
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

func scanOperation(row interface{ Scan(...interface{}) error }) (*store.Operation, error) {
	var (
		id, opType, status, defID, idemKey, reqHash string
		stateVer, expectedRev                       int
		bundleID, valuesRevID                       string
		valuesPatch                                 []byte
		actorJSON                                   string
		createdAt, updatedAt                        string
		deadline                                    *string
		lastError                                   string
		terminalAt                                  *string
	)

	err := row.Scan(
		&id, &opType, &status, &defID,
		&idemKey, &reqHash, &stateVer,
		&bundleID, &valuesRevID, &expectedRev, &valuesPatch,
		&actorJSON, &createdAt, &updatedAt, &deadline, &lastError,
		&terminalAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan operation: %w", err)
	}

	return buildOperation(id, opType, status, defID, idemKey, reqHash,
		stateVer, bundleID, valuesRevID, expectedRev, valuesPatch,
		actorJSON, createdAt, updatedAt, deadline, lastError, terminalAt)
}

func scanOperationFromRows(rows *sql.Rows) (*store.Operation, error) {
	var (
		id, opType, status, defID, idemKey, reqHash string
		stateVer, expectedRev                       int
		bundleID, valuesRevID                       string
		valuesPatch                                 []byte
		actorJSON                                   string
		createdAt, updatedAt                        string
		deadline                                    *string
		lastError                                   string
		terminalAt                                  *string
	)

	err := rows.Scan(
		&id, &opType, &status, &defID,
		&idemKey, &reqHash, &stateVer,
		&bundleID, &valuesRevID, &expectedRev, &valuesPatch,
		&actorJSON, &createdAt, &updatedAt, &deadline, &lastError,
		&terminalAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan operation row: %w", err)
	}

	return buildOperation(id, opType, status, defID, idemKey, reqHash,
		stateVer, bundleID, valuesRevID, expectedRev, valuesPatch,
		actorJSON, createdAt, updatedAt, deadline, lastError, terminalAt)
}

func buildOperation(id, opType, status, defID, idemKey, reqHash string,
	stateVer int, bundleID, valuesRevID string, expectedRev int,
	valuesPatch []byte, actorJSON, createdAt, updatedAt string,
	deadline *string, lastError string, terminalAt *string,
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

	var dl *time.Time
	if deadline != nil && *deadline != "" {
		t, err := time.Parse(time.RFC3339, *deadline)
		if err != nil {
			return nil, fmt.Errorf("parse deadline: %w", err)
		}
		dl = &t
	}

	var terminal *time.Time
	if terminalAt != nil && *terminalAt != "" {
		t, err := time.Parse(time.RFC3339, *terminalAt)
		if err != nil {
			return nil, fmt.Errorf("parse terminal_at: %w", err)
		}
		terminal = &t
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
		ValuesPatch:         valuesPatch,
		Actor:               actor,
		CreatedAt:           ct,
		UpdatedAt:           ut,
		Deadline:            dl,
		LastError:           lastError,
		TerminalAt:          terminal,
	}, nil
}

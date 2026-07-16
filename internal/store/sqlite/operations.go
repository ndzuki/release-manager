package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

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
			emergency_action, convergence,
			actor, created_at, updated_at, deadline, last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		op.ID, string(op.OperationType), string(op.Status), op.ReleaseDefinitionID,
		op.IdempotencyKey, op.RequestHash, op.StateVersion,
		op.BundleID, op.ValuesRevisionID, op.ExpectedRevision, op.ValuesPatch,
		string(op.EmergencyAction), string(op.Convergence),
		string(actorJSON), op.CreatedAt.UTC().Format(time.RFC3339), op.UpdatedAt.UTC().Format(time.RFC3339),
		deadline, op.LastError,
	)
	if err != nil {
		return fmt.Errorf("insert operation: %w", err)
	}
	return nil
}

func (s *operationStore) Get(ctx context.Context, id string) (*store.Operation, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, operation_type, status, release_definition_id,
			idempotency_key, request_hash, state_version,
			bundle_id, values_revision_id, expected_revision, values_patch,
			emergency_action, convergence,
			actor, created_at, updated_at, deadline, last_error
		FROM operations WHERE id = ?
	`, id)
	return scanOperation(row)
}

func (s *operationStore) GetByIdempotencyKey(ctx context.Context, key string) (*store.Operation, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, operation_type, status, release_definition_id,
			idempotency_key, request_hash, state_version,
			bundle_id, values_revision_id, expected_revision, values_patch,
			emergency_action, convergence,
			actor, created_at, updated_at, deadline, last_error
		FROM operations WHERE idempotency_key = ?
	`, key)
	return scanOperation(row)
}

func (s *operationStore) UpdateStatus(ctx context.Context, id string, status store.OperationStatus, stateVersion int, lastError string) (*store.Operation, error) {
	now := nowUTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE operations
		SET status = ?, state_version = state_version + 1, last_error = ?, updated_at = ?
		WHERE id = ? AND state_version = ?
	`, string(status), lastError, now, id, stateVersion)
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

// HasActiveStandardForDefinition returns true if a standard operation is active.
func (s *operationStore) HasActiveStandardForDefinition(ctx context.Context, definitionID string) (bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM operations
		WHERE release_definition_id = ?
		  AND operation_type IN ('INSTALL','UPGRADE','ROLLBACK')
		  AND status NOT IN ('succeeded','failed','cancelled','timeout')
	`, definitionID)
	var count int
	if err := row.Scan(&count); err != nil {
		return false, fmt.Errorf("count active standard operations: %w", err)
	}
	return count > 0, nil
}

// HasPendingPromotionForDefinition returns true when Helm must wait for a
// promoted ValuesRevision after a successful emergency change.
func (s *operationStore) HasPendingPromotionForDefinition(ctx context.Context, definitionID string) (bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM operations
		WHERE release_definition_id = ?
		  AND operation_type = 'EMERGENCY'
		  AND status = 'succeeded'
		  AND convergence = 'require_promotion'
		  AND values_revision_id = ''
	`, definitionID)
	var count int
	if err := row.Scan(&count); err != nil {
		return false, fmt.Errorf("count pending emergency promotions: %w", err)
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
			emergency_action, convergence,
			actor, created_at, updated_at, deadline, last_error
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
		emergencyAction, convergence                string
		actorJSON                                   string
		createdAt, updatedAt                        string
		deadline                                    *string
		lastError                                   string
	)

	err := row.Scan(
		&id, &opType, &status, &defID,
		&idemKey, &reqHash, &stateVer,
		&bundleID, &valuesRevID, &expectedRev, &valuesPatch,
		&emergencyAction, &convergence,
		&actorJSON, &createdAt, &updatedAt, &deadline, &lastError,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan operation: %w", err)
	}

	return buildOperation(id, opType, status, defID, idemKey, reqHash,
		stateVer, bundleID, valuesRevID, expectedRev, valuesPatch,
		emergencyAction, convergence, actorJSON, createdAt, updatedAt, deadline, lastError)
}

func scanOperationFromRows(rows *sql.Rows) (*store.Operation, error) {
	var (
		id, opType, status, defID, idemKey, reqHash string
		stateVer, expectedRev                       int
		bundleID, valuesRevID                       string
		valuesPatch                                 []byte
		emergencyAction, convergence                string
		actorJSON                                   string
		createdAt, updatedAt                        string
		deadline                                    *string
		lastError                                   string
	)

	err := rows.Scan(
		&id, &opType, &status, &defID,
		&idemKey, &reqHash, &stateVer,
		&bundleID, &valuesRevID, &expectedRev, &valuesPatch,
		&emergencyAction, &convergence,
		&actorJSON, &createdAt, &updatedAt, &deadline, &lastError,
	)
	if err != nil {
		return nil, fmt.Errorf("scan operation row: %w", err)
	}

	return buildOperation(id, opType, status, defID, idemKey, reqHash,
		stateVer, bundleID, valuesRevID, expectedRev, valuesPatch,
		emergencyAction, convergence, actorJSON, createdAt, updatedAt, deadline, lastError)
}

func buildOperation(
	id, opType, status, defID, idemKey, reqHash string,
	stateVer int,
	bundleID, valuesRevID string,
	expectedRev int,
	valuesPatch []byte,
	emergencyAction, convergence, actorJSON, createdAt, updatedAt string,
	deadline *string,
	lastError string,
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
		EmergencyAction:     store.EmergencyAction(emergencyAction),
		Convergence:         store.EmergencyConvergence(convergence),
		Actor:               actor,
		CreatedAt:           ct,
		UpdatedAt:           ut,
		Deadline:            dl,
		LastError:           lastError,
	}, nil
}

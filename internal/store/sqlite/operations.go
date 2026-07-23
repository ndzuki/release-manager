package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ndzuki/release-manager/internal/store"
)

type operationStore struct {
	db *sql.DB
	pl *preflightLifecycleStore // best-effort preflight lifecycle GC integration
}

const operationColumns = `id, operation_type, status, release_definition_id,
    idempotency_key, idempotency_scope, request_hash, state_version,
    bundle_id, bundle_chart_ref, bundle_chart_digest, image_refs_json, image_digests_json, policy_version,
    values_revision_id, expected_revision, target_revision, values_patch, patch_digest, effective_values_digest, reason,
    actor, created_at, updated_at, deadline, last_error`

func (s *operationStore) Create(ctx context.Context, op *store.Operation) error {
	return createOperation(ctx, s.db, op)
}

func (s *operationStore) CreateIfAvailable(ctx context.Context, op *store.Operation) error {
	return retryBusy(ctx, func() error { return createIfAvailable(ctx, s.db, op, nil) })
}

func (s *operationStore) CreateIfAvailableWithDispatch(ctx context.Context, op *store.Operation, dispatch *store.OutboxEntry) error {
	return retryBusy(ctx, func() error { return createIfAvailable(ctx, s.db, op, dispatch) })
}

func createIfAvailable(ctx context.Context, db *sql.DB, op *store.Operation, dispatch *store.OutboxEntry) error {
	tx, err := db.BeginTx(ctx, nil)
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
	if dispatch != nil {
		if err := createOutbox(ctx, tx, dispatch); err != nil {
			return fmt.Errorf("create preflight dispatch: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create operation: %w", err)
	}
	return nil
}

// retryBusy retries fn up to 10 times with exponential backoff when
// the error indicates a SQLite busy condition (concurrent write in WAL mode).
func retryBusy(ctx context.Context, fn func() error) error {
	const maxRetries = 10
	backoff := time.Millisecond
	var lastErr error
	for range maxRetries + 1 {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if !isSQLiteBusy(lastErr) {
			return lastErr
		}
		time.Sleep(backoff)
		backoff *= 2
	}
	return lastErr
}

func isSQLiteBusy(err error) bool {
	message := err.Error()
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked")
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

	var deadline *string
	if op.Deadline != nil {
		d := op.Deadline.UTC().Format(time.RFC3339)
		deadline = &d
	}

	_, err = execer.ExecContext(ctx, `
		INSERT INTO operations (
			id, operation_type, status, release_definition_id,
			idempotency_key, idempotency_scope, request_hash, state_version,
			bundle_id, bundle_chart_ref, bundle_chart_digest, image_refs_json, image_digests_json, policy_version,
			values_revision_id, expected_revision, target_revision, values_patch, patch_digest, effective_values_digest, reason,
			actor, created_at, updated_at, deadline, last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		op.ID, string(op.OperationType), string(op.Status), op.ReleaseDefinitionID,
		op.IdempotencyKey, op.IdempotencyScope, op.RequestHash, op.StateVersion,
		op.BundleID, op.BundleChartRef, op.BundleChartDigest, op.ImageRefsJSON, op.ImageDigestsJSON, op.PolicyVersion,
		op.ValuesRevisionID, op.ExpectedRevision, op.TargetRevision, op.ValuesPatch, op.PatchDigest, op.EffectiveValuesDigest, op.Reason,
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

func createOutbox(ctx context.Context, execer operationExecer, entry *store.OutboxEntry) error {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = entry.CreatedAt
	}
	if entry.Status == "" {
		entry.Status = store.CommandPending
	}
	if entry.CommandID == "" {
		entry.CommandID = entry.ID
	}
	if entry.Payload == nil {
		entry.Payload = []byte{}
	}
	_, err := execer.ExecContext(ctx, `
		INSERT INTO outbox (id, command_id, operation_id, operation_type, operator_id, payload, status, max_inflight, sequence, result_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, entry.ID, entry.CommandID, entry.OperationID, entry.OperationType, entry.OperatorID, entry.Payload,
		string(entry.Status), entry.MaxInFlight, entry.Sequence, entry.ResultJSON,
		entry.CreatedAt.UTC().Format(time.RFC3339), entry.UpdatedAt.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("insert outbox entry: %w", err)
	}
	return nil
}

func (s *operationStore) Get(ctx context.Context, id string) (*store.Operation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM operations WHERE id = ?`, id)
	return scanOperation(row)
}

func (s *operationStore) GetByIdempotencyScopeAndKey(ctx context.Context, scope, key string) (*store.Operation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM operations WHERE idempotency_scope = ? AND idempotency_key = ?`, scope, key)
	return scanOperation(row)
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
	tx, err := s.db.BeginTx(ctx, nil)
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

	now := nowUTC()
	result, err := tx.ExecContext(ctx, `
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

	updated := *current
	updated.Status = status
	updated.StateVersion++
	updated.LastError = lastError
	updated.UpdatedAt, err = time.Parse(time.RFC3339, now)
	if err != nil {
		return nil, fmt.Errorf("parse transition time: %w", err)
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

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit operation transition: %w", err)
	}

	// REQ-069: When an operation reaches a terminal state, update the associated
	// preflight lifecycle record's operation_terminal_at so GC can evaluate it.
	if status.IsTerminal() {
		s.maybeSetPreflightTerminal(ctx, id, updated.UpdatedAt)
	}

	return &updated, nil
}

func (s *operationStore) HasActiveForDefinition(ctx context.Context, definitionID string) (bool, error) {
	row := s.db.QueryRowContext(ctx, `
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
	rows, err := s.db.QueryContext(ctx, `SELECT `+operationColumns+` FROM operations WHERE release_definition_id = ? ORDER BY created_at DESC`, definitionID)
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
	rows, err := s.db.QueryContext(ctx, `SELECT `+operationColumns+` FROM operations WHERE status NOT IN ('succeeded','failed','cancelled','timeout') ORDER BY created_at ASC`)
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
	row := queryer.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM operations WHERE id = ?`, id)
	return scanOperation(row)
}
func scanOperation(row interface{ Scan(...interface{}) error }) (*store.Operation, error) {
	var (
		id, opType, status, defID, idemKey, idemScope, reqHash           string
		stateVer, expectedRev, targetRev                                 int
		bundleID, bundleChartRef, bundleChartDigest                      string
		imageRefsJSON, imageDigestsJSON                                  []byte
		policyVersion, valuesRevID, patchDigest, effectiveDigest, reason string
		valuesPatch                                                      []byte
		actorJSON                                                        string
		createdAt, updatedAt                                             string
		deadline                                                         *string
		lastError                                                        string
	)

	err := row.Scan(
		&id, &opType, &status, &defID,
		&idemKey, &idemScope, &reqHash, &stateVer,
		&bundleID, &bundleChartRef, &bundleChartDigest, &imageRefsJSON, &imageDigestsJSON, &policyVersion,
		&valuesRevID, &expectedRev, &targetRev, &valuesPatch, &patchDigest, &effectiveDigest, &reason,
		&actorJSON, &createdAt, &updatedAt, &deadline, &lastError,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan operation: %w", err)
	}

	return buildOperation(id, opType, status, defID, idemKey, idemScope, reqHash,
		stateVer, bundleID, bundleChartRef, bundleChartDigest, imageRefsJSON, imageDigestsJSON, policyVersion,
		valuesRevID, expectedRev, targetRev, valuesPatch, patchDigest, effectiveDigest, reason,
		actorJSON, createdAt, updatedAt, deadline, lastError)
}

func scanOperationFromRows(rows *sql.Rows) (*store.Operation, error) {
	return scanOperation(rows)
}

func buildOperation(id, opType, status, defID, idemKey, idemScope, reqHash string,
	stateVer int, bundleID, bundleChartRef, bundleChartDigest string, imageRefsJSON, imageDigestsJSON []byte, policyVersion string,
	valuesRevID string, expectedRev, targetRev int, valuesPatch []byte, patchDigest, effectiveDigest, reason string,
	actorJSON, createdAt, updatedAt string, deadline *string, lastError string,
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
		ID: id, OperationType: store.OperationType(opType), Status: store.OperationStatus(status), ReleaseDefinitionID: defID,
		IdempotencyKey: idemKey, IdempotencyScope: idemScope, RequestHash: reqHash, StateVersion: stateVer,
		BundleID: bundleID, BundleChartRef: bundleChartRef, BundleChartDigest: bundleChartDigest,
		ImageRefsJSON: imageRefsJSON, ImageDigestsJSON: imageDigestsJSON, PolicyVersion: policyVersion,
		ValuesRevisionID: valuesRevID, ExpectedRevision: expectedRev, TargetRevision: targetRev, ValuesPatch: valuesPatch,
		PatchDigest: patchDigest, EffectiveValuesDigest: effectiveDigest, Reason: reason,
		Actor: actor, CreatedAt: ct, UpdatedAt: ut, Deadline: dl, LastError: lastError,
	}, nil
}

// maybeSetPreflightTerminal updates the preflight lifecycle record's
// operation_terminal_at when the operation reaches a terminal state.
// This is best-effort — failures are silently ignored since the operation
// transition already succeeded and GC will re-evaluate on the next cycle.
func (s *operationStore) maybeSetPreflightTerminal(ctx context.Context, operationID string, terminalAt time.Time) {
	if s.pl == nil {
		return
	}
	_ = s.pl.SetOperationTerminal(ctx, operationID, terminalAt) //nolint:errcheck // Best-effort GC metadata after the operation commit.
}

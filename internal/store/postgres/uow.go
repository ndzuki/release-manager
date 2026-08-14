package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	infrastructure "github.com/ndzuki/release-manager/internal/postgres"
	"github.com/ndzuki/release-manager/internal/store"
	"gorm.io/gorm"
)

// OperationCreationUnitOfWork returns the PostgreSQL adapter for the operation
// creation transaction. It executes on the REQ-070 transaction seam so GORM
// writes and raw SQL share one connection (ADR-014).
func (s *Store) OperationCreationUnitOfWork() store.OperationCreationUnitOfWork {
	return func(ctx context.Context, req store.OperationCreationRequest) (*store.OperationCreationResult, error) {
		if req.Operation == nil {
			return nil, errors.New("operation is required")
		}
		if req.Dispatch == nil {
			return nil, errors.New("preflight dispatch is required")
		}
		var result *store.OperationCreationResult
		err := infrastructure.OperationCreationUnitOfWork(ctx, s.gormDB, func(tx *gorm.DB, _ *sql.Tx) error {
			// Tx wraps the GORM transaction and applies the ? → $N
			// placeholder translation used by the rest of this package.
			pgTx := &Tx{gorm: tx}
			var err error
			result, err = createOperationUnitOfWork(ctx, pgTx, req)
			return err
		})
		return result, err
	}
}

// createOperationUnitOfWork atomically commits operation, dispatch, bundle
// CAS, candidate artifact links, and the definition's current_bundle_id.
func createOperationUnitOfWork(
	ctx context.Context,
	tx *Tx,
	req store.OperationCreationRequest,
) (*store.OperationCreationResult, error) {
	if err := checkAuthorizationFence(ctx, tx, req.ExpectedAuthorizationVersion); err != nil {
		return nil, err
	}
	// No other non-terminal operation may exist for the definition
	// (standard/EMERGENCY mutual exclusion is expressed by the caller
	// rejecting EMERGENCY; keep the same guard as the SQLite adapter).
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM operations
		WHERE release_definition_id = ?
		  AND status NOT IN ('succeeded','failed','cancelled','timeout')
	`, req.Operation.ReleaseDefinitionID).Scan(&count); err != nil {
		return nil, fmt.Errorf("count conflicting operations: %w", err)
	}
	if count > 0 {
		return nil, store.ErrReleaseBusy
	}
	if err := createOperation(ctx, tx, req.Operation); err != nil {
		return nil, err
	}
	if err := createOutboxEntry(ctx, tx, req.Dispatch); err != nil {
		return nil, fmt.Errorf("create preflight dispatch: %w", err)
	}
	restored, err := setCurrentBundle(ctx, tx, req.Operation.ReleaseDefinitionID, req.Operation.BundleID)
	if err != nil {
		return nil, fmt.Errorf("set current bundle: %w", err)
	}
	linked, err := linkCandidateArtifacts(ctx, tx, req.Operation.BundleID, req.CandidateArtifactDigests)
	if err != nil {
		return nil, err
	}
	return &store.OperationCreationResult{
		Operation:            req.Operation,
		BundleRestored:       restored,
		LinkedCandidateCount: linked,
	}, nil
}

// createOutboxEntry inserts one outbox row inside the caller's transaction.
func createOutboxEntry(ctx context.Context, execer operationExecer, e *store.OutboxEntry) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = e.CreatedAt
	}
	if len(e.Payload) == 0 {
		e.Payload = []byte{}
	}
	if e.Status == "" {
		e.Status = store.CommandPending
	}
	if e.CommandID == "" {
		e.CommandID = e.ID
	}
	_, err := execer.ExecContext(ctx, `
INSERT INTO outbox (id, command_id, operation_id, operation_type, operator_id, payload, status, max_inflight, sequence, result_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		e.ID, e.CommandID, e.OperationID, e.OperationType, e.OperatorID, e.Payload, string(e.Status),
		e.MaxInFlight, e.Sequence, e.ResultJSON,
		e.CreatedAt.UTC().Format(time.RFC3339), e.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert outbox entry: %w", err)
	}
	return nil
}

// setCurrentBundle applies the REQ-067 decision table inside the transaction:
// validated continues; archived + archived_from_status=validated is restored
// via CAS; archived + received/rejected is rejected; anything else is
// bundle_not_ready. It then updates the definition's current_bundle_id.
func setCurrentBundle(ctx context.Context, tx *Tx, defID, bundleID string) (bool, error) {
	var status string
	var archivedFromStatus sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT status, archived_from_status
		FROM release_bundles
		WHERE id = ?
		FOR UPDATE
	`, bundleID).Scan(&status, &archivedFromStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("bundle %s: %w", bundleID, store.ErrNotFound)
		}
		return false, fmt.Errorf("query bundle state: %w", err)
	}

	restored := false
	switch store.BundleStatus(status) {
	case store.BundleValidated:
	case store.BundleReceived:
		return false, store.ErrBundleNotReady
	case store.BundleRejected:
		return false, store.ErrBundleRejected
	case store.BundleArchived:
		var err error
		restored, err = restoreArchivedBundle(ctx, tx, bundleID, store.BundleStatus(archivedFromStatus.String))
		if err != nil {
			return false, err
		}
	default:
		return false, store.ErrBundleNotReady
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE release_definitions
		SET current_bundle_id = ?
		WHERE id = ? AND (current_bundle_id IS NULL OR current_bundle_id != ?)
	`, bundleID, defID, bundleID)
	if err != nil {
		return false, fmt.Errorf("update definition current_bundle_id: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("definition rows affected: %w", err)
	}
	if rows == 0 {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM release_definitions WHERE id = ?`, defID).Scan(&exists); err != nil {
			return false, fmt.Errorf("check definition existence: %w", err)
		}
		if exists == 0 {
			return false, fmt.Errorf("definition %s: %w", defID, store.ErrNotFound)
		}
	}
	return restored, nil
}

// restoreArchivedBundle applies the archived branch of the REQ-067
// SetCurrentBundle decision table: only bundles archived from validated are
// CAS-restored; received/rejected stay archived and are rejected.
func restoreArchivedBundle(ctx context.Context, tx *Tx, bundleID string, archivedFrom store.BundleStatus) (bool, error) {
	switch archivedFrom {
	case store.BundleValidated:
		result, err := tx.ExecContext(ctx, `
			UPDATE release_bundles
			SET status = 'validated', archived_at = NULL, archived_from_status = ''
			WHERE id = ? AND status = 'archived' AND archived_from_status = 'validated'
		`, bundleID)
		if err != nil {
			return false, fmt.Errorf("restore archived bundle %s: %w", bundleID, err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("restore archived bundle rows affected: %w", err)
		}
		if rows != 1 {
			return false, store.ErrOptimisticLock
		}
		return true, nil
	case store.BundleReceived:
		return false, store.ErrBundleNotReady
	case store.BundleRejected:
		return false, store.ErrBundleRejected
	default:
		return false, store.ErrBundleNotReady
	}
}

// linkCandidateArtifacts batch-links candidate artifacts by digest inside the
// caller's transaction. Artifacts that gain their first bundle association
// have orphaned_at cleared (AC-067-19).
func linkCandidateArtifacts(ctx context.Context, tx *Tx, bundleID string, digests []string) (int64, error) {
	if len(digests) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(digests)+1)
	args = append(args, bundleID)
	for _, digest := range digests {
		args = append(args, digest)
	}
	placeholders := ""
	for i := range digests {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO bundle_candidate_artifacts (bundle_id, artifact_id, linked_at)
		SELECT ?, ca.id, NOW()
		FROM candidate_artifacts ca
		WHERE ca.digest IN (`+placeholders+`)
		ON CONFLICT (bundle_id, artifact_id) DO NOTHING
	`, args...)
	if err != nil {
		return 0, fmt.Errorf("insert bundle candidate artifact links: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE candidate_artifacts
		SET orphaned_at = NULL
		WHERE id IN (
			SELECT artifact_id FROM bundle_candidate_artifacts WHERE bundle_id = ?
		) AND orphaned_at IS NOT NULL
	`, bundleID); err != nil {
		return 0, fmt.Errorf("clear linked candidate artifact orphaned_at: %w", err)
	}
	return result.RowsAffected()
}

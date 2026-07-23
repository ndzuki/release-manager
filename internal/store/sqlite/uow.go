package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ndzuki/release-manager/internal/store"
)

// OperationCreationUnitOfWork returns the SQLite adapter for the operation
// creation transaction.
func (s *Store) OperationCreationUnitOfWork() store.OperationCreationUnitOfWork {
	return s.createOperation
}

func (s *Store) createOperation(
	ctx context.Context,
	req store.OperationCreationRequest,
) (*store.OperationCreationResult, error) {
	if req.Operation == nil {
		return nil, errors.New("operation is required")
	}
	if req.Dispatch == nil {
		return nil, errors.New("preflight dispatch is required")
	}

	var result *store.OperationCreationResult
	err := retryBusy(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin operation creation: %w", err)
		}
		defer tx.Rollback() //nolint:errcheck // Rollback is a no-op after successful Commit.

		result, err = createOperationUnitOfWork(ctx, tx, req)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit operation creation: %w", err)
		}
		return nil
	})
	return result, err
}

func createOperationUnitOfWork(
	ctx context.Context,
	tx *sql.Tx,
	req store.OperationCreationRequest,
) (*store.OperationCreationResult, error) {
	if err := ensureOperationAvailable(ctx, tx, req.Operation); err != nil {
		return nil, err
	}
	if err := createOperation(ctx, tx, req.Operation); err != nil {
		return nil, err
	}
	if err := createOutbox(ctx, tx, req.Dispatch); err != nil {
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

func ensureOperationAvailable(ctx context.Context, tx *sql.Tx, op *store.Operation) error {
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
	return nil
}

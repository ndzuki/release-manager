package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
	if req.Emergency != nil {
		if req.Emergency.Operation == nil || req.Emergency.Intent == nil {
			return nil, errors.New("emergency creation requires operation and intent")
		}
	}
	// TASK-082 (D-108 ①b): a nil Dispatch is valid — UPGRADE operations do not
	// run preflight stages and runUpgrade builds its :execute entry itself, so
	// no :artifact row may be committed for them (a first-delivered :artifact
	// poisons the operator stream). createOperationUnitOfWork skips the outbox
	// write when Dispatch is nil.

	var result *store.OperationCreationResult
	err := retryBusy(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin operation creation: %w", err)
		}
		defer tx.Rollback() //nolint:errcheck // Rollback is a no-op after successful Commit.

		if req.Emergency != nil {
			result, err = createEmergencyOperationUnitOfWork(ctx, tx, req)
		} else {
			result, err = createOperationUnitOfWork(ctx, tx, req)
		}
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

// createEmergencyOperationUnitOfWork atomically commits the EMERGENCY
// operation, its intent, optional convergence task and the idempotency record
// on the shared operation-creation seam (REQ-079).
//
//nolint:gocyclo // Mirrors the legacy emergency create transaction; branches are domain-ordered.
func createEmergencyOperationUnitOfWork(
	ctx context.Context,
	tx *sql.Tx,
	req store.OperationCreationRequest,
) (*store.OperationCreationResult, error) {
	command := req.Emergency
	op := command.Operation
	if err := checkAuthorizationFence(ctx, tx, command.ExpectedAuthorizationVersion); err != nil {
		return nil, err
	}
	// 空 Key 时跳过全部幂等逻辑（不 lookup、不 insert），业务写入照常。
	idempotent := command.IdempotencyKeyHash != ""
	if idempotent {
		replayed, err := lookupEmergencyReplay(ctx, tx, *command)
		if err != nil {
			return nil, err
		}
		if replayed != nil {
			return emergencyUowResult(replayed), nil
		}
	}

	var standardCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM operations
		WHERE release_definition_id = ?
		  AND operation_type != 'EMERGENCY'
		  AND status NOT IN ('succeeded','failed','cancelled','timeout')
	`, op.ReleaseDefinitionID).Scan(&standardCount); err != nil {
		return nil, fmt.Errorf("count standard operation conflicts: %w", err)
	}
	if standardCount > 0 {
		return nil, store.ErrReleaseBusy
	}
	// D18: release-global EMERGENCY mutex (AC-079-G3).
	var emergencyCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM operations
		WHERE release_definition_id = ?
		  AND operation_type = 'EMERGENCY'
		  AND status NOT IN ('succeeded','failed','cancelled','timeout')
	`, op.ReleaseDefinitionID).Scan(&emergencyCount); err != nil {
		return nil, fmt.Errorf("count in-flight emergency operations: %w", err)
	}
	if emergencyCount > 0 {
		return nil, store.ErrEmergencyOperationInProgress
	}

	active, err := listActiveEmergencyIntents(ctx, tx, op.ReleaseDefinitionID)
	if err != nil {
		return nil, err
	}
	for _, existing := range active {
		if emergencyIntentsConflict(existing, command.Intent) {
			return nil, store.ErrEmergencyConflict
		}
	}

	if err := createOperation(ctx, tx, op); err != nil {
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
		OperationID: op.ID,
		IntentID:    command.Intent.ID,
	}
	if command.ConvergenceTask != nil {
		reference.ConvergenceTaskID = command.ConvergenceTask.ID
	}
	if idempotent {
		responseRef, err := json.Marshal(reference)
		if err != nil {
			return nil, fmt.Errorf("marshal emergency replay reference: %w", err)
		}
		expiresAt := command.IdempotencyExpiresAt
		if expiresAt.IsZero() {
			expiresAt = time.Now().UTC().Add(emergencyIdempotencyTTL)
		}
		existing, created, err := createOrGetIdempotencyRecord(ctx, tx, &store.IdempotencyRecord{
			Scope: command.IdempotencyScope, Key: command.IdempotencyKeyHash,
			RequestHash: command.RequestHash, ResponseRef: responseRef, ExpiresAt: expiresAt,
		}, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if !created {
			// 并发窗口内另一事务已提交相同 scope+key+hash：重放其结果，业务写入随回滚丢弃。
			replayed, err := decodeEmergencyReplay(ctx, tx, existing)
			if err != nil {
				return nil, err
			}
			return emergencyUowResult(replayed), nil
		}
	}
	if err := checkAuthorizationFence(ctx, tx, command.ExpectedAuthorizationVersion); err != nil {
		return nil, err
	}
	return emergencyUowResult(&store.EmergencyCreateResult{
		Operation: op, Intent: command.Intent, ConvergenceTask: command.ConvergenceTask,
	}), nil
}

func emergencyUowResult(created *store.EmergencyCreateResult) *store.OperationCreationResult {
	return &store.OperationCreationResult{
		Operation:       created.Operation,
		Intent:          created.Intent,
		ConvergenceTask: created.ConvergenceTask,
		Replayed:        created.Replayed,
	}
}

func createOperationUnitOfWork(
	ctx context.Context,
	tx *sql.Tx,
	req store.OperationCreationRequest,
) (*store.OperationCreationResult, error) {
	if err := checkAuthorizationFence(ctx, tx, req.ExpectedAuthorizationVersion); err != nil {
		return nil, err
	}
	if err := ensureOperationAvailable(ctx, tx, req.Operation); err != nil {
		return nil, err
	}
	if err := createOperation(ctx, tx, req.Operation); err != nil {
		return nil, err
	}
	// TASK-082: UPGRADE carries no preflight dispatch (runUpgrade creates the
	// :execute entry); skip the outbox write instead of inserting an
	// unconsumed :artifact row.
	if req.Dispatch != nil {
		if err := createOutbox(ctx, tx, req.Dispatch); err != nil {
			return nil, fmt.Errorf("create preflight dispatch: %w", err)
		}
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

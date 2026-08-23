package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ndzuki/release-manager/internal/store"
)

type valuesLifecycleStore struct{ gorm *DB }

type valuesLifecycleReplayRef struct {
	RevisionID string `json:"revision_id"`
	// Revision snapshots the first Create response so an idempotent replay
	// returns the original result even after the draft moved on (D-9: replay
	// returns first result, created=false). Discard replays reload the row —
	// the discarded state is terminal and immutable.
	Revision      *store.ValuesRevision `json:"revision,omitempty"`
	PreviousState store.ValuesStatus    `json:"previous_state,omitempty"`
	NewState      store.ValuesStatus    `json:"new_state,omitempty"`
	DecidedAt     time.Time             `json:"decided_at,omitempty"`
}

//nolint:gocyclo // The atomic create transaction mirrors the ordered REQ-018 validation and write contract.
func (s *valuesLifecycleStore) CreateDraft(ctx context.Context, command store.CreateValuesDraftCommand) (*store.CreateValuesDraftResult, error) {
	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create values draft: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback after Commit is a no-op.
	if err := checkAuthorizationFence(ctx, tx, command.ExpectedAuthorizationVersion); err != nil {
		return nil, err
	}
	if replay, err := lookupCreateValuesReplay(ctx, tx, command); err != nil || replay != nil {
		return replay, err
	}
	var prepared *store.PrepareSession
	if command.PrepareTokenHash != "" {
		prepared, err = consumePrepareSession(ctx, tx, command)
		if err != nil {
			return nil, err
		}
		if command.ExpectedLockedPathHash != "" && prepared.LockedPathHash != command.ExpectedLockedPathHash {
			return nil, store.ErrConvergenceConflict
		}
	}
	var definitionID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM release_definitions WHERE id = ? FOR UPDATE`, command.Revision.ReleaseDefinitionID).Scan(&definitionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("lock release definition: %w", err)
	}
	nextVersion, err := validateValuesParent(ctx, tx, command.Revision.ReleaseDefinitionID, command.Revision.ParentRevisionID, command.ExpectedParentVersion)
	if err != nil {
		// REQ-018 D18/校验顺序 8: a converged initial create rechecks the
		// definition still has no revisions; if one appeared between Prepare
		// and Create, that is chain-head drift → parent_conflict. The plain
		// create keeps ErrDuplicateKey → invalid_argument (AC-018-17).
		if command.PrepareTokenHash != "" && errors.Is(err, store.ErrDuplicateKey) {
			return nil, store.ErrParentConflict
		}
		return nil, err
	}
	command.Revision.Version = nextVersion
	command.Revision.StateVersion = 1
	command.Revision.Status = store.ValuesStatusDraft
	command.Revision.CreatedByUserID = command.ActorUserID
	if command.Revision.CreatedAt.IsZero() {
		command.Revision.CreatedAt = time.Now().UTC()
	}
	if command.Revision.UpdatedAt.IsZero() {
		command.Revision.UpdatedAt = command.Revision.CreatedAt
	}
	if err := insertValuesDraft(ctx, tx, command.Revision); err != nil {
		return nil, err
	}
	if prepared != nil {
		if err := bindPrepareTasks(ctx, tx, prepared, command.Revision); err != nil {
			return nil, err
		}
	}
	if err := insertValuesCreatedEvent(ctx, tx, command); err != nil {
		return nil, err
	}
	if err := insertCreateValuesIdempotency(ctx, tx, command); err != nil {
		if errors.Is(err, store.ErrIdempotencyConflict) {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				return nil, fmt.Errorf("rollback create values draft after idempotency conflict: %w", rollbackErr)
			}
			if replay, replayErr := s.loadCreateValuesReplay(ctx, command); replayErr != nil || replay != nil {
				return replay, replayErr
			}
		}
		return nil, err
	}
	if err := checkAuthorizationFence(ctx, tx, command.ExpectedAuthorizationVersion); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create values draft: %w", err)
	}
	return &store.CreateValuesDraftResult{Revision: command.Revision}, nil
}

//nolint:gocyclo // The atomic discard transaction keeps CAS, evidence, unbinding, outbox, and idempotency together.
func (s *valuesLifecycleStore) Discard(ctx context.Context, command store.DiscardValuesCommand) (*store.DiscardValuesResult, error) {
	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin discard values revision: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback after Commit is a no-op.
	if err := checkAuthorizationFence(ctx, tx, command.ExpectedAuthorizationVersion); err != nil {
		return nil, err
	}
	if replay, err := lookupDiscardValuesReplay(ctx, tx, command); err != nil || replay != nil {
		return replay, err
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `
		UPDATE values_revisions SET status = 'discarded', state_version = state_version + 1,
			decided_at = ?, updated_at = ?
		WHERE id = ? AND status = 'draft' AND state_version = ? AND created_by_user_id = ?
	`, now, now, command.RevisionID, command.ExpectedStateVersion, command.ActorUserID)
	if err != nil {
		return nil, fmt.Errorf("discard values revision: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("discard values revision rows: %w", err)
	}
	if rows != 1 {
		if _, getErr := getValuesApprovalRevision(ctx, tx, command.RevisionID); getErr != nil {
			return nil, getErr
		}
		return nil, store.ErrDiscardNotAllowed
	}
	revision, err := getValuesApprovalRevision(ctx, tx, command.RevisionID)
	if err != nil {
		return nil, err
	}
	decision := &store.ValuesRevisionDecision{
		ID: uuid.NewString(), RevisionID: revision.ID, ReleaseDefinitionID: revision.ReleaseDefinitionID,
		Action: store.ValuesDecisionDiscarded, FromState: store.ValuesStatusDraft, ToState: store.ValuesStatusDiscarded,
		ActorUserID: command.ActorUserID, ActorOrgID: command.ActorOrgID, ActorRole: command.ActorRole,
		Comment: command.Comment, RequestID: command.RequestID, IdempotencyKeyHash: command.IdempotencyKeyHash, CreatedAt: now,
	}
	if err := insertValuesRevisionDecision(ctx, tx, decision); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE convergence_tasks SET active_revision_id = NULL, active_revision_status = NULL
		WHERE active_revision_id = ? AND status = 'pending_promotion'
	`, revision.ID); err != nil {
		return nil, fmt.Errorf("unbind discarded convergence tasks: %w", err)
	}
	if err := insertValuesDiscardedEvent(ctx, tx, command, revision, now); err != nil {
		return nil, err
	}
	discarded := &store.DiscardValuesResult{
		Revision: revision, PreviousState: store.ValuesStatusDraft, NewState: store.ValuesStatusDiscarded, DecidedAt: now,
	}
	if err := insertDiscardValuesIdempotency(ctx, tx, command, discarded); err != nil {
		if errors.Is(err, store.ErrIdempotencyConflict) {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				return nil, fmt.Errorf("rollback discard values revision after idempotency conflict: %w", rollbackErr)
			}
			if replay, replayErr := s.loadDiscardValuesReplay(ctx, command); replayErr != nil || replay != nil {
				return replay, replayErr
			}
		}
		return nil, err
	}
	if err := checkAuthorizationFence(ctx, tx, command.ExpectedAuthorizationVersion); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit discard values revision: %w", err)
	}
	return discarded, nil
}

// validateValuesParent 校验链头并发契约（AC-018-05/24）：definition 无
// revision 时仅允许 initial（parent 省略且 expected=0，返回 version=1）；
// 已有 revision 时 parent 必填、属同 definition 且 MAX(version) == expected，
// 失配返回 ErrParentConflict；initial 哨兵撞上已存在 revision 返回
// ErrDuplicateKey（普通创建 → invalid_argument；收敛创建由调用方转
// parent_conflict）。
func validateValuesParent(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, definitionID, parentID string, expectedVersion int64) (int64, error) {
	var maxVersion sql.NullInt64
	if err := queryer.QueryRowContext(ctx, `SELECT MAX(version) FROM values_revisions WHERE release_definition_id = ?`, definitionID).Scan(&maxVersion); err != nil {
		return 0, fmt.Errorf("read values chain head: %w", err)
	}
	if !maxVersion.Valid {
		if parentID != "" || expectedVersion != 0 {
			return 0, store.ErrParentConflict
		}
		return 1, nil
	}
	if parentID == "" && expectedVersion == 0 {
		return 0, store.ErrDuplicateKey
	}
	if parentID == "" || expectedVersion != maxVersion.Int64 {
		return 0, store.ErrParentConflict
	}
	var parentDefinition string
	if err := queryer.QueryRowContext(ctx, `SELECT release_definition_id FROM values_revisions WHERE id = ?`, parentID).Scan(&parentDefinition); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, store.ErrParentConflict
		}
		return 0, fmt.Errorf("read values parent: %w", err)
	}
	if parentDefinition != definitionID {
		return 0, store.ErrParentConflict
	}
	return maxVersion.Int64 + 1, nil
}

func consumePrepareSession(ctx context.Context, tx *Tx, command store.CreateValuesDraftCommand) (*store.PrepareSession, error) {
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `
		UPDATE convergence_prepare_sessions SET consumed_at = ?
		WHERE token_hash = ? AND actor_user_id = ? AND organization_id = ?
			AND release_definition_id = ? AND consumed_at IS NULL AND expires_at > ?
	`, now, command.PrepareTokenHash, command.ActorUserID, command.OrganizationID,
		command.Revision.ReleaseDefinitionID, now)
	if err != nil {
		return nil, fmt.Errorf("consume prepare session: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("consume prepare session rows: %w", err)
	}
	session, err := getPrepareSession(ctx, tx, command.PrepareTokenHash)
	if err != nil {
		return nil, err
	}
	if rows == 1 {
		return session, nil
	}
	if !session.ExpiresAt.After(now) {
		return nil, store.ErrPrepareTokenExpired
	}
	if session.ConsumedAt != nil {
		return nil, store.ErrPrepareTokenConsumed
	}
	return nil, store.ErrConvergenceConflict
}

//nolint:gocyclo // Task validation and binding intentionally stay in one transaction-local invariant check.
func bindPrepareTasks(ctx context.Context, tx *Tx, session *store.PrepareSession, revision *store.ValuesRevision) error {
	if session.ParentRevisionID != revision.ParentRevisionID || session.ParentVersion != revision.Version-1 {
		return store.ErrParentConflict
	}
	rows, err := tx.QueryContext(ctx, convergenceTaskSelect+` WHERE release_definition_id = ? FOR UPDATE`, revision.ReleaseDefinitionID)
	if err != nil {
		return fmt.Errorf("lock convergence tasks: %w", err)
	}
	defer rows.Close()

	allTasks := make([]*store.ConvergenceTask, 0)
	for rows.Next() {
		task, scanErr := scanConvergenceTask(rows)
		if scanErr != nil {
			return scanErr
		}
		allTasks = append(allTasks, task)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate convergence tasks: %w", err)
	}

	tasks := make([]*store.ConvergenceTask, 0, len(session.TaskIDs))
	for _, taskID := range session.TaskIDs {
		for _, task := range allTasks {
			if task.ID == taskID {
				tasks = append(tasks, task)
				break
			}
		}
	}
	paths, err := store.LockedPathsForTasks(session.TaskIDs, tasks)
	if err != nil {
		return err
	}
	if store.LockedPathHash(paths) != session.LockedPathHash || store.LockedPathHash(session.LockedPaths) != session.LockedPathHash {
		return store.ErrConvergenceConflict
	}
	conflict, err := store.HasActiveConvergencePathConflict(session.TaskIDs, paths, allTasks)
	if err != nil {
		return err
	}
	if conflict {
		return store.ErrConvergenceRevisionExists
	}
	for _, taskID := range session.TaskIDs {
		updated, updateErr := tx.ExecContext(ctx, `
			UPDATE convergence_tasks SET active_revision_id = ?, active_revision_status = 'draft'
			WHERE id = ? AND release_definition_id = ? AND status = 'pending_promotion' AND active_revision_id IS NULL
		`, revision.ID, taskID, revision.ReleaseDefinitionID)
		if updateErr != nil {
			return fmt.Errorf("bind convergence task: %w", updateErr)
		}
		count, countErr := updated.RowsAffected()
		if countErr != nil {
			return fmt.Errorf("bind convergence task rows: %w", countErr)
		}
		if count != 1 {
			return store.ErrConvergenceRevisionExists
		}
	}
	return nil
}

func insertValuesDraft(ctx context.Context, tx *Tx, revision *store.ValuesRevision) error {
	refs, err := json.Marshal(revision.SecretRefs)
	if err != nil {
		return fmt.Errorf("encode values secret refs: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO values_revisions (
			id, release_definition_id, version, state_version, status, "values", digest,
			parent_revision_id, secret_refs, created_by, created_by_user_id, approved_by,
			approved_at, rejected_by, rejection_reason, submitted_at, decided_at,
			convergence_task_ids, locked_paths, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, '', '', NULL, NULL, ?::uuid[], ?::text[], ?, ?)
	`, revision.ID, revision.ReleaseDefinitionID, revision.Version, revision.StateVersion, string(revision.Status),
		[]byte(revision.CanonicalDocument), revision.Digest, valuesOptionalString(revision.ParentRevisionID), refs,
		revision.CreatedByUserID, revision.CreatedByUserID, postgresArrayLiteral(revision.ConvergenceTaskIds),
		postgresArrayLiteral(revision.LockedPaths), revision.CreatedAt.UTC(), revision.UpdatedAt.UTC())
	if err != nil {
		if isUniqueConstraint(err) {
			return store.ErrParentConflict
		}
		return fmt.Errorf("insert values draft: %w", err)
	}
	return nil
}

func insertValuesCreatedEvent(ctx context.Context, tx *Tx, command store.CreateValuesDraftCommand) error {
	payload, err := json.Marshal(map[string]any{
		"event_id": uuid.NewString(), "revision_id": command.Revision.ID,
		"release_definition_id": command.Revision.ReleaseDefinitionID, "organization_id": command.OrganizationID,
		"created_by_user_id": command.ActorUserID, "parent_revision_id": command.Revision.ParentRevisionID,
		"digest": command.Revision.Digest, "created_at": command.Revision.CreatedAt.UTC().Format(time.RFC3339Nano),
		"request_id": command.RequestID,
	})
	if err != nil {
		return fmt.Errorf("encode values created event: %w", err)
	}
	return insertValuesApprovalOutbox(ctx, tx, auditApprovalOutbox, &store.ApprovalOutboxEntry{
		ID: uuid.NewString(), EventType: "ValuesRevisionCreated", PayloadJSON: payload, CreatedAt: command.Revision.CreatedAt,
	})
}

func insertValuesDiscardedEvent(ctx context.Context, tx *Tx, command store.DiscardValuesCommand, revision *store.ValuesRevision, now time.Time) error {
	payload := map[string]any{
		"event_id": uuid.NewString(), "revision_id": revision.ID,
		"release_definition_id": revision.ReleaseDefinitionID, "organization_id": command.ActorOrgID,
		"decided_by_user_id": command.ActorUserID, "decided_at": now.Format(time.RFC3339Nano),
		"request_id": command.RequestID,
	}
	if command.Comment != nil {
		sum := sha256.Sum256([]byte(*command.Comment))
		payload["comment_hash"] = hex.EncodeToString(sum[:])
		payload["comment_length"] = len([]byte(*command.Comment))
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode values discarded event: %w", err)
	}
	return insertValuesApprovalOutbox(ctx, tx, auditApprovalOutbox, &store.ApprovalOutboxEntry{
		ID: uuid.NewString(), EventType: "ValuesRevisionDiscarded", PayloadJSON: encoded, CreatedAt: now,
	})
}

func lookupCreateValuesReplay(ctx context.Context, queryer operationQueryer, command store.CreateValuesDraftCommand) (*store.CreateValuesDraftResult, error) {
	if command.IdempotencyKeyHash == "" {
		return nil, nil
	}
	ref, err := lookupValuesLifecycleReplay(ctx, queryer, command.IdempotencyScope, command.IdempotencyKeyHash, command.RequestHash)
	if err != nil || ref == nil {
		return nil, err
	}
	if ref.Revision != nil {
		return &store.CreateValuesDraftResult{Revision: ref.Revision, Replayed: true}, nil
	}
	// Fallback for records persisted before the snapshot field existed.
	revision, err := getValuesApprovalRevision(ctx, queryer, ref.RevisionID)
	if err != nil {
		return nil, err
	}
	return &store.CreateValuesDraftResult{Revision: revision, Replayed: true}, nil
}

func lookupDiscardValuesReplay(ctx context.Context, queryer operationQueryer, command store.DiscardValuesCommand) (*store.DiscardValuesResult, error) {
	if command.IdempotencyKeyHash == "" {
		return nil, nil
	}
	ref, err := lookupValuesLifecycleReplay(ctx, queryer, command.IdempotencyScope, command.IdempotencyKeyHash, command.RequestHash)
	if err != nil || ref == nil {
		return nil, err
	}
	revision, err := getValuesApprovalRevision(ctx, queryer, ref.RevisionID)
	if err != nil {
		return nil, err
	}
	return &store.DiscardValuesResult{
		Revision: revision, PreviousState: ref.PreviousState, NewState: ref.NewState, DecidedAt: ref.DecidedAt, Replayed: true,
	}, nil
}

func lookupValuesLifecycleReplay(ctx context.Context, queryer operationQueryer, scope, keyHash, requestHash string) (*valuesLifecycleReplayRef, error) {
	var persistedHash string
	var responseRef []byte
	err := queryer.QueryRowContext(ctx, `
		SELECT request_hash, response_ref FROM idempotency_records
		WHERE scope = ? AND text_key = ? AND expires_at > ?
	`, scope, keyHash, time.Now().UTC()).Scan(&persistedHash, &responseRef)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup values lifecycle idempotency: %w", err)
	}
	if persistedHash != requestHash {
		return nil, store.ErrIdempotencyConflict
	}
	var ref valuesLifecycleReplayRef
	if err := json.Unmarshal(responseRef, &ref); err != nil {
		return nil, fmt.Errorf("decode values lifecycle replay: %w", err)
	}
	return &ref, nil
}

func insertCreateValuesIdempotency(ctx context.Context, tx *Tx, command store.CreateValuesDraftCommand) error {
	revision := command.Revision
	if revision != nil {
		snapshot := *revision
		revision = &snapshot
	}
	return insertValuesLifecycleIdempotency(ctx, tx, command.IdempotencyScope, command.IdempotencyKeyHash,
		command.RequestHash, command.IdempotencyExpiresAt, valuesLifecycleReplayRef{RevisionID: command.Revision.ID, Revision: revision})
}

func insertDiscardValuesIdempotency(ctx context.Context, tx *Tx, command store.DiscardValuesCommand, result *store.DiscardValuesResult) error {
	return insertValuesLifecycleIdempotency(ctx, tx, command.IdempotencyScope, command.IdempotencyKeyHash,
		command.RequestHash, command.IdempotencyExpiresAt, valuesLifecycleReplayRef{
			RevisionID: result.Revision.ID, PreviousState: result.PreviousState, NewState: result.NewState, DecidedAt: result.DecidedAt,
		})
}

func insertValuesLifecycleIdempotency(ctx context.Context, tx *Tx, scope, keyHash, requestHash string, expiresAt time.Time, ref valuesLifecycleReplayRef) error {
	if keyHash == "" {
		return nil
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().UTC().Add(24 * time.Hour)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM idempotency_records WHERE scope = ? AND text_key = ? AND expires_at <= ?`, scope, keyHash, time.Now().UTC()); err != nil {
		return fmt.Errorf("delete expired values idempotency: %w", err)
	}
	encoded, err := json.Marshal(ref)
	if err != nil {
		return fmt.Errorf("encode values lifecycle idempotency: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO idempotency_records (scope, text_key, request_hash, response_ref, expires_at)
		VALUES (?, ?, ?, ?, ?)
	`, scope, keyHash, requestHash, encoded, expiresAt.UTC()); err != nil {
		if isUniqueConstraint(err) {
			return store.ErrIdempotencyConflict
		}
		return fmt.Errorf("insert values lifecycle idempotency: %w", err)
	}
	return nil
}

func (s *valuesLifecycleStore) loadCreateValuesReplay(ctx context.Context, command store.CreateValuesDraftCommand) (*store.CreateValuesDraftResult, error) {
	return lookupCreateValuesReplay(ctx, s.gorm, command)
}

func (s *valuesLifecycleStore) loadDiscardValuesReplay(ctx context.Context, command store.DiscardValuesCommand) (*store.DiscardValuesResult, error) {
	return lookupDiscardValuesReplay(ctx, s.gorm, command)
}

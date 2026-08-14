package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/google/uuid"

	"github.com/ndzuki/release-manager/internal/store"
)

const idempotencyTTL = 24 * time.Hour

type valuesApprovalStore struct{ db *sql.DB }

type approvalTransition struct {
	action       store.ValuesDecisionAction
	expectedFrom store.ValuesStatus
	to           store.ValuesStatus
	eventType    string
}

func (s *valuesApprovalStore) Submit(ctx context.Context, command store.ValuesApprovalCommand) (*store.ValuesApprovalResult, error) {
	return s.runTransition(ctx, command, approvalTransition{
		action:       store.ValuesDecisionSubmitted,
		expectedFrom: store.ValuesStatusDraft,
		to:           store.ValuesStatusPendingApproval,
		eventType:    "ValuesRevisionSubmitted",
	})
}

func (s *valuesApprovalStore) Approve(ctx context.Context, command store.ValuesApprovalCommand) (*store.ValuesApprovalResult, error) {
	return s.runTransition(ctx, command, approvalTransition{
		action:       store.ValuesDecisionApproved,
		expectedFrom: store.ValuesStatusPendingApproval,
		to:           store.ValuesStatusApproved,
		eventType:    "ValuesRevisionApproved",
	})
}

func (s *valuesApprovalStore) Reject(ctx context.Context, command store.ValuesApprovalCommand) (*store.ValuesApprovalResult, error) {
	return s.runTransition(ctx, command, approvalTransition{
		action:       store.ValuesDecisionRejected,
		expectedFrom: store.ValuesStatusPendingApproval,
		to:           store.ValuesStatusRejected,
		eventType:    "ValuesRevisionRejected",
	})
}

func (s *valuesApprovalStore) runTransition(
	ctx context.Context,
	command store.ValuesApprovalCommand,
	transition approvalTransition,
) (*store.ValuesApprovalResult, error) {
	var result *store.ValuesApprovalResult
	err := retryBusy(ctx, func() error {
		var err error
		result, err = s.transition(ctx, command, transition)
		return err
	})
	return result, err
}

//nolint:gocyclo // The transaction intentionally keeps the seven required atomic workflow steps in one boundary.
func (s *valuesApprovalStore) transition(
	ctx context.Context,
	command store.ValuesApprovalCommand,
	transition approvalTransition,
) (*store.ValuesApprovalResult, error) {
	if !command.Authorized {
		return nil, store.ErrNotAuthorized
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin values approval transition: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback after Commit is a no-op.
	if err := checkAuthorizationFence(ctx, tx, command.ExpectedAuthorizationVersion); err != nil {
		return nil, err
	}

	if replay, err := lookupIdempotency(ctx, tx, command); err != nil {
		return nil, err
	} else if replay != nil {
		replay.Replayed = true
		return replay, nil
	}

	revision, err := getValues(ctx, tx, command.RevisionID)
	if err != nil {
		return nil, err
	}
	if revision.StateVersion != command.ExpectedStateVersion {
		return nil, &store.StateVersionConflictError{
			Expected: command.ExpectedStateVersion,
			Current:  revision.StateVersion,
		}
	}
	if revision.Status != transition.expectedFrom {
		return nil, &store.InvalidValuesStateError{
			Actual:   revision.Status,
			Expected: transition.expectedFrom,
		}
	}

	now := time.Now().UTC()
	supersededIDs := make([]string, 0)
	if transition.action == store.ValuesDecisionSubmitted {
		if err := ensureNoPendingRevision(ctx, tx, revision); err != nil {
			return nil, err
		}
	}
	if transition.action == store.ValuesDecisionApproved {
		supersededIDs, err = supersedeApprovedRevisions(ctx, tx, revision, now)
		if err != nil {
			return nil, err
		}
	}

	result, err := updateRevisionState(ctx, tx, revision, transition, command.ExpectedStateVersion, now)
	if err != nil {
		return nil, err
	}
	if transition.action == store.ValuesDecisionApproved {
		updated, err := tx.ExecContext(ctx, `
			UPDATE release_definitions SET approved_revision_id = ? WHERE id = ?
		`, revision.ID, revision.ReleaseDefinitionID)
		if err != nil {
			return nil, fmt.Errorf("update approved revision pointer: %w", err)
		}
		rows, err := updated.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("approved revision pointer rows: %w", err)
		}
		if rows != 1 {
			return nil, fmt.Errorf("update approved revision pointer: %w", store.ErrNotFound)
		}
	}

	decision := &store.ValuesRevisionDecision{
		ID:                  uuid.New().String(),
		RevisionID:          revision.ID,
		ReleaseDefinitionID: revision.ReleaseDefinitionID,
		Action:              transition.action,
		FromState:           transition.expectedFrom,
		ToState:             transition.to,
		ActorUserID:         command.ActorUserID,
		ActorOrgID:          command.ActorOrgID,
		ActorRole:           command.ActorRole,
		Comment:             command.Comment,
		Reason:              command.Reason,
		RequestID:           command.RequestID,
		IdempotencyKeyHash:  command.IdempotencyKeyHash,
		CreatedAt:           now,
	}
	if err := insertDecision(ctx, tx, decision); err != nil {
		return nil, err
	}

	auditPayload, notificationPayload, err := successEventPayloads(command, result, supersededIDs, now)
	if err != nil {
		return nil, err
	}
	if err := insertApprovalOutbox(ctx, tx, "audit_outbox", &store.ApprovalOutboxEntry{
		ID: uuid.New().String(), EventType: transition.eventType, PayloadJSON: auditPayload, CreatedAt: now,
	}); err != nil {
		return nil, err
	}
	if err := insertApprovalOutbox(ctx, tx, "notification_outbox", &store.ApprovalOutboxEntry{
		ID: uuid.New().String(), EventType: transition.eventType, PayloadJSON: notificationPayload, CreatedAt: now,
	}); err != nil {
		return nil, err
	}

	approvalResult := &store.ValuesApprovalResult{
		Revision:              result,
		PreviousState:         transition.expectedFrom,
		NewState:              transition.to,
		DecidedAt:             now,
		SupersededRevisionIDs: supersededIDs,
	}
	replay, err := insertIdempotency(ctx, tx, command, approvalResult, now.Add(idempotencyTTL))
	if err != nil {
		return nil, err
	}
	if replay != nil {
		return replay, nil
	}
	if err := checkAuthorizationFence(ctx, tx, command.ExpectedAuthorizationVersion); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit values approval transition: %w", err)
	}
	return approvalResult, nil
}

func lookupIdempotency(
	ctx context.Context,
	tx *sql.Tx,
	command store.ValuesApprovalCommand,
) (*store.ValuesApprovalResult, error) {
	if command.IdempotencyKeyHash == "" {
		return nil, nil
	}
	record, err := loadActiveIdempotencyRecord(
		ctx, tx, command.IdempotencyScope, command.IdempotencyKeyHash, time.Now().UTC(),
	)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup values approval idempotency: %w", err)
	}
	if record.RequestHash != command.RequestHash {
		return nil, store.ErrIdempotencyConflict
	}
	var result store.ValuesApprovalResult
	if err := json.Unmarshal(record.ResponseRef, &result); err != nil {
		return nil, fmt.Errorf("decode values approval replay: %w", err)
	}
	return &result, nil
}
func insertIdempotency(
	ctx context.Context,
	tx *sql.Tx,
	command store.ValuesApprovalCommand,
	result *store.ValuesApprovalResult,
	expiresAt time.Time,
) (*store.ValuesApprovalResult, error) {
	if command.IdempotencyKeyHash == "" {
		return nil, nil
	}
	responseRef, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode values approval response: %w", err)
	}
	existing, created, err := createOrGetIdempotencyRecord(ctx, tx, &store.IdempotencyRecord{
		Scope: command.IdempotencyScope, Key: command.IdempotencyKeyHash,
		RequestHash: command.RequestHash, ResponseRef: responseRef, ExpiresAt: expiresAt,
	}, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if created {
		return nil, nil
	}
	// 并发窗口内另一事务已提交相同 scope+key+hash：decode 已有记录返回重放结果。
	var replay store.ValuesApprovalResult
	if err := json.Unmarshal(existing.ResponseRef, &replay); err != nil {
		return nil, fmt.Errorf("decode values approval replay: %w", err)
	}
	replay.Replayed = true
	return &replay, nil
}

func ensureNoPendingRevision(ctx context.Context, tx *sql.Tx, revision *store.ValuesRevision) error {
	var existingID string
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM values_revisions
		WHERE release_definition_id = ? AND status = 'pending_approval' AND id != ?
		LIMIT 1
	`, revision.ReleaseDefinitionID, revision.ID).Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("query pending values revision: %w", err)
	}
	return &store.ApprovalPendingError{DefinitionID: revision.ReleaseDefinitionID}
}

func supersedeApprovedRevisions(
	ctx context.Context,
	tx *sql.Tx,
	revision *store.ValuesRevision,
	now time.Time,
) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM values_revisions
		WHERE release_definition_id = ? AND status = 'approved' AND id != ?
		ORDER BY version, id
	`, revision.ReleaseDefinitionID, revision.ID)
	if err != nil {
		return nil, fmt.Errorf("list approved values revisions: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan approved values revision id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate approved values revision ids: %w", err)
	}
	if len(ids) == 0 {
		return ids, nil
	}

	updated, err := tx.ExecContext(ctx, `
		UPDATE values_revisions
		SET status = 'superseded', state_version = state_version + 1,
			decided_at = ?, updated_at = ?
		WHERE release_definition_id = ? AND status = 'approved' AND id != ?
	`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), revision.ReleaseDefinitionID, revision.ID)
	if err != nil {
		return nil, fmt.Errorf("supersede approved values revisions: %w", err)
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("supersede approved values revision rows: %w", err)
	}
	if count != int64(len(ids)) {
		return nil, store.ErrOptimisticLock
	}
	return ids, nil
}

func updateRevisionState(
	ctx context.Context,
	tx *sql.Tx,
	revision *store.ValuesRevision,
	transition approvalTransition,
	expectedVersion int64,
	now time.Time,
) (*store.ValuesRevision, error) {
	submittedAt := any(nil)
	decidedAt := any(nil)
	if transition.action == store.ValuesDecisionSubmitted {
		submittedAt = now.Format(time.RFC3339Nano)
	}
	if transition.action == store.ValuesDecisionApproved || transition.action == store.ValuesDecisionRejected {
		decidedAt = now.Format(time.RFC3339Nano)
	}
	updated, err := tx.ExecContext(ctx, `
		UPDATE values_revisions
		SET status = ?, state_version = state_version + 1,
			submitted_at = COALESCE(?, submitted_at), decided_at = COALESCE(?, decided_at), updated_at = ?
		WHERE id = ? AND status = ? AND state_version = ?
	`, string(transition.to), submittedAt, decidedAt, now.Format(time.RFC3339Nano),
		revision.ID, string(transition.expectedFrom), expectedVersion)
	if err != nil {
		if isUniqueConstraint(err) && transition.action == store.ValuesDecisionSubmitted {
			return nil, &store.ApprovalPendingError{DefinitionID: revision.ReleaseDefinitionID}
		}
		return nil, fmt.Errorf("update values revision state: %w", err)
	}
	rows, err := updated.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("update values revision state rows: %w", err)
	}
	if rows != 1 {
		current, currentErr := getValues(ctx, tx, revision.ID)
		if currentErr != nil {
			return nil, currentErr
		}
		return nil, &store.StateVersionConflictError{Expected: expectedVersion, Current: current.StateVersion}
	}
	return getValues(ctx, tx, revision.ID)
}

func insertDecision(ctx context.Context, tx *sql.Tx, decision *store.ValuesRevisionDecision) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO values_revision_decisions (
			id, revision_id, release_definition_id, action, from_state, to_state,
			actor_user_id, actor_org_id, actor_role, comment, reason, request_id,
			idempotency_key_hash, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, decision.ID, decision.RevisionID, decision.ReleaseDefinitionID, string(decision.Action),
		string(decision.FromState), string(decision.ToState), decision.ActorUserID, decision.ActorOrgID,
		string(decision.ActorRole), decision.Comment, decision.Reason, decision.RequestID,
		decision.IdempotencyKeyHash, decision.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert values revision decision: %w", err)
	}
	return nil
}

func successEventPayloads(
	command store.ValuesApprovalCommand,
	revision *store.ValuesRevision,
	supersededIDs []string,
	now time.Time,
) (auditPayload, notificationPayload []byte, err error) {
	base := map[string]any{
		"event_id":                uuid.New().String(),
		"revision_id":             revision.ID,
		"release_definition_id":   revision.ReleaseDefinitionID,
		"organization_id":         command.ActorOrgID,
		"request_id":              command.RequestID,
		"state":                   revision.Status,
		"state_version":           revision.StateVersion,
		"superseded_revision_ids": supersededIDs,
		"occurred_at":             now.Format(time.RFC3339Nano),
	}
	notification := maps.Clone(base)
	switch revision.Status {
	case store.ValuesStatusPendingApproval:
		notification["created_by_user_id"] = revision.CreatedByUserID
		notification["submitted_at"] = now.Format(time.RFC3339Nano)
	case store.ValuesStatusApproved:
		notification["decided_by_user_id"] = command.ActorUserID
		notification["role"] = command.ActorRole
		notification["decided_at"] = now.Format(time.RFC3339Nano)
	case store.ValuesStatusRejected:
		notification["decided_by_user_id"] = command.ActorUserID
		notification["role"] = command.ActorRole
		notification["reason"] = command.Reason
		notification["decided_at"] = now.Format(time.RFC3339Nano)
	}
	audit := maps.Clone(notification)
	audit["actor_user_id"] = command.ActorUserID
	audit["actor_role"] = command.ActorRole
	delete(audit, "reason")
	if command.Comment != nil {
		audit["comment_hash"] = hashApprovalAuditText(*command.Comment)
		audit["comment_length"] = len([]byte(*command.Comment))
	}
	if command.Reason != "" {
		audit["reason_hash"] = hashApprovalAuditText(command.Reason)
		audit["reason_length"] = len([]byte(command.Reason))
	}

	auditPayload, err = json.Marshal(audit)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal values approval audit event: %w", err)
	}
	notificationPayload, err = json.Marshal(notification)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal values approval notification event: %w", err)
	}
	return auditPayload, notificationPayload, nil
}
func hashApprovalAuditText(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func insertApprovalOutbox(
	ctx context.Context,
	execer interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	},
	table string,
	entry *store.ApprovalOutboxEntry,
) error {
	query := "INSERT INTO " + table + " (id, event_type, payload_json, created_at, delivered, delivered_at) VALUES (?, ?, ?, ?, ?, ?)" //nolint:gosec // table is an internal constant selected by callers.
	_, err := execer.ExecContext(ctx, query, entry.ID, entry.EventType, entry.PayloadJSON,
		entry.CreatedAt.Format(time.RFC3339Nano), entry.Delivered, formatOptionalTime(entry.DeliveredAt))
	if err != nil {
		return fmt.Errorf("insert %s: %w", table, err)
	}
	return nil
}

func (s *valuesApprovalStore) RecordAttempt(ctx context.Context, entry *store.ApprovalOutboxEntry) error {
	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	if err := insertApprovalOutbox(ctx, s.db, "audit_outbox", entry); err != nil {
		return fmt.Errorf("record values approval attempt: %w", err)
	}
	return nil
}

func (s *valuesApprovalStore) ListDecisions(ctx context.Context, revisionID string) ([]*store.ValuesRevisionDecision, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, revision_id, release_definition_id, action, from_state, to_state,
			actor_user_id, actor_org_id, actor_role, comment, reason, request_id,
			idempotency_key_hash, created_at
		FROM values_revision_decisions WHERE revision_id = ? ORDER BY created_at, id
	`, revisionID)
	if err != nil {
		return nil, fmt.Errorf("list values revision decisions: %w", err)
	}
	defer rows.Close()

	decisions := make([]*store.ValuesRevisionDecision, 0)
	for rows.Next() {
		var decision store.ValuesRevisionDecision
		var action, fromState, toState, role, createdAt string
		var comment sql.NullString
		if err := rows.Scan(&decision.ID, &decision.RevisionID, &decision.ReleaseDefinitionID,
			&action, &fromState, &toState, &decision.ActorUserID, &decision.ActorOrgID,
			&role, &comment, &decision.Reason, &decision.RequestID, &decision.IdempotencyKeyHash,
			&createdAt); err != nil {
			return nil, fmt.Errorf("scan values revision decision: %w", err)
		}
		decision.Action = store.ValuesDecisionAction(action)
		decision.FromState = store.ValuesStatus(fromState)
		decision.ToState = store.ValuesStatus(toState)
		decision.ActorRole = store.Role(role)
		if comment.Valid {
			decision.Comment = &comment.String
		}
		decision.CreatedAt, err = parseSQLiteTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse values revision decision created_at: %w", err)
		}
		decisions = append(decisions, &decision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate values revision decisions: %w", err)
	}
	return decisions, nil
}

func (s *valuesApprovalStore) ListAuditOutbox(ctx context.Context, revisionID string) ([]*store.ApprovalOutboxEntry, error) {
	return s.listApprovalOutbox(ctx, "audit_outbox", revisionID)
}

func (s *valuesApprovalStore) ListNotificationOutbox(ctx context.Context, revisionID string) ([]*store.ApprovalOutboxEntry, error) {
	return s.listApprovalOutbox(ctx, "notification_outbox", revisionID)
}

func (s *valuesApprovalStore) listApprovalOutbox(
	ctx context.Context,
	table string,
	revisionID string,
) ([]*store.ApprovalOutboxEntry, error) {
	query := "SELECT id, event_type, payload_json, created_at, delivered, delivered_at FROM " + table + " ORDER BY created_at, id" //nolint:gosec // table is an internal constant selected by callers.
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", table, err)
	}
	defer rows.Close()

	entries := make([]*store.ApprovalOutboxEntry, 0)
	for rows.Next() {
		var entry store.ApprovalOutboxEntry
		var createdAt string
		var deliveredAt sql.NullString
		if err := rows.Scan(&entry.ID, &entry.EventType, &entry.PayloadJSON, &createdAt,
			&entry.Delivered, &deliveredAt); err != nil {
			return nil, fmt.Errorf("scan %s: %w", table, err)
		}
		var payload struct {
			RevisionID string `json:"revision_id"`
		}
		if err := json.Unmarshal(entry.PayloadJSON, &payload); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", table, err)
		}
		if payload.RevisionID != revisionID {
			continue
		}
		entry.CreatedAt, err = parseSQLiteTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse %s created_at: %w", table, err)
		}
		if deliveredAt.Valid {
			entry.DeliveredAt, err = parseOptionalTime(deliveredAt.String)
			if err != nil {
				return nil, fmt.Errorf("parse %s delivered_at: %w", table, err)
			}
		}
		entries = append(entries, &entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", table, err)
	}
	return entries, nil
}

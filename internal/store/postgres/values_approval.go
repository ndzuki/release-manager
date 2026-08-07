package postgres

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

const valuesApprovalIdempotencyTTL = 24 * time.Hour

type approvalTransition struct {
	action       store.ValuesDecisionAction
	expectedFrom store.ValuesStatus
	to           store.ValuesStatus
	eventType    string
}

type approvalOutboxTable string

const (
	auditApprovalOutbox        approvalOutboxTable = "audit_outbox"
	notificationApprovalOutbox approvalOutboxTable = "notification_outbox"
)

func (s *valuesApprovalStore) Submit(ctx context.Context, command store.ValuesApprovalCommand) (*store.ValuesApprovalResult, error) {
	return s.transition(ctx, command, approvalTransition{
		action:       store.ValuesDecisionSubmitted,
		expectedFrom: store.ValuesStatusDraft,
		to:           store.ValuesStatusPendingApproval,
		eventType:    "ValuesRevisionSubmitted",
	})
}

func (s *valuesApprovalStore) Approve(ctx context.Context, command store.ValuesApprovalCommand) (*store.ValuesApprovalResult, error) {
	return s.transition(ctx, command, approvalTransition{
		action:       store.ValuesDecisionApproved,
		expectedFrom: store.ValuesStatusPendingApproval,
		to:           store.ValuesStatusApproved,
		eventType:    "ValuesRevisionApproved",
	})
}

func (s *valuesApprovalStore) Reject(ctx context.Context, command store.ValuesApprovalCommand) (*store.ValuesApprovalResult, error) {
	return s.transition(ctx, command, approvalTransition{
		action:       store.ValuesDecisionRejected,
		expectedFrom: store.ValuesStatusPendingApproval,
		to:           store.ValuesStatusRejected,
		eventType:    "ValuesRevisionRejected",
	})
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
	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin values approval transition: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback after Commit is a no-op.
	if err := checkAuthorizationFence(ctx, tx, command.ExpectedAuthorizationVersion); err != nil {
		return nil, err
	}

	if replay, err := lookupValuesApprovalIdempotency(ctx, tx, command); err != nil {
		return nil, err
	} else if replay != nil {
		replay.Replayed = true
		return replay, nil
	}

	revision, err := getValuesApprovalRevision(ctx, tx, command.RevisionID)
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
		if err := ensureNoPendingValuesRevision(ctx, tx, revision); err != nil {
			return nil, err
		}
	}
	if transition.action == store.ValuesDecisionApproved {
		supersededIDs, err = supersedeApprovedValuesRevisions(ctx, tx, revision, now)
		if err != nil {
			return nil, err
		}
	}

	result, err := updateValuesApprovalRevisionState(
		ctx,
		tx,
		revision,
		transition,
		command.ExpectedStateVersion,
		now,
	)
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
	if err := insertValuesRevisionDecision(ctx, tx, decision); err != nil {
		return nil, err
	}

	auditPayload, notificationPayload, err := valuesApprovalSuccessEventPayloads(command, result, supersededIDs, now)
	if err != nil {
		return nil, err
	}
	if err := insertValuesApprovalOutbox(ctx, tx, auditApprovalOutbox, &store.ApprovalOutboxEntry{
		ID: uuid.New().String(), EventType: transition.eventType, PayloadJSON: auditPayload, CreatedAt: now,
	}); err != nil {
		return nil, err
	}
	if err := insertValuesApprovalOutbox(ctx, tx, notificationApprovalOutbox, &store.ApprovalOutboxEntry{
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
	if err := insertValuesApprovalIdempotency(
		ctx,
		tx,
		command,
		approvalResult,
		now.Add(valuesApprovalIdempotencyTTL),
	); err != nil {
		return nil, err
	}
	if err := checkAuthorizationFence(ctx, tx, command.ExpectedAuthorizationVersion); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit values approval transition: %w", err)
	}
	return approvalResult, nil
}

func lookupValuesApprovalIdempotency(
	ctx context.Context,
	tx *Tx,
	command store.ValuesApprovalCommand,
) (*store.ValuesApprovalResult, error) {
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
		return nil, fmt.Errorf("lookup values approval idempotency: %w", err)
	}
	if requestHash != command.RequestHash {
		return nil, store.ErrIdempotencyConflict
	}
	var result store.ValuesApprovalResult
	if err := json.Unmarshal(responseRef, &result); err != nil {
		return nil, fmt.Errorf("decode values approval replay: %w", err)
	}
	return &result, nil
}

func insertValuesApprovalIdempotency(
	ctx context.Context,
	tx *Tx,
	command store.ValuesApprovalCommand,
	result *store.ValuesApprovalResult,
	expiresAt time.Time,
) error {
	if command.IdempotencyKeyHash == "" {
		return nil
	}
	responseRef, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode values approval response: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO idempotency_records (scope, text_key, request_hash, response_ref, expires_at)
		VALUES (?, ?, ?, ?, ?)
	`, command.IdempotencyScope, command.IdempotencyKeyHash, command.RequestHash, responseRef, expiresAt.UTC())
	if err != nil {
		if isUniqueConstraint(err) {
			return store.ErrIdempotencyConflict
		}
		return fmt.Errorf("insert values approval idempotency: %w", err)
	}
	return nil
}

func ensureNoPendingValuesRevision(ctx context.Context, tx *Tx, revision *store.ValuesRevision) error {
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

func supersedeApprovedValuesRevisions(
	ctx context.Context,
	tx *Tx,
	revision *store.ValuesRevision,
	now time.Time,
) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM values_revisions
		WHERE release_definition_id = ? AND status = 'approved' AND id != ?
		ORDER BY revision, id
		FOR UPDATE
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
		SET status = 'superseded', state_version = state_version + 1, version = version + 1,
			decided_at = ?, updated_at = ?
		WHERE release_definition_id = ? AND status = 'approved' AND id != ?
	`, now.UTC(), now.UTC(), revision.ReleaseDefinitionID, revision.ID)
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

func updateValuesApprovalRevisionState(
	ctx context.Context,
	tx *Tx,
	revision *store.ValuesRevision,
	transition approvalTransition,
	expectedVersion int64,
	now time.Time,
) (*store.ValuesRevision, error) {
	var submittedAt any
	var decidedAt any
	if transition.action == store.ValuesDecisionSubmitted {
		submittedAt = now.UTC()
	}
	if transition.action == store.ValuesDecisionApproved || transition.action == store.ValuesDecisionRejected {
		decidedAt = now.UTC()
	}
	updated, err := tx.ExecContext(ctx, `
		UPDATE values_revisions
		SET status = ?, state_version = state_version + 1, version = version + 1,
			submitted_at = COALESCE(CAST(? AS TIMESTAMPTZ), submitted_at),
			decided_at = COALESCE(CAST(? AS TIMESTAMPTZ), decided_at), updated_at = ?
		WHERE id = ? AND status = ? AND state_version = ?
	`, string(transition.to), submittedAt, decidedAt, now.UTC(),
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
		current, currentErr := getValuesApprovalRevision(ctx, tx, revision.ID)
		if currentErr != nil {
			return nil, currentErr
		}
		return nil, &store.StateVersionConflictError{Expected: expectedVersion, Current: current.StateVersion}
	}
	return getValuesApprovalRevision(ctx, tx, revision.ID)
}

func insertValuesRevisionDecision(ctx context.Context, tx *Tx, decision *store.ValuesRevisionDecision) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO values_revision_decisions (
			id, revision_id, release_definition_id, action, from_state, to_state,
			actor_user_id, actor_org_id, actor_role, comment, reason, request_id,
			idempotency_key_hash, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, decision.ID, decision.RevisionID, decision.ReleaseDefinitionID, string(decision.Action),
		string(decision.FromState), string(decision.ToState), decision.ActorUserID, decision.ActorOrgID,
		string(decision.ActorRole), decision.Comment, decision.Reason, decision.RequestID,
		decision.IdempotencyKeyHash, decision.CreatedAt.UTC())
	if err != nil {
		return fmt.Errorf("insert values revision decision: %w", err)
	}
	return nil
}

func valuesApprovalSuccessEventPayloads(
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
		"occurred_at":             now.UTC().Format(time.RFC3339Nano),
	}
	notification := maps.Clone(base)
	switch revision.Status {
	case store.ValuesStatusPendingApproval:
		notification["created_by_user_id"] = revision.CreatedByUserID
		notification["submitted_at"] = now.UTC().Format(time.RFC3339Nano)
	case store.ValuesStatusApproved:
		notification["decided_by_user_id"] = command.ActorUserID
		notification["role"] = command.ActorRole
		notification["decided_at"] = now.UTC().Format(time.RFC3339Nano)
	case store.ValuesStatusRejected:
		notification["decided_by_user_id"] = command.ActorUserID
		notification["role"] = command.ActorRole
		notification["reason"] = command.Reason
		notification["decided_at"] = now.UTC().Format(time.RFC3339Nano)
	}
	audit := maps.Clone(notification)
	audit["actor_user_id"] = command.ActorUserID
	audit["actor_role"] = command.ActorRole
	delete(audit, "reason")
	if command.Comment != nil {
		audit["comment_hash"] = hashValuesApprovalAuditText(*command.Comment)
		audit["comment_length"] = len([]byte(*command.Comment))
	}
	if command.Reason != "" {
		audit["reason_hash"] = hashValuesApprovalAuditText(command.Reason)
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

func hashValuesApprovalAuditText(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

type valuesApprovalExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertValuesApprovalOutbox(
	ctx context.Context,
	execer valuesApprovalExecer,
	table approvalOutboxTable,
	entry *store.ApprovalOutboxEntry,
) error {
	query, err := valuesApprovalOutboxInsertQuery(table)
	if err != nil {
		return err
	}
	_, err = execer.ExecContext(ctx, query, entry.ID, entry.EventType, entry.PayloadJSON,
		entry.CreatedAt.UTC(), entry.Delivered, valuesApprovalOptionalTime(entry.DeliveredAt))
	if err != nil {
		return fmt.Errorf("insert %s: %w", table, err)
	}
	return nil
}

func valuesApprovalOutboxInsertQuery(table approvalOutboxTable) (string, error) {
	switch table {
	case auditApprovalOutbox:
		return `INSERT INTO audit_outbox (id, event_type, payload_json, created_at, delivered, delivered_at) VALUES (?, ?, ?, ?, ?, ?)`, nil
	case notificationApprovalOutbox:
		return `INSERT INTO notification_outbox (id, event_type, payload_json, created_at, delivered, delivered_at) VALUES (?, ?, ?, ?, ?, ?)`, nil
	default:
		return "", fmt.Errorf("unsupported values approval outbox table %q", table)
	}
}

func valuesApprovalOptionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func (s *valuesApprovalStore) RecordAttempt(ctx context.Context, entry *store.ApprovalOutboxEntry) error {
	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	if err := insertValuesApprovalOutbox(ctx, s.gorm, auditApprovalOutbox, entry); err != nil {
		return fmt.Errorf("record values approval attempt: %w", err)
	}
	return nil
}

func (s *valuesApprovalStore) ListDecisions(ctx context.Context, revisionID string) ([]*store.ValuesRevisionDecision, error) {
	rows, err := s.gorm.QueryContext(ctx, `
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
		var action, fromState, toState, role string
		var comment sql.NullString
		if err := rows.Scan(&decision.ID, &decision.RevisionID, &decision.ReleaseDefinitionID,
			&action, &fromState, &toState, &decision.ActorUserID, &decision.ActorOrgID,
			&role, &comment, &decision.Reason, &decision.RequestID, &decision.IdempotencyKeyHash,
			&decision.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan values revision decision: %w", err)
		}
		decision.Action = store.ValuesDecisionAction(action)
		decision.FromState = store.ValuesStatus(fromState)
		decision.ToState = store.ValuesStatus(toState)
		decision.ActorRole = store.Role(role)
		decision.CreatedAt = decision.CreatedAt.UTC()
		if comment.Valid {
			value := comment.String
			decision.Comment = &value
		}
		decisions = append(decisions, &decision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate values revision decisions: %w", err)
	}
	return decisions, nil
}

func (s *valuesApprovalStore) ListAuditOutbox(ctx context.Context, revisionID string) ([]*store.ApprovalOutboxEntry, error) {
	return s.listValuesApprovalOutbox(ctx, auditApprovalOutbox, revisionID)
}

func (s *valuesApprovalStore) ListNotificationOutbox(ctx context.Context, revisionID string) ([]*store.ApprovalOutboxEntry, error) {
	return s.listValuesApprovalOutbox(ctx, notificationApprovalOutbox, revisionID)
}

func (s *valuesApprovalStore) listValuesApprovalOutbox(
	ctx context.Context,
	table approvalOutboxTable,
	revisionID string,
) ([]*store.ApprovalOutboxEntry, error) {
	query, err := valuesApprovalOutboxSelectQuery(table)
	if err != nil {
		return nil, err
	}
	rows, err := s.gorm.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", table, err)
	}
	defer rows.Close()

	entries := make([]*store.ApprovalOutboxEntry, 0)
	for rows.Next() {
		var entry store.ApprovalOutboxEntry
		var deliveredAt sql.NullTime
		if err := rows.Scan(&entry.ID, &entry.EventType, &entry.PayloadJSON, &entry.CreatedAt,
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
		entry.CreatedAt = entry.CreatedAt.UTC()
		if deliveredAt.Valid {
			value := deliveredAt.Time.UTC()
			entry.DeliveredAt = &value
		}
		entries = append(entries, &entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", table, err)
	}
	return entries, nil
}

func valuesApprovalOutboxSelectQuery(table approvalOutboxTable) (string, error) {
	switch table {
	case auditApprovalOutbox:
		return `SELECT id, event_type, payload_json, created_at, delivered, delivered_at FROM audit_outbox ORDER BY created_at, id`, nil
	case notificationApprovalOutbox:
		return `SELECT id, event_type, payload_json, created_at, delivered, delivered_at FROM notification_outbox ORDER BY created_at, id`, nil
	default:
		return "", fmt.Errorf("unsupported values approval outbox table %q", table)
	}
}

const valuesApprovalRevisionSelect = `
	SELECT id, release_definition_id, revision, state_version, status, "values",
		digest, parent_revision_id, secret_refs, created_by_user_id,
		submitted_at, decided_at, created_at, updated_at
	FROM values_revisions`

func getValuesApprovalRevision(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (*store.ValuesRevision, error) {
	return scanValues(q.QueryRowContext(ctx, valuesApprovalRevisionSelect+` WHERE id = ?`, id))
}

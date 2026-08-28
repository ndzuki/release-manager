package postgres

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

type operatorManagementStore struct{ gorm *DB }

var _ store.OperatorManagementStore = (*operatorManagementStore)(nil)

func (s *operatorManagementStore) CreateEnrollmentToken(
	ctx context.Context,
	token *store.EnrollmentToken,
	replacePending bool,
	auditEvent *store.AuditEvent,
) (*store.EnrollmentTokenMutation, error) {
	if token == nil {
		return nil, errors.New("create enrollment token: token is required")
	}
	prepareEnrollmentToken(token)
	return s.createEnrollmentToken(ctx, token, replacePending, auditEvent)
}

//nolint:gocyclo // The transaction keeps expiry, replacement, audit, and rollback gates explicit.
func (s *operatorManagementStore) createEnrollmentToken(
	ctx context.Context,
	token *store.EnrollmentToken,
	replacePending bool,
	auditEvent *store.AuditEvent,
) (*store.EnrollmentTokenMutation, error) {
	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create enrollment token: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after successful commit

	var previousID, previousExpiresAt string
	err = tx.QueryRowContext(ctx, `
SELECT id, expires_at
FROM enrollment_tokens
WHERE customer_id = ? AND cluster_id = ? AND state = 'pending'
ORDER BY created_at DESC, id DESC
LIMIT 1
`, token.CustomerID, token.ClusterID).Scan(&previousID, &previousExpiresAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("query pending enrollment token: %w", err)
	}
	if err == nil {
		expiresAt, parseErr := time.Parse(time.RFC3339Nano, previousExpiresAt)
		if parseErr != nil {
			return nil, fmt.Errorf("parse pending enrollment token expiry: %w", parseErr)
		}
		if !time.Now().UTC().Before(expiresAt) {
			if _, updateErr := tx.ExecContext(ctx, `UPDATE enrollment_tokens SET state = 'expired' WHERE id = ? AND state = 'pending'`, previousID); updateErr != nil {
				return nil, fmt.Errorf("expire pending enrollment token: %w", updateErr)
			}
			previousID = ""
		} else if !replacePending {
			return nil, store.ErrPendingTokenExists
		}
	}

	if previousID != "" {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		result, updateErr := tx.ExecContext(ctx, `
UPDATE enrollment_tokens
SET state = 'revoked', revoked_at = ?, replaced_by_id = ?
WHERE id = ? AND state = 'pending'
`, now, token.ID, previousID)
		if updateErr != nil {
			return nil, fmt.Errorf("revoke replaced enrollment token: %w", updateErr)
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return nil, fmt.Errorf("count replaced enrollment token: %w", rowsErr)
		}
		if rows != 1 {
			return nil, store.ErrTokenReplaceConflict
		}
	}

	if err := insertEnrollmentToken(ctx, tx, token); err != nil {
		if isUniqueConstraint(err) {
			if replacePending {
				return nil, store.ErrTokenReplaceConflict
			}
			return nil, store.ErrPendingTokenExists
		}
		return nil, err
	}
	if err := insertOperatorAuditEvent(ctx, tx, auditEvent); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create enrollment token: %w", err)
	}
	return &store.EnrollmentTokenMutation{Token: token, PreviousID: previousID, Changed: true}, nil
}

//nolint:gocyclo // The transaction keeps idempotent terminal states and audit rollback explicit.
func (s *operatorManagementStore) RevokePendingEnrollmentToken(
	ctx context.Context,
	customerID string,
	clusterID string,
	auditEvent *store.AuditEvent,
) (*store.EnrollmentTokenMutation, error) {
	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin revoke pending enrollment token: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after successful commit

	token, err := scanEnrollmentToken(tx.QueryRowContext(ctx, `SELECT `+enrollmentTokenSelect+`
FROM enrollment_tokens
WHERE customer_id = ? AND cluster_id = ? AND state = 'pending'
ORDER BY created_at DESC, id DESC
LIMIT 1
`, customerID, clusterID))
	if errors.Is(err, store.ErrNotFound) {
		if err := insertOperatorAuditEvent(ctx, tx, auditEvent); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit idempotent revoke pending enrollment token: %w", err)
		}
		return &store.EnrollmentTokenMutation{Changed: false}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query pending enrollment token: %w", err)
	}

	now := time.Now().UTC()
	if !now.Before(token.ExpiresAt) {
		result, updateErr := tx.ExecContext(ctx, `
UPDATE enrollment_tokens
SET state = 'expired'
WHERE id = ? AND state = 'pending'
`, token.ID)
		if updateErr != nil {
			return nil, fmt.Errorf("expire pending enrollment token: %w", updateErr)
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return nil, fmt.Errorf("count expired enrollment token: %w", rowsErr)
		}
		token.State = store.TokenStateExpired
		if err := insertOperatorAuditEvent(ctx, tx, auditEvent); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit expired pending enrollment token: %w", err)
		}
		return &store.EnrollmentTokenMutation{Token: token, Changed: rows == 1}, nil
	}
	result, err := tx.ExecContext(ctx, `
UPDATE enrollment_tokens
SET state = 'revoked', revoked_at = ?
WHERE id = ? AND state = 'pending'
`, now.Format(time.RFC3339Nano), token.ID)
	if err != nil {
		return nil, fmt.Errorf("revoke pending enrollment token: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("count revoked enrollment token: %w", err)
	}
	if rows != 1 {
		return &store.EnrollmentTokenMutation{Token: token, Changed: false}, nil
	}

	token.State = store.TokenStateRevoked
	token.RevokedAt = &now
	if err := insertOperatorAuditEvent(ctx, tx, auditEvent); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit revoke pending enrollment token: %w", err)
	}
	return &store.EnrollmentTokenMutation{Token: token, Changed: true}, nil
}

//nolint:gocyclo // Enrollment atomically validates and mutates token, identity, session, and supersession state.
func (s *operatorManagementStore) EnrollOperator(
	ctx context.Context,
	tokenID string,
	op *store.Operator,
	session *store.Session,
) (*store.OperatorEnrollment, error) {
	if tokenID == "" || op == nil || session == nil {
		return nil, errors.New("enroll operator: token, operator, and session are required")
	}
	if op.CustomerID == "" || op.ClusterID == "" || op.Name == "" {
		return nil, errors.New("enroll operator: operator scope and name are required")
	}
	prepareSession(session)
	session.OperatorID = op.ID
	session.CustomerID = op.CustomerID
	session.ClusterID = op.ClusterID
	op.RegisteredAt = session.StartedAt
	op.UpdatedAt = session.StartedAt
	if op.Status == "" {
		op.Status = store.OperatorActive
	}
	capabilities, err := json.Marshal(session.Capabilities)
	if err != nil {
		return nil, fmt.Errorf("marshal enrollment session capabilities: %w", err)
	}

	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin enroll operator: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after successful commit

	var tokenCustomerID, tokenClusterID, tokenName, tokenState, expiresAt string
	if err := tx.QueryRowContext(ctx, `
SELECT customer_id, cluster_id, operator_name, state, expires_at
FROM enrollment_tokens
WHERE id = ?
`, tokenID).Scan(&tokenCustomerID, &tokenClusterID, &tokenName, &tokenState, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("query enrollment token: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("parse enrollment token expiry: %w", err)
	}
	if tokenState != string(store.TokenStatePending) || time.Now().UTC().After(expires) {
		return nil, store.ErrOperatorStateConflict
	}
	if tokenCustomerID != op.CustomerID || tokenClusterID != op.ClusterID || tokenName != op.Name {
		return nil, store.ErrNotAuthorized
	}
	// Consume the token FIRST (REQ-015 事务边界 1; v2 checkpoint 重排序):
	// the pending→used CAS is the transaction's first mutation, so the
	// concurrent loser of the same token fails here with a state conflict
	// instead of a confusing duplicate-key error further down.
	tokenCAS, err := tx.ExecContext(ctx, `
UPDATE enrollment_tokens
SET state = 'used', used_at = ?, operator_id = ?
WHERE id = ? AND state = 'pending'
`, session.StartedAt.UTC().Format(time.RFC3339Nano), op.ID, tokenID)
	if err != nil {
		return nil, fmt.Errorf("consume enrollment token: %w", err)
	}
	tokenRows, err := tokenCAS.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("consume enrollment token rows affected: %w", err)
	}
	if tokenRows != 1 {
		return nil, store.ErrOperatorStateConflict
	}

	var supersededID string
	existing, existingErr := scanOperator(tx.QueryRowContext(ctx, `SELECT `+operatorSelect+`
FROM operators
WHERE cluster_id = ? AND status = 'active'
`, op.ClusterID))
	if existingErr != nil && !errors.Is(existingErr, store.ErrNotFound) {
		return nil, fmt.Errorf("query active operator for enrollment: %w", existingErr)
	}
	if existingErr == nil {
		supersededID = existing.ID
		now := session.StartedAt.UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `
UPDATE operators
SET status = 'superseded', superseded_by = ?, superseded_at = ?, updated_at = ?
WHERE id = ? AND status = 'active'
`, op.ID, now, now, existing.ID); err != nil {
			return nil, fmt.Errorf("supersede active operator: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE sessions
SET status = 'revoked', status_reason = ?, closed_at = ?
WHERE operator_id = ? AND status != 'revoked'
`, string(store.SessionReasonOperatorSuperseded), now, existing.ID); err != nil {
			return nil, fmt.Errorf("revoke superseded operator sessions: %w", err)
		}
	}

	var certExpiresAt any
	if op.CertificateExpiresAt != nil {
		certExpiresAt = op.CertificateExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO operators (
	id, customer_id, cluster_id, operator_name, cert_serial, certificate_expires_at, status,
	superseded_by, superseded_at, revoked_at, revoke_reason, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, '', NULL, NULL, '', ?, ?)
`, op.ID, op.CustomerID, op.ClusterID, op.Name, op.CertSerial, certExpiresAt, string(op.Status),
		op.RegisteredAt.UTC().Format(time.RFC3339Nano), op.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		if isUniqueConstraint(err) {
			return nil, store.ErrDuplicateOperatorName
		}
		return nil, fmt.Errorf("insert enrolled operator: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO sessions (
	id, operator_id, customer_id, cluster_id, instance_id, version, capabilities, active_config_version,
	status, status_reason, started_at, last_heartbeat, expires_at, closed_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, NULL)
`, session.ID, session.OperatorID, session.CustomerID, session.ClusterID, session.InstanceID,
		session.Version, string(capabilities), session.ActiveConfigVersion, string(session.Status),
		session.StartedAt.UTC().Format(time.RFC3339Nano), session.LastHeartbeat.UTC().Format(time.RFC3339Nano),
		session.ExpiresAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return nil, fmt.Errorf("insert enrollment session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit enroll operator: %w", err)
	}
	return &store.OperatorEnrollment{Operator: op, Session: session, SupersededOperatorID: supersededID}, nil
}

//nolint:gocyclo // Revocation atomically preserves first-write metadata, session state, and audit evidence.
func (s *operatorManagementStore) RevokeOperator(
	ctx context.Context,
	customerID string,
	clusterID string,
	operatorID string,
	reason string,
	auditEvent *store.AuditEvent,
) (*store.OperatorRevocation, error) {
	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin revoke operator: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after successful commit

	op, err := scanOperator(tx.QueryRowContext(ctx, `SELECT `+operatorSelect+`
FROM operators
WHERE id = ? AND customer_id = ? AND cluster_id = ?
`, operatorID, customerID, clusterID))
	if errors.Is(err, store.ErrNotFound) {
		return nil, store.ErrOperatorNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query operator for revoke: %w", err)
	}

	latestSession, sessionErr := scanSession(tx.QueryRowContext(ctx, sessionSelect+`
 WHERE operator_id = ?
 ORDER BY started_at DESC, id DESC
 LIMIT 1
`, operatorID))
	if sessionErr != nil && !errors.Is(sessionErr, store.ErrNotFound) {
		return nil, fmt.Errorf("query operator session for revoke: %w", sessionErr)
	}
	if errors.Is(sessionErr, store.ErrNotFound) {
		latestSession = nil
	}

	if op.Status == store.OperatorRevoked {
		if err := insertOperatorAuditEvent(ctx, tx, auditEvent); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit idempotent revoke operator: %w", err)
		}
		return &store.OperatorRevocation{Operator: op, Session: latestSession, Changed: false}, nil
	}

	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `
UPDATE operators
SET status = 'revoked', revoked_at = ?, revoke_reason = ?, updated_at = ?
WHERE id = ? AND customer_id = ? AND cluster_id = ? AND status != 'revoked'
`, now.Format(time.RFC3339Nano), reason, now.Format(time.RFC3339Nano), operatorID, customerID, clusterID)
	if err != nil {
		return nil, fmt.Errorf("update revoked operator: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("count revoked operator: %w", err)
	}
	if rows != 1 {
		return nil, store.ErrOperatorStateConflict
	}

	sessionReason := store.SessionReasonCertRevoked
	if op.Status == store.OperatorSuperseded {
		sessionReason = store.SessionReasonOperatorSuperseded
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE sessions
SET status = 'revoked', status_reason = ?, closed_at = ?
WHERE operator_id = ? AND status != 'revoked'
`, string(sessionReason), now.Format(time.RFC3339Nano), operatorID); err != nil {
		return nil, fmt.Errorf("revoke operator sessions: %w", err)
	}

	op.Status = store.OperatorRevoked
	op.RevokedAt = &now
	op.RevokeReason = reason
	op.UpdatedAt = now
	if latestSession != nil {
		latestSession.Status = store.SessionRevoked
		latestSession.StatusReason = &sessionReason
		latestSession.ClosedAt = &now
	}
	if err := insertOperatorAuditEvent(ctx, tx, auditEvent); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit revoke operator: %w", err)
	}
	return &store.OperatorRevocation{Operator: op, Session: latestSession, Changed: true}, nil
}

func insertOperatorAuditEvent(ctx context.Context, tx *Tx, event *store.AuditEvent) error {
	if event == nil {
		event = &store.AuditEvent{
			ID:             uuid.NewString(),
			ActorKind:      store.AuditActorSystem,
			ActorID:        "system",
			OrganizationID: "system",
			Role:           "system",
			ResourceType:   "operator",
			Action:         "operator.revoked",
			Status:         "succeeded",
		}
	}
	if event.ID == "" || event.ActorID == "" || event.OrganizationID == "" || event.Action == "" {
		return store.ErrAuditUnavailable
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("marshal operator audit metadata: %w", err)
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO audit_events (
	id, actor_kind, actor_id, organization_id, role, resource_type, resource_id,
	action, status, duration_ms, change_summary, metadata, created_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		event.ID,
		string(event.ActorKind),
		event.ActorID,
		event.OrganizationID,
		event.Role,
		event.ResourceType,
		event.ResourceID,
		event.Action,
		event.Status,
		event.DurationMs,
		event.ChangeSummary,
		string(metadata),
		event.CreatedAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("insert operator audit event: %w", err)
	}
	return nil
}

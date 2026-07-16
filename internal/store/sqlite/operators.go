package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type enrollmentTokenStore struct{ db *sql.DB }

func (s *enrollmentTokenStore) Create(ctx context.Context, t *store.EnrollmentToken) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO enrollment_tokens (id, customer_id, cluster_id, token, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?)
`,
		t.ID, t.CustomerID, t.ClusterID, t.Token,
		t.CreatedAt.UTC().Format(time.RFC3339), t.ExpiresAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert enrollment token: %w", err)
	}
	return nil
}

func (s *enrollmentTokenStore) GetByToken(ctx context.Context, token string) (*store.EnrollmentToken, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, customer_id, cluster_id, token, created_at, expires_at, used, used_at, operator_id
FROM enrollment_tokens WHERE token = ?
`, token)
	return scanEnrollmentToken(row)
}

func (s *enrollmentTokenStore) MarkUsed(ctx context.Context, id, operatorID string) error {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
UPDATE enrollment_tokens SET used=1, used_at=?, operator_id=? WHERE id=? AND used=0
`,
		now.Format(time.RFC3339), operatorID, id,
	)
	if err != nil {
		return fmt.Errorf("mark enrollment token used: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func scanEnrollmentToken(row interface{ Scan(...interface{}) error }) (*store.EnrollmentToken, error) {
	var (
		id, customerID, clusterID, token, createdAt, expiresAt string
		used                                                  bool
		usedAt, operatorID                                    *string
	)
	if err := row.Scan(&id, &customerID, &clusterID, &token, &createdAt, &expiresAt, &used, &usedAt, &operatorID); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan enrollment token: %w", err)
	}

	ct, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse token created_at: %w", err)
	}
	et, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("parse token expires_at: %w", err)
	}

	st := &store.EnrollmentToken{
		ID:         id,
		CustomerID: customerID,
		ClusterID:  clusterID,
		Token:      token,
		CreatedAt:  ct,
		ExpiresAt:  et,
		Used:       used,
	}

	if usedAt != nil {
		t, err := time.Parse(time.RFC3339, *usedAt)
		if err != nil {
			return nil, fmt.Errorf("parse token used_at: %w", err)
		}
		st.UsedAt = &t
	}
	if operatorID != nil {
		st.OperatorID = *operatorID
	}

	return st, nil
}

// ---------------------------------------------------------------------------
// Operator store
// ---------------------------------------------------------------------------

type operatorStore struct{ db *sql.DB }

func (s *operatorStore) Create(ctx context.Context, op *store.Operator) error {
	if op.CreatedAt.IsZero() {
		op.CreatedAt = time.Now().UTC()
	}
	if op.UpdatedAt.IsZero() {
		op.UpdatedAt = op.CreatedAt
	}
	if op.Status == "" {
		op.Status = store.OperatorActive
	}

	var revokedAt *string
	if op.RevokedAt != nil {
		v := op.RevokedAt.UTC().Format(time.RFC3339)
		revokedAt = &v
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO operators (id, customer_id, cluster_id, cert_serial, status, superseded_by, revoked_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		op.ID, op.CustomerID, op.ClusterID, op.CertSerial, string(op.Status),
		op.SupersededBy, revokedAt,
		op.CreatedAt.UTC().Format(time.RFC3339), op.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert operator: %w", err)
	}
	return nil
}

func (s *operatorStore) Get(ctx context.Context, id string) (*store.Operator, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, customer_id, cluster_id, cert_serial, status, superseded_by, revoked_at, created_at, updated_at
FROM operators WHERE id = ?
`, id)
	return scanOperator(row)
}

func (s *operatorStore) GetByCertSerial(ctx context.Context, serial string) (*store.Operator, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, customer_id, cluster_id, cert_serial, status, superseded_by, revoked_at, created_at, updated_at
FROM operators WHERE cert_serial = ?
`, serial)
	return scanOperator(row)
}

func (s *operatorStore) Update(ctx context.Context, op *store.Operator) error {
	op.UpdatedAt = time.Now().UTC()

	var revokedAt *string
	if op.RevokedAt != nil {
		v := op.RevokedAt.UTC().Format(time.RFC3339)
		revokedAt = &v
	}

	_, err := s.db.ExecContext(ctx, `
UPDATE operators SET cert_serial=?, status=?, superseded_by=?, revoked_at=?, updated_at=?
WHERE id=?
`,
		op.CertSerial, string(op.Status), op.SupersededBy, revokedAt,
		op.UpdatedAt.UTC().Format(time.RFC3339), op.ID,
	)
	if err != nil {
		return fmt.Errorf("update operator: %w", err)
	}
	return nil
}

func scanOperator(row interface{ Scan(...interface{}) error }) (*store.Operator, error) {
	var (
		id, customerID, clusterID, certSerial, status, supersededBy string
		revokedAt                                                   *string
		createdAt, updatedAt                                        string
	)
	if err := row.Scan(&id, &customerID, &clusterID, &certSerial, &status, &supersededBy, &revokedAt, &createdAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan operator: %w", err)
	}

	ct, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse operator created_at: %w", err)
	}
	ut, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse operator updated_at: %w", err)
	}

	op := &store.Operator{
		ID:           id,
		CustomerID:   customerID,
		ClusterID:    clusterID,
		CertSerial:   certSerial,
		Status:       store.OperatorStatus(status),
		SupersededBy: supersededBy,
		CreatedAt:    ct,
		UpdatedAt:    ut,
	}
	if revokedAt != nil {
		t, err := time.Parse(time.RFC3339, *revokedAt)
		if err != nil {
			return nil, fmt.Errorf("parse operator revoked_at: %w", err)
		}
		op.RevokedAt = &t
	}
	return op, nil
}

// ---------------------------------------------------------------------------
// Session store
// ---------------------------------------------------------------------------

type sessionStore struct{ db *sql.DB }

func (s *sessionStore) Create(ctx context.Context, sess *store.Session) error {
	if sess.StartedAt.IsZero() {
		sess.StartedAt = time.Now().UTC()
	}
	if sess.LastHeartbeat.IsZero() {
		sess.LastHeartbeat = sess.StartedAt
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (id, operator_id, status, started_at, last_heartbeat, expires_at)
VALUES (?, ?, ?, ?, ?, ?)
`,
		sess.ID, sess.OperatorID, string(sess.Status),
		sess.StartedAt.UTC().Format(time.RFC3339), sess.LastHeartbeat.UTC().Format(time.RFC3339),
		sess.ExpiresAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

func (s *sessionStore) Get(ctx context.Context, id string) (*store.Session, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, operator_id, status, started_at, last_heartbeat, expires_at
FROM sessions WHERE id = ?
`, id)
	return scanSession(row)
}

func (s *sessionStore) Heartbeat(ctx context.Context, id string) error {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
UPDATE sessions SET last_heartbeat=? WHERE id=?
`, now.Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("heartbeat session: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *sessionStore) UpdateStatus(ctx context.Context, id string, status store.SessionStatus) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE sessions SET status=? WHERE id=?
`, string(status), id)
	if err != nil {
		return fmt.Errorf("update session status: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *sessionStore) GetActiveByOperator(ctx context.Context, operatorID string) (*store.Session, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, operator_id, status, started_at, last_heartbeat, expires_at
FROM sessions WHERE operator_id=? AND status='online' ORDER BY started_at DESC LIMIT 1
`, operatorID)
	return scanSession(row)
}

func (s *sessionStore) ListExpiredSuspect(ctx context.Context, suspectAfter time.Duration) ([]*store.Session, error) {
	threshold := time.Now().UTC().Add(-suspectAfter).Format(time.RFC3339)

	rows, err := s.db.QueryContext(ctx, `
SELECT id, operator_id, status, started_at, last_heartbeat, expires_at
FROM sessions WHERE status IN ('online', 'suspect') AND last_heartbeat < ?
`, threshold)
	if err != nil {
		return nil, fmt.Errorf("list expired sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*store.Session
	for rows.Next() {
		var (
			id, operatorID, status, startedAt, lastHeartbeat, expiresAt string
		)
		if err := rows.Scan(&id, &operatorID, &status, &startedAt, &lastHeartbeat, &expiresAt); err != nil {
			return nil, fmt.Errorf("scan expired session: %w", err)
		}

		st, err := time.Parse(time.RFC3339, startedAt)
		if err != nil {
			return nil, fmt.Errorf("parse session started_at: %w", err)
		}
		lh, err := time.Parse(time.RFC3339, lastHeartbeat)
		if err != nil {
			return nil, fmt.Errorf("parse session last_heartbeat: %w", err)
		}
		ex, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			return nil, fmt.Errorf("parse session expires_at: %w", err)
		}

		sessions = append(sessions, &store.Session{
			ID:            id,
			OperatorID:    operatorID,
			Status:        store.SessionStatus(status),
			StartedAt:     st,
			LastHeartbeat: lh,
			ExpiresAt:     ex,
		})
	}
	return sessions, rows.Err()
}

func scanSession(row interface{ Scan(...interface{}) error }) (*store.Session, error) {
	var (
		id, operatorID, status, startedAt, lastHeartbeat, expiresAt string
	)
	if err := row.Scan(&id, &operatorID, &status, &startedAt, &lastHeartbeat, &expiresAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan session: %w", err)
	}

	st, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return nil, fmt.Errorf("parse session started_at: %w", err)
	}
	lh, err := time.Parse(time.RFC3339, lastHeartbeat)
	if err != nil {
		return nil, fmt.Errorf("parse session last_heartbeat: %w", err)
	}
	ex, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("parse session expires_at: %w", err)
	}

	return &store.Session{
		ID:            id,
		OperatorID:    operatorID,
		Status:        store.SessionStatus(status),
		StartedAt:     st,
		LastHeartbeat: lh,
		ExpiresAt:     ex,
	}, nil
}

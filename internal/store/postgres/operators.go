package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type enrollmentTokenStore struct{ gorm *DB }

func (s *enrollmentTokenStore) Create(ctx context.Context, t *store.EnrollmentToken) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}

	// Compute SHA-256 hash of the raw token.
	if t.TokenHash == "" && t.Token != "" {
		h := sha256.Sum256([]byte(t.Token))
		t.TokenHash = hex.EncodeToString(h[:])
	}

	_, err := s.gorm.ExecContext(ctx, `
INSERT INTO enrollment_tokens (id, customer_id, cluster_id, token, token_hash, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
`,
		t.ID, t.CustomerID, t.ClusterID, t.Token, t.TokenHash,
		t.CreatedAt.UTC().Format(time.RFC3339), t.ExpiresAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert enrollment token: %w", err)
	}
	return nil
}
func (s *enrollmentTokenStore) GetByToken(ctx context.Context, token string) (*store.EnrollmentToken, error) {
	tokenHash := sha256Hex(token)
	row := s.gorm.QueryRowContext(ctx, `
SELECT id, customer_id, cluster_id, token, token_hash, created_at, expires_at, used, used_at, operator_id
FROM enrollment_tokens WHERE token_hash = ?
`, tokenHash)
	return scanEnrollmentToken(row)
}

func (s *enrollmentTokenStore) MarkUsed(ctx context.Context, id, operatorID string) error {
	now := time.Now().UTC()
	result, err := s.gorm.ExecContext(ctx, `
UPDATE enrollment_tokens SET used=TRUE, used_at=?, operator_id=? WHERE id=? AND used=FALSE
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
		id, customerID, clusterID, token, tokenHash, createdAt, expiresAt string
		used                                                              bool
		usedAt, operatorID                                                *string
	)
	if err := row.Scan(&id, &customerID, &clusterID, &token, &tokenHash, &createdAt, &expiresAt, &used, &usedAt, &operatorID); err != nil {
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
		TokenHash:  tokenHash,
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

type operatorStore struct{ gorm *DB }

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

	_, err := s.gorm.ExecContext(ctx, `
INSERT INTO operators (id, customer_id, cluster_id, operator_name, cert_serial, status, superseded_by, revoked_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		op.ID, op.CustomerID, op.ClusterID, op.Name, op.CertSerial, string(op.Status),
		op.SupersededBy, revokedAt,
		op.CreatedAt.UTC().Format(time.RFC3339), op.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert operator: %w", err)
	}
	return nil
}

func (s *operatorStore) Get(ctx context.Context, id string) (*store.Operator, error) {
	row := s.gorm.QueryRowContext(ctx, `
SELECT id, operator_name, customer_id, cluster_id, cert_serial, status, superseded_by, revoked_at, created_at, updated_at
FROM operators WHERE id = ?
`, id)
	return scanOperator(row)
}

func (s *operatorStore) GetByCertSerial(ctx context.Context, serial string) (*store.Operator, error) {
	row := s.gorm.QueryRowContext(ctx, `
SELECT id, operator_name, customer_id, cluster_id, cert_serial, status, superseded_by, revoked_at, created_at, updated_at
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

	_, err := s.gorm.ExecContext(ctx, `
UPDATE operators SET operator_name=?, cert_serial=?, status=?, superseded_by=?, revoked_at=?, updated_at=?
WHERE id=?
`,
		op.Name, op.CertSerial, string(op.Status), op.SupersededBy, revokedAt,
		op.UpdatedAt.UTC().Format(time.RFC3339), op.ID,
	)
	if err != nil {
		return fmt.Errorf("update operator: %w", err)
	}
	return nil
}

// GetByClusterID returns the active operator for a cluster, if any.
func (s *operatorStore) GetByClusterID(ctx context.Context, clusterID string) (*store.Operator, error) {
	row := s.gorm.QueryRowContext(ctx, `
SELECT id, operator_name, customer_id, cluster_id, cert_serial, status, superseded_by, revoked_at, created_at, updated_at
FROM operators WHERE cluster_id = ? AND status = ?
`, clusterID, string(store.OperatorActive))
	return scanOperator(row)
}

// GetByName returns the operator with the given name, regardless of status.
func (s *operatorStore) GetByName(ctx context.Context, name string) (*store.Operator, error) {
	row := s.gorm.QueryRowContext(ctx, `
SELECT id, operator_name, customer_id, cluster_id, cert_serial, status, superseded_by, revoked_at, created_at, updated_at
FROM operators WHERE operator_name = ?
`, name)
	return scanOperator(row)
}

// Revoke marks an operator as revoked with a timestamp.
func (s *operatorStore) Revoke(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.gorm.ExecContext(ctx, `
UPDATE operators SET status=?, revoked_at=?, updated_at=? WHERE id=?
`, string(store.OperatorRevoked), now, now, id)
	if err != nil {
		return fmt.Errorf("revoke operator: %w", err)
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

// ListByCustomer returns all operators for a customer.
func (s *operatorStore) ListByCustomer(ctx context.Context, customerID string) ([]*store.Operator, error) {
	rows, err := s.gorm.QueryContext(ctx, `
SELECT id, operator_name, customer_id, cluster_id, cert_serial, status, superseded_by, revoked_at, created_at, updated_at
FROM operators WHERE customer_id = ?
`, customerID)
	if err != nil {
		return nil, fmt.Errorf("list operators by customer: %w", err)
	}
	defer rows.Close()
	return scanOperators(rows)
}

// ListByCluster returns all operators for a cluster.
func (s *operatorStore) ListByCluster(ctx context.Context, clusterID string) ([]*store.Operator, error) {
	rows, err := s.gorm.QueryContext(ctx, `
SELECT id, operator_name, customer_id, cluster_id, cert_serial, status, superseded_by, revoked_at, created_at, updated_at
FROM operators WHERE cluster_id = ?
`, clusterID)
	if err != nil {
		return nil, fmt.Errorf("list operators by cluster: %w", err)
	}
	defer rows.Close()
	return scanOperators(rows)
}

func scanOperator(row interface{ Scan(...interface{}) error }) (*store.Operator, error) {
	var (
		id, name, customerID, clusterID, certSerial, status, supersededBy string
		revokedAt                                                         *string
		createdAt, updatedAt                                              string
	)
	if err := row.Scan(&id, &name, &customerID, &clusterID, &certSerial, &status, &supersededBy, &revokedAt, &createdAt, &updatedAt); err != nil {
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
		Name:         name,
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

type sessionStore struct{ gorm *DB }

func (s *sessionStore) Create(ctx context.Context, sess *store.Session) error {
	prepareSession(sess)
	capabilities, err := json.Marshal(sess.Capabilities)
	if err != nil {
		return fmt.Errorf("marshal session capabilities: %w", err)
	}

	_, err = s.gorm.ExecContext(ctx, `
INSERT INTO sessions (
	id, operator_id, instance_id, version, capabilities, active_config_version,
	status, started_at, last_heartbeat, expires_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		sess.ID,
		sess.OperatorID,
		sess.InstanceID,
		sess.Version,
		string(capabilities),
		sess.ActiveConfigVersion,
		string(sess.Status),
		sess.StartedAt.UTC().Format(time.RFC3339Nano),
		sess.LastHeartbeat.UTC().Format(time.RFC3339Nano),
		sess.ExpiresAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

func (s *sessionStore) Establish(ctx context.Context, sess *store.Session) error {
	prepareSession(sess)
	capabilities, err := json.Marshal(sess.Capabilities)
	if err != nil {
		return fmt.Errorf("marshal session capabilities: %w", err)
	}

	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin establish session: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback after Commit is a no-op.

	var activeInstanceID string
	err = tx.QueryRowContext(ctx, `
SELECT instance_id
FROM sessions
WHERE operator_id = ? AND status IN ('online', 'suspect')
ORDER BY started_at DESC
LIMIT 1
`, sess.OperatorID).Scan(&activeInstanceID)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("query active session: %w", err)
	}
	if err == nil && activeInstanceID != sess.InstanceID {
		return store.ErrDuplicateKey
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE sessions
SET status = ?
WHERE operator_id = ? AND status IN ('online', 'suspect')
`, string(store.SessionOffline), sess.OperatorID); err != nil {
		return fmt.Errorf("close active sessions: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO sessions (
	id, operator_id, instance_id, version, capabilities, active_config_version,
	status, started_at, last_heartbeat, expires_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		sess.ID,
		sess.OperatorID,
		sess.InstanceID,
		sess.Version,
		string(capabilities),
		sess.ActiveConfigVersion,
		string(sess.Status),
		sess.StartedAt.UTC().Format(time.RFC3339Nano),
		sess.LastHeartbeat.UTC().Format(time.RFC3339Nano),
		sess.ExpiresAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("insert established session: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit establish session: %w", err)
	}
	return nil
}

func (s *sessionStore) Get(ctx context.Context, id string) (*store.Session, error) {
	row := s.gorm.QueryRowContext(ctx, sessionSelect+" WHERE id = ?", id)
	return scanSession(row)
}

func (s *sessionStore) Heartbeat(ctx context.Context, id string) error {
	now := time.Now().UTC()
	result, err := s.gorm.ExecContext(ctx, `
UPDATE sessions SET last_heartbeat=?, status=? WHERE id=? AND status IN ('online', 'suspect')
`, now.Format(time.RFC3339Nano), string(store.SessionOnline), id)
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
	result, err := s.gorm.ExecContext(ctx, `
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
	row := s.gorm.QueryRowContext(ctx, sessionSelect+`
 WHERE operator_id=? AND status IN ('online', 'suspect')
 ORDER BY started_at DESC LIMIT 1
`, operatorID)
	return scanSession(row)
}

func (s *sessionStore) ListExpiredSuspect(ctx context.Context, suspectAfter time.Duration) ([]*store.Session, error) {
	threshold := time.Now().UTC().Add(-suspectAfter).Format(time.RFC3339Nano)
	rows, err := s.gorm.QueryContext(ctx, sessionSelect+`
 WHERE status IN ('online', 'suspect') AND last_heartbeat < ?
`, threshold)
	if err != nil {
		return nil, fmt.Errorf("list expired sessions: %w", err)
	}
	defer rows.Close()

	sessions := []*store.Session{}
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired sessions: %w", err)
	}
	return sessions, nil
}

const sessionSelect = `
SELECT id, operator_id, instance_id, version, capabilities, active_config_version,
       status, started_at, last_heartbeat, expires_at
FROM sessions`

func prepareSession(sess *store.Session) {
	now := time.Now().UTC()
	if sess.Status == "" {
		sess.Status = store.SessionOnline
	}
	if sess.StartedAt.IsZero() {
		sess.StartedAt = now
	}
	if sess.LastHeartbeat.IsZero() {
		sess.LastHeartbeat = sess.StartedAt
	}
	if sess.ExpiresAt.IsZero() {
		sess.ExpiresAt = sess.StartedAt
	}
	if sess.Capabilities == nil {
		sess.Capabilities = map[string]string{}
	}
}

func scanSession(row interface{ Scan(...interface{}) error }) (*store.Session, error) {
	var (
		sess                                store.Session
		capabilities, status                string
		startedAt, lastHeartbeat, expiresAt string
	)
	if err := row.Scan(
		&sess.ID,
		&sess.OperatorID,
		&sess.InstanceID,
		&sess.Version,
		&capabilities,
		&sess.ActiveConfigVersion,
		&status,
		&startedAt,
		&lastHeartbeat,
		&expiresAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan session: %w", err)
	}

	sess.Status = store.SessionStatus(status)
	if err := json.Unmarshal([]byte(capabilities), &sess.Capabilities); err != nil {
		return nil, fmt.Errorf("unmarshal session capabilities: %w", err)
	}

	var err error
	sess.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return nil, fmt.Errorf("parse session started_at: %w", err)
	}
	sess.LastHeartbeat, err = time.Parse(time.RFC3339Nano, lastHeartbeat)
	if err != nil {
		return nil, fmt.Errorf("parse session last_heartbeat: %w", err)
	}
	sess.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("parse session expires_at: %w", err)
	}
	return &sess, nil
}

// sha256Hex returns a hex-encoded SHA-256 hash of the input string.
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// scanOperators scans multiple operator rows.
func scanOperators(rows *sql.Rows) ([]*store.Operator, error) {
	var ops []*store.Operator
	for rows.Next() {
		op, err := scanRowOperator(rows)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate operator rows: %w", err)
	}
	return ops, nil
}

// scanRowOperator scans a single operator from a *sql.Rows iterator.
func scanRowOperator(row interface{ Scan(...interface{}) error }) (*store.Operator, error) {
	var (
		id, name, customerID, clusterID, certSerial, status, supersededBy string
		revokedAt                                                         *string
		createdAt, updatedAt                                              string
	)
	if err := row.Scan(&id, &name, &customerID, &clusterID, &certSerial, &status, &supersededBy, &revokedAt, &createdAt, &updatedAt); err != nil {
		return nil, fmt.Errorf("scan operator row: %w", err)
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
		Name:         name,
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
// EnrollmentToken revoke & list methods (REQ-015)
// ---------------------------------------------------------------------------

// Revoke marks an enrollment token as used/revoked.
func (s *enrollmentTokenStore) Revoke(ctx context.Context, id string) error {
	now := time.Now().UTC()
	result, err := s.gorm.ExecContext(ctx, `
UPDATE enrollment_tokens SET used=TRUE, used_at=? WHERE id=?
`, now.Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("revoke enrollment token: %w", err)
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

// ListByCustomer returns all enrollment tokens for a customer.
func (s *enrollmentTokenStore) ListByCustomer(ctx context.Context, customerID string) ([]*store.EnrollmentToken, error) {
	rows, err := s.gorm.QueryContext(ctx, `
SELECT id, customer_id, cluster_id, token, token_hash, created_at, expires_at, used, used_at, operator_id
FROM enrollment_tokens WHERE customer_id = ?
`, customerID)
	if err != nil {
		return nil, fmt.Errorf("list tokens by customer: %w", err)
	}
	defer rows.Close()
	return scanEnrollmentTokens(rows)
}

// ListByCluster returns all enrollment tokens for a cluster.
func (s *enrollmentTokenStore) ListByCluster(ctx context.Context, clusterID string) ([]*store.EnrollmentToken, error) {
	rows, err := s.gorm.QueryContext(ctx, `
SELECT id, customer_id, cluster_id, token, token_hash, created_at, expires_at, used, used_at, operator_id
FROM enrollment_tokens WHERE cluster_id = ?
`, clusterID)
	if err != nil {
		return nil, fmt.Errorf("list tokens by cluster: %w", err)
	}
	defer rows.Close()
	return scanEnrollmentTokens(rows)
}

func scanEnrollmentTokens(rows *sql.Rows) ([]*store.EnrollmentToken, error) {
	var tokens []*store.EnrollmentToken
	for rows.Next() {
		var (
			id, customerID, clusterID, token, tokenHash, createdAt, expiresAt string
			used                                                              bool
			usedAt, operatorID                                                *string
		)
		if err := rows.Scan(&id, &customerID, &clusterID, &token, &tokenHash, &createdAt, &expiresAt, &used, &usedAt, &operatorID); err != nil {
			return nil, fmt.Errorf("scan enrollment token row: %w", err)
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
			TokenHash:  tokenHash,
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
		tokens = append(tokens, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate token rows: %w", err)
	}
	return tokens, nil
}

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

// ---------------------------------------------------------------------------
// Enrollment token store
// ---------------------------------------------------------------------------

type enrollmentTokenStore struct{ gorm *DB }

// enrollmentTokenExecer is satisfied by both *DB and *Tx.
type enrollmentTokenExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// prepareEnrollmentToken fills write defaults and derives the token hash.
func prepareEnrollmentToken(token *store.EnrollmentToken) {
	if token.CreatedAt.IsZero() {
		token.CreatedAt = time.Now().UTC()
	}
	if token.State == "" {
		token.State = store.TokenStatePending
	}
	if token.TokenHash == "" && token.Token != "" {
		token.TokenHash = sha256Hex(token.Token)
	}
}

func insertEnrollmentToken(ctx context.Context, execer enrollmentTokenExecer, token *store.EnrollmentToken) error {
	_, err := execer.ExecContext(ctx, `
INSERT INTO enrollment_tokens (id, customer_id, cluster_id, token, token_hash, operator_name, state, created_by_display_name, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		token.ID,
		token.CustomerID,
		token.ClusterID,
		token.TokenHash, // Legacy token column retains only the irreversible hash.
		token.TokenHash,
		token.OperatorName,
		string(token.State),
		token.CreatedByDisplayName,
		token.CreatedAt.UTC().Format(time.RFC3339Nano),
		token.ExpiresAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert enrollment token: %w", err)
	}
	return nil
}

//nolint:gosec // column list containing the token_hash column name, not a credential
const enrollmentTokenSelect = `
id, customer_id, cluster_id, token, token_hash, operator_name, state, created_by_display_name,
created_at, expires_at, used_at, operator_id, revoked_at, replaced_by_id`

func (s *enrollmentTokenStore) Create(ctx context.Context, t *store.EnrollmentToken) error {
	prepareEnrollmentToken(t)
	if err := insertEnrollmentToken(ctx, s.gorm, t); err != nil {
		if isUniqueConstraint(err) {
			return store.ErrPendingTokenExists
		}
		return err
	}
	return nil
}

func (s *enrollmentTokenStore) GetByToken(ctx context.Context, token string) (*store.EnrollmentToken, error) {
	tokenHash := sha256Hex(token)
	row := s.gorm.QueryRowContext(ctx, `SELECT `+enrollmentTokenSelect+`
FROM enrollment_tokens WHERE token_hash = ?
`, tokenHash)
	return scanEnrollmentToken(row)
}

func (s *enrollmentTokenStore) MarkUsed(ctx context.Context, id, operatorID string) error {
	now := time.Now().UTC()
	result, err := s.gorm.ExecContext(ctx, `
UPDATE enrollment_tokens SET state='used', used_at=?, operator_id=? WHERE id=? AND state='pending'
`, now.Format(time.RFC3339Nano), operatorID, id)
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

// Revoke marks an enrollment token as revoked.
func (s *enrollmentTokenStore) Revoke(ctx context.Context, id string) error {
	now := time.Now().UTC()
	result, err := s.gorm.ExecContext(ctx, `
UPDATE enrollment_tokens SET state='revoked', revoked_at=? WHERE id=?
`, now.Format(time.RFC3339Nano), id)
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

// GetPendingByCluster returns the pending enrollment token for a given cluster, if any.
func (s *enrollmentTokenStore) GetPendingByCluster(ctx context.Context, customerID, clusterID string) (*store.EnrollmentToken, error) {
	row := s.gorm.QueryRowContext(ctx, `SELECT `+enrollmentTokenSelect+`
FROM enrollment_tokens WHERE customer_id = ? AND cluster_id = ? AND state = 'pending'
ORDER BY created_at DESC LIMIT 1
`, customerID, clusterID)
	return scanEnrollmentToken(row)
}

// ListByCustomer returns all enrollment tokens for a customer.
func (s *enrollmentTokenStore) ListByCustomer(ctx context.Context, customerID string) ([]*store.EnrollmentToken, error) {
	rows, err := s.gorm.QueryContext(ctx, `SELECT `+enrollmentTokenSelect+`
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
	rows, err := s.gorm.QueryContext(ctx, `SELECT `+enrollmentTokenSelect+`
FROM enrollment_tokens WHERE cluster_id = ?
`, clusterID)
	if err != nil {
		return nil, fmt.Errorf("list tokens by cluster: %w", err)
	}
	defer rows.Close()
	return scanEnrollmentTokens(rows)
}

func scanEnrollmentToken(row interface{ Scan(...interface{}) error }) (*store.EnrollmentToken, error) {
	var (
		token                                    store.EnrollmentToken
		legacyToken, state, createdAt, expiresAt string
		usedAt, operatorID, revokedAt            *string
		replacedByID                             *string
	)
	if err := row.Scan(
		&token.ID,
		&token.CustomerID,
		&token.ClusterID,
		&legacyToken,
		&token.TokenHash,
		&token.OperatorName,
		&state,
		&token.CreatedByDisplayName,
		&createdAt,
		&expiresAt,
		&usedAt,
		&operatorID,
		&revokedAt,
		&replacedByID,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan enrollment token: %w", err)
	}
	_ = legacyToken // Never expose the legacy token column; it contains only a hash after TASK-053.
	token.State = store.EnrollmentTokenState(state)
	var err error
	token.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse token created_at: %w", err)
	}
	token.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("parse token expires_at: %w", err)
	}
	if usedAt != nil {
		parsed, parseErr := time.Parse(time.RFC3339Nano, *usedAt)
		if parseErr != nil {
			return nil, fmt.Errorf("parse token used_at: %w", parseErr)
		}
		token.UsedAt = &parsed
	}
	if operatorID != nil {
		token.OperatorID = *operatorID
	}
	if revokedAt != nil {
		parsed, parseErr := time.Parse(time.RFC3339Nano, *revokedAt)
		if parseErr != nil {
			return nil, fmt.Errorf("parse token revoked_at: %w", parseErr)
		}
		token.RevokedAt = &parsed
	}
	if replacedByID != nil {
		token.ReplacedByID = *replacedByID
	}
	return &token, nil
}

func scanEnrollmentTokens(rows *sql.Rows) ([]*store.EnrollmentToken, error) {
	tokens := make([]*store.EnrollmentToken, 0)
	for rows.Next() {
		token, err := scanEnrollmentToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate token rows: %w", err)
	}
	return tokens, nil
}

// ---------------------------------------------------------------------------
// Operator store
// ---------------------------------------------------------------------------

type operatorStore struct{ gorm *DB }

const operatorSelect = `
id, operator_name, customer_id, cluster_id, cert_serial, status, superseded_by, superseded_at, revoked_at, revoke_reason, created_at, updated_at`

// prepareOperator fills write defaults for an Operator.
func prepareOperator(op *store.Operator) {
	if op.RegisteredAt.IsZero() {
		op.RegisteredAt = time.Now().UTC()
	}
	if op.UpdatedAt.IsZero() {
		op.UpdatedAt = op.RegisteredAt
	}
	if op.Status == "" {
		op.Status = store.OperatorActive
	}
}

func (s *operatorStore) Create(ctx context.Context, op *store.Operator) error {
	prepareOperator(op)

	var revokedAt, supersededAt *string
	if op.RevokedAt != nil {
		v := op.RevokedAt.UTC().Format(time.RFC3339Nano)
		revokedAt = &v
	}
	if op.SupersededAt != nil {
		v := op.SupersededAt.UTC().Format(time.RFC3339Nano)
		supersededAt = &v
	}

	_, err := s.gorm.ExecContext(ctx, `
INSERT INTO operators (id, customer_id, cluster_id, operator_name, cert_serial, status, superseded_by, superseded_at, revoked_at, revoke_reason, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		op.ID, op.CustomerID, op.ClusterID, op.Name, op.CertSerial, string(op.Status),
		op.SupersededBy, supersededAt, revokedAt, op.RevokeReason,
		op.RegisteredAt.UTC().Format(time.RFC3339Nano), op.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert operator: %w", err)
	}
	return nil
}

func (s *operatorStore) Get(ctx context.Context, id string) (*store.Operator, error) {
	row := s.gorm.QueryRowContext(ctx, `SELECT `+operatorSelect+`
FROM operators WHERE id = ?
`, id)
	return scanOperator(row)
}

func (s *operatorStore) GetByCertSerial(ctx context.Context, serial string) (*store.Operator, error) {
	row := s.gorm.QueryRowContext(ctx, `SELECT `+operatorSelect+`
FROM operators WHERE cert_serial = ?
`, serial)
	return scanOperator(row)
}

func (s *operatorStore) Update(ctx context.Context, op *store.Operator) error {
	op.UpdatedAt = time.Now().UTC()

	var revokedAt, supersededAt *string
	if op.RevokedAt != nil {
		v := op.RevokedAt.UTC().Format(time.RFC3339Nano)
		revokedAt = &v
	}
	if op.SupersededAt != nil {
		v := op.SupersededAt.UTC().Format(time.RFC3339Nano)
		supersededAt = &v
	}

	_, err := s.gorm.ExecContext(ctx, `
UPDATE operators SET operator_name=?, cert_serial=?, status=?, superseded_by=?, superseded_at=?, revoked_at=?, revoke_reason=?, updated_at=?
WHERE id=?
`,
		op.Name, op.CertSerial, string(op.Status), op.SupersededBy, supersededAt, revokedAt, op.RevokeReason,
		op.UpdatedAt.UTC().Format(time.RFC3339Nano), op.ID,
	)
	if err != nil {
		return fmt.Errorf("update operator: %w", err)
	}
	return nil
}

// GetByClusterID returns the active operator for a cluster, if any.
func (s *operatorStore) GetByClusterID(ctx context.Context, clusterID string) (*store.Operator, error) {
	row := s.gorm.QueryRowContext(ctx, `SELECT `+operatorSelect+`
FROM operators WHERE cluster_id = ? AND status = ?
`, clusterID, string(store.OperatorActive))
	return scanOperator(row)
}

// GetActiveByName returns the active operator with the given name in a Customer scope.
func (s *operatorStore) GetActiveByName(ctx context.Context, customerID, name string) (*store.Operator, error) {
	row := s.gorm.QueryRowContext(ctx, `SELECT `+operatorSelect+`
FROM operators WHERE customer_id = ? AND operator_name = ? AND status = ?
`, customerID, name, string(store.OperatorActive))
	return scanOperator(row)
}

// GetByName returns the operator with the given name, regardless of status.
func (s *operatorStore) GetByName(ctx context.Context, name string) (*store.Operator, error) {
	row := s.gorm.QueryRowContext(ctx, `SELECT `+operatorSelect+`
FROM operators WHERE operator_name = ?
`, name)
	return scanOperator(row)
}

// Revoke marks an operator as revoked with a timestamp.
func (s *operatorStore) Revoke(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
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
	rows, err := s.gorm.QueryContext(ctx, `SELECT `+operatorSelect+`
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
	rows, err := s.gorm.QueryContext(ctx, `SELECT `+operatorSelect+`
FROM operators WHERE cluster_id = ?
`, clusterID)
	if err != nil {
		return nil, fmt.Errorf("list operators by cluster: %w", err)
	}
	defer rows.Close()
	return scanOperators(rows)
}

// ListByClusterFilter returns a stable cursor-paginated Operator page.
func (s *operatorStore) ListByClusterFilter(
	ctx context.Context,
	customerID string,
	clusterID string,
	filter store.OperatorListFilter,
	pageSize int32,
	cursor *store.OperatorCursor,
) (*store.OperatorPage, error) {
	query := `SELECT ` + operatorSelect + `
FROM operators AS o
WHERE o.customer_id = ? AND o.cluster_id = ?`
	args := []any{customerID, clusterID}
	if filter.LifecycleStatus != nil {
		query += ` AND o.status = ?`
		args = append(args, string(*filter.LifecycleStatus))
	}
	if filter.NoSession {
		query += ` AND NOT EXISTS (SELECT 1 FROM sessions AS s WHERE s.operator_id = o.id)`
	} else if filter.SessionStatus != nil {
		query += ` AND (
			SELECT s.status FROM sessions AS s
			WHERE s.operator_id = o.id
			ORDER BY s.started_at DESC, s.id DESC
			LIMIT 1
		) = ?`
		args = append(args, string(*filter.SessionStatus))
	}
	if cursor != nil {
		query += ` AND (o.created_at < ? OR (o.created_at = ? AND o.id < ?))`
		registeredAt := cursor.RegisteredAt.UTC().Format(time.RFC3339Nano)
		args = append(args, registeredAt, registeredAt, cursor.OperatorID)
	}
	query += ` ORDER BY o.created_at DESC, o.id DESC LIMIT ?`
	args = append(args, pageSize+1)

	rows, err := s.gorm.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list operators by cluster filter: %w", err)
	}
	defer rows.Close()
	operators, err := scanOperators(rows)
	if err != nil {
		return nil, err
	}

	page := &store.OperatorPage{Operators: operators}
	if len(operators) > int(pageSize) {
		page.Operators = operators[:pageSize]
		last := page.Operators[len(page.Operators)-1]
		page.NextPageCursor = &store.OperatorCursor{
			CustomerID:      customerID,
			ClusterID:       clusterID,
			LifecycleStatus: filter.LifecycleStatus,
			SessionStatus:   filter.SessionStatus,
			NoSession:       filter.NoSession,
			RegisteredAt:    last.RegisteredAt,
			OperatorID:      last.ID,
		}
	}

	countQuery := `SELECT COUNT(*) FROM operators AS o WHERE o.customer_id = ? AND o.cluster_id = ?`
	countArgs := []any{customerID, clusterID}
	if filter.LifecycleStatus != nil {
		countQuery += ` AND o.status = ?`
		countArgs = append(countArgs, string(*filter.LifecycleStatus))
	}
	if filter.NoSession {
		countQuery += ` AND NOT EXISTS (SELECT 1 FROM sessions AS s WHERE s.operator_id = o.id)`
	} else if filter.SessionStatus != nil {
		countQuery += ` AND (
			SELECT s.status FROM sessions AS s
			WHERE s.operator_id = o.id
			ORDER BY s.started_at DESC, s.id DESC
			LIMIT 1
		) = ?`
		countArgs = append(countArgs, string(*filter.SessionStatus))
	}
	if err := s.gorm.QueryRowContext(ctx, countQuery, countArgs...).Scan(&page.TotalCount); err != nil {
		return nil, fmt.Errorf("count operators: %w", err)
	}
	return page, nil
}

func scanOperator(row interface{ Scan(...interface{}) error }) (*store.Operator, error) {
	var (
		id, name, customerID, clusterID, certSerial, status, supersededBy, revokeReason string
		supersededAt, revokedAt                                                         *string
		createdAt, updatedAt                                                            string
	)
	if err := row.Scan(&id, &name, &customerID, &clusterID, &certSerial, &status, &supersededBy, &supersededAt, &revokedAt, &revokeReason, &createdAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan operator: %w", err)
	}

	ct, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse operator created_at: %w", err)
	}
	ut, err := time.Parse(time.RFC3339Nano, updatedAt)
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
		RevokeReason: revokeReason,
		RegisteredAt: ct,
		UpdatedAt:    ut,
	}
	if supersededAt != nil {
		t, err := time.Parse(time.RFC3339Nano, *supersededAt)
		if err != nil {
			return nil, fmt.Errorf("parse operator superseded_at: %w", err)
		}
		op.SupersededAt = &t
	}
	if revokedAt != nil {
		t, err := time.Parse(time.RFC3339Nano, *revokedAt)
		if err != nil {
			return nil, fmt.Errorf("parse operator revoked_at: %w", err)
		}
		op.RevokedAt = &t
	}
	return op, nil
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
		id, name, customerID, clusterID, certSerial, status, supersededBy, revokeReason string
		supersededAt, revokedAt                                                         *string
		createdAt, updatedAt                                                            string
	)
	if err := row.Scan(&id, &name, &customerID, &clusterID, &certSerial, &status, &supersededBy, &supersededAt, &revokedAt, &revokeReason, &createdAt, &updatedAt); err != nil {
		return nil, fmt.Errorf("scan operator row: %w", err)
	}

	ct, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse operator created_at: %w", err)
	}
	ut, err := time.Parse(time.RFC3339Nano, updatedAt)
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
		RevokeReason: revokeReason,
		RegisteredAt: ct,
		UpdatedAt:    ut,
	}
	if supersededAt != nil {
		t, err := time.Parse(time.RFC3339Nano, *supersededAt)
		if err != nil {
			return nil, fmt.Errorf("parse operator superseded_at: %w", err)
		}
		op.SupersededAt = &t
	}
	if revokedAt != nil {
		t, err := time.Parse(time.RFC3339Nano, *revokedAt)
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

	var statusReason, closedAt *string
	if sess.StatusReason != nil {
		v := string(*sess.StatusReason)
		statusReason = &v
	}
	if sess.ClosedAt != nil {
		v := sess.ClosedAt.UTC().Format(time.RFC3339Nano)
		closedAt = &v
	}

	_, err = s.gorm.ExecContext(ctx, `
INSERT INTO sessions (
	id, operator_id, customer_id, cluster_id, instance_id, version, capabilities, active_config_version,
	status, status_reason, started_at, last_heartbeat, expires_at, closed_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		sess.ID,
		sess.OperatorID,
		sess.CustomerID,
		sess.ClusterID,
		sess.InstanceID,
		sess.Version,
		string(capabilities),
		sess.ActiveConfigVersion,
		string(sess.Status),
		statusReason,
		sess.StartedAt.UTC().Format(time.RFC3339Nano),
		sess.LastHeartbeat.UTC().Format(time.RFC3339Nano),
		sess.ExpiresAt.UTC().Format(time.RFC3339Nano),
		closedAt,
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
	// An active session with an empty instance_id is the Enroll placeholder
	// session (Enroll establishes one before the agent opens its first
	// stream, TASK-075). It has never been claimed by a live agent, so the
	// first Establish replaces it instead of treating it as a concurrent
	// connection (REQ-044: only distinct live instances conflict).
	if err == nil && activeInstanceID != "" && activeInstanceID != sess.InstanceID {
		return store.ErrDuplicateKey
	}

	replacedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
UPDATE sessions
SET status = ?, status_reason = ?, closed_at = ?
WHERE operator_id = ? AND status IN ('online', 'suspect')
`, string(store.SessionOffline), string(store.SessionReasonSessionReplaced), replacedAt, sess.OperatorID); err != nil {
		return fmt.Errorf("close active sessions: %w", err)
	}

	var statusReason, closedAt *string
	if sess.StatusReason != nil {
		v := string(*sess.StatusReason)
		statusReason = &v
	}
	if sess.ClosedAt != nil {
		v := sess.ClosedAt.UTC().Format(time.RFC3339Nano)
		closedAt = &v
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO sessions (
	id, operator_id, customer_id, cluster_id, instance_id, version, capabilities, active_config_version,
	status, status_reason, started_at, last_heartbeat, expires_at, closed_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		sess.ID,
		sess.OperatorID,
		sess.CustomerID,
		sess.ClusterID,
		sess.InstanceID,
		sess.Version,
		string(capabilities),
		sess.ActiveConfigVersion,
		string(sess.Status),
		statusReason,
		sess.StartedAt.UTC().Format(time.RFC3339Nano),
		sess.LastHeartbeat.UTC().Format(time.RFC3339Nano),
		sess.ExpiresAt.UTC().Format(time.RFC3339Nano),
		closedAt,
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
UPDATE sessions SET last_heartbeat=?, status=?, status_reason=NULL WHERE id=? AND status IN ('online', 'suspect')
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
	var reason *string
	switch status {
	case store.SessionSuspect:
		value := string(store.SessionReasonHeartbeatDelayed)
		reason = &value
	case store.SessionOffline:
		value := string(store.SessionReasonHeartbeatTimeout)
		reason = &value
	}
	result, err := s.gorm.ExecContext(ctx, `
UPDATE sessions SET status=?, status_reason=? WHERE id=? AND status != 'revoked'
`, string(status), reason, id)
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

// UpdateStatusReason updates a session's status and reason.
func (s *sessionStore) UpdateStatusReason(ctx context.Context, id string, status store.SessionStatus, reason store.SessionStatusReason) error {
	result, err := s.gorm.ExecContext(ctx, `
UPDATE sessions SET status=?, status_reason=? WHERE id=?
`, string(status), string(reason), id)
	if err != nil {
		return fmt.Errorf("update session status reason: %w", err)
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

// GetLatestByOperator returns the most recent session for an operator regardless of status.
func (s *sessionStore) GetLatestByOperator(ctx context.Context, operatorID string) (*store.Session, error) {
	row := s.gorm.QueryRowContext(ctx, sessionSelect+`
 WHERE operator_id=? ORDER BY started_at DESC LIMIT 1
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
SELECT id, operator_id, customer_id, cluster_id, instance_id, version, capabilities, active_config_version,
       status, status_reason, started_at, last_heartbeat, expires_at, closed_at
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
		statusReason, closedAt              *string
		startedAt, lastHeartbeat, expiresAt string
	)
	if err := row.Scan(
		&sess.ID,
		&sess.OperatorID,
		&sess.CustomerID,
		&sess.ClusterID,
		&sess.InstanceID,
		&sess.Version,
		&capabilities,
		&sess.ActiveConfigVersion,
		&status,
		&statusReason,
		&startedAt,
		&lastHeartbeat,
		&expiresAt,
		&closedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan session: %w", err)
	}
	sess.Status = store.SessionStatus(status)

	if statusReason != nil && *statusReason != "" {
		sr := store.SessionStatusReason(*statusReason)
		sess.StatusReason = &sr
	}
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
	if closedAt != nil && *closedAt != "" {
		ct, err := time.Parse(time.RFC3339Nano, *closedAt)
		if err != nil {
			return nil, fmt.Errorf("parse session closed_at: %w", err)
		}
		sess.ClosedAt = &ct
	}
	return &sess, nil
}

// sha256Hex returns a hex-encoded SHA-256 hash of the input string.
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// operatorLifecycleStore owns the REQ-015 certificate and scope-disable
// transactions (事务边界 3/4): certificate rotation CAS and irreversible
// customer/cluster cascade revocation.
type operatorLifecycleStore struct{ gorm *DB }

var _ store.OperatorLifecycleStore = (*operatorLifecycleStore)(nil)

// UpdateCertificate rotates the effective cert serial with a CAS on the
// expected serial (ADR-018: the last committed serial is authoritative).
// A stale expectedSerial (concurrent renew) yields ErrCertificateConflict.
func (s *operatorLifecycleStore) UpdateCertificate(
	ctx context.Context,
	operatorID, expectedSerial, newSerial string,
	expiresAt time.Time,
) error {
	now := time.Now().UTC()
	result, err := s.gorm.ExecContext(ctx, `
UPDATE operators SET cert_serial = ?, certificate_expires_at = ?, updated_at = ?
WHERE id = ? AND cert_serial = ? AND status = ?
`,
		newSerial, expiresAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		operatorID, expectedSerial, string(store.OperatorActive))
	if err != nil {
		if isUniqueConstraint(err) {
			return store.ErrDuplicateKey
		}
		return fmt.Errorf("update operator certificate: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update operator certificate rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrCertificateConflict
	}
	return nil
}

// DisableCustomer revokes every pending token, non-revoked operator and live
// session of a customer in one transaction (irreversible).
func (s *operatorLifecycleStore) DisableCustomer(
	ctx context.Context,
	customerID, reason string,
) (*store.CascadeRevokeResult, error) {
	return s.revokeScope(ctx, "customer_id", customerID, reason)
}

// DisableCluster revokes every pending token, non-revoked operator and live
// session of a cluster in one transaction (irreversible).
func (s *operatorLifecycleStore) DisableCluster(
	ctx context.Context,
	clusterID, reason string,
) (*store.CascadeRevokeResult, error) {
	return s.revokeScope(ctx, "cluster_id", clusterID, reason)
}

// revokeScope is the shared transactional cascade: list → revoke tokens and
// operators → close live sessions, all-or-nothing. Already-revoked records
// are not re-touched (first-write reason/time preserved, idempotent).
// column is restricted to the "customer_id"/"cluster_id" whitelist (see
// DisableCustomer/DisableCluster); values are always parameterized.
func (s *operatorLifecycleStore) revokeScope(
	ctx context.Context,
	column, value, reason string,
) (*store.CascadeRevokeResult, error) {
	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin scope revoke: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after successful commit

	result := new(store.CascadeRevokeResult)
	now := time.Now().UTC()

	if result.TokenIDs, err = queryIDs(ctx, tx,
		`SELECT id FROM enrollment_tokens WHERE `+column+` = ? AND state = ?`,
		value, string(store.TokenStatePending)); err != nil {
		return nil, fmt.Errorf("list cascade tokens: %w", err)
	}
	if result.OperatorIDs, err = queryIDs(ctx, tx,
		`SELECT id FROM operators WHERE `+column+` = ? AND status <> ?`,
		value, string(store.OperatorRevoked)); err != nil {
		return nil, fmt.Errorf("list cascade operators: %w", err)
	}
	if len(result.OperatorIDs) > 0 {
		args := append([]any{string(store.SessionOnline), string(store.SessionSuspect)}, stringsToAny(result.OperatorIDs)...)
		if result.SessionIDs, err = queryIDs(ctx, tx,
			`SELECT id FROM sessions WHERE status IN (?, ?) AND operator_id IN (`+placeholders(len(result.OperatorIDs))+`)`,
			args...); err != nil {
			return nil, fmt.Errorf("list cascade sessions: %w", err)
		}
	}

	if len(result.TokenIDs) > 0 {
		if _, err := tx.ExecContext(ctx, `
UPDATE enrollment_tokens SET state = ?, revoked_at = ?
WHERE `+column+` = ? AND state = ?
`, string(store.TokenStateRevoked), now.Format(time.RFC3339Nano), value, string(store.TokenStatePending)); err != nil {
			return nil, fmt.Errorf("revoke cascade tokens: %w", err)
		}
	}
	if len(result.OperatorIDs) > 0 {
		if _, err := tx.ExecContext(ctx, `
UPDATE operators SET status = ?, revoked_at = ?, revoke_reason = ?, updated_at = ?
WHERE `+column+` = ? AND status <> ?
`, string(store.OperatorRevoked), now.Format(time.RFC3339Nano), reason, now.Format(time.RFC3339Nano), value, string(store.OperatorRevoked)); err != nil {
			return nil, fmt.Errorf("revoke cascade operators: %w", err)
		}
	}
	if len(result.SessionIDs) > 0 {
		args := append([]any{
			string(store.SessionRevoked), string(store.SessionReasonCertRevoked), now.Format(time.RFC3339Nano),
		}, stringsToAny(result.OperatorIDs)...)
		args = append(args, string(store.SessionOnline), string(store.SessionSuspect))
		if _, err := tx.ExecContext(ctx, `
UPDATE sessions SET status = ?, status_reason = ?, closed_at = ?
WHERE operator_id IN (`+placeholders(len(result.OperatorIDs))+`) AND status IN (?, ?)
`, args...); err != nil {
			return nil, fmt.Errorf("close cascade sessions: %w", err)
		}
	}
	result.Changed = len(result.TokenIDs) > 0 || len(result.OperatorIDs) > 0 || len(result.SessionIDs) > 0

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit scope revoke: %w", err)
	}
	return result, nil
}

// queryIDs collects a single id column into a slice (cascade listing inside
// the revoke transaction). The queryer covers *DB and *Tx.
func queryIDs(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args ...any) ([]string, error) {
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// placeholders renders n `?` placeholders for a SQL IN clause.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// stringsToAny converts a string slice to any for variadic SQL args.
func stringsToAny(ss []string) []any {
	args := make([]any, len(ss))
	for i, s := range ss {
		args[i] = s
	}
	return args
}

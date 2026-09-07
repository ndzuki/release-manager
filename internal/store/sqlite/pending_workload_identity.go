package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ndzuki/release-manager/internal/store"
)

// pendingWorkloadIdentityStore buffers authoritative identity reports whose
// release_inventory row does not exist yet (REQ-088, D2=A). Mirrors the
// migrationStatements CREATE TABLE; see migrations/000025_pending_workload_identity.up.sql.
type pendingWorkloadIdentityStore struct{ db *sql.DB }

// Upsert persists one buffered identity for a release key. The unique key
// (customer_id, cluster_id, namespace, release_name) is the idempotency key
// (D6=A): re-reporting the same release replaces the four-tuple and refreshes
// created_at (the TTL start).
func (s *pendingWorkloadIdentityStore) Upsert(ctx context.Context, pending *store.PendingWorkloadIdentity) error {
	if pending == nil {
		return fmt.Errorf("upsert pending workload identity: nil pending")
	}
	if pending.ID == "" {
		pending.ID = uuid.NewString()
	}
	// A caller-supplied created_at is honored on first insert (deterministic
	// TTL tests); a conflicting re-report always refreshes created_at to NOW
	// because a repeat report restarts the TTL from its own arrival time.
	insertedAt := pending.CreatedAt
	if insertedAt.IsZero() {
		insertedAt = time.Now().UTC()
	}
	refreshedAt := time.Now().UTC()
	const stmt = `INSERT INTO pending_workload_identity
		(id, customer_id, cluster_id, namespace, release_name,
		 workload_kind, workload_name, workload_namespace, workload_uid, created_at)
	 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	 ON CONFLICT(customer_id, cluster_id, namespace, release_name) DO UPDATE SET
		workload_kind = excluded.workload_kind,
		workload_name = excluded.workload_name,
		workload_namespace = excluded.workload_namespace,
		workload_uid = excluded.workload_uid,
		created_at = ?`
	_, err := s.db.ExecContext(ctx, stmt,
		pending.ID, pending.CustomerID, pending.ClusterID, pending.Namespace, pending.ReleaseName,
		pending.WorkloadKind, pending.WorkloadName, pending.WorkloadNamespace, pending.WorkloadUID,
		insertedAt.UTC().Format(time.RFC3339),
		refreshedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("upsert pending workload identity: %w", err)
	}
	return nil
}

// GetByReleaseKey returns the buffered identity for one inventory unique key.
func (s *pendingWorkloadIdentityStore) GetByReleaseKey(ctx context.Context, customerID, clusterID, namespace, releaseName string) (*store.PendingWorkloadIdentity, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, customer_id, cluster_id, namespace, release_name,
		       workload_kind, workload_name, workload_namespace, workload_uid, created_at
		FROM pending_workload_identity
		WHERE customer_id = ? AND cluster_id = ? AND namespace = ? AND release_name = ?
	`, customerID, clusterID, namespace, releaseName)
	pending, err := scanPendingWorkloadIdentity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return pending, err
}

// ListByCluster returns every buffered identity for one cluster.
func (s *pendingWorkloadIdentityStore) ListByCluster(ctx context.Context, customerID, clusterID string) ([]*store.PendingWorkloadIdentity, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, customer_id, cluster_id, namespace, release_name,
		       workload_kind, workload_name, workload_namespace, workload_uid, created_at
		FROM pending_workload_identity
		WHERE customer_id = ? AND cluster_id = ?
		ORDER BY namespace, release_name
	`, customerID, clusterID)
	if err != nil {
		return nil, fmt.Errorf("list pending workload identities: %w", err)
	}
	defer rows.Close()
	return scanPendingWorkloadIdentities(rows)
}

// ListAll returns every buffered identity across all clusters (periodic sweep
// enumeration).
func (s *pendingWorkloadIdentityStore) ListAll(ctx context.Context) ([]*store.PendingWorkloadIdentity, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, customer_id, cluster_id, namespace, release_name,
		       workload_kind, workload_name, workload_namespace, workload_uid, created_at
		FROM pending_workload_identity
		ORDER BY customer_id, cluster_id, namespace, release_name
	`)
	if err != nil {
		return nil, fmt.Errorf("list all pending workload identities: %w", err)
	}
	defer rows.Close()
	return scanPendingWorkloadIdentities(rows)
}

// DeleteByReleaseKey removes the buffered identity for one release key.
func (s *pendingWorkloadIdentityStore) DeleteByReleaseKey(ctx context.Context, customerID, clusterID, namespace, releaseName string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM pending_workload_identity
		WHERE customer_id = ? AND cluster_id = ? AND namespace = ? AND release_name = ?
	`, customerID, clusterID, namespace, releaseName)
	if err != nil {
		return fmt.Errorf("delete pending workload identity: %w", err)
	}
	return nil
}

// PurgeExpired removes pending rows whose created_at precedes cutoff (TTL
// orphans, D3=A) and returns the number purged.
func (s *pendingWorkloadIdentityStore) PurgeExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM pending_workload_identity
		WHERE created_at < ?
	`, cutoff.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("purge expired pending workload identities: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("pending workload identity purge rows: %w", err)
	}
	return count, nil
}

func scanPendingWorkloadIdentity(row interface{ Scan(...interface{}) error }) (*store.PendingWorkloadIdentity, error) {
	var pending store.PendingWorkloadIdentity
	var createdAt string
	if err := row.Scan(
		&pending.ID, &pending.CustomerID, &pending.ClusterID, &pending.Namespace, &pending.ReleaseName,
		&pending.WorkloadKind, &pending.WorkloadName, &pending.WorkloadNamespace, &pending.WorkloadUID,
		&createdAt,
	); err != nil {
		return nil, err
	}
	var err error
	pending.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse pending workload identity created_at: %w", err)
	}
	return &pending, nil
}

func scanPendingWorkloadIdentities(rows *sql.Rows) ([]*store.PendingWorkloadIdentity, error) {
	var pendings []*store.PendingWorkloadIdentity
	for rows.Next() {
		pending, err := scanPendingWorkloadIdentity(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pending workload identity: %w", err)
		}
		pendings = append(pendings, pending)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending workload identities: %w", err)
	}
	return pendings, nil
}

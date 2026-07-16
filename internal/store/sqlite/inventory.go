package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type inventoryStore struct {
	db *sql.DB
}

// Upsert inserts or updates an inventory row by unique key.
func (s *inventoryStore) Upsert(ctx context.Context, item *store.ReleaseInventory) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	item.UpdatedAt = time.Now().UTC()

	const stmt = `INSERT INTO release_inventory
		(customer_id, cluster_id, namespace, release_name, chart, chart_version, revision, status,
		 values_digest, inventory_status, last_sync_id, snapshot_version, created_at, updated_at)
	 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	 ON CONFLICT(customer_id, cluster_id, namespace, release_name) DO UPDATE SET
		chart = excluded.chart,
		chart_version = excluded.chart_version,
		revision = excluded.revision,
		status = excluded.status,
		values_digest = excluded.values_digest,
		inventory_status = excluded.inventory_status,
		last_sync_id = excluded.last_sync_id,
		snapshot_version = excluded.snapshot_version,
		updated_at = excluded.updated_at`

	_, err := s.db.ExecContext(ctx, stmt,
		item.CustomerID, item.ClusterID, item.Namespace, item.ReleaseName,
		item.Chart, item.ChartVersion, item.Revision, item.Status,
		item.ValuesDigest, string(item.InventoryStatus),
		item.LastSyncID, item.SnapshotVersion,
		item.CreatedAt.UTC().Format(time.RFC3339), now,
	)
	return err
}

// ListByCluster returns all inventory rows for a cluster.
func (s *inventoryStore) ListByCluster(ctx context.Context, customerID, clusterID string) ([]*store.ReleaseInventory, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT customer_id, cluster_id, namespace, release_name, chart, chart_version,
		        revision, status, values_digest, inventory_status, last_sync_id,
		        snapshot_version, created_at, updated_at
		 FROM release_inventory
		 WHERE customer_id = ? AND cluster_id = ?
		 ORDER BY namespace, release_name`,
		customerID, clusterID,
	)
	if err != nil {
		return nil, fmt.Errorf("list inventory: %w", err)
	}
	defer rows.Close()

	var items []*store.ReleaseInventory
	for rows.Next() {
		var item store.ReleaseInventory
		var createdAt, updatedAt string
		if err := rows.Scan(
			&item.CustomerID, &item.ClusterID, &item.Namespace, &item.ReleaseName,
			&item.Chart, &item.ChartVersion, &item.Revision, &item.Status,
			&item.ValuesDigest, &item.InventoryStatus, &item.LastSyncID,
			&item.SnapshotVersion, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan inventory: %w", err)
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339, createdAt) //nolint:errcheck // stored timestamps always valid RFC3339
		item.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt) //nolint:errcheck // stored timestamps always valid RFC3339
		items = append(items, &item)
	}
	return items, rows.Err()
}

// MarkMissing sets InventoryMissing for all rows in a cluster not present in the given set.
func (s *inventoryStore) MarkMissing(ctx context.Context, customerID, clusterID string, presentKeys []string) (int, error) {
	if len(presentKeys) == 0 {
		// Mark all as missing when no releases are present.
		res, err := s.db.ExecContext(ctx,
			`UPDATE release_inventory SET inventory_status = ?, updated_at = ?
			 WHERE customer_id = ? AND cluster_id = ? AND inventory_status != ?`,
			string(store.InventoryMissing), time.Now().UTC().Format(time.RFC3339),
			customerID, clusterID, string(store.InventoryMissing),
		)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		return int(n), nil
	}

	placeholders := make([]string, len(presentKeys))
	args := make([]interface{}, 0, len(presentKeys)+4)
	args = append(args, string(store.InventoryMissing), time.Now().UTC().Format(time.RFC3339), customerID, clusterID, string(store.InventoryMissing))
	for i, key := range presentKeys {
		placeholders[i] = "?"
		args = append(args, key)
	}

	//nolint:gosec // only ? placeholders are injected, never user values
	stmt := fmt.Sprintf(
		`UPDATE release_inventory SET inventory_status = ?, updated_at = ?
		 WHERE customer_id = ? AND cluster_id = ? AND inventory_status != ?
		 AND (namespace || '/' || release_name) NOT IN (%s)`,
		strings.Join(placeholders, ","),
	)

	res, err := s.db.ExecContext(ctx, stmt, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// CreateSyncLog records a sync attempt for idempotency.
// Returns true if inserted (first time), false if already exists.
func (s *inventoryStore) CreateSyncLog(ctx context.Context, log *store.InventorySyncLog) (bool, error) {
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now().UTC()
	}

	const stmt = `INSERT INTO inventory_sync_log
		(sync_id, customer_id, cluster_id, is_full_snapshot, accepted_count, missing_count, snapshot_version, created_at)
	 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	isFullSnapshot := 0
	if log.IsFullSnapshot {
		isFullSnapshot = 1
	}
	_, err := s.db.ExecContext(ctx, stmt,
		log.SyncID, log.CustomerID, log.ClusterID, isFullSnapshot,
		log.AcceptedCount, log.MissingCount, log.SnapshotVersion,
		log.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// GetBySyncID checks whether a sync_id has already been applied.
func (s *inventoryStore) GetBySyncID(ctx context.Context, syncID string) (*store.InventorySyncLog, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT sync_id, customer_id, cluster_id, is_full_snapshot, accepted_count,
		        missing_count, snapshot_version, created_at
		 FROM inventory_sync_log WHERE sync_id = ?`,
		syncID,
	)
	var log store.InventorySyncLog
	var createdAt string
	var isFull int
	err := row.Scan(&log.SyncID, &log.CustomerID, &log.ClusterID, &isFull,
		&log.AcceptedCount, &log.MissingCount, &log.SnapshotVersion, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get sync log: %w", err)
	}
	log.IsFullSnapshot = isFull == 1
	log.CreatedAt, _ = time.Parse(time.RFC3339, createdAt) //nolint:errcheck // stored timestamps always valid RFC3339
	return &log, nil
}

// isUniqueConstraint checks for SQLite unique constraint violation.
func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

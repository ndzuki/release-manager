package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type inventoryStore struct {
	gorm *DB
}

// Upsert inserts or updates an inventory row by unique key.
func (s *inventoryStore) Upsert(ctx context.Context, item *store.ReleaseInventory) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	item.UpdatedAt = time.Now().UTC()

	const stmt = `INSERT INTO release_inventory
		(customer_id, cluster_id, release_definition_id, namespace, release_name, chart, chart_version, revision, status,
		 values_digest, observed_bundle_digest, observed_chart_digest, observed_effective_values_digest,
		 observed_manifest_digest, live_status, last_operation_id, inventory_status, last_sync_id, snapshot_version, created_at, updated_at)
	 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	 ON CONFLICT(customer_id, cluster_id, namespace, release_name) DO UPDATE SET
		release_definition_id = excluded.release_definition_id,
		chart = excluded.chart,
		chart_version = excluded.chart_version,
		revision = excluded.revision,
		status = excluded.status,
		values_digest = excluded.values_digest,
		observed_bundle_digest = excluded.observed_bundle_digest,
		observed_chart_digest = excluded.observed_chart_digest,
		observed_effective_values_digest = excluded.observed_effective_values_digest,
		observed_manifest_digest = excluded.observed_manifest_digest,
		live_status = excluded.live_status,
		last_operation_id = excluded.last_operation_id,
		inventory_status = excluded.inventory_status,
		last_sync_id = excluded.last_sync_id,
		snapshot_version = excluded.snapshot_version,
		updated_at = excluded.updated_at`

	_, err := s.gorm.ExecContext(ctx, stmt,
		item.CustomerID, item.ClusterID, item.ReleaseDefinitionID, item.Namespace, item.ReleaseName,
		item.Chart, item.ChartVersion, item.Revision, item.Status, item.ValuesDigest,
		item.ObservedBundleDigest, item.ObservedChartDigest, item.ObservedEffectiveValuesDigest,
		item.ObservedManifestDigest, item.LiveStatus, item.LastOperationID, string(item.InventoryStatus),
		item.LastSyncID, item.SnapshotVersion, item.CreatedAt.UTC().Format(time.RFC3339), now,
	)
	return err
}

// ListByCluster returns all inventory rows for a cluster.
func (s *inventoryStore) ListByCluster(ctx context.Context, customerID, clusterID string) ([]*store.ReleaseInventory, error) {
	rows, err := s.gorm.QueryContext(ctx,
		`SELECT customer_id, cluster_id, release_definition_id, namespace, release_name, chart, chart_version,
		        revision, status, values_digest, observed_bundle_digest, observed_chart_digest,
		        observed_effective_values_digest, observed_manifest_digest, live_status, last_operation_id,
		        inventory_status, last_sync_id, snapshot_version, created_at, updated_at
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
			&item.CustomerID, &item.ClusterID, &item.ReleaseDefinitionID, &item.Namespace, &item.ReleaseName,
			&item.Chart, &item.ChartVersion, &item.Revision, &item.Status, &item.ValuesDigest,
			&item.ObservedBundleDigest, &item.ObservedChartDigest, &item.ObservedEffectiveValuesDigest,
			&item.ObservedManifestDigest, &item.LiveStatus, &item.LastOperationID, &item.InventoryStatus, &item.LastSyncID,
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

// GetByDefinition returns the inventory row linked to a release definition.
func (s *inventoryStore) GetByDefinition(ctx context.Context, definitionID string) (*store.ReleaseInventory, error) {
	row := s.gorm.QueryRowContext(ctx, `
		SELECT customer_id, cluster_id, release_definition_id, namespace, release_name, chart, chart_version,
		       revision, status, values_digest, observed_bundle_digest, observed_chart_digest,
		       observed_effective_values_digest, observed_manifest_digest, live_status, last_operation_id,
		       inventory_status, last_sync_id, snapshot_version, created_at, updated_at
		FROM release_inventory
		WHERE release_definition_id = ?
	`, definitionID)
	var item store.ReleaseInventory
	if err := row.Scan(
		&item.CustomerID, &item.ClusterID, &item.ReleaseDefinitionID, &item.Namespace, &item.ReleaseName,
		&item.Chart, &item.ChartVersion, &item.Revision, &item.Status, &item.ValuesDigest,
		&item.ObservedBundleDigest, &item.ObservedChartDigest, &item.ObservedEffectiveValuesDigest,
		&item.ObservedManifestDigest, &item.LiveStatus, &item.LastOperationID, &item.InventoryStatus, &item.LastSyncID,
		&item.SnapshotVersion, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("get inventory by definition: %w", err)
	}
	return &item, nil
}

// MarkMissing sets InventoryMissing for all rows in a cluster not present in the given set.
func (s *inventoryStore) MarkMissing(ctx context.Context, customerID, clusterID string, presentKeys []string) (int, error) {
	if len(presentKeys) == 0 {
		// Mark all as missing when no releases are present.
		res, err := s.gorm.ExecContext(ctx,
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

	res, err := s.gorm.ExecContext(ctx, stmt, args...)
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

	isFullSnapshot := log.IsFullSnapshot
	_, err := s.gorm.ExecContext(ctx, stmt,
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
	row := s.gorm.QueryRowContext(ctx,
		`SELECT sync_id, customer_id, cluster_id, is_full_snapshot, accepted_count,
		        missing_count, snapshot_version, created_at
		 FROM inventory_sync_log WHERE sync_id = ?`,
		syncID,
	)
	var log store.InventorySyncLog
	var createdAt string
	var isFull bool
	err := row.Scan(&log.SyncID, &log.CustomerID, &log.ClusterID, &isFull,
		&log.AcceptedCount, &log.MissingCount, &log.SnapshotVersion, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get sync log: %w", err)
	}
	log.IsFullSnapshot = isFull
	log.CreatedAt, _ = time.Parse(time.RFC3339, createdAt) //nolint:errcheck // stored timestamps always valid RFC3339
	return &log, nil
}

const defaultInventoryPageSize = 50

type inventoryCursor struct {
	QueryHash       string `json:"query_hash"`
	SnapshotVersion int64  `json:"snapshot_version"`
	UpdatedAt       string `json:"updated_at"`
	Namespace       string `json:"namespace"`
	ReleaseName     string `json:"release_name"`
}

// Query returns a filtered, stable page of inventory rows for one cluster.
func (s *inventoryStore) Query(ctx context.Context, query store.InventoryQuery) (*store.InventoryPage, error) {
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = defaultInventoryPageSize
	}
	query.PageSize = pageSize

	snapshotVersion, lastSyncAt, err := s.inventoryVersion(ctx, query.CustomerID, query.ClusterID)
	if err != nil {
		return nil, err
	}

	cursor, err := decodeInventoryCursor(query.Cursor)
	if err != nil {
		return nil, store.ErrInvalidCursor
	}
	queryHash := inventoryQueryHash(query)
	if cursor != nil && (cursor.QueryHash != queryHash || cursor.SnapshotVersion != snapshotVersion) {
		return nil, store.ErrInvalidCursor
	}

	statusExpression := `CASE
		WHEN ri.inventory_status = 'missing' THEN 'missing'
		WHEN ri.release_definition_id != ''
		 AND EXISTS (
			SELECT 1 FROM values_revisions vr
			WHERE vr.release_definition_id = ri.release_definition_id
			  AND vr.status = 'approved'
			  AND vr.revision = (
				SELECT MAX(latest.revision) FROM values_revisions latest
				WHERE latest.release_definition_id = ri.release_definition_id
				  AND latest.status = 'approved'
			  )
			  AND vr.digest != ri.values_digest
		 ) THEN 'out_of_sync'
		ELSE 'active'
	END`

	where := []string{"ri.customer_id = ?", "ri.cluster_id = ?"}
	args := []any{query.CustomerID, query.ClusterID}
	if query.Status != "" {
		where = append(where, statusExpression+" = ?")
		args = append(args, string(query.Status))
	}
	if search := strings.TrimSpace(query.NameSearch); search != "" {
		where = append(where, "LOWER(ri.release_name) LIKE ?")
		args = append(args, "%"+strings.ToLower(search)+"%")
	}

	var totalCount int
	countQuery := `SELECT COUNT(*) FROM release_inventory ri WHERE ` + strings.Join(where, " AND ")
	if err := s.gorm.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, fmt.Errorf("count inventory query: %w", err)
	}

	pageWhere := append([]string(nil), where...)
	pageArgs := append([]any(nil), args...)
	if cursor != nil {
		pageWhere = append(pageWhere, `(ri.updated_at > ? OR
		(ri.updated_at = ? AND ri.namespace > ?) OR
		(ri.updated_at = ? AND ri.namespace = ? AND ri.release_name > ?))`)
		pageArgs = append(pageArgs,
			cursor.UpdatedAt,
			cursor.UpdatedAt, cursor.Namespace,
			cursor.UpdatedAt, cursor.Namespace, cursor.ReleaseName,
		)
	}

	pageArgs = append(pageArgs, pageSize+1)
	selectQuery := `SELECT ri.customer_id, ri.cluster_id, ri.release_definition_id, ri.namespace,
		ri.release_name, ri.chart, ri.chart_version, ri.revision, ri.status, ri.values_digest,
		ri.observed_bundle_digest, ri.observed_chart_digest, ri.observed_effective_values_digest,
		ri.observed_manifest_digest, ri.live_status, ri.last_operation_id,
		` + statusExpression + ` AS consistency_status, ri.last_sync_id,
		ri.snapshot_version, ri.created_at, ri.updated_at
		FROM release_inventory ri
		WHERE ` + strings.Join(pageWhere, " AND ") + `
		ORDER BY ri.updated_at, ri.namespace, ri.release_name
		LIMIT ?`

	rows, err := s.gorm.QueryContext(ctx, selectQuery, pageArgs...)
	if err != nil {
		return nil, fmt.Errorf("query inventory page: %w", err)
	}
	defer rows.Close()

	items := make([]*store.ReleaseInventory, 0, min(pageSize, totalCount))
	updatedAts := make([]string, 0, pageSize+1)
	for rows.Next() {
		var item store.ReleaseInventory
		var createdAt, updatedAt string
		if err := rows.Scan(
			&item.CustomerID, &item.ClusterID, &item.ReleaseDefinitionID, &item.Namespace, &item.ReleaseName,
			&item.Chart, &item.ChartVersion, &item.Revision, &item.Status, &item.ValuesDigest,
			&item.ObservedBundleDigest, &item.ObservedChartDigest, &item.ObservedEffectiveValuesDigest,
			&item.ObservedManifestDigest, &item.LiveStatus, &item.LastOperationID, &item.InventoryStatus, &item.LastSyncID,
			&item.SnapshotVersion, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan inventory page: %w", err)
		}
		item.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse inventory created_at: %w", err)
		}
		item.UpdatedAt, err = time.Parse(time.RFC3339, updatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse inventory updated_at: %w", err)
		}
		items = append(items, &item)
		updatedAts = append(updatedAts, updatedAt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inventory page: %w", err)
	}

	var nextCursor string
	if len(items) > pageSize {
		items = items[:pageSize]
		updatedAts = updatedAts[:pageSize]
		last := items[len(items)-1]
		nextCursor, err = encodeInventoryCursor(inventoryCursor{
			QueryHash:       queryHash,
			SnapshotVersion: snapshotVersion,
			UpdatedAt:       updatedAts[len(updatedAts)-1],
			Namespace:       last.Namespace,
			ReleaseName:     last.ReleaseName,
		})
		if err != nil {
			return nil, err
		}
	}

	return &store.InventoryPage{
		Items:      items,
		NextCursor: nextCursor,
		TotalCount: totalCount,
		LastSyncAt: lastSyncAt,
	}, nil
}

func (s *inventoryStore) inventoryVersion(ctx context.Context, customerID, clusterID string) (int64, time.Time, error) {
	var snapshotVersion int64
	if err := s.gorm.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(snapshot_version), 0)
		FROM release_inventory
		WHERE customer_id = ? AND cluster_id = ?`, customerID, clusterID,
	).Scan(&snapshotVersion); err != nil {
		return 0, time.Time{}, fmt.Errorf("query inventory version: %w", err)
	}

	var syncedAt string
	err := s.gorm.QueryRowContext(ctx, `
		SELECT created_at
		FROM inventory_sync_log
		WHERE customer_id = ? AND cluster_id = ?
		ORDER BY snapshot_version DESC, created_at DESC
		LIMIT 1`, customerID, clusterID,
	).Scan(&syncedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshotVersion, time.Time{}, nil
	}
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("query inventory last sync: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339, syncedAt)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("parse inventory last sync: %w", err)
	}
	return snapshotVersion, parsed, nil
}

func inventoryQueryHash(query store.InventoryQuery) string {
	payload := strings.Join([]string{
		query.CustomerID,
		query.ClusterID,
		string(query.Status),
		strings.ToLower(strings.TrimSpace(query.NameSearch)),
		fmt.Sprintf("%d", query.PageSize),
	}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func encodeInventoryCursor(cursor inventoryCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode inventory cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeInventoryCursor(encoded string) (*inventoryCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	var cursor inventoryCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return nil, err
	}
	if cursor.QueryHash == "" || cursor.UpdatedAt == "" || cursor.Namespace == "" || cursor.ReleaseName == "" {
		return nil, errors.New("incomplete inventory cursor")
	}
	return &cursor, nil
}

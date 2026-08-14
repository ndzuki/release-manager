package sqlite

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

const defaultInventoryPageSize = 50

type inventoryCursor struct {
	QueryHash       string `json:"query_hash"`
	SnapshotVersion int64  `json:"snapshot_version"`
	UpdatedAt       string `json:"updated_at"`
	Namespace       string `json:"namespace"`
	ReleaseName     string `json:"release_name"`
}

// Query returns a filtered, stable page of inventory rows for one cluster.
//
//nolint:gocyclo // stable pagination combines cursor, filters, and consistency projection
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
			  AND vr.version = (
				SELECT MAX(latest.version) FROM values_revisions latest
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
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount); err != nil {
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
	// #nosec G202 -- statusExpression and pageWhere contain only fixed SQL fragments; all user values stay parameterized.
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

	rows, err := s.db.QueryContext(ctx, selectQuery, pageArgs...)
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
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(snapshot_version), 0)
		FROM release_inventory
		WHERE customer_id = ? AND cluster_id = ?`, customerID, clusterID,
	).Scan(&snapshotVersion); err != nil {
		return 0, time.Time{}, fmt.Errorf("query inventory version: %w", err)
	}

	var syncedAt string
	err := s.db.QueryRowContext(ctx, `
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

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type inventorySyncRequestStore struct{ gorm *DB }

func (s *inventorySyncRequestStore) CreateIfAvailable(
	ctx context.Context,
	request *store.InventorySyncRequest,
	outbox *store.OutboxEntry,
) (*store.InventorySyncRequest, bool, error) {
	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin inventory sync request: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback is a no-op after Commit.

	existing, err := getActiveInventorySyncRequest(ctx, tx, request.CustomerID, request.ClusterID)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, false, err
	}

	now := time.Now().UTC()
	if request.Status == "" {
		request.Status = store.InventorySyncPending
	}
	if request.CreatedAt.IsZero() {
		request.CreatedAt = now
	}
	if request.UpdatedAt.IsZero() {
		request.UpdatedAt = request.CreatedAt
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO inventory_sync_requests (
			id, customer_id, cluster_id, operator_id, command_id, status, last_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, request.ID, request.CustomerID, request.ClusterID, request.OperatorID, request.CommandID,
		string(request.Status), request.LastError, request.CreatedAt.UTC(), request.UpdatedAt.UTC()); err != nil {
		if isUniqueConstraint(err) {
			existing, getErr := getActiveInventorySyncRequest(ctx, tx, request.CustomerID, request.ClusterID)
			if getErr == nil {
				return existing, false, nil
			}
		}
		return nil, false, fmt.Errorf("insert inventory sync request: %w", err)
	}
	if outbox != nil {
		if err := createOutboxEntry(ctx, tx, outbox); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit inventory sync request: %w", err)
	}
	return request, true, nil
}

func (s *inventorySyncRequestStore) Get(ctx context.Context, id string) (*store.InventorySyncRequest, error) {
	return scanInventorySyncRequest(s.gorm.QueryRowContext(ctx, inventorySyncRequestSelect+` WHERE id = ?`, id))
}

func (s *inventorySyncRequestStore) GetActiveByCluster(ctx context.Context, customerID, clusterID string) (*store.InventorySyncRequest, error) {
	return getActiveInventorySyncRequest(ctx, s.gorm, customerID, clusterID)
}

func getActiveInventorySyncRequest(ctx context.Context, queryer operationQueryer, customerID, clusterID string) (*store.InventorySyncRequest, error) {
	return scanInventorySyncRequest(queryer.QueryRowContext(ctx, inventorySyncRequestSelect+`
		WHERE customer_id = ? AND cluster_id = ? AND status IN ('pending','running')
		ORDER BY created_at DESC LIMIT 1
	`, customerID, clusterID))
}

func (s *inventorySyncRequestStore) UpdateStatus(ctx context.Context, id string, status store.InventorySyncRequestStatus, lastError string) error {
	result, err := s.gorm.ExecContext(ctx, `
		UPDATE inventory_sync_requests SET status = ?, last_error = ?, updated_at = ? WHERE id = ?
	`, string(status), lastError, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("update inventory sync request: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inventory sync rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

const inventorySyncRequestSelect = `
	SELECT id, customer_id, cluster_id, operator_id, command_id, status, last_error, created_at, updated_at
	FROM inventory_sync_requests`

func scanInventorySyncRequest(row interface{ Scan(...any) error }) (*store.InventorySyncRequest, error) {
	var request store.InventorySyncRequest
	var status string
	if err := row.Scan(&request.ID, &request.CustomerID, &request.ClusterID, &request.OperatorID,
		&request.CommandID, &status, &request.LastError, &request.CreatedAt, &request.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan inventory sync request: %w", err)
	}
	request.Status = store.InventorySyncRequestStatus(status)
	request.CreatedAt = request.CreatedAt.UTC()
	request.UpdatedAt = request.UpdatedAt.UTC()
	return &request, nil
}

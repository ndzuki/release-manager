package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
	"gorm.io/gorm"
)

type inventorySyncRequestStore struct{ gorm *DB }

func (s *inventorySyncRequestStore) CreateIfAvailable(
	ctx context.Context,
	request *store.InventorySyncRequest,
	outbox *store.OutboxEntry,
) (*store.InventorySyncRequest, bool, error) {
	now := time.Now().UTC()
	if request.CreatedAt.IsZero() {
		request.CreatedAt = now
	}
	request.UpdatedAt = now
	if request.Status == "" {
		request.Status = store.InventorySyncPending
	}
	if outbox.CreatedAt.IsZero() {
		outbox.CreatedAt = request.CreatedAt
	}
	if outbox.UpdatedAt.IsZero() {
		outbox.UpdatedAt = outbox.CreatedAt
	}
	if outbox.Status == "" {
		outbox.Status = store.CommandPending
	}

	err := s.gorm.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing store.InventorySyncRequest
		var status string
		err := tx.Raw(`
			SELECT id, customer_id, cluster_id, operator_id, command_id, status, last_error, created_at, updated_at
			FROM inventory_sync_requests
			WHERE customer_id = ? AND cluster_id = ? AND status IN ('pending', 'running')
			LIMIT 1 FOR UPDATE
		`, request.CustomerID, request.ClusterID).Row().Scan(
			&existing.ID, &existing.CustomerID, &existing.ClusterID, &existing.OperatorID,
			&existing.CommandID, &status, &existing.LastError, &existing.CreatedAt, &existing.UpdatedAt,
		)
		if err == nil {
			existing.Status = store.InventorySyncRequestStatus(status)
			*request = existing
			return store.ErrDuplicateKey
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("get active inventory sync request: %w", err)
		}
		if err := tx.Exec(`
			INSERT INTO inventory_sync_requests
				(id, customer_id, cluster_id, operator_id, command_id, status, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, request.ID, request.CustomerID, request.ClusterID, request.OperatorID, request.CommandID,
			string(request.Status), request.LastError, request.CreatedAt, request.UpdatedAt).Error; err != nil {
			return fmt.Errorf("insert inventory sync request: %w", err)
		}
		outboxStore := &outboxStore{gorm: &DB{gorm: tx}}
		if err := outboxStore.Create(ctx, outbox); err != nil {
			return fmt.Errorf("insert inventory sync outbox: %w", err)
		}
		return nil
	})
	if errors.Is(err, store.ErrDuplicateKey) {
		return request, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return request, true, nil
}

func (s *inventorySyncRequestStore) Get(ctx context.Context, id string) (*store.InventorySyncRequest, error) {
	return s.scan(ctx, `WHERE id = ?`, id)
}

func (s *inventorySyncRequestStore) GetActiveByCluster(ctx context.Context, customerID, clusterID string) (*store.InventorySyncRequest, error) {
	return s.scan(ctx, `WHERE customer_id = ? AND cluster_id = ? AND status IN ('pending', 'running') LIMIT 1`, customerID, clusterID)
}

func (s *inventorySyncRequestStore) scan(ctx context.Context, suffix string, args ...any) (*store.InventorySyncRequest, error) {
	row := s.gorm.QueryRowContext(ctx, `
		SELECT id, customer_id, cluster_id, operator_id, command_id, status, last_error, created_at, updated_at
		FROM inventory_sync_requests `+suffix, args...)
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
	return &request, nil
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
		return fmt.Errorf("inventory sync request rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

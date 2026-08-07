package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type inventorySyncRequestStore struct{ db *sql.DB }

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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin inventory sync request: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	existing, err := getActiveInventorySyncRequest(ctx, tx, request.CustomerID, request.ClusterID)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, false, err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO inventory_sync_requests
		(id, customer_id, cluster_id, operator_id, command_id, status, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		request.ID, request.CustomerID, request.ClusterID, request.OperatorID, request.CommandID,
		string(request.Status), request.LastError,
		request.CreatedAt.Format(time.RFC3339Nano), request.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			existing, getErr := getActiveInventorySyncRequest(ctx, tx, request.CustomerID, request.ClusterID)
			if getErr == nil {
				return existing, false, nil
			}
		}
		return nil, false, fmt.Errorf("insert inventory sync request: %w", err)
	}

	if err := createOutboxEntry(ctx, tx, outbox); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit inventory sync request: %w", err)
	}
	return request, true, nil
}

func (s *inventorySyncRequestStore) Get(ctx context.Context, id string) (*store.InventorySyncRequest, error) {
	return scanInventorySyncRequest(s.db.QueryRowContext(ctx, `
		SELECT id, customer_id, cluster_id, operator_id, command_id, status, last_error, created_at, updated_at
		FROM inventory_sync_requests WHERE id = ?`, id))
}

func (s *inventorySyncRequestStore) GetActiveByCluster(ctx context.Context, customerID, clusterID string) (*store.InventorySyncRequest, error) {
	return getActiveInventorySyncRequest(ctx, s.db, customerID, clusterID)
}

func (s *inventorySyncRequestStore) UpdateStatus(ctx context.Context, id string, status store.InventorySyncRequestStatus, lastError string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE inventory_sync_requests SET status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		string(status), lastError, time.Now().UTC().Format(time.RFC3339Nano), id,
	)
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

type inventorySyncRequestQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getActiveInventorySyncRequest(
	ctx context.Context,
	queryer inventorySyncRequestQueryer,
	customerID, clusterID string,
) (*store.InventorySyncRequest, error) {
	return scanInventorySyncRequest(queryer.QueryRowContext(ctx, `
		SELECT id, customer_id, cluster_id, operator_id, command_id, status, last_error, created_at, updated_at
		FROM inventory_sync_requests
		WHERE customer_id = ? AND cluster_id = ? AND status IN ('pending', 'running')
		ORDER BY created_at DESC LIMIT 1`, customerID, clusterID))
}

func scanInventorySyncRequest(row interface{ Scan(...any) error }) (*store.InventorySyncRequest, error) {
	var request store.InventorySyncRequest
	var status, createdAt, updatedAt string
	if err := row.Scan(
		&request.ID, &request.CustomerID, &request.ClusterID, &request.OperatorID,
		&request.CommandID, &status, &request.LastError, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan inventory sync request: %w", err)
	}
	request.Status = store.InventorySyncRequestStatus(status)
	var err error
	request.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse inventory sync request created_at: %w", err)
	}
	request.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse inventory sync request updated_at: %w", err)
	}
	return &request, nil
}

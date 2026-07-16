package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type clusterStore struct{ db *sql.DB }

func (s *clusterStore) Create(ctx context.Context, c *store.Cluster) error {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = c.CreatedAt
	}
	if c.Status == "" {
		c.Status = store.ClusterActive
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO clusters (id, name, customer_id, kubeconfig_ref, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
`,
		c.ID, c.Name, c.CustomerID, c.KubeconfigRef, string(c.Status),
		c.CreatedAt.UTC().Format(time.RFC3339), c.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert cluster: %w", err)
	}
	return nil
}

func (s *clusterStore) Get(ctx context.Context, id string) (*store.Cluster, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, name, customer_id, kubeconfig_ref, status, created_at, updated_at
FROM clusters WHERE id = ?
`, id)
	return scanCluster(row)
}

func (s *clusterStore) Update(ctx context.Context, c *store.Cluster) error {
	c.UpdatedAt = time.Now().UTC()

	_, err := s.db.ExecContext(ctx, `
UPDATE clusters SET name=?, kubeconfig_ref=?, status=?, updated_at=?
WHERE id=?
`,
		c.Name, c.KubeconfigRef, string(c.Status),
		c.UpdatedAt.UTC().Format(time.RFC3339), c.ID,
	)
	if err != nil {
		return fmt.Errorf("update cluster: %w", err)
	}
	return nil
}

func (s *clusterStore) List(ctx context.Context, customerID string) ([]*store.Cluster, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, customer_id, kubeconfig_ref, status, created_at, updated_at
FROM clusters WHERE customer_id = ? ORDER BY created_at DESC
`, customerID)
	if err != nil {
		return nil, fmt.Errorf("list clusters: %w", err)
	}
	defer rows.Close()
	return scanClusters(rows)
}

func (s *clusterStore) ListAll(ctx context.Context) ([]*store.Cluster, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, customer_id, kubeconfig_ref, status, created_at, updated_at
FROM clusters ORDER BY created_at DESC
`)
	if err != nil {
		return nil, fmt.Errorf("list all clusters: %w", err)
	}
	defer rows.Close()
	return scanClusters(rows)
}

func scanCluster(row interface{ Scan(...interface{}) error }) (*store.Cluster, error) {
	var (
		id, name, customerID, kubeconfigRef, status string
		createdAt, updatedAt                        string
	)
	if err := row.Scan(&id, &name, &customerID, &kubeconfigRef, &status, &createdAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan cluster: %w", err)
	}

	ct, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse cluster created_at: %w", err)
	}
	ut, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse cluster updated_at: %w", err)
	}

	return &store.Cluster{
		ID:            id,
		Name:          name,
		CustomerID:    customerID,
		KubeconfigRef: kubeconfigRef,
		Status:        store.ClusterStatus(status),
		CreatedAt:     ct,
		UpdatedAt:     ut,
	}, nil
}

func scanClusters(rows *sql.Rows) ([]*store.Cluster, error) {
	var clusters []*store.Cluster
	for rows.Next() {
		var (
			id, name, customerID, kubeconfigRef, status string
			createdAt, updatedAt                        string
		)
		if err := rows.Scan(&id, &name, &customerID, &kubeconfigRef, &status, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan cluster row: %w", err)
		}

		ct, err := time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse cluster created_at: %w", err)
		}
		ut, err := time.Parse(time.RFC3339, updatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse cluster updated_at: %w", err)
		}

		clusters = append(clusters, &store.Cluster{
			ID:            id,
			Name:          name,
			CustomerID:    customerID,
			KubeconfigRef: kubeconfigRef,
			Status:        store.ClusterStatus(status),
			CreatedAt:     ct,
			UpdatedAt:     ut,
		})
	}
	return clusters, rows.Err()
}

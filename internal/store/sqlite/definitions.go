package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type definitionStore struct{ db *sql.DB }

func (s *definitionStore) Create(ctx context.Context, def *store.ReleaseDefinition) error {
	if def.CreatedAt.IsZero() {
		def.CreatedAt = time.Now().UTC()
	}
	if def.UpdatedAt.IsZero() {
		def.UpdatedAt = def.CreatedAt
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO release_definitions (
			id, name, customer_id, cluster_id, namespace, release_name,
			chart_name, hpa_managed, status, optimistic_version, created_by,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		def.ID, def.Name, def.CustomerID, def.ClusterID,
		def.Namespace, def.ReleaseName, def.ChartName, def.HPAManaged,
		string(def.Status), def.OptimisticVersion, def.CreatedBy,
		def.CreatedAt.UTC().Format(time.RFC3339), def.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert definition: %w", err)
	}
	return nil
}

func (s *definitionStore) Get(ctx context.Context, id string) (*store.ReleaseDefinition, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, customer_id, cluster_id, namespace, release_name,
			chart_name, hpa_managed, status, optimistic_version, created_by,
			created_at, updated_at
		FROM release_definitions WHERE id = ?
	`, id)
	return scanDefinition(row)
}

func (s *definitionStore) Update(ctx context.Context, def *store.ReleaseDefinition) error {
	now := nowUTC()
	def.UpdatedAt = time.Now().UTC()

	result, err := s.db.ExecContext(ctx, `
		UPDATE release_definitions
		SET name = ?, customer_id = ?, cluster_id = ?, namespace = ?,
		    release_name = ?, chart_name = ?, hpa_managed = ?, status = ?,
		    optimistic_version = optimistic_version + 1,
		    updated_at = ?
		WHERE id = ? AND optimistic_version = ?
	`,
		def.Name, def.CustomerID, def.ClusterID,
		def.Namespace, def.ReleaseName, def.ChartName, def.HPAManaged,
		string(def.Status), now, def.ID, def.OptimisticVersion,
	)
	if err != nil {
		return fmt.Errorf("update definition: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrOptimisticLock
	}
	return nil
}

func (s *definitionStore) List(ctx context.Context) ([]*store.ReleaseDefinition, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, customer_id, cluster_id, namespace, release_name,
			chart_name, hpa_managed, status, optimistic_version, created_by,
			created_at, updated_at
		FROM release_definitions
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list definitions: %w", err)
	}
	defer rows.Close()

	var defs []*store.ReleaseDefinition
	for rows.Next() {
		def, err := scanDefinition(rows)
		if err != nil {
			return nil, err
		}
		defs = append(defs, def)
	}
	return defs, rows.Err()
}

func scanDefinition(row interface{ Scan(...interface{}) error }) (*store.ReleaseDefinition, error) {
	var (
		id, name, customerID, clusterID, namespace, releaseName, chartName string
		hpaManaged                                                         bool
		status                                                             string
		optimisticVersion                                                  int
		createdBy                                                          string
		createdAt, updatedAt                                               string
	)

	err := row.Scan(
		&id, &name, &customerID, &clusterID, &namespace, &releaseName,
		&chartName, &hpaManaged, &status, &optimisticVersion, &createdBy,
		&createdAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan definition: %w", err)
	}

	ct, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	ut, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}

	return &store.ReleaseDefinition{
		ID:                id,
		Name:              name,
		CustomerID:        customerID,
		ClusterID:         clusterID,
		Namespace:         namespace,
		ReleaseName:       releaseName,
		ChartName:         chartName,
		HPAManaged:        hpaManaged,
		Status:            store.DefinitionStatus(status),
		OptimisticVersion: optimisticVersion,
		CreatedBy:         createdBy,
		CreatedAt:         ct,
		UpdatedAt:         ut,
	}, nil
}

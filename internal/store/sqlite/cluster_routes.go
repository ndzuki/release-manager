package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type clusterRouteStore struct{ db *sql.DB }

func (s *clusterRouteStore) Create(ctx context.Context, r *store.ClusterRoute) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = r.CreatedAt
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO cluster_routes (id, cluster_id, artifact_type, mode, source_prefix, target_prefix, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`,
		r.ID, r.ClusterID, string(r.ArtifactType), string(r.Mode),
		r.SourcePrefix, r.TargetPrefix,
		r.CreatedAt.UTC().Format(time.RFC3339), r.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert cluster_route: %w", err)
	}
	return nil
}
func (s *clusterRouteStore) Update(ctx context.Context, r *store.ClusterRoute) error {
	r.UpdatedAt = time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
UPDATE cluster_routes SET artifact_type = ?, mode = ?, source_prefix = ?, target_prefix = ?, updated_at = ?
WHERE id = ? AND cluster_id = ?
`,
		string(r.ArtifactType), string(r.Mode), r.SourcePrefix, r.TargetPrefix,
		r.UpdatedAt.UTC().Format(time.RFC3339),
		r.ID, r.ClusterID,
	)
	if err != nil {
		return fmt.Errorf("update cluster_route: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update cluster_route rows_affected: %w", err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *clusterRouteStore) Get(ctx context.Context, id string) (*store.ClusterRoute, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, cluster_id, artifact_type, mode, source_prefix, target_prefix, created_at, updated_at
FROM cluster_routes WHERE id = ?
`, id)
	return scanClusterRoute(row)
}

func (s *clusterRouteStore) ListByCluster(ctx context.Context, clusterID string) ([]*store.ClusterRoute, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, cluster_id, artifact_type, mode, source_prefix, target_prefix, created_at, updated_at
FROM cluster_routes WHERE cluster_id = ? ORDER BY artifact_type, source_prefix
`, clusterID)
	if err != nil {
		return nil, fmt.Errorf("list cluster_routes: %w", err)
	}
	defer rows.Close()
	return scanClusterRoutes(rows)
}

func (s *clusterRouteStore) ListByClusterAndType(ctx context.Context, clusterID string, artifactType store.ArtifactType) ([]*store.ClusterRoute, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, cluster_id, artifact_type, mode, source_prefix, target_prefix, created_at, updated_at
FROM cluster_routes WHERE cluster_id = ? AND artifact_type = ? ORDER BY source_prefix
`, clusterID, string(artifactType))
	if err != nil {
		return nil, fmt.Errorf("list cluster_routes by type: %w", err)
	}
	defer rows.Close()
	return scanClusterRoutes(rows)
}

func (s *clusterRouteStore) Delete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM cluster_routes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete cluster_route: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete cluster_route rows_affected: %w", err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func scanClusterRoute(row interface{ Scan(...interface{}) error }) (*store.ClusterRoute, error) {
	var (
		id, clusterID, artifactType, mode, sourcePrefix, targetPrefix string
		createdAt, updatedAt                                          string
	)
	if err := row.Scan(&id, &clusterID, &artifactType, &mode, &sourcePrefix, &targetPrefix, &createdAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan cluster_route: %w", err)
	}

	ct, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse cluster_route created_at: %w", err)
	}
	ut, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse cluster_route updated_at: %w", err)
	}

	return &store.ClusterRoute{
		ID:           id,
		ClusterID:    clusterID,
		ArtifactType: store.ArtifactType(artifactType),
		Mode:         store.ArtifactMode(mode),
		SourcePrefix: sourcePrefix,
		TargetPrefix: targetPrefix,
		CreatedAt:    ct,
		UpdatedAt:    ut,
	}, nil
}

func scanClusterRoutes(rows *sql.Rows) ([]*store.ClusterRoute, error) {
	var routes []*store.ClusterRoute
	for rows.Next() {
		var (
			id, clusterID, artifactType, mode, sourcePrefix, targetPrefix string
			createdAt, updatedAt                                          string
		)
		if err := rows.Scan(&id, &clusterID, &artifactType, &mode, &sourcePrefix, &targetPrefix, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan cluster_routes row: %w", err)
		}
		ct, err := time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse cluster_route created_at: %w", err)
		}
		ut, err := time.Parse(time.RFC3339, updatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse cluster_route updated_at: %w", err)
		}
		routes = append(routes, &store.ClusterRoute{
			ID:           id,
			ClusterID:    clusterID,
			ArtifactType: store.ArtifactType(artifactType),
			Mode:         store.ArtifactMode(mode),
			SourcePrefix: sourcePrefix,
			TargetPrefix: targetPrefix,
			CreatedAt:    ct,
			UpdatedAt:    ut,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cluster_routes: %w", err)
	}
	return routes, nil
}

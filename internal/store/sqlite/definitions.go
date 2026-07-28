package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type definitionStore struct{ db *sql.DB }

func (s *definitionStore) Create(
	ctx context.Context,
	def *store.ReleaseDefinition,
	event *store.ReleaseDefinitionEvent,
) error {
	if def.CreatedAt.IsZero() {
		def.CreatedAt = time.Now().UTC()
	}
	if def.UpdatedAt.IsZero() {
		def.UpdatedAt = def.CreatedAt
	}
	if def.OptimisticVersion == 0 {
		def.OptimisticVersion = 1
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create definition: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback after commit is a no-op.

	_, err = tx.ExecContext(ctx, `
		INSERT INTO release_definitions (
			id, name, customer_id, cluster_id, namespace, release_name,
			chart_name, status, optimistic_version, created_by,
			owner_organization_id, approved_revision_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		def.ID, def.Name, def.CustomerID, def.ClusterID,
		def.Namespace, def.ReleaseName, def.ChartName,
		string(def.Status), def.OptimisticVersion, def.CreatedBy,
		def.OwnerOrganizationID, def.ApprovedRevisionID,
		def.CreatedAt.UTC().Format(time.RFC3339Nano), def.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return store.ErrDuplicateKey
		}
		return fmt.Errorf("insert definition: %w", err)
	}
	if err := insertDefinitionEvent(ctx, tx, event); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create definition: %w", err)
	}
	return nil
}

func (s *definitionStore) Get(ctx context.Context, id string) (*store.ReleaseDefinition, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, customer_id, cluster_id, namespace, release_name,
			chart_name, status, optimistic_version, current_bundle_id, created_by,
			owner_organization_id, approved_revision_id, created_at, updated_at
		FROM release_definitions WHERE id = ?
	`, id)
	return scanDefinition(row)
}

func (s *definitionStore) Update(
	ctx context.Context,
	def *store.ReleaseDefinition,
	event *store.ReleaseDefinitionEvent,
) (*store.ReleaseDefinition, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin update definition: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback after commit is a no-op.

	updatedAt := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `
		UPDATE release_definitions
		SET name = ?, customer_id = ?, cluster_id = ?, namespace = ?,
		    release_name = ?, chart_name = ?, status = ?,
		    optimistic_version = optimistic_version + 1,
		    updated_at = ?
		WHERE id = ? AND optimistic_version = ?
	`,
		def.Name, def.CustomerID, def.ClusterID,
		def.Namespace, def.ReleaseName, def.ChartName,
		string(def.Status), updatedAt.Format(time.RFC3339Nano), def.ID, def.OptimisticVersion,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return nil, store.ErrDuplicateKey
		}
		return nil, fmt.Errorf("update definition: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("definition rows affected: %w", err)
	}
	if rows == 0 {
		return nil, store.ErrOptimisticLock
	}
	if err := insertDefinitionEvent(ctx, tx, event); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit update definition: %w", err)
	}
	return s.Get(ctx, def.ID)
}

func (s *definitionStore) List(
	ctx context.Context,
	customerID, clusterID string,
	includeDisabled bool,
) ([]*store.ReleaseDefinition, error) {
	query := `
		SELECT id, name, customer_id, cluster_id, namespace, release_name,
			chart_name, status, optimistic_version, current_bundle_id, created_by,
			owner_organization_id, approved_revision_id, created_at, updated_at
		FROM release_definitions
		WHERE 1 = 1`
	args := make([]any, 0, 2)
	if customerID != "" {
		query += ` AND customer_id = ?`
		args = append(args, customerID)
	}
	if clusterID != "" {
		query += ` AND cluster_id = ?`
		args = append(args, clusterID)
	}
	if !includeDisabled {
		query += ` AND status != 'disabled'`
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list definitions: %w", err)
	}
	defer rows.Close()

	defs := make([]*store.ReleaseDefinition, 0)
	for rows.Next() {
		def, err := scanDefinition(rows)
		if err != nil {
			return nil, err
		}
		defs = append(defs, def)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate definitions: %w", err)
	}
	return defs, nil
}

func insertDefinitionEvent(
	ctx context.Context,
	tx *sql.Tx,
	event *store.ReleaseDefinitionEvent,
) error {
	if event == nil {
		return nil
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO release_definition_events (id, definition_id, event_type, created_at)
		VALUES (?, ?, ?, ?)
	`, event.ID, event.DefinitionID, event.EventType, event.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert definition event: %w", err)
	}
	return nil
}

func scanDefinition(row interface{ Scan(...interface{}) error }) (*store.ReleaseDefinition, error) {
	var (
		id, name, customerID, clusterID, namespace, releaseName, chartName string
		status                                                             string
		optimisticVersion                                                  int
		currentBundleID, ownerOrganizationID, approvedRevisionID           *string
		createdBy                                                          string
		createdAt, updatedAt                                               string
	)

	err := row.Scan(
		&id, &name, &customerID, &clusterID, &namespace, &releaseName,
		&chartName, &status, &optimisticVersion, &currentBundleID, &createdBy,
		&ownerOrganizationID, &approvedRevisionID, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan definition: %w", err)
	}

	ct, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	ut, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}

	return &store.ReleaseDefinition{
		ID:                  id,
		Name:                name,
		CustomerID:          customerID,
		ClusterID:           clusterID,
		Namespace:           namespace,
		ReleaseName:         releaseName,
		ChartName:           chartName,
		Status:              store.DefinitionStatus(status),
		OptimisticVersion:   optimisticVersion,
		CurrentBundleID:     currentBundleID,
		CreatedBy:           createdBy,
		OwnerOrganizationID: ownerOrganizationID,
		ApprovedRevisionID:  approvedRevisionID,
		CreatedAt:           ct,
		UpdatedAt:           ut,
	}, nil
}

// SetCurrentBundle associates a bundle with a release definition.
// If the bundle is archived, it is unarchived in the same transaction.
// Returns true if the bundle was unarchived.
func (s *definitionStore) SetCurrentBundle(ctx context.Context, defID, bundleID string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin set current bundle: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback after Commit is a no-op.

	result, err := tx.ExecContext(ctx, `
		UPDATE release_definitions SET current_bundle_id = ?
		WHERE id = ?
	`, bundleID, defID)
	if err != nil {
		return false, fmt.Errorf("update definition current_bundle_id: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("definition rows affected: %w", err)
	}
	if n == 0 {
		return false, fmt.Errorf("definition %s: %w", defID, store.ErrNotFound)
	}

	// If the referenced bundle is archived, unarchive it.
	ur, err := tx.ExecContext(ctx, `
		UPDATE release_bundles SET status = 'validated', archived_at = NULL
		WHERE id = ? AND status = 'archived'
	`, bundleID)
	if err != nil {
		return false, fmt.Errorf("unarchive bundle %s: %w", bundleID, err)
	}
	unarchived, err := ur.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("unarchive bundle rows affected: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit set current bundle: %w", err)
	}
	return unarchived > 0, nil
}

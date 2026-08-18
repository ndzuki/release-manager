package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
	"gorm.io/gorm"
)

type bundleStore struct{ db *sql.DB }

func (s *bundleStore) Create(ctx context.Context, b *store.ReleaseBundle) error {
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now().UTC()
	}

	imagesJSON, err := json.Marshal(b.Images)
	if err != nil {
		return fmt.Errorf("marshal images: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO release_bundles (
			id, name, digest_alg, digest_value, status,
			chart_ref, chart_version, chart_digest,
			images,
			git_commit, pipeline_id,
			signature_ref, sbom_ref, provenance_ref,
			created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		b.ID, b.Name, b.DigestAlg, b.DigestValue, string(b.Status),
		b.ChartRef, b.ChartVersion, b.ChartDigest,
		string(imagesJSON),
		b.GitCommit, b.PipelineID,
		b.SignatureRef, b.SBOMRef, b.ProvenanceRef,
		b.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert release_bundle: %w", err)
	}
	return nil
}

func (s *bundleStore) Get(ctx context.Context, id string) (*store.ReleaseBundle, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, digest_alg, digest_value, status,
			chart_ref, chart_version, chart_digest,
			images,
			git_commit, pipeline_id,
			signature_ref, sbom_ref, provenance_ref,
			archived_at, archived_from_status, created_at
		FROM release_bundles WHERE id = ?
	`, id)
	return scanBundle(row)
}

func (s *bundleStore) GetByDigest(ctx context.Context, alg, value string) (*store.ReleaseBundle, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, digest_alg, digest_value, status,
			chart_ref, chart_version, chart_digest,
			images,
			git_commit, pipeline_id,
			signature_ref, sbom_ref, provenance_ref,
			archived_at, archived_from_status, created_at
		FROM release_bundles WHERE digest_alg = ? AND digest_value = ?
	`, alg, value)
	return scanBundle(row)
}

func scanBundle(row interface{ Scan(...interface{}) error }) (*store.ReleaseBundle, error) {
	var (
		id, name, digestAlg, digestValue, status string
		chartRef, chartVersion, chartDigest      string
		imagesJSON                               string
		gitCommit, pipelineID                    string
		sigRef, sbomRef, provRef                 string
		archivedAt, archivedFromStatus           *string
		createdAt                                string
	)

	if err := row.Scan(
		&id, &name, &digestAlg, &digestValue, &status,
		&chartRef, &chartVersion, &chartDigest,
		&imagesJSON,
		&gitCommit, &pipelineID,
		&sigRef, &sbomRef, &provRef,
		&archivedAt, &archivedFromStatus,
		&createdAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan release_bundle: %w", err)
	}

	var images []store.BundleImage
	if imagesJSON != "" {
		if err := json.Unmarshal([]byte(imagesJSON), &images); err != nil {
			return nil, fmt.Errorf("unmarshal bundle images: %w", err)
		}
	}

	ts, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}

	var archived *time.Time
	if archivedAt != nil {
		at, err := time.Parse(time.RFC3339, *archivedAt)
		if err != nil {
			return nil, fmt.Errorf("parse archived_at: %w", err)
		}
		archived = &at
	}
	var archivedFrom *store.BundleStatus
	if archivedFromStatus != nil && *archivedFromStatus != "" {
		status := store.BundleStatus(*archivedFromStatus)
		archivedFrom = &status
	}

	return &store.ReleaseBundle{
		ID:                 id,
		Name:               name,
		DigestAlg:          digestAlg,
		DigestValue:        digestValue,
		Status:             store.BundleStatus(status),
		ChartRef:           chartRef,
		ChartVersion:       chartVersion,
		ChartDigest:        chartDigest,
		Images:             images,
		GitCommit:          gitCommit,
		PipelineID:         pipelineID,
		SignatureRef:       sigRef,
		SBOMRef:            sbomRef,
		ProvenanceRef:      provRef,
		ArchivedAt:         archived,
		ArchivedFromStatus: archivedFrom,
		CreatedAt:          ts,
	}, nil
}

// ListForArchive returns bundle IDs eligible for archival.
// Eligible: status IN ('received','validated','rejected'), created_at < now - retentionDays,
// NOT referenced by any active definition (via current_bundle_id),
// NOT referenced by any non-terminal operation.
func (s *bundleStore) ListForArchive(ctx context.Context, retentionDays int, terminalStates []store.OperationStatus, limits ...int) ([]string, error) {
	limit := 100
	if len(limits) > 0 && limits[0] > 0 && limits[0] < limit {
		limit = limits[0]
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339)
	terminal := make([]string, len(terminalStates))
	for i, status := range terminalStates {
		terminal[i] = string(status)
	}
	//nolint:gosec // only generated placeholders are concatenated; bundle IDs and statuses remain bound parameters
	rows, err := s.db.QueryContext(ctx, `
        SELECT b.id FROM release_bundles b
        WHERE b.status IN ('received','validated','rejected') AND b.created_at < ?
          AND NOT EXISTS (SELECT 1 FROM release_definitions d WHERE d.current_bundle_id = b.id AND d.status = 'active')
          AND NOT EXISTS (SELECT 1 FROM operations o WHERE o.bundle_id = b.id AND o.status NOT IN (`+placeholders(len(terminal))+`))
        ORDER BY b.created_at, b.id LIMIT ?
    `, append(append([]any{cutoff}, stringsToAny(terminal)...), limit)...)
	if err != nil {
		return nil, fmt.Errorf("list bundles for archive: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan bundle id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Archive marks the given bundles as archived.
func (s *bundleStore) Archive(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin archive: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback is a no-op after successful Commit.

	now := time.Now().UTC().Format(time.RFC3339)
	//nolint:gosec // only generated placeholders are concatenated; bundle IDs remain bound parameters
	result, err := tx.ExecContext(ctx, `
		UPDATE release_bundles
		SET archived_from_status = status, status = 'archived', archived_at = ?
		WHERE id IN (`+placeholders(len(ids))+`) AND status IN ('received','validated','rejected')
	`, append([]any{now}, stringsToAny(ids)...)...)
	if err != nil {
		return 0, fmt.Errorf("archive bundles: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit archive: %w", err)
	}
	return result.RowsAffected()
}

// DeleteBefore deletes bundles eligible for physical removal:
// (status='archived' AND archived_at < cutoff) OR (status='rejected' AND created_at < cutoff).
func (s *bundleStore) DeleteBefore(ctx context.Context, cutoff time.Time, limits ...int) (int64, error) {
	limit := 100
	if len(limits) > 0 && limits[0] > 0 && limits[0] < limit {
		limit = limits[0]
	}
	cutoffStr := cutoff.UTC().Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx, `DELETE FROM release_bundles WHERE rowid IN (SELECT rowid FROM release_bundles WHERE (status = 'archived' AND archived_at < ?) OR (status = 'rejected' AND created_at < ?) ORDER BY created_at, id LIMIT ?)`, cutoffStr, cutoffStr, limit)
	if err != nil {
		return 0, fmt.Errorf("delete bundles: %w", err)
	}
	return result.RowsAffected()
}

func (s *bundleStore) DeleteExpiredBefore(ctx context.Context, cutoff time.Time, limits ...int) (int64, error) {
	limit := 100
	if len(limits) > 0 && limits[0] > 0 && limits[0] < limit {
		limit = limits[0]
	}
	cutoffStr := cutoff.UTC().Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)
	var deleted int64
	err := retryBusy(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin delete expired bundles: %w", err)
		}
		defer tx.Rollback() //nolint:errcheck // Rollback is a no-op after successful Commit.

		// Clear definition references to the bundles being removed (AC-069-05).
		if _, err := tx.ExecContext(ctx, `
			UPDATE release_definitions SET current_bundle_id = NULL
			WHERE current_bundle_id IN (
				SELECT id FROM release_bundles
				WHERE status = 'archived' AND archived_at < ?
				ORDER BY archived_at, id LIMIT ?
			)`, cutoffStr, limit); err != nil {
			return fmt.Errorf("clear expired bundle references: %w", err)
		}
		// Mark candidates whose only link disappears as orphaned (AC-069-05).
		if _, err := tx.ExecContext(ctx, `
			UPDATE candidate_artifacts SET orphaned_at = COALESCE(orphaned_at, ?)
			WHERE bundle_id IN (
				SELECT id FROM release_bundles
				WHERE status = 'archived' AND archived_at < ?
				ORDER BY archived_at, id LIMIT ?
			)`, now, cutoffStr, limit); err != nil {
			return fmt.Errorf("mark expired candidate orphans: %w", err)
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM release_bundles WHERE rowid IN (SELECT rowid FROM release_bundles WHERE status = 'archived' AND archived_at < ? ORDER BY archived_at, id LIMIT ?)`, cutoffStr, limit)
		if err != nil {
			return fmt.Errorf("delete expired bundles: %w", err)
		}
		deleted, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("expired bundle rows affected: %w", err)
		}
		return tx.Commit()
	})
	return deleted, err
}

// Unarchive restores a bundle only when it was archived from validated.
// Already validated bundles are an idempotent success.
func (s *bundleStore) Unarchive(ctx context.Context, id string) (string, error) {
	var previousStatus string
	err := retryBusy(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin unarchive bundle: %w", err)
		}
		defer tx.Rollback() //nolint:errcheck // Rollback is a no-op after successful Commit.

		var status string
		var archivedFromStatus sql.NullString
		if err := tx.QueryRowContext(ctx, `
			SELECT status, archived_from_status
			FROM release_bundles
			WHERE id = ?
		`, id).Scan(&status, &archivedFromStatus); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("bundle %s: %w", id, store.ErrNotFound)
			}
			return fmt.Errorf("query bundle state: %w", err)
		}

		previousStatus = status
		switch store.BundleStatus(status) {
		case store.BundleValidated:
			return nil
		case store.BundleReceived:
			return store.ErrBundleNotReady
		case store.BundleRejected:
			return store.ErrBundleRejected
		case store.BundleArchived:
			previousStatus = archivedFromStatus.String
			if store.BundleStatus(previousStatus) != store.BundleValidated {
				if store.BundleStatus(previousStatus) == store.BundleRejected {
					return store.ErrBundleRejected
				}
				return store.ErrBundleNotReady
			}
		default:
			return store.ErrBundleNotReady
		}

		result, err := tx.ExecContext(ctx, `
			UPDATE release_bundles
			SET status = 'validated', archived_at = NULL, archived_from_status = ''
			WHERE id = ? AND status = 'archived' AND archived_from_status = 'validated'
		`, id)
		if err != nil {
			return fmt.Errorf("unarchive bundle %s: %w", id, err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("unarchive bundle rows affected: %w", err)
		}
		if rows != 1 {
			return store.ErrOptimisticLock
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit unarchive bundle: %w", err)
		}
		return nil
	})
	return previousStatus, err
}

func placeholders(n int) string {
	if n == 0 {
		return ""
	}
	switch n {
	case 1:
		return "?"
	default:
		b := make([]byte, 0, n*3-1)
		b = append(b, "?,"...)
		for i := 1; i < n-1; i++ {
			b = append(b, "?,"...)
		}
		b = append(b, '?')
		return string(b)
	}
}

func stringsToAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func (s *bundleStore) CreateTx(_ *gorm.DB, _ *store.ReleaseBundle) error {
	return errors.New("sqlite bundle transactions are unsupported")
}

func (s *bundleStore) GetByAlias(ctx context.Context, alias string) (*store.ReleaseBundle, error) {
	return s.Get(ctx, alias)
}

func (s *bundleStore) List(context.Context, store.BundleListFilter) (*store.BundlePage, error) {
	return nil, errors.New("sqlite bundle listing is unsupported")
}

func (s *bundleStore) UpdateStatusTx(_ *gorm.DB, _ string, _, _ store.BundleStatus, _ string) error {
	return errors.New("sqlite bundle status transactions are unsupported")
}

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
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
	var archivedFrom store.BundleStatus
	if archivedFromStatus != nil {
		archivedFrom = store.BundleStatus(*archivedFromStatus)
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
// Eligible: status IN ('received','validated'), created_at < now - retentionDays,
// NOT referenced by any active definition (via current_bundle_id),
// NOT referenced by any non-terminal operation.
func (s *bundleStore) ListForArchive(ctx context.Context, retentionDays int, terminalStates []store.OperationStatus) ([]string, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339)
	terminal := make([]string, len(terminalStates))
	for i, s := range terminalStates {
		terminal[i] = string(s)
	}

	//nolint:gosec // only generated placeholders are concatenated; terminal states remain bound parameters
	rows, err := s.db.QueryContext(ctx, `
		SELECT b.id FROM release_bundles b
		WHERE b.status IN ('received','validated')
		  AND b.created_at < ?
		  AND NOT EXISTS (
		    SELECT 1 FROM release_definitions d
		    WHERE d.current_bundle_id = b.id AND d.status = 'active'
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM operations o
		    WHERE o.bundle_id = b.id AND o.status NOT IN (`+placeholders(len(terminal))+`)
		  )
	`, append([]any{cutoff}, stringsToAny(terminal)...)...)
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
		WHERE id IN (`+placeholders(len(ids))+`) AND status IN ('received','validated')
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
func (s *bundleStore) DeleteBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	cutoffStr := cutoff.UTC().Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM release_bundles
		WHERE (status = 'archived' AND archived_at < ?)
		   OR (status = 'rejected' AND created_at < ?)
	`, cutoffStr, cutoffStr)
	if err != nil {
		return 0, fmt.Errorf("delete bundles: %w", err)
	}
	return result.RowsAffected()
}

// Unarchive transitions a bundle from archived back to validated.
func (s *bundleStore) Unarchive(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE release_bundles
		SET status = archived_from_status, archived_at = NULL, archived_from_status = ''
		WHERE id = ? AND status = 'archived' AND archived_from_status != ''
	`, id)
	if err != nil {
		return fmt.Errorf("unarchive bundle %s: %w", id, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("unarchive rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("bundle %s: %w", id, store.ErrNotFound)
	}
	return nil
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

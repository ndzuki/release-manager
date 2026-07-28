package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/store"
)

// Create inserts a candidate artifact, tolerating digest-type duplicates.
// When BundleID is set the artifact is also linked in the join table.
func (s *candidateArtifactStore) Create(ctx context.Context, ca *store.CandidateArtifact) error {
	if ca.ID == "" {
		ca.ID = uuid.New().String()
	}
	if ca.CreatedAt.IsZero() {
		ca.CreatedAt = time.Now().UTC()
	}

	var bundleID *string
	if ca.BundleID != nil {
		bundleID = ca.BundleID
	}

	createdAt := ca.CreatedAt.UTC().Format(time.RFC3339)
	var artifactID string
	err := s.gorm.QueryRowContext(ctx, `
		INSERT INTO candidate_artifacts (id, artifact_type, ref, digest, bundle_id, created_at, last_seen_at, validated_at, source_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(digest, artifact_type) DO UPDATE SET
			ref = EXCLUDED.ref,
			last_seen_at = EXCLUDED.last_seen_at,
			validated_at = COALESCE(EXCLUDED.validated_at, candidate_artifacts.validated_at),
			source_id = CASE WHEN EXCLUDED.source_id = '' THEN candidate_artifacts.source_id ELSE EXCLUDED.source_id END
		RETURNING id
	`,
		ca.ID, string(ca.ArtifactType), ca.Ref, ca.Digest,
		bundleID, createdAt, createdAt, ca.ValidatedAt, ca.SourceID,
	).Scan(&artifactID)
	if err != nil {
		return fmt.Errorf("insert candidate artifact: %w", err)
	}
	ca.ID = artifactID

	// Maintain the join table when BundleID is set at creation time.
	if bundleID != nil {
		if _, err := s.gorm.ExecContext(ctx, `
			INSERT INTO bundle_candidate_artifacts (bundle_id, candidate_artifact_id)
			VALUES (?, ?)
			ON CONFLICT DO NOTHING
		`, *bundleID, artifactID); err != nil {
			return fmt.Errorf("link candidate artifact to bundle at create: %w", err)
		}
	}

	return nil
}

func (s *candidateArtifactStore) Get(ctx context.Context, id string) (*store.CandidateArtifact, error) {
	return scanCandidateArtifact(s.gorm.QueryRowContext(ctx, candidateArtifactSelect+` WHERE id = ?`, id))
}

func (s *candidateArtifactStore) ListValidated(ctx context.Context) ([]*store.CandidateArtifact, error) {
	rows, err := s.gorm.QueryContext(ctx, candidateArtifactSelect+` WHERE validated_at IS NOT NULL ORDER BY validated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list validated candidate artifacts: %w", err)
	}
	defer rows.Close()
	artifacts := make([]*store.CandidateArtifact, 0)
	for rows.Next() {
		artifact, err := scanCandidateArtifact(rows)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate candidate artifacts: %w", err)
	}
	return artifacts, nil
}

// LinkToBundle associates a candidate artifact with a release bundle.
func (s *candidateArtifactStore) LinkToBundle(ctx context.Context, artifactID, bundleID string) error {
	result, err := s.gorm.ExecContext(ctx, `
		UPDATE candidate_artifacts SET bundle_id = ? WHERE id = ?
	`, bundleID, artifactID)
	if err != nil {
		return fmt.Errorf("link candidate artifact %s to bundle %s: %w", artifactID, bundleID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("link rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("candidate artifact %s: %w", artifactID, store.ErrNotFound)
	}

	// Maintain the join table.
	if _, err := s.gorm.ExecContext(ctx, `
		INSERT INTO bundle_candidate_artifacts (bundle_id, candidate_artifact_id)
		VALUES (?, ?)
		ON CONFLICT DO NOTHING
	`, bundleID, artifactID); err != nil {
		return fmt.Errorf("link candidate artifact to bundle join: %w", err)
	}

	return nil
}

// DeleteOrphanBefore removes unlinked candidate artifacts older than cutoff.
func (s *candidateArtifactStore) DeleteOrphanBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	result, err := s.gorm.ExecContext(ctx, `
		DELETE FROM candidate_artifacts
		WHERE bundle_id IS NULL AND created_at < ?
	`, cutoff.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("delete orphan candidate artifacts: %w", err)
	}
	return result.RowsAffected()
}

const candidateArtifactSelect = `
	SELECT id, artifact_type, ref, digest, bundle_id, created_at, validated_at, source_id
	FROM candidate_artifacts`

func scanCandidateArtifact(row interface{ Scan(...any) error }) (*store.CandidateArtifact, error) {
	var artifact store.CandidateArtifact
	var artifactType string
	var bundleID sql.NullString
	if err := row.Scan(&artifact.ID, &artifactType, &artifact.Ref, &artifact.Digest, &bundleID,
		&artifact.CreatedAt, &artifact.ValidatedAt, &artifact.SourceID); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan candidate artifact: %w", err)
	}
	artifact.ArtifactType = store.ArtifactType(artifactType)
	if bundleID.Valid {
		artifact.BundleID = &bundleID.String
	}
	artifact.CreatedAt = artifact.CreatedAt.UTC()
	if artifact.ValidatedAt != nil {
		value := artifact.ValidatedAt.UTC()
		artifact.ValidatedAt = &value
	}
	return &artifact, nil
}

package postgres

import (
	"context"
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
		INSERT INTO candidate_artifacts (id, artifact_type, ref, digest, bundle_id, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(digest, artifact_type) DO UPDATE SET
			ref = EXCLUDED.ref,
			last_seen_at = EXCLUDED.last_seen_at
		RETURNING id
	`,
		ca.ID, string(ca.ArtifactType), ca.Ref, ca.Digest,
		bundleID,
		createdAt,
		createdAt,
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

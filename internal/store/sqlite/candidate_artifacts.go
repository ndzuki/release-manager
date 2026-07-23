package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/store"
)

type candidateArtifactStore struct{ db *sql.DB }

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

	orphanedAt := ca.CreatedAt.UTC().Format(time.RFC3339)
	if bundleID != nil {
		orphanedAt = ""
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO candidate_artifacts (id, artifact_type, ref, digest, bundle_id, orphaned_at, created_at)
		VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?)
		ON CONFLICT(digest, artifact_type) DO NOTHING
	`,
		ca.ID, string(ca.ArtifactType), ca.Ref, ca.Digest,
		bundleID, orphanedAt,
		ca.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert candidate artifact: %w", err)
	}
	return nil
}

func (s *candidateArtifactStore) LinkToBundle(ctx context.Context, artifactID, bundleID string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE candidate_artifacts SET bundle_id = ?, orphaned_at = NULL WHERE id = ?
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
	return nil
}

func (s *candidateArtifactStore) LinkCandidateArtifacts(ctx context.Context, bundleID string, digests []string) (int64, error) {
	var linked int64
	err := retryBusy(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin link candidate artifacts: %w", err)
		}
		defer tx.Rollback() //nolint:errcheck // Rollback is a no-op after successful Commit.

		linked, err = linkCandidateArtifacts(ctx, tx, bundleID, digests)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit link candidate artifacts: %w", err)
		}
		return nil
	})
	return linked, err
}

func linkCandidateArtifacts(ctx context.Context, tx *sql.Tx, bundleID string, digests []string) (int64, error) {
	if len(digests) == 0 {
		return 0, nil
	}

	args := make([]any, 0, len(digests)+2)
	args = append(args, bundleID, time.Now().UTC().Format(time.RFC3339))
	for _, digest := range digests {
		args = append(args, digest)
	}
	//nolint:gosec // only generated ? placeholders are concatenated; digest values remain bound parameters
	query := `
		INSERT INTO bundle_candidate_artifacts (bundle_id, artifact_id, linked_at)
		SELECT ?, ca.id, ?
		FROM candidate_artifacts ca
		WHERE ca.digest IN (` + placeholders(len(digests)) + `)
		ON CONFLICT(bundle_id, artifact_id) DO NOTHING
	`
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("insert bundle candidate artifact links: %w", err)
	}
	linked, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("candidate artifact link rows affected: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE candidate_artifacts
		SET orphaned_at = NULL
		WHERE id IN (
			SELECT artifact_id FROM bundle_candidate_artifacts WHERE bundle_id = ?
		) AND orphaned_at IS NOT NULL
	`, bundleID); err != nil {
		return 0, fmt.Errorf("clear linked candidate artifact orphaned_at: %w", err)
	}
	return linked, nil
}

func (s *candidateArtifactStore) DeleteOrphanBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM candidate_artifacts
		WHERE orphaned_at IS NOT NULL AND orphaned_at < ?
	`, cutoff.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("delete orphan candidate artifacts: %w", err)
	}
	return result.RowsAffected()
}

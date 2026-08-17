package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/store"
	"gorm.io/gorm"
)

type candidateArtifactStore struct{ db *sql.DB }

func (s *candidateArtifactStore) Create(ctx context.Context, ca *store.CandidateArtifact) error {
	if ca.ID == "" {
		ca.ID = uuid.New().String()
	}
	if ca.CreatedAt.IsZero() {
		ca.CreatedAt = time.Now().UTC()
	}

	var validatedAt *string
	if ca.ValidatedAt != nil {
		value := ca.ValidatedAt.UTC().Format(time.RFC3339Nano)
		validatedAt = &value
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
		INSERT INTO candidate_artifacts (id, artifact_type, ref, digest, bundle_id, orphaned_at, created_at, validated_at, source_id)
		VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?)
		ON CONFLICT(digest, artifact_type) DO UPDATE SET
			ref = excluded.ref,
			orphaned_at = COALESCE(excluded.orphaned_at, candidate_artifacts.orphaned_at),
			validated_at = COALESCE(excluded.validated_at, candidate_artifacts.validated_at),
			source_id = CASE WHEN excluded.source_id = '' THEN candidate_artifacts.source_id ELSE excluded.source_id END
	`,
		ca.ID, string(ca.ArtifactType), ca.Ref, ca.Digest,
		bundleID, orphanedAt,
		ca.CreatedAt.UTC().Format(time.RFC3339Nano), validatedAt, ca.SourceID,
	)
	if err != nil {
		return fmt.Errorf("insert candidate artifact: %w", err)
	}
	return nil
}

func (s *candidateArtifactStore) Get(ctx context.Context, id string) (*store.CandidateArtifact, error) {
	return scanCandidateArtifact(s.db.QueryRowContext(ctx, candidateArtifactSelect+` WHERE id = ?`, id))
}

func (s *candidateArtifactStore) ListValidated(ctx context.Context) ([]*store.CandidateArtifact, error) {
	rows, err := s.db.QueryContext(ctx, candidateArtifactSelect+` WHERE validated_at IS NOT NULL ORDER BY validated_at DESC`)
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

func (s *candidateArtifactStore) DeleteOrphanBefore(ctx context.Context, cutoff time.Time, _ ...int) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM candidate_artifacts
		WHERE orphaned_at IS NOT NULL AND orphaned_at < ?
	`, cutoff.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("delete orphan candidate artifacts: %w", err)
	}
	return result.RowsAffected()
}

func (s *candidateArtifactStore) UpsertTx(_ *gorm.DB, _ *store.CandidateArtifact) error {
	return errors.New("sqlite candidate transactions are unsupported")
}

func (s *candidateArtifactStore) UpsertLocationTx(_ *gorm.DB, _, _, _ string, _ time.Time) error {
	return errors.New("sqlite candidate location transactions are unsupported")
}

func (s *candidateArtifactStore) LinkToBundleTx(_ *gorm.DB, _ string, _ []store.ArtifactDigest) error {
	return errors.New("sqlite candidate link transactions are unsupported")
}

const candidateArtifactSelect = `
	SELECT id, artifact_type, ref, digest, bundle_id, created_at, validated_at, source_id, orphaned_at
	FROM candidate_artifacts`

func scanCandidateArtifact(row interface{ Scan(...any) error }) (*store.CandidateArtifact, error) {
	var artifact store.CandidateArtifact
	var artifactType, createdAt string
	var bundleID, validatedAt, orphanedAt sql.NullString
	if err := row.Scan(&artifact.ID, &artifactType, &artifact.Ref, &artifact.Digest, &bundleID,
		&createdAt, &validatedAt, &artifact.SourceID, &orphanedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan candidate artifact: %w", err)
	}
	artifact.ArtifactType = store.ArtifactType(artifactType)
	if bundleID.Valid {
		artifact.BundleID = &bundleID.String
	}
	var err error
	artifact.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse candidate created_at: %w", err)
	}
	if validatedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, validatedAt.String)
		if parseErr != nil {
			return nil, fmt.Errorf("parse candidate validated_at: %w", parseErr)
		}
		artifact.ValidatedAt = &value
	}
	if orphanedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, orphanedAt.String)
		if parseErr != nil {
			return nil, fmt.Errorf("parse candidate orphaned_at: %w", parseErr)
		}
		artifact.OrphanedAt = &value
	}
	return &artifact, nil
}

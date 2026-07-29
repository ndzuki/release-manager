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

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO candidate_artifacts (id, artifact_type, ref, digest, bundle_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(digest, artifact_type) DO NOTHING
	`, ca.ID, string(ca.ArtifactType), ca.Ref, ca.Digest, ca.BundleID, ca.CreatedAt.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("insert candidate artifact: %w", err)
	}
	return nil
}

func (s *candidateArtifactStore) LinkToBundle(ctx context.Context, artifactID, bundleID string) error {
	result, err := s.db.ExecContext(ctx, `
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
	return nil
}

func (s *candidateArtifactStore) DeleteOrphanBefore(ctx context.Context, cutoff time.Time, _ ...int) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM candidate_artifacts
		WHERE bundle_id IS NULL AND created_at < ?
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

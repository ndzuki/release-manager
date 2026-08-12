package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Create preserves the legacy direct Store call while using the normalized schema.
func (s *candidateArtifactStore) Create(ctx context.Context, candidate *store.CandidateArtifact) error {
	return s.gorm.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.UpsertTx(tx, candidate); err != nil {
			return err
		}
		if candidate.Ref != "" {
			return s.UpsertLocationTx(tx, candidate.ID, candidate.Ref, "legacy", time.Now().UTC())
		}
		return nil
	})
}

// LinkToBundle preserves the legacy direct association call.
func (s *candidateArtifactStore) LinkToBundle(ctx context.Context, artifactID, bundleID string) error {
	result := s.gorm.gorm.WithContext(ctx).Exec(`
		INSERT INTO bundle_candidate_artifacts (bundle_id, artifact_id, linked_at)
		SELECT ?, id, ? FROM candidate_artifacts WHERE id = ?
		ON CONFLICT DO NOTHING
	`, bundleID, time.Now().UTC(), artifactID)
	if result.Error != nil {
		return fmt.Errorf("link candidate artifact to bundle: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := s.gorm.gorm.WithContext(ctx).Raw(`SELECT COUNT(*) FROM candidate_artifacts WHERE id = ?`, artifactID).Scan(&count).Error; err != nil {
			return fmt.Errorf("check candidate artifact: %w", err)
		}
		if count == 0 {
			return fmt.Errorf("candidate artifact %s: %w", artifactID, store.ErrNotFound)
		}
	}
	return nil
}

func (s *candidateArtifactStore) UpsertTx(tx *gorm.DB, candidate *store.CandidateArtifact) error {
	if tx == nil {
		return fmt.Errorf("upsert candidate artifact: nil transaction")
	}
	now := time.Now().UTC()
	if candidate.ID == "" {
		candidate.ID = uuid.NewString()
	}
	if candidate.CreatedAt.IsZero() {
		candidate.CreatedAt = now
	}
	if candidate.LastSeenAt.IsZero() {
		candidate.LastSeenAt = now
	}

	type candidateRow struct {
		ID           string     `gorm:"column:id"`
		ArtifactType string     `gorm:"column:artifact_type"`
		Digest       string     `gorm:"column:digest"`
		CreatedAt    time.Time  `gorm:"column:created_at"`
		LastSeenAt   time.Time  `gorm:"column:last_seen_at"`
		OrphanedAt   *time.Time `gorm:"column:orphaned_at"`
		ValidatedAt  *time.Time `gorm:"column:validated_at"`
		SourceID     string     `gorm:"column:source_id"`
	}
	row := candidateRow{
		ID: candidate.ID, ArtifactType: string(candidate.ArtifactType), Digest: candidate.Digest,
		CreatedAt: candidate.CreatedAt.UTC(), LastSeenAt: candidate.LastSeenAt.UTC(), OrphanedAt: candidate.OrphanedAt,
		ValidatedAt: candidate.ValidatedAt, SourceID: candidate.SourceID,
	}
	if err := tx.Clauses(
		clause.OnConflict{
			Columns:   []clause.Column{{Name: "digest"}, {Name: "artifact_type"}},
			DoUpdates: clause.Assignments(map[string]any{"last_seen_at": candidate.LastSeenAt.UTC(), "orphaned_at": nil}),
		},
		clause.Returning{},
	).Table("candidate_artifacts").Create(&row).Error; err != nil {
		return fmt.Errorf("upsert candidate artifact: %w", err)
	}
	candidate.ID = row.ID
	candidate.CreatedAt = row.CreatedAt
	candidate.LastSeenAt = row.LastSeenAt
	candidate.OrphanedAt = row.OrphanedAt
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

func (s *candidateArtifactStore) UpsertLocationTx(tx *gorm.DB, artifactID, ref, sourceID string, now time.Time) error {
	if tx == nil {
		return fmt.Errorf("upsert candidate artifact location: nil transaction")
	}
	if ref == "" {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result := tx.Exec(`
		INSERT INTO candidate_artifact_locations
			(artifact_id, ref, source_id, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (artifact_id, ref) DO UPDATE SET
			source_id = EXCLUDED.source_id,
			last_seen_at = EXCLUDED.last_seen_at
	`, artifactID, ref, sourceID, now.UTC(), now.UTC())
	if result.Error != nil {
		return fmt.Errorf("upsert candidate artifact location: %w", result.Error)
	}
	return nil
}

func (s *candidateArtifactStore) LinkToBundleTx(tx *gorm.DB, bundleID string, digests []store.ArtifactDigest) error {
	if tx == nil {
		return fmt.Errorf("link candidate artifacts: nil transaction")
	}
	for _, digest := range digests {
		result := tx.Exec(`
			INSERT INTO bundle_candidate_artifacts (bundle_id, artifact_id, linked_at)
			SELECT ?, id, ? FROM candidate_artifacts
			WHERE digest = ? AND artifact_type = ?
			ON CONFLICT DO NOTHING
		`, bundleID, time.Now().UTC(), digest.Digest, string(digest.ArtifactType))
		if result.Error != nil {
			return fmt.Errorf("link candidate artifact to bundle: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			var count int64
			if err := tx.Raw(`
				SELECT COUNT(*) FROM candidate_artifacts WHERE digest = ? AND artifact_type = ?
			`, digest.Digest, string(digest.ArtifactType)).Scan(&count).Error; err != nil {
				return fmt.Errorf("check candidate artifact identity: %w", err)
			}
			if count == 0 {
				return fmt.Errorf("candidate artifact %s/%s: %w", digest.ArtifactType, digest.Digest, store.ErrNotFound)
			}
		}
	}
	return nil
}

// DeleteOrphanBefore removes unlinked candidate artifacts older than cutoff in bounded batches.
func (s *candidateArtifactStore) DeleteOrphanBefore(ctx context.Context, cutoff time.Time, limits ...int) (int64, error) {
	limit := 500
	if len(limits) > 0 && limits[0] > 0 {
		limit = limits[0]
	}
	result := s.gorm.gorm.WithContext(ctx).Exec(`
		DELETE FROM candidate_artifacts
		WHERE id IN (
			SELECT ca.id
			FROM candidate_artifacts AS ca
			WHERE ca.last_seen_at < ?
			  AND NOT EXISTS (
				SELECT 1 FROM bundle_candidate_artifacts AS link WHERE link.artifact_id = ca.id
			  )
			ORDER BY ca.last_seen_at, ca.id
			LIMIT ?
		)
	`, cutoff.UTC(), limit)
	if result.Error != nil {
		return 0, fmt.Errorf("delete orphan candidate artifacts: %w", result.Error)
	}
	return result.RowsAffected, nil
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

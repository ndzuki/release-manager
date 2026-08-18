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
	return s.gorm.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(`
			INSERT INTO bundle_candidate_artifacts (bundle_id, artifact_id, linked_at)
			SELECT ?, id, ? FROM candidate_artifacts WHERE id = ?
			ON CONFLICT DO NOTHING
		`, bundleID, time.Now().UTC(), artifactID)
		if result.Error != nil {
			return fmt.Errorf("link candidate artifact to bundle: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			var count int64
			if err := tx.Raw(`SELECT COUNT(*) FROM candidate_artifacts WHERE id = ?`, artifactID).Scan(&count).Error; err != nil {
				return fmt.Errorf("check candidate artifact: %w", err)
			}
			if count == 0 {
				return fmt.Errorf("candidate artifact %s: %w", artifactID, store.ErrNotFound)
			}
		}
		if err := tx.Exec(`UPDATE candidate_artifacts SET orphaned_at = NULL WHERE id = ?`, artifactID).Error; err != nil {
			return fmt.Errorf("clear candidate artifact orphaned_at: %w", err)
		}
		return nil
	})
}

// LinkCandidateArtifacts batch-links candidate artifacts by digest to a bundle.
func (s *candidateArtifactStore) LinkCandidateArtifacts(ctx context.Context, bundleID string, digests []string) (int64, error) {
	if len(digests) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(digests)+1)
	args = append(args, bundleID)
	for _, digest := range digests {
		args = append(args, digest)
	}
	placeholders := ""
	for i := range digests {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
	}
	var linked int64
	err := s.gorm.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(`
			INSERT INTO bundle_candidate_artifacts (bundle_id, artifact_id, linked_at)
			SELECT ?, ca.id, NOW()
			FROM candidate_artifacts ca
			WHERE ca.digest IN (`+placeholders+`)
			ON CONFLICT (bundle_id, artifact_id) DO NOTHING
		`, args...)
		if result.Error != nil {
			return fmt.Errorf("insert bundle candidate artifact links: %w", result.Error)
		}
		linked = result.RowsAffected
		if err := tx.Exec(`
			UPDATE candidate_artifacts SET orphaned_at = NULL
			WHERE id IN (SELECT artifact_id FROM bundle_candidate_artifacts WHERE bundle_id = ?)
		`, bundleID).Error; err != nil {
			return fmt.Errorf("clear linked candidate artifact orphaned_at: %w", err)
		}
		return nil
	})
	return linked, err
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
	}
	row := candidateRow{ID: candidate.ID, ArtifactType: string(candidate.ArtifactType), Digest: candidate.Digest, CreatedAt: candidate.CreatedAt.UTC(), LastSeenAt: candidate.LastSeenAt.UTC(), OrphanedAt: candidate.OrphanedAt}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "digest"}, {Name: "artifact_type"}},
		DoUpdates: clause.Assignments(map[string]any{"last_seen_at": candidate.LastSeenAt.UTC()}),
	}, clause.Returning{}).Table("candidate_artifacts").Create(&row).Error; err != nil {
		return fmt.Errorf("upsert candidate artifact: %w", err)
	}
	candidate.ID, candidate.CreatedAt, candidate.LastSeenAt, candidate.OrphanedAt = row.ID, row.CreatedAt, row.LastSeenAt, row.OrphanedAt
	return nil
}

func (s *candidateArtifactStore) Get(ctx context.Context, id string) (*store.CandidateArtifact, error) {
	return scanCandidateArtifact(s.gorm.QueryRowContext(ctx, candidateArtifactSelect+` WHERE ca.id = ?`, id))
}

func (s *candidateArtifactStore) ListValidated(ctx context.Context) ([]*store.CandidateArtifact, error) {
	rows, err := s.gorm.QueryContext(ctx, candidateArtifactSelect+` WHERE ca.id IN (SELECT artifact_id FROM candidate_artifact_locations) ORDER BY ca.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list candidate artifacts: %w", err)
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
	if err := tx.Exec(`
		INSERT INTO candidate_artifact_locations (artifact_id, ref, source_id, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (artifact_id, ref) DO UPDATE SET source_id = EXCLUDED.source_id, last_seen_at = EXCLUDED.last_seen_at
	`, artifactID, ref, sourceID, now.UTC(), now.UTC()).Error; err != nil {
		return fmt.Errorf("upsert candidate artifact location: %w", err)
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
			SELECT ?, id, ? FROM candidate_artifacts WHERE digest = ? AND artifact_type = ?
			ON CONFLICT DO NOTHING
		`, bundleID, time.Now().UTC(), digest.Digest, string(digest.ArtifactType))
		if result.Error != nil {
			return fmt.Errorf("link candidate artifact to bundle: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			var count int64
			if err := tx.Raw(`SELECT COUNT(*) FROM candidate_artifacts WHERE digest = ? AND artifact_type = ?`, digest.Digest, string(digest.ArtifactType)).Scan(&count).Error; err != nil {
				return fmt.Errorf("check candidate artifact identity: %w", err)
			}
			if count == 0 {
				return fmt.Errorf("candidate artifact %s/%s: %w", digest.ArtifactType, digest.Digest, store.ErrNotFound)
			}
		}
	}
	if err := tx.Exec(`UPDATE candidate_artifacts SET orphaned_at = NULL WHERE id IN (SELECT artifact_id FROM bundle_candidate_artifacts WHERE bundle_id = ?)`, bundleID).Error; err != nil {
		return fmt.Errorf("clear linked candidate artifact orphaned_at: %w", err)
	}
	return nil
}

func (s *candidateArtifactStore) DeleteOrphanBefore(ctx context.Context, cutoff time.Time, limits ...int) (int64, error) {
	limit := 100
	if len(limits) > 0 && limits[0] > 0 && limits[0] < limit {
		limit = limits[0]
	}
	result := s.gorm.gorm.WithContext(ctx).Exec(`
		DELETE FROM candidate_artifacts
		WHERE id IN (
			SELECT ca.id FROM candidate_artifacts AS ca
			WHERE ca.orphaned_at IS NOT NULL AND ca.orphaned_at < ?
			  AND NOT EXISTS (SELECT 1 FROM bundle_candidate_artifacts AS link WHERE link.artifact_id = ca.id)
			ORDER BY ca.orphaned_at, ca.id LIMIT ?
		)
	`, cutoff.UTC(), limit)
	if result.Error != nil {
		return 0, fmt.Errorf("delete orphan candidate artifacts: %w", result.Error)
	}
	return result.RowsAffected, nil
}

const candidateArtifactSelect = `
	SELECT ca.id, ca.artifact_type, loc.ref, ca.digest, ca.created_at, ca.last_seen_at, ca.orphaned_at, loc.source_id
	FROM candidate_artifacts AS ca
	LEFT JOIN LATERAL (
		SELECT ref, source_id FROM candidate_artifact_locations
		WHERE artifact_id = ca.id ORDER BY last_seen_at DESC, ref LIMIT 1
	) AS loc ON TRUE`

func scanCandidateArtifact(row interface{ Scan(...any) error }) (*store.CandidateArtifact, error) {
	var artifact store.CandidateArtifact
	var artifactType string
	var ref, sourceID sql.NullString
	if err := row.Scan(&artifact.ID, &artifactType, &ref, &artifact.Digest, &artifact.CreatedAt, &artifact.LastSeenAt, &artifact.OrphanedAt, &sourceID); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan candidate artifact: %w", err)
	}
	artifact.ArtifactType = store.ArtifactType(artifactType)
	if ref.Valid {
		artifact.Ref = ref.String
	}
	if sourceID.Valid {
		artifact.SourceID = sourceID.String
	}
	artifact.CreatedAt = artifact.CreatedAt.UTC()
	artifact.LastSeenAt = artifact.LastSeenAt.UTC()
	if artifact.OrphanedAt != nil {
		value := artifact.OrphanedAt.UTC()
		artifact.OrphanedAt = &value
	}
	return &artifact, nil
}

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

	// TASK-163: PostgreSQL persists last_seen_at from the domain record, defaulting to
	// NOW (not to created_at) when the caller left it zero
	// (internal/store/postgres/candidate_artifacts.go: `if candidate.LastSeenAt.IsZero()
	// { candidate.LastSeenAt = now }`). SQLite dropped the column entirely; mirroring the
	// PostgreSQL rule — including which clock the fallback reads — is the point of the fix.
	lastSeenAt := ca.LastSeenAt
	if lastSeenAt.IsZero() {
		lastSeenAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO candidate_artifacts (id, artifact_type, ref, digest, bundle_id, orphaned_at, created_at, validated_at, source_id, last_seen_at)
		VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?)
		ON CONFLICT(digest, artifact_type) DO UPDATE SET
			ref = excluded.ref,
			orphaned_at = COALESCE(excluded.orphaned_at, candidate_artifacts.orphaned_at),
			validated_at = COALESCE(excluded.validated_at, candidate_artifacts.validated_at),
			source_id = CASE WHEN excluded.source_id = '' THEN candidate_artifacts.source_id ELSE excluded.source_id END,
			last_seen_at = excluded.last_seen_at
	`,
		ca.ID, string(ca.ArtifactType), ca.Ref, ca.Digest,
		bundleID, orphanedAt,
		ca.CreatedAt.UTC().Format(time.RFC3339Nano), validatedAt, ca.SourceID,
		lastSeenAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert candidate artifact: %w", err)
	}
	return nil
}

func (s *candidateArtifactStore) Get(ctx context.Context, id string) (*store.CandidateArtifact, error) {
	return scanCandidateArtifact(s.db.QueryRowContext(ctx, candidateArtifactSelect+` WHERE id = ?`, id))
}

// ListValidated returns the candidate artifacts that passed validation.
//
// G11 dual-engine contract (TASK-168 follow-up): "validated" means the artifact
// passed validation, NOT that it currently has a resolvable location. This
// engine has no location table, so validated_at IS NOT NULL is the definition
// the PostgreSQL engine must match
// (internal/store/postgres/candidate_artifacts.go). The ordering
// (validated_at DESC, id ASC) is also part of the contract: it is deterministic
// in both engines.
func (s *candidateArtifactStore) ListValidated(ctx context.Context) ([]*store.CandidateArtifact, error) {
	rows, err := s.db.QueryContext(ctx, candidateArtifactSelect+` WHERE validated_at IS NOT NULL ORDER BY validated_at DESC, id ASC`)
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

// LinkToBundle writes the same link table PostgreSQL writes. It used to set the legacy
// candidate_artifacts.bundle_id column instead, which nothing that reads the association ever
// consulted -- the operation-creation unit of work links through bundle_candidate_artifacts on
// both engines (TASK-229).
func (s *candidateArtifactStore) LinkToBundle(ctx context.Context, artifactID, bundleID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin link candidate artifact to bundle: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // rollback after a successful commit is a no-op

	result, err := tx.ExecContext(ctx, `
		INSERT INTO bundle_candidate_artifacts (bundle_id, artifact_id, linked_at)
		SELECT ?, id, ? FROM candidate_artifacts WHERE id = ?
		ON CONFLICT(bundle_id, artifact_id) DO NOTHING
	`, bundleID, time.Now().UTC().Format(time.RFC3339), artifactID)
	if err != nil {
		return fmt.Errorf("link candidate artifact %s to bundle %s: %w", artifactID, bundleID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("link rows affected: %w", err)
	}
	if n == 0 {
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM candidate_artifacts WHERE id = ?`, artifactID).Scan(&count); err != nil {
			return fmt.Errorf("check candidate artifact %s: %w", artifactID, err)
		}
		if count == 0 {
			return fmt.Errorf("candidate artifact %s: %w", artifactID, store.ErrNotFound)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_artifacts SET orphaned_at = NULL WHERE id = ?`, artifactID); err != nil {
		return fmt.Errorf("clear candidate artifact orphaned_at: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit link candidate artifact to bundle: %w", err)
	}
	return nil
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

// DeleteOrphanBefore honours the same guard as PostgreSQL: a candidate that still has a bundle
// link is never deleted, even if it was marked orphaned before it was linked again (TASK-229).
func (s *candidateArtifactStore) DeleteOrphanBefore(ctx context.Context, cutoff time.Time, limits ...int) (int64, error) {
	limit := 100
	if len(limits) > 0 && limits[0] > 0 && limits[0] < limit {
		limit = limits[0]
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM candidate_artifacts
		WHERE id IN (
			SELECT ca.id FROM candidate_artifacts ca
			WHERE ca.orphaned_at IS NOT NULL AND ca.orphaned_at < ?
			  AND NOT EXISTS (SELECT 1 FROM bundle_candidate_artifacts link WHERE link.artifact_id = ca.id)
			ORDER BY ca.orphaned_at, ca.id LIMIT ?
		)
	`, cutoff.UTC().Format(time.RFC3339), limit)
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

// MarkValidatedForBundleTx: bundle ingestion (and therefore validation) only exists on
// PostgreSQL in this engine, which is the same reason LinkToBundleTx is unsupported here.
// BundlesForArtifact reads the link table, exactly like PostgreSQL: candidate_artifacts.bundle_id
// is the legacy column that nothing writes any more. Reading it here made the lookup return
// nothing for production data, which silently disabled the emergency bundle trust fallback on
// SQLite -- the operation-creation unit of work had linked the artifact all along (TASK-229).
func (s *candidateArtifactStore) BundlesForArtifact(ctx context.Context, artifactID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT bundle_id FROM bundle_candidate_artifacts WHERE artifact_id = ? ORDER BY bundle_id
	`, artifactID)
	if err != nil {
		return nil, fmt.Errorf("list bundles for candidate artifact: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var bundleIDs []string
	for rows.Next() {
		var bundleID string
		if scanErr := rows.Scan(&bundleID); scanErr != nil {
			return nil, fmt.Errorf("scan bundle id for candidate artifact: %w", scanErr)
		}
		bundleIDs = append(bundleIDs, bundleID)
	}
	return bundleIDs, rows.Err()
}

func (s *candidateArtifactStore) MarkValidatedForBundleTx(_ *gorm.DB, _ string, _ time.Time) (int64, error) {
	return 0, errors.New("sqlite candidate validation marking is unsupported")
}

func (s *candidateArtifactStore) LinkToBundleTx(_ *gorm.DB, _ string, _ []store.ArtifactDigest) error {
	return errors.New("sqlite candidate link transactions are unsupported")
}

const candidateArtifactSelect = `
	SELECT id, artifact_type, ref, digest, bundle_id, created_at, validated_at, source_id, orphaned_at, last_seen_at
	FROM candidate_artifacts`

func scanCandidateArtifact(row interface{ Scan(...any) error }) (*store.CandidateArtifact, error) {
	var artifact store.CandidateArtifact
	var artifactType, createdAt string
	var bundleID, validatedAt, orphanedAt, lastSeenAt sql.NullString
	if err := row.Scan(&artifact.ID, &artifactType, &artifact.Ref, &artifact.Digest, &bundleID,
		&createdAt, &validatedAt, &artifact.SourceID, &orphanedAt, &lastSeenAt); err != nil {
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
	if lastSeenAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, lastSeenAt.String)
		if parseErr != nil {
			return nil, fmt.Errorf("parse candidate last_seen_at: %w", parseErr)
		}
		artifact.LastSeenAt = value
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

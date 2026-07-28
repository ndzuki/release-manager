package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type verificationStore struct {
	db *sql.DB
}

// Create inserts a new verification record.
func (s *verificationStore) Create(ctx context.Context, rec *store.VerificationRecord) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO verification_records (id, artifact_digest, policy_version, status, issuer, subject, summary, root_id, key_id, revocation_epoch, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.ArtifactDigest, rec.PolicyVersion, string(rec.Status),
		rec.Issuer, rec.Subject, rec.Summary, rec.RootID, rec.KeyID, rec.RevocationEpoch,
		rec.CreatedAt.Format(time.RFC3339),
	)
	return err
}

// GetByDigestAndPolicy retrieves the latest verification record for a given digest and policy version.
func (s *verificationStore) GetByDigestAndPolicy(ctx context.Context, artifactDigest, policyVersion string) (*store.VerificationRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, artifact_digest, policy_version, status, issuer, subject, summary, root_id, key_id, revocation_epoch, created_at
		 FROM verification_records
		 WHERE artifact_digest = ? AND policy_version = ?
		 ORDER BY created_at DESC
		 LIMIT 1`,
		artifactDigest, policyVersion,
	)
	rec := &store.VerificationRecord{}
	var createdAt string
	err := row.Scan(&rec.ID, &rec.ArtifactDigest, &rec.PolicyVersion, &rec.Status, &rec.Issuer, &rec.Subject, &rec.Summary, &rec.RootID, &rec.KeyID, &rec.RevocationEpoch, &createdAt)
	if err == sql.ErrNoRows {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rec.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, err
	}
	return rec, nil
}

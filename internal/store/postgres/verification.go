package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type verificationStore struct {
	gorm *DB
}

// Create inserts a new verification record.
func (s *verificationStore) Create(ctx context.Context, rec *store.VerificationRecord) error {
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	_, err := s.gorm.ExecContext(ctx,
		`INSERT INTO verification_records (id, artifact_digest, policy_version, signature_identity, status, issuer, subject, summary, root_id, key_id, revocation_epoch, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.ArtifactDigest, rec.PolicyVersion, rec.SignatureIdentity, string(rec.Status),
		rec.Issuer, rec.Subject, rec.Summary, rec.RootID, rec.KeyID, rec.RevocationEpoch,
		rec.CreatedAt.UTC(),
	)
	return err
}

// GetByDigestPolicyAndSignature retrieves the latest verification record for one signature identity.
func (s *verificationStore) GetByDigestPolicyAndSignature(
	ctx context.Context,
	artifactDigest string,
	policyVersion string,
	signatureIdentity string,
) (*store.VerificationRecord, error) {
	row := s.gorm.QueryRowContext(ctx,
		`SELECT id, artifact_digest, policy_version, signature_identity, status, issuer, subject, summary, root_id, key_id, revocation_epoch, created_at
		 FROM verification_records
		 WHERE artifact_digest = ? AND policy_version = ? AND signature_identity = ?
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`,
		artifactDigest, policyVersion, signatureIdentity,
	)
	return scanVerificationRecord(row)
}

// GetByDigestAndPolicy retrieves the latest verification record for a digest and policy version.
func (s *verificationStore) GetByDigestAndPolicy(
	ctx context.Context,
	artifactDigest string,
	policyVersion string,
) (*store.VerificationRecord, error) {
	row := s.gorm.QueryRowContext(ctx,
		`SELECT id, artifact_digest, policy_version, signature_identity, status, issuer, subject, summary, root_id, key_id, revocation_epoch, created_at
		 FROM verification_records
		 WHERE artifact_digest = ? AND policy_version = ?
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`,
		artifactDigest, policyVersion,
	)
	return scanVerificationRecord(row)
}

func scanVerificationRecord(row interface{ Scan(...any) error }) (*store.VerificationRecord, error) {
	rec := &store.VerificationRecord{}
	err := row.Scan(&rec.ID, &rec.ArtifactDigest, &rec.PolicyVersion, &rec.SignatureIdentity, &rec.Status, &rec.Issuer, &rec.Subject, &rec.Summary, &rec.RootID, &rec.KeyID, &rec.RevocationEpoch, &rec.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// Package sqlite provides a SQLite-backed implementation of the store interfaces.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type trustRootStore struct{ db *sql.DB }

// Create inserts a new trust root.
func (s *trustRootStore) Create(ctx context.Context, r *store.TrustRoot) error {
	if r.ID == "" || r.Environment == "" {
		return fmt.Errorf("trust_root: id and environment are required")
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = r.CreatedAt
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO trust_roots (id, environment, key_id, public_key_pem, issuer, subject_pattern,
    state, valid_from, grace_until, created_at, updated_at, revoked_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		r.ID, r.Environment, r.KeyID, r.PublicKeyPEM, r.Issuer, r.SubjectPattern,
		string(r.State), r.ValidFrom.UTC().Format(time.RFC3339),
		formatTimePtr(r.GraceUntil), r.CreatedAt.UTC().Format(time.RFC3339),
		r.UpdatedAt.UTC().Format(time.RFC3339), formatTimePtr(r.RevokedAt),
	)
	if err != nil {
		return fmt.Errorf("insert trust_root: %w", err)
	}
	return nil
}

// Get retrieves a trust root by ID.
func (s *trustRootStore) Get(ctx context.Context, id string) (*store.TrustRoot, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, environment, key_id, public_key_pem, issuer, subject_pattern,
    state, valid_from, grace_until, created_at, updated_at, revoked_at
FROM trust_roots WHERE id = ?
`, id)
	return scanTrustRoot(row)
}

// ListByEnvironment returns all trust roots for an environment.
func (s *trustRootStore) ListByEnvironment(ctx context.Context, env string) ([]*store.TrustRoot, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, environment, key_id, public_key_pem, issuer, subject_pattern,
    state, valid_from, grace_until, created_at, updated_at, revoked_at
FROM trust_roots WHERE environment = ? ORDER BY created_at ASC
`, env)
	if err != nil {
		return nil, fmt.Errorf("list trust_roots: %w", err)
	}
	defer rows.Close()
	return scanTrustRoots(rows)
}

// GetActiveByEnvironment returns roots in active or grace state that accept signatures at the given time.
func (s *trustRootStore) GetActiveByEnvironment(ctx context.Context, env string, at time.Time) ([]*store.TrustRoot, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, environment, key_id, public_key_pem, issuer, subject_pattern,
    state, valid_from, grace_until, created_at, updated_at, revoked_at
FROM trust_roots
WHERE environment = ?
  AND state IN ('active', 'grace')
  AND valid_from <= ?
  AND (state = 'active' OR (grace_until IS NOT NULL AND grace_until > ?))
ORDER BY created_at ASC
`, env, at.UTC().Format(time.RFC3339), at.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("list active trust_roots: %w", err)
	}
	defer rows.Close()
	return scanTrustRoots(rows)
}

// Update modifies an existing trust root.
func (s *trustRootStore) Update(ctx context.Context, r *store.TrustRoot) error {
	r.UpdatedAt = time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
UPDATE trust_roots
SET environment = ?, key_id = ?, public_key_pem = ?, issuer = ?, subject_pattern = ?,
    state = ?, valid_from = ?, grace_until = ?, updated_at = ?, revoked_at = ?
WHERE id = ?
`,
		r.Environment, r.KeyID, r.PublicKeyPEM, r.Issuer, r.SubjectPattern,
		string(r.State), r.ValidFrom.UTC().Format(time.RFC3339),
		formatTimePtr(r.GraceUntil), r.UpdatedAt.UTC().Format(time.RFC3339),
		formatTimePtr(r.RevokedAt), r.ID,
	)
	if err != nil {
		return fmt.Errorf("update trust_root: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update trust_root rows_affected: %w", err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// GetPolicy retrieves the trust policy metadata for an environment.
func (s *trustRootStore) GetPolicy(ctx context.Context, env string) (*store.TrustPolicyMeta, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT environment, version, revocation_epoch
FROM trust_policies WHERE environment = ?
`, env)
	var meta store.TrustPolicyMeta
	err := row.Scan(&meta.Environment, &meta.Version, &meta.RevocationEpoch)
	if err == sql.ErrNoRows {
		return &store.TrustPolicyMeta{Environment: env, Version: 1, RevocationEpoch: 0}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get trust_policy: %w", err)
	}
	return &meta, nil
}

// BumpPolicy atomically increments the policy version for an environment.
// Returns the new (version, epoch) pair.
func (s *trustRootStore) BumpPolicy(ctx context.Context, env string) (version, epoch int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback on committed tx is a no-op.

	// Ensure the policy row exists (lazy bootstrap).
	_, err = tx.ExecContext(ctx, `
INSERT OR IGNORE INTO trust_policies (environment, version, revocation_epoch, updated_at)
VALUES (?, 0, 0, ?)
`, env, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, 0, fmt.Errorf("ensure trust_policy: %w", err)
	}

	// Increment version.
	result, err := tx.ExecContext(ctx, `
UPDATE trust_policies
SET version = version + 1, updated_at = ?
WHERE environment = ?
`, time.Now().UTC().Format(time.RFC3339), env)
	if err != nil {
		return 0, 0, fmt.Errorf("bump policy version: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("bump policy rows_affected: %w", err)
	}
	if n == 0 {
		return 0, 0, fmt.Errorf("trust_policy not found for env %q", env)
	}

	// Read back the new values.
	row := tx.QueryRowContext(ctx, `
SELECT version, revocation_epoch FROM trust_policies WHERE environment = ?
`, env)
	if err = row.Scan(&version, &epoch); err != nil {
		return 0, 0, fmt.Errorf("read bumped policy: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit bump policy: %w", err)
	}
	return version, epoch, nil
}

// BumpRevocationEpoch atomically increments the revocation epoch.
func (s *trustRootStore) BumpRevocationEpoch(ctx context.Context, env string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback on committed tx is a no-op.

	// Ensure the policy row exists.
	_, err = tx.ExecContext(ctx, `
INSERT OR IGNORE INTO trust_policies (environment, version, revocation_epoch, updated_at)
VALUES (?, 0, 0, ?)
`, env, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("ensure trust_policy: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
UPDATE trust_policies
SET revocation_epoch = revocation_epoch + 1, updated_at = ?
WHERE environment = ?
`, time.Now().UTC().Format(time.RFC3339), env)
	if err != nil {
		return 0, fmt.Errorf("bump revocation epoch: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("bump epoch rows_affected: %w", err)
	}
	if n == 0 {
		return 0, fmt.Errorf("trust_policy not found for env %q", env)
	}

	row := tx.QueryRowContext(ctx, `
SELECT revocation_epoch FROM trust_policies WHERE environment = ?
`, env)
	var newEpoch int64
	if err := row.Scan(&newEpoch); err != nil {
		return 0, fmt.Errorf("read bumped epoch: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit bump epoch: %w", err)
	}
	return newEpoch, nil
}

// scanTrustRoot scans a single trust root row.
func scanTrustRoot(row interface{ Scan(...interface{}) error }) (*store.TrustRoot, error) {
	var (
		id, env, keyID, publicKeyPEM, issuer, subjectPattern, stateStr string
		validFrom, graceUntil, createdAt, updatedAt, revokedAt         *string
	)
	err := row.Scan(&id, &env, &keyID, &publicKeyPEM, &issuer, &subjectPattern,
		&stateStr, &validFrom, &graceUntil, &createdAt, &updatedAt, &revokedAt)
	if err == sql.ErrNoRows {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan trust_root: %w", err)
	}

	r := &store.TrustRoot{
		ID:             id,
		Environment:    env,
		KeyID:          keyID,
		PublicKeyPEM:   publicKeyPEM,
		Issuer:         issuer,
		SubjectPattern: subjectPattern,
		State:          store.TrustRootState(stateStr),
	}

	r.ValidFrom, err = time.Parse(time.RFC3339, *validFrom)
	if err != nil {
		return nil, fmt.Errorf("parse valid_from: %w", err)
	}
	r.CreatedAt, err = time.Parse(time.RFC3339, *createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	r.UpdatedAt, err = time.Parse(time.RFC3339, *updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}

	if graceUntil != nil {
		t, err := time.Parse(time.RFC3339, *graceUntil)
		if err != nil {
			return nil, fmt.Errorf("parse grace_until: %w", err)
		}
		r.GraceUntil = &t
	}
	if revokedAt != nil {
		t, err := time.Parse(time.RFC3339, *revokedAt)
		if err != nil {
			return nil, fmt.Errorf("parse revoked_at: %w", err)
		}
		r.RevokedAt = &t
	}

	return r, nil
}

// scanTrustRoots scans multiple trust root rows.
func scanTrustRoots(rows *sql.Rows) ([]*store.TrustRoot, error) {
	var roots []*store.TrustRoot
	for rows.Next() {
		var (
			id, env, keyID, publicKeyPEM, issuer, subjectPattern, stateStr string
			validFrom, graceUntil, createdAt, updatedAt, revokedAt         *string
		)
		var err error
		if err = rows.Scan(&id, &env, &keyID, &publicKeyPEM, &issuer, &subjectPattern,
			&stateStr, &validFrom, &graceUntil, &createdAt, &updatedAt, &revokedAt); err != nil {
			return nil, fmt.Errorf("scan trust_roots row: %w", err)
		}
		r := &store.TrustRoot{
			ID:             id,
			Environment:    env,
			KeyID:          keyID,
			PublicKeyPEM:   publicKeyPEM,
			Issuer:         issuer,
			SubjectPattern: subjectPattern,
			State:          store.TrustRootState(stateStr),
		}
		r.ValidFrom, err = time.Parse(time.RFC3339, *validFrom)
		if err != nil {
			return nil, fmt.Errorf("parse valid_from: %w", err)
		}
		r.CreatedAt, err = time.Parse(time.RFC3339, *createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		r.UpdatedAt, err = time.Parse(time.RFC3339, *updatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse updated_at: %w", err)
		}
		if graceUntil != nil {
			t, err := time.Parse(time.RFC3339, *graceUntil)
			if err != nil {
				return nil, fmt.Errorf("parse grace_until: %w", err)
			}
			r.GraceUntil = &t
		}
		if revokedAt != nil {
			t, err := time.Parse(time.RFC3339, *revokedAt)
			if err != nil {
				return nil, fmt.Errorf("parse revoked_at: %w", err)
			}
			r.RevokedAt = &t
		}
		roots = append(roots, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate trust_roots: %w", err)
	}
	return roots, nil
}

// formatTimePtr formats a *time.Time as RFC3339 string, or nil.
func formatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

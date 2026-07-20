package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ndzuki/release-manager/internal/store"
)

type preflightStore struct{ db *sql.DB }

func (s *preflightStore) Create(ctx context.Context, rec *store.PreflightRecord) error {
	if rec.ID == "" {
		rec.ID = uuid.New().String()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO preflight_results (
	id, operation_id, routing_version, bundle_digest,
	trust_policy_version, sbom_policy_version, result_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(operation_id, routing_version, bundle_digest, trust_policy_version, sbom_policy_version)
DO NOTHING
`,
		rec.ID,
		rec.Key.OperationID,
		rec.Key.RoutingVersion,
		rec.Key.BundleDigest,
		rec.Key.TrustPolicyVersion,
		rec.Key.SBOMPolicyVersion,
		rec.ResultJSON,
		rec.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert preflight result: %w", err)
	}
	return nil
}

func (s *preflightStore) GetByKey(ctx context.Context, key store.PreflightCacheKey) (*store.PreflightRecord, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, operation_id, routing_version, bundle_digest,
	trust_policy_version, sbom_policy_version, result_json, created_at
FROM preflight_results
WHERE operation_id = ? AND routing_version = ? AND bundle_digest = ?
	AND trust_policy_version = ? AND sbom_policy_version = ?
`,
		key.OperationID,
		key.RoutingVersion,
		key.BundleDigest,
		key.TrustPolicyVersion,
		key.SBOMPolicyVersion,
	)

	rec := &store.PreflightRecord{}
	var createdAt string
	if err := row.Scan(
		&rec.ID,
		&rec.Key.OperationID,
		&rec.Key.RoutingVersion,
		&rec.Key.BundleDigest,
		&rec.Key.TrustPolicyVersion,
		&rec.Key.SBOMPolicyVersion,
		&rec.ResultJSON,
		&createdAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan preflight result: %w", err)
	}

	created, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse preflight result created_at: %w", err)
	}
	rec.CreatedAt = created
	return rec, nil
}

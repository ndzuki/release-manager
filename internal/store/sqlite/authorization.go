package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type authorizationStore struct{ db *sql.DB }

func (s *authorizationStore) Load(ctx context.Context) (*store.AuthorizationSnapshot, error) {
	return loadAuthorizationSnapshot(ctx, s.db)
}

func (s *authorizationStore) Apply(ctx context.Context, command store.AuthorizationApplyCommand) (*store.AuthorizationSnapshot, error) {
	var snapshot *store.AuthorizationSnapshot
	err := retryBusy(ctx, func() error {
		var err error
		snapshot, err = s.apply(ctx, command)
		return err
	})
	return snapshot, err
}

//nolint:gocyclo // Version, grant, rule, and optimistic-lock updates form one atomic transaction.
func (s *authorizationStore) apply(ctx context.Context, command store.AuthorizationApplyCommand) (*store.AuthorizationSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin authorization apply: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback after Commit is a no-op.

	current, err := loadAuthorizationVersions(ctx, tx)
	if err != nil {
		return nil, err
	}
	if current.SourceVersion != command.ExpectedSourceVersion || current.PolicyVersion != command.ExpectedPolicyVersion {
		return nil, store.ErrOptimisticLock
	}
	if command.Grants != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM capability_grants`); err != nil {
			return nil, fmt.Errorf("replace capability grants: %w", err)
		}
		for _, grant := range command.Grants {
			if err := insertCapabilityGrant(ctx, tx, grant); err != nil {
				return nil, err
			}
		}
	}
	if command.Rules != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM casbin_rule`); err != nil {
			return nil, fmt.Errorf("replace casbin rules: %w", err)
		}
		for _, rule := range command.Rules {
			if err := insertCasbinRule(ctx, tx, rule); err != nil {
				return nil, err
			}
		}
	}

	nextSource := current.SourceVersion
	if command.Mutation != store.AuthorizationPolicyChanged {
		nextSource++
	}
	nextPolicy := current.PolicyVersion
	if command.Rules != nil || command.Mutation == store.AuthorizationPolicyChanged || command.Mutation == store.AuthorizationGrantChanged {
		nextPolicy++
	}
	result, err := tx.ExecContext(ctx, `
UPDATE authorization_source_version SET version = ? WHERE id = 1 AND version = ?`, nextSource, current.SourceVersion)
	if err != nil {
		return nil, fmt.Errorf("update authorization source version: %w", err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil {
		return nil, fmt.Errorf("authorization source version rows affected: %w", rowsErr)
	} else if rows != 1 {
		return nil, store.ErrOptimisticLock
	}
	result, err = tx.ExecContext(ctx, `UPDATE policy_version SET version = ? WHERE id = 1 AND version = ?`, nextPolicy, current.PolicyVersion)
	if err != nil {
		return nil, fmt.Errorf("update policy version: %w", err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil {
		return nil, fmt.Errorf("policy version rows affected: %w", rowsErr)
	} else if rows != 1 {
		return nil, store.ErrOptimisticLock
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit authorization apply: %w", err)
	}
	return s.Load(ctx)
}

func (s *authorizationStore) GetCheckpoint(ctx context.Context, organizationID, customerID string) (*store.AuthorizationCheckpoint, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT organization_id, customer_id, source_version, policy_version, fresh, updated_at
FROM authorization_checkpoints WHERE organization_id = ? AND customer_id = ?`, organizationID, customerID)
	return scanAuthorizationCheckpoint(row)
}

func (s *authorizationStore) SaveCheckpoint(ctx context.Context, checkpoint store.AuthorizationCheckpoint) error {
	return retryBusy(ctx, func() error {
		if checkpoint.UpdatedAt.IsZero() {
			checkpoint.UpdatedAt = time.Now().UTC()
		}
		// The guarded UPSERT is monotonic and idempotent: an equal or lower version
		// is deliberately not persisted, and the consumer relies on the module-level
		// regression check (previous > remote) to fail closed. No read-back is
		// performed: a read-back on a separate pooled connection can spuriously
		// observe the pre-write row under concurrency and must not fail the call.
		_, err := s.db.ExecContext(ctx, `
INSERT INTO authorization_checkpoints (
    organization_id, customer_id, source_version, policy_version, fresh, updated_at
) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(organization_id, customer_id) DO UPDATE SET
    source_version = excluded.source_version,
    policy_version = excluded.policy_version,
    fresh = excluded.fresh,
    updated_at = excluded.updated_at
WHERE excluded.source_version > authorization_checkpoints.source_version
   OR (excluded.source_version = authorization_checkpoints.source_version
       AND excluded.policy_version >= authorization_checkpoints.policy_version)`,
			checkpoint.OrganizationID, checkpoint.CustomerID, checkpoint.SourceVersion,
			checkpoint.PolicyVersion, checkpoint.Fresh, checkpoint.UpdatedAt.UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("save authorization checkpoint: %w", err)
		}
		return nil
	})
}

type authorizationQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

//nolint:gocyclo // The durable snapshot loads versions, grants, rules, and checkpoints in one read model.
func loadAuthorizationSnapshot(ctx context.Context, queryer authorizationQueryer) (*store.AuthorizationSnapshot, error) {
	snapshot, err := loadAuthorizationVersions(ctx, queryer)
	if err != nil {
		return nil, err
	}
	grantRows, err := queryer.QueryContext(ctx, `
SELECT organization_id, subject, action, granted_by, revoked, created_at, updated_at
FROM capability_grants ORDER BY organization_id, subject, action`)
	if err != nil {
		return nil, fmt.Errorf("list capability grants: %w", err)
	}
	defer grantRows.Close()
	for grantRows.Next() {
		var grant store.CapabilityGrant
		var createdAt, updatedAt string
		if err := grantRows.Scan(&grant.OrganizationID, &grant.Subject, &grant.Action, &grant.GrantedBy, &grant.Revoked, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan capability grant: %w", err)
		}
		grant.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			grant.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		}
		if err != nil {
			return nil, fmt.Errorf("parse capability grant created_at: %w", err)
		}
		grant.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			grant.UpdatedAt, err = time.Parse(time.RFC3339, updatedAt)
		}
		if err != nil {
			return nil, fmt.Errorf("parse capability grant updated_at: %w", err)
		}
		snapshot.Grants = append(snapshot.Grants, grant)
	}
	if err := grantRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate capability grants: %w", err)
	}

	ruleRows, err := queryer.QueryContext(ctx, `SELECT ptype, v0, v1, v2, v3, v4, v5 FROM casbin_rule ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list casbin rules: %w", err)
	}
	defer ruleRows.Close()
	for ruleRows.Next() {
		var rule store.CasbinRule
		if err := ruleRows.Scan(&rule.PType, &rule.V0, &rule.V1, &rule.V2, &rule.V3, &rule.V4, &rule.V5); err != nil {
			return nil, fmt.Errorf("scan casbin rule: %w", err)
		}
		snapshot.Rules = append(snapshot.Rules, rule)
	}
	if err := ruleRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate casbin rules: %w", err)
	}

	checkpointRows, err := queryer.QueryContext(ctx, `
SELECT organization_id, customer_id, source_version, policy_version, fresh, updated_at
FROM authorization_checkpoints ORDER BY organization_id, customer_id`)
	if err != nil {
		return nil, fmt.Errorf("list authorization checkpoints: %w", err)
	}
	defer checkpointRows.Close()
	for checkpointRows.Next() {
		checkpoint, scanErr := scanAuthorizationCheckpoint(checkpointRows)
		if scanErr != nil {
			return nil, scanErr
		}
		snapshot.Checkpoints = append(snapshot.Checkpoints, *checkpoint)
	}
	if err := checkpointRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate authorization checkpoints: %w", err)
	}
	return snapshot, nil
}

func loadAuthorizationVersions(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (*store.AuthorizationSnapshot, error) {
	var snapshot store.AuthorizationSnapshot
	if err := queryer.QueryRowContext(ctx, `SELECT version FROM authorization_source_version WHERE id = 1`).Scan(&snapshot.SourceVersion); err != nil {
		return nil, fmt.Errorf("load authorization source version: %w", err)
	}
	if err := queryer.QueryRowContext(ctx, `SELECT version FROM policy_version WHERE id = 1`).Scan(&snapshot.PolicyVersion); err != nil {
		return nil, fmt.Errorf("load policy version: %w", err)
	}
	return &snapshot, nil
}

func insertCapabilityGrant(ctx context.Context, execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, grant store.CapabilityGrant) error {
	if grant.CreatedAt.IsZero() {
		grant.CreatedAt = time.Now().UTC()
	}
	if grant.UpdatedAt.IsZero() {
		grant.UpdatedAt = grant.CreatedAt
	}
	_, err := execer.ExecContext(ctx, `
INSERT INTO capability_grants (organization_id, subject, action, granted_by, revoked, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, grant.OrganizationID, grant.Subject, grant.Action, grant.GrantedBy,
		grant.Revoked, grant.CreatedAt.UTC().Format(time.RFC3339Nano), grant.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert capability grant: %w", err)
	}
	return nil
}

func insertCasbinRule(ctx context.Context, execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, rule store.CasbinRule) error {
	_, err := execer.ExecContext(ctx, `
INSERT INTO casbin_rule (ptype, v0, v1, v2, v3, v4, v5) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rule.PType, rule.V0, rule.V1, rule.V2, rule.V3, rule.V4, rule.V5)
	if err != nil {
		return fmt.Errorf("insert casbin rule: %w", err)
	}
	return nil
}

func scanAuthorizationCheckpoint(row interface{ Scan(...any) error }) (*store.AuthorizationCheckpoint, error) {
	var checkpoint store.AuthorizationCheckpoint
	var updatedAt string
	if err := row.Scan(&checkpoint.OrganizationID, &checkpoint.CustomerID, &checkpoint.SourceVersion,
		&checkpoint.PolicyVersion, &checkpoint.Fresh, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan authorization checkpoint: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, updatedAt)
	}
	if err != nil {
		return nil, fmt.Errorf("parse authorization checkpoint updated_at: %w", err)
	}
	checkpoint.UpdatedAt = parsed
	return &checkpoint, nil
}

var _ store.AuthorizationStore = (*authorizationStore)(nil)

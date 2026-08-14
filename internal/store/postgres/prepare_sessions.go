package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type prepareSessionStore struct{ gorm *DB }

func (s *prepareSessionStore) Create(ctx context.Context, session *store.PrepareSession, expectedAuthorizationVersion uint64) error {
	if session == nil {
		return fmt.Errorf("create prepare session: nil session")
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now().UTC()
	}
	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create prepare session: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback after Commit is a no-op.
	if err := checkAuthorizationFence(ctx, tx, expectedAuthorizationVersion); err != nil {
		return err
	}
	taskIDs, err := json.Marshal(session.TaskIDs)
	if err != nil {
		return fmt.Errorf("encode prepare task ids: %w", err)
	}
	lockedPaths, err := json.Marshal(session.LockedPaths)
	if err != nil {
		return fmt.Errorf("encode prepare locked paths: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO convergence_prepare_sessions (
			token_hash, actor_user_id, organization_id, release_definition_id,
			parent_revision_id, parent_version, task_ids, locked_paths,
			locked_path_hash, expires_at, consumed_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?::jsonb, ?::jsonb, ?, ?, ?, ?)
	`, session.TokenHash, session.ActorUserID, session.OrganizationID, session.ReleaseDefinitionID,
		valuesOptionalString(session.ParentRevisionID), session.ParentVersion, taskIDs, lockedPaths,
		session.LockedPathHash, session.ExpiresAt.UTC(), valuesOptionalTime(session.ConsumedAt), session.CreatedAt.UTC())
	if err != nil {
		if isUniqueConstraint(err) {
			return store.ErrDuplicateKey
		}
		return fmt.Errorf("insert prepare session: %w", err)
	}
	if err := checkAuthorizationFence(ctx, tx, expectedAuthorizationVersion); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create prepare session: %w", err)
	}
	return nil
}

func (s *prepareSessionStore) Get(ctx context.Context, tokenHash string) (*store.PrepareSession, error) {
	return getPrepareSession(ctx, s.gorm, tokenHash)
}

func (s *prepareSessionStore) DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	result, err := s.gorm.ExecContext(ctx, `
		DELETE FROM convergence_prepare_sessions
		WHERE (expires_at <= ? AND expires_at < ?)
			OR (consumed_at IS NOT NULL AND consumed_at < ?)
	`, time.Now().UTC(), cutoff.UTC(), cutoff.UTC())
	if err != nil {
		return 0, fmt.Errorf("delete expired prepare sessions: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prepare session delete rows: %w", err)
	}
	return count, nil
}

func getPrepareSession(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, tokenHash string) (*store.PrepareSession, error) {
	var session store.PrepareSession
	var taskIDs, lockedPaths []byte
	var parentRevisionID sql.NullString
	var consumedAt sql.NullTime
	if err := queryer.QueryRowContext(ctx, `
		SELECT token_hash, actor_user_id, organization_id, release_definition_id,
			parent_revision_id, parent_version, task_ids, locked_paths,
			locked_path_hash, expires_at, consumed_at, created_at
		FROM convergence_prepare_sessions WHERE token_hash = ?
	`, tokenHash).Scan(
		&session.TokenHash, &session.ActorUserID, &session.OrganizationID, &session.ReleaseDefinitionID,
		&parentRevisionID, &session.ParentVersion, &taskIDs, &lockedPaths,
		&session.LockedPathHash, &session.ExpiresAt, &consumedAt, &session.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan prepare session: %w", err)
	}
	if err := json.Unmarshal(taskIDs, &session.TaskIDs); err != nil {
		return nil, fmt.Errorf("decode prepare task ids: %w", err)
	}
	if err := json.Unmarshal(lockedPaths, &session.LockedPaths); err != nil {
		return nil, fmt.Errorf("decode prepare locked paths: %w", err)
	}
	if parentRevisionID.Valid {
		session.ParentRevisionID = parentRevisionID.String
	}
	session.ExpiresAt = session.ExpiresAt.UTC()
	session.CreatedAt = session.CreatedAt.UTC()
	if consumedAt.Valid {
		value := consumedAt.Time.UTC()
		session.ConsumedAt = &value
	}
	return &session, nil
}

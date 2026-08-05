package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type prepareSessionStore struct{ db *sql.DB }

func (s *prepareSessionStore) Create(ctx context.Context, session *store.PrepareSession, expectedAuthorizationVersion uint64) error {
	if session == nil {
		return fmt.Errorf("create prepare session: nil session")
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
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
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, session.TokenHash, session.ActorUserID, session.OrganizationID, session.ReleaseDefinitionID,
		valuesOptionalString(session.ParentRevisionID), session.ParentVersion, taskIDs, lockedPaths,
		session.LockedPathHash, session.ExpiresAt.UTC().Format(time.RFC3339Nano),
		formatOptionalTime(session.ConsumedAt), session.CreatedAt.UTC().Format(time.RFC3339Nano))
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
	return getPrepareSession(ctx, s.db, tokenHash)
}

func (s *prepareSessionStore) DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	encodedCutoff := cutoff.UTC().Format(time.RFC3339Nano)
	encodedNow := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM convergence_prepare_sessions
		WHERE (expires_at <= ? AND expires_at < ?)
			OR (consumed_at IS NOT NULL AND consumed_at < ?)
	`, encodedNow, encodedCutoff, encodedCutoff)
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
	var parentRevisionID, consumedAt sql.NullString
	var expiresAt, createdAt string
	if err := queryer.QueryRowContext(ctx, `
		SELECT token_hash, actor_user_id, organization_id, release_definition_id,
			parent_revision_id, parent_version, task_ids, locked_paths,
			locked_path_hash, expires_at, consumed_at, created_at
		FROM convergence_prepare_sessions WHERE token_hash = ?
	`, tokenHash).Scan(
		&session.TokenHash, &session.ActorUserID, &session.OrganizationID, &session.ReleaseDefinitionID,
		&parentRevisionID, &session.ParentVersion, &taskIDs, &lockedPaths,
		&session.LockedPathHash, &expiresAt, &consumedAt, &createdAt,
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
	var err error
	session.ExpiresAt, err = parseSQLiteTime(expiresAt)
	if err != nil {
		return nil, fmt.Errorf("parse prepare expires_at: %w", err)
	}
	session.CreatedAt, err = parseSQLiteTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse prepare created_at: %w", err)
	}
	if consumedAt.Valid {
		session.ConsumedAt, err = parseOptionalTime(consumedAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse prepare consumed_at: %w", err)
		}
	}
	return &session, nil
}

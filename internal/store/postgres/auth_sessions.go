package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type authSessionStore struct{ gorm *DB }

func (s *authSessionStore) Create(ctx context.Context, ss *store.AuthSession) error {
	if ss.CreatedAt.IsZero() {
		ss.CreatedAt = time.Now().UTC()
	}
	revoked := ss.Revoked

	_, err := s.gorm.ExecContext(ctx, `
		INSERT INTO auth_sessions (id, user_id, token_family, refresh_token_hash, expires_at, created_at, revoked)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ss.ID, ss.UserID, ss.TokenFamily, ss.RefreshTokenHash,
		ss.ExpiresAt.UTC().Format(time.RFC3339),
		ss.CreatedAt.UTC().Format(time.RFC3339), revoked,
	)
	if err != nil {
		return fmt.Errorf("insert auth session: %w", err)
	}
	return nil
}

func (s *authSessionStore) Get(ctx context.Context, id string) (*store.AuthSession, error) {
	row := s.gorm.QueryRowContext(ctx, `
		SELECT id, user_id, token_family, refresh_token_hash, expires_at, created_at, revoked
		FROM auth_sessions WHERE id = ?`, id)
	return scanAuthSession(row)
}

func (s *authSessionStore) GetByRefreshHash(ctx context.Context, hash string) (*store.AuthSession, error) {
	row := s.gorm.QueryRowContext(ctx, `
		SELECT id, user_id, token_family, refresh_token_hash, expires_at, created_at, revoked
		FROM auth_sessions WHERE refresh_token_hash = ?`, hash)
	return scanAuthSession(row)
}

func (s *authSessionStore) GetByTokenFamily(ctx context.Context, family string) ([]*store.AuthSession, error) {
	rows, err := s.gorm.QueryContext(ctx, `
		SELECT id, user_id, token_family, refresh_token_hash, expires_at, created_at, revoked
		FROM auth_sessions WHERE token_family = ?`, family)
	if err != nil {
		return nil, fmt.Errorf("query auth sessions by family: %w", err)
	}
	defer rows.Close()

	var sessions []*store.AuthSession
	for rows.Next() {
		ss, err := scanAuthSessionFromRows(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, ss)
	}
	return sessions, rows.Err()
}

func (s *authSessionStore) RevokeFamily(ctx context.Context, family string) error {
	_, err := s.gorm.ExecContext(ctx, `
		UPDATE auth_sessions SET revoked = TRUE WHERE token_family = ?`, family)
	if err != nil {
		return fmt.Errorf("revoke auth session family: %w", err)
	}
	return nil
}

func (s *authSessionStore) RevokeByUserID(ctx context.Context, userID string) error {
	_, err := s.gorm.ExecContext(ctx, `
		UPDATE auth_sessions SET revoked = TRUE WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("revoke auth sessions by user: %w", err)
	}
	return nil
}

func (s *authSessionStore) HasActiveByUserID(ctx context.Context, userID string) (bool, error) {
	var count int
	if err := s.gorm.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM auth_sessions
		WHERE user_id = ? AND revoked = FALSE AND expires_at > ?`,
		userID, time.Now().UTC().Format(time.RFC3339),
	).Scan(&count); err != nil {
		return false, fmt.Errorf("count active auth sessions: %w", err)
	}
	return count > 0, nil
}

func (s *authSessionStore) DeleteExpired(ctx context.Context) (int64, error) {
	result, err := s.gorm.ExecContext(ctx, `
		DELETE FROM auth_sessions WHERE expires_at < ?`,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("delete expired auth sessions: %w", err)
	}
	return result.RowsAffected()
}

func scanAuthSession(row interface{ Scan(...interface{}) error }) (*store.AuthSession, error) {
	var (
		ss           store.AuthSession
		revoked      bool
		expiresStr   string
		createdAtStr string
	)
	if err := row.Scan(&ss.ID, &ss.UserID, &ss.TokenFamily, &ss.RefreshTokenHash,
		&expiresStr, &createdAtStr, &revoked); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan auth session: %w", err)
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresStr)
	if err != nil {
		return nil, fmt.Errorf("parse auth session expires_at: %w", err)
	}
	ss.ExpiresAt = expiresAt
	ss.Revoked = revoked
	createdAt, err := time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parse auth session created_at: %w", err)
	}
	ss.CreatedAt = createdAt
	return &ss, nil
}

func scanAuthSessionFromRows(rows *sql.Rows) (*store.AuthSession, error) {
	return scanAuthSession(rows)
}

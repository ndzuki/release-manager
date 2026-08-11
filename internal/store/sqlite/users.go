package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/contracts"
	"github.com/ndzuki/release-manager/internal/store"
)

// userCursorPrefix version-tags username cursors so malformed or foreign
// tokens are rejected structurally (same stance as the TASK-010 audit cursor).
const userCursorPrefix = "u1|"

type userStore struct{ db *sql.DB }

func (s *userStore) Create(ctx context.Context, u *store.User) error {
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	if u.UpdatedAt.IsZero() {
		u.UpdatedAt = u.CreatedAt
	}
	if u.Status == "" {
		u.Status = store.UserActive
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, provider, subject, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, u.Provider, u.Subject, string(u.Status),
		u.CreatedAt.UTC().Format(time.RFC3339), u.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return store.ErrDuplicateKey
		}
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

// CreateWithMembership atomically creates a local user and its organization membership.
func (s *userStore) CreateWithMembership(ctx context.Context, u *store.User, member *store.OrganizationMember) error {
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	if u.UpdatedAt.IsZero() {
		u.UpdatedAt = u.CreatedAt
	}
	if u.Status == "" {
		u.Status = store.UserActive
	}
	if member.CreatedAt.IsZero() {
		member.CreatedAt = u.CreatedAt
	}
	if member.UpdatedAt.IsZero() {
		member.UpdatedAt = member.CreatedAt
	}
	if member.Role == "" {
		member.Role = store.RoleViewer
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create local user: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback after Commit is a no-op.

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, provider, subject, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, u.Provider, u.Subject, string(u.Status),
		u.CreatedAt.UTC().Format(time.RFC3339), u.UpdatedAt.UTC().Format(time.RFC3339),
	); err != nil {
		if isUniqueConstraint(err) {
			return store.ErrDuplicateKey
		}
		return fmt.Errorf("insert local user: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO organization_members (org_id, user_id, role, optimistic_version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		member.OrgID, member.UserID, string(member.Role), member.OptimisticVersion,
		member.CreatedAt.UTC().Format(time.RFC3339), member.UpdatedAt.UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("insert local user membership: %w", err)
	}
	if err := bumpAuthorizationSourceVersion(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create local user: %w", err)
	}
	return nil
}

func (s *userStore) Get(ctx context.Context, id string) (*store.User, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, provider, subject, status, created_at, updated_at
		FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func (s *userStore) GetByUsername(ctx context.Context, username string) (*store.User, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, provider, subject, status, created_at, updated_at
		FROM users WHERE username = ?`, username)
	return scanUser(row)
}

func (s *userStore) GetByProviderSubject(ctx context.Context, provider, subject string) (*store.User, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, provider, subject, status, created_at, updated_at
		FROM users WHERE provider = ? AND subject = ?`, provider, subject)
	return scanUser(row)
}

func (s *userStore) Update(ctx context.Context, u *store.User) error {
	u.UpdatedAt = time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin update user: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback after Commit is a no-op.

	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET password_hash = ?, status = ?, updated_at = ?
		WHERE id = ?`,
		u.PasswordHash, string(u.Status), u.UpdatedAt.UTC().Format(time.RFC3339), u.ID,
	); err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	if u.Status == store.UserDisabled {
		if _, err := tx.ExecContext(ctx, `
			UPDATE auth_sessions SET revoked = 1 WHERE user_id = ?`, u.ID); err != nil {
			return fmt.Errorf("revoke disabled user sessions: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit update user: %w", err)
	}
	return nil
}

func scanUser(row interface{ Scan(...interface{}) error }) (*store.User, error) {
	var (
		u            store.User
		status       string
		createdAtStr string
		updatedAtStr string
	)
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Provider, &u.Subject,
		&status, &createdAtStr, &updatedAtStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan user: %w", err)
	}
	u.Status = store.UserStatus(status)
	createdAt, err := time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parse user created_at: %w", err)
	}
	u.CreatedAt = createdAt
	updatedAt, err := time.Parse(time.RFC3339, updatedAtStr)
	if err != nil {
		return nil, fmt.Errorf("parse user updated_at: %w", err)
	}
	u.UpdatedAt = updatedAt
	return &u, nil
}

func (s *userStore) Count(ctx context.Context, orgID string) (int64, error) {
	var count int64
	var err error
	if orgID == "" {
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id IN (SELECT user_id FROM organization_members WHERE organization_id = ?)`, orgID).Scan(&count)
	}
	return count, err
}

// List returns a stable keyset page of users ordered by username (REQ-010 cursor pagination).
// The cursor is an opaque base64-encoded username; rows strictly after the cursor are returned.
func (s *userStore) List(ctx context.Context, query store.UserListQuery) (*store.UserPage, error) {
	pageSize := int(contracts.NormalizePageSize(query.PageSize))

	args := make([]any, 0, 2)
	where := ""
	if query.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(query.Cursor)
		if err != nil || !bytes.HasPrefix(raw, []byte(userCursorPrefix)) || len(raw) == len(userCursorPrefix) {
			return nil, store.ErrInvalidCursor
		}
		where = " WHERE username > ?"
		args = append(args, string(raw[len(userCursorPrefix):]))
	}

	querySQL := `
		SELECT id, username, password_hash, provider, subject, status, created_at, updated_at
		FROM users` + where + ` ORDER BY username LIMIT ?`
	args = append(args, pageSize+1)

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := make([]*store.User, 0, pageSize+1)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users: %w", err)
	}

	page := &store.UserPage{Users: users}
	if len(users) > pageSize {
		page.Users = users[:pageSize]
		last := page.Users[len(page.Users)-1]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(userCursorPrefix + last.Username))
	}
	return page, nil
}

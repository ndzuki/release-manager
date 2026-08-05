package postgres

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

const defaultValuesPageSize = 50

var valuesCursorSecret = []byte("release-manager-values-pagination-v1")

type valuesStore struct{ gorm *DB }

type valuesCursor struct {
	QueryHash string `json:"query_hash"`
	Version   int64  `json:"version"`
	ID        string `json:"id"`
	Signature string `json:"signature"`
}

func (s *valuesStore) Create(ctx context.Context, revision *store.ValuesRevision) error {
	if revision.StateVersion == 0 {
		revision.StateVersion = 1
	}
	if revision.CreatedAt.IsZero() {
		revision.CreatedAt = time.Now().UTC()
	}
	if revision.UpdatedAt.IsZero() {
		revision.UpdatedAt = revision.CreatedAt
	}
	refs, err := json.Marshal(revision.SecretRefs)
	if err != nil {
		return fmt.Errorf("encode values secret refs: %w", err)
	}
	_, err = s.gorm.ExecContext(ctx, `
		INSERT INTO values_revisions (
			id, release_definition_id, version, state_version, status, "values",
			digest, parent_revision_id, secret_refs, created_by, created_by_user_id,
			approved_by, approved_at, rejected_by, rejection_reason, submitted_at, decided_at,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, '', '', ?, ?, ?, ?)
	`, revision.ID, revision.ReleaseDefinitionID, revision.Version, revision.StateVersion,
		string(revision.Status), []byte(revision.CanonicalDocument), revision.Digest, valuesOptionalString(revision.ParentRevisionID),
		refs, revision.CreatedByUserID, revision.CreatedByUserID, valuesOptionalTime(revision.SubmittedAt),
		valuesOptionalTime(revision.DecidedAt), revision.CreatedAt.UTC(), revision.UpdatedAt.UTC())
	if err != nil {
		if isUniqueConstraint(err) {
			return store.ErrDuplicateKey
		}
		return fmt.Errorf("insert values revision: %w", err)
	}
	return nil
}

func (s *valuesStore) Get(ctx context.Context, id string) (*store.ValuesRevision, error) {
	return scanValues(s.gorm.QueryRowContext(ctx, valuesSelect+` WHERE id = ?`, id))
}

func (s *valuesStore) GetByDigest(ctx context.Context, definitionID, digest string) (*store.ValuesRevision, error) {
	return scanValues(s.gorm.QueryRowContext(ctx, valuesSelect+`
		WHERE release_definition_id = ? AND digest = ?
		ORDER BY version DESC, id DESC LIMIT 1
	`, definitionID, digest))
}

func (s *valuesStore) GetLatestApproved(ctx context.Context, definitionID string) (*store.ValuesRevision, error) {
	return scanValues(s.gorm.QueryRowContext(ctx, valuesSelect+`
		WHERE release_definition_id = ? AND status = 'approved'
		ORDER BY version DESC, id DESC LIMIT 1
	`, definitionID))
}

func (s *valuesStore) GetLatest(ctx context.Context, definitionID string) (*store.ValuesRevision, error) {
	return scanValues(s.gorm.QueryRowContext(ctx, valuesSelect+`
		WHERE release_definition_id = ?
		ORDER BY version DESC, id DESC LIMIT 1
	`, definitionID))
}

func (s *valuesStore) List(ctx context.Context, definitionID string) ([]*store.ValuesRevision, error) {
	rows, err := s.gorm.QueryContext(ctx, valuesSelect+`
		WHERE release_definition_id = ? ORDER BY version DESC, id DESC
	`, definitionID)
	if err != nil {
		return nil, fmt.Errorf("list values revisions: %w", err)
	}
	defer rows.Close()
	return scanValuesRows(rows)
}

func (s *valuesStore) ListPage(ctx context.Context, filter store.ValuesListFilter) (*store.ValuesPage, error) {
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = defaultValuesPageSize
	}
	if pageSize > 100 {
		pageSize = 100
	}
	queryHash := valuesQueryHash(filter)
	where := ` WHERE release_definition_id = ?`
	args := []any{filter.ReleaseDefinitionID}
	if filter.Status != "" {
		where += ` AND status = ?`
		args = append(args, string(filter.Status))
	}
	if filter.Cursor != "" {
		cursor, err := decodeValuesCursor(filter.Cursor, queryHash)
		if err != nil {
			return nil, err
		}
		where += ` AND (version < ? OR (version = ? AND id < ?))`
		args = append(args, cursor.Version, cursor.Version, cursor.ID)
	}
	rows, err := s.gorm.QueryContext(ctx, valuesSelect+where+`
		ORDER BY version DESC, id DESC LIMIT ?
	`, append(args, pageSize+1)...)
	if err != nil {
		return nil, fmt.Errorf("list values revision page: %w", err)
	}
	defer rows.Close()
	items, err := scanValuesRows(rows)
	if err != nil {
		return nil, err
	}
	hasMore := len(items) > pageSize
	if hasMore {
		items = items[:pageSize]
	}
	page := &store.ValuesPage{Items: items}
	if hasMore {
		last := items[len(items)-1]
		page.NextCursor, err = encodeValuesCursor(valuesCursor{QueryHash: queryHash, Version: last.Version, ID: last.ID})
		if err != nil {
			return nil, err
		}
	}
	return page, nil
}

func (s *valuesStore) GetNextRevisionNumber(ctx context.Context, definitionID string) (int64, error) {
	var maxVersion sql.NullInt64
	if err := s.gorm.QueryRowContext(ctx, `
		SELECT MAX(version) FROM values_revisions WHERE release_definition_id = ?
	`, definitionID).Scan(&maxVersion); err != nil {
		return 0, fmt.Errorf("get next values version: %w", err)
	}
	if maxVersion.Valid {
		return maxVersion.Int64 + 1, nil
	}
	return 1, nil
}

const valuesSelect = `
	SELECT id, release_definition_id, version, state_version, status, "values",
		digest, parent_revision_id, secret_refs, created_by_user_id,
		submitted_at, decided_at, created_at, updated_at
	FROM values_revisions`

func scanValues(row interface{ Scan(...any) error }) (*store.ValuesRevision, error) {
	var revision store.ValuesRevision
	var status string
	var refs []byte
	var parentRevisionID sql.NullString
	var submittedAt, decidedAt sql.NullTime
	if err := row.Scan(
		&revision.ID, &revision.ReleaseDefinitionID, &revision.Version, &revision.StateVersion,
		&status, &revision.CanonicalDocument, &revision.Digest, &parentRevisionID, &refs,
		&revision.CreatedByUserID, &submittedAt, &decidedAt, &revision.CreatedAt, &revision.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan values revision: %w", err)
	}
	if len(refs) > 0 {
		if err := json.Unmarshal(refs, &revision.SecretRefs); err != nil {
			return nil, fmt.Errorf("decode values secret refs: %w", err)
		}
	}
	if parentRevisionID.Valid {
		revision.ParentRevisionID = parentRevisionID.String
	}
	revision.Status = store.ValuesStatus(status)
	revision.CreatedAt = revision.CreatedAt.UTC()
	revision.UpdatedAt = revision.UpdatedAt.UTC()
	if submittedAt.Valid {
		value := submittedAt.Time.UTC()
		revision.SubmittedAt = &value
	}
	if decidedAt.Valid {
		value := decidedAt.Time.UTC()
		revision.DecidedAt = &value
	}
	return &revision, nil
}

func scanValuesRows(rows *sql.Rows) ([]*store.ValuesRevision, error) {
	items := make([]*store.ValuesRevision, 0)
	for rows.Next() {
		item, err := scanValues(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate values revisions: %w", err)
	}
	return items, nil
}

func valuesOptionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func valuesOptionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func valuesQueryHash(filter store.ValuesListFilter) string {
	sum := sha256.Sum256([]byte(filter.ReleaseDefinitionID + "\n" + string(filter.Status)))
	return hex.EncodeToString(sum[:])
}

func encodeValuesCursor(cursor valuesCursor) (string, error) {
	cursor.Signature = valuesCursorSignature(cursor.QueryHash, cursor.Version, cursor.ID)
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode values cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeValuesCursor(encoded, expectedQueryHash string) (*valuesCursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, store.ErrInvalidCursor
	}
	var cursor valuesCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.ID == "" || cursor.Version < 1 {
		return nil, store.ErrInvalidCursor
	}
	if cursor.QueryHash != expectedQueryHash || !hmac.Equal(
		[]byte(cursor.Signature), []byte(valuesCursorSignature(cursor.QueryHash, cursor.Version, cursor.ID)),
	) {
		return nil, store.ErrInvalidCursor
	}
	return &cursor, nil
}

func valuesCursorSignature(queryHash string, version int64, id string) string {
	mac := hmac.New(sha256.New, valuesCursorSecret)
	_, _ = fmt.Fprintf(mac, "%s\x00%d\x00%s", queryHash, version, id)
	return hex.EncodeToString(mac.Sum(nil))
}

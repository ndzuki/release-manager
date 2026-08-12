package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type idempotencyStore struct{ db *sql.DB }

type idempotencyQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type idempotencyExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type idempotencyDB interface {
	idempotencyQueryer
	idempotencyExecer
}

func (s *idempotencyStore) CreateOrGet(
	ctx context.Context,
	record *store.IdempotencyRecord,
) (*store.IdempotencyRecord, bool, error) {
	return createOrGetIdempotencyRecord(ctx, s.db, record, time.Now().UTC())
}

func createOrGetIdempotencyRecord(
	ctx context.Context,
	db idempotencyDB,
	record *store.IdempotencyRecord,
	now time.Time,
) (*store.IdempotencyRecord, bool, error) {
	if err := validateIdempotencyRecord(record); err != nil {
		return nil, false, err
	}
	for {
		inserted, err := insertIdempotencyRecord(ctx, db, record, true)
		if err != nil {
			return nil, false, err
		}
		if inserted {
			return cloneIdempotencyRecord(record), true, nil
		}

		existing, err := loadIdempotencyRecord(ctx, db, record.Scope, record.Key)
		if err != nil {
			return nil, false, err
		}
		if !existing.ExpiresAt.After(now) {
			deleted, deleteErr := db.ExecContext(ctx, `
				DELETE FROM idempotency_records
				WHERE scope = ? AND text_key = ? AND expires_at <= ?
			`, record.Scope, record.Key, now.Format(time.RFC3339Nano))
			if deleteErr != nil {
				return nil, false, fmt.Errorf("delete expired idempotency record: %w", deleteErr)
			}
			deletedRows, rowsErr := deleted.RowsAffected()
			if rowsErr != nil {
				return nil, false, fmt.Errorf("expired idempotency rows affected: %w", rowsErr)
			}
			if deletedRows == 1 {
				continue
			}
			existing, err = loadIdempotencyRecord(ctx, db, record.Scope, record.Key)
			if err != nil {
				return nil, false, err
			}
		}
		if existing.RequestHash != record.RequestHash {
			return nil, false, store.ErrIdempotencyConflict
		}
		return existing, false, nil
	}
}

func (s *idempotencyStore) GetExpired(
	ctx context.Context,
	before time.Time,
	limit int,
) ([]*store.IdempotencyRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT scope, text_key, request_hash, response_ref, expires_at
		FROM idempotency_records
		WHERE expires_at < ?
		ORDER BY expires_at, scope, text_key
		LIMIT ?
	`, before.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, fmt.Errorf("query expired idempotency records: %w", err)
	}
	defer rows.Close()

	records := make([]*store.IdempotencyRecord, 0)
	for rows.Next() {
		record, scanErr := scanIdempotencyRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired idempotency records: %w", err)
	}
	return records, nil
}

func (s *idempotencyStore) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM idempotency_records WHERE expires_at < ?
	`, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("delete expired idempotency records: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("deleted idempotency rows affected: %w", err)
	}
	return count, nil
}

func validateIdempotencyRecord(record *store.IdempotencyRecord) error {
	if record == nil || record.Scope == "" || record.Key == "" || record.RequestHash == "" {
		return fmt.Errorf("create idempotency record: invalid record")
	}
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = time.Now().UTC().Add(24 * time.Hour)
	}
	if record.ResponseRef == nil {
		record.ResponseRef = []byte{}
	}
	return nil
}

func insertIdempotencyRecord(
	ctx context.Context,
	execer idempotencyExecer,
	record *store.IdempotencyRecord,
	ignoreConflict bool,
) (bool, error) {
	if err := validateIdempotencyRecord(record); err != nil {
		return false, err
	}
	query := `
		INSERT INTO idempotency_records (scope, text_key, request_hash, response_ref, expires_at)
		VALUES (?, ?, ?, ?, ?)`
	if ignoreConflict {
		query += ` ON CONFLICT(scope, text_key) DO NOTHING`
	}
	result, err := execer.ExecContext(
		ctx,
		query,
		record.Scope,
		record.Key,
		record.RequestHash,
		[]byte(record.ResponseRef),
		record.ExpiresAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return false, store.ErrIdempotencyConflict
		}
		return false, fmt.Errorf("insert idempotency record: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("idempotency rows affected: %w", err)
	}
	return rowsAffected == 1, nil
}

func loadActiveIdempotencyRecord(
	ctx context.Context,
	queryer idempotencyQueryer,
	scope string,
	key string,
	now time.Time,
) (*store.IdempotencyRecord, error) {
	row := queryer.QueryRowContext(ctx, `
		SELECT scope, text_key, request_hash, response_ref, expires_at
		FROM idempotency_records
		WHERE scope = ? AND text_key = ? AND expires_at > ?
	`, scope, key, now.UTC().Format(time.RFC3339Nano))
	return scanLoadedIdempotencyRecord(row)
}

func loadIdempotencyRecord(
	ctx context.Context,
	queryer idempotencyQueryer,
	scope string,
	key string,
) (*store.IdempotencyRecord, error) {
	row := queryer.QueryRowContext(ctx, `
		SELECT scope, text_key, request_hash, response_ref, expires_at
		FROM idempotency_records
		WHERE scope = ? AND text_key = ?
	`, scope, key)
	return scanLoadedIdempotencyRecord(row)
}

func scanLoadedIdempotencyRecord(scanner idempotencyScanner) (*store.IdempotencyRecord, error) {
	record, err := scanIdempotencyRecord(scanner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return record, nil
}

type idempotencyScanner interface {
	Scan(dest ...any) error
}

func scanIdempotencyRecord(scanner idempotencyScanner) (*store.IdempotencyRecord, error) {
	var record store.IdempotencyRecord
	var expiresAt string
	if err := scanner.Scan(&record.Scope, &record.Key, &record.RequestHash, &record.ResponseRef, &expiresAt); err != nil {
		return nil, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("parse idempotency expires_at: %w", err)
	}
	record.ExpiresAt = parsed
	return &record, nil
}

func cloneIdempotencyRecord(record *store.IdempotencyRecord) *store.IdempotencyRecord {
	clone := *record
	clone.ResponseRef = append([]byte(nil), record.ResponseRef...)
	return &clone
}

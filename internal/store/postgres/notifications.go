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

type notificationStore struct{ gorm *DB }

func (s *notificationStore) Create(ctx context.Context, j *store.NotificationJob) error {
	metaJSON, err := json.Marshal(j.Metadata)
	if err != nil {
		return fmt.Errorf("marshal notification metadata: %w", err)
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = time.Now().UTC()
	}
	if j.UpdatedAt.IsZero() {
		j.UpdatedAt = j.CreatedAt
	}

	var nextRetryStr *string
	if j.NextRetryAt != nil {
		v := j.NextRetryAt.UTC().Format(time.RFC3339)
		nextRetryStr = &v
	}
	var sentAtStr *string
	if j.SentAt != nil {
		v := j.SentAt.UTC().Format(time.RFC3339)
		sentAtStr = &v
	}

	_, err = s.gorm.ExecContext(ctx, `
		INSERT INTO notification_jobs (id, operation_id, channel, recipient,
			status, attempts, retry_count, max_retries, error_code, next_retry_at, last_error,
			sent_at, metadata, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		j.ID, j.OperationID, string(j.Channel), j.Recipient,
		string(j.Status), j.Attempts, j.RetryCount, j.MaxRetries, j.ErrorCode,
		nextRetryStr, j.LastError, sentAtStr,
		string(metaJSON), j.CreatedAt.UTC().Format(time.RFC3339), j.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert notification job: %w", err)
	}
	return nil
}

func (s *notificationStore) Get(ctx context.Context, id string) (*store.NotificationJob, error) {
	row := s.gorm.QueryRowContext(ctx, `
		SELECT id, operation_id, channel, recipient, status, attempts,
			retry_count, max_retries, error_code, next_retry_at, last_error,
			sent_at, dead_letter_at,
			metadata, created_at, updated_at
		FROM notification_jobs WHERE id = ?
	`, id)
	return scanNotificationJob(row)
}

func (s *notificationStore) GetPending(ctx context.Context, now time.Time, limit int) ([]*store.NotificationJob, error) {
	rows, err := s.gorm.QueryContext(ctx, `
		SELECT id, operation_id, channel, recipient, status, attempts,
			retry_count, max_retries, error_code, next_retry_at, last_error,
			sent_at, dead_letter_at,
			metadata, created_at, updated_at
		FROM notification_jobs
		WHERE status IN ('pending', 'failed')
		  AND (next_retry_at IS NULL OR next_retry_at <= ?)
		ORDER BY created_at ASC
		LIMIT ?
	`, now.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return nil, fmt.Errorf("query pending notification jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*store.NotificationJob
	for rows.Next() {
		j, err := scanNotificationJobFromRows(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ClaimNext atomically claims the next due pending job by updating its
// status to 'sending' in a single statement with a row-level lock.
// Returns nil, nil when no jobs are available.
func (s *notificationStore) ClaimNext(ctx context.Context, now time.Time) (*store.NotificationJob, error) {
	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin claim tx: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return
		}
	}()

	nowStr := now.UTC().Format(time.RFC3339)

	// SELECT ... FOR UPDATE is not supported by modernc.org/sqlite with the
	// CGo-free pure-Go driver in all modes, so the SQLite twin of this store
	// uses an immediate UPDATE with RETURNING instead. On PostgreSQL the
	// subquery must lock the candidate row: without FOR UPDATE SKIP LOCKED,
	// two concurrent workers can both claim the same job (READ COMMITTED
	// re-evaluates the UPDATE target after the lock is released), double
	// incrementing attempts and double delivering — breaking AC-031-01
	// idempotency. SKIP LOCKED makes concurrent claims disjoint.
	row := tx.QueryRowContext(ctx, `
		UPDATE notification_jobs
		SET status = 'sending', attempts = attempts + 1, updated_at = ?
		WHERE id = (
			SELECT id FROM notification_jobs
			WHERE status IN ('pending', 'failed')
			  AND (next_retry_at IS NULL OR next_retry_at <= ?)
			ORDER BY created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, operation_id, channel, recipient, status, attempts,
			retry_count, max_retries, error_code, next_retry_at, last_error,
			sent_at, dead_letter_at,
			metadata, created_at, updated_at
	`, nowStr, nowStr)

	j, err := scanNotificationJob(row)
	if err != nil {
		if err == store.ErrNotFound {
			// No pending jobs — normal empty poll.
			return nil, nil
		}
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}
	return j, nil
}

func (s *notificationStore) UpdateStatus(ctx context.Context, id string, status store.NotificationStatus,
	attempts, retryCount int, errorCode string, nextRetryAt *time.Time, lastError string, sentAt *time.Time,
) error {
	var nextRetryStr *string
	if nextRetryAt != nil {
		v := nextRetryAt.UTC().Format(time.RFC3339)
		nextRetryStr = &v
	}
	var sentAtStr *string
	if sentAt != nil {
		v := sentAt.UTC().Format(time.RFC3339)
		sentAtStr = &v
	}

	_, err := s.gorm.ExecContext(ctx, `
		UPDATE notification_jobs
		SET status = ?, attempts = ?, retry_count = ?, error_code = ?,
		    next_retry_at = ?, last_error = ?, sent_at = ?, updated_at = ?
		WHERE id = ?
	`, string(status), attempts, retryCount, errorCode,
		nextRetryStr, lastError, sentAtStr, nowUTC(), id)
	if err != nil {
		return fmt.Errorf("update notification job status: %w", err)
	}
	return nil
}

func (s *notificationStore) MarkDeadLetter(ctx context.Context, id, errorCode, lastError string) error {
	now := nowUTC()
	nowStr := now
	_, err := s.gorm.ExecContext(ctx, `
		UPDATE notification_jobs
		SET status = 'dead_letter', error_code = ?, last_error = ?,
		    dead_letter_at = ?, updated_at = ?
		WHERE id = ?
	`, errorCode, lastError, nowStr, nowStr, id)
	if err != nil {
		return fmt.Errorf("mark notification dead letter: %w", err)
	}
	return nil
}

// DeleteDeadLetterBefore removes dead-letter jobs older than the cutoff.
// Returns the number of rows deleted.
func (s *notificationStore) DeleteDeadLetterBefore(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.gorm.ExecContext(ctx, `
		DELETE FROM notification_jobs
		WHERE status = 'dead_letter' AND dead_letter_at <= ?
	`, before.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("delete dead letter jobs: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

func scanNotificationJob(row interface{ Scan(...interface{}) error }) (*store.NotificationJob, error) {
	var (
		id, opID, channel, recipient, status, errorCode, lastError, metaJSON string
		attempts, retryCount, maxRetries                                     int
		nextRetryStr, sentAtStr, deadLetterStr                               *string
		createdStr, updatedStr                                               string
	)
	err := row.Scan(&id, &opID, &channel, &recipient, &status, &attempts,
		&retryCount, &maxRetries, &errorCode, &nextRetryStr, &lastError,
		&sentAtStr, &deadLetterStr,
		&metaJSON, &createdStr, &updatedStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan notification job: %w", err)
	}
	return buildNotificationJob(id, opID, channel, recipient, status, attempts,
		retryCount, maxRetries, errorCode, nextRetryStr, lastError, sentAtStr, deadLetterStr,
		metaJSON, createdStr, updatedStr)
}

func scanNotificationJobFromRows(rows *sql.Rows) (*store.NotificationJob, error) {
	var (
		id, opID, channel, recipient, status, errorCode, lastError, metaJSON string
		attempts, retryCount, maxRetries                                     int
		nextRetryStr, sentAtStr, deadLetterStr                               *string
		createdStr, updatedStr                                               string
	)
	err := rows.Scan(&id, &opID, &channel, &recipient, &status, &attempts,
		&retryCount, &maxRetries, &errorCode, &nextRetryStr, &lastError,
		&sentAtStr, &deadLetterStr,
		&metaJSON, &createdStr, &updatedStr)
	if err != nil {
		return nil, fmt.Errorf("scan notification job from rows: %w", err)
	}
	return buildNotificationJob(id, opID, channel, recipient, status, attempts,
		retryCount, maxRetries, errorCode, nextRetryStr, lastError, sentAtStr, deadLetterStr,
		metaJSON, createdStr, updatedStr)
}

func buildNotificationJob(id, opID, channel, recipient, status string,
	attempts, retryCount, maxRetries int, errorCode string,
	nextRetryStr *string, lastError string,
	sentAtStr, deadLetterStr *string,
	metaJSON, createdStr, updatedStr string,
) (*store.NotificationJob, error) {
	createdAt, err := time.Parse(time.RFC3339, createdStr)
	if err != nil {
		return nil, fmt.Errorf("parse notification created_at: %w", err)
	}
	updatedAt, err := time.Parse(time.RFC3339, updatedStr)
	if err != nil {
		return nil, fmt.Errorf("parse notification updated_at: %w", err)
	}

	var metadata map[string]string
	if metaJSON != "" && metaJSON != "{}" {
		if err := json.Unmarshal([]byte(metaJSON), &metadata); err != nil {
			return nil, fmt.Errorf("unmarshal notification metadata: %w", err)
		}
	}

	j := &store.NotificationJob{
		ID:          id,
		OperationID: opID,
		Channel:     store.NotificationChannel(channel),
		Recipient:   recipient,
		Status:      store.NotificationStatus(status),
		Attempts:    attempts,
		RetryCount:  retryCount,
		MaxRetries:  maxRetries,
		ErrorCode:   errorCode,
		LastError:   lastError,
		Metadata:    metadata,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}
	if nextRetryStr != nil {
		t, err := time.Parse(time.RFC3339, *nextRetryStr)
		if err != nil {
			return nil, fmt.Errorf("parse next_retry_at: %w", err)
		}
		j.NextRetryAt = &t
	}
	if sentAtStr != nil {
		t, err := time.Parse(time.RFC3339, *sentAtStr)
		if err != nil {
			return nil, fmt.Errorf("parse sent_at: %w", err)
		}
		j.SentAt = &t
	}
	if deadLetterStr != nil {
		t, err := time.Parse(time.RFC3339, *deadLetterStr)
		if err != nil {
			return nil, fmt.Errorf("parse dead_letter_at: %w", err)
		}
		j.DeadLetterAt = &t
	}
	return j, nil
}

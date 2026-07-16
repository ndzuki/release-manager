package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type notificationStore struct{ db *sql.DB }

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

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO notification_jobs (id, operation_id, channel, recipient,
			status, retry_count, max_retries, next_retry_at, last_error,
			metadata, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		j.ID, j.OperationID, string(j.Channel), j.Recipient,
		string(j.Status), j.RetryCount, j.MaxRetries, nextRetryStr, j.LastError,
		string(metaJSON), j.CreatedAt.UTC().Format(time.RFC3339), j.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert notification job: %w", err)
	}
	return nil
}

func (s *notificationStore) Get(ctx context.Context, id string) (*store.NotificationJob, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, operation_id, channel, recipient, status, retry_count,
			max_retries, next_retry_at, last_error, dead_letter_at,
			metadata, created_at, updated_at
		FROM notification_jobs WHERE id = ?
	`, id)
	return scanNotificationJob(row)
}

func (s *notificationStore) GetPending(ctx context.Context, now time.Time, limit int) ([]*store.NotificationJob, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, operation_id, channel, recipient, status, retry_count,
			max_retries, next_retry_at, last_error, dead_letter_at,
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

func (s *notificationStore) UpdateStatus(ctx context.Context, id string, status store.NotificationStatus, retryCount int, nextRetryAt *time.Time, lastError string) error {
	var nextRetryStr *string
	if nextRetryAt != nil {
		v := nextRetryAt.UTC().Format(time.RFC3339)
		nextRetryStr = &v
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE notification_jobs
		SET status = ?, retry_count = ?, next_retry_at = ?, last_error = ?, updated_at = ?
		WHERE id = ?
	`, string(status), retryCount, nextRetryStr, lastError, nowUTC(), id)
	if err != nil {
		return fmt.Errorf("update notification job status: %w", err)
	}
	return nil
}

func (s *notificationStore) MarkDeadLetter(ctx context.Context, id string) error {
	now := nowUTC()
	_, err := s.db.ExecContext(ctx, `
		UPDATE notification_jobs
		SET status = 'dead_letter', dead_letter_at = ?, updated_at = ?
		WHERE id = ?
	`, now, now, id)
	if err != nil {
		return fmt.Errorf("mark notification dead letter: %w", err)
	}
	return nil
}

func scanNotificationJob(row interface{ Scan(...interface{}) error }) (*store.NotificationJob, error) {
	var (
		id, opID, channel, recipient, status, lastError, metaJSON string
		retryCount, maxRetries                                     int
		nextRetryStr, deadLetterStr                                *string
		createdStr, updatedStr                                     string
	)
	err := row.Scan(&id, &opID, &channel, &recipient, &status, &retryCount,
		&maxRetries, &nextRetryStr, &lastError, &deadLetterStr,
		&metaJSON, &createdStr, &updatedStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan notification job: %w", err)
	}
	return buildNotificationJob(id, opID, channel, recipient, status, retryCount, maxRetries,
		nextRetryStr, lastError, deadLetterStr, metaJSON, createdStr, updatedStr)
}

func scanNotificationJobFromRows(rows *sql.Rows) (*store.NotificationJob, error) {
	var (
		id, opID, channel, recipient, status, lastError, metaJSON string
		retryCount, maxRetries                                     int
		nextRetryStr, deadLetterStr                                *string
		createdStr, updatedStr                                     string
	)
	err := rows.Scan(&id, &opID, &channel, &recipient, &status, &retryCount,
		&maxRetries, &nextRetryStr, &lastError, &deadLetterStr,
		&metaJSON, &createdStr, &updatedStr)
	if err != nil {
		return nil, fmt.Errorf("scan notification job from rows: %w", err)
	}
	return buildNotificationJob(id, opID, channel, recipient, status, retryCount, maxRetries,
		nextRetryStr, lastError, deadLetterStr, metaJSON, createdStr, updatedStr)
}

func buildNotificationJob(id, opID, channel, recipient, status string,
	retryCount, maxRetries int, nextRetryStr *string, lastError string,
	deadLetterStr *string, metaJSON, createdStr, updatedStr string,
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
		RetryCount:  retryCount,
		MaxRetries:  maxRetries,
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
	if deadLetterStr != nil {
		t, err := time.Parse(time.RFC3339, *deadLetterStr)
		if err != nil {
			return nil, fmt.Errorf("parse dead_letter_at: %w", err)
		}
		j.DeadLetterAt = &t
	}
	return j, nil
}

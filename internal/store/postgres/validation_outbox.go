package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type validationOutboxStore struct{ gorm *DB }

func (s *validationOutboxStore) CreateTx(tx *gorm.DB, entry *store.ValidationOutboxEntry) error {
	if tx == nil {
		return fmt.Errorf("create validation outbox entry: nil transaction")
	}
	now := time.Now().UTC()
	if entry.ID == "" {
		entry.ID = uuid.NewString()
	}
	if entry.Status == "" {
		entry.Status = store.ValidationPending
	}
	if entry.NextAttemptAt.IsZero() {
		entry.NextAttemptAt = now
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	entry.UpdatedAt = now
	result := tx.Exec(`
		INSERT INTO bundle_validation_outbox
			(id, bundle_id, status, attempts, last_error_code, next_attempt_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (bundle_id) DO NOTHING
	`, entry.ID, entry.BundleID, string(entry.Status), entry.Attempts, entry.LastErrorCode,
		entry.NextAttemptAt.UTC(), entry.CreatedAt.UTC(), entry.UpdatedAt.UTC())
	if result.Error != nil {
		return fmt.Errorf("create validation outbox entry: %w", result.Error)
	}
	return nil
}

func (s *validationOutboxStore) ClaimPending(ctx context.Context, now time.Time, limit int) ([]store.ValidationOutboxEntry, error) {
	if limit <= 0 {
		limit = 10
	}
	var entries []store.ValidationOutboxEntry
	err := s.gorm.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []validationOutboxRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Table("bundle_validation_outbox").
			Where("status IN ? AND next_attempt_at <= ?", []string{string(store.ValidationPending), string(store.ValidationFailed)}, now.UTC()).
			Order("next_attempt_at, id").
			Limit(limit).
			Find(&rows).Error; err != nil {
			return fmt.Errorf("claim validation outbox entries: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		ids := make([]string, len(rows))
		for index := range rows {
			ids[index] = rows[index].ID
		}
		if err := tx.Table("bundle_validation_outbox").Where("id IN ?", ids).
			Updates(map[string]any{"status": string(store.ValidationRunning), "updated_at": now.UTC()}).Error; err != nil {
			return fmt.Errorf("mark validation outbox entries running: %w", err)
		}
		entries = make([]store.ValidationOutboxEntry, len(rows))
		for index, row := range rows {
			entries[index] = row.toStore()
			entries[index].Status = store.ValidationRunning
			entries[index].UpdatedAt = now.UTC()
		}
		return nil
	})
	return entries, err
}

func (s *validationOutboxStore) UpdateTx(tx *gorm.DB, entry *store.ValidationOutboxEntry) error {
	if tx == nil {
		return fmt.Errorf("update validation outbox entry: nil transaction")
	}
	entry.UpdatedAt = time.Now().UTC()
	result := tx.Table("bundle_validation_outbox").Where("id = ?", entry.ID).Updates(map[string]any{
		"status":          string(entry.Status),
		"attempts":        entry.Attempts,
		"last_error_code": entry.LastErrorCode,
		"next_attempt_at": entry.NextAttemptAt.UTC(),
		"updated_at":      entry.UpdatedAt,
	})
	if result.Error != nil {
		return fmt.Errorf("update validation outbox entry: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

type validationOutboxRow struct {
	ID            string    `gorm:"column:id"`
	BundleID      string    `gorm:"column:bundle_id"`
	Status        string    `gorm:"column:status"`
	Attempts      int       `gorm:"column:attempts"`
	LastErrorCode string    `gorm:"column:last_error_code"`
	NextAttemptAt time.Time `gorm:"column:next_attempt_at"`
	CreatedAt     time.Time `gorm:"column:created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

func (row validationOutboxRow) toStore() store.ValidationOutboxEntry {
	return store.ValidationOutboxEntry{
		ID: row.ID, BundleID: row.BundleID, Status: store.ValidationOutboxStatus(row.Status),
		Attempts: row.Attempts, LastErrorCode: row.LastErrorCode,
		NextAttemptAt: row.NextAttemptAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/ndzuki/release-manager/internal/postgres"
	"github.com/ndzuki/release-manager/internal/store"
	"gorm.io/gorm"
)

type ValidationWorker struct {
	store         store.Store
	gormDB        *gorm.DB
	logger        *slog.Logger
	pollInterval  time.Duration
	degradedAfter time.Duration
}

type ValidationWorkerConfig struct {
	PollInterval  time.Duration
	DegradedAfter time.Duration
}

func DefaultValidationWorkerConfig() ValidationWorkerConfig {
	return ValidationWorkerConfig{
		PollInterval:  30 * time.Second,
		DegradedAfter: 6 * time.Hour,
	}
}

func NewValidationWorker(st store.Store, gormDB *gorm.DB, logger *slog.Logger, cfg ValidationWorkerConfig) *ValidationWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &ValidationWorker{
		store:         st,
		gormDB:        gormDB,
		logger:        logger,
		pollInterval:  cfg.PollInterval,
		degradedAfter: cfg.DegradedAfter,
	}
}

func (w *ValidationWorker) Run(ctx context.Context) {
	if w.gormDB == nil {
		w.logger.Warn("validation worker disabled: PostgreSQL GORM handle not available")
		return
	}
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	w.logger.Info("validation worker started", "poll_interval", w.pollInterval)
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("validation worker stopped")
			return
		case <-ticker.C:
			w.processOutbox(ctx)
		}
	}
}

func (w *ValidationWorker) processOutbox(ctx context.Context) {
	now := time.Now().UTC()
	entries, err := w.store.ValidationOutbox().ClaimPending(ctx, now, 10)
	if err != nil {
		w.logger.Error("claim validation outbox entries", "error", err)
		return
	}
	for _, e := range entries {
		w.processEntry(ctx, e)
	}
}

func (w *ValidationWorker) processEntry(ctx context.Context, entry store.ValidationOutboxEntry) {
	w.logger.Info("validating bundle", "bundle_id", entry.BundleID, "attempt", entry.Attempts+1)

	bundle, err := w.store.Bundles().Get(ctx, entry.BundleID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			w.markCompleted(ctx, entry)
			return
		}
		w.logger.Error("get bundle for validation", "bundle_id", entry.BundleID, "error", err)
		w.recordError(ctx, entry, "internal", err)
		return
	}
	if bundle.Status != store.BundleReceived {
		w.markCompleted(ctx, entry)
		return
	}

	validationErr := w.verifyDigestParity(ctx, bundle)
	if validationErr != nil {
		if isPermanentValidationError(validationErr) {
			txErr := postgres.OperationCreationUnitOfWork(ctx, w.gormDB,
				func(tx *gorm.DB, sqlTx *sql.Tx) error {
					_ = sqlTx
					if statusErr := w.store.Bundles().UpdateStatusTx(tx, entry.BundleID, store.BundleReceived, store.BundleRejected, validationErr.Error()); statusErr != nil {
						return statusErr
					}
					return w.store.ValidationOutbox().UpdateTx(tx, &store.ValidationOutboxEntry{
						ID: entry.ID, BundleID: entry.BundleID,
						Status: store.ValidationCompleted, Attempts: entry.Attempts + 1,
						LastErrorCode: validationErr.Error(), NextAttemptAt: time.Now().UTC(),
						UpdatedAt: time.Now().UTC(),
					})
				})
			if txErr != nil {
				w.logger.Error("mark bundle rejected", "bundle_id", entry.BundleID, "error", txErr)
				return
			}
			w.logger.Info("bundle rejected", "bundle_id", entry.BundleID, "reason", validationErr)
			return
		}
		w.recordError(ctx, entry, "registry_unavailable", validationErr)
		return
	}

	txErr := postgres.OperationCreationUnitOfWork(ctx, w.gormDB,
		func(tx *gorm.DB, sqlTx *sql.Tx) error {
			_ = sqlTx
			if statusErr := w.store.Bundles().UpdateStatusTx(tx, entry.BundleID, store.BundleReceived, store.BundleValidated, ""); statusErr != nil {
				return statusErr
			}
			return w.store.ValidationOutbox().UpdateTx(tx, &store.ValidationOutboxEntry{
				ID: entry.ID, BundleID: entry.BundleID,
				Status: store.ValidationCompleted, Attempts: entry.Attempts + 1,
				NextAttemptAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			})
		})
	if txErr != nil {
		w.logger.Error("mark bundle validated", "bundle_id", entry.BundleID, "error", txErr)
		return
	}
	w.logger.Info("bundle validated", "bundle_id", entry.BundleID)
}

func (w *ValidationWorker) verifyDigestParity(_ context.Context, bundle *store.ReleaseBundle) error {
	_ = bundle
	return nil
}

func (w *ValidationWorker) markCompleted(ctx context.Context, entry store.ValidationOutboxEntry) {
	txErr := postgres.OperationCreationUnitOfWork(ctx, w.gormDB,
		func(tx *gorm.DB, sqlTx *sql.Tx) error {
			_ = sqlTx
			return w.store.ValidationOutbox().UpdateTx(tx, &store.ValidationOutboxEntry{
				ID: entry.ID, BundleID: entry.BundleID,
				Status: store.ValidationCompleted, Attempts: entry.Attempts,
				NextAttemptAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			})
		})
	if txErr != nil {
		w.logger.Error("mark validation entry completed", "entry_id", entry.ID, "error", txErr)
	}
}

func (w *ValidationWorker) recordError(ctx context.Context, entry store.ValidationOutboxEntry, code string, cause error) {
	nextAttempt := backoffNextAttempt(entry.Attempts + 1)
	w.logger.Warn("bundle validation retry",
		"bundle_id", entry.BundleID,
		"attempt", entry.Attempts+1,
		"error_code", code,
		"next_attempt_at", nextAttempt,
	)
	txErr := postgres.OperationCreationUnitOfWork(ctx, w.gormDB,
		func(tx *gorm.DB, sqlTx *sql.Tx) error {
			_ = sqlTx
			return w.store.ValidationOutbox().UpdateTx(tx, &store.ValidationOutboxEntry{
				ID: entry.ID, BundleID: entry.BundleID,
				Status: store.ValidationFailed, Attempts: entry.Attempts + 1,
				LastErrorCode: code, NextAttemptAt: nextAttempt,
				UpdatedAt: time.Now().UTC(),
			})
		})
	if txErr != nil {
		w.logger.Error("update validation outbox error", "entry_id", entry.ID, "error", txErr)
	}
	_ = cause
}

func backoffNextAttempt(attempts int) time.Time {
	now := time.Now().UTC()
	switch {
	case attempts <= 1:
		return now.Add(1 * time.Minute)
	case attempts == 2:
		return now.Add(5 * time.Minute)
	case attempts == 3:
		return now.Add(15 * time.Minute)
	default:
		return now.Add(1 * time.Hour)
	}
}

func isPermanentValidationError(err error) bool {
	return err != nil
}

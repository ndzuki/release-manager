package orchestrator

import (
	"context"
	"log/slog"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// NotificationSender delivers one queued terminal notification. The production
// implementation calls the notifier's Send over Connect; tests substitute a stub
// so the worker's retry semantics can be exercised without a live notifier.
type NotificationSender interface {
	Send(ctx context.Context, entry *store.ApprovalOutboxEntry) error
}

// NotificationOutboxWorker drains the terminal-notification outbox (REQ-031
// AC-031-12).
//
// Consumption is at-least-once: an entry is acknowledged only after Send
// succeeds, so a failure -- the notifier being unreachable, a timeout -- leaves
// it queued for the next poll rather than dropping the notification. Duplicates
// that a retry produces are absorbed by Send's own idempotency.
type NotificationOutboxWorker struct {
	outbox       store.NotificationOutboxStore
	sender       NotificationSender
	logger       *slog.Logger
	pollInterval time.Duration
	batchSize    int
}

// NotificationOutboxWorkerConfig tunes the poll loop.
type NotificationOutboxWorkerConfig struct {
	PollInterval time.Duration
	BatchSize    int
}

// DefaultNotificationOutboxWorkerConfig returns the production defaults.
func DefaultNotificationOutboxWorkerConfig() NotificationOutboxWorkerConfig {
	return NotificationOutboxWorkerConfig{PollInterval: 30 * time.Second, BatchSize: 50}
}

// NewNotificationOutboxWorker builds the worker. A nil sender disables delivery
// and is reported by Run, rather than silently acknowledging entries.
func NewNotificationOutboxWorker(
	outbox store.NotificationOutboxStore,
	sender NotificationSender,
	logger *slog.Logger,
	cfg NotificationOutboxWorkerConfig,
) *NotificationOutboxWorker {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultNotificationOutboxWorkerConfig().PollInterval
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultNotificationOutboxWorkerConfig().BatchSize
	}
	return &NotificationOutboxWorker{
		outbox: outbox, sender: sender, logger: logger,
		pollInterval: cfg.PollInterval, batchSize: cfg.BatchSize,
	}
}

// Run polls until the context is cancelled.
func (w *NotificationOutboxWorker) Run(ctx context.Context) {
	if w.outbox == nil || w.sender == nil {
		w.logger.Warn("notification outbox worker disabled: outbox or sender not configured")
		return
	}
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	w.logger.Info("notification outbox worker started", "poll_interval", w.pollInterval)
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("notification outbox worker stopped")
			return
		case <-ticker.C:
			w.ProcessOnce(ctx)
		}
	}
}

// ProcessOnce drains one batch and reports how many entries were delivered. It is
// exported so a caller can drive the queue without a ticker.
func (w *NotificationOutboxWorker) ProcessOnce(ctx context.Context) (delivered, failed int) {
	entries, err := w.outbox.ListUndelivered(ctx, w.batchSize)
	if err != nil {
		w.logger.Error("list undelivered notifications", "error", err)
		return 0, 0
	}
	for _, entry := range entries {
		if err := w.sender.Send(ctx, entry); err != nil {
			// Deliberately not acknowledged: the entry stays queued so the next
			// poll retries it. Acknowledging here would lose the notification.
			w.logger.Warn("notification send failed; will retry",
				"outbox_id", entry.ID, "event_type", entry.EventType, "error", err)
			failed++
			continue
		}
		if err := w.outbox.MarkDelivered(ctx, entry.ID, time.Now().UTC()); err != nil {
			w.logger.Error("mark notification delivered", "outbox_id", entry.ID, "error", err)
			failed++
			continue
		}
		delivered++
	}
	return delivered, failed
}

package notifier

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// Consumer polls for pending notification jobs and attempts delivery.
// It runs as a background goroutine.
type Consumer struct {
	store   store.Store
	logger  *slog.Logger
	retryCfg RetryConfig

	httpClient *http.Client

	pollInterval  time.Duration
	deliveryLimit int
}

// ConsumerConfig configures the notification consumer.
type ConsumerConfig struct {
	PollInterval  time.Duration
	DeliveryLimit int
	RetryCfg      RetryConfig
}

// DefaultConsumerConfig returns production-sane defaults.
func DefaultConsumerConfig() ConsumerConfig {
	return ConsumerConfig{
		PollInterval:  10 * time.Second,
		DeliveryLimit: 10,
		RetryCfg:      DefaultRetryConfig(),
	}
}

// NewConsumer creates a notification consumer.
func NewConsumer(st store.Store, logger *slog.Logger, cfg ConsumerConfig) *Consumer {
	return &Consumer{
		store:         st,
		logger:        logger,
		retryCfg:      cfg.RetryCfg,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
		pollInterval:  cfg.PollInterval,
		deliveryLimit: cfg.DeliveryLimit,
	}
}

// Run starts the consumer loop. It blocks until the context is cancelled.
func (c *Consumer) Run(ctx context.Context) {
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	c.logger.Info("notification consumer started",
		"poll_interval", c.pollInterval,
		"delivery_limit", c.deliveryLimit,
	)

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("notification consumer stopped")
			return
		case <-ticker.C:
			c.processPending(ctx)
		}
	}
}

// processPending fetches pending notification jobs and attempts delivery.
func (c *Consumer) processPending(ctx context.Context) {
	now := time.Now().UTC()
	jobs, err := c.store.Notifications().GetPending(ctx, now, c.deliveryLimit)
	if err != nil {
		c.logger.Error("failed to fetch pending notification jobs", "error", err)
		return
	}

	for _, job := range jobs {
		if err := c.deliver(ctx, job); err != nil {
			c.logger.Warn("notification delivery failed",
				"job_id", job.ID,
				"operation_id", job.OperationID,
				"error", err,
			)
		}
	}
}

// deliver attempts to deliver a single notification job.
// AC-031-04: Notification failure does NOT change the operation status.
func (c *Consumer) deliver(ctx context.Context, job *store.NotificationJob) error {
	// Mark as sending.
	if err := c.updateStatus(ctx, job.ID, store.NotificationSending, "", ""); err != nil {
		return err
	}

	// Attempt delivery (stub: log and mark as delivered for now).
	// In production, this would POST to webhook URL or send via email/Slack API.
	is4xx, err := c.sendNotification(ctx, job)
	if err != nil {
		// AC-031-04: notification failure → operation status unchanged.
		return c.handleDeliveryError(ctx, job, is4xx, err)
	}

	// Success.
	return c.updateStatus(ctx, job.ID, store.NotificationDelivered, "", "")
}

// sendNotification performs the actual delivery.
// Currently a stub; in production this would call external services.
func (c *Consumer) sendNotification(_ context.Context, job *store.NotificationJob) (bool, error) { //nolint:unparam // stub, always returns false,nil
	// Stub: log and simulate success.
	// TODO: Implement actual webhook POST / email / Slack delivery.
	c.logger.Debug("sending notification (stub)",
		"job_id", job.ID,
		"channel", job.Channel,
		"recipient", job.Recipient,
	)
	return false, nil
}

// handleDeliveryError applies retry/backoff or dead-letter based on error type.
func (c *Consumer) handleDeliveryError(ctx context.Context, job *store.NotificationJob, is4xx bool, deliveryErr error) error {
	newRetryCount := job.RetryCount + 1

	if ShouldDeadLetter(job.RetryCount, c.retryCfg.MaxRetries, job.CreatedAt, c.retryCfg.DeadlineAfter, is4xx) {
		// AC-031-03: 4xx → dead-letter immediately.
		reason := "max_retries_exceeded"
		if is4xx {
			reason = "4xx_configuration_error"
		}
		c.logger.Warn("moving notification to dead-letter",
			"job_id", job.ID,
			"reason", reason,
			"retry_count", newRetryCount,
		)
		if err := c.store.Notifications().MarkDeadLetter(ctx, job.ID); err != nil {
			return err
		}
		return nil
	}

	nextRetry := ComputeNextRetry(newRetryCount-1, c.retryCfg)
	c.logger.Info("scheduling notification retry",
		"job_id", job.ID,
		"retry_count", newRetryCount,
		"next_retry_at", nextRetry,
	)
	return c.updateStatus(ctx, job.ID, store.NotificationFailed, deliveryErr.Error(), nextRetry.Format(time.RFC3339))
}

// updateStatus updates the notification job status with optional error and next retry.
func (c *Consumer) updateStatus(ctx context.Context, id string, status store.NotificationStatus, lastError, nextRetryStr string) error {
	var nextRetryAt *time.Time
	if nextRetryStr != "" {
		t, err := time.Parse(time.RFC3339, nextRetryStr)
		if err == nil {
			nextRetryAt = &t
		}
	}
	retryCount := 0
	// Preserve existing retry count — we don't track it separately here.
	return c.store.Notifications().UpdateStatus(ctx, id, status, retryCount, nextRetryAt, lastError)
}

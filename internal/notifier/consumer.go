package notifier

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// Consumer polls for pending notification jobs and attempts delivery.
// It runs as a background goroutine managed by app.Run lifecycle.
type Consumer struct {
	store    store.NotificationStore
	logger   *slog.Logger
	retryCfg RetryConfig
	sender   Sender
	clock    Clock

	httpClient    *http.Client
	pollInterval  time.Duration
	deliveryLimit int

	// Deadline for how long a job can stay alive before dead-letter.
	jobDeadline time.Duration
	// Cleanup interval and age for dead-letter records.
	dlCleanupInterval time.Duration
	dlCleanupMaxAge   time.Duration
}

// ConsumerConfig configures the notification consumer.
type ConsumerConfig struct {
	PollInterval      time.Duration
	DeliveryLimit     int
	RetryCfg          RetryConfig
	JobDeadline       time.Duration
	DLCleanupInterval time.Duration
	DLCleanupMaxAge   time.Duration
}

// DefaultConsumerConfig returns production-sane defaults.
func DefaultConsumerConfig() ConsumerConfig {
	return ConsumerConfig{
		PollInterval:      10 * time.Second,
		DeliveryLimit:     10,
		RetryCfg:          DefaultRetryConfig(),
		JobDeadline:       24 * time.Hour,
		DLCleanupInterval: 1 * time.Hour,
		DLCleanupMaxAge:   30 * 24 * time.Hour,
	}
}

// NewConsumer creates a notification consumer.
func NewConsumer(
	st store.NotificationStore,
	sender Sender,
	logger *slog.Logger,
	cfg ConsumerConfig,
) *Consumer {
	return &Consumer{
		store:             st,
		sender:            sender,
		logger:            logger,
		retryCfg:          cfg.RetryCfg,
		clock:             &realClock{},
		httpClient:        &http.Client{Timeout: 30 * time.Second},
		pollInterval:      cfg.PollInterval,
		deliveryLimit:     cfg.DeliveryLimit,
		jobDeadline:       cfg.JobDeadline,
		dlCleanupInterval: cfg.DLCleanupInterval,
		dlCleanupMaxAge:   cfg.DLCleanupMaxAge,
	}
}

// SetClock injects a custom clock for testing.
func (c *Consumer) SetClock(clk Clock) { c.clock = clk }

// Run starts the consumer loop. It blocks until the context is cancelled.
// Owned by app.Run lifecycle — cancellation triggers clean shutdown.
func (c *Consumer) Run(ctx context.Context) {
	pollTicker := time.NewTicker(c.pollInterval)
	defer pollTicker.Stop()

	cleanupTicker := time.NewTicker(c.dlCleanupInterval)
	defer cleanupTicker.Stop()

	c.logger.Info("notification consumer started",
		"poll_interval", c.pollInterval,
		"delivery_limit", c.deliveryLimit,
		"job_deadline", c.jobDeadline,
		"dl_cleanup_max_age", c.dlCleanupMaxAge,
	)

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("notification consumer stopped")
			return
		case <-cleanupTicker.C:
			c.cleanupDeadLetters(ctx)
		case <-pollTicker.C:
			c.processPending(ctx)
		}
	}
}

// processPending claims and delivers due notification jobs.
func (c *Consumer) processPending(ctx context.Context) {
	now := c.clock.Now()
	for i := 0; i < c.deliveryLimit; i++ {
		job, err := c.store.ClaimNext(ctx, now)
		if err != nil {
			c.logger.Error("failed to claim next notification job", "error", err)
			return
		}
		if job == nil {
			return // no more jobs
		}

		if err := c.deliver(ctx, job); err != nil {
			c.logger.Warn("notification delivery failed",
				"job_id", job.ID,
				"operation_id", job.OperationID,
				"error", err,
			)
		}
	}
}

// cleanupDeadLetters removes dead-letter records older than the max age.
func (c *Consumer) cleanupDeadLetters(ctx context.Context) {
	cutoff := c.clock.Now().Add(-c.dlCleanupMaxAge)
	n, err := c.store.DeleteDeadLetterBefore(ctx, cutoff)
	if err != nil {
		c.logger.Error("dead letter cleanup failed", "error", err)
		return
	}
	if n > 0 {
		c.logger.Info("dead letter cleanup completed", "deleted", n)
	}
}

// deliver attempts to deliver a single notification job.
// AC-031-04: Notification failure does NOT change the operation status.
func (c *Consumer) deliver(ctx context.Context, job *store.NotificationJob) error {
	// Check job deadline — max alive 24h.
	if c.jobDeadline > 0 {
		deadline := job.CreatedAt.Add(c.jobDeadline)
		if c.clock.Now().After(deadline) {
			c.logger.Warn("job expired, moving to dead-letter",
				"job_id", job.ID,
				"age", c.clock.Now().Sub(job.CreatedAt),
			)
			return c.store.MarkDeadLetter(ctx, job.ID, "expired", "job exceeded max lifetime")
		}
	}

	// Attempt delivery.
	errCode, is4xx, err := c.sender.Send(ctx, job)
	if err != nil {
		// AC-031-04: notification failure → operation status unchanged.
		return c.handleDeliveryError(ctx, job, errCode, is4xx, err)
	}

	// Success.
	now := c.clock.Now()
	return c.store.UpdateStatus(ctx, job.ID, store.NotificationDelivered,
		job.Attempts, job.RetryCount, "", nil, "", &now)
}

// handleDeliveryError applies retry/backoff or dead-letter based on error type.
func (c *Consumer) handleDeliveryError(ctx context.Context, job *store.NotificationJob,
	errCode string, is4xx bool, deliveryErr error,
) error {
	newRetryCount := job.RetryCount + 1

	if ShouldDeadLetter(job.RetryCount, c.retryCfg.MaxRetries, job.CreatedAt, c.retryCfg.DeadlineAfter, is4xx) {
		reason := "max_retries_exceeded"
		if is4xx {
			reason = "4xx_configuration_error"
		}
		c.logger.Warn("moving notification to dead-letter",
			"job_id", job.ID,
			"reason", reason,
			"error_code", errCode,
			"retry_count", newRetryCount,
		)
		return c.store.MarkDeadLetter(ctx, job.ID, errCode, deliveryErr.Error())
	}

	nextRetry := ComputeNextRetry(newRetryCount-1, c.retryCfg)
	c.logger.Info("scheduling notification retry",
		"job_id", job.ID,
		"error_code", errCode,
		"retry_count", newRetryCount,
		"next_retry_at", nextRetry,
	)
	return c.store.UpdateStatus(ctx, job.ID, store.NotificationFailed,
		job.Attempts, newRetryCount, errCode, &nextRetry, deliveryErr.Error(), nil)
}

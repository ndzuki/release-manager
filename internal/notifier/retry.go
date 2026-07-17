package notifier

import (
	"math"
	"time"
)

// RetryConfig configures exponential backoff for notification retries.
type RetryConfig struct {
	// InitialBackoff is the starting delay between retries.
	InitialBackoff time.Duration
	// MaxBackoff is the maximum backoff delay (capped at this value).
	MaxBackoff time.Duration
	// Multiplier is the exponential backoff factor.
	Multiplier float64
	// MaxRetries is the maximum number of retry attempts before dead-letter.
	MaxRetries int
	// DeadlineAfter is how long a job can stay in retry state before dead-letter.
	// 0 means no deadline.
	DeadlineAfter time.Duration
}

// DefaultRetryConfig returns production-sane retry configuration.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		InitialBackoff: 5 * time.Second,
		MaxBackoff:     24 * time.Hour,
		Multiplier:     2.0,
		MaxRetries:     5,
		DeadlineAfter:  24 * time.Hour,
	}
}

// ComputeNextRetry calculates the next retry time using exponential backoff.
// AC-031-02: Exponential backoff up to MaxBackoff (24h).
func ComputeNextRetry(retryCount int, cfg RetryConfig) time.Time {
	delay := float64(cfg.InitialBackoff) * math.Pow(cfg.Multiplier, float64(retryCount))
	if delay > float64(cfg.MaxBackoff) {
		delay = float64(cfg.MaxBackoff)
	}
	return time.Now().UTC().Add(time.Duration(delay))
}

// ComputeNextRetryWithClock calculates the next retry time using the given clock.
func ComputeNextRetryWithClock(clk Clock, retryCount int, cfg RetryConfig) time.Time {
	delay := float64(cfg.InitialBackoff) * math.Pow(cfg.Multiplier, float64(retryCount))
	if delay > float64(cfg.MaxBackoff) {
		delay = float64(cfg.MaxBackoff)
	}
	return clk.Now().Add(time.Duration(delay))
}

// ShouldDeadLetter determines if a job should be moved to the dead-letter queue.
// AC-031-03: 4xx errors (non-retryable) → dead-letter immediately.
// AC-031-02: MaxRetries exceeded → dead-letter.
// Deadlines also trigger dead-letter.
func ShouldDeadLetter(retryCount, maxRetries int, createdAt time.Time, deadlineAfter time.Duration, is4xx bool) bool {
	if is4xx {
		return true
	}
	if maxRetries > 0 && retryCount >= maxRetries {
		return true
	}
	if deadlineAfter > 0 && time.Since(createdAt) > deadlineAfter {
		return true
	}
	return false
}

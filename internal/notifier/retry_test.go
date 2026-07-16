package notifier

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestComputeNextRetry_ExponentialBackoff(t *testing.T) {
	cfg := DefaultRetryConfig()
	cfg.InitialBackoff = 1 * time.Second
	cfg.MaxBackoff = 24 * time.Hour

	// First retry (retryCount=0): 1s * 2^0 = 1s
	next := ComputeNextRetry(0, cfg)
	assert.True(t, next.After(time.Now().Add(500*time.Millisecond)),
		"first retry should be ~1s after now")

	// Second retry (retryCount=1): 1s * 2^1 = 2s
	next2 := ComputeNextRetry(1, cfg)
	diff := next2.Sub(next)
	assert.True(t, diff >= 500*time.Millisecond,
		"second retry delay should be larger than first")
}

func TestComputeNextRetry_CappedAtMax(t *testing.T) {
	cfg := DefaultRetryConfig()
	cfg.MaxBackoff = 1 * time.Second

	// Even with high retry count, cap at MaxBackoff.
	next := ComputeNextRetry(10, cfg)
	diff := time.Until(next)
	assert.True(t, diff <= 1*time.Second+100*time.Millisecond,
		"retry should be capped at MaxBackoff")
}

func TestShouldDeadLetter_4xxImmediate(t *testing.T) {
	cfg := DefaultRetryConfig()
	cfg.MaxRetries = 5
	cfg.DeadlineAfter = 24 * time.Hour

	// AC-031-03: 4xx config errors → dead-letter immediately.
	assert.True(t, ShouldDeadLetter(0, cfg.MaxRetries, time.Now(), cfg.DeadlineAfter, true),
		"4xx should trigger immediate dead-letter")
}

func TestShouldDeadLetter_MaxRetriesExceeded(t *testing.T) {
	cfg := DefaultRetryConfig()
	cfg.MaxRetries = 3

	// AC-031-02: MaxRetries exceeded → dead-letter.
	assert.True(t, ShouldDeadLetter(3, cfg.MaxRetries, time.Now(), 0, false),
		"retry count at max should dead-letter")
	assert.True(t, ShouldDeadLetter(5, cfg.MaxRetries, time.Now(), 0, false),
		"retry count above max should dead-letter")
}

func TestShouldDeadLetter_DeadlineExpired(t *testing.T) {
	cfg := DefaultRetryConfig()
	cfg.MaxRetries = 5
	cfg.DeadlineAfter = 1 * time.Second

	createdAt := time.Now().Add(-2 * time.Second)
	assert.True(t, ShouldDeadLetter(0, cfg.MaxRetries, createdAt, cfg.DeadlineAfter, false),
		"expired deadline should dead-letter")
}

func TestShouldDeadLetter_StillRetryable(t *testing.T) {
	cfg := DefaultRetryConfig()
	cfg.MaxRetries = 5
	cfg.DeadlineAfter = 24 * time.Hour

	assert.False(t, ShouldDeadLetter(1, cfg.MaxRetries, time.Now(), cfg.DeadlineAfter, false),
		"not at max retries and not 4xx should be retryable")
}

package retry

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDo_Success(t *testing.T) {
	ctx := context.Background()
	cfg := Config{
		MaxAttempts: 3,
		InitialWait: 1 * time.Millisecond,
		MaxWait:     10 * time.Millisecond,
		Multiplier:  2.0,
	}

	callCount := 0
	err := Do(ctx, cfg, func() error {
		callCount++
		return nil
	})

	assert.NoError(t, err)
	assert.Equal(t, 1, callCount, "should succeed on first attempt")
}

func TestDo_RetryThenSuccess(t *testing.T) {
	ctx := context.Background()
	cfg := Config{
		MaxAttempts: 5,
		InitialWait: 1 * time.Millisecond,
		MaxWait:     10 * time.Millisecond,
		Multiplier:  2.0,
	}

	callCount := 0
	err := Do(ctx, cfg, func() error {
		callCount++
		if callCount < 3 {
			return errors.New("transient error")
		}
		return nil
	})

	assert.NoError(t, err)
	assert.Equal(t, 3, callCount, "should succeed on third attempt")
}

func TestDo_AllAttemptsFail(t *testing.T) {
	ctx := context.Background()
	cfg := Config{
		MaxAttempts: 3,
		InitialWait: 1 * time.Millisecond,
		MaxWait:     10 * time.Millisecond,
		Multiplier:  2.0,
	}

	expectedErr := errors.New("persistent error")
	callCount := 0
	err := Do(ctx, cfg, func() error {
		callCount++
		return expectedErr
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "all 3 attempts failed")
	assert.Contains(t, err.Error(), "persistent error")
	assert.Equal(t, 3, callCount)
}

func TestDo_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	cfg := Config{
		MaxAttempts: 10,
		InitialWait: 100 * time.Millisecond,
		MaxWait:     1 * time.Second,
		Multiplier:  2.0,
	}

	callCount := 0
	done := make(chan error, 1)
	go func() {
		done <- Do(ctx, cfg, func() error {
			callCount++
			return errors.New("fail")
		})
	}()

	// 第一次尝试开始等待后取消
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cancelled")
		assert.LessOrEqual(t, callCount, 2) // at most 2 calls before cancel takes effect
	case <-time.After(2 * time.Second):
		t.Fatal("test timed out")
	}
}

func TestDoWithRetryable(t *testing.T) {
	ctx := context.Background()
	cfg := Config{
		MaxAttempts: 5,
		InitialWait: 1 * time.Millisecond,
		MaxWait:     10 * time.Millisecond,
		Multiplier:  2.0,
	}

	retryableErr := errors.New("retryable")
	nonRetryableErr := errors.New("non-retryable")

	callCount := 0
	err := DoWithRetryable(ctx, cfg, func() error {
		callCount++
		if callCount == 1 {
			return retryableErr
		}
		return nonRetryableErr
	}, func(err error) bool {
		return err == retryableErr
	})

	assert.Error(t, err)
	assert.Equal(t, nonRetryableErr, err)
	assert.Equal(t, 2, callCount, "should stop after non-retryable error")
}

func TestCalculateWait(t *testing.T) {
	cfg := Config{
		InitialWait: 1 * time.Second,
		MaxWait:     10 * time.Second,
		Multiplier:  2.0,
	}

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 10 * time.Second}, // capped at max
		{10, 10 * time.Second},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("attempt_%d", tt.attempt), func(t *testing.T) {
			got := calculateWait(cfg, tt.attempt)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, DefaultMaxAttempts, cfg.MaxAttempts)
	assert.Equal(t, DefaultInitialWait, cfg.InitialWait)
	assert.Equal(t, DefaultMaxWait, cfg.MaxWait)
	assert.Equal(t, DefaultMultiplier, cfg.Multiplier)
}

// TestDoWithRetryable_AllRetryable 验证所有错误可重试时函数继续重试
// errors are retryable until max attempts are exhausted.
func TestDoWithRetryable_AllRetryable(t *testing.T) {
	ctx := context.Background()
	cfg := Config{
		MaxAttempts: 4,
		InitialWait: 1 * time.Millisecond,
		MaxWait:     10 * time.Millisecond,
		Multiplier:  2.0,
	}

	retryableErr := errors.New("always retryable")
	callCount := 0
	err := DoWithRetryable(ctx, cfg, func() error {
		callCount++
		return retryableErr
	}, func(err error) bool {
		return true // all errors are retryable
	})

	require.Error(t, err)
	assert.Equal(t, 4, callCount)
}

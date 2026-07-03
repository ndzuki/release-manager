// Package retry 提供指数退避重试工具。
//
// 支持可配置的重试次数、退避策略、可重试错误判断和上下文取消。
// 使用 time.NewTimer 而非 time.After 以避免 timer 泄漏。
package retry

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
)

// 默认重试参数。
const (
	DefaultMaxAttempts = 5
	DefaultInitialWait = 1 * time.Second
	DefaultMaxWait     = 60 * time.Second
	DefaultMultiplier  = 2.0
)

// Config 定义重试策略参数。
type Config struct {
	MaxAttempts int           // 最大尝试次数（含首次调用）
	InitialWait time.Duration // 首次重试前的等待时间
	MaxWait     time.Duration // 单次重试的最大等待时间
	Multiplier  float64       // 指数退避乘数
}

// DefaultConfig 返回带生产级默认值的 Config。
func DefaultConfig() Config {
	return Config{
		MaxAttempts: DefaultMaxAttempts,
		InitialWait: DefaultInitialWait,
		MaxWait:     DefaultMaxWait,
		Multiplier:  DefaultMultiplier,
	}
}

// Do 执行 fn 并重试。fn 返回 nil 即成功，否则重试直到 MaxAttempts 耗尽。
// ctx 取消时立即中止等待并返回错误。
func Do(ctx context.Context, cfg Config, fn func() error) error {
	return DoWithLog(ctx, cfg, logr.Discard(), "retry", fn)
}

// DoWithLog 执行 fn 并重试，同时输出结构化日志。
func DoWithLog(ctx context.Context, cfg Config, log logr.Logger, operation string, fn func() error) error {
	var lastErr error

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		// 每次尝试前检查 context 是否已取消
		select {
		case <-ctx.Done():
			return fmt.Errorf("retry cancelled: %w", ctx.Err())
		default:
		}

		err := fn()
		if err == nil {
			return nil
		}

		lastErr = err
		log.V(1).Info("operation failed, retrying",
			"operation", operation,
			"attempt", attempt,
			"max_attempts", cfg.MaxAttempts,
			"error", err,
		)

		if attempt == cfg.MaxAttempts {
			break
		}

		// 使用 time.NewTimer 代替 time.After，context 取消时可主动 Stop 防止泄漏
		wait := calculateWait(cfg, attempt)
		log.V(2).Info("waiting before retry",
			"operation", operation,
			"wait_duration", wait,
		)

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("retry cancelled during wait: %w", ctx.Err())
		case <-timer.C:
		}
	}

	return fmt.Errorf("all %d attempts failed, last error: %w", cfg.MaxAttempts, lastErr)
}

// calculateWait 计算第 attempt 次重试前的等待时间。
// 使用指数退避: wait = InitialWait * Multiplier^(attempt-1)，不超过 MaxWait。
func calculateWait(cfg Config, attempt int) time.Duration {
	wait := float64(cfg.InitialWait)
	for i := 1; i < attempt; i++ {
		wait *= cfg.Multiplier
	}
	if time.Duration(wait) > cfg.MaxWait {
		return cfg.MaxWait
	}
	return time.Duration(wait)
}

// IsRetryable 是一个函数类型，用于判断错误是否可重试。
type IsRetryable func(error) bool

// DoWithRetryable 执行 fn 并仅对可重试错误进行重试。
// 若 fn 返回不可重试的错误，则立即中止并返回该错误。
func DoWithRetryable(ctx context.Context, cfg Config, fn func() error, isRetryable IsRetryable) error {
	var lastErr error

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("retry cancelled: %w", ctx.Err())
		default:
		}

		err := fn()
		if err == nil {
			return nil
		}

		lastErr = err

		// 不可重试的错误立即返回
		if !isRetryable(err) {
			return err
		}

		if attempt == cfg.MaxAttempts {
			break
		}

		wait := calculateWait(cfg, attempt)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("retry cancelled during wait: %w", ctx.Err())
		case <-timer.C:
		}
	}

	return fmt.Errorf("all %d attempts failed, last error: %w", cfg.MaxAttempts, lastErr)
}

package app

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

// These tests deliberately avoid t.Parallel: startupLogger mutates the
// process-wide slog default (exactly what REQ-094 wires), so they serialize
// with each other and restore the previous default afterwards.

// TestApplyLogLevelChangesHandlerLevel is the REQ-094 AC2 behavior proof:
// different log_level values observably change what the process-wide logger
// emits (the shared handler level, not just a local copy).
func TestApplyLogLevelChangesHandlerLevel(t *testing.T) {
	ctx := context.Background()
	logger, level := startupLogger()
	t.Cleanup(func() { level.Set(slog.LevelDebug) })

	// startupLogger pre-wires Debug (the pre-TASK-094 hardcoded level).
	assert.True(t, logger.Enabled(ctx, slog.LevelDebug), "debug must be enabled before config is applied")

	applyLogLevel("warn", level, logger)
	assert.False(t, logger.Enabled(ctx, slog.LevelInfo), "info must be suppressed at log_level=warn")
	assert.False(t, logger.Enabled(ctx, slog.LevelDebug))
	assert.True(t, logger.Enabled(ctx, slog.LevelWarn))

	applyLogLevel("error", level, logger)
	assert.False(t, logger.Enabled(ctx, slog.LevelWarn), "warn must be suppressed at log_level=error")
	assert.True(t, logger.Enabled(ctx, slog.LevelError))

	applyLogLevel("debug", level, logger)
	assert.True(t, logger.Enabled(ctx, slog.LevelDebug), "debug must return at log_level=debug")
}

// TestApplyLogLevelInvalidKeepsDebug: a typo falls back to Debug and startup
// continues instead of exiting.
func TestApplyLogLevelInvalidKeepsDebug(t *testing.T) {
	ctx := context.Background()
	logger, level := startupLogger()
	t.Cleanup(func() { level.Set(slog.LevelDebug) })

	applyLogLevel("verbose", level, logger)
	assert.True(t, logger.Enabled(ctx, slog.LevelDebug))
	assert.True(t, logger.Enabled(ctx, slog.LevelInfo), "invalid value must not raise the level above the Debug default")
}

// TestStartupLoggerInstallsDefault covers the slog.SetDefault half of the
// wiring: most packages log through the default logger, so a local handler
// swap alone would not take effect.
func TestStartupLoggerInstallsDefault(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	logger, level := startupLogger()
	t.Cleanup(func() { level.Set(slog.LevelDebug) })

	applyLogLevel("error", level, logger)
	assert.False(t, slog.Default().Enabled(context.Background(), slog.LevelWarn),
		"slog.Default() must share the configured level")
}

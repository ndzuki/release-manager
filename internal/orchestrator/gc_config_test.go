package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultGcConfigValidates(t *testing.T) {
	require.NoError(t, DefaultGcConfig().Validate())
}

// AC-069-41: bundle_retention_days < 7 must fail startup validation.
func TestGcConfigValidateBundleRetentionMinimum(t *testing.T) {
	cfg := DefaultGcConfig()
	cfg.BundleRetentionDays = 6
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bundle_retention_days")
}

// AC-069-42: archive_grace_days < 1 must fail startup validation.
func TestGcConfigValidateArchiveGraceMinimum(t *testing.T) {
	cfg := DefaultGcConfig()
	cfg.ArchiveGraceDays = 0
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "archive_grace_days")
}

// AC-069-43: a non-zero gc.interval below 5m must fail startup validation.
func TestGcConfigValidateIntervalMinimum(t *testing.T) {
	cfg := DefaultGcConfig()
	cfg.Interval = 4 * time.Minute
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "interval")
}

// AC-069-34: an interval of exactly zero disables periodic GC and is valid.
func TestGcConfigValidateZeroIntervalAllowed(t *testing.T) {
	cfg := DefaultGcConfig()
	cfg.Interval = 0
	require.NoError(t, cfg.Validate())
}

// AC-069-50: cleanup_idempotency_retention_hours < 1 must fail startup validation.
func TestGcConfigValidateIdempotencyRetentionMinimum(t *testing.T) {
	cfg := DefaultGcConfig()
	cfg.CleanupIdempotencyRetentionHours = 0
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cleanup_idempotency_retention_hours")
}

// AC-069-44/19: when gc.interval exceeds half of the shortest of all five
// retention parameters, the gc_interval_large warning fires without rejecting
// the configuration.
func TestGcConfigDiagnosticsIntervalLargeUsesShortestOfFiveRetentions(t *testing.T) {
	cfg := DefaultGcConfig()
	// Shortest retention is orphan_preflight_retention_days (7d); an interval
	// larger than half of it (3.5d) must trigger the warning.
	cfg.Interval = 4 * 24 * time.Hour
	cfg.ArchiveGraceDays = 90
	cfg.CandidateArtifactRetentionDays = 90
	cfg.PreflightRetentionDays = 90
	warnings := cfg.Diagnostics()
	require.Len(t, warnings, 1)
	assert.Equal(t, "gc_interval_large", warnings[0].Code)

	// Interval below half of every retention: no warning.
	cfg.Interval = time.Hour
	require.Empty(t, cfg.Diagnostics())
}

// AC-069-53: gc_max_duration_minutes >= gc.interval emits the
// gc_duration_exceeds_interval warning without rejecting the configuration.
func TestGcConfigDiagnosticsDurationExceedsInterval(t *testing.T) {
	cfg := DefaultGcConfig()
	cfg.Interval = time.Hour
	cfg.GCMaxDurationMinutes = 60
	warnings := cfg.Diagnostics()
	require.Len(t, warnings, 1)
	assert.Equal(t, "gc_duration_exceeds_interval", warnings[0].Code)

	cfg.GCMaxDurationMinutes = 59
	require.Empty(t, cfg.Diagnostics())
}

// A disabled GC (interval=0) emits no cross-field diagnostics.
func TestGcConfigDiagnosticsDisabledIntervalSilent(t *testing.T) {
	cfg := DefaultGcConfig()
	cfg.Interval = 0
	cfg.GCMaxDurationMinutes = 5
	require.Empty(t, cfg.Diagnostics())
}

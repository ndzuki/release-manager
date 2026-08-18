package orchestrator

import (
	"fmt"
	"time"
)

// GcConfig contains artifact lifecycle garbage-collection configuration.
// Prepare-session cleanup remains part of the cleanup engine configuration.
type GcConfig struct {
	Interval                         time.Duration `mapstructure:"interval"`
	BundleRetentionDays              int           `mapstructure:"bundle_retention_days"`
	ArchiveGraceDays                 int           `mapstructure:"archive_grace_days"`
	CandidateArtifactRetentionDays   int           `mapstructure:"candidate_artifact_retention_days"`
	PreflightRetentionDays           int           `mapstructure:"preflight_retention_days"`
	OrphanPreflightRetentionDays     int           `mapstructure:"orphan_preflight_retention_days"`
	GCMaxDurationMinutes             int           `mapstructure:"gc_max_duration_minutes"`
	CleanupIdempotencyRetentionHours int           `mapstructure:"cleanup_idempotency_retention_hours"`
}

// GcDiagnostic is a non-fatal configuration diagnostic.
type GcDiagnostic struct {
	Code    string
	Message string
}

// DefaultGcConfig returns the REQ-069 defaults.
func DefaultGcConfig() GcConfig {
	return GcConfig{
		Interval:                         6 * time.Hour,
		BundleRetentionDays:              90,
		ArchiveGraceDays:                 30,
		CandidateArtifactRetentionDays:   30,
		PreflightRetentionDays:           90,
		OrphanPreflightRetentionDays:     7,
		GCMaxDurationMinutes:             55,
		CleanupIdempotencyRetentionHours: 24,
	}
}

// Validate enforces the minimum values. An interval of zero disables periodic GC.
func (c GcConfig) Validate() error {
	if c.Interval < 0 || (c.Interval > 0 && c.Interval < 5*time.Minute) {
		return fmt.Errorf("interval must be 0 or >= 5m, got %s", c.Interval)
	}
	checks := []struct {
		name  string
		value int
		min   int
	}{
		{"bundle_retention_days", c.BundleRetentionDays, 7},
		{"archive_grace_days", c.ArchiveGraceDays, 1},
		{"candidate_artifact_retention_days", c.CandidateArtifactRetentionDays, 1},
		{"preflight_retention_days", c.PreflightRetentionDays, 1},
		{"orphan_preflight_retention_days", c.OrphanPreflightRetentionDays, 1},
		{"gc_max_duration_minutes", c.GCMaxDurationMinutes, 5},
		{"cleanup_idempotency_retention_hours", c.CleanupIdempotencyRetentionHours, 1},
	}
	for _, check := range checks {
		if check.value < check.min {
			return fmt.Errorf("%s must be >= %d, got %d", check.name, check.min, check.value)
		}
	}
	return nil
}

// Diagnostics returns non-fatal cross-field warnings for a valid configuration.
func (c GcConfig) Diagnostics() []GcDiagnostic {
	if c.Interval <= 0 {
		return nil
	}
	minRetention := c.BundleRetentionDays
	for _, value := range []int{c.ArchiveGraceDays, c.CandidateArtifactRetentionDays, c.PreflightRetentionDays, c.OrphanPreflightRetentionDays} {
		if value < minRetention {
			minRetention = value
		}
	}
	warnings := make([]GcDiagnostic, 0, 2)
	if c.Interval > time.Duration(minRetention)*24*time.Hour/2 {
		warnings = append(warnings, GcDiagnostic{Code: "gc_interval_large", Message: "gc interval exceeds half of the shortest retention period"})
	}
	if c.GCMaxDurationMinutes >= int(c.Interval/time.Minute) {
		warnings = append(warnings, GcDiagnostic{Code: "gc_duration_exceeds_interval", Message: "gc max duration is greater than or equal to the gc interval"})
	}
	return warnings
}

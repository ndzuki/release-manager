package audit

import (
	"fmt"
	"time"
)

// ArchiveConfig holds configuration for audit event archiving.
// retention_days=0 disables archiving entirely.
type ArchiveConfig struct {
	RetentionDays     int           `mapstructure:"retention_days"`
	PollInterval      time.Duration `mapstructure:"poll_interval"`
	BatchSize         int           `mapstructure:"batch_size"`
	ArchiveDir        string        `mapstructure:"archive_dir"`
	Compression       string        `mapstructure:"compression"`
	ChecksumAlgorithm string        `mapstructure:"checksum_algorithm"`
}

// DefaultArchiveConfig returns production-sane defaults.
func DefaultArchiveConfig() ArchiveConfig {
	return ArchiveConfig{
		RetentionDays:     90,
		PollInterval:      6 * time.Hour,
		BatchSize:         1000,
		ArchiveDir:        "data/archives",
		Compression:       "gzip_jsonl",
		ChecksumAlgorithm: "sha256",
	}
}

// Validate checks the configuration and returns an error if invalid.
func (c ArchiveConfig) Validate() error {
	if c.RetentionDays < 0 {
		return fmt.Errorf("retention_days must be >= 0, got %d", c.RetentionDays)
	}
	if c.RetentionDays == 0 {
		return nil // archiving disabled
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("poll_interval must be > 0, got %v", c.PollInterval)
	}
	if c.BatchSize <= 0 {
		return fmt.Errorf("batch_size must be > 0, got %d", c.BatchSize)
	}
	if c.ArchiveDir == "" {
		return fmt.Errorf("archive_dir must not be empty")
	}
	if c.Compression != "" && c.Compression != "gzip_jsonl" {
		return fmt.Errorf("unsupported compression: %s (only gzip_jsonl is supported)", c.Compression)
	}
	if c.ChecksumAlgorithm != "" && c.ChecksumAlgorithm != "sha256" {
		return fmt.Errorf("unsupported checksum algorithm: %s (only sha256 is supported)", c.ChecksumAlgorithm)
	}
	return nil
}

// Enabled returns true if archiving is configured.
func (c ArchiveConfig) Enabled() bool {
	return c.RetentionDays > 0
}

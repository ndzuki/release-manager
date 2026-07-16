// Package config loads release-manager configuration.
package config

import (
	"fmt"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

// Config holds the service configuration.
type Config struct {
	HTTPPort int    `mapstructure:"http_port"`
	LogLevel string `mapstructure:"log_level"`
}

// Load reads the configuration from the given path.
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	return &cfg, nil
}

// ArchiveCfg mirrors audit.ArchiveConfig for mapstructure unmarshalling.
type ArchiveCfg struct {
	RetentionDays     int           `mapstructure:"retention_days"`
	PollInterval      time.Duration `mapstructure:"poll_interval"`
	BatchSize         int           `mapstructure:"batch_size"`
	ArchiveDir        string        `mapstructure:"archive_dir"`
	Compression       string        `mapstructure:"compression"`
	ChecksumAlgorithm string        `mapstructure:"checksum_algorithm"`
}

// ServiceConfig holds flat configuration for individual microservices.
type ServiceConfig struct {
	HTTPPort int        `mapstructure:"http_port"`
	LogLevel string     `mapstructure:"log_level"`
	Audit    AuditCfg   `mapstructure:"audit"`
}

// AuditCfg holds audit-related service configuration.
type AuditCfg struct {
	Archive ArchiveCfg `mapstructure:"archive"`
}

// LoadService reads a flat service configuration from the given path.
func LoadService(path string) (*ServiceConfig, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg ServiceConfig
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	return &cfg, nil
}

// WatchConfigFile starts watching the config file for changes.
// onChange is called (with debounce) when the file is written.
// Returns a stop function that should be called on shutdown.
func WatchConfigFile(path string, onChange func()) (func(), error) {
	v := viper.New()
	v.SetConfigFile(path)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("reading config for watch: %w", err)
	}

	v.WatchConfig()

	// Debounce: wait for a quiet period before firing.
	var debounceTimer *time.Timer
	const debounceInterval = 500 * time.Millisecond

	v.OnConfigChange(func(_ fsnotify.Event) {
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
		debounceTimer = time.AfterFunc(debounceInterval, onChange)
	})

	return func() {
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
	}, nil
}

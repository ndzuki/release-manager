// Package config loads release-manager configuration.
package config

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
)

// Config holds the service configuration.
type Config struct {
	HTTPPort             int               `mapstructure:"http_port"`
	LogLevel             string            `mapstructure:"log_level"`
	RuntimePullPreflight RuntimePullConfig `mapstructure:"runtime_pull_preflight"`
}

type RuntimePullConfig struct {
	Enabled        bool          `mapstructure:"enabled"`
	Namespace      string        `mapstructure:"namespace"`
	ServiceAccount string        `mapstructure:"service_account"`
	Timeout        time.Duration `mapstructure:"timeout"`
	CleanupPolicy  string        `mapstructure:"cleanup_policy"`
	ProbeCommand   []string      `mapstructure:"probe_command"`
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

// ServiceConfig holds flat configuration for individual microservices.
type ServiceConfig struct {
	HTTPPort int    `mapstructure:"http_port"`
	LogLevel string `mapstructure:"log_level"`
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

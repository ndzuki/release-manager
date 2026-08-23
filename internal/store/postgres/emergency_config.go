package postgres

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// Emergency configuration keys (REQ-079 D6/D16). Missing keys fail closed:
// Enabled defaults to false and OperationTimeout to the D16 default.
const (
	emergencyEnabledKey = "emergency.enabled"
	emergencyTimeoutKey = "emergency.operation_timeout"
)

type emergencyConfigStore struct{ gorm *DB }

func (s *emergencyConfigStore) GetEmergencyConfig(ctx context.Context) (store.EmergencyConfig, error) {
	cfg := store.EmergencyConfig{OperationTimeout: store.DefaultEmergencyOperationTimeout}
	rows, err := s.gorm.QueryContext(ctx, `
		SELECT key, value FROM app_settings
		WHERE key IN (?, ?)
	`, emergencyEnabledKey, emergencyTimeoutKey)
	if err != nil {
		return cfg, fmt.Errorf("query emergency config: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return cfg, fmt.Errorf("scan emergency config: %w", err)
		}
		switch key {
		case emergencyEnabledKey:
			cfg.Enabled = value == "true"
		case emergencyTimeoutKey:
			if parsed, parseErr := time.ParseDuration(value); parseErr == nil && parsed > 0 {
				cfg.OperationTimeout = parsed
			}
		}
	}
	if err := rows.Err(); err != nil {
		return cfg, fmt.Errorf("iterate emergency config: %w", err)
	}
	return cfg, nil
}

func (s *emergencyConfigStore) SetEmergencyConfig(ctx context.Context, config store.EmergencyConfig) error {
	if config.OperationTimeout <= 0 {
		config.OperationTimeout = store.DefaultEmergencyOperationTimeout
	}
	now := time.Now().UTC()
	entries := map[string]string{
		emergencyEnabledKey: strconv.FormatBool(config.Enabled),
		emergencyTimeoutKey: config.OperationTimeout.String(),
	}
	for key, value := range entries {
		if _, err := s.gorm.ExecContext(ctx, `
			INSERT INTO app_settings (key, value, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at
		`, key, value, now); err != nil {
			return fmt.Errorf("upsert emergency config key %s: %w", key, err)
		}
	}
	return nil
}

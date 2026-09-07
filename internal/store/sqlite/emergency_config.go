package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// Emergency configuration keys (REQ-079 D6/D16, REQ-087 D5=B). Missing keys
// fail closed: Enabled defaults to false, OperationTimeout to the D16 default
// and EffectObserveTimeout to the D5=B default (24h).
const (
	emergencyEnabledKey              = "emergency.enabled"
	emergencyTimeoutKey              = "emergency.operation_timeout"
	emergencyEffectObserveTimeoutKey = "emergency.effect_observe_timeout"
)

type emergencyConfigStore struct{ db *sql.DB }

func (s *emergencyConfigStore) GetEmergencyConfig(ctx context.Context) (store.EmergencyConfig, error) {
	cfg := store.EmergencyConfig{OperationTimeout: store.DefaultEmergencyOperationTimeout, EffectObserveTimeout: store.DefaultEmergencyEffectObserveTimeout}
	rows, err := s.db.QueryContext(ctx, `
		SELECT key, value FROM app_settings
		WHERE key IN (?, ?, ?)
	`, emergencyEnabledKey, emergencyTimeoutKey, emergencyEffectObserveTimeoutKey)
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
		case emergencyEffectObserveTimeoutKey:
			if parsed, parseErr := time.ParseDuration(value); parseErr == nil && parsed > 0 {
				cfg.EffectObserveTimeout = parsed
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
	if config.EffectObserveTimeout <= 0 {
		config.EffectObserveTimeout = store.DefaultEmergencyEffectObserveTimeout
	}
	now := time.Now().UTC()
	entries := map[string]string{
		emergencyEnabledKey:              strconv.FormatBool(config.Enabled),
		emergencyTimeoutKey:              config.OperationTimeout.String(),
		emergencyEffectObserveTimeoutKey: config.EffectObserveTimeout.String(),
	}
	for key, value := range entries {
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO app_settings (key, value, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
		`, key, value, now.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("upsert emergency config key %s: %w", key, err)
		}
	}
	return nil
}

package ca

import (
	"context"
	"fmt"

	"github.com/ndzuki/release-manager/internal/config"
)

// LoadConfigured assembles the CA from the service configuration (ADR-017):
// Vault when ca.vault_path is set, dev files otherwise. It returns the
// authority plus the renew window ratio used by RenewCertificate.
func LoadConfigured(ctx context.Context, cfg config.CAConfig) (*CA, float64, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, 0, err
	}
	var provider Provider
	if cfg.VaultPath != "" {
		vaultProvider, err := NewVaultProviderFromEnvironment(cfg.VaultPath)
		if err != nil {
			return nil, 0, err
		}
		provider = vaultProvider
	} else {
		provider = NewFileProvider(cfg.CertPath, cfg.KeyPath)
	}
	authority, err := LoadOrCreateWithProvider(ctx, provider, Config{TTL: cfg.CertTTL})
	if err != nil {
		return nil, 0, fmt.Errorf("load operator CA: %w", err)
	}
	return authority, cfg.RenewBeforeRatio, nil
}

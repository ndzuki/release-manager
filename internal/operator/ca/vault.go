package ca

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	vault "github.com/hashicorp/vault/api"
)

const (
	vaultCertificateKey = "certificate_pem"
	vaultPrivateKeyKey  = "private_key_pem"
)

// VaultProvider stores CA credentials in a Vault KV v2 secret (ADR-017:
// production backend). The reference has the form "<mount>/<secret-path>".
type VaultProvider struct {
	client *vault.Client
	mount  string
	path   string
}

// NewVaultProvider returns a Vault-backed CA credential provider.
func NewVaultProvider(client *vault.Client, reference string) (*VaultProvider, error) {
	if client == nil {
		return nil, fmt.Errorf("vault client is required")
	}
	mount, path, ok := strings.Cut(strings.Trim(reference, "/"), "/")
	if !ok || mount == "" || path == "" {
		return nil, fmt.Errorf("vault path must include mount and secret path")
	}
	return &VaultProvider{client: client, mount: mount, path: path}, nil
}

// NewVaultProviderFromEnvironment builds a provider from VAULT_ADDR / token
// environment defaults (vault.DefaultConfig).
func NewVaultProviderFromEnvironment(reference string) (*VaultProvider, error) {
	client, err := vault.NewClient(vault.DefaultConfig())
	if err != nil {
		return nil, fmt.Errorf("create Vault client: %w", err)
	}
	return NewVaultProvider(client, reference)
}

// Load reads the stored CA credentials. ErrCredentialsNotFound is returned
// when the secret does not exist yet.
func (p *VaultProvider) Load(ctx context.Context) (Credentials, error) {
	secret, err := p.client.KVv2(p.mount).Get(ctx, p.path)
	if err != nil {
		if errors.Is(err, vault.ErrSecretNotFound) {
			return Credentials{}, ErrCredentialsNotFound
		}
		return Credentials{}, fmt.Errorf("read Vault CA credentials: %w", err)
	}
	certPEM, ok := secret.Data[vaultCertificateKey].(string)
	if !ok || certPEM == "" {
		return Credentials{}, fmt.Errorf("vault secret %q missing %q", p.path, vaultCertificateKey)
	}
	keyB64, ok := secret.Data[vaultPrivateKeyKey].(string)
	if !ok || keyB64 == "" {
		return Credentials{}, fmt.Errorf("vault secret %q missing %q", p.path, vaultPrivateKeyKey)
	}
	keyPEM, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return Credentials{}, fmt.Errorf("decode Vault CA private key: %w", err)
	}
	return Credentials{CertificatePEM: []byte(certPEM), PrivateKeyPEM: keyPEM}, nil
}

// Create writes the CA credentials as a KV v2 secret (PEM strings; the key is
// base64-encoded to survive JSON string round-trips).
func (p *VaultProvider) Create(ctx context.Context, credentials Credentials) error {
	_, err := p.client.KVv2(p.mount).Put(ctx, p.path, map[string]any{
		vaultCertificateKey: string(credentials.CertificatePEM),
		vaultPrivateKeyKey:  base64.StdEncoding.EncodeToString(credentials.PrivateKeyPEM),
	})
	if err != nil {
		return fmt.Errorf("write Vault CA credentials: %w", err)
	}
	return nil
}

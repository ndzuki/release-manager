package ca

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrCredentialsNotFound reports that a Provider holds no CA credentials yet.
var ErrCredentialsNotFound = errors.New("CA credentials not found")

// Credentials carries the persistent CA key pair material.
type Credentials struct {
	CertificatePEM []byte
	PrivateKeyPEM  []byte
}

// Provider abstracts the CA credential source (ADR-017): Vault KV in prod,
// files in dev. Load returns ErrCredentialsNotFound when nothing is stored;
// Create persists freshly generated credentials exactly once (racing creators
// re-load instead of overwriting).
type Provider interface {
	Load(context.Context) (Credentials, error)
	Create(context.Context, Credentials) error
}

// FileProvider persists CA credentials as PEM files (dev backend, ADR-017):
// the key is written 0600, the certificate 0644, both atomically.
type FileProvider struct {
	certPath string
	keyPath  string
}

// NewFileProvider returns a file-backed CA credential provider.
func NewFileProvider(certPath, keyPath string) *FileProvider {
	return &FileProvider{certPath: certPath, keyPath: keyPath}
}

// Load reads both PEM files. Missing or unreadable files surface
// ErrCredentialsNotFound so LoadOrCreate generates a fresh CA.
func (p *FileProvider) Load(_ context.Context) (Credentials, error) {
	certPEM, certErr := os.ReadFile(p.certPath)
	keyPEM, keyErr := os.ReadFile(p.keyPath)
	if certErr != nil || keyErr != nil {
		return Credentials{}, ErrCredentialsNotFound
	}
	return Credentials{CertificatePEM: certPEM, PrivateKeyPEM: keyPEM}, nil
}

// Create persists both PEM files atomically (temp file + rename). The key
// file is created 0600; the certificate 0644.
func (p *FileProvider) Create(_ context.Context, credentials Credentials) error {
	if err := atomicWriteFile(p.keyPath, credentials.PrivateKeyPEM, 0o600); err != nil {
		return fmt.Errorf("write CA key: %w", err)
	}
	if err := atomicWriteFile(p.certPath, credentials.CertificatePEM, 0o644); err != nil {
		return fmt.Errorf("write CA certificate: %w", err)
	}
	return nil
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

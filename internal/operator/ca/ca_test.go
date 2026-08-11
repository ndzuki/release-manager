package ca_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/operator/ca"
)

// TestLoadOrCreateRoundTrip: first call generates and persists the CA, a
// second call must load the same identity (TASK-075 gateway restart keeps the
// agent trust chain stable — AC-075-01/02 precondition).
func TestLoadOrCreateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca.key")
	certPath := filepath.Join(dir, "ca.crt")

	first, err := ca.LoadOrCreate(ca.Config{TTL: 24 * time.Hour}, keyPath, certPath)
	require.NoError(t, err)

	// Files exist with the expected permissions (key 0600, cert 0644).
	keyInfo, err := os.Stat(keyPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), keyInfo.Mode().Perm())
	certInfo, err := os.Stat(certPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), certInfo.Mode().Perm())

	// Second load must reproduce the same CA certificate (identity stability).
	second, err := ca.LoadOrCreate(ca.Config{TTL: 24 * time.Hour}, keyPath, certPath)
	require.NoError(t, err)
	require.Equal(t, string(first.CertPEM()), string(second.CertPEM()))
}

// TestLoadOrCreatePartialMissing: a missing key file (e.g. deleted state) must
// regenerate the pair instead of loading a half CA.
func TestLoadOrCreatePartialMissing(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca.key")
	certPath := filepath.Join(dir, "ca.crt")

	first, err := ca.LoadOrCreate(ca.Config{}, keyPath, certPath)
	require.NoError(t, err)
	require.NoError(t, os.Remove(keyPath))

	second, err := ca.LoadOrCreate(ca.Config{}, keyPath, certPath)
	require.NoError(t, err)
	require.NotEqual(t, string(first.CertPEM()), string(second.CertPEM()),
		"regenerated CA must differ from the deleted one")
}

// TestLoadOrCreateEmptyPaths: missing paths are a configuration error, not a
// silent generation in an undefined location.
func TestLoadOrCreateEmptyPaths(t *testing.T) {
	_, err := ca.LoadOrCreate(ca.Config{}, "", "")
	require.Error(t, err)
}

// TestLoadRejectsCorruptFiles: a corrupt key or certificate must fail loudly
// instead of silently replacing the persisted CA.
func TestLoadRejectsCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca.key")
	certPath := filepath.Join(dir, "ca.crt")
	_, err := ca.LoadOrCreate(ca.Config{}, keyPath, certPath)
	require.NoError(t, err)

	// Corrupt key: Load must reject it.
	keyPEM, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	goodCert, err := os.ReadFile(certPath)
	require.NoError(t, err)
	_, err = ca.Load([]byte("not a pem"), goodCert, ca.Config{})
	require.Error(t, err)

	// Corrupt certificate: Load must reject it.
	_, err = ca.Load(keyPEM, []byte("garbage"), ca.Config{})
	require.Error(t, err)
}

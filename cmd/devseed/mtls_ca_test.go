package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	operatorca "github.com/ndzuki/release-manager/internal/operator/ca"
)

// TestEnsureDevMTLSCA_GenerateReuseRegenerate covers AC-065-36 at the format
// contract level: the helper generates a CA pair the operator gateway's own
// loader accepts (PKCS#8 Ed25519 + self-signed CA cert), reuses a parseable
// pair untouched, and regenerates a corrupt pair.
func TestEnsureDevMTLSCA_GenerateReuseRegenerate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dev-ca")

	require.NoError(t, ensureDevMTLSCA(dir))
	keyPath := filepath.Join(dir, "ca.key")
	certPath := filepath.Join(dir, "ca.crt")

	// The operator gateway's loader accepts the generated pair.
	keyPEM, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	certPEM, err := os.ReadFile(certPath)
	require.NoError(t, err)
	loaded, err := operatorca.Load(keyPEM, certPEM, operatorca.Config{})
	require.NoError(t, err)
	require.NotNil(t, loaded)

	// 0600 files + 0700 dir (REQ-065 安全边界: 全部敏感运行时文件 0600).
	for _, path := range []string{keyPath, certPath} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "expected 0600 on %s", path)
	}
	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm(), "expected 0700 on the CA dir")

	// Reuse: a parseable pair is left untouched.
	require.NoError(t, ensureDevMTLSCA(dir))
	keyPEM2, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	require.True(t, bytes.Equal(keyPEM, keyPEM2), "parseable CA must be reused, not regenerated")

	// Corrupt certificate → regenerate (REQ-065: 缺失/不可解析时生成写入).
	require.NoError(t, os.WriteFile(certPath, []byte("garbage"), 0o600))
	require.NoError(t, ensureDevMTLSCA(dir))
	keyPEM3, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	require.False(t, bytes.Equal(keyPEM, keyPEM3), "corrupt pair must be regenerated")
	certPEM3, err := os.ReadFile(certPath)
	require.NoError(t, err)
	_, err = operatorca.Load(keyPEM3, certPEM3, operatorca.Config{})
	require.NoError(t, err, "regenerated pair must load in the operator gateway")
}

// TestEnsureDevMTLSCA_RequiresDir covers the misuse path: an empty target
// directory fails fast instead of writing into the working directory.
func TestEnsureDevMTLSCA_RequiresDir(t *testing.T) {
	require.Error(t, ensureDevMTLSCA(""))
}

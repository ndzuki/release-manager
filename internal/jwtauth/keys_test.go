package jwtauth

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// REQ-065 AC-065-01 / D1=A: these parsers are the only place JWT key material
// enters a process, so they fail closed — a wrong key type or a private key
// handed to a verifier is a startup error, never a silently weaker scheme.

func privateKeyPEM(t *testing.T, privateKey ed25519.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func publicKeyPEM(t *testing.T, publicKey ed25519.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func testJWTKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return publicKey, privateKey
}

// ── key material parsing (fail closed at startup) ──────────────────────────

func TestParseEd25519PrivateKeyPEM(t *testing.T) {
	_, privateKey := testJWTKeyPair(t)
	validPEM := privateKeyPEM(t, privateKey)
	publicKey, _ := testJWTKeyPair(t)

	t.Run("accepts a PKCS#8 Ed25519 key", func(t *testing.T) {
		parsed, err := ParseEd25519PrivateKeyPEM(validPEM)
		require.NoError(t, err)
		assert.True(t, parsed.Equal(privateKey))
	})

	// Regression: dev-up used to mint `head -c 64 /dev/urandom | base64` as the
	// HS256 secret. That material must no longer configure a signing manager —
	// otherwise the AC would pass on paper while the process still ran on a
	// symmetric key.
	t.Run("rejects the legacy symmetric key format", func(t *testing.T) {
		legacy := make([]byte, 64)
		_, err := rand.Read(legacy)
		require.NoError(t, err)
		_, err = ParseEd25519PrivateKeyPEM(base64.StdEncoding.EncodeToString(legacy))
		require.Error(t, err)
	})

	t.Run("rejects a public key", func(t *testing.T) {
		_, err := ParseEd25519PrivateKeyPEM(publicKeyPEM(t, publicKey))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "public key")
	})

	t.Run("rejects a non-Ed25519 key", func(t *testing.T) {
		ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		der, err := x509.MarshalPKCS8PrivateKey(ecdsaKey)
		require.NoError(t, err)
		_, err = ParseEd25519PrivateKeyPEM(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be Ed25519")
	})

	for _, tc := range []struct{ name, value string }{
		{name: "empty", value: ""},
		{name: "garbage", value: "not a pem document"},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			_, err := ParseEd25519PrivateKeyPEM(tc.value)
			require.Error(t, err)
		})
	}
}

func TestParseEd25519PublicKeyPEM(t *testing.T) {
	publicKey, privateKey := testJWTKeyPair(t)
	validPEM := publicKeyPEM(t, publicKey)

	t.Run("accepts a PKIX Ed25519 key", func(t *testing.T) {
		parsed, err := ParseEd25519PublicKeyPEM(validPEM)
		require.NoError(t, err)
		assert.True(t, parsed.Equal(publicKey))
	})

	// A verifier handed signing material would silently regain the ability to
	// mint tokens, which is exactly what the migration removes.
	t.Run("rejects a private key", func(t *testing.T) {
		_, err := ParseEd25519PublicKeyPEM(privateKeyPEM(t, privateKey))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "private key")
	})

	t.Run("rejects a non-Ed25519 key", func(t *testing.T) {
		ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		der, err := x509.MarshalPKIXPublicKey(&ecdsaKey.PublicKey)
		require.NoError(t, err)
		_, err = ParseEd25519PublicKeyPEM(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be Ed25519")
	})

	for _, tc := range []struct{ name, value string }{
		{name: "empty", value: ""},
		{name: "garbage", value: "not a pem document"},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			_, err := ParseEd25519PublicKeyPEM(tc.value)
			require.Error(t, err)
		})
	}
}

func TestParseEd25519PrivateKeyPEMRejectsTrailingGarbage(t *testing.T) {
	_, privateKey := testJWTKeyPair(t)
	_, err := ParseEd25519PrivateKeyPEM(privateKeyPEM(t, privateKey) + "trailing")
	require.Error(t, err, "a Secret with trailing bytes must not half-configure a service")
}

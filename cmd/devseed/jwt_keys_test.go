package main

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/jwtauth"
)

// REQ-065 AC-065-01 / D1=A: the dev environment must ship a real Ed25519 key
// pair, split into the two halves the Services mount separately. These tests
// pin the helper's contract (generate / reuse / derive / repair), because the
// dev-up smoke cannot run without a cluster.

func TestEnsureDevJWTKeysGeneratesThenReuses(t *testing.T) {
	dir := t.TempDir()
	privatePath, publicPath, err := ensureDevJWTKeys(dir, "")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, jwtPrivateKeyFileName), privatePath)
	assert.Equal(t, filepath.Join(dir, jwtPublicKeyFileName), publicPath)

	privatePEM, err := os.ReadFile(privatePath)
	require.NoError(t, err)
	publicPEM, err := os.ReadFile(publicPath)
	require.NoError(t, err)

	privateKey, err := jwtauth.ParseEd25519PrivateKeyPEM(string(privatePEM))
	require.NoError(t, err)
	publicKey, err := jwtauth.ParseEd25519PublicKeyPEM(string(publicPEM))
	require.NoError(t, err)
	assert.True(t, publicKey.Equal(privateKey.Public()), "the two files must be one pair")

	// The private half must not be readable by others; the public half is not
	// secret but stays inside the 0700 directory.
	privateInfo, err := os.Stat(privatePath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), privateInfo.Mode().Perm())
	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	// Reuse: a second call must not rotate the key (rotation is delete + rerun).
	_, _, err = ensureDevJWTKeys(dir, "")
	require.NoError(t, err)
	reusedPrivate, err := os.ReadFile(privatePath)
	require.NoError(t, err)
	assert.Equal(t, privatePEM, reusedPrivate, "an existing valid pair must be reused, not regenerated")
}

// The ci profile injects only the private half; the public half must be derived
// from it so the two mounted Secrets can never disagree.
func TestEnsureDevJWTKeysDerivesPublicHalfFromExplicitPrivateKey(t *testing.T) {
	sourceDir := t.TempDir()
	sourcePrivatePath, sourcePublicPath, err := ensureDevJWTKeys(sourceDir, "")
	require.NoError(t, err)
	sourcePrivate, err := os.ReadFile(sourcePrivatePath)
	require.NoError(t, err)
	sourcePublic, err := os.ReadFile(sourcePublicPath)
	require.NoError(t, err)

	dir := t.TempDir()
	privatePath, publicPath, err := ensureDevJWTKeys(dir, string(sourcePrivate))
	require.NoError(t, err)

	writtenPrivate, err := os.ReadFile(privatePath)
	require.NoError(t, err)
	assert.Equal(t, sourcePrivate, writtenPrivate)
	writtenPublic, err := os.ReadFile(publicPath)
	require.NoError(t, err)
	assert.Equal(t, sourcePublic, writtenPublic, "the derived public half must match the private key")
}

// A mismatched pair (hand-edited, or a leftover from a rotated key) must be
// repaired rather than served: the verifiers would otherwise reject every token
// the issuer signs.
func TestEnsureDevJWTKeysRegeneratesMismatchedPair(t *testing.T) {
	dir := t.TempDir()
	privatePath, publicPath, err := ensureDevJWTKeys(dir, "")
	require.NoError(t, err)

	// Replace the public half with an unrelated key's public half.
	otherDir := t.TempDir()
	_, otherPublicPath, err := ensureDevJWTKeys(otherDir, "")
	require.NoError(t, err)
	otherPublic, err := os.ReadFile(otherPublicPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(publicPath, otherPublic, 0o644))

	_, _, err = ensureDevJWTKeys(dir, "")
	require.NoError(t, err)

	privateKey, err := jwtauth.ParseEd25519PrivateKeyPEM(string(mustRead(t, privatePath)))
	require.NoError(t, err)
	publicKey, err := jwtauth.ParseEd25519PublicKeyPEM(string(mustRead(t, publicPath)))
	require.NoError(t, err)
	assert.True(t, publicKey.Equal(privateKey.Public()), "a mismatched pair must be regenerated as a pair")
}

// The legacy symmetric dev secret must not silently configure the new scheme.
func TestEnsureDevJWTKeysRejectsNonEd25519Input(t *testing.T) {
	legacy := make([]byte, 64)
	_, err := rand.Read(legacy)
	require.NoError(t, err)

	_, _, err = ensureDevJWTKeys(t.TempDir(), base64.StdEncoding.EncodeToString(legacy))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DEV_JWT_PRIVATE_KEY")
}

// The public file must contain only the public half: writing the private half
// there would hand the signing key to every verifier, which is the whole
// regression the split exists to prevent.
func TestEnsureDevJWTKeysPublicFileCarriesNoPrivateMaterial(t *testing.T) {
	dir := t.TempDir()
	_, publicPath, err := ensureDevJWTKeys(dir, "")
	require.NoError(t, err)

	publicPEM := mustRead(t, publicPath)
	_, err = jwtauth.ParseEd25519PrivateKeyPEM(string(publicPEM))
	require.Error(t, err, "the public file must not parse as a private key")
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

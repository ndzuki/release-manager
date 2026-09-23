package jwtauth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/auth"
)

// REQ-065 AC-065-01 / D1=A: cmd/api verifies management-plane tokens it did not
// issue, so it must accept the issuer's EdDSA tokens and reject everything an
// attacker holding only the public key could produce.

// TestManager_VerifiesTokenMintedByAuthIssuer pins the real cross-service path:
// internal/auth signs with the private key, this package verifies with the
// public key alone.
func TestManager_VerifiesTokenMintedByAuthIssuer(t *testing.T) {
	publicKey, privateKey := testJWTKeyPair(t)
	issuer := auth.NewJWTManager(privateKey, time.Hour, time.Hour)

	token, _, err := issuer.GenerateAccessToken("user-1", "org-1", []string{"release_admin"})
	require.NoError(t, err)

	claims, err := New(publicKey, time.Hour).ValidateAccessToken(token)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.UserID)
	assert.Equal(t, "org-1", claims.OrgID)
	assert.Equal(t, []string{"release_admin"}, claims.Roles)
}

// TestManager_RejectsAlgorithmConfusion: the verifier's key is public, so an
// attacker can sign an HS256 token using those bytes as the HMAC secret. A
// verifier that picked its key from the token header would accept a token it
// could have forged itself; pinning the method by type must reject it.
func TestManager_RejectsAlgorithmConfusion(t *testing.T) {
	publicKey, _ := testJWTKeyPair(t)

	forged, err := jwt.NewWithClaims(jwt.SigningMethodHS256, &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "attacker",
			IssuedAt:  jwt.NewNumericDate(time.Now().UTC()),
			ExpiresAt: jwt.NewNumericDate(time.Now().UTC().Add(time.Hour)),
		},
		UserID: "attacker",
		OrgID:  "org-victim",
		Roles:  []string{"platform_admin"},
	}).SignedString([]byte(publicKey))
	require.NoError(t, err, "the attacker can mint this token; the verifier must refuse it")

	claims, err := New(publicKey, time.Hour).ValidateAccessToken(forged)
	require.Error(t, err, "an HS256 token signed with the public key bytes must be rejected")
	assert.Nil(t, claims)
	assert.Contains(t, err.Error(), "unexpected signing method")
}

func TestManager_RejectsTamperedPayload(t *testing.T) {
	publicKey, privateKey := testJWTKeyPair(t)
	token, _, err := auth.NewJWTManager(privateKey, time.Hour, time.Hour).
		GenerateAccessToken("user-1", "org-1", []string{"viewer"})
	require.NoError(t, err)

	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(payload, &decoded))
	decoded["uid"] = "attacker"
	modified, err := json.Marshal(decoded)
	require.NoError(t, err)
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(modified) + "." + parts[2]

	_, err = New(publicKey, time.Hour).ValidateAccessToken(tampered)
	require.Error(t, err, "a token whose payload was modified must fail signature verification")
}

func TestManager_RejectsTokenFromAnotherKey(t *testing.T) {
	publicKey, _ := testJWTKeyPair(t)
	_, otherPrivateKey := testJWTKeyPair(t)
	foreign, _, err := auth.NewJWTManager(otherPrivateKey, time.Hour, time.Hour).
		GenerateAccessToken("user-1", "org-1", []string{"viewer"})
	require.NoError(t, err)

	_, err = New(publicKey, time.Hour).ValidateAccessToken(foreign)
	require.Error(t, err, "a token signed by an unrelated Ed25519 key must be rejected")
}

func TestManager_RejectsExpiredToken(t *testing.T) {
	publicKey, privateKey := testJWTKeyPair(t)
	token, _, err := auth.NewJWTManager(privateKey, -time.Hour, time.Hour).
		GenerateAccessToken("user-1", "org-1", nil)
	require.NoError(t, err)

	_, err = New(publicKey, time.Hour).ValidateAccessToken(token)
	require.Error(t, err, "an expired token must be rejected")
}

// A manager built without key material must refuse every token rather than
// accepting one because it has nothing to check against.
func TestManagerWithoutKeyFailsClosed(t *testing.T) {
	_, privateKey := testJWTKeyPair(t)
	token, _, err := auth.NewJWTManager(privateKey, time.Hour, time.Hour).
		GenerateAccessToken("user-1", "org-1", nil)
	require.NoError(t, err)

	_, err = New(nil, time.Hour).ValidateAccessToken(token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no Ed25519 public key configured")
}

// The manager is verify-only: signing lives in internal/auth, which is the only
// process holding the private key.
func TestManagerHasNoSigningSurface(t *testing.T) {
	var manager any = New(ed25519.PublicKey(make([]byte, ed25519.PublicKeySize)), time.Hour)
	_, signs := manager.(interface {
		GenerateAccessToken(string, string, []string) (string, time.Time, error)
	})
	assert.False(t, signs, "a verify-only manager must not expose a signing method")
}

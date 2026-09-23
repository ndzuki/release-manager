package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// REQ-065 AC-065-01 / D1=A: management-plane access tokens are EdDSA (Ed25519).
// These tests pin the two properties that make the asymmetric scheme worth the
// migration: the issuer signs with the private key, and a verifier holding only
// the public key can neither sign nor be fooled into accepting a token it could
// have forged itself.

func TestJWTManager_SignsEdDSAAndRoundTrips(t *testing.T) {
	publicKey, privateKey := testJWTKeyPair(t)
	manager := NewJWTManager(privateKey, time.Hour, time.Hour)

	token, expiresAt, err := manager.GenerateAccessToken("user-1", "org-1", []string{"viewer"})
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(time.Hour), expiresAt, time.Minute)

	// The header is the observable contract: an HS256 token would fail the AC.
	parsed, _, err := jwt.NewParser().ParseUnverified(token, &Claims{})
	require.NoError(t, err)
	assert.Equal(t, "EdDSA", parsed.Header["alg"])

	claims, err := manager.ValidateAccessToken(token)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.UserID)
	assert.Equal(t, "org-1", claims.OrgID)
	assert.Equal(t, []string{"viewer"}, claims.Roles)

	// A verifier built from the public half alone accepts the issuer's token.
	verifier := NewJWTVerifier(publicKey, time.Hour, time.Hour)
	verified, err := verifier.ValidateAccessToken(token)
	require.NoError(t, err)
	assert.Equal(t, "user-1", verified.UserID)
}

// TestJWTManager_RejectsAlgorithmConfusion is the negative control the migration
// exists for. The verifier's key is public, so an attacker knows it: they sign an
// HS256 token using the public key bytes as the HMAC secret and label it
// `alg: HS256`. A verifier that chose its key from the token header would accept
// it. Pinning the signing method by type must reject it.
func TestJWTManager_RejectsAlgorithmConfusion(t *testing.T) {
	publicKey, privateKey := testJWTKeyPair(t)
	manager := NewJWTManager(privateKey, time.Hour, time.Hour)

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

	claims, err := manager.ValidateAccessToken(forged)
	require.Error(t, err, "an HS256 token signed with the public key bytes must be rejected")
	assert.Nil(t, claims)
	assert.Contains(t, err.Error(), "unexpected signing method")

	// The same forged token must also fail at a verify-only manager.
	verifier := NewJWTVerifier(publicKey, time.Hour, time.Hour)
	_, err = verifier.ValidateAccessToken(forged)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected signing method")
}

// "none" is the other classic confusion: an unsigned token claiming alg=none.
func TestJWTManager_RejectsUnsignedToken(t *testing.T) {
	_, privateKey := testJWTKeyPair(t)
	manager := NewJWTManager(privateKey, time.Hour, time.Hour)

	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "attacker",
			ExpiresAt: jwt.NewNumericDate(time.Now().UTC().Add(time.Hour)),
		},
		UserID: "attacker",
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	_, err = manager.ValidateAccessToken(unsigned)
	require.Error(t, err, "an unsigned token must be rejected")
}

func TestJWTManager_RejectsTamperedPayload(t *testing.T) {
	_, privateKey := testJWTKeyPair(t)
	manager := NewJWTManager(privateKey, time.Hour, time.Hour)

	token, _, err := manager.GenerateAccessToken("user-1", "org-1", []string{"viewer"})
	require.NoError(t, err)

	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(payload, &decoded))
	decoded["uid"] = "attacker"
	decoded["roles"] = []string{"platform_admin"}
	modified, err := json.Marshal(decoded)
	require.NoError(t, err)
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(modified) + "." + parts[2]

	_, err = manager.ValidateAccessToken(tampered)
	require.Error(t, err, "a token whose payload was modified must fail signature verification")
}

func TestJWTManager_RejectsTokenSignedByAnotherKey(t *testing.T) {
	_, privateKey := testJWTKeyPair(t)
	otherPrivateKey := testJWTPrivateKeyValue(t)
	manager := NewJWTManager(privateKey, time.Hour, time.Hour)

	foreign, _, err := NewJWTManager(otherPrivateKey, time.Hour, time.Hour).
		GenerateAccessToken("user-1", "org-1", []string{"viewer"})
	require.NoError(t, err)

	_, err = manager.ValidateAccessToken(foreign)
	require.Error(t, err, "a token signed by an unrelated Ed25519 key must be rejected")
}

func TestJWTManager_RejectsExpiredToken(t *testing.T) {
	_, privateKey := testJWTKeyPair(t)
	// A negative access TTL mints an already-expired token; the 5s leeway does
	// not rescue it.
	manager := NewJWTManager(privateKey, -time.Hour, time.Hour)

	token, _, err := manager.GenerateAccessToken("user-1", "org-1", nil)
	require.NoError(t, err)

	_, err = manager.ValidateAccessToken(token)
	require.Error(t, err, "an expired token must be rejected")
}

// TestJWTVerifier_CannotSignButVerifies is the security property itself: a
// verifier that only ever sees the public key must not be able to mint, and must
// not silently fall back to another algorithm to "make it work".
func TestJWTVerifier_CannotSignButVerifies(t *testing.T) {
	publicKey, privateKey := testJWTKeyPair(t)
	verifier := NewJWTVerifier(publicKey, time.Hour, time.Hour)

	_, _, err := verifier.GenerateAccessToken("user-1", "org-1", []string{"platform_admin"})
	require.Error(t, err, "a verify-only manager must never sign")
	assert.Contains(t, err.Error(), "verify-only")

	token, _, err := NewJWTManager(privateKey, time.Hour, time.Hour).
		GenerateAccessToken("user-1", "org-1", []string{"viewer"})
	require.NoError(t, err)
	claims, err := verifier.ValidateAccessToken(token)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.UserID)
}

func TestNewJWTVerifierWithoutKeyFailsClosed(t *testing.T) {
	verifier := NewJWTVerifier(nil, time.Hour, time.Hour)

	// A token minted elsewhere is refused because no key is configured.
	_, privateKey := testJWTKeyPair(t)
	token, _, err := NewJWTManager(privateKey, time.Hour, time.Hour).GenerateAccessToken("u", "o", nil)
	require.NoError(t, err)

	_, err = verifier.ValidateAccessToken(token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no Ed25519 public key configured")
}

// ── helpers ────────────────────────────────────────────────────────────────

func testJWTKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return publicKey, privateKey
}

func testJWTPrivateKeyValue(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, privateKey := testJWTKeyPair(t)
	return privateKey
}

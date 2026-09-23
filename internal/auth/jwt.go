package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWTManager handles access and refresh token signing and validation.
//
// Access tokens are EdDSA (Ed25519) JWTs (REQ-065 AC-065-01, decision D1=A):
// the issuer (cmd/auth) holds the private key and every verifier holds only the
// public key. Do not reintroduce an HMAC scheme — a symmetric secret has to be
// copied to every verifier, which lets a compromised verifier mint arbitrary
// identities and forces the secret into deployments that never verify anything.
//
// A manager built by NewJWTVerifier has no private key: GenerateAccessToken
// fails closed on it rather than falling back to a weaker algorithm.
type JWTManager struct {
	privateKey   ed25519.PrivateKey // wrong length on a verify-only manager
	publicKey    ed25519.PublicKey
	accessTTL    time.Duration
	refreshTTL   time.Duration
	refreshBytes int
}

// NewJWTManager creates a signing manager from an Ed25519 private key. The
// public half is derived from the private key, so the issuer also verifies the
// tokens it mints.
func NewJWTManager(privateKey ed25519.PrivateKey, accessTTL, refreshTTL time.Duration) *JWTManager {
	manager := &JWTManager{
		accessTTL:    accessTTL,
		refreshTTL:   refreshTTL,
		refreshBytes: 32,
	}
	if len(privateKey) == ed25519.PrivateKeySize {
		manager.privateKey = privateKey
		if publicKey, ok := privateKey.Public().(ed25519.PublicKey); ok {
			manager.publicKey = publicKey
		}
	}
	return manager
}

// NewJWTVerifier creates a verify-only manager from an Ed25519 public key. Use
// it wherever a service validates tokens it did not issue (cmd/orchestrator,
// cmd/api): such a service must not be able to sign.
func NewJWTVerifier(publicKey ed25519.PublicKey, accessTTL, refreshTTL time.Duration) *JWTManager {
	return &JWTManager{
		publicKey:    publicKey,
		accessTTL:    accessTTL,
		refreshTTL:   refreshTTL,
		refreshBytes: 32,
	}
}

// Claims carries the JWT payload.
type Claims struct {
	jwt.RegisteredClaims
	UserID string   `json:"uid"`
	Roles  []string `json:"roles"`
	OrgID  string   `json:"org_id,omitempty"`
}

// GenerateAccessToken creates a signed EdDSA (Ed25519) JWT for the given user
// within an organization.
//
// It fails closed on a verify-only manager: without a private key there is no
// honest token to return, and silently falling back to another algorithm is the
// downgrade this design exists to prevent.
func (m *JWTManager) GenerateAccessToken(userID, orgID string, roles []string) (string, time.Time, error) {
	if len(m.privateKey) != ed25519.PrivateKeySize {
		return "", time.Time{}, errors.New("no Ed25519 private key configured: a verify-only JWT manager cannot sign")
	}

	now := time.Now().UTC()
	expiresAt := now.Add(m.accessTTL)

	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        newID(),
		},
		UserID: userID,
		Roles:  roles,
		OrgID:  orgID,
	}

	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(m.privateKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return token, expiresAt, nil
}

// ValidateAccessToken parses and validates an access token, returning its claims.
func (m *JWTManager) ValidateAccessToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(
		tokenStr, &Claims{},
		m.verificationKey,
		jwt.WithLeeway(5*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("parse access token: %w", err)
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}
	return claims, nil
}

// verificationKey returns the public key for a token whose signing method has
// been pinned to Ed25519.
//
// The method is checked by TYPE, never by the token's `alg` header. Trusting the
// header is the classic algorithm-confusion attack: an attacker takes the
// (public) verification key, signs an HS256 token with those bytes as the HMAC
// secret, and sets `alg: HS256`; a verifier that picks its key from the header
// then accepts a token it could have forged itself. Pinning the type makes such
// a token fail before any key is used.
func (m *JWTManager) verificationKey(token *jwt.Token) (any, error) {
	if _, ok := token.Method.(*jwt.SigningMethodEd25519); !ok {
		return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
	}
	if len(m.publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("no Ed25519 public key configured")
	}
	return m.publicKey, nil
}

// GenerateRefreshToken creates a cryptographically random refresh token string
// and its SHA-256 hash for storage. It starts a new token family and returns
// (raw token, token family ID, hash).
//
// Refresh tokens stay opaque random strings rather than JWTs: they are revocable
// through the session store, and keeping them out of the signed-token scheme is
// what makes an access-token algorithm change a non-event for signed-in users —
// they simply refresh into the new algorithm.
func (m *JWTManager) GenerateRefreshToken() (raw, family, hash string, err error) {
	raw, hash, err = m.generateRefreshToken()
	if err != nil {
		return "", "", "", err
	}
	return raw, newID(), hash, nil
}

func (m *JWTManager) generateRefreshToken() (raw, hash string, err error) {
	b := make([]byte, m.refreshBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate refresh token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, hashToken(raw), nil
}

// HashRefreshToken returns the SHA-256 hash of a raw refresh token.
func (m *JWTManager) HashRefreshToken(raw string) string {
	return hashToken(raw)
}

// RefreshTTL returns the configured refresh token TTL.
func (m *JWTManager) RefreshTTL() time.Duration { return m.refreshTTL }

func hashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

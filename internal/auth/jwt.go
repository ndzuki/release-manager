package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWTManager handles access and refresh token signing and validation.
type JWTManager struct {
	signingKey   []byte
	accessTTL    time.Duration
	refreshTTL   time.Duration
	refreshBytes int
}

// NewJWTManager creates a JWTManager with the given configuration.
func NewJWTManager(signingKey []byte, accessTTL, refreshTTL time.Duration) *JWTManager {
	return &JWTManager{
		signingKey:   signingKey,
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

// GenerateAccessToken creates a signed HS256 JWT for the given user and organization.
func (m *JWTManager) GenerateAccessToken(userID string, roles []string, orgID string) (string, time.Time, error) {
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

	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.signingKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return token, expiresAt, nil
}

// ValidateAccessToken parses and validates an access token, returning its claims.
func (m *JWTManager) ValidateAccessToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{},
		func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return m.signingKey, nil
		},
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

// GenerateRefreshToken creates a cryptographically random refresh token string
// and its SHA-256 hash for storage. Returns (raw token, token family ID, hash).
func (m *JWTManager) GenerateRefreshToken() (string, string, string, error) {
	b := make([]byte, m.refreshBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("generate refresh token: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(b)
	family := newID()
	hash := hashToken(raw)
	return raw, family, hash, nil
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

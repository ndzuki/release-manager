// Package jwtauth validates release-manager access tokens.
//
// It is the verifier half of the management-plane JWT scheme: tokens are EdDSA
// (Ed25519) and this package holds only the public key, so a service that uses
// it (cmd/api) cannot mint identities (REQ-065 AC-065-01, decision D1=A).
// Signing lives in internal/auth, which is the only place the private key is
// mounted — a single signer is the point of the asymmetric scheme.
package jwtauth

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims carries the access-token authorization snapshot.
type Claims struct {
	jwt.RegisteredClaims
	UserID string   `json:"uid"`
	Roles  []string `json:"roles"`
	OrgID  string   `json:"org_id,omitempty"`
}

// Manager validates EdDSA (Ed25519) access tokens against a public key.
type Manager struct {
	publicKey ed25519.PublicKey
	accessTTL time.Duration
}

// New creates a verify-only Manager from an Ed25519 public key.
func New(publicKey ed25519.PublicKey, accessTTL time.Duration) *Manager {
	return &Manager{publicKey: publicKey, accessTTL: accessTTL}
}

// ValidateAccessToken parses and validates an EdDSA access token.
func (m *Manager) ValidateAccessToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, m.verificationKey, jwt.WithLeeway(5*time.Second))
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
func (m *Manager) verificationKey(token *jwt.Token) (any, error) {
	if _, ok := token.Method.(*jwt.SigningMethodEd25519); !ok {
		return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
	}
	if len(m.publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("no Ed25519 public key configured")
	}
	return m.publicKey, nil
}

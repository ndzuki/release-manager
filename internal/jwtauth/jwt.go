// Package jwtauth signs and validates release-manager access tokens.
package jwtauth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Claims carries the access-token authorization snapshot.
type Claims struct {
	jwt.RegisteredClaims
	UserID string   `json:"uid"`
	Roles  []string `json:"roles"`
	OrgID  string   `json:"org_id,omitempty"`
}

// Manager signs and validates HS256 access tokens.
type Manager struct {
	signingKey []byte
	accessTTL  time.Duration
}

// New creates a Manager.
func New(signingKey []byte, accessTTL time.Duration) *Manager {
	return &Manager{signingKey: signingKey, accessTTL: accessTTL}
}

// GenerateAccessToken creates an HS256 access token.
func (m *Manager) GenerateAccessToken(userID, orgID string, roles []string) (string, time.Time, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(m.accessTTL)
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: userID, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expiresAt), ID: uuid.NewString(),
		},
		UserID: userID, OrgID: orgID, Roles: roles,
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.signingKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return token, expiresAt, nil
}

// ValidateAccessToken parses and validates an HS256 access token.
func (m *Manager) ValidateAccessToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return m.signingKey, nil
	}, jwt.WithLeeway(5*time.Second))
	if err != nil {
		return nil, fmt.Errorf("parse access token: %w", err)
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}
	return claims, nil
}

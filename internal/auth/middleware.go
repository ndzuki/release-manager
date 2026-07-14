// Package auth implements multi-tenant authentication middleware.
//
// Supported auth methods:
//   - API Key: X-API-Key header (backward compatibility)
//   - Session Token: Bearer token (OIDC/LDAP/DingTalk login)
//   - OIDC: OpenID Connect
//   - LDAP: Enterprise LDAP directory
//   - DingTalk: DingTalk OAuth 2.0 QR code login
//
// All authenticated users must belong to an Organization.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

// User represents an authenticated platform user.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	OrgID    string `json:"org_id"`
	OrgName  string `json:"org_name"`
	Role     string `json:"role"`
}

// Provider authenticates requests and returns a User.
type Provider interface {
	Name() string
	Authenticate(r *http.Request) (*User, error)
}

// =============================================================================
// API Key Authentication (backward compatible)
// =============================================================================

// APIKeyAuth authenticates via X-API-Key header.
type APIKeyAuth struct {
	apiKey string
	log    logr.Logger
}

// NewAPIKeyAuth creates a new APIKeyAuth provider.
func NewAPIKeyAuth(apiKey string, log logr.Logger) *APIKeyAuth {
	return &APIKeyAuth{apiKey: apiKey, log: log}
}

// Name returns the provider name.
func (a *APIKeyAuth) Name() string { return "apikey" }

// Authenticate validates the X-API-Key header.
func (a *APIKeyAuth) Authenticate(r *http.Request) (*User, error) {
	key := r.Header.Get("X-API-Key")
	if key == "" {
		key = r.URL.Query().Get("api_key")
	}
	if key != a.apiKey || a.apiKey == "" {
		return nil, fmt.Errorf("invalid API key")
	}
	return &User{ID: "api", Username: "api", Role: "admin"}, nil
}

// =============================================================================
// Session Token Authentication
// =============================================================================

// SessionCache is the cache interface for session storage.
type SessionCache interface {
	Get(key string) (any, bool)
	Set(key string, value any)
	Delete(key string)
}

// SessionAuth authenticates via Bearer token.
type SessionAuth struct {
	cache  SessionCache
	maxAge time.Duration
	log    logr.Logger
}

// NewSessionAuth creates a new SessionAuth provider.
func NewSessionAuth(cache SessionCache, maxAge time.Duration, log logr.Logger) *SessionAuth {
	return &SessionAuth{cache: cache, log: log, maxAge: maxAge}
}

// Name returns the provider name.
func (a *SessionAuth) Name() string { return "session" }

// Authenticate validates the Bearer token.
func (a *SessionAuth) Authenticate(r *http.Request) (*User, error) {
	token := extractBearerToken(r)
	if token == "" {
		return nil, fmt.Errorf("missing bearer token")
	}

	val, ok := a.cache.Get("session:" + token)
	if !ok {
		return nil, fmt.Errorf("invalid or expired session")
	}
	user, ok := val.(*User)
	if !ok {
		return nil, fmt.Errorf("invalid session data")
	}
	return user, nil
}

// CreateSession creates a session token for a user.
func (a *SessionAuth) CreateSession(user *User) (string, error) {
	token, err := generateToken(32)
	if err != nil {
		return "", err
	}
	a.cache.Set("session:"+token, user)
	return token, nil
}

// =============================================================================
// Composite Auth Middleware
// =============================================================================

// Middleware tries multiple auth providers in order.
type Middleware struct {
	providers []Provider
	log       logr.Logger
}

// NewMiddleware creates a composite auth middleware.
func NewMiddleware(log logr.Logger, providers ...Provider) *Middleware {
	return &Middleware{providers: providers, log: log}
}

// Authenticate tries each provider and returns the first successful user.
func (m *Middleware) Authenticate(r *http.Request) (*User, error) {
	for _, p := range m.providers {
		user, err := p.Authenticate(r)
		if err == nil {
			return user, nil
		}
	}
	return nil, fmt.Errorf("authentication failed")
}

// Handler returns an HTTP middleware that injects the authenticated user.
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := m.Authenticate(r)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`)) //nolint:errcheck // best-effort write
		}
		ctx := context.WithValue(r.Context(), ctxKeyUser, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// =============================================================================
// Context Utilities
// =============================================================================

type contextKey string

const ctxKeyUser contextKey = "user"

// UserFromContext extracts the authenticated user from context.
func UserFromContext(ctx context.Context) (*User, bool) {
	u, ok := ctx.Value(ctxKeyUser).(*User)
	return u, ok
}

// =============================================================================
// Helpers
// =============================================================================

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return ""
	}
	return strings.TrimPrefix(auth, prefix)
}

func generateToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

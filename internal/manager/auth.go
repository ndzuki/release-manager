// Package manager 实现多租户认证中间件。
//
// 支持的认证方式:
//   - OIDC (OpenID Connect): 通过标准 OIDC Provider 认证
//   - LDAP: 通过企业 LDAP 目录认证
//   - 钉钉扫码: 通过钉钉 OAuth 2.0 扫码登录
//
// 所有认证方式返回的 User 必须关联到一个 Organization，
// 只有所属组织的管理员可以管理该组织的客户和 chart。
package manager

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

// AuthProvider 认证提供者接口。
type AuthProvider interface {
	// Authenticate 验证请求并返回用户信息。
	Authenticate(r *http.Request) (*User, error)
	// Name 返回提供者名称。
	Name() string
}

// =============================================================================
// API Key 认证（简化模式，向后兼容）
// =============================================================================

// APIKeyAuth 通过 X-API-Key header 认证。
type APIKeyAuth struct {
	apiKey string
	log    logr.Logger
}

func NewAPIKeyAuth(apiKey string, log logr.Logger) *APIKeyAuth {
	return &APIKeyAuth{apiKey: apiKey, log: log}
}

func (a *APIKeyAuth) Name() string { return "apikey" }

func (a *APIKeyAuth) Authenticate(r *http.Request) (*User, error) {
	key := r.Header.Get("X-API-Key")
	if key == "" {
		key = r.URL.Query().Get("api_key")
	}
	if key == "" || key != a.apiKey {
		return nil, fmt.Errorf("invalid API key")
	}
	// API Key 认证返回默认管理员用户
	return &User{
		ID:           "admin",
		OrgID:        "default",
		Name:         "Administrator",
		Role:         "admin",
		AuthProvider: "apikey",
	}, nil
}

// =============================================================================
// Session Token 认证
// =============================================================================

// SessionAuth 通过 Bearer token 认证（OIDC/LDAP/钉钉登录后的 session）。
type SessionAuth struct {
	cache  *Cache
	log    logr.Logger
	maxAge time.Duration
}

func NewSessionAuth(cache *Cache, maxAge time.Duration, log logr.Logger) *SessionAuth {
	return &SessionAuth{cache: cache, log: log, maxAge: maxAge}
}

func (a *SessionAuth) Name() string { return "session" }

func (a *SessionAuth) Authenticate(r *http.Request) (*User, error) {
	token := extractBearerToken(r)
	if token == "" {
		return nil, fmt.Errorf("missing Bearer token")
	}

	// 从缓存中查找 session
	val, ok := a.cache.Get("session:" + token)
	if !ok {
		return nil, fmt.Errorf("session expired or invalid")
	}

	user, ok := val.(*User)
	if !ok {
		return nil, fmt.Errorf("invalid session data")
	}

	return user, nil
}

// CreateSession 为用户创建 session token。
func (a *SessionAuth) CreateSession(user *User) (string, error) {
	token, err := generateToken(32)
	if err != nil {
		return "", err
	}
	a.cache.Set("session:"+token, user, a.maxAge)
	return token, nil
}

// =============================================================================
// 组合认证中间件
// =============================================================================

// AuthMiddleware 按优先级尝试多种认证方式。
type AuthMiddleware struct {
	providers []AuthProvider
	log       logr.Logger
}

func NewAuthMiddleware(log logr.Logger, providers ...AuthProvider) *AuthMiddleware {
	return &AuthMiddleware{providers: providers, log: log}
}

// Authenticate 依次尝试各认证提供者，返回第一个成功的用户。
func (m *AuthMiddleware) Authenticate(r *http.Request) (*User, error) {
	for _, p := range m.providers {
		user, err := p.Authenticate(r)
		if err != nil {
			m.log.V(2).Info("auth provider failed", "provider", p.Name(), "error", err)
			continue
		}
		m.log.V(1).Info("authenticated", "provider", p.Name(), "user", user.ID)
		return user, nil
	}
	return nil, fmt.Errorf("all auth providers failed")
}

// Handler 返回 HTTP 中间件。
func (m *AuthMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := m.Authenticate(r)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized: " + err.Error()})
			return
		}
		// 将用户信息注入 context
		ctx := context.WithValue(r.Context(), ctxKeyUser, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}


// =============================================================================
// 工具函数
// =============================================================================

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

func generateToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

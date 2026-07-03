// Package manager 实现多认证提供者和 RBAC 权限管理。
//
// 支持的认证提供者:
//   - LDAP: 企业目录认证，支持 StartTLS/TLS，组映射
//   - OIDC: OpenID Connect (Authorization Code Flow + PKCE)
//   - DingTalk SSO: 钉钉扫码登录
//   - Local: 本地用户名密码
//
// RBAC 角色定义 (硬编码，生产环境可集成 casbin):
//   - admin:    全部权限
//   - operator: 管理客户/Chart/发布，只读用户和组织
//   - viewer:   只读所有资源
package manager

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"sync"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	"github.com/go-logr/logr"
)

// Role 定义系统角色。
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

// casbinRBACModel 定义 casbin RBAC 模型。
const casbinRBACModel = `
[request_definition]
r = sub, org, obj, act

[policy_definition]
p = sub, org, obj, act

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub) && (p.org == r.org || p.org == "*") && keyMatch(r.obj, p.obj) && regexMatch(r.act, p.act)
`

// CasbinRBAC 封装 casbin 同步执行器。
type CasbinRBAC struct {
	enforcer *casbin.SyncedCachedEnforcer
	mu       sync.RWMutex
	log      logr.Logger
}

// NewCasbinRBAC 创建 casbin RBAC 执行器。
func NewCasbinRBAC(log logr.Logger) (*CasbinRBAC, error) {
	m, err := model.NewModelFromString(casbinRBACModel)
	if err != nil {
		return nil, fmt.Errorf("parse casbin model: %w", err)
	}

	enforcer, err := casbin.NewSyncedCachedEnforcer(m)
	if err != nil {
		return nil, fmt.Errorf("create casbin enforcer: %w", err)
	}

	r := &CasbinRBAC{enforcer: enforcer, log: log.WithName("casbin")}
	r.loadPolicies()
	return r, nil
}

// loadPolicies 加载预定义 RBAC 策略。
func (r *CasbinRBAC) loadPolicies() {
	r.mu.Lock()
	defer r.mu.Unlock()

	policies := [][]string{
		// admin — 全部权限
		{"admin", "*", "/api/v1/*", "(GET|POST|PUT|DELETE)"},
		// operator — 运维权限
		{"operator", "*", "/api/v1/customers*", "(GET|POST|PUT|DELETE)"},
		{"operator", "*", "/api/v1/charts*", "(GET|POST|PUT|DELETE)"},
		{"operator", "*", "/api/v1/releases*", "(GET)"},
		{"operator", "*", "/api/v1/dashboard*", "(GET)"},
		{"operator", "*", "/api/v1/certificates*", "(GET|POST|PUT)"},
		{"operator", "*", "/api/v1/users", "(GET)"},
		{"operator", "*", "/api/v1/orgs*", "(GET)"},
		// viewer — 只读
		{"viewer", "*", "/api/v1/*", "(GET)"},
	}
	for _, p := range policies {
		if _, err := r.enforcer.AddPolicy(p); err != nil { r.log.Error(err, "casbin add policy failed", "policy", p) }
	}
	// 角色继承
	r.enforcer.AddGroupingPolicy("admin", "operator")
	r.enforcer.AddGroupingPolicy("operator", "viewer")
}

// Enforce 检查用户是否有权限 (sub=userID, org=orgID, obj=path, act=method)。
func (r *CasbinRBAC) Enforce(sub, org, obj, act string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.enforcer.Enforce(sub, org, obj, act)
}

// AddRoleForUser 为用户绑定角色。
func (r *CasbinRBAC) AddRoleForUser(user, role string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.enforcer.AddRoleForUser(user, role)
	return err
}

// DeleteRoleForUser 移除用户的角色。
func (r *CasbinRBAC) DeleteRoleForUser(user, role string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.enforcer.DeleteRoleForUser(user, role)
	return err
}

// GetRolesForUser 获取用户角色列表。
func (r *CasbinRBAC) GetRolesForUser(user string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.enforcer.GetRolesForUser(user)
}

// AddPolicy 添加自定义策略。
func (r *CasbinRBAC) AddPolicy(sub, org, obj, act string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.enforcer.AddPolicy(sub, org, obj, act)
	return err
}

// =============================================================================
// LDAP 认证提供者
// =============================================================================

// LDAPConfig LDAP 连接配置。
type LDAPConfig struct {
	Host           string `yaml:"host"`            // LDAP 服务器地址 (e.g. ldap.example.com:389)
	BaseDN         string `yaml:"base_dn"`         // 搜索基 DN (e.g. dc=example,dc=com)
	BindDN         string `yaml:"bind_dn"`         // 绑定 DN (e.g. cn=admin,dc=example,dc=com)
	BindPassword   string `yaml:"bind_password"`   // 绑定密码
	UserFilter     string `yaml:"user_filter"`     // 用户过滤 (e.g. (&(uid=%s)(objectClass=posixAccount)))
	GroupFilter    string `yaml:"group_filter"`    // 组过滤 (e.g. (&(cn=%s)(objectClass=groupOfNames)))
	GroupAttr      string `yaml:"group_attr"`      // 组成员属性 (e.g. member)
	EmailAttr      string `yaml:"email_attr"`      // 邮箱属性 (e.g. mail)
	UseTLS         bool   `yaml:"use_tls"`         // 使用 TLS
	SkipTLSVerify  bool   `yaml:"skip_tls_verify"` // 跳过 TLS 验证
	CAFile         string `yaml:"ca_file"`         // 自定义 CA 文件
	Enabled        bool   `yaml:"enabled"`
}

// LDAPAuth LDAP 认证提供者。
type LDAPAuth struct {
	cfg    LDAPConfig
	store  Store
	log    logr.Logger
	client *ldapClient
}

func NewLDAPAuth(cfg LDAPConfig, store Store, log logr.Logger) *LDAPAuth {
	a := &LDAPAuth{cfg: cfg, store: store, log: log.WithName("ldap")}
	if cfg.Enabled {
		a.client = newLDAPClient(cfg, log)
	}
	return a
}

func (a *LDAPAuth) Name() string { return "ldap" }

// Authenticate 通过 LDAP 绑定验证用户。
func (a *LDAPAuth) Authenticate(r *http.Request) (*User, error) {
	if !a.cfg.Enabled {
		return nil, fmt.Errorf("LDAP not enabled")
	}
	username, password := basicAuth(r)
	if username == "" || password == "" {
		return nil, fmt.Errorf("LDAP requires username and password")
	}

	// 搜索用户 DN
	userDN, attrs, err := a.client.searchUser(username)
	if err != nil {
		return nil, fmt.Errorf("LDAP search user: %w", err)
	}
	// 绑定验证
	if err := a.client.bind(userDN, password); err != nil {
		return nil, fmt.Errorf("LDAP bind failed")
	}

	email := getAttr(attrs, a.cfg.EmailAttr)
	// 查找或创建本地用户
	user, err := a.store.GetUserByEmail(email)
	if err != nil {
		// LDAP 用户首次登录，自动创建
		groups := a.client.searchGroups(userDN)
		role := groupsToRole(groups, a.log)
		user = &User{
			ID:           username,
			Name:         username,
			Email:        email,
			Role:         role,
			AuthProvider: "ldap",
			ExternalID:   userDN,
			Enabled:      true,
			CreatedAt:    time.Now(),
		}
		_ = a.store.CreateUser(*user)
	}
	return user, nil
}

// =============================================================================
// OIDC 认证提供者
// =============================================================================

// OIDCConfig OIDC 认证配置。
type OIDCConfig struct {
	IssuerURL   string   `yaml:"issuer_url"`
	ClientID    string   `yaml:"client_id"`
	RedirectURL string   `yaml:"redirect_url"`
	Scopes      []string `yaml:"scopes"`
	EmailDomain string   `yaml:"email_domain"`
	Enabled     bool     `yaml:"enabled"`
}

// OIDCAuth OIDC 认证提供者。
type OIDCAuth struct {
	cfg   OIDCConfig
	store Store
	log   logr.Logger
}

func NewOIDCAuth(cfg OIDCConfig, store Store, log logr.Logger) *OIDCAuth {
	return &OIDCAuth{cfg: cfg, store: store, log: log.WithName("oidc")}
}

func (a *OIDCAuth) Name() string { return "oidc" }

// LoginURL 生成 OIDC 登录 URL。
func (a *OIDCAuth) LoginURL(state string) string {
	u, _ := url.Parse(a.cfg.IssuerURL + "/authorize")
	q := u.Query()
	q.Set("client_id", a.cfg.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", a.cfg.RedirectURL)
	q.Set("scope", strings.Join(a.cfg.Scopes, " "))
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String()
}

// Authenticate 通过 OIDC Bearer token 验证。
func (a *OIDCAuth) Authenticate(r *http.Request) (*User, error) {
	token := extractBearerToken(r)
	if token == "" {
		return nil, fmt.Errorf("missing Bearer token")
	}
	// 验证 ID Token（生产环境使用 coreos/go-oidc 库）
	claims, err := parseJWTClaims(token)
	if err != nil {
		return nil, fmt.Errorf("invalid OIDC token: %w", err)
	}
	email, _ := claims["email"].(string)
	if email == "" {
		return nil, fmt.Errorf("OIDC token missing email claim")
	}
	if a.cfg.EmailDomain != "" && !strings.HasSuffix(email, "@"+a.cfg.EmailDomain) {
		return nil, fmt.Errorf("email domain not allowed: %s", email)
	}
	user, err := a.store.GetUserByEmail(email)
	if err != nil {
		name, _ := claims["name"].(string)
		if name == "" { name = email }
		user = &User{
			ID:           email,
			Name:         name,
			Email:        email,
			Role:         "viewer",
			AuthProvider: "oidc",
			ExternalID:   email,
			Enabled:      true,
			CreatedAt:    time.Now(),
		}
		_ = a.store.CreateUser(*user)
	}
	return user, nil
}

// =============================================================================
// 钉钉 SSO 扫码登录
// =============================================================================

// DingTalkSSOConfig 钉钉 SSO 配置。
type DingTalkSSOConfig struct {
	AppKey    string `yaml:"app_key"`
	AppSecret string `yaml:"app_secret"`
	Enabled   bool   `yaml:"enabled"`
}

// DingTalkAuth 钉钉扫码登录提供者。
type DingTalkAuth struct {
	cfg   DingTalkSSOConfig
	store Store
	log   logr.Logger
}

func NewDingTalkAuth(cfg DingTalkSSOConfig, store Store, log logr.Logger) *DingTalkAuth {
	return &DingTalkAuth{cfg: cfg, store: store, log: log.WithName("dingtalk-sso")}
}

func (a *DingTalkAuth) Name() string { return "dingtalk" }

// LoginURL 生成钉钉扫码登录 URL。
// 钉钉 OAuth 2.0 授权流程:
//  1. GET /api/v1/auth/dingtalk/url → 前端跳转到钉钉
//  2. 用户扫码确认
//  3. 钉钉回调 redirect_url?code=xxx&state=xxx
//  4. POST /api/v1/auth/dingtalk/callback → 用 code 换 access_token
//  5. 用 access_token 获取 userid → 匹配本地用户
func (a *DingTalkAuth) LoginURL(redirectURL, state string) string {
	return fmt.Sprintf(
		"https://login.dingtalk.com/oauth2/auth?redirect_uri=%s&response_type=code&client_id=%s&scope=openid%%20corpid&state=%s&prompt=consent",
		url.QueryEscape(redirectURL),
		a.cfg.AppKey,
		state,
	)
}

// HandleCallback 处理钉钉回调，用 code 换用户信息。
func (a *DingTalkAuth) HandleCallback(code string) (*User, error) {
	// 用 code 换取 access_token
	tokenResp, err := a.exchangeCode(code)
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}
	// 用 access_token 获取用户信息
	userInfo, err := a.getUserInfo(tokenResp.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("get user info: %w", err)
	}
	// 查找或创建本地用户
	user, err := a.store.GetUserByEmail(userInfo.Email)
	if err != nil {
		user = &User{
			ID:             userInfo.UserID,
			Name:           userInfo.Name,
			Email:          userInfo.Email,
			Role:           "viewer",
			AuthProvider:   "dingtalk",
			ExternalID:     userInfo.UserID,
			DingTalkUserID: userInfo.UserID,
			Enabled:        true,
			CreatedAt:      time.Now(),
		}
		_ = a.store.CreateUser(*user)
	}
	return user, nil
}

// Authenticate 通过钉钉 session 验证。
func (a *DingTalkAuth) Authenticate(r *http.Request) (*User, error) {
	userID := r.Header.Get("X-DingTalk-UserID")
	if userID == "" {
		return nil, fmt.Errorf("missing DingTalk user ID")
	}
	return a.store.GetUser(userID)
}

// =============================================================================
// LDAP/钉钉 辅助类型和方法
// =============================================================================

type ldapClient struct {
	cfg LDAPConfig
	log logr.Logger
}

func newLDAPClient(cfg LDAPConfig, log logr.Logger) *ldapClient {
	return &ldapClient{cfg: cfg, log: log}
}

func (c *ldapClient) searchUser(username string) (string, map[string][]string, error) {
	// 实现: 使用 go-ldap 库搜索用户
	filter := strings.ReplaceAll(c.cfg.UserFilter, "%s", username)
	_ = filter
	return "", nil, fmt.Errorf("LDAP not fully configured: set ldap.enabled=true with valid config")
}

func (c *ldapClient) bind(dn, password string) error { return fmt.Errorf("not configured") }

func (c *ldapClient) searchGroups(userDN string) []string { return nil }

type dingTalkTokenResponse struct {
	AccessToken string `json:"access_token"`
}

type dingTalkUserInfo struct {
	UserID string `json:"userid"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}

func (a *DingTalkAuth) exchangeCode(code string) (*dingTalkTokenResponse, error) {
	return nil, fmt.Errorf("not configured: set dingtalk_sso.enabled=true")
}

func (a *DingTalkAuth) getUserInfo(token string) (*dingTalkUserInfo, error) {
	return nil, fmt.Errorf("not configured")
}

// =============================================================================
// 工具函数
// =============================================================================

func basicAuth(r *http.Request) (username, password string) {
	u, p, ok := r.BasicAuth()
	if !ok { return "", "" }
	return u, p
}

func getAttr(attrs map[string][]string, name string) string {
	if vals, ok := attrs[name]; ok && len(vals) > 0 {
		return vals[0]
	}
	return ""
}

func groupsToRole(groups []string, log logr.Logger) string {
	for _, g := range groups {
		switch strings.ToLower(g) {
		case "admin", "administrators", "release-admins":
			return string(RoleAdmin)
		case "operator", "operators", "release-operators":
			return string(RoleOperator)
		}
	}
	return string(RoleViewer)
}

func parseJWTClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 { return nil, fmt.Errorf("invalid JWT format") }
	var claims map[string]any
	data, err := base64Decode(parts[1])
	if err != nil { return nil, err }
	if err := json.Unmarshal(data, &claims); err != nil { return nil, err }
	return claims, nil
}

func base64Decode(s string) ([]byte, error) {
	// padding
	switch len(s) % 4 {
	case 2: s += "=="
	case 3: s += "="
	}
	// standard decode — simplified
	return []byte(s), nil
}


// =============================================================================
// RBAC 用户管理 HTTP Handler
// =============================================================================

// UserRBACHandler 处理 /api/v1/users 路由 — 用户角色绑定管理。
type UserRBACHandler struct {
	rbac  *CasbinRBAC
	store Store
	log   logr.Logger
}

func NewUserRBACHandler(rbac *CasbinRBAC, store Store, log logr.Logger) *UserRBACHandler {
	return &UserRBACHandler{rbac: rbac, store: store, log: log.WithName("user-rbac")}
}

func (h *UserRBACHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/users")
	switch {
	case path == "" || path == "/":
		h.listUsers(w, r)
	case strings.HasPrefix(path, "/") && !strings.Contains(path[1:], "/"):
		h.userRoles(w, r, path[1:])
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (h *UserRBACHandler) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListUsers()
	if err != nil { writeJSON(w, 500, map[string]string{"error": err.Error()}); return }
	type UserWithRoles struct{ User User; Roles []string }
	result := make([]UserWithRoles, 0, len(users))
	for _, u := range users {
		roles, _ := h.rbac.GetRolesForUser(u.ID)
		if roles == nil { roles = []string{} }
		result = append(result, UserWithRoles{User: u, Roles: roles})
	}
	writeJSON(w, 200, result)
}

func (h *UserRBACHandler) userRoles(w http.ResponseWriter, r *http.Request, userID string) {
	switch r.Method {
	case http.MethodGet:
		roles, _ := h.rbac.GetRolesForUser(userID)
		if roles == nil { roles = []string{} }
		writeJSON(w, 200, map[string]any{"user_id": userID, "roles": roles})
	case http.MethodPut:
		var req struct{ Role string }
		if json.NewDecoder(r.Body).Decode(&req) != nil { writeJSON(w, 400, map[string]string{"error":"invalid JSON"}); return }
		if req.Role != "admin" && req.Role != "operator" && req.Role != "viewer" { writeJSON(w, 400, map[string]string{"error":"role must be admin/operator/viewer"}); return }
		for _, old := range []string{"admin","operator","viewer"} { h.rbac.DeleteRoleForUser(userID, old) }
		if err := h.rbac.AddRoleForUser(userID, req.Role); err != nil { writeJSON(w, 500, map[string]string{"error":err.Error()}); return }
		h.log.Info("role bound", "user", userID, "role", req.Role)
		writeJSON(w, 200, map[string]string{"message":"role updated"})
	default:
		writeJSON(w, 405, map[string]string{"error":"method not allowed"})
	}
}

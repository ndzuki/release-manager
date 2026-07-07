// Package manager 实现 release-manager 首次初始化流程。
//
// 生产环境: 首次启动 → 前端重定向到 /init → 用户创建管理员账号(用户名+密码+邮箱)
//
//	→ SMTP 发送验证邮件 → 验证通过后进入控制台
//
// 开发环境 (dev_mode=true): 自动初始化 admin/admin，跳过 SMTP
package manager

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/crypto/bcrypt"
)

// InitHandler 处理首次初始化请求。
type InitHandler struct {
	store    Store
	smtpCfg  SMTPConfig
	devMode  bool
	log      logr.Logger
	mu       sync.Mutex // 保证初始化只执行一次
	initDone bool
}

// SMTPConfig 邮件配置。
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	Enabled  bool
}

// NewInitHandler 创建初始化处理器。
func NewInitHandler(store Store, smtpCfg SMTPConfig, devMode bool, log logr.Logger) *InitHandler {
	h := &InitHandler{
		store:   store,
		smtpCfg: smtpCfg,
		devMode: devMode,
		log:     log.WithName("init"),
	}

	// 开发模式: 自动初始化
	if devMode {
		h.autoInitDev()
	}

	return h
}

// autoInitDev 开发模式自动初始化 admin/admin。
func (h *InitHandler) autoInitDev() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.initDone {
		return
	}

	existing, err := h.store.GetInitStatus()
	if err == nil && existing {
		h.initDone = true
		return
	}

	passwordHash := hashPassword("admin")
	admin := AdminUser{
		Username:      "admin",
		PasswordHash:  passwordHash,
		Email:         "admin@localhost.local",
		Role:          "admin",
		EmailVerified: true,
		CreatedAt:     time.Now(),
	}

	if err := h.store.CreateAdminUser(admin); err != nil {
		h.log.Error(err, "dev auto-init: failed to create admin user")
		return
	}

	if err := h.store.SetInitStatus(true); err != nil {
		h.log.Error(err, "dev auto-init: failed to set init status")
		return
	}

	h.initDone = true
	h.log.Info("DEV MODE: auto-initialized admin/admin (no SMTP required)")
}

// IsInitialized 检查系统是否已初始化。
func (h *InitHandler) IsInitialized() bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.initDone {
		return true
	}

	done, err := h.store.GetInitStatus()
	if err == nil && done {
		h.initDone = true
	}
	return h.initDone
}

// ServeHTTP 处理初始化请求。
func (h *InitHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleGetStatus(w, r)
	case http.MethodPost:
		h.handleInit(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// 查询初始化状态
// @Summary      查询初始化状态
// @Description  检查系统是否已完成首次初始化
// @Tags         初始化
// @Accept       json
// @Produce      json
// @Success      200  {object}  map[string]any  "初始化状态"
// @Router       /api/v1/init [get]
func (h *InitHandler) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"initialized": h.IsInitialized(),
		"dev_mode":    h.devMode,
	})
}

// 创建管理员账号
// @Summary      创建管理员账号
// @Description  首次初始化系统，创建管理员用户（仅未初始化时可用）
// @Tags         初始化
// @Accept       json
// @Produce      json
// @Param        request  body      object{username=string,password=string,email=string}  true  "管理员信息"
// @Success      201      {object}  map[string]any  "初始化成功"
// @Failure      400      {object}  map[string]string  "参数校验失败"
// @Failure      409      {object}  map[string]string  "系统已初始化"
// @Router       /api/v1/init [post]
func (h *InitHandler) handleInit(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// 直接读 initDone 避免调用 IsInitialized() 导致死锁
	if h.initDone {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already initialized"})
		return
	}
	if done, err := h.store.GetInitStatus(); err == nil && done {
		h.initDone = true
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already initialized"})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Email    string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	// 校验输入
	if len(req.Username) < 3 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username must be at least 3 characters"})
		return
	}
	if len(req.Password) < 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password must be at least 6 characters"})
		return
	}
	if !isValidEmail(req.Email) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid email address"})
		return
	}

	// 创建管理员
	admin := AdminUser{
		Username:      req.Username,
		PasswordHash:  hashPassword(req.Password),
		Email:         req.Email,
		Role:          "admin",
		EmailVerified: !h.smtpCfg.Enabled, // 如果 SMTP 未启用则直接标记已验证
		CreatedAt:     time.Now(),
	}

	if err := h.store.CreateAdminUser(admin); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create admin user: " + err.Error()})
		return
	}

	// 发送验证邮件
	if h.smtpCfg.Enabled {
		verifyToken := h.generateVerifyToken(req.Email)
		if err := h.store.SetVerifyToken(req.Email, verifyToken); err != nil {
			h.log.Error(err, "failed to persist verification token")
		}

		go func() {
			if err := h.sendVerifyEmail(req.Email, req.Username, verifyToken); err != nil {
				h.log.Error(err, "failed to send verification email", "email", req.Email)
			}
		}()
	}

	if err := h.store.SetInitStatus(true); err != nil {
		h.log.Error(err, "failed to set init status")
	}
	h.initDone = true

	h.log.Info("system initialized", "username", req.Username, "email", req.Email)

	writeJSON(w, http.StatusCreated, map[string]any{
		"message":        "system initialized successfully",
		"email_verified": admin.EmailVerified,
	})
}

// 管理员登录
// @Summary      管理员登录
// @Description  使用用户名和密码登录，返回 session token
// @Tags         认证
// @Accept       json
// @Produce      json
// @Param        request  body      object{username=string,password=string}  true  "登录凭证"
// @Success      200      {object}  map[string]any  "登录成功"
// @Failure      401      {object}  map[string]string  "认证失败"
// @Router       /api/v1/auth/login [post]
func (h *InitHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	user, err := h.store.GetAdminUser(req.Username)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	// 密码验证：优先 bcrypt，兼容旧 SHA256 并自动迁移
	if !verifyLoginPassword(user, req.Password) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	// SHA256 → bcrypt 自动迁移
	if !strings.HasPrefix(user.PasswordHash, "$2a$") {
		user.PasswordHash = hashPassword(req.Password)
		if err := h.store.CreateAdminUser(*user); err != nil {
			h.log.Error(err, "failed to migrate password hash to bcrypt")
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"token":    "admin-session-" + user.Username,
		"username": user.Username,
		"email":    user.Email,
		"role":     user.Role,
	})
}

// =============================================================================
// 邮箱验证
// =============================================================================

func (h *InitHandler) generateVerifyToken(email string) string {
	b := make([]byte, 16)
	if _, err := cryptorand.Read(b); err != nil {
		h.log.Error(err, "crypto/rand.Read failed, using fallback")
		// 退化方案: 用时间戳 + sha256 作为备选 token
		hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", email, time.Now().UnixNano())))
		return hex.EncodeToString(hash[:])
	}
	return hex.EncodeToString(b)
}

func (h *InitHandler) sendVerifyEmail(to, username, token string) error {
	verifyURL := fmt.Sprintf("http://localhost:8080/api/v1/auth/verify-email?token=%s", token)

	body := fmt.Sprintf("From: %s\r\n"+
		"To: %s\r\n"+
		"Subject: Release Manager - 验证邮箱地址\r\n"+
		"Content-Type: text/plain; charset=UTF-8\r\n"+
		"\r\n"+
		"您好 %s,\r\n\r\n"+
		"请点击以下链接验证您的邮箱地址:\r\n"+
		"%s\r\n\r\n"+
		"此链接在 24 小时内有效。\r\n"+
		"-- Release Manager 运维管理平台\r\n",
		h.smtpCfg.From, to, username, verifyURL)

	addr := fmt.Sprintf("%s:%d", h.smtpCfg.Host, h.smtpCfg.Port)
	auth := smtp.PlainAuth("", h.smtpCfg.Username, h.smtpCfg.Password, h.smtpCfg.Host)
	return smtp.SendMail(addr, auth, h.smtpCfg.From, []string{to}, []byte(body))
}

// =============================================================================
// AdminUser 模型 + Store 方法
// =============================================================================

// AdminUser 管理员用户。
type AdminUser struct {
	Username      string    `json:"username"`
	PasswordHash  string    `json:"-"`
	Email         string    `json:"email"`
	Role          string    `json:"role"`
	EmailVerified bool      `json:"email_verified"`
	CreatedAt     time.Time `json:"created_at"`
}

// hashPassword 使用 bcrypt 对密码进行哈希，cost=12。
func hashPassword(password string) string {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		// bcrypt 极少失败，当失败时退回 SHA256（如内存不足）
		sha256Hash := sha256.Sum256([]byte(password))
		return hex.EncodeToString(sha256Hash[:])
	}
	return string(hash)
}

// sha256Hex 计算 SHA256 哈希的十六进制字符串（仅用于 SHA256→bcrypt 迁移比对）。
func sha256Hex(s string) string {
	hash := sha256.Sum256([]byte(s))
	return hex.EncodeToString(hash[:])
}

// 简单邮箱校验
func isValidEmail(email string) bool {
	return strings.Contains(email, "@") && strings.Contains(email, ".")
}

// verifyLoginPassword 验证登录密码，优先 bcrypt 比对，兼容旧 SHA256 哈希。
func verifyLoginPassword(user *AdminUser, password string) bool {
	if strings.HasPrefix(user.PasswordHash, "$2a$") {
		return bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) == nil
	}
	// 旧 SHA256 哈希兼容比对
	return user.PasswordHash == sha256Hex(password)
}

// =============================================================================
// Store 接口扩展方法
// =============================================================================

// 这些方法需要在 Store 接口和 MemoryStore 中实现。
// 为简洁起见，通过在 store.go 中添加方法实现。

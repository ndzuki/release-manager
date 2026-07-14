// Package auth init handles first-time system initialization and admin login.
package auth

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/crypto/bcrypt"
)

// AdminUser represents an administrator account.
type AdminUser struct {
	ID            string    `json:"id"`
	Username      string    `json:"username"`
	PasswordHash  string    `json:"-"`
	Email         string    `json:"email"`
	EmailVerified bool      `json:"email_verified"`
	CreatedAt     time.Time `json:"created_at"`
}

// SMTPConfig holds email configuration.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	Enabled  bool
}

// InitStore is the subset of Store needed by the init handler.
type InitStore interface {
	GetInitStatus() (bool, error)
	SetInitStatus(initialized bool) error
	CreateAdminUser(u AdminUser) error
	GetAdminUser(username string) (*AdminUser, error)
	SetVerifyToken(email, token string) error
}

// InitHandler handles first-time initialization and admin login.
type InitHandler struct {
	store   InitStore
	smtpCfg SMTPConfig
	devMode bool
	log     logr.Logger
}

// NewInitHandler creates a new InitHandler.
func NewInitHandler(store InitStore, smtpCfg SMTPConfig, devMode bool, log logr.Logger) *InitHandler {
	h := &InitHandler{
		store:   store,
		smtpCfg: smtpCfg,
		devMode: devMode,
		log:     log.WithName("init"),
	}
	if devMode {
		h.autoInitDev()
	}
	return h
}

func (h *InitHandler) autoInitDev() {
	initialized, _ := h.store.GetInitStatus() //nolint:errcheck // best-effort
	if initialized {
		return
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("admin"), 4) //nolint:errcheck // best-effort
	_ = h.store.CreateAdminUser(AdminUser{                     //nolint:errcheck // best-effort
		Username:      "admin",
		PasswordHash:  string(hash),
		Email:         "admin@dev.local",
		EmailVerified: true,
		CreatedAt:     time.Now(),
	})
	_ = h.store.SetInitStatus(true) //nolint:errcheck // best-effort
	h.log.Info("dev mode: auto-initialized admin/admin")
}

// IsInitialized checks if the system is initialized.
func (h *InitHandler) IsInitialized() bool {
	ok, _ := h.store.GetInitStatus() //nolint:errcheck // best-effort
	return ok
}

// ServeHTTP handles init requests.
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

func (h *InitHandler) handleGetStatus(w http.ResponseWriter, _ *http.Request) {
	initialized := h.IsInitialized()
	writeJSON(w, http.StatusOK, map[string]any{
		"initialized": initialized,
		"dev_mode":    h.devMode,
	})
}

func (h *InitHandler) handleInit(w http.ResponseWriter, r *http.Request) {
	if h.IsInitialized() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "system already initialized"})
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
	if req.Username == "" || req.Password == "" || req.Email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username, password, and email are required"})
		return
	}
	if !strings.Contains(req.Email, "@") || !strings.Contains(req.Email, ".") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid email"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to hash password"})
		return
	}

	admin := AdminUser{
		Username:      req.Username,
		PasswordHash:  string(hash),
		Email:         req.Email,
		EmailVerified: !h.smtpCfg.Enabled,
		CreatedAt:     time.Now(),
	}
	if err := h.store.CreateAdminUser(admin); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := h.store.SetInitStatus(true); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if h.smtpCfg.Enabled {
		token := h.generateVerifyToken(req.Email)
		h.sendVerifyEmail(req.Email, req.Username, token)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"status":  "initialized",
		"message": "admin user created",
	})
}

// HandleLogin handles admin login.
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

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"message": "login successful",
		"user": map[string]string{
			"username": user.Username,
			"email":    user.Email,
		},
	})
}

func (h *InitHandler) generateVerifyToken(email string) string {
	hash := sha256.Sum256([]byte(email + time.Now().String()))
	return fmt.Sprintf("%x", hash[:16])
}

func (h *InitHandler) sendVerifyEmail(to, username, token string) {
	h.log.Info("would send verification email",
		"to", to,
		"username", username,
		"token", token[:8]+"...",
	)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data) //nolint:errcheck // best-effort
}

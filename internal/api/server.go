package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-logr/logr"
)

// Server sets up and manages the HTTP API server.
type Server struct {
	cfg *ServerConfig
	log logr.Logger

	customerHandler *CustomerHandler
	releaseHandler  *ReleaseHandler
	auditHandler    *AuditHandler

	// Optional hooks for flexible composition.
	WebhookHandler  http.Handler
	InitHandler     http.Handler
	LoginHandler    http.HandlerFunc
	ChartHandler    http.Handler
	RBACHandler     http.Handler
	AuthMiddleware  func(http.Handler) http.Handler
	AuditMiddleware func(http.Handler) http.Handler
	DingTalkEnabled bool

	httpServer *http.Server
}

// ServerConfig holds the HTTP server configuration.
type ServerConfig struct {
	HTTPAddr        string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ShutdownTimeout time.Duration
	APIKey          string
}

// NewServer creates a new API HTTP server.
func NewServer(cfg *ServerConfig, log logr.Logger) *Server {
	return &Server{
		cfg: cfg,
		log: log.WithName("api"),
	}
}

// RegisterCustomerHandler sets the customer management handler.
func (s *Server) RegisterCustomerHandler(h *CustomerHandler) { s.customerHandler = h }

// RegisterReleaseHandler sets the release record handler.
func (s *Server) RegisterReleaseHandler(h *ReleaseHandler) { s.releaseHandler = h }

// RegisterAuditHandler sets the audit log handler.
func (s *Server) RegisterAuditHandler(h *AuditHandler) { s.auditHandler = h }

// BuildMux constructs the HTTP mux with all registered handlers.
func (s *Server) BuildMux() http.Handler {
	mux := http.NewServeMux()

	// Init + login (no auth required).
	if s.InitHandler != nil {
		mux.Handle("/api/v1/init", s.InitHandler)
	}
	if s.LoginHandler != nil {
		mux.HandleFunc("/api/v1/auth/login", s.LoginHandler)
	}

	// Webhook (HMAC signature, no auth).
	if s.WebhookHandler != nil {
		mux.Handle("/api/v1/webhook/harbor", s.WebhookHandler)
	}

	// Multi-tenant endpoints (auth required).
	if s.ChartHandler != nil && s.AuthMiddleware != nil {
		mux.Handle("/api/v1/orgs/", s.AuthMiddleware(s.ChartHandler))
		mux.Handle("/api/v1/dashboard/", s.AuthMiddleware(s.ChartHandler))
	}

	// User role management (auth + RBAC).
	if s.RBACHandler != nil && s.AuthMiddleware != nil {
		mux.Handle("/api/v1/users", s.AuthMiddleware(s.RBACHandler))
		mux.Handle("/api/v1/users/", s.AuthMiddleware(s.RBACHandler))
	}

	// Management API (API key auth, backward compatible).
	if s.customerHandler != nil {
		authHandler := s.apiKeyMiddleware(http.HandlerFunc(s.customerHandler.ServeHTTP))
		mux.Handle("/api/v1/customers", authHandler)
		mux.Handle("/api/v1/customers/", authHandler)
	}
	if s.releaseHandler != nil {
		authHandler := s.apiKeyMiddleware(http.HandlerFunc(s.releaseHandler.ServeHTTP))
		mux.Handle("/api/v1/releases/", authHandler)
	}

	// Audit logs (auth required).
	if s.auditHandler != nil && s.AuthMiddleware != nil {
		mux.Handle("/api/v1/audit-logs", s.AuthMiddleware(s.auditHandler))
	}

	// Health check (no auth).
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`)) //nolint:errcheck // best-effort
	})

	var h http.Handler = mux
	if s.AuditMiddleware != nil {
		h = s.AuditMiddleware(h)
	}

	return withLogging(s.log, h)
}

// ListenAndServe starts the HTTP server and blocks until shutdown.
func (s *Server) ListenAndServe() error {
	handler := s.BuildMux()

	s.httpServer = &http.Server{
		Addr:         s.cfg.HTTPAddr,
		Handler:      handler,
		ReadTimeout:  s.cfg.ReadTimeout,
		WriteTimeout: s.cfg.WriteTimeout,
		IdleTimeout:  120 * time.Second,
	}

	s.log.Info("HTTP server listening", "addr", s.cfg.HTTPAddr)
	if err := s.httpServer.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(_ time.Duration) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Close()
}

func (s *Server) apiKeyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIKey == "" {
			s.log.Info("WARNING: REST API has no API key configured — endpoints are unprotected")
			next.ServeHTTP(w, r)
			return
		}

		key := r.Header.Get("X-API-Key")
		if key == "" {
			key = r.URL.Query().Get("api_key")
		}
		if key != s.cfg.APIKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"}) //nolint:errcheck // best-effort
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withLogging wraps an HTTP handler with request logging.
func withLogging(log logr.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &loggingRW{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(lrw, r)
		log.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", lrw.statusCode,
			"duration", time.Since(start).String(),
		)
	})
}

type loggingRW struct {
	http.ResponseWriter
	statusCode int
}

func (l *loggingRW) WriteHeader(code int) {
	l.statusCode = code
	l.ResponseWriter.WriteHeader(code)
}

// Package manager 实现 release-manager 中心通知服务。
//
// 该服务提供两大功能:
//  1. 接收 Harbor webhook，解析 Helm chart 推送事件，转发到目标客户集群
//  2. 管理客户白名单（REST API），控制哪些客户接收自动更新
//
// 安全: webhook 端点通过 HMAC-SHA256 签名验证，管理 API 通过 X-API-Key 认证。
package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/ndzuki/release-manager/internal/config"
	releasev1 "github.com/ndzuki/release-manager/api/gen/release/v1"
)

// Server 是 release-manager 服务的核心结构体。
type Server struct {
	cfg           *config.Config
	log           logr.Logger
	store         Store               // 客户和发布记录存储
	cache         *Cache              // 内存缓存层
	webhook       *WebhookHandler     // Harbor webhook 处理器
	forwarder     *Forwarder          // gRPC 通知转发器
	dingtalk      *DingTalkClient     // 钉钉通知客户端
	chartConfig   *ChartConfigHandler // chart 部署配置管理
	authMiddleware *AuthMiddleware    // 多租户认证中间件
	initHandler    *InitHandler       // 首次初始化处理器
	casbinRBAC     *CasbinRBAC
	userRBAC       *UserRBACHandler
	grpcServer    *grpc.Server
	httpServer    *http.Server
}

// NewServer 创建 release-manager 服务实例。
func NewServer(cfg *config.Config, log logr.Logger) (*Server, error) {
	var store Store
	switch cfg.Store.Type {
	case "postgres":
		pgStore, err := NewPostgreSQLStore(cfg.Store.DSN, log)
		if err != nil {
			return nil, fmt.Errorf("connect postgres: %w", err)
		}
		store = pgStore
	case "sqlite":
		dsn := cfg.Store.DSN
		if dsn == "" {
			dsn = "data/release-manager.db"
		}
		if idx := strings.LastIndex(dsn, "/"); idx > 0 {
			if err := os.MkdirAll(dsn[:idx], 0o755); err != nil {
				return nil, fmt.Errorf("create store dir: %w", err)
			}
		}
		sqliteStore, err := NewSQLiteStore(dsn, log)
		if err != nil {
			log.Error(err, "failed to open SQLite, falling back to memory store")
			store = NewMemoryStore(log)
		} else {
			store = sqliteStore
		}
	default:
		store = NewMemoryStore(log)
	}

	forwarder := NewForwarder(store, &cfg.TLS, log, 60*time.Second)
	dingtalk := NewDingTalkClient(cfg.DingTalk.WebhookURL, cfg.DingTalk.Secret, log)
	cache := NewCache(1000)
	authM := NewAuthMiddleware(log, NewAPIKeyAuth(cfg.APIKey, log), NewSessionAuth(cache, 24*time.Hour, log))
	chartCfg := NewChartConfigHandler(store, cache, log)

	// 构建 SMTP 配置
	smtpCfg := SMTPConfig{
		Host: cfg.SMTP.Host, Port: cfg.SMTP.Port,
		Username: cfg.SMTP.Username, Password: cfg.SMTP.Password,
		From: cfg.SMTP.From, Enabled: cfg.SMTP.Enabled,
	}
	initH := NewInitHandler(store, smtpCfg, cfg.DevMode, log)

	// Casbin RBAC
	rbac, err := NewCasbinRBAC(log)
	if err != nil { return nil, fmt.Errorf("init casbin rbac: %w", err) }
	userRBAC := NewUserRBACHandler(rbac, store, log)

	s := &Server{
		initHandler: initH,
		casbinRBAC:  rbac,
		userRBAC:    userRBAC,
		cfg:       cfg,
		cache:     cache,
		chartConfig: chartCfg,
		authMiddleware: authM,
		log:       log.WithName("manager"),
		store:     store,
		forwarder: forwarder,
		dingtalk:  dingtalk,
	}

	s.webhook = NewWebhookHandler(log, cfg.Harbor.WebhookHMACSecret, s.onReleaseNotification)

	return s, nil
}

// onReleaseNotification 是 Harbor webhook 接收后的回调。
// 将发布通知转发到所有启用的客户，并通过钉钉通知运维团队。
func (s *Server) onReleaseNotification(notification ReleaseNotification) error {
	s.log.Info("processing release notification",
		"chart", notification.ChartName,
		"version", notification.ChartVersion,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	results, err := s.forwarder.ForwardToAll(ctx, notification)
	if err != nil {
		return fmt.Errorf("forward to customers: %w", err)
	}

	// 钉钉通知为尽力而为，不因钉钉失败而阻塞主流程
	if s.cfg.DingTalk.Enabled {
		if err := s.dingtalk.SendReleaseNotification(
			notification.ChartName,
			notification.ChartVersion,
			results,
		); err != nil {
			s.log.Error(err, "failed to send DingTalk notification")
		}
	}

	return nil
}

// Run 启动服务并阻塞直到收到关闭信号。
func (s *Server) Run(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error { return s.serveGRPC(ctx) })
	g.Go(func() error { return s.serveHTTP(ctx) })

	g.Go(func() error {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		select {
		case sig := <-sigCh:
			s.log.Info("received signal, shutting down", "signal", sig)
			shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.Server.ShutdownTimeout)
			defer cancel()

			if s.httpServer != nil {
				s.httpServer.Shutdown(shutdownCtx)
			}
			if s.grpcServer != nil {
				s.grpcServer.GracefulStop()
			}
			// 关闭数据存储连接
			if s.store != nil {
				s.store.Close()
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	if err := g.Wait(); err != nil && err != context.Canceled {
		return err
	}
	return nil
}

// serveGRPC 启动 gRPC 服务器（客户管理 API 和状态上报接收）。
func (s *Server) serveGRPC(ctx context.Context) error {
	if s.cfg.Server.GRPCAddr == "" {
		return nil
	}

	var opts []grpc.ServerOption
	if s.cfg.TLS.CertFile != "" {
		tlsCfg, err := s.cfg.TLS.BuildTLSConfig()
		if err != nil {
			return fmt.Errorf("build TLS config: %w", err)
		}
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	}

	s.grpcServer = grpc.NewServer(opts...)
	svc := &customerManagementServer{store: s.store, log: s.log}
	releasev1.RegisterCustomerManagementServiceServer(s.grpcServer, svc)
	// Register StatusReportService so operators can report back release results.
	statusSvc := &statusReportServer{store: s.store, log: s.log}
	releasev1.RegisterStatusReportServiceServer(s.grpcServer, statusSvc)

	lis, err := net.Listen("tcp", s.cfg.Server.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen gRPC: %w", err)
	}

	s.log.Info("gRPC server listening", "addr", s.cfg.Server.GRPCAddr)

	go func() {
		<-ctx.Done()
		s.grpcServer.GracefulStop()
	}()

	return s.grpcServer.Serve(lis)
}

// serveHTTP 启动 HTTP 服务器（webhook 接收 + REST 管理 API）。
func (s *Server) serveHTTP(ctx context.Context) error {
	if s.cfg.Server.HTTPAddr == "" {
		return nil
	}

	mux := http.NewServeMux()

	// 初始化 + 登录 (无需认证)
	mux.Handle("/api/v1/init", s.initHandler)
	mux.HandleFunc("/api/v1/auth/login", s.initHandler.HandleLogin)

	// Webhook 端点（HMAC 签名验证，不需要认证）
	mux.Handle("/api/v1/webhook/harbor", s.webhook)

	// Chart 部署配置 & 监控面板（多租户认证）
	mux.Handle("/api/v1/orgs/", s.authMiddleware.Handler(s.chartConfig))
	mux.Handle("/api/v1/dashboard/", s.authMiddleware.Handler(s.chartConfig))

	// 用户角色管理（多租户认证 + Casbin RBAC）
	mux.Handle("/api/v1/users", s.authMiddleware.Handler(s.userRBAC))
	mux.Handle("/api/v1/users/", s.authMiddleware.Handler(s.userRBAC))

	// 管理 API（API Key 认证保护，向后兼容）
	authHandler := s.apiKeyMiddleware(http.HandlerFunc(s.routeREST))
	mux.Handle("/api/v1/customers", authHandler)
	mux.Handle("/api/v1/customers/", authHandler)
	mux.Handle("/api/v1/releases/", authHandler)

	// 健康检查（无需认证）
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	s.httpServer = &http.Server{
		Addr:         s.cfg.Server.HTTPAddr,
		Handler:      withLogging(s.log, mux),
		ReadTimeout:  s.cfg.Server.ReadTimeout,
		WriteTimeout: s.cfg.Server.WriteTimeout,
		IdleTimeout:  120 * time.Second,
	}

	s.log.Info("HTTP server listening", "addr", s.cfg.Server.HTTPAddr)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.Server.ShutdownTimeout)
		defer cancel()
		s.httpServer.Shutdown(shutdownCtx)
	}()

	if err := s.httpServer.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("HTTP server: %w", err)
	}
	return nil
}

// =============================================================================
// 认证中间件
// =============================================================================

// apiKeyMiddleware 通过 X-API-Key header 验证 API 请求。
// 未配置 APIKey 时输出警告但不拒绝请求（向后兼容）。
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
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// routeREST 根据 URL 路径路由 REST 请求。
func (s *Server) routeREST(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/api/v1/customers":
		s.handleCustomers(w, r)
	case strings.HasPrefix(path, "/api/v1/customers/"):
		s.handleCustomer(w, r)
	case strings.HasPrefix(path, "/api/v1/releases/"):
		s.handleReleases(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// =============================================================================
// REST API Handler — 客户管理
// =============================================================================

// 列出所有客户
// @Summary      列出所有客户
// @Description  返回所有已注册的客户列表，支持按启用状态过滤
// @Tags         客户管理
// @Accept       json
// @Produce      json
// @Param        enabled  query     bool    false  "是否只返回已启用客户"
// @Success      200      {array}   Customer  "客户列表"
// @Failure      500      {object}  map[string]string  "内部错误"
// @Security     ApiKeyAuth
// @Router       /api/v1/customers [get]
func (s *Server) handleCustomers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		enabledOnly := r.URL.Query().Get("enabled") == "true"
		customers, err := s.store.ListCustomers(enabledOnly)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if customers == nil {
			customers = []Customer{}
		}
		writeJSON(w, http.StatusOK, customers)

	// 创建新客户
	// @Summary      创建新客户
	// @Description  注册一个新的客户到白名单中
	// @Tags         客户管理
	// @Accept       json
	// @Produce      json
	// @Param        request  body      CreateCustomerRequest  true  "客户信息"
	// @Success      201      {object}  Customer               "创建成功"
	// @Failure      400      {object}  map[string]string      "参数校验失败"
	// @Failure      409      {object}  map[string]string      "客户已存在"
	// @Security     ApiKeyAuth
	// @Router       /api/v1/customers [post]
	case http.MethodPost:
		var c Customer
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		// 必填字段校验
		if c.ID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
			return
		}
		if c.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
			return
		}
		if c.OperatorEndpoint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "operator_endpoint is required"})
			return
		}
		created, err := s.store.CreateCustomer(c)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, created)

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleCustomer 处理单个客户的 GET/PUT/DELETE 操作。
//
// 获取单个客户
// @Summary      获取客户详情
// @Description  根据客户 ID 获取客户信息
// @Tags         客户管理
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "客户 ID"
// @Success      200  {object}  Customer  "客户信息"
// @Failure      404  {object}  map[string]string  "客户不存在"
// @Security     ApiKeyAuth
// @Router       /api/v1/customers/{id} [get]
//
// 更新客户信息
// @Summary      更新客户信息
// @Description  更新指定客户的信息（支持部分更新）
// @Tags         客户管理
// @Accept       json
// @Produce      json
// @Param        id       path      string                true  "客户 ID"
// @Param        request  body      UpdateCustomerRequest true  "要更新的字段"
// @Success      200      {object}  Customer              "更新后的客户信息"
// @Failure      404      {object}  map[string]string     "客户不存在"
// @Security     ApiKeyAuth
// @Router       /api/v1/customers/{id} [put]
//
// 删除客户
// @Summary      删除客户
// @Description  从白名单中移除指定客户
// @Tags         客户管理
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "客户 ID"
// @Success      200  {object}  map[string]string  "删除成功"
// @Failure      404  {object}  map[string]string  "客户不存在"
// @Security     ApiKeyAuth
// @Router       /api/v1/customers/{id} [delete]
func (s *Server) handleCustomer(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/customers/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "customer ID required"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		c, err := s.store.GetCustomer(id)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, c)

	case http.MethodPut:
		var req UpdateCustomerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		c := Customer{ID: id}
		if req.Name != nil { c.Name = *req.Name }
		if req.OperatorEndpoint != nil { c.OperatorEndpoint = *req.OperatorEndpoint }
		if req.CertFingerprint != nil { c.CertFingerprint = *req.CertFingerprint }
		if req.Enabled != nil { c.Enabled = *req.Enabled }
		if req.Labels != nil { c.Labels = req.Labels }
		updated, err := s.store.UpdateCustomer(c)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			} else {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			}
			return
		}
		writeJSON(w, http.StatusOK, updated)

	case http.MethodDelete:
		if err := s.store.DeleteCustomer(id); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"deleted": "true"})

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// 查询发布记录
// @Summary      查询发布记录
// @Description  根据请求 ID 查询发布状态记录
// @Tags         发布管理
// @Accept       json
// @Produce      json
// @Param        requestId  path      string  true  "请求 ID"
// @Success      200        {array}   ReleaseRecord  "发布记录列表"
// @Failure      500        {object}  map[string]string  "内部错误"
// @Security     ApiKeyAuth
// @Router       /api/v1/releases/{requestId} [get]
func (s *Server) handleReleases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	requestID := strings.TrimPrefix(r.URL.Path, "/api/v1/releases/")
	records, err := s.store.ListReleaseRecords(requestID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if records == nil {
		records = []ReleaseRecord{}
	}
	writeJSON(w, http.StatusOK, records)
}

// =============================================================================
// gRPC CustomerManagementService 桩实现
// =============================================================================

type customerManagementServer struct {
	releasev1.UnimplementedCustomerManagementServiceServer
	store Store
	log   logr.Logger
}

// =============================================================================
// 通用工具函数
// =============================================================================

// writeJSON 以 JSON 格式写入 HTTP 响应。
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// withLogging 为 HTTP handler 添加请求日志中间件。
func withLogging(log logr.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.V(1).Info("HTTP request",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(start).String(),
		)
	})
}

// =============================================================================
// gRPC StatusReportService — 接收 operator 上报的 release 结果
// =============================================================================

type statusReportServer struct {
	releasev1.UnimplementedStatusReportServiceServer
	store Store
	log   logr.Logger
}

// ReportStatus records a release status report from an operator.
func (s *statusReportServer) ReportStatus(ctx context.Context, req *releasev1.ReportStatusRequest) (*releasev1.ReportStatusResponse, error) {
	s.log.Info("received release status report",
		"customer_id", req.CustomerId,
		"request_id", req.RequestId,
		"chart", req.ChartName,
		"version", req.ChartVersion,
		"status", req.Status,
	)

	record := ReleaseRecord{
		RequestID:       req.RequestId,
		CustomerID:      req.CustomerId,
		ChartName:       req.ChartName,
		ChartVersion:    req.ChartVersion,
		Status:          req.Status.String(),
		ErrorMessage:    req.ErrorMessage,
		DurationSecs: req.DurationSeconds,
		StartedAt:    time.Unix(req.StartedAt, 0),
		CompletedAt:  time.Now(),
	}

	if err := s.store.CreateReleaseRecord(record); err != nil {
		s.log.Error(err, "failed to create release record")
		return &releasev1.ReportStatusResponse{
			Acknowledged: false,
			Message:      err.Error(),
		}, nil
	}

	return &releasev1.ReportStatusResponse{
		Acknowledged: true,
		Message:      "status recorded",
	}, nil
}

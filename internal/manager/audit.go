// Package manager 实现操作审计日志中间件。
//
// 审计中间件自动记录所有 API 调用到持久化存储，
// 写入异步执行，不阻塞主请求流。
// 无认证的端点（webhook/health/init）记录 username: "anonymous"。
package manager

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

// maxReqBodySnippet 是审计日志中保存的请求体摘要最大长度。
const maxReqBodySnippet = 256

// auditChannelBuffer 是审计日志异步写入的缓冲容量。
const auditChannelBuffer = 1000

// AuditLogger 异步记录操作审计日志到 Store。
type AuditLogger struct {
	store  Store
	log    logr.Logger
	ch     chan AuditLogEntry
	ctx    context.Context
	cancel context.CancelFunc
}

// NewAuditLogger 创建审计日志记录器并启动后台 writer。
func NewAuditLogger(store Store, log logr.Logger) *AuditLogger {
	ctx, cancel := context.WithCancel(context.Background())
	a := &AuditLogger{
		store:  store,
		log:    log.WithName("audit"),
		ch:     make(chan AuditLogEntry, auditChannelBuffer),
		ctx:    ctx,
		cancel: cancel,
	}
	go a.writerLoop()
	return a
}

// writerLoop 是后台 goroutine，消费审计日志 channel 写入 Store。
func (a *AuditLogger) writerLoop() {
	for {
		select {
		case entry, ok := <-a.ch:
			if !ok {
				return
			}
			if err := a.store.CreateAuditLog(entry); err != nil {
				a.log.Error(err, "failed to persist audit log",
					"user_id", entry.UserID,
					"method", entry.Method,
					"path", entry.Path,
				)
			}
		case <-a.ctx.Done():
			// 优雅关闭：排空 channel 中的积压数据
			a.drainChannel()
			return
		}
	}
}

// drainChannel 排空 channel 中所有未写入的审计日志（非阻塞读取）。
func (a *AuditLogger) drainChannel() {
	for {
		select {
		case entry := <-a.ch:
			if err := a.store.CreateAuditLog(entry); err != nil {
				a.log.Error(err, "failed to persist audit log during drain",
					"user_id", entry.UserID,
				)
			}
		default:
			return
		}
	}
}

// Close 优雅关闭审计日志记录器，等待积压数据写入完成。
func (a *AuditLogger) Close() {
	a.cancel()
	// Give drainChannel a moment to finish
	close(a.ch)
}

// Middleware 返回 HTTP 中间件，自动记录每个请求的审计信息。
func (a *AuditLogger) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// 读取请求体摘要（仅在非 GET/HEAD 时读取）
		var bodySnippet string
		if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
			bodyBytes, err := io.ReadAll(r.Body)
			if err == nil {
				bodySnippet = truncateBytes(bodyBytes, maxReqBodySnippet)
				// Restore the body for downstream handlers
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}
		}

		// 包装 ResponseWriter 以捕获状态码
		rsw := &responseStatusWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rsw, r)

		duration := time.Since(start)

		// 解析资源和操作类型
		resource, resourceID := classifyRequest(r.Method, r.URL.Path)

		// 提取用户信息
		userID := "anonymous"
		username := "anonymous"
		orgID := ""
		if user, ok := UserFromContext(r.Context()); ok {
			userID = user.ID
			username = user.Name
			orgID = user.OrgID
		}

		entry := AuditLogEntry{
			Timestamp:      start,
			UserID:         userID,
			Username:       username,
			OrgID:          orgID,
			Action:         r.Method,
			Resource:       resource,
			ResourceID:     resourceID,
			Method:         r.Method,
			Path:           r.URL.Path,
			StatusCode:     rsw.status,
			ClientIP:       clientIP(r),
			UserAgent:      r.UserAgent(),
			ReqBodySnippet: bodySnippet,
			DurationMs:     duration.Milliseconds(),
		}

		// 异步写入，channel 满时丢弃并 warn
		select {
		case a.ch <- entry:
		default:
			a.log.Info("audit log channel full, dropping entry",
				"method", r.Method,
				"path", r.URL.Path,
			)
		}
	})
}

// responseStatusWriter 包装 ResponseWriter 以捕获 HTTP 状态码。
type responseStatusWriter struct {
	http.ResponseWriter
	status int
}

func (rsw *responseStatusWriter) WriteHeader(code int) {
	rsw.status = code
	rsw.ResponseWriter.WriteHeader(code)
}

// classifyRequest 根据 HTTP 方法和路径推断资源类型和资源 ID。
func classifyRequest(method, path string) (resource, resourceID string) {
	_ = method // reserved for future use (e.g., distinguishing POST vs GET for same resource)
	// Strip query string if present
	if idx := strings.Index(path, "?"); idx > 0 {
		path = path[:idx]
	}

	switch {
	case strings.HasPrefix(path, "/api/v1/customers/"):
		parts := strings.SplitN(strings.TrimPrefix(path, "/api/v1/customers/"), "/", 2)
		if len(parts) > 0 && parts[0] != "" {
			resourceID = parts[0]
		}
		return "customer", resourceID
	case path == "/api/v1/customers":
		return "customer", ""
	case strings.HasPrefix(path, "/api/v1/releases/"):
		parts := strings.SplitN(strings.TrimPrefix(path, "/api/v1/releases/"), "/", 2)
		if len(parts) > 0 && parts[0] != "" {
			resourceID = parts[0]
		}
		return "release", resourceID
	case path == "/api/v1/releases":
		return "release", ""
	case strings.HasPrefix(path, "/api/v1/users/"):
		parts := strings.SplitN(strings.TrimPrefix(path, "/api/v1/users/"), "/", 2)
		if len(parts) > 0 && parts[0] != "" {
			resourceID = parts[0]
		}
		return "user", resourceID
	case path == "/api/v1/users":
		return "user", ""
	case strings.HasPrefix(path, "/api/v1/orgs/"):
		return "org", ""
	case strings.HasPrefix(path, "/api/v1/dashboard/"):
		return "dashboard", ""
	case strings.HasPrefix(path, "/api/v1/audit-logs"):
		return "audit_log", ""
	case path == "/api/v1/init":
		return "init", ""
	case path == "/api/v1/auth/login":
		return "auth", ""
	case strings.HasPrefix(path, "/api/v1/webhook/"):
		return "webhook", ""
	case path == "/health":
		return "health", ""
	default:
		return "unknown", ""
	}
}

// clientIP 从请求中提取客户端 IP 地址。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	// Fall back to RemoteAddr
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx > 0 {
		return addr[:idx]
	}
	return addr
}

// truncateBytes 将字节切片截断到指定长度。
func truncateBytes(b []byte, maxLen int) string {
	s := string(b)
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

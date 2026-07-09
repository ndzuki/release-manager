// Package manager 实现 Harbor webhook 处理和通知转发。
//
// 入口 WebhookHandler.ServeHTTP 接收 Harbor PUSH_HELMCHART 事件，
// 解析 payload 后触发 release 通知流的开始。
package manager

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
)

// HarborWebhookPayload 是 Harbor webhook PUSH_HELMCHART 事件的 payload 结构。
// 参考: https://goharbor.io/docs/latest/working-with-projects/project-configuration/configure-webhooks/
type HarborWebhookPayload struct {
	Type      string `json:"type"`     // 事件类型，如 "PUSH_HELMCHART"
	OccurAt   int64  `json:"occur_at"` // 事件发生时间（Unix 时间戳）
	Operator  string `json:"operator"` // 操作者
	EventData struct {
		Resources  []HarborResource `json:"resources"`  // 推送的 chart 资源列表
		Repository HarborRepository `json:"repository"` // 仓库元数据
	} `json:"event_data"`
}

// HarborResource 表示一个推送的 Helm chart 资源。
type HarborResource struct {
	Digest      string            `json:"digest"`       // 内容摘要
	Tag         string            `json:"tag"`          // chart 版本号
	ResourceURL string            `json:"resource_url"` // OCI 资源 URL
	Labels      map[string]string `json:"labels,omitempty"`
}

// HarborRepository 表示 Harbor 仓库元数据。
type HarborRepository struct {
	Name         string `json:"name"`           // 如 "helm/magic-sandbox"
	Namespace    string `json:"namespace"`      // 项目名
	RepoFullName string `json:"repo_full_name"` // 如 "library/helm/magic-sandbox"
	RepoType     string `json:"repo_type"`      // 仓库类型，"CHART"
}

// ReleaseNotification 是从 Harbor webhook 中提取的发布通知数据。
type ReleaseNotification struct {
	ChartName    string            `json:"chart_name"`       // Helm chart 名称
	ChartVersion string            `json:"chart_version"`    // chart 版本
	ChartURL     string            `json:"chart_url"`        // OCI chart URL
	ProjectName  string            `json:"project_name"`     // Harbor 项目名
	Images       map[string]string `json:"images,omitempty"` // 组件→镜像 tag 映射
	OccurredAt   time.Time         `json:"occurred_at"`      // 事件发生时间
}

// 请求体最大 1 MiB，防止 OOM。
const maxBodySize = 1 << 20

// WebhookHandler 处理 Harbor webhook HTTP 请求。
type WebhookHandler struct {
	log      logr.Logger
	hmacKey  []byte
	notifier func(notification ReleaseNotification) error // 通知回调
}

// NewWebhookHandler 创建 WebhookHandler。
// hmacKey 为空时跳过 HMAC 签名验证。
func NewWebhookHandler(log logr.Logger, hmacKey string, notifier func(ReleaseNotification) error) *WebhookHandler {
	return &WebhookHandler{
		log:      log.WithName("webhook"),
		hmacKey:  []byte(hmacKey),
		notifier: notifier,
	}
}

// ServeHTTP 处理 Harbor webhook HTTP POST 请求。
//
// 流程:
//  1. 校验 Content-Type 和 Method
//  2. 限制请求体大小（1 MiB）
//  3. 验证 HMAC-SHA256 签名（若配置）
//  4. 解析 JSON payload
//  5. 仅处理 PUSH_HELMCHART 事件，调用 notifier 回调
//
// 接收 Harbor webhook
// @Summary      接收 Harbor webhook
// @Description  处理 Harbor PUSH_HELMCHART 事件，验证 HMAC 签名并转发到客户集群
// @Tags         Webhook
// @Accept       json
// @Produce      json
// @Param        request  body      HarborWebhookPayload  true  "Harbor webhook 载荷"
// @Success      200      {object}  map[string]string      "处理成功"
// @Failure      400      {object}  map[string]string      "无效请求"
// @Failure      500      {object}  map[string]string      "处理失败"
// @Security     HmacSignature
// @Router       /api/v1/webhook/harbor [post]
func (s *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
		return
	}

	// 限制请求体大小，防止内存耗尽
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.log.Error(err, "failed to read webhook body")
		if strings.Contains(err.Error(), "http: request body too large") {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "bad request", http.StatusBadRequest)
		}
		return
	}
	defer r.Body.Close()

	// HMAC 签名验证
	if len(s.hmacKey) > 0 {
		if err := s.verifySignature(r, body); err != nil {
			s.log.Error(err, "HMAC signature verification failed")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	var payload HarborWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		s.log.Error(err, "failed to parse webhook payload")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	s.log.Info("received Harbor webhook",
		"type", payload.Type,
		"operator", payload.Operator,
	)

	// 仅处理 Helm chart 推送事件
	if payload.Type != "PUSH_HELMCHART" {
		s.log.V(1).Info("skipping non-chart event", "type", payload.Type)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message":"ignored"}`))
		return
	}

	// 逐个处理推送的 chart 资源
	for _, res := range payload.EventData.Resources {
		notification := ReleaseNotification{
			ChartName:    payload.EventData.Repository.Name,
			ChartVersion: res.Tag,
			ChartURL:     res.ResourceURL,
			ProjectName:  payload.EventData.Repository.Namespace,
			OccurredAt:   time.Unix(payload.OccurAt, 0),
		}

		s.log.Info("processing chart release",
			"chart", notification.ChartName,
			"version", notification.ChartVersion,
		)

		if err := s.notifier(notification); err != nil {
			s.log.Error(err, "failed to process release notification",
				"chart", notification.ChartName,
			)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"message":"accepted"}`))
}

// verifySignature 验证 Harbor webhook 的 HMAC-SHA256 签名。
// Harbor 在 Authorization header 中发送签名:
//
//	Authorization: Harbor-Signature <base64-encoded-hmac-sha256>
//
// 使用 hmac.Equal 进行常数时间比较，防止时序攻击。
func (s *WebhookHandler) verifySignature(r *http.Request, body []byte) error {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return fmt.Errorf("missing Authorization header")
	}

	const prefix = "Harbor-Signature "
	if !strings.HasPrefix(authHeader, prefix) {
		return fmt.Errorf("invalid Authorization header format, expected 'Harbor-Signature <base64>'")
	}

	encodedSig := strings.TrimPrefix(authHeader, prefix)
	receivedSig, err := base64.StdEncoding.DecodeString(encodedSig)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}

	// 计算期望的 HMAC-SHA256
	mac := hmac.New(sha256.New, s.hmacKey)
	mac.Write(body)
	expectedSig := mac.Sum(nil)

	// 常数时间比较
	if !hmac.Equal(receivedSig, expectedSig) {
		return fmt.Errorf("HMAC signature mismatch")
	}

	return nil
}

// GenerateRequestID 生成唯一的请求追踪 ID（UUID v4）。
func GenerateRequestID() string {
	return uuid.New().String()
}

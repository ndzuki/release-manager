// Package manager 实现按客户定制的 Chart 部署配置管理。
//
// 核心概念:
//   - ChartDefinition: 可用 chart 模板（名称、OCI URL、默认 values）
//   - CustomerChartBinding: 将 chart 绑定到客户，支持客户专属 values 覆盖
//   - 部署顺序: DeployOrder 控制依赖图，低值先部署
//
// REST API:
//   GET    /api/v1/orgs/{orgId}/charts                     # 列出组织下的 chart 定义
//   POST   /api/v1/orgs/{orgId}/charts                     # 创建 chart 定义
//   GET    /api/v1/orgs/{orgId}/customers/{custId}/charts  # 列出客户分配的 charts
//   POST   /api/v1/orgs/{orgId}/customers/{custId}/charts  # 为客户分配 chart
//   DELETE /api/v1/orgs/{orgId}/customers/{custId}/charts/{bindingId} # 移除分配
package manager

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

// ChartConfigHandler 处理 chart 部署配置的 HTTP 请求。
type ChartConfigHandler struct {
	store Store
	cache *Cache
	log   logr.Logger
}

func NewChartConfigHandler(store Store, cache *Cache, log logr.Logger) *ChartConfigHandler {
	return &ChartConfigHandler{store: store, cache: cache, log: log.WithName("chart-config")}
}

// ServeHTTP 路由 chart 配置请求。
func (h *ChartConfigHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/")

	switch {
	// GET/POST /api/v1/orgs/{orgId}/charts
	case matchPath(path, "orgs/*/charts") && !strings.Contains(path, "/customers/"):
		h.handleCharts(w, r)

	// GET /api/v1/orgs/{orgId}/customers/{custId}/charts
	case matchPath(path, "orgs/*/customers/*/charts"):
		h.handleCustomerCharts(w, r)

	// GET /api/v1/dashboard/overview
	case path == "dashboard/overview":
		h.handleOverview(w, r)

	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// handleCharts 处理 chart 定义的 CRUD。
func (h *ChartConfigHandler) handleCharts(w http.ResponseWriter, r *http.Request) {
	orgID := extractSegment(r.URL.Path, "orgs")

	switch r.Method {
	case http.MethodGet:
		charts, err := h.store.ListChartDefinitions(orgID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if charts == nil {
			charts = []ChartDefinition{}
		}
		writeJSON(w, http.StatusOK, charts)

	case http.MethodPost:
		var req CreateChartDefinitionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		chart := ChartDefinition{
			ID:            generateID("chart"),
			OrgID:         orgID,
			Name:          req.Name,
			Description:   req.Description,
			OCIURL:        req.OCIURL,
			DefaultValues: req.DefaultValues,
			Labels:        req.Labels,
			Enabled:       true,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
		}
		created, err := h.store.CreateChartDefinition(chart)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, created)

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleCustomerCharts 处理客户-chart 分配的 CRUD。
func (h *ChartConfigHandler) handleCustomerCharts(w http.ResponseWriter, r *http.Request) {
	orgID := extractSegment(r.URL.Path, "orgs")
	custID := extractSegment(r.URL.Path, "customers")

	switch r.Method {
	case http.MethodGet:
		bindings, err := h.store.ListCustomerChartBindings(orgID, custID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if bindings == nil {
			bindings = []CustomerChartBinding{}
		}
		writeJSON(w, http.StatusOK, bindings)

	case http.MethodDelete:
		bindingID := extractSegment(r.URL.Path, "charts")
		if bindingID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "binding ID required in path"})
			return
		}
		if err := h.store.DeleteCustomerChartBinding(orgID, bindingID); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		h.cache.InvalidateCustomerStatus(custID)
		writeJSON(w, http.StatusOK, map[string]string{"deleted": "true"})

	case http.MethodPost:
		var req CreateCustomerChartBindingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		// 获取 chart 定义以获取 chartName
		chart, err := h.store.GetChartDefinition(orgID, req.ChartID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "chart not found: " + err.Error()})
			return
		}

		binding := CustomerChartBinding{
			ID:          generateID("binding"),
			OrgID:       orgID,
			CustomerID:  custID,
			ChartID:     req.ChartID,
			ChartName:   chart.Name,
			Enabled:     true,
			ReleaseName: req.ReleaseName,
			Namespace:   req.Namespace,
			CustomValues: req.CustomValues,
			DeployOrder:  req.DeployOrder,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		if binding.ReleaseName == "" {
			binding.ReleaseName = chart.Name
		}
		if binding.Namespace == "" {
			binding.Namespace = "default"
		}

		created, err := h.store.CreateCustomerChartBinding(binding)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		// 使缓存失效
		h.cache.InvalidateCustomerStatus(custID)
		writeJSON(w, http.StatusCreated, created)

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleOverview 返回系统概览（监控面板）。
func (h *ChartConfigHandler) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	// 尝试从缓存获取
	if overview, ok := h.cache.GetSystemOverview(); ok {
		writeJSON(w, http.StatusOK, overview)
		return
	}

	// 构建概览数据
	customers, err := h.store.ListCustomers(false)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list customers: " + err.Error()})
		return
	}
	enabledCount := 0
	for _, c := range customers {
		if c.Enabled {
			enabledCount++
		}
	}

	recentReleases, err := h.store.ListReleaseRecords("")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list releases: " + err.Error()})
		return
	}
	// 只取最近 20 条
	if len(recentReleases) > 20 {
		recentReleases = recentReleases[:20]
	}

	overview := &SystemOverview{
		TotalCustomers:   len(customers),
		EnabledCustomers: enabledCount,
		RecentReleases:   recentReleases,
	}

	// 计算成功率（简化）
	if len(recentReleases) > 0 {
		successCount := 0
		for _, r := range recentReleases {
			if r.Status == "SUCCEEDED" {
				successCount++
			}
		}
		overview.ReleaseSuccessRate = float64(successCount) / float64(len(recentReleases)) * 100
	}

	h.cache.SetSystemOverview(overview)
	writeJSON(w, http.StatusOK, overview)
}

// =============================================================================
// URL 路径工具函数
// =============================================================================

// matchPath 简单路径匹配，* 匹配单个路径段。
func matchPath(path, pattern string) bool {
	pathParts := strings.Split(strings.Trim(path, "/"), "/")
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	if len(pathParts) != len(patternParts) {
		return false
	}
	for i, p := range patternParts {
		if p == "*" {
			continue
		}
		if !strings.EqualFold(pathParts[i], p) {
			return false
		}
	}
	return true
}

// extractSegment 从路径中提取指定段落之后的值。
// extractSegment("orgs/abc/customers/xyz/charts", "orgs") → "abc"
// extractSegment("orgs/abc/customers/xyz/charts", "customers") → "xyz"
func extractSegment(path, segment string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if strings.EqualFold(p, segment) && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func generateID(prefix string) string {
	return prefix + "-" + time.Now().Format("20060102150405")
}

// Package manager 定义 release-manager 平台的核心数据模型。
//
// 支持多租户架构:
//   - Organization（组织/租户）→ Users（用户）→ Customers（客户）
//   - ChartDefinition（可用 chart 目录）→ CustomerChartBinding（客户-chart 分配）
package manager

import "time"

// =============================================================================
// 多租户模型
// =============================================================================

// Organization 表示一个组织（租户），数据完全隔离。
type Organization struct {
	ID        string    `json:"id"`         // 组织唯一标识
	Name      string    `json:"name"`       // 组织名称
	AdminUser string    `json:"admin_user"` // 管理员用户 ID
	Enabled   bool      `json:"enabled"`    // 是否启用
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// User 表示平台用户，属于某个组织。
type User struct {
	ID             string    `json:"id"`              // 用户唯一标识
	OrgID          string    `json:"org_id"`          // 所属组织 ID
	Name           string    `json:"name"`            // 显示名称
	Email          string    `json:"email"`           // 邮箱（OIDC/LDAP 映射键）
	Role           string    `json:"role"`            // admin, operator, viewer
	AuthProvider   string    `json:"auth_provider"`   // oidc, ldap, dingtalk
	ExternalID     string    `json:"external_id"`     // 外部认证系统的用户 ID
	DingTalkUserID string    `json:"dingtalk_user_id"` // 钉钉扫码登录的 userid
	Enabled        bool      `json:"enabled"`
	LastLoginAt    time.Time `json:"last_login_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// =============================================================================
// Chart 部署配置
// =============================================================================

// ChartDefinition 定义可用的 Helm chart 模板。
type ChartDefinition struct {
	ID              string            `json:"id"`                // chart 唯一标识
	OrgID           string            `json:"org_id"`            // 所属组织
	Name            string            `json:"name"`              // chart 名称，如 magic-sandbox
	Description     string            `json:"description"`       // 描述
	OCIURL          string            `json:"oci_url"`           // OCI 仓库 URL
	DefaultValues   map[string]any    `json:"default_values"`    // 默认 values
	RequiredParams  []string          `json:"required_params"`   // 必填参数列表
	VersionTemplate string            `json:"version_template"`  // 版本模板，如 "0.0.{{.BuildNum}}"
	Labels          map[string]string `json:"labels"`
	Enabled         bool              `json:"enabled"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// CustomerChartBinding 将 chart 分配给特定客户，支持客户定制 values。
type CustomerChartBinding struct {
	ID              string         `json:"id"`
	OrgID           string         `json:"org_id"`
	CustomerID      string         `json:"customer_id"`       // 客户 ID
	ChartID         string         `json:"chart_id"`          // chart 定义 ID
	ChartName       string         `json:"chart_name"`        // chart 名称（冗余，方便查询）
	Enabled         bool           `json:"enabled"`           // 是否启用自动更新
	ReleaseName     string         `json:"release_name"`       // Helm release 名称
	Namespace       string         `json:"namespace"`          // 部署的 K8s namespace
	CustomValues    map[string]any `json:"custom_values"`      // 客户定制 values（覆盖默认）
	DeployOrder     int            `json:"deploy_order"`       // 部署顺序（依赖管理）
	CurrentVersion  string         `json:"current_version"`    // 当前部署版本
	LastDeployedAt  time.Time      `json:"last_deployed_at"`   // 最后部署时间
	LastStatus      string         `json:"last_status"`        // 最后部署状态
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// =============================================================================
// 系统概览（监控面板）
// =============================================================================

// SystemOverview 用于监控面板的系统概览数据。
type SystemOverview struct {
	TotalOrganizations  int                    `json:"total_organizations"`
	TotalCustomers      int                    `json:"total_customers"`
	EnabledCustomers    int                    `json:"enabled_customers"`
	TotalCharts         int                    `json:"total_charts"`
	TotalDeployments    int                    `json:"total_deployments"`
	RecentReleases      []ReleaseRecord        `json:"recent_releases"`
	CustomerStatuses    []CustomerStatus       `json:"customer_statuses"`
	ReleaseSuccessRate  float64                `json:"release_success_rate"`  // 近 30 天成功率
	CertificateWarnings []CertificateWarning   `json:"certificate_warnings"`  // 证书到期预警
}

// CustomerStatus 客户集群健康状态。
type CustomerStatus struct {
	CustomerID        string    `json:"customer_id"`
	CustomerName      string    `json:"customer_name"`
	Online            bool      `json:"online"`              // operator 是否在线
	LastSeenAt        time.Time `json:"last_seen_at"`        // 最后心跳时间
	ReleaseCount      int       `json:"release_count"`       // 管理的 release 数
	FailedReleases    int       `json:"failed_releases"`     // 失败 release 数
	OperatorVersion   string    `json:"operator_version"`    // operator 版本
	HelmVersion       string    `json:"helm_version"`        // Helm 版本
	DaysUntilCertExpiry int64   `json:"days_until_cert_expiry"` // 证书到期天数
}

// CertificateWarning 证书到期预警。
type CertificateWarning struct {
	CustomerID       string `json:"customer_id"`
	CustomerName     string `json:"customer_name"`
	DaysUntilExpiry  int64  `json:"days_until_expiry"`
	Fingerprint      string `json:"fingerprint"`
	Severity         string `json:"severity"` // warning, critical
}

// =============================================================================
// 操作审计日志
// =============================================================================

// AuditLogEntry 表示一条 API 操作审计记录。
// 审计日志只增不删（append-only），管理员只读。
type AuditLogEntry struct {
	ID             int64     `json:"id"`               // 自增主键
	Timestamp      time.Time `json:"timestamp"`        // 请求时间
	UserID         string    `json:"user_id"`          // 操作人 ID（匿名请求为 "anonymous"）
	Username       string    `json:"username"`         // 操作人名称
	OrgID          string    `json:"org_id"`           // 所属组织 ID
	Action         string    `json:"action"`           // 操作类型（HTTP method）
	Resource       string    `json:"resource"`         // 目标资源类型（customer, release, user, chart 等）
	ResourceID     string    `json:"resource_id"`      // 目标资源 ID
	Method         string    `json:"method"`           // HTTP method
	Path           string    `json:"path"`             // 请求路径
	StatusCode     int       `json:"status_code"`      // HTTP 响应状态码
	ClientIP       string    `json:"client_ip"`        // 客户端 IP
	UserAgent      string    `json:"user_agent"`       // User-Agent
	ReqBodySnippet string    `json:"req_body_snippet"` // 请求体摘要（截断至 256 字符）
	DurationMs     int64     `json:"duration_ms"`      // 请求处理耗时（毫秒）
}

// AuditLogFilter 审计日志查询过滤条件。
type AuditLogFilter struct {
	UserID     string `json:"user_id"`     // 按操作人过滤
	Resource   string `json:"resource"`    // 按资源类型过滤
	ResourceID string `json:"resource_id"` // 按资源 ID 过滤
	Method     string `json:"method"`      // 按 HTTP method 过滤
	Path       string `json:"path"`        // 按路径前缀过滤
	Since      string `json:"since"`       // 起始时间，RFC3339 格式
	Until      string `json:"until"`       // 截止时间，RFC3339 格式
	Limit      int    `json:"limit"`       // 每页条数，默认 50，最大 200
	Offset     int    `json:"offset"`      // 分页偏移量
}

// =============================================================================
// API 请求/响应类型
// =============================================================================

type CreateOrganizationRequest struct {
	Name      string `json:"name"`
	AdminUser string `json:"admin_user"`
}
type CreateChartDefinitionRequest struct {
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	OCIURL         string            `json:"oci_url"`
	DefaultValues  map[string]any    `json:"default_values"`
	Labels         map[string]string `json:"labels"`
}
type CreateCustomerChartBindingRequest struct {
	CustomerID   string         `json:"customer_id"`
	ChartID      string         `json:"chart_id"`
	ReleaseName  string         `json:"release_name"`
	Namespace    string         `json:"namespace"`
	CustomValues map[string]any `json:"custom_values"`
	DeployOrder  int            `json:"deploy_order"`
}

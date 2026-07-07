// Package manager 实现客户和发布记录的数据持久化。
//
// 提供 Store 接口及两种实现:
//   - MemoryStore: 内存存储，适合测试和单节点部署
//   - SQLiteStore: 基于 SQLite 的持久化存储
package manager

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	_ "modernc.org/sqlite"
)

// Customer 表示一个私有化部署客户。
type Customer struct {
	ID               string            `json:"id" example:"customer-001"`                  // 客户唯一标识
	Name             string            `json:"name" example:"某客户"`                         // 客户名称
	OperatorEndpoint string            `json:"operator_endpoint" example:"10.0.0.5:8443"`  // release-operator gRPC 地址
	CertFingerprint  string            `json:"cert_fingerprint" example:"ABCDEF123456..."` // mTLS 客户端证书 SHA256 指纹
	Enabled          bool              `json:"enabled" example:"true"`                     // 是否启用自动更新
	Labels           map[string]string `json:"labels,omitempty"`                           // 客户标签（分组/筛选）
	CreatedAt        time.Time         `json:"created_at"`                                 // 创建时间
	UpdatedAt        time.Time         `json:"updated_at"`                                 // 更新时间
}

// CreateCustomerRequest 创建客户的请求体。
type CreateCustomerRequest struct {
	ID               string            `json:"id" example:"customer-001"`                  // 客户唯一标识（必填）
	Name             string            `json:"name" example:"某客户"`                         // 客户名称（必填）
	OperatorEndpoint string            `json:"operator_endpoint" example:"10.0.0.5:8443"`  // operator gRPC 地址（必填）
	CertFingerprint  string            `json:"cert_fingerprint" example:"ABCDEF123456..."` // 证书指纹（必填）
	Enabled          bool              `json:"enabled" example:"true"`                     // 是否启用
	Labels           map[string]string `json:"labels,omitempty"`                           // 客户标签
}

// UpdateCustomerRequest 更新客户的请求体（所有字段可选）。
type UpdateCustomerRequest struct {
	Name             *string           `json:"name,omitempty"`              // 客户名称
	OperatorEndpoint *string           `json:"operator_endpoint,omitempty"` // operator gRPC 地址
	CertFingerprint  *string           `json:"cert_fingerprint,omitempty"`  // 证书指纹
	Enabled          *bool             `json:"enabled,omitempty"`           // 是否启用
	Labels           map[string]string `json:"labels,omitempty"`            // 客户标签
}

// ReleaseRecord 表示一次发布操作记录。
type ReleaseRecord struct {
	ID           string    `json:"id"`            // 记录 ID
	RequestID    string    `json:"request_id"`    // 请求 ID
	CustomerID   string    `json:"customer_id"`   // 客户 ID
	ChartName    string    `json:"chart_name"`    // Helm chart 名称
	ChartVersion string    `json:"chart_version"` // chart 版本
	Status       string    `json:"status"`        // 发布状态
	ErrorMessage string    `json:"error_message"` // 错误信息
	DurationSecs int64     `json:"duration_secs"` // 操作耗时（秒）
	StartedAt    time.Time `json:"started_at"`    // 开始时间
	CompletedAt  time.Time `json:"completed_at"`  // 完成时间
}

// Store 定义客户和发布记录的数据持久化接口。
type Store interface {
	// --- 客户操作 ---
	ListCustomers(enabledOnly bool) ([]Customer, error)
	GetCustomer(id string) (*Customer, error)
	CreateCustomer(c Customer) (*Customer, error)
	UpdateCustomer(c Customer) (*Customer, error)
	DeleteCustomer(id string) error

	// --- 发布记录操作 ---
	CreateReleaseRecord(r ReleaseRecord) error
	UpdateReleaseRecord(requestID, customerID, status, errMsg string, completedAt time.Time, durationSecs int64) error
	ListReleaseRecords(requestID string) ([]ReleaseRecord, error)

	// --- 用户管理 ---
	ListUsers() ([]User, error)
	GetUser(id string) (*User, error)
	CreateUser(u User) error
	GetUserByEmail(email string) (*User, error)

	// --- 初始化 & 管理员用户 ---
	GetInitStatus() (bool, error)
	SetInitStatus(initialized bool) error
	CreateAdminUser(u AdminUser) error
	GetAdminUser(username string) (*AdminUser, error)
	SetVerifyToken(email, token string) error

	// --- Chart 部署配置操作 ---
	ListChartDefinitions(orgID string) ([]ChartDefinition, error)
	GetChartDefinition(orgID, chartID string) (*ChartDefinition, error)
	CreateChartDefinition(c ChartDefinition) (*ChartDefinition, error)
	ListCustomerChartBindings(orgID, customerID string) ([]CustomerChartBinding, error)
	CreateCustomerChartBinding(b CustomerChartBinding) (*CustomerChartBinding, error)
	DeleteCustomerChartBinding(orgID, bindingID string) error

	// --- 操作审计日志 ---
	CreateAuditLog(entry AuditLogEntry) error
	ListAuditLogs(filter AuditLogFilter) ([]AuditLogEntry, error)

	Close() error
}

// =============================================================================
// MemoryStore — 内存存储实现
// =============================================================================

// MemoryStore 是 Store 的内存实现，适合测试和单节点部署。
type MemoryStore struct {
	mu           sync.RWMutex
	customers    map[string]Customer
	records      map[string]ReleaseRecord
	charts       map[string]ChartDefinition
	bindings     map[string]CustomerChartBinding
	initDone     bool
	adminUser    *AdminUser
	verifyTokens map[string]string  // email → token
	auditLogs    []AuditLogEntry    // 审计日志（内存版）
	log          logr.Logger
}

// NewMemoryStore 创建一个新的 MemoryStore。
func NewMemoryStore(log logr.Logger) *MemoryStore {
	return &MemoryStore{
		customers:    make(map[string]Customer),
		records:      make(map[string]ReleaseRecord),
		charts:       make(map[string]ChartDefinition),
		bindings:     make(map[string]CustomerChartBinding),
		verifyTokens: make(map[string]string),
		log:          log.WithName("memory-store"),
	}
}

// ListCustomers 返回所有客户，enabledOnly 为 true 时仅返回已启用客户。
func (s *MemoryStore) ListCustomers(enabledOnly bool) ([]Customer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Customer
	for _, c := range s.customers {
		if enabledOnly && !c.Enabled {
			continue
		}
		result = append(result, c)
	}
	return result, nil
}

// GetCustomer 根据 ID 获取客户，不存在时返回错误。
func (s *MemoryStore) GetCustomer(id string) (*Customer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	c, ok := s.customers[id]
	if !ok {
		return nil, fmt.Errorf("customer %s not found", id)
	}
	return &c, nil
}

// CreateCustomer 创建新客户，ID 重复时返回错误。
func (s *MemoryStore) CreateCustomer(c Customer) (*Customer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.customers[c.ID]; exists {
		return nil, fmt.Errorf("customer %s already exists", c.ID)
	}

	now := time.Now()
	c.CreatedAt = now
	c.UpdatedAt = now
	s.customers[c.ID] = c

	s.log.Info("customer created", "id", c.ID, "name", c.Name)
	return &c, nil
}

// UpdateCustomer 更新客户信息，不存在时返回错误。
func (s *MemoryStore) UpdateCustomer(c Customer) (*Customer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.customers[c.ID]
	if !ok {
		return nil, fmt.Errorf("customer %s not found", c.ID)
	}

	existing.UpdatedAt = time.Now()
	if c.Name != "" {
		existing.Name = c.Name
	}
	if c.OperatorEndpoint != "" {
		existing.OperatorEndpoint = c.OperatorEndpoint
	}
	if c.CertFingerprint != "" {
		existing.CertFingerprint = c.CertFingerprint
	}
	existing.Enabled = c.Enabled
	if c.Labels != nil {
		// 拷贝 map 避免外部修改影响内部状态
		existing.Labels = make(map[string]string, len(c.Labels))
		for k, v := range c.Labels {
			existing.Labels[k] = v
		}
	}

	s.customers[c.ID] = existing
	s.log.Info("customer updated", "id", c.ID)
	return &existing, nil
}

// DeleteCustomer 删除客户，不存在时返回错误。
func (s *MemoryStore) DeleteCustomer(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.customers[id]; !ok {
		return fmt.Errorf("customer %s not found", id)
	}
	delete(s.customers, id)
	s.log.Info("customer deleted", "id", id)
	return nil
}

// CreateReleaseRecord 创建发布记录。
func (s *MemoryStore) CreateReleaseRecord(r ReleaseRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := r.RequestID + ":" + r.CustomerID
	s.records[key] = r
	return nil
}

// UpdateReleaseRecord 更新发布记录状态。
func (s *MemoryStore) UpdateReleaseRecord(requestID, customerID, status, errMsg string, completedAt time.Time, durationSecs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := requestID + ":" + customerID
	r, ok := s.records[key]
	if !ok {
		return fmt.Errorf("release record %s not found", key)
	}

	r.Status = status
	r.ErrorMessage = errMsg
	r.CompletedAt = completedAt
	r.DurationSecs = durationSecs
	s.records[key] = r
	return nil
}

// ListReleaseRecords 查询发布记录，requestID 为空时返回所有。
func (s *MemoryStore) ListReleaseRecords(requestID string) ([]ReleaseRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []ReleaseRecord
	for _, r := range s.records {
		if requestID == "" || r.RequestID == requestID {
			result = append(result, r)
		}
	}
	return result, nil
}

// --- Admin user & init methods ---
func (s *MemoryStore) GetInitStatus() (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.initDone, nil
}
func (s *MemoryStore) SetInitStatus(initialized bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initDone = initialized
	return nil
}
func (s *MemoryStore) CreateAdminUser(u AdminUser) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := u
	s.adminUser = &copied
	return nil
}
func (s *MemoryStore) GetAdminUser(username string) (*AdminUser, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.adminUser == nil || s.adminUser.Username != username {
		return nil, fmt.Errorf("user not found")
	}
	c := *s.adminUser
	return &c, nil
}
func (s *MemoryStore) SetVerifyToken(email, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verifyTokens[email] = token
	return nil
}

// --- User management stubs ---
func (s *MemoryStore) ListUsers() ([]User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var users []User
	for _, c := range s.customers {
		users = append(users, User{ID: c.ID, Name: c.Name, OrgID: "default", Role: "viewer", Enabled: c.Enabled})
	}
	if s.adminUser != nil {
		users = append(users, User{ID: s.adminUser.Username, Name: s.adminUser.Username, OrgID: "default", Role: s.adminUser.Role, Email: s.adminUser.Email})
	}
	return users, nil
}
func (s *MemoryStore) GetUser(id string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.adminUser != nil && s.adminUser.Username == id {
		return &User{ID: id, Name: id, Role: s.adminUser.Role, Email: s.adminUser.Email}, nil
	}
	return nil, fmt.Errorf("user %s not found", id)
}
func (s *MemoryStore) CreateUser(u User) error { return nil }
func (s *MemoryStore) GetUserByEmail(email string) (*User, error) {
	return nil, fmt.Errorf("not found")
}

// --- 审计日志 (MemoryStore) ---

func (s *MemoryStore) CreateAuditLog(entry AuditLogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditLogs = append(s.auditLogs, entry)
	return nil
}

func (s *MemoryStore) ListAuditLogs(filter AuditLogFilter) ([]AuditLogEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}

	var result []AuditLogEntry
	for i := len(s.auditLogs) - 1; i >= 0; i-- {
		e := s.auditLogs[i]
		if filter.UserID != "" && e.UserID != filter.UserID {
			continue
		}
		if filter.Resource != "" && e.Resource != filter.Resource {
			continue
		}
		if filter.ResourceID != "" && e.ResourceID != filter.ResourceID {
			continue
		}
		if filter.Method != "" && e.Method != filter.Method {
			continue
		}
		if filter.Path != "" && !strings.HasPrefix(e.Path, filter.Path) {
			continue
		}
		result = append(result, e)
	}

	// Apply pagination
	start := filter.Offset
	if start >= len(result) {
		return []AuditLogEntry{}, nil
	}
	end := start + filter.Limit
	if end > len(result) {
		end = len(result)
	}
	return result[start:end], nil
}

// Close 无操作，满足 Store 接口。
func (s *MemoryStore) Close() error { return nil }

// --- Chart 部署配置方法 (MemoryStore) ---

func (s *MemoryStore) ListChartDefinitions(orgID string) ([]ChartDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []ChartDefinition
	for _, c := range s.charts {
		if c.OrgID == orgID {
			result = append(result, c)
		}
	}
	return result, nil
}
func (s *MemoryStore) GetChartDefinition(orgID, chartID string) (*ChartDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.charts[orgID+":"+chartID]
	if !ok {
		return nil, fmt.Errorf("chart %s not found", chartID)
	}
	return &c, nil
}
func (s *MemoryStore) CreateChartDefinition(c ChartDefinition) (*ChartDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := c.OrgID + ":" + c.ID
	s.charts[key] = c
	return &c, nil
}
func (s *MemoryStore) ListCustomerChartBindings(orgID, customerID string) ([]CustomerChartBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []CustomerChartBinding
	for _, b := range s.bindings {
		if b.OrgID == orgID && b.CustomerID == customerID {
			result = append(result, b)
		}
	}
	return result, nil
}
func (s *MemoryStore) CreateCustomerChartBinding(b CustomerChartBinding) (*CustomerChartBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := b.OrgID + ":" + b.CustomerID + ":" + b.ChartID
	s.bindings[key] = b
	return &b, nil
}
func (s *MemoryStore) DeleteCustomerChartBinding(orgID, bindingID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, b := range s.bindings {
		if b.OrgID == orgID && b.ID == bindingID {
			delete(s.bindings, k)
			return nil
		}
	}
	return fmt.Errorf("binding %s not found", bindingID)
}

// =============================================================================
// SQLiteStore — SQLite 持久化实现
// =============================================================================

// SQLiteStore 是 Store 的 SQLite 实现，提供持久化存储。
type SQLiteStore struct {
	db  *sql.DB
	log logr.Logger
}

// NewSQLiteStore 创建 SQLiteStore 并自动执行数据库迁移。
func NewSQLiteStore(dsn string, log logr.Logger) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := migrate(db, log); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &SQLiteStore{db: db, log: log.WithName("sqlite-store")}, nil
}

// migrate 执行数据库建表迁移。
func migrate(db *sql.DB, log logr.Logger) error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS customers (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			operator_endpoint TEXT NOT NULL,
			cert_fingerprint TEXT NOT NULL,
			enabled INTEGER DEFAULT 1,
			labels TEXT DEFAULT '{}',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS release_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			request_id TEXT NOT NULL,
			customer_id TEXT NOT NULL,
			chart_name TEXT NOT NULL,
			chart_version TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			error_message TEXT,
			duration_secs INTEGER DEFAULT 0,
			started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME,
			UNIQUE(request_id, customer_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_release_records_request ON release_records(request_id)`,
		`CREATE INDEX IF NOT EXISTS idx_release_records_customer ON release_records(customer_id)`,

		// 用户管理
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			org_id TEXT,
			name TEXT NOT NULL,
			email TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'viewer',
			auth_provider TEXT DEFAULT 'local',
			external_id TEXT DEFAULT '',
			dingtalk_user_id TEXT DEFAULT '',
			enabled INTEGER DEFAULT 1,
			last_login_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_users_email ON users(email)`,

		// 管理员用户
		`CREATE TABLE IF NOT EXISTS admin_users (
			username TEXT PRIMARY KEY,
			password_hash TEXT NOT NULL,
			email TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'admin',
			email_verified INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// 系统配置
		`CREATE TABLE IF NOT EXISTS system_config (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// 验证令牌
		`CREATE TABLE IF NOT EXISTS verify_tokens (
			email TEXT PRIMARY KEY,
			token TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// Chart 部署配置
		`CREATE TABLE IF NOT EXISTS chart_definitions (
			id TEXT PRIMARY KEY,
			org_id TEXT NOT NULL,
			name TEXT NOT NULL,
			description TEXT DEFAULT '',
			oci_url TEXT NOT NULL,
			default_values TEXT DEFAULT '{}',
			labels TEXT DEFAULT '{}',
			enabled INTEGER DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_chart_definitions_org ON chart_definitions(org_id)`,

		`CREATE TABLE IF NOT EXISTS customer_chart_bindings (
			id TEXT PRIMARY KEY,
			org_id TEXT NOT NULL,
			customer_id TEXT NOT NULL,
			chart_id TEXT NOT NULL,
			chart_name TEXT NOT NULL,
			enabled INTEGER DEFAULT 1,
			release_name TEXT NOT NULL,
			namespace TEXT NOT NULL DEFAULT 'default',
			custom_values TEXT DEFAULT '{}',
			deploy_order INTEGER DEFAULT 0,
			current_version TEXT DEFAULT '',
			last_deployed_at DATETIME,
			last_status TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_customer_chart_bindings_org_cust ON customer_chart_bindings(org_id, customer_id)`,

		// 操作审计日志
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
			user_id TEXT NOT NULL DEFAULT 'anonymous',
			username TEXT NOT NULL DEFAULT '',
			org_id TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL DEFAULT '',
			resource TEXT NOT NULL DEFAULT '',
			resource_id TEXT NOT NULL DEFAULT '',
			method TEXT NOT NULL DEFAULT '',
			path TEXT NOT NULL DEFAULT '',
			status_code INTEGER NOT NULL DEFAULT 0,
			client_ip TEXT NOT NULL DEFAULT '',
			user_agent TEXT NOT NULL DEFAULT '',
			req_body_snippet TEXT NOT NULL DEFAULT '',
			duration_ms INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_user ON audit_logs(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_timestamp ON audit_logs(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_resource ON audit_logs(resource)`,
	}

	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			return fmt.Errorf("exec migration: %w", err)
		}
	}

	log.Info("database migration completed")
	return nil
}

// ListCustomers 查询客户列表。
func (s *SQLiteStore) ListCustomers(enabledOnly bool) ([]Customer, error) {
	query := "SELECT id, name, operator_endpoint, cert_fingerprint, enabled, labels, created_at, updated_at FROM customers"
	if enabledOnly {
		query += " WHERE enabled = 1"
	}
	query += " ORDER BY name"

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("query customers: %w", err)
	}
	defer rows.Close()

	var customers []Customer
	for rows.Next() {
		var c Customer
		var labelsJSON string
		if err := rows.Scan(&c.ID, &c.Name, &c.OperatorEndpoint, &c.CertFingerprint,
			&c.Enabled, &labelsJSON, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan customer: %w", err)
		}
		customers = append(customers, c)
	}

	return customers, rows.Err()
}

// GetCustomer 查询单个客户。
func (s *SQLiteStore) GetCustomer(id string) (*Customer, error) {
	var c Customer
	var labelsJSON string
	err := s.db.QueryRow(
		"SELECT id, name, operator_endpoint, cert_fingerprint, enabled, labels, created_at, updated_at FROM customers WHERE id = ?",
		id,
	).Scan(&c.ID, &c.Name, &c.OperatorEndpoint, &c.CertFingerprint, &c.Enabled, &labelsJSON, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get customer %s: %w", id, err)
	}
	return &c, nil
}

// CreateCustomer 插入新客户。
func (s *SQLiteStore) CreateCustomer(c Customer) (*Customer, error) {
	_, err := s.db.Exec(
		`INSERT INTO customers (id, name, operator_endpoint, cert_fingerprint, enabled, labels)
		 VALUES (?, ?, ?, ?, ?, '{}')`,
		c.ID, c.Name, c.OperatorEndpoint, c.CertFingerprint, c.Enabled,
	)
	if err != nil {
		return nil, fmt.Errorf("create customer: %w", err)
	}
	return &c, nil
}

// UpdateCustomer 更新客户信息。
func (s *SQLiteStore) UpdateCustomer(c Customer) (*Customer, error) {
	result, err := s.db.Exec(
		`UPDATE customers SET
			name = CASE WHEN ? != '' THEN ? ELSE name END,
			operator_endpoint = CASE WHEN ? != '' THEN ? ELSE operator_endpoint END,
			cert_fingerprint = CASE WHEN ? != '' THEN ? ELSE cert_fingerprint END,
			enabled = ?,
			updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		c.Name, c.Name,
		c.OperatorEndpoint, c.OperatorEndpoint,
		c.CertFingerprint, c.CertFingerprint,
		c.Enabled, c.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update customer: %w", err)
	}
	// 检查是否真的更新了行
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("customer %s not found", c.ID)
	}
	return s.GetCustomer(c.ID)
}

// DeleteCustomer 删除客户。
func (s *SQLiteStore) DeleteCustomer(id string) error {
	r, err := s.db.Exec("DELETE FROM customers WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return fmt.Errorf("customer %s not found", id)
	}
	return nil
}

func (s *SQLiteStore) CreateReleaseRecord(r ReleaseRecord) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO release_records (request_id, customer_id, chart_name, chart_version, status, started_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		r.RequestID, r.CustomerID, r.ChartName, r.ChartVersion, r.Status, r.StartedAt,
	)
	return err
}

func (s *SQLiteStore) UpdateReleaseRecord(requestID, customerID, status, errMsg string, completedAt time.Time, durationSecs int64) error {
	r, err := s.db.Exec(
		`UPDATE release_records SET status = ?, error_message = ?, completed_at = ?, duration_secs = ?
		 WHERE request_id = ? AND customer_id = ?`,
		status, errMsg, completedAt, durationSecs, requestID, customerID,
	)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return fmt.Errorf("release record %s:%s not found", requestID, customerID)
	}
	return nil
}

func (s *SQLiteStore) ListReleaseRecords(requestID string) ([]ReleaseRecord, error) {
	query := "SELECT id, request_id, customer_id, chart_name, chart_version, status, COALESCE(error_message,''), duration_secs, started_at, completed_at FROM release_records"
	args := []any{}
	if requestID != "" {
		query += " WHERE request_id = ?"
		args = append(args, requestID)
	}
	query += " ORDER BY started_at DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []ReleaseRecord
	for rows.Next() {
		var r ReleaseRecord
		var completedAt sql.NullTime
		if err := rows.Scan(&r.ID, &r.RequestID, &r.CustomerID, &r.ChartName, &r.ChartVersion,
			&r.Status, &r.ErrorMessage, &r.DurationSecs, &r.StartedAt, &completedAt); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			r.CompletedAt = completedAt.Time
		}
	}
	return records, rows.Err()
}

// =============================================================================
// SQLite — 用户管理
// =============================================================================

func (s *SQLiteStore) ListUsers() ([]User, error) {
	rows, err := s.db.Query("SELECT id, org_id, name, email, role, auth_provider, external_id, dingtalk_user_id, enabled, COALESCE(last_login_at,''), created_at, updated_at FROM users ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var orgID, email, authProvider, externalID, dingTalkUserID sql.NullString
		var lastLoginAt string
		if err := rows.Scan(&u.ID, &orgID, &u.Name, &email, &u.Role, &authProvider, &externalID, &dingTalkUserID, &u.Enabled, &lastLoginAt, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		if orgID.Valid {
			u.OrgID = orgID.String
		}
		if email.Valid {
			u.Email = email.String
		}
		if authProvider.Valid {
			u.AuthProvider = authProvider.String
		}
		if externalID.Valid {
			u.ExternalID = externalID.String
		}
		if dingTalkUserID.Valid {
			u.DingTalkUserID = dingTalkUserID.String
		}
		if t, err := time.Parse("2006-01-02 15:04:05", lastLoginAt); err == nil {
			u.LastLoginAt = t
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *SQLiteStore) GetUser(id string) (*User, error) {
	var u User
	var orgID, email, authProvider, externalID, dingTalkUserID sql.NullString
	var lastLoginAt string
	err := s.db.QueryRow(
		"SELECT id, org_id, name, email, role, auth_provider, external_id, dingtalk_user_id, enabled, COALESCE(last_login_at,''), created_at, updated_at FROM users WHERE id = ?", id,
	).Scan(&u.ID, &orgID, &u.Name, &email, &u.Role, &authProvider, &externalID, &dingTalkUserID, &u.Enabled, &lastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("user %s not found", id)
	}
	if orgID.Valid {
		u.OrgID = orgID.String
	}
	if email.Valid {
		u.Email = email.String
	}
	if authProvider.Valid {
		u.AuthProvider = authProvider.String
	}
	if externalID.Valid {
		u.ExternalID = externalID.String
	}
	if dingTalkUserID.Valid {
		u.DingTalkUserID = dingTalkUserID.String
	}
	if t, err := time.Parse("2006-01-02 15:04:05", lastLoginAt); err == nil {
		u.LastLoginAt = t
	}
	return &u, nil
}

func (s *SQLiteStore) CreateUser(u User) error {
	_, err := s.db.Exec(
		`INSERT INTO users (id, org_id, name, email, role, auth_provider, external_id, dingtalk_user_id, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, nullStr(u.OrgID), u.Name, nullStr(u.Email), u.Role, nullStr(u.AuthProvider), nullStr(u.ExternalID), nullStr(u.DingTalkUserID), u.Enabled, u.CreatedAt, u.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetUserByEmail(email string) (*User, error) {
	var u User
	var orgID, authProvider, externalID, dingTalkUserID sql.NullString
	var emailStr sql.NullString
	var lastLoginAt string
	err := s.db.QueryRow(
		"SELECT id, org_id, name, email, role, auth_provider, external_id, dingtalk_user_id, enabled, COALESCE(last_login_at,''), created_at, updated_at FROM users WHERE email = ?", email,
	).Scan(&u.ID, &orgID, &u.Name, &emailStr, &u.Role, &authProvider, &externalID, &dingTalkUserID, &u.Enabled, &lastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("user with email %s not found", email)
	}
	if orgID.Valid {
		u.OrgID = orgID.String
	}
	if emailStr.Valid {
		u.Email = emailStr.String
	}
	if authProvider.Valid {
		u.AuthProvider = authProvider.String
	}
	if externalID.Valid {
		u.ExternalID = externalID.String
	}
	if dingTalkUserID.Valid {
		u.DingTalkUserID = dingTalkUserID.String
	}
	if t, err := time.Parse("2006-01-02 15:04:05", lastLoginAt); err == nil {
		u.LastLoginAt = t
	}
	return &u, nil
}

// =============================================================================
// SQLite — 初始化 & 管理员用户
// =============================================================================

func (s *SQLiteStore) GetInitStatus() (bool, error) {
	var val string
	err := s.db.QueryRow("SELECT value FROM system_config WHERE key='initialized'").Scan(&val)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get init status: %w", err)
	}
	return val == "true", nil
}

func (s *SQLiteStore) SetInitStatus(initialized bool) error {
	v := "false"
	if initialized {
		v = "true"
	}
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO system_config (key, value, updated_at) VALUES ('initialized', ?, CURRENT_TIMESTAMP)", v,
	)
	if err != nil {
		return fmt.Errorf("set init status: %w", err)
	}
	return nil
}

func (s *SQLiteStore) CreateAdminUser(u AdminUser) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO admin_users (username, password_hash, email, role, email_verified, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		u.Username, u.PasswordHash, u.Email, u.Role, u.EmailVerified, u.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetAdminUser(username string) (*AdminUser, error) {
	var u AdminUser
	err := s.db.QueryRow(
		"SELECT username, password_hash, email, role, email_verified, created_at FROM admin_users WHERE username = ?", username,
	).Scan(&u.Username, &u.PasswordHash, &u.Email, &u.Role, &u.EmailVerified, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("user %s not found", username)
	}
	return &u, nil
}

func (s *SQLiteStore) SetVerifyToken(email, token string) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO verify_tokens (email, token, created_at) VALUES (?, ?, CURRENT_TIMESTAMP)", email, token,
	)
	if err != nil {
		return fmt.Errorf("set verify token: %w", err)
	}
	return nil
}

// =============================================================================
// SQLite — Chart 部署配置
// =============================================================================

func (s *SQLiteStore) ListChartDefinitions(orgID string) ([]ChartDefinition, error) {
	rows, err := s.db.Query(
		"SELECT id, org_id, name, description, oci_url, enabled, labels, created_at, updated_at FROM chart_definitions WHERE org_id = ? ORDER BY name", orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list chart definitions: %w", err)
	}
	defer rows.Close()

	var charts []ChartDefinition
	for rows.Next() {
		var c ChartDefinition
		var labelsJSON string
		if err := rows.Scan(&c.ID, &c.OrgID, &c.Name, &c.Description, &c.OCIURL, &c.Enabled, &labelsJSON, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan chart definition: %w", err)
		}
		charts = append(charts, c)
	}
	return charts, rows.Err()
}

func (s *SQLiteStore) GetChartDefinition(orgID, chartID string) (*ChartDefinition, error) {
	var c ChartDefinition
	var labelsJSON string
	err := s.db.QueryRow(
		"SELECT id, org_id, name, description, oci_url, enabled, labels, created_at, updated_at FROM chart_definitions WHERE org_id = ? AND id = ?", orgID, chartID,
	).Scan(&c.ID, &c.OrgID, &c.Name, &c.Description, &c.OCIURL, &c.Enabled, &labelsJSON, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("chart %s not found", chartID)
	}
	return &c, nil
}

func (s *SQLiteStore) CreateChartDefinition(c ChartDefinition) (*ChartDefinition, error) {
	_, err := s.db.Exec(
		`INSERT INTO chart_definitions (id, org_id, name, description, oci_url, enabled, labels) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.OrgID, c.Name, c.Description, c.OCIURL, c.Enabled, "{}",
	)
	if err != nil {
		return nil, fmt.Errorf("create chart definition: %w", err)
	}
	return &c, nil
}

func (s *SQLiteStore) ListCustomerChartBindings(orgID, customerID string) ([]CustomerChartBinding, error) {
	rows, err := s.db.Query(
		`SELECT id, org_id, customer_id, chart_id, chart_name, enabled, release_name, namespace, deploy_order,
		        COALESCE(current_version,''), COALESCE(last_status,''), created_at, updated_at
		 FROM customer_chart_bindings WHERE org_id = ? AND customer_id = ? ORDER BY deploy_order`, orgID, customerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list customer chart bindings: %w", err)
	}
	defer rows.Close()

	var bindings []CustomerChartBinding
	for rows.Next() {
		var b CustomerChartBinding
		if err := rows.Scan(&b.ID, &b.OrgID, &b.CustomerID, &b.ChartID, &b.ChartName, &b.Enabled, &b.ReleaseName, &b.Namespace, &b.DeployOrder, &b.CurrentVersion, &b.LastStatus, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan binding: %w", err)
		}
		bindings = append(bindings, b)
	}
	return bindings, rows.Err()
}

func (s *SQLiteStore) CreateCustomerChartBinding(b CustomerChartBinding) (*CustomerChartBinding, error) {
	_, err := s.db.Exec(
		`INSERT INTO customer_chart_bindings (id, org_id, customer_id, chart_id, chart_name, enabled, release_name, namespace, deploy_order)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.OrgID, b.CustomerID, b.ChartID, b.ChartName, b.Enabled, b.ReleaseName, b.Namespace, b.DeployOrder,
	)
	if err != nil {
		return nil, fmt.Errorf("create customer chart binding: %w", err)
	}
	return &b, nil
}

func (s *SQLiteStore) DeleteCustomerChartBinding(orgID, bindingID string) error {
	r, err := s.db.Exec("DELETE FROM customer_chart_bindings WHERE org_id = ? AND id = ?", orgID, bindingID)
	if err != nil {
		return fmt.Errorf("delete customer chart binding: %w", err)
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return fmt.Errorf("binding %s not found", bindingID)
	}
	return nil
}

// =============================================================================
// SQLite — 操作审计日志
// =============================================================================

func (s *SQLiteStore) CreateAuditLog(entry AuditLogEntry) error {
	_, err := s.db.Exec(
		`INSERT INTO audit_logs (timestamp, user_id, username, org_id, action, resource, resource_id, method, path, status_code, client_ip, user_agent, req_body_snippet, duration_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.Timestamp, entry.UserID, entry.Username, entry.OrgID, entry.Action,
		entry.Resource, entry.ResourceID, entry.Method, entry.Path,
		entry.StatusCode, entry.ClientIP, entry.UserAgent, entry.ReqBodySnippet, entry.DurationMs,
	)
	return err
}

func (s *SQLiteStore) ListAuditLogs(filter AuditLogFilter) ([]AuditLogEntry, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}

	query := "SELECT id, timestamp, user_id, username, org_id, action, resource, resource_id, method, path, status_code, client_ip, user_agent, req_body_snippet, duration_ms FROM audit_logs WHERE 1=1"
	args := []any{}

	if filter.UserID != "" {
		query += " AND user_id = ?"
		args = append(args, filter.UserID)
	}
	if filter.Resource != "" {
		query += " AND resource = ?"
		args = append(args, filter.Resource)
	}
	if filter.ResourceID != "" {
		query += " AND resource_id = ?"
		args = append(args, filter.ResourceID)
	}
	if filter.Method != "" {
		query += " AND method = ?"
		args = append(args, filter.Method)
	}
	if filter.Path != "" {
		query += " AND path LIKE ?"
		args = append(args, filter.Path+"%")
	}
	if filter.Since != "" {
		query += " AND timestamp >= ?"
		args = append(args, filter.Since)
	}
	if filter.Until != "" {
		query += " AND timestamp <= ?"
		args = append(args, filter.Until)
	}

	query += " ORDER BY timestamp DESC LIMIT ? OFFSET ?"
	args = append(args, filter.Limit, filter.Offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	defer rows.Close()

	var entries []AuditLogEntry
	for rows.Next() {
		var e AuditLogEntry
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.UserID, &e.Username, &e.OrgID, &e.Action,
			&e.Resource, &e.ResourceID, &e.Method, &e.Path,
			&e.StatusCode, &e.ClientIP, &e.UserAgent, &e.ReqBodySnippet, &e.DurationMs); err != nil {
			return nil, fmt.Errorf("scan audit log: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// Close 关闭数据库连接。
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// nullStr 返回 NULL 对应的 SQL nullable string 值，空字符串视为 NULL。
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

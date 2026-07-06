// Package manager 实现客户和发布记录的数据持久化。
//
// 提供 Store 接口及两种实现:
//   - MemoryStore: 内存存储，适合测试和单节点部署
//   - SQLiteStore: 基于 SQLite 的持久化存储
package manager

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	_ "modernc.org/sqlite"
)

// Customer 表示一个私有化部署客户。
type Customer struct {
	ID               string            `json:"id" example:"customer-001"`              // 客户唯一标识
	Name             string            `json:"name" example:"某客户"`                     // 客户名称
	OperatorEndpoint string            `json:"operator_endpoint" example:"10.0.0.5:8443"` // release-operator gRPC 地址
	CertFingerprint  string            `json:"cert_fingerprint" example:"ABCDEF123456..."` // mTLS 客户端证书 SHA256 指纹
	Enabled          bool              `json:"enabled" example:"true"`                 // 是否启用自动更新
	Labels           map[string]string `json:"labels,omitempty"`                      // 客户标签（分组/筛选）
	CreatedAt        time.Time         `json:"created_at"`                            // 创建时间
	UpdatedAt        time.Time         `json:"updated_at"`                            // 更新时间
}

// CreateCustomerRequest 创建客户的请求体。
type CreateCustomerRequest struct {
	ID               string            `json:"id" example:"customer-001"`              // 客户唯一标识（必填）
	Name             string            `json:"name" example:"某客户"`                     // 客户名称（必填）
	OperatorEndpoint string            `json:"operator_endpoint" example:"10.0.0.5:8443"` // operator gRPC 地址（必填）
	CertFingerprint  string            `json:"cert_fingerprint" example:"ABCDEF123456..."` // 证书指纹（必填）
	Enabled          bool              `json:"enabled" example:"true"`                 // 是否启用
	Labels           map[string]string `json:"labels,omitempty"`                      // 客户标签
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
	ID            string    `json:"id"`             // 记录 ID
	RequestID     string    `json:"request_id"`     // 请求 ID
	CustomerID    string    `json:"customer_id"`    // 客户 ID
	ChartName     string    `json:"chart_name"`     // Helm chart 名称
	ChartVersion  string    `json:"chart_version"`  // chart 版本
	Status        string    `json:"status"`         // 发布状态
	ErrorMessage  string    `json:"error_message"`  // 错误信息
	DurationSecs  int64     `json:"duration_secs"`  // 操作耗时（秒）
	StartedAt     time.Time `json:"started_at"`     // 开始时间
	CompletedAt   time.Time `json:"completed_at"`   // 完成时间
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

	Close() error
}

// =============================================================================
// MemoryStore — 内存存储实现
// =============================================================================

// MemoryStore 是 Store 的内存实现，适合测试和单节点部署。
type MemoryStore struct {
	mu        sync.RWMutex
	customers map[string]Customer
	records   map[string]ReleaseRecord
	charts    map[string]ChartDefinition
	bindings  map[string]CustomerChartBinding
	initDone  bool
	adminUser *AdminUser
	verifyTokens map[string]string // email → token
	log       logr.Logger
}

// NewMemoryStore 创建一个新的 MemoryStore。
func NewMemoryStore(log logr.Logger) *MemoryStore {
	return &MemoryStore{
		customers: make(map[string]Customer),
		records:   make(map[string]ReleaseRecord),
		charts:    make(map[string]ChartDefinition),
		bindings:  make(map[string]CustomerChartBinding),
		verifyTokens: make(map[string]string),
		log:       log.WithName("memory-store"),
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
func (s *MemoryStore) GetInitStatus() (bool, error) { s.mu.RLock(); defer s.mu.RUnlock(); return s.initDone, nil }
func (s *MemoryStore) SetInitStatus(initialized bool) error { s.mu.Lock(); defer s.mu.Unlock(); s.initDone = initialized; return nil }
func (s *MemoryStore) CreateAdminUser(u AdminUser) error { s.mu.Lock(); defer s.mu.Unlock(); copied := u; s.adminUser = &copied; return nil }
func (s *MemoryStore) GetAdminUser(username string) (*AdminUser, error) {
	s.mu.RLock(); defer s.mu.RUnlock()
	if s.adminUser == nil || s.adminUser.Username != username { return nil, fmt.Errorf("user not found") }
	c := *s.adminUser; return &c, nil
}
func (s *MemoryStore) SetVerifyToken(email, token string) error { s.mu.Lock(); defer s.mu.Unlock(); s.verifyTokens[email] = token; return nil }

// --- User management stubs ---
func (s *MemoryStore) ListUsers() ([]User, error) {
	s.mu.RLock(); defer s.mu.RUnlock()
	var users []User
	for _, c := range s.customers {
		users = append(users, User{ID: c.ID, Name: c.Name, OrgID: "default", Role: "viewer", Enabled: c.Enabled})
	}
	if s.adminUser != nil { users = append(users, User{ID: s.adminUser.Username, Name: s.adminUser.Username, OrgID: "default", Role: s.adminUser.Role, Email: s.adminUser.Email}) }
	return users, nil
}
func (s *MemoryStore) GetUser(id string) (*User, error) {
	s.mu.RLock(); defer s.mu.RUnlock()
	if s.adminUser != nil && s.adminUser.Username == id { return &User{ID: id, Name: id, Role: s.adminUser.Role, Email: s.adminUser.Email}, nil }
	return nil, fmt.Errorf("user %s not found", id)
}
func (s *MemoryStore) CreateUser(u User) error { return nil }
func (s *MemoryStore) GetUserByEmail(email string) (*User, error) {
	return nil, fmt.Errorf("not found")
}

// Close 无操作，满足 Store 接口。
func (s *MemoryStore) Close() error { return nil }

// --- Chart 部署配置方法 (MemoryStore) ---

func (s *MemoryStore) ListChartDefinitions(orgID string) ([]ChartDefinition, error) {
	s.mu.RLock(); defer s.mu.RUnlock()
	var result []ChartDefinition
	for _, c := range s.charts {
		if c.OrgID == orgID { result = append(result, c) }
	}
	return result, nil
}
func (s *MemoryStore) GetChartDefinition(orgID, chartID string) (*ChartDefinition, error) {
	s.mu.RLock(); defer s.mu.RUnlock()
	c, ok := s.charts[orgID+":"+chartID]
	if !ok { return nil, fmt.Errorf("chart %s not found", chartID) }
	return &c, nil
}
func (s *MemoryStore) CreateChartDefinition(c ChartDefinition) (*ChartDefinition, error) {
	s.mu.Lock(); defer s.mu.Unlock()
	key := c.OrgID + ":" + c.ID
	s.charts[key] = c
	return &c, nil
}
func (s *MemoryStore) ListCustomerChartBindings(orgID, customerID string) ([]CustomerChartBinding, error) {
	s.mu.RLock(); defer s.mu.RUnlock()
	var result []CustomerChartBinding
	for _, b := range s.bindings {
		if b.OrgID == orgID && b.CustomerID == customerID { result = append(result, b) }
	}
	return result, nil
}
func (s *MemoryStore) CreateCustomerChartBinding(b CustomerChartBinding) (*CustomerChartBinding, error) {
	s.mu.Lock(); defer s.mu.Unlock()
	key := b.OrgID + ":" + b.CustomerID + ":" + b.ChartID
	s.bindings[key] = b
	return &b, nil
}
func (s *MemoryStore) DeleteCustomerChartBinding(orgID, bindingID string) error {
	s.mu.Lock(); defer s.mu.Unlock()
	for k, b := range s.bindings {
		if b.OrgID == orgID && b.ID == bindingID { delete(s.bindings, k); return nil }
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
	if err != nil { return err }
	if n, _ := r.RowsAffected(); n == 0 { return fmt.Errorf("customer %s not found", id) }
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
	if err != nil { return err }
	if n, _ := r.RowsAffected(); n == 0 { return fmt.Errorf("release record %s:%s not found", requestID, customerID) }
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

// Close 关闭数据库连接。
// SQLite admin stubs
func (s *SQLiteStore) ListUsers() ([]User, error) { return nil, fmt.Errorf("not implemented") }
func (s *SQLiteStore) GetUser(id string) (*User, error) { return nil, fmt.Errorf("not found") }
func (s *SQLiteStore) CreateUser(u User) error { return fmt.Errorf("not implemented") }
func (s *SQLiteStore) GetUserByEmail(email string) (*User, error) { return nil, fmt.Errorf("not found") }

func (s *SQLiteStore) GetInitStatus() (bool, error) { return false, fmt.Errorf("not implemented") }
func (s *SQLiteStore) SetInitStatus(initialized bool) error { return fmt.Errorf("not implemented") }
func (s *SQLiteStore) CreateAdminUser(u AdminUser) error { return fmt.Errorf("not implemented") }
func (s *SQLiteStore) GetAdminUser(username string) (*AdminUser, error) { return nil, fmt.Errorf("not found") }
func (s *SQLiteStore) SetVerifyToken(email, token string) error { return fmt.Errorf("not implemented") }

// SQLite chart config stubs — 生产环境应使用完整的数据库实现
func (s *SQLiteStore) ListChartDefinitions(orgID string) ([]ChartDefinition, error) { return nil, nil }
func (s *SQLiteStore) GetChartDefinition(orgID, chartID string) (*ChartDefinition, error) {
	return nil, fmt.Errorf("chart %s not found", chartID)
}
func (s *SQLiteStore) CreateChartDefinition(c ChartDefinition) (*ChartDefinition, error) { return &c, nil }
func (s *SQLiteStore) ListCustomerChartBindings(orgID, customerID string) ([]CustomerChartBinding, error) { return nil, nil }
func (s *SQLiteStore) CreateCustomerChartBinding(b CustomerChartBinding) (*CustomerChartBinding, error) { return &b, nil }
func (s *SQLiteStore) DeleteCustomerChartBinding(orgID, bindingID string) error { return nil }

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

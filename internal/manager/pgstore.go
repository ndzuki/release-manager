// Package manager 实现 PostgreSQL 持久化存储。
//
// 生产环境使用 PostgreSQL，开发本地使用 SQLite。
// 通过 StoreConfig.Type 选择: "postgres" 或 "sqlite"。
package manager

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	_ "github.com/lib/pq"
)

// PostgreSQLStore 是 Store 的 PostgreSQL 实现，用于生产环境。
type PostgreSQLStore struct {
	db  *sql.DB
	log logr.Logger
}

// NewPostgreSQLStore 创建 PostgreSQLStore 并自动执行数据库迁移。
func NewPostgreSQLStore(dsn string, log logr.Logger) (*PostgreSQLStore, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	// 连接池配置
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	if err := migratePostgres(db, log); err != nil {
		return nil, fmt.Errorf("migrate postgres: %w", err)
	}

	return &PostgreSQLStore{db: db, log: log.WithName("pgstore")}, nil
}

func migratePostgres(db *sql.DB, log logr.Logger) error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS customers (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			operator_endpoint TEXT NOT NULL,
			cert_fingerprint TEXT NOT NULL,
			enabled BOOLEAN DEFAULT true,
			labels JSONB DEFAULT '{}',
			created_at TIMESTAMPTZ DEFAULT NOW(),
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS release_records (
			id SERIAL PRIMARY KEY,
			request_id TEXT NOT NULL,
			customer_id TEXT NOT NULL,
			chart_name TEXT NOT NULL,
			chart_version TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			error_message TEXT DEFAULT '',
			duration_secs INTEGER DEFAULT 0,
			started_at TIMESTAMPTZ DEFAULT NOW(),
			completed_at TIMESTAMPTZ,
			UNIQUE(request_id, customer_id)
		)`,
		`CREATE TABLE IF NOT EXISTS chart_definitions (
			id TEXT PRIMARY KEY,
			org_id TEXT NOT NULL,
			name TEXT NOT NULL,
			description TEXT DEFAULT '',
			oci_url TEXT NOT NULL,
			default_values JSONB DEFAULT '{}',
			labels JSONB DEFAULT '{}',
			enabled BOOLEAN DEFAULT true,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS customer_chart_bindings (
			id TEXT PRIMARY KEY,
			org_id TEXT NOT NULL,
			customer_id TEXT NOT NULL,
			chart_id TEXT NOT NULL,
			chart_name TEXT NOT NULL,
			enabled BOOLEAN DEFAULT true,
			release_name TEXT NOT NULL,
			namespace TEXT NOT NULL DEFAULT 'default',
			custom_values JSONB DEFAULT '{}',
			deploy_order INTEGER DEFAULT 0,
			current_version TEXT DEFAULT '',
			last_deployed_at TIMESTAMPTZ,
			last_status TEXT DEFAULT '',
			created_at TIMESTAMPTZ DEFAULT NOW(),
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS admin_users (
			username TEXT PRIMARY KEY,
			password_hash TEXT NOT NULL,
			email TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'admin',
			email_verified BOOLEAN DEFAULT false,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS system_config (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS verify_tokens (
			email TEXT PRIMARY KEY,
			token TEXT NOT NULL,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			org_id TEXT,
			name TEXT NOT NULL,
			email TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'viewer',
			auth_provider TEXT DEFAULT 'local',
			external_id TEXT DEFAULT '',
			dingtalk_user_id TEXT DEFAULT '',
			enabled BOOLEAN DEFAULT true,
			last_login_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_users_email ON users(email)`,
		`CREATE INDEX IF NOT EXISTS idx_release_records_request ON release_records(request_id)`,
		`CREATE INDEX IF NOT EXISTS idx_release_records_customer ON release_records(customer_id)`,
		`CREATE INDEX IF NOT EXISTS idx_chart_definitions_org ON chart_definitions(org_id)`,
		`CREATE INDEX IF NOT EXISTS idx_customer_chart_bindings_org_cust ON customer_chart_bindings(org_id, customer_id)`,
	}

	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			return fmt.Errorf("exec migration: %w\nSQL: %s", err, m[:60])
		}
	}

	log.Info("postgres migration completed")
	return nil
}

// =============================================================================
// Customer CRUD
// =============================================================================

func (s *PostgreSQLStore) ListCustomers(enabledOnly bool) ([]Customer, error) {
	q := "SELECT id, name, operator_endpoint, cert_fingerprint, enabled, labels, created_at, updated_at FROM customers"
	if enabledOnly {
		q += " WHERE enabled = true"
	}
	q += " ORDER BY name"

	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var customers []Customer
	for rows.Next() {
		var c Customer
		var labelsJSON []byte
		if err := rows.Scan(&c.ID, &c.Name, &c.OperatorEndpoint, &c.CertFingerprint, &c.Enabled, &labelsJSON, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan customer: %w", err)
		}
		customers = append(customers, c)
	}
	return customers, rows.Err()
}

func (s *PostgreSQLStore) GetCustomer(id string) (*Customer, error) {
	var c Customer
	var labelsJSON []byte
	err := s.db.QueryRow(
		"SELECT id, name, operator_endpoint, cert_fingerprint, enabled, labels, created_at, updated_at FROM customers WHERE id = $1", id,
	).Scan(&c.ID, &c.Name, &c.OperatorEndpoint, &c.CertFingerprint, &c.Enabled, &labelsJSON, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("customer %s not found", id)
	}
	return &c, nil
}

func (s *PostgreSQLStore) CreateCustomer(c Customer) (*Customer, error) {
	_, err := s.db.Exec(
		`INSERT INTO customers (id, name, operator_endpoint, cert_fingerprint, enabled, labels)
		 VALUES ($1,$2,$3,$4,$5,'{}')`,
		c.ID, c.Name, c.OperatorEndpoint, c.CertFingerprint, c.Enabled,
	)
	if err != nil {
		return nil, fmt.Errorf("create customer: %w", err)
	}
	return &c, nil
}

func (s *PostgreSQLStore) UpdateCustomer(c Customer) (*Customer, error) {
	r, err := s.db.Exec(
		`UPDATE customers SET
		 name = COALESCE(NULLIF($1,''), name),
		 operator_endpoint = COALESCE(NULLIF($2,''), operator_endpoint),
		 cert_fingerprint = COALESCE(NULLIF($3,''), cert_fingerprint),
		 enabled = $4,
		 updated_at = NOW()
		 WHERE id = $5`,
		c.Name, c.OperatorEndpoint, c.CertFingerprint, c.Enabled, c.ID,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("customer %s not found", c.ID)
	}
	return s.GetCustomer(c.ID)
}

func (s *PostgreSQLStore) DeleteCustomer(id string) error {
	r, err := s.db.Exec("DELETE FROM customers WHERE id = $1", id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return fmt.Errorf("customer %s not found", id)
	}
	return nil
}

// =============================================================================
// Release Records
// =============================================================================

func (s *PostgreSQLStore) CreateReleaseRecord(r ReleaseRecord) error {
	_, err := s.db.Exec(
		`INSERT INTO release_records (request_id, customer_id, chart_name, chart_version, status, started_at)
		 VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (request_id, customer_id) DO UPDATE SET status=$5, started_at=$6`,
		r.RequestID, r.CustomerID, r.ChartName, r.ChartVersion, r.Status, r.StartedAt,
	)
	return err
}

func (s *PostgreSQLStore) UpdateReleaseRecord(requestID, customerID, status, errMsg string, completedAt time.Time, durationSecs int64) error {
	r, err := s.db.Exec(
		`UPDATE release_records SET status=$1, error_message=$2, completed_at=$3, duration_secs=$4
		 WHERE request_id=$5 AND customer_id=$6`,
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

func (s *PostgreSQLStore) ListReleaseRecords(requestID string) ([]ReleaseRecord, error) {
	q := "SELECT id, request_id, customer_id, chart_name, chart_version, status, COALESCE(error_message,''), duration_secs, started_at, COALESCE(completed_at, TIMESTAMPTZ 'epoch') FROM release_records"
	args := []any{}
	if requestID != "" {
		q += " WHERE request_id = $1"
		args = append(args, requestID)
	}
	q += " ORDER BY started_at DESC LIMIT 100"

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []ReleaseRecord
	for rows.Next() {
		var r ReleaseRecord
		if err := rows.Scan(&r.ID, &r.RequestID, &r.CustomerID, &r.ChartName, &r.ChartVersion, &r.Status, &r.ErrorMessage, &r.DurationSecs, &r.StartedAt, &r.CompletedAt); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// =============================================================================
// Chart Config
// =============================================================================

func (s *PostgreSQLStore) ListChartDefinitions(orgID string) ([]ChartDefinition, error) {
	rows, err := s.db.Query("SELECT id, org_id, name, description, oci_url, enabled, labels, created_at, updated_at FROM chart_definitions WHERE org_id=$1 ORDER BY name", orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var charts []ChartDefinition
	for rows.Next() {
		var c ChartDefinition
		var labelsJSON []byte
		if err := rows.Scan(&c.ID, &c.OrgID, &c.Name, &c.Description, &c.OCIURL, &c.Enabled, &labelsJSON, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		charts = append(charts, c)
	}
	return charts, rows.Err()
}

func (s *PostgreSQLStore) GetChartDefinition(orgID, chartID string) (*ChartDefinition, error) {
	var c ChartDefinition
	var labelsJSON []byte
	err := s.db.QueryRow("SELECT id, org_id, name, description, oci_url, enabled, labels, created_at, updated_at FROM chart_definitions WHERE org_id=$1 AND id=$2", orgID, chartID).
		Scan(&c.ID, &c.OrgID, &c.Name, &c.Description, &c.OCIURL, &c.Enabled, &labelsJSON, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("chart %s not found", chartID)
	}
	return &c, nil
}

func (s *PostgreSQLStore) CreateChartDefinition(c ChartDefinition) (*ChartDefinition, error) {
	_, err := s.db.Exec(
		`INSERT INTO chart_definitions (id, org_id, name, description, oci_url, enabled, labels) VALUES ($1,$2,$3,$4,$5,$6,'{}')`,
		c.ID, c.OrgID, c.Name, c.Description, c.OCIURL, c.Enabled,
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *PostgreSQLStore) ListCustomerChartBindings(orgID, customerID string) ([]CustomerChartBinding, error) {
	rows, err := s.db.Query(
		`SELECT id, org_id, customer_id, chart_id, chart_name, enabled, release_name, namespace, deploy_order, COALESCE(current_version,''), COALESCE(last_status,''), created_at, updated_at
		 FROM customer_chart_bindings WHERE org_id=$1 AND customer_id=$2 ORDER BY deploy_order`, orgID, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bindings []CustomerChartBinding
	for rows.Next() {
		var b CustomerChartBinding
		if err := rows.Scan(&b.ID, &b.OrgID, &b.CustomerID, &b.ChartID, &b.ChartName, &b.Enabled, &b.ReleaseName, &b.Namespace, &b.DeployOrder, &b.CurrentVersion, &b.LastStatus, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		bindings = append(bindings, b)
	}
	return bindings, rows.Err()
}

func (s *PostgreSQLStore) CreateCustomerChartBinding(b CustomerChartBinding) (*CustomerChartBinding, error) {
	_, err := s.db.Exec(
		`INSERT INTO customer_chart_bindings (id, org_id, customer_id, chart_id, chart_name, enabled, release_name, namespace, deploy_order) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		b.ID, b.OrgID, b.CustomerID, b.ChartID, b.ChartName, b.Enabled, b.ReleaseName, b.Namespace, b.DeployOrder,
	)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *PostgreSQLStore) DeleteCustomerChartBinding(orgID, bindingID string) error {
	r, err := s.db.Exec("DELETE FROM customer_chart_bindings WHERE org_id=$1 AND id=$2", orgID, bindingID)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return fmt.Errorf("binding %s not found", bindingID)
	}
	return nil
}

// =============================================================================
// Admin & Init
// =============================================================================

func (s *PostgreSQLStore) ListUsers() ([]User, error) {
	rows, err := s.db.Query("SELECT id, COALESCE(org_id,''), name, COALESCE(email,''), role, COALESCE(auth_provider,'local'), COALESCE(external_id,''), COALESCE(dingtalk_user_id,''), enabled, COALESCE(last_login_at, TIMESTAMPTZ 'epoch'), created_at, updated_at FROM users ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.OrgID, &u.Name, &u.Email, &u.Role, &u.AuthProvider, &u.ExternalID, &u.DingTalkUserID, &u.Enabled, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *PostgreSQLStore) GetUser(id string) (*User, error) {
	var u User
	err := s.db.QueryRow(
		"SELECT id, COALESCE(org_id,''), name, COALESCE(email,''), role, COALESCE(auth_provider,'local'), COALESCE(external_id,''), COALESCE(dingtalk_user_id,''), enabled, COALESCE(last_login_at, TIMESTAMPTZ 'epoch'), created_at, updated_at FROM users WHERE id = $1", id,
	).Scan(&u.ID, &u.OrgID, &u.Name, &u.Email, &u.Role, &u.AuthProvider, &u.ExternalID, &u.DingTalkUserID, &u.Enabled, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("user %s not found", id)
	}
	return &u, nil
}

func (s *PostgreSQLStore) CreateUser(u User) error {
	_, err := s.db.Exec(
		`INSERT INTO users (id, org_id, name, email, role, auth_provider, external_id, dingtalk_user_id, enabled, created_at, updated_at)
		 VALUES ($1, NULLIF($2,''), $3, NULLIF($4,''), $5, NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), $9, $10, $11)`,
		u.ID, u.OrgID, u.Name, u.Email, u.Role, u.AuthProvider, u.ExternalID, u.DingTalkUserID, u.Enabled, u.CreatedAt, u.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) GetUserByEmail(email string) (*User, error) {
	var u User
	err := s.db.QueryRow(
		"SELECT id, COALESCE(org_id,''), name, COALESCE(email,''), role, COALESCE(auth_provider,'local'), COALESCE(external_id,''), COALESCE(dingtalk_user_id,''), enabled, COALESCE(last_login_at, TIMESTAMPTZ 'epoch'), created_at, updated_at FROM users WHERE email = $1", email,
	).Scan(&u.ID, &u.OrgID, &u.Name, &u.Email, &u.Role, &u.AuthProvider, &u.ExternalID, &u.DingTalkUserID, &u.Enabled, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("user with email %s not found", email)
	}
	return &u, nil
}

func (s *PostgreSQLStore) GetInitStatus() (bool, error) {
	var val string
	err := s.db.QueryRow("SELECT value FROM system_config WHERE key='initialized'").Scan(&val)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return val == "true", nil
}

func (s *PostgreSQLStore) SetInitStatus(initialized bool) error {
	v := "false"
	if initialized {
		v = "true"
	}
	_, err := s.db.Exec(
		`INSERT INTO system_config (key, value) VALUES ('initialized',$1) ON CONFLICT (key) DO UPDATE SET value=$1, updated_at=NOW()`, v)
	return err
}

func (s *PostgreSQLStore) CreateAdminUser(u AdminUser) error {
	_, err := s.db.Exec(
		`INSERT INTO admin_users (username, password_hash, email, role, email_verified) VALUES ($1,$2,$3,$4,$5)`,
		u.Username, u.PasswordHash, u.Email, u.Role, u.EmailVerified)
	return err
}

func (s *PostgreSQLStore) GetAdminUser(username string) (*AdminUser, error) {
	var u AdminUser
	err := s.db.QueryRow(
		"SELECT username, password_hash, email, role, email_verified, created_at FROM admin_users WHERE username=$1", username,
	).Scan(&u.Username, &u.PasswordHash, &u.Email, &u.Role, &u.EmailVerified, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	return &u, nil
}

func (s *PostgreSQLStore) SetVerifyToken(email, token string) error {
	_, err := s.db.Exec(
		`INSERT INTO verify_tokens (email, token) VALUES ($1,$2) ON CONFLICT (email) DO UPDATE SET token=$2, created_at=NOW()`, email, token)
	return err
}

// Close 关闭数据库连接池。
func (s *PostgreSQLStore) Close() error {
	return s.db.Close()
}

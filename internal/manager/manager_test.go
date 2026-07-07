package manager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/ndzuki/release-manager/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebhookHandler_PushHelmChart(t *testing.T) {
	log := logr.Discard()

	var receivedNotification *ReleaseNotification
	handler := NewWebhookHandler(log, "", func(n ReleaseNotification) error {
		receivedNotification = &n
		return nil
	})

	payload := HarborWebhookPayload{
		Type:     "PUSH_HELMCHART",
		OccurAt:  time.Now().Unix(),
		Operator: "admin",
		EventData: struct {
			Resources  []HarborResource `json:"resources"`
			Repository HarborRepository `json:"repository"`
		}{
			Resources: []HarborResource{
				{
					Digest:      "sha256:abc123",
					Tag:         "0.0.15",
					ResourceURL: "oci://harbor.example.com/helm/magic-sandbox",
				},
			},
			Repository: HarborRepository{
				Name:         "helm/magic-sandbox",
				Namespace:    "library",
				RepoFullName: "library/helm/magic-sandbox",
				RepoType:     "CHART",
			},
		},
	}

	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/harbor", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, receivedNotification)
	assert.Equal(t, "helm/magic-sandbox", receivedNotification.ChartName)
	assert.Equal(t, "0.0.15", receivedNotification.ChartVersion)
	assert.Equal(t, "oci://harbor.example.com/helm/magic-sandbox", receivedNotification.ChartURL)
}

func TestWebhookHandler_NonChartEvent(t *testing.T) {
	log := logr.Discard()

	called := false
	handler := NewWebhookHandler(log, "", func(n ReleaseNotification) error {
		called = true
		return nil
	})

	payload := HarborWebhookPayload{
		Type:     "PUSH_ARTIFACT",
		OccurAt:  time.Now().Unix(),
		Operator: "admin",
	}

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/harbor", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.False(t, called, "should not trigger notification for non-chart events")
}

func TestWebhookHandler_MethodNotAllowed(t *testing.T) {
	log := logr.Discard()
	handler := NewWebhookHandler(log, "", nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/webhook/harbor", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestWebhookHandler_InvalidJSON(t *testing.T) {
	log := logr.Discard()
	handler := NewWebhookHandler(log, "", nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/harbor", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestWebhookHandler_NotifierError(t *testing.T) {
	log := logr.Discard()
	handler := NewWebhookHandler(log, "", func(n ReleaseNotification) error {
		return assert.AnError
	})

	payload := HarborWebhookPayload{
		Type:     "PUSH_HELMCHART",
		OccurAt:  time.Now().Unix(),
		Operator: "admin",
		EventData: struct {
			Resources  []HarborResource `json:"resources"`
			Repository HarborRepository `json:"repository"`
		}{
			Resources: []HarborResource{
				{Tag: "1.0.0", ResourceURL: "oci://harbor/helm/test"},
			},
			Repository: HarborRepository{Name: "helm/test"},
		},
	}

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/harbor", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestGenerateRequestID(t *testing.T) {
	id1 := GenerateRequestID()
	id2 := GenerateRequestID()

	assert.NotEmpty(t, id1)
	assert.NotEmpty(t, id2)
	assert.NotEqual(t, id1, id2, "request IDs should be unique")
	assert.Len(t, id1, 36) // UUID v4 format
}

func TestHarborWebhookPayload_Unmarshal(t *testing.T) {
	raw := `{
		"type": "PUSH_HELMCHART",
		"occur_at": 1718000000,
		"operator": "admin",
		"event_data": {
			"resources": [{
				"digest": "sha256:def456",
				"tag": "0.0.14",
				"resource_url": "oci://harbor.example.com/helm/magic-sandbox"
			}],
			"repository": {
				"name": "helm/magic-sandbox",
				"namespace": "library",
				"repo_full_name": "library/helm/magic-sandbox",
				"repo_type": "CHART"
			}
		}
	}`

	var payload HarborWebhookPayload
	err := json.Unmarshal([]byte(raw), &payload)
	require.NoError(t, err)

	assert.Equal(t, "PUSH_HELMCHART", payload.Type)
	assert.Equal(t, "admin", payload.Operator)
	assert.Equal(t, int64(1718000000), payload.OccurAt)
	assert.Len(t, payload.EventData.Resources, 1)
	assert.Equal(t, "0.0.14", payload.EventData.Resources[0].Tag)
}

// 确保 MemoryStore 实现 Store 接口
var _ Store = (*MemoryStore)(nil)

func TestMemoryStore_CreateAndGet(t *testing.T) {
	log := logr.Discard()
	store := NewMemoryStore(log)

	c := Customer{
		ID:               "cust-001",
		Name:             "Test Customer",
		OperatorEndpoint: "10.0.0.1:8443",
		CertFingerprint:  "ABCDEF1234567890",
		Enabled:          true,
	}

	created, err := store.CreateCustomer(c)
	require.NoError(t, err)
	assert.Equal(t, "cust-001", created.ID)
	assert.NotZero(t, created.CreatedAt)

	got, err := store.GetCustomer("cust-001")
	require.NoError(t, err)
	assert.Equal(t, "Test Customer", got.Name)
	assert.Equal(t, "10.0.0.1:8443", got.OperatorEndpoint)
	assert.True(t, got.Enabled)
}

func TestMemoryStore_ListCustomers(t *testing.T) {
	log := logr.Discard()
	store := NewMemoryStore(log)

	_, _ = store.CreateCustomer(Customer{ID: "a", Name: "A", Enabled: true})
	_, _ = store.CreateCustomer(Customer{ID: "b", Name: "B", Enabled: false})
	_, _ = store.CreateCustomer(Customer{ID: "c", Name: "C", Enabled: true})

	all, err := store.ListCustomers(false)
	require.NoError(t, err)
	assert.Len(t, all, 3)

	enabled, err := store.ListCustomers(true)
	require.NoError(t, err)
	assert.Len(t, enabled, 2)
}

func TestMemoryStore_UpdateCustomer(t *testing.T) {
	log := logr.Discard()
	store := NewMemoryStore(log)

	_, _ = store.CreateCustomer(Customer{ID: "c1", Name: "Original", Enabled: true})
	_, err := store.UpdateCustomer(Customer{ID: "c1", Name: "Updated", Enabled: false})
	require.NoError(t, err)

	got, _ := store.GetCustomer("c1")
	assert.Equal(t, "Updated", got.Name)
	assert.False(t, got.Enabled)
}

func TestMemoryStore_DeleteCustomer(t *testing.T) {
	log := logr.Discard()
	store := NewMemoryStore(log)

	_, _ = store.CreateCustomer(Customer{ID: "d1", Name: "Delete Me"})
	err := store.DeleteCustomer("d1")
	require.NoError(t, err)

	_, err = store.GetCustomer("d1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestMemoryStore_ReleaseRecords(t *testing.T) {
	log := logr.Discard()
	store := NewMemoryStore(log)

	now := time.Now()
	r := ReleaseRecord{
		RequestID:    "req-123",
		CustomerID:   "cust-001",
		ChartName:    "magic-sandbox",
		ChartVersion: "0.0.15",
		Status:       "PENDING",
		StartedAt:    now,
	}

	err := store.CreateReleaseRecord(r)
	require.NoError(t, err)

	records, err := store.ListReleaseRecords("req-123")
	require.NoError(t, err)
	assert.Len(t, records, 1)
	assert.Equal(t, "magic-sandbox", records[0].ChartName)

	err = store.UpdateReleaseRecord("req-123", "cust-001", "SUCCEEDED", "", now.Add(time.Minute), 60)
	require.NoError(t, err)

	records, _ = store.ListReleaseRecords("req-123")
	assert.Equal(t, "SUCCEEDED", records[0].Status)
	assert.Equal(t, int64(60), records[0].DurationSecs)
}

func TestMemoryStore_DuplicateCustomer(t *testing.T) {
	log := logr.Discard()
	store := NewMemoryStore(log)

	_, err := store.CreateCustomer(Customer{ID: "dup", Name: "First"})
	require.NoError(t, err)

	_, err = store.CreateCustomer(Customer{ID: "dup", Name: "Second"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

// ---------------------------------------------------------------------------
// 钉钉单元测试
// ---------------------------------------------------------------------------

func TestDingTalkClient_SkipEmptyWebhook(t *testing.T) {
	log := logr.Discard()
	client := NewDingTalkClient("", "", log)

	err := client.SendReleaseNotification("test", "1.0", nil)
	assert.NoError(t, err)
}

func TestDingTalkClient_SignURL(t *testing.T) {
	url, err := signURL("https://oapi.dingtalk.com/robot/send?access_token=test", "secret123")
	require.NoError(t, err)
	assert.Contains(t, url, "timestamp=")
	assert.Contains(t, url, "sign=")
}

func TestDingTalkMessage_MarkdownFormat(t *testing.T) {
	results := []ForwardResult{
		{CustomerName: "Client A", Success: true, ErrorMessage: "", Duration: 30 * time.Second},
		{CustomerName: "Client B", Success: false, ErrorMessage: "timeout", Duration: 120 * time.Second},
	}

	// With empty webhook URL, send is skipped — test just the formatting path exists
	log := logr.Discard()
	client := NewDingTalkClient("", "secret", log)
	err := client.SendReleaseNotification("magic-sandbox", "0.0.15", results)
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Forwarder 单元测试（无实际 gRPC）
// ---------------------------------------------------------------------------

func TestForwarder_NoCustomers(t *testing.T) {
	log := logr.Discard()
	store := NewMemoryStore(log)
	forwarder := NewForwarder(store, nil, log, 10*time.Second)

	results, err := forwarder.ForwardToAll(context.Background(), ReleaseNotification{
		ChartName:    "test",
		ChartVersion: "1.0",
	})

	assert.NoError(t, err)
	assert.Nil(t, results)
}

func TestForwarder_OnlyEnabled(t *testing.T) {
	log := logr.Discard()
	store := NewMemoryStore(log)

	_, _ = store.CreateCustomer(Customer{ID: "a", Name: "Enabled", OperatorEndpoint: "1.2.3.4:8443", Enabled: true})
	_, _ = store.CreateCustomer(Customer{ID: "b", Name: "Disabled", OperatorEndpoint: "5.6.7.8:8443", Enabled: false})

	forwarder := NewForwarder(store, nil, log, 1*time.Second)

	results, err := forwarder.ForwardToAll(context.Background(), ReleaseNotification{
		ChartName:    "test",
		ChartVersion: "1.0",
	})

	assert.NoError(t, err)
	assert.Len(t, results, 1) // only the enabled one
	assert.Equal(t, "a", results[0].CustomerID)
	assert.False(t, results[0].Success) // will fail because no real gRPC server
}

// ---------------------------------------------------------------------------
// bcrypt 密码哈希
// ---------------------------------------------------------------------------

func TestHashPassword_Bcrypt(t *testing.T) {
	hash := hashPassword("testpassword123")
	assert.True(t, strings.HasPrefix(hash, "$2a$"), "hash should be bcrypt format starting with $2a$")
	assert.NotEqual(t, "testpassword123", hash, "hash should not be plaintext")

	// Verify same password produces different hash (bcrypt uses random salt)
	hash2 := hashPassword("testpassword123")
	assert.NotEqual(t, hash, hash2, "bcrypt should produce different hashes due to random salt")
}

func TestVerifyLoginPassword_Bcrypt(t *testing.T) {
	hash := hashPassword("secure-password")
	user := &AdminUser{Username: "test", PasswordHash: hash, Email: "test@example.com", Role: "admin"}

	assert.True(t, verifyLoginPassword(user, "secure-password"),
		"bcrypt should verify correct password")
	assert.False(t, verifyLoginPassword(user, "wrong-password"),
		"bcrypt should reject wrong password")
}

func TestVerifyLoginPassword_SHA256Compat(t *testing.T) {
	// Simulate legacy SHA256 hash
	legacyHash := sha256Hex("old-password")
	user := &AdminUser{Username: "legacy", PasswordHash: legacyHash, Email: "legacy@example.com", Role: "admin"}

	assert.True(t, verifyLoginPassword(user, "old-password"),
		"SHA256 compat should verify correct password")
	assert.False(t, verifyLoginPassword(user, "wrong"),
		"SHA256 compat should reject wrong password")
}

// ---------------------------------------------------------------------------
// base64Decode
// ---------------------------------------------------------------------------

func TestBase64Decode(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"standard padded", base64.StdEncoding.EncodeToString([]byte(`{"sub":"user1"}`))},
		{"url-safe unpadded (JWT)", base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user2","email":"a@b.com"}`))},
		{"raw standard unpadded", base64.RawStdEncoding.EncodeToString([]byte(`{"sub":"user3"}`))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := base64Decode(tt.input)
			require.NoError(t, err)
			assert.Contains(t, string(data), `"sub"`)
		})
	}

	// JWT payload: typical eyJ... format (base64 url-safe unpadded)
	jwtPayload := "eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ"
	data, err := base64Decode(jwtPayload)
	require.NoError(t, err)
	assert.Contains(t, string(data), "John")
}

// ---------------------------------------------------------------------------
// SQLiteStore 用户管理 & 初始化 & Chart 配置
// ---------------------------------------------------------------------------

func newTestSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(":memory:", logr.Discard())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSQLiteStore_UserManagement(t *testing.T) {
	store := newTestSQLiteStore(t)

	u := User{
		ID: "user-1", OrgID: "org-1", Name: "Test User",
		Email: "user@example.com", Role: "operator",
		AuthProvider: "oidc", ExternalID: "ext-1",
		Enabled:   true,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		UpdatedAt: time.Now().UTC().Truncate(time.Second),
	}

	// Create
	err := store.CreateUser(u)
	require.NoError(t, err)

	// GetUser
	got, err := store.GetUser("user-1")
	require.NoError(t, err)
	assert.Equal(t, "Test User", got.Name)
	assert.Equal(t, "operator", got.Role)
	assert.Equal(t, "user@example.com", got.Email)

	// GetUserByEmail
	gotByEmail, err := store.GetUserByEmail("user@example.com")
	require.NoError(t, err)
	assert.Equal(t, "user-1", gotByEmail.ID)

	// ListUsers
	u2 := User{ID: "user-2", OrgID: "org-1", Name: "User 2", Email: "u2@example.com", Role: "viewer", Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, store.CreateUser(u2))

	users, err := store.ListUsers()
	require.NoError(t, err)
	assert.Len(t, users, 2)
}

func TestSQLiteStore_InitFlow(t *testing.T) {
	store := newTestSQLiteStore(t)

	// Initially not initialized
	init, err := store.GetInitStatus()
	require.NoError(t, err)
	assert.False(t, init)

	// Create admin user
	admin := AdminUser{
		Username:      "admin",
		PasswordHash:  "$2a$12$...",
		Email:         "admin@example.com",
		Role:          "admin",
		EmailVerified: true,
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
	}
	err = store.CreateAdminUser(admin)
	require.NoError(t, err)

	// Get admin user
	got, err := store.GetAdminUser("admin")
	require.NoError(t, err)
	assert.Equal(t, "admin@example.com", got.Email)
	assert.Equal(t, "admin", got.Role)

	// Set verify token
	err = store.SetVerifyToken("admin@example.com", "token-abc123")
	require.NoError(t, err)

	// Set init status
	err = store.SetInitStatus(true)
	require.NoError(t, err)

	init, err = store.GetInitStatus()
	require.NoError(t, err)
	assert.True(t, init)

	// Replace admin user (insert or replace)
	admin.PasswordHash = "$2a$12$newhash..."
	err = store.CreateAdminUser(admin)
	require.NoError(t, err)
	got, err = store.GetAdminUser("admin")
	require.NoError(t, err)
	assert.Equal(t, "$2a$12$newhash...", got.PasswordHash)
}

func TestSQLiteStore_ChartConfig(t *testing.T) {
	store := newTestSQLiteStore(t)

	// Create chart definitions
	cd1 := ChartDefinition{
		ID: "chart-1", OrgID: "org-1", Name: "Magic Sandbox",
		Description: "Sandbox app", OCIURL: "oci://harbor/helm/magic-sandbox",
		Enabled: true,
	}
	_, err := store.CreateChartDefinition(cd1)
	require.NoError(t, err)

	cd2 := ChartDefinition{
		ID: "chart-2", OrgID: "org-1", Name: "Monitor",
		Description: "Monitoring", OCIURL: "oci://harbor/helm/monitor",
		Enabled: true,
	}
	_, err = store.CreateChartDefinition(cd2)
	require.NoError(t, err)

	// List by org
	charts, err := store.ListChartDefinitions("org-1")
	require.NoError(t, err)
	assert.Len(t, charts, 2)

	// Get specific
	got, err := store.GetChartDefinition("org-1", "chart-1")
	require.NoError(t, err)
	assert.Equal(t, "Magic Sandbox", got.Name)

	// Create customer chart binding
	binding := CustomerChartBinding{
		ID: "bind-1", OrgID: "org-1", CustomerID: "cust-1",
		ChartID: "chart-1", ChartName: "Magic Sandbox",
		Enabled: true, ReleaseName: "magic-sandbox-cust1",
		Namespace: "production", DeployOrder: 0,
	}
	_, err = store.CreateCustomerChartBinding(binding)
	require.NoError(t, err)

	binding2 := CustomerChartBinding{
		ID: "bind-2", OrgID: "org-1", CustomerID: "cust-1",
		ChartID: "chart-2", ChartName: "Monitor",
		Enabled: true, ReleaseName: "monitor-cust1",
		Namespace: "monitoring", DeployOrder: 1,
	}
	_, err = store.CreateCustomerChartBinding(binding2)
	require.NoError(t, err)

	// List bindings for customer
	bindings, err := store.ListCustomerChartBindings("org-1", "cust-1")
	require.NoError(t, err)
	assert.Len(t, bindings, 2)
	assert.Equal(t, "magic-sandbox-cust1", bindings[0].ReleaseName)

	// Delete binding
	err = store.DeleteCustomerChartBinding("org-1", "bind-1")
	require.NoError(t, err)

	bindings, err = store.ListCustomerChartBindings("org-1", "cust-1")
	require.NoError(t, err)
	assert.Len(t, bindings, 1)
	assert.Equal(t, "bind-2", bindings[0].ID)
}

// ---------------------------------------------------------------------------
// 审计日志
// ---------------------------------------------------------------------------

func TestSQLiteStore_AuditLog(t *testing.T) {
	store := newTestSQLiteStore(t)

	entry := AuditLogEntry{
		Timestamp:      time.Now().UTC().Truncate(time.Second),
		UserID:         "admin",
		Username:       "Administrator",
		OrgID:          "default",
		Action:         "POST",
		Resource:       "customer",
		ResourceID:     "cust-001",
		Method:         "POST",
		Path:           "/api/v1/customers",
		StatusCode:     201,
		ClientIP:       "10.0.0.1",
		UserAgent:      "curl/8.0",
		ReqBodySnippet: `{"id":"cust-001","name":"Test"}`,
		DurationMs:     45,
	}

	// Create
	err := store.CreateAuditLog(entry)
	require.NoError(t, err)

	// List with filter: by user
	logs, err := store.ListAuditLogs(AuditLogFilter{UserID: "admin", Limit: 50})
	require.NoError(t, err)
	assert.Len(t, logs, 1)
	assert.Equal(t, "customer", logs[0].Resource)
	assert.Equal(t, "cust-001", logs[0].ResourceID)
	assert.Equal(t, int64(45), logs[0].DurationMs)

	// Filter by resource
	logs, err = store.ListAuditLogs(AuditLogFilter{Resource: "customer", Limit: 50})
	require.NoError(t, err)
	assert.Len(t, logs, 1)

	// Filter with no match
	logs, err = store.ListAuditLogs(AuditLogFilter{UserID: "nonexistent", Limit: 50})
	require.NoError(t, err)
	assert.Len(t, logs, 0)
}

func TestAuditLogger_Middleware(t *testing.T) {
	store := NewMemoryStore(logr.Discard())
	auditLogger := NewAuditLogger(store, logr.Discard())
	defer auditLogger.Close()

	// Wrap a simple handler
	handler := auditLogger.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/customers", nil)
	req.RemoteAddr = "10.0.0.5:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Wait for async write
	time.Sleep(50 * time.Millisecond)

	logs, err := store.ListAuditLogs(AuditLogFilter{Limit: 50})
	require.NoError(t, err)
	require.Len(t, logs, 1)
	assert.Equal(t, "GET", logs[0].Method)
	assert.Equal(t, "/api/v1/customers", logs[0].Path)
	assert.Equal(t, http.StatusOK, logs[0].StatusCode)
	assert.Equal(t, "anonymous", logs[0].UserID)
	assert.True(t, logs[0].DurationMs >= 0)
}

func TestAuditLogger_AsyncWrite(t *testing.T) {
	store := NewMemoryStore(logr.Discard())
	auditLogger := NewAuditLogger(store, logr.Discard())
	defer auditLogger.Close()

	// Send multiple requests rapidly
	for i := 0; i < 20; i++ {
		go func(n int) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/releases", nil)
			req.RemoteAddr = "10.0.0.1:12345"
			req.Header.Set("X-API-Key", "test-key") // auth middleware would set user in real path
			w := httptest.NewRecorder()
			auditLogger.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
			})).ServeHTTP(w, req)
		}(i)
	}

	// Give async writer time to flush
	time.Sleep(100 * time.Millisecond)

	logs, err := store.ListAuditLogs(AuditLogFilter{Limit: 200})
	require.NoError(t, err)
	assert.Len(t, logs, 20, "all 20 audit entries should be persisted")
}

func TestClassifyRequest(t *testing.T) {
	tests := []struct {
		method, path          string
		expectedRes, expectedID string
	}{
		{"GET", "/api/v1/customers", "customer", ""},
		{"GET", "/api/v1/customers/cust-001", "customer", "cust-001"},
		{"POST", "/api/v1/customers", "customer", ""},
		{"PUT", "/api/v1/customers/cust-001", "customer", "cust-001"},
		{"DELETE", "/api/v1/customers/cust-001", "customer", "cust-001"},
		{"GET", "/api/v1/releases/req-123", "release", "req-123"},
		{"GET", "/api/v1/users", "user", ""},
		{"GET", "/api/v1/users/user-1", "user", "user-1"},
		{"GET", "/api/v1/orgs/", "org", ""},
		{"GET", "/api/v1/dashboard/", "dashboard", ""},
		{"GET", "/api/v1/audit-logs", "audit_log", ""},
		{"POST", "/api/v1/init", "init", ""},
		{"POST", "/api/v1/auth/login", "auth", ""},
		{"POST", "/api/v1/webhook/harbor", "webhook", ""},
		{"GET", "/health", "health", ""},
		{"GET", "/some/unknown/path", "unknown", ""},
	}

	for _, tt := range tests {
		resource, resourceID := classifyRequest(tt.method, tt.path)
		assert.Equal(t, tt.expectedRes, resource, "method=%s path=%s", tt.method, tt.path)
		assert.Equal(t, tt.expectedID, resourceID, "method=%s path=%s", tt.method, tt.path)
	}
}

// ---------------------------------------------------------------------------
// HTTP Handler 层测试
// ---------------------------------------------------------------------------

func TestHandleCustomers_CRUD(t *testing.T) {
	log := logr.Discard()
	store := NewMemoryStore(log)

	cfg := &config.Config{APIKey: "test-key"}
	srv := &Server{
		cfg:   cfg,
		log:   log,
		store: store,
	}

	// POST - Create customer
	createBody := `{"id":"cust-001","name":"Test","operator_endpoint":"10.0.0.1:8443","cert_fingerprint":"ABC123","enabled":true}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/customers", strings.NewReader(createBody))
	createReq.Header.Set("X-API-Key", "test-key")
	createW := httptest.NewRecorder()
	srv.Handler().ServeHTTP(createW, createReq)
	assert.Equal(t, http.StatusCreated, createW.Code, "POST should return 201")

	// GET - List customers
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/customers", nil)
	listReq.Header.Set("X-API-Key", "test-key")
	listW := httptest.NewRecorder()
	srv.Handler().ServeHTTP(listW, listReq)
	assert.Equal(t, http.StatusOK, listW.Code)

	// GET - Single customer
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/customers/cust-001", nil)
	getReq.Header.Set("X-API-Key", "test-key")
	getW := httptest.NewRecorder()
	srv.Handler().ServeHTTP(getW, getReq)
	assert.Equal(t, http.StatusOK, getW.Code)

	// PUT - Update customer
	updateBody := `{"name":"Updated Name","enabled":false}`
	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/customers/cust-001", strings.NewReader(updateBody))
	updateReq.Header.Set("X-API-Key", "test-key")
	updateW := httptest.NewRecorder()
	srv.Handler().ServeHTTP(updateW, updateReq)
	assert.Equal(t, http.StatusOK, updateW.Code)

	// DELETE - Delete customer
	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/customers/cust-001", nil)
	deleteReq.Header.Set("X-API-Key", "test-key")
	deleteW := httptest.NewRecorder()
	srv.Handler().ServeHTTP(deleteW, deleteReq)
	assert.Equal(t, http.StatusOK, deleteW.Code)
}

func TestHandleCustomers_Unauthorized(t *testing.T) {
	log := logr.Discard()
	store := NewMemoryStore(log)

	cfg := &config.Config{APIKey: "test-key"}
	srv := &Server{
		cfg:   cfg,
		log:   log,
		store: store,
	}

	// Without API key
	req := httptest.NewRequest(http.MethodGet, "/api/v1/customers", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandleCustomers_Validation(t *testing.T) {
	log := logr.Discard()
	store := NewMemoryStore(log)

	cfg := &config.Config{APIKey: "test-key"}
	srv := &Server{
		cfg:   cfg,
		log:   log,
		store: store,
	}

	// Missing required fields
	body := `{"id":"","name":"","operator_endpoint":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/customers", strings.NewReader(body))
	req.Header.Set("X-API-Key", "test-key")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleReleases(t *testing.T) {
	log := logr.Discard()
	store := NewMemoryStore(log)

	cfg := &config.Config{APIKey: "test-key"}
	srv := &Server{
		cfg:   cfg,
		log:   log,
		store: store,
	}

	// Create a release record first
	_ = store.CreateReleaseRecord(ReleaseRecord{
		RequestID:    "req-001",
		CustomerID:   "cust-001",
		ChartName:    "magic-sandbox",
		ChartVersion: "1.0.0",
		Status:       "success",
		StartedAt:    time.Now(),
	})

	// GET releases
	req := httptest.NewRequest(http.MethodGet, "/api/v1/releases/req-001", nil)
	req.Header.Set("X-API-Key", "test-key")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandleLogin(t *testing.T) {
	log := logr.Discard()
	store := NewSQLiteInMemoryStore(t)

	// Create an admin user via the store
	hash := hashPassword("testpassword")
	err := store.CreateAdminUser(AdminUser{
		Username:      "testadmin",
		PasswordHash:  hash,
		Email:         "admin@test.com",
		Role:          "admin",
		EmailVerified: true,
		CreatedAt:     time.Now(),
	})
	require.NoError(t, err)

	cfg := &config.Config{APIKey: ""}
	mockSMTP := SMTPConfig{Enabled: false}
	initH := NewInitHandler(store, mockSMTP, false, log)

	srv := &Server{
		cfg:         cfg,
		log:         log,
		store:       store,
		initHandler: initH,
	}

	// POST /api/v1/auth/login with correct credentials
	loginBody := `{"username":"testadmin","password":"testpassword"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Wrong password
	loginBody = `{"username":"testadmin","password":"wrong"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHealthEndpoint(t *testing.T) {
	log := logr.Discard()
	store := NewMemoryStore(log)

	cfg := &config.Config{APIKey: ""}
	srv := &Server{
		cfg:   cfg,
		log:   log,
		store: store,
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"ok"`)
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// Handler returns the Server's HTTP handler assembly for httptest.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Init + Login (no auth)
	mux.Handle("/api/v1/init", s.initHandler)
	mux.HandleFunc("/api/v1/auth/login", s.initHandler.HandleLogin)

	// Webhook
	if s.webhook != nil {
		mux.Handle("/api/v1/webhook/harbor", s.webhook)
	}

	// Chart config & dashboard (auth middleware)
	if s.authMiddleware != nil {
		if s.chartConfig != nil {
			mux.Handle("/api/v1/orgs/", s.authMiddleware.Handler(s.chartConfig))
			mux.Handle("/api/v1/dashboard/", s.authMiddleware.Handler(s.chartConfig))
		}
		if s.userRBAC != nil {
			mux.Handle("/api/v1/users", s.authMiddleware.Handler(s.userRBAC))
			mux.Handle("/api/v1/users/", s.authMiddleware.Handler(s.userRBAC))
		}
	}

	// Audit logs
	if s.store != nil {
		mux.Handle("/api/v1/audit-logs", http.HandlerFunc(s.handleAuditLogs))
	}

	// REST API (API key auth)
	authHandler := s.apiKeyMiddleware(http.HandlerFunc(s.routeREST))
	mux.Handle("/api/v1/customers", authHandler)
	mux.Handle("/api/v1/customers/", authHandler)
	mux.Handle("/api/v1/releases/", authHandler)

	// Health
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	return mux
}

// NewSQLiteInMemoryStore creates a SQLiteStore for testing using :memory: DSN.
func NewSQLiteInMemoryStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(":memory:", logr.Discard())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

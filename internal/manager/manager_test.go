package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
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

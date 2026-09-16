package webhook

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
)

// recordingBundleClient captures the Harbor forward so the ingress test can
// assert the credential and the source id at the forwarding boundary
// (REQ-011 §562: the Harbor key is scoped to RecordArtifactEvent).
type recordingBundleClient struct {
	orchestratorv1connect.BundleServiceClient
	lastRecord   *connect.Request[orchestratorv1.RecordArtifactEventRequest]
	recordResult func() (*orchestratorv1.RecordArtifactEventResponse, error)
}

func (c *recordingBundleClient) RecordArtifactEvent(
	_ context.Context, req *connect.Request[orchestratorv1.RecordArtifactEventRequest],
) (*connect.Response[orchestratorv1.RecordArtifactEventResponse], error) {
	c.lastRecord = req
	result, err := c.recordResult()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(result), nil
}

func harborEventBody(t *testing.T) string {
	t.Helper()
	event := map[string]any{
		"id":   "event-001",
		"type": "harbor.artifact.pushed",
		"time": "2026-09-16T12:00:00Z",
		"data": map[string]any{
			"repository": map[string]any{"name": "team/api"},
			"resources": []map[string]any{{
				"digest": "sha256:abc", "tag": "v1", "resource_url": "registry.example/team/api:v1",
			}},
		},
	}
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	return string(raw)
}

func TestHarborIngressForwardsWithItsOwnCredential(t *testing.T) {
	client := &recordingBundleClient{recordResult: func() (*orchestratorv1.RecordArtifactEventResponse, error) {
		return &orchestratorv1.RecordArtifactEventResponse{EventRecordId: "event-001", Created: true}, nil
	}}
	handler := NewHarborHandler(client, "harbor-prod", "harbor-service-token")
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/webhooks/harbor", strings.NewReader(harborEventBody(t)))
	require.NoError(t, err)
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	assert.Equal(t, http.StatusOK, response.StatusCode)

	require.NotNil(t, client.lastRecord)
	assert.Equal(t, "Bearer harbor-service-token", client.lastRecord.Header().Get("Authorization"),
		"the Harbor forward must carry the Harbor service token")
	assert.Equal(t, "harbor-prod", client.lastRecord.Msg.GetSourceId())
	assert.Equal(t, "event-001", client.lastRecord.Msg.GetEventId())
	assert.Equal(t, "harbor.artifact.pushed", client.lastRecord.Msg.GetEventType())
	require.Len(t, client.lastRecord.Msg.GetResources(), 1)
	assert.Equal(t, "sha256:abc", client.lastRecord.Msg.GetResources()[0].GetDigest())
}

func TestHarborIngressRejectsMalformedInput(t *testing.T) {
	client := &recordingBundleClient{recordResult: func() (*orchestratorv1.RecordArtifactEventResponse, error) {
		return &orchestratorv1.RecordArtifactEventResponse{}, nil
	}}
	logger := slog.New(slog.DiscardHandler)
	_ = logger
	handler := NewHarborHandler(client, "harbor-prod", "harbor-service-token")

	tests := []struct {
		name     string
		method   string
		body     string
		wantCode int
	}{
		{name: "non POST", method: http.MethodGet, body: "", wantCode: http.StatusMethodNotAllowed},
		{name: "invalid json", method: http.MethodPost, body: "{", wantCode: http.StatusBadRequest},
		{name: "unsupported event type", method: http.MethodPost, body: `{"id":"e1","type":"other.event"}`, wantCode: http.StatusBadRequest},
		{name: "missing event id", method: http.MethodPost, body: `{"type":"harbor.artifact.pushed"}`, wantCode: http.StatusBadRequest},
		{name: "no resources", method: http.MethodPost, body: `{"id":"e1","type":"harbor.artifact.pushed","data":{"resources":[]}}`, wantCode: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(context.Background(), tt.method, "/webhooks/harbor", strings.NewReader(tt.body))
			handler.ServeHTTP(rec, req)
			assert.Equal(t, tt.wantCode, rec.Code)
			assert.Nil(t, client.lastRecord, "a rejected event must not reach the orchestrator")
		})
	}
}

func TestHarborIngressReportsDuplicatesAsOK(t *testing.T) {
	client := &recordingBundleClient{recordResult: func() (*orchestratorv1.RecordArtifactEventResponse, error) {
		return nil, connect.NewError(connect.CodeAlreadyExists, assert.AnError)
	}}
	handler := NewHarborHandler(client, "harbor-prod", "harbor-service-token")
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks/harbor", strings.NewReader(harborEventBody(t)))
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "duplicate")
}

package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWebhookReadinessCheckReflectsOrchestrator pins TASK-099 AC3 for the
// webhook: /readyz must mirror the upstream orchestrator (200 pass-through,
// 503 failure, connection refusal failure), never the old noop.
func TestWebhookReadinessCheckReflectsOrchestrator(t *testing.T) {
	t.Parallel()

	var status atomic.Int32
	status.Store(http.StatusOK)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/readyz", r.URL.Path)
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(upstream.Close)

	checks := (&webhookSvc{orchestratorURL: upstream.URL}).ReadinessChecks()
	check, ok := checks["orchestrator"]
	require.True(t, ok, "orchestrator check must exist")

	require.NoError(t, check(), "orchestrator ready must pass the webhook check")

	status.Store(http.StatusServiceUnavailable)
	require.Error(t, check(), "orchestrator degraded must fail the webhook check")

	upstream.Close()
	require.Error(t, check(), "unreachable orchestrator must fail the webhook check")
}

// TestWebhookReadinessUsesConfiguredDefaultURL: an unset flag keeps the same
// default as Register's client (single source of truth).
func TestWebhookReadinessUsesConfiguredDefaultURL(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "http://localhost:8083", (&webhookSvc{}).orchestratorBaseURL())
	assert.Equal(t, "http://orchestrator:8083", (&webhookSvc{orchestratorURL: "http://orchestrator:8083"}).orchestratorBaseURL())
}

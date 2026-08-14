package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// getEnvironment executes the handler and decodes the payload.
func getEnvironment(t *testing.T, service string) (environmentResponse, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/environment", nil)
	environmentHandler(service)(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp environmentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp, rec
}

func TestEnvironmentDefaults(t *testing.T) {
	t.Setenv("APP_ENVIRONMENT", "")
	t.Setenv("ENVIRONMENT_ID", "")
	t.Setenv("DEV_PROFILE", "")
	t.Setenv("E2E_RUN_ID", "")
	t.Setenv("APP_PRODUCTION", "")

	resp, rec := getEnvironment(t, "webhook")
	require.Equal(t, "webhook", resp.Service)
	require.Equal(t, "development", resp.Environment)
	require.Equal(t, "dev-local", resp.EnvironmentID)
	require.False(t, resp.Production)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestEnvironmentCiProfile(t *testing.T) {
	t.Setenv("APP_ENVIRONMENT", "")
	t.Setenv("ENVIRONMENT_ID", "")
	t.Setenv("DEV_PROFILE", "ci")
	t.Setenv("E2E_RUN_ID", "run-42")
	t.Setenv("APP_PRODUCTION", "")

	resp, _ := getEnvironment(t, "orchestrator")
	require.Equal(t, "ci-run-42", resp.EnvironmentID)
	require.False(t, resp.Production)
}

func TestEnvironmentInjectedMetadataWins(t *testing.T) {
	t.Setenv("APP_ENVIRONMENT", "staging")
	t.Setenv("ENVIRONMENT_ID", "injected-id")
	t.Setenv("DEV_PROFILE", "ci")
	t.Setenv("E2E_RUN_ID", "ignored")
	t.Setenv("APP_PRODUCTION", "true")

	resp, _ := getEnvironment(t, "auth")
	require.Equal(t, "staging", resp.Environment)
	require.Equal(t, "injected-id", resp.EnvironmentID)
	require.True(t, resp.Production)
}

func TestEnvironmentProductionOnlyExactTrue(t *testing.T) {
	t.Setenv("APP_PRODUCTION", "1")
	resp, _ := getEnvironment(t, "notifier")
	require.False(t, resp.Production)

	t.Setenv("APP_PRODUCTION", "true")
	resp, _ = getEnvironment(t, "notifier")
	require.True(t, resp.Production)
}

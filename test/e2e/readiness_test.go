package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckReadinessChecksAllDeclaredEndpoints(t *testing.T) {
	t.Parallel()

	const (
		environment   = "ci"
		environmentID = "ci-run-123"
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/health", "/readyz":
			writer.WriteHeader(http.StatusOK)
			if _, err := writer.Write([]byte(`{"status":"ok"}`)); err != nil {
				t.Errorf("write readiness body: %v", err)
			}
		case "/environment":
			writer.WriteHeader(http.StatusOK)
			if err := json.NewEncoder(writer).Encode(EnvironmentObservation{
				Service:       strings.TrimPrefix(request.Host, "service-"),
				Environment:   environment,
				EnvironmentID: environmentID,
			}); err != nil {
				t.Errorf("encode environment observation: %v", err)
			}
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := &Config{
		Environment:   environment,
		EnvironmentID: environmentID,
		Endpoints: Endpoints{
			ReleaseOrchestrator: server.URL,
			ReleaseWebhook:      server.URL,
			ReleaseOperator:     server.URL,
			ReleaseAuth:         server.URL,
			ReleaseNotifier:     server.URL,
			ReleaseAPI:          server.URL,
		},
	}
	result, err := CheckReadiness(context.Background(), cfg, nil, time.Second)
	require.NoError(t, err)
	require.Len(t, result.Endpoints, 6)
	for _, endpoint := range result.Endpoints {
		assert.Equal(t, http.StatusOK, endpoint.HealthStatus)
		assert.Equal(t, http.StatusOK, endpoint.ReadyStatus)
		assert.Equal(t, environment, endpoint.Environment.Environment)
		assert.Equal(t, environmentID, endpoint.Environment.EnvironmentID)
		assert.False(t, endpoint.Environment.Production)
	}
}

func TestCheckReadinessRejectsProductionEnvironment(t *testing.T) {
	t.Parallel()

	server := readinessServer(t, EnvironmentObservation{
		Service:       "orchestrator",
		Environment:   "ci",
		EnvironmentID: "ci-run-123",
		Production:    true,
	})
	defer server.Close()

	cfg := readinessConfig(server.URL)
	_, err := CheckReadiness(context.Background(), cfg, nil, time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEnvironmentUnhealthy)
	assert.ErrorContains(t, err, "production environment is not allowed")
}

func TestCheckReadinessRejectsEnvironmentMismatch(t *testing.T) {
	t.Parallel()

	server := readinessServer(t, EnvironmentObservation{
		Service:       "orchestrator",
		Environment:   "other",
		EnvironmentID: "ci-run-123",
	})
	defer server.Close()

	cfg := readinessConfig(server.URL)
	_, err := CheckReadiness(context.Background(), cfg, nil, time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEnvironmentUnhealthy)
	assert.ErrorContains(t, err, "environment mismatch")
}

func TestCheckReadinessRejectsMissingEndpoint(t *testing.T) {
	t.Parallel()

	cfg := readinessConfig("http://127.0.0.1")
	cfg.Endpoints.ReleaseNotifier = ""
	_, err := CheckReadiness(context.Background(), cfg, nil, time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConfigInvalid)
	assert.ErrorContains(t, err, "endpoints.release_notifier")
}

func TestCheckReadinessHonorsContextTimeout(t *testing.T) {
	t.Parallel()

	// The timeout is proven by the request context, so the handler never writes
	// a response and does not need the ResponseWriter.
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := CheckReadiness(ctx, readinessConfig(server.URL), nil, time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEnvironmentUnhealthy)
	assert.ErrorContains(t, err, "request failed")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestReadinessErrorDoesNotExposeURLOrResponseBody(t *testing.T) {
	t.Parallel()

	secretBody := "password=do-not-log"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		if _, err := writer.Write([]byte(secretBody)); err != nil {
			t.Errorf("write secret body: %v", err)
		}
	}))
	defer server.Close()

	_, err := CheckReadiness(context.Background(), readinessConfig(server.URL), nil, time.Second)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), server.URL)
	assert.NotContains(t, err.Error(), secretBody)
}

func readinessConfig(baseURL string) *Config {
	return &Config{
		Environment:   "ci",
		EnvironmentID: "ci-run-123",
		Endpoints: Endpoints{
			ReleaseOrchestrator: baseURL,
			ReleaseWebhook:      baseURL,
			ReleaseOperator:     baseURL,
			ReleaseAuth:         baseURL,
			ReleaseNotifier:     baseURL,
			ReleaseAPI:          baseURL,
		},
	}
}

func readinessServer(t *testing.T, environment EnvironmentObservation) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/health", "/readyz":
			writer.WriteHeader(http.StatusOK)
			if _, err := writer.Write([]byte(`{"status":"ok"}`)); err != nil {
				t.Errorf("write readiness body: %v", err)
			}
		case "/environment":
			writer.WriteHeader(http.StatusOK)
			if err := json.NewEncoder(writer).Encode(environment); err != nil {
				t.Errorf("encode environment observation: %v", err)
			}
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
}

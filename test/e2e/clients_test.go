package e2e

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClientBundleConstructsTypedClients(t *testing.T) {
	t.Parallel()

	cfg := testClientConfig()
	clients, err := NewClientBundle(cfg)
	require.NoError(t, err)
	require.NotNil(t, clients)

	assert.NotNil(t, clients.auth)
	assert.NotNil(t, clients.organization)
	assert.NotNil(t, clients.binding)
	assert.NotNil(t, clients.authorization)
	assert.NotNil(t, clients.externalIdentity)
	assert.NotNil(t, clients.orchestrator)
	assert.NotNil(t, clients.bundle)
	assert.NotNil(t, clients.cleanup)
	assert.NotNil(t, clients.trust)
	assert.NotNil(t, clients.webhook)
	assert.NotNil(t, clients.operator)
	assert.NotNil(t, clients.notifier)
}

func TestNewClientBundleRejectsMissingEndpoint(t *testing.T) {
	t.Parallel()

	cfg := testClientConfig()
	cfg.Endpoints.ReleaseAuth = ""

	clients, err := NewClientBundle(cfg)
	require.Error(t, err)
	assert.Nil(t, clients)
	assert.ErrorIs(t, err, ErrConfigInvalid)
	assert.ErrorContains(t, err, "endpoints.release_auth")
}

// TestClientBundleDoesNotSerializeRuntimeHandles asserts the structural reason a
// bundle cannot leak: it exposes no exported field, so encoding/json has nothing
// to encode. Marshalling it instead would be vacuous — the result is always
// "{}" no matter what the bundle holds, so the assertion could never fail.
func TestClientBundleDoesNotSerializeRuntimeHandles(t *testing.T) {
	t.Parallel()

	clients, err := NewClientBundle(testClientConfig())
	require.NoError(t, err)

	bundleType := reflect.TypeOf(clients).Elem()
	require.Positive(t, bundleType.NumField(), "bundle unexpectedly has no fields")
	for index := range bundleType.NumField() {
		field := bundleType.Field(index)
		if field.IsExported() {
			t.Fatalf("ClientBundle.%s is exported; a runtime handle would become serializable", field.Name)
		}
	}
}

func TestConfigPasswordDoesNotSerialize(t *testing.T) {
	t.Parallel()

	cfg := testClientConfig()
	cfg.password = "super-secret-password"
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	assert.NotContains(t, string(data), cfg.password)
	assert.NotContains(t, string(data), "super-secret")
}

func TestNewClientBundleWithOptionsUsesOperatorTransport(t *testing.T) {
	t.Parallel()

	general := &recordingHTTPClient{}
	operator := &recordingHTTPClient{}
	clients, err := NewClientBundleWithOptions(testClientConfig(), ClientOptions{
		HTTPClient:         general,
		OperatorHTTPClient: operator,
	})
	require.NoError(t, err)
	assert.Same(t, general, clients.httpClient)
	assert.Same(t, operator, clients.operatorHTTPClient)
}

func testClientConfig() *Config {
	return &Config{
		Endpoints: Endpoints{
			ReleaseOrchestrator: "http://orchestrator.test",
			ReleaseWebhook:      "http://webhook.test",
			ReleaseOperator:     "https://operator.test",
			ReleaseAuth:         "http://auth.test",
			ReleaseNotifier:     "http://notifier.test",
			ReleaseAPI:          "http://api.test",
		},
	}
}

type recordingHTTPClient struct{}

func (*recordingHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: request}, nil
}

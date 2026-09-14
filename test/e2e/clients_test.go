package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
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

// urlRecordingHTTPClient captures the URL of every request so a test can assert
// which declared endpoint a generated client actually addresses.
type urlRecordingHTTPClient struct {
	mu   sync.Mutex
	urls []string
}

func (c *urlRecordingHTTPClient) Do(request *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.urls = append(c.urls, request.URL.String())
	c.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/proto"}},
		Body:       http.NoBody,
		Request:    request,
	}, nil
}

func (c *urlRecordingHTTPClient) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.urls...)
}

// TestOperatorSessionReadUsesTheControlPlaneEndpoint pins the endpoint the
// runner reads operator sessions from. release_operator is the TLS agent gateway
// and answers no plain-HTTP request, so binding the OperatorService client to it
// made every read fail and the control-plane stage report the session as missing
// (real smoke 2026-09-11). The read belongs to release_orchestrator, like every
// other control-plane client in the bundle.
func TestOperatorSessionReadUsesTheControlPlaneEndpoint(t *testing.T) {
	t.Parallel()

	recorder := &urlRecordingHTTPClient{}
	clients, err := NewClientBundleWithOptions(testClientConfig(), ClientOptions{HTTPClient: recorder})
	require.NoError(t, err)

	response, err := clients.Operator().GetActiveOperatorSession(context.Background(),
		connect.NewRequest(&operatorv1.GetActiveOperatorSessionRequest{OperatorId: "operator-1"}))
	require.NoError(t, err)
	require.NotNil(t, response)
	require.NotNil(t, response.Msg)

	requested := recorder.recorded()
	require.Len(t, requested, 1)
	assert.Contains(t, requested[0], "orchestrator.test")
	assert.NotContains(t, requested[0], "operator.test")
}

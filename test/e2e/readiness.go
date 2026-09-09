package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultReadinessTimeout = 5 * time.Second
	maxEnvironmentBodySize  = 16 << 10
)

// ErrEnvironmentUnhealthy marks a service endpoint or environment metadata
// that is not safe for E2E execution.
var ErrEnvironmentUnhealthy = errors.New("environment_unhealthy")

// EnvironmentObservation is the public, non-secret response from /environment.
// The service field is intentionally retained only for diagnostics; readiness
// compares Environment, EnvironmentID, and Production across all endpoints.
type EnvironmentObservation struct {
	Service       string `json:"service,omitempty"`
	Environment   string `json:"environment,omitempty"`
	EnvironmentID string `json:"environment_id,omitempty"`
	Production    bool   `json:"production"`
}

// EndpointReadiness records status codes and public environment metadata for a
// single declared endpoint. It contains no URL, credentials, tokens, or body
// payload and is safe to use as in-memory stage evidence.
type EndpointReadiness struct {
	Name         string                 `json:"name"`
	HealthStatus int                    `json:"health_status"`
	ReadyStatus  int                    `json:"ready_status"`
	Environment  EnvironmentObservation `json:"environment"`
}

// ReadinessResult is the immutable result of a control-plane readiness probe.
type ReadinessResult struct {
	Endpoints []EndpointReadiness `json:"endpoints"`
}

// ReadinessError identifies a safe endpoint/path failure without including the
// endpoint URL or response body.
type ReadinessError struct {
	Endpoint string
	Path     string
	Reason   string
	Cause    error
}

func (e *ReadinessError) Error() string {
	if e == nil {
		return ErrEnvironmentUnhealthy.Error()
	}
	if e.Endpoint == "" {
		return fmt.Sprintf("%s: %s", ErrEnvironmentUnhealthy, e.Reason)
	}
	if e.Path == "" {
		return fmt.Sprintf("%s: %s: %s", ErrEnvironmentUnhealthy, e.Endpoint, e.Reason)
	}
	return fmt.Sprintf("%s: %s %s: %s", ErrEnvironmentUnhealthy, e.Endpoint, e.Path, e.Reason)
}

func (e *ReadinessError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrEnvironmentUnhealthy
	}
	return errors.Join(ErrEnvironmentUnhealthy, e.Cause)
}

// ReadinessChecker runs the six declared service endpoint checks. It is a thin
// reusable wrapper shared by run and cleanup callers.
type ReadinessChecker struct {
	config  *Config
	clients *ClientBundle
	timeout time.Duration
}

// NewReadinessChecker creates a checker. A non-positive timeout uses the
// default per-request timeout.
func NewReadinessChecker(config *Config, clients *ClientBundle, timeout time.Duration) (*ReadinessChecker, error) {
	if config == nil {
		return nil, configInvalid("config", "nil config")
	}
	if err := validateClientEndpoints(config.Endpoints); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = defaultReadinessTimeout
	}
	return &ReadinessChecker{config: config, clients: clients, timeout: timeout}, nil
}

// Check probes /health, /readyz, and /environment on every declared endpoint.
func (r *ReadinessChecker) Check(ctx context.Context) (*ReadinessResult, error) {
	if r == nil || r.config == nil {
		return nil, configInvalid("config", "nil config")
	}
	return CheckReadiness(ctx, r.config, r.clients, r.timeout)
}

// CheckReadiness runs the control-plane probe using the transports from clients.
// A nil bundle uses the default HTTP transport, which is useful for tests and
// for deployments without a separate operator mTLS transport.
func CheckReadiness(ctx context.Context, config *Config, clients *ClientBundle, timeout time.Duration) (*ReadinessResult, error) {
	if config == nil {
		return nil, configInvalid("config", "nil config")
	}
	if err := validateClientEndpoints(config.Endpoints); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = defaultReadinessTimeout
	}

	generalClient := HTTPDoer(http.DefaultClient)
	operatorClient := generalClient
	if clients != nil {
		if clients.httpClient != nil {
			generalClient = clients.httpClient
		}
		if clients.operatorHTTPClient != nil {
			operatorClient = clients.operatorHTTPClient
		}
	}
	return checkEndpoints(ctx, config, generalClient, operatorClient, timeout)
}

// CheckEnvironmentReadiness is a descriptive alias for CheckReadiness.
func CheckEnvironmentReadiness(ctx context.Context, config *Config, clients *ClientBundle, timeout time.Duration) (*ReadinessResult, error) {
	return CheckReadiness(ctx, config, clients, timeout)
}

// HTTPDoer is the minimal transport seam used by readiness checks.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// CheckEndpoints probes all endpoints with one HTTP transport. Operator-aware
// callers should prefer CheckReadiness so the gateway can use a dedicated
// HTTPS/mTLS transport.
func CheckEndpoints(ctx context.Context, endpoints Endpoints, httpClient HTTPDoer, expectedEnvironment, expectedEnvironmentID string, timeout time.Duration) (*ReadinessResult, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if timeout <= 0 {
		timeout = defaultReadinessTimeout
	}
	config := &Config{
		Environment:   expectedEnvironment,
		EnvironmentID: expectedEnvironmentID,
		Endpoints:     endpoints,
	}
	if err := validateClientEndpoints(endpoints); err != nil {
		return nil, err
	}
	return checkEndpoints(ctx, config, httpClient, httpClient, timeout)
}

func checkEndpoints(ctx context.Context, config *Config, generalClient, operatorClient HTTPDoer, timeout time.Duration) (*ReadinessResult, error) {
	specs := []endpointSpec{
		{name: "release_orchestrator", url: config.Endpoints.ReleaseOrchestrator, client: generalClient},
		{name: "release_webhook", url: config.Endpoints.ReleaseWebhook, client: generalClient},
		{name: "release_operator", url: config.Endpoints.ReleaseOperator, client: operatorClient},
		{name: "release_auth", url: config.Endpoints.ReleaseAuth, client: generalClient},
		{name: "release_notifier", url: config.Endpoints.ReleaseNotifier, client: generalClient},
		{name: "release_api", url: config.Endpoints.ReleaseAPI, client: generalClient},
	}

	result := &ReadinessResult{Endpoints: make([]EndpointReadiness, len(specs))}
	errs := make([]error, len(specs))
	var wg sync.WaitGroup
	wg.Add(len(specs))
	for index := range specs {
		index := index
		go func() {
			defer wg.Done()
			result.Endpoints[index], errs[index] = checkEndpoint(ctx, specs[index], config.Environment, config.EnvironmentID, timeout)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

type endpointSpec struct {
	name   string
	url    string
	client HTTPDoer
}

func checkEndpoint(ctx context.Context, spec endpointSpec, expectedEnvironment, expectedEnvironmentID string, timeout time.Duration) (EndpointReadiness, error) {
	if spec.client == nil {
		spec.client = http.DefaultClient
	}
	healthStatus, err := getStatus(ctx, spec.client, spec.name, spec.url, "/health", timeout)
	if err != nil {
		return EndpointReadiness{}, err
	}
	readyStatus, err := getStatus(ctx, spec.client, spec.name, spec.url, "/readyz", timeout)
	if err != nil {
		return EndpointReadiness{}, err
	}
	environment, err := getEnvironment(ctx, spec.client, spec.name, spec.url, timeout)
	if err != nil {
		return EndpointReadiness{}, err
	}
	if environment.Environment != expectedEnvironment {
		return EndpointReadiness{}, readinessFailure(spec.name, "/environment", "environment mismatch", nil)
	}
	if environment.EnvironmentID != expectedEnvironmentID {
		return EndpointReadiness{}, readinessFailure(spec.name, "/environment", "environment_id mismatch", nil)
	}
	if environment.Production {
		return EndpointReadiness{}, readinessFailure(spec.name, "/environment", "production environment is not allowed", nil)
	}
	if environment.Service == "" {
		return EndpointReadiness{}, readinessFailure(spec.name, "/environment", "missing service", nil)
	}
	return EndpointReadiness{
		Name:         spec.name,
		HealthStatus: healthStatus,
		ReadyStatus:  readyStatus,
		Environment:  environment,
	}, nil
}

func getStatus(ctx context.Context, client HTTPDoer, endpoint, baseURL, path string, timeout time.Duration) (int, error) {
	response, err := doGET(ctx, client, endpoint, baseURL, path, timeout)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return response.StatusCode, readinessFailure(endpoint, path, fmt.Sprintf("unexpected status %d", response.StatusCode), nil)
	}
	return response.StatusCode, nil
}

func getEnvironment(ctx context.Context, client HTTPDoer, endpoint, baseURL string, timeout time.Duration) (EnvironmentObservation, error) {
	response, err := doGET(ctx, client, endpoint, baseURL, "/environment", timeout)
	if err != nil {
		return EnvironmentObservation{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return EnvironmentObservation{}, readinessFailure(endpoint, "/environment", fmt.Sprintf("unexpected status %d", response.StatusCode), nil)
	}
	var observation EnvironmentObservation
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxEnvironmentBodySize))
	if err := decoder.Decode(&observation); err != nil {
		return EnvironmentObservation{}, readinessFailure(endpoint, "/environment", "invalid response", err)
	}
	return observation, nil
}

func doGET(ctx context.Context, client HTTPDoer, endpoint, baseURL, path string, timeout time.Duration) (*http.Response, error) {
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, strings.TrimRight(baseURL, "/")+path, http.NoBody)
	if err != nil {
		return nil, readinessFailure(endpoint, path, "invalid endpoint", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, readinessFailure(endpoint, path, "request failed", err)
	}
	if response == nil {
		return nil, readinessFailure(endpoint, path, "empty response", nil)
	}
	return response, nil
}

func readinessFailure(endpoint, path, reason string, cause error) error {
	return &ReadinessError{Endpoint: endpoint, Path: path, Reason: reason, Cause: cause}
}

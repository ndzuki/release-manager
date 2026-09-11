package livewire

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"k8s.io/apimachinery/pkg/util/wait"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	e2e "github.com/ndzuki/release-manager/test/e2e"
)

// operatorSessionOnline is the session status that means the operator gateway
// has re-established its control stream. It mirrors the store's SessionOnline
// value; the E2E harness deliberately imports no internal package, so it
// observes only the public API contract and restates the wire value here.
const operatorSessionOnline = "online"

// ControlPlaneWaiter observes control-plane recovery after a restart: every
// declared service endpoint answers its health probe, and the operator gateway
// reports an online session again.
type ControlPlaneWaiter struct {
	cfg        *e2e.Config
	clients    *e2e.ClientBundle
	session    *e2e.RunnerSession
	httpClient *http.Client
	poll       time.Duration
}

// NewControlPlaneWaiter builds the recovery barrier over the declared service
// endpoints and the operator service client.
func NewControlPlaneWaiter(cfg *e2e.Config, clients *e2e.ClientBundle, session *e2e.RunnerSession) (*ControlPlaneWaiter, error) {
	if cfg == nil || clients == nil || session == nil {
		return nil, errors.New("livewire: control-plane waiter requires a config, clients, and a session")
	}
	return &ControlPlaneWaiter{
		cfg:        cfg,
		clients:    clients,
		session:    session,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		poll:       defaultPollInterval,
	}, nil
}

// WithPollInterval overrides the barrier poll cadence.
func (w *ControlPlaneWaiter) WithPollInterval(interval time.Duration) *ControlPlaneWaiter {
	if w == nil {
		return nil
	}
	if interval > 0 {
		w.poll = interval
	}
	return w
}

// WithHTTPClient injects the transport used for health probes.
func (w *ControlPlaneWaiter) WithHTTPClient(client *http.Client) *ControlPlaneWaiter {
	if w == nil {
		return nil
	}
	if client != nil {
		w.httpClient = client
	}
	return w
}

// AwaitServices implements stages.ControlPlaneWaiter. Every declared endpoint
// must answer its health probe; an endpoint that never does keeps the barrier
// closed instead of reporting a recovery the run cannot see.
func (w *ControlPlaneWaiter) AwaitServices(ctx context.Context) error {
	if w == nil || w.cfg == nil {
		return errors.New("livewire: control-plane waiter is unavailable")
	}
	probes, err := w.healthProbes()
	if err != nil {
		return err
	}
	for _, probe := range probes {
		probe := probe
		pollErr := wait.PollUntilContextCancel(ctx, w.poll, true, func(condCtx context.Context) (bool, error) {
			return w.healthProbeSucceeds(condCtx, probe)
		})
		if pollErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("livewire: control-plane endpoint %s never became ready: %w", probe.service, pollErr)
		}
	}
	return nil
}

// transientSessionReadError reports whether a session-read failure is one the
// restart barrier is expected to wait through. A session that is not registered
// yet and a gateway that is still coming up both clear on their own; a rejected
// request or a denied caller does not. Swallowing the latter hid exactly that
// (real smoke 2026-09-11: a blank operator id polled invalid_argument every
// 250ms until the 600s stage timeout, and the stage reported only a timeout).
//
// CodeUnknown covers a transport failure that never reached the server and
// produced no Connect status, which is transient by nature.
func transientSessionReadError(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeUnknown, connect.CodeNotFound, connect.CodeUnavailable,
		connect.CodeDeadlineExceeded, connect.CodeCanceled:
		return true
	default:
		return false
	}
}

// AwaitOperatorSession implements stages.ControlPlaneWaiter. It waits for the
// operator gateway to report an online session, which is the observable proof
// that the operator's control stream reconnected after the restart.
func (w *ControlPlaneWaiter) AwaitOperatorSession(ctx context.Context) error {
	if w == nil || w.clients == nil || w.session == nil {
		return errors.New("livewire: control-plane waiter is unavailable")
	}
	operatorID, ok := w.cfg.Seed.OperatorID()
	if !ok {
		// The API selects a session by operator id and rejects a blank one, so a
		// missing id is a configuration fault, not a state to wait through.
		return errors.New("livewire: control-plane waiter requires a seeded operator id")
	}
	if err := w.session.EnsureLogin(ctx); err != nil {
		return err
	}
	pollErr := wait.PollUntilContextCancel(ctx, w.poll, true, func(condCtx context.Context) (bool, error) {
		response, err := w.clients.Operator().GetActiveOperatorSession(condCtx,
			authorizedRequest(w.session.Token(), &operatorv1.GetActiveOperatorSessionRequest{
				OperatorId: operatorID,
			}))
		if err != nil {
			// A gateway that has not yet re-established its control stream has
			// no active session and answers with a transient error. That is the
			// expected state this barrier waits through, and the caller's context
			// deadline bounds it. A permanent error is a different thing: no
			// amount of waiting fixes a rejected request, so it fails the bar
			// instead of burning the stage timeout.
			if transientSessionReadError(err) {
				return false, nil //nolint:nilerr // transient not-yet-online session; bounded by the context deadline
			}
			return false, err
		}
		session := response.Msg.GetSession()
		return session != nil && strings.EqualFold(session.GetStatus(), operatorSessionOnline), nil
	})
	if pollErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("livewire: operator session never returned online: %w", pollErr)
	}
	return nil
}

// healthProbe is one declared service and its health URL.
type healthProbe struct {
	service string
	url     string
	// tcpAddress is set for a TLS-only listener; exactly one of url and
	// tcpAddress is populated.
	tcpAddress string
}

// healthProbes resolves the health URL for every declared endpoint. Endpoints
// are separate services, so each must be individually healthy.
func (w *ControlPlaneWaiter) healthProbes() ([]healthProbe, error) {
	declared := []struct {
		service  string
		endpoint string
		tcpOnly  bool
	}{
		{service: "release_orchestrator", endpoint: w.cfg.Endpoints.ReleaseOrchestrator},
		{service: "release_webhook", endpoint: w.cfg.Endpoints.ReleaseWebhook},
		// The operator endpoint is the orchestrator's mTLS agent gateway: it
		// serves TLS-only Connect handlers with no /health route, so recovery
		// is asserted by reachability (see stages.TransportTCP).
		{service: "release_operator", endpoint: w.cfg.Endpoints.ReleaseOperator, tcpOnly: true},
		{service: "release_auth", endpoint: w.cfg.Endpoints.ReleaseAuth},
		{service: "release_notifier", endpoint: w.cfg.Endpoints.ReleaseNotifier},
		{service: "release_api", endpoint: w.cfg.Endpoints.ReleaseAPI},
	}
	probes := make([]healthProbe, 0, len(declared))
	for _, item := range declared {
		endpoint := strings.TrimSpace(item.endpoint)
		if endpoint == "" {
			continue
		}
		if item.tcpOnly {
			address, err := probeAddress(endpoint)
			if err != nil {
				return nil, fmt.Errorf("livewire: %s: %w", item.service, err)
			}
			probes = append(probes, healthProbe{service: item.service, tcpAddress: address})
			continue
		}
		healthURL, err := healthURLFor(endpoint)
		if err != nil {
			return nil, fmt.Errorf("livewire: %s: %w", item.service, err)
		}
		probes = append(probes, healthProbe{service: item.service, url: healthURL})
	}
	if len(probes) == 0 {
		return nil, errors.New("livewire: no control-plane endpoints are declared")
	}
	return probes, nil
}

// healthURLFor resolves the health probe for one service endpoint. It shares
// probeURL with the control-plane observer so both seams validate an endpoint
// identically.
func healthURLFor(endpoint string) (string, error) {
	return probeURL(endpoint, "health")
}

// healthProbeSucceeds performs one health probe and drains the body so the
// connection can be reused. A probe with a TCP address asserts reachability
// instead, which is the only recovery signal a TLS-only listener offers.
func (w *ControlPlaneWaiter) healthProbeSucceeds(ctx context.Context, probe healthProbe) (bool, error) {
	if probe.tcpAddress != "" {
		dialer := &net.Dialer{Timeout: probeDialTimeout}
		conn, err := dialer.DialContext(ctx, "tcp", probe.tcpAddress)
		if err != nil {
			return false, nil
		}
		// The probe only needs the connection; close errors are not actionable.
		_ = conn.Close()
		return true, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, probe.url, http.NoBody)
	if err != nil {
		return false, err
	}
	response, err := w.httpClient.Do(request)
	if err != nil {
		return false, nil
	}
	defer func() {
		_ = response.Body.Close()
	}()
	return response.StatusCode == http.StatusOK, nil
}

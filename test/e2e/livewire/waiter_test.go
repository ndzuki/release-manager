package livewire

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
)

func TestHealthURLForBuildsProbeURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		endpoint string
		want     string
		wantErr  bool
	}{
		{name: "http", endpoint: "http://localhost:8083", want: "http://localhost:8083/health"},
		{name: "trailing slash", endpoint: "http://localhost:8083/", want: "http://localhost:8083/health"},
		{name: "https", endpoint: "https://orchestrator.internal", want: "https://orchestrator.internal/health"},
		{name: "no scheme", endpoint: "localhost:8083", wantErr: true},
		{name: "empty host", endpoint: "http://", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := healthURLFor(test.endpoint)
			if test.wantErr {
				if err == nil {
					t.Fatalf("healthURLFor(%q) = %q, want an error", test.endpoint, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("healthURLFor(%q) error = %v", test.endpoint, err)
			}
			if got != test.want {
				t.Fatalf("healthURLFor(%q) = %q, want %q", test.endpoint, got, test.want)
			}
		})
	}
}

func TestAwaitServicesSucceedsWhenEveryEndpointIsHealthy(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := h.newWaiter(t).AwaitServices(ctx); err != nil {
		t.Fatalf("AwaitServices() error = %v", err)
	}
}

func TestAwaitServicesStaysClosedOnUnhealthyEndpoint(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	// A non-200 health probe must keep the barrier closed: reporting recovery
	// the run cannot observe is exactly the vacuous pass this stage prevents.
	h.setHealth(http.StatusServiceUnavailable)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	if err := h.newWaiter(t).AwaitServices(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AwaitServices() error = %v, want the context deadline", err)
	}
}

func TestAwaitServicesRecoversWhenEndpointBecomesHealthy(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.setHealth(http.StatusServiceUnavailable)
	waiter := h.newWaiter(t)

	go func() {
		time.Sleep(20 * time.Millisecond)
		h.setHealth(http.StatusOK)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := waiter.AwaitServices(ctx); err != nil {
		t.Fatalf("AwaitServices() error = %v, want recovery once the endpoint is healthy", err)
	}
}

func TestAwaitOperatorSessionWaitsForOnline(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	// The gateway reports an intermediate status first, so the barrier must
	// actually wait rather than accept the first response.
	h.operator.setSessions(
		&operatorv1.OperatorSession{SessionId: "s-1", Status: "suspect"},
		&operatorv1.OperatorSession{SessionId: "s-2", Status: operatorSessionOnline},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := h.newWaiter(t).AwaitOperatorSession(ctx); err != nil {
		t.Fatalf("AwaitOperatorSession() error = %v", err)
	}
	if got := h.operator.callCount(); got < 2 {
		t.Fatalf("operator calls = %d, want at least 2 (it must wait for online)", got)
	}
}

func TestAwaitOperatorSessionStaysClosedWithoutOnlineSession(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.operator.setSessions(&operatorv1.OperatorSession{SessionId: "s-1", Status: "suspect"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	if err := h.newWaiter(t).AwaitOperatorSession(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AwaitOperatorSession() error = %v, want the context deadline", err)
	}
}

func TestAwaitOperatorSessionToleratesTransientRPCFailure(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	// Before the operator reconnects the route legitimately has no session and
	// returns an error; that transient state must not fail the barrier.
	h.operator.mu.Lock()
	h.operator.err = errors.New("no active session")
	h.operator.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	// It must time out (still closed) rather than return an immediate error:
	// an immediate error would make the barrier's failure mode indistinguishable
	// from a misconfigured environment.
	err := h.newWaiter(t).AwaitOperatorSession(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AwaitOperatorSession() error = %v, want the context deadline", err)
	}
}

func TestNewControlPlaneWaiterValidatesInputs(t *testing.T) {
	t.Parallel()

	if _, err := NewControlPlaneWaiter(nil, nil, nil); err == nil {
		t.Fatal("NewControlPlaneWaiter(nil, nil, nil) error = nil, want an error")
	}
}

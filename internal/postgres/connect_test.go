package postgres

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/ndzuki/release-manager/internal/config"
	"github.com/stretchr/testify/require"
)

// testPolicy is a compressed version of defaultConnectPolicy: same shape, small
// delays, so the retry contract is observable without sleeping for seconds.
func testPolicy(attempts int) connectPolicy {
	return connectPolicy{
		Attempts:       attempts,
		BaseDelay:      time.Second,
		MaxDelay:       8 * time.Second,
		Budget:         20 * time.Second,
		AttemptTimeout: 5 * time.Second,
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// identityJitter keeps the assertions on the backoff schedule deterministic.
func identityJitter(d time.Duration) time.Duration { return d }

// refused is the error shape kube-router produces when the postgres
// NetworkPolicy has not yet admitted a new pod's address: the dial is rejected
// with icmp-port-unreachable, which surfaces as ECONNREFUSED.
func refused() error {
	return &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: os.NewSyscallError("connect", syscall.ECONNREFUSED),
	}
}

// TestWaitForServerSucceedsAfterTransientFailures pins the TASK-351 fix: the
// first pings fail with connection refused and the wait must not give up; the
// fourth attempt succeeds.
func TestWaitForServerSucceedsAfterTransientFailures(t *testing.T) {
	attempts := 0
	ping := func(context.Context) error {
		attempts++
		if attempts < 4 {
			return refused()
		}
		return nil
	}
	var slept []time.Duration
	sleep := func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}

	err := waitForServer(context.Background(), ping, testPolicy(5), discardLogger(), sleep, identityJitter)

	require.NoError(t, err)
	require.Equal(t, 4, attempts)
	// Exponential backoff: 1s, then 2s, then 4s.
	require.Equal(t, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}, slept)
}

// TestWaitForServerExhaustsAttempts pins the other half: the wait is bounded and
// returns an error instead of looping forever, and the error still carries the
// transport cause so callers can errors.Is it.
func TestWaitForServerExhaustsAttempts(t *testing.T) {
	attempts := 0
	ping := func(context.Context) error {
		attempts++
		return refused()
	}
	var slept []time.Duration
	sleep := func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}

	err := waitForServer(context.Background(), ping, testPolicy(5), discardLogger(), sleep, identityJitter)

	require.Error(t, err)
	require.Equal(t, 5, attempts)
	// Delays are capped at MaxDelay (8s): 1, 2, 4, 8.
	require.Equal(t, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}, slept)
	require.ErrorContains(t, err, "after 5 attempts")
	require.ErrorIs(t, err, syscall.ECONNREFUSED)
}

// TestWaitForServerDoesNotRetryServerSideErrors pins fail-fast: a server that
// answers with a credential error is not a transient network problem, and
// retrying would only delay the real diagnosis.
func TestWaitForServerDoesNotRetryServerSideErrors(t *testing.T) {
	authErr := fmt.Errorf("failed to connect: %w", &pgconn.PgError{
		Severity: "FATAL",
		Code:     "28P01",
		Message:  `password authentication failed for user "release_manager"`,
	})
	attempts := 0
	ping := func(context.Context) error {
		attempts++
		return authErr
	}
	slept := 0
	sleep := func(context.Context, time.Duration) error {
		slept++
		return nil
	}

	err := waitForServer(context.Background(), ping, testPolicy(5), discardLogger(), sleep, identityJitter)

	require.ErrorIs(t, err, authErr)
	require.Equal(t, 1, attempts)
	require.Zero(t, slept)
}

// TestWaitForServerRetriesPerAttemptTimeout covers an unreachable host (packets
// dropped rather than rejected): the per-attempt deadline is transient, unlike
// a caller cancellation.
func TestWaitForServerRetriesPerAttemptTimeout(t *testing.T) {
	attempts := 0
	ping := func(context.Context) error {
		attempts++
		if attempts == 1 {
			return context.DeadlineExceeded
		}
		return nil
	}
	sleep := func(context.Context, time.Duration) error { return nil }

	err := waitForServer(context.Background(), ping, testPolicy(5), discardLogger(), sleep, identityJitter)

	require.NoError(t, err)
	require.Equal(t, 2, attempts)
}

// TestWaitForServerStopsOnContextCancel proves the wait is interruptible: a
// cancelled caller neither sleeps again nor reports success.
func TestWaitForServerStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ping := func(context.Context) error { return refused() }
	sleep := func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}

	err := waitForServer(ctx, ping, testPolicy(5), discardLogger(), sleep, identityJitter)

	require.ErrorIs(t, err, context.Canceled)
}

// TestWaitForServerBudgetBoundsTheWholeWait proves the budget (not the attempt
// count) ends a wait whose attempts are slow, and that this is surfaced.
func TestWaitForServerBudgetBoundsTheWholeWait(t *testing.T) {
	policy := connectPolicy{
		Attempts:       50,
		BaseDelay:      10 * time.Millisecond,
		MaxDelay:       20 * time.Millisecond,
		Budget:         50 * time.Millisecond,
		AttemptTimeout: 10 * time.Millisecond,
	}
	ping := func(context.Context) error { return refused() }

	err := waitForServer(context.Background(), ping, policy, discardLogger(), sleepCtx, identityJitter)

	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestDefaultConnectPolicyCoversTheStartupWindow guards the constants: the
// observed kube-router admission window is sub-second, so the first backoff has
// to cover it, and the sum of the backoffs has to fit inside the budget.
func TestDefaultConnectPolicyCoversTheStartupWindow(t *testing.T) {
	require.Equal(t, 5, defaultConnectPolicy.Attempts)
	require.Equal(t, time.Second, defaultConnectPolicy.BaseDelay)
	require.Equal(t, 8*time.Second, defaultConnectPolicy.MaxDelay)
	require.Equal(t, 20*time.Second, defaultConnectPolicy.Budget)
	require.Equal(t, 5*time.Second, defaultConnectPolicy.AttemptTimeout)

	total := time.Duration(0)
	delay := defaultConnectPolicy.BaseDelay
	for i := 1; i < defaultConnectPolicy.Attempts; i++ {
		total += delay
		delay *= 2
		if delay > defaultConnectPolicy.MaxDelay {
			delay = defaultConnectPolicy.MaxDelay
		}
	}
	require.Equal(t, 15*time.Second, total)
	require.Less(t, total, defaultConnectPolicy.Budget)
}

func TestJitterDelayStaysWithinTwentyPercent(t *testing.T) {
	for range 200 {
		got := jitterDelay(time.Second)
		require.GreaterOrEqual(t, got, 800*time.Millisecond)
		require.LessOrEqual(t, got, 1200*time.Millisecond)
	}
	require.Zero(t, jitterDelay(0))
}

func TestSleepCtxReturnsImmediatelyForZeroDelay(t *testing.T) {
	require.NoError(t, sleepCtx(context.Background(), 0))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, sleepCtx(ctx, time.Hour), context.Canceled)
}

// TestOpenRejectsNonPostgresDriver pins that the startup wait never hides a
// configuration error: driver validation happens before any dial or retry.
func TestOpenRejectsNonPostgresDriver(t *testing.T) {
	_, err := open(context.Background(), config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    "data/api.db",
	}, defaultConnectPolicy, discardLogger(), sleepCtx, identityJitter)
	require.ErrorContains(t, err, "requires database.driver=postgres")
}

// TestOpenSurfacesConnectionRefusedWithPrefix keeps the operator-facing error
// shape stable: the retry must not swallow the "connection_unavailable: ping
// PostgreSQL" prefix that ops runbooks grep for.
func TestOpenSurfacesConnectionRefusedWithPrefix(t *testing.T) {
	// Reserve a port, then release it so the dial is refused immediately.
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	policy := testPolicy(2)
	policy.BaseDelay = time.Millisecond
	policy.Budget = 200 * time.Millisecond

	_, err = open(context.Background(), config.DatabaseConfig{
		Driver: "postgres",
		DSN:    "postgres://user:secret@" + addr + "/release?sslmode=disable",
	}, policy, discardLogger(), sleepCtx, identityJitter)

	require.Error(t, err)
	require.ErrorContains(t, err, "connection_unavailable: ping PostgreSQL")
	require.ErrorContains(t, err, "after 2 attempts")
}

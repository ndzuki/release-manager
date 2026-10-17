package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Startup connection wait (TASK-351).
//
// A pod that boots into a namespace with NetworkPolicies cannot reach
// PostgreSQL for the first few hundred milliseconds, and the failure looks like
// a server outage: the dial returns ECONNREFUSED. The cause is in the policy
// data plane, not in PostgreSQL. kube-router admits traffic to the postgres pod
// only when the source address matches the policy's source ipset, and it can
// only populate that set once the apiserver pod object carries status.podIP.
// The kubelet reports that status asynchronously, after the sandbox already
// exists; the container starts in between, so its first dial is rejected by the
// postgres pod's policy chain (REJECT --reject-with icmp-port-unreachable ->
// "connect: connection refused"). By the time the container is restarted, the
// ipset has caught up and the same ping succeeds, which is why the symptom
// looks like a transient database outage that heals itself.
//
// A single ping turns that sub-second window into exit 1 plus a
// CrashLoopBackOff restart (measured: 5.7 s RTO with one restart, 26.2 s with
// two). The bounded wait below removes the restart without weakening the
// fail-fast contract: the server must answer within connectBudget, only
// transport-level failures are retried, and a server that answers with an
// authentication or configuration error is reported immediately.
const (
	// connectAttempts is the total number of pings, not the number of retries.
	connectAttempts = 5
	// connectBaseDelay is the first backoff; it doubles up to connectMaxDelay.
	connectBaseDelay = time.Second
	connectMaxDelay  = 8 * time.Second
	// connectBudget bounds the whole wait (5 attempts, 1+2+4+8 s of backoff).
	connectBudget = 20 * time.Second
	// connectAttemptTimeout bounds a single ping. An unreachable host (dropped
	// packets, no RST) would otherwise hang until the caller's context expires,
	// which is context.Background() for every service that opens the store.
	connectAttemptTimeout = 5 * time.Second
)

// connectPolicy is the retry budget for reaching the server at startup. It is
// a struct rather than constants so tests can shrink the delays.
type connectPolicy struct {
	Attempts       int
	BaseDelay      time.Duration
	MaxDelay       time.Duration
	Budget         time.Duration
	AttemptTimeout time.Duration
}

// defaultConnectPolicy retries five times over a 20 s budget. The window being
// papered over is sub-second, so the first backoff alone covers it; the rest of
// the budget only matters when the database really is still starting.
var defaultConnectPolicy = connectPolicy{
	Attempts:       connectAttempts,
	BaseDelay:      connectBaseDelay,
	MaxDelay:       connectMaxDelay,
	Budget:         connectBudget,
	AttemptTimeout: connectAttemptTimeout,
}

type (
	pingFunc   func(ctx context.Context) error
	sleepFunc  func(ctx context.Context, d time.Duration) error
	jitterFunc func(d time.Duration) time.Duration
)

// waitForServer pings until the server answers, the attempt budget is spent, or
// the budget context ends. The returned error always wraps the last ping error,
// so callers keep the "connection_unavailable: ping PostgreSQL: ..." shape and
// can still errors.Is() the underlying cause. Non-transient errors (the server
// answered) are returned without retrying.
func waitForServer(
	ctx context.Context,
	ping pingFunc,
	policy connectPolicy,
	logger *slog.Logger,
	sleep sleepFunc,
	jitter jitterFunc,
) error {
	if policy.Attempts < 1 {
		policy.Attempts = 1
	}
	budgetCtx, cancel := context.WithTimeout(ctx, policy.Budget)
	defer cancel()
	if logger == nil {
		logger = slog.Default()
	}

	var lastErr error
	delay := policy.BaseDelay
	for attempt := 1; ; attempt++ {
		attemptCtx := budgetCtx
		attemptCancel := context.CancelFunc(func() {})
		if policy.AttemptTimeout > 0 {
			attemptCtx, attemptCancel = context.WithTimeout(budgetCtx, policy.AttemptTimeout)
		}
		err := ping(attemptCtx)
		attemptCancel()
		if err == nil {
			if attempt > 1 {
				logger.Info("PostgreSQL answered after startup retry", "attempt", attempt)
			}
			return nil
		}
		lastErr = err

		// A spent budget (or a cancelled caller) ends the wait here: no sleep,
		// no further attempts.
		if budgetErr := budgetCtx.Err(); budgetErr != nil {
			logger.Error("PostgreSQL still unreachable after startup retries",
				"attempts", attempt, "error", err)
			return fmt.Errorf("after %d attempts: %w", attempt, errors.Join(err, budgetErr))
		}
		if !isTransientConnectError(err) {
			// The server answered (bad credentials, unknown database, ...);
			// retrying cannot change that answer.
			return err
		}
		if attempt >= policy.Attempts {
			logger.Error("PostgreSQL still unreachable after startup retries",
				"attempts", attempt, "error", err)
			return fmt.Errorf("after %d attempts: %w", attempt, lastErr)
		}

		wait := jitter(delay)
		logger.Warn("PostgreSQL not reachable yet, retrying",
			"attempt", attempt, "of", policy.Attempts, "retry_in", wait.String(), "error", err)
		if err := sleep(budgetCtx, wait); err != nil {
			logger.Error("PostgreSQL still unreachable after startup retries",
				"attempts", attempt, "error", lastErr)
			return fmt.Errorf("after %d attempts: %w", attempt, errors.Join(lastErr, err))
		}
		delay *= 2
		if delay > policy.MaxDelay {
			delay = policy.MaxDelay
		}
	}
}

// isTransientConnectError reports whether retrying the ping could succeed. It
// retries transport-level failures (refused, reset, unreachable, DNS, per-attempt
// timeout) and refuses to retry anything the server itself answered with.
func isTransientConnectError(err error) bool {
	if err == nil {
		return false
	}
	// pgx wraps a server rejection in *pgconn.ConnectError; the PgError below it
	// is the authoritative "the server answered" signal (28P01 bad password,
	// 3D000 unknown database, 53300 too many connections, ...). Check it first:
	// also a dial failure can be wrapped, but never around a PgError.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	// A per-attempt timeout means the server did not answer in time; the budget
	// context is checked separately before this runs.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH)
}

// sleepCtx waits for d unless ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// jitterDelay spreads the backoff by +/-20% so that a rollout restarting several
// services at once does not make them reconnect in lockstep.
func jitterDelay(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	// G404: jitter only needs to be uncorrelated, not unpredictable; a
	// crypto/rand source would add an error path for no security benefit.
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64())) //nolint:gosec // see above
}

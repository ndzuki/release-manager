// Package operation implements the core Operation state machine (REQ-023).
package operation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// RecoverOptions controls recovery behavior.
type RecoverOptions struct {
	// DeadlineGracePeriod is added to the operation deadline before declaring timeout.
	// Accounts for clock skew and transient delays (default 30s).
	DeadlineGracePeriod time.Duration

	// CancellingTimeout converts stale cancelling operations to failed.
	// Avoids permanent cancelling state when handler never ACKs (AC-023-04).
	CancellingTimeout time.Duration
}

// DefaultRecoverOptions returns conservative recovery defaults.
func DefaultRecoverOptions() RecoverOptions {
	return RecoverOptions{
		DeadlineGracePeriod: 30 * time.Second,
		CancellingTimeout:   5 * time.Minute,
	}
}

// RecoverNonTerminal queries all non-terminal operations and resolves stale states.
// Called on service restart (REQ-023 AC-023-05).
// Returns the count of operations transitioned.
func RecoverNonTerminal(ctx context.Context, st store.Store, logger *slog.Logger, opts RecoverOptions) int {
	ops, err := st.Operations().ListNonTerminal(ctx)
	if err != nil {
		logger.Error("recovery: failed to list non-terminal operations", "err", err)
		return 0
	}

	if len(ops) == 0 {
		logger.Info("recovery: no non-terminal operations found")
		return 0
	}

	logger.Info("recovery: found non-terminal operations", "count", len(ops))
	now := time.Now().UTC()
	recovered := 0

	for _, op := range ops {
		recovered += recoverOne(ctx, st, logger, op, opts, now)
	}

	return recovered
}

func recoverOne(ctx context.Context, st store.Store, logger *slog.Logger, op *store.Operation, opts RecoverOptions, now time.Time) int {
	// AC-023-04: Stale cancelling → failed (handler never ACKed).
	if op.Status == store.StatusCancelling {
		if opts.CancellingTimeout > 0 && now.After(op.UpdatedAt.Add(opts.CancellingTimeout)) {
			logger.Warn("recovery: stale cancelling operation, transitioning to failed",
				"op_id", op.ID, "updated_at", op.UpdatedAt,
			)
			return transitionTo(ctx, st, logger, op, store.StatusFailed, "recovery: cancelling handler did not acknowledge")
		}
		// Still within grace period — leave as is.
		return 0
	}

	// Deadline exceeded → timeout.
	if op.Deadline != nil {
		deadline := op.Deadline.Add(opts.DeadlineGracePeriod)
		if now.After(deadline) {
			logger.Warn("recovery: deadline exceeded, transitioning to timeout",
				"op_id", op.ID, "deadline", op.Deadline, "now", now,
			)
			return transitionTo(ctx, st, logger, op, store.StatusTimeout, fmt.Sprintf("deadline exceeded: %s", op.Deadline.Format(time.RFC3339)))
		}
	}

	// Non-terminal, not stale — leave for operator to reconnect.
	logger.Info("recovery: non-terminal operation left running for operator reconnect",
		"op_id", op.ID, "status", op.Status, "state_version", op.StateVersion,
	)
	return 0
}

func transitionTo(ctx context.Context, st store.Store, logger *slog.Logger, op *store.Operation, target store.OperationStatus, reason string) int {
	_, err := st.Operations().Transition(ctx, op.ID, target, op.StateVersion, reason)
	if err != nil {
		logger.Error("recovery: transition failed",
			"op_id", op.ID, "from", op.Status, "to", target, "err", err,
		)
		return 0
	}

	logger.Info("recovery: operation transitioned",
		"op_id", op.ID, "from", op.Status, "to", target,
	)
	return 1
}

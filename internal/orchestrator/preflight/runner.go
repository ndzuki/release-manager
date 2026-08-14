package preflight

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ndzuki/release-manager/internal/store"
)

// Runner owns the lifecycle of preflight coordinator goroutines per operation.
// It provides operation-scoped cancellation (AC-019-03), idempotent start,
// and a bounded graceful drain before the store closes. A completed Run always
// unregisters itself; the operation CAS in the store remains the authoritative
// terminal state (ADR-009) — in-memory state never decides business outcomes.
type Runner struct {
	mu      sync.Mutex
	closed  bool
	entries map[string]context.CancelFunc
	run     func(ctx context.Context, op *store.Operation)
	logger  *slog.Logger
	wg      sync.WaitGroup
}

// NewRunner creates a runner executing run for each started operation.
func NewRunner(run func(ctx context.Context, op *store.Operation), logger *slog.Logger) *Runner {
	return &Runner{
		entries: make(map[string]context.CancelFunc),
		run:     run,
		logger:  logger,
	}
}

// Start begins (or resumes) the preflight pipeline for an operation with a
// context detached from any HTTP request, so request completion cannot cancel
// it. Duplicate starts for the same operation are no-ops; starts after
// Shutdown are rejected. Returns whether the run was started.
func (r *Runner) Start(op *store.Operation) bool {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		r.logger.Warn("preflight runner rejected during shutdown", "op_id", op.ID)
		return false
	}
	if _, exists := r.entries[op.ID]; exists {
		r.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(context.Background()))
	r.entries[op.ID] = cancel
	r.wg.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.wg.Done()
		defer cancel()
		defer r.unregister(op.ID)
		r.run(ctx, op)
	}()
	return true
}
// Resume restarts preflight coordination for every operation still in the
// preflight state after a service restart (ADR-009 recovery). The caller
// supplies the candidate operations; recovery policy (which states resume)
// lives in the Runner, not the service. Idempotent: operations already
// running are no-ops. Returns the number of operations resumed.
func (r *Runner) Resume(ops []*store.Operation) int {
	started := 0
	for _, op := range ops {
		if op.Status == store.StatusPreflight && r.Start(op) {
			started++
		}
	}
	return started
}

// Cancel propagates cancellation to a running preflight. Idempotent: cancelling
// an operation that already finished or was never started is a no-op.
func (r *Runner) Cancel(opID string) {
	r.mu.Lock()
	cancel, ok := r.entries[opID]
	r.mu.Unlock()
	if ok {
		cancel()
	}
}

func (r *Runner) unregister(opID string) {
	r.mu.Lock()
	delete(r.entries, opID)
	r.mu.Unlock()
}

// Shutdown cancels all runs and waits for them to drain within ctx, so no
// coordinator touches the store after it closes.
func (r *Runner) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	cancels := make([]context.CancelFunc, 0, len(r.entries))
	for _, cancel := range r.entries {
		cancels = append(cancels, cancel)
	}
	r.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for preflight coordinators: %w", ctx.Err())
	}
}

package preflight

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRun blocks until ctx is cancelled, then records the operation it ran.
// Channel-driven so tests never sleep on real timeouts (Step 5 prototype gate:
// cancel/complete/shutdown races are exercised deterministically under -race).
func fakeRun(blocked, done chan<- string) func(context.Context, *store.Operation) {
	return func(ctx context.Context, op *store.Operation) {
		blocked <- op.ID
		<-ctx.Done()
		done <- op.ID
	}
}

func testOperation(id string) *store.Operation {
	return &store.Operation{ID: id, OperationType: store.OperationInstall, Status: store.StatusPreflight}
}

func newTestRunner(t *testing.T, run func(context.Context, *store.Operation)) *Runner {
	t.Helper()
	return NewRunner(run, slog.New(slog.DiscardHandler))
}

// AC-019-03: Cancel terminates a running preflight and the run unregisters.
func TestRunnerCancelUnregisters(t *testing.T) {
	blocked := make(chan string, 1)
	done := make(chan string, 1)
	r := newTestRunner(t, fakeRun(blocked, done))

	op := testOperation("op-cancel")
	r.Start(op)
	require.Equal(t, "op-cancel", <-blocked, "run must start")

	r.Cancel(op.ID)
	require.Equal(t, "op-cancel", <-done, "run must exit after cancel")

	// Cancel is idempotent after completion.
	r.Cancel(op.ID)
	// Unregistration is visible: a fresh Start runs again. The fresh start can
	// only succeed once the previous entry is unregistered, so a hang here
	// means the completion path leaked the registration — fail instead of
	// blocking forever.
	r.Start(op)
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("restart after completion must run: previous entry not unregistered")
	}
	r.Cancel(op.ID)
	<-done
}

// ADR-009 recovery: Resume restarts only operations still in preflight and is
// idempotent across repeated calls (running entries are no-ops).
func TestRunnerResumeStartsPreflightOps(t *testing.T) {
	blocked := make(chan string, 2)
	done := make(chan string, 2)
	r := newTestRunner(t, fakeRun(blocked, done))

	ops := []*store.Operation{
		testOperation("op-resume-1"),
		testOperation("op-resume-2"),
		{ID: "op-queued", OperationType: store.OperationInstall, Status: store.StatusQueued},
		{ID: "op-terminal", OperationType: store.OperationInstall, Status: store.StatusFailed},
	}
	assert.Equal(t, 2, r.Resume(ops), "only preflight operations resume")
	started := map[string]bool{<-blocked: true, <-blocked: true}
	assert.True(t, started["op-resume-1"] && started["op-resume-2"], "both preflight operations must start")
	// Repeated Resume must not start duplicates.
	assert.Equal(t, 0, r.Resume(ops), "already-running operations are no-ops")
	r.Cancel("op-resume-1")
	r.Cancel("op-resume-2")
	exited := map[string]bool{<-done: true, <-done: true}
	assert.True(t, exited["op-resume-1"] && exited["op-resume-2"], "both cancelled runs must exit")
}

// Duplicate starts for the same operation are no-ops.
func TestRunnerStartIdempotent(t *testing.T) {
	blocked := make(chan string, 2)
	done := make(chan string, 2)
	r := newTestRunner(t, fakeRun(blocked, done))

	op := testOperation("op-dup")
	r.Start(op)
	r.Start(op)
	require.Equal(t, "op-dup", <-blocked, "exactly one run starts")
	select {
	case extra := <-blocked:
		t.Fatalf("unexpected second run for %s", extra)
	case <-time.After(100 * time.Millisecond):
	}
	r.Cancel(op.ID)
	<-done
}

// Shutdown cancels every run and drains before returning; starts afterwards are
// rejected (no DB access after store close).
func TestRunnerShutdownDrainsAndRejects(t *testing.T) {
	blocked := make(chan string, 4)
	done := make(chan string, 4)
	r := newTestRunner(t, fakeRun(blocked, done))

	for _, id := range []string{"op-1", "op-2", "op-3"} {
		op := testOperation(id)
		r.Start(op)
	}
	for range 3 {
		<-blocked
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, r.Shutdown(shutdownCtx), "shutdown must drain all runs")

	// Start after shutdown is rejected: no goroutine is spawned.
	op := testOperation("op-late")
	r.Start(op)
	select {
	case id := <-blocked:
		t.Fatalf("start after shutdown must be rejected, ran %s", id)
	case <-time.After(100 * time.Millisecond):
	}
}

// Concurrent cancel/complete/shutdown must not race or double-unregister.
func TestRunnerConcurrentCancelAndShutdown(t *testing.T) {
	blocked := make(chan string, 8)
	done := make(chan string, 8)
	r := newTestRunner(t, fakeRun(blocked, done))

	for _, id := range []string{"op-a", "op-b", "op-c", "op-d"} {
		r.Start(testOperation(id))
	}
	for range 4 {
		<-blocked
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, r.Shutdown(shutdownCtx))
	}()
	for _, id := range []string{"op-a", "op-b", "op-c", "op-d"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			r.Cancel(id)
		}(id)
	}
	wg.Wait()
	for range 4 {
		<-done
	}
}

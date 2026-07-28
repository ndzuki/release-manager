package preflight

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerStartRejectsDuplicateAndUnregisters(t *testing.T) {
	runner := NewRunner()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})

	require.True(t, runner.Start(t.Context(), "operation-1", func(context.Context) {
		close(started)
		<-release
		close(done)
	}))
	<-started
	assert.True(t, runner.Active("operation-1"))
	assert.False(t, runner.Start(t.Context(), "operation-1", func(context.Context) {}))

	close(release)
	<-done
	require.Eventually(t, func() bool { return !runner.Active("operation-1") }, time.Second, time.Millisecond)
}

func TestRunnerCancelPropagatesAndToleratesCompletedRuns(t *testing.T) {
	runner := NewRunner()
	cancelled := make(chan struct{})
	require.True(t, runner.Start(t.Context(), "operation-1", func(ctx context.Context) {
		<-ctx.Done()
		close(cancelled)
	}))

	assert.True(t, runner.Cancel("operation-1"))
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("runner cancellation was not propagated")
	}
	require.Eventually(t, func() bool { return !runner.Active("operation-1") }, time.Second, time.Millisecond)
	assert.False(t, runner.Cancel("operation-1"))
}

func TestRunnerWaitObservesCompletionAndTimeout(t *testing.T) {
	runner := NewRunner()
	release := make(chan struct{})
	require.True(t, runner.Start(t.Context(), "operation-1", func(context.Context) {
		<-release
	}))

	timeoutCtx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	assert.False(t, runner.Wait(timeoutCtx, "operation-1"))
	close(release)
	assert.True(t, runner.Wait(t.Context(), "operation-1"))
	assert.True(t, runner.Wait(t.Context(), "missing-operation"))
}

func TestRunnerStopAllCancelsActiveRuns(t *testing.T) {
	runner := NewRunner()
	cancelled := make(chan string, 2)
	for _, operationID := range []string{"operation-1", "operation-2"} {
		operationID := operationID
		require.True(t, runner.Start(t.Context(), operationID, func(ctx context.Context) {
			<-ctx.Done()
			cancelled <- operationID
		}))
	}

	runner.StopAll()
	seen := map[string]bool{}
	for range 2 {
		select {
		case operationID := <-cancelled:
			seen[operationID] = true
		case <-time.After(time.Second):
			t.Fatal("runner StopAll did not cancel every run")
		}
	}
	assert.Equal(t, map[string]bool{"operation-1": true, "operation-2": true}, seen)
}

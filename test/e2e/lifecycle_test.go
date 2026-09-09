package e2e_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ndzuki/release-manager/test/e2e"
)

func TestLifecycleCloseRejectsAdmissionAndDrains(t *testing.T) {
	t.Parallel()

	lifecycle := e2e.NewLifecycle(time.Second)
	op, err := lifecycle.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	closed := make(chan error, 1)
	go func() { closed <- lifecycle.Close(context.Background()) }()
	deadline := time.After(time.Second)
	for lifecycle.State() != e2e.LifecycleClosing {
		select {
		case <-deadline:
			t.Fatal("lifecycle did not enter closing")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if _, err := lifecycle.Begin(context.Background()); !errors.Is(err, e2e.ErrLifecycleClosing) {
		t.Fatalf("begin during close = %v, want ErrLifecycleClosing", err)
	}
	op.Done()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if got := lifecycle.State(); got != e2e.LifecycleClosed {
		t.Fatalf("state = %s, want closed", got)
	}
}

func TestLifecycleReentrantCloseAndCleanupContext(t *testing.T) {
	t.Parallel()

	lifecycle := e2e.NewLifecycle(time.Second)
	var reentrant error
	if err := lifecycle.Execute(context.Background(), func(ctx context.Context) error {
		reentrant = lifecycle.Close(ctx)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(reentrant, e2e.ErrReentrantClose) {
		t.Fatalf("reentrant close = %v, want ErrReentrantClose", reentrant)
	}

	caller, cancel := context.WithCancel(context.Background())
	cancel()
	cleanupCalled := make(chan struct{})
	if err := lifecycle.Close(caller, func(ctx context.Context) error {
		if ctx.Err() != nil {
			t.Errorf("cleanup context already canceled: %v", ctx.Err())
		}
		close(cleanupCalled)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cleanupCalled:
	case <-time.After(time.Second):
		t.Fatal("cleanup was not called")
	}
}

func TestLifecycleConcurrentCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	lifecycle := e2e.NewLifecycle(time.Second)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- lifecycle.Close(context.Background())
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
}

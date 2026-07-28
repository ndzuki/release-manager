package preflight

import (
	"context"
	"sync"
)

// Runner owns operation-scoped preflight contexts independently of HTTP requests.
type Runner struct {
	mu     sync.Mutex
	cancel map[string]context.CancelFunc
	done   map[string]chan struct{}
}

func NewRunner() *Runner {
	return &Runner{
		cancel: make(map[string]context.CancelFunc),
		done:   make(map[string]chan struct{}),
	}
}

func (r *Runner) Start(ctx context.Context, operationID string, run func(context.Context)) bool {
	r.mu.Lock()
	if _, exists := r.cancel[operationID]; exists {
		r.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.done[operationID] = done
	r.cancel[operationID] = cancel
	r.mu.Unlock()

	go func() {
		defer close(done)
		defer cancel()
		defer r.unregister(operationID)
		run(ctx)
	}()
	return true
}

func (r *Runner) Cancel(operationID string) bool {
	r.mu.Lock()
	cancel, exists := r.cancel[operationID]
	r.mu.Unlock()
	if !exists {
		return false
	}
	cancel()
	return true
}

// Wait blocks until the operation run exits or ctx is cancelled.
func (r *Runner) Wait(ctx context.Context, operationID string) bool {
	r.mu.Lock()
	done, exists := r.done[operationID]
	r.mu.Unlock()
	if !exists {
		return true
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (r *Runner) Active(operationID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, exists := r.cancel[operationID]
	return exists
}

func (r *Runner) StopAll() {
	r.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(r.cancel))
	done := make([]<-chan struct{}, 0, len(r.done))
	for operationID, cancel := range r.cancel {
		cancels = append(cancels, cancel)
		done = append(done, r.done[operationID])
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	for _, completed := range done {
		<-completed
	}
}

func (r *Runner) unregister(operationID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cancel, operationID)
	delete(r.done, operationID)
}

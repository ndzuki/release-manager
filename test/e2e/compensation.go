package e2e

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	// ErrCompensationClosed reports registration after cleanup has started.
	ErrCompensationClosed = errors.New("e2e: compensation registry closed")
	// ErrCompensationDuplicate reports a duplicate compensation identity.
	ErrCompensationDuplicate = errors.New("e2e: duplicate compensation")
	// ErrCompensationDirty reports cleanup that did not fully succeed.
	ErrCompensationDirty = errors.New("e2e: compensation dirty")
)

// CompensationFunc performs one idempotent cleanup action.
type CompensationFunc func(context.Context) error

type compensationEntry struct {
	id string
	fn CompensationFunc
}

// CompensationRegistry records cleanup actions and executes them exactly once
// in strict last-in-first-out order.
type CompensationRegistry struct {
	mu      sync.Mutex
	entries []compensationEntry
	state   compensationState
	done    chan struct{}
	err     error
}

type compensationState uint8

const (
	compensationOpen compensationState = iota
	compensationRunning
	compensationFinished
)

// NewCompensationRegistry creates an empty compensation registry.
func NewCompensationRegistry() *CompensationRegistry {
	return &CompensationRegistry{state: compensationOpen}
}

// Register adds one uniquely identified compensation action.
func (r *CompensationRegistry) Register(id string, fn CompensationFunc) error {
	if r == nil {
		return ErrCompensationClosed
	}
	if id == "" || fn == nil {
		return fmt.Errorf("register compensation: %w", ErrInvalidLedgerEntry)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != compensationOpen {
		return ErrCompensationClosed
	}
	for _, entry := range r.entries {
		if entry.id == id {
			return fmt.Errorf("register compensation %q: %w", id, ErrCompensationDuplicate)
		}
	}
	r.entries = append(r.entries, compensationEntry{id: id, fn: fn})
	return nil
}

// Add is an alias for Register.
func (r *CompensationRegistry) Add(id string, fn CompensationFunc) error {
	return r.Register(id, fn)
}

// Len reports the number of registered actions, including actions already run.
func (r *CompensationRegistry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// Run executes all actions once in LIFO order. Every action is attempted even
// when another action fails; concurrent callers share the first result.
func (r *CompensationRegistry) Run(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	switch r.state {
	case compensationFinished:
		err := r.err
		r.mu.Unlock()
		return err
	case compensationRunning:
		done := r.done
		r.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
		r.mu.Lock()
		err := r.err
		r.mu.Unlock()
		return err
	default:
		r.state = compensationRunning
		r.done = make(chan struct{})
		entries := append([]compensationEntry(nil), r.entries...)
		done := r.done
		r.mu.Unlock()

		var runErr error
		for i := len(entries) - 1; i >= 0; i-- {
			if err := entries[i].fn(ctx); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("compensate %s: %w", entries[i].id, err))
			}
		}
		if runErr != nil {
			runErr = errors.Join(ErrCompensationDirty, runErr)
		}

		r.mu.Lock()
		r.err = runErr
		r.state = compensationFinished
		close(done)
		r.mu.Unlock()
		return runErr
	}
}

// Compensate is an alias for Run.
func (r *CompensationRegistry) Compensate(ctx context.Context) error {
	return r.Run(ctx)
}

// Error returns the result of the completed run, or nil before it runs.
func (r *CompensationRegistry) Error() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

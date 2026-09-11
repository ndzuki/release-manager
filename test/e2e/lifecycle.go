package e2e

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// LifecycleState is the admission state of a lifecycle coordinator.
type LifecycleState string

const (
	LifecycleOpen    LifecycleState = "open"
	LifecycleClosing LifecycleState = "closing"
	LifecycleClosed  LifecycleState = "closed"
)

var (
	// ErrLifecycleClosing reports that new work is refused during teardown.
	ErrLifecycleClosing = errors.New("e2e: lifecycle is closing")
	// ErrLifecycleClosed reports that the lifecycle has already terminated.
	ErrLifecycleClosed = errors.New("e2e: lifecycle is closed")
	// ErrReentrantClose reports a close attempted by an admitted operation.
	ErrReentrantClose = errors.New("e2e: reentrant close")
	// ErrCleanupTimeout reports that in-flight work did not drain in time.
	ErrCleanupTimeout = errors.New("e2e: cleanup timeout")
)

const defaultCleanupTimeout = 30 * time.Second

type lifecycleContextKey struct{}

// Operation is an admitted operation. Call Done exactly once when work ends.
type Operation struct {
	lifecycle *Lifecycle
	ctx       context.Context
	once      sync.Once
}

// Context returns the operation context carrying its private admission token.
func (o *Operation) Context() context.Context {
	if o == nil || o.ctx == nil {
		return context.Background()
	}
	return o.ctx
}

// Done releases this operation's admission slot. It is idempotent.
func (o *Operation) Done() {
	if o == nil || o.lifecycle == nil {
		return
	}
	o.once.Do(func() {
		o.lifecycle.mu.Lock()
		if o.lifecycle.active > 0 {
			o.lifecycle.active--
			if o.lifecycle.active == 0 {
				close(o.lifecycle.drained)
			}
		}
		o.lifecycle.mu.Unlock()
		o.lifecycle.inFlight.Done()
	})
}

// Lifecycle is a mutex-owned Open-to-Closed admission gate.
type Lifecycle struct {
	mu             sync.Mutex
	state          LifecycleState
	inFlight       sync.WaitGroup
	active         int
	drained        chan struct{}
	cleanupTimeout time.Duration
	closeDone      chan struct{}
	closeErr       error
}

// NewLifecycle creates an open lifecycle with a bounded cleanup timeout.
func NewLifecycle(timeout ...time.Duration) *Lifecycle {
	d := defaultCleanupTimeout
	if len(timeout) > 0 && timeout[0] > 0 {
		d = timeout[0]
	}
	drained := make(chan struct{})
	close(drained)
	return &Lifecycle{
		state:          LifecycleOpen,
		drained:        drained,
		cleanupTimeout: d,
		closeDone:      make(chan struct{}),
	}
}

// State returns the current lifecycle state.
func (l *Lifecycle) State() LifecycleState {
	if l == nil {
		return LifecycleClosed
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state == "" {
		return LifecycleOpen
	}
	return l.state
}

// Begin admits one operation and returns its private operation context.
func (l *Lifecycle) Begin(ctx context.Context) (*Operation, error) {
	if l == nil {
		return nil, ErrLifecycleClosed
	}
	ctx = contextOrBackground(ctx)

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state == LifecycleClosing {
		return nil, ErrLifecycleClosing
	}
	if l.state == LifecycleClosed {
		return nil, ErrLifecycleClosed
	}
	if l.active == 0 {
		l.drained = make(chan struct{})
	}

	op := &Operation{lifecycle: l}
	op.ctx = context.WithValue(ctx, lifecycleContextKey{}, op)
	l.inFlight.Add(1)
	l.active++
	return op, nil
}

// Execute admits an operation, invokes fn, and releases it on return.
func (l *Lifecycle) Execute(ctx context.Context, fn func(context.Context) error) error {
	if fn == nil {
		return fmt.Errorf("execute lifecycle operation: nil function")
	}
	op, err := l.Begin(ctx)
	if err != nil {
		return err
	}
	defer op.Done()
	// op.Context() is derived from the caller's ctx in Begin (context.WithValue
	// on the incoming ctx) and stored on the operation; contextcheck cannot
	// follow it through the struct field.
	return fn(op.Context()) //nolint:contextcheck // derived from the caller's context in Begin
}

// Close seals admission, drains in-flight work, and optionally runs cleanup.
// Cleanup receives a context derived with context.WithoutCancel and bounded by
// the lifecycle timeout. Repeated Close calls return the first result.
func (l *Lifecycle) Close(ctx context.Context, cleanup ...func(context.Context) error) error {
	if l == nil {
		return nil
	}
	if len(cleanup) > 1 {
		return fmt.Errorf("close lifecycle: too many cleanup functions")
	}
	if op, ok := operationFromContext(ctx); ok && op.lifecycle == l {
		return ErrReentrantClose
	}

	l.mu.Lock()
	if l.state == LifecycleClosed {
		err := l.closeErr
		l.mu.Unlock()
		return err
	}
	if l.state == LifecycleClosing {
		done := l.closeDone
		timeout := l.cleanupTimeout
		l.mu.Unlock()
		waitCtx, cancel := CleanupContext(ctx, timeout)
		defer cancel()
		select {
		case <-done:
			l.mu.Lock()
			err := l.closeErr
			l.mu.Unlock()
			return err
		case <-waitCtx.Done():
			return fmt.Errorf("wait for lifecycle close: %w", ErrCleanupTimeout)
		}
	}
	l.state = LifecycleClosing
	done := l.closeDone
	drained := l.drained
	timeout := l.cleanupTimeout
	l.mu.Unlock()

	cleanupCtx, cancel := CleanupContext(ctx, timeout)
	defer cancel()

	var closeErr error
	select {
	case <-drained:
		l.inFlight.Wait()
	case <-cleanupCtx.Done():
		closeErr = errors.Join(closeErr, fmt.Errorf("drain lifecycle operations: %w", ErrCleanupTimeout))
	}

	if closeErr == nil && len(cleanup) == 1 {
		cleanupDone := make(chan error, 1)
		go func() { cleanupDone <- cleanup[0](cleanupCtx) }()
		select {
		case err := <-cleanupDone:
			if err != nil {
				closeErr = errors.Join(closeErr, fmt.Errorf("cleanup lifecycle: %w", err))
			}
		case <-cleanupCtx.Done():
			closeErr = errors.Join(closeErr, fmt.Errorf("cleanup lifecycle: %w", ErrCleanupTimeout))
		}
	}

	l.mu.Lock()
	l.closeErr = closeErr
	l.state = LifecycleClosed
	close(done)
	l.mu.Unlock()
	return closeErr
}

// CleanupContext derives a bounded context that ignores parent cancellation.
func CleanupContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	parent = contextOrBackground(parent)
	if timeout <= 0 {
		timeout = defaultCleanupTimeout
	}
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}

// contextOrBackground returns ctx unchanged, or a background context when the
// caller passed none. The fallback keeps these entry points' zero value usable:
// context.WithValue panics on a nil parent, so without it a defensive default
// would become a crash. contextcheck reads the fallback as a new context that
// ignores the caller, which is precisely what it is — there is no caller context
// to inherit from.
func contextOrBackground(ctx context.Context) context.Context { //nolint:contextcheck // no parent context exists to inherit from
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func operationFromContext(ctx context.Context) (*Operation, bool) {
	if ctx == nil {
		return nil, false
	}
	op, ok := ctx.Value(lifecycleContextKey{}).(*Operation)
	return op, ok && op != nil
}

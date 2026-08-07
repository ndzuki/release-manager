package authorization

import (
	"context"
	"sync"
)

type fenceKey struct{}

type fenceState struct {
	mu            sync.RWMutex
	sourceVersion uint64
	set           bool
}

// WithFenceCapture adds a request-scoped holder for the exact source version
// used by an authorization decision. The holder is intentionally internal to
// the request context so concurrent requests cannot overwrite each other.
func WithFenceCapture(ctx context.Context) context.Context {
	return context.WithValue(ctx, fenceKey{}, &fenceState{})
}

// SourceVersionFromContext returns the exact authorization source version used
// by the successful decision on this request.
func SourceVersionFromContext(ctx context.Context) (uint64, bool) {
	state, ok := ctx.Value(fenceKey{}).(*fenceState)
	if !ok || state == nil {
		return 0, false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.sourceVersion, state.set
}

func recordFence(ctx context.Context, sourceVersion uint64) {
	state, ok := ctx.Value(fenceKey{}).(*fenceState)
	if !ok || state == nil {
		return
	}
	state.mu.Lock()
	state.sourceVersion = sourceVersion
	state.set = true
	state.mu.Unlock()
}

package operator

import (
	"context"
	"sync"
)

// StreamRegistry tracks active Operator command streams so revocation can stop them immediately.
type StreamRegistry struct {
	mu      sync.Mutex
	streams map[string]activeStream
}

type activeStream struct {
	sessionID string
	cancel    context.CancelFunc
}

// NewStreamRegistry creates an empty active stream registry.
func NewStreamRegistry() *StreamRegistry {
	return &StreamRegistry{streams: make(map[string]activeStream)}
}

var processStreamRegistry = NewStreamRegistry()

// ProcessStreamRegistry returns the process-wide registry shared by the
// Operator handler and the Orchestrator management handler in combined deployments.
func ProcessStreamRegistry() *StreamRegistry { return processStreamRegistry }

// Register replaces any stale stream for the Operator and returns an unregister callback.
func (r *StreamRegistry) Register(operatorID, sessionID string, cancel context.CancelFunc) func() {
	if r == nil || operatorID == "" || cancel == nil {
		return func() {}
	}
	r.mu.Lock()
	previous, exists := r.streams[operatorID]
	r.streams[operatorID] = activeStream{sessionID: sessionID, cancel: cancel}
	r.mu.Unlock()
	if exists {
		previous.cancel()
	}
	return func() {
		r.mu.Lock()
		current, ok := r.streams[operatorID]
		if ok && current.sessionID == sessionID {
			delete(r.streams, operatorID)
		}
		r.mu.Unlock()
	}
}

// Revoke cancels the active stream, if present.
func (r *StreamRegistry) Revoke(operatorID, _ string) bool {
	if r == nil || operatorID == "" {
		return false
	}
	r.mu.Lock()
	stream, ok := r.streams[operatorID]
	if ok {
		delete(r.streams, operatorID)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	stream.cancel()
	return true
}

// StreamRevoker closes active Operator command streams after a committed revocation.
type StreamRevoker interface {
	Revoke(operatorID, reason string) bool
}

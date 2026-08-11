package operator

import (
	"context"
	"crypto/tls"
)

// tlsStateContextKey carries the *tls.ConnectionState of the gateway listener
// request into Connect handlers (streaming handlers derive their context from
// the HTTP request context).
type tlsStateContextKey struct{}

// WithTLSState returns a context carrying the TLS connection state of the
// incoming gateway request. The orchestrator gateway middleware injects it so
// CommandStream can enforce the mTLS identity path (TASK-075).
func WithTLSState(ctx context.Context, state *tls.ConnectionState) context.Context {
	return context.WithValue(ctx, tlsStateContextKey{}, state)
}

// TLSStateFromContext returns the TLS connection state injected by the gateway
// middleware, or nil on the plain management-plane path.
func TLSStateFromContext(ctx context.Context) *tls.ConnectionState {
	if state, ok := ctx.Value(tlsStateContextKey{}).(*tls.ConnectionState); ok {
		return state
	}
	return nil
}

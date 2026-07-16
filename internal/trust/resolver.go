package trust

import (
	"context"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// RootResolver provides access to live trust roots for verification.
type RootResolver interface {
	// ResolveActive returns roots in active or grace state that accept
	// signatures at the given time.
	ResolveActive(ctx context.Context, env string, at time.Time) ([]*store.TrustRoot, error)

	// GetPolicyMeta returns the current policy version and revocation epoch.
	GetPolicyMeta(ctx context.Context, env string) (*store.TrustPolicyMeta, error)
}

// storeResolver implements RootResolver backed by store.TrustRootStore.
type storeResolver struct {
	st store.TrustRootStore
}

// NewStoreResolver creates a RootResolver backed by a TrustRootStore.
func NewStoreResolver(st store.TrustRootStore) RootResolver {
	return &storeResolver{st: st}
}

func (r *storeResolver) ResolveActive(ctx context.Context, env string, at time.Time) ([]*store.TrustRoot, error) {
	return r.st.GetActiveByEnvironment(ctx, env, at)
}

func (r *storeResolver) GetPolicyMeta(ctx context.Context, env string) (*store.TrustPolicyMeta, error) {
	return r.st.GetPolicy(ctx, env)
}

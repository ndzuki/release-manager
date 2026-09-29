package auth

import (
	"context"

	"github.com/ndzuki/release-manager/internal/store"
)

// CustomerResolver loads customer lifecycle state from the tenancy service.
type CustomerResolver interface {
	Resolve(ctx context.Context, customerID string) (*store.Customer, error)
}

// StoreCustomerResolver resolves customer lifecycle state from this process's store.
//
// This is the resolver the binding service runs with. Three facts decide it:
//   - release-auth and release-orchestrator MUST open the same database (the invariant
//     recorded in configs/auth.dev.yaml, TASK-104), so the orchestrator's customer writes
//     are visible to this read;
//   - this process already reads the customers table directly
//     (internal/auth/authorization_snapshot.go, internal/authorization/store_authorizer.go),
//     and validateWritableTarget already reads organizations through this same store, so
//     the coupling exists and this adds none;
//   - a remote resolver needs a service principal holding `release read` in the
//     customer's organization domain, and a background resolution has no principal. The
//     Connect implementation that tried was never wired, so the process shipped with
//     StubResolver and every binding write answered NOT_FOUND customer_not_found
//     (TASK-191).
type StoreCustomerResolver struct {
	store store.Store
}

// NewStoreCustomerResolver creates a resolver backed by the process's own store.
func NewStoreCustomerResolver(st store.Store) *StoreCustomerResolver {
	return &StoreCustomerResolver{store: st}
}

// Resolve returns the customer, preserving store.ErrNotFound for an unknown id.
func (r *StoreCustomerResolver) Resolve(ctx context.Context, customerID string) (*store.Customer, error) {
	return r.store.Customers().Get(ctx, customerID)
}

var _ CustomerResolver = (*StoreCustomerResolver)(nil)

// StubResolver is a no-op CustomerResolver that returns ErrNotFound.
//
// It is NOT a production default any more: wiring it made every binding write fail with
// `NOT_FOUND customer_not_found` (TASK-191). It survives for tests that need a resolver
// which refuses everything.
type StubResolver struct{}

// Resolve always returns ErrNotFound.
func (StubResolver) Resolve(_ context.Context, _ string) (*store.Customer, error) {
	return nil, store.ErrNotFound
}

var _ CustomerResolver = StubResolver{}

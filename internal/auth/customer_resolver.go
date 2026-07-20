package auth

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/store"
)

// CustomerResolver loads customer lifecycle state from the tenancy service.
type CustomerResolver interface {
	Resolve(ctx context.Context, customerID string) (*store.Customer, error)
}

// ConnectCustomerResolver resolves customers through release-orchestrator.
type ConnectCustomerResolver struct {
	client orchestratorv1connect.OrchestratorServiceClient
}

// NewConnectCustomerResolver creates a customer resolver backed by a Connect client.
func NewConnectCustomerResolver(client orchestratorv1connect.OrchestratorServiceClient) *ConnectCustomerResolver {
	return &ConnectCustomerResolver{client: client}
}

// Resolve returns the customer, including disabled lifecycle state.
func (r *ConnectCustomerResolver) Resolve(ctx context.Context, customerID string) (*store.Customer, error) {
	resp, err := r.client.GetCustomer(ctx, connect.NewRequest(&orchestratorv1.GetCustomerRequest{
		CustomerId: customerID,
	}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("get customer: %w", err)
	}

	customer := resp.Msg.GetCustomer()
	if customer == nil {
		return nil, fmt.Errorf("get customer: empty response")
	}
	return &store.Customer{
		ID:     customer.GetId(),
		Name:   customer.GetName(),
		Slug:   customer.GetSlug(),
		Status: store.CustomerStatus(customer.GetStatus()),
	}, nil
}

// StubCustomerResolver always returns ErrNotFound for use when no orchestrator is available.
type StubCustomerResolver struct{}

// Resolve returns ErrNotFound for every lookup.
func (StubCustomerResolver) Resolve(_ context.Context, _ string) (*store.Customer, error) {
	return nil, store.ErrNotFound
}

var _ CustomerResolver = StubCustomerResolver{}
var _ CustomerResolver = (*ConnectCustomerResolver)(nil)

//nolint:dupl // Customer and Cluster handlers share the same CRUD pattern
package orchestrator

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// CreateCustomer creates a new tenant customer.
func (s *Service) CreateCustomer(
	ctx context.Context,
	req *connect.Request[orchestratorv1.CreateCustomerRequest],
) (*connect.Response[orchestratorv1.CreateCustomerResponse], error) {
	msg := req.Msg

	id := msg.GetId()
	if id == "" {
		id = uuid.New().String()
	}

	c := &store.Customer{
		ID:     id,
		Name:   msg.GetName(),
		Slug:   msg.GetSlug(),
		Status: store.CustomerActive,
	}
	if c.Slug == "" {
		c.Slug = id
	}

	if err := s.store.Customers().Create(ctx, c); err != nil {
		s.logger.Error("create customer failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create customer: %w", err))
	}

	s.logger.Info("customer created", "id", c.ID, "name", c.Name)
	return connect.NewResponse(&orchestratorv1.CreateCustomerResponse{
		Customer: toProtoCustomer(c),
	}), nil
}

// GetCustomer retrieves a customer by ID.
func (s *Service) GetCustomer(
	ctx context.Context,
	req *connect.Request[orchestratorv1.GetCustomerRequest],
) (*connect.Response[orchestratorv1.GetCustomerResponse], error) {
	c, err := s.store.Customers().Get(ctx, req.Msg.GetCustomerId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("customer %q not found", req.Msg.GetCustomerId()))
		}
		s.logger.Error("get customer failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&orchestratorv1.GetCustomerResponse{
		Customer: toProtoCustomer(c),
	}), nil
}

// ListCustomers returns all customers.
func (s *Service) ListCustomers(
	ctx context.Context,
	_ *connect.Request[orchestratorv1.ListCustomersRequest],
) (*connect.Response[orchestratorv1.ListCustomersResponse], error) {
	customers, err := s.store.Customers().List(ctx)
	if err != nil {
		s.logger.Error("list customers failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	protoCustomers := make([]*commonv1.Customer, 0, len(customers))
	for _, c := range customers {
		protoCustomers = append(protoCustomers, toProtoCustomer(c))
	}

	return connect.NewResponse(&orchestratorv1.ListCustomersResponse{
		Customers: protoCustomers,
	}), nil
}

// UpdateCustomer updates a customer's name or slug.
func (s *Service) UpdateCustomer(
	ctx context.Context,
	req *connect.Request[orchestratorv1.UpdateCustomerRequest],
) (*connect.Response[orchestratorv1.UpdateCustomerResponse], error) {
	msg := req.Msg
	c, err := s.store.Customers().Get(ctx, msg.GetCustomerId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("customer %q not found", msg.GetCustomerId()))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if name := msg.GetName(); name != "" {
		c.Name = name
	}
	if slug := msg.GetSlug(); slug != "" {
		c.Slug = slug
	}

	if err := s.store.Customers().Update(ctx, c); err != nil {
		s.logger.Error("update customer failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update customer: %w", err))
	}

	s.logger.Info("customer updated", "id", c.ID)
	return connect.NewResponse(&orchestratorv1.UpdateCustomerResponse{
		Customer: toProtoCustomer(c),
	}), nil
}

// DisableCustomer disables a customer by ID. Disabled customers reject write operations.
func (s *Service) DisableCustomer(
	ctx context.Context,
	req *connect.Request[orchestratorv1.DisableCustomerRequest],
) (*connect.Response[orchestratorv1.DisableCustomerResponse], error) {
	c, err := s.store.Customers().Get(ctx, req.Msg.GetCustomerId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("customer %q not found", req.Msg.GetCustomerId()))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	c.Status = store.CustomerDisabled
	if err := s.store.Customers().Update(ctx, c); err != nil {
		s.logger.Error("disable customer failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("disable customer: %w", err))
	}

	s.logger.Warn("customer disabled", "id", c.ID, "name", c.Name)
	return connect.NewResponse(&orchestratorv1.DisableCustomerResponse{}), nil
}

// toProtoCustomer converts a store.Customer to a commonv1.Customer proto message.
func toProtoCustomer(c *store.Customer) *commonv1.Customer {
	return &commonv1.Customer{
		Id:   c.ID,
		Name: c.Name,
		Slug: c.Slug,
	}
}

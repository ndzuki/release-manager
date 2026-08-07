//nolint:dupl // Customer and Cluster handlers share the same CRUD pattern
package orchestrator

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
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
	req *connect.Request[orchestratorv1.ListCustomersRequest],
) (*connect.Response[orchestratorv1.ListCustomersResponse], error) {
	customers, err := s.store.Customers().List(ctx, req.Msg.GetIncludeDisabled())
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
//
//nolint:gocyclo // idempotent disable cascades across customer, tokens, operators, sessions, and events.
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

	// AC-013-04: Idempotent — already disabled is a no-op (still emit event
	// for the first disable only).
	if c.Status == store.CustomerDisabled {
		s.logger.Info("customer already disabled", "id", c.ID)
		return connect.NewResponse(&orchestratorv1.DisableCustomerResponse{}), nil
	}

	wasActive := c.Status != store.CustomerDisabled
	c.Status = store.CustomerDisabled
	if err := s.store.Customers().Update(ctx, c); err != nil {
		s.logger.Error("disable customer failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("disable customer: %w", err))
	}

	// Emit CustomerDisabled event for downstream cascading (AC-013-04).
	if wasActive {
		ev := &store.CustomerEvent{
			ID:         uuid.New().String(),
			CustomerID: c.ID,
			EventType:  "customer_disabled",
		}
		if err := s.store.CustomerEvents().Create(ctx, ev); err != nil {
			s.logger.Error("failed to persist CustomerDisabled event", "customer_id", c.ID, "error", err)
			// Non-fatal: the disable succeeded; event persistence is best-effort.
		}
	}

	// Cascade: revoke all enrollment tokens for this customer (AC-015-04).
	tokens, err := s.store.EnrollmentTokens().ListByCustomer(ctx, c.ID)
	if err != nil {
		s.logger.Warn("listing tokens for cascade revoke", "error", err)
	}
	for _, t := range tokens {
		if !t.Used {
			if err := s.store.EnrollmentTokens().Revoke(ctx, t.ID); err != nil {
				s.logger.Warn("cascade revoke token", "token_id", t.ID, "error", err)
			}
		}
	}

	// Cascade: revoke all active operators for this customer.
	operators, err := s.store.Operators().ListByCustomer(ctx, c.ID)
	if err != nil {
		s.logger.Warn("listing operators for cascade revoke", "error", err)
	}
	for _, op := range operators {
		if op.Status == store.OperatorActive {
			if err := s.store.Operators().Revoke(ctx, op.ID); err != nil {
				s.logger.Warn("cascade revoke operator", "operator_id", op.ID, "error", err)
			}
			// Close active sessions.
			if sess, err := s.store.Sessions().GetActiveByOperator(ctx, op.ID); err == nil {
				if err := s.store.Sessions().UpdateStatus(ctx, sess.ID, store.SessionOffline); err != nil {
					s.logger.Warn("cascade close session", "session_id", sess.ID, "error", err)
				}
			}
		}
	}
	s.logger.Warn("customer disabled", "id", c.ID, "name", c.Name)
	return connect.NewResponse(&orchestratorv1.DisableCustomerResponse{}), nil
}

// toProtoCustomer converts a store.Customer to a commonv1.Customer proto message.
func toProtoCustomer(c *store.Customer) *commonv1.Customer {
	return &commonv1.Customer{
		Id:        c.ID,
		Name:      c.Name,
		Slug:      c.Slug,
		Status:    string(c.Status),
		CreatedAt: timestamppb.New(c.CreatedAt),
	}
}

//nolint:dupl // Customer and Cluster handlers share the same CRUD pattern
package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	authctx "github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

// CreateCustomer creates a new tenant customer and atomically binds it to the
// acting organization (REQ-051 Step 1): the customer row, the active org
// binding, its binding event, and the authorization source-version bump commit
// in one transaction, so a created customer is immediately visible to its
// creator.
func (s *Service) CreateCustomer(
	ctx context.Context,
	req *connect.Request[orchestratorv1.CreateCustomerRequest],
) (*connect.Response[orchestratorv1.CreateCustomerResponse], error) {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok || actor.OrganizationID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("actor organization is required"))
	}

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

	if err := s.store.CustomerCreates().CreateCustomerWithOrgBinding(ctx, store.CustomerBindingCreateCommand{
		Customer:  c,
		OrgID:     actor.OrganizationID,
		BindingID: uuid.New().String(),
	}); err != nil {
		s.logger.Error("create customer failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create customer: %w", err))
	}

	s.recordCustomerEvent(ctx, c.ID, "customer_created")
	s.logger.Info("customer created", "id", c.ID, "name", c.Name, "organization_id", actor.OrganizationID)
	return connect.NewResponse(&orchestratorv1.CreateCustomerResponse{
		Customer: toProtoCustomer(c),
	}), nil
}

// GetCustomer retrieves a customer by ID. Organization-domain access is
// enforced by the auth interceptor via the customer_id binding check.
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

// ListCustomers returns customers visible to the acting organization's active
// org-customer bindings (REQ-051 Step 2). The request carries no customer
// scope field, so the handler filters explicitly instead of relying on the
// generic interceptor; customers of other organizations are excluded rather
// than enumerated.
func (s *Service) ListCustomers(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ListCustomersRequest],
) (*connect.Response[orchestratorv1.ListCustomersResponse], error) {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok || actor.OrganizationID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("actor organization is required"))
	}

	bindings, err := s.store.Bindings().ListByOrg(ctx, actor.OrganizationID)
	if err != nil {
		s.logger.Error("list org bindings failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	visible := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if binding.Status == store.BindingActive {
			visible[binding.CustomerID] = struct{}{}
		}
	}

	customers, err := s.store.Customers().List(ctx, req.Msg.GetIncludeDisabled())
	if err != nil {
		s.logger.Error("list customers failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	protoCustomers := make([]*commonv1.Customer, 0, len(customers))
	for _, c := range customers {
		if _, ok := visible[c.ID]; !ok {
			continue
		}
		protoCustomers = append(protoCustomers, toProtoCustomer(c))
	}

	return connect.NewResponse(&orchestratorv1.ListCustomersResponse{
		Customers: protoCustomers,
	}), nil
}

// UpdateCustomer updates a customer's name or slug with optimistic locking
// (AC-051-02): expected_version must match the stored version, otherwise the
// update is rejected with CodeAborted / optimistic_lock_conflict.
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

	if msg.GetExpectedVersion() != c.Version {
		return nil, customerConflictError("data was modified by another user")
	}

	if name := msg.GetName(); name != "" {
		c.Name = name
	}
	if slug := msg.GetSlug(); slug != "" {
		c.Slug = slug
	}

	if err := s.store.Customers().Update(ctx, c, msg.GetExpectedVersion()); err != nil {
		if errors.Is(err, store.ErrOptimisticLock) {
			return nil, customerConflictError("data was modified by another user")
		}
		s.logger.Error("update customer failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update customer: %w", err))
	}

	s.recordCustomerEvent(ctx, c.ID, "customer_updated")
	s.logger.Info("customer updated", "id", c.ID)
	return connect.NewResponse(&orchestratorv1.UpdateCustomerResponse{
		Customer: toProtoCustomer(c),
	}), nil
}

// DisableCustomer disables a customer by ID. Disabled customers reject write
// operations.
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
	if err := s.store.Customers().Update(ctx, c, c.Version); err != nil {
		if errors.Is(err, store.ErrOptimisticLock) {
			return nil, customerConflictError("customer was modified concurrently; refresh and retry")
		}
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
		if t.State == store.TokenStatePending {
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
			if _, err := s.store.OperatorManagement().RevokeOperator(ctx, c.ID, op.ClusterID, op.ID, "customer disabled", nil); err != nil {
				s.logger.Warn("cascade revoke operator", "operator_id", op.ID, "error", err)
			}
		}
	}
	s.logger.Warn("customer disabled", "id", c.ID, "name", c.Name)
	return connect.NewResponse(&orchestratorv1.DisableCustomerResponse{}), nil
}

// ListCustomerEvents returns the customer lifecycle history, newest first.
// Organization-domain access is enforced by the auth interceptor via the
// customer_id binding check; disabled customers and their history stay
// readable (REQ-051 Step 2).
func (s *Service) ListCustomerEvents(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ListCustomerEventsRequest],
) (*connect.Response[orchestratorv1.ListCustomerEventsResponse], error) {
	// An unknown customer is NotFound; a known customer without events
	// returns an empty list (AC-051-04 empty state).
	if _, err := s.store.Customers().Get(ctx, req.Msg.GetCustomerId()); err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("customer %q not found", req.Msg.GetCustomerId()))
		}
		s.logger.Error("get customer for events failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	events, err := s.store.CustomerEvents().ListByCustomer(ctx, req.Msg.GetCustomerId())
	if err != nil {
		s.logger.Error("list customer events failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	protoEvents := make([]*commonv1.CustomerEvent, 0, len(events))
	for _, event := range events {
		protoEvents = append(protoEvents, &commonv1.CustomerEvent{
			Id:         event.ID,
			CustomerId: event.CustomerID,
			EventType:  event.EventType,
			CreatedAt:  timestamppb.New(event.CreatedAt),
		})
	}

	return connect.NewResponse(&orchestratorv1.ListCustomerEventsResponse{
		Events: protoEvents,
	}), nil
}

// recordCustomerEvent persists one lifecycle observation for the history view.
// Persistence is best-effort: a history write failure must not fail the
// originating mutation.
func (s *Service) recordCustomerEvent(ctx context.Context, customerID, eventType string) {
	event := &store.CustomerEvent{
		ID:         uuid.New().String(),
		CustomerID: customerID,
		EventType:  eventType,
	}
	if err := s.store.CustomerEvents().Create(ctx, event); err != nil {
		s.logger.Error("persist customer event failed", "customer_id", customerID, "event_type", eventType, "error", err)
	}
}

// customerConflictError maps an optimistic-lock conflict to the stable
// CodeAborted / optimistic_lock_conflict contract consumed by the web client.
func customerConflictError(description string) error {
	return connect.NewError(connect.CodeAborted, errors.New("optimistic_lock_conflict: "+description))
}

// toProtoCustomer converts a store.Customer to a commonv1.Customer proto message.
func toProtoCustomer(c *store.Customer) *commonv1.Customer {
	return &commonv1.Customer{
		Id:        c.ID,
		Name:      c.Name,
		Slug:      c.Slug,
		Status:    string(c.Status),
		CreatedAt: timestamppb.New(c.CreatedAt),
		Version:   c.Version,
	}
}

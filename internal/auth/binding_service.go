package auth

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/store"
)

// BindingService implements the BindingService Connect handler (REQ-049).
type BindingService struct {
	store    store.Store
	resolver CustomerResolver
	logger   *slog.Logger
}

// NewBindingService creates a new BindingService.
func NewBindingService(st store.Store, resolver CustomerResolver, logger *slog.Logger) *BindingService {
	return &BindingService{store: st, resolver: resolver, logger: logger}
}

// CreateBinding creates an org-customer binding (AC-049-01).
func (s *BindingService) CreateBinding(
	ctx context.Context,
	req *connect.Request[authv1.CreateBindingRequest],
) (*connect.Response[authv1.CreateBindingResponse], error) {
	msg := req.Msg

	// Verify organization exists and is active.
	org, err := s.store.Organizations().Get(ctx, msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("organization not found"))
	}
	if org.Status == store.OrgDisabled {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("organization is disabled"))
	}

	// Verify customer exists via resolver.
	exists, err := s.resolver.Exists(ctx, msg.GetCustomerId())
	if err != nil {
		s.logger.Error("customer resolver failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("customer resolution failed"))
	}
	if !exists {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("customer not found"))
	}

	// Check for existing binding.
	existing, err := s.store.Bindings().GetByOrgAndCustomer(ctx, msg.GetOrgId(), msg.GetCustomerId())
	if err == nil && existing != nil {
		// AC-049-01: if revoked, re-activate.
		if existing.Status == store.BindingRevoked {
			existing.Status = store.BindingActive
			if err := s.store.Bindings().Update(ctx, existing); err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reactivate binding: %w", err))
			}
			return connect.NewResponse(&authv1.CreateBindingResponse{
				Binding: toProtoBinding(existing),
			}), nil
		}
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("binding already exists"))
	}

	b := &store.OrgCustomerBinding{
		ID:         newID(),
		OrgID:      msg.GetOrgId(),
		CustomerID: msg.GetCustomerId(),
	}
	if err := s.store.Bindings().Create(ctx, b); err != nil {
		s.logger.Error("create binding failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create binding: %w", err))
	}

	s.logger.Info("binding created", "binding_id", b.ID, "org_id", b.OrgID, "customer_id", b.CustomerID)
	return connect.NewResponse(&authv1.CreateBindingResponse{
		Binding: toProtoBinding(b),
	}), nil
}

// GetBinding retrieves a binding by ID.
func (s *BindingService) GetBinding(
	ctx context.Context,
	req *connect.Request[authv1.GetBindingRequest],
) (*connect.Response[authv1.GetBindingResponse], error) {
	b, err := s.store.Bindings().Get(ctx, req.Msg.GetBindingId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("binding not found"))
	}
	// AC-049-04: cross-org access denied (checked by interceptor).
	return connect.NewResponse(&authv1.GetBindingResponse{
		Binding: toProtoBinding(b),
	}), nil
}

// ListBindings lists all bindings for an organization.
//
//nolint:dupl // Connect list handlers intentionally share the project response pattern.
func (s *BindingService) ListBindings(
	ctx context.Context,
	req *connect.Request[authv1.ListBindingsRequest],
) (*connect.Response[authv1.ListBindingsResponse], error) {
	bindings, err := s.store.Bindings().ListByOrg(ctx, req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list bindings: %w", err))
	}
	resp := &authv1.ListBindingsResponse{}
	for _, b := range bindings {
		resp.Bindings = append(resp.Bindings, toProtoBinding(b))
	}
	return connect.NewResponse(resp), nil
}

// RevokeBinding revokes a binding (AC-049-02).
//
//nolint:dupl // Connect update handlers intentionally share optimistic-lock mapping.
func (s *BindingService) RevokeBinding(
	ctx context.Context,
	req *connect.Request[authv1.RevokeBindingRequest],
) (*connect.Response[authv1.RevokeBindingResponse], error) {
	msg := req.Msg
	b, err := s.store.Bindings().Get(ctx, msg.GetBindingId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("binding not found"))
	}

	b.Status = store.BindingRevoked
	b.OptimisticVersion = msg.GetExpectedVersion()
	if err := s.store.Bindings().Update(ctx, b); err != nil {
		if err == store.ErrOptimisticLock {
			return nil, connect.NewError(connect.CodeAborted, fmt.Errorf("optimistic lock conflict"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("revoke binding: %w", err))
	}
	return connect.NewResponse(&authv1.RevokeBindingResponse{
		Binding: toProtoBinding(b),
	}), nil
}

func toProtoBinding(b *store.OrgCustomerBinding) *authv1.OrgCustomerBinding {
	return &authv1.OrgCustomerBinding{
		Id:                b.ID,
		OrgId:             b.OrgID,
		CustomerId:        b.CustomerID,
		Status:            string(b.Status),
		OptimisticVersion: b.OptimisticVersion,
		CreatedAt:         timestamppb.New(b.CreatedAt),
		UpdatedAt:         timestamppb.New(b.UpdatedAt),
	}
}

var _ authv1connect.BindingServiceHandler = (*BindingService)(nil)

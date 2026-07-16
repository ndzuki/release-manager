package auth

import (
	"context"
	"errors"
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
	if msg.GetOrgId() == "" || msg.GetCustomerId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("organization_id and customer_id are required"))
	}
	if err := s.authorize(ctx, msg.GetOrgId(), true); err != nil {
		return nil, err
	}
	if err := s.validateWritableTarget(ctx, msg.GetOrgId(), msg.GetCustomerId()); err != nil {
		return nil, err
	}

	existing, err := s.store.Bindings().GetByOrgAndCustomer(ctx, msg.GetOrgId(), msg.GetCustomerId())
	if err == nil {
		return s.handleExistingBinding(ctx, existing)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("lookup binding: %w", err))
	}

	binding := &store.OrgCustomerBinding{
		ID:         newID(),
		OrgID:      msg.GetOrgId(),
		CustomerID: msg.GetCustomerId(),
	}
	if err := s.store.Bindings().Create(ctx, binding); err != nil {
		if errors.Is(err, store.ErrDuplicateKey) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("duplicate_binding"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create binding: %w", err))
	}

	s.logger.Info("binding created", "binding_id", binding.ID, "org_id", binding.OrgID, "customer_id", binding.CustomerID)
	return connect.NewResponse(&authv1.CreateBindingResponse{Binding: toProtoBinding(binding)}), nil
}

// GetBinding retrieves a binding by ID.
func (s *BindingService) GetBinding(
	ctx context.Context,
	req *connect.Request[authv1.GetBindingRequest],
) (*connect.Response[authv1.GetBindingResponse], error) {
	binding, err := s.getBinding(ctx, req.Msg.GetBindingId())
	if err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, binding.OrgID, false); err != nil {
		return nil, err
	}
	return connect.NewResponse(&authv1.GetBindingResponse{Binding: toProtoBinding(binding)}), nil
}

// ListBindings lists all bindings for an organization.
func (s *BindingService) ListBindings(
	ctx context.Context,
	req *connect.Request[authv1.ListBindingsRequest],
) (*connect.Response[authv1.ListBindingsResponse], error) {
	orgID := req.Msg.GetOrgId()
	if orgID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("organization_id is required"))
	}
	if err := s.authorize(ctx, orgID, false); err != nil {
		return nil, err
	}

	bindings, err := s.store.Bindings().ListByOrg(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list bindings: %w", err))
	}
	resp := &authv1.ListBindingsResponse{Bindings: make([]*authv1.OrgCustomerBinding, 0, len(bindings))}
	for _, binding := range bindings {
		resp.Bindings = append(resp.Bindings, toProtoBinding(binding))
	}
	return connect.NewResponse(resp), nil
}

// RevokeBinding revokes a binding (AC-049-02).
func (s *BindingService) RevokeBinding(
	ctx context.Context,
	req *connect.Request[authv1.RevokeBindingRequest],
) (*connect.Response[authv1.RevokeBindingResponse], error) {
	msg := req.Msg
	binding, err := s.getBinding(ctx, msg.GetBindingId())
	if err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, binding.OrgID, true); err != nil {
		return nil, err
	}
	if binding.OptimisticVersion != msg.GetExpectedVersion() {
		return nil, connect.NewError(connect.CodeAborted, errors.New("optimistic_lock_conflict"))
	}
	if binding.Status == store.BindingRevoked {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("binding_revoked"))
	}
	if err := s.validateWritableTarget(ctx, binding.OrgID, binding.CustomerID); err != nil {
		return nil, err
	}

	if err := s.store.Bindings().SetStatus(ctx, binding, store.BindingRevoked); err != nil {
		if errors.Is(err, store.ErrOptimisticLock) {
			return nil, connect.NewError(connect.CodeAborted, errors.New("optimistic_lock_conflict"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("revoke binding: %w", err))
	}
	return connect.NewResponse(&authv1.RevokeBindingResponse{Binding: toProtoBinding(binding)}), nil
}

func (s *BindingService) validateWritableTarget(ctx context.Context, orgID, customerID string) error {
	org, err := s.store.Organizations().Get(ctx, orgID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return connect.NewError(connect.CodeNotFound, errors.New("organization_not_found"))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("get organization: %w", err))
	}
	if org.Status == store.OrgDisabled {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("organization_disabled"))
	}

	customer, err := s.resolver.Resolve(ctx, customerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return connect.NewError(connect.CodeNotFound, errors.New("customer_not_found"))
		}
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("resolve customer: %w", err))
	}
	if customer.Status == store.CustomerDisabled {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("customer_disabled"))
	}
	return nil
}

func (s *BindingService) handleExistingBinding(
	ctx context.Context,
	binding *store.OrgCustomerBinding,
) (*connect.Response[authv1.CreateBindingResponse], error) {
	if binding.Status == store.BindingActive {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("duplicate_binding"))
	}

	if err := s.store.Bindings().SetStatus(ctx, binding, store.BindingActive); err != nil {
		if errors.Is(err, store.ErrOptimisticLock) {
			return nil, connect.NewError(connect.CodeAborted, errors.New("optimistic_lock_conflict"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reactivate binding: %w", err))
	}
	return connect.NewResponse(&authv1.CreateBindingResponse{Binding: toProtoBinding(binding)}), nil
}

func (s *BindingService) getBinding(ctx context.Context, bindingID string) (*store.OrgCustomerBinding, error) {
	binding, err := s.store.Bindings().Get(ctx, bindingID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("binding_not_found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get binding: %w", err))
	}
	return binding, nil
}

func (s *BindingService) authorize(ctx context.Context, orgID string, write bool) error {
	userID, ok := ctx.Value(userIDKey).(string)
	if !ok || userID == "" {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("user_not_authenticated"))
	}

	member, err := s.store.OrgMembers().Get(ctx, orgID, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return connect.NewError(connect.CodePermissionDenied, errors.New("permission_denied"))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("authorize binding: %w", err))
	}
	if write && member.Role != store.RolePlatformAdmin && member.Role != store.RoleReleaseAdmin {
		return connect.NewError(connect.CodePermissionDenied, errors.New("permission_denied"))
	}
	return nil
}

func toProtoBinding(binding *store.OrgCustomerBinding) *authv1.OrgCustomerBinding {
	return &authv1.OrgCustomerBinding{
		Id:                binding.ID,
		OrgId:             binding.OrgID,
		CustomerId:        binding.CustomerID,
		Status:            string(binding.Status),
		OptimisticVersion: binding.OptimisticVersion,
		CreatedAt:         timestamppb.New(binding.CreatedAt),
		UpdatedAt:         timestamppb.New(binding.UpdatedAt),
	}
}

var _ authv1connect.BindingServiceHandler = (*BindingService)(nil)

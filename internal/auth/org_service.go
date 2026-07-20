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

// OrgService implements the OrganizationService Connect handler (REQ-026).
type OrgService struct {
	store    store.Store
	logger   *slog.Logger
	enforcer *Enforcer
}

// NewOrgService creates a new OrgService.
func NewOrgService(st store.Store, logger *slog.Logger, enforcer ...*Enforcer) *OrgService {
	service := &OrgService{store: st, logger: logger}
	if len(enforcer) > 0 {
		service.enforcer = enforcer[0]
	}
	return service
}

// CreateOrganization creates a new organization. Only platform_admin can create.
func (s *OrgService) CreateOrganization(
	ctx context.Context,
	req *connect.Request[authv1.CreateOrganizationRequest],
) (*connect.Response[authv1.CreateOrganizationResponse], error) {
	if err := s.requireRole(ctx, store.RolePlatformAdmin); err != nil {
		return nil, err
	}

	org := &store.Organization{
		ID:   newID(),
		Name: req.Msg.GetName(),
	}
	if err := s.store.Organizations().Create(ctx, org); err != nil {
		s.logger.Error("create organization failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create organization: %w", err))
	}

	// Automatically add the creator as platform_admin in the new org.
	userID, err := s.userID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	member := &store.OrganizationMember{
		OrgID:  org.ID,
		UserID: userID,
		Role:   store.RolePlatformAdmin,
	}
	if err := s.store.OrgMembers().Create(ctx, member); err != nil {
		s.logger.Error("add creator as platform_admin failed", "error", err)
		// Org is created but member add failed — log and continue.
	}
	if err := s.refreshPolicies(ctx); err != nil {
		return nil, err
	}

	s.logger.Info("organization created", "org_id", org.ID, "name", org.Name)
	return connect.NewResponse(&authv1.CreateOrganizationResponse{
		Organization: toProtoOrg(org),
	}), nil
}

// GetOrganization retrieves an organization by ID.
//
//nolint:dupl // Connect CRUD handlers intentionally share the project response pattern.
func (s *OrgService) GetOrganization(
	ctx context.Context,
	req *connect.Request[authv1.GetOrganizationRequest],
) (*connect.Response[authv1.GetOrganizationResponse], error) {
	org, err := s.store.Organizations().Get(ctx, req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("organization not found"))
	}
	return connect.NewResponse(&authv1.GetOrganizationResponse{
		Organization: toProtoOrg(org),
	}), nil
}

// ListOrganizations lists all organizations.
//
//nolint:dupl // Connect list handlers intentionally share the project response pattern.
func (s *OrgService) ListOrganizations(
	ctx context.Context,
	_ *connect.Request[authv1.ListOrganizationsRequest],
) (*connect.Response[authv1.ListOrganizationsResponse], error) {
	orgs, err := s.store.Organizations().List(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list organizations: %w", err))
	}
	resp := &authv1.ListOrganizationsResponse{}
	for _, org := range orgs {
		resp.Organizations = append(resp.Organizations, toProtoOrg(org))
	}
	return connect.NewResponse(resp), nil
}

// UpdateOrganization updates an organization name with optimistic locking.
//
//nolint:dupl // Connect update handlers intentionally share optimistic-lock mapping.
func (s *OrgService) UpdateOrganization(
	ctx context.Context,
	req *connect.Request[authv1.UpdateOrganizationRequest],
) (*connect.Response[authv1.UpdateOrganizationResponse], error) {
	msg := req.Msg
	org, err := s.store.Organizations().Get(ctx, msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("organization not found"))
	}
	if org.Status == store.OrgDisabled {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("organization is disabled"))
	}

	org.Name = msg.GetName()
	org.OptimisticVersion = msg.GetExpectedVersion()
	if err := s.store.Organizations().Update(ctx, org); err != nil {
		if err == store.ErrOptimisticLock {
			return nil, connect.NewError(connect.CodeAborted, fmt.Errorf("optimistic lock conflict"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update organization: %w", err))
	}
	return connect.NewResponse(&authv1.UpdateOrganizationResponse{
		Organization: toProtoOrg(org),
	}), nil
}

// DisableOrganization disables an organization (AC-026-03).
//
//nolint:dupl // Connect update handlers intentionally share optimistic-lock mapping.
func (s *OrgService) DisableOrganization(
	ctx context.Context,
	req *connect.Request[authv1.DisableOrganizationRequest],
) (*connect.Response[authv1.DisableOrganizationResponse], error) {
	msg := req.Msg
	org, err := s.store.Organizations().Get(ctx, msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("organization not found"))
	}

	org.Status = store.OrgDisabled
	org.OptimisticVersion = msg.GetExpectedVersion()
	if err := s.store.Organizations().Update(ctx, org); err != nil {
		if err == store.ErrOptimisticLock {
			return nil, connect.NewError(connect.CodeAborted, fmt.Errorf("optimistic lock conflict"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("disable organization: %w", err))
	}
	return connect.NewResponse(&authv1.DisableOrganizationResponse{
		Organization: toProtoOrg(org),
	}), nil
}

// AddMember adds a user to an organization with a role (AC-026-01 role guard).
func (s *OrgService) AddMember(
	ctx context.Context,
	req *connect.Request[authv1.AddMemberRequest],
) (*connect.Response[authv1.AddMemberResponse], error) {
	msg := req.Msg
	callerID, err := s.userID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	targetRole := store.Role(msg.GetRole())
	if !targetRole.Valid() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid role: %s", targetRole))
	}

	// Check org is not disabled.
	org, err := s.store.Organizations().Get(ctx, msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("organization not found"))
	}
	if org.Status == store.OrgDisabled {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("organization is disabled"))
	}

	// Get caller's role in this org.
	callerMember, err := s.store.OrgMembers().Get(ctx, msg.GetOrgId(), callerID)
	if err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("not a member of this organization"))
	}

	// AC-026-01: release_admin cannot grant platform_admin.
	if !callerMember.Role.CanGrant(targetRole) {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("role %s cannot grant role %s", callerMember.Role, targetRole))
	}

	member := &store.OrganizationMember{
		OrgID:  msg.GetOrgId(),
		UserID: msg.GetUserId(),
		Role:   targetRole,
	}
	if err := s.store.OrgMembers().Create(ctx, member); err != nil {
		s.logger.Error("add member failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("add member: %w", err))
	}
	if err := s.refreshPolicies(ctx); err != nil {
		return nil, err
	}

	s.logger.Info("member added", "org_id", member.OrgID, "user_id", member.UserID, "role", member.Role)
	return connect.NewResponse(&authv1.AddMemberResponse{
		Member: toProtoMember(member),
	}), nil
}

// RemoveMember removes a user from an organization (AC-026-02 last platform_admin guard).
func (s *OrgService) RemoveMember(
	ctx context.Context,
	req *connect.Request[authv1.RemoveMemberRequest],
) (*connect.Response[authv1.RemoveMemberResponse], error) {
	msg := req.Msg
	_, err := s.userID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	// AC-026-02: cannot remove the last platform_admin.
	targetMember, err := s.store.OrgMembers().Get(ctx, msg.GetOrgId(), msg.GetUserId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("member not found"))
	}

	if targetMember.Role == store.RolePlatformAdmin {
		members, err := s.store.OrgMembers().ListByOrg(ctx, msg.GetOrgId())
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list members: %w", err))
		}
		adminCount := 0
		for _, m := range members {
			if m.Role == store.RolePlatformAdmin {
				adminCount++
			}
		}
		if adminCount <= 1 {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("cannot remove the last platform_admin"))
		}
	}

	if err := s.store.OrgMembers().Delete(ctx, msg.GetOrgId(), msg.GetUserId()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("remove member: %w", err))
	}
	if err := s.refreshPolicies(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&authv1.RemoveMemberResponse{}), nil
}

// ListMembers lists all members of an organization.
//
//nolint:dupl // Connect list handlers intentionally share the project response pattern.
func (s *OrgService) ListMembers(
	ctx context.Context,
	req *connect.Request[authv1.ListMembersRequest],
) (*connect.Response[authv1.ListMembersResponse], error) {
	members, err := s.store.OrgMembers().ListByOrg(ctx, req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list members: %w", err))
	}
	resp := &authv1.ListMembersResponse{}
	for _, m := range members {
		resp.Members = append(resp.Members, toProtoMember(m))
	}
	return connect.NewResponse(resp), nil
}

// UpdateMemberRole updates a member's role (AC-026-01, AC-026-04 optimistic lock).
func (s *OrgService) UpdateMemberRole(
	ctx context.Context,
	req *connect.Request[authv1.UpdateMemberRoleRequest],
) (*connect.Response[authv1.UpdateMemberRoleResponse], error) {
	msg := req.Msg
	callerID, err := s.userID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	newRole := store.Role(msg.GetNewRole())
	if !newRole.Valid() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid role: %s", newRole))
	}

	// Get caller's role.
	callerMember, err := s.store.OrgMembers().Get(ctx, msg.GetOrgId(), callerID)
	if err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("not a member of this organization"))
	}

	// AC-026-01: caller must be able to grant the new role.
	if !callerMember.Role.CanGrant(newRole) {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("role %s cannot grant role %s", callerMember.Role, newRole))
	}

	// AC-026-02: cannot demote the last platform_admin.
	targetMember, err := s.store.OrgMembers().Get(ctx, msg.GetOrgId(), msg.GetUserId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("member not found"))
	}

	if targetMember.Role == store.RolePlatformAdmin && newRole != store.RolePlatformAdmin {
		members, err := s.store.OrgMembers().ListByOrg(ctx, msg.GetOrgId())
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list members: %w", err))
		}
		adminCount := 0
		for _, m := range members {
			if m.Role == store.RolePlatformAdmin {
				adminCount++
			}
		}
		if adminCount <= 1 {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("cannot demote the last platform_admin"))
		}
	}

	// AC-026-04: optimistic lock.
	targetMember.Role = newRole
	targetMember.OptimisticVersion = msg.GetExpectedVersion()
	if err := s.store.OrgMembers().Update(ctx, targetMember); err != nil {
		if err == store.ErrOptimisticLock {
			return nil, connect.NewError(connect.CodeAborted, fmt.Errorf("optimistic lock conflict"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update member role: %w", err))
	}
	if err := s.refreshPolicies(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&authv1.UpdateMemberRoleResponse{
		Member: toProtoMember(targetMember),
	}), nil
}

// requireRole checks that the caller has the required role in at least one organization.
func (s *OrgService) requireRole(ctx context.Context, required store.Role) error {
	userID, err := s.userID(ctx)
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, err)
	}
	members, err := s.store.OrgMembers().ListByUser(ctx, userID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("check roles: %w", err))
	}
	for _, m := range members {
		if m.Role == required {
			return nil
		}
	}
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("requires role %s", required))
}

func (s *OrgService) refreshPolicies(ctx context.Context) error {
	if s.enforcer == nil {
		return nil
	}
	if _, err := s.enforcer.RefreshPolicies(ctx); err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("refresh authorization policy: %w", err))
	}
	return nil
}

func (s *OrgService) userID(ctx context.Context) (string, error) {
	uid, ok := ctx.Value(userIDKey).(string)
	if !ok || uid == "" {
		return "", fmt.Errorf("user not authenticated")
	}
	return uid, nil
}

//nolint:dupl // Proto conversion helpers intentionally share timestamp field mapping.
func toProtoOrg(o *store.Organization) *authv1.Organization {
	return &authv1.Organization{
		Id:                o.ID,
		Name:              o.Name,
		Status:            string(o.Status),
		OptimisticVersion: o.OptimisticVersion,
		CreatedAt:         timestamppb.New(o.CreatedAt),
		UpdatedAt:         timestamppb.New(o.UpdatedAt),
	}
}

//nolint:dupl // Proto conversion helpers intentionally share timestamp field mapping.
func toProtoMember(m *store.OrganizationMember) *authv1.OrganizationMember {
	return &authv1.OrganizationMember{
		OrgId:             m.OrgID,
		UserId:            m.UserID,
		Role:              string(m.Role),
		OptimisticVersion: m.OptimisticVersion,
		CreatedAt:         timestamppb.New(m.CreatedAt),
		UpdatedAt:         timestamppb.New(m.UpdatedAt),
	}
}

var _ authv1connect.OrganizationServiceHandler = (*OrgService)(nil)

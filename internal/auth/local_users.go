package auth

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

// CreateLocalUser creates a local user and binds it to the caller's organization (D-12/D-14).
//
// Authorization (D-15): only platform_admin may create; the target role must be
// grantable by the caller (store.Role.CanGrant); authorization precedes the
// idempotency hit (ADR-009). platform_admin roles are rejected (D-16).
//
// Idempotency (D-13): an existing username returns the existing user without
// updating the password; concurrent duplicate inserts fall back to the same read-back.
func (s *AuthService) CreateLocalUser(
	ctx context.Context,
	req *connect.Request[authv1.CreateLocalUserRequest],
) (*connect.Response[authv1.CreateLocalUserResponse], error) {
	callerID, err := s.userIDFromCtx(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	msg := req.Msg
	username := msg.GetUsername()
	if username == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("username is required"))
	}

	// D-13: Idempotency-Key is optional — accepted and length-checked (≤64 chars)
	// per the approved plan, but the natural key (username) remains the idempotency
	// authority; no IK record is persisted (same stance as login-class procedures).
	if ik := req.Header().Get("Idempotency-Key"); len(ik) > 64 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("Idempotency-Key must be at most 64 characters"))
	}

	targetRole, err := resolveTargetRole(msg.GetRoles())
	if err != nil {
		return nil, err
	}

	// Resolve the target organization and authorize the caller (D-14/D-15).
	// Authorization precedes the idempotency hit (ADR-009); the interceptor already
	// denies non-platform_admin at the (auth, write) object level — this is defense
	// in depth plus the CanGrant baseline.
	targetOrgID, callerRole, err := s.resolveTargetOrgAndRole(ctx, callerID, msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	if !callerRole.CanGrant(targetRole) {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("role %s cannot grant role %s", callerRole, targetRole))
	}

	// Idempotent hit (D-13): return the existing user, never update its password.
	existing, err := s.store.Users().GetByUsername(ctx, username)
	switch {
	case err == nil:
		return s.localUserResponseFor(ctx, existing)
	case !errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("lookup user: %w", err))
	}

	hash, err := HashPassword(msg.GetPassword())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
	}
	user := &store.User{
		ID:           newID(),
		Username:     username,
		PasswordHash: hash,
		Status:       store.UserActive,
	}
	member := &store.OrganizationMember{
		OrgID:  targetOrgID,
		UserID: user.ID,
		Role:   targetRole,
	}
	if err := s.store.Users().CreateWithMembership(ctx, user, member); err != nil {
		if errors.Is(err, store.ErrDuplicateKey) {
			// Concurrent create with the same username: return the winner (D-13).
			winner, getErr := s.store.Users().GetByUsername(ctx, username)
			if getErr != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("read back user: %w", getErr))
			}
			return s.localUserResponseFor(ctx, winner)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create local user: %w", err))
	}

	// Casbin policy must reflect the new member immediately, otherwise the user
	// cannot act on any resource right after creation (same seam as OrgService).
	if err := s.refreshPolicies(ctx); err != nil {
		return nil, err
	}

	s.logger.Info("local user created", "user_id", user.ID, "username", username, "org_id", targetOrgID, "role", targetRole)
	return s.createLocalUserResponse(ctx, user)
}

// localUserResponseFor refreshes Casbin policies and renders the response for an
// existing user — the shared tail of the idempotency hit and the concurrent
// duplicate read-back paths.
func (s *AuthService) localUserResponseFor(ctx context.Context, u *store.User) (*connect.Response[authv1.CreateLocalUserResponse], error) {
	if err := s.refreshPolicies(ctx); err != nil {
		return nil, err
	}
	return s.createLocalUserResponse(ctx, u)
}

// resolveTargetRole validates the roles field (0..1 values, no platform_admin) and
// returns the effective role (empty list defaults to viewer, D-12/D-16).
func resolveTargetRole(roles []string) (store.Role, error) {
	targetRole := store.RoleViewer
	if len(roles) > 1 {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at most one role may be assigned"))
	}
	if len(roles) == 1 {
		targetRole = store.Role(roles[0])
		if !targetRole.Valid() {
			return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid role: %s", targetRole))
		}
	}
	if targetRole == store.RolePlatformAdmin {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("platform_admin is created via Initialize"))
	}
	return targetRole, nil
}

// resolveTargetOrgAndRole resolves the target organization — explicit org_id must
// be a membership of the caller, empty defaults to the caller's primary org (D-14) —
// and enforces the platform_admin requirement (D-15).
func (s *AuthService) resolveTargetOrgAndRole(ctx context.Context, callerID, orgID string) (string, store.Role, error) {
	members, err := s.store.OrgMembers().ListByUser(ctx, callerID)
	if err != nil {
		return "", "", connect.NewError(connect.CodeInternal, fmt.Errorf("list caller memberships: %w", err))
	}
	if len(members) == 0 {
		return "", "", connect.NewError(connect.CodePermissionDenied, fmt.Errorf("not a member of any organization"))
	}

	// D-14: an empty org_id binds the user to the caller's active organization —
	// the same domain the interceptor already authorized (the JWT org claim, which
	// SwitchOrganization re-signs). Fall back to the primary membership only for
	// in-process callers that carry no actor context (unit tests, service tokens).
	targetOrgID := orgID
	if targetOrgID == "" {
		if actor, ok := authctx.ActorFromContext(ctx); ok && actor.OrganizationID != "" {
			targetOrgID = actor.OrganizationID
		}
	}
	if targetOrgID == "" {
		if members[0].Role != store.RolePlatformAdmin {
			return "", "", connect.NewError(connect.CodePermissionDenied, fmt.Errorf("requires role platform_admin"))
		}
		return members[0].OrgID, members[0].Role, nil
	}

	org, err := s.store.Organizations().Get(ctx, targetOrgID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", "", connect.NewError(connect.CodeNotFound, fmt.Errorf("organization not found"))
		}
		return "", "", connect.NewError(connect.CodeInternal, fmt.Errorf("get organization: %w", err))
	}
	if org.Status == store.OrgDisabled {
		return "", "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("organization is disabled"))
	}
	var callerRole store.Role
	for _, m := range members {
		if m.OrgID == targetOrgID {
			callerRole = m.Role
			break
		}
	}
	if callerRole == "" {
		return "", "", connect.NewError(connect.CodePermissionDenied, fmt.Errorf("not a member of the organization"))
	}
	if callerRole != store.RolePlatformAdmin {
		return "", "", connect.NewError(connect.CodePermissionDenied, fmt.Errorf("requires role platform_admin"))
	}
	return targetOrgID, callerRole, nil
}

// refreshPolicies recompiles Casbin policies after a membership mutation (same
// helper as OrgService); a nil enforcer keeps unit tests without Casbin working.
func (s *AuthService) refreshPolicies(ctx context.Context) error {
	if s.enforcer == nil {
		return nil
	}
	if _, err := s.enforcer.RefreshPolicies(ctx); err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("refresh authorization policy: %w", err))
	}
	return nil
}

// GetLocalUser returns a local user by username including its role aggregation (D-12).
func (s *AuthService) GetLocalUser(
	ctx context.Context,
	req *connect.Request[authv1.GetLocalUserRequest],
) (*connect.Response[authv1.GetLocalUserResponse], error) {
	user, err := s.store.Users().GetByUsername(ctx, req.Msg.GetUsername())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get user: %w", err))
	}
	protoUser, err := s.localUserProto(ctx, user)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("aggregate roles: %w", err))
	}
	return connect.NewResponse(&authv1.GetLocalUserResponse{User: protoUser}), nil
}

// ListLocalUsers returns a stable cursor page of local users (REQ-010 pagination contract).
func (s *AuthService) ListLocalUsers(
	ctx context.Context,
	req *connect.Request[authv1.ListLocalUsersRequest],
) (*connect.Response[authv1.ListLocalUsersResponse], error) {
	page, err := s.store.Users().List(ctx, store.UserListQuery{
		Cursor:   req.Msg.GetCursor(),
		PageSize: req.Msg.GetPageSize(),
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalidCursor) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid or expired cursor"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list users: %w", err))
	}

	users := make([]*authv1.LocalUser, 0, len(page.Users))
	for _, u := range page.Users {
		protoUser, err := s.localUserProto(ctx, u)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("aggregate roles: %w", err))
		}
		users = append(users, protoUser)
	}
	return connect.NewResponse(&authv1.ListLocalUsersResponse{
		Users:      users,
		NextCursor: page.NextCursor,
	}), nil
}

// localUserProto converts a store user into the API view, aggregating its roles
// from organization memberships (D-14). The primary org matches the
// userAuthorizationContext convention (first membership).
func (s *AuthService) localUserProto(ctx context.Context, u *store.User) (*authv1.LocalUser, error) {
	members, err := s.store.OrgMembers().ListByUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	orgID, roles := membershipProjection(members)
	return &authv1.LocalUser{
		Id:       u.ID,
		Username: u.Username,
		Roles:    roles,
		OrgId:    orgID,
		Status:   string(u.Status),
	}, nil
}

// createLocalUserResponse renders the LocalUser response for a create/idempotent-hit
// outcome; memberships are re-read so the response reflects the committed state.
func (s *AuthService) createLocalUserResponse(ctx context.Context, u *store.User) (*connect.Response[authv1.CreateLocalUserResponse], error) {
	protoUser, err := s.localUserProto(ctx, u)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("aggregate roles: %w", err))
	}
	return connect.NewResponse(&authv1.CreateLocalUserResponse{User: protoUser}), nil
}

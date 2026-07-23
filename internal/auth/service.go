package auth

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/store"
)

const (
	AccessCookieName  = "rm_access"
	RefreshCookieName = "rm_refresh"
	CSRFCookieName    = "rm_csrf"
	CSRFHeaderName    = "X-CSRF-Token"
)

// BrowserSessionConfig controls browser cookie security.
type BrowserSessionConfig struct {
	SecureCookies bool
}

// AuthService implements the AuthService Connect handler (REQ-025).
//
//nolint:revive // AuthService is the canonical name matching the proto service
type AuthService struct {
	store          store.Store
	jwt            *JWTManager
	limiter        *RateLimiter
	logger         *slog.Logger
	browser        BrowserSessionConfig
	browserEnabled bool
}

// NewAuthService creates a new AuthService.
func NewAuthService(st store.Store, jwt *JWTManager, limiter *RateLimiter, logger *slog.Logger, browser ...BrowserSessionConfig) *AuthService {
	config := BrowserSessionConfig{SecureCookies: true}
	enabled := len(browser) > 0
	if enabled {
		config = browser[0]
	}
	return &AuthService{store: st, jwt: jwt, limiter: limiter, logger: logger, browser: config, browserEnabled: enabled}
}

// Login authenticates a user with username + password, returning tokens.
// AC-025-04: Error message does not reveal whether the account exists.
func (s *AuthService) Login(
	ctx context.Context,
	req *connect.Request[authv1.LoginRequest],
) (*connect.Response[authv1.LoginResponse], error) {
	if s.browserEnabled {
		return s.loginBrowserSession(ctx, req)
	}
	msg := req.Msg

	// Rate limit by username.
	ipKey := "login:" + msg.GetUsername()
	if !s.limiter.Allow(ipKey) {
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("too many login attempts"))
	}

	u, err := s.store.Users().GetByUsername(ctx, msg.GetUsername())
	if err != nil || u.Status != store.UserActive {
		// AC-025-04: uniform error — do not leak account existence.
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
	}

	if !VerifyPassword(u.PasswordHash, msg.GetPassword()) {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
	}

	orgID, roles := s.userAuthorizationContext(ctx, u.ID)

	accessToken, accessExp, err := s.jwt.GenerateAccessToken(u.ID, orgID, roles)
	if err != nil {
		s.logger.Error("generate access token failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("token generation failed"))
	}

	refreshRaw, family, refreshHash, err := s.jwt.GenerateRefreshToken()
	if err != nil {
		s.logger.Error("generate refresh token failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("token generation failed"))
	}

	ss := &store.AuthSession{
		ID:               newID(),
		UserID:           u.ID,
		TokenFamily:      family,
		RefreshTokenHash: refreshHash,
		ExpiresAt:        time.Now().UTC().Add(s.jwt.RefreshTTL()),
	}
	if err := s.store.AuthSessions().Create(ctx, ss); err != nil {
		s.logger.Error("create session failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("session creation failed"))
	}

	s.logger.Info("user logged in", "user_id", u.ID)
	return connect.NewResponse(&authv1.LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshRaw,
		ExpiresAt:    accessExp.Unix(),
		TokenType:    "Bearer",
	}), nil
}

// Logout revokes the token family of the given refresh token, or all sessions
// for the authenticated user if identified via access token (AC-025-03).
func (s *AuthService) Logout(
	ctx context.Context,
	req *connect.Request[authv1.LogoutRequest],
) (*connect.Response[authv1.LogoutResponse], error) {
	if s.browserEnabled {
		if err := s.validateCSRF(req.Header()); err != nil {
			return nil, err
		}
		if err := s.revokeRefreshCookie(ctx, req.Header()); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("logout failed: %w", err))
		}
		response := connect.NewResponse(&authv1.LogoutResponse{})
		setResponseCookies(response.Header(), s.clearBrowserCookies())
		return response, nil
	}
	// If a refresh token is provided, revoke its token family.
	if rt := req.Msg.GetRefreshToken(); rt != "" {
		refreshHash := s.jwt.HashRefreshToken(rt)
		ss, err := s.store.AuthSessions().GetByRefreshHash(ctx, refreshHash)
		if err != nil {
			// Token not found — idempotent logout (nilerr: intentional).
			return connect.NewResponse(&authv1.LogoutResponse{}), nil //nolint:nilerr // Logout is idempotent for unknown refresh tokens.
		}
		if err := s.store.AuthSessions().RevokeFamily(ctx, ss.TokenFamily); err != nil {
			s.logger.Error("revoke family failed", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("logout failed"))
		}
		return connect.NewResponse(&authv1.LogoutResponse{}), nil
	}

	// Fall back to access token: revoke all sessions for the authenticated user.
	userID, err := s.userIDFromCtx(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if err := s.store.AuthSessions().RevokeByUserID(ctx, userID); err != nil {
		s.logger.Error("revoke by user failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("logout failed"))
	}
	return connect.NewResponse(&authv1.LogoutResponse{}), nil
}

// RefreshToken rotates the refresh token and issues a new access token (AC-025-02).
func (s *AuthService) RefreshToken(
	ctx context.Context,
	req *connect.Request[authv1.RefreshTokenRequest],
) (*connect.Response[authv1.RefreshTokenResponse], error) {
	if s.browserEnabled {
		return s.refreshBrowserSession(ctx, req)
	}
	msg := req.Msg
	refreshHash := s.jwt.HashRefreshToken(msg.GetRefreshToken())

	ss, err := s.store.AuthSessions().GetByRefreshHash(ctx, refreshHash)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid refresh token"))
	}

	user, err := s.store.Users().Get(ctx, ss.UserID)
	if err != nil || user.Status != store.UserActive {
		if revokeErr := s.store.AuthSessions().RevokeFamily(ctx, ss.TokenFamily); revokeErr != nil {
			s.logger.Error("revoke disabled user session failed", "error", revokeErr)
		}
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid refresh token"))
	}

	if ss.Revoked {
		// AC-025-02: Refresh token replay — revoke the entire family.
		if err := s.store.AuthSessions().RevokeFamily(ctx, ss.TokenFamily); err != nil {
			s.logger.Error("revoke replayed family failed", "error", err)
		}
		s.logger.Warn("refresh token replay detected", "user_id", ss.UserID, "family", ss.TokenFamily)
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("refresh token has been revoked"))
	}
	if !ss.ExpiresAt.After(time.Now().UTC()) {
		if err := s.store.AuthSessions().RevokeFamily(ctx, ss.TokenFamily); err != nil {
			s.logger.Error("revoke expired family failed", "error", err)
		}
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("refresh token has expired"))
	}

	// Revoke the existing token family (rotation).
	if err := s.store.AuthSessions().RevokeFamily(ctx, ss.TokenFamily); err != nil {
		s.logger.Error("revoke old family failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("token rotation failed"))
	}

	orgID, roles := s.userAuthorizationContext(ctx, ss.UserID)

	accessToken, accessExp, err := s.jwt.GenerateAccessToken(ss.UserID, orgID, roles)
	if err != nil {
		s.logger.Error("generate access token failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("token generation failed"))
	}

	refreshRaw, refreshHash2, err := s.jwt.generateRefreshToken()
	if err != nil {
		s.logger.Error("generate refresh token failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("token generation failed"))
	}

	newSS := &store.AuthSession{
		ID:               newID(),
		UserID:           ss.UserID,
		TokenFamily:      ss.TokenFamily,
		RefreshTokenHash: refreshHash2,
		ExpiresAt:        time.Now().UTC().Add(s.jwt.RefreshTTL()),
	}
	if err := s.store.AuthSessions().Create(ctx, newSS); err != nil {
		s.logger.Error("create new session failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("session creation failed"))
	}

	return connect.NewResponse(&authv1.RefreshTokenResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshRaw,
		ExpiresAt:    accessExp.Unix(),
		TokenType:    "Bearer",
	}), nil
}

// ValidateToken validates an access token and returns the associated principal.
func (s *AuthService) ValidateToken(
	ctx context.Context,
	req *connect.Request[authv1.ValidateTokenRequest],
) (*connect.Response[authv1.ValidateTokenResponse], error) {
	if s.browserEnabled {
		return s.validateBrowserSession(ctx, req)
	}
	claims, err := s.jwt.ValidateAccessToken(req.Msg.GetToken())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid token"))
	}
	return connect.NewResponse(&authv1.ValidateTokenResponse{
		Valid:  true,
		UserId: claims.UserID,
		Roles:  claims.Roles,
		OrgId:  claims.OrgID,
	}), nil
}

// ChangePassword changes a user's password after verifying the old one (AC-025-03).
func (s *AuthService) ChangePassword(
	ctx context.Context,
	req *connect.Request[authv1.ChangePasswordRequest],
) (*connect.Response[authv1.ChangePasswordResponse], error) {
	msg := req.Msg

	userID, err := s.userIDFromCtx(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	u, err := s.store.Users().Get(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("user not found"))
	}

	if !VerifyPassword(u.PasswordHash, msg.GetOldPassword()) {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid old password"))
	}

	newHash, err := HashPassword(msg.GetNewPassword())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("password hashing failed"))
	}

	u.PasswordHash = newHash
	if err := s.store.Users().Update(ctx, u); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update password failed"))
	}

	// AC-025-03: Password changes fail closed if session revocation fails.
	if err := s.store.AuthSessions().RevokeByUserID(ctx, userID); err != nil {
		s.logger.Error("revoke sessions after password change failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("revoke sessions failed"))
	}

	return connect.NewResponse(&authv1.ChangePasswordResponse{}), nil
}

// userAuthorizationContext returns the user's primary organization and unique roles.
func (s *AuthService) userAuthorizationContext(ctx context.Context, userID string) (orgID string, roles []string) {
	members, err := s.store.OrgMembers().ListByUser(ctx, userID)
	if err != nil || len(members) == 0 {
		return "", []string{}
	}

	orgID = members[0].OrgID
	roles = make([]string, 0, len(members))
	seen := make(map[string]struct{}, len(members))
	for _, m := range members {
		r := string(m.Role)
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		roles = append(roles, r)
	}
	return orgID, roles
}

// userIDFromCtx extracts the authenticated user ID from context.
func (s *AuthService) userIDFromCtx(ctx context.Context) (string, error) {
	uid, ok := ctx.Value(userIDKey).(string)
	if !ok || uid == "" {
		return "", fmt.Errorf("user not authenticated")
	}
	return uid, nil
}

type contextKey string

const userIDKey contextKey = "userID"

var _ authv1connect.AuthServiceHandler = (*AuthService)(nil)

// GetInitStatus reports whether the system has been initialized (at least one admin user exists).
func (s *AuthService) GetInitStatus(
	ctx context.Context,
	_ *connect.Request[authv1.GetInitStatusRequest],
) (*connect.Response[authv1.GetInitStatusResponse], error) {
	count, err := s.store.Users().Count(ctx, "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check init status"))
	}
	return connect.NewResponse(&authv1.GetInitStatusResponse{
		Initialized: count > 0,
	}), nil
}

// Initialize bootstraps the system with a platform admin user and organization.
// It is idempotent: returns an error if the system is already initialized.
func (s *AuthService) Initialize(
	ctx context.Context,
	req *connect.Request[authv1.InitializeRequest],
) (*connect.Response[authv1.InitializeResponse], error) {
	msg := req.Msg

	count, err := s.store.Users().Count(ctx, "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check init status"))
	}
	if count > 0 {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("system already initialized"))
	}

	orgID := newID()
	org := &store.Organization{
		ID:     orgID,
		Name:   msg.GetOrganizationName(),
		Status: store.OrgActive,
	}
	if err := s.store.Organizations().Create(ctx, org); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create organization"))
	}

	hash, err := HashPassword(msg.GetPassword())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to hash password"))
	}

	userID := newID()
	user := &store.User{
		ID:           userID,
		Username:     msg.GetUsername(),
		PasswordHash: hash,
		Status:       store.UserActive,
	}
	if err := s.store.Users().Create(ctx, user); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create user"))
	}

	member := &store.OrganizationMember{
		OrgID:  orgID,
		UserID: userID,
		Role:   store.RolePlatformAdmin,
	}
	if err := s.store.OrgMembers().Create(ctx, member); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to add member"))
	}

	if s.browserEnabled {
		principal, organizations, expiresAt, cookies, sessionErr := s.issueBrowserSession(ctx, user, orgID)
		if sessionErr != nil {
			return nil, sessionErr
		}
		response := connect.NewResponse(&authv1.InitializeResponse{
			User: principal, Organizations: organizations, ExpiresAt: expiresAt.Unix(),
		})
		setResponseCookies(response.Header(), cookies)
		return response, nil
	}
	orgID2, roles := s.userAuthorizationContext(ctx, userID)

	accessToken, accessExp, err := s.jwt.GenerateAccessToken(userID, orgID2, roles)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("token generation failed"))
	}

	refreshRaw, family, refreshHash, err := s.jwt.GenerateRefreshToken()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("token generation failed"))
	}

	ss := &store.AuthSession{
		ID:               newID(),
		UserID:           userID,
		TokenFamily:      family,
		RefreshTokenHash: refreshHash,
		ExpiresAt:        time.Now().UTC().Add(s.jwt.RefreshTTL()),
	}
	if err := s.store.AuthSessions().Create(ctx, ss); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("session creation failed"))
	}

	orgs, err := s.store.Organizations().List(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list organizations"))
	}
	orgProtos := make([]*authv1.Organization, 0, len(orgs))
	for _, o := range orgs {
		orgProtos = append(orgProtos, &authv1.Organization{
			Id:     o.ID,
			Name:   o.Name,
			Status: string(o.Status),
		})
	}

	respUser := &authv1.SessionUser{
		Id:          userID,
		Username:    msg.GetUsername(),
		Roles:       roles,
		ActiveOrgId: orgID2,
	}

	return connect.NewResponse(&authv1.InitializeResponse{
		User:          respUser,
		Organizations: orgProtos,
		ExpiresAt:     accessExp.Unix(),
		AccessToken:   accessToken,
		RefreshToken:  refreshRaw,
		TokenType:     "Bearer",
	}), nil
}

// SwitchOrganization switches the active organization for the authenticated user (REQ-025).
func (s *AuthService) SwitchOrganization(
	ctx context.Context,
	req *connect.Request[authv1.SwitchOrganizationRequest],
) (*connect.Response[authv1.SwitchOrganizationResponse], error) {
	if s.browserEnabled {
		return s.switchBrowserOrganization(ctx, req)
	}
	userID, err := s.userIDFromCtx(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	msg := req.Msg
	targetOrgID := msg.GetOrgId()

	// Verify the user is a member of the target organization.
	members, err := s.store.OrgMembers().ListByUser(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list memberships"))
	}

	found := false
	var roles []string
	for _, m := range members {
		if m.OrgID == targetOrgID {
			found = true
		}
		r := string(m.Role)
		seen := false
		for _, existing := range roles {
			if existing == r {
				seen = true
				break
			}
		}
		if !seen {
			roles = append(roles, r)
		}
	}
	if !found {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("not a member of the organization"))
	}

	u, err := s.store.Users().Get(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("user not found"))
	}

	accessToken, accessExp, err := s.jwt.GenerateAccessToken(userID, targetOrgID, roles)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("token generation failed"))
	}

	orgs, err := s.store.Organizations().List(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list organizations"))
	}
	orgProtos := make([]*authv1.Organization, 0, len(orgs))
	for _, o := range orgs {
		orgProtos = append(orgProtos, &authv1.Organization{
			Id:     o.ID,
			Name:   o.Name,
			Status: string(o.Status),
		})
	}

	respUser := &authv1.SessionUser{
		Id:          userID,
		Username:    u.Username,
		Roles:       roles,
		ActiveOrgId: targetOrgID,
	}

	return connect.NewResponse(&authv1.SwitchOrganizationResponse{
		User:          respUser,
		Organizations: orgProtos,
		ExpiresAt:     accessExp.Unix(),
		AccessToken:   accessToken,
		TokenType:     "Bearer",
	}), nil
}

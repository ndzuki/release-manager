package auth

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/store"
)

// AuthService implements the AuthService Connect handler (REQ-025).
//
//nolint:revive // AuthService is the canonical name matching the proto service
type AuthService struct {
	store   store.Store
	jwt     *JWTManager
	limiter *RateLimiter
	logger  *slog.Logger
}

// NewAuthService creates a new AuthService.
func NewAuthService(st store.Store, jwt *JWTManager, limiter *RateLimiter, logger *slog.Logger) *AuthService {
	return &AuthService{store: st, jwt: jwt, limiter: limiter, logger: logger}
}

// Login authenticates a user with username + password, returning tokens.
// AC-025-04: Error message does not reveal whether the account exists.
func (s *AuthService) Login(
	ctx context.Context,
	req *connect.Request[authv1.LoginRequest],
) (*connect.Response[authv1.LoginResponse], error) {
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

	roles, orgID := s.userAccess(ctx, u.ID)

	accessToken, accessExp, err := s.jwt.GenerateAccessToken(u.ID, roles, orgID)
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
		ExpiresAt:        accessExp.Add(s.jwt.RefreshTTL()),
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
	// If a refresh token is provided, revoke its token family.
	if rt := req.Msg.GetRefreshToken(); rt != "" {
		refreshHash := s.jwt.HashRefreshToken(rt)
		ss, err := s.store.AuthSessions().GetByRefreshHash(ctx, refreshHash)
		if err != nil {
			// Token not found — idempotent logout (nilerr: intentional).
			return connect.NewResponse(&authv1.LogoutResponse{}), nil //nolint:nilerr
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
	msg := req.Msg
	refreshHash := s.jwt.HashRefreshToken(msg.GetRefreshToken())

	ss, err := s.store.AuthSessions().GetByRefreshHash(ctx, refreshHash)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid refresh token"))
	}

	if ss.Revoked {
		// AC-025-02: Refresh token replay — revoke the entire family.
		_ = s.store.AuthSessions().RevokeFamily(ctx, ss.TokenFamily) //nolint:errcheck
		s.logger.Warn("refresh token replay detected", "user_id", ss.UserID, "family", ss.TokenFamily)
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("refresh token has been revoked"))
	}

	// Revoke the existing token family (rotation).
	if err := s.store.AuthSessions().RevokeFamily(ctx, ss.TokenFamily); err != nil {
		s.logger.Error("revoke old family failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("token rotation failed"))
	}

	roles, orgID := s.userAccess(ctx, ss.UserID)

	accessToken, accessExp, err := s.jwt.GenerateAccessToken(ss.UserID, roles, orgID)
	if err != nil {
		s.logger.Error("generate access token failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("token generation failed"))
	}

	refreshRaw, family, refreshHash2, err := s.jwt.GenerateRefreshToken()
	if err != nil {
		s.logger.Error("generate refresh token failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("token generation failed"))
	}

	newSS := &store.AuthSession{
		ID:               newID(),
		UserID:           ss.UserID,
		TokenFamily:      family,
		RefreshTokenHash: refreshHash2,
		ExpiresAt:        accessExp.Add(s.jwt.RefreshTTL()),
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

	// AC-025-03: Revoke all sessions after password change.
	if err := s.store.AuthSessions().RevokeByUserID(ctx, userID); err != nil {
		s.logger.Error("revoke sessions after password change failed", "error", err)
	}

	return connect.NewResponse(&authv1.ChangePasswordResponse{}), nil
}

func (s *AuthService) userAccess(
	ctx context.Context,
	userID string,
) (roles []string, organizationID string) {
	members, err := s.store.OrgMembers().ListByUser(ctx, userID)
	if err != nil || len(members) == 0 {
		return []string{}, ""
	}
	seen := make(map[string]bool, len(members))
	roles = make([]string, 0, len(members))
	for _, member := range members {
		role := string(member.Role)
		if !seen[role] {
			seen[role] = true
			roles = append(roles, role)
		}
	}
	return roles, members[0].OrgID
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

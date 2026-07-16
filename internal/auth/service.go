package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/store"
)

const (
	accessCookieName   = "rm_access"
	refreshCookieName  = "rm_refresh"
	csrfCookieName     = "rm_csrf"
	csrfHeaderName     = "X-CSRF-Token"
	minimumPasswordLen = 12
)

// Browser session contract constants.
const (
	AccessCookieName  = accessCookieName
	RefreshCookieName = refreshCookieName
	CSRFCookieName    = csrfCookieName
	CSRFHeaderName    = csrfHeaderName
)

// BrowserSessionConfig controls browser cookie security.
type BrowserSessionConfig struct {
	SecureCookies bool
}

// AuthService implements the AuthService Connect handler (REQ-025, REQ-033).
//
//nolint:revive // AuthService is the canonical name matching the proto service
type AuthService struct {
	store   store.Store
	jwt     *JWTManager
	limiter *RateLimiter
	logger  *slog.Logger
	browser BrowserSessionConfig
}

// NewAuthService creates a new AuthService.
func NewAuthService(
	st store.Store,
	jwt *JWTManager,
	limiter *RateLimiter,
	logger *slog.Logger,
	browser BrowserSessionConfig,
) *AuthService {
	return &AuthService{store: st, jwt: jwt, limiter: limiter, logger: logger, browser: browser}
}

// GetInitStatus reports whether a local administrator already exists.
func (s *AuthService) GetInitStatus(
	ctx context.Context,
	_ *connect.Request[authv1.GetInitStatusRequest],
) (*connect.Response[authv1.GetInitStatusResponse], error) {
	count, err := s.store.Users().Count(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check initialization: %w", err))
	}
	return connect.NewResponse(&authv1.GetInitStatusResponse{Initialized: count > 0}), nil
}

// Initialize creates the first platform administrator and organization.
func (s *AuthService) Initialize(
	ctx context.Context,
	req *connect.Request[authv1.InitializeRequest],
) (*connect.Response[authv1.InitializeResponse], error) {
	msg := req.Msg
	username := strings.TrimSpace(msg.GetUsername())
	organizationName := strings.TrimSpace(msg.GetOrganizationName())
	if username == "" || organizationName == "" || len(msg.GetPassword()) < minimumPasswordLen {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("username, organization name, and a 12-character password are required"),
		)
	}

	count, err := s.store.Users().Count(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check initialization: %w", err))
	}
	if count != 0 {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("system is already initialized"))
	}

	passwordHash, err := HashPassword(msg.GetPassword())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("password hashing failed"))
	}
	user := &store.User{
		ID:           newID(),
		Username:     username,
		PasswordHash: passwordHash,
		Status:       store.UserActive,
	}
	if err := s.store.Users().Create(ctx, user); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create initial user: %w", err))
	}
	organization := &store.Organization{ID: newID(), Name: organizationName, Status: store.OrgActive}
	if err := s.store.Organizations().Create(ctx, organization); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create initial organization: %w", err))
	}
	membership := &store.OrganizationMember{
		OrgID:  organization.ID,
		UserID: user.ID,
		Role:   store.RolePlatformAdmin,
	}
	if err := s.store.OrgMembers().Create(ctx, membership); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create initial membership: %w", err))
	}

	principal, organizations, expiresAt, cookies, err := s.issueSession(ctx, user, organization.ID)
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&authv1.InitializeResponse{
		User:          principal,
		Organizations: organizations,
		ExpiresAt:     expiresAt.Unix(),
	})
	setCookies(response.Header(), cookies)
	s.logger.Info("system initialized", "user_id", user.ID, "org_id", organization.ID)
	return response, nil
}

// Login authenticates a user and establishes a browser cookie session.
func (s *AuthService) Login(
	ctx context.Context,
	req *connect.Request[authv1.LoginRequest],
) (*connect.Response[authv1.LoginResponse], error) {
	msg := req.Msg
	if !s.limiter.Allow("login:" + msg.GetUsername()) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many login attempts"))
	}

	user, err := s.store.Users().GetByUsername(ctx, msg.GetUsername())
	if err != nil || user.Status != store.UserActive || !VerifyPassword(user.PasswordHash, msg.GetPassword()) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid credentials"))
	}
	organizationID, err := s.primaryOrganization(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	principal, organizations, expiresAt, cookies, err := s.issueSession(ctx, user, organizationID)
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&authv1.LoginResponse{
		User:          principal,
		Organizations: organizations,
		ExpiresAt:     expiresAt.Unix(),
	})
	setCookies(response.Header(), cookies)
	s.logger.Info("user logged in", "user_id", user.ID, "org_id", organizationID)
	return response, nil
}

// Logout revokes the refresh token family and clears browser cookies.
func (s *AuthService) Logout(
	ctx context.Context,
	req *connect.Request[authv1.LogoutRequest],
) (*connect.Response[authv1.LogoutResponse], error) {
	if err := s.requireCSRF(req.Header()); err != nil {
		return nil, err
	}
	if err := s.revokeRefreshCookie(ctx, req.Header()); err != nil {
		return nil, err
	}
	response := connect.NewResponse(&authv1.LogoutResponse{})
	setCookies(response.Header(), s.clearSessionCookies())
	return response, nil
}

// RefreshToken rotates the refresh token and restores a browser session.
func (s *AuthService) RefreshToken(
	ctx context.Context,
	req *connect.Request[authv1.RefreshTokenRequest],
) (*connect.Response[authv1.RefreshTokenResponse], error) {
	if err := s.requireCSRF(req.Header()); err != nil {
		return nil, err
	}
	refreshToken := cookieValue(req.Header(), refreshCookieName)
	if refreshToken == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing refresh cookie"))
	}

	session, err := s.store.AuthSessions().GetByRefreshHash(ctx, s.jwt.HashRefreshToken(refreshToken))
	if err != nil || session.Revoked || time.Now().UTC().After(session.ExpiresAt) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid refresh session"))
	}
	if err := s.store.AuthSessions().RevokeFamily(ctx, session.TokenFamily); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("token rotation failed"))
	}
	user, err := s.store.Users().Get(ctx, session.UserID)
	if err != nil || user.Status != store.UserActive {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("user is unavailable"))
	}
	organizationID, err := s.requestedOrPrimaryOrganization(ctx, user.ID, req.Header())
	if err != nil {
		return nil, err
	}
	principal, organizations, expiresAt, cookies, err := s.issueSession(ctx, user, organizationID)
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&authv1.RefreshTokenResponse{
		User:          principal,
		Organizations: organizations,
		ExpiresAt:     expiresAt.Unix(),
	})
	setCookies(response.Header(), cookies)
	return response, nil
}

// ValidateToken restores the current principal from the access cookie.
func (s *AuthService) ValidateToken(
	ctx context.Context,
	req *connect.Request[authv1.ValidateTokenRequest],
) (*connect.Response[authv1.ValidateTokenResponse], error) {
	claims, err := s.validateAccessCookie(req.Header())
	if err != nil {
		return nil, err
	}
	user, err := s.store.Users().Get(ctx, claims.UserID)
	if err != nil || user.Status != store.UserActive {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("user is unavailable"))
	}
	principal, organizations, err := s.sessionPrincipal(ctx, user, claims.OrgID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&authv1.ValidateTokenResponse{
		Valid:         true,
		User:          principal,
		Organizations: organizations,
		ExpiresAt:     claims.ExpiresAt.Unix(),
	}), nil
}

// SwitchOrganization validates membership and reissues the browser session with a new domain.
func (s *AuthService) SwitchOrganization(
	ctx context.Context,
	req *connect.Request[authv1.SwitchOrganizationRequest],
) (*connect.Response[authv1.SwitchOrganizationResponse], error) {
	if err := s.requireCSRF(req.Header()); err != nil {
		return nil, err
	}
	claims, err := s.validateAccessCookie(req.Header())
	if err != nil {
		return nil, err
	}
	organizationID := strings.TrimSpace(req.Msg.GetOrgId())
	if _, err := s.store.OrgMembers().Get(ctx, organizationID, claims.UserID); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("organization membership is required"))
	}
	organization, err := s.store.Organizations().Get(ctx, organizationID)
	if err != nil || organization.Status != store.OrgActive {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("organization is unavailable"))
	}
	user, err := s.store.Users().Get(ctx, claims.UserID)
	if err != nil || user.Status != store.UserActive {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("user is unavailable"))
	}
	if err := s.revokeRefreshCookie(ctx, req.Header()); err != nil {
		return nil, err
	}
	principal, organizations, expiresAt, cookies, err := s.issueSession(ctx, user, organizationID)
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&authv1.SwitchOrganizationResponse{
		User:          principal,
		Organizations: organizations,
		ExpiresAt:     expiresAt.Unix(),
	})
	setCookies(response.Header(), cookies)
	return response, nil
}

// ChangePassword changes a user's password after verifying the old one.
func (s *AuthService) ChangePassword(
	ctx context.Context,
	req *connect.Request[authv1.ChangePasswordRequest],
) (*connect.Response[authv1.ChangePasswordResponse], error) {
	if err := s.requireCSRF(req.Header()); err != nil {
		return nil, err
	}
	userID, err := s.userIDFromCtx(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if len(req.Msg.GetNewPassword()) < minimumPasswordLen {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("new password must contain at least 12 characters"))
	}
	user, err := s.store.Users().Get(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("user not found"))
	}
	if !VerifyPassword(user.PasswordHash, req.Msg.GetOldPassword()) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid old password"))
	}
	newHash, err := HashPassword(req.Msg.GetNewPassword())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("password hashing failed"))
	}
	user.PasswordHash = newHash
	if err := s.store.Users().Update(ctx, user); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("update password failed"))
	}
	if err := s.store.AuthSessions().RevokeByUserID(ctx, userID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("revoke sessions failed"))
	}
	response := connect.NewResponse(&authv1.ChangePasswordResponse{})
	setCookies(response.Header(), s.clearSessionCookies())
	return response, nil
}

func (s *AuthService) issueSession(
	ctx context.Context,
	user *store.User,
	organizationID string,
) (*authv1.SessionUser, []*authv1.Organization, time.Time, []*http.Cookie, error) {
	principal, organizations, err := s.sessionPrincipal(ctx, user, organizationID)
	if err != nil {
		return nil, nil, time.Time{}, nil, err
	}
	accessToken, accessExpiresAt, err := s.jwt.GenerateAccessToken(user.ID, principal.Roles, organizationID)
	if err != nil {
		return nil, nil, time.Time{}, nil, connect.NewError(connect.CodeInternal, errors.New("token generation failed"))
	}
	refreshToken, family, refreshHash, err := s.jwt.GenerateRefreshToken()
	if err != nil {
		return nil, nil, time.Time{}, nil, connect.NewError(connect.CodeInternal, errors.New("token generation failed"))
	}
	refreshExpiresAt := time.Now().UTC().Add(s.jwt.RefreshTTL())
	if err := s.store.AuthSessions().Create(ctx, &store.AuthSession{
		ID:               newID(),
		UserID:           user.ID,
		TokenFamily:      family,
		RefreshTokenHash: refreshHash,
		ExpiresAt:        refreshExpiresAt,
	}); err != nil {
		return nil, nil, time.Time{}, nil, connect.NewError(connect.CodeInternal, errors.New("session creation failed"))
	}
	csrfToken, err := randomToken(32)
	if err != nil {
		return nil, nil, time.Time{}, nil, connect.NewError(connect.CodeInternal, errors.New("session creation failed"))
	}
	return principal, organizations, accessExpiresAt, []*http.Cookie{
		s.sessionCookie(accessCookieName, accessToken, accessExpiresAt, true),
		s.sessionCookie(refreshCookieName, refreshToken, refreshExpiresAt, true),
		s.sessionCookie(csrfCookieName, csrfToken, refreshExpiresAt, false),
	}, nil
}

func (s *AuthService) sessionPrincipal(
	ctx context.Context,
	user *store.User,
	organizationID string,
) (*authv1.SessionUser, []*authv1.Organization, error) {
	memberships, err := s.store.OrgMembers().ListByUser(ctx, user.ID)
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list memberships: %w", err))
	}
	roles := make([]string, 0, len(memberships))
	organizations := make([]*authv1.Organization, 0, len(memberships))
	activeFound := organizationID == ""
	for _, membership := range memberships {
		if membership.OrgID == organizationID {
			activeFound = true
		}
		roles = append(roles, string(membership.Role))
		organization, err := s.store.Organizations().Get(ctx, membership.OrgID)
		if err != nil || organization.Status != store.OrgActive {
			continue
		}
		organizations = append(organizations, toProtoOrg(organization))
	}
	if !activeFound {
		return nil, nil, connect.NewError(connect.CodePermissionDenied, errors.New("organization membership is required"))
	}
	return &authv1.SessionUser{
		Id:          user.ID,
		Username:    user.Username,
		Roles:       roles,
		ActiveOrgId: organizationID,
	}, organizations, nil
}

func (s *AuthService) primaryOrganization(ctx context.Context, userID string) (string, error) {
	memberships, err := s.store.OrgMembers().ListByUser(ctx, userID)
	if err != nil {
		return "", connect.NewError(connect.CodeInternal, fmt.Errorf("list memberships: %w", err))
	}
	if len(memberships) == 0 {
		return "", connect.NewError(connect.CodeFailedPrecondition, errors.New("user has no organization membership"))
	}
	return memberships[0].OrgID, nil
}

func (s *AuthService) requestedOrPrimaryOrganization(
	ctx context.Context,
	userID string,
	header http.Header,
) (string, error) {
	if token := cookieValue(header, accessCookieName); token != "" {
		if claims, err := s.jwt.ValidateAccessToken(token); err == nil && claims.UserID == userID && claims.OrgID != "" {
			if _, err := s.store.OrgMembers().Get(ctx, claims.OrgID, userID); err == nil {
				return claims.OrgID, nil
			}
		}
	}
	return s.primaryOrganization(ctx, userID)
}

func (s *AuthService) validateAccessCookie(header http.Header) (*Claims, error) {
	token := cookieValue(header, accessCookieName)
	if token == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing access cookie"))
	}
	claims, err := s.jwt.ValidateAccessToken(token)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid session"))
	}
	return claims, nil
}

func (s *AuthService) revokeRefreshCookie(ctx context.Context, header http.Header) error {
	refreshToken := cookieValue(header, refreshCookieName)
	if refreshToken == "" {
		return nil
	}
	session, err := s.store.AuthSessions().GetByRefreshHash(ctx, s.jwt.HashRefreshToken(refreshToken))
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("load browser session failed"))
	}
	if err := s.store.AuthSessions().RevokeFamily(ctx, session.TokenFamily); err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("session rotation failed"))
	}
	return nil
}

func (s *AuthService) requireCSRF(header http.Header) error {
	cookieToken := cookieValue(header, csrfCookieName)
	headerToken := header.Get(csrfHeaderName)
	if cookieToken == "" || headerToken == "" || cookieToken != headerToken {
		return connect.NewError(connect.CodePermissionDenied, errors.New("csrf validation failed"))
	}
	return nil
}

func (s *AuthService) sessionCookie(name, value string, expiresAt time.Time, httpOnly bool) *http.Cookie {
	maxAge := int(time.Until(expiresAt).Seconds())
	if value == "" {
		maxAge = -1
	}
	// #nosec G124 -- Secure is configurable only for explicit local HTTP development; production defaults true.
	return &http.Cookie{ //nolint:gosec // Secure is configurable only for explicit local HTTP development.
		Name:     name,
		Value:    value,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   maxAge,
		HttpOnly: httpOnly,
		Secure:   s.browser.SecureCookies,
		SameSite: http.SameSiteStrictMode,
	}
}

func (s *AuthService) clearSessionCookies() []*http.Cookie {
	expiresAt := time.Unix(1, 0).UTC()
	return []*http.Cookie{
		s.sessionCookie(accessCookieName, "", expiresAt, true),
		s.sessionCookie(refreshCookieName, "", expiresAt, true),
		s.sessionCookie(csrfCookieName, "", expiresAt, false),
	}
}

func cookieValue(header http.Header, name string) string {
	request := &http.Request{Header: header}
	cookie, err := request.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func setCookies(header http.Header, cookies []*http.Cookie) {
	for _, cookie := range cookies {
		header.Add("Set-Cookie", cookie.String())
	}
}

func randomToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func (s *AuthService) userIDFromCtx(ctx context.Context) (string, error) {
	userID, ok := ctx.Value(userIDKey).(string)
	if !ok || userID == "" {
		return "", errors.New("user not authenticated")
	}
	return userID, nil
}

type contextKey string

const userIDKey contextKey = "userID"

var _ authv1connect.AuthServiceHandler = (*AuthService)(nil)

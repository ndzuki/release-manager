package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

func (s *AuthService) loginBrowserSession(ctx context.Context, req *connect.Request[authv1.LoginRequest]) (*connect.Response[authv1.LoginResponse], error) {
	if !s.limiter.Allow("login:" + req.Msg.GetUsername()) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many login attempts"))
	}
	user, err := s.store.Users().GetByUsername(ctx, req.Msg.GetUsername())
	if err != nil || user.Status != store.UserActive || !VerifyPassword(user.PasswordHash, req.Msg.GetPassword()) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid credentials"))
	}
	orgID, err := s.primaryOrganization(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	session, err := s.issueBrowserSession(ctx, user, orgID)
	if err != nil {
		return nil, err
	}
	accessToken, refreshToken, tokenType := bearerPair(req.Header(), session)
	response := connect.NewResponse(&authv1.LoginResponse{
		User:          session.principal,
		Organizations: session.organizations,
		ExpiresAt:     session.expiresAt.Unix(),
		AccessToken:   accessToken,
		RefreshToken:  refreshToken,
		TokenType:     tokenType,
	})
	setResponseCookies(response.Header(), session.cookies)
	return response, nil
}

func (s *AuthService) refreshBrowserSession(ctx context.Context, req *connect.Request[authv1.RefreshTokenRequest]) (*connect.Response[authv1.RefreshTokenResponse], error) {
	if err := s.validateCSRF(req.Header()); err != nil {
		return nil, err
	}
	refreshToken := cookieValue(req.Header(), RefreshCookieName)
	if refreshToken == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing refresh cookie"))
	}
	session, err := s.store.AuthSessions().GetByRefreshHash(ctx, s.jwt.HashRefreshToken(refreshToken))
	if err != nil {
		if errors.Is(err, store.ErrUnavailable) {
			return nil, mapStoreError(err, "invalid refresh session")
		}
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid refresh session"))
	}
	if session.Revoked || !session.ExpiresAt.After(time.Now().UTC()) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid refresh session"))
	}
	if err := s.store.AuthSessions().RevokeFamily(ctx, session.TokenFamily); err != nil {
		return nil, mapStoreError(err, "token rotation failed")
	}
	user, err := s.store.Users().Get(ctx, session.UserID)
	if err != nil || user.Status != store.UserActive {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("user is unavailable"))
	}
	orgID, err := s.primaryOrganization(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	issued, err := s.issueBrowserSession(ctx, user, orgID)
	if err != nil {
		return nil, err
	}
	accessToken, refreshToken, tokenType := bearerPair(req.Header(), issued)
	response := connect.NewResponse(&authv1.RefreshTokenResponse{
		User:          issued.principal,
		Organizations: issued.organizations,
		ExpiresAt:     issued.expiresAt.Unix(),
		AccessToken:   accessToken,
		RefreshToken:  refreshToken,
		TokenType:     tokenType,
	})
	setResponseCookies(response.Header(), issued.cookies)
	return response, nil
}

func (s *AuthService) validateBrowserSession(ctx context.Context, req *connect.Request[authv1.ValidateTokenRequest]) (*connect.Response[authv1.ValidateTokenResponse], error) {
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
	return connect.NewResponse(&authv1.ValidateTokenResponse{Valid: true, User: principal, Organizations: organizations, ExpiresAt: claims.ExpiresAt.Unix()}), nil
}

func (s *AuthService) switchBrowserOrganization(ctx context.Context, req *connect.Request[authv1.SwitchOrganizationRequest]) (*connect.Response[authv1.SwitchOrganizationResponse], error) {
	if err := s.validateCSRF(req.Header()); err != nil {
		return nil, err
	}
	claims, err := s.validateAccessCookie(req.Header())
	if err != nil {
		return nil, err
	}
	user, err := s.store.Users().Get(ctx, claims.UserID)
	if err != nil || user.Status != store.UserActive {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("user is unavailable"))
	}
	session, err := s.issueBrowserSession(ctx, user, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	accessToken, refreshToken, tokenType := bearerPair(req.Header(), session)
	response := connect.NewResponse(&authv1.SwitchOrganizationResponse{
		User:          session.principal,
		Organizations: session.organizations,
		ExpiresAt:     session.expiresAt.Unix(),
		AccessToken:   accessToken,
		RefreshToken:  refreshToken,
		TokenType:     tokenType,
	})
	setResponseCookies(response.Header(), session.cookies)
	return response, nil
}

// browserSession is everything one browser login mints: the response projection,
// the cookie set the console carries, and the raw bearer pair.
//
// Dual mode (ADR-028): the tokens are part of the result on purpose. Callers
// that authenticate with `Authorization: Bearer` — devseed, the E2E runner, the
// kulala collections — read Login.AccessToken, so serving cookies only would
// silently break all of them.
type browserSession struct {
	principal     *authv1.SessionUser
	organizations []*authv1.Organization
	expiresAt     time.Time
	cookies       []*http.Cookie
	accessToken   string
	refreshToken  string
}

// browserCaller reports whether the request comes from a browser page rather
// than from a CLI/CI client.
//
// ADR-028 clause 6: the console must stay HttpOnly-cookie-only, so the bearer
// pair is returned ONLY to callers that need it (devseed, the E2E runner, the
// kulala collections). Handing tokens to a browser response body would let any
// XSS in the console origin read them, which is exactly what the HttpOnly cookie
// exists to prevent. Browsers always send Origin on a cross-origin-capable POST,
// so its presence is the discriminator.
func browserCaller(header http.Header) bool {
	return header.Get("Origin") != "" || header.Get("Sec-Fetch-Mode") != ""
}

// bearerPair returns the token fields to embed in a response: the pair for a
// non-browser caller, empty strings for a browser caller (ADR-028 clause 6).
func bearerPair(header http.Header, session browserSession) (accessToken, refreshToken, tokenType string) {
	if browserCaller(header) {
		return "", "", ""
	}
	return session.accessToken, session.refreshToken, "Bearer"
}

func (s *AuthService) issueBrowserSession(ctx context.Context, user *store.User, organizationID string) (browserSession, error) {
	principal, organizations, err := s.sessionPrincipal(ctx, user, organizationID)
	if err != nil {
		return browserSession{}, err
	}
	accessToken, accessExpiresAt, err := s.jwt.GenerateAccessToken(user.ID, organizationID, principal.Roles)
	if err != nil {
		return browserSession{}, connect.NewError(connect.CodeInternal, errors.New("token generation failed"))
	}
	refreshToken, family, refreshHash, err := s.jwt.GenerateRefreshToken()
	if err != nil {
		return browserSession{}, connect.NewError(connect.CodeInternal, errors.New("token generation failed"))
	}
	refreshExpiresAt := time.Now().UTC().Add(s.jwt.RefreshTTL())
	if err := s.store.AuthSessions().Create(ctx, &store.AuthSession{ID: newID(), UserID: user.ID, TokenFamily: family, RefreshTokenHash: refreshHash, ExpiresAt: refreshExpiresAt}); err != nil {
		return browserSession{}, mapStoreError(err, "session creation failed")
	}
	csrfToken, err := randomToken(32)
	if err != nil {
		return browserSession{}, connect.NewError(connect.CodeInternal, errors.New("session creation failed"))
	}
	return browserSession{
		principal:     principal,
		organizations: organizations,
		expiresAt:     accessExpiresAt,
		cookies: []*http.Cookie{
			s.sessionCookie(AccessCookieName, accessToken, accessExpiresAt, true),
			s.sessionCookie(RefreshCookieName, refreshToken, refreshExpiresAt, true),
			s.sessionCookie(CSRFCookieName, csrfToken, refreshExpiresAt, false),
		},
		accessToken:  accessToken,
		refreshToken: refreshToken,
	}, nil
}

func (s *AuthService) sessionPrincipal(ctx context.Context, user *store.User, organizationID string) (*authv1.SessionUser, []*authv1.Organization, error) {
	memberships, err := s.store.OrgMembers().ListByUser(ctx, user.ID)
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list memberships: %w", err))
	}
	roles := make([]string, 0, 1)
	organizations := make([]*authv1.Organization, 0, len(memberships))
	activeFound := organizationID == ""
	for _, membership := range memberships {
		if membership.OrgID == organizationID {
			activeFound = true
			roles = append(roles, string(membership.Role))
		}
		organization, orgErr := s.store.Organizations().Get(ctx, membership.OrgID)
		if orgErr == nil && organization.Status == store.OrgActive {
			organizations = append(organizations, &authv1.Organization{Id: organization.ID, Name: organization.Name, Status: string(organization.Status)})
		}
	}
	if !activeFound {
		return nil, nil, connect.NewError(connect.CodePermissionDenied, errors.New("organization membership is required"))
	}
	return &authv1.SessionUser{Id: user.ID, Username: user.Username, Roles: roles, ActiveOrgId: organizationID}, organizations, nil
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

func (s *AuthService) validateAccessCookie(header http.Header) (*Claims, error) {
	token := cookieValue(header, AccessCookieName)
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
	refreshToken := cookieValue(header, RefreshCookieName)
	if refreshToken == "" {
		return nil
	}
	session, err := s.store.AuthSessions().GetByRefreshHash(ctx, s.jwt.HashRefreshToken(refreshToken))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("get refresh session: %w", err)
	}
	return s.store.AuthSessions().RevokeFamily(ctx, session.TokenFamily)
}

func (s *AuthService) validateCSRF(header http.Header) error {
	cookieToken := cookieValue(header, CSRFCookieName)
	headerToken := header.Get(CSRFHeaderName)
	if cookieToken == "" || headerToken == "" || subtle.ConstantTimeCompare([]byte(cookieToken), []byte(headerToken)) != 1 {
		return connect.NewError(connect.CodePermissionDenied, errors.New("csrf token mismatch"))
	}
	return nil
}

//nolint:gosec // Secure is environment-configurable so local HTTP development can exercise browser sessions.
func (s *AuthService) sessionCookie(name, value string, expiresAt time.Time, httpOnly bool) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", Expires: expiresAt, HttpOnly: httpOnly, Secure: s.browser.SecureCookies, SameSite: http.SameSiteStrictMode}
}

//nolint:gosec // Secure is environment-configurable; deletion cookies retain HttpOnly and SameSite protections.
func (s *AuthService) clearBrowserCookies() []*http.Cookie {
	expiresAt := time.Unix(1, 0).UTC()
	return []*http.Cookie{
		{Name: AccessCookieName, Path: "/", Expires: expiresAt, MaxAge: -1, HttpOnly: true, Secure: s.browser.SecureCookies, SameSite: http.SameSiteStrictMode},
		{Name: RefreshCookieName, Path: "/", Expires: expiresAt, MaxAge: -1, HttpOnly: true, Secure: s.browser.SecureCookies, SameSite: http.SameSiteStrictMode},
		{Name: CSRFCookieName, Path: "/", Expires: expiresAt, MaxAge: -1, Secure: s.browser.SecureCookies, SameSite: http.SameSiteStrictMode},
	}
}

func setResponseCookies(header http.Header, cookies []*http.Cookie) {
	for _, cookie := range cookies {
		header.Add("Set-Cookie", cookie.String())
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

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("read random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

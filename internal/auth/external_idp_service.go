package auth

import (
	"context"
	"encoding/json"
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

type ExternalIdentityServiceConfig struct {
	AutoCreate        bool
	RequireApproval   bool
	OrganizationID    string
	SessionAccessTTL  time.Duration
	SessionRefreshTTL time.Duration
}

type ExternalIdentityService struct {
	store     store.Store
	jwt       *JWTManager
	providers map[string]ExternalIdP
	config    ExternalIdentityServiceConfig
	logger    *slog.Logger
}

func NewExternalIdentityService(
	st store.Store,
	jwt *JWTManager,
	providers map[string]ExternalIdP,
	cfg ExternalIdentityServiceConfig,
	logger *slog.Logger,
) *ExternalIdentityService {
	if cfg.SessionAccessTTL <= 0 {
		cfg.SessionAccessTTL = 15 * time.Minute
	}
	if cfg.SessionRefreshTTL <= 0 {
		cfg.SessionRefreshTTL = 7 * 24 * time.Hour
	}
	if providers == nil {
		providers = make(map[string]ExternalIdP)
	}
	return &ExternalIdentityService{store: st, jwt: jwt, providers: providers, config: cfg, logger: logger}
}

func (s *ExternalIdentityService) AuthenticateLDAP(
	ctx context.Context,
	req *connect.Request[authv1.AuthenticateLDAPRequest],
) (*connect.Response[authv1.AuthenticateLDAPResponse], error) {
	identity, err := s.authenticateProvider(ctx, ProviderLDAP, LDAPCredential{
		Username: req.Msg.GetUsername(),
		Password: req.Msg.GetPassword(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&authv1.AuthenticateLDAPResponse{Session: identity}), nil
}

func (s *ExternalIdentityService) GetOIDCAuthURL(
	ctx context.Context,
	_ *connect.Request[authv1.GetOIDCAuthURLRequest],
) (*connect.Response[authv1.GetOIDCAuthURLResponse], error) {
	url, err := s.externalAuthURL(ctx, ProviderOIDC)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&authv1.GetOIDCAuthURLResponse{Url: url}), nil
}

func (s *ExternalIdentityService) GetDingTalkAuthURL(
	ctx context.Context,
	_ *connect.Request[authv1.GetDingTalkAuthURLRequest],
) (*connect.Response[authv1.GetDingTalkAuthURLResponse], error) {
	url, err := s.externalAuthURL(ctx, ProviderDingTalk)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&authv1.GetDingTalkAuthURLResponse{Url: url}), nil
}

func (s *ExternalIdentityService) externalAuthURL(ctx context.Context, providerName string) (string, error) {
	provider, err := s.provider(providerName)
	if err != nil {
		return "", err
	}
	var authURL string
	switch typed := provider.(type) {
	case *OIDCProvider:
		authURL, err = typed.AuthURL(ctx)
	case *DingTalkProvider:
		authURL, err = typed.AuthURL(ctx)
	default:
		return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s provider is not configured", providerName))
	}
	if err != nil {
		return "", connect.NewError(connect.CodeUnavailable, fmt.Errorf("create %s authorization url: %w", providerName, err))
	}
	return authURL, nil
}

func (s *ExternalIdentityService) authenticateProvider(ctx context.Context, providerName string, credential any) (*authv1.LoginResponse, error) {
	provider, err := s.provider(providerName)
	if err != nil {
		return nil, err
	}
	identity, err := provider.Authenticate(ctx, credential)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("external authentication failed"))
	}
	if identity == nil || identity.Subject == "" || identity.Provider == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("external authentication returned an invalid identity"))
	}
	return s.signIn(ctx, identity)
}

func (s *ExternalIdentityService) provider(name string) (ExternalIdP, error) {
	provider, ok := s.providers[name]
	if !ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s provider is not configured", name))
	}
	return provider, nil
}

func (s *ExternalIdentityService) signIn(ctx context.Context, identity *ExternalIdentity) (*authv1.LoginResponse, error) {
	user, err := s.store.Users().GetByProviderSubject(ctx, identity.Provider, identity.Subject)
	if errors.Is(err, store.ErrNotFound) {
		if !s.config.AutoCreate {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("external identity is not provisioned"))
		}
		user, err = s.createUser(ctx, identity)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("find external user: %w", err))
	}
	if user.Status == store.UserDisabled {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("user account is disabled"))
	}
	if user.Status == store.UserPending {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("external identity approval is required"))
	}
	if err := s.applyRoleMapping(ctx, user.ID, identity); err != nil {
		return nil, err
	}
	roles := s.userRoles(ctx, user.ID)
	return s.issueSession(ctx, user.ID, roles)
}

func (s *ExternalIdentityService) createUser(ctx context.Context, identity *ExternalIdentity) (*store.User, error) {
	username := identity.Attributes["username"]
	if username == "" {
		username = identity.Attributes["email"]
	}
	if username == "" {
		username = identity.Subject
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("external identity has no username"))
	}
	status := store.UserActive
	if s.config.RequireApproval {
		status = store.UserPending
	}
	user := &store.User{ID: newID(), Username: username, Provider: identity.Provider, Subject: identity.Subject, Status: status}
	if err := s.store.Users().Create(ctx, user); err != nil {
		if errors.Is(err, store.ErrDuplicateKey) {
			user, getErr := s.store.Users().GetByProviderSubject(ctx, identity.Provider, identity.Subject)
			if getErr == nil {
				return user, nil
			}
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create external user: %w", err))
	}
	return user, nil
}

func (s *ExternalIdentityService) applyRoleMapping(ctx context.Context, userID string, identity *ExternalIdentity) error {
	if s.config.OrganizationID == "" {
		return nil
	}
	roleName := identity.Attributes["role"]
	if roleName == "" {
		return nil
	}
	role := store.Role(roleName)
	if !role.Valid() {
		return connect.NewError(connect.CodePermissionDenied, errors.New("external group maps to an invalid role"))
	}
	member, err := s.store.OrgMembers().Get(ctx, s.config.OrganizationID, userID)
	if errors.Is(err, store.ErrNotFound) {
		if err := s.store.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: s.config.OrganizationID, UserID: userID, Role: role}); err != nil {
			if errors.Is(err, store.ErrDuplicateKey) {
				return nil
			}
			return connect.NewError(connect.CodeInternal, fmt.Errorf("create external organization membership: %w", err))
		}
		return nil
	}
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("find external organization membership: %w", err))
	}
	if member.Role == role {
		return nil
	}
	member.Role = role
	if err := s.store.OrgMembers().Update(ctx, member); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("update external organization role: %w", err))
	}
	return nil
}

func (s *ExternalIdentityService) userRoles(ctx context.Context, userID string) []string {
	members, err := s.store.OrgMembers().ListByUser(ctx, userID)
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{}, len(members))
	roles := make([]string, 0, len(members))
	for _, member := range members {
		role := string(member.Role)
		if _, ok := seen[role]; ok {
			continue
		}
		seen[role] = struct{}{}
		roles = append(roles, role)
	}
	return roles
}

func (s *ExternalIdentityService) issueSession(ctx context.Context, userID string, roles []string) (*authv1.LoginResponse, error) {
	accessToken, accessExp, err := s.jwt.GenerateAccessToken(userID, roles)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("token generation failed"))
	}
	refreshRaw, family, refreshHash, err := s.jwt.GenerateRefreshToken()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("token generation failed"))
	}
	session := &store.AuthSession{ID: newID(), UserID: userID, TokenFamily: family, RefreshTokenHash: refreshHash, ExpiresAt: time.Now().UTC().Add(s.config.SessionRefreshTTL)}
	if err := s.store.AuthSessions().Create(ctx, session); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("session creation failed"))
	}
	return &authv1.LoginResponse{AccessToken: accessToken, RefreshToken: refreshRaw, ExpiresAt: accessExp.Unix(), TokenType: "Bearer"}, nil
}

func (s *ExternalIdentityService) OIDCCallback(w http.ResponseWriter, r *http.Request) {
	provider, err := s.provider(ProviderOIDC)
	if err != nil {
		http.Error(w, "oidc provider is not configured", http.StatusNotImplemented)
		return
	}
	identity, err := provider.Authenticate(r.Context(), OIDCCredential{State: r.URL.Query().Get("state"), Code: r.URL.Query().Get("code")})
	if err != nil {
		http.Error(w, "external authentication failed", http.StatusUnauthorized)
		return
	}
	response, err := s.signIn(r.Context(), identity)
	if err != nil {
		writeConnectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *ExternalIdentityService) DingTalkCallback(w http.ResponseWriter, r *http.Request) {
	provider, err := s.provider(ProviderDingTalk)
	if err != nil {
		http.Error(w, "dingtalk provider is not configured", http.StatusNotImplemented)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		code = r.URL.Query().Get("authCode")
	}
	identity, err := provider.Authenticate(r.Context(), DingTalkCredential{State: r.URL.Query().Get("state"), Code: code})
	if err != nil {
		http.Error(w, "external authentication failed", http.StatusUnauthorized)
		return
	}
	response, err := s.signIn(r.Context(), identity)
	if err != nil {
		writeConnectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}

func writeConnectError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch connect.CodeOf(err) {
	case connect.CodePermissionDenied:
		status = http.StatusForbidden
	case connect.CodeUnauthenticated:
		status = http.StatusUnauthorized
	case connect.CodeInvalidArgument:
		status = http.StatusBadRequest
	case connect.CodeFailedPrecondition:
		status = http.StatusPreconditionFailed
	case connect.CodeUnavailable:
		status = http.StatusServiceUnavailable
	}
	http.Error(w, http.StatusText(status), status)
}

var _ authv1connect.ExternalIdentityServiceHandler = (*ExternalIdentityService)(nil)

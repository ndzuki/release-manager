// Package main starts the release-auth service.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/auth"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type authSvc struct {
	dbPath          string
	signingKey      string
	providers       map[string]auth.ExternalIdP
	autoCreate      bool
	requireApproval bool
	organizationID  string
}

func (s *authSvc) Name() string { return "release-auth" }

func (s *authSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	logger.Info("store opened", "db", s.dbPath)

	signingKey := []byte(s.signingKey)

	jwtMgr := auth.NewJWTManager(signingKey, 15*time.Minute, 7*24*time.Hour)
	limiter := auth.NewRateLimiter(5, time.Minute)
	resolver := auth.StubResolver{}

	enforcer, err := auth.NewEnforcer(st, logger)
	if err != nil {
		return err
	}
	//nolint:staticcheck // context.TODO is appropriate for startup initialization
	if err := enforcer.LoadPolicies(context.TODO()); err != nil {
		logger.Error("initial policy load failed", "error", err)
	}
	enforcer.StartPolicyReloader(context.TODO(), 30*time.Second)

	publicMethods := map[string]bool{
		authv1connect.AuthServiceLoginProcedure:        true,
		authv1connect.AuthServiceRefreshTokenProcedure: true,
	}

	interceptor := auth.NewAuthInterceptor(jwtMgr, enforcer, publicMethods, logger)
	interceptorOpt := connect.WithInterceptors(interceptor)

	authSvc := auth.NewAuthService(st, jwtMgr, limiter, logger)
	authPath, authHandler := authv1connect.NewAuthServiceHandler(authSvc, interceptorOpt)
	mux.Handle(authPath, authHandler)

	orgSvc := auth.NewOrgService(st, logger)
	orgPath, orgHandler := authv1connect.NewOrganizationServiceHandler(orgSvc, interceptorOpt)
	mux.Handle(orgPath, orgHandler)

	bindingSvc := auth.NewBindingService(st, resolver, logger)
	bindingPath, bindingHandler := authv1connect.NewBindingServiceHandler(bindingSvc, interceptorOpt)
	mux.Handle(bindingPath, bindingHandler)
	externalSvc := auth.NewExternalIdentityService(st, jwtMgr, s.providers, auth.ExternalIdentityServiceConfig{
		AutoCreate:      s.autoCreate,
		RequireApproval: s.requireApproval,
		OrganizationID:  s.organizationID,
	}, logger)
	externalPath, externalHandler := authv1connect.NewExternalIdentityServiceHandler(externalSvc)
	mux.Handle(externalPath, externalHandler)
	mux.HandleFunc("GET /auth/oidc/callback", externalSvc.OIDCCallback)
	mux.HandleFunc("GET /auth/dingtalk/callback", externalSvc.DingTalkCallback)

	return nil
}

func main() {
	configPath := flag.String("config", "configs/auth.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/auth.db", "path to SQLite database")
	signingKey := flag.String("signing-key", "change-me-in-production", "JWT signing key")
	autoCreate := flag.Bool("external-auto-create", false, "automatically create users for external identities")
	requireApproval := flag.Bool("external-require-approval", false, "create external users in pending approval state")
	organizationID := flag.String("external-organization-id", "", "organization receiving mapped external roles")
	oidcIssuer := flag.String("oidc-issuer", "", "OIDC issuer URL")
	oidcClientID := flag.String("oidc-client-id", "", "OIDC client ID")
	oidcClientSecret := flag.String("oidc-client-secret", "", "OIDC client secret")
	oidcRedirectURL := flag.String("oidc-redirect-url", "", "OIDC callback URL")
	ldapURL := flag.String("ldap-url", "", "LDAP server URL")
	ldapBindDN := flag.String("ldap-bind-dn", "", "LDAP service account bind DN")
	ldapBindPassword := flag.String("ldap-bind-password", "", "LDAP service account password")
	ldapBaseDN := flag.String("ldap-base-dn", "", "LDAP search base DN")
	ldapStartTLS := flag.Bool("ldap-starttls", false, "upgrade LDAP connection with StartTLS")
	ldapProduction := flag.Bool("ldap-production", false, "reject plaintext LDAP binding")
	dingTalkClientID := flag.String("dingtalk-client-id", "", "DingTalk client ID")
	dingTalkClientSecret := flag.String("dingtalk-client-secret", "", "DingTalk client secret")
	dingTalkRedirectURL := flag.String("dingtalk-redirect-url", "", "DingTalk callback URL")
	flag.Parse()

	providers := make(map[string]auth.ExternalIdP)
	if *oidcIssuer != "" {
		provider, err := auth.NewOIDCProvider(context.Background(), auth.OIDCConfig{
			IssuerURL: *oidcIssuer, ClientID: *oidcClientID, ClientSecret: *oidcClientSecret, RedirectURL: *oidcRedirectURL,
		})
		if err != nil {
			slog.Error("initialize oidc provider", "error", err)
			return
		}
		providers[auth.ProviderOIDC] = provider
	}
	if *ldapURL != "" {
		provider := auth.NewLDAPProvider(auth.LDAPConfig{
			URL: *ldapURL, BindDN: *ldapBindDN, BindPassword: *ldapBindPassword, BaseDN: *ldapBaseDN,
			StartTLS: *ldapStartTLS, Production: *ldapProduction,
		})
		if err := provider.Validate(context.Background()); err != nil {
			slog.Error("initialize ldap provider", "error", err)
			return
		}
		providers[auth.ProviderLDAP] = provider
	}
	if *dingTalkClientID != "" {
		provider := auth.NewDingTalkProvider(auth.DingTalkConfig{
			ClientID: *dingTalkClientID, ClientSecret: *dingTalkClientSecret, RedirectURL: *dingTalkRedirectURL,
		})
		if err := provider.Validate(context.Background()); err != nil {
			slog.Error("initialize dingtalk provider", "error", err)
			return
		}
		providers[auth.ProviderDingTalk] = provider
	}

	app.Run(*configPath, &authSvc{
		dbPath: *dbPath, signingKey: *signingKey, providers: providers,
		autoCreate: *autoCreate, requireApproval: *requireApproval, organizationID: *organizationID,
	})
}

package auth

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-ldap/ldap/v3"
	"golang.org/x/oauth2"
)

const (
	ProviderOIDC     = "oidc"
	ProviderLDAP     = "ldap"
	ProviderDingTalk = "dingtalk"
)

type OIDCConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
}

type OIDCProvider struct {
	config   OIDCConfig
	oauth    *oauth2.Config
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	states   *stateStore
}

func NewOIDCProvider(ctx context.Context, cfg OIDCConfig) (*OIDCProvider, error) {
	if cfg.IssuerURL == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
		return nil, errors.New("oidc issuer, client id, and redirect url are required")
	}
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("discover oidc provider: %w", err)
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	oauthConfig := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  cfg.RedirectURL,
		Scopes:       cfg.Scopes,
	}
	return &OIDCProvider{
		config:   cfg,
		oauth:    oauthConfig,
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		states:   newStateStore(10 * time.Minute),
	}, nil
}

func (p *OIDCProvider) Provider() string { return ProviderOIDC }
func (p *OIDCProvider) Validate(context.Context) error {
	if p == nil || p.provider == nil || p.oauth == nil {
		return errors.New("oidc provider is not initialized")
	}
	return nil
}

func (p *OIDCProvider) AuthURL(ctx context.Context) (string, error) {
	if err := p.Validate(ctx); err != nil {
		return "", err
	}
	state, err := randomURLToken(32)
	if err != nil {
		return "", fmt.Errorf("generate oidc state: %w", err)
	}
	nonce, err := randomURLToken(32)
	if err != nil {
		return "", fmt.Errorf("generate oidc nonce: %w", err)
	}
	verifier := oauth2.GenerateVerifier()
	p.states.Put(state, oidcState{Nonce: nonce, Verifier: verifier})
	return p.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("nonce", nonce)), nil
}

func (p *OIDCProvider) Authenticate(ctx context.Context, credential any) (*ExternalIdentity, error) {
	input, ok := credential.(OIDCCredential)
	if !ok {
		return nil, errors.New("invalid oidc credential")
	}
	storedValue, ok := p.states.Take(input.State)
	if !ok {
		return nil, errors.New("invalid or expired oidc state")
	}
	stored, ok := storedValue.(oidcState)
	if !ok {
		return nil, errors.New("invalid oidc state")
	}
	if input.Code == "" {
		return nil, errors.New("oidc authorization code is required")
	}
	token, err := p.oauth.Exchange(ctx, input.Code, oauth2.VerifierOption(stored.Verifier))
	if err != nil {
		return nil, fmt.Errorf("exchange oidc authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, errors.New("oidc token response missing id token")
	}
	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("verify oidc id token: %w", err)
	}
	if idToken.Nonce != stored.Nonce {
		return nil, errors.New("oidc nonce mismatch")
	}
	claims := struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}{}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("decode oidc claims: %w", err)
	}
	attributes := map[string]string{"issuer": idToken.Issuer}
	if claims.Email != "" {
		attributes["email"] = claims.Email
	}
	if claims.Name != "" {
		attributes["name"] = claims.Name
	}
	if claims.EmailVerified {
		attributes["email_verified"] = "true"
	}
	return &ExternalIdentity{Provider: ProviderOIDC, Subject: idToken.Subject, Attributes: attributes}, nil
}

type OIDCCredential struct{ State, Code string }

type LDAPConfig struct {
	URL          string
	BindDN       string
	BindPassword string
	BaseDN       string
	UserFilter   string
	UsernameAttr string
	SubjectAttr  string
	GroupFilter  string
	GroupAttr    string
	RoleMappings map[string]string
	Timeout      time.Duration
	Production   bool
	StartTLS     bool
	TLSConfig    *tls.Config
}

type LDAPProvider struct{ config LDAPConfig }

func NewLDAPProvider(cfg LDAPConfig) *LDAPProvider {
	if cfg.UsernameAttr == "" {
		cfg.UsernameAttr = "uid"
	}
	if cfg.SubjectAttr == "" {
		cfg.SubjectAttr = "entryUUID"
	}
	if cfg.GroupFilter == "" {
		cfg.GroupFilter = "(member=%s)"
	}
	if cfg.GroupAttr == "" {
		cfg.GroupAttr = "cn"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	return &LDAPProvider{config: cfg}
}
func (p *LDAPProvider) Provider() string { return ProviderLDAP }
func (p *LDAPProvider) Validate(context.Context) error {
	if p == nil || p.config.URL == "" || p.config.BindDN == "" || p.config.BaseDN == "" {
		return errors.New("ldap url, bind dn, and base dn are required")
	}
	u, err := url.Parse(p.config.URL)
	if err != nil || (u.Scheme != "ldap" && u.Scheme != "ldaps") {
		return errors.New("ldap url must use ldap or ldaps scheme")
	}
	if p.config.Production && u.Scheme == "ldap" && !p.config.StartTLS {
		return errors.New("ldap plaintext binding is disabled in production")
	}
	return nil
}
func (p *LDAPProvider) Authenticate(ctx context.Context, credential any) (*ExternalIdentity, error) {
	if err := p.Validate(ctx); err != nil {
		return nil, err
	}
	input, ok := credential.(LDAPCredential)
	if !ok || input.Username == "" || input.Password == "" {
		return nil, errors.New("invalid ldap credential")
	}
	conn, err := p.connect()
	if err != nil {
		return nil, fmt.Errorf("connect to ldap: %w", err)
	}
	defer conn.Close()
	if err := conn.Bind(p.config.BindDN, p.config.BindPassword); err != nil {
		return nil, fmt.Errorf("bind ldap service account: %w", err)
	}
	filter := p.config.UserFilter
	if filter == "" {
		filter = "(" + p.config.UsernameAttr + "=%s)"
	}
	filter = fmt.Sprintf(filter, ldapEscapeFilter(input.Username))
	result, err := conn.Search(ldap.NewSearchRequest(p.config.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, 0, false, filter, []string{p.config.SubjectAttr, p.config.UsernameAttr, "dn"}, nil))
	if err != nil {
		return nil, fmt.Errorf("search ldap user: %w", err)
	}
	if len(result.Entries) != 1 {
		return nil, errors.New("ldap user not found or ambiguous")
	}
	entry := result.Entries[0]
	if err := conn.Bind(entry.DN, input.Password); err != nil {
		return nil, errors.New("invalid ldap credentials")
	}
	subject := entry.GetAttributeValue(p.config.SubjectAttr)
	if subject == "" {
		subject = entry.DN
	}
	attributes := map[string]string{"username": entry.GetAttributeValue(p.config.UsernameAttr), "dn": entry.DN}
	groups, err := p.groups(conn, entry.DN)
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		if role := p.config.RoleMappings[group]; role != "" {
			attributes["role"] = role
			break
		}
	}
	return &ExternalIdentity{Provider: ProviderLDAP, Subject: subject, Attributes: attributes}, nil
}
func (p *LDAPProvider) connect() (*ldap.Conn, error) {
	u, err := url.Parse(p.config.URL)
	if err != nil {
		return nil, fmt.Errorf("parse ldap url: %w", err)
	}
	dialer := &net.Dialer{Timeout: p.config.Timeout}
	tlsConfig := p.config.TLSConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	conn, err := ldap.DialURL(p.config.URL, ldap.DialWithDialer(dialer), ldap.DialWithTLSConfig(tlsConfig))
	if err != nil {
		return nil, err
	}
	conn.SetTimeout(p.config.Timeout)
	if u.Scheme == "ldap" && p.config.StartTLS {
		if err := conn.StartTLS(tlsConfig); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return conn, nil
}
func (p *LDAPProvider) groups(conn *ldap.Conn, userDN string) ([]string, error) {
	filter := fmt.Sprintf(p.config.GroupFilter, ldapEscapeFilter(userDN))
	result, err := conn.Search(ldap.NewSearchRequest(p.config.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false, filter, []string{p.config.GroupAttr}, nil))
	if err != nil {
		return nil, fmt.Errorf("search ldap groups: %w", err)
	}
	groups := make([]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		if group := entry.GetAttributeValue(p.config.GroupAttr); group != "" {
			groups = append(groups, group)
		}
	}
	return groups, nil
}

type LDAPCredential struct{ Username, Password string }

type DingTalkConfig struct {
	ClientID, ClientSecret, RedirectURL string
	HTTPClient                          *http.Client
}
type DingTalkProvider struct {
	config DingTalkConfig
	states *stateStore
}

func NewDingTalkProvider(cfg DingTalkConfig) *DingTalkProvider {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &DingTalkProvider{config: cfg, states: newStateStore(10 * time.Minute)}
}
func (p *DingTalkProvider) Provider() string { return ProviderDingTalk }
func (p *DingTalkProvider) Validate(context.Context) error {
	if p == nil || p.config.ClientID == "" || p.config.ClientSecret == "" || p.config.RedirectURL == "" {
		return errors.New("dingtalk client id, client secret, and redirect url are required")
	}
	return nil
}
func (p *DingTalkProvider) AuthURL(ctx context.Context) (string, error) {
	if err := p.Validate(ctx); err != nil {
		return "", err
	}
	state, err := randomURLToken(32)
	if err != nil {
		return "", err
	}
	p.states.Put(state, struct{}{})
	values := url.Values{
		"redirect_uri":  {p.config.RedirectURL},
		"response_type": {"code"},
		"client_id":     {p.config.ClientID},
		"scope":         {"openid"},
		"state":         {state},
		"prompt":        {"consent"},
	}
	return "https://login.dingtalk.com/oauth2/auth?" + values.Encode(), nil
}
func (p *DingTalkProvider) Authenticate(ctx context.Context, credential any) (*ExternalIdentity, error) {
	if err := p.Validate(ctx); err != nil {
		return nil, err
	}
	input, ok := credential.(DingTalkCredential)
	if !ok || input.Code == "" {
		return nil, errors.New("dingtalk authorization code is required")
	}
	if _, ok := p.states.Take(input.State); !ok {
		return nil, errors.New("invalid or expired dingtalk state")
	}
	accessToken, err := p.exchangeCode(ctx, input.Code)
	if err != nil {
		return nil, err
	}
	return p.fetchUser(ctx, accessToken)
}

func (p *DingTalkProvider) exchangeCode(ctx context.Context, code string) (string, error) {
	tokenBody, err := json.Marshal(map[string]string{
		"clientId":     p.config.ClientID,
		"clientSecret": p.config.ClientSecret,
		"code":         code,
		"grantType":    "authorization_code",
	})
	if err != nil {
		return "", fmt.Errorf("encode dingtalk token request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.dingtalk.com/v1.0/oauth2/userAccessToken", strings.NewReader(string(tokenBody)))
	if err != nil {
		return "", fmt.Errorf("create dingtalk token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("exchange dingtalk code: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("dingtalk token exchange returned status %d", resp.StatusCode)
	}
	var token struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return "", fmt.Errorf("decode dingtalk token response: %w", err)
	}
	if token.AccessToken == "" {
		return "", errors.New("dingtalk token response missing access token")
	}
	return token.AccessToken, nil
}

func (p *DingTalkProvider) fetchUser(ctx context.Context, accessToken string) (*ExternalIdentity, error) {
	userReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.dingtalk.com/v1.0/contact/users/me", http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create dingtalk user request: %w", err)
	}
	userReq.Header.Set("x-acs-dingtalk-access-token", accessToken)
	userResp, err := p.config.HTTPClient.Do(userReq)
	if err != nil {
		return nil, fmt.Errorf("get dingtalk user: %w", err)
	}
	defer userResp.Body.Close()
	if userResp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("dingtalk user request returned status %d", userResp.StatusCode)
	}
	var user struct {
		UnionID string `json:"unionId"`
		OpenID  string `json:"openId"`
		Email   string `json:"email"`
		Nick    string `json:"nick"`
	}
	if err := json.NewDecoder(userResp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode dingtalk user response: %w", err)
	}
	subject := user.UnionID
	if subject == "" {
		subject = user.OpenID
	}
	if subject == "" {
		return nil, errors.New("dingtalk user response missing subject")
	}
	return &ExternalIdentity{
		Provider: ProviderDingTalk,
		Subject:  subject,
		Attributes: map[string]string{
			"email": user.Email,
			"name":  user.Nick,
		},
	}, nil
}

type DingTalkCredential struct{ State, Code string }

type oidcState struct{ Nonce, Verifier string }
type stateStore struct {
	mu     sync.Mutex
	ttl    time.Duration
	values map[string]stateValue
}
type stateValue struct {
	value     any
	expiresAt time.Time
}

func newStateStore(ttl time.Duration) *stateStore {
	return &stateStore{ttl: ttl, values: make(map[string]stateValue)}
}
func (s *stateStore) Put(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = stateValue{value: value, expiresAt: time.Now().Add(s.ttl)}
}
func (s *stateStore) Take(key string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	if !ok {
		return nil, false
	}
	delete(s.values, key)
	if time.Now().After(value.expiresAt) {
		return nil, false
	}
	return value.value, true
}
func randomURLToken(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func ldapEscapeFilter(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch r {
		case '*', '(', ')', '\\', 0:
			fmt.Fprintf(&b, "\\%02x", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

var _ ExternalIdP = (*OIDCProvider)(nil)
var _ ExternalIdP = (*LDAPProvider)(nil)
var _ ExternalIdP = (*DingTalkProvider)(nil)

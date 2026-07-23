package auth_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestAuthService_BrowserSessionLifecycle(t *testing.T) {
	st, err := sqlitestore.Open("file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	jwtManager := auth.NewJWTManager([]byte("0123456789abcdef0123456789abcdef"), 15*time.Minute, 24*time.Hour)
	service := auth.NewAuthService(
		st,
		jwtManager,
		auth.NewRateLimiter(10, time.Minute),
		slog.Default(),
		auth.BrowserSessionConfig{SecureCookies: false},
	)
	ctx := context.Background()

	initStatus, err := service.GetInitStatus(ctx, connect.NewRequest(&authv1.GetInitStatusRequest{}))
	require.NoError(t, err)
	assert.False(t, initStatus.Msg.GetInitialized())

	initialized, err := service.Initialize(ctx, connect.NewRequest(&authv1.InitializeRequest{
		Username:         "admin",
		Password:         "correct horse battery staple",
		OrganizationName: "Platform",
	}))
	require.NoError(t, err)
	require.NotNil(t, initialized.Msg.GetUser())
	assert.Equal(t, "admin", initialized.Msg.GetUser().GetUsername())
	assert.NotEmpty(t, initialized.Msg.GetUser().GetActiveOrgId())

	cookies := responseCookies(initialized.Header())
	assert.True(t, cookies[auth.AccessCookieName].HttpOnly)
	assert.True(t, cookies[auth.RefreshCookieName].HttpOnly)
	assert.False(t, cookies[auth.CSRFCookieName].HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, cookies[auth.AccessCookieName].SameSite)

	validateRequest := connect.NewRequest(&authv1.ValidateTokenRequest{})
	addRequestCookies(validateRequest.Header(), cookies)
	validated, err := service.ValidateToken(ctx, validateRequest)
	require.NoError(t, err)
	assert.Equal(t, initialized.Msg.GetUser().GetId(), validated.Msg.GetUser().GetId())

	logoutWithoutCSRF := connect.NewRequest(&authv1.LogoutRequest{})
	addRequestCookies(logoutWithoutCSRF.Header(), cookies)
	secondOrganization := &store.Organization{ID: "org-second", Name: "Second", Status: store.OrgActive}
	require.NoError(t, st.Organizations().Create(ctx, secondOrganization))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: secondOrganization.ID, UserID: initialized.Msg.GetUser().GetId(), Role: store.RoleViewer,
	}))

	switchRequest := connect.NewRequest(&authv1.SwitchOrganizationRequest{OrgId: secondOrganization.ID})
	addRequestCookies(switchRequest.Header(), cookies)
	switchRequest.Header().Set(auth.CSRFHeaderName, cookies[auth.CSRFCookieName].Value)
	switched, err := service.SwitchOrganization(ctx, switchRequest)
	require.NoError(t, err)
	assert.Equal(t, secondOrganization.ID, switched.Msg.GetUser().GetActiveOrgId())
	switchedCookies := responseCookies(switched.Header())
	switchedClaims, err := jwtManager.ValidateAccessToken(switchedCookies[auth.AccessCookieName].Value)
	require.NoError(t, err)
	assert.Equal(t, secondOrganization.ID, switchedClaims.OrgID)

	nonMemberRequest := connect.NewRequest(&authv1.SwitchOrganizationRequest{OrgId: "org-non-member"})
	addRequestCookies(nonMemberRequest.Header(), switchedCookies)
	nonMemberRequest.Header().Set(auth.CSRFHeaderName, switchedCookies[auth.CSRFCookieName].Value)
	_, err = service.SwitchOrganization(ctx, nonMemberRequest)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	missingCSRFRequest := connect.NewRequest(&authv1.SwitchOrganizationRequest{OrgId: secondOrganization.ID})
	addRequestCookies(missingCSRFRequest.Header(), switchedCookies)
	_, err = service.SwitchOrganization(ctx, missingCSRFRequest)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = service.Logout(ctx, logoutWithoutCSRF)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	logoutRequest := connect.NewRequest(&authv1.LogoutRequest{})
	addRequestCookies(logoutRequest.Header(), cookies)
	logoutRequest.Header().Set(auth.CSRFHeaderName, cookies[auth.CSRFCookieName].Value)
	loggedOut, err := service.Logout(ctx, logoutRequest)
	require.NoError(t, err)
	for _, cookie := range responseCookies(loggedOut.Header()) {
		assert.Negative(t, cookie.MaxAge)
	}
}

func responseCookies(header http.Header) map[string]*http.Cookie {
	response := httptest.NewRecorder().Result()
	response.Header = header
	cookies := make(map[string]*http.Cookie)
	for _, cookie := range response.Cookies() {
		cookies[cookie.Name] = cookie
	}
	return cookies
}

func addRequestCookies(header http.Header, cookies map[string]*http.Cookie) {
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://release-manager.test", http.NoBody)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	header.Set("Cookie", request.Header.Get("Cookie"))
}

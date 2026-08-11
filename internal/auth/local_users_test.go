package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/store"
)

// localUserHarness spins up the real handler stack (JWT + Casbin + sessions)
// so authorization precedence (AC-072-04) is exercised at the interceptor seam.
type localUserHarness struct {
	server   *httptest.Server
	client   authv1connect.AuthServiceClient
	st       store.Store
	enforcer *Enforcer
	svc      *AuthService
}

func newLocalUserHarness(t *testing.T) *localUserHarness {
	t.Helper()
	enforcer, st := setupEnforcer(t)
	ctx := context.Background()
	passwordHash, err := HashPassword("password")
	require.NoError(t, err)
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: "org-1", Name: "Org 1"}))
	require.NoError(t, st.Users().Create(ctx, &store.User{
		ID: "admin-1", Username: "admin", PasswordHash: passwordHash,
	}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: "org-1", UserID: "admin-1", Role: store.RolePlatformAdmin,
	}))
	require.NoError(t, enforcer.LoadPolicies(ctx))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	jwtManager := NewJWTManager([]byte("test-signing-key"), time.Hour, time.Hour)
	interceptor := NewAuthInterceptor(jwtManager, st, enforcer, map[string]bool{
		authv1connect.AuthServiceLoginProcedure: true,
	}, logger)
	authService := NewAuthService(st, jwtManager, NewRateLimiter(10, time.Minute), logger, enforcer)
	mux := http.NewServeMux()
	authPath, authHandler := authv1connect.NewAuthServiceHandler(
		authService,
		connect.WithInterceptors(interceptor),
	)
	mux.Handle(authPath, authHandler)
	server := httptest.NewServer(mux)
	return &localUserHarness{
		server:   server,
		client:   authv1connect.NewAuthServiceClient(server.Client(), server.URL),
		st:       st,
		enforcer: enforcer,
		svc:      authService,
	}
}

func (h *localUserHarness) login(t *testing.T, username string) string {
	t.Helper()
	resp, err := h.client.Login(context.Background(), connect.NewRequest(&authv1.LoginRequest{
		Username: username, Password: "password",
	}))
	require.NoError(t, err)
	return resp.Msg.GetAccessToken()
}

func (h *localUserHarness) createLocalUser(t *testing.T, token, username, password string, roles []string, orgID string) (*authv1.LocalUser, error) {
	t.Helper()
	req := connect.NewRequest(&authv1.CreateLocalUserRequest{
		Username: username, Password: password, Roles: roles, OrgId: orgID,
	})
	req.Header().Set("Authorization", "Bearer "+token)
	resp, err := h.client.CreateLocalUser(context.Background(), req)
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetUser(), nil
}

func newLocalUserHarnessWithMember(t *testing.T, username string, role store.Role) (h *localUserHarness, token string) {
	t.Helper()
	h = newLocalUserHarness(t)
	passwordHash, err := HashPassword("password")
	require.NoError(t, err)
	require.NoError(t, h.st.Users().Create(context.Background(), &store.User{
		ID: username + "-id", Username: username, PasswordHash: passwordHash,
	}))
	require.NoError(t, h.st.OrgMembers().Create(context.Background(), &store.OrganizationMember{
		OrgID: "org-1", UserID: username + "-id", Role: role,
	}))
	token = h.login(t, username)

	return h, token
}

// Multi-org regression (deep review): an empty org_id must bind the new user to
// the caller's ACTIVE organization — the JWT org claim the interceptor
// authorized, which SwitchOrganization re-signs — not the oldest membership.
// Before the fix this either created the user in the wrong org or returned a
// spurious PermissionDenied for a platform_admin whose active org differed
// from their first membership.
func TestCreateLocalUser_EmptyOrgUsesActiveOrganization(t *testing.T) {
	h := newLocalUserHarness(t)
	ctx := context.Background()

	// Admin is platform_admin in both org-1 (oldest membership) and org-2.
	// After switching the active org to org-2, an empty org_id must bind the new
	// user to org-2 — the JWT org claim the interceptor authorized — not the
	// oldest membership (org-1). Before the fix the user was silently created in
	// org-1 (or, when the oldest membership lacked platform_admin, the request
	// failed with a spurious PermissionDenied despite the interceptor granting
	// (auth, write) in the active org).
	require.NoError(t, h.st.Organizations().Create(ctx, &store.Organization{ID: "org-2", Name: "Org 2"}))
	require.NoError(t, h.st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: "org-2", UserID: "admin-1", Role: store.RolePlatformAdmin,
	}))
	_, err := h.enforcer.RefreshPolicies(ctx)
	require.NoError(t, err)
	// The direct switch below mints a token without a Login session row; the
	// interceptor requires an active session for the user, so create one.
	require.NoError(t, h.st.AuthSessions().Create(ctx, &store.AuthSession{
		ID: "sess-admin-1", UserID: "admin-1", TokenFamily: "tf-1",
		RefreshTokenHash: "h", ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(),
	}))

	// SwitchOrganization is not mapped in the interceptor's action table
	// (pre-existing on main: "Switch" matches no action prefix), so the switch
	// happens through the service directly with the interceptor's user context —
	// the minted token is identical to what the production flow would use, and
	// CreateLocalUser below still runs through the real HTTP + interceptor stack.
	switchCtx := context.WithValue(ctx, userIDKey, "admin-1")
	switched, err := h.svc.SwitchOrganization(switchCtx, connect.NewRequest(&authv1.SwitchOrganizationRequest{OrgId: "org-2"}))
	require.NoError(t, err)
	switchedToken := switched.Msg.GetAccessToken()

	// Empty org_id: the new user must land in the active org (org-2), not the
	// oldest membership (org-1).
	created, err := h.createLocalUser(t, switchedToken, "org2-user", "pass", nil, "")
	require.NoError(t, err)
	assert.Equal(t, "org-2", created.GetOrgId())
}

// D-14 org domain: an explicit org_id must be a membership of the caller even
// when it is disabled — a disabled org rejects user creation (FailedPrecondition).
func TestCreateLocalUser_DisabledOrgRejected(t *testing.T) {
	h := newLocalUserHarness(t)
	ctx := context.Background()
	require.NoError(t, h.st.Organizations().Create(ctx, &store.Organization{ID: "org-2", Name: "Org 2", Status: store.OrgDisabled}))
	require.NoError(t, h.st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: "org-2", UserID: "admin-1", Role: store.RolePlatformAdmin,
	}))
	_, err := h.enforcer.RefreshPolicies(ctx)
	require.NoError(t, err)
	require.NoError(t, h.st.AuthSessions().Create(ctx, &store.AuthSession{
		ID: "sess-admin-1", UserID: "admin-1", TokenFamily: "tf-1",
		RefreshTokenHash: "h", ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(),
	}))
	// Switch via the service directly (see the interceptor gap noted in
	// TestCreateLocalUser_EmptyOrgUsesActiveOrganization); CreateLocalUser still
	// runs through the real HTTP + interceptor stack.
	switchCtx := context.WithValue(ctx, userIDKey, "admin-1")
	switched, err := h.svc.SwitchOrganization(switchCtx, connect.NewRequest(&authv1.SwitchOrganizationRequest{OrgId: "org-2"}))
	require.NoError(t, err)
	switchedToken := switched.Msg.GetAccessToken()
	_, err = h.createLocalUser(t, switchedToken, "disabled-org-user", "pass", nil, "org-2")
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// AC-072-01: Given a platform_admin calls CreateLocalUser, When retried
// idempotently, Then the same user is returned without a duplicate creation.
func TestCreateLocalUser_IdempotentRetryReturnsSameUser(t *testing.T) {
	h := newLocalUserHarness(t)
	token := h.login(t, "admin")

	first, err := h.createLocalUser(t, token, "dev-deployer", "deployer-pass-1", []string{"deployer"}, "")
	require.NoError(t, err)
	assert.Equal(t, "dev-deployer", first.GetUsername())
	assert.Equal(t, "org-1", first.GetOrgId())
	assert.Equal(t, []string{"deployer"}, first.GetRoles())
	assert.Equal(t, string(store.UserActive), first.GetStatus())

	// Idempotent retry: same username returns the same user (same ID), password unchanged.
	retry, err := h.createLocalUser(t, token, "dev-deployer", "different-password", []string{"deployer"}, "")
	require.NoError(t, err)
	assert.Equal(t, first.GetId(), retry.GetId())

	page, err := h.st.Users().List(context.Background(), store.UserListQuery{PageSize: 100})
	require.NoError(t, err)
	count := 0
	for _, u := range page.Users {
		if u.Username == "dev-deployer" {
			count++
		}
	}
	assert.Equal(t, 1, count, "username must not be duplicated")
}

// AC-072-02: Given 已创建用户，When GetLocalUser/ListLocalUsers，Then 可查询（含角色信息）。
func TestGetAndListLocalUsers_IncludeRoles(t *testing.T) {
	h := newLocalUserHarness(t)
	token := h.login(t, "admin")

	created, err := h.createLocalUser(t, token, "dev-deployer", "deployer-pass-1", []string{"deployer"}, "")
	require.NoError(t, err)
	_, err = h.createLocalUser(t, token, "dev-reader", "reader-pass-1", nil, "")
	require.NoError(t, err)

	getReq := connect.NewRequest(&authv1.GetLocalUserRequest{Username: "dev-deployer"})
	getReq.Header().Set("Authorization", "Bearer "+token)
	getResp, err := h.client.GetLocalUser(context.Background(), getReq)
	require.NoError(t, err)
	assert.Equal(t, created.GetId(), getResp.Msg.GetUser().GetId())
	assert.Equal(t, []string{"deployer"}, getResp.Msg.GetUser().GetRoles())

	listReq := connect.NewRequest(&authv1.ListLocalUsersRequest{PageSize: 10})
	listReq.Header().Set("Authorization", "Bearer "+token)
	listResp, err := h.client.ListLocalUsers(context.Background(), listReq)
	require.NoError(t, err)
	usernames := make([]string, 0, len(listResp.Msg.GetUsers()))
	byName := map[string]*authv1.LocalUser{}
	for _, u := range listResp.Msg.GetUsers() {
		usernames = append(usernames, u.GetUsername())
		byName[u.GetUsername()] = u
	}
	assert.Contains(t, usernames, "dev-deployer")
	assert.Contains(t, usernames, "dev-reader")
	assert.Equal(t, []string{"viewer"}, byName["dev-reader"].GetRoles(), "empty roles must default to viewer")
}

// AC-072-03: Given devseed, When dev-admin goes through Initialize and
// dev-deployer/dev-reader through CreateLocalUser with roles, Then the REQ-065
// seed contract holds (platform_admin rejected; idempotent hit reuses the password).
func TestCreateLocalUser_SeedContract(t *testing.T) {
	h := newLocalUserHarness(t)
	token := h.login(t, "admin")

	// dev-deployer binds the deployer role via CreateLocalUser.
	_, err := h.createLocalUser(t, token, "dev-deployer", "deployer-pass-1", []string{"deployer"}, "")
	require.NoError(t, err)

	// D-16: CreateLocalUser rejects the platform_admin role.
	_, err = h.createLocalUser(t, token, "dev-admin", "admin-pass-1", []string{"platform_admin"}, "")
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// D-17: an idempotent hit reuses the existing password — replaying the same
	// username with a different password returns the existing user and the
	// original password keeps working.
	replayed, err := h.createLocalUser(t, token, "dev-deployer", "rotated-password", nil, "")
	require.NoError(t, err)
	assert.Equal(t, "dev-deployer", replayed.GetUsername())

	loginResp, err := h.client.Login(context.Background(), connect.NewRequest(&authv1.LoginRequest{
		Username: "dev-deployer", Password: "deployer-pass-1",
	}))
	require.NoError(t, err, "original password must remain valid after idempotent replay")
	assert.NotEmpty(t, loginResp.Msg.GetAccessToken())
}

// AC-072-04: Given 非授权角色调用 CreateLocalUser，When 执行，Then permission_denied。
func TestCreateLocalUser_NonAdminDenied(t *testing.T) {
	for _, role := range []store.Role{store.RoleReleaseAdmin, store.RoleDeployer, store.RoleViewer} {
		t.Run(string(role), func(t *testing.T) {
			h, token := newLocalUserHarnessWithMember(t, "actor-"+string(role), role)
			_, err := h.createLocalUser(t, token, "victim", "pass", nil, "")
			require.Error(t, err)
			assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		})
	}
}

// Concurrent idempotency (plan non-blocking observation ②): N goroutines
// creating the same username yield a single user, backed by the username UNIQUE
// constraint.
func TestCreateLocalUser_ConcurrentSameUsernameCreatesSingleUser(t *testing.T) {
	h := newLocalUserHarness(t)
	token := h.login(t, "admin")

	const workers = 8
	var wg sync.WaitGroup
	ids := make([]string, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := connect.NewRequest(&authv1.CreateLocalUserRequest{
				Username: "concurrent-user", Password: fmt.Sprintf("pass-%d", i),
			})
			req.Header().Set("Authorization", "Bearer "+token)
			resp, err := h.client.CreateLocalUser(context.Background(), req)
			if err == nil {
				ids[i] = resp.Msg.GetUser().GetId()
			}
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i := 0; i < workers; i++ {
		require.NoError(t, errs[i], "all concurrent creates must succeed via idempotent read-back")
		require.NotEmpty(t, ids[i])
		assert.Equal(t, ids[0], ids[i], "all workers must observe the same user")
	}

	page, err := h.st.Users().List(context.Background(), store.UserListQuery{PageSize: 100})
	require.NoError(t, err)
	count := 0
	for _, u := range page.Users {
		if u.Username == "concurrent-user" {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

// ListLocalUsers pagination boundaries: page_size truncation + next_cursor paging
// + malformed cursor → InvalidArgument.
func TestListLocalUsers_Pagination(t *testing.T) {
	h := newLocalUserHarness(t)
	token := h.login(t, "admin")
	for i := 0; i < 5; i++ {
		_, err := h.createLocalUser(t, token, fmt.Sprintf("user-%02d", i), "pass", nil, "")
		require.NoError(t, err)
	}

	req := connect.NewRequest(&authv1.ListLocalUsersRequest{PageSize: 2})
	req.Header().Set("Authorization", "Bearer "+token)
	first, err := h.client.ListLocalUsers(context.Background(), req)
	require.NoError(t, err)
	assert.Len(t, first.Msg.GetUsers(), 2)
	assert.NotEmpty(t, first.Msg.GetNextCursor())

	req2 := connect.NewRequest(&authv1.ListLocalUsersRequest{PageSize: 2, Cursor: first.Msg.GetNextCursor()})
	req2.Header().Set("Authorization", "Bearer "+token)
	second, err := h.client.ListLocalUsers(context.Background(), req2)
	require.NoError(t, err)
	assert.Len(t, second.Msg.GetUsers(), 2)
	seen := map[string]bool{}
	for _, u := range append(append([]*authv1.LocalUser{}, first.Msg.GetUsers()...), second.Msg.GetUsers()...) {
		assert.False(t, seen[u.GetUsername()], "pages must not overlap")
		seen[u.GetUsername()] = true
	}

	bad := connect.NewRequest(&authv1.ListLocalUsersRequest{Cursor: "not-a-cursor"})
	bad.Header().Set("Authorization", "Bearer "+token)
	_, err = h.client.ListLocalUsers(context.Background(), bad)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// Request-level validation: >1 roles → InvalidArgument; unknown role →
// InvalidArgument; empty username → InvalidArgument.
func TestCreateLocalUser_RequestValidation(t *testing.T) {
	h := newLocalUserHarness(t)
	token := h.login(t, "admin")

	_, err := h.createLocalUser(t, token, "", "pass", nil, "")
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = h.createLocalUser(t, token, "multi-role", "pass", []string{"viewer", "deployer"}, "")
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = h.createLocalUser(t, token, "bogus-role", "pass", []string{"superadmin"}, "")
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// D-13: Idempotency-Key 可选接受，超 64 字符 → InvalidArgument；合法 IK 不影响自然键幂等。
	ikReq := connect.NewRequest(&authv1.CreateLocalUserRequest{
		Username: "ik-user", Password: "pass",
	})
	ikReq.Header().Set("Authorization", "Bearer "+token)
	ikReq.Header().Set("Idempotency-Key", strings.Repeat("k", 65))
	_, err = h.client.CreateLocalUser(context.Background(), ikReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	okIK := connect.NewRequest(&authv1.CreateLocalUserRequest{
		Username: "ik-user", Password: "pass",
	})
	okIK.Header().Set("Authorization", "Bearer "+token)
	okIK.Header().Set("Idempotency-Key", "ik-001")
	first, err := h.client.CreateLocalUser(context.Background(), okIK)
	require.NoError(t, err)
	retryIK := connect.NewRequest(&authv1.CreateLocalUserRequest{
		Username: "ik-user", Password: "rotated",
	})
	retryIK.Header().Set("Authorization", "Bearer "+token)
	retryIK.Header().Set("Idempotency-Key", "ik-002")
	replayed, err := h.client.CreateLocalUser(context.Background(), retryIK)
	require.NoError(t, err)
	assert.Equal(t, first.Msg.GetUser().GetId(), replayed.Msg.GetUser().GetId())

	// 跨组织：创建者不是目标 org 成员 → PermissionDenied。
	require.NoError(t, h.st.Organizations().Create(context.Background(), &store.Organization{ID: "org-2", Name: "Org 2"}))
	_, err = h.createLocalUser(t, token, "cross-org", "pass", nil, "org-2")
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// AC-072-03 real-path regression: a platform_admin created by Initialize must be
// usable immediately (the Casbin policy is recompiled on membership creation,
// otherwise the seed flow 403s at its first step).
func TestInitialize_AdminImmediatelyAuthorized(t *testing.T) {
	enforcer, st := setupEnforcer(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	jwtManager := NewJWTManager([]byte("test-signing-key"), time.Hour, time.Hour)
	interceptor := NewAuthInterceptor(jwtManager, st, enforcer, map[string]bool{
		authv1connect.AuthServiceInitializeProcedure: true,
		authv1connect.AuthServiceLoginProcedure:      true,
	}, logger)
	mux := http.NewServeMux()
	authPath, authHandler := authv1connect.NewAuthServiceHandler(
		NewAuthService(st, jwtManager, NewRateLimiter(10, time.Minute), logger, enforcer),
		connect.WithInterceptors(interceptor),
	)
	mux.Handle(authPath, authHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := authv1connect.NewAuthServiceClient(server.Client(), server.URL)

	initResp, err := client.Initialize(ctx, connect.NewRequest(&authv1.InitializeRequest{
		Username: "dev-admin", Password: "dev-admin-pass", OrganizationName: "Dev Org",
	}))
	require.NoError(t, err)
	token := initResp.Msg.GetAccessToken()

	// 不等待 policy reloader：Initialize 返回后立即以 admin 身份创建本地用户。
	req := connect.NewRequest(&authv1.CreateLocalUserRequest{
		Username: "dev-deployer", Password: "deployer-pass-1", Roles: []string{"deployer"},
	})
	req.Header().Set("Authorization", "Bearer "+token)
	resp, err := client.CreateLocalUser(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "dev-deployer", resp.Msg.GetUser().GetUsername())
	assert.Equal(t, []string{"deployer"}, resp.Msg.GetUser().GetRoles())
}

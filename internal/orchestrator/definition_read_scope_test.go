package orchestrator

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

// TASK-371: definition reads had no tenant scope. Both read RPCs answered from
// any organization's row, which the probe behind the fix confirmed end to end:
// an org-A viewer got org-B's definition in an empty-filter List and could Get
// it by id. These tests pin the two halves of the fix -- the handler scope and
// the fact that the real auth interceptor leaves the request to the handler --
// plus the positive controls that keep the scoped reads usable.
type definitionScopeFixture struct {
	svc                  *Service
	st                   store.Store
	orgA, orgB           string
	userA, userB         string
	customerA, customerB string
	defA, defB           string
}

// seedDefinitionScope builds two complete tenants. Each organization has a
// viewer, an active binding to its own customer, and one definition under that
// customer; neither organization can reach the other's customer.
func seedDefinitionScope(t *testing.T) definitionScopeFixture {
	t.Helper()
	svc, st, cleanup := setupService(t)
	t.Cleanup(cleanup)

	f := definitionScopeFixture{
		svc: svc, st: st,
		orgA: "org-scope-a", orgB: "org-scope-b",
		userA: "user-scope-a", userB: "user-scope-b",
		customerA: "cust-scope-a", customerB: "cust-scope-b",
		defA: "def-scope-a", defB: "def-scope-b",
	}
	ctx := context.Background()
	for _, tenant := range []struct{ org, user string }{
		{f.orgA, f.userA}, {f.orgB, f.userB},
	} {
		require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: tenant.org, Name: tenant.org}))
		require.NoError(t, st.Users().Create(ctx, &store.User{
			ID: tenant.user, Username: tenant.user, Status: store.UserActive,
		}))
		require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
			OrgID: tenant.org, UserID: tenant.user, Role: store.RoleViewer,
		}))
		// The interceptor test authenticates through the real JWT + session path.
		require.NoError(t, st.AuthSessions().Create(ctx, &store.AuthSession{
			ID: "session-" + tenant.user, UserID: tenant.user, TokenFamily: "family-" + tenant.user,
			RefreshTokenHash: "refresh-" + tenant.user, ExpiresAt: time.Now().UTC().Add(time.Hour),
		}))
	}

	seedCustomer(t, st, f.customerA, "scope-a")
	seedCluster(t, st, "cls-scope-a", f.customerA)
	seedCustomer(t, st, f.customerB, "scope-b")
	seedCluster(t, st, "cls-scope-b", f.customerB)
	require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{
		ID: "binding-scope-a", OrgID: f.orgA, CustomerID: f.customerA,
	}))
	require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{
		ID: "binding-scope-b", OrgID: f.orgB, CustomerID: f.customerB,
	}))

	ownerA, ownerB := f.orgA, f.orgB
	for _, def := range []*store.ReleaseDefinition{
		{
			ID: f.defA, Name: "scope-a", CustomerID: f.customerA, ClusterID: "cls-scope-a",
			Namespace: "ns-a", ReleaseName: "release-a", ChartName: "nginx",
			Status: store.DefStatusActive, CreatedBy: f.userA, OwnerOrganizationID: &ownerA,
		},
		{
			ID: f.defB, Name: "scope-b", CustomerID: f.customerB, ClusterID: "cls-scope-b",
			Namespace: "ns-b", ReleaseName: "release-b", ChartName: "nginx",
			Status: store.DefStatusActive, CreatedBy: f.userB, OwnerOrganizationID: &ownerB,
		},
	} {
		require.NoError(t, st.Definitions().Create(ctx, def, nil))
	}
	return f
}

func (f definitionScopeFixture) actorCtx(userID, orgID string) context.Context {
	return authctx.WithActor(context.Background(), authctx.Actor{
		UserID: userID, OrganizationID: orgID, Roles: []string{string(store.RoleViewer)},
	})
}

func TestListReleaseDefinitions_EmptyFilterIsOrganizationScoped(t *testing.T) {
	f := seedDefinitionScope(t)

	resp, err := f.svc.ListReleaseDefinitions(f.actorCtx(f.userA, f.orgA), connect.NewRequest(
		&orchestratorv1.ListReleaseDefinitionsRequest{IncludeDisabled: true},
	))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetDefinitions(), 1, "an empty filter must return the caller's own tenant only")
	assert.Equal(t, f.defA, resp.Msg.GetDefinitions()[0].GetId())
	assert.Equal(t, f.customerA, resp.Msg.GetDefinitions()[0].GetCustomerId())
}

func TestListReleaseDefinitions_ForeignCustomerFilterReturnsNothing(t *testing.T) {
	f := seedDefinitionScope(t)

	resp, err := f.svc.ListReleaseDefinitions(f.actorCtx(f.userA, f.orgA), connect.NewRequest(
		&orchestratorv1.ListReleaseDefinitionsRequest{CustomerId: f.customerB, IncludeDisabled: true},
	))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetDefinitions(),
		"naming a foreign customer must not escape the organization scope")
}

func TestGetDefinition_CrossOrganizationIsNotFound(t *testing.T) {
	f := seedDefinitionScope(t)

	_, foreignErr := f.svc.GetReleaseDefinition(f.actorCtx(f.userA, f.orgA), connect.NewRequest(
		&orchestratorv1.GetReleaseDefinitionRequest{DefinitionId: f.defB},
	))
	require.Error(t, foreignErr)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(foreignErr))

	_, missingErr := f.svc.GetReleaseDefinition(f.actorCtx(f.userA, f.orgA), connect.NewRequest(
		&orchestratorv1.GetReleaseDefinitionRequest{DefinitionId: "no-such-definition"},
	))
	require.Error(t, missingErr)
	assert.Equal(t, connect.CodeOf(missingErr), connect.CodeOf(foreignErr),
		"an unreadable definition must be indistinguishable from a missing one")
}

func TestGetDefinition_OwnOrganizationSucceeds(t *testing.T) {
	f := seedDefinitionScope(t)

	resp, err := f.svc.GetReleaseDefinition(f.actorCtx(f.userA, f.orgA), connect.NewRequest(
		&orchestratorv1.GetReleaseDefinitionRequest{DefinitionId: f.defA},
	))
	require.NoError(t, err)
	assert.Equal(t, f.customerA, resp.Msg.GetDefinition().GetCustomerId())
}

// TestDefinitionReadScope_ThroughAuthInterceptor stacks the real interceptor
// (JWT + Casbin + binding check) in front of the handler: the handler test alone
// would not prove that no gate above it makes the scope redundant, and the
// exploit this task closes lived exactly in that gap (an empty customer_id skips
// enforceRequestBinding). The explicit-customer subtest is the control: the
// interceptor really does refuse a named foreign customer, so the empty filter
// was the bypass.
func TestDefinitionReadScope_ThroughAuthInterceptor(t *testing.T) {
	f := seedDefinitionScope(t)
	logger := slog.New(slog.DiscardHandler)
	enforcer, err := auth.NewEnforcer(f.st, logger)
	require.NoError(t, err)
	require.NoError(t, enforcer.LoadPolicies(context.Background()))
	t.Cleanup(enforcer.Close)

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	jwtManager := auth.NewJWTManager(privateKey, time.Hour, time.Hour)
	token, _, err := jwtManager.GenerateAccessToken(f.userA, f.orgA, []string{string(store.RoleViewer)})
	require.NoError(t, err)

	mux := http.NewServeMux()
	path, handler := orchestratorv1connect.NewOrchestratorServiceHandler(f.svc,
		connect.WithInterceptors(auth.NewAuthInterceptor(jwtManager, f.st, enforcer, map[string]bool{}, logger)))
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := orchestratorv1connect.NewOrchestratorServiceClient(srv.Client(), srv.URL)

	authed := func() string {
		return "Bearer " + token
	}

	t.Run("empty-filter list is scoped to the caller's organization", func(t *testing.T) {
		req := connect.NewRequest(&orchestratorv1.ListReleaseDefinitionsRequest{IncludeDisabled: true})
		req.Header().Set("Authorization", authed())
		resp, listErr := client.ListReleaseDefinitions(context.Background(), req)
		require.NoError(t, listErr)
		require.Len(t, resp.Msg.GetDefinitions(), 1)
		assert.Equal(t, f.defA, resp.Msg.GetDefinitions()[0].GetId())
	})

	t.Run("foreign definition is not found", func(t *testing.T) {
		req := connect.NewRequest(&orchestratorv1.GetReleaseDefinitionRequest{DefinitionId: f.defB})
		req.Header().Set("Authorization", authed())
		_, getErr := client.GetReleaseDefinition(context.Background(), req)
		require.Error(t, getErr)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(getErr))
	})

	t.Run("explicit foreign customer is refused above the handler", func(t *testing.T) {
		req := connect.NewRequest(&orchestratorv1.ListReleaseDefinitionsRequest{CustomerId: f.customerB})
		req.Header().Set("Authorization", authed())
		_, listErr := client.ListReleaseDefinitions(context.Background(), req)
		require.Error(t, listErr)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(listErr))
	})
}

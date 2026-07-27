package auth

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func setupEnforcer(t *testing.T) (*Enforcer, store.Store) {
	t.Helper()
	st, err := sqlitestore.Open(t.TempDir() + "/auth.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	enforcer, err := NewEnforcer(st, logger)
	require.NoError(t, err)
	return enforcer, st
}

func seedAuthorization(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: "org-1", Name: "Org 1"}))
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: "org-2", Name: "Org 2"}))
	require.NoError(t, st.Users().Create(ctx, &store.User{ID: "user-1", Username: "user-1", PasswordHash: "hash"}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: "org-1", UserID: "user-1", Role: store.RoleViewer,
	}))
}

func TestEnforcer_AuthorizationContracts(t *testing.T) {
	tests := []struct {
		name string
		seed func(*testing.T, store.Store, *Enforcer)
		run  func(context.Context, *Enforcer) error
		want string
	}{
		{
			name: "viewer cannot write",
			seed: func(t *testing.T, st store.Store, _ *Enforcer) { seedAuthorization(t, st) },
			run: func(_ context.Context, e *Enforcer) error {
				return e.Enforce("user-1", "org-1", "organization", "write")
			},
			want: "permission_denied",
		},
		{
			name: "unbound customer is denied",
			seed: func(t *testing.T, st store.Store, _ *Enforcer) { seedAuthorization(t, st) },
			run: func(ctx context.Context, e *Enforcer) error {
				return e.CheckBinding(ctx, "org-1", "customer-1")
			},
			want: "domain_binding_missing",
		},
		{
			name: "service actor cannot cross domain",
			seed: func(t *testing.T, st store.Store, _ *Enforcer) { seedAuthorization(t, st) },
			run: func(ctx context.Context, e *Enforcer) error {
				return e.EnforceServiceActor(ctx, "notifier", "org-2", "release", "read", "customer-1")
			},
			want: "domain_binding_missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, st := setupEnforcer(t)
			tt.seed(t, st, e)
			require.NoError(t, e.LoadPolicies(context.Background()))
			err := tt.run(context.Background(), e)
			require.Error(t, err)
			assert.Equal(t, tt.want, authorizationReason(err))
		})
	}
}

func TestEnforcer_PolicyReloadTakesEffect(t *testing.T) {
	e, st := setupEnforcer(t)
	seedAuthorization(t, st)
	ctx := context.Background()
	require.NoError(t, e.LoadPolicies(ctx))
	require.Error(t, e.Enforce("user-1", "org-1", "organization", "write"))

	member, err := st.OrgMembers().Get(ctx, "org-1", "user-1")
	require.NoError(t, err)
	member.Role = store.RoleReleaseAdmin
	require.NoError(t, st.OrgMembers().Update(ctx, member))
	require.NoError(t, e.LoadPolicies(ctx))
	require.NoError(t, e.Enforce("user-1", "org-1", "organization", "write"))
}

func TestEnforcer_OperatorPermissions(t *testing.T) {
	e, st := setupEnforcer(t)
	ctx := context.Background()
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: "org-operator", Name: "Operator Org"}))
	for _, member := range []*store.OrganizationMember{
		{OrgID: "org-operator", UserID: "viewer-operator", Role: store.RoleViewer},
		{OrgID: "org-operator", UserID: "deployer-operator", Role: store.RoleDeployer},
		{OrgID: "org-operator", UserID: "admin-operator", Role: store.RoleReleaseAdmin},
	} {
		require.NoError(t, st.Users().Create(ctx, &store.User{ID: member.UserID, Username: member.UserID, PasswordHash: "hash"}))
		require.NoError(t, st.OrgMembers().Create(ctx, member))
	}
	require.NoError(t, e.LoadPolicies(ctx))

	for _, userID := range []string{"viewer-operator", "deployer-operator", "admin-operator"} {
		require.NoError(t, e.Enforce(userID, "org-operator", "operator", "read"))
	}
	for _, action := range []string{"enroll", "revoke"} {
		require.Error(t, e.Enforce("viewer-operator", "org-operator", "operator", action))
		require.Error(t, e.Enforce("deployer-operator", "org-operator", "operator", action))
		require.NoError(t, e.Enforce("admin-operator", "org-operator", "operator", action))
	}
}

func TestAuthorizationErrorsExposeReasonCodes(t *testing.T) {
	err := newPermissionDenied("user-1", "org-1", "organization", "write")
	var denied *PermissionDeniedError
	require.True(t, errors.As(err, &denied))
	assert.Equal(t, "permission_denied", denied.AuthorizationReason())
	assert.Equal(t, "org-1", denied.Domain)
}

func TestEnforcer_WriteFailsClosedBeforePolicyLoad(t *testing.T) {
	e, st := setupEnforcer(t)
	seedAuthorization(t, st)
	err := e.Enforce("user-1", "org-1", "organization", "write")
	require.Error(t, err)
	assert.Equal(t, "policy_unavailable", authorizationReason(err))
}

func TestEnforcer_OperatorWritesFailClosedBeforePolicyLoad(t *testing.T) {
	e, st := setupEnforcer(t)
	ctx := context.Background()
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: "org-operator-unhealthy", Name: "Operator Org"}))
	require.NoError(t, st.Users().Create(ctx, &store.User{ID: "admin-operator-unhealthy", Username: "admin-operator-unhealthy", PasswordHash: "hash"}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: "org-operator-unhealthy", UserID: "admin-operator-unhealthy", Role: store.RoleReleaseAdmin}))

	for _, action := range []string{"enroll", "revoke"} {
		err := e.Enforce("admin-operator-unhealthy", "org-operator-unhealthy", "operator", action)
		require.Error(t, err)
		assert.Equal(t, "policy_unavailable", authorizationReason(err))
	}
}

func TestEnforcer_ServiceActorAllowedOnlyInBoundDomain(t *testing.T) {
	e, st := setupEnforcer(t)
	seedAuthorization(t, st)
	ctx := context.Background()
	require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{
		ID: "binding-1", OrgID: "org-1", CustomerID: "customer-1",
	}))
	require.NoError(t, e.LoadPolicies(ctx))
	require.NoError(t, e.AddPolicy("service_actor", "org-1", "customer", "read"))
	require.NoError(t, e.AddServiceActorBinding("notifier", "service_actor", "org-1"))

	require.NoError(t, e.EnforceServiceActor(ctx, "notifier", "org-1", "customer", "read", "customer-1"))
	err := e.EnforceServiceActor(ctx, "notifier", "org-2", "customer", "read", "customer-1")
	require.Error(t, err)
	assert.Equal(t, "domain_binding_missing", authorizationReason(err))
}

func TestEnforcer_ServiceActorWithoutDomainBindingIsDenied(t *testing.T) {
	e, st := setupEnforcer(t)
	seedAuthorization(t, st)
	ctx := context.Background()
	require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{
		ID: "binding-1", OrgID: "org-1", CustomerID: "customer-1",
	}))
	require.NoError(t, e.LoadPolicies(ctx))
	require.NoError(t, e.AddPolicy("service_actor", "org-1", "customer", "read"))

	err := e.EnforceServiceActor(ctx, "notifier", "org-1", "customer", "read", "customer-1")
	require.Error(t, err)
	assert.Equal(t, "permission_denied", authorizationReason(err))
}

func TestEnforcer_PolicyVersionAdvances(t *testing.T) {
	e, st := setupEnforcer(t)
	seedAuthorization(t, st)
	ctx := context.Background()
	require.NoError(t, e.LoadPolicies(ctx))
	first := e.PolicyVersion()
	_, err := e.RefreshPolicies(ctx)
	require.NoError(t, err)
	assert.Greater(t, e.PolicyVersion(), first)
}

package auth

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestAuthorizationServiceSnapshotAndCapabilityLifecycle(t *testing.T) {
	const (
		orgID      = "7224a4ec-94ed-43c6-afbd-20b3e585f2b4"
		customerID = "0872085f-a514-414d-afd6-0024e1b86fa0"
		adminID    = "a0110954-1930-4c49-9483-3bb1b5cb8422"
		deployerID = "09eeac92-5c4a-45cb-938a-71ee84d60cff"
	)
	ctx := context.Background()
	st := sqlitestore.OpenTest(t)
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: customerID, Name: "Customer", Slug: "customer"}))
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: "Organization"}))
	require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{ID: "binding", OrgID: orgID, CustomerID: customerID}))
	for userID, role := range map[string]store.Role{adminID: store.RolePlatformAdmin, deployerID: store.RoleDeployer} {
		require.NoError(t, st.Users().Create(ctx, &store.User{ID: userID, Username: userID, Status: store.UserActive}))
		require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: orgID, UserID: userID, Role: role}))
	}
	enforcer, err := NewEnforcer(st, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NoError(t, enforcer.LoadPolicies(ctx))
	svc := NewAuthorizationService(st, enforcer, nil, slog.New(slog.DiscardHandler))

	deployerCtx := authctx.WithActor(ctx, authctx.Actor{UserID: deployerID, OrganizationID: orgID})
	snapshotRequest := connect.NewRequest(&authv1.GetAuthorizationSnapshotRequest{OrganizationId: orgID, CustomerId: customerID})
	before, err := svc.GetAuthorizationSnapshot(deployerCtx, snapshotRequest)
	require.NoError(t, err)
	assert.False(t, before.Msg.GetCanExecuteEmergency())
	assert.True(t, before.Msg.GetCanCreateValuesRevision())
	// AC-079-07 / D6: emergencyChangeEnabled kill switch projection — missing
	// config fails closed to false.
	assert.False(t, before.Msg.GetEmergencyChangeEnabled())

	require.NoError(t, st.EmergencyConfig().SetEmergencyConfig(ctx, store.EmergencyConfig{Enabled: true}))
	enabledSnap, err := svc.GetAuthorizationSnapshot(deployerCtx, snapshotRequest)
	require.NoError(t, err)
	assert.True(t, enabledSnap.Msg.GetEmergencyChangeEnabled())

	adminCtx := authctx.WithActor(ctx, authctx.Actor{UserID: adminID, OrganizationID: orgID})
	grantRequest := connect.NewRequest(&authv1.SetCapabilityGrantRequest{
		OrganizationId: orgID,
		Subject:        deployerID,
		Action:         string(store.AuthorizationExecuteEmergency),
	})
	granted, err := svc.SetCapabilityGrant(adminCtx, grantRequest)
	require.NoError(t, err)
	assert.Greater(t, granted.Msg.GetSourceVersion(), before.Msg.GetSourceVersion())
	afterGrant, err := svc.GetAuthorizationSnapshot(deployerCtx, snapshotRequest)
	require.NoError(t, err)
	assert.True(t, afterGrant.Msg.GetCanExecuteEmergency())

	grantRequest.Msg.Revoked = true
	_, err = svc.SetCapabilityGrant(adminCtx, grantRequest)
	require.NoError(t, err)
	afterRevoke, err := svc.GetAuthorizationSnapshot(deployerCtx, snapshotRequest)
	require.NoError(t, err)
	assert.False(t, afterRevoke.Msg.GetCanExecuteEmergency())
}

func TestAuthorizationServiceRejectsClientActorAndLeaksNoScopeDetails(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	enforcer, err := NewEnforcer(st, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	svc := NewAuthorizationService(st, enforcer, nil, slog.New(slog.DiscardHandler))

	_, err = svc.GetAuthorizationSnapshot(context.Background(), connect.NewRequest(&authv1.GetAuthorizationSnapshotRequest{
		OrganizationId: "7224a4ec-94ed-43c6-afbd-20b3e585f2b4",
		CustomerId:     "0872085f-a514-414d-afd6-0024e1b86fa0",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	var connectErr *connect.Error
	require.True(t, errors.As(err, &connectErr))
	assert.Equal(t, "INVALID_ACTOR_CONTEXT", connectErr.Meta().Get("X-Reason-Code"))
	assert.NotContains(t, err.Error(), "membership")
	assert.NotContains(t, err.Error(), "binding")
	assert.NotContains(t, err.Error(), "capability")
}

package auth

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// TestAuthInterceptor_PreviouslyUnmappedProcedures is the integration seam for
// the procedures the prefix table left permanently permission_denied:
// CheckEmergencyConflict, TriggerInventorySync, RecordArtifactEvent (empty
// action) and SetCapabilityGrant (empty action behind the handler-authorization
// table). Each case drives the real interceptor with a real JWT and asserts the
// role matrix that the explicit registry now encodes.
func TestAuthInterceptor_PreviouslyUnmappedProcedures(t *testing.T) {
	ctx := context.Background()
	st, err := sqlitestore.Open(t.TempDir() + "/auth-unmapped.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	const (
		organizationID = "org-unmapped"
		customerID     = "customer-unmapped"
		platformAdmin  = "user-platform-admin"
		releaseAdmin   = "user-release-admin"
		viewer         = "user-viewer"
	)
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: organizationID, Name: "Unmapped Team"}))
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: customerID, Name: "Unmapped Customer", Slug: "unmapped-customer"}))
	require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{
		ID: "binding-unmapped", OrgID: organizationID, CustomerID: customerID,
	}))
	for _, user := range []struct {
		id   string
		role store.Role
	}{
		{id: platformAdmin, role: store.RolePlatformAdmin},
		{id: releaseAdmin, role: store.RoleReleaseAdmin},
		{id: viewer, role: store.RoleViewer},
	} {
		require.NoError(t, st.Users().Create(ctx, &store.User{ID: user.id, Username: user.id, PasswordHash: "unused"}))
		require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
			OrgID: organizationID, UserID: user.id, Role: user.role,
		}))
		require.NoError(t, st.AuthSessions().Create(ctx, &store.AuthSession{
			ID: user.id + "-session", UserID: user.id, TokenFamily: user.id + "-family",
			RefreshTokenHash: user.id + "-refresh", ExpiresAt: time.Now().UTC().Add(time.Hour),
		}))
	}

	logger := slog.New(slog.DiscardHandler)
	enforcer, err := NewEnforcer(st, logger)
	require.NoError(t, err)
	require.NoError(t, enforcer.LoadPolicies(ctx))
	jwtManager := NewJWTManager(testJWTPrivateKey, time.Hour, time.Hour)
	interceptor := NewAuthInterceptor(jwtManager, st, enforcer, map[string]bool{}, logger)
	call := interceptor(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&orchestratorv1.CheckEmergencyConflictResponse{}), nil
	})

	token := func(t *testing.T, userID string, role store.Role) string {
		t.Helper()
		value, _, err := jwtManager.GenerateAccessToken(userID, organizationID, []string{string(role)})
		require.NoError(t, err)
		return value
	}

	tests := []struct {
		name      string
		request   func() connect.AnyRequest
		userID    string
		role      store.Role
		wantCode  connect.Code
		wantReach bool
	}{
		{
			name: "CheckEmergencyConflict allows a viewer read",
			request: func() connect.AnyRequest {
				return &checkEmergencyConflictRequest{Request: connect.NewRequest(
					&orchestratorv1.CheckEmergencyConflictRequest{ReleaseDefinitionId: "def-1"})}
			},
			userID: viewer, role: store.RoleViewer, wantReach: true,
		},
		{
			name: "TriggerInventorySync denies a viewer write",
			request: func() connect.AnyRequest {
				return &triggerInventorySyncRequest{Request: connect.NewRequest(
					&orchestratorv1.TriggerInventorySyncRequest{CustomerId: customerID, ClusterId: "cluster-1"})}
			},
			userID: viewer, role: store.RoleViewer, wantCode: connect.CodePermissionDenied,
		},
		{
			name: "TriggerInventorySync allows a release admin",
			request: func() connect.AnyRequest {
				return &triggerInventorySyncRequest{Request: connect.NewRequest(
					&orchestratorv1.TriggerInventorySyncRequest{CustomerId: customerID, ClusterId: "cluster-1"})}
			},
			userID: releaseAdmin, role: store.RoleReleaseAdmin, wantReach: true,
		},
		{
			name: "RecordArtifactEvent stays platform-admin-only",
			request: func() connect.AnyRequest {
				return &recordArtifactEventRequest{Request: connect.NewRequest(
					&orchestratorv1.RecordArtifactEventRequest{SourceId: "harbor", EventId: "event-1"})}
			},
			userID: releaseAdmin, role: store.RoleReleaseAdmin, wantCode: connect.CodePermissionDenied,
		},
		{
			name: "RecordArtifactEvent allows a platform admin",
			request: func() connect.AnyRequest {
				return &recordArtifactEventRequest{Request: connect.NewRequest(
					&orchestratorv1.RecordArtifactEventRequest{SourceId: "harbor", EventId: "event-1"})}
			},
			userID: platformAdmin, role: store.RolePlatformAdmin, wantReach: true,
		},
		{
			name: "SetCapabilityGrant reaches the handler authorization",
			request: func() connect.AnyRequest {
				return &setCapabilityGrantRequest{Request: connect.NewRequest(
					&authv1.SetCapabilityGrantRequest{OrganizationId: organizationID, Subject: viewer})}
			},
			userID: releaseAdmin, role: store.RoleReleaseAdmin, wantReach: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := tt.request()
			request.Header().Set("Authorization", "Bearer "+token(t, tt.userID, tt.role))
			_, err := call(ctx, request)
			if tt.wantReach {
				require.NoError(t, err, "procedure must not be rejected as unmapped or unauthorized")
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.wantCode, connect.CodeOf(err))
			if connect.CodeOf(err) == connect.CodePermissionDenied {
				assert.NotEqual(t, "invalid_actor_context", reasonFromConnectError(t, err),
					"the procedure must be mapped, not rejected for a missing action")
			}
		})
	}
}

type checkEmergencyConflictRequest struct {
	*connect.Request[orchestratorv1.CheckEmergencyConflictRequest]
}

func (r *checkEmergencyConflictRequest) Spec() connect.Spec {
	return connect.Spec{
		Procedure:  orchestratorv1connect.OrchestratorServiceCheckEmergencyConflictProcedure,
		StreamType: connect.StreamTypeUnary,
	}
}

type triggerInventorySyncRequest struct {
	*connect.Request[orchestratorv1.TriggerInventorySyncRequest]
}

func (r *triggerInventorySyncRequest) Spec() connect.Spec {
	return connect.Spec{
		Procedure:  orchestratorv1connect.OrchestratorServiceTriggerInventorySyncProcedure,
		StreamType: connect.StreamTypeUnary,
	}
}

type recordArtifactEventRequest struct {
	*connect.Request[orchestratorv1.RecordArtifactEventRequest]
}

func (r *recordArtifactEventRequest) Spec() connect.Spec {
	return connect.Spec{
		Procedure:  orchestratorv1connect.BundleServiceRecordArtifactEventProcedure,
		StreamType: connect.StreamTypeUnary,
	}
}

type setCapabilityGrantRequest struct {
	*connect.Request[authv1.SetCapabilityGrantRequest]
}

func (r *setCapabilityGrantRequest) Spec() connect.Spec {
	return connect.Spec{
		Procedure:  authv1connect.AuthorizationServiceSetCapabilityGrantProcedure,
		StreamType: connect.StreamTypeUnary,
	}
}

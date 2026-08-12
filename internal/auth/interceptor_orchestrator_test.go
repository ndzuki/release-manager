package auth

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestAuthInterceptor_OrchestratorEmergencyChange(t *testing.T) {
	ctx := context.Background()
	st, err := sqlitestore.Open(t.TempDir() + "/auth.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	const (
		organizationID = "org-release"
		allowedUserID  = "user-release-admin"
		viewerUserID   = "user-viewer"
	)
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{
		ID:   organizationID,
		Name: "Release Team",
	}))
	for _, userID := range []string{allowedUserID, viewerUserID} {
		require.NoError(t, st.Users().Create(ctx, &store.User{
			ID:           userID,
			Username:     userID,
			PasswordHash: "unused",
		}))
		require.NoError(t, st.AuthSessions().Create(ctx, &store.AuthSession{
			ID: userID + "-session", UserID: userID, TokenFamily: userID + "-family",
			RefreshTokenHash: userID + "-refresh", ExpiresAt: time.Now().UTC().Add(time.Hour),
		}))
	}
	for _, member := range []*store.OrganizationMember{
		{OrgID: organizationID, UserID: allowedUserID, Role: store.RoleReleaseAdmin},
		{OrgID: organizationID, UserID: viewerUserID, Role: store.RoleViewer},
	} {
		require.NoError(t, st.OrgMembers().Create(ctx, member))
	}
	for _, userID := range []string{allowedUserID, viewerUserID} {
		require.NoError(t, st.AuthSessions().Create(ctx, &store.AuthSession{
			ID:               "session-" + userID,
			UserID:           userID,
			TokenFamily:      "family-" + userID,
			RefreshTokenHash: "refresh-" + userID,
			ExpiresAt:        time.Now().UTC().Add(time.Hour),
		}))
	}

	logger := slog.New(slog.DiscardHandler)
	enforcer, err := NewEnforcer(st, logger)
	require.NoError(t, err)
	require.NoError(t, enforcer.LoadPolicies(ctx))
	jwtManager := NewJWTManager([]byte("test-signing-key"), time.Hour, time.Hour)
	interceptor := NewAuthInterceptor(jwtManager, st, enforcer, map[string]bool{}, logger)
	call := interceptor(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&orchestratorv1.EmergencyChangeResponse{}), nil
	})
	newRequest := func() connect.AnyRequest {
		return &orchestratorRequest{
			Request: connect.NewRequest(&orchestratorv1.EmergencyChangeRequest{}),
		}
	}

	t.Run("missing token", func(t *testing.T) {
		_, err := call(ctx, newRequest())
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("viewer denied", func(t *testing.T) {
		token, _, err := jwtManager.GenerateAccessToken(
			viewerUserID,
			organizationID,
			[]string{string(store.RoleViewer)},
		)
		require.NoError(t, err)
		request := newRequest()
		request.Header().Set("Authorization", "Bearer "+token)
		_, err = call(ctx, request)
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})

	t.Run("release admin allowed", func(t *testing.T) {
		token, _, err := jwtManager.GenerateAccessToken(
			allowedUserID,
			organizationID,
			[]string{string(store.RoleReleaseAdmin)},
		)
		require.NoError(t, err)
		request := newRequest()
		request.Header().Set("Authorization", "Bearer "+token)
		_, err = call(ctx, request)
		require.NoError(t, err)
	})

	t.Run("release admin cookie write rejected without csrf", func(t *testing.T) {
		token, _, err := jwtManager.GenerateAccessToken(
			allowedUserID,
			organizationID,
			[]string{string(store.RoleReleaseAdmin)},
		)
		require.NoError(t, err)
		request := newRequest()
		request.Header().Set("Cookie", authCookieHeader(AccessCookieName, token))
		_, err = call(ctx, request)
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})

	t.Run("release admin cookie write allowed with csrf", func(t *testing.T) {
		token, _, err := jwtManager.GenerateAccessToken(
			allowedUserID,
			organizationID,
			[]string{string(store.RoleReleaseAdmin)},
		)
		require.NoError(t, err)
		request := newRequest()
		request.Header().Set("Cookie", authCookieHeader(AccessCookieName, token)+"; "+authCookieHeader(CSRFCookieName, "csrf-token"))
		request.Header().Set(CSRFHeaderName, "csrf-token")
		_, err = call(ctx, request)
		require.NoError(t, err)
	})
}

func authCookieHeader(name, value string) string {
	return name + "=" + value
}

func TestAuthInterceptor_OperatorWritePermission(t *testing.T) {
	ctx := context.Background()
	st, err := sqlitestore.Open(t.TempDir() + "/auth-operator.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	const (
		organizationID = "org-operator"
		customerID     = "customer-operator"
		clusterID      = "cluster-operator"
		adminUserID    = "user-operator-admin"
		viewerUserID   = "user-operator-viewer"
	)
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: organizationID, Name: "Operator Team"}))
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: customerID, Name: "Operator Customer", Slug: "operator-customer"}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: clusterID, Name: "Operator Cluster", CustomerID: customerID}))
	require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{ID: "binding-operator", OrgID: organizationID, CustomerID: customerID}))
	for _, user := range []struct {
		id   string
		role store.Role
	}{
		{id: adminUserID, role: store.RoleReleaseAdmin},
		{id: viewerUserID, role: store.RoleViewer},
	} {
		require.NoError(t, st.Users().Create(ctx, &store.User{ID: user.id, Username: user.id, PasswordHash: "unused"}))
		require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: organizationID, UserID: user.id, Role: user.role}))
		require.NoError(t, st.AuthSessions().Create(ctx, &store.AuthSession{
			ID: user.id + "-session", UserID: user.id, TokenFamily: user.id + "-family",
			RefreshTokenHash: user.id + "-refresh", ExpiresAt: time.Now().UTC().Add(time.Hour),
		}))
	}

	logger := slog.New(slog.DiscardHandler)
	enforcer, err := NewEnforcer(st, logger)
	require.NoError(t, err)
	require.NoError(t, enforcer.LoadPolicies(ctx))
	jwtManager := NewJWTManager([]byte("operator-signing-key"), time.Hour, time.Hour)
	interceptor := NewAuthInterceptor(jwtManager, st, enforcer, map[string]bool{}, logger)
	call := interceptor(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&orchestratorv1.CreateEnrollmentTokenResponse{}), nil
	})

	newRequest := func() connect.AnyRequest {
		return &operatorWriteRequest{Request: connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
			CustomerId: customerID, ClusterId: clusterID, OperatorName: "operator-one",
		})}
	}
	for _, test := range []struct {
		name     string
		userID   string
		role     store.Role
		wantCode connect.Code
	}{
		{name: "viewer denied", userID: viewerUserID, role: store.RoleViewer, wantCode: connect.CodePermissionDenied},
		{name: "release admin allowed", userID: adminUserID, role: store.RoleReleaseAdmin},
	} {
		t.Run(test.name, func(t *testing.T) {
			token, _, err := jwtManager.GenerateAccessToken(test.userID, organizationID, []string{string(test.role)})
			require.NoError(t, err)
			request := newRequest()
			request.Header().Set("Authorization", "Bearer "+token)
			_, err = call(ctx, request)
			if test.wantCode == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, test.wantCode, connect.CodeOf(err))
		})
	}
}

type operatorWriteRequest struct {
	*connect.Request[orchestratorv1.CreateEnrollmentTokenRequest]
}

func (r *operatorWriteRequest) Spec() connect.Spec {
	return connect.Spec{
		Procedure:  orchestratorv1connect.OrchestratorServiceCreateEnrollmentTokenProcedure,
		StreamType: connect.StreamTypeUnary,
	}
}

type orchestratorRequest struct {
	*connect.Request[orchestratorv1.EmergencyChangeRequest]
}

func (r *orchestratorRequest) Spec() connect.Spec {
	return connect.Spec{
		Procedure:  orchestratorv1connect.OrchestratorServiceEmergencyChangeProcedure,
		StreamType: connect.StreamTypeUnary,
	}
}

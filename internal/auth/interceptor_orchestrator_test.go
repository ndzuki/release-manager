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

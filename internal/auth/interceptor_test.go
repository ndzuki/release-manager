package auth

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
)

func TestAuthInterceptor_HTTPContracts(t *testing.T) {
	tests := []struct {
		name       string
		request    *authv1.UpdateOrganizationRequest
		wantCode   connect.Code
		wantReason string
	}{
		{
			name:       "viewer write denied",
			request:    &authv1.UpdateOrganizationRequest{OrgId: "org-1", Name: "renamed"},
			wantCode:   connect.CodePermissionDenied,
			wantReason: "permission_denied",
		},
		{
			name:       "token cannot switch organization",
			request:    &authv1.UpdateOrganizationRequest{OrgId: "org-2", Name: "renamed"},
			wantCode:   connect.CodePermissionDenied,
			wantReason: "permission_denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, st := setupEnforcer(t)
			seedAuthorization(t, st)
			require.NoError(t, e.LoadPolicies(context.Background()))

			jwtManager := NewJWTManager([]byte("test-signing-key"), time.Hour, time.Hour)
			token, _, err := jwtManager.GenerateAccessToken("user-1", "org-1", []string{"viewer"})
			require.NoError(t, err)

			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
			service := NewOrgService(st, logger)
			path, handler := authv1connect.NewOrganizationServiceHandler(
				service,
				connect.WithInterceptors(NewAuthInterceptor(jwtManager, nil, e, map[string]bool{}, logger)),
			)
			mux := http.NewServeMux()
			mux.Handle(path, handler)
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)

			client := authv1connect.NewOrganizationServiceClient(server.Client(), server.URL)
			req := connect.NewRequest(tt.request)
			req.Header().Set("Authorization", "Bearer "+token)
			_, err = client.UpdateOrganization(context.Background(), req)
			require.Error(t, err)
			assert.Equal(t, tt.wantCode, connect.CodeOf(err))
			assert.Equal(t, tt.wantReason, reasonFromConnectError(t, err))
			assert.NotEmpty(t, policyVersionFromConnectError(t, err))
		})
	}
}

func TestAuthInterceptor_BindingMissing(t *testing.T) {
	e, st := setupEnforcer(t)
	seedAuthorization(t, st)
	ctx := context.Background()
	require.NoError(t, e.LoadPolicies(ctx))

	jwtManager := NewJWTManager([]byte("test-signing-key"), time.Hour, time.Hour)
	token, _, err := jwtManager.GenerateAccessToken("user-1", "org-1", []string{"viewer"})
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	service := NewBindingService(st, StubResolver{}, logger)
	path, handler := authv1connect.NewBindingServiceHandler(
		service,
		connect.WithInterceptors(NewAuthInterceptor(jwtManager, nil, e, map[string]bool{}, logger)),
	)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := authv1connect.NewBindingServiceClient(server.Client(), server.URL)
	req := connect.NewRequest(&authv1.GetBindingRequest{BindingId: "missing-binding"})
	req.Header().Set("Authorization", "Bearer "+token)
	_, err = client.GetBinding(ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Equal(t, "domain_binding_missing", reasonFromConnectError(t, err))
	assert.NotEmpty(t, policyVersionFromConnectError(t, err))
}

func TestMapProcedure(t *testing.T) {
	t.Run("organization update", func(t *testing.T) {
		object, action := mapProcedure(authv1connect.OrganizationServiceUpdateOrganizationProcedure)
		assert.Equal(t, "organization", object)
		assert.Equal(t, "write", action)
	})

	t.Run("orchestrator get operation", func(t *testing.T) {
		object, action := mapProcedure(orchestratorv1connect.OrchestratorServiceGetOperationProcedure)
		assert.Equal(t, "release", object)
		assert.Equal(t, "read", action)
	})

	t.Run("orchestrator cancel operation", func(t *testing.T) {
		object, action := mapProcedure(orchestratorv1connect.OrchestratorServiceCancelOperationProcedure)
		assert.Equal(t, "release", object)
		assert.Equal(t, "write", action)
	})
}

func TestMapMethodToActionClassifiesValuesApprovalAsWrite(t *testing.T) {
	assert.Equal(t, "write", mapMethodToAction("ApproveValuesRevision"))
	assert.Equal(t, "write", mapMethodToAction("RejectValuesRevision"))
	assert.Equal(t, "read", mapMethodToAction("ListSecrets"))
}

func reasonFromConnectError(t *testing.T, err error) string {
	t.Helper()
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	return connectErr.Meta().Get("X-Reason-Code")
}

func policyVersionFromConnectError(t *testing.T, err error) string {
	t.Helper()
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	return connectErr.Meta().Get("X-Policy-Version")
}

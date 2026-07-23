package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestOrchestratorValuesApprovalEndToEnd(t *testing.T) {
	const signingKey = "test-signing-key"
	ctx := context.Background()
	dbPath := t.TempDir() + "/orchestrator.db"
	seedStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)

	const (
		organizationID   = "org-068-smoke"
		customerID       = "customer-068-smoke"
		definitionID     = "definition-068-smoke"
		revisionID       = "revision-068-smoke"
		viewerRevisionID = "revision-068-viewer"
		creatorID        = "creator-068-smoke"
		approverID       = "approver-068-smoke"
		viewerID         = "viewer-068-smoke"
	)
	ownerOrganizationID := organizationID
	require.NoError(t, seedStore.Customers().Create(ctx, &store.Customer{
		ID: customerID, Name: "Customer 068 Smoke", Slug: "customer-068-smoke",
	}))
	require.NoError(t, seedStore.Organizations().Create(ctx, &store.Organization{
		ID: organizationID, Name: "Organization 068 Smoke",
	}))
	require.NoError(t, seedStore.Bindings().Create(ctx, &store.OrgCustomerBinding{
		ID: "binding-068-smoke", OrgID: organizationID, CustomerID: customerID,
	}))
	for userID, role := range map[string]store.Role{
		creatorID:  store.RoleDeployer,
		approverID: store.RoleReleaseAdmin,
		viewerID:   store.RoleViewer,
	} {
		require.NoError(t, seedStore.Users().Create(ctx, &store.User{
			ID: userID, Username: userID, PasswordHash: "unused",
		}))
		require.NoError(t, seedStore.OrgMembers().Create(ctx, &store.OrganizationMember{
			OrgID: organizationID, UserID: userID, Role: role,
		}))
		require.NoError(t, seedStore.AuthSessions().Create(ctx, &store.AuthSession{
			ID: uuid.NewString(), UserID: userID, TokenFamily: uuid.NewString(),
			RefreshTokenHash: uuid.NewString(), ExpiresAt: time.Now().UTC().Add(time.Hour),
		}))
	}
	require.NoError(t, seedStore.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: definitionID, Name: "definition-068-smoke", CustomerID: customerID,
		ClusterID: "cluster-068-smoke", ReleaseName: "release-068-smoke",
		Status: store.DefStatusActive, OwnerOrganizationID: &ownerOrganizationID,
	}, nil))
	require.NoError(t, seedStore.Values().Create(ctx, &store.ValuesRevision{
		ID: revisionID, ReleaseDefinitionID: definitionID, Revision: 1,
		StateVersion: 1, Status: store.ValuesStatusDraft, Values: []byte(`{"replicas":1}`),
		Digest: "sha256:068-smoke", CreatedByUserID: creatorID,
	}))
	require.NoError(t, seedStore.Values().Create(ctx, &store.ValuesRevision{
		ID: viewerRevisionID, ReleaseDefinitionID: definitionID, Revision: 2,
		StateVersion: 1, Status: store.ValuesStatusDraft, Values: []byte(`{"replicas":2}`),
		Digest: "sha256:068-viewer", CreatedByUserID: viewerID,
	}))
	require.NoError(t, seedStore.Close())

	mux := http.NewServeMux()
	svc := &orchSvc{dbPath: dbPath, targetEnv: "staging", signingKey: signingKey}
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	jwtManager := auth.NewJWTManager([]byte(signingKey), time.Hour, time.Hour)
	creatorToken, _, err := jwtManager.GenerateAccessToken(
		creatorID, organizationID, []string{string(store.RoleDeployer)},
	)
	require.NoError(t, err)
	approverToken, _, err := jwtManager.GenerateAccessToken(
		approverID, organizationID, []string{string(store.RoleReleaseAdmin)},
	)
	require.NoError(t, err)
	viewerToken, _, err := jwtManager.GenerateAccessToken(
		viewerID, organizationID, []string{string(store.RoleViewer)},
	)
	require.NoError(t, err)
	client := orchestratorv1connect.NewOrchestratorServiceClient(http.DefaultClient, server.URL)
	viewerRequest := connect.NewRequest(&orchestratorv1.SubmitValuesRevisionRequest{
		RevisionId: viewerRevisionID, ExpectedStateVersion: 1, Comment: "ready",
	})
	viewerRequest.Header().Set("Authorization", "Bearer "+viewerToken)
	viewerRequest.Header().Set("Idempotency-Key", "submit-068-viewer")
	_, err = client.SubmitValuesRevision(ctx, viewerRequest)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.ErrorContains(t, err, "insufficient for submit")
	viewerAudit, err := svc.st.ValuesApprovalEvidence().ListAuditOutbox(ctx, viewerRevisionID)
	require.NoError(t, err)
	require.Len(t, viewerAudit, 1)
	viewerRevision, err := svc.st.Values().Get(ctx, viewerRevisionID)
	require.NoError(t, err)
	assert.Equal(t, store.ValuesStatusDraft, viewerRevision.Status)

	submitRequest := connect.NewRequest(&orchestratorv1.SubmitValuesRevisionRequest{
		RevisionId: revisionID, ExpectedStateVersion: 1, Comment: "ready",
	})
	submitRequest.Header().Set("Authorization", "Bearer "+creatorToken)
	submitRequest.Header().Set("Idempotency-Key", "submit-068-smoke")
	submitResponse, err := client.SubmitValuesRevision(ctx, submitRequest)
	require.NoErrorf(t, err, "submit values revision failed: code=%s err=%v", connect.CodeOf(err), err)
	assert.Equal(t, int64(2), submitResponse.Msg.GetRevision().GetStateVersion())

	approveRequest := connect.NewRequest(&orchestratorv1.ApproveValuesRevisionRequest{
		RevisionId: revisionID, ExpectedStateVersion: 2, Comment: "approved",
	})
	approveRequest.Header().Set("Authorization", "Bearer "+approverToken)
	approveRequest.Header().Set("Idempotency-Key", "approve-068-smoke")
	approveResponse, err := client.ApproveValuesRevision(ctx, approveRequest)
	require.NoErrorf(t, err, "approve values revision failed: code=%s err=%v", connect.CodeOf(err), err)
	assert.Equal(t, int64(3), approveResponse.Msg.GetRevision().GetStateVersion())
	assert.Equal(t, "VALUES_STATUS_APPROVED", approveResponse.Msg.GetNewState().String())

	legacyRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/values-revisions/"+revisionID+"/approve", http.NoBody)
	require.NoError(t, err)
	legacyResponse, err := http.DefaultClient.Do(legacyRequest)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, legacyResponse.Body.Close()) })
	assert.Equal(t, http.StatusNotFound, legacyResponse.StatusCode)
}

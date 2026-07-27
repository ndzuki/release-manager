package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/operator"
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

func TestOperatorManagementEndToEnd(t *testing.T) {
	const signingKey = "operator-smoke-signing-key"
	ctx := context.Background()
	dbPath := t.TempDir() + "/operator-smoke.db"
	seedStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	const (
		organizationID = "org-053-smoke"
		customerID     = "customer-053-smoke"
		clusterID      = "cluster-053-smoke"
		adminID        = "admin-053-smoke"
		viewerID       = "viewer-053-smoke"
	)
	require.NoError(t, seedStore.Organizations().Create(ctx, &store.Organization{ID: organizationID, Name: "Operator Smoke Org"}))
	require.NoError(t, seedStore.Customers().Create(ctx, &store.Customer{ID: customerID, Name: "Operator Smoke Customer", Slug: customerID}))
	require.NoError(t, seedStore.Clusters().Create(ctx, &store.Cluster{ID: clusterID, Name: "Operator Smoke Cluster", CustomerID: customerID}))
	require.NoError(t, seedStore.Bindings().Create(ctx, &store.OrgCustomerBinding{ID: "binding-053-smoke", OrgID: organizationID, CustomerID: customerID}))
	for userID, role := range map[string]store.Role{adminID: store.RoleReleaseAdmin, viewerID: store.RoleViewer} {
		require.NoError(t, seedStore.Users().Create(ctx, &store.User{ID: userID, Username: userID, PasswordHash: "unused"}))
		require.NoError(t, seedStore.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: organizationID, UserID: userID, Role: role}))
		require.NoError(t, seedStore.AuthSessions().Create(ctx, &store.AuthSession{
			ID: uuid.NewString(), UserID: userID, TokenFamily: uuid.NewString(), RefreshTokenHash: uuid.NewString(), ExpiresAt: time.Now().UTC().Add(time.Hour),
		}))
	}
	require.NoError(t, seedStore.Close())

	operatorMux := http.NewServeMux()
	operatorStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	operatorService, err := operator.NewService(operatorStore, slog.New(slog.DiscardHandler), operator.NewStreamRegistry())
	require.NoError(t, err)
	operatorPath, operatorHandler := operatorv1connect.NewOperatorServiceHandler(operatorService)
	operatorMux.Handle(operatorPath, operatorHandler)
	operatorServer := httptest.NewServer(operatorMux)
	t.Cleanup(operatorServer.Close)
	t.Cleanup(func() { require.NoError(t, operatorStore.Close()) })

	mux := http.NewServeMux()
	svc := &orchSvc{dbPath: dbPath, targetEnv: "staging", signingKey: signingKey, operatorEndpoint: operatorServer.URL}
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	jwtManager := auth.NewJWTManager([]byte(signingKey), time.Hour, time.Hour)
	adminToken, _, err := jwtManager.GenerateAccessToken(adminID, organizationID, []string{string(store.RoleReleaseAdmin)})
	require.NoError(t, err)
	viewerToken, _, err := jwtManager.GenerateAccessToken(viewerID, organizationID, []string{string(store.RoleViewer)})
	require.NoError(t, err)
	client := orchestratorv1connect.NewOrchestratorServiceClient(http.DefaultClient, server.URL)

	viewerCreate := connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
		CustomerId: customerID, ClusterId: clusterID, OperatorName: "operator-smoke", TtlMinutes: 5,
	})
	viewerCreate.Header().Set("Authorization", "Bearer "+viewerToken)
	_, err = client.CreateEnrollmentToken(ctx, viewerCreate)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	viewerList := connect.NewRequest(&orchestratorv1.ListOperatorsRequest{CustomerId: customerID, ClusterId: clusterID})
	viewerList.Header().Set("Authorization", "Bearer "+viewerToken)
	viewerPage, err := client.ListOperators(ctx, viewerList)
	require.NoError(t, err)
	assert.Empty(t, viewerPage.Msg.GetOperators())
	createRequest := connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
		CustomerId: customerID, ClusterId: clusterID, OperatorName: "operator-smoke", TtlMinutes: 5,
	})
	createRequest.Header().Set("Authorization", "Bearer "+adminToken)
	created, err := client.CreateEnrollmentToken(ctx, createRequest)
	require.NoError(t, err)
	plaintext := created.Msg.GetToken()
	assert.NotEmpty(t, plaintext)
	assert.Equal(t, operatorServer.URL, created.Msg.GetOperatorEndpoint())
	assert.Contains(t, created.Msg.GetInstallCommandTemplate(), "${ENROLLMENT_TOKEN}")
	assert.NotContains(t, created.Msg.GetInstallCommandTemplate(), plaintext)

	statusRequest := connect.NewRequest(&orchestratorv1.GetEnrollmentTokenStatusRequest{CustomerId: customerID, ClusterId: clusterID})
	statusRequest.Header().Set("Authorization", "Bearer "+adminToken)
	status, err := client.GetEnrollmentTokenStatus(ctx, statusRequest)
	require.NoError(t, err)
	assert.Equal(t, orchestratorv1.EnrollmentTokenState_ENROLLMENT_TOKEN_STATE_PENDING, status.Msg.GetStatus().GetState())
	assert.NotContains(t, status.Msg.String(), plaintext)

	replaceRequest := connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
		CustomerId: customerID, ClusterId: clusterID, OperatorName: "operator-smoke", TtlMinutes: 5, ReplacePendingToken: true,
	})
	replaceRequest.Header().Set("Authorization", "Bearer "+adminToken)
	replaced, err := client.CreateEnrollmentToken(ctx, replaceRequest)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, replaced.Msg.GetToken())
	assert.NotContains(t, replaced.Msg.String(), plaintext)

	csr := generateOperatorSmokeCSR(t, "operator-smoke", customerID, clusterID)
	operatorClient := operatorv1connect.NewOperatorServiceClient(http.DefaultClient, operatorServer.URL)
	enrolled, err := operatorClient.Enroll(ctx, connect.NewRequest(&operatorv1.EnrollRequest{
		EnrollmentToken: replaced.Msg.GetToken(),
		CustomerId:      customerID,
		ClusterId:       clusterID,
		OperatorId:      "operator-053-enrolled",
		CsrPem:          csr,
		Capabilities:    map[string]string{"helm": "true"},
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, enrolled.Msg.GetSessionId())

	historyRequest := connect.NewRequest(&orchestratorv1.ListOperatorsRequest{CustomerId: customerID, ClusterId: clusterID})
	historyRequest.Header().Set("Authorization", "Bearer "+adminToken)
	history, err := client.ListOperators(ctx, historyRequest)
	require.NoError(t, err)
	require.Len(t, history.Msg.GetOperators(), 1)
	assert.Equal(t, "operator-053-enrolled", history.Msg.GetOperators()[0].GetId())

	operatorID := "operator-053-enrolled"
	streamCtx, cancelStream := context.WithCancel(context.Background())
	operatorService.StreamRegistry().Register(operatorID, enrolled.Msg.GetSessionId(), cancelStream)

	revokeRequest := connect.NewRequest(&orchestratorv1.RevokeOperatorRequest{
		CustomerId: customerID, ClusterId: clusterID, OperatorId: operatorID, Reason: "security incident",
	})
	revokeRequest.Header().Set("Authorization", "Bearer "+adminToken)
	revoked, err := client.RevokeOperator(ctx, revokeRequest)
	require.NoError(t, err)
	assert.True(t, revoked.Msg.GetChanged())
	assert.Equal(t, orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_REVOKED, revoked.Msg.GetOperator().GetSessionStatus())
	select {
	case <-streamCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("operator stream was not closed after revocation")
	}

	detailRequest := connect.NewRequest(&orchestratorv1.GetOperatorRequest{CustomerId: customerID, ClusterId: clusterID, OperatorId: operatorID})
	detailRequest.Header().Set("Authorization", "Bearer "+adminToken)
	detail, err := client.GetOperator(ctx, detailRequest)
	require.NoError(t, err)
	assert.Equal(t, orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_REVOKED, detail.Msg.GetOperator().GetSummary().GetSessionStatus())
}

func generateOperatorSmokeCSR(t *testing.T, operatorName, customerID, clusterID string) []byte {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: operatorName},
		DNSNames: []string{customerID, clusterID},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, template, privateKey)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

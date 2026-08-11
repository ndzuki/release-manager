package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	trustv1 "github.com/ndzuki/release-manager/api/gen/trust/v1"
	trustv1connect "github.com/ndzuki/release-manager/api/gen/trust/v1/trustv1connect"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/postgres"
	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/internal/trust"
	"github.com/ndzuki/release-manager/migrations"
)

func TestOrchestratorValuesApprovalEndToEnd(t *testing.T) {
	const signingKey = "test-signing-key"
	ctx := context.Background()
	dbPath := t.TempDir() + "/orchestrator.db"
	seedStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)

	const (
		organizationID   = "1d4d2977-3a2a-4a63-b695-b19a241e7ba5"
		customerID       = "5e62e848-b527-4a7a-b8b6-13937b6f1fa7"
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
	authStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, authStore.Close()) })
	authServer := newTestAuthorizationServer(t, authStore, signingKey)

	svc := &orchSvc{targetEnv: "staging", signingKey: signingKey, authURL: authServer.URL}
	svc.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}})
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
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.ErrorContains(t, err, "authorization snapshot stale")
	_, err = client.SubmitValuesRevision(ctx, viewerRequest)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.ErrorContains(t, err, "insufficient for submit")
	viewerAudit, err := svc.store.ValuesApprovalEvidence().ListAuditOutbox(ctx, viewerRevisionID)
	require.NoError(t, err)
	require.Len(t, viewerAudit, 1)
	viewerRevision, err := svc.store.Values().Get(ctx, viewerRevisionID)
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

func TestTrustServiceMountAndEd25519TrustChain(t *testing.T) {
	const (
		signingKey       = "trust-test-signing-key"
		organizationID   = "org-trust-live"
		platformAdminID  = "platform-admin-trust"
		deployerID       = "deployer-trust"
		customerID       = "customer-trust-live"
		definitionID     = "definition-trust-live"
		valuesRevisionID = "values-trust-live"
		bundleID         = "bundle-trust-live"
		rejectedBundleID = "bundle-trust-rejected"
	)
	ctx := t.Context()
	dbPath := t.TempDir() + "/orchestrator.db"
	seedStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, seedStore.Customers().Create(ctx, &store.Customer{ID: customerID, Name: "Trust Customer", Slug: "trust-customer"}))
	require.NoError(t, seedStore.Organizations().Create(ctx, &store.Organization{ID: organizationID, Name: "Trust Organization"}))
	require.NoError(t, seedStore.Bindings().Create(ctx, &store.OrgCustomerBinding{ID: "binding-trust-live", OrgID: organizationID, CustomerID: customerID}))
	for userID, role := range map[string]store.Role{platformAdminID: store.RolePlatformAdmin, deployerID: store.RoleDeployer} {
		require.NoError(t, seedStore.Users().Create(ctx, &store.User{ID: userID, Username: userID, PasswordHash: "unused", Status: store.UserActive}))
		require.NoError(t, seedStore.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: organizationID, UserID: userID, Role: role}))
		require.NoError(t, seedStore.AuthSessions().Create(ctx, &store.AuthSession{
			ID: uuid.NewString(), UserID: userID, TokenFamily: uuid.NewString(), RefreshTokenHash: uuid.NewString(),
			ExpiresAt: time.Now().UTC().Add(time.Hour),
		}))
	}
	ownerOrganizationID := organizationID
	require.NoError(t, seedStore.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: definitionID, Name: "Trust Definition", CustomerID: customerID, ClusterID: "cluster-trust-live",
		Namespace: "trust", ReleaseName: "trust", ChartName: "fixture", Status: store.DefStatusActive,
		OwnerOrganizationID: &ownerOrganizationID,
	}, nil))
	require.NoError(t, seedStore.Values().Create(ctx, &store.ValuesRevision{
		ID: valuesRevisionID, ReleaseDefinitionID: definitionID, Revision: 1, StateVersion: 1,
		Status: store.ValuesStatusApproved, Values: []byte(`{"replicas":1}`), Digest: "sha256:values-trust-live",
	}))
	bundleDigest := fmt.Sprintf("%064x", 74)
	require.NoError(t, seedStore.Bundles().Create(ctx, &store.ReleaseBundle{
		ID: bundleID, Name: "Trust Bundle", DigestAlg: "sha256", DigestValue: bundleDigest,
		Status: store.BundleValidated, CreatedAt: time.Now().UTC(),
	}))
	require.NoError(t, seedStore.Bundles().Create(ctx, &store.ReleaseBundle{
		ID: rejectedBundleID, Name: "Rejected Trust Bundle", DigestAlg: "sha256", DigestValue: fmt.Sprintf("%064x", 75),
		Status: store.BundleValidated, CreatedAt: time.Now().UTC(),
	}))
	require.NoError(t, seedStore.Close())

	mux := http.NewServeMux()
	authStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, authStore.Close()) })
	authServer := newTestAuthorizationServer(t, authStore, signingKey)
	svc := &orchSvc{targetEnv: "staging", signingKey: signingKey, authURL: authServer.URL}
	svc.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}})
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(context.Background())) })
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	jwtManager := auth.NewJWTManager([]byte(signingKey), time.Hour, time.Hour)
	platformToken, _, err := jwtManager.GenerateAccessToken(platformAdminID, organizationID, []string{string(store.RolePlatformAdmin)})
	require.NoError(t, err)
	deployerToken, _, err := jwtManager.GenerateAccessToken(deployerID, organizationID, []string{string(store.RoleDeployer)})
	require.NoError(t, err)
	trustClient := trustv1connect.NewTrustServiceClient(server.Client(), server.URL)

	publicKeyPEM, privateKey := testEd25519KeyPair(t)
	deniedCreate := connect.NewRequest(&trustv1.CreateTrustRootRequest{
		Environment: "staging", KeyId: "key-denied", PublicKeyPem: publicKeyPEM, Issuer: "release-manager-ci",
	})
	deniedCreate.Header().Set("Authorization", "Bearer "+deployerToken)
	_, err = trustClient.CreateTrustRoot(ctx, deniedCreate)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	createRoot := connect.NewRequest(&trustv1.CreateTrustRootRequest{
		Environment: "staging", KeyId: "key-live", PublicKeyPem: publicKeyPEM,
		Issuer: "release-manager-ci", SubjectPattern: "repo:release-manager:", Operator: "spoofed-operator",
	})
	createRoot.Header().Set("Authorization", "Bearer "+platformToken)
	rootResponse, err := trustClient.CreateTrustRoot(ctx, createRoot)
	require.NoError(t, err)
	require.NotNil(t, rootResponse.Msg.GetRoot())

	getPolicy := connect.NewRequest(&trustv1.GetTrustPolicyRequest{Environment: "staging"})
	getPolicy.Header().Set("Authorization", "Bearer "+deployerToken)
	policyResponse, err := trustClient.GetTrustPolicy(ctx, getPolicy)
	require.NoError(t, err)
	assert.Len(t, policyResponse.Msg.GetPolicy().GetRoots(), 1)

	deniedRotate := connect.NewRequest(&trustv1.RotateTrustRootRequest{
		Environment: "staging", OldRootId: rootResponse.Msg.GetRoot().GetId(), KeyId: "key-rotate-denied",
		PublicKeyPem: publicKeyPEM, Issuer: "release-manager-ci-rotate-denied",
	})
	deniedRotate.Header().Set("Authorization", "Bearer "+deployerToken)
	_, err = trustClient.RotateTrustRoot(ctx, deniedRotate)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	deniedEndGrace := connect.NewRequest(&trustv1.EndGraceRequest{
		Environment: "staging", RootId: rootResponse.Msg.GetRoot().GetId(),
	})
	deniedEndGrace.Header().Set("Authorization", "Bearer "+deployerToken)
	_, err = trustClient.EndGrace(ctx, deniedEndGrace)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	deniedRetire := connect.NewRequest(&trustv1.RetireTrustRootRequest{
		Environment: "staging", RootId: rootResponse.Msg.GetRoot().GetId(),
	})
	deniedRetire.Header().Set("Authorization", "Bearer "+deployerToken)
	_, err = trustClient.RetireTrustRoot(ctx, deniedRetire)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	deniedRevoke := connect.NewRequest(&trustv1.RevokeTrustRootRequest{
		Environment: "staging", RootId: rootResponse.Msg.GetRoot().GetId(),
	})
	deniedRevoke.Header().Set("Authorization", "Bearer "+deployerToken)
	_, err = trustClient.RevokeTrustRoot(ctx, deniedRevoke)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	unauthenticatedPolicy := connect.NewRequest(&trustv1.GetTrustPolicyRequest{Environment: "staging"})
	_, err = trustClient.GetTrustPolicy(ctx, unauthenticatedPolicy)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	digest := "sha256:" + bundleDigest
	rejectedDigest := "sha256:" + fmt.Sprintf("%064x", 75)
	operationClient := orchestratorv1connect.NewOrchestratorServiceClient(server.Client(), server.URL)
	trustedRequest := connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: bundleID, ReleaseDefinitionId: definitionID,
		ValuesRevisionId: valuesRevisionID, IdempotencyKey: "trust-live-accepted",
		SignatureRef: &commonv1.SignatureRef{
			Digest: digest, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(digest))),
			Issuer: "release-manager-ci", Subject: "repo:release-manager:ref:refs/heads/main",
		},
		Actor: &commonv1.ActorContext{UserId: deployerID, Organization: organizationID},
	})
	trustedRequest.Header().Set("Authorization", "Bearer "+deployerToken)
	trustedResponse, err := operationClient.CreateOperation(ctx, trustedRequest)
	require.NoError(t, err)
	assert.Equal(t, commonv1.VerificationResult_VERIFICATION_RESULT_TRUSTED, trustedResponse.Msg.GetVerificationResult())

	_, wrongPrivateKey := testEd25519KeyPair(t)
	rejectedRequest := connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: rejectedBundleID, ReleaseDefinitionId: definitionID,
		ValuesRevisionId: valuesRevisionID, IdempotencyKey: "trust-live-rejected",
		SignatureRef: &commonv1.SignatureRef{
			Digest: rejectedDigest, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(wrongPrivateKey, []byte(rejectedDigest))),
			Issuer: "release-manager-ci", Subject: "repo:release-manager:ref:refs/heads/main",
		},
		Actor: &commonv1.ActorContext{UserId: deployerID, Organization: organizationID},
	})
	rejectedRequest.Header().Set("Authorization", "Bearer "+deployerToken)
	_, err = operationClient.CreateOperation(ctx, rejectedRequest)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.ErrorContains(t, err, "signature_invalid")

	require.NoError(t, svc.Shutdown(context.Background()))
	events, err := svc.store.AuditEvents().ListByResource(ctx, "trust_root", rootResponse.Msg.GetRoot().GetId())
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, platformAdminID, events[0].ActorID)
	assert.NotEqual(t, "spoofed-operator", events[0].ActorID)
}

func TestTrustServiceMaintenanceGate(t *testing.T) {
	const (
		signingKey      = "trust-maintenance-signing-key"
		organizationID  = "org-trust-maintenance"
		platformAdminID = "platform-admin-maintenance"
	)
	ctx := t.Context()
	dbPath := t.TempDir() + "/orchestrator.db"
	seedStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, seedStore.Organizations().Create(ctx, &store.Organization{ID: organizationID, Name: "Trust Maintenance Org"}))
	require.NoError(t, seedStore.Users().Create(ctx, &store.User{ID: platformAdminID, Username: platformAdminID, PasswordHash: "unused", Status: store.UserActive}))
	require.NoError(t, seedStore.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: organizationID, UserID: platformAdminID, Role: store.RolePlatformAdmin}))
	require.NoError(t, seedStore.AuthSessions().Create(ctx, &store.AuthSession{
		ID: uuid.NewString(), UserID: platformAdminID, TokenFamily: uuid.NewString(), RefreshTokenHash: uuid.NewString(),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}))
	require.NoError(t, seedStore.Close())

	mux := http.NewServeMux()
	authStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, authStore.Close()) })
	authServer := newTestAuthorizationServer(t, authStore, signingKey)
	svc := &orchSvc{targetEnv: "staging", signingKey: signingKey, authURL: authServer.URL}
	svc.Configure(&config.ServiceConfig{
		Database:    config.DatabaseConfig{Driver: "sqlite", DSN: dbPath},
		Maintenance: true,
	})
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(context.Background())) })
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	jwtManager := auth.NewJWTManager([]byte(signingKey), time.Hour, time.Hour)
	adminToken, _, err := jwtManager.GenerateAccessToken(platformAdminID, organizationID, []string{string(store.RolePlatformAdmin)})
	require.NoError(t, err)
	trustClient := trustv1connect.NewTrustServiceClient(server.Client(), server.URL)

	// maintenance allowlist：GetTrustPolicy 在 handler 前放行。
	getPolicy := connect.NewRequest(&trustv1.GetTrustPolicyRequest{Environment: "staging"})
	getPolicy.Header().Set("Authorization", "Bearer "+adminToken)
	policyResponse, err := trustClient.GetTrustPolicy(ctx, getPolicy)
	require.NoError(t, err)
	require.NotNil(t, policyResponse.Msg.GetPolicy())

	// maintenance 拦截器：五个写 RPC 在 auth/handler 前拒绝，platform_admin 也不例外。
	publicKeyPEM, _ := testEd25519KeyPair(t)
	writes := []struct {
		name   string
		call   func() error
	}{
		{
			name: "create",
			call: func() error {
				req := connect.NewRequest(&trustv1.CreateTrustRootRequest{
					Environment: "staging", KeyId: "key-maintenance", PublicKeyPem: publicKeyPEM, Issuer: "release-manager-ci",
				})
				req.Header().Set("Authorization", "Bearer "+adminToken)
				_, err := trustClient.CreateTrustRoot(ctx, req)
				return err
			},
		},
		{
			name: "rotate",
			call: func() error {
				req := connect.NewRequest(&trustv1.RotateTrustRootRequest{
					Environment: "staging", OldRootId: "root-none", KeyId: "key-maintenance-rotate",
					PublicKeyPem: publicKeyPEM, Issuer: "release-manager-ci",
				})
				req.Header().Set("Authorization", "Bearer "+adminToken)
				_, err := trustClient.RotateTrustRoot(ctx, req)
				return err
			},
		},
		{
			name: "end_grace",
			call: func() error {
				req := connect.NewRequest(&trustv1.EndGraceRequest{Environment: "staging", RootId: "root-none"})
				req.Header().Set("Authorization", "Bearer "+adminToken)
				_, err := trustClient.EndGrace(ctx, req)
				return err
			},
		},
		{
			name: "retire",
			call: func() error {
				req := connect.NewRequest(&trustv1.RetireTrustRootRequest{Environment: "staging", RootId: "root-none"})
				req.Header().Set("Authorization", "Bearer "+adminToken)
				_, err := trustClient.RetireTrustRoot(ctx, req)
				return err
			},
		},
		{
			name: "revoke",
			call: func() error {
				req := connect.NewRequest(&trustv1.RevokeTrustRootRequest{Environment: "staging", RootId: "root-none"})
				req.Header().Set("Authorization", "Bearer "+adminToken)
				_, err := trustClient.RevokeTrustRoot(ctx, req)
				return err
			},
		},
	}
	for _, tt := range writes {
		t.Run("write rejected: "+tt.name, func(t *testing.T) {
			err := tt.call()
			require.Error(t, err)
			assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
			assert.ErrorContains(t, err, "maintenance")
		})
	}
}

func TestProductionTrustResolverFailureFailsClosed(t *testing.T) {
	const (
		signingKey       = "trust-unavailable-signing-key"
		organizationID   = "org-trust-unavailable"
		deployerID       = "deployer-trust-unavailable"
		customerID       = "customer-trust-unavailable"
		definitionID     = "definition-trust-unavailable"
		valuesRevisionID = "values-trust-unavailable"
		bundleID         = "bundle-trust-unavailable"
	)
	ctx := t.Context()
	dbPath := t.TempDir() + "/orchestrator.db"
	seedStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, seedStore.Customers().Create(ctx, &store.Customer{ID: customerID, Name: "Trust Unavailable Customer", Slug: "trust-unavailable"}))
	require.NoError(t, seedStore.Organizations().Create(ctx, &store.Organization{ID: organizationID, Name: "Trust Unavailable Organization"}))
	require.NoError(t, seedStore.Bindings().Create(ctx, &store.OrgCustomerBinding{ID: "binding-trust-unavailable", OrgID: organizationID, CustomerID: customerID}))
	require.NoError(t, seedStore.Users().Create(ctx, &store.User{ID: deployerID, Username: deployerID, PasswordHash: "unused", Status: store.UserActive}))
	require.NoError(t, seedStore.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: organizationID, UserID: deployerID, Role: store.RoleDeployer}))
	require.NoError(t, seedStore.AuthSessions().Create(ctx, &store.AuthSession{
		ID: uuid.NewString(), UserID: deployerID, TokenFamily: uuid.NewString(), RefreshTokenHash: uuid.NewString(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}))
	ownerOrganizationID := organizationID
	require.NoError(t, seedStore.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: definitionID, Name: "Trust Unavailable Definition", CustomerID: customerID, ClusterID: "cluster-trust-unavailable",
		Namespace: "trust", ReleaseName: "trust-unavailable", ChartName: "fixture", Status: store.DefStatusActive,
		OwnerOrganizationID: &ownerOrganizationID,
	}, nil))
	require.NoError(t, seedStore.Values().Create(ctx, &store.ValuesRevision{
		ID: valuesRevisionID, ReleaseDefinitionID: definitionID, Revision: 1, StateVersion: 1,
		Status: store.ValuesStatusApproved, Values: []byte(`{"replicas":1}`), Digest: "sha256:values-trust-unavailable",
	}))
	bundleDigest := fmt.Sprintf("%064x", 76)
	require.NoError(t, seedStore.Bundles().Create(ctx, &store.ReleaseBundle{
		ID: bundleID, Name: "Trust Unavailable Bundle", DigestAlg: "sha256", DigestValue: bundleDigest,
		Status: store.BundleValidated, CreatedAt: time.Now().UTC(),
	}))
	require.NoError(t, seedStore.Close())

	authStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, authStore.Close()) })
	authServer := newTestAuthorizationServer(t, authStore, signingKey)
	mux := http.NewServeMux()
	svc := &orchSvc{
		targetEnv: "production", signingKey: signingKey, authURL: authServer.URL,
		trustResolver: failingTrustResolver{err: errors.New("trust store offline")},
	}
	svc.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}})
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	jwtManager := auth.NewJWTManager([]byte(signingKey), time.Hour, time.Hour)
	deployerToken, _, err := jwtManager.GenerateAccessToken(deployerID, organizationID, []string{string(store.RoleDeployer)})
	require.NoError(t, err)
	digest := "sha256:" + bundleDigest
	request := connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: bundleID, ReleaseDefinitionId: definitionID,
		ValuesRevisionId: valuesRevisionID, IdempotencyKey: "trust-unavailable",
		SignatureRef: &commonv1.SignatureRef{
			Digest: digest, Signature: base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)),
			Issuer: "release-manager-ci", Subject: "repo:release-manager:ref:refs/heads/main",
		},
		Actor: &commonv1.ActorContext{UserId: deployerID, Organization: organizationID},
	})
	request.Header().Set("Authorization", "Bearer "+deployerToken)
	client := orchestratorv1connect.NewOrchestratorServiceClient(server.Client(), server.URL)
	_, err = client.CreateOperation(ctx, request)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.ErrorContains(t, err, "verification_unavailable")
}

func TestRevocationEpochInvalidatesCachedVerification(t *testing.T) {
	ctx := t.Context()
	st, err := sqlitestore.Open(t.TempDir() + "/trust.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	publicKeyPEM, privateKey := testEd25519KeyPair(t)
	backupPublicKeyPEM, _ := testEd25519KeyPair(t)
	trustService := trust.NewTrustService(st.TrustRoots(), nil, slog.New(slog.DiscardHandler))
	primary, err := trustService.CreateTrustRoot(ctx, connect.NewRequest(&trustv1.CreateTrustRootRequest{
		Environment: "production", KeyId: "primary", PublicKeyPem: publicKeyPEM, Issuer: "release-manager-ci",
	}))
	require.NoError(t, err)
	_, err = trustService.CreateTrustRoot(ctx, connect.NewRequest(&trustv1.CreateTrustRootRequest{
		Environment: "production", KeyId: "backup", PublicKeyPem: backupPublicKeyPEM, Issuer: "backup-ci",
	}))
	require.NoError(t, err)
	verifier := trust.NewEd25519Verifier(st.Verifications(), trust.NewStoreResolver(st.TrustRoots()), time.Second, slog.New(slog.DiscardHandler))
	digest := "sha256:" + fmt.Sprintf("%064x", 99)
	input := trust.Input{
		Digest: digest,
		SignatureRef: &commonv1.SignatureRef{
			Digest: digest, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(digest))),
			Issuer: "release-manager-ci",
		},
		Policy: trust.DefaultPolicy("production"), Environment: "production",
	}
	first, err := verifier.Verify(ctx, input)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationTrusted, first.Status)

	_, err = trustService.RevokeTrustRoot(ctx, connect.NewRequest(&trustv1.RevokeTrustRootRequest{
		Environment: "production", RootId: primary.Msg.GetRoot().GetId(),
	}))
	require.NoError(t, err)
	second, err := verifier.Verify(ctx, input)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationRejected, second.Status)
	assert.Contains(t, second.Summary, "untrusted_issuer")
}

func testEd25519KeyPair(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), privateKey
}

type failingTrustResolver struct {
	err error
}

func (r failingTrustResolver) ResolveActive(context.Context, string, time.Time) ([]*store.TrustRoot, error) {
	return nil, r.err
}

func (r failingTrustResolver) GetPolicyMeta(context.Context, string) (*store.TrustPolicyMeta, error) {
	return nil, r.err
}

type testAuthorizationHandler struct {
	store store.Store
	jwt   *auth.JWTManager
}

func newTestAuthorizationServer(t *testing.T, st store.Store, signingKey string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := authv1connect.NewAuthorizationServiceHandler(&testAuthorizationHandler{
		store: st,
		jwt:   auth.NewJWTManager([]byte(signingKey), time.Hour, time.Hour),
	})
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func (h *testAuthorizationHandler) GetAuthorizationSnapshot(
	ctx context.Context,
	req *connect.Request[authv1.GetAuthorizationSnapshotRequest],
) (*connect.Response[authv1.GetAuthorizationSnapshotResponse], error) {
	token := strings.TrimPrefix(req.Header().Get("Authorization"), "Bearer ")
	claims, err := h.jwt.ValidateAccessToken(token)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid token: %w", err))
	}
	if claims.OrgID != req.Msg.GetOrganizationId() {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("scope mismatch"))
	}
	member, err := h.store.OrgMembers().Get(ctx, claims.OrgID, claims.UserID)
	if err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}
	snapshot, err := h.store.Authorization().Load(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&authv1.GetAuthorizationSnapshotResponse{
		OrganizationId:           req.Msg.GetOrganizationId(),
		CustomerId:               req.Msg.GetCustomerId(),
		ActorId:                  claims.UserID,
		Role:                     string(member.Role),
		CanExecuteEmergency:      member.Role == store.RoleReleaseAdmin || member.Role == store.RolePlatformAdmin,
		CanResolveEmergency:      member.Role == store.RolePlatformAdmin,
		CanCreateValuesRevision:  testRoleAllows(member.Role, store.AuthorizationCreateValues),
		CanApproveValuesRevision: testRoleAllows(member.Role, store.AuthorizationApproveValues),
		SourceVersion:            snapshot.SourceVersion,
		PolicyVersion:            snapshot.PolicyVersion,
		Checkpoint:               snapshot.SourceVersion,
		Fresh:                    true,
	}), nil
}

func (*testAuthorizationHandler) SetCapabilityGrant(
	context.Context,
	*connect.Request[authv1.SetCapabilityGrantRequest],
) (*connect.Response[authv1.SetCapabilityGrantResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("not used"))
}

func testRoleAllows(role store.Role, action store.AuthorizationAction) bool {
	switch role {
	case store.RolePlatformAdmin:
		return true
	case store.RoleReleaseAdmin:
		return action == store.AuthorizationCreateValues || action == store.AuthorizationApproveValues
	case store.RoleDeployer:
		return action == store.AuthorizationCreateValues
	default:
		return false
	}
}

var _ authv1connect.AuthorizationServiceHandler = (*testAuthorizationHandler)(nil)

func TestOrchestratorPostgreSQLCutoverAuthority(t *testing.T) {
	baseDSN := os.Getenv("POSTGRES_TEST_DSN")
	if baseDSN == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := t.Context()
	dsn := orchestratorTestSchema(ctx, t, baseDSN)
	database, err := postgres.Open(ctx, config.DatabaseConfig{Driver: "postgres", DSN: dsn})
	require.NoError(t, err)
	require.NoError(t, postgres.RunMigrations(ctx, database.SQLDB(), migrations.FS))
	seedStore, err := postgresstore.New(database.SQLDB(), database.GORM())
	require.NoError(t, err)
	const (
		signingKey     = "postgres-cutover-signing-key"
		organizationID = "org-070-cutover"
		userID         = "user-070-cutover"
		customerID     = "customer-070-cutover"
	)
	require.NoError(t, seedStore.Organizations().Create(ctx, &store.Organization{ID: organizationID, Name: "Cutover Org"}))
	require.NoError(t, seedStore.Users().Create(ctx, &store.User{ID: userID, Username: userID, PasswordHash: "unused"}))
	require.NoError(t, seedStore.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: organizationID, UserID: userID, Role: store.RolePlatformAdmin}))
	require.NoError(t, seedStore.AuthSessions().Create(ctx, &store.AuthSession{
		ID: uuid.NewString(), UserID: userID, TokenFamily: uuid.NewString(), RefreshTokenHash: uuid.NewString(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}))
	require.NoError(t, seedStore.Close())

	svc := &orchSvc{targetEnv: "staging", signingKey: signingKey}
	svc.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "postgres", DSN: dsn}})
	mux := http.NewServeMux()
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	jwtManager := auth.NewJWTManager([]byte(signingKey), time.Hour, time.Hour)
	token, _, err := jwtManager.GenerateAccessToken(userID, organizationID, []string{string(store.RolePlatformAdmin)})
	require.NoError(t, err)
	request := connect.NewRequest(&orchestratorv1.CreateCustomerRequest{Id: customerID, Name: "PostgreSQL Customer", Slug: "postgresql-customer"})
	request.Header().Set("Authorization", "Bearer "+token)
	client := orchestratorv1connect.NewOrchestratorServiceClient(server.Client(), server.URL)
	_, err = client.CreateCustomer(ctx, request)
	require.NoError(t, err)
	assert.IsType(t, &postgresstore.Store{}, svc.store)
	created, err := svc.store.Customers().Get(ctx, customerID)
	require.NoError(t, err)
	assert.Equal(t, "PostgreSQL Customer", created.Name)

	sqlitePath := t.TempDir() + "/rollback.db"
	rollback, err := sqlitestore.Open(sqlitePath)
	require.NoError(t, err)
	require.NoError(t, rollback.Customers().Create(ctx, &store.Customer{ID: "sqlite-before-cutover", Name: "SQLite Snapshot", Slug: "sqlite-snapshot"}))
	require.NoError(t, rollback.Close())
	before, err := os.ReadFile(sqlitePath)
	require.NoError(t, err)
	require.NoError(t, svc.store.Customers().Create(ctx, &store.Customer{ID: "postgres-after-cutover", Name: "PostgreSQL Only", Slug: "postgres-only"}))
	after, err := os.ReadFile(sqlitePath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "PostgreSQL writes must not modify the SQLite rollback snapshot")

	rollback, err = sqlitestore.Open(sqlitePath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rollback.Close()) })
	_, err = rollback.Customers().Get(ctx, "sqlite-before-cutover")
	require.NoError(t, err)
	_, err = rollback.Customers().Get(ctx, "postgres-after-cutover")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func orchestratorTestSchema(ctx context.Context, t *testing.T, baseDSN string) string {
	t.Helper()
	schema := "task070_orchestrator_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	db, err := sql.Open("pgx", baseDSN)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA %q`, schema)) //nolint:gosec // schema is generated from a UUID.
	require.NoError(t, err)
	cleanupCtx := context.WithoutCancel(ctx)
	t.Cleanup(func() {
		_, dropErr := db.ExecContext(cleanupCtx, fmt.Sprintf(`DROP SCHEMA %q CASCADE`, schema)) //nolint:gosec // schema is generated from a UUID.
		require.NoError(t, dropErr)
		require.NoError(t, db.Close())
	})
	parsed, err := url.Parse(baseDSN)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func TestServiceReadOnlyProcedures(t *testing.T) {
	orchestratorReadOnly := orchestratorReadOnlyProcedures()
	for _, procedure := range []string{
		orchestratorv1connect.OrchestratorServiceGetReleaseDefinitionProcedure,
		orchestratorv1connect.OrchestratorServiceListReleaseDefinitionsProcedure,
		orchestratorv1connect.OrchestratorServiceGetCustomerProcedure,
		orchestratorv1connect.OrchestratorServiceListCustomersProcedure,
		orchestratorv1connect.OrchestratorServiceGetClusterProcedure,
		orchestratorv1connect.OrchestratorServiceListClustersProcedure,
		orchestratorv1connect.OrchestratorServiceGetClusterRoutesProcedure,
		orchestratorv1connect.OrchestratorServiceGetOperationProcedure,
	} {
		assert.Contains(t, orchestratorReadOnly, procedure)
	}
	assert.NotContains(t, orchestratorReadOnly, trustv1connect.TrustServiceGetTrustPolicyProcedure)
	assert.NotContains(t, orchestratorReadOnly, orchestratorv1connect.OrchestratorServiceCreateOperationProcedure)

	trustReadOnly := trustReadOnlyProcedures()
	assert.Contains(t, trustReadOnly, trustv1connect.TrustServiceGetTrustPolicyProcedure)
	for _, procedure := range []string{
		trustv1connect.TrustServiceCreateTrustRootProcedure,
		trustv1connect.TrustServiceRotateTrustRootProcedure,
		trustv1connect.TrustServiceEndGraceProcedure,
		trustv1connect.TrustServiceRetireTrustRootProcedure,
		trustv1connect.TrustServiceRevokeTrustRootProcedure,
	} {
		assert.NotContains(t, trustReadOnly, procedure)
	}
}

func TestLoadTrustConfig(t *testing.T) {
	configPath := t.TempDir() + "/orchestrator.yaml"
	require.NoError(t, os.WriteFile(configPath, []byte("trust:\n  verification_timeout: 250ms\n"), 0o600))
	svc := &orchSvc{configPath: configPath}

	trustCfg, err := svc.loadTrustConfig()

	require.NoError(t, err)
	assert.Equal(t, 250*time.Millisecond, trustCfg.VerificationTimeout)
}

func TestLoadTrustConfig_DefaultsNonPositiveTimeout(t *testing.T) {
	configPath := t.TempDir() + "/orchestrator.yaml"
	require.NoError(t, os.WriteFile(configPath, []byte("trust:\n  verification_timeout: 0s\n"), 0o600))
	svc := &orchSvc{configPath: configPath}

	trustCfg, err := svc.loadTrustConfig()

	require.NoError(t, err)
	assert.Equal(t, trust.DefaultVerificationTimeout, trustCfg.VerificationTimeout)
}

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	trustv1 "github.com/ndzuki/release-manager/api/gen/trust/v1"
	trustv1connect "github.com/ndzuki/release-manager/api/gen/trust/v1/trustv1connect"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/postgres"
	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/internal/trust"
	"github.com/ndzuki/release-manager/internal/values"
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
		ID: revisionID, ReleaseDefinitionID: definitionID, Version: 1,
		StateVersion: 1, Status: store.ValuesStatusDraft, CanonicalDocument: []byte(`{"replicas":1}`),
		Digest: "sha256:068-smoke", CreatedByUserID: creatorID,
	}))
	require.NoError(t, seedStore.Values().Create(ctx, &store.ValuesRevision{
		ID: viewerRevisionID, ReleaseDefinitionID: definitionID, Version: 2,
		StateVersion: 1, Status: store.ValuesStatusDraft, CanonicalDocument: []byte(`{"replicas":2}`),
		Digest: "sha256:068-viewer", ParentRevisionID: revisionID, CreatedByUserID: viewerID,
	}))
	require.NoError(t, seedStore.Close())

	mux := http.NewServeMux()
	authStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, authStore.Close()) })
	authServer := newTestAuthorizationServer(t, authStore, signingKey)

	svc := &orchSvc{targetEnv: "staging", signingKey: signingKey, authURL: authServer.URL}
	svc.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}, CA: testCAConfig(t)})
	require.NoError(t, svc.Register(mux, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))))
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
		organizationID   = "0f7e6d3e-8a2b-4f6e-9a1c-3d5b7e9f1a2b"
		platformAdminID  = "platform-admin-trust"
		deployerID       = "deployer-trust"
		customerID       = "1c2d3e4f-5a6b-7c8d-9e0f-1a2b3c4d5e6f"
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
		ID: valuesRevisionID, ReleaseDefinitionID: definitionID, Version: 1, StateVersion: 1,
		Status: store.ValuesStatusApproved, CanonicalDocument: []byte(`{"replicas":1}`), Digest: "sha256:values-trust-live",
	}))
	bundleDigest := fmt.Sprintf("%064x", 74)
	require.NoError(t, seedStore.Bundles().Create(ctx, &store.ReleaseBundle{
		ID: bundleID, Name: "Trust Bundle", DigestAlg: "sha256", DigestValue: bundleDigest,
		Status: store.BundleValidated, ChartRef: "fixture", CreatedAt: time.Now().UTC(),
	}))
	require.NoError(t, seedStore.Bundles().Create(ctx, &store.ReleaseBundle{
		ID: rejectedBundleID, Name: "Rejected Trust Bundle", DigestAlg: "sha256", DigestValue: fmt.Sprintf("%064x", 75),
		Status: store.BundleValidated, ChartRef: "fixture", CreatedAt: time.Now().UTC(),
	}))
	require.NoError(t, seedStore.Close())

	mux := http.NewServeMux()
	authStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, authStore.Close()) })
	authServer := newTestAuthorizationServer(t, authStore, signingKey)
	svc := &orchSvc{targetEnv: "staging", signingKey: signingKey, authURL: authServer.URL}
	svc.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}, CA: testCAConfig(t)})
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	// Bump the authorization source version so the Module snapshot is fresh
	// (a fresh database starts at version 0, which fails closed).
	authSnap, err := svc.store.Authorization().Load(context.Background())
	require.NoError(t, err)
	_, err = svc.store.Authorization().Apply(context.Background(), store.AuthorizationApplyCommand{
		ExpectedSourceVersion: authSnap.SourceVersion,
		ExpectedPolicyVersion: authSnap.PolicyVersion,
		Mutation:              store.AuthorizationMembershipChanged,
	})
	require.NoError(t, err)
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
	warmAuthorization(ctx, t, operationClient, platformToken, definitionID, "warm-trust-live")
	trustedRequest := connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: bundleID, ReleaseDefinitionId: definitionID,
		ValuesRevisionId: valuesRevisionID,
		SignatureRef: &commonv1.SignatureRef{
			Digest: digest, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(digest))),
			Issuer: "release-manager-ci", Subject: "repo:release-manager:ref:refs/heads/main",
		},
	})
	trustedRequest.Header().Set("Authorization", "Bearer "+platformToken)
	trustedRequest.Header().Set("Idempotency-Key", "trust-live-accepted")
	trustedResponse, err := operationClient.CreateOperation(ctx, trustedRequest)
	require.NoError(t, err)
	assert.Equal(t, commonv1.VerificationResult_VERIFICATION_RESULT_TRUSTED, trustedResponse.Msg.GetVerificationResult())

	// The trusted operation enters preflight and fails closed (this fixture
	// seeds no operator). Wait for it to reach a terminal state before the
	// rejected request: on a slow -race CI run the second CreateOperation
	// otherwise hits release_busy instead of the signature check.
	require.Eventually(t, func() bool {
		op, err := svc.store.Operations().Get(ctx, trustedResponse.Msg.GetOperationId())
		return err == nil && op.Status == store.StatusFailed
	}, 5*time.Second, 50*time.Millisecond)

	_, wrongPrivateKey := testEd25519KeyPair(t)
	rejectedRequest := connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: rejectedBundleID, ReleaseDefinitionId: definitionID,
		ValuesRevisionId: valuesRevisionID,
		SignatureRef: &commonv1.SignatureRef{
			Digest: rejectedDigest, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(wrongPrivateKey, []byte(rejectedDigest))),
			Issuer: "release-manager-ci", Subject: "repo:release-manager:ref:refs/heads/main",
		},
	})
	rejectedRequest.Header().Set("Authorization", "Bearer "+platformToken)
	rejectedRequest.Header().Set("Idempotency-Key", "trust-live-rejected")
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
		CA:          testCAConfig(t),
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
		name string
		call func() error
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
		organizationID   = "2a3b4c5d-6e7f-8a9b-0c1d-2e3f4a5b6c7d"
		deployerID       = "deployer-trust-unavailable"
		customerID       = "3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f"
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
	require.NoError(t, seedStore.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: organizationID, UserID: deployerID, Role: store.RoleReleaseAdmin}))
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
		ID: valuesRevisionID, ReleaseDefinitionID: definitionID, Version: 1, StateVersion: 1,
		Status: store.ValuesStatusApproved, CanonicalDocument: []byte(`{"replicas":1}`), Digest: "sha256:values-trust-unavailable",
	}))
	bundleDigest := fmt.Sprintf("%064x", 76)
	require.NoError(t, seedStore.Bundles().Create(ctx, &store.ReleaseBundle{
		ID: bundleID, Name: "Trust Unavailable Bundle", DigestAlg: "sha256", DigestValue: bundleDigest,
		Status: store.BundleValidated, ChartRef: "fixture", CreatedAt: time.Now().UTC(),
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
	svc.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}, CA: testCAConfig(t)})
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	// Bump the authorization source version so the Module snapshot is fresh.
	authSnap, err := svc.store.Authorization().Load(context.Background())
	require.NoError(t, err)
	_, err = svc.store.Authorization().Apply(context.Background(), store.AuthorizationApplyCommand{
		ExpectedSourceVersion: authSnap.SourceVersion,
		ExpectedPolicyVersion: authSnap.PolicyVersion,
		Mutation:              store.AuthorizationMembershipChanged,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	jwtManager := auth.NewJWTManager([]byte(signingKey), time.Hour, time.Hour)
	adminToken, _, err := jwtManager.GenerateAccessToken(deployerID, organizationID, []string{string(store.RoleReleaseAdmin)})
	require.NoError(t, err)
	digest := "sha256:" + bundleDigest
	request := connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: bundleID, ReleaseDefinitionId: definitionID,
		ValuesRevisionId: valuesRevisionID,
		SignatureRef: &commonv1.SignatureRef{
			Digest: digest, Signature: base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)),
			Issuer: "release-manager-ci", Subject: "repo:release-manager:ref:refs/heads/main",
		},
	})
	request.Header().Set("Authorization", "Bearer "+adminToken)
	request.Header().Set("Idempotency-Key", "trust-unavailable")
	client := orchestratorv1connect.NewOrchestratorServiceClient(server.Client(), server.URL)
	warmAuthorization(ctx, t, client, adminToken, definitionID, "warm-trust-unavailable")
	_, err = client.CreateOperation(ctx, request)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.ErrorContains(t, err, "verification_unavailable")
}

func TestRevocationEpochInvalidatesCachedVerification(t *testing.T) {
	// AC-074-04b：紧急 revoke 后，同 digest 再次验证不复用缓存中的 trusted 记录——
	// 全部经正式 Connect API（ADR-013）：platform_admin 激活 root、deployer 提交带签名 operation、
	// revoke 提升 epoch、同签名再次提交被 untrusted_issuer 拒绝。
	const (
		signingKey       = "trust-epoch-signing-key"
		organizationID   = "4d5e6f7a-8b9c-0d1e-2f3a-4b5c6d7e8f9a"
		platformAdminID  = "platform-admin-epoch"
		deployerID       = "deployer-trust-epoch"
		customerID       = "5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b"
		definitionID     = "definition-trust-epoch"
		valuesRevisionID = "values-trust-epoch"
		bundleID         = "bundle-trust-epoch"
	)
	ctx := t.Context()
	dbPath := t.TempDir() + "/orchestrator.db"
	seedStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, seedStore.Customers().Create(ctx, &store.Customer{ID: customerID, Name: "Trust Epoch Customer", Slug: "trust-epoch"}))
	require.NoError(t, seedStore.Organizations().Create(ctx, &store.Organization{ID: organizationID, Name: "Trust Epoch Organization"}))
	require.NoError(t, seedStore.Bindings().Create(ctx, &store.OrgCustomerBinding{ID: "binding-trust-epoch", OrgID: organizationID, CustomerID: customerID}))
	for userID, role := range map[string]store.Role{platformAdminID: store.RolePlatformAdmin, deployerID: store.RoleDeployer} {
		require.NoError(t, seedStore.Users().Create(ctx, &store.User{ID: userID, Username: userID, PasswordHash: "unused", Status: store.UserActive}))
		require.NoError(t, seedStore.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: organizationID, UserID: userID, Role: role}))
		require.NoError(t, seedStore.AuthSessions().Create(ctx, &store.AuthSession{
			ID: uuid.NewString(), UserID: userID, TokenFamily: uuid.NewString(), RefreshTokenHash: uuid.NewString(), ExpiresAt: time.Now().UTC().Add(time.Hour),
		}))
	}
	ownerOrganizationID := organizationID
	require.NoError(t, seedStore.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: definitionID, Name: "Trust Epoch Definition", CustomerID: customerID, ClusterID: "cluster-trust-epoch",
		Namespace: "trust", ReleaseName: "trust-epoch", ChartName: "fixture", Status: store.DefStatusActive,
		OwnerOrganizationID: &ownerOrganizationID,
	}, nil))
	require.NoError(t, seedStore.Values().Create(ctx, &store.ValuesRevision{
		ID: valuesRevisionID, ReleaseDefinitionID: definitionID, Version: 1, StateVersion: 1,
		Status: store.ValuesStatusApproved, CanonicalDocument: []byte(`{"replicas":1}`), Digest: "sha256:values-trust-epoch",
	}))
	bundleDigest := fmt.Sprintf("%064x", 98)
	require.NoError(t, seedStore.Bundles().Create(ctx, &store.ReleaseBundle{
		ID: bundleID, Name: "Trust Epoch Bundle", DigestAlg: "sha256", DigestValue: bundleDigest,
		Status: store.BundleValidated, ChartRef: "fixture", CreatedAt: time.Now().UTC(),
	}))
	require.NoError(t, seedStore.Close())

	mux := http.NewServeMux()
	authStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, authStore.Close()) })
	authServer := newTestAuthorizationServer(t, authStore, signingKey)
	svc := &orchSvc{targetEnv: "staging", signingKey: signingKey, authURL: authServer.URL}
	svc.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}, CA: testCAConfig(t)})
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	// Bump the authorization source version so the Module snapshot is fresh.
	authSnap, err := svc.store.Authorization().Load(context.Background())
	require.NoError(t, err)
	_, err = svc.store.Authorization().Apply(context.Background(), store.AuthorizationApplyCommand{
		ExpectedSourceVersion: authSnap.SourceVersion,
		ExpectedPolicyVersion: authSnap.PolicyVersion,
		Mutation:              store.AuthorizationMembershipChanged,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(context.Background())) })
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	jwtManager := auth.NewJWTManager([]byte(signingKey), time.Hour, time.Hour)
	adminToken, _, err := jwtManager.GenerateAccessToken(platformAdminID, organizationID, []string{string(store.RolePlatformAdmin)})
	require.NoError(t, err)

	// 正式 Connect API：platform_admin 激活 root。
	trustClient := trustv1connect.NewTrustServiceClient(server.Client(), server.URL)
	publicKeyPEM, privateKey := testEd25519KeyPair(t)
	createRoot := connect.NewRequest(&trustv1.CreateTrustRootRequest{
		Environment: "staging", KeyId: "epoch-primary", PublicKeyPem: publicKeyPEM, Issuer: "release-manager-ci",
	})
	createRoot.Header().Set("Authorization", "Bearer "+adminToken)
	rootResponse, err := trustClient.CreateTrustRoot(ctx, createRoot)
	require.NoError(t, err)
	// 第二把 key 作为 backup root：revoke 不能移除最后一个 active root（REQ-043 AC-043-03）。
	backupPublicKeyPEM, _ := testEd25519KeyPair(t)
	createBackup := connect.NewRequest(&trustv1.CreateTrustRootRequest{
		Environment: "staging", KeyId: "epoch-backup", PublicKeyPem: backupPublicKeyPEM, Issuer: "backup-ci",
	})
	createBackup.Header().Set("Authorization", "Bearer "+adminToken)
	_, err = trustClient.CreateTrustRoot(ctx, createBackup)
	require.NoError(t, err)

	digest := "sha256:" + bundleDigest

	operationClient := orchestratorv1connect.NewOrchestratorServiceClient(server.Client(), server.URL)
	warmAuthorization(ctx, t, operationClient, adminToken, definitionID, "warm-epoch")
	newRequest := func(idempotencyKey string) *connect.Request[orchestratorv1.CreateOperationRequest] {
		req := connect.NewRequest(&orchestratorv1.CreateOperationRequest{
			OperationType: "INSTALL", BundleId: bundleID, ReleaseDefinitionId: definitionID,
			ValuesRevisionId: valuesRevisionID,
			SignatureRef: &commonv1.SignatureRef{
				Digest: digest, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(digest))),
				Issuer: "release-manager-ci", Subject: "repo:release-manager:ref:refs/heads/main",
			},
		})
		req.Header().Set("Authorization", "Bearer "+adminToken)
		req.Header().Set("Idempotency-Key", idempotencyKey)
		return req
	}

	// 首次提交：受信。
	first, err := operationClient.CreateOperation(ctx, newRequest("trust-epoch-first"))
	require.NoError(t, err)
	assert.Equal(t, commonv1.VerificationResult_VERIFICATION_RESULT_TRUSTED, first.Msg.GetVerificationResult())

	// 紧急 revoke：epoch 提升，root 移出 live 集合。
	revoke := connect.NewRequest(&trustv1.RevokeTrustRootRequest{Environment: "staging", RootId: rootResponse.Msg.GetRoot().GetId()})
	revoke.Header().Set("Authorization", "Bearer "+adminToken)
	_, err = trustClient.RevokeTrustRoot(ctx, revoke)
	require.NoError(t, err)

	// 同 digest、同签名、新幂等键再次提交：缓存不复用 → 重新验证 → untrusted_issuer rejected。
	_, err = operationClient.CreateOperation(ctx, newRequest("trust-epoch-second"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.ErrorContains(t, err, "untrusted_issuer")
}

// warmAuthorization issues one CreateOperation call that fails at the
// authorization gate: the Module's first pull after a source bump observes a
// changed checkpoint and fails closed (REQ-027), so the real request below
// succeeds on the caught-up snapshot.
func warmAuthorization(ctx context.Context, t *testing.T, client orchestratorv1connect.OrchestratorServiceClient, token, definitionID, idempotencyKey string) {
	t.Helper()
	req := connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-warm",
		ReleaseDefinitionId: definitionID,
		ValuesRevisionId:    "values-warm",
	})
	req.Header().Set("Authorization", "Bearer "+token)
	req.Header().Set("Idempotency-Key", idempotencyKey)
	_, err := client.CreateOperation(ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
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

func TestOrchestratorValuesRevisionManagementEndToEnd(t *testing.T) {
	const signingKey = "test-signing-key"
	ctx := context.Background()
	dbPath := t.TempDir() + "/orchestrator.db"
	seedStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)

	const (
		organizationID = "71e2f6bc-70b0-4c3a-a693-c906207d45ce"
		customerID     = "b31fd8f2-9b67-4cb7-8c0e-8c108a28ed8a"
		definitionID   = "definition-018-smoke"
		creatorID      = "creator-018-smoke"
	)
	ownerOrganizationID := organizationID
	require.NoError(t, seedStore.Customers().Create(ctx, &store.Customer{
		ID: customerID, Name: "Customer 018 Smoke", Slug: "customer-018-smoke",
	}))
	require.NoError(t, seedStore.Organizations().Create(ctx, &store.Organization{
		ID: organizationID, Name: "Organization 018 Smoke",
	}))
	require.NoError(t, seedStore.Bindings().Create(ctx, &store.OrgCustomerBinding{
		ID: "binding-018-smoke", OrgID: organizationID, CustomerID: customerID,
	}))
	require.NoError(t, seedStore.Users().Create(ctx, &store.User{
		ID: creatorID, Username: creatorID, PasswordHash: "unused",
	}))
	require.NoError(t, seedStore.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: organizationID, UserID: creatorID, Role: store.RoleDeployer,
	}))
	require.NoError(t, seedStore.AuthSessions().Create(ctx, &store.AuthSession{
		ID: uuid.NewString(), UserID: creatorID, TokenFamily: uuid.NewString(),
		RefreshTokenHash: uuid.NewString(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}))
	require.NoError(t, seedStore.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: definitionID, Name: "definition-018-smoke", CustomerID: customerID,
		ClusterID: "cluster-018-smoke", ReleaseName: "release-018-smoke",
		Status: store.DefStatusActive, OwnerOrganizationID: &ownerOrganizationID,
	}, nil))
	require.NoError(t, seedStore.Close())

	mux := http.NewServeMux()
	authStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, authStore.Close()) })
	authServer := newTestAuthorizationServer(t, authStore, signingKey)

	svc := &orchSvc{targetEnv: "staging", signingKey: signingKey, authURL: authServer.URL}
	svc.Configure(&config.ServiceConfig{
		Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath},
		Values:   config.ValuesConfig{MaxDocumentBytes: 1 << 20},
		CA:       testCAConfig(t),
	})
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	runCtx, cancelRun := context.WithCancel(context.Background())
	t.Cleanup(cancelRun)
	go svc.Run(runCtx)

	jwtManager := auth.NewJWTManager([]byte(signingKey), time.Hour, time.Hour)
	creatorToken, _, err := jwtManager.GenerateAccessToken(
		creatorID,
		organizationID,
		[]string{string(store.RoleDeployer)},
	)
	require.NoError(t, err)
	client := orchestratorv1connect.NewOrchestratorServiceClient(server.Client(), server.URL)

	createRequest := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: definitionID,
		Document:            "database:\n  password: null\nreplicas: 2\n",
		SecretRefs: []*commonv1.SecretRef{
			{Path: "/database/password", Name: "database-secret", Key: "password"},
		},
	})
	createRequest.Header().Set("Authorization", "Bearer "+creatorToken)
	createRequest.Header().Set("Idempotency-Key", "create-018-smoke")
	var created *connect.Response[orchestratorv1.CreateValuesRevisionResponse]
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		var createErr error
		created, createErr = client.CreateValuesRevision(ctx, createRequest)
		if !assert.NoError(collect, createErr) {
			assert.Contains(collect, []connect.Code{connect.CodeUnavailable, connect.CodeFailedPrecondition}, connect.CodeOf(createErr))
		}
	}, 3*time.Second, 50*time.Millisecond)
	assert.True(t, created.Msg.GetCreated())
	assert.Equal(t, int64(1), created.Msg.GetRevision().GetVersion())
	assert.Equal(t, "{\"database\":{\"password\":null},\"replicas\":2}", string(created.Msg.GetRevision().GetCanonicalDocument()))

	replayed, err := client.CreateValuesRevision(ctx, createRequest)
	require.NoError(t, err)
	assert.False(t, replayed.Msg.GetCreated())
	assert.Equal(t, created.Msg.GetRevision().GetId(), replayed.Msg.GetRevision().GetId())

	getRequest := connect.NewRequest(&orchestratorv1.GetValuesRevisionRequest{
		RevisionId: created.Msg.GetRevision().GetId(),
	})
	getRequest.Header().Set("Authorization", "Bearer "+creatorToken)
	got, err := client.GetValuesRevision(ctx, getRequest)
	require.NoError(t, err)
	assert.Equal(t, created.Msg.GetRevision().GetDigest(), got.Msg.GetDigest())
	assert.Equal(t, created.Msg.GetRevision().GetSecretRefs(), got.Msg.GetSecretRefs())

	listRequest := connect.NewRequest(&orchestratorv1.ListValuesRevisionsRequest{
		ReleaseDefinitionId: definitionID,
		Status:              commonv1.ValuesStatus_VALUES_STATUS_DRAFT,
		PageSize:            10,
	})
	listRequest.Header().Set("Authorization", "Bearer "+creatorToken)
	listed, err := client.ListValuesRevisions(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetItems(), 1)
	assert.Equal(t, created.Msg.GetRevision().GetId(), listed.Msg.GetItems()[0].GetId())

	discardRequest := connect.NewRequest(&orchestratorv1.DiscardValuesRevisionRequest{
		RevisionId:           created.Msg.GetRevision().GetId(),
		ExpectedStateVersion: created.Msg.GetRevision().GetStateVersion(),
		Comment:              "obsolete draft",
	})
	discardRequest.Header().Set("Authorization", "Bearer "+creatorToken)
	discardRequest.Header().Set("Idempotency-Key", "discard-018-smoke")
	discarded, err := client.DiscardValuesRevision(ctx, discardRequest)
	require.NoErrorf(t, err, "discard values revision failed: code=%s err=%v", connect.CodeOf(err), err)
	assert.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_DISCARDED, discarded.Msg.GetNewState())
	assert.Equal(t, created.Msg.GetRevision().GetCanonicalDocument(), discarded.Msg.GetRevision().GetCanonicalDocument())

	secretRequest := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId:   definitionID,
		Document:              "password: plaintext-secret",
		ParentRevisionId:      created.Msg.GetRevision().GetId(),
		ExpectedParentVersion: created.Msg.GetRevision().GetVersion(),
	})
	secretRequest.Header().Set("Authorization", "Bearer "+creatorToken)
	secretRequest.Header().Set("Idempotency-Key", "secret-018-smoke")
	_, err = client.CreateValuesRevision(ctx, secretRequest)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.ErrorContains(t, err, "secret_literal_forbidden")
}

type testAuthorizationHandler struct {
	store store.Store
	jwt   *auth.JWTManager
}

// testCAConfig points the operator service at a per-test CA file pair so the
// fail-closed CA loading (ADR-017) has a dev backend in integration tests.
func testCAConfig(t *testing.T) config.CAConfig {
	t.Helper()
	dir := t.TempDir()
	return config.CAConfig{
		KeyPath:          filepath.Join(dir, "ca.key"),
		CertPath:         filepath.Join(dir, "ca.crt"),
		CertTTL:          24 * time.Hour,
		RenewBeforeRatio: 0.5,
	}
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
	svc.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "postgres", DSN: dsn}, CA: testCAConfig(t)})
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
		orchestratorv1connect.OrchestratorServiceGetValuesRevisionProcedure,
		orchestratorv1connect.OrchestratorServiceListValuesRevisionsProcedure,
		orchestratorv1connect.OrchestratorServiceGetPrepareSessionProcedure,
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
		orchestratorv1connect.OrchestratorServiceCreateOperationProcedure,
		orchestratorv1connect.OrchestratorServiceCreateEnrollmentTokenProcedure,
		orchestratorv1connect.OrchestratorServiceExecuteEmergencyChangeProcedure,
		orchestratorv1connect.OrchestratorServiceSyncInventoryProcedure,
		orchestratorv1connect.OrchestratorServiceConfigureClusterRouteProcedure,
		orchestratorv1connect.OrchestratorServiceCreateValuesRevisionProcedure,
		orchestratorv1connect.OrchestratorServiceDiscardValuesRevisionProcedure,
		orchestratorv1connect.OrchestratorServiceCreatePrepareSessionProcedure,
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

// TestOperatorManagementPostgreSQLFlow exercises the full REQ-053 management
// contract against the PostgreSQL store: token create/replace/status, viewer
// denial, enrollment through the in-process OperatorService, live stream
// revocation, idempotent re-revoke, and the read-only maintenance allowlist.
func TestOperatorManagementPostgreSQLFlow(t *testing.T) {
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
	require.NoError(t, database.Close())

	const signingKey = "operator-smoke-signing-key"
	mux := http.NewServeMux()
	authStore, err := sqlitestore.Open(t.TempDir() + "/auth.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, authStore.Close()) })
	authServer := newTestAuthorizationServer(t, authStore, signingKey)
	svc := &orchSvc{
		targetEnv:      "staging",
		signingKey:     signingKey,
		authURL:        authServer.URL,
		streamRegistry: operator.NewStreamRegistry(),
	}
	svc.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "postgres", DSN: dsn}, CA: testCAConfig(t)})
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	jwtManager := auth.NewJWTManager([]byte(signingKey), time.Hour, time.Hour)
	adminToken, _, err := jwtManager.GenerateAccessToken(adminID, organizationID, []string{string(store.RoleReleaseAdmin)})
	require.NoError(t, err)
	viewerToken, _, err := jwtManager.GenerateAccessToken(viewerID, organizationID, []string{string(store.RoleViewer)})
	require.NoError(t, err)
	client := orchestratorv1connect.NewOrchestratorServiceClient(server.Client(), server.URL)
	operatorClient := operatorv1connect.NewOperatorServiceClient(server.Client(), server.URL)

	// Viewer write RPC is rejected server-side even though the UI hides the
	// entry (AC-053-03).
	viewerCreate := connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
		CustomerId: customerID, ClusterId: clusterID, OperatorName: "operator-smoke", TtlMinutes: 5,
	})
	viewerCreate.Header().Set("Authorization", "Bearer "+viewerToken)
	_, err = client.CreateEnrollmentToken(ctx, viewerCreate)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// Empty state returns an empty page, not an error (AC-053-11).
	viewerList := connect.NewRequest(&orchestratorv1.ListOperatorsRequest{CustomerId: customerID, ClusterId: clusterID})
	viewerList.Header().Set("Authorization", "Bearer "+viewerToken)
	viewerPage, err := client.ListOperators(ctx, viewerList)
	require.NoError(t, err)
	assert.Empty(t, viewerPage.Msg.GetOperators())

	// Token create returns the plaintext exactly once (AC-053-01/05).
	createRequest := connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
		CustomerId: customerID, ClusterId: clusterID, OperatorName: "operator-smoke", TtlMinutes: 5,
	})
	createRequest.Header().Set("Authorization", "Bearer "+adminToken)
	created, err := client.CreateEnrollmentToken(ctx, createRequest)
	require.NoError(t, err)
	plaintext := created.Msg.GetToken()
	assert.NotEmpty(t, plaintext)
	assert.NotEmpty(t, created.Msg.GetOperatorEndpoint())
	assert.Contains(t, created.Msg.GetInstallCommandTemplate(), "${ENROLLMENT_TOKEN}")
	assert.NotContains(t, created.Msg.GetInstallCommandTemplate(), plaintext)

	// Status returns only non-sensitive metadata (AC-053-06/07).
	statusRequest := connect.NewRequest(&orchestratorv1.GetEnrollmentTokenStatusRequest{CustomerId: customerID, ClusterId: clusterID})
	statusRequest.Header().Set("Authorization", "Bearer "+adminToken)
	status, err := client.GetEnrollmentTokenStatus(ctx, statusRequest)
	require.NoError(t, err)
	assert.Equal(t, orchestratorv1.EnrollmentTokenState_ENROLLMENT_TOKEN_STATE_PENDING, status.Msg.GetStatus().GetState())
	assert.NotContains(t, status.Msg.String(), plaintext)

	// Explicit replace atomically revokes the old token and issues a new one
	// in the same transaction (AC-053-07).
	replaceRequest := connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
		CustomerId: customerID, ClusterId: clusterID, OperatorName: "operator-smoke", TtlMinutes: 5, ReplacePendingToken: true,
	})
	replaceRequest.Header().Set("Authorization", "Bearer "+adminToken)
	replaced, err := client.CreateEnrollmentToken(ctx, replaceRequest)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, replaced.Msg.GetToken())
	assert.NotContains(t, replaced.Msg.String(), plaintext)

	// Enroll through the in-process OperatorService mount (ADR-001/002).
	csr := generateOperatorSmokeCSR(t, "operator-smoke", customerID, clusterID)
	enrolled, err := operatorClient.Enroll(ctx, connect.NewRequest(&operatorv1.EnrollRequest{
		EnrollmentToken: replaced.Msg.GetToken(),
		CustomerId:      customerID,
		ClusterId:       clusterID,
		CsrPem:          csr,
		Capabilities:    map[string]string{"helm": "true"},
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, enrolled.Msg.GetSessionId())
	// operator_id is center-generated (REQ-015 决策 4).
	operatorID := enrolled.Msg.GetOperatorId()
	assert.NotEmpty(t, operatorID)

	// History now contains the enrolled operator (AC-053-12).
	historyRequest := connect.NewRequest(&orchestratorv1.ListOperatorsRequest{CustomerId: customerID, ClusterId: clusterID})
	historyRequest.Header().Set("Authorization", "Bearer "+adminToken)
	history, err := client.ListOperators(ctx, historyRequest)
	require.NoError(t, err)
	require.Len(t, history.Msg.GetOperators(), 1)
	assert.Equal(t, operatorID, history.Msg.GetOperators()[0].GetId())

	// Revoke closes the live stream and reports revoked immediately
	// (AC-053-04/22).
	streamCtx, cancelStream := context.WithCancel(context.Background())
	svc.streamRegistry.Register(operatorID, enrolled.Msg.GetSessionId(), cancelStream)

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

	// Duplicate revoke is an idempotent success (AC-053-14).
	retryRevoke := connect.NewRequest(&orchestratorv1.RevokeOperatorRequest{
		CustomerId: customerID, ClusterId: clusterID, OperatorId: operatorID, Reason: "security incident",
	})
	retryRevoke.Header().Set("Authorization", "Bearer "+adminToken)
	retry, err := client.RevokeOperator(ctx, retryRevoke)
	require.NoError(t, err)
	assert.False(t, retry.Msg.GetChanged())

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
		DNSNames: []string{customerID, clusterID, strings.ToLower(clusterID + "." + customerID + ".rm")},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, template, privateKey)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

// TestOperatorReadOnlyProcedures pins the REQ-053 read RPCs in the maintenance
// read-only allowlist while the write RPCs stay gated (AC-053-03).
func TestOperatorReadOnlyProcedures(t *testing.T) {
	orchestratorReadOnly := orchestratorReadOnlyProcedures()
	for _, procedure := range []string{
		orchestratorv1connect.OrchestratorServiceListOperatorsProcedure,
		orchestratorv1connect.OrchestratorServiceGetOperatorProcedure,
		orchestratorv1connect.OrchestratorServiceGetEnrollmentTokenStatusProcedure,
	} {
		assert.Contains(t, orchestratorReadOnly, procedure)
	}
	for _, procedure := range []string{
		orchestratorv1connect.OrchestratorServiceRevokeOperatorProcedure,
		orchestratorv1connect.OrchestratorServiceCreateEnrollmentTokenProcedure,
		orchestratorv1connect.OrchestratorServiceRevokePendingEnrollmentTokenProcedure,
	} {
		assert.NotContains(t, orchestratorReadOnly, procedure)
	}
}

// TestValuesCreateListIdempotencyConnectEndToEnd exercises the TASK-018
// delivered Create/List/Discard/CreatePrepareSession/GetPrepareSession
// procedures through the real Connect server + auth interceptor + RBAC
// enforcer (AC-071-01/02/03): draft creation with digest consistency and
// prepare_token CAS consumption, list returning ALL revisions including
// discarded history ordered version DESC with cursor paging, and the
// idempotency double-branch under the REQ-018 D14 scope (same key + same
// request_hash replay vs same key + different request_hash).
func TestValuesCreateListIdempotencyConnectEndToEnd(t *testing.T) {
	const signingKey = "test-signing-key"
	ctx := context.Background()
	dbPath := t.TempDir() + "/orchestrator.db"
	seedStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)

	const (
		organizationID    = "3d7a5c91-6f1b-4d2e-9a58-2c4e8b1f7a03"
		customerID        = "8e2f4b6d-1a3c-4e5f-9b7a-0c1d2e3f4a5b"
		definitionID      = "definition-071-smoke"
		creatorID         = "creator-071-smoke"
		approverID        = "approver-071-smoke"
		convergenceTaskID = "task-071-convergence"
	)
	ownerOrganizationID := organizationID
	require.NoError(t, seedStore.Customers().Create(ctx, &store.Customer{
		ID: customerID, Name: "Customer 071 Smoke", Slug: "customer-071-smoke",
	}))
	require.NoError(t, seedStore.Organizations().Create(ctx, &store.Organization{
		ID: organizationID, Name: "Organization 071 Smoke",
	}))
	require.NoError(t, seedStore.Bindings().Create(ctx, &store.OrgCustomerBinding{
		ID: "binding-071-smoke", OrgID: organizationID, CustomerID: customerID,
	}))
	for userID, role := range map[string]store.Role{
		creatorID:  store.RoleDeployer,
		approverID: store.RoleReleaseAdmin,
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
		ID: definitionID, Name: "definition-071-smoke", CustomerID: customerID,
		ClusterID: "cluster-071-smoke", ReleaseName: "release-071-smoke",
		Status: store.DefStatusActive, OwnerOrganizationID: &ownerOrganizationID,
	}, nil))
	// One pending_promotion convergence task backing the prepare_token path;
	// it references an operation (FK constraint on convergence_tasks).
	require.NoError(t, seedStore.Operations().Create(ctx, &store.Operation{
		ID:                  "op-071-smoke",
		ReleaseDefinitionID: definitionID,
		OperationType:       store.OperationInstall,
		Status:              store.StatusPending,
		Actor:               store.ActorContext{UserID: creatorID},
	}))
	require.NoError(t, seedStore.ConvergenceTasks().Create(ctx, &store.ConvergenceTask{
		ID:                  convergenceTaskID,
		OperationID:         "op-071-smoke",
		ReleaseDefinitionID: definitionID,
		Action:              store.EmergencySetContainerImage,
		TargetSummary:       "seed chain",
		PromotionPaths:      json.RawMessage(`["/replicas"]`),
		Status:              "pending_promotion",
		SubmittedAt:         time.Now().UTC(),
		CreatedAt:           time.Now().UTC(),
	}))
	require.NoError(t, seedStore.Close())

	mux := http.NewServeMux()
	authStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, authStore.Close()) })
	authServer := newTestAuthorizationServer(t, authStore, signingKey)

	svc := &orchSvc{targetEnv: "staging", signingKey: signingKey, authURL: authServer.URL}
	svc.Configure(&config.ServiceConfig{
		Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath},
		Values:   config.ValuesConfig{MaxDocumentBytes: 1 << 20},
		CA:       testCAConfig(t),
	})
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	runCtx, cancelRun := context.WithCancel(context.Background())
	t.Cleanup(cancelRun)
	go svc.Run(runCtx)

	jwtManager := auth.NewJWTManager([]byte(signingKey), time.Hour, time.Hour)
	creatorToken, _, err := jwtManager.GenerateAccessToken(
		creatorID, organizationID, []string{string(store.RoleDeployer)},
	)
	require.NoError(t, err)
	approverToken, _, err := jwtManager.GenerateAccessToken(
		approverID, organizationID, []string{string(store.RoleReleaseAdmin)},
	)
	require.NoError(t, err)
	client := orchestratorv1connect.NewOrchestratorServiceClient(server.Client(), server.URL)

	const (
		documentA = "replicas: 2\nimage:\n  tag: v1\n"
		documentB = "replicas: 3\nimage:\n  tag: v2\n"
	)
	canonicalA, err := values.Canonicalize([]byte(documentA))
	require.NoError(t, err)

	create := func(document, idemKey, prepareToken, parentID string, parentVersion int64) (*connect.Response[orchestratorv1.CreateValuesRevisionResponse], error) {
		req := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
			ReleaseDefinitionId:   definitionID,
			Document:              document,
			PrepareToken:          prepareToken,
			ParentRevisionId:      parentID,
			ExpectedParentVersion: parentVersion,
		})
		req.Header().Set("Authorization", "Bearer "+creatorToken)
		req.Header().Set("Idempotency-Key", idemKey)
		return client.CreateValuesRevision(ctx, req)
	}
	createEventually := func(document, idemKey, prepareToken, parentID string, parentVersion int64) *connect.Response[orchestratorv1.CreateValuesRevisionResponse] {
		var created *connect.Response[orchestratorv1.CreateValuesRevisionResponse]
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			var createErr error
			created, createErr = create(document, idemKey, prepareToken, parentID, parentVersion)
			if !assert.NoError(collect, createErr) {
				assert.Contains(collect, []connect.Code{connect.CodeUnavailable, connect.CodeFailedPrecondition}, connect.CodeOf(createErr))
			}
		}, 3*time.Second, 50*time.Millisecond)
		return created
	}

	// AC-071-01: create the initial draft through the formal Connect seam —
	// created=true, status=draft, digest consistent with the canonical document.
	first := createEventually(documentA, "create-071-a", "", "", 0)
	assert.True(t, first.Msg.GetCreated())
	assert.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_DRAFT, first.Msg.GetRevision().GetStatus())
	assert.Equal(t, int64(1), first.Msg.GetRevision().GetVersion())
	assert.Equal(t, string(canonicalA), string(first.Msg.GetRevision().GetCanonicalDocument()))
	assert.Equal(t, values.Digest(canonicalA), first.Msg.GetRevision().GetDigest())

	// Second ordinary revision chains off the first and becomes the chain head
	// for the prepare session.
	second := createEventually(documentB, "create-071-b", "", first.Msg.GetRevision().GetId(), first.Msg.GetRevision().GetVersion())
	assert.True(t, second.Msg.GetCreated())
	assert.Equal(t, int64(2), second.Msg.GetRevision().GetVersion())

	// AC-071-01 (convergence path): CreatePrepareSession locks the chain head,
	// GetPrepareSession reads the session back, and CreateValuesRevision
	// consumes the prepare_token inside the same transaction.
	prepareReq := connect.NewRequest(&orchestratorv1.CreatePrepareSessionRequest{
		ReleaseDefinitionId:   definitionID,
		TaskIds:               []string{convergenceTaskID},
		ExpectedParentVersion: second.Msg.GetRevision().GetVersion(),
	})
	prepareReq.Header().Set("Authorization", "Bearer "+creatorToken)
	var prepareResponse *connect.Response[orchestratorv1.CreatePrepareSessionResponse]
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		var prepareErr error
		prepareResponse, prepareErr = client.CreatePrepareSession(ctx, prepareReq)
		if !assert.NoError(collect, prepareErr) {
			assert.Contains(collect, []connect.Code{connect.CodeUnavailable, connect.CodeFailedPrecondition}, connect.CodeOf(prepareErr))
		}
	}, 3*time.Second, 50*time.Millisecond)
	prepareToken := prepareResponse.Msg.GetPrepareToken()
	assert.NotEmpty(t, prepareToken)
	assert.Equal(t, second.Msg.GetRevision().GetId(), prepareResponse.Msg.GetParentRevisionId())
	assert.Equal(t, second.Msg.GetRevision().GetVersion(), prepareResponse.Msg.GetParentVersion())
	assert.Equal(t, []string{"/replicas"}, prepareResponse.Msg.GetLockedPaths())

	getPrepareReq := connect.NewRequest(&orchestratorv1.GetPrepareSessionRequest{PrepareToken: prepareToken})
	getPrepareReq.Header().Set("Authorization", "Bearer "+creatorToken)
	sessionInfo, err := client.GetPrepareSession(ctx, getPrepareReq)
	require.NoError(t, err)
	assert.Equal(t, definitionID, sessionInfo.Msg.GetReleaseDefinitionId())
	assert.Equal(t, second.Msg.GetRevision().GetId(), sessionInfo.Msg.GetParentRevisionId())
	assert.Equal(t, []string{convergenceTaskID}, sessionInfo.Msg.GetTaskIds())
	assert.Equal(t, []string{"/replicas"}, sessionInfo.Msg.GetLockedPaths())
	assert.NotEmpty(t, sessionInfo.Msg.GetLockedPathsHash())

	converged := createEventually("replicas: 4\nimage:\n  tag: v3\n", "create-071-converge", prepareToken, "", 0)
	assert.True(t, converged.Msg.GetCreated())
	assert.Equal(t, int64(3), converged.Msg.GetRevision().GetVersion())
	assert.Equal(t, second.Msg.GetRevision().GetId(), converged.Msg.GetRevision().GetParentRevisionId())
	// CAS consumption: replaying the same request (same token + key + hash)
	// returns the original revision instead of creating another.
	replayedConverged, err := create("replicas: 4\nimage:\n  tag: v3\n", "create-071-converge", prepareToken, "", 0)
	require.NoError(t, err)
	assert.False(t, replayedConverged.Msg.GetCreated())
	assert.Equal(t, converged.Msg.GetRevision().GetId(), replayedConverged.Msg.GetRevision().GetId())

	// AC-071-02: discard the first revision, then list by definition — ALL
	// revisions including discarded history, ordered version DESC.
	discardRequest := connect.NewRequest(&orchestratorv1.DiscardValuesRevisionRequest{
		RevisionId:           first.Msg.GetRevision().GetId(),
		ExpectedStateVersion: first.Msg.GetRevision().GetStateVersion(),
		Comment:              "superseded by seed chain",
	})
	discardRequest.Header().Set("Authorization", "Bearer "+creatorToken)
	discardRequest.Header().Set("Idempotency-Key", "discard-071-1")
	discarded, err := client.DiscardValuesRevision(ctx, discardRequest)
	require.NoErrorf(t, err, "discard values revision failed: code=%s err=%v", connect.CodeOf(err), err)
	assert.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_DISCARDED, discarded.Msg.GetNewState())

	listRequest := connect.NewRequest(&orchestratorv1.ListValuesRevisionsRequest{
		ReleaseDefinitionId: definitionID,
		PageSize:            10,
	})
	listRequest.Header().Set("Authorization", "Bearer "+creatorToken)
	listed, err := client.ListValuesRevisions(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetItems(), 3)
	assert.Equal(t, converged.Msg.GetRevision().GetId(), listed.Msg.GetItems()[0].GetId())
	assert.Equal(t, second.Msg.GetRevision().GetId(), listed.Msg.GetItems()[1].GetId())
	assert.Equal(t, first.Msg.GetRevision().GetId(), listed.Msg.GetItems()[2].GetId())
	assert.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_DISCARDED, listed.Msg.GetItems()[2].GetStatus())
	assert.Empty(t, listed.Msg.GetNextCursor())

	// Cursor paging walks every revision exactly once.
	pagedRequest := connect.NewRequest(&orchestratorv1.ListValuesRevisionsRequest{
		ReleaseDefinitionId: definitionID,
		PageSize:            1,
	})
	pagedRequest.Header().Set("Authorization", "Bearer "+creatorToken)
	seen := map[string]bool{}
	for page := range 3 {
		pageResponse, pageErr := client.ListValuesRevisions(ctx, pagedRequest)
		require.NoError(t, pageErr)
		require.Len(t, pageResponse.Msg.GetItems(), 1)
		itemID := pageResponse.Msg.GetItems()[0].GetId()
		seen[itemID] = true
		if page < 2 {
			require.NotEmpty(t, pageResponse.Msg.GetNextCursor())
			pagedRequest.Msg.Cursor = pageResponse.Msg.GetNextCursor()
		} else {
			assert.Empty(t, pageResponse.Msg.GetNextCursor())
		}
	}
	require.Len(t, seen, 3)

	// AC-071-03 branch 1: same scope + same key + same request_hash replay →
	// created=false, no duplicate write (list count unchanged).
	replayed, err := create(documentA, "create-071-a", "", "", 0)
	require.NoError(t, err)
	assert.False(t, replayed.Msg.GetCreated())
	assert.Equal(t, first.Msg.GetRevision().GetId(), replayed.Msg.GetRevision().GetId())
	counted, err := client.ListValuesRevisions(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, counted.Msg.GetItems(), 3)

	// AC-071-03 branch 2: same scope + same key + different request_hash →
	// CodeAlreadyExists with idempotency_conflict (REQ-010 semantics).
	_, err = create(documentA+"extra: true\n", "create-071-a", "", "", 0)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	assert.ErrorContains(t, err, "idempotency_conflict")

	// Full chain (devseed semantics): Submit by the creator, self-approval
	// refused (REQ-068), then Approve by the release admin.
	submitRequest := connect.NewRequest(&orchestratorv1.SubmitValuesRevisionRequest{
		RevisionId:           second.Msg.GetRevision().GetId(),
		ExpectedStateVersion: second.Msg.GetRevision().GetStateVersion(),
		Comment:              "seeded by devseed",
	})
	submitRequest.Header().Set("Authorization", "Bearer "+creatorToken)
	submitRequest.Header().Set("Idempotency-Key", "submit-071-2")
	submitted, err := client.SubmitValuesRevision(ctx, submitRequest)
	require.NoErrorf(t, err, "submit values revision failed: code=%s err=%v", connect.CodeOf(err), err)
	assert.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_PENDING_APPROVAL, submitted.Msg.GetNewState())

	selfApproveRequest := connect.NewRequest(&orchestratorv1.ApproveValuesRevisionRequest{
		RevisionId:           second.Msg.GetRevision().GetId(),
		ExpectedStateVersion: submitted.Msg.GetRevision().GetStateVersion(),
		Comment:              "self approve",
	})
	selfApproveRequest.Header().Set("Authorization", "Bearer "+creatorToken)
	selfApproveRequest.Header().Set("Idempotency-Key", "self-approve-071-2")
	_, err = client.ApproveValuesRevision(ctx, selfApproveRequest)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	var selfApproveErr *connect.Error
	require.ErrorAs(t, err, &selfApproveErr)
	assert.Equal(t, "self_approval_forbidden", selfApproveErr.Meta().Get("X-Reason-Code"))

	approveRequest := connect.NewRequest(&orchestratorv1.ApproveValuesRevisionRequest{
		RevisionId:           second.Msg.GetRevision().GetId(),
		ExpectedStateVersion: submitted.Msg.GetRevision().GetStateVersion(),
		Comment:              "approved",
	})
	approveRequest.Header().Set("Authorization", "Bearer "+approverToken)
	approveRequest.Header().Set("Idempotency-Key", "approve-071-2")
	approved, err := client.ApproveValuesRevision(ctx, approveRequest)
	require.NoErrorf(t, err, "approve values revision failed: code=%s err=%v", connect.CodeOf(err), err)
	assert.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_APPROVED, approved.Msg.GetNewState())
}

// TestPreflightLifecycleConnectEndToEnd drives the REQ-019 two-phase lifecycle
// through the real Connect server: CreateOperation (AC-019-04/05/06), stage
// results via the outbox, first-dispatch consumption (D-87), and restart
// recovery of operations left in preflight (ADR-009).
func TestPreflightLifecycleConnectEndToEnd(t *testing.T) {
	const signingKey = "test-signing-key"
	ctx := context.Background()
	dbPath := t.TempDir() + "/orchestrator.db"
	seedStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)

	const (
		organizationID = "a1b2c3d4-0000-4000-8000-000000000019"
		customerID     = "a1b2c3d4-0000-4000-8000-0000000000c0"
		clusterID      = "cluster-preflight-e2e"
		definitionID   = "def-preflight-e2e"
		revisionID     = "revision-preflight-e2e"
		bundleID       = "bundle-preflight-e2e"
		userID         = "user-preflight-e2e"
	)
	require.NoError(t, seedStore.Customers().Create(ctx, &store.Customer{ID: customerID, Name: "Preflight E2E", Slug: "preflight-e2e"}))
	require.NoError(t, seedStore.Organizations().Create(ctx, &store.Organization{ID: organizationID, Name: "Preflight E2E Org"}))
	require.NoError(t, seedStore.Bindings().Create(ctx, &store.OrgCustomerBinding{ID: "binding-preflight-e2e", OrgID: organizationID, CustomerID: customerID}))
	require.NoError(t, seedStore.Users().Create(ctx, &store.User{ID: userID, Username: userID, PasswordHash: "unused"}))
	require.NoError(t, seedStore.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: organizationID, UserID: userID, Role: store.RoleReleaseAdmin}))
	require.NoError(t, seedStore.AuthSessions().Create(ctx, &store.AuthSession{
		ID: uuid.NewString(), UserID: userID, TokenFamily: uuid.NewString(),
		RefreshTokenHash: uuid.NewString(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}))
	owner := organizationID
	require.NoError(t, seedStore.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: definitionID, Name: "preflight-e2e", CustomerID: customerID, ClusterID: clusterID,
		Namespace: "default", ReleaseName: "preflight-e2e-rel", Status: store.DefStatusActive,
		OwnerOrganizationID: &owner,
	}, nil))
	require.NoError(t, seedStore.Values().Create(ctx, &store.ValuesRevision{
		ID: revisionID, ReleaseDefinitionID: definitionID, Version: 1, StateVersion: 1,
		Status: store.ValuesStatusApproved, CanonicalDocument: []byte(`{"replicas":1}`),
		Digest: "sha256:preflight-e2e", CreatedByUserID: userID,
	}))
	require.NoError(t, seedStore.Bundles().Create(ctx, &store.ReleaseBundle{
		ID: bundleID, Name: "preflight-e2e-bundle", DigestAlg: "sha256", DigestValue: "preflight-e2e-digest",
		Status: store.BundleValidated, Images: []store.BundleImage{{Ref: "registry/app:v1", Digest: "sha256:preflight-e2e-image"}},
	}))
	// Active operator so artifact stages dispatch and poll for results.
	require.NoError(t, seedStore.Operators().Create(ctx, &store.Operator{
		ID: "operator-preflight-e2e", Name: "operator-preflight-e2e", CustomerID: customerID, ClusterID: clusterID,
		CertSerial: "serial-preflight-e2e", Status: store.OperatorActive,
	}))
	require.NoError(t, seedStore.Close())

	mux := http.NewServeMux()
	authStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, authStore.Close()) })
	authServer := newTestAuthorizationServer(t, authStore, signingKey)

	svc := &orchSvc{targetEnv: "staging", signingKey: signingKey, authURL: authServer.URL}
	svc.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}, CA: testCAConfig(t)})
	require.NoError(t, svc.Register(mux, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	// Bump the authorization source version so the Module pulls a fresh
	// snapshot with the seeded membership grants (REQ-027 pattern from
	// TestProductionTrustResolverFailureFailsClosed).
	authSnap, err := svc.store.Authorization().Load(context.Background())
	require.NoError(t, err)
	_, err = svc.store.Authorization().Apply(context.Background(), store.AuthorizationApplyCommand{
		ExpectedSourceVersion: authSnap.SourceVersion,
		ExpectedPolicyVersion: authSnap.PolicyVersion,
		Mutation:              store.AuthorizationMembershipChanged,
	})
	require.NoError(t, err)

	jwtManager := auth.NewJWTManager([]byte(signingKey), time.Hour, time.Hour)
	token, _, err := jwtManager.GenerateAccessToken(userID, organizationID, []string{string(store.RoleReleaseAdmin)})
	require.NoError(t, err)
	client := orchestratorv1connect.NewOrchestratorServiceClient(http.DefaultClient, server.URL)

	createRequest := func(key string) *connect.Request[orchestratorv1.CreateOperationRequest] {
		req := connect.NewRequest(&orchestratorv1.CreateOperationRequest{
			OperationType: "INSTALL", BundleId: bundleID, ReleaseDefinitionId: definitionID, ValuesRevisionId: revisionID,
		})
		req.Header().Set("Authorization", "Bearer "+token)
		req.Header().Set("Idempotency-Key", key)
		return req
	}

	// The first call warms the authorization snapshot; on a stale-snapshot
	// failure the identical request is retried once (same pattern as the
	// values E2E tests).
	createResp, err := client.CreateOperation(ctx, createRequest("create-preflight-e2e"))
	if err != nil {
		require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err), "first create should fail only on stale snapshot: %v", err)
		createResp, err = client.CreateOperation(ctx, createRequest("create-preflight-e2e"))
	}
	require.NoErrorf(t, err, "create operation failed: code=%s err=%v", connect.CodeOf(err), err)
	opID := createResp.Msg.GetOperationId()
	assert.Equal(t, "preflight", createResp.Msg.GetState())

	// AC-019-05: the lifecycle is running and the UOW first dispatch exists.
	pl, err := svc.store.PreflightLifecycles().GetByOperationID(ctx, opID)
	require.NoError(t, err)
	assert.Equal(t, "running", pl.Overall)
	_, err = svc.store.Outbox().GetByCommandID(ctx, opID+":artifact")
	require.NoError(t, err, "D-87 first dispatch must be pre-created by the creation transaction")

	// Drive the four stages to passed through the outbox.
	for _, stage := range []string{"artifact", "render", "cluster", "runtime_pull"} {
		var entry *store.OutboxEntry
		require.Eventually(t, func() bool {
			e, err := svc.store.Outbox().GetByCommandID(ctx, opID+":"+stage)
			if err != nil {
				return false
			}
			entry = e
			return true
		}, 5*time.Second, 50*time.Millisecond)
		require.NoError(t, svc.store.Outbox().UpdateStatus(ctx, entry.ID, store.CommandPersisted, `{"status":"passed"}`))
	}

	// AC-019-04/06: operation CAS to queued, lifecycle passed with canonical stages.
	require.Eventually(t, func() bool {
		op, err := svc.store.Operations().Get(ctx, opID)
		return err == nil && op.Status == store.StatusQueued
	}, 5*time.Second, 50*time.Millisecond)
	// The lifecycle finalization is a separate transaction from the operation
	// CAS (observational write), so poll for the terminal result too — the
	// two-transaction commit window made this flaky under CI's -race full
	// suite (TASK-077 CI failure).
	require.Eventually(t, func() bool {
		pl, err := svc.store.PreflightLifecycles().GetByOperationID(ctx, opID)
		return err == nil && pl.Overall == "passed" && pl.Stages == "artifact,render,dryrun,runtime_pull"
	}, 5*time.Second, 50*time.Millisecond)

	// Restart recovery: an operation left in preflight resumes coordination.
	restartOpID := "op-restart-preflight-e2e"
	require.NoError(t, svc.store.Operations().Create(ctx, &store.Operation{
		ID: restartOpID, OperationType: store.OperationInstall, Status: store.StatusPreflight,
		ReleaseDefinitionID: definitionID, IdempotencyKey: "idem-restart-preflight-e2e",
		RequestHash: "hash-restart-preflight-e2e", StateVersion: 1,
	}))
	require.NoError(t, svc.emergency.Shutdown(context.Background()))
	require.NoError(t, svc.Close())

	// Second generation on the same DB: Register resumes the preflight op.
	mux2 := http.NewServeMux()
	svc2 := &orchSvc{targetEnv: "staging", signingKey: signingKey, authURL: authServer.URL}
	svc2.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}, CA: testCAConfig(t)})
	require.NoError(t, svc2.Register(mux2, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))))
	server2 := httptest.NewServer(mux2)
	t.Cleanup(func() {
		require.NoError(t, svc2.emergency.Shutdown(context.Background()))
		require.NoError(t, svc2.Close())
		server2.Close()
	})

	// The resumed coordinator re-records the lifecycle as running (AC-019-05 retry).
	require.Eventually(t, func() bool {
		pl, err := svc2.store.PreflightLifecycles().GetByOperationID(ctx, restartOpID)
		return err == nil && pl.Overall == "running"
	}, 5*time.Second, 50*time.Millisecond)
}

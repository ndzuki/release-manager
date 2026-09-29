package orchestrator

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	authctx "github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

func TestCreateOperation_IdempotencyIsScopedByIdentityAndResource(t *testing.T) {
	svc, st, cleanup := setupService(t)
	t.Cleanup(cleanup)
	seedDefinition(t, st)
	seedActorScope(t, st)
	seedDefinitionForScope(t, st)
	linkCurrentBundle(t, st, "def-002", "bundle-002")

	first, firstCtx := createOperationRequestForScope("idem-shared", "user-001", "org-001", "def-001", "bundle-001", "vr-001")
	firstResponse, err := svc.CreateOperation(firstCtx, first)
	require.NoError(t, err)

	replayReq, replayCtx := createOperationRequestForScope(
		"idem-shared", "user-001", "org-001", "def-001", "bundle-001", "vr-001",
	)
	replayResponse, err := svc.CreateOperation(replayCtx, replayReq)
	require.NoError(t, err)
	assert.Equal(t, firstResponse.Msg.GetOperationId(), replayResponse.Msg.GetOperationId())

	otherReq, otherCtx := createOperationRequestForScope(
		"idem-shared", "user-002", "org-002", "def-002", "bundle-002", "values-def-002",
	)
	otherIdentityResponse, err := svc.CreateOperation(otherCtx, otherReq)
	require.NoError(t, err)
	assert.NotEqual(t, firstResponse.Msg.GetOperationId(), otherIdentityResponse.Msg.GetOperationId())
}

func TestCreateOperation_IdempotencyConflictUsesRequestHash(t *testing.T) {
	svc, st, cleanup := setupService(t)
	t.Cleanup(cleanup)
	seedDefinition(t, st)

	first, firstCtx := createOperationRequestForScope(
		"idem-conflict", "user-001", "org-001", "def-001", "bundle-001", "vr-001",
	)
	_, err := svc.CreateOperation(firstCtx, first)
	require.NoError(t, err)

	conflicting, conflictCtx := createOperationRequestForScope(
		"idem-conflict", "user-001", "org-001", "def-001", "bundle-002", "vr-001",
	)
	_, err = svc.CreateOperation(conflictCtx, conflicting)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func TestRollbackRelease_IdempotencyReplaysAndDetectsConflict(t *testing.T) {
	svc, st, cleanup := setupService(t)
	t.Cleanup(cleanup)
	seedDefinition(t, st)
	seedRollbackInventory(t, st)

	request, ctx := rollbackRequestForScope("rollback-key", "user-001", "org-001", 1)
	first, err := svc.RollbackRelease(ctx, request)
	require.NoError(t, err)

	replayReq, replayCtx := rollbackRequestForScope("rollback-key", "user-001", "org-001", 1)
	replay, err := svc.RollbackRelease(replayCtx, replayReq)
	require.NoError(t, err)
	assert.Equal(t, first.Msg.GetOperationId(), replay.Msg.GetOperationId())

	conflictReq, conflictCtx := rollbackRequestForScope("rollback-key", "user-001", "org-001", 2)
	_, err = svc.RollbackRelease(conflictCtx, conflictReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

// createOperationRequestForScope builds a CreateOperation request with the
// idempotency key on the HTTP header and returns it with an actor context
// (REQ-067: actor from interceptor, key from header).
func createOperationRequestForScope(
	key, userID, organizationID, definitionID, bundleID, valuesRevisionID string,
) (*connect.Request[orchestratorv1.CreateOperationRequest], context.Context) {
	req := connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:           "INSTALL",
		BundleId:                bundleID,
		ReleaseDefinitionId:     definitionID,
		ValuesRevisionId:        valuesRevisionID,
		ExpectedCurrentRevision: 1,
	})
	req.Header().Set("Idempotency-Key", key)
	ctx := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: userID, OrganizationID: organizationID, Roles: []string{string(store.RoleReleaseAdmin)},
	})
	return req, ctx
}

func rollbackRequestForScope(key, userID, organizationID string, targetRevision int32) (*connect.Request[orchestratorv1.RollbackReleaseRequest], context.Context) {
	req := connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-001",
		TargetRevision:          targetRevision,
		ExpectedCurrentRevision: 3,
		Reason:                  fmt.Sprintf("restore revision %d", targetRevision),
	})
	req.Header().Set("Idempotency-Key", key)
	ctx := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: userID, OrganizationID: organizationID, Roles: []string{string(store.RoleReleaseAdmin)},
	})
	return req, ctx
}

func seedDefinitionForScope(t *testing.T, st store.Store) {
	t.Helper()
	definitionID := "def-002"
	customerID := "cust-002"
	clusterID := "cluster-002"
	ctx := t.Context()
	if _, err := st.Customers().Get(ctx, customerID); err != nil {
		require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: customerID, Name: customerID, Slug: customerID, Status: store.CustomerActive}))
	}
	if _, err := st.Clusters().Get(ctx, clusterID); err != nil {
		require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: clusterID, CustomerID: customerID, Name: clusterID, Status: store.ClusterActive}))
	}
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: definitionID, Name: definitionID, CustomerID: customerID, ClusterID: clusterID,
		Namespace: "default", ReleaseName: definitionID, ChartName: "nginx", Status: store.DefStatusActive,
	}, nil))
	seedValuesRevision(t, st, "values-"+definitionID, definitionID, store.ValuesStatusApproved)
}

// seedActorScope creates the SECOND organization used by the cross-tenant tests: a
// different organization serving a different customer, which is what makes the tenant
// boundary observable (two organizations bound to the SAME customer legitimately share
// that customer's bundles).
func seedActorScope(t *testing.T, st store.Store) {
	t.Helper()
	organizationID := "org-002"
	userID := "user-002"
	customerID := "cust-002"
	ctx := t.Context()
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: organizationID, Name: organizationID}))
	require.NoError(t, st.Users().Create(ctx, &store.User{ID: userID, Username: userID, Status: store.UserActive}))
	require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{
		ID: "binding-" + organizationID, OrgID: organizationID, CustomerID: customerID,
	}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: organizationID, UserID: userID, Role: store.RoleReleaseAdmin,
	}))
}

// ── TASK-215: the bundle a write path may reference ──────────────────

// The reported hole: authorizing the NAMED definition is not a statement about the bundle.
// Before this gate the only relationship was a chart-name match, so a caller could point
// its own definition's operation at another organization's same-named-chart bundle.
func TestCreateOperationRefusesAnotherOrganizationsBundle(t *testing.T) {
	svc, st, cleanup := setupService(t)
	t.Cleanup(cleanup)
	seedDefinition(t, st)
	seedActorScope(t, st)
	seedDefinitionForScope(t, st)

	// org-002 may read def-002, but bundle-001 belongs to org-001's definition.
	reachable, reachErr := st.Bundles().ReachableFromOrganization(t.Context(), "bundle-001", "org-002")
	require.NoError(t, reachErr)
	claimed, claimErr := st.Bundles().BundleClaimedOutsideOrganization(t.Context(), "bundle-001", "org-002")
	require.NoError(t, claimErr)
	require.False(t, reachable, "sanity: org-002 must not reach org-001's bundle")
	require.True(t, claimed, "sanity: the bundle is claimed by another organization")

	req, ctx := createOperationRequestForScope(
		"cross-org", "user-002", "org-002", "def-002", "bundle-001", "values-def-002",
	)
	_, err := svc.CreateOperation(ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "bundle_not_reachable")

	// The owner organization still deploys it.
	ownReq, ownCtx := createOperationRequestForScope(
		"own-org", "user-001", "org-001", "def-001", "bundle-001", "vr-001",
	)
	_, err = svc.CreateOperation(ownCtx, ownReq)
	require.NoError(t, err)
}

// The reachability gate runs BEFORE the bundle-status switch: a foreign caller must not
// learn whether someone else's bundle is received/rejected/archived from our error codes.
func TestCreateOperationDoesNotLeakForeignBundleState(t *testing.T) {
	svc, st, cleanup := setupService(t)
	t.Cleanup(cleanup)
	seedDefinition(t, st)
	seedActorScope(t, st)
	seedDefinitionForScope(t, st)
	require.NoError(t, st.Bundles().Create(t.Context(), &store.ReleaseBundle{
		ID: "bundle-foreign-received", Name: "foreign", DigestAlg: "sha256",
		DigestValue: fmt.Sprintf("%064x", 55), Status: store.BundleReceived,
		ChartRef: "nginx", CreatedAt: time.Now().UTC(),
	}))
	// Another organization claims it. The raw write builds a state the product does not
	// produce (an independent review found no writer that turns a validated bundle back
	// into received); without a claim the bundle is adoptable and the STATUS gate would
	// answer instead, which is not the leak this test is about.
	forceCurrentBundle(t, st, "def-001", "bundle-foreign-received")

	req, ctx := createOperationRequestForScope(
		"no-leak", "user-002", "org-002", "def-002", "bundle-foreign-received", "values-def-002",
	)
	_, err := svc.CreateOperation(ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "the status gate must not answer first")
	assert.Contains(t, err.Error(), "bundle_not_reachable")
	assert.NotContains(t, err.Error(), "bundle_not_ready")
}

// Platform admins and internal service callers keep the global view, mirroring GetBundle.
func TestCreateOperationBundleReachabilityExemptions(t *testing.T) {
	svc, st, cleanup := setupService(t)
	t.Cleanup(cleanup)
	seedDefinition(t, st)
	seedActorScope(t, st)
	seedDefinitionForScope(t, st)

	adminReq, _ := createOperationRequestForScope(
		"admin-exempt", "user-002", "org-002", "def-002", "bundle-001", "values-def-002",
	)
	adminCtx := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-002", OrganizationID: "org-002", Roles: []string{string(store.RolePlatformAdmin)},
	})
	_, err := svc.CreateOperation(adminCtx, adminReq)
	require.NoError(t, err, "a platform admin keeps the global view")

	// An internal service caller is NOT exempt from the definition authorization on this
	// write path (authorizeOperationActor runs first and requires the customer grant), so
	// the `actor.Service` branch of the reachability gate is defensive: it keeps a
	// service-to-service caller from being stopped by a tenant filter it has no
	// organization for. The authorization refusal below is therefore the expected answer,
	// and it proves the reachability check does not run before authorization.
	internalReq, _ := createOperationRequestForScope(
		"svc-exempt", "system", "org-001", "def-001", "bundle-001", "vr-001",
	)
	internalCtx := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "system", OrganizationID: "org-001", Service: "orchestrator",
	})
	_, err = svc.CreateOperation(internalCtx, internalReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "permission_denied", "the definition authorization answers first")
}

// The rule must not break a first install: a bundle nobody has claimed yet is adoptable,
// because the definition only becomes its owner when THIS operation is created
// (setCurrentBundle runs inside the operation-creation unit of work). The dev seed failed
// exactly here while the rule was plain reachability.
func TestCreateOperationAcceptsAnUnclaimedBundle(t *testing.T) {
	svc, st, cleanup := setupService(t)
	t.Cleanup(cleanup)
	seedDefinition(t, st)
	require.NoError(t, st.Bundles().Create(t.Context(), &store.ReleaseBundle{
		ID: "bundle-fresh", Name: "fresh", DigestAlg: "sha256",
		DigestValue: fmt.Sprintf("%064x", 57), Status: store.BundleValidated,
		ChartRef: "nginx", CreatedAt: time.Now().UTC(),
	}))

	req, ctx := createOperationRequestForScope(
		"fresh-install", "user-001", "org-001", "def-001", "bundle-fresh", "vr-001",
	)
	_, err := svc.CreateOperation(ctx, req)
	require.NoError(t, err, "an unclaimed bundle must be installable")

	// ... and once adopted, the definition owns it.
	definition, err := st.Definitions().Get(t.Context(), "def-001")
	require.NoError(t, err)
	require.NotNil(t, definition.CurrentBundleID)
	assert.Equal(t, "bundle-fresh", *definition.CurrentBundleID)
}

// KNOWN LIMITATION, pinned so it cannot regress silently (TASK-216 needs a product
// decision on recording who submitted a bundle):
//
// This bundle belongs to nobody yet -- it was never deployed, and a bundle carries no
// submitter attribution to check -- so ANOTHER organization may adopt it. The consequence
// is not only "the other tenant got a bundle": once adopted, the original submitter's own
// first install is refused, because the bundle is then claimed by a definition outside its
// organization. The test asserts the current behaviour deliberately; when attribution
// lands, this test must flip to expecting a refusal.
func TestCreateOperationAdoptsAnUnclaimedBundleFromAnotherOrganization(t *testing.T) {
	svc, st, cleanup := setupService(t)
	t.Cleanup(cleanup)
	seedDefinition(t, st)
	seedActorScope(t, st)
	seedDefinitionForScope(t, st)

	require.NoError(t, st.Bundles().Create(t.Context(), &store.ReleaseBundle{
		ID: "bundle-unclaimed-foreign", Name: "unclaimed", DigestAlg: "sha256",
		DigestValue: fmt.Sprintf("%064x", 58), Status: store.BundleValidated,
		ChartRef: "nginx", CreatedAt: time.Now().UTC(),
	}))

	// org-002 (a different organization, a different customer) adopts it.
	req, ctx := createOperationRequestForScope(
		"adopt-unclaimed", "user-002", "org-002", "def-002", "bundle-unclaimed-foreign", "values-def-002",
	)
	_, err := svc.CreateOperation(ctx, req)
	require.NoError(t, err, "KNOWN LIMITATION: an unclaimed bundle is adoptable (TASK-216)")

	// And the owner of the definition it was submitted for can no longer adopt it.
	ownerReq, ownerCtx := createOperationRequestForScope(
		"adopt-lost", "user-001", "org-001", "def-001", "bundle-unclaimed-foreign", "vr-001",
	)
	_, err = svc.CreateOperation(ownerCtx, ownerReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"the adoption above now blocks the original organization: that is why TASK-216 matters")
}

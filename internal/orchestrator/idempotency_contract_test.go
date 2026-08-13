package orchestrator

import (
	"context"
	"fmt"
	"testing"

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
	seedActorScope(t, st, "org-002", "user-002", "cust-001")
	seedDefinitionForScope(t, st, "def-002", "cust-001", "cluster-001")

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

func seedDefinitionForScope(t *testing.T, st store.Store, definitionID, customerID, clusterID string) {
	t.Helper()
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

func seedActorScope(t *testing.T, st store.Store, organizationID, userID, customerID string) {
	t.Helper()
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

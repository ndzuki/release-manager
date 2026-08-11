package orchestrator

import (
	"context"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

func TestCreateOperation_IdempotencyIsScopedByIdentityAndResource(t *testing.T) {
	svc, st, cleanup := setupService(t)
	t.Cleanup(cleanup)
	seedDefinition(t, st)
	seedActorScope(t, st, "org-002", "user-002", "cust-001")
	seedDefinitionForScope(t, st, "def-002", "cust-001", "cluster-001")

	first := createOperationRequestForScope("idem-shared", "user-001", "org-001", "def-001", "bundle-001", "vr-001")
	firstResponse, err := svc.CreateOperation(context.Background(), connect.NewRequest(first))
	require.NoError(t, err)

	replayResponse, err := svc.CreateOperation(context.Background(), connect.NewRequest(createOperationRequestForScope(
		"idem-shared", "user-001", "org-001", "def-001", "bundle-001", "vr-001",
	)))
	require.NoError(t, err)
	assert.Equal(t, firstResponse.Msg.GetOperationId(), replayResponse.Msg.GetOperationId())

	otherIdentityResponse, err := svc.CreateOperation(context.Background(), connect.NewRequest(createOperationRequestForScope(
		"idem-shared", "user-002", "org-002", "def-002", "bundle-002", "values-def-002",
	)))
	require.NoError(t, err)
	assert.NotEqual(t, firstResponse.Msg.GetOperationId(), otherIdentityResponse.Msg.GetOperationId())
}

func TestCreateOperation_IdempotencyConflictUsesRequestHash(t *testing.T) {
	svc, st, cleanup := setupService(t)
	t.Cleanup(cleanup)
	seedDefinition(t, st)

	_, err := svc.CreateOperation(context.Background(), connect.NewRequest(createOperationRequestForScope(
		"idem-conflict", "user-001", "org-001", "def-001", "bundle-001", "vr-001",
	)))
	require.NoError(t, err)

	_, err = svc.CreateOperation(context.Background(), connect.NewRequest(createOperationRequestForScope(
		"idem-conflict", "user-001", "org-001", "def-001", "bundle-002", "vr-001",
	)))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func TestRollbackRelease_IdempotencyReplaysAndDetectsConflict(t *testing.T) {
	svc, st, cleanup := setupService(t)
	t.Cleanup(cleanup)
	seedDefinition(t, st)

	request := rollbackRequestForScope("rollback-key", "user-001", "org-001", 1)
	first, err := svc.RollbackRelease(context.Background(), connect.NewRequest(request))
	require.NoError(t, err)

	replay, err := svc.RollbackRelease(context.Background(), connect.NewRequest(rollbackRequestForScope(
		"rollback-key", "user-001", "org-001", 1,
	)))
	require.NoError(t, err)
	assert.Equal(t, first.Msg.GetOperationId(), replay.Msg.GetOperationId())

	_, err = svc.RollbackRelease(context.Background(), connect.NewRequest(rollbackRequestForScope(
		"rollback-key", "user-001", "org-001", 2,
	)))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func createOperationRequestForScope(
	key, userID, organizationID, definitionID, bundleID, valuesRevisionID string,
) *orchestratorv1.CreateOperationRequest {
	return &orchestratorv1.CreateOperationRequest{
		OperationType:           "INSTALL",
		BundleId:                bundleID,
		ReleaseDefinitionId:     definitionID,
		ValuesRevisionId:        valuesRevisionID,
		ExpectedCurrentRevision: 1,
		IdempotencyKey:          key,
		Actor:                   &commonv1.ActorContext{UserId: userID, Organization: organizationID},
	}
}

func rollbackRequestForScope(key, userID, organizationID string, targetRevision int32) *orchestratorv1.RollbackReleaseRequest {
	return &orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-001",
		TargetRevision:          targetRevision,
		ExpectedCurrentRevision: 3,
		Reason:                  fmt.Sprintf("restore revision %d", targetRevision),
		IdempotencyKey:          key,
		Actor:                   &commonv1.ActorContext{UserId: userID, Organization: organizationID},
	}
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
		Namespace: "default", ReleaseName: definitionID, Status: store.DefStatusActive,
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
		OrgID: organizationID, UserID: userID, Role: store.RoleDeployer,
	}))
}

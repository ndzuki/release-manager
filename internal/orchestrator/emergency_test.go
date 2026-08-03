package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

type recordingEmergencyDispatcher struct {
	commands []*operatorv1.EmergencyCommand
	err      error
}

func (d *recordingEmergencyDispatcher) DispatchEmergency(_ context.Context, _ string, command *operatorv1.EmergencyCommand) error {
	if d.err != nil {
		return d.err
	}
	d.commands = append(d.commands, command)
	return nil
}

func emergencyTestService(t *testing.T) (*Service, store.Store, *recordingEmergencyDispatcher) {
	t.Helper()
	svc, st, _ := setupService(t)
	seedDefinition(t, st)
	require.NoError(t, st.Users().Create(t.Context(), &store.User{
		ID: "release-admin", Username: "release-admin", Status: store.UserActive,
	}))
	require.NoError(t, st.OrgMembers().Create(t.Context(), &store.OrganizationMember{
		OrgID: "org-001", UserID: "release-admin", Role: store.RoleReleaseAdmin,
	}))
	require.NoError(t, st.Clusters().Create(t.Context(), &store.Cluster{ID: "cls-001", Name: "cls-001", CustomerID: "cust-001"}))
	require.NoError(t, st.Operators().Create(t.Context(), &store.Operator{ID: "op-001", Name: "op-001", CustomerID: "cust-001", ClusterID: "cls-001"}))
	require.NoError(t, st.Sessions().Create(t.Context(), &store.Session{
		ID: uuid.NewString(), OperatorID: "op-001", Status: store.SessionOnline, InstanceID: "inst-1",
		StartedAt: time.Now().UTC(), LastHeartbeat: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour),
	}))
	dispatcher := &recordingEmergencyDispatcher{}
	svc.emergencyDispatcher = dispatcher
	return svc, st, dispatcher
}
func emergencyAdminContext() context.Context {
	return authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "release-admin", OrganizationID: "org-001", Roles: []string{string(store.RoleReleaseAdmin)},
	})
}

func emergencyReplicasRequest(key string, replicas int32) *connect.Request[orchestratorv1.EmergencyChangeRequest] {
	return connect.NewRequest(&orchestratorv1.EmergencyChangeRequest{
		ReleaseDefinitionId: "def-001", IdempotencyKey: key,
		Reason:      "restore service during incident",
		WorkloadRef: &orchestratorv1.WorkloadRef{Kind: workloadDeployment, Name: "api", Namespace: "default", Uid: "uid-api"},
		Change:      &orchestratorv1.EmergencyChangeRequest_SetReplicas{SetReplicas: &orchestratorv1.SetReplicas{Replicas: replicas}},
		Convergence: orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REVERT_ON_NEXT_RECONCILE,
	})
}

func TestEmergencyChangeRejectsUnauthenticated(t *testing.T) {
	svc, _, _ := emergencyTestService(t)
	_, err := svc.EmergencyChange(context.Background(), emergencyReplicasRequest("unauthed-3", 3))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestEmergencyChangeRejectsHPAReplicas(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	def, err := st.Definitions().Get(t.Context(), "def-001")
	require.NoError(t, err)
	def.HPAManaged = true
	_, err = st.Definitions().Update(t.Context(), def, nil)
	require.NoError(t, err)
	_, err = svc.EmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("hpa-replicas", 3))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestEmergencyChangePersistsAndDispatchesReplicas(t *testing.T) {
	svc, st, dispatcher := emergencyTestService(t)
	resp, err := svc.EmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("replicas-2", 2))
	require.NoError(t, err)
	assert.Equal(t, string(store.StatusQueued), resp.Msg.GetStatus())
	assert.Len(t, dispatcher.commands, 1)
	assert.Equal(t, int32(2), dispatcher.commands[0].GetSetReplicas().GetReplicas())
	intent, err := st.EmergencyIntents().GetByOperationID(t.Context(), resp.Msg.GetOperationId())
	require.NoError(t, err)
	assert.Equal(t, "queued", intent.DeliveryStatus)
}

func TestEmergencyChangeIdempotency(t *testing.T) {
	svc, _, dispatcher := emergencyTestService(t)
	first, err := svc.EmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("same-replicas", 3))
	require.NoError(t, err)
	second, err := svc.EmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("same-replicas", 3))
	require.NoError(t, err)
	assert.Equal(t, first.Msg.GetOperationId(), second.Msg.GetOperationId())
	assert.Len(t, dispatcher.commands, 1)
	_, err = svc.EmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("same-replicas", 4))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func TestEmergencyChangeAuthorizationDenyHasNoWriteSideEffects(t *testing.T) {
	svc, st, dispatcher := emergencyTestService(t)
	deployerID := "emergency-deployer"
	require.NoError(t, st.Users().Create(t.Context(), &store.User{ID: deployerID, Username: deployerID, Status: store.UserActive}))
	require.NoError(t, st.OrgMembers().Create(t.Context(), &store.OrganizationMember{OrgID: "org-001", UserID: deployerID, Role: store.RoleDeployer}))
	ctx := authctx.WithActor(context.Background(), authctx.Actor{UserID: deployerID, OrganizationID: "org-001"})
	_, err := svc.EmergencyChange(ctx, emergencyReplicasRequest("deny-no-write", 3))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Empty(t, dispatcher.commands)
	var operations, intents, idempotency int
	storeWithDB, storeHasDB := st.(interface{ DB() *sql.DB })
	require.True(t, storeHasDB)
	storeDB := storeWithDB.DB()
	require.NoError(t, storeDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM operations`).Scan(&operations))
	require.NoError(t, storeDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM emergency_intents`).Scan(&intents))
	require.NoError(t, storeDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM idempotency_records`).Scan(&idempotency))
	assert.Zero(t, operations)
	assert.Zero(t, intents)
	assert.Zero(t, idempotency)
}

func TestEmergencyChangeFieldLocks(t *testing.T) {
	svc, _, _ := emergencyTestService(t)
	_, err := svc.EmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("lock-1", 3))
	require.NoError(t, err)
	_, err = svc.EmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("lock-2", 4))
	require.Error(t, err)
}

func TestEmergencyChangeRejectedByStandardOperation(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	require.NoError(t, st.Operations().Create(t.Context(), &store.Operation{
		ID: uuid.NewString(), OperationType: store.OperationUpgrade, Status: store.StatusRunning,
		ReleaseDefinitionID: "def-001", IdempotencyKey: uuid.NewString(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))
	_, err := svc.EmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("blocked", 3))
	require.Error(t, err)
	assert.Equal(t, "release_busy", connectErrorReason(err))
}

func TestEmergencyChangeDefinitionNotFound(t *testing.T) {
	svc, _, _ := emergencyTestService(t)
	req := emergencyReplicasRequest("not-found", 3)
	req.Msg.ReleaseDefinitionId = "nonexistent"
	_, err := svc.EmergencyChange(emergencyAdminContext(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestExpireEmergencyOperationsTimesOutOverdueOperation(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	resp, err := svc.EmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("timeout-emergency", 3))
	require.NoError(t, err)
	_, err = st.Operations().UpdateStatus(t.Context(), resp.Msg.GetOperationId(), store.StatusRunning, 2, "")
	require.NoError(t, err)
	sqliteStore, ok := st.(interface{ DB() *sql.DB })
	require.True(t, ok)
	_, err = sqliteStore.DB().ExecContext(t.Context(), `UPDATE operations SET deadline = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Second).Format(time.RFC3339), resp.Msg.GetOperationId())
	require.NoError(t, err)

	assert.Equal(t, 1, svc.ExpireEmergencyOperations(t.Context()))
	operation, err := st.Operations().Get(t.Context(), resp.Msg.GetOperationId())
	require.NoError(t, err)
	assert.Equal(t, store.StatusTimeout, operation.Status)
	assert.Equal(t, "operation_timeout", operation.LastError)
}

func TestGetOperationReturnsEmergencyResult(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	resp, err := svc.EmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("get-emergency", 3))
	require.NoError(t, err)
	intent, err := st.EmergencyIntents().GetByOperationID(t.Context(), resp.Msg.GetOperationId())
	require.NoError(t, err)
	op, err := st.Operations().Get(t.Context(), resp.Msg.GetOperationId())
	require.NoError(t, err)
	_, err = st.Operations().UpdateStatus(t.Context(), op.ID, store.StatusRunning, op.StateVersion, "")
	require.NoError(t, err)
	op, err = st.Operations().Get(t.Context(), op.ID)
	require.NoError(t, err)
	_, err = st.EmergencyIntents().Finish(
		t.Context(), intent.ID, op.ID, op.StateVersion, store.StatusSucceeded, "",
		[]byte(`{"workload_uid":"uid-api","replicas":2}`),
		[]byte(`{"workload_uid":"uid-api","replicas":3}`),
	)
	require.NoError(t, err)

	getResp, err := svc.GetOperation(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.GetOperationRequest{
		OperationId: op.ID,
	}))
	require.NoError(t, err)
	require.NotNil(t, getResp.Msg.GetEmergencyResult())
	assert.Equal(t, orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_REPLICAS, getResp.Msg.GetEmergencyResult().GetOpType())
	assert.Equal(t, int32(2), getResp.Msg.GetEmergencyResult().GetBefore().GetReplicasValues().GetReplicas())
	assert.Equal(t, int32(3), getResp.Msg.GetEmergencyResult().GetAfter().GetReplicasValues().GetReplicas())
	assert.Equal(t, "awaiting_standard_release", getResp.Msg.GetEmergencyResult().GetRevertStatus())
}

func connectErrorReason(err error) string {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr.Meta().Get("X-Reason-Code")
	}
	return ""
}

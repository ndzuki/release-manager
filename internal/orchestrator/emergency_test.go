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
		t.Context(), intent.ID, op.ID, op.StateVersion, store.StatusSucceeded, store.EmergencyEffectApplied, "",
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

// AC-023-13: running EMERGENCY → cancel_not_allowed
func TestCancelOperation_EmergencyRunningRejected(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	resp, err := svc.EmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("cancel-em-running", 3))
	require.NoError(t, err)
	opID := resp.Msg.GetOperationId()
	op, err := st.Operations().Get(t.Context(), opID)
	require.NoError(t, err)
	_, err = st.Operations().UpdateStatus(t.Context(), opID, store.StatusRunning, op.StateVersion, "")
	require.NoError(t, err)

	req := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: opID, Reason: "attempt cancel", ExpectedStateVersion: int64(op.StateVersion + 1),
	})
	req.Header().Set("Idempotency-Key", "cancel-em-running-key")
	_, cancelErr := svc.CancelOperation(emergencyAdminContext(), req)
	require.Error(t, cancelErr)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(cancelErr))
	assert.Contains(t, cancelErr.Error(), "cancel_not_allowed")
}

// AC-023-13: pending EMERGENCY → cancelled
func TestCancelOperation_EmergencyPendingSucceeds(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	// EmergencyChange with working dispatcher → operation is created and queued.
	// queued EMERGENCY is cancellable (AC-023-13: pending/queued EMERGENCY → cancelled).
	resp, err := svc.EmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("cancel-em-pending", 3))
	require.NoError(t, err)
	opID := resp.Msg.GetOperationId()

	// Operation should be queued (dispatched successfully).
	opCheck, err := st.Operations().Get(t.Context(), opID)
	require.NoError(t, err)
	require.Equal(t, store.StatusQueued, opCheck.Status)

	req := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: opID, Reason: "cancelling queued emergency", ExpectedStateVersion: int64(opCheck.StateVersion),
	})
	req.Header().Set("Idempotency-Key", "cancel-em-queued-key")
	cancelResp, cancelErr := svc.CancelOperation(emergencyAdminContext(), req)
	require.NoError(t, cancelErr)
	assert.Equal(t, orchestratorv1.OperationStatus_OPERATION_STATUS_CANCELLED, cancelResp.Msg.Operation.State)
}

// AC-023-13: queued undelivered EMERGENCY → cancelled + NOT_APPLIED
func TestCancelOperation_EmergencyQueuedUndelivered(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	resp, err := svc.EmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("cancel-em-queued", 3))
	require.NoError(t, err)
	opID := resp.Msg.GetOperationId()
	intent, err := st.EmergencyIntents().GetByOperationID(t.Context(), opID)
	require.NoError(t, err)
	op, err := st.Operations().Get(t.Context(), opID)
	require.NoError(t, err)
	require.NoError(t, st.EmergencyIntents().UpdateDeliveryStatus(t.Context(), intent.ID, "pending"))

	req := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: opID, Reason: "cancelling queued undelivered", ExpectedStateVersion: int64(op.StateVersion),
	})
	req.Header().Set("Idempotency-Key", "cancel-em-queued-key")
	cancelResp, cancelErr := svc.CancelOperation(emergencyAdminContext(), req)
	require.NoError(t, cancelErr)
	assert.Equal(t, orchestratorv1.OperationStatus_OPERATION_STATUS_CANCELLED, cancelResp.Msg.Operation.State)

	updatedIntent, err := st.EmergencyIntents().GetByOperationID(t.Context(), opID)
	require.NoError(t, err)
	assert.Equal(t, store.EmergencyEffectNotApplied, updatedIntent.EffectStatus)
}

func connectErrorReason(err error) string {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr.Meta().Get("X-Reason-Code")
	}
	return ""
}

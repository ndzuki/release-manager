package orchestrator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"connectrpc.com/connect"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// createEmergencyPendingViaUOW creates one pending EMERGENCY operation +
// intent (REVERT convergence, no task) on def-001 through the shared
// OperationCreationUnitOfWork seam.
func createEmergencyPendingViaUOW(t *testing.T, st store.Store, key string) *store.OperationCreationResult {
	t.Helper()
	return createEmergencyPendingOn(t, st, "def-001", "api", key)
}

// createEmergencyPendingOn creates a pending EMERGENCY op + intent (REVERT,
// no convergence task) on an explicit definition and workload so tests can
// place several stuck/active locks side by side (the UOW enforces one
// in-flight EMERGENCY per definition and field-lock conflicts per target).
func createEmergencyPendingOn(t *testing.T, st store.Store, definitionID, workloadName, key string) *store.OperationCreationResult {
	t.Helper()
	uowStore, ok := st.(interface {
		store.Store
		OperationCreationUnitOfWork() store.OperationCreationUnitOfWork
	})
	require.True(t, ok)
	now := time.Now().UTC()
	opID := uuid.NewString()
	replicas := int32(2)
	intent := &store.EmergencyIntent{
		ID: uuid.NewString(), ReleaseDefinitionID: definitionID, OperationID: opID, CommandID: uuid.NewString(),
		Action: store.EmergencySetReplicas, WorkloadKind: workloadDeployment, WorkloadName: workloadName,
		WorkloadNamespace: "default", WorkloadUID: "uid-image-0001",
		Convergence: store.EmergencyRevertOnNextReconcile, TargetReplicas: &replicas,
	}
	hash := sha256.Sum256([]byte(key))
	keyHash := hex.EncodeToString(hash[:])
	scope := "org-001:" + definitionID
	cmd := store.EmergencyCreateCommand{
		Operation: &store.Operation{
			ID: opID, OperationType: store.OperationEmergency, Status: store.StatusPending,
			ReleaseDefinitionID: definitionID, IdempotencyKey: keyHash, IdempotencyScope: scope,
			RequestHash: "hash-" + key, CreatedAt: now, UpdatedAt: now,
		},
		Intent:               intent,
		IdempotencyScope:     scope,
		IdempotencyKeyHash:   keyHash,
		RequestHash:          "hash-" + key,
		IdempotencyExpiresAt: now.Add(time.Hour),
	}
	created, err := uowStore.OperationCreationUnitOfWork()(context.Background(), store.OperationCreationRequest{
		Operation: cmd.Operation, Emergency: &cmd,
	})
	require.NoError(t, err)
	return created
}

func sqliteExec(t *testing.T, st store.Store) *sql.DB {
	t.Helper()
	sqliteStore, ok := st.(interface{ DB() *sql.DB })
	require.True(t, ok)
	return sqliteStore.DB()
}

// TestExpireEmergencyOperations_PendingReleasesNotApplied (REQ-087 AC-087-04,
// D8): a pending (never-dispatched) EMERGENCY op that hits its deadline is
// provably undelivered — it times out with effect NOT_APPLIED, releasing the
// target lock (no permanent UNKNOWN lock).
func TestExpireEmergencyOperations_PendingReleasesNotApplied(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	created := createEmergencyPendingViaUOW(t, st, "timeout-pending")
	require.Equal(t, store.StatusPending, created.Operation.Status)
	_, err := sqliteExec(t, st).ExecContext(t.Context(), `UPDATE operations SET deadline = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Second).Format(time.RFC3339), created.Operation.ID)
	require.NoError(t, err)

	assert.Equal(t, 1, svc.ExpireEmergencyOperations(t.Context()))
	op, err := st.Operations().Get(t.Context(), created.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusTimeout, op.Status)
	assert.Equal(t, store.EmergencyEffectNotApplied, op.EffectStatus)
	intent, err := st.EmergencyIntents().GetByOperationID(t.Context(), created.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, store.EmergencyEffectNotApplied, intent.EffectStatus)

	// The lock is released: a fresh emergency on the same target is accepted.
	next := createEmergencyPendingViaUOW(t, st, "timeout-pending-2")
	queued, err := st.Operations().UpdateStatus(t.Context(), next.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	_, err = st.EmergencyIntents().Finish(t.Context(), next.Intent.ID, next.Operation.ID, queued.StateVersion,
		store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
}

// TestExpireEmergencyOperations_QueuedRetainsUnknown (REQ-087 AC-087-05, D8):
// a queued (dispatch-initiated, possibly executed) op that times out keeps
// effect UNKNOWN so a late result can still resolve it — the lock is retained.
func TestExpireEmergencyOperations_QueuedRetainsUnknown(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	created := createEmergencyPendingViaUOW(t, st, "timeout-queued")
	queued, err := st.Operations().UpdateStatus(t.Context(), created.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	require.Equal(t, 2, queued.StateVersion)
	_, err = sqliteExec(t, st).ExecContext(t.Context(), `UPDATE operations SET deadline = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Second).Format(time.RFC3339), created.Operation.ID)
	require.NoError(t, err)

	assert.Equal(t, 1, svc.ExpireEmergencyOperations(t.Context()))
	op, err := st.Operations().Get(t.Context(), created.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusTimeout, op.Status)
	assert.Equal(t, store.EmergencyEffectUnknown, op.EffectStatus)
	intent, err := st.EmergencyIntents().GetByOperationID(t.Context(), created.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, store.EmergencyEffectUnknown, intent.EffectStatus)
}

// TestExpireEmergencyOperations_RunningRetainsUnknown: ACK_PERSISTED received
// — a running op that times out keeps UNKNOWN (AC-032-31 late-result window).
func TestExpireEmergencyOperations_RunningRetainsUnknown(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	created := createEmergencyPendingViaUOW(t, st, "timeout-running")
	queued, err := st.Operations().UpdateStatus(t.Context(), created.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	running, err := st.Operations().UpdateStatus(t.Context(), created.Operation.ID, store.StatusRunning, queued.StateVersion, "")
	require.NoError(t, err)
	_, err = sqliteExec(t, st).ExecContext(t.Context(), `UPDATE operations SET deadline = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Second).Format(time.RFC3339), created.Operation.ID)
	require.NoError(t, err)

	assert.Equal(t, 1, svc.ExpireEmergencyOperations(t.Context()))
	op, err := st.Operations().Get(t.Context(), created.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusTimeout, op.Status)
	assert.Equal(t, running.StateVersion+1, op.StateVersion)
	assert.Equal(t, store.EmergencyEffectUnknown, op.EffectStatus)
}

// cancelRequest builds a CancelOperation connect request with the required
// Idempotency-Key header.
func cancelRequest(operationID string, stateVersion int, key string) *connect.Request[orchestratorv1.CancelOperationRequest] {
	req := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: operationID, ExpectedStateVersion: int64(stateVersion), Reason: "emergency cancel test",
	})
	req.Header().Set("Idempotency-Key", key)
	return req
}

// TestCancelEmergency_PendingReleasesNotApplied (REQ-087 AC-087-06): a
// pending EMERGENCY whose command is provably undelivered cancels with
// effect NOT_APPLIED, releasing the target lock.
func TestCancelEmergency_PendingReleasesNotApplied(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	created := createEmergencyPendingViaUOW(t, st, "cancel-pending")
	// intent delivery_status defaults to pending → DeliveryUndelivered.
	intent, err := st.EmergencyIntents().GetByOperationID(t.Context(), created.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", intent.DeliveryStatus)

	resp, err := svc.CancelOperation(emergencyAdminContext(), cancelRequest(created.Operation.ID, 1, "idem-cancel-pending"))
	require.NoError(t, err)
	assert.Equal(t, orchestratorv1.OperationStatus_OPERATION_STATUS_CANCELLED, resp.Msg.GetOperation().GetState())
	assert.Equal(t, orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_NOT_APPLIED, resp.Msg.GetOperation().GetEffectStatus())
}

// TestCancelEmergency_QueuedRetainsUnknown (REQ-087 AC-087-06): a queued
// EMERGENCY whose command may already have crossed the delivery boundary
// cancels with effect UNKNOWN, retaining the lock for late-result observation.
func TestCancelEmergency_QueuedRetainsUnknown(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	created := createEmergencyPendingViaUOW(t, st, "cancel-queued")
	queued, err := st.Operations().UpdateStatus(t.Context(), created.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	// queued delivery = may have crossed the boundary (DeliveryUnknown).
	require.NoError(t, st.EmergencyIntents().UpdateDeliveryStatus(t.Context(), created.Intent.ID, "queued"))

	resp, err := svc.CancelOperation(emergencyAdminContext(), cancelRequest(created.Operation.ID, queued.StateVersion, "idem-cancel-queued"))
	require.NoError(t, err)
	assert.Equal(t, orchestratorv1.OperationStatus_OPERATION_STATUS_CANCELLED, resp.Msg.GetOperation().GetState())
	assert.Equal(t, orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_UNKNOWN, resp.Msg.GetOperation().GetEffectStatus())
	intent, err := st.EmergencyIntents().GetByOperationID(t.Context(), created.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, store.EmergencyEffectUnknown, intent.EffectStatus)
}

var _ = json.Marshal

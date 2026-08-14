package orchestrator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/internal/store"
)

// seedUnresolvedEmergencyEffect inserts a terminal EMERGENCY operation whose
// effect is still UNKNOWN for the definition (AC-067-20 precondition).
func seedUnresolvedEmergencyEffect(t *testing.T, st store.Store, opID string) {
	t.Helper()
	const defID = "def-001"
	now := time.Now().UTC()
	require.NoError(t, st.Operations().Create(context.Background(), &store.Operation{
		ID: opID, OperationType: store.OperationEmergency, Status: store.StatusSucceeded,
		ReleaseDefinitionID: defID, IdempotencyKey: "seed-" + opID, TerminalAt: &now, CreatedAt: now, UpdatedAt: now,
	}))
	db, ok := st.(*sqlitestore.Store)
	require.True(t, ok, "test store must be sqlite")
	_, err := db.DB().ExecContext(context.Background(), `
		INSERT INTO emergency_intents (id, release_definition_id, operation_id, command_id,
			action, workload_kind, workload_name, workload_namespace, workload_uid,
			container, artifact_id, image_reference, target_replicas, annotation_scope,
			annotation_entries, convergence, promotion_paths, delivery_status, effect_status,
			last_delivery_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'set_container_image', 'deployment', 'app', 'default', 'uid-1',
			'app', 'artifact-1', 'registry/app:latest', 1, '',
			'[]', 'require_promotion', '[]', 'persisted', 'UNKNOWN',
			NULL, ?, ?)
	`, "ei-"+opID, defID, opID, "cmd-"+opID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	require.NoError(t, err)
}

// seedPendingPromotionTask inserts a pending_promotion convergence task
// (AC-067-21 precondition).
func seedPendingPromotionTask(t *testing.T, st store.Store) *store.ConvergenceTask {
	t.Helper()
	const defID = "def-001"
	now := time.Now().UTC()
	require.NoError(t, st.Operations().Create(context.Background(), &store.Operation{
		ID: "op-task-" + defID, OperationType: store.OperationEmergency, Status: store.StatusSucceeded,
		ReleaseDefinitionID: defID, IdempotencyKey: "seed-task-" + defID, TerminalAt: &now, CreatedAt: now, UpdatedAt: now,
	}))
	task := &store.ConvergenceTask{
		ID: "task-" + defID, OperationID: "op-task-" + defID, ReleaseDefinitionID: defID,
		Action: "set_image", TargetSummary: "registry/app:latest", Reason: "emergency",
		PromotionPaths: json.RawMessage(`[]`), Status: "pending_promotion", SubmittedAt: now, CreatedAt: now,
	}
	require.NoError(t, st.ConvergenceTasks().Create(context.Background(), task))
	return task
}

// gateDetail extracts the CreateOperationGateDetail from a connect error.
func gateDetail(t *testing.T, err error) *orchestratorv1.CreateOperationGateDetail {
	t.Helper()
	connectErr, ok := err.(*connect.Error)
	require.True(t, ok, "expected connect error, got %T", err)
	require.Len(t, connectErr.Details(), 1)
	value, detailErr := connectErr.Details()[0].Value()
	require.NoError(t, detailErr)
	detail, ok := value.(*orchestratorv1.CreateOperationGateDetail)
	require.True(t, ok, "expected CreateOperationGateDetail, got %T", value)
	return detail
}

func installRequest() *connect.Request[orchestratorv1.CreateOperationRequest] {
	return withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
	}), "idem-gate-"+time.Now().Format("150405.000000000"))
}

func rollbackRequest() *connect.Request[orchestratorv1.RollbackReleaseRequest] {
	return withIdempotencyKey(connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-001",
		TargetRevision:          1,
		ExpectedCurrentRevision: 3,
		Reason:                  "gate test",
	}), "idem-gate-rb-"+time.Now().Format("150405.000000000"))
}

// AC-067-20: an unresolved emergency effect blocks standard operation creation.
func TestCreateOperation_UnresolvedEffectBlocks(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedUnresolvedEmergencyEffect(t, st, "op-unresolved-1")

	_, err := svc.CreateOperation(adminCtx(), installRequest())
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "emergency_effect_unresolved")
	detail := gateDetail(t, err)
	assert.Equal(t, []string{"op-unresolved-1"}, detail.GetUnresolvedOperationIds())

	ops, listErr := st.Operations().List(context.Background(), "def-001")
	require.NoError(t, listErr)
	assert.Len(t, ops, 1, "no operation may be written")
}

// AC-067-20: ROLLBACK is equally gated.
func TestRollbackRelease_UnresolvedEffectBlocks(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedRollbackInventory(t, st)
	seedUnresolvedEmergencyEffect(t, st, "op-unresolved-rb-1")

	_, err := svc.RollbackRelease(adminCtx(), rollbackRequest())
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "emergency_effect_unresolved")
	detail := gateDetail(t, err)
	assert.Equal(t, []string{"op-unresolved-rb-1"}, detail.GetUnresolvedOperationIds())
}

// AC-067-21: a pending_promotion convergence task blocks standard operation creation.
func TestCreateOperation_PendingPromotionBlocks(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	task := seedPendingPromotionTask(t, st)

	_, err := svc.CreateOperation(adminCtx(), installRequest())
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "release_convergence_pending")
	detail := gateDetail(t, err)
	assert.Equal(t, []string{task.ID}, detail.GetConvergenceTaskIds())
}

// AC-067-21: ROLLBACK is equally gated.
func TestRollbackRelease_PendingPromotionBlocks(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedRollbackInventory(t, st)
	task := seedPendingPromotionTask(t, st)

	_, err := svc.RollbackRelease(adminCtx(), rollbackRequest())
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "release_convergence_pending")
	detail := gateDetail(t, err)
	assert.Equal(t, []string{task.ID}, detail.GetConvergenceTaskIds())
}

// AC-067-22: with a stale authorization snapshot, an unresolved effect, and a
// pending task all present, the authorization error wins.
func TestCreateOperation_GatePriorityAuthorizationFirst(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedUnresolvedEmergencyEffect(t, st, "op-priority-emergency")
	seedPendingPromotionTask(t, st)

	// authorizer == nil simulates an unavailable authorization snapshot
	// (fail closed before any gate runs).
	noAuthSvc := NewService(st, svc.verifier, "staging", nil, sqliteUOW(st), nil, svc.logger)
	_, err := noAuthSvc.CreateOperation(adminCtx(), installRequest())
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "authorization_snapshot_stale")
}

// AC-067-22: when both gates are present, the emergency gate wins as the
// top-level reason, but the typed detail carries both ID arrays.
func TestCreateOperation_EmergencyGateDetailCarriesBothIDArrays(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedUnresolvedEmergencyEffect(t, st, "op-both-emergency")
	task := seedPendingPromotionTask(t, st)

	_, err := svc.CreateOperation(adminCtx(), installRequest())
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "emergency_effect_unresolved")
	detail := gateDetail(t, err)
	assert.Equal(t, []string{"op-both-emergency"}, detail.GetUnresolvedOperationIds())
	assert.Equal(t, []string{task.ID}, detail.GetConvergenceTaskIds())
}

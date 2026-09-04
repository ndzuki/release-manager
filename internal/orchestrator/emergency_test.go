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

// seedEmergencyTestArtifact seeds a validated + trusted candidate image so the
// canonical ExecuteEmergencyChange artifact resolution succeeds.
func seedEmergencyTestArtifact(t *testing.T, st store.Store) {
	t.Helper()
	validatedAt := time.Now().UTC()
	require.NoError(t, st.CandidateArtifacts().Create(t.Context(), &store.CandidateArtifact{
		ID: "artifact-img", ArtifactType: store.ArtifactImage,
		Ref: "registry.example/team/api:1.0.0", Digest: "sha256:abc",
		ValidatedAt: &validatedAt, SourceID: "source-1",
	}))
	require.NoError(t, st.Verifications().Create(t.Context(), &store.VerificationRecord{
		ID: uuid.NewString(), ArtifactDigest: "sha256:abc", PolicyVersion: "v1",
		Status: store.VerificationTrusted, RevocationEpoch: 0,
	}))
}

func emergencyTestService(t *testing.T) (*Service, store.Store, *recordingEmergencyDispatcher) {
	t.Helper()
	svc, st, _ := setupService(t)
	seedDefinition(t, st)
	seedEmergencyImageIdentity(t, st)
	return emergencyTestServiceFromExisting(t, svc, st)
}

// seedEmergencyImageIdentity seeds the inventory row for the canonical image
// branch fixture: the workload_ref "deployments/default/api" resolves against
// the authoritative identity (REQ-085). The release name is my-release, the
// workload name api — identity reports carry the manifest workload name, not
// the Helm release name.
func seedEmergencyImageIdentity(t *testing.T, st store.Store) {
	t.Helper()
	require.NoError(t, st.Inventories().Upsert(t.Context(), &store.ReleaseInventory{
		ReleaseDefinitionID: "def-001", CustomerID: "cust-001", ClusterID: "cls-001",
		Namespace: "default", ReleaseName: "my-release", Status: "deployed", InventoryStatus: store.InventoryActive,
		WorkloadKind: workloadDeployment, WorkloadName: "api", WorkloadNamespace: "default", WorkloadUID: "uid-image-0001",
	}))
}

func emergencyAdminContext() context.Context {
	return authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "release-admin", OrganizationID: "org-001", Roles: []string{string(store.RoleReleaseAdmin)},
	})
}

func emergencyImageRequest(key string) *connect.Request[orchestratorv1.ExecuteEmergencyChangeRequest] {
	return connect.NewRequest(&orchestratorv1.ExecuteEmergencyChangeRequest{
		ReleaseDefinitionId: "def-001",
		WorkloadRef:         "deployments/default/api",
		Container:           "api",
		OperationVersion:    "v1.0.0",
		ArtifactRef:         "artifact-img",
		ConvergenceStrategy: orchestratorv1.ConvergenceStrategy_REVERT_ON_NEXT_RECONCILE,
		IdempotencyKey:      key,
	})
}

func emergencyDetailFrom(t *testing.T, err error) *orchestratorv1.EmergencyErrorDetail {
	t.Helper()
	var connectErr *connect.Error
	require.True(t, errors.As(err, &connectErr))
	for _, detail := range connectErr.Details() {
		value, valueErr := detail.Value()
		if valueErr != nil {
			continue
		}
		if msg, ok := value.(*orchestratorv1.EmergencyErrorDetail); ok {
			return msg
		}
	}
	t.Fatalf("no EmergencyErrorDetail in connect error details")
	return nil
}

func TestExecuteEmergencyChangeRejectsUnauthenticated(t *testing.T) {
	svc, _, _ := emergencyTestService(t)
	_, err := svc.ExecuteEmergencyChange(context.Background(), emergencyImageRequest("unauthed-3"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// AC-079-G2: kill switch off → FailedPrecondition + KILL_SWITCH_DISABLED, not retryable.
func TestExecuteEmergencyChangeKillSwitchDisabled(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	require.NoError(t, st.EmergencyConfig().SetEmergencyConfig(t.Context(), store.EmergencyConfig{Enabled: false}))
	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("kill-switch"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Equal(t, "KILL_SWITCH_DISABLED", connectErrorReason(err))
	detail := emergencyDetailFrom(t, err)
	assert.Equal(t, orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_KILL_SWITCH_DISABLED, detail.GetReasonCode())
	assert.False(t, detail.GetRetryable())
}

// AC-081-02 (D2=A): with the kill switch enabled (the value the orchestrator
// startup seed writes from config), ExecuteEmergencyChange no longer returns
// KILL_SWITCH_DISABLED and the acceptance flow proceeds.
func TestExecuteEmergencyChangeKillSwitchEnabled(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	// The startup seed path calls SetEmergencyConfig — verify the enabled
	// value survives the upsert and is read back (D2=A seed semantics).
	require.NoError(t, st.EmergencyConfig().SetEmergencyConfig(t.Context(), store.EmergencyConfig{Enabled: true}))
	loaded, err := st.EmergencyConfig().GetEmergencyConfig(t.Context())
	require.NoError(t, err)
	assert.True(t, loaded.Enabled)
	assert.Equal(t, store.DefaultEmergencyOperationTimeout, loaded.OperationTimeout)

	resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("kill-switch-on"))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetResult().GetRequested())
}

// AC-079-G7: UNSPECIFIED convergence strategy is rejected.
func TestExecuteEmergencyChangeRejectsUnspecifiedStrategy(t *testing.T) {
	svc, _, _ := emergencyTestService(t)
	req := emergencyImageRequest("unspecified-strategy")
	req.Msg.ConvergenceStrategy = orchestratorv1.ConvergenceStrategy_CONVERGENCE_STRATEGY_UNSPECIFIED
	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, "convergence_strategy_required", connectErrorReason(err))
}

// AC-079-G9: REQUIRE_PROMOTION without target locks is rejected.
func TestExecuteEmergencyChangeTargetLocksRequired(t *testing.T) {
	svc, _, _ := emergencyTestService(t)
	req := emergencyImageRequest("target-locks-required")
	req.Msg.ConvergenceStrategy = orchestratorv1.ConvergenceStrategy_REQUIRE_PROMOTION
	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, "target_locks_required", connectErrorReason(err))
}

// AC-079-G5: workload_ref is mandatory (container/operation_version cascade).
func TestExecuteEmergencyChangeCascadeWorkloadRefRequired(t *testing.T) {
	svc, _, _ := emergencyTestService(t)
	req := emergencyImageRequest("cascade-workload-ref")
	req.Msg.WorkloadRef = ""
	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, "invalid_workload_ref", connectErrorReason(err))
}

func TestExecuteEmergencyChangeInvalidOperationVersion(t *testing.T) {
	svc, _, _ := emergencyTestService(t)
	req := emergencyImageRequest("invalid-version")
	req.Msg.OperationVersion = "not-a-version"
	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	detail := emergencyDetailFrom(t, err)
	assert.Equal(t, orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_VERSION_INVALID, detail.GetReasonCode())
}

// AC-079-G8: unresolvable artifact_ref → NotFound + ARTIFACT_NOT_FOUND.
func TestExecuteEmergencyChangeArtifactNotFound(t *testing.T) {
	svc, _, _ := emergencyTestService(t)
	req := emergencyImageRequest("artifact-not-found")
	req.Msg.ArtifactRef = "missing-artifact"
	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	detail := emergencyDetailFrom(t, err)
	assert.Equal(t, orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_ARTIFACT_NOT_FOUND, detail.GetReasonCode())
	assert.False(t, detail.GetRetryable())
}

// AC-079-G1: accepted and queued; response carries requested=true and the
// derived operationVersion without blocking on execution.
func TestExecuteEmergencyChangePersistsAndDispatches(t *testing.T) {
	svc, st, dispatcher := emergencyTestService(t)
	resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("image-1"))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetResult().GetRequested())
	assert.NotEmpty(t, resp.Msg.GetOperationId())
	assert.NotEmpty(t, resp.Msg.GetOperationVersion())
	assert.Len(t, dispatcher.commands, 1)
	assert.Equal(t, "api", dispatcher.commands[0].GetSetContainerImage().GetContainer())
	intent, err := st.EmergencyIntents().GetByOperationID(t.Context(), resp.Msg.GetOperationId())
	require.NoError(t, err)
	assert.Equal(t, "queued", intent.DeliveryStatus)
	operation, err := st.Operations().Get(t.Context(), resp.Msg.GetOperationId())
	require.NoError(t, err)
	assert.Equal(t, store.StatusQueued, operation.Status)
}

// AC-079-G4: idempotent replay returns the existing acceptance result.
func TestExecuteEmergencyChangeIdempotency(t *testing.T) {
	svc, _, dispatcher := emergencyTestService(t)
	first, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("same-image"))
	require.NoError(t, err)
	second, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("same-image"))
	require.NoError(t, err)
	assert.Equal(t, first.Msg.GetOperationId(), second.Msg.GetOperationId())
	assert.True(t, second.Msg.GetResult().GetRequested())
	assert.Len(t, dispatcher.commands, 1)

	conflict := emergencyImageRequest("same-image")
	conflict.Msg.Container = "other-container"
	_, err = svc.ExecuteEmergencyChange(emergencyAdminContext(), conflict)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func TestExecuteEmergencyChangeAuthorizationDenyHasNoWriteSideEffects(t *testing.T) {
	svc, st, dispatcher := emergencyTestService(t)
	deployerID := "emergency-deployer"
	require.NoError(t, st.Users().Create(t.Context(), &store.User{ID: deployerID, Username: deployerID, Status: store.UserActive}))
	require.NoError(t, st.OrgMembers().Create(t.Context(), &store.OrganizationMember{OrgID: "org-001", UserID: deployerID, Role: store.RoleDeployer}))
	ctx := authctx.WithActor(context.Background(), authctx.Actor{UserID: deployerID, OrganizationID: "org-001"})
	_, err := svc.ExecuteEmergencyChange(ctx, emergencyImageRequest("deny-no-write"))
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

// AC-079-G3 / D18: one in-flight EMERGENCY operation per release.
func TestExecuteEmergencyChangeInProgressAborted(t *testing.T) {
	svc, _, _ := emergencyTestService(t)
	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("in-progress-1"))
	require.NoError(t, err)
	_, err = svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("in-progress-2"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	assert.Equal(t, "OPERATION_IN_PROGRESS", connectErrorReason(err))
	detail := emergencyDetailFrom(t, err)
	assert.Equal(t, orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_OPERATION_IN_PROGRESS, detail.GetReasonCode())
	assert.True(t, detail.GetRetryable())
}

// AC-079-G9 + G10: REQUIRE_PROMOTION with target locks persists the strategy
// and creates the convergence task.
func TestExecuteEmergencyChangeRequirePromotionCreatesConvergenceTask(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	definition, err := st.Definitions().Get(t.Context(), "def-001")
	require.NoError(t, err)
	definition.PromotionMappings = []store.PromotionMapping{{
		WorkloadKind: workloadDeployment, WorkloadName: "api", Container: "api",
		Field: "image_digest", ValuesPath: "api.image.digest",
	}}
	_, err = st.Definitions().Update(t.Context(), definition, nil)
	require.NoError(t, err)

	req := emergencyImageRequest("require-promotion")
	req.Msg.ConvergenceStrategy = orchestratorv1.ConvergenceStrategy_REQUIRE_PROMOTION
	req.Msg.TargetLocks = []string{"api.image.digest"}
	resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetResult().GetRequested())
	require.Len(t, resp.Msg.GetResult().GetConvergenceTasks(), 1)

	intent, err := st.EmergencyIntents().GetByOperationID(t.Context(), resp.Msg.GetOperationId())
	require.NoError(t, err)
	assert.Equal(t, store.EmergencyRequirePromotion, intent.Convergence)
	task, err := st.ConvergenceTasks().GetByOperationID(t.Context(), resp.Msg.GetOperationId())
	require.NoError(t, err)
	assert.Equal(t, "pending_promotion", task.Status)
	assert.Equal(t, resp.Msg.GetResult().GetConvergenceTasks()[0].GetTaskId(), task.ID)
}

func TestExecuteEmergencyChangeRejectedByStandardOperation(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	require.NoError(t, st.Operations().Create(t.Context(), &store.Operation{
		ID: uuid.NewString(), OperationType: store.OperationUpgrade, Status: store.StatusRunning,
		ReleaseDefinitionID: "def-001", IdempotencyKey: uuid.NewString(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))
	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("blocked"))
	require.Error(t, err)
	assert.Equal(t, "release_busy", connectErrorReason(err))
}

func TestExecuteEmergencyChangeDefinitionNotFound(t *testing.T) {
	svc, _, _ := emergencyTestService(t)
	req := emergencyImageRequest("not-found")
	req.Msg.ReleaseDefinitionId = "nonexistent"
	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestExpireEmergencyOperationsTimesOutOverdueOperation(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("timeout-emergency"))
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

// AC-079-02: GetOperation projects EmergencyResult.requested for the accepted
// emergency operation.
func TestGetOperationReturnsEmergencyResult(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("get-emergency"))
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
		[]byte(`{"container":"api","image_reference":"registry.example/team/api:1.0.0"}`),
		[]byte(`{"container":"api","image_reference":"registry.example/team/api@sha256:abc"}`),
	)
	require.NoError(t, err)

	getResp, err := svc.GetOperation(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.GetOperationRequest{
		OperationId: op.ID,
	}))
	require.NoError(t, err)
	require.NotNil(t, getResp.Msg.GetEmergencyResult())
	assert.True(t, getResp.Msg.GetEmergencyResult().GetRequested())
	assert.Equal(t, orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_CONTAINER_IMAGE, getResp.Msg.GetEmergencyResult().GetOpType())
	assert.Equal(t, "api", getResp.Msg.GetEmergencyResult().GetBefore().GetImageRefValues().GetContainer())
	assert.Equal(t, "registry.example/team/api@sha256:abc", getResp.Msg.GetEmergencyResult().GetAfter().GetImageRefValues().GetImageReference())
	assert.Equal(t, "awaiting_standard_release", getResp.Msg.GetEmergencyResult().GetRevertStatus())
}

// AC-023-13: running EMERGENCY → cancel_not_allowed
func TestCancelOperation_EmergencyRunningRejected(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("cancel-em-running"))
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
	// ExecuteEmergencyChange with working dispatcher → operation is created and queued.
	// queued EMERGENCY is cancellable (AC-023-13: pending/queued EMERGENCY → cancelled).
	resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("cancel-em-pending"))
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
	resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("cancel-em-queued"))
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

// --- REQ-081 set_replicas path (AC-081-03) ---

func emergencyReplicasRequest(key string, replicas int32) *connect.Request[orchestratorv1.ExecuteEmergencyChangeRequest] {
	return connect.NewRequest(&orchestratorv1.ExecuteEmergencyChangeRequest{
		ReleaseDefinitionId: "def-001",
		WorkloadRef:         "deployments/default/my-release",
		OperationVersion:    "v1.0.0",
		ConvergenceStrategy: orchestratorv1.ConvergenceStrategy_REVERT_ON_NEXT_RECONCILE,
		IdempotencyKey:      key,
		SetReplicas:         replicas,
	})
}

// seedReplicasDefinition configures def-001 for the set_replicas path: HPA
// flag, max emergency replicas and promotion mappings (field "replicas" per
// REQ-032 §222 promotion mapping field vocabulary). It also seeds the
// authoritative workload identity matching emergencyReplicasRequest's
// workload_ref "deployments/default/my-release" (REQ-085).
func seedReplicasDefinition(t *testing.T, st store.Store, mappings []store.PromotionMapping, hpa bool, maxReplicas int32) {
	t.Helper()
	definition, err := st.Definitions().Get(t.Context(), "def-001")
	require.NoError(t, err)
	definition.HPAManaged = hpa
	definition.MaxEmergencyReplicas = maxReplicas
	definition.PromotionMappings = mappings
	_, err = st.Definitions().Update(t.Context(), definition, nil)
	require.NoError(t, err)
	require.NoError(t, st.Inventories().Upsert(t.Context(), &store.ReleaseInventory{
		ReleaseDefinitionID: "def-001", CustomerID: "cust-001", ClusterID: "cls-001",
		Namespace: "default", ReleaseName: "my-release", Status: "deployed", InventoryStatus: store.InventoryActive,
		WorkloadKind: workloadDeployment, WorkloadName: "my-release", WorkloadNamespace: "default", WorkloadUID: "uid-replicas-0001",
	}))
}

func replicasPromotionMapping() []store.PromotionMapping {
	return []store.PromotionMapping{{
		WorkloadKind: workloadDeployment, WorkloadName: "my-release",
		Field: "replicas", ValuesPath: "replicaCount",
	}}
}

// AC-081-03: set_replicas:2 resolves through the real SetReplicas storage
// entry point (not the hard-coded image branch) and dispatches
// EmergencyCommand_SetReplicas.
func TestExecuteEmergencyChangeSetReplicas(t *testing.T) {
	svc, st, dispatcher := emergencyTestService(t)
	seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)

	resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("replicas-1", 2))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetResult().GetRequested())
	assert.NotEmpty(t, resp.Msg.GetOperationId())
	assert.NotEmpty(t, resp.Msg.GetOperationVersion())

	require.Len(t, dispatcher.commands, 1)
	require.NotNil(t, dispatcher.commands[0].GetSetReplicas())
	assert.Equal(t, int32(2), dispatcher.commands[0].GetSetReplicas().GetReplicas())
	assert.Equal(t, "my-release", dispatcher.commands[0].GetWorkloadName())
	// AC-085-02: the dispatched command carries the authoritative non-empty
	// uid persisted on the inventory row — the operator no longer rejects it
	// as an empty-identity invalid_command.
	assert.Equal(t, "uid-replicas-0001", dispatcher.commands[0].GetWorkloadUid())
	assert.Equal(t, workloadDeployment, dispatcher.commands[0].GetWorkloadKind())
	assert.Equal(t, "default", dispatcher.commands[0].GetWorkloadNamespace())

	intent, err := st.EmergencyIntents().GetByOperationID(t.Context(), resp.Msg.GetOperationId())
	require.NoError(t, err)
	assert.Equal(t, store.EmergencySetReplicas, intent.Action)
	assert.Equal(t, "uid-replicas-0001", intent.WorkloadUID)
	require.NotNil(t, intent.TargetReplicas)
	assert.Equal(t, int32(2), *intent.TargetReplicas)
	assert.Nil(t, intent.Container)
	assert.Nil(t, intent.ArtifactID)
	assert.Nil(t, intent.ImageReference)
}

// AC-085-03: a definition whose inventory row carries no operator-reported
// workload identity is rejected with the stable invalid_workload_ref reason
// code before any intent/operation exists — an empty WorkloadUID is never
// persisted nor dispatched (D-110 ③).
func TestExecuteEmergencyChangeRejectsMissingWorkloadIdentity(t *testing.T) {
	svc, st, dispatcher := emergencyTestServiceWithoutIdentity(t)
	definition, err := st.Definitions().Get(t.Context(), "def-001")
	require.NoError(t, err)
	definition.MaxEmergencyReplicas = 10
	_, err = st.Definitions().Update(t.Context(), definition, nil)
	require.NoError(t, err)

	_, err = svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("missing-identity", 2))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, "invalid_workload_ref", connectErrorReason(err))
	assert.Empty(t, dispatcher.commands, "no command may be dispatched without identity")

	intents, intentErr := st.EmergencyIntents().ListPendingDeliveryByDefinition(t.Context(), "def-001")
	require.NoError(t, intentErr)
	assert.Empty(t, intents, "no intent may be persisted without identity")
}

// AC-085-03: an identity whose kind/name/namespace disagrees with the
// requested workload_ref is rejected (fail closed) — the request may not
// execute against a workload the authoritative identity does not describe.
func TestExecuteEmergencyChangeRejectsMismatchedWorkloadIdentity(t *testing.T) {
	svc, st, dispatcher := emergencyTestService(t)
	seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)
	require.NoError(t, st.Inventories().UpdateWorkloadIdentity(t.Context(), "cust-001", "cls-001", "default", "my-release", store.WorkloadIdentity{
		Kind: workloadDeployment, Name: "other-workload", Namespace: "default", UID: "uid-other",
	}))

	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("mismatched-identity", 2))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, "invalid_workload_ref", connectErrorReason(err))
	assert.Empty(t, dispatcher.commands)
}

// AC-085-03: an unparseable identity (kind outside the emergency whitelist)
// is rejected with invalid_workload_ref before dispatch.
func TestExecuteEmergencyChangeRejectsUnparseableWorkloadIdentity(t *testing.T) {
	svc, st, dispatcher := emergencyTestService(t)
	seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)
	require.NoError(t, st.Inventories().UpdateWorkloadIdentity(t.Context(), "cust-001", "cls-001", "default", "my-release", store.WorkloadIdentity{
		Kind: "JOB", Name: "my-release", Namespace: "default", UID: "uid-job",
	}))

	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("unparseable-identity", 2))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, "invalid_workload_ref", connectErrorReason(err))
	assert.Empty(t, dispatcher.commands)
}

// emergencyTestServiceWithoutIdentity mirrors emergencyTestService but skips
// the inventory identity seed — for the fail-closed negative matrix.
func emergencyTestServiceWithoutIdentity(t *testing.T) (*Service, store.Store, *recordingEmergencyDispatcher) {
	t.Helper()
	svc, st, _ := setupService(t)
	seedDefinition(t, st)
	return emergencyTestServiceFromExisting(t, svc, st)
}

// AC-081-03 (D4=A): set_replicas combined with container/artifact_ref is
// rejected as conflicting_change (single action per request).
func TestExecuteEmergencyChangeSetReplicasConflictingImage(t *testing.T) {
	svc, st, dispatcher := emergencyTestService(t)
	seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)

	req := emergencyReplicasRequest("replicas-conflict", 2)
	req.Msg.Container = "api"
	req.Msg.ArtifactRef = "artifact-img"
	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, "conflicting_change", connectErrorReason(err))
	assert.Empty(t, dispatcher.commands)
}

// AC-081-03: SET_REPLICAS validation rules per REQ-032 §171 — workload kind,
// HPA managed and replicas bounds.
func TestExecuteEmergencyChangeSetReplicasValidation(t *testing.T) {
	t.Run("replicas below zero", func(t *testing.T) {
		svc, st, _ := emergencyTestService(t)
		seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)
		_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("replicas-neg", -1))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		assert.Equal(t, "invalid_replicas", connectErrorReason(err))
	})
	t.Run("zero means not requested", func(t *testing.T) {
		// REQ-081 Step 2 risk note: set_replicas is a flat int32 without
		// presence — 0 falls back to the image branch and its artifact_ref
		// requirement. Pinned here so a future scale-to-zero contract change
		// (oneof/optional) is a deliberate, visible decision.
		svc, st, _ := emergencyTestService(t)
		seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)
		_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("replicas-zero", 0))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		assert.Equal(t, "artifact_ref_required", connectErrorReason(err))
	})
	t.Run("replicas above max", func(t *testing.T) {
		svc, st, _ := emergencyTestService(t)
		seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 5)
		_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("replicas-max", 6))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		assert.Equal(t, "invalid_replicas", connectErrorReason(err))
	})
	t.Run("replicas at max accepted", func(t *testing.T) {
		svc, st, dispatcher := emergencyTestService(t)
		seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 3)
		resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("replicas-at-max", 3))
		require.NoError(t, err)
		require.Len(t, dispatcher.commands, 1)
		require.NotNil(t, dispatcher.commands[0].GetSetReplicas())
		assert.Equal(t, int32(3), dispatcher.commands[0].GetSetReplicas().GetReplicas())
		assert.NotEmpty(t, resp.Msg.GetOperationId())
	})
	t.Run("hpa managed", func(t *testing.T) {
		svc, st, _ := emergencyTestService(t)
		seedReplicasDefinition(t, st, replicasPromotionMapping(), true, 10)
		_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("replicas-hpa", 2))
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		assert.Equal(t, "hpa_managed", connectErrorReason(err))
	})
	t.Run("workload kind not supported", func(t *testing.T) {
		svc, st, _ := emergencyTestService(t)
		seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)
		req := emergencyReplicasRequest("replicas-daemonset", 2)
		req.Msg.WorkloadRef = "daemonsets/default/my-release"
		_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		assert.Equal(t, "workload_kind_not_supported", connectErrorReason(err))
	})
}

// AC-081-03: REQUIRE_PROMOTION without a field=replicas promotion mapping is
// rejected (same promotion_not_supported contract as the image branch).
func TestExecuteEmergencyChangeSetReplicasRequirePromotionWithoutMapping(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	seedReplicasDefinition(t, st, nil, false, 10)
	req := emergencyReplicasRequest("replicas-no-mapping", 2)
	req.Msg.ConvergenceStrategy = orchestratorv1.ConvergenceStrategy_REQUIRE_PROMOTION
	req.Msg.TargetLocks = []string{"replicaCount"}
	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Equal(t, "promotion_not_supported", connectErrorReason(err))
}

// AC-081-03: REQUIRE_PROMOTION with a replicas mapping creates the
// convergence task bound to the replicas promotion path.
func TestExecuteEmergencyChangeSetReplicasRequirePromotion(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)
	req := emergencyReplicasRequest("replicas-promotion", 2)
	req.Msg.ConvergenceStrategy = orchestratorv1.ConvergenceStrategy_REQUIRE_PROMOTION
	req.Msg.TargetLocks = []string{"replicaCount"}
	resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetResult().GetConvergenceTasks(), 1)

	intent, err := st.EmergencyIntents().GetByOperationID(t.Context(), resp.Msg.GetOperationId())
	require.NoError(t, err)
	assert.Equal(t, store.EmergencyRequirePromotion, intent.Convergence)
	task, err := st.ConvergenceTasks().GetByOperationID(t.Context(), resp.Msg.GetOperationId())
	require.NoError(t, err)
	assert.Equal(t, store.EmergencySetReplicas, task.Action)
	assert.Equal(t, "pending_promotion", task.Status)
}

// AC-081-03 (ADR-009): the set_replicas branch reuses the existing
// idempotency key + request hash path — same request replays, a changed
// replicas value conflicts.
func TestExecuteEmergencyChangeSetReplicasIdempotency(t *testing.T) {
	svc, st, dispatcher := emergencyTestService(t)
	seedReplicasDefinition(t, st, nil, false, 10)
	first, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("same-replicas", 2))
	require.NoError(t, err)
	second, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyReplicasRequest("same-replicas", 2))
	require.NoError(t, err)
	assert.Equal(t, first.Msg.GetOperationId(), second.Msg.GetOperationId())
	assert.Len(t, dispatcher.commands, 1)

	conflict := emergencyReplicasRequest("same-replicas", 3)
	_, err = svc.ExecuteEmergencyChange(emergencyAdminContext(), conflict)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

// AC-081-03 recovery path: a rejected replicas request leaves no state
// behind — correcting the input with the same idempotency key succeeds.
func TestExecuteEmergencyChangeSetReplicasRecoversAfterValidationFailure(t *testing.T) {
	svc, st, dispatcher := emergencyTestService(t)
	seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)

	invalid := emergencyReplicasRequest("replicas-recover", 11)
	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), invalid)
	require.Error(t, err)
	assert.Equal(t, "invalid_replicas", connectErrorReason(err))
	assert.Empty(t, dispatcher.commands)

	corrected := emergencyReplicasRequest("replicas-recover", 2)
	resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), corrected)
	require.NoError(t, err)
	require.Len(t, dispatcher.commands, 1)
	require.NotNil(t, dispatcher.commands[0].GetSetReplicas())
	assert.Equal(t, int32(2), dispatcher.commands[0].GetSetReplicas().GetReplicas())
	assert.Equal(t, store.StatusQueued, mustGetEmergencyOperation(t, st, resp.Msg.GetOperationId()).Status)
}

func mustGetEmergencyOperation(t *testing.T, st store.Store, operationID string) *store.Operation {
	t.Helper()
	operation, err := st.Operations().Get(t.Context(), operationID)
	require.NoError(t, err)
	return operation
}

func connectErrorReason(err error) string {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr.Meta().Get("X-Reason-Code")
	}
	return ""
}

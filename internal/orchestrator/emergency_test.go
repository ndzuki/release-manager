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
	return emergencyTestServiceFromExisting(t, svc, st)
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

func connectErrorReason(err error) string {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr.Meta().Get("X-Reason-Code")
	}
	return ""
}

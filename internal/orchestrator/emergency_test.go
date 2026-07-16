package orchestrator

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

func TestEmergencyChange_Success(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	req := connect.NewRequest(&orchestratorv1.EmergencyChangeRequest{
		ReleaseDefinitionId: "def-001",
		Action:              orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_CONTAINER_IMAGE,
		Payload:             `{"workload":"deployment/nginx","container":"nginx","image":"nginx@sha256:deadbeef"}`,
		Reason:              "Critical CVE-2024-0001",
		Convergence:         orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REQUIRE_PROMOTION,
		Actor:               &commonv1.ActorContext{UserId: "user-1", Organization: "org-1"},
	})

	resp, err := svc.EmergencyChange(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Msg.OperationId)
	assert.Equal(t, string(store.StatusPending), resp.Msg.Status)
}

func TestEmergencyChange_InvalidAction(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	req := connect.NewRequest(&orchestratorv1.EmergencyChangeRequest{
		ReleaseDefinitionId: "def-001",
		Action:              orchestratorv1.EmergencyAction_EMERGENCY_ACTION_UNSPECIFIED,
		Payload:             `{}`,
		Reason:              "test",
	})

	_, err := svc.EmergencyChange(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported emergency action")
}

func TestEmergencyChange_DefinitionNotFound(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	req := connect.NewRequest(&orchestratorv1.EmergencyChangeRequest{
		ReleaseDefinitionId: "nonexistent",
		Action:              orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_CONTAINER_IMAGE,
		Payload:             `{"workload":"deployment/nginx","container":"nginx","image":"nginx@sha256:deadbeef"}`,
		Reason:              "test",
	})

	_, err := svc.EmergencyChange(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "release definition not found")
}

func TestEmergencyChange_ConflictWithActiveOperation(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	// Create an active standard operation first.
	op := &store.Operation{
		ID:                  "op-active-1",
		OperationType:       store.OperationUpgrade,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: "def-001",
		IdempotencyKey:      "idem-1",
	}
	require.NoError(t, st.Operations().Create(context.Background(), op))

	// AC-032-05: attempting EMERGENCY should be rejected.
	req := connect.NewRequest(&orchestratorv1.EmergencyChangeRequest{
		ReleaseDefinitionId: "def-001",
		Action:              orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_CONTAINER_IMAGE,
		Payload:             `{"workload":"deployment/nginx","container":"nginx","image":"nginx@sha256:deadbeef"}`,
		Reason:              "test",
	})

	_, err := svc.EmergencyChange(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "running standard operation")
}

func TestCreateOperation_RejectedByActiveEmergency(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	// Create an active EMERGENCY operation.
	op := &store.Operation{
		ID:                  "op-emergency-1",
		OperationType:       store.OperationEmergency,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: "def-001",
		IdempotencyKey:      "idem-emergency",
	}
	require.NoError(t, st.Operations().Create(context.Background(), op))

	// AC-032-06: standard operation should be rejected.
	req := connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		ReleaseDefinitionId: "def-001",
		OperationType:       "UPGRADE",
		IdempotencyKey:      "idem-standard",
		Actor:               &commonv1.ActorContext{UserId: "user-1"},
	})

	_, err := svc.CreateOperation(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "running EMERGENCY")
}

func TestEmergencyChange_HPARejectsSetReplicas(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	definition, err := st.Definitions().Get(context.Background(), "def-001")
	require.NoError(t, err)
	definition.HPAManaged = true
	require.NoError(t, st.Definitions().Update(context.Background(), definition))

	_, err = svc.EmergencyChange(context.Background(), connect.NewRequest(&orchestratorv1.EmergencyChangeRequest{
		ReleaseDefinitionId: "def-001",
		Action:              orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_REPLICAS,
		Payload:             `{"workload":"deployment/nginx","replicas":3}`,
		Reason:              "restore capacity",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "HPA managed workload")
}

func TestEmergencyChange_AllowsConcurrentEmergencyOperations(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	require.NoError(t, st.Operations().Create(context.Background(), &store.Operation{
		ID:                  "op-emergency-running",
		OperationType:       store.OperationEmergency,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: "def-001",
		IdempotencyKey:      "idem-running-emergency",
	}))

	resp, err := svc.EmergencyChange(context.Background(), connect.NewRequest(&orchestratorv1.EmergencyChangeRequest{
		ReleaseDefinitionId: "def-001",
		Action:              orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_REPLICAS,
		Payload:             `{"workload":"deployment/nginx","replicas":3}`,
		Reason:              "restore capacity",
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Msg.OperationId)
}

func TestEmergencyChange_RejectsNonApprovedAnnotation(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	_, err := svc.EmergencyChange(context.Background(), connect.NewRequest(&orchestratorv1.EmergencyChangeRequest{
		ReleaseDefinitionId: "def-001",
		Action:              orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_APPROVED_ANNOTATION,
		Payload:             `{"workload":"deployment/nginx","key":"arbitrary.example/key","value":"yes"}`,
		Reason:              "test annotation",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "annotation_not_allowed")
}

func TestEmergencyChange_PersistsConvergenceAndAction(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	resp, err := svc.EmergencyChange(context.Background(), connect.NewRequest(&orchestratorv1.EmergencyChangeRequest{
		ReleaseDefinitionId: "def-001",
		Action:              orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_REPLICAS,
		Payload:             `{"workload":"deployment/nginx","replicas":3}`,
		Reason:              "restore capacity",
		Convergence:         orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REQUIRE_PROMOTION,
	}))
	require.NoError(t, err)

	operation, err := st.Operations().Get(context.Background(), resp.Msg.OperationId)
	require.NoError(t, err)
	assert.Equal(t, store.EmergencySetReplicas, operation.EmergencyAction)
	assert.Equal(t, store.EmergencyRequirePromotion, operation.Convergence)
}

func TestCreateOperation_RequiresApprovedPromotion(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	emergency := &store.Operation{
		ID:                  "op-emergency-succeeded",
		OperationType:       store.OperationEmergency,
		Status:              store.StatusSucceeded,
		ReleaseDefinitionID: "def-001",
		IdempotencyKey:      "idem-emergency-succeeded",
		Convergence:         store.EmergencyRequirePromotion,
	}
	require.NoError(t, st.Operations().Create(context.Background(), emergency))

	_, err := svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "UPGRADE",
		ReleaseDefinitionId: "def-001",
		IdempotencyKey:      "idem-blocked-upgrade",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "convergence_required")

	revision := &store.ValuesRevision{
		ID:                  "vr-promoted",
		ReleaseDefinitionID: "def-001",
		Revision:            1,
		Status:              store.ValuesStatusApproved,
		Values:              []byte(`{}`),
		Digest:              "sha256:promoted",
	}
	require.NoError(t, st.Values().Create(context.Background(), revision))

	resp, err := svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "UPGRADE",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    revision.ID,
		IdempotencyKey:      "idem-promoted-upgrade",
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Msg.OperationId)
}

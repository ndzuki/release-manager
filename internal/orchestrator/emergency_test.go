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
		Payload:             `{"workload":"deployment/nginx","container":"nginx","image":"nginx:1.25.0"}`,
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
		Payload:             `{}`,
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
		Payload:             `{"workload":"deployment/nginx","container":"nginx","image":"nginx:1.25.0"}`,
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
		Actor:               &commonv1.ActorContext{UserId: "user-1", Organization: "org-001"},
	})

	_, err := svc.CreateOperation(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "running EMERGENCY")
}

func TestEmergencyChange_AllowsConcurrentEmergency(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	request := func(payload string) *connect.Request[orchestratorv1.EmergencyChangeRequest] {
		return connect.NewRequest(&orchestratorv1.EmergencyChangeRequest{
			ReleaseDefinitionId: "def-001",
			Action:              orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_CONTAINER_IMAGE,
			Payload:             payload,
			Reason:              "urgent remediation",
			Actor:               &commonv1.ActorContext{UserId: "user-1", Organization: "org-1"},
		})
	}

	first, err := svc.EmergencyChange(context.Background(), request(`{"workload":"deployment/nginx","container":"nginx","image":"nginx:1.25.1"}`))
	require.NoError(t, err)
	second, err := svc.EmergencyChange(context.Background(), request(`{"workload":"deployment/nginx","container":"nginx","image":"nginx:1.25.2"}`))
	require.NoError(t, err)
	assert.NotEqual(t, first.Msg.OperationId, second.Msg.OperationId)
}

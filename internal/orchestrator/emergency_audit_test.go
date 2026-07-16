package orchestrator

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/trust"
)

func TestEmergencyChange_AuditsSuccessAndFailure(t *testing.T) {
	_, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	logger := slog.New(slog.DiscardHandler)
	emitter := audit.NewEmitter(st.AuditEvents(), logger, audit.EmitterConfig{
		BufferSize:    16,
		FlushInterval: time.Hour,
		BatchSize:     16,
		SpoolPath:     t.TempDir() + "/audit.jsonl",
	})
	svc := NewService(
		st,
		trust.NewStubVerifier(st.Verifications(), logger),
		nil,
		"staging",
		emitter,
		logger,
	)

	successResp, err := svc.EmergencyChange(context.Background(), connect.NewRequest(&orchestratorv1.EmergencyChangeRequest{
		ReleaseDefinitionId: "def-001",
		Action:              orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_REPLICAS,
		Payload:             `{"workload":"deployment/nginx","replicas":3}`,
		Reason:              "restore capacity",
		Actor:               &commonv1.ActorContext{UserId: "release-admin", Organization: "org-1"},
	}))
	require.NoError(t, err)

	_, err = svc.EmergencyChange(context.Background(), connect.NewRequest(&orchestratorv1.EmergencyChangeRequest{
		ReleaseDefinitionId: "def-001",
		Action:              orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_APPROVED_ANNOTATION,
		Payload:             `{"workload":"deployment/nginx","key":"forbidden/key","value":"yes"}`,
		Reason:              "invalid annotation",
		Actor:               &commonv1.ActorContext{UserId: "release-admin", Organization: "org-1"},
	}))
	require.Error(t, err)

	require.NoError(t, emitter.Shutdown(context.Background()))

	successEvents, err := st.AuditEvents().ListByResource(context.Background(), "operation", successResp.Msg.OperationId)
	require.NoError(t, err)
	require.Len(t, successEvents, 1)
	assert.Equal(t, "succeeded", successEvents[0].Status)
	assert.Equal(t, "emergency_change", successEvents[0].Action)
	assert.Equal(t, "release-admin", successEvents[0].ActorID)
	assert.NotEmpty(t, successEvents[0].Metadata["payload_hash"])
	assert.NotContains(t, successEvents[0].ChangeSummary, "deployment/nginx")

	failureEvents, err := st.AuditEvents().ListByResource(context.Background(), "operation", "def-001")
	require.NoError(t, err)
	require.Len(t, failureEvents, 1)
	assert.Equal(t, "failed", failureEvents[0].Status)
	assert.Equal(t, connect.CodeInvalidArgument.String(), failureEvents[0].Metadata["error_code"])
	assert.Equal(t, "invalid annotation", failureEvents[0].Metadata["reason"])
}

func TestEmergencyChange_AuditDoesNotPersistRawPayload(t *testing.T) {
	_, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	logger := slog.New(slog.DiscardHandler)
	emitter := audit.NewEmitter(st.AuditEvents(), logger, audit.EmitterConfig{
		BufferSize:    4,
		FlushInterval: time.Hour,
		BatchSize:     4,
		SpoolPath:     t.TempDir() + "/audit.jsonl",
	})
	svc := NewService(st, trust.NewStubVerifier(st.Verifications(), logger), nil, "staging", emitter, logger)

	resp, err := svc.EmergencyChange(context.Background(), connect.NewRequest(&orchestratorv1.EmergencyChangeRequest{
		ReleaseDefinitionId: "def-001",
		Action:              orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_CONTAINER_IMAGE,
		Payload:             `{"workload":"deployment/nginx","container":"nginx","image":"registry.example/nginx@sha256:deadbeef"}`,
		Reason:              "patch CVE",
	}))
	require.NoError(t, err)
	require.NoError(t, emitter.Shutdown(context.Background()))

	events, err := st.AuditEvents().ListByResource(context.Background(), "operation", resp.Msg.OperationId)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.NotContains(t, events[0].ChangeSummary, "registry.example")
	assert.NotContains(t, events[0].Metadata["payload_hash"], "registry.example")
	assert.Equal(t, store.AuditActorUser, events[0].ActorKind)
}

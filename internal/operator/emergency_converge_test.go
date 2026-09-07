package operator_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"connectrpc.com/connect"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/store"
)

// seedQueuedEmergency creates an EMERGENCY operation + intent (pending, no
// convergence task) through the canonical UOW and advances the operation to
// queued, mirroring a successful orchestrator dispatch.
func seedQueuedEmergency(t *testing.T, st store.Store, definitionID string) (*store.Operation, *store.EmergencyIntent) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: definitionID, Name: definitionID, CustomerID: "cust-1", ClusterID: "clus-1",
		Namespace: "default", ReleaseName: definitionID, Status: store.DefStatusActive,
	}, nil))
	now := time.Now().UTC()
	opID := uuid.NewString()
	replicas := int32(3)
	intent := &store.EmergencyIntent{
		ID: uuid.NewString(), ReleaseDefinitionID: definitionID, OperationID: opID, CommandID: uuid.NewString(),
		Action: store.EmergencySetReplicas, WorkloadKind: "DEPLOYMENT", WorkloadName: "api",
		WorkloadNamespace: "default", WorkloadUID: "uid-api",
		Convergence: store.EmergencyRevertOnNextReconcile, TargetReplicas: &replicas,
	}
	cmd := store.EmergencyCreateCommand{
		Operation: &store.Operation{
			ID: opID, OperationType: store.OperationEmergency, Status: store.StatusPending,
			ReleaseDefinitionID: definitionID, IdempotencyKey: "k-" + definitionID,
			IdempotencyScope: "org:" + definitionID, RequestHash: "h-" + definitionID,
			CreatedAt: now, UpdatedAt: now,
		},
		Intent:           intent,
		IdempotencyScope: "org:" + definitionID, IdempotencyKeyHash: "kh-" + definitionID,
		RequestHash: "h-" + definitionID, IdempotencyExpiresAt: now.Add(time.Hour),
	}
	uowStore, ok := st.(interface {
		store.Store
		OperationCreationUnitOfWork() store.OperationCreationUnitOfWork
	})
	require.True(t, ok)
	created, err := uowStore.OperationCreationUnitOfWork()(ctx, store.OperationCreationRequest{
		Operation: cmd.Operation, Emergency: &cmd,
	})
	require.NoError(t, err)
	queued, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	return queued, created.Intent
}

func emergencyResultMsg(intent *store.EmergencyIntent, status string) *operatorv1.CommandStreamRequest {
	payload, _ := json.Marshal(map[string]any{
		"before": map[string]any{"replicas": 1},
		"after":  map[string]any{"replicas": 3},
	})
	return &operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_EmergencyResult{
			EmergencyResult: &operatorv1.EmergencyResult{
				EmergencyCommandId: intent.CommandID,
				OperationId:        intent.OperationID,
				Status:             status,
				ResultJson:         string(payload),
			},
		},
	}
}

// emergencyCommandStream opens a Hello stream against the real HTTP/2 gateway
// and returns the client stream.
func emergencyCommandStream(t *testing.T, st store.Store, svc *operator.Service) *connect.BidiStreamForClient[operatorv1.CommandStreamRequest, operatorv1.CommandStreamResponse] {
	t.Helper()
	client := commandStreamPair(t, svc)
	stream := client.CommandStream(t.Context())
	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Hello{
			Hello: &operatorv1.Hello{SessionId: "sess-1", OperatorId: "op-1", LastSeenSequence: 1},
		},
	}))
	_, err := stream.Receive()
	require.NoError(t, err)
	return stream
}

// TestEmergencyResultConvergesWhenRunningMigrationLagged (REQ-087 AC-087-01/02,
// D-111 defect ②): the authoritative result arrives over the control stream
// while the operation is still queued (the ACK queued→running migration has
// not landed). finishEmergencyResult must converge it to succeeded via the
// single-CAS store path — queued→running→succeeded with no queued→succeeded
// timeline row, and effect APPLIED.
func TestEmergencyResultConvergesWhenRunningMigrationLagged(t *testing.T) {
	st := newTestSvc(t)
	queued, intent := seedQueuedEmergency(t, st, "definition-emergency-race")
	require.Equal(t, store.StatusQueued, queued.Status)

	svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)
	stream := emergencyCommandStream(t, st, svc)
	require.NoError(t, stream.Send(emergencyResultMsg(intent, "succeeded")))
	require.NoError(t, stream.CloseRequest())
	for {
		if _, err := stream.Receive(); err != nil {
			break
		}
	}

	op, err := st.Operations().Get(context.Background(), queued.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusSucceeded, op.Status)
	assert.True(t, op.Status.IsTerminal())
	assert.Equal(t, queued.StateVersion+2, op.StateVersion)
	assert.Equal(t, store.EmergencyEffectApplied, op.EffectStatus)

	updatedIntent, err := st.EmergencyIntents().GetByOperationID(context.Background(), queued.ID)
	require.NoError(t, err)
	assert.Equal(t, store.EmergencyEffectApplied, updatedIntent.EffectStatus)

	// Timeline only contains legal hops (no queued→succeeded).
	entries, err := st.Timeline().List(context.Background(), queued.ID, 0, 1<<30)
	require.NoError(t, err)
	sawQueuedToRunning := false
	sawRunningToSucceeded := false
	for _, entry := range entries {
		if entry.Kind != string(store.TimelineEntryStateTransition) {
			continue
		}
		var data store.StateTransitionTimelineData
		require.NoError(t, json.Unmarshal(entry.Data, &data))
		if data.FromState == "queued" && data.ToState == "running" {
			sawQueuedToRunning = true
		}
		if data.FromState == "running" && data.ToState == "succeeded" {
			sawRunningToSucceeded = true
		}
		if data.FromState == "queued" {
			assert.False(t, store.OperationStatus(data.ToState).IsTerminal(),
				"queued→%s terminal hop must never be written", data.ToState)
		}
	}
	assert.True(t, sawQueuedToRunning)
	assert.True(t, sawRunningToSucceeded)
}

// TestEmergencyResultConvergesFromRunning: the common path where the ACK
// already moved the operation to running before the result arrives — only the
// running→succeeded terminal hop is applied.
func TestEmergencyResultConvergesFromRunning(t *testing.T) {
	st := newTestSvc(t)
	queued, intent := seedQueuedEmergency(t, st, "definition-emergency-running")
	running, err := st.Operations().UpdateStatus(context.Background(), queued.ID, store.StatusRunning, queued.StateVersion, "")
	require.NoError(t, err)

	svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)
	stream := emergencyCommandStream(t, st, svc)
	require.NoError(t, stream.Send(emergencyResultMsg(intent, "failed")))
	require.NoError(t, stream.CloseRequest())
	for {
		if _, err := stream.Receive(); err != nil {
			break
		}
	}

	op, err := st.Operations().Get(context.Background(), queued.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusFailed, op.Status)
	assert.Equal(t, running.StateVersion+1, op.StateVersion)
	assert.Equal(t, store.EmergencyEffectNotApplied, op.EffectStatus)
}

// TestEmergencyResultResolvesLateUnknownEffect (REQ-087 AC-087-03): a terminal
// timeout op with effect UNKNOWN accepts a late authoritative result — the
// effect is resolved exactly once with state_version+1 and an
// EMERGENCY_EFFECT_RESOLVED timeline entry.
func TestEmergencyResultResolvesLateUnknownEffect(t *testing.T) {
	st := newTestSvc(t)
	queued, intent := seedQueuedEmergency(t, st, "definition-emergency-late")
	timeout, err := st.EmergencyIntents().Finish(context.Background(), intent.ID, queued.ID, queued.StateVersion,
		store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	require.Equal(t, store.StatusTimeout, timeout.Status)

	svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)
	stream := emergencyCommandStream(t, st, svc)
	require.NoError(t, stream.Send(emergencyResultMsg(intent, "succeeded")))
	require.NoError(t, stream.CloseRequest())
	for {
		if _, err := stream.Receive(); err != nil {
			break
		}
	}

	op, err := st.Operations().Get(context.Background(), queued.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusTimeout, op.Status, "late result must not change the terminal status")
	assert.Equal(t, timeout.StateVersion+1, op.StateVersion)
	assert.Equal(t, store.EmergencyEffectApplied, op.EffectStatus)

	intentAfter, err := st.EmergencyIntents().GetByOperationID(context.Background(), queued.ID)
	require.NoError(t, err)
	assert.Equal(t, store.EmergencyEffectApplied, intentAfter.EffectStatus)

	resolved := 0
	entries, err := st.Timeline().List(context.Background(), queued.ID, 0, 1<<30)
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Kind == string(store.TimelineEntryEmergencyEffectResolved) {
			resolved++
		}
	}
	assert.Equal(t, 1, resolved)
}

// TestEmergencyResultReplayIsNoop (AC-087-03): a replayed result for the same
// terminal state+effect is an idempotent no-op — no duplicate timeline entry,
// no duplicate audit, no state change.
func TestEmergencyResultReplayIsNoop(t *testing.T) {
	st := newTestSvc(t)
	queued, intent := seedQueuedEmergency(t, st, "definition-emergency-replay")

	svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)
	stream := emergencyCommandStream(t, st, svc)
	// First result converges; second identical result is a replay.
	require.NoError(t, stream.Send(emergencyResultMsg(intent, "succeeded")))
	require.NoError(t, stream.Send(emergencyResultMsg(intent, "succeeded")))
	require.NoError(t, stream.CloseRequest())
	for {
		if _, err := stream.Receive(); err != nil {
			break
		}
	}

	op, err := st.Operations().Get(context.Background(), queued.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusSucceeded, op.Status)
	assert.Equal(t, queued.StateVersion+2, op.StateVersion, "replay must not bump the state version again")

	stateTransitions := 0
	entries, err := st.Timeline().List(context.Background(), queued.ID, 0, 1<<30)
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Kind == string(store.TimelineEntryStateTransition) {
			stateTransitions++
		}
	}
	// pending→queued (seed) + queued→running + running→succeeded; replay adds none.
	assert.Equal(t, 3, stateTransitions)
}

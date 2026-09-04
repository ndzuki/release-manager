//nolint:revive // test doubles intentionally implement the broad Engine contract
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/commandtype"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/ndzuki/release-manager/internal/operator/localstore"
	"github.com/ndzuki/release-manager/internal/operator/observer"
	"github.com/ndzuki/release-manager/internal/operator/secretmetadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
)

// TestAgent_InstallReplaysDeployedRelease locks the idempotent install
// contract (real smoke 2026-08-27): the preflight pipeline dispatches
// artifact/render/cluster as INSTALL-typed commands (the wire Command carries
// no stage), so later stages find the release already deployed by the first
// install. executeInstall must replay a deployed release as success without
// calling Install again.
// REQ-085 AC-085-01/02: a successful install reports the authoritative
// workload identity (kind/name/namespace/uid read from the live cluster)
// BEFORE the terminal result, so the orchestrator has identity persisted
// when the operation completes. Job workloads are outside the emergency
// whitelist and workloads without a live uid are skipped (fail closed).
func TestAgent_InstallReportsWorkloadIdentityBeforeTerminalResult(t *testing.T) {
	engine := &recordingEngine{
		release: &helmengine.Release{
			Name: "example", Namespace: "apps", Revision: 1, Status: "deployed",
			ManifestDigest: "sha256:manifest",
			Workloads: []helmengine.WorkloadSummary{
				{Kind: "Deployment", Name: "api", Namespace: "apps"},
				{Kind: "Job", Name: "migrate", Namespace: "apps"},
				{Kind: "StatefulSet", Name: "api-sts", Namespace: "apps"},
			},
		},
	}
	kube := kubernetesfake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps", UID: "uid-deployment-1"},
	})
	store := newMemoryStore()
	agent, err := New(Config{
		Client: noopClient{}, Engine: engine, Store: store,
		SessionID: "session-1", OperatorID: "operator-1", KubeClient: kube,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		InstallFlags: InstallFlags{Atomic: true, Timeout: time.Minute},
	})
	require.NoError(t, err)
	stream := newTestStream()

	require.NoError(t, agent.handleCommand(t.Context(), stream, installCommand("cmd-identity")))
	require.Len(t, stream.sent, 3, "expected [AckPersisted, WorkloadIdentityReport, Result]")
	assert.Equal(t, operatorv1.AckType_ACK_TYPE_PERSISTED, stream.sent[0].GetAck().GetAckType())

	report := stream.sent[1].GetWorkloadIdentityReport()
	require.NotNil(t, report, "identity report must precede the terminal result")
	require.Len(t, report.GetItems(), 1, "Job and uid-less workloads must be skipped")
	item := report.GetItems()[0]
	assert.Equal(t, "apps", item.GetReleaseNamespace())
	assert.Equal(t, "example", item.GetReleaseName())
	assert.Equal(t, "DEPLOYMENT", item.GetKind())
	assert.Equal(t, "api", item.GetName())
	assert.Equal(t, "apps", item.GetNamespace())
	assert.Equal(t, "uid-deployment-1", item.GetUid())

	assert.Equal(t, "succeeded", stream.sent[2].GetResult().GetStatus(), "terminal result stays last")
}

// REQ-085: releases without workloads or without a kube client send no
// identity report — the terminal result sequence stays unchanged.
func TestAgent_InstallWithoutWorkloadsSendsNoIdentityReport(t *testing.T) {
	engine := &recordingEngine{
		release: &helmengine.Release{Name: "example", Namespace: "apps", Revision: 1, Status: "deployed"},
	}
	kube := kubernetesfake.NewSimpleClientset()
	store := newMemoryStore()
	agent, err := New(Config{
		Client: noopClient{}, Engine: engine, Store: store,
		SessionID: "session-1", OperatorID: "operator-1", KubeClient: kube,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		InstallFlags: InstallFlags{Atomic: true, Timeout: time.Minute},
	})
	require.NoError(t, err)
	stream := newTestStream()

	require.NoError(t, agent.handleCommand(t.Context(), stream, installCommand("cmd-no-workloads")))
	require.Len(t, stream.sent, 2)
	assert.NotNil(t, stream.sent[0].GetAck())
	assert.NotNil(t, stream.sent[1].GetResult())
}

// REQ-085 startup scan: after the stream session is established, the agent
// reports identities for releases already deployed (engine.List + Status),
// best-effort — a failing List only logs a warning and the command loop
// keeps running.
func TestAgent_StartupScanReportsWorkloadIdentity(t *testing.T) {
	engine := &recordingEngine{
		list: []*helmengine.ReleaseListItem{{Namespace: "apps", Name: "example", Revision: 1}},
		statusRelease: &helmengine.Release{
			Name: "example", Namespace: "apps", Revision: 1, Status: "deployed",
			Workloads: []helmengine.WorkloadSummary{{Kind: "Deployment", Name: "api", Namespace: "apps"}},
		},
	}
	kube := kubernetesfake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps", UID: "uid-scan-1"},
	})
	stream := newScriptedStream(&operatorv1.CommandStreamResponse{
		Payload: &operatorv1.CommandStreamResponse_SessionEstablished{
			SessionEstablished: &operatorv1.SessionEstablished{SessionId: "session-1"},
		},
	})
	agent, err := New(Config{
		Client: scriptedClient{stream: stream}, Engine: engine, Store: newMemoryStore(),
		SessionID: "session-1", OperatorID: "operator-1", KubeClient: kube,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		InstallFlags: InstallFlags{Atomic: true, Timeout: time.Minute},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	go func() { runDone <- agent.Run(ctx) }()
	waitForStreamSend(t, stream, 2, runDone)
	cancel()
	close(stream.done)
	require.NoError(t, <-runDone)

	require.Len(t, stream.sent, 2, "expected [Hello, WorkloadIdentityReport]")
	report := stream.sent[1].GetWorkloadIdentityReport()
	require.NotNil(t, report, "startup scan must report the deployed release identity")
	require.Len(t, report.GetItems(), 1)
	assert.Equal(t, "DEPLOYMENT", report.GetItems()[0].GetKind())
	assert.Equal(t, "uid-scan-1", report.GetItems()[0].GetUid())
}

// REQ-085 startup scan failure is non-fatal: List error logs a warning and
// the agent still enters the command loop (context cancel ends Run cleanly).
func TestAgent_StartupScanFailureDoesNotBreakLoop(t *testing.T) {
	engine := &recordingEngine{listErr: errors.New("list exploded")}
	stream := newScriptedStream(&operatorv1.CommandStreamResponse{
		Payload: &operatorv1.CommandStreamResponse_SessionEstablished{
			SessionEstablished: &operatorv1.SessionEstablished{SessionId: "session-1"},
		},
	})
	agent, err := New(Config{
		Client: scriptedClient{stream: stream}, Engine: engine, Store: newMemoryStore(),
		SessionID: "session-1", OperatorID: "operator-1",
		KubeClient: kubernetesfake.NewSimpleClientset(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		InstallFlags: InstallFlags{Atomic: true, Timeout: time.Minute},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	go func() { runDone <- agent.Run(ctx) }()
	waitForStreamSend(t, stream, 1, runDone)
	cancel()
	close(stream.done)
	require.NoError(t, <-runDone)

	require.Len(t, stream.sent, 1, "scan failure must not produce a report")
	assert.NotNil(t, stream.sent[0].GetHello())
}

// waitForStreamSend blocks until the scripted stream recorded n messages or
// the run terminated unexpectedly (fails the test on early exit).
func waitForStreamSend(t *testing.T, stream *scriptedStream, n int, runDone <-chan error) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if stream.sentCount() >= n {
			return
		}
		select {
		case err := <-runDone:
			t.Fatalf("agent Run exited before %d sends: %v (sent=%d)", n, err, stream.sentCount())
		case <-deadline:
			t.Fatalf("timed out waiting for %d sends (sent=%d)", n, stream.sentCount())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestAgent_InstallReplaysDeployedRelease(t *testing.T) {
	engine := &recordingEngine{
		statusRelease: &helmengine.Release{
			Name: "example", Namespace: "apps", Revision: 1, Status: "deployed", ManifestDigest: "sha256:replay",
		},
	}
	store := newMemoryStore()
	agent := newTestAgent(t, engine, store, nil)
	stream := newTestStream()
	command := installCommand("cmd-replay")

	require.NoError(t, agent.handleCommand(t.Context(), stream, command))
	require.Len(t, stream.sent, 2)
	assert.Equal(t, "succeeded", stream.sent[1].GetResult().GetStatus())
	assert.Equal(t, 0, engine.installCalls, "deployed release must not re-install")
	assert.Equal(t, 1, engine.statusCalls)
	assert.Contains(t, stream.sent[1].GetResult().GetResultJson(), `"manifest_digest":"sha256:replay"`)
}

// TestAgent_InstallCommand covers the happy path install.
func TestAgent_InstallCommand(t *testing.T) {
	engine := &recordingEngine{
		release: &helmengine.Release{
			Name:           "example",
			Namespace:      "apps",
			Revision:       1,
			Status:         "deployed",
			Chart:          "example-chart-1.0.0",
			ManifestDigest: "sha256:manifest",
		},
	}
	store := newMemoryStore()
	notifier := new(recordingNotifier)
	agent := newTestAgent(t, engine, store, notifier)
	stream := newTestStream()
	command := installCommand("cmd-1")

	require.NoError(t, agent.handleCommand(t.Context(), stream, command))
	require.Len(t, stream.sent, 2)
	assert.Equal(t, operatorv1.AckType_ACK_TYPE_PERSISTED, stream.sent[0].GetAck().GetAckType())
	assert.Equal(t, "succeeded", stream.sent[1].GetResult().GetStatus())
	assert.Contains(t, stream.sent[1].GetResult().GetResultJson(), `"revision":1`)
	assert.Equal(t, 1, notifier.calls)
	assert.Equal(t, "definition-1", notifier.definitionID)
	assert.Equal(t, "apps", engine.lastInstall.Namespace)
	assert.Equal(t, "example", engine.lastInstall.ReleaseName)
	assert.Equal(t, "oci://registry.example.com/charts/example", engine.lastInstall.ChartPath)
	assert.Equal(t, "1.0.0", engine.lastInstall.ChartVersion)
	assert.True(t, engine.lastInstall.CreateNamespace)
	assert.Equal(t, 45*time.Second, engine.lastInstall.Timeout)
	assert.Equal(t, 1, notifier.calls)
}

// TestAgent_InstallAppliesBundleImageOverride locks the install image
// contract (real smoke 2026-08-27): the bundle image (values_path +
// FULL_REFERENCE) must be merged into the installed values — otherwise the
// chart deploys its static image.repository (bare localhost:5001/... →
// ErrImagePull :latest).
func TestAgent_InstallAppliesBundleImageOverride(t *testing.T) {
	engine := &recordingEngine{
		release: &helmengine.Release{Name: "example", Namespace: "apps", Revision: 1, Status: "deployed", ManifestDigest: "sha256:m"},
	}
	store := newMemoryStore()
	agent := newTestAgent(t, engine, store, nil)
	command := installCommand("cmd-img")
	command.Bundle = &commonv1.ReleaseBundle{
		Name: "bundle", ChartRef: "oci://registry.example.com/charts/example", ChartVersion: "1.0.0",
		Images: []*commonv1.BundleImage{{
			Ref: "localhost:5001/release-fixture:dev", Digest: "sha256:abc123",
			ValuesPath: "image.repository",
			ValueKind:  commonv1.ImageValueKind_IMAGE_VALUE_KIND_FULL_REFERENCE,
		}},
	}
	command.Values = []byte(`{"message":"hello"}`)

	require.NoError(t, agent.handleCommand(t.Context(), newTestStream(), command))
	image, ok := engine.lastInstall.Values["image"].(map[string]interface{})
	require.True(t, ok, "install values must carry an image object")
	assert.Equal(t, "localhost:5001/release-fixture:dev@sha256:abc123", image["repository"])
	assert.Equal(t, "hello", engine.lastInstall.Values["message"])
}

func TestAgent_DuplicateCommandReturnsCachedResult(t *testing.T) {
	engine := new(recordingEngine)
	store := newMemoryStore()
	agent := newTestAgent(t, engine, store, nil)
	stream := newTestStream()
	command := installCommand("cmd-duplicate")
	resultJSON := `{"operation_id":"op-1","command_id":"cmd-duplicate","status":"succeeded","release":{"name":"example","namespace":"apps","revision":1,"status":"deployed","chart":"example-chart-1.0.0","manifest_digest":"sha256:manifest","notes":""},"inventory_sync_hint":true,"resource_summary":{"manifest_digest":"sha256:manifest"}}`
	require.NoError(t, store.Save(t.Context(), &localstore.CommandEntry{
		CommandID:     command.GetCommandId(),
		OutboxID:      command.GetOutboxId(),
		OperationID:   command.GetOperationId(),
		OperationType: command.GetOperationType(),
		Sequence:      command.GetSequence(),
		Status:        localstore.StatusSucceeded,
		ResultJSON:    resultJSON,
	}))

	require.NoError(t, agent.handleCommand(t.Context(), stream, command))
	require.Len(t, stream.sent, 1)
	assert.Equal(t, resultJSON, stream.sent[0].GetResult().GetResultJson())
	assert.Equal(t, 0, engine.installCalls)
}

func TestAgent_InstallErrorMapping(t *testing.T) {
	tests := []struct {
		name      string
		engineErr error
		wantCode  string
	}{
		{name: "already exists", engineErr: helmengine.ErrAlreadyExists, wantCode: "release_already_exists"},
		{name: "timeout", engineErr: helmengine.ErrTimeout, wantCode: "timeout"},
		{name: "cancelled", engineErr: helmengine.ErrCancelled, wantCode: "cancelled"},
		{name: "generic failure", engineErr: helmengine.ErrActionFailed, wantCode: "helm_install_failed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine := &recordingEngine{err: test.engineErr}
			store := newMemoryStore()
			agent := newTestAgent(t, engine, store, nil)
			stream := newTestStream()

			require.NoError(t, agent.handleCommand(t.Context(), stream, installCommand("cmd-error")))
			require.Len(t, stream.sent, 2)
			assert.Equal(t, "failed", stream.sent[1].GetResult().GetStatus())
			assert.Contains(t, stream.sent[1].GetResult().GetResultJson(), `"code":"`+test.wantCode+`"`)
		})
	}
}

func TestAgent_UpgradeCommand(t *testing.T) {
	valuesJSON := []byte(`{"message":"hello"}`)
	engine := &recordingEngine{
		status: &helmengine.Release{
			Name:       "example",
			Namespace:  "apps",
			Revision:   1,
			Status:     "deployed",
			Provenance: "legacy",
		},
		release: &helmengine.Release{
			Name:                  "example",
			Namespace:             "apps",
			Revision:              2,
			Status:                "deployed",
			ManifestDigest:        "manifest-v2",
			BundleDigest:          "sha256:bundle",
			ChartDigest:           "sha256:chart",
			EffectiveValuesDigest: sha256Hex(valuesJSON),
			Provenance:            "managed",
		},
	}
	store := newMemoryStore()
	notifier := new(recordingNotifier)
	agent := newTestAgent(t, engine, store, notifier)
	stream := newTestStream()
	command := upgradeCommand("cmd-upgrade", valuesJSON, sha256Hex(valuesJSON))

	require.NoError(t, agent.handleCommand(t.Context(), stream, command))
	require.Len(t, stream.sent, 2)
	assert.Equal(t, operatorv1.AckType_ACK_TYPE_PERSISTED, stream.sent[0].GetAck().GetAckType())
	result := stream.sent[1].GetCommandResult()
	require.NotNil(t, result)
	assert.Equal(t, "succeeded", result.GetStatus())
	assert.Equal(t, uint64(2), result.GetUpgrade().GetActive().GetHelmRevision())
	assert.Equal(t, operatorv1.ReleaseProvenance_RELEASE_PROVENANCE_LEGACY, result.GetUpgrade().GetFrom().GetProvenance())
	assert.Equal(t, 1, engine.upgradeCalls)
	assert.True(t, engine.lastUpgrade.ResetValues)
	assert.False(t, engine.lastUpgrade.ReuseValues)
	assert.Equal(t, 1, notifier.calls)
}

func TestAgent_UpgradeDigestMismatchDoesNotCallHelm(t *testing.T) {
	engine := new(recordingEngine)
	agent := newTestAgent(t, engine, newMemoryStore(), nil)
	stream := newTestStream()
	command := upgradeCommand("cmd-digest", []byte(`{"message":"hello"}`), "wrong")

	require.NoError(t, agent.handleCommand(t.Context(), stream, command))
	require.Len(t, stream.sent, 2)
	result := stream.sent[1].GetCommandResult()
	require.NotNil(t, result)
	assert.Equal(t, "digest_mismatch", result.GetError().GetCode())
	assert.Zero(t, engine.upgradeCalls)
}

func TestAgent_UpgradeAtomicFailureReturnsActiveSnapshot(t *testing.T) {
	active := &helmengine.Release{Name: "example", Namespace: "apps", Revision: 1, Status: "deployed", ManifestDigest: "manifest-v1"}
	engine := &recordingEngine{status: active, release: active, upgradeErr: helmengine.ErrActionFailed}
	agent := newTestAgent(t, engine, newMemoryStore(), nil)
	stream := newTestStream()
	valuesJSON := []byte(`{"message":"hello"}`)

	require.NoError(t, agent.handleCommand(t.Context(), stream, upgradeCommand("cmd-atomic", valuesJSON, sha256Hex(valuesJSON))))
	result := stream.sent[1].GetCommandResult()
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.GetStatus())
	assert.Equal(t, "helm_upgrade_failed", result.GetError().GetCode())
	// TASK-084 AC-084-04: an undecorated engine error carries no rollback
	// signal — the legacy revision-equality heuristic (1==1 here) fabricated
	// rollback_succeeded; the mapping must fail closed.
	assert.False(t, result.GetUpgrade().GetRollbackSucceeded())
	assert.Equal(t, uint64(1), result.GetUpgrade().GetActive().GetHelmRevision())
}

func TestAgent_UpgradeErrorMapping(t *testing.T) {
	valuesJSON := []byte(`{"message":"hello"}`)
	tests := []struct {
		name       string
		engineErr  error
		wantCode   string
		retryable  bool
		wantStatus string
	}{
		{name: "revision conflict", engineErr: helmengine.ErrConflict, wantCode: "revision_conflict", retryable: true, wantStatus: "failed"},
		{name: "release busy", engineErr: helmengine.ErrReleaseBusy, wantCode: "release_busy", retryable: true, wantStatus: "failed"},
		{name: "not deployed", engineErr: helmengine.ErrReleaseNotDeployed, wantCode: "release_not_deployed", wantStatus: "failed"},
		{name: "rollback failed", engineErr: helmengine.ErrAtomicRollbackFailed, wantCode: "atomic_rollback_failed", wantStatus: "failed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			active := &helmengine.Release{Name: "example", Namespace: "apps", Revision: 1, Status: "deployed"}
			engine := &recordingEngine{status: active, release: active, upgradeErr: test.engineErr}
			agent := newTestAgent(t, engine, newMemoryStore(), nil)
			stream := newTestStream()

			require.NoError(t, agent.handleCommand(t.Context(), stream, upgradeCommand("cmd-"+test.wantCode, valuesJSON, sha256Hex(valuesJSON))))
			result := stream.sent[1].GetCommandResult()
			require.NotNil(t, result)
			assert.Equal(t, test.wantStatus, result.GetStatus())
			assert.Equal(t, test.wantCode, result.GetError().GetCode())
			assert.Equal(t, test.retryable, result.GetError().GetRetryable())
			assert.NotContains(t, result.GetError().GetMessage(), "example")
		})
	}
}

func TestAgent_UpgradePreflightRejectsInventoryStale(t *testing.T) {
	engine := &recordingEngine{
		statusRelease: &helmengine.Release{
			Name:      "example",
			Namespace: "apps",
			Revision:  2,
			Status:    "deployed",
		},
		release: &helmengine.Release{Revision: 3},
	}
	agent := newTestAgent(t, engine, newMemoryStore(), nil)
	stream := newTestStream()

	valuesJSON := []byte(`{"message":"hello"}`)
	command := upgradeCommand("cmd-inventory-stale", valuesJSON, sha256Hex(valuesJSON))
	command.GetUpgrade().ExpectedRevision = 2 // 现场 revision=2，跳过 revision_conflict
	command.ExpectedCurrentRevision = 1       // 与现场 revision=2 不符 → inventory_stale
	require.NoError(t, agent.handleCommand(t.Context(), stream, command))
	require.Len(t, stream.sent, 2)
	result := stream.sent[1].GetCommandResult()
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.GetStatus())
	assert.Equal(t, "inventory_stale", result.GetError().GetCode())
	assert.Equal(t, 2, engine.statusCalls)
	assert.Zero(t, engine.upgradeCalls)
}

func TestAgent_RollbackPreflightRejectsMissingTargetRevision(t *testing.T) {
	engine := &recordingEngine{
		history: []helmengine.ReleaseHistoryEntry{
			{Revision: 2, Status: "deployed", Chart: "example-2.0.0"},
			{Revision: 3, Status: "deployed", Chart: "example-3.0.0"},
		},
		release: &helmengine.Release{Revision: 4},
	}
	agent := newTestAgent(t, engine, newMemoryStore(), nil)
	stream := newTestStream()

	require.NoError(t, agent.handleCommand(t.Context(), stream, rollbackCommand("cmd-target-missing")))
	require.Len(t, stream.sent, 2)
	assert.Equal(t, "failed", stream.sent[1].GetResult().GetStatus())
	assert.Contains(t, stream.sent[1].GetResult().GetResultJson(), `"code":"target_revision_not_found"`)
	assert.Equal(t, 1, engine.historyCalls)
	assert.Zero(t, engine.rollbackCalls)
}

func TestAgent_RollbackCommand(t *testing.T) {
	engine := &recordingEngine{
		history: []helmengine.ReleaseHistoryEntry{
			{Revision: 1, Status: "superseded", Chart: "example-1.0.0"},
			{Revision: 3, Status: "deployed", Chart: "example-3.0.0"},
		},
		release: &helmengine.Release{
			Name: "example", Namespace: "apps", Revision: 4, Status: "deployed",
		},
	}
	agent := newTestAgent(t, engine, newMemoryStore(), nil)
	stream := newTestStream()

	require.NoError(t, agent.handleCommand(t.Context(), stream, rollbackCommand("cmd-rollback")))
	require.Len(t, stream.sent, 2)
	assert.Equal(t, "succeeded", stream.sent[1].GetResult().GetStatus())
	assert.Equal(t, 1, engine.historyCalls)
	assert.Equal(t, 1, engine.rollbackCalls)
	assert.Equal(t, 1, engine.lastRollback.TargetRevision)
	assert.Equal(t, "apps", engine.lastRollback.Namespace)
	assert.Equal(t, "example", engine.lastRollback.ReleaseName)
}

func TestAgent_EmergencyCommandPersistsAcknowledgesAndExecutes(t *testing.T) {
	store := newMemoryStore()
	executor := &recordingEmergencyExecutor{resultJSON: `{"before":{"replicas":2},"after":{"replicas":3}}`}
	agent, err := New(Config{
		Client: noopClient{}, Engine: new(recordingEngine), Store: store,
		EmergencyExecutor: executor, SessionID: "session-1", OperatorID: "operator-1",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	stream := newTestStream()
	command := &operatorv1.EmergencyCommand{
		CommandId: "emergency-command", OperationId: "emergency-operation",
		WorkloadKind: "DEPLOYMENT", WorkloadName: "api", WorkloadNamespace: "apps", WorkloadUid: "uid-api",
		Change: &operatorv1.EmergencyCommand_SetReplicas{SetReplicas: &operatorv1.EmergencySetReplicas{Replicas: 3}},
	}

	require.NoError(t, agent.handleEmergencyCommand(t.Context(), stream, command))
	require.Len(t, stream.sent, 2)
	assert.Equal(t, operatorv1.AckType_ACK_TYPE_PERSISTED, stream.sent[0].GetEmergencyAck().GetAckType())
	assert.Equal(t, "succeeded", stream.sent[1].GetEmergencyResult().GetStatus())
	assert.Equal(t, executor.resultJSON, stream.sent[1].GetEmergencyResult().GetResultJson())
	assert.Equal(t, 1, executor.calls)

	require.NoError(t, agent.handleEmergencyCommand(t.Context(), stream, command))
	require.Len(t, stream.sent, 3)
	assert.Equal(t, "succeeded", stream.sent[2].GetEmergencyResult().GetStatus())
	assert.Equal(t, 1, executor.calls)
}

func TestAgent_ReplayEmergencyPersistedWaitsForResult(t *testing.T) {
	store := newMemoryStore()
	executor := &recordingEmergencyExecutor{resultJSON: `{"before":{"replicas":2},"after":{"replicas":3}}`}
	agent, err := New(Config{
		Client: noopClient{}, Engine: new(recordingEngine), Store: store,
		EmergencyExecutor: executor, SessionID: "session-1", OperatorID: "operator-1",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	command := &operatorv1.EmergencyCommand{
		CommandId: "emergency-persisted", OperationId: "emergency-operation",
		WorkloadKind: "DEPLOYMENT", WorkloadName: "api", WorkloadNamespace: "apps", WorkloadUid: "uid-api",
		Change: &operatorv1.EmergencyCommand_SetReplicas{SetReplicas: &operatorv1.EmergencySetReplicas{Replicas: 3}},
	}
	payload, err := proto.Marshal(command)
	require.NoError(t, err)
	require.NoError(t, store.Save(t.Context(), &localstore.CommandEntry{
		CommandID: command.GetCommandId(), OperationID: command.GetOperationId(), OperationType: "EMERGENCY",
		Payload: payload, Status: localstore.StatusPending,
	}))
	stream := newTestStream()

	require.NoError(t, agent.replayActive(t.Context(), stream))
	require.Len(t, stream.sent, 1)
	assert.Equal(t, operatorv1.AckType_ACK_TYPE_PERSISTED, stream.sent[0].GetEmergencyAck().GetAckType())
	assert.Equal(t, 0, executor.calls)
}

func TestAgent_EmergencyCommandReturnsStableFailure(t *testing.T) {
	executor := &recordingEmergencyExecutor{err: codedEmergencyError{code: "resource_version_conflict"}}
	agent, err := New(Config{
		Client: noopClient{}, Engine: new(recordingEngine), Store: newMemoryStore(),
		EmergencyExecutor: executor, SessionID: "session-1", OperatorID: "operator-1",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	stream := newTestStream()
	command := &operatorv1.EmergencyCommand{
		CommandId: "emergency-failed", OperationId: "emergency-operation",
		WorkloadKind: "DEPLOYMENT", WorkloadName: "api", WorkloadNamespace: "apps", WorkloadUid: "uid-api",
		Change: &operatorv1.EmergencyCommand_SetReplicas{SetReplicas: &operatorv1.EmergencySetReplicas{Replicas: 3}},
	}

	require.NoError(t, agent.handleEmergencyCommand(t.Context(), stream, command))
	require.Len(t, stream.sent, 2)
	assert.Equal(t, "failed", stream.sent[1].GetEmergencyResult().GetStatus())
	assert.Equal(t, "resource_version_conflict", stream.sent[1].GetEmergencyResult().GetErrorCode())
}

type recordingEmergencyExecutor struct {
	calls      int
	resultJSON string
	err        error
}

func (e *recordingEmergencyExecutor) Execute(context.Context, *operatorv1.EmergencyCommand) (string, error) {
	e.calls++
	return e.resultJSON, e.err
}

type codedEmergencyError struct{ code string }

func (e codedEmergencyError) Error() string     { return e.code }
func (e codedEmergencyError) ErrorCode() string { return e.code }

func TestAgent_SecretMetadataCommandReturnsOnlyMetadata(t *testing.T) {
	lister := recordingSecretLister{secrets: []secretmetadata.Secret{{Name: "app-config", Keys: []string{"ca.crt", "token"}}}}
	agent, err := New(Config{
		Client: noopClient{}, Engine: new(recordingEngine), Store: newMemoryStore(),
		SecretLister: lister, SessionID: "session-1", OperatorID: "operator-1",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	stream := newTestStream()
	command := &operatorv1.Command{OutboxId: "outbox-secrets", CommandId: "command-secrets", OperationId: "request-secrets", OperationType: commandtype.SecretMetadataList, Namespace: "apps", Sequence: 10}

	require.NoError(t, agent.handleCommand(t.Context(), stream, command))
	require.Len(t, stream.sent, 2)
	resultJSON := stream.sent[1].GetResult().GetResultJson()
	assert.Equal(t, "succeeded", stream.sent[1].GetResult().GetStatus())
	assert.JSONEq(t, `{"command_id":"command-secrets","definition_id":"","inventory_sync_hint":false,"operation_id":"request-secrets","resource_summary":{},"secrets":[{"name":"app-config","keys":["ca.crt","token"]}],"status":"succeeded"}`, resultJSON)
	assert.NotContains(t, resultJSON, "secret-value")
}

// REQ-077 wiring invariant: rollout observation requires a kube client for
// generation resolution; the agent must fail fast at construction.
func TestAgent_ObserverRequiresKubeClient(t *testing.T) {
	_, err := New(Config{
		Client: noopClient{}, Engine: new(recordingEngine), Store: newMemoryStore(),
		SessionID: "session-1", OperatorID: "operator-1",
		Observer: observer.NewFake(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "observer requires kube client")
}

type recordingSecretLister struct {
	secrets []secretmetadata.Secret
}

func (l recordingSecretLister) List(context.Context, string) ([]secretmetadata.Secret, error) {
	return l.secrets, nil
}

type recordingSyncExecutor struct {
	calls int
	err   error
}

func (e *recordingSyncExecutor) SyncNow(context.Context) error {
	e.calls++
	return e.err
}
func newTestAgent(
	t *testing.T,
	engine helmengine.Engine,
	store localstore.Store,
	notifier InventoryNotifier,
) *Agent {
	t.Helper()

	agent, err := New(Config{
		Client:     noopClient{},
		Engine:     engine,
		Store:      store,
		Notifier:   notifier,
		SessionID:  "session-1",
		OperatorID: "operator-1",
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		InstallFlags: InstallFlags{
			Atomic:  true,
			Timeout: time.Minute,
		},
	})
	require.NoError(t, err)
	return agent
}

func installCommand(commandID string) *operatorv1.Command {
	return &operatorv1.Command{
		OutboxId:      "outbox-1",
		CommandId:     commandID,
		OperationId:   "op-1",
		OperationType: "INSTALL",
		Bundle: &commonv1.ReleaseBundle{
			Name:         "ignored-bundle-name",
			ChartRef:     "oci://registry.example.com/charts/example",
			ChartVersion: "1.0.0",
		},
		Values:          []byte(`{"message":"hello"}`),
		Sequence:        7,
		DefinitionId:    "definition-1",
		Namespace:       "apps",
		ReleaseName:     "example",
		CreateNamespace: true,
		TimeoutSeconds:  45,
	}
}

func upgradeCommand(commandID string, valuesJSON []byte, digest string) *operatorv1.Command {
	return &operatorv1.Command{
		OutboxId:       "outbox-upgrade",
		CommandId:      commandID,
		OperationId:    "operation-upgrade",
		OperationType:  "UPGRADE",
		DefinitionId:   "definition-1",
		Sequence:       8,
		PayloadVersion: 2,
		TypedPayload: &operatorv1.Command_Upgrade{Upgrade: &operatorv1.UpgradeCommand{
			DefinitionId:          "definition-1",
			Namespace:             "apps",
			ReleaseName:           "example",
			Bundle:                &operatorv1.ReleaseBundleSnapshot{BundleDigest: "sha256:bundle"},
			Chart:                 &operatorv1.ChartReference{ResolvedUri: "chart.tgz", Version: "1.0.0", Digest: "sha256:chart"},
			EffectiveValuesJson:   valuesJSON,
			EffectiveValuesDigest: digest,
			OperationId:           "operation-upgrade",
			CommandId:             commandID,
			ExpectedRevision:      1,
			Atomic:                true,
			Timeout:               durationpb.New(time.Minute),
			MaxHistory:            10,
		}},
	}
}

//nolint:unused // Reserved for future rollback-command tests; intentionally kept.
func rollbackCommand(commandID string) *operatorv1.Command {
	return &operatorv1.Command{
		OutboxId:                "outbox-rollback",
		CommandId:               commandID,
		OperationId:             "op-rollback",
		OperationType:           "ROLLBACK",
		DefinitionId:            "definition-rollback",
		Namespace:               "apps",
		ReleaseName:             "example",
		ExpectedCurrentRevision: 3,
		TargetRevision:          1,
		TimeoutSeconds:          45,
	}
}

type noopClient struct{}

func (noopClient) CommandStream(context.Context) Stream { return newTestStream() }

type testStream struct {
	sent []*operatorv1.CommandStreamRequest
}

func newTestStream() *testStream { return &testStream{sent: []*operatorv1.CommandStreamRequest{}} }

func (s *testStream) Send(request *operatorv1.CommandStreamRequest) error {
	s.sent = append(s.sent, request)
	return nil
}

func (*testStream) Receive() (*operatorv1.CommandStreamResponse, error) {
	return nil, errors.New("not implemented")
}
func (*testStream) CloseRequest() error  { return nil }
func (*testStream) CloseResponse() error { return nil }

type recordingNotifier struct {
	calls        int
	definitionID string
}

func (n *recordingNotifier) NotifyOperationComplete(_, _, _, definitionID string) {
	n.calls++
	n.definitionID = definitionID
}

type recordingEngine struct {
	installCalls  int
	upgradeCalls  int
	rollbackCalls int
	statusCalls   int
	historyCalls  int
	lastInstall   helmengine.InstallOptions
	lastUpgrade   helmengine.UpgradeOptions
	lastRollback  helmengine.RollbackOptions
	statusRelease *helmengine.Release
	status        *helmengine.Release
	statusErr     error
	statusOptions []helmengine.StatusOptions
	// statusFailOnCall, when > 0, makes the Status call at that ordinal fail
	// with statusErr (TASK-084 second-Status failure matrix).
	statusFailOnCall int
	upgradeErr       error
	history          []helmengine.ReleaseHistoryEntry
	release          *helmengine.Release
	err              error
	// list/listErr script the REQ-085 startup identity scan; both empty
	// keeps the legacy "not implemented" behaviour.
	list    []*helmengine.ReleaseListItem
	listErr error
}

func (e *recordingEngine) Install(_ context.Context, opts helmengine.InstallOptions) (*helmengine.Release, error) {
	e.installCalls++
	e.lastInstall = opts
	return e.release, e.err
}

func (e *recordingEngine) Upgrade(_ context.Context, opts helmengine.UpgradeOptions) (*helmengine.Release, error) {
	e.upgradeCalls++
	e.lastUpgrade = opts
	return e.release, e.upgradeErr
}
func (e *recordingEngine) Rollback(_ context.Context, opts helmengine.RollbackOptions) (*helmengine.Release, error) {
	e.rollbackCalls++
	e.lastRollback = opts
	return e.release, e.err
}
func (e *recordingEngine) Status(_ context.Context, opts helmengine.StatusOptions) (*helmengine.Release, error) {
	e.statusCalls++
	e.statusOptions = append(e.statusOptions, opts)
	if e.statusFailOnCall > 0 {
		if e.statusCalls == e.statusFailOnCall && e.statusErr != nil {
			return nil, e.statusErr
		}
	} else if e.statusErr != nil {
		return nil, e.statusErr
	}
	if e.statusRelease != nil {
		return e.statusRelease, nil
	}
	if e.status == nil {
		return nil, helmengine.ErrNotFound
	}
	return e.status, nil
}
func (e *recordingEngine) History(context.Context, helmengine.HistoryOptions) ([]helmengine.ReleaseHistoryEntry, error) {
	e.historyCalls++
	return e.history, e.err
}
func (*recordingEngine) GetValues(context.Context, helmengine.GetValuesOptions) (map[string]interface{}, error) {
	return nil, errors.New("not implemented")
}
func (e *recordingEngine) List(context.Context, string) ([]*helmengine.ReleaseListItem, error) {
	if e.list != nil || e.listErr != nil {
		return e.list, e.listErr
	}
	return nil, errors.New("not implemented")
}

type memoryStore struct {
	entries map[string]*localstore.CommandEntry
}

func newMemoryStore() *memoryStore {
	return &memoryStore{entries: map[string]*localstore.CommandEntry{}}
}

func (s *memoryStore) Save(_ context.Context, entry *localstore.CommandEntry) error {
	cloned := *entry
	s.entries[entry.CommandID] = &cloned
	return nil
}
func (s *memoryStore) Get(_ context.Context, commandID string) (*localstore.CommandEntry, error) {
	entry, ok := s.entries[commandID]
	if !ok {
		return nil, localstore.ErrNotFound
	}
	cloned := *entry
	return &cloned, nil
}
func (s *memoryStore) GetByOutboxID(_ context.Context, outboxID string) (*localstore.CommandEntry, error) {
	for _, entry := range s.entries {
		if entry.OutboxID == outboxID {
			cloned := *entry
			return &cloned, nil
		}
	}
	return nil, localstore.ErrNotFound
}
func (s *memoryStore) UpdateStatus(_ context.Context, commandID, status, resultJSON string) error {
	entry, ok := s.entries[commandID]
	if !ok {
		return localstore.ErrNotFound
	}
	entry.Status = status
	if resultJSON != "" {
		entry.ResultJSON = resultJSON
	}
	return nil
}
func (s *memoryStore) ListActive(context.Context) ([]*localstore.CommandEntry, error) {
	entries := []*localstore.CommandEntry{}
	for _, entry := range s.entries {
		if !localstore.IsTerminal(entry.Status) {
			cloned := *entry
			entries = append(entries, &cloned)
		}
	}
	return entries, nil
}
func (s *memoryStore) LastSequence(context.Context) (int64, error) {
	var last int64
	for _, entry := range s.entries {
		if entry.Sequence > last {
			last = entry.Sequence
		}
	}
	return last, nil
}
func (*memoryStore) Close() error { return nil }

func TestAgent_UpgradeReleaseNotFound(t *testing.T) {
	engine := &recordingEngine{statusErr: helmengine.ErrNotFound}
	agent := newTestAgent(t, engine, newMemoryStore(), nil)
	stream := newTestStream()
	valuesJSON := []byte(`{"message":"hello"}`)

	require.NoError(t, agent.handleCommand(t.Context(), stream, upgradeCommand("cmd-notfound", valuesJSON, sha256Hex(valuesJSON))))
	require.Len(t, stream.sent, 2)
	result := stream.sent[1].GetCommandResult()
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.GetStatus())
	assert.Equal(t, "release_not_found", result.GetError().GetCode())
	assert.False(t, result.GetError().GetRetryable())
	assert.Zero(t, engine.upgradeCalls, "Status returned release_not_found, must not call engine.Upgrade")
}

func TestAgent_UpgradeCachedResultRedelivery(t *testing.T) {
	store := newMemoryStore()
	agent := newTestAgent(t, new(recordingEngine), store, nil)
	stream := newTestStream()
	valuesJSON := []byte(`{"message":"hello"}`)
	command := upgradeCommand("cmd-cached-upgrade", valuesJSON, sha256Hex(valuesJSON))
	payload, err := protojson.Marshal(command)
	require.NoError(t, err)
	resultJSON := `{"operation_id":"operation-upgrade","command_id":"cmd-cached-upgrade","definition_id":"definition-1","status":"succeeded","upgrade":{"from":{"helm_revision":1,"bundle_digest":"sha256:bundle","chart_digest":"sha256:chart","effective_values_digest":"","manifest_digest":"","provenance":2},"attempted":{"helm_revision":2,"bundle_digest":"sha256:bundle","chart_digest":"sha256:chart","effective_values_digest":"","manifest_digest":"","provenance":1},"active":{"helm_revision":2,"bundle_digest":"sha256:bundle","chart_digest":"sha256:chart","effective_values_digest":"","manifest_digest":"","provenance":1}},"inventory_sync_hint":true,"resource_summary":{"manifest_digest":"sha256:manifest"}}`
	require.NoError(t, store.Save(t.Context(), &localstore.CommandEntry{
		CommandID: command.GetCommandId(), OutboxID: command.GetOutboxId(), OperationID: command.GetOperationId(),
		OperationType: command.GetOperationType(), Sequence: command.GetSequence(), Payload: payload,
		Status: localstore.StatusSucceeded, ResultJSON: resultJSON,
	}))

	require.NoError(t, agent.handleCommand(t.Context(), stream, command))
	require.Len(t, stream.sent, 1)
	cmdResult := stream.sent[0].GetCommandResult()
	require.NotNil(t, cmdResult)
	assert.Equal(t, "succeeded", cmdResult.GetStatus())
	assert.Equal(t, "cmd-cached-upgrade", cmdResult.GetCommandId())
	assert.Equal(t, uint64(2), cmdResult.GetUpgrade().GetActive().GetHelmRevision())
}

// TASK-084 AC-084-01: both Helm SDK calls of one UPGRADE execute entry use the
// typed identity, even when the decoded wire Command carries empty top-level
// namespace/release_name (the D-109 repro: the second engine.Status used the
// empty top-level fields and produced an invalid Helm release).
func TestExecuteUpgradeUsesTypedIdentity(t *testing.T) {
	valuesJSON := []byte(`{"message":"hello"}`)
	engine := &recordingEngine{
		status: &helmengine.Release{
			Name: "example", Namespace: "apps", Revision: 1, Status: "deployed", Provenance: "legacy",
		},
		release: &helmengine.Release{
			Name: "example", Namespace: "apps", Revision: 2, Status: "deployed",
			ManifestDigest: "manifest-v2", Provenance: "managed",
		},
	}
	agent := newTestAgent(t, engine, newMemoryStore(), nil)
	command := upgradeCommand("cmd-typed-identity", valuesJSON, sha256Hex(valuesJSON))
	// The wire Command intentionally carries no top-level identity: the typed
	// payload is the only authority (DecodeCommandPayload mirrors the
	// orchestrator envelope, which the pre-TASK-084 BuildUpgradePayload left
	// empty).
	require.Empty(t, command.GetNamespace())
	require.Empty(t, command.GetReleaseName())

	require.NoError(t, agent.handleCommand(t.Context(), newTestStream(), command))
	require.Len(t, engine.statusOptions, 2, "resolveUpgradeInputs + executeUpgrade each perform one Status")
	for _, opts := range engine.statusOptions {
		assert.Equal(t, "apps", opts.Namespace)
		assert.Equal(t, "example", opts.ReleaseName)
	}
	assert.Equal(t, "apps", engine.lastUpgrade.Namespace)
	assert.Equal(t, "example", engine.lastUpgrade.ReleaseName)
}

// TASK-084 AC-084-01 negative: a typed UpgradeCommand without a valid
// namespace/release_name fails closed with invalid_command before any Helm SDK
// call (ADR-008: missing identity is rejected, never executed).
func TestResolveUpgradeInputsRejectsEmptyIdentity(t *testing.T) {
	tests := []struct {
		name        string
		namespace   string
		releaseName string
	}{
		{name: "empty namespace", releaseName: "example"},
		{name: "empty release name", namespace: "apps"},
		{name: "both empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine := new(recordingEngine)
			agent := newTestAgent(t, engine, newMemoryStore(), nil)
			stream := newTestStream()
			valuesJSON := []byte(`{"message":"hello"}`)
			command := upgradeCommand("cmd-empty-identity", valuesJSON, sha256Hex(valuesJSON))
			command.GetUpgrade().Namespace = test.namespace
			command.GetUpgrade().ReleaseName = test.releaseName

			require.NoError(t, agent.handleCommand(t.Context(), stream, command))
			require.Len(t, stream.sent, 2)
			result := stream.sent[1].GetCommandResult()
			require.NotNil(t, result)
			assert.Equal(t, "failed", result.GetStatus())
			assert.Equal(t, "invalid_command", result.GetError().GetCode())
			require.Zero(t, engine.statusCalls, "no Helm SDK call may observe an invalid identity")
			require.Zero(t, engine.upgradeCalls)
		})
	}
}

// TASK-084 AC-084-02: every UPGRADE failure path carries the typed
// CommandResult.upgrade payload so the gateway never rejects the result with
// "upgrade result is required" (D-109 repro: the second-Status failure and
// resolveUpgradeInputs failures were sent without typed Upgrade and the
// operation stayed RUNNING forever).
func TestExecuteUpgradeFailurePathsCarryTypedResult(t *testing.T) {
	valuesJSON := []byte(`{"message":"hello"}`)
	rev1 := &helmengine.Release{Name: "example", Namespace: "apps", Revision: 1, Status: "deployed", Provenance: "legacy"}
	rev2 := &helmengine.Release{Name: "example", Namespace: "apps", Revision: 2, Status: "deployed", Provenance: "managed"}

	tests := []struct {
		name        string
		command     func() *operatorv1.Command
		setup       func(command *operatorv1.Command)
		engine      *recordingEngine
		wantCode    string
		wantFromRev uint64
	}{
		{
			name: "payload version rejected",
			command: func() *operatorv1.Command {
				command := upgradeCommand("cmd-fail-version", valuesJSON, sha256Hex(valuesJSON))
				command.PayloadVersion = 1
				return command
			},
			engine:   new(recordingEngine),
			wantCode: "unsupported_command_version",
		},
		{
			name: "digest mismatch",
			command: func() *operatorv1.Command {
				return upgradeCommand("cmd-fail-digest", valuesJSON, "wrong")
			},
			engine:   new(recordingEngine),
			wantCode: "digest_mismatch",
		},
		{
			name: "invalid values JSON",
			command: func() *operatorv1.Command {
				return upgradeCommand("cmd-fail-json", []byte(`{not-json`), sha256Hex([]byte(`{not-json`)))
			},
			engine:   new(recordingEngine),
			wantCode: "invalid_command",
		},
		{
			name: "first status fails",
			command: func() *operatorv1.Command {
				return upgradeCommand("cmd-fail-status", valuesJSON, sha256Hex(valuesJSON))
			},
			engine:   &recordingEngine{statusErr: helmengine.ErrNotFound},
			wantCode: "release_not_found",
		},
		{
			name: "revision conflict",
			command: func() *operatorv1.Command {
				return upgradeCommand("cmd-fail-revision", valuesJSON, sha256Hex(valuesJSON))
			},
			engine:   &recordingEngine{status: rev2},
			wantCode: "revision_conflict",
		},
		{
			name: "secret ref changed",
			command: func() *operatorv1.Command {
				command := upgradeCommand("cmd-fail-secret", valuesJSON, sha256Hex(valuesJSON))
				command.GetUpgrade().SecretRefs = []*operatorv1.SecretRef{{Path: "db.password", Name: "db", Key: "password"}}
				return command
			},
			engine:   &recordingEngine{status: rev1},
			wantCode: "secret_ref_changed",
		},
		{
			name: "second status fails",
			command: func() *operatorv1.Command {
				return upgradeCommand("cmd-fail-status2", valuesJSON, sha256Hex(valuesJSON))
			},
			engine:   &recordingEngine{status: rev1, statusFailOnCall: 2, statusErr: helmengine.ErrReleaseNotDeployed},
			wantCode: "release_not_deployed", wantFromRev: 1,
		},
		{
			name:    "inventory stale",
			command: func() *operatorv1.Command { return upgradeCommand("cmd-fail-stale", valuesJSON, sha256Hex(valuesJSON)) },
			setup: func(command *operatorv1.Command) {
				command.GetUpgrade().ExpectedRevision = 2
				command.ExpectedCurrentRevision = 1
			},
			engine:   &recordingEngine{status: rev2},
			wantCode: "inventory_stale", wantFromRev: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := newTestAgent(t, test.engine, newMemoryStore(), nil)
			stream := newTestStream()
			command := test.command()
			if test.setup != nil {
				test.setup(command)
			}
			require.NoError(t, agent.handleCommand(t.Context(), stream, command))
			require.Len(t, stream.sent, 2)
			result := stream.sent[1].GetCommandResult()
			require.NotNil(t, result)
			assert.Equal(t, "failed", result.GetStatus())
			assert.Equal(t, test.wantCode, result.GetError().GetCode())
			require.NotNil(t, result.GetUpgrade(), "failed UPGRADE result must carry the typed payload")
			if test.wantFromRev > 0 {
				require.NotNil(t, result.GetUpgrade().GetFrom(), "from snapshot must be preserved when resolved")
				assert.Equal(t, test.wantFromRev, result.GetUpgrade().GetFrom().GetHelmRevision())
			}
			assert.False(t, result.GetUpgrade().GetRollbackSucceeded())
		})
	}
}

// TASK-084 AC-084-02 wire-edge assertion: commandResultRequest must never emit
// a UPGRADE command result without the typed payload — even for the invalid
// local payload path (executeEntry → finishFailure).
func TestCommandResultRequestAlwaysCarriesTypedUpgrade(t *testing.T) {
	req := commandResultRequest(&operatorv1.Command{
		CommandId: "cmd-1", OperationId: "op-1", OperationType: "UPGRADE",
	}, Result{Status: "failed", Code: "invalid_command", Message: "invalid command payload"})
	require.NotNil(t, req.GetCommandResult().GetUpgrade(),
		"UPGRADE command result must never be sent without CommandResult.upgrade")

	withTyped := commandResultRequest(&operatorv1.Command{
		CommandId: "cmd-2", OperationId: "op-2", OperationType: "UPGRADE",
	}, Result{
		Status: "failed", Code: "helm_cancelled",
		Upgrade: &operatorv1.UpgradeResult{Active: &operatorv1.ReleaseSnapshot{HelmRevision: 3}},
	})
	assert.Equal(t, uint64(3), withTyped.GetCommandResult().GetUpgrade().GetActive().GetHelmRevision())
}

// TASK-084 AC-084-04: the engine's structured outcome is the only authority
// for rollback_succeeded — the legacy revision-equality heuristic is gone and
// an undecorated error fails closed (D-109 anti-fabrication guard).
func TestExecuteUpgradeRollbackOutcomeMapping(t *testing.T) {
	valuesJSON := []byte(`{"message":"hello"}`)
	rev1 := &helmengine.Release{Name: "example", Namespace: "apps", Revision: 1, Status: "deployed", Provenance: "legacy"}
	rev2 := &helmengine.Release{Name: "example", Namespace: "apps", Revision: 2, Status: "deployed", Provenance: "managed"}
	rev2Failed := &helmengine.Release{Name: "example", Namespace: "apps", Revision: 2, Status: "failed"}

	tests := []struct {
		name         string
		release      *helmengine.Release
		upgradeErr   error
		wantCode     string
		wantRollback bool
	}{
		{
			name:    "atomic rollback restored",
			release: rev1,
			upgradeErr: helmengine.NewOutcomeError(
				helmengine.ErrActionFailed,
				helmengine.UpgradeOutcome{Attempted: rev2, Active: rev1, Restored: true},
			),
			wantCode: "helm_upgrade_failed", wantRollback: true,
		},
		{
			name:    "rollback cascade failed",
			release: rev2Failed,
			upgradeErr: helmengine.NewOutcomeError(
				helmengine.ErrAtomicRollbackFailed,
				helmengine.UpgradeOutcome{Attempted: rev2, Active: rev2Failed, Restored: false},
			),
			wantCode: "atomic_rollback_failed", wantRollback: false,
		},
		{
			name:       "undecorated error with coincidental revisions fails closed",
			release:    rev1,
			upgradeErr: helmengine.ErrActionFailed,
			// rev1.Revision == fromRelease.Revision would have faked success
			// under the legacy heuristic; without an outcome the mapping must
			// never report rollback_succeeded.
			wantCode: "helm_upgrade_failed", wantRollback: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine := &recordingEngine{status: rev1, release: test.release, upgradeErr: test.upgradeErr}
			agent := newTestAgent(t, engine, newMemoryStore(), nil)
			stream := newTestStream()

			require.NoError(t, agent.handleCommand(t.Context(), stream, upgradeCommand("cmd-outcome", valuesJSON, sha256Hex(valuesJSON))))
			require.Len(t, stream.sent, 2)
			result := stream.sent[1].GetCommandResult()
			require.NotNil(t, result)
			assert.Equal(t, "failed", result.GetStatus())
			assert.Equal(t, test.wantCode, result.GetError().GetCode())
			require.NotNil(t, result.GetUpgrade())
			assert.Equal(t, test.wantRollback, result.GetUpgrade().GetRollbackSucceeded())
		})
	}
}

// blockingEngine models an in-flight Helm Upgrade that only returns once the
// execution context is cancelled (TASK-084 Step 3 seam).
type blockingEngine struct {
	recordingEngine
	started chan struct{}
}

func newBlockingEngine() *blockingEngine {
	return &blockingEngine{
		recordingEngine: recordingEngine{statusRelease: &helmengine.Release{
			Name: "example", Namespace: "apps", Revision: 1, Status: "deployed", Provenance: "legacy",
		}},
		started: make(chan struct{}),
	}
}

func (e *blockingEngine) Upgrade(ctx context.Context, opts helmengine.UpgradeOptions) (*helmengine.Release, error) {
	e.upgradeCalls++
	e.lastUpgrade = opts
	close(e.started)
	<-ctx.Done()
	return nil, helmengine.ErrCancelled
}

// scriptedStream delivers one command and then blocks until done is closed
// (simulating a stream that dies when the server revokes the connection).
type scriptedStream struct {
	mu        sync.Mutex
	sent      []*operatorv1.CommandStreamRequest
	response  *operatorv1.CommandStreamResponse
	delivered bool
	done      chan struct{}
}

func newScriptedStream(response *operatorv1.CommandStreamResponse) *scriptedStream {
	return &scriptedStream{response: response, done: make(chan struct{})}
}

func (s *scriptedStream) Send(request *operatorv1.CommandStreamRequest) error {
	s.mu.Lock()
	s.sent = append(s.sent, request)
	s.mu.Unlock()
	return nil
}

// sentCount is a race-free observation point for polling tests (-race safe).
func (s *scriptedStream) sentCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *scriptedStream) Receive() (*operatorv1.CommandStreamResponse, error) {
	if !s.delivered {
		s.delivered = true
		return s.response, nil
	}
	<-s.done
	return nil, context.Canceled
}
func (*scriptedStream) CloseRequest() error  { return nil }
func (*scriptedStream) CloseResponse() error { return nil }

type scriptedClient struct{ stream Stream }

func (c scriptedClient) CommandStream(context.Context) Stream { return c.stream }

// TASK-084 AC-084-03: revoking the connection cancels the in-flight Helm call
// through the connection-scoped context; the helm_cancelled typed result is
// persisted locally first (ADR-005 persist-before-send) and replayed as a
// typed CommandResult on the next delivery.
func TestStreamDisconnectCancelsInFlightUpgrade(t *testing.T) {
	valuesJSON := []byte(`{"message":"hello"}`)
	command := upgradeCommand("cmd-cancel-flight", valuesJSON, sha256Hex(valuesJSON))
	store := newMemoryStore()
	engine := newBlockingEngine()
	stream := newScriptedStream(&operatorv1.CommandStreamResponse{
		Payload: &operatorv1.CommandStreamResponse_Command{Command: command},
	})
	agent, err := New(Config{
		Client: scriptedClient{stream: stream}, Engine: engine, Store: store,
		SessionID: "session-1", OperatorID: "operator-1",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)

	parentCtx, cancel := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	go func() { runDone <- agent.Run(parentCtx) }()
	select {
	case <-engine.started:
	case <-time.After(5 * time.Second):
		t.Fatal("engine.Upgrade never started")
	}

	// Server-side revocation: the parent connection dies.
	cancel()
	close(stream.done)
	require.NoError(t, <-runDone)

	entry, err := store.Get(context.Background(), command.GetCommandId())
	require.NoError(t, err)
	require.Equal(t, localstore.StatusFailed, entry.Status, "cancelled result must persist before the stream dies")
	var cached Result
	require.NoError(t, json.Unmarshal([]byte(entry.ResultJSON), &cached))
	assert.Equal(t, "helm_cancelled", cached.Code)
	require.NotNil(t, cached.Upgrade, "helm_cancelled result must carry the typed payload")

	// Reconnect replay: the redelivered command returns the cached typed
	// result — the agent acknowledgement the gateway consumes.
	replayStream := newTestStream()
	require.NoError(t, agent.handleCommand(context.Background(), replayStream, command))
	require.Len(t, replayStream.sent, 1)
	replayed := replayStream.sent[0].GetCommandResult()
	require.NotNil(t, replayed)
	assert.Equal(t, "helm_cancelled", replayed.GetError().GetCode())
	require.NotNil(t, replayed.GetUpgrade(), "replayed cancel result must carry CommandResult.upgrade")
}

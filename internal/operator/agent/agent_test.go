//nolint:revive // test doubles intentionally implement the broad Engine contract
package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/ndzuki/release-manager/internal/operator/localstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
)

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
	assert.True(t, result.GetUpgrade().GetRollbackSucceeded())
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

func TestAgent_InventorySyncCommandExecutesFullSync(t *testing.T) {
	executor := new(recordingSyncExecutor)
	agent, err := New(Config{
		Client: noopClient{}, Engine: new(recordingEngine), Store: newMemoryStore(),
		SyncExecutor: executor, SessionID: "session-1", OperatorID: "operator-1",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	stream := newTestStream()
	command := &operatorv1.Command{
		OutboxId: "outbox-sync", CommandId: "command-sync", OperationId: "request-sync",
		OperationType: "INVENTORY_SYNC", Sequence: 9,
	}

	require.NoError(t, agent.handleCommand(t.Context(), stream, command))
	require.Len(t, stream.sent, 2)
	assert.Equal(t, 1, executor.calls)
	assert.Equal(t, "succeeded", stream.sent[1].GetResult().GetStatus())
	assert.Contains(t, stream.sent[1].GetResult().GetResultJson(), `"inventory_sync_hint":true`)
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
		OutboxId:       "outbox-rollback",
		CommandId:      commandID,
		OperationId:    "op-rollback",
		OperationType:  "ROLLBACK",
		TargetRevision: 1,
		DefinitionId:   "definition-rollback",
		Namespace:      "apps",
		ReleaseName:    "example",
		TimeoutSeconds: 45,
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
	installCalls int
	lastInstall  helmengine.InstallOptions
	release      *helmengine.Release
	err          error
	upgradeCalls int
	lastUpgrade  helmengine.UpgradeOptions
	status       *helmengine.Release
	statusErr    error
	upgradeErr   error
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
func (e *recordingEngine) Rollback(context.Context, helmengine.RollbackOptions) (*helmengine.Release, error) {
	return nil, errors.New("not implemented")
}
func (e *recordingEngine) Status(context.Context, helmengine.StatusOptions) (*helmengine.Release, error) {
	if e.statusErr != nil {
		return nil, e.statusErr
	}
	if e.status == nil {
		return nil, helmengine.ErrNotFound
	}
	return e.status, nil
}
func (*recordingEngine) History(context.Context, helmengine.HistoryOptions) ([]helmengine.ReleaseHistoryEntry, error) {
	return nil, errors.New("not implemented")
}
func (*recordingEngine) GetValues(context.Context, helmengine.GetValuesOptions) (map[string]interface{}, error) {
	return nil, errors.New("not implemented")
}
func (*recordingEngine) List(context.Context, string) ([]*helmengine.ReleaseListItem, error) {
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

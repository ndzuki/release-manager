//nolint:revive // test doubles intentionally implement the broad Engine contract
package agent

import (
	"context"
	"errors"
	"fmt"
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

func TestAgent_UpgradeCommandMergesValuesPatchAndChecksRevision(t *testing.T) {
	engine := &recordingEngine{
		release: &helmengine.Release{
			Name:      "example",
			Namespace: "apps",
			Revision:  2,
			Status:    "deployed",
		},
	}
	agent := newTestAgent(t, engine, newMemoryStore(), nil)
	stream := newTestStream()
	command := upgradeCommand("cmd-upgrade")

	require.NoError(t, agent.handleCommand(t.Context(), stream, command))
	require.Len(t, stream.sent, 2)
	assert.Equal(t, "succeeded", stream.sent[1].GetResult().GetStatus())
	assert.Equal(t, 1, engine.upgradeCalls)
	assert.Equal(t, 1, engine.lastUpgrade.ExpectedRevision)
	assert.True(t, engine.lastUpgrade.Atomic)
	assert.Equal(t, map[string]interface{}{"replicas": float64(3), "image": map[string]interface{}{"tag": "v2"}}, engine.lastUpgrade.Values)
}

func TestAgent_UpgradeConflictErrorMapping(t *testing.T) {
	engine := &recordingEngine{err: helmengine.ErrConflict}
	agent := newTestAgent(t, engine, newMemoryStore(), nil)
	stream := newTestStream()

	require.NoError(t, agent.handleCommand(t.Context(), stream, upgradeCommand("cmd-conflict")))
	require.Len(t, stream.sent, 2)
	assert.Contains(t, stream.sent[1].GetResult().GetResultJson(), `"code":"revision_conflict"`)
}

func TestAgent_RollbackCommand(t *testing.T) {
	engine := &recordingEngine{
		release: &helmengine.Release{
			Name:      "example",
			Namespace: "apps",
			Revision:  3,
			Status:    "deployed",
		},
	}
	agent := newTestAgent(t, engine, newMemoryStore(), nil)
	stream := newTestStream()
	command := rollbackCommand("cmd-rollback")

	require.NoError(t, agent.handleCommand(t.Context(), stream, command))
	require.Len(t, stream.sent, 2)
	assert.Equal(t, "succeeded", stream.sent[1].GetResult().GetStatus())
	assert.Equal(t, 1, engine.rollbackCalls)
	assert.Equal(t, 1, engine.lastRollback.TargetRevision)
	assert.Equal(t, "apps", engine.lastRollback.Namespace)
	assert.Equal(t, "example", engine.lastRollback.ReleaseName)
}

func TestAgent_RollbackMissingTargetRevision(t *testing.T) {
	engine := &recordingEngine{}
	agent := newTestAgent(t, engine, newMemoryStore(), nil)
	stream := newTestStream()
	command := rollbackCommand("cmd-no-target")
	command.TargetRevision = 0

	require.NoError(t, agent.handleCommand(t.Context(), stream, command))
	require.Len(t, stream.sent, 2)
	assert.Contains(t, stream.sent[1].GetResult().GetResultJson(), `"code":"invalid_command"`)
	assert.Equal(t, 0, engine.rollbackCalls)
}

func TestAgent_RollbackErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{"release not found", helmengine.ErrNotFound, "release_not_found"},
		{"target revision not found", helmengine.ErrRevisionNotFound, "target_revision_not_found"},
		{"artifact unavailable", helmengine.ErrArtifactUnavailable, "historical_artifact_unavailable"},
		{"timeout", helmengine.ErrTimeout, "timeout"},
		{"cancelled", helmengine.ErrCancelled, "cancelled"},
		{"generic failure", errors.New("something went wrong"), "helm_rollback_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := &recordingEngine{err: tt.err}
			agent := newTestAgent(t, engine, newMemoryStore(), nil)
			stream := newTestStream()

			require.NoError(t, agent.handleCommand(t.Context(), stream, rollbackCommand("cmd-err")))
			require.Len(t, stream.sent, 2)
			assert.Contains(t, stream.sent[1].GetResult().GetResultJson(), fmt.Sprintf(`"code":"%s"`, tt.wantCode))
		})
	}
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

func upgradeCommand(commandID string) *operatorv1.Command {
	return &operatorv1.Command{
		OutboxId:               "outbox-upgrade",
		CommandId:              commandID,
		OperationId:            "op-upgrade",
		OperationType:          "UPGRADE",
		Bundle:                 &commonv1.ReleaseBundle{ChartRef: "chart", ChartVersion: "1.0.0"},
		Values:                 []byte(`{"replicas":2,"image":{"tag":"v1"}}`),
		ValuesPatch:            []byte(`{"replicas":3,"image":{"tag":"v2"}}`),
		ExpectedCurrentRevision: 1,
		Atomic:                 true,
		DefinitionId:           "definition-upgrade",
		Namespace:              "apps",
		ReleaseName:            "example",
		TimeoutSeconds:         45,
	}
}

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
	installCalls  int
	upgradeCalls  int
	rollbackCalls int
	lastInstall   helmengine.InstallOptions
	lastUpgrade   helmengine.UpgradeOptions
	lastRollback  helmengine.RollbackOptions
	release       *helmengine.Release
	err           error
}

func (e *recordingEngine) Install(_ context.Context, opts helmengine.InstallOptions) (*helmengine.Release, error) {
	e.installCalls++
	e.lastInstall = opts
	return e.release, e.err
}

func (e *recordingEngine) Upgrade(_ context.Context, opts helmengine.UpgradeOptions) (*helmengine.Release, error) {
	e.upgradeCalls++
	e.lastUpgrade = opts
	return e.release, e.err
}
func (e *recordingEngine) Rollback(_ context.Context, opts helmengine.RollbackOptions) (*helmengine.Release, error) {
	e.rollbackCalls++
	e.lastRollback = opts
	return e.release, e.err
}
func (*recordingEngine) Status(context.Context, helmengine.StatusOptions) (*helmengine.Release, error) {
	return nil, errors.New("not implemented")
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

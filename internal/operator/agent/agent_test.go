package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/ndzuki/release-manager/internal/operator/localstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testStream struct {
	responses []*operatorv1.CommandStreamResponse
	sent      []*operatorv1.CommandStreamRequest
}

func (s *testStream) Send(req *operatorv1.CommandStreamRequest) error {
	s.sent = append(s.sent, req)
	return nil
}

func (s *testStream) Receive() (*operatorv1.CommandStreamResponse, error) {
	if len(s.responses) == 0 {
		return nil, io.EOF
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

func (s *testStream) CloseRequest() error  { return nil }
func (s *testStream) CloseResponse() error { return nil }

type memoryStore struct {
	entries map[string]*localstore.CommandEntry
}

func newMemoryStore() *memoryStore {
	return &memoryStore{entries: map[string]*localstore.CommandEntry{}}
}

func (s *memoryStore) Save(_ context.Context, entry *localstore.CommandEntry) error {
	copyEntry := *entry
	s.entries[entry.CommandID] = &copyEntry
	return nil
}

func (s *memoryStore) Get(_ context.Context, commandID string) (*localstore.CommandEntry, error) {
	entry, ok := s.entries[commandID]
	if !ok {
		return nil, localstore.ErrNotFound
	}
	copyEntry := *entry
	return &copyEntry, nil
}

func (s *memoryStore) GetByOutboxID(_ context.Context, outboxID string) (*localstore.CommandEntry, error) {
	for _, entry := range s.entries {
		if entry.OutboxID == outboxID {
			copyEntry := *entry
			return &copyEntry, nil
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
	entry.ResultJSON = resultJSON
	return nil
}

func (s *memoryStore) ListActive(_ context.Context) ([]*localstore.CommandEntry, error) {
	var active []*localstore.CommandEntry
	for _, entry := range s.entries {
		if !localstore.IsTerminal(entry.Status) {
			active = append(active, entry)
		}
	}
	return active, nil
}

func (s *memoryStore) LastSequence(_ context.Context) (int64, error) {
	var sequence int64
	for _, entry := range s.entries {
		if entry.Sequence > sequence {
			sequence = entry.Sequence
		}
	}
	return sequence, nil
}

func (s *memoryStore) Close() error { return nil }

func TestAgentHandleCommand_Upgrade(t *testing.T) {
	engine := helmengine.NewFake()
	_, err := engine.Install(context.Background(), helmengine.InstallOptions{
		Namespace:   "default",
		ReleaseName: "my-release",
		ChartPath:   "nginx",
	})
	require.NoError(t, err)

	store := newMemoryStore()
	stream := &testStream{}
	agent, err := New(Options{
		Engine:        engine,
		Store:         store,
		SessionID:     "session-1",
		OperatorID:    "operator-1",
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		StreamFactory: func(context.Context) CommandStream { return stream },
	})
	require.NoError(t, err)

	values, err := json.Marshal(commandPayload{
		Namespace:        "default",
		ReleaseName:      "my-release",
		ChartPath:        "nginx",
		Values:           map[string]interface{}{"replicas": 2},
		ExpectedRevision: 1,
		Atomic:           true,
	})
	require.NoError(t, err)
	command := &operatorv1.Command{
		OutboxId:      "outbox-1",
		CommandId:     "command-1",
		OperationId:   "operation-1",
		OperationType: "UPGRADE",
		Values:        values,
		Sequence:      1,
	}

	require.NoError(t, agent.handleCommand(context.Background(), stream, command))
	stored, err := store.Get(context.Background(), command.CommandId)
	require.NoError(t, err)
	assert.Equal(t, localstore.StatusSucceeded, stored.Status)
	assert.Equal(t, 3, len(stream.sent))
	result := stream.sent[2].GetResult()
	require.NotNil(t, result)
	assert.Equal(t, "succeeded", result.Status)
	assert.Equal(t, "command-1", result.CommandId)

	release, err := engine.Status(context.Background(), helmengine.StatusOptions{
		Namespace:   "default",
		ReleaseName: "my-release",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, release.Revision)
}

func TestAgentHandleCommand_DuplicateReturnsCachedResult(t *testing.T) {
	store := newMemoryStore()
	resultJSON := `{"status":"succeeded","operation_id":"operation-1","namespace":"default","release_name":"my-release"}`
	require.NoError(t, store.Save(context.Background(), &localstore.CommandEntry{
		CommandID:   "command-1",
		OutboxID:    "outbox-1",
		OperationID: "operation-1",
		Sequence:    1,
		Status:      localstore.StatusSucceeded,
		ResultJSON:  resultJSON,
	}))

	agent, err := New(Options{
		Engine:        helmengine.NewFake(),
		Store:         store,
		SessionID:     "session-1",
		OperatorID:    "operator-1",
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		StreamFactory: func(context.Context) CommandStream { return &testStream{} },
	})
	require.NoError(t, err)
	stream := &testStream{}
	require.NoError(t, agent.handleCommand(context.Background(), stream, &operatorv1.Command{
		OutboxId:  "outbox-1",
		CommandId: "command-1",
		Sequence:  1,
	}))

	require.Len(t, stream.sent, 1)
	assert.Equal(t, resultJSON, stream.sent[0].GetResult().ResultJson)
}

func TestAgentExecute_MapsUpgradeErrors(t *testing.T) {
	agent, err := New(Options{
		Engine:        helmengine.NewFake(),
		Store:         newMemoryStore(),
		SessionID:     "session-1",
		OperatorID:    "operator-1",
		StreamFactory: func(context.Context) CommandStream { return &testStream{} },
	})
	require.NoError(t, err)

	result := agent.execute(context.Background(), &operatorv1.Command{
		OperationType: "UPGRADE",
		Values:        []byte(`{"namespace":"default","release_name":"missing","chart_path":"nginx","expected_revision":1}`),
	})
	assert.Equal(t, "failed", result.Status)
	assert.Equal(t, "release_not_found", result.ErrorCode)
}

func TestErrorCode_Default(t *testing.T) {
	assert.Equal(t, "fallback", errorCode(errors.New("failure"), "fallback"))
}

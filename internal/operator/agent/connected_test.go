package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gateStream delivers a live session: Send records, Receive blocks until the
// test closes release, then errors out like a dropped connection.
type gateStream struct {
	mu      sync.Mutex
	sent    []*operatorv1.CommandStreamRequest
	release chan struct{}
}

func (s *gateStream) Send(request *operatorv1.CommandStreamRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, request)
	return nil
}

func (s *gateStream) Receive() (*operatorv1.CommandStreamResponse, error) {
	<-s.release
	return nil, errors.New("stream dropped")
}

func (s *gateStream) CloseRequest() error  { return nil }
func (s *gateStream) CloseResponse() error { return nil }

type gateClient struct{ stream Stream }

func (c gateClient) CommandStream(context.Context) Stream { return c.stream }

// TestRunTracksConnectedWhileSessionLive is the TASK-099 AC3 evidence for the
// operator-agent: Connected() flips to true only once the gateway accepted
// the Hello and replay completed, and flips back when the stream dies, so
// /readyz reports NotReady while the reconnect loop is between sessions.
func TestRunTracksConnectedWhileSessionLive(t *testing.T) {
	store := newMemoryStore()
	stream := &gateStream{release: make(chan struct{})}
	agent, err := New(Config{
		Client:       gateClient{stream: stream},
		Engine:       &recordingEngine{},
		Store:        store,
		SessionID:    "session-connected",
		OperatorID:   "operator-connected",
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		InstallFlags: InstallFlags{Timeout: time.Minute},
	})
	require.NoError(t, err)
	assert.False(t, agent.Connected(), "a fresh Agent must not claim a session")

	runErr := make(chan error, 1)
	go func() { runErr <- agent.Run(context.Background()) }()

	deadline := time.After(2 * time.Second)
	for !agent.Connected() {
		select {
		case <-deadline:
			t.Fatal("agent.Run did not mark the session connected")
		case err := <-runErr:
			t.Fatalf("agent.Run exited early: %v", err)
		case <-time.After(time.Millisecond):
		}
	}

	close(stream.release)
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
		t.Fatal("agent.Run did not return after the stream dropped")
	}
	assert.False(t, agent.Connected(), "a dead stream must clear the connected flag")
}

// failSendStream fails at the first Send, i.e. the Hello never lands.
type failSendStream struct{ gateStream }

func (s *failSendStream) Send(*operatorv1.CommandStreamRequest) error {
	return errors.New("dial refused")
}

// TestRunNotConnectedWhenHelloFails: a rejected Hello must never credit the
// readiness check, not even transiently.
func TestRunNotConnectedWhenHelloFails(t *testing.T) {
	store := newMemoryStore()
	stream := &failSendStream{gateStream: gateStream{release: make(chan struct{})}}
	agent, err := New(Config{
		Client:     gateClient{stream: stream},
		Engine:     &recordingEngine{},
		Store:      store,
		SessionID:  "session-x",
		OperatorID: "operator-x",
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	require.Error(t, agent.Run(context.Background()))
	assert.False(t, agent.Connected())
}

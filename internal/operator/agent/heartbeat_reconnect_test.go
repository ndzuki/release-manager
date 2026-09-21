package agent

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
)

// reconnectingClient hands out a fresh stream per connection, the way
// cmd/operator's runAgentLoop drives one long-lived Agent through an
// orchestrator restart (TASK-155).
type reconnectingClient struct {
	created chan *scriptedStream
}

func newReconnectingClient() *reconnectingClient {
	return &reconnectingClient{created: make(chan *scriptedStream, 4)}
}

func (c *reconnectingClient) CommandStream(context.Context) Stream {
	stream := newScriptedStream(&operatorv1.CommandStreamResponse{
		Payload: &operatorv1.CommandStreamResponse_SessionEstablished{
			SessionEstablished: &operatorv1.SessionEstablished{
				SessionId:                "session-1",
				HeartbeatIntervalSeconds: 1,
			},
		},
	})
	c.created <- stream
	return stream
}

func heartbeatCount(stream *scriptedStream) int {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	count := 0
	for _, request := range stream.sent {
		if request.GetHeartbeat() != nil {
			count++
		}
	}
	return count
}

func waitForHeartbeats(t *testing.T, stream *scriptedStream, want int, runDone <-chan error) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		if heartbeatCount(stream) >= want {
			return
		}
		select {
		case err := <-runDone:
			t.Fatalf("agent Run exited before %d heartbeats: %v", want, err)
		case <-deadline:
			t.Fatalf("timed out waiting for %d heartbeats (got %d)", want, heartbeatCount(stream))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TASK-155: the heartbeat guard is per connection, not per Agent.
//
// The operator reconnects to a restarted orchestrator with the SAME Agent
// (cmd/operator's runAgentLoop reuses it), so the second connection must start
// its own heartbeat. Before the fix a sync.Once held on the Agent was consumed
// by the first connection: the reconnected session never reported liveness
// again and was judged offline while its command stream was still alive (real
// smoke 2026-09-21: all four operator sessions 0 ONLINE, ExecuteEmergencyChange
// refused with "operator is offline").
func TestAgent_HeartbeatRestartsOnEveryConnection(t *testing.T) {
	client := newReconnectingClient()
	agent, err := New(Config{
		Client: client, Engine: &recordingEngine{}, Store: newMemoryStore(),
		SessionID: "session-1", OperatorID: "operator-1",
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		InstallFlags: InstallFlags{Atomic: true, Timeout: time.Minute},
	})
	require.NoError(t, err)

	streams := make([]*scriptedStream, 0, 2)
	for connection := 1; connection <= 2; connection++ {
		ctx, cancel := context.WithCancel(context.Background())
		runDone := make(chan error, 1)
		go func() { runDone <- agent.Run(ctx) }()

		stream := <-client.created
		waitForHeartbeats(t, stream, 2, runDone)

		cancel()
		close(stream.done)
		require.NoError(t, <-runDone, "connection %d must end cleanly", connection)
		streams = append(streams, stream)
	}

	// TASK-155 AC 3: each connection's heartbeat goroutine stops with its own
	// connection — no frames arrive after both Runs returned. The settle window
	// covers the one tick a goroutine may still race in after ctx is cancelled
	// (select picks randomly among ready cases); the observation window then has
	// to be longer than the 1s interval for a live goroutine to be caught.
	time.Sleep(1500 * time.Millisecond)
	settled := []int{heartbeatCount(streams[0]), heartbeatCount(streams[1])}
	time.Sleep(1500 * time.Millisecond)

	assert.Equal(t, settled[0], heartbeatCount(streams[0]),
		"connection 1's heartbeat goroutine must stop with its connection")
	assert.Equal(t, settled[1], heartbeatCount(streams[1]),
		"connection 2's heartbeat goroutine must stop with its connection")
}

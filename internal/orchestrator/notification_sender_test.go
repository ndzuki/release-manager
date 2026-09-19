package orchestrator

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	notifierv1 "github.com/ndzuki/release-manager/api/gen/notifier/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// stubNotifierClient records the Send requests it receives.
type stubNotifierClient struct {
	requests []*notifierv1.SendNotificationRequest
	err      error
}

func (c *stubNotifierClient) Send(_ context.Context, req *connect.Request[notifierv1.SendNotificationRequest]) (*connect.Response[notifierv1.SendNotificationResponse], error) {
	if c.err != nil {
		return nil, c.err
	}
	c.requests = append(c.requests, req.Msg)
	return connect.NewResponse(&notifierv1.SendNotificationResponse{}), nil
}

func (c *stubNotifierClient) GetStatus(context.Context, *connect.Request[notifierv1.GetNotificationStatusRequest]) (*connect.Response[notifierv1.GetNotificationStatusResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

// terminalOutboxEntry builds the entry the terminal transition writes.
func terminalOutboxEntry(t *testing.T) *store.ApprovalOutboxEntry {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"event_type":            "OperationTerminal",
		"operation_id":          "op-123",
		"operation_type":        "INSTALL",
		"terminal_status":       "succeeded",
		"release_definition_id": "def-1",
	})
	require.NoError(t, err)
	return &store.ApprovalOutboxEntry{ID: "outbox-1", EventType: "OperationTerminal", PayloadJSON: payload}
}

// AC-031-12: the worker's sender turns a queued entry into a Send call carrying
// the operation, the configured recipient and the terminal status.
func TestNotifierClientSender_SendsTheQueuedNotification(t *testing.T) {
	client := &stubNotifierClient{}
	sender, err := NewNotifierClientSender(client, "https://hooks.example/ops", "webhook")
	require.NoError(t, err)

	require.NoError(t, sender.Send(context.Background(), terminalOutboxEntry(t)))

	require.Len(t, client.requests, 1)
	sent := client.requests[0]
	assert.Equal(t, "op-123", sent.GetOperationId())
	assert.Equal(t, "https://hooks.example/ops", sent.GetRecipient())
	assert.Equal(t, notifierv1.NotificationChannel_NOTIFICATION_CHANNEL_WEBHOOK, sent.GetChannel())
	assert.Equal(t, "succeeded", sent.GetMetadata()["terminal_status"])
	assert.Equal(t, "INSTALL", sent.GetMetadata()["operation_type"])
}

// A send failure must reach the worker so it leaves the entry queued.
func TestNotifierClientSender_PropagatesSendFailure(t *testing.T) {
	client := &stubNotifierClient{err: connect.NewError(connect.CodeUnavailable, nil)}
	sender, err := NewNotifierClientSender(client, "https://hooks.example/ops", "webhook")
	require.NoError(t, err)

	assert.Error(t, sender.Send(context.Background(), terminalOutboxEntry(t)))
}

// A payload the sender cannot read must not be acknowledged silently.
func TestNotifierClientSender_RejectsUndecodablePayload(t *testing.T) {
	client := &stubNotifierClient{}
	sender, err := NewNotifierClientSender(client, "https://hooks.example/ops", "webhook")
	require.NoError(t, err)

	err = sender.Send(context.Background(), &store.ApprovalOutboxEntry{ID: "x", EventType: "OperationTerminal", PayloadJSON: []byte("not json")})
	require.Error(t, err)
	assert.Empty(t, client.requests, "an undecodable entry must not be sent")
}

// An empty recipient would create a job that can never be delivered.
func TestNewNotifierClientSender_RequiresARecipient(t *testing.T) {
	_, err := NewNotifierClientSender(&stubNotifierClient{}, "  ", "webhook")
	assert.Error(t, err)
	_, err = NewNotifierClientSender(nil, "https://hooks.example/ops", "webhook")
	assert.Error(t, err)
}

func TestNotificationChannelMapping(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want notifierv1.NotificationChannel
	}{
		{"webhook", notifierv1.NotificationChannel_NOTIFICATION_CHANNEL_WEBHOOK},
		{"Slack", notifierv1.NotificationChannel_NOTIFICATION_CHANNEL_SLACK},
		{"email", notifierv1.NotificationChannel_NOTIFICATION_CHANNEL_EMAIL},
		{"", notifierv1.NotificationChannel_NOTIFICATION_CHANNEL_WEBHOOK},
		{"unknown", notifierv1.NotificationChannel_NOTIFICATION_CHANNEL_WEBHOOK},
	} {
		assert.Equal(t, tc.want, notificationChannel(tc.in), "channel %q", tc.in)
	}
}

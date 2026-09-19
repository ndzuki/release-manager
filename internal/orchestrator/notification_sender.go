package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	notifierv1 "github.com/ndzuki/release-manager/api/gen/notifier/v1"
	notifierv1connect "github.com/ndzuki/release-manager/api/gen/notifier/v1/notifierv1connect"
	"github.com/ndzuki/release-manager/internal/store"
)

// NotifierClientSender delivers a queued terminal notification by calling the
// notifier's Send over Connect (REQ-031 AC-031-12).
//
// The outbox payload carries the operation id; the recipient and channel are
// deployment configuration, because REQ-031's payload contract names them but no
// per-organization source exists yet (D-N, 2026-09-19).
type NotifierClientSender struct {
	client    notifierv1connect.NotifierServiceClient
	recipient string
	channel   notifierv1.NotificationChannel
}

// NewNotifierClientSender builds the sender. recipient must be non-empty: an
// empty recipient would make Send create a job that can never be delivered.
func NewNotifierClientSender(
	client notifierv1connect.NotifierServiceClient,
	recipient, channel string,
) (*NotifierClientSender, error) {
	if client == nil {
		return nil, fmt.Errorf("notifier client is required")
	}
	if strings.TrimSpace(recipient) == "" {
		return nil, fmt.Errorf("notification recipient is required")
	}
	return &NotifierClientSender{
		client:    client,
		recipient: recipient,
		channel:   notificationChannel(channel),
	}, nil
}

// notificationChannel maps a configured channel name onto the wire enum. An
// unknown name becomes the webhook channel, matching the notifier's own
// documented fallback rather than failing the delivery.
func notificationChannel(name string) notifierv1.NotificationChannel {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "email":
		return notifierv1.NotificationChannel_NOTIFICATION_CHANNEL_EMAIL
	case "slack":
		return notifierv1.NotificationChannel_NOTIFICATION_CHANNEL_SLACK
	default:
		return notifierv1.NotificationChannel_NOTIFICATION_CHANNEL_WEBHOOK
	}
}

// operationTerminalPayload mirrors the fields the terminal transition writes.
type operationTerminalPayload struct {
	EventType           string `json:"event_type"`
	OperationID         string `json:"operation_id"`
	OperationType       string `json:"operation_type"`
	TerminalStatus      string `json:"terminal_status"`
	ReleaseDefinitionID string `json:"release_definition_id"`
}

// Send decodes one outbox entry and delivers it. A malformed payload is an error
// so the entry stays queued and visible, rather than being acknowledged.
func (s *NotifierClientSender) Send(ctx context.Context, entry *store.ApprovalOutboxEntry) error {
	if entry == nil {
		return fmt.Errorf("outbox entry is required")
	}
	var payload operationTerminalPayload
	if err := json.Unmarshal(entry.PayloadJSON, &payload); err != nil {
		return fmt.Errorf("decode %s payload: %w", entry.EventType, err)
	}
	if payload.OperationID == "" {
		return fmt.Errorf("%s payload has no operation_id", entry.EventType)
	}
	metadata := map[string]string{}
	if payload.TerminalStatus != "" {
		metadata["terminal_status"] = payload.TerminalStatus
	}
	if payload.OperationType != "" {
		metadata["operation_type"] = payload.OperationType
	}
	_, err := s.client.Send(ctx, connect.NewRequest(&notifierv1.SendNotificationRequest{
		OperationId: payload.OperationID,
		Channel:     s.channel,
		Recipient:   s.recipient,
		Metadata:    metadata,
	}))
	if err != nil {
		return fmt.Errorf("send terminal notification for %s: %w", payload.OperationID, err)
	}
	return nil
}

var _ NotificationSender = (*NotifierClientSender)(nil)

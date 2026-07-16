// Package notifier implements the notification service with retry and dead-letter support.
package notifier

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	notifierv1 "github.com/ndzuki/release-manager/api/gen/notifier/v1"
	notifierv1connect "github.com/ndzuki/release-manager/api/gen/notifier/v1/notifierv1connect"
	"github.com/ndzuki/release-manager/internal/store"
)

// Service implements the NotifierServiceHandler Connect interface.
type Service struct {
	store  store.Store
	logger *slog.Logger
}

// NewService creates a new NotifierService Connect handler.
func NewService(st store.Store, logger *slog.Logger) *Service {
	return &Service{store: st, logger: logger}
}

// Send creates a notification job for the given terminal operation event.
// AC-031-01: deduplication is enforced by the SQLite UNIQUE(operation_id, channel, recipient) index.
func (s *Service) Send(
	ctx context.Context,
	req *connect.Request[notifierv1.SendNotificationRequest],
) (*connect.Response[notifierv1.SendNotificationResponse], error) {
	msg := req.Msg

	channel := notificationChannelFromProto(msg.Channel)
	job := &store.NotificationJob{
		ID:          uuid.New().String(),
		OperationID: msg.OperationId,
		Channel:     channel,
		Recipient:   msg.Recipient,
		Status:      store.NotificationPending,
		MaxRetries:  3,
		Metadata:    msg.Metadata,
	}
	if job.Metadata == nil {
		job.Metadata = make(map[string]string)
	}

	if err := s.store.Notifications().Create(ctx, job); err != nil {
		s.logger.Error("failed to create notification job",
			"operation_id", msg.OperationId,
			"channel", channel,
			"error", err,
		)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	s.logger.Info("notification job created",
		"job_id", job.ID,
		"operation_id", msg.OperationId,
		"channel", channel,
	)

	return connect.NewResponse(&notifierv1.SendNotificationResponse{
		JobId:  job.ID,
		Status: notifierv1.NotificationStatus_NOTIFICATION_STATUS_PENDING,
	}), nil
}

// GetStatus returns the current delivery status of a notification job.
func (s *Service) GetStatus(
	ctx context.Context,
	req *connect.Request[notifierv1.GetNotificationStatusRequest],
) (*connect.Response[notifierv1.GetNotificationStatusResponse], error) {
	job, err := s.store.Notifications().Get(ctx, req.Msg.JobId)
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := &notifierv1.GetNotificationStatusResponse{
		JobId:      job.ID,
		Status:     notificationStatusToProto(job.Status),
		RetryCount: int32(job.RetryCount), //nolint:gosec // RetryCount bounded by MaxRetries (≤ 100)
		LastError:  job.LastError,
	}
	if job.NextRetryAt != nil {
		resp.NextRetryAt = timestamppbNew(*job.NextRetryAt)
	}

	return connect.NewResponse(resp), nil
}

// Compile-time interface check.
var _ notifierv1connect.NotifierServiceHandler = (*Service)(nil)

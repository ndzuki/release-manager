package notifier

import (
	"time"

	notifierv1 "github.com/ndzuki/release-manager/api/gen/notifier/v1"
	"github.com/ndzuki/release-manager/internal/store"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func notificationChannelFromProto(ch notifierv1.NotificationChannel) store.NotificationChannel {
	switch ch {
	case notifierv1.NotificationChannel_NOTIFICATION_CHANNEL_WEBHOOK:
		return store.NotificationChannelWebhook
	case notifierv1.NotificationChannel_NOTIFICATION_CHANNEL_EMAIL:
		return store.NotificationChannelEmail
	case notifierv1.NotificationChannel_NOTIFICATION_CHANNEL_SLACK:
		return store.NotificationChannelSlack
	default:
		return store.NotificationChannelWebhook
	}
}

func notificationStatusToProto(s store.NotificationStatus) notifierv1.NotificationStatus {
	switch s {
	case store.NotificationPending:
		return notifierv1.NotificationStatus_NOTIFICATION_STATUS_PENDING
	case store.NotificationSending:
		return notifierv1.NotificationStatus_NOTIFICATION_STATUS_SENDING
	case store.NotificationDelivered:
		return notifierv1.NotificationStatus_NOTIFICATION_STATUS_DELIVERED
	case store.NotificationFailed:
		return notifierv1.NotificationStatus_NOTIFICATION_STATUS_FAILED
	case store.NotificationDeadLetter:
		return notifierv1.NotificationStatus_NOTIFICATION_STATUS_DEAD_LETTER
	default:
		return notifierv1.NotificationStatus_NOTIFICATION_STATUS_UNSPECIFIED
	}
}

func timestamppbNew(t time.Time) *timestamppb.Timestamp {
	return timestamppb.New(t)
}

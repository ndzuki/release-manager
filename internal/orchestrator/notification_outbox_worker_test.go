package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// stubSender records what it was asked to send and can be made to fail.
type stubSender struct {
	failNext int
	sent     []string
}

func (s *stubSender) Send(_ context.Context, entry *store.ApprovalOutboxEntry) error {
	if s.failNext > 0 {
		s.failNext--
		return errors.New("notifier unavailable")
	}
	s.sent = append(s.sent, entry.ID)
	return nil
}

// seedNotificationOutbox writes one undelivered entry the way the terminal
// transition does.
func seedNotificationOutbox(t *testing.T, st *sqlitestore.Store, id string) {
	t.Helper()
	_, err := st.DB().ExecContext(context.Background(), `
		INSERT INTO notification_outbox (id, event_type, payload_json, created_at, delivered)
		VALUES (?, 'OperationTerminal', '{}', ?, 0)
	`, id, time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
}

func notificationOutboxDelivered(t *testing.T, st *sqlitestore.Store, id string) bool {
	t.Helper()
	var delivered bool
	require.NoError(t, st.DB().QueryRowContext(context.Background(),
		`SELECT delivered FROM notification_outbox WHERE id = ?`, id).Scan(&delivered))
	return delivered
}

func newWorkerForTest(t *testing.T, st *sqlitestore.Store, sender NotificationSender) *NotificationOutboxWorker {
	t.Helper()
	return NewNotificationOutboxWorker(
		st.NotificationOutbox(), sender, slog.New(slog.DiscardHandler),
		NotificationOutboxWorkerConfig{PollInterval: time.Minute, BatchSize: 10},
	)
}

// AC-031-12: a delivered notification is acknowledged exactly once.
func TestNotificationOutboxWorker_DeliversAndAcknowledges(t *testing.T) {
	st, err := sqlitestore.Open(t.TempDir() + "/notif-outbox.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	seedNotificationOutbox(t, st, "outbox-1")
	sender := &stubSender{}
	worker := newWorkerForTest(t, st, sender)

	delivered, failed := worker.ProcessOnce(context.Background())
	assert.Equal(t, 1, delivered)
	assert.Equal(t, 0, failed)
	assert.Equal(t, []string{"outbox-1"}, sender.sent)
	assert.True(t, notificationOutboxDelivered(t, st, "outbox-1"))

	// A second pass finds nothing: the entry is no longer undelivered.
	delivered, _ = worker.ProcessOnce(context.Background())
	assert.Equal(t, 0, delivered, "an acknowledged entry must not be sent twice")
	assert.Len(t, sender.sent, 1)
}

// AC-031-12: a Send failure must NOT acknowledge the entry, so the notification
// survives and is retried.
func TestNotificationOutboxWorker_FailureKeepsTheEntryQueued(t *testing.T) {
	st, err := sqlitestore.Open(t.TempDir() + "/notif-outbox-fail.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	seedNotificationOutbox(t, st, "outbox-2")
	sender := &stubSender{failNext: 1}
	worker := newWorkerForTest(t, st, sender)

	delivered, failed := worker.ProcessOnce(context.Background())
	assert.Equal(t, 0, delivered)
	assert.Equal(t, 1, failed)
	assert.False(t, notificationOutboxDelivered(t, st, "outbox-2"),
		"a failed send must leave the entry undelivered for the next poll")

	// The next poll retries it and succeeds.
	delivered, failed = worker.ProcessOnce(context.Background())
	assert.Equal(t, 1, delivered, "the retry must deliver the notification")
	assert.Equal(t, 0, failed)
	assert.True(t, notificationOutboxDelivered(t, st, "outbox-2"))
}

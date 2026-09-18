package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// newJob builds a job for the given dedup triple.
func newJob(triple string, status store.NotificationStatus) *store.NotificationJob {
	now := time.Now().UTC()
	return &store.NotificationJob{
		ID: uuid.New().String(), OperationID: triple, Channel: "webhook", Recipient: "ops@example.com",
		Status: status, MaxRetries: 3, CreatedAt: now, UpdatedAt: now,
	}
}

// AC-031-10: a dead_letter job must not block a new job for the same
// (operation_id, channel, recipient) -- that is the replay chain's prerequisite.
func TestNotificationJobDedup_DeadLetterAllowsANewJob(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	require.NoError(t, st.Notifications().Create(ctx, newJob("op-replay", store.NotificationDeadLetter)))

	err := st.Notifications().Create(ctx, newJob("op-replay", store.NotificationPending))
	assert.NoError(t, err, "a dead_letter job must not block a new one")
}

// The other half of the same rule: a non-terminal job still owns the triple.
func TestNotificationJobDedup_NonTerminalStillBlocks(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	require.NoError(t, st.Notifications().Create(ctx, newJob("op-live", store.NotificationPending)))

	err := st.Notifications().Create(ctx, newJob("op-live", store.NotificationSending))
	require.Error(t, err, "a non-terminal job must keep owning the triple")
}

// A delivered job is terminal too, so it does not block either.
func TestNotificationJobDedup_DeliveredAllowsANewJob(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	require.NoError(t, st.Notifications().Create(ctx, newJob("op-done", store.NotificationDelivered)))

	assert.NoError(t, st.Notifications().Create(ctx, newJob("op-done", store.NotificationPending)))
}

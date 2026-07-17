package notifier_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"log/slog"

	"github.com/ndzuki/release-manager/internal/notifier"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// stubSender records delivery attempts for test observation.
type stubSender struct {
	results []stubResult
	mu      sync.Mutex
	calls   atomic.Int32
}

type stubResult struct {
	errorCode string
	is4xx     bool
	err       error
}

func (s *stubSender) Send(_ context.Context, _ *store.NotificationJob) (
	errorCode string,
	is4xx bool,
	err error,
) {
	s.mu.Lock()
	idx := int(s.calls.Add(1)) - 1
	var r stubResult
	if idx < len(s.results) {
		r = s.results[idx]
	}
	s.mu.Unlock()
	return r.errorCode, r.is4xx, r.err
}

type testClock struct {
	t time.Time
}

func (c *testClock) Now() time.Time { return c.t }

func setupTestConsumer(t *testing.T, st *sqlitestore.Store) (*notifier.Consumer, *stubSender, *testClock) {
	t.Helper()
	sender := &stubSender{}
	logger := slog.New(slog.DiscardHandler)
	cfg := notifier.DefaultConsumerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	cfg.RetryCfg = notifier.DefaultRetryConfig()
	cfg.RetryCfg.MaxRetries = 3
	cfg.RetryCfg.InitialBackoff = 1 * time.Millisecond
	cfg.RetryCfg.DeadlineAfter = 1 * time.Hour
	cfg.JobDeadline = 1 * time.Hour
	cfg.DLCleanupInterval = 1 * time.Hour
	cfg.DLCleanupMaxAge = 30 * 24 * time.Hour

	c := notifier.NewConsumer(st.Notifications(), sender, logger, cfg)
	clk := &testClock{t: time.Now().UTC()}
	c.SetClock(clk)
	return c, sender, clk
}

func createJob(t *testing.T, st *sqlitestore.Store, opID string) *store.NotificationJob {
	t.Helper()
	job := &store.NotificationJob{
		ID:          uuid.New().String(),
		OperationID: opID,
		Channel:     store.NotificationChannelWebhook,
		Recipient:   "https://example.com/notify",
		Status:      store.NotificationPending,
		MaxRetries:  3,
	}
	require.NoError(t, st.Notifications().Create(context.Background(), job))
	return job
}

func TestConsumer_SingleDelivery(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	c, sender, _ := setupTestConsumer(t, st)

	sender.results = []stubResult{
		{errorCode: notifier.ErrCodeDelivered, is4xx: false, err: nil},
	}
	job := createJob(t, st, "op-1")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go c.Run(ctx)

	// Wait for delivery.
	assert.Eventually(t, func() bool {
		j, err := st.Notifications().Get(context.Background(), job.ID)
		return err == nil && j.Status == store.NotificationDelivered
	}, 5*time.Second, 50*time.Millisecond, "job should be delivered")
}

func TestConsumer_RetryOn429(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	_, sender, clk := setupTestConsumer(t, st)

	// First two calls fail with 429, third succeeds.
	sender.results = []stubResult{
		{errorCode: notifier.ErrCodeRateLimited, is4xx: false, err: assert.AnError},
		{errorCode: notifier.ErrCodeRateLimited, is4xx: false, err: assert.AnError},
		{errorCode: notifier.ErrCodeDelivered, is4xx: false, err: nil},
	}
	job := createJob(t, st, "op-2")

	// Manually simulate claim → deliver → retry cycle.
	claimed, err := st.Notifications().ClaimNext(context.Background(), clk.Now())
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, job.ID, claimed.ID)
	assert.Equal(t, 1, claimed.Attempts)
	assert.Equal(t, store.NotificationSending, claimed.Status)

	// Manually deliver (will fail with rate_limited).
	errCode, is4xx, err := sender.Send(context.Background(), claimed)
	require.Error(t, err)
	assert.Equal(t, notifier.ErrCodeRateLimited, errCode)
	assert.False(t, is4xx)

	// Schedule retry.
	newRetry := notifier.ComputeNextRetry(0, notifier.DefaultRetryConfig())
	require.NoError(t, st.Notifications().UpdateStatus(context.Background(), claimed.ID,
		store.NotificationFailed, claimed.Attempts, 1, errCode, &newRetry, err.Error(), nil))

	// Verify failed status.
	j, err := st.Notifications().Get(context.Background(), claimed.ID)
	require.NoError(t, err)
	assert.Equal(t, store.NotificationFailed, j.Status)
	assert.Equal(t, 1, j.RetryCount)
	assert.Equal(t, notifier.ErrCodeRateLimited, j.ErrorCode)

	// Advance clock past next_retry_at.
	clk.t = newRetry.Add(1 * time.Second)

	// Claim again — should pick up the retryable job.
	claimed2, err := st.Notifications().ClaimNext(context.Background(), clk.Now())
	require.NoError(t, err)
	require.NotNil(t, claimed2)
	assert.Equal(t, job.ID, claimed2.ID)
	assert.Equal(t, 2, claimed2.Attempts)
}

func TestConsumer_DeadLetterOn4xx(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	_, sender, clk := setupTestConsumer(t, st)

	// 4xx config error → instant dead-letter.
	sender.results = []stubResult{
		{errorCode: notifier.ErrCodeInvalidRecipient, is4xx: true, err: assert.AnError},
	}
	createJob(t, st, "op-3")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	claimed, err := st.Notifications().ClaimNext(ctx, clk.Now())
	require.NoError(t, err)
	require.NotNil(t, claimed)

	_, is4xx, err := sender.Send(ctx, claimed)
	require.Error(t, err)
	assert.True(t, is4xx)

	// Should dead-letter.
	require.NoError(t, st.Notifications().MarkDeadLetter(ctx, claimed.ID,
		notifier.ErrCodeInvalidRecipient, err.Error()))

	j, err := st.Notifications().Get(ctx, claimed.ID)
	require.NoError(t, err)
	assert.Equal(t, store.NotificationDeadLetter, j.Status)
	assert.Equal(t, notifier.ErrCodeInvalidRecipient, j.ErrorCode)
	assert.NotNil(t, j.DeadLetterAt)
}

func TestConsumer_DedupOnDuplicateEvent(t *testing.T) {
	// AC-031-01: duplicate terminal event → same job, no duplicate delivery.
	st := sqlitestore.OpenTest(t)

	job1 := &store.NotificationJob{
		ID:          uuid.New().String(),
		OperationID: "op-dup",
		Channel:     store.NotificationChannelWebhook,
		Recipient:   "https://example.com/notify",
		Status:      store.NotificationPending,
		MaxRetries:  3,
	}
	require.NoError(t, st.Notifications().Create(context.Background(), job1))

	// Second Create with same (operation_id, channel, recipient) → fails.
	job2 := &store.NotificationJob{
		ID:          uuid.New().String(),
		OperationID: "op-dup",
		Channel:     store.NotificationChannelWebhook,
		Recipient:   "https://example.com/notify",
		Status:      store.NotificationPending,
		MaxRetries:  3,
	}
	err := st.Notifications().Create(context.Background(), job2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNIQUE")
}

func TestConsumer_DeleteDeadLetter(t *testing.T) {
	st := sqlitestore.OpenTest(t)

	// Create a dead-letter job.
	job := &store.NotificationJob{
		ID:          uuid.New().String(),
		OperationID: "op-dl",
		Channel:     store.NotificationChannelWebhook,
		Recipient:   "https://example.com/notify",
		Status:      store.NotificationPending,
		MaxRetries:  3,
	}
	require.NoError(t, st.Notifications().Create(context.Background(), job))
	require.NoError(t, st.Notifications().MarkDeadLetter(context.Background(),
		job.ID, "expired", "test cleanup"))

	j, err := st.Notifications().Get(context.Background(), job.ID)
	require.NoError(t, err)
	assert.NotNil(t, j.DeadLetterAt)

	// Delete records older than 1 second from now (should match this one).
	n, err := st.Notifications().DeleteDeadLetterBefore(
		context.Background(), time.Now().UTC().Add(1*time.Second))
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	// Verify deleted.
	_, err = st.Notifications().Get(context.Background(), job.ID)
	assert.Error(t, err)
}

func TestConsumer_ClaimNextNoJobs(t *testing.T) {
	st := sqlitestore.OpenTest(t)

	claimed, err := st.Notifications().ClaimNext(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	assert.Nil(t, claimed, "no jobs should return nil")
}

func TestConsumer_ClaimNextSkipsFutureRetry(t *testing.T) {
	st := sqlitestore.OpenTest(t)

	// Create a job with future next_retry_at.
	future := time.Now().UTC().Add(1 * time.Hour)
	job := &store.NotificationJob{
		ID:          uuid.New().String(),
		OperationID: "op-future",
		Channel:     store.NotificationChannelWebhook,
		Recipient:   "https://example.com/notify",
		Status:      store.NotificationFailed,
		MaxRetries:  3,
		RetryCount:  1,
		NextRetryAt: &future,
	}
	require.NoError(t, st.Notifications().Create(context.Background(), job))

	// ClaimNext with "now" should skip this job.
	claimed, err := st.Notifications().ClaimNext(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	assert.Nil(t, claimed, "future retry should not be claimed")
}

func TestConsumer_ClaimNextAtomic(t *testing.T) {
	// Two concurrent claims should return different jobs.
	st := sqlitestore.OpenTest(t)

	createJob(t, st, "op-a")
	createJob(t, st, "op-b")

	ctx := context.Background()
	now := time.Now().UTC()

	claimed1, err := st.Notifications().ClaimNext(ctx, now)
	require.NoError(t, err)
	require.NotNil(t, claimed1)

	claimed2, err := st.Notifications().ClaimNext(ctx, now)
	require.NoError(t, err)
	require.NotNil(t, claimed2)

	assert.NotEqual(t, claimed1.ID, claimed2.ID, "concurrent claims must return different jobs")
}

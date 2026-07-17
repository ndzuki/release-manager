package notifier_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ndzuki/release-manager/internal/notifier"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebhookSender_Success(t *testing.T) {
	var capturedMethod, capturedContentType, capturedIdempotency string
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedContentType = r.Header.Get("Content-Type")
		capturedIdempotency = r.Header.Get("Idempotency-Key")
		capturedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := notifier.NewWebhookSender(nil)
	job := &store.NotificationJob{
		ID:          "job-1",
		OperationID: "op-1",
		Channel:     store.NotificationChannelWebhook,
		Recipient:   srv.URL + "/notify",
		Metadata:    map[string]string{"key": "val"},
	}

	errCode, is4xx, err := sender.Send(context.Background(), job)
	require.NoError(t, err)
	assert.Equal(t, notifier.ErrCodeDelivered, errCode)
	assert.False(t, is4xx)
	assert.Equal(t, http.MethodPost, capturedMethod)
	assert.Equal(t, "application/json", capturedContentType)
	assert.NotEmpty(t, capturedIdempotency)
	assert.Contains(t, string(capturedBody), `"operation_id"`)
}

func TestWebhookSender_429RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	sender := notifier.NewWebhookSender(nil)
	job := &store.NotificationJob{
		ID:        "job-2",
		Channel:   store.NotificationChannelWebhook,
		Recipient: srv.URL + "/notify",
	}

	errCode, is4xx, err := sender.Send(context.Background(), job)
	require.Error(t, err)
	assert.Equal(t, notifier.ErrCodeRateLimited, errCode)
	assert.False(t, is4xx, "429 should be retryable not 4xx dead-letter")
}

func TestWebhookSender_5xxRetryable(t *testing.T) {
	for _, code := range []int{500, 502, 503} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.WriteHeader(code)
			}))
			defer srv.Close()

			sender := notifier.NewWebhookSender(nil)
			job := &store.NotificationJob{
				ID:        "job-5xx",
				Channel:   store.NotificationChannelWebhook,
				Recipient: srv.URL + "/notify",
			}

			errCode, is4xx, err := sender.Send(context.Background(), job)
			require.Error(t, err)
			assert.Equal(t, notifier.ErrCodeNetworkError, errCode)
			assert.False(t, is4xx, "5xx should be retryable")
		})
	}
}

func TestWebhookSender_401403DeadLetter(t *testing.T) {
	for _, code := range []int{401, 403} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.WriteHeader(code)
			}))
			defer srv.Close()

			sender := notifier.NewWebhookSender(nil)
			job := &store.NotificationJob{
				ID:        "job-auth",
				Channel:   store.NotificationChannelWebhook,
				Recipient: srv.URL + "/notify",
			}

			errCode, is4xx, err := sender.Send(context.Background(), job)
			require.Error(t, err)
			assert.Equal(t, notifier.ErrCodeCredentialInvalid, errCode)
			assert.True(t, is4xx, "401/403 should be non-retryable dead-letter")
		})
	}
}

func TestWebhookSender_400InvalidRecipient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	sender := notifier.NewWebhookSender(nil)
	job := &store.NotificationJob{
		ID:        "job-bad",
		Channel:   store.NotificationChannelWebhook,
		Recipient: srv.URL + "/notify",
	}

	errCode, is4xx, err := sender.Send(context.Background(), job)
	require.Error(t, err)
	assert.Equal(t, notifier.ErrCodeInvalidRecipient, errCode)
	assert.True(t, is4xx, "400 should be dead-letter")
}

func TestWebhookSender_InvalidURL(t *testing.T) {
	sender := notifier.NewWebhookSender(nil)
	job := &store.NotificationJob{
		ID:        "job-bad-url",
		Channel:   store.NotificationChannelWebhook,
		Recipient: "::invalid-url",
	}

	errCode, is4xx, err := sender.Send(context.Background(), job)
	require.Error(t, err)
	assert.Equal(t, notifier.ErrCodeInvalidRecipient, errCode)
	assert.True(t, is4xx)
}

func TestWebhookSender_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 10 * time.Millisecond}
	sender := notifier.NewWebhookSender(client)
	job := &store.NotificationJob{
		ID:        "job-timeout",
		Channel:   store.NotificationChannelWebhook,
		Recipient: srv.URL + "/notify",
	}

	errCode, is4xx, err := sender.Send(context.Background(), job)
	require.Error(t, err)
	assert.Equal(t, notifier.ErrCodeTimeout, errCode)
	assert.False(t, is4xx, "timeout should be retryable")
}

func TestWebhookSender_WrongChannel(t *testing.T) {
	sender := notifier.NewWebhookSender(nil)
	job := &store.NotificationJob{
		ID:        "job-email",
		Channel:   store.NotificationChannelEmail,
		Recipient: "user@example.com",
	}

	errCode, is4xx, err := sender.Send(context.Background(), job)
	require.Error(t, err)
	assert.Equal(t, notifier.ErrCodeInvalidRecipient, errCode)
	assert.True(t, is4xx, "unsupported channel should dead-letter")
}

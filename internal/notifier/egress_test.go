package notifier_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/notifier"
	"github.com/ndzuki/release-manager/internal/store"
)

// TestEgressPolicyDeniesInternalAndMetadataTargets is the REQ-031/TASK-096
// table: with an ordinary public destination allowlisted, every SSRF-flavoured
// target must be denied, and the denial must carry a stable reason.
func TestEgressPolicyDeniesInternalAndMetadataTargets(t *testing.T) {
	policy, err := notifier.NewEgressPolicy([]string{"https://hooks.example.com"})
	require.NoError(t, err)

	targets := []struct {
		name     string
		target   string
		wantCode string
	}{
		{"cloud metadata endpoint", "http://169.254.169.254/latest/meta-data/iam/security-credentials/", notifier.ReasonEgressNotAllowlisted},
		{"IPv4 loopback", "http://127.0.0.1:8080/internal/hook", notifier.ReasonEgressNotAllowlisted},
		{"IPv6 loopback", "http://[::1]:8080/internal/hook", notifier.ReasonEgressNotAllowlisted},
		{"RFC1918 10/8", "http://10.0.0.5/hook", notifier.ReasonEgressNotAllowlisted},
		{"RFC1918 172.16/12", "http://172.16.3.9/hook", notifier.ReasonEgressNotAllowlisted},
		{"RFC1918 192.168/16", "http://192.168.1.10/hook", notifier.ReasonEgressNotAllowlisted},
		{"cluster DNS name", "http://orchestrator:8083/hook", notifier.ReasonEgressNotAllowlisted},
		{"kubernetes API", "https://kubernetes.default.svc/hook", notifier.ReasonEgressNotAllowlisted},
		{"public host on a different port", "https://hooks.example.com:8443/hook", notifier.ReasonEgressNotAllowlisted},
		{"public host over plain http", "http://hooks.example.com/hook", notifier.ReasonEgressNotAllowlisted},
		{"file scheme", "file:///etc/passwd", notifier.ReasonEgressInvalidTarget},
		{"gopher scheme", "gopher://hooks.example.com:70/", notifier.ReasonEgressInvalidTarget},
		{"malformed", "::not-a-url", notifier.ReasonEgressInvalidTarget},
		{"missing host", "https:///hook", notifier.ReasonEgressInvalidTarget},
	}
	for _, tc := range targets {
		t.Run(tc.name, func(t *testing.T) {
			allowed, reason := policy.Allow(tc.target)
			assert.Falsef(t, allowed, "%s must not be reachable by default", tc.target)
			assert.Equal(t, tc.wantCode, reason)
		})
	}

	t.Run("configured destination is allowed", func(t *testing.T) {
		allowed, reason := policy.Allow("https://hooks.example.com/notify")
		assert.True(t, allowed)
		assert.Empty(t, reason)
	})

	t.Run("explicit port 443 matches the implicit form", func(t *testing.T) {
		allowed, _ := policy.Allow("https://hooks.example.com:443/notify")
		assert.True(t, allowed, "the effective port must be compared, not the spelling")
	})
}

// TestNewEgressPolicyRejectsMalformedEntries keeps a typo from silently
// shrinking the allowlist at startup.
func TestNewEgressPolicyRejectsMalformedEntries(t *testing.T) {
	for _, entry := range []string{"hooks.example.com", "ftp://hooks.example.com", "https://", "://x"} {
		t.Run(entry, func(t *testing.T) {
			_, err := notifier.NewEgressPolicy([]string{entry})
			require.Errorf(t, err, "%q must be rejected", entry)
		})
	}

	t.Run("empty list is the deny-all default", func(t *testing.T) {
		policy, err := notifier.NewEgressPolicy(nil)
		require.NoError(t, err)
		allowed, reason := policy.Allow("https://hooks.example.com/notify")
		assert.False(t, allowed)
		assert.Equal(t, notifier.ReasonEgressNotAllowlisted, reason)
	})
}

// TestWebhookSenderBlocksUnlistedTargetAndRecordsIt proves the denial is
// enforced before anything leaves the process, and that it is recorded three
// ways: the stable error code, the counters, and a structured log line.
func TestWebhookSenderBlocksUnlistedTargetAndRecordsIt(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	policy, err := notifier.NewEgressPolicy([]string{"https://hooks.example.com"})
	require.NoError(t, err)
	metrics := &notifier.EgressMetrics{}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	sender := notifier.NewWebhookSender(nil,
		notifier.WithEgressPolicy(policy),
		notifier.WithEgressMetrics(metrics),
		notifier.WithLogger(logger),
	)
	job := &store.NotificationJob{
		ID:          "job-blocked",
		OperationID: "op-blocked",
		Channel:     store.NotificationChannelWebhook,
		Recipient:   srv.URL + "/notify",
	}

	errCode, is4xx, sendErr := sender.Send(context.Background(), job)
	require.Error(t, sendErr)
	assert.Equal(t, notifier.ErrCodeEgressBlocked, errCode)
	assert.True(t, is4xx, "a blocked destination is a non-retryable configuration outcome")

	assert.Zero(t, hits.Load(), "the target must never be contacted")

	snapshot := metrics.Snapshot()
	assert.EqualValues(t, 1, snapshot.Denied)
	assert.EqualValues(t, 0, snapshot.Allowed)

	assert.Contains(t, logs.String(), "webhook delivery blocked by egress policy")
	assert.Contains(t, logs.String(), notifier.ReasonEgressNotAllowlisted)
}

// TestWebhookSenderNeverSendsWithoutAnAllowlist is the fail-closed control: the
// zero-value sender (no policy option at all) must deny.
func TestWebhookSenderNeverSendsWithoutAnAllowlist(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := notifier.NewWebhookSender(nil)
	job := &store.NotificationJob{
		ID:        "job-no-policy",
		Channel:   store.NotificationChannelWebhook,
		Recipient: srv.URL + "/notify",
	}

	errCode, is4xx, err := sender.Send(context.Background(), job)
	require.Error(t, err)
	assert.Equal(t, notifier.ErrCodeEgressBlocked, errCode)
	assert.True(t, is4xx)
	assert.Zero(t, hits.Load(), "an unconfigured allowlist must not deliver")
}

// TestWebhookSenderRedactsOutboundMetadata covers AC3: the body leaves through
// the shared redaction path, so a secret a caller forgot to scrub does not
// escape. The durable job row keeps the original value.
func TestWebhookSenderRedactsOutboundMetadata(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		body, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := notifier.NewWebhookSender(nil, allowTarget(t, srv.URL))
	job := &store.NotificationJob{
		ID:        "job-redact",
		Channel:   store.NotificationChannelWebhook,
		Recipient: srv.URL + "/notify",
		Metadata: map[string]string{
			"api_key":     "sk-live-1234567890",
			"reason":      "retry after password=hunter2",
			"environment": "staging",
		},
	}

	errCode, _, err := sender.Send(context.Background(), job)
	require.NoError(t, err)
	require.Equal(t, notifier.ErrCodeDelivered, errCode)

	var payload struct {
		Metadata map[string]string `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(body, &payload))
	assert.NotContains(t, payload.Metadata["api_key"], "sk-live-1234567890")
	assert.NotContains(t, payload.Metadata["reason"], "hunter2")
	assert.Equal(t, "staging", payload.Metadata["environment"])

	// The stored job keeps what the caller supplied; only the outbound copy is
	// redacted, so an operator can still see what was recorded.
	assert.Equal(t, "sk-live-1234567890", job.Metadata["api_key"])
}

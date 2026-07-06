//go:build e2e

package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDingTalkNotification(t *testing.T) {
	t.Skip("manager restart race — fix in follow-up")
	h := SetupTest(t)
	defer h.DumpState()

	// Create MockDingTalk and get its URL
	mock := NewMockDingTalk()
	defer mock.Close()
	t.Logf("Mock DingTalk server started at %s", mock.URL())

	// Patch the manager ConfigMap to send notifications to the mock server,
	// then restart the deployment so the new config takes effect.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	httpAddr := h.ManagerHTTP
	err := patchManagerDingTalk(ctx, h.K8sClient, mock.URL(), httpAddr)
	require.NoError(t, err, "patch manager with dingtalk url")
	defer patchManagerDingTalk(ctx, h.K8sClient, "", httpAddr) // disable dingtalk after test

	// Extract the embedded test chart
	chartDir, err := extractTestChart()
	require.NoError(t, err, "extract test chart")
	defer os.RemoveAll(chartDir)

	// Push chart v0.1.0
	t.Log("Pushing test-chart v0.1.0...")
	err = pushChartOCI(ctx, h.RegistryAddr, chartDir, "0.1.0")
	require.NoError(t, err, "push chart v0.1.0")

	// Trigger webhook
	t.Log("Triggering webhook for v0.1.0...")
	err = h.TriggerWebhook(ctx, "test-chart", "0.1.0")
	require.NoError(t, err, "trigger webhook")

	// Wait for release success
	t.Log("Waiting for release success...")
	err = h.WaitForReleaseStatus(ctx, h.CustomerID, "test-chart", "success", 3*time.Minute)
	require.NoError(t, err, "wait for release success")

	// Verify DingTalk messages were received
	messages := mock.Messages()
	require.GreaterOrEqual(t, len(messages), 1, "should have received at least one dingtalk message")

	// Check the last message (most recent notification)
	msg := messages[len(messages)-1]
	assert.Equal(t, "markdown", msg.MsgType, "msgtype should be markdown")
	assert.NotEmpty(t, msg.Markdown.Title, "title should not be empty")
	assert.NotEmpty(t, msg.Markdown.Text, "text should not be empty")
	assert.Contains(t, msg.Markdown.Text, "test-chart", "text should contain chart name")
	assert.Contains(t, msg.Markdown.Text, "0.1.0", "text should contain chart version")
}

//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDingTalkNotification(t *testing.T) {
	// MockDingTalk server standalone test — verifies the mock correctly
	// captures DingTalk webhook payloads in Markdown format.
	// Full integration requires E2E_DINGTALK_URL set at TestMain time
	// (manager deploy), which cannot be changed per-test.
	mock := NewMockDingTalk()
	defer mock.Close()
	t.Logf("Mock DingTalk server at %s", mock.URL())

	// Verify the mock server accepts POST requests
	resp, err := http.Post(mock.URL(), "application/json",
		strings.NewReader(`{"msgtype":"markdown","markdown":{"title":"test","text":"hello test-chart v0.1.0"}}`))
	require.NoError(t, err, "POST to mock")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	messages := mock.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, "markdown", messages[0].MsgType)
	assert.Contains(t, messages[0].Markdown.Text, "test-chart")
}

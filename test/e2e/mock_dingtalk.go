//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
)

// DingTalkMessage represents a DingTalk bot webhook message payload.
type DingTalkMessage struct {
	MsgType  string `json:"msgtype"`
	Markdown struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	} `json:"markdown"`
}

// MockDingTalk is a mock DingTalk bot server that captures incoming webhook messages.
type MockDingTalk struct {
	server   *httptest.Server
	messages []DingTalkMessage
	mu       sync.Mutex
}

// NewMockDingTalk creates a new MockDingTalk and starts the HTTP server.
func NewMockDingTalk() *MockDingTalk {
	m := &MockDingTalk{
		messages: make([]DingTalkMessage, 0),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", m.handleWebhook)
	m.server = httptest.NewServer(mux)
	return m
}

// URL returns the base URL of the mock server (e.g. http://127.0.0.1:54321).
func (m *MockDingTalk) URL() string {
	return m.server.URL
}

// Messages returns a copy of all captured DingTalk messages.
func (m *MockDingTalk) Messages() []DingTalkMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]DingTalkMessage, len(m.messages))
	copy(result, m.messages)
	return result
}

// Close stops the mock server.
func (m *MockDingTalk) Close() {
	m.server.Close()
}

// handleWebhook handles incoming DingTalk webhook POST requests.
func (m *MockDingTalk) handleWebhook(w http.ResponseWriter, r *http.Request) {
	var msg DingTalkMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	m.messages = append(m.messages, msg)
	m.mu.Unlock()

	// DingTalk success response
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
}

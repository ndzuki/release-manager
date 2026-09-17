package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	notifierv1 "github.com/ndzuki/release-manager/api/gen/notifier/v1"
	"github.com/ndzuki/release-manager/internal/auth"
)

type stubNotifier struct {
	sends    int
	statuses int
}

func (s *stubNotifier) Send(context.Context, *connect.Request[notifierv1.SendNotificationRequest]) (*connect.Response[notifierv1.SendNotificationResponse], error) {
	s.sends++
	return connect.NewResponse(&notifierv1.SendNotificationResponse{}), nil
}

func (s *stubNotifier) GetStatus(context.Context, *connect.Request[notifierv1.GetNotificationStatusRequest]) (*connect.Response[notifierv1.GetNotificationStatusResponse], error) {
	s.statuses++
	return connect.NewResponse(&notifierv1.GetNotificationStatusResponse{}), nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func post(t *testing.T, base, path, token string) int {
	t.Helper()
	// Connect-Go decodes a unary request before running unary interceptors, so
	// the body must be decodable for the credential check to be the thing under
	// test; an empty body would answer invalid_argument first.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+path, strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	require.NoError(t, err)
	return resp.StatusCode
}

// TestNotifierRejectsUnauthenticatedCallsDirectly covers REQ-031/TASK-096 AC1
// on the direct path: the notifier surface used to accept anonymous machine
// calls, and must now require a scoped service token.
func TestNotifierRejectsUnauthenticatedCallsDirectly(t *testing.T) {
	stub := &stubNotifier{}
	path, handler := newNotifierHandler(stub, discardLogger(), auth.TokenHashes("notifier-token"))
	server := httptest.NewServer(handler)
	defer server.Close()

	sendPath := path + "Send"
	assert.Equal(t, http.StatusUnauthorized, post(t, server.URL, sendPath, ""), "no credential must be rejected")
	assert.Equal(t, http.StatusUnauthorized, post(t, server.URL, sendPath, "wrong-token"), "an unknown credential must be rejected")
	assert.Equal(t, http.StatusUnauthorized, post(t, server.URL, path+"GetStatus", ""), "every procedure is guarded")
	assert.Zero(t, stub.sends, "a rejected call must not reach the handler")

	assert.Equal(t, http.StatusOK, post(t, server.URL, sendPath, "notifier-token"))
	assert.Equal(t, 1, stub.sends, "the configured credential reaches the handler")
}

// TestNotifierRejectsUnauthenticatedCallsThroughTheConsoleOrigin covers the
// other entry named by the card: web/nginx.conf proxies ^~ /notifier.v1. to the
// notifier, so the guard must live in the service (it does) and reject a call
// that arrives through the console origin.
func TestNotifierRejectsUnauthenticatedCallsThroughTheConsoleOrigin(t *testing.T) {
	stub := &stubNotifier{}
	path, handler := newNotifierHandler(stub, discardLogger(), auth.TokenHashes("notifier-token"))
	backend := httptest.NewServer(handler)
	defer backend.Close()

	target, err := url.Parse(backend.URL)
	require.NoError(t, err)
	proxy := httptest.NewServer(httputil.NewSingleHostReverseProxy(target))
	defer proxy.Close()

	sendPath := path + "Send"
	assert.Equal(t, http.StatusUnauthorized, post(t, proxy.URL, sendPath, ""),
		"the console origin must not bypass the service credential")
	assert.Equal(t, http.StatusUnauthorized, post(t, proxy.URL, sendPath, "wrong-token"))
	assert.Zero(t, stub.sends)

	assert.Equal(t, http.StatusOK, post(t, proxy.URL, sendPath, "notifier-token"))
	assert.Equal(t, 1, stub.sends)
}

// TestNotifierRotationAcceptsThePreviousToken documents the rotation contract:
// both the current and the previous digest are accepted while a credential is
// being rolled.
func TestNotifierRotationAcceptsThePreviousToken(t *testing.T) {
	stub := &stubNotifier{}
	path, handler := newNotifierHandler(stub, discardLogger(), auth.TokenHashes("current-token", "previous-token"))
	server := httptest.NewServer(handler)
	defer server.Close()

	assert.Equal(t, http.StatusOK, post(t, server.URL, path+"GetStatus", "current-token"))
	assert.Equal(t, http.StatusOK, post(t, server.URL, path+"GetStatus", "previous-token"))
	assert.Equal(t, http.StatusUnauthorized, post(t, server.URL, path+"GetStatus", "retired-token"))
}

// TestNotifierWithoutTokensIsClosed is the fail-closed control: an unconfigured
// deployment must not expose the surface at all.
func TestNotifierWithoutTokensIsClosed(t *testing.T) {
	stub := &stubNotifier{}
	path, handler := newNotifierHandler(stub, discardLogger(), nil)
	server := httptest.NewServer(handler)
	defer server.Close()

	assert.Equal(t, http.StatusUnauthorized, post(t, server.URL, path+"Send", "anything"))
	assert.Equal(t, http.StatusUnauthorized, post(t, server.URL, path+"Send", ""))
	assert.Zero(t, stub.sends)
}

// Package handler contains HTTP handler tests.
package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealth(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", http.NoBody)
	rec := httptest.NewRecorder()

	Health().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if body == "" {
		t.Error("expected non-empty body")
	}
}

func TestReady_AllPassing(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", http.NoBody)
	rec := httptest.NewRecorder()

	checks := map[string]func() error{
		"db":  func() error { return nil },
		"k8s": func() error { return nil },
	}

	Ready(checks).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestReady_Degraded(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", http.NoBody)
	rec := httptest.NewRecorder()

	checks := map[string]func() error{
		"db":  func() error { return nil },
		"k8s": func() error { return errCheckFailed },
	}

	Ready(checks).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

var errCheckFailed = &testError{msg: "connection refused"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

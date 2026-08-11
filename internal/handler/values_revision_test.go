package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/contracts"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/stretchr/testify/require"
)

// stubValuesStore is a simple in-memory ValuesStore for handler tests.

type stubValuesStore struct {
	items map[string]*store.ValuesRevision
}

func newStubValuesStore() *stubValuesStore {
	return &stubValuesStore{items: make(map[string]*store.ValuesRevision)}
}

func (s *stubValuesStore) Create(_ context.Context, vr *store.ValuesRevision) error {
	s.items[vr.ID] = vr
	return nil
}

func (s *stubValuesStore) Get(_ context.Context, id string) (*store.ValuesRevision, error) {
	vr, ok := s.items[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return vr, nil
}

func (s *stubValuesStore) GetByDigest(_ context.Context, defID, digest string) (*store.ValuesRevision, error) {
	for _, vr := range s.items {
		if vr.ReleaseDefinitionID == defID && vr.Digest == digest {
			return vr, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *stubValuesStore) GetLatestApproved(_ context.Context, defID string) (*store.ValuesRevision, error) {
	var latest *store.ValuesRevision
	for _, vr := range s.items {
		if vr.ReleaseDefinitionID == defID && vr.Status == store.ValuesStatusApproved {
			if latest == nil || vr.Revision > latest.Revision {
				latest = vr
			}
		}
	}
	if latest == nil {
		return nil, store.ErrNotFound
	}
	return latest, nil
}

func (s *stubValuesStore) GetNextRevisionNumber(_ context.Context, defID string) (int, error) {
	maxRev := 0
	for _, vr := range s.items {
		if vr.ReleaseDefinitionID == defID && vr.Revision > maxRev {
			maxRev = vr.Revision
		}
	}
	return maxRev + 1, nil
}

func (s *stubValuesStore) List(_ context.Context, defID string) ([]*store.ValuesRevision, error) {
	var revs []*store.ValuesRevision
	for _, vr := range s.items {
		if vr.ReleaseDefinitionID == defID {
			revs = append(revs, vr)
		}
	}
	return revs, nil
}

func newTestHandler() *ValuesHandler {
	return newValuesHandlerFromValuesStore(newStubValuesStore(), 0, slog.Default())
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

func mustUnmarshal[T any](t *testing.T, data []byte) T {
	t.Helper()
	var value T
	require.NoError(t, json.Unmarshal(data, &value))
	return value
}

// failingValuesStore fails Create with the given error, inheriting the rest
// of stubValuesStore behavior.
type failingValuesStore struct {
	stubValuesStore
	createErr error
}

func (s *failingValuesStore) Create(_ context.Context, _ *store.ValuesRevision) error {
	return s.createErr
}

func TestCreateValuesRevision_InternalErrorSanitized(t *testing.T) {
	h := newValuesHandlerFromValuesStore(&failingValuesStore{
		createErr: fmt.Errorf("create values revision: %w", errors.New("UNIQUE constraint failed: values_revisions.id")),
	}, 0, slog.Default())
	mux := http.NewServeMux()
	h.Register(mux)

	body := map[string]any{
		"release_definition_id": uuid.New().String(),
		"values":                "replicas: 3",
	}
	req := httptest.NewRequestWithContext(
		contracts.WithRequestID(context.Background(), "req-values"),
		http.MethodPost,
		"/api/v1/values-revisions",
		bytes.NewReader(mustMarshal(t, body)),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	resp := mustUnmarshal[errorResponse](t, rec.Body.Bytes())
	if resp.Code != "internal" {
		t.Errorf("expected code internal, got %q", resp.Code)
	}
	if resp.Message != "internal error" {
		t.Errorf("expected generic message, got %q", resp.Message)
	}
	if resp.RequestID != "req-values" {
		t.Errorf("expected request_id req-values, got %q", resp.RequestID)
	}
	if rec.Header().Get(contracts.RequestIDHeader) != "req-values" {
		t.Errorf("expected X-Request-ID header echo, got %q", rec.Header().Get(contracts.RequestIDHeader))
	}
	// AC-010-04: no SQL/internal text reaches the client.
	if body := rec.Body.String(); strings.Contains(body, "UNIQUE") || strings.Contains(body, "values_revisions") {
		t.Errorf("internal detail leaked to client: %s", body)
	}
}

func TestCreateValuesRevision_BadRequestEnvelope(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequestWithContext(
		contracts.WithRequestID(context.Background(), "req-malformed"),
		http.MethodPost,
		"/api/v1/values-revisions",
		bytes.NewReader([]byte("{not json")),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	resp := mustUnmarshal[errorResponse](t, rec.Body.Bytes())
	if resp.Code != "invalid_argument" {
		t.Errorf("expected code invalid_argument, got %q", resp.Code)
	}
	if resp.Message != "invalid request body" {
		t.Errorf("expected stable message, got %q", resp.Message)
	}
	if resp.RequestID != "req-malformed" {
		t.Errorf("expected request_id req-malformed, got %q", resp.RequestID)
	}
	if body := rec.Body.String(); strings.Contains(body, "{not json") {
		t.Errorf("raw decode detail leaked to client: %s", body)
	}
}

func TestCreateValuesRevision_Success(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	h.Register(mux)

	body := map[string]any{
		"release_definition_id": uuid.New().String(),
		"values":                "replicas: 3\nimage:\n  tag: v1.0",
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/values-revisions", bytes.NewReader(mustMarshal(t, body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := mustUnmarshal[valuesResponse](t, rec.Body.Bytes())
	if resp.ID == "" {
		t.Error("expected non-empty id")
	}
	if resp.Status != string(store.ValuesStatusDraft) {
		t.Errorf("expected draft, got %s", resp.Status)
	}
	if resp.Digest == "" {
		t.Error("expected non-empty digest")
	}
	if resp.Revision != 1 {
		t.Errorf("expected revision 1, got %d", resp.Revision)
	}
}

func TestCreateValuesRevision_SecretRejected(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	h.Register(mux)

	body := map[string]any{
		"release_definition_id": uuid.New().String(),
		"values":                "password: mysecret",
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/values-revisions", bytes.NewReader(mustMarshal(t, body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for secret, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateValuesRevision_MissingDefinitionID(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	h.Register(mux)

	body := map[string]any{"values": "key: value"}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/values-revisions", bytes.NewReader(mustMarshal(t, body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestGetValuesRevision_Found(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	h.Register(mux)

	defID := uuid.New().String()
	body := map[string]any{
		"release_definition_id": defID,
		"values":                "key: value",
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/values-revisions", bytes.NewReader(mustMarshal(t, body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	created := mustUnmarshal[valuesResponse](t, rec.Body.Bytes())

	req2 := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/values-revisions/"+created.ID, http.NoBody)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec2.Code)
	}

	got := mustUnmarshal[valuesResponse](t, rec2.Body.Bytes())
	if got.ID != created.ID {
		t.Errorf("id mismatch: %s vs %s", got.ID, created.ID)
	}
}

func TestGetValuesRevision_NotFound(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/values-revisions/nonexistent", http.NoBody)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestListValuesRevision(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	h.Register(mux)

	defID := uuid.New().String()

	for i := 0; i < 2; i++ {
		body := map[string]any{
			"release_definition_id": defID,
			"values":                "key: value",
		}
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/values-revisions", bytes.NewReader(mustMarshal(t, body)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/values-revisions?definition_id="+defID, http.NoBody)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	type listResp struct {
		Revisions []valuesResponse `json:"revisions"`
	}
	resp := mustUnmarshal[listResp](t, rec.Body.Bytes())
	if len(resp.Revisions) != 2 {
		t.Errorf("expected 2 revisions, got %d", len(resp.Revisions))
	}
}

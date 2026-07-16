package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/store"
	"log/slog"
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

func (s *stubValuesStore) Update(_ context.Context, vr *store.ValuesRevision, expectedParentRev string) error {
	existing, ok := s.items[vr.ID]
	if !ok {
		return store.ErrNotFound
	}
	if existing.ParentRevisionID != expectedParentRev {
		return store.ErrOptimisticLock
	}
	existing.Status = vr.Status
	existing.UpdatedAt = vr.UpdatedAt
	existing.Digest = vr.Digest
	existing.SecretRefs = vr.SecretRefs
	return nil
}

func newTestHandler() *ValuesHandler {
	return NewValuesHandler(newStubValuesStore(), 0, slog.Default())
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustUnmarshal[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
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

func TestApproveValuesRevision(t *testing.T) {
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

	req2 := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/values-revisions/"+created.ID+"/approve", http.NoBody)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	approved := mustUnmarshal[valuesResponse](t, rec2.Body.Bytes())
	if approved.Status != string(store.ValuesStatusApproved) {
		t.Errorf("expected approved, got %s", approved.Status)
	}
}

func TestApproveValuesRevision_NotDraft(t *testing.T) {
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

	req2 := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/values-revisions/"+created.ID+"/approve", http.NoBody)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	req3 := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/values-revisions/"+created.ID+"/approve", http.NoBody)
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for double approve, got %d", rec3.Code)
	}
}

func TestRejectValuesRevision(t *testing.T) {
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

	req2 := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/values-revisions/"+created.ID+"/reject", http.NoBody)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec2.Code)
	}

	rejected := mustUnmarshal[valuesResponse](t, rec2.Body.Bytes())
	if rejected.Status != string(store.ValuesStatusRejected) {
		t.Errorf("expected rejected, got %s", rejected.Status)
	}
}

func TestUpdate_OptimisticLock(t *testing.T) {
	st := newStubValuesStore()
	h := NewValuesHandler(st, 0, slog.Default())
	mux := http.NewServeMux()
	h.Register(mux)

	defID := uuid.New().String()

	vr := &store.ValuesRevision{
		ID:                  uuid.New().String(),
		ReleaseDefinitionID: defID,
		Revision:            1,
		Status:              store.ValuesStatusDraft,
		Values:              []byte(`{"key":"value"}`),
		Digest:              "abc123",
		ParentRevisionID:    "original-parent",
	}
	if err := st.Create(context.Background(), vr); err != nil {
		t.Fatal(err)
	}

	vr2 := *vr
	vr2.Status = store.ValuesStatusApproved
	err := st.Update(context.Background(), &vr2, "wrong-parent")
	if err != store.ErrOptimisticLock {
		t.Errorf("expected ErrOptimisticLock, got %v", err)
	}
}

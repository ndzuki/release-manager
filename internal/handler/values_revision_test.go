package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/stretchr/testify/require"
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

func (s *stubValuesStore) Approve(_ context.Context, id string, expectedVersion int, approvedBy string) (approvedRevision, supersededRevision *store.ValuesRevision, err error) {
	vr, ok := s.items[id]
	if !ok {
		return nil, nil, store.ErrNotFound
	}
	if vr.Version != expectedVersion || vr.Status != store.ValuesStatusDraft {
		return nil, nil, store.ErrOptimisticLock
	}
	vr.Status = store.ValuesStatusApproved
	vr.Version++
	vr.ApprovedBy = approvedBy
	now := time.Now().UTC()
	vr.ApprovedAt = &now
	return vr, nil, nil
}

func (s *stubValuesStore) Reject(_ context.Context, id string, expectedVersion int, rejectedBy, reason string) (*store.ValuesRevision, error) {
	vr, ok := s.items[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	if vr.Version != expectedVersion || vr.Status != store.ValuesStatusDraft {
		return nil, store.ErrOptimisticLock
	}
	vr.Status = store.ValuesStatusRejected
	vr.Version++
	vr.RejectedBy = rejectedBy
	vr.RejectionReason = reason
	return vr, nil
}

func newTestHandler() *ValuesHandler {
	return newValuesHandlerFromValuesStore(newStubValuesStore(), 0, slog.Default())
}

type approvalFixture struct {
	handler      *ValuesHandler
	st           *sqlitestore.Store
	definitionID string
	customerID   string
	organization string
	creatorID    string
	approverID   string
	revisionID   string
}

func newApprovalFixture(t *testing.T, role store.Role) approvalFixture {
	t.Helper()
	st := sqlitestore.OpenTest(t)
	ctx := context.Background()
	customerID := "customer-068"
	definitionID := "definition-068"
	organizationID := "org-068"
	creatorID := "creator-068"
	approverID := "approver-068"
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: customerID, Name: "Customer 068", Slug: "customer-068"}))
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: definitionID, Name: "definition-068", CustomerID: customerID, ClusterID: "cluster-068", ReleaseName: "release-068",
	}, nil))
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: organizationID, Name: "Organization 068"}))
	require.NoError(t, st.Users().Create(ctx, &store.User{ID: creatorID, Username: creatorID, PasswordHash: "hash"}))
	require.NoError(t, st.Users().Create(ctx, &store.User{ID: approverID, Username: approverID, PasswordHash: "hash"}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: organizationID, UserID: approverID, Role: role}))
	require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{ID: "binding-068", OrgID: organizationID, CustomerID: customerID}))

	revision := &store.ValuesRevision{
		ID: "revision-068", ReleaseDefinitionID: definitionID, Revision: 1, Version: 1,
		Status: store.ValuesStatusDraft, Values: []byte(`{"key":"value"}`), Digest: "sha256:revision-068", CreatedBy: creatorID,
	}
	require.NoError(t, st.Values().Create(ctx, revision))
	h := NewValuesHandler(st, 0, slog.Default())
	return approvalFixture{handler: h, st: st, definitionID: definitionID, customerID: customerID, organization: organizationID, creatorID: creatorID, approverID: approverID, revisionID: revision.ID}
}

func approvalRequestBody(t *testing.T, actor store.ActorContext, reason string) *bytes.Reader {
	t.Helper()
	body := map[string]any{"expected_version": 1, "actor": actor, "comment": reason, "reason": reason}
	return bytes.NewReader(mustMarshal(t, body))
}

func serveApprovalRequest(t *testing.T, h *ValuesHandler, path string, body *bytes.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	h.Register(mux)
	mux.ServeHTTP(rec, req)
	return rec
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

func TestValuesApprovalWorkflow(t *testing.T) {
	tests := []struct {
		name     string
		role     store.Role
		actor    func(approvalFixture) store.ActorContext
		action   string
		expected int
		error    string
	}{
		{
			name: "self approval forbidden",
			role: store.RoleReleaseAdmin,
			actor: func(f approvalFixture) store.ActorContext {
				return store.ActorContext{UserID: f.creatorID, Organization: f.organization}
			},
			action: "approve", expected: http.StatusForbidden, error: errSelfApproval.Error(),
		},
		{
			name: "deployer not authorized",
			role: store.RoleDeployer,
			actor: func(f approvalFixture) store.ActorContext {
				return store.ActorContext{UserID: f.approverID, Organization: f.organization}
			},
			action: "approve", expected: http.StatusForbidden, error: errNotAuthorized.Error(),
		},
		{
			name: "cross organization not authorized",
			role: store.RoleReleaseAdmin,
			actor: func(f approvalFixture) store.ActorContext {
				return store.ActorContext{UserID: f.approverID, Organization: "other-org"}
			},
			action: "approve", expected: http.StatusForbidden, error: errNotAuthorized.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newApprovalFixture(t, tt.role)
			actor := tt.actor(fixture)
			response := serveApprovalRequest(t, fixture.handler, "/api/v1/values-revisions/"+fixture.revisionID+"/"+tt.action, approvalRequestBody(t, actor, "changes"))
			require.Equal(t, tt.expected, response.Code, response.Body.String())
			require.JSONEq(t, `{"error":"`+tt.error+`"}`, response.Body.String())
		})
	}
}

func TestValuesApprovalWorkflow_RejectAndRecreateParent(t *testing.T) {
	fixture := newApprovalFixture(t, store.RoleReleaseAdmin)
	actor := store.ActorContext{UserID: fixture.approverID, Organization: fixture.organization}
	response := serveApprovalRequest(t, fixture.handler, "/api/v1/values-revisions/"+fixture.revisionID+"/reject", approvalRequestBody(t, actor, "needs changes"))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	rejected, err := fixture.st.Values().Get(context.Background(), fixture.revisionID)
	require.NoError(t, err)
	require.Equal(t, store.ValuesStatusRejected, rejected.Status)
	require.Equal(t, "needs changes", rejected.RejectionReason)

	createBody := map[string]any{
		"release_definition_id": fixture.definitionID,
		"parent_revision_id":    rejected.ID,
		"values":                "key: revised",
		"actor":                 store.ActorContext{UserID: fixture.creatorID, Organization: fixture.organization},
	}
	createReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/api/v1/values-revisions",
		bytes.NewReader(mustMarshal(t, createBody)),
	)
	createRec := httptest.NewRecorder()
	mux := http.NewServeMux()
	fixture.handler.Register(mux)
	mux.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code, createRec.Body.String())

	created := mustUnmarshal[valuesResponse](t, createRec.Body.Bytes())
	persisted, err := fixture.st.Values().Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, store.ValuesStatusDraft, persisted.Status)
	require.Equal(t, rejected.ID, persisted.ParentRevisionID)
}

func TestValuesApprovalWorkflow_ConcurrentVersionConflict(t *testing.T) {
	fixture := newApprovalFixture(t, store.RoleReleaseAdmin)
	actor := store.ActorContext{UserID: fixture.approverID, Organization: fixture.organization}
	first := serveApprovalRequest(t, fixture.handler, "/api/v1/values-revisions/"+fixture.revisionID+"/approve", approvalRequestBody(t, actor, ""))
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	second := serveApprovalRequest(t, fixture.handler, "/api/v1/values-revisions/"+fixture.revisionID+"/approve", approvalRequestBody(t, actor, ""))
	require.Equal(t, http.StatusConflict, second.Code, second.Body.String())
	require.JSONEq(t, `{"error":"optimistic_lock_conflict"}`, second.Body.String())
}

func TestValuesApprovalWorkflow_AuditEvent(t *testing.T) {
	fixture := newApprovalFixture(t, store.RoleReleaseAdmin)
	actor := store.ActorContext{UserID: fixture.approverID, Organization: fixture.organization}
	response := serveApprovalRequest(t, fixture.handler, "/api/v1/values-revisions/"+fixture.revisionID+"/approve", approvalRequestBody(t, actor, "approved"))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	events, err := fixture.st.AuditEvents().ListByResource(context.Background(), "values_revision", fixture.revisionID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, store.AuditActorUser, events[0].ActorKind)
	require.Equal(t, fixture.approverID, events[0].ActorID)
	require.Equal(t, fixture.organization, events[0].OrganizationID)
	require.Equal(t, string(store.RoleReleaseAdmin), events[0].Role)
	require.Equal(t, "values_revision", events[0].ResourceType)
	require.Equal(t, fixture.revisionID, events[0].ResourceID)
	require.Equal(t, "approved", events[0].Action)
	require.Equal(t, "succeeded", events[0].Status)
	require.Equal(t, "approved", events[0].ChangeSummary)
}

func TestRejectValuesRevision_RequiresReason(t *testing.T) {
	fixture := newApprovalFixture(t, store.RoleReleaseAdmin)
	actor := store.ActorContext{UserID: fixture.approverID, Organization: fixture.organization}
	response := serveApprovalRequest(t, fixture.handler, "/api/v1/values-revisions/"+fixture.revisionID+"/reject", approvalRequestBody(t, actor, ""))
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.JSONEq(t, `{"error":"reason is required"}`, response.Body.String())
}

func TestValuesApprovalWorkflow_TerminalStateErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     store.ValuesStatus
		wantStatus int
		wantError  string
	}{
		{name: "approved revision", status: store.ValuesStatusApproved, wantStatus: http.StatusBadRequest, wantError: errAlreadyApproved.Error()},
		{name: "rejected revision", status: store.ValuesStatusRejected, wantStatus: http.StatusBadRequest, wantError: errAlreadyRejected.Error()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newApprovalFixture(t, store.RoleReleaseAdmin)
			revision, err := fixture.st.Values().Get(context.Background(), fixture.revisionID)
			require.NoError(t, err)
			revision.Status = tt.status
			require.NoError(t, fixture.st.Values().Update(context.Background(), revision, revision.ParentRevisionID))

			actor := store.ActorContext{UserID: fixture.approverID, Organization: fixture.organization}
			response := serveApprovalRequest(t, fixture.handler, "/api/v1/values-revisions/"+fixture.revisionID+"/approve", approvalRequestBody(t, actor, ""))
			require.Equal(t, tt.wantStatus, response.Code, response.Body.String())
			require.JSONEq(t, `{"error":"`+tt.wantError+`"}`, response.Body.String())
		})
	}
}

func TestValuesApprovalWorkflow_SupersedesPreviousApproved(t *testing.T) {
	fixture := newApprovalFixture(t, store.RoleReleaseAdmin)
	ctx := context.Background()
	previous := &store.ValuesRevision{
		ID: "revision-068-previous", ReleaseDefinitionID: fixture.definitionID, Revision: 0, Version: 1,
		Status: store.ValuesStatusApproved, Values: []byte(`{"key":"old"}`), Digest: "sha256:old", CreatedBy: fixture.creatorID,
	}
	require.NoError(t, fixture.st.Values().Create(ctx, previous))
	actor := store.ActorContext{UserID: fixture.approverID, Organization: fixture.organization}
	response := serveApprovalRequest(t, fixture.handler, "/api/v1/values-revisions/"+fixture.revisionID+"/approve", approvalRequestBody(t, actor, ""))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	result := mustUnmarshal[approvalResponse](t, response.Body.Bytes())
	require.Equal(t, previous.ID, result.PreviousApprovedSuperseded)
	persisted, err := fixture.st.Values().Get(ctx, previous.ID)
	require.NoError(t, err)
	require.Equal(t, store.ValuesStatusSuperseded, persisted.Status)
}

func TestUpdate_OptimisticLock(t *testing.T) {
	st := newStubValuesStore()
	defID := uuid.New().String()
	vr := &store.ValuesRevision{ID: uuid.New().String(), ReleaseDefinitionID: defID, Revision: 1, Status: store.ValuesStatusDraft, Values: []byte(`{"key":"value"}`), Digest: "abc123", ParentRevisionID: "original-parent"}
	require.NoError(t, st.Create(context.Background(), vr))
	vr2 := *vr
	vr2.Status = store.ValuesStatusApproved
	err := st.Update(context.Background(), &vr2, "wrong-parent")
	require.ErrorIs(t, err, store.ErrOptimisticLock)
}

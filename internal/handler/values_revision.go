// Package handler provides HTTP handlers for the release-manager API.
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/values"
)

// DefaultMaxValuesSize is the default input size limit for values documents (1 MiB).
const DefaultMaxValuesSize = 1 << 20

// ValuesHandler handles CRUD and approval for values revisions.
type ValuesHandler struct {
	values      store.ValuesStore
	definitions store.DefinitionStore
	customers   store.CustomerStore
	members     store.OrganizationMemberStore
	bindings    store.BindingStore
	auditEvents store.AuditEventStore
	maxSize     int64
	logger      *slog.Logger
}

// NewValuesHandler creates a ValuesHandler with the complete persistence store.
func NewValuesHandler(st store.Store, maxSize int64, logger *slog.Logger) *ValuesHandler {
	return newValuesHandler(
		st.Values(), st.Definitions(), st.Customers(), st.OrgMembers(), st.Bindings(), st.AuditEvents(),
		maxSize, logger,
	)
}

// newValuesHandlerFromValuesStore keeps unit tests independent from the aggregate store.
func newValuesHandlerFromValuesStore(valuesStore store.ValuesStore, maxSize int64, logger *slog.Logger) *ValuesHandler {
	return newValuesHandler(valuesStore, nil, nil, nil, nil, nil, maxSize, logger)
}

func newValuesHandler(
	valuesStore store.ValuesStore,
	definitions store.DefinitionStore,
	customers store.CustomerStore,
	members store.OrganizationMemberStore,
	bindings store.BindingStore,
	auditEvents store.AuditEventStore,
	maxSize int64,
	logger *slog.Logger,
) *ValuesHandler {
	if maxSize <= 0 {
		maxSize = DefaultMaxValuesSize
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ValuesHandler{
		values: valuesStore, definitions: definitions, customers: customers,
		members: members, bindings: bindings, auditEvents: auditEvents,
		maxSize: maxSize, logger: logger,
	}
}

// createRequest is the JSON body for POST /api/v1/values-revisions.
type createRequest struct {
	ReleaseDefinitionID string             `json:"release_definition_id"`
	ParentRevisionID    string             `json:"parent_revision_id,omitempty"`
	Values              string             `json:"values"`
	Actor               store.ActorContext `json:"actor"`
}

type approvalRequest struct {
	ExpectedVersion int                `json:"expected_version"`
	Comment         string             `json:"comment,omitempty"`
	Actor           store.ActorContext `json:"actor"`
}

type rejectionRequest struct {
	ExpectedVersion int                `json:"expected_version"`
	Reason          string             `json:"reason"`
	Actor           store.ActorContext `json:"actor"`
}

type approvalResponse struct {
	RevisionID                 string `json:"revision_id"`
	NewState                   string `json:"new_state"`
	PreviousApprovedSuperseded string `json:"previous_approved_superseded,omitempty"`
	ApprovedAt                 string `json:"approved_at"`
}

var (
	errRevisionNotFound   = errors.New("revision_not_found")
	errAlreadyApproved    = errors.New("already_approved")
	errAlreadyRejected    = errors.New("already_rejected")
	errSelfApproval       = errors.New("self_approval_forbidden")
	errNotAuthorized      = errors.New("not_authorized")
	errOptimisticConflict = errors.New("optimistic_lock_conflict")
)

// Create handles POST /api/v1/values-revisions.
func (h *ValuesHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid request body: "+err.Error()))
		return
	}

	if req.ReleaseDefinitionID == "" {
		writeJSON(w, http.StatusBadRequest, errResp("release_definition_id is required"))
		return
	}

	// Validate, canonicalize, and compute digest.
	result, err := values.Validate([]byte(req.Values), h.maxSize)
	if err != nil {
		code := http.StatusBadRequest
		switch {
		case err == values.ErrSecretLiteral:
			// stays 400, specific message
		case err == values.ErrSizeExceeded:
		case values.IsYAMLError(err):
		default:
			h.logger.Error("values validate", "error", err)
			writeJSON(w, http.StatusInternalServerError, errResp("validation failed"))
			return
		}
		writeJSON(w, code, errResp(err.Error()))
		return
	}

	// Get next revision number.
	nextRev, err := h.values.GetNextRevisionNumber(r.Context(), req.ReleaseDefinitionID)
	if err != nil {
		h.logger.Error("get next revision", "error", err)
		writeJSON(w, http.StatusInternalServerError, errResp("failed to get next revision number"))
		return
	}

	vr := &store.ValuesRevision{
		ID:                  uuid.New().String(),
		ReleaseDefinitionID: req.ReleaseDefinitionID,
		Revision:            nextRev,
		Status:              store.ValuesStatusDraft,
		Values:              result.Canonical,
		Digest:              result.Digest,
		ParentRevisionID:    req.ParentRevisionID,
		Version:             1,
		CreatedBy:           req.Actor.UserID,
		CreatedAt:           time.Now().UTC(),
		UpdatedAt:           time.Now().UTC(),
	}

	if err := h.values.Create(r.Context(), vr); err != nil {
		h.logger.Error("create values revision", "error", err)
		writeJSON(w, http.StatusInternalServerError, errResp("failed to create values revision"))
		return
	}

	writeJSON(w, http.StatusCreated, toResponse(vr))
}

// Get handles GET /api/v1/values-revisions/{id}.
func (h *ValuesHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errResp("id is required"))
		return
	}

	vr, err := h.values.Get(r.Context(), id)
	if err != nil {
		if err == store.ErrNotFound {
			writeJSON(w, http.StatusNotFound, errResp("not found"))
			return
		}
		h.logger.Error("get values revision", "error", err)
		writeJSON(w, http.StatusInternalServerError, errResp("failed to get values revision"))
		return
	}

	writeJSON(w, http.StatusOK, toResponse(vr))
}

// List handles GET /api/v1/values-revisions?definition_id=X.
func (h *ValuesHandler) List(w http.ResponseWriter, r *http.Request) {
	defID := r.URL.Query().Get("definition_id")
	if defID == "" {
		writeJSON(w, http.StatusBadRequest, errResp("definition_id query parameter is required"))
		return
	}

	revs, err := h.values.List(r.Context(), defID)
	if err != nil {
		h.logger.Error("list values revisions", "error", err)
		writeJSON(w, http.StatusInternalServerError, errResp("failed to list values revisions"))
		return
	}

	items := make([]valuesResponse, 0, len(revs))
	for _, vr := range revs {
		items = append(items, toResponse(vr))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"revisions": items,
	})
}

// Approve handles POST /api/v1/values-revisions/{id}/approve.
func (h *ValuesHandler) Approve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errResp("revision_id is required"))
		return
	}

	var req approvalRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp(err.Error()))
		return
	}
	revision, role, err := h.authorizeApproval(r, id, req.Actor)
	if err != nil {
		h.writeApprovalError(w, err)
		return
	}
	if err := validateApprovalState(revision, req.ExpectedVersion); err != nil {
		h.writeApprovalError(w, err)
		return
	}

	approved, superseded, err := h.values.Approve(r.Context(), id, req.ExpectedVersion, req.Actor.UserID)
	if err != nil {
		h.writeApprovalStoreError(w, "approve", err)
		return
	}
	if err := h.writeAuditEvent(r, req.Actor, role, id, "approved", req.Comment); err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("audit_event_failed"))
		return
	}

	resp := approvalResponse{
		RevisionID: id,
		NewState:   string(approved.Status),
		ApprovedAt: approved.ApprovedAt.UTC().Format(time.RFC3339),
	}
	if superseded != nil {
		resp.PreviousApprovedSuperseded = superseded.ID
	}
	writeJSON(w, http.StatusOK, resp)
}

// Reject handles POST /api/v1/values-revisions/{id}/reject.
func (h *ValuesHandler) Reject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errResp("revision_id is required"))
		return
	}

	var req rejectionRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp(err.Error()))
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		writeJSON(w, http.StatusBadRequest, errResp("reason is required"))
		return
	}
	revision, role, err := h.authorizeApproval(r, id, req.Actor)
	if err != nil {
		h.writeApprovalError(w, err)
		return
	}
	if err := validateApprovalState(revision, req.ExpectedVersion); err != nil {
		h.writeApprovalError(w, err)
		return
	}

	rejected, err := h.values.Reject(r.Context(), id, req.ExpectedVersion, req.Actor.UserID, req.Reason)
	if err != nil {
		h.writeApprovalStoreError(w, "reject", err)
		return
	}
	if err := h.writeAuditEvent(r, req.Actor, role, id, "rejected", req.Reason); err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("audit_event_failed"))
		return
	}

	writeJSON(w, http.StatusOK, approvalResponse{
		RevisionID: id,
		NewState:   string(rejected.Status),
	})
}

func (h *ValuesHandler) authorizeApproval(r *http.Request, revisionID string, actor store.ActorContext) (*store.ValuesRevision, store.Role, error) {
	revision, err := h.values.Get(r.Context(), revisionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, "", errRevisionNotFound
		}
		return nil, "", fmt.Errorf("get values revision: %w", err)
	}
	if actor.UserID == "" || actor.Organization == "" {
		return nil, "", errNotAuthorized
	}
	if revision.CreatedBy == actor.UserID {
		return nil, "", errSelfApproval
	}

	definition, err := h.definitions.Get(r.Context(), revision.ReleaseDefinitionID)
	if err != nil {
		return nil, "", fmt.Errorf("get release definition: %w", err)
	}
	if _, err := h.customers.Get(r.Context(), definition.CustomerID); err != nil {
		return nil, "", fmt.Errorf("get customer: %w", err)
	}
	binding, err := h.bindings.GetByOrgAndCustomer(r.Context(), actor.Organization, definition.CustomerID)
	if err != nil || binding.Status != store.BindingActive {
		return nil, "", errNotAuthorized
	}
	member, err := h.members.Get(r.Context(), actor.Organization, actor.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, "", errNotAuthorized
		}
		return nil, "", fmt.Errorf("get organization member: %w", err)
	}
	if member.Role != store.RoleReleaseAdmin && member.Role != store.RolePlatformAdmin {
		return nil, "", errNotAuthorized
	}
	return revision, member.Role, nil
}

func validateApprovalState(revision *store.ValuesRevision, expectedVersion int) error {
	if revision.Version != expectedVersion {
		return errOptimisticConflict
	}
	switch revision.Status {
	case store.ValuesStatusDraft:
		return nil
	case store.ValuesStatusApproved, store.ValuesStatusSuperseded:
		return errAlreadyApproved
	case store.ValuesStatusRejected:
		return errAlreadyRejected
	default:
		return fmt.Errorf("unsupported values status %q", revision.Status)
	}
}

func (h *ValuesHandler) writeAuditEvent(r *http.Request, actorContext store.ActorContext, role store.Role, revisionID, action, summary string) error {
	event := audit.NewEvent(
		store.AuditActorUser,
		actorContext.UserID,
		actorContext.Organization,
		string(role),
		"values_revision",
		revisionID,
		action,
		"succeeded",
		summary,
		nil,
	)
	event.ID = uuid.New().String()
	if err := h.auditEvents.Create(r.Context(), event); err != nil {
		h.logger.Error("write values approval audit event", "action", action, "revision_id", revisionID, "error", err)
		return err
	}
	return nil
}

func (h *ValuesHandler) writeApprovalStoreError(w http.ResponseWriter, action string, err error) {
	if errors.Is(err, store.ErrOptimisticLock) {
		h.writeApprovalError(w, errOptimisticConflict)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		h.writeApprovalError(w, errRevisionNotFound)
		return
	}
	h.logger.Error(action+" values revision", "error", err)
	writeJSON(w, http.StatusInternalServerError, errResp("failed to "+action+" values revision"))
}

func (h *ValuesHandler) writeApprovalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errRevisionNotFound):
		writeJSON(w, http.StatusNotFound, errResp(errRevisionNotFound.Error()))
	case errors.Is(err, errSelfApproval), errors.Is(err, errNotAuthorized):
		writeJSON(w, http.StatusForbidden, errResp(err.Error()))
	case errors.Is(err, errOptimisticConflict):
		writeJSON(w, http.StatusConflict, errResp(errOptimisticConflict.Error()))
	case errors.Is(err, errAlreadyApproved), errors.Is(err, errAlreadyRejected):
		writeJSON(w, http.StatusBadRequest, errResp(err.Error()))
	default:
		h.logger.Error("values approval request failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errResp("approval_failed"))
	}
}

func decodeJSONBody(r *http.Request, target any) error {
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	return nil
}

// Register mounts all values revision routes on the given mux.
func (h *ValuesHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/values-revisions", h.Create)
	mux.HandleFunc("GET /api/v1/values-revisions/{id}", h.Get)
	mux.HandleFunc("GET /api/v1/values-revisions", h.List)
	mux.HandleFunc("POST /api/v1/values-revisions/{id}/approve", h.Approve)
	mux.HandleFunc("POST /api/v1/values-revisions/{id}/reject", h.Reject)
}

// --- response helpers ---

type valuesResponse struct {
	ID                  string `json:"id"`
	ReleaseDefinitionID string `json:"release_definition_id"`
	Revision            int    `json:"revision"`
	Status              string `json:"status"`
	Values              string `json:"values,omitempty"`
	Digest              string `json:"digest,omitempty"`
	ParentRevisionID    string `json:"parent_revision_id,omitempty"`
	RevisionNumber      int    `json:"revision_number,omitempty"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func errResp(msg string) errorResponse {
	return errorResponse{Error: msg}
}

func toResponse(vr *store.ValuesRevision) valuesResponse {
	return valuesResponse{
		ID:                  vr.ID,
		ReleaseDefinitionID: vr.ReleaseDefinitionID,
		Revision:            vr.Revision,
		Status:              string(vr.Status),
		Digest:              vr.Digest,
		ParentRevisionID:    vr.ParentRevisionID,
		RevisionNumber:      vr.Revision,
		CreatedAt:           vr.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:           vr.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json", "error", err)
	}
}

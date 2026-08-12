// Package handler provides HTTP handlers for the release-manager API.
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/contracts"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/values"
)

// DefaultMaxValuesSize is the default input size limit for values documents (1 MiB).
const DefaultMaxValuesSize = 1 << 20

// ValuesHandler handles ValuesRevision CRUD.
type ValuesHandler struct {
	values  store.ValuesStore
	maxSize int64
	logger  *slog.Logger
}

// NewValuesHandler creates a ValuesHandler with the complete persistence store.
func NewValuesHandler(st store.Store, maxSize int64, logger *slog.Logger) *ValuesHandler {
	return newValuesHandlerFromValuesStore(st.Values(), maxSize, logger)
}

// newValuesHandlerFromValuesStore keeps unit tests independent from the aggregate store.
func newValuesHandlerFromValuesStore(valuesStore store.ValuesStore, maxSize int64, logger *slog.Logger) *ValuesHandler {
	if maxSize <= 0 {
		maxSize = DefaultMaxValuesSize
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ValuesHandler{values: valuesStore, maxSize: maxSize, logger: logger}
}

// createRequest is the JSON body for POST /api/v1/values-revisions.
type createRequest struct {
	ReleaseDefinitionID string             `json:"release_definition_id"`
	ParentRevisionID    string             `json:"parent_revision_id,omitempty"`
	Values              string             `json:"values"`
	Actor               store.ActorContext `json:"actor"`
}

// Create handles POST /api/v1/values-revisions.
func (h *ValuesHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Error("decode create values revision request", "error", err)
		h.writeError(w, r, http.StatusBadRequest, connect.CodeInvalidArgument.String(), "invalid request body")
		return
	}

	if req.ReleaseDefinitionID == "" {
		h.writeError(w, r, http.StatusBadRequest, connect.CodeInvalidArgument.String(), "release_definition_id is required")
		return
	}

	// Validate, canonicalize, and compute digest.
	result, err := values.Validate([]byte(req.Values), h.maxSize)
	if err != nil {
		switch {
		case err == values.ErrSecretLiteral:
			// 稳定业务错误：400 + 具体 message。
			h.writeError(w, r, http.StatusBadRequest, connect.CodeInvalidArgument.String(), err.Error())
		case err == values.ErrSizeExceeded:
			h.writeError(w, r, http.StatusBadRequest, connect.CodeInvalidArgument.String(), err.Error())
		case values.IsYAMLError(err):
			// YAML 解析细节（行号等）属输入诊断，仅日志。
			h.logger.Debug("values validate yaml", "error", err)
			h.writeError(w, r, http.StatusBadRequest, connect.CodeInvalidArgument.String(), values.ErrInvalidYAML.Error())
		default:
			h.writeInternalError(w, r, "values validate", err)
		}
		return
	}

	// Get next revision number.
	nextRev, err := h.values.GetNextRevisionNumber(r.Context(), req.ReleaseDefinitionID)
	if err != nil {
		h.writeInternalError(w, r, "get next revision", err)
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
		StateVersion:        1,
		CreatedByUserID:     req.Actor.UserID,
		CreatedAt:           time.Now().UTC(),
		UpdatedAt:           time.Now().UTC(),
	}

	if err := h.values.Create(r.Context(), vr); err != nil {
		h.writeInternalError(w, r, "create values revision", err)
		return
	}

	writeJSON(w, http.StatusCreated, toResponse(vr))
}

// Get handles GET /api/v1/values-revisions/{id}.
func (h *ValuesHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		h.writeError(w, r, http.StatusBadRequest, connect.CodeInvalidArgument.String(), "id is required")
		return
	}

	vr, err := h.values.Get(r.Context(), id)
	if err != nil {
		if err == store.ErrNotFound {
			h.writeError(w, r, http.StatusNotFound, connect.CodeNotFound.String(), "not found")
			return
		}
		h.writeInternalError(w, r, "get values revision", err)
		return
	}

	writeJSON(w, http.StatusOK, toResponse(vr))
}

// List handles GET /api/v1/values-revisions?definition_id=X.
func (h *ValuesHandler) List(w http.ResponseWriter, r *http.Request) {
	defID := r.URL.Query().Get("definition_id")
	if defID == "" {
		h.writeError(w, r, http.StatusBadRequest, connect.CodeInvalidArgument.String(), "definition_id query parameter is required")
		return
	}

	revs, err := h.values.List(r.Context(), defID)
	if err != nil {
		h.writeInternalError(w, r, "list values revisions", err)
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

// Register mounts ValuesRevision CRUD routes on the given mux.
func (h *ValuesHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/values-revisions", h.Create)
	mux.HandleFunc("GET /api/v1/values-revisions/{id}", h.Get)
	mux.HandleFunc("GET /api/v1/values-revisions", h.List)
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

// errorResponse is the REQ-010 output-contract error envelope:
// {code, message, request_id, field_errors?}.
type errorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// writeError writes a stable error envelope per the REQ-010 output contract.
// msg MUST be client-safe; internal detail goes to logs only.
func (h *ValuesHandler) writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	rid := requestID(r.Context())
	w.Header().Set(contracts.RequestIDHeader, rid)
	writeJSON(w, status, errorResponse{Code: code, Message: msg, RequestID: rid})
}

// writeInternalError logs the full detail and responds with the generic
// internal-error envelope (AC-010-04): no SQL/stack/credential text reaches
// the client.
func (h *ValuesHandler) writeInternalError(w http.ResponseWriter, r *http.Request, op string, err error) {
	rid := contracts.RequestID(r.Context())
	h.logger.Error(op, "request_id", rid, "error", err)
	h.writeError(w, r, http.StatusInternalServerError, connect.CodeInternal.String(), "internal error")
}

// requestID returns the request_id from ctx, generating a UUID when the
// request did not carry one (the values routes are plain HTTP handlers and
// are not wrapped by the RequestID interceptor chain).
func requestID(ctx context.Context) string {
	if rid := contracts.RequestID(ctx); rid != "" {
		return rid
	}
	return uuid.NewString()
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

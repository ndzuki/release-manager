// Package handler provides HTTP handlers for the release-manager API.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
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
		StateVersion:        1,
		CreatedByUserID:     req.Actor.UserID,
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

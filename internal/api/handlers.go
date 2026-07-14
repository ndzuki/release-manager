package api

import (
	"net/http"

	"github.com/go-logr/logr"
)

// ReleaseHandler handles release record queries.
type ReleaseHandler struct {
	store ReleaseStore
	log   logr.Logger
}

// NewReleaseHandler creates a new ReleaseHandler.
func NewReleaseHandler(store ReleaseStore, log logr.Logger) *ReleaseHandler {
	return &ReleaseHandler{store: store, log: log.WithName("releases")}
}

// ServeHTTP handles GET /api/v1/releases/{requestId}.
func (h *ReleaseHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	requestID := extractReleaseRequestID(r.URL.Path)
	if requestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing request ID"})
		return
	}

	records, err := h.store.ListReleaseRecords(requestID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if records == nil {
		records = []ReleaseRecord{}
	}
	writeJSON(w, http.StatusOK, records)
}

func extractReleaseRequestID(path string) string {
	// /api/v1/releases/{requestId}
	const prefix = "/api/v1/releases/"
	if len(path) > len(prefix) {
		return path[len(prefix):]
	}
	return ""
}

// AuditHandler handles audit log queries.
type AuditHandler struct {
	store AuditStore
	log   logr.Logger
}

// NewAuditHandler creates a new AuditHandler.
func NewAuditHandler(store AuditStore, log logr.Logger) *AuditHandler {
	return &AuditHandler{store: store, log: log.WithName("audit")}
}

// ServeHTTP handles GET /api/v1/audit-logs.
func (h *AuditHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	q := r.URL.Query()
	limit := 50
	if l := q.Get("limit"); l != "" {
		if v := parseInt(l); v > 0 && v <= 200 {
			limit = v
		}
	}
	offset := 0
	if o := q.Get("offset"); o != "" {
		offset = parseInt(o)
	}

	filter := AuditLogFilter{
		UserID:     q.Get("user_id"),
		Resource:   q.Get("resource"),
		ResourceID: q.Get("resource_id"),
		Method:     q.Get("method"),
		Path:       q.Get("path"),
		Since:      q.Get("since"),
		Until:      q.Get("until"),
		Limit:      limit,
		Offset:     offset,
	}

	entries, err := h.store.ListAuditLogs(filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if entries == nil {
		entries = []AuditLogEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

func parseInt(s string) int {
	var v int
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		v = v*10 + int(c-'0')
	}
	return v
}

// Package api implements the REST management API for release-manager.
//
// Provides customer management, release tracking, audit logs, and dashboard.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-logr/logr"
)

// CustomerStore is the subset of Store used by customer management handlers.
type CustomerStore interface {
	ListCustomers(enabledOnly bool) ([]Customer, error)
	GetCustomer(id string) (*Customer, error)
	CreateCustomer(c Customer) (*Customer, error)
	UpdateCustomer(c Customer) (*Customer, error)
	DeleteCustomer(id string) error
}

// ReleaseStore is the subset of Store used by release record handlers.
type ReleaseStore interface {
	CreateReleaseRecord(r ReleaseRecord) error
	ListReleaseRecords(requestID string) ([]ReleaseRecord, error)
}

// AuditStore is the subset of Store used by audit log handlers.
type AuditStore interface {
	ListAuditLogs(filter AuditLogFilter) ([]AuditLogEntry, error)
}

// Customer represents a private-deployment customer.
type Customer struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	OperatorEndpoint string            `json:"operator_endpoint"`
	CertFingerprint  string            `json:"cert_fingerprint"`
	Enabled          bool              `json:"enabled"`
	Labels           map[string]string `json:"labels,omitempty"`
	CreatedAt        string            `json:"created_at"`
	UpdatedAt        string            `json:"updated_at"`
}

// CreateCustomerRequest is the request body for creating a customer.
type CreateCustomerRequest struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	OperatorEndpoint string            `json:"operator_endpoint"`
	CertFingerprint  string            `json:"cert_fingerprint"`
	Enabled          bool              `json:"enabled"`
	Labels           map[string]string `json:"labels,omitempty"`
}

// UpdateCustomerRequest is the request body for updating a customer (all fields optional).
type UpdateCustomerRequest struct {
	Name             *string           `json:"name,omitempty"`
	OperatorEndpoint *string           `json:"operator_endpoint,omitempty"`
	CertFingerprint  *string           `json:"cert_fingerprint,omitempty"`
	Enabled          *bool             `json:"enabled,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
}

// ReleaseRecord represents a release operation record.
type ReleaseRecord struct {
	ID           string `json:"id"`
	RequestID    string `json:"request_id"`
	CustomerID   string `json:"customer_id"`
	ChartName    string `json:"chart_name"`
	ChartVersion string `json:"chart_version"`
	Status       string `json:"status"`
	ErrorMessage string `json:"error_message"`
	DurationSecs int64  `json:"duration_secs"`
	StartedAt    string `json:"started_at"`
	CompletedAt  string `json:"completed_at"`
}

// AuditLogEntry represents an API operation audit record.
type AuditLogEntry struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	StatusCode  int    `json:"status_code"`
	BodySnippet string `json:"body_snippet,omitempty"`
	Timestamp   string `json:"timestamp"`
}

// AuditLogFilter filters audit log queries.
type AuditLogFilter struct {
	UserID     string
	Resource   string
	ResourceID string
	Method     string
	Path       string
	Since      string
	Until      string
	Limit      int
	Offset     int
}

// CustomerHandler handles customer CRUD operations.
type CustomerHandler struct {
	store CustomerStore
	log   logr.Logger
}

// NewCustomerHandler creates a new CustomerHandler.
func NewCustomerHandler(store CustomerStore, log logr.Logger) *CustomerHandler {
	return &CustomerHandler{store: store, log: log.WithName("customers")}
}

// ServeHTTP routes customer requests.
func (h *CustomerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/api/v1/customers":
		h.handleCustomers(w, r)
	default:
		h.handleCustomer(w, r)
	}
}

// handleCustomers handles GET /api/v1/customers.
func (h *CustomerHandler) handleCustomers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	enabledOnly := r.URL.Query().Get("enabled") == "true"
	customers, err := h.store.ListCustomers(enabledOnly)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if customers == nil {
		customers = []Customer{}
	}
	writeJSON(w, http.StatusOK, customers)
}

// handleCustomer handles GET/PUT/DELETE /api/v1/customers/{id}.
func (h *CustomerHandler) handleCustomer(w http.ResponseWriter, r *http.Request) {
	id := extractCustomerID(r.URL.Path)

	switch r.Method {
	case http.MethodGet:
		c, err := h.store.GetCustomer(id)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "customer not found"})
			return
		}
		writeJSON(w, http.StatusOK, c)

	case http.MethodPut:
		var req UpdateCustomerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		c, err := h.store.GetCustomer(id)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "customer not found"})
			return
		}
		if req.Name != nil {
			c.Name = *req.Name
		}
		if req.OperatorEndpoint != nil {
			c.OperatorEndpoint = *req.OperatorEndpoint
		}
		if req.CertFingerprint != nil {
			c.CertFingerprint = *req.CertFingerprint
		}
		if req.Enabled != nil {
			c.Enabled = *req.Enabled
		}
		if req.Labels != nil {
			c.Labels = req.Labels
		}
		c, err = h.store.UpdateCustomer(*c)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, c)

	case http.MethodDelete:
		if err := h.store.DeleteCustomer(id); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func extractCustomerID(path string) string {
	// /api/v1/customers/{id}
	const prefix = "/api/v1/customers/"
	if len(path) > len(prefix) {
		return path[len(prefix):]
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data) //nolint:errcheck // best-effort
}

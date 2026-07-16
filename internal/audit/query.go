// Package audit provides audit event collection and persistence.
//
// BLOCKED: TASK-005 (RBAC) — REQ-029 audit query/export requires organization-scoped
// access control (REQ-027) not yet delivered. The interfaces below define the contract
// for when TASK-005 lands.
package audit

import (
	"context"

	"github.com/ndzuki/release-manager/internal/store"
)

// QueryFilter scopes audit event queries.
type QueryFilter struct {
	OrganizationID string
	ResourceType   string
	ResourceID     string
	ActorID        string
	Action         string
	Since          string // RFC3339
	Until          string // RFC3339
}

// QueryResult holds a cursor-paginated audit query result.
type QueryResult struct {
	Events     []*store.AuditEvent
	NextCursor string
	HasMore    bool
}

// Querier defines the audit query interface (REQ-029).
// BLOCKED: TASK-005 — requires organization-scoped access control (REQ-027).
type Querier interface {
	// Query returns cursor-paginated audit events scoped to the caller's organization.
	// The caller's organization is derived from auth context, not passed explicitly.
	Query(ctx context.Context, filter QueryFilter, cursor string, limit int) (*QueryResult, error)

	// Export returns a gzipped JSONL export of audit events for the given time range.
	// The caller's organization scope is enforced by auth context.
	Export(ctx context.Context, since, until string) ([]byte, error)
}

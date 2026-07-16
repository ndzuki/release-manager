// Package helmengine defines the Helm SDK adapter contract (REQ-041).
// It provides a stable interface for Helm operations that release-operator
// consumes, decoupled from the concrete Helm SDK implementation.
package helmengine

import (
	"context"
	"errors"
)

// Release represents the result of a Helm operation.
type Release struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Revision  int    `json:"revision"`
	Status    string `json:"status"`
	Chart     string `json:"chart"`
	// ManifestDigest is the SHA-256 of the rendered manifest (never includes Secret values).
	ManifestDigest string `json:"manifest_digest"`
	// Notes are the Helm chart NOTES.txt output (never includes Secret values).
	Notes string `json:"notes"`
}

// ReleaseListItem is a lightweight inventory entry for listing releases.
// It carries only metadata and a values digest — NEVER raw Secret values.
type ReleaseListItem struct {
	Namespace    string
	Name         string
	Chart        string
	ChartVersion string
	Revision     int
	Status       string
	ValuesDigest string // SHA-256 of canonical values, not the values themselves
}

// ReleaseHistoryEntry represents one entry in the release history.
type ReleaseHistoryEntry struct {
	Revision    int    `json:"revision"`
	Status      string `json:"status"`
	Chart       string `json:"chart"`
	Description string `json:"description"`
}

// Sentinel errors returned by HelmEngine operations.
var (
	ErrNotFound      = errors.New("helm: release not found")
	ErrAlreadyExists = errors.New("helm: release already exists")
	ErrForbidden     = errors.New("helm: forbidden")
	ErrConflict      = errors.New("helm: conflict")
	ErrTimeout       = errors.New("helm: timeout")
	ErrCancelled     = errors.New("helm: cancelled")
	ErrRenderFailed  = errors.New("helm: render failed")
	ErrActionFailed  = errors.New("helm: action failed")
)

// Engine defines the contract for Helm SDK operations (REQ-041).
// Implementations MUST use the Helm Go SDK only (helm.sh/helm/v3/pkg/action);
// no os/exec or subprocess calls are permitted.
// Each operation receives its own action.Configuration; mutable action state
// MUST NOT be shared across concurrent operations.
type Engine interface {
	// Install installs a chart. Returns ErrAlreadyExists if the release exists.
	Install(ctx context.Context, opts InstallOptions) (*Release, error)

	// Upgrade upgrades an existing release. Returns ErrNotFound if not found.
	Upgrade(ctx context.Context, opts UpgradeOptions) (*Release, error)

	// Rollback rolls back a release to a target revision. Returns ErrNotFound if not found.
	Rollback(ctx context.Context, opts RollbackOptions) (*Release, error)

	// Status returns the current status of a release.
	Status(ctx context.Context, opts StatusOptions) (*Release, error)

	// History returns the revision history for a release.
	History(ctx context.Context, opts HistoryOptions) ([]ReleaseHistoryEntry, error)

	// GetValues returns the current values for a release.
	GetValues(ctx context.Context, opts GetValuesOptions) (map[string]interface{}, error)

	// List returns all releases in a namespace. Used for inventory sync.
	// The returned ReleaseListItem MUST NOT contain raw Secret values.
	List(ctx context.Context, namespace string) ([]*ReleaseListItem, error)
}

// InstallOptions holds parameters for Install.
type InstallOptions struct {
	Namespace   string
	ReleaseName string
	ChartPath   string
	Values      map[string]interface{}
}

// UpgradeOptions holds parameters for Upgrade.
type UpgradeOptions struct {
	Namespace        string
	ReleaseName      string
	ChartPath        string
	Values           map[string]interface{}
	ExpectedRevision int  // if > 0, must match current revision (AC-021-02)
	Atomic           bool // if true, rollback on failure (AC-021-04)
	Timeout          int  // seconds; 0 uses default
}

// RollbackOptions holds parameters for Rollback.
type RollbackOptions struct {
	Namespace      string
	ReleaseName    string
	TargetRevision int
}

// StatusOptions holds parameters for Status.
type StatusOptions struct {
	Namespace   string
	ReleaseName string
}

// HistoryOptions holds parameters for History.
type HistoryOptions struct {
	Namespace    string
	ReleaseName  string
	MaxRevisions int
}

// GetValuesOptions holds parameters for GetValues.
type GetValuesOptions struct {
	Namespace   string
	ReleaseName string
}

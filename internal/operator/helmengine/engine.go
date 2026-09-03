// Package helmengine defines the Helm SDK adapter contract (REQ-041).
// It provides a stable interface for Helm operations that release-operator
// consumes, decoupled from the concrete Helm SDK implementation.
package helmengine

import (
	"context"
	"errors"
	"time"
)

// WorkloadSummary identifies one four-GVR workload (Deployment/StatefulSet/
// DaemonSet/Job) contained in a release manifest. It is non-sensitive
// metadata only — never manifest bodies or Secret values (REQ-077 Q1).
type WorkloadSummary struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
}

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
	Notes                 string            `json:"notes"`
	Description           string            `json:"description,omitempty"`
	Labels                map[string]string `json:"labels,omitempty"`
	BundleDigest          string            `json:"bundle_digest,omitempty"`
	ChartDigest           string            `json:"chart_digest,omitempty"`
	EffectiveValuesDigest string            `json:"effective_values_digest,omitempty"`
	Provenance            string            `json:"provenance,omitempty"`
	// Workloads lists the four-GVR workload identities extracted from the
	// rendered manifest (REQ-077 Q1: observation input equals execution input).
	Workloads []WorkloadSummary `json:"workloads,omitempty"`
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
	ErrNotFound             = errors.New("helm: release not found")
	ErrAlreadyExists        = errors.New("helm: release already exists")
	ErrForbidden            = errors.New("helm: forbidden")
	ErrConflict             = errors.New("helm: conflict")
	ErrReleaseBusy          = errors.New("helm: release busy")
	ErrReleaseNotDeployed   = errors.New("helm: release not deployed")
	ErrTimeout              = errors.New("helm: timeout")
	ErrCancelled            = errors.New("helm: cancelled")
	ErrRenderFailed         = errors.New("helm: render failed")
	ErrActionFailed         = errors.New("helm: action failed")
	ErrRevisionNotFound     = errors.New("helm: target revision not found in history")
	ErrArtifactUnavailable  = errors.New("helm: historical artifact unavailable")
	ErrDigestMismatch       = errors.New("helm: digest mismatch")
	ErrSecretRefChanged     = errors.New("helm: secret ref changed")
	ErrRenderDrift          = errors.New("helm: render drift")
	ErrAtomicRollbackFailed = errors.New("helm: atomic rollback failed")
	ErrSchemaFailed         = errors.New("helm: schema validation failed")
)

// UpgradeOutcome describes what a failed Upgrade left behind (TASK-084
// AC-084-04). It is the authoritative post-failure signal captured in the
// same SDK call sequence — the agent maps it into UpgradeResult instead of
// guessing from revision equality (the D-109 fabrication source).
type UpgradeOutcome struct {
	// Attempted is the failed new revision; nil when the attempt produced no
	// release record (preparation failures).
	Attempted *Release
	// Active is the release that is active after the failure; nil when the
	// post-failure state could not be observed.
	Active *Release
	// Restored is true when the atomic rollback restored the pre-upgrade
	// revision (only then may the caller report rollback_succeeded).
	Restored bool
}

// OutcomeError decorates an Upgrade failure with the structured post-failure
// state. It preserves the wrapped error for errors.Is classification.
type OutcomeError struct {
	Err     error
	Outcome UpgradeOutcome
}

// NewOutcomeError builds an OutcomeError wrapping err.
func NewOutcomeError(err error, outcome UpgradeOutcome) error {
	if err == nil {
		return nil
	}
	return &OutcomeError{Err: err, Outcome: outcome}
}

func (e *OutcomeError) Error() string { return e.Err.Error() }
func (e *OutcomeError) Unwrap() error { return e.Err }

// OutcomeOf extracts the structured post-failure outcome from an Upgrade
// error. A non-decorated error yields the zero outcome (Restored=false),
// which callers MUST treat as "no authoritative rollback signal" — never as
// success.
func OutcomeOf(err error) UpgradeOutcome {
	var decorated interface{ UpgradeOutcome() UpgradeOutcome }
	if errors.As(err, &decorated) {
		return decorated.UpgradeOutcome()
	}
	return UpgradeOutcome{}
}

// UpgradeOutcome implements the OutcomeOf extraction interface.
func (e *OutcomeError) UpgradeOutcome() UpgradeOutcome { return e.Outcome }

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
	Namespace       string
	ReleaseName     string
	ChartPath       string
	ChartVersion    string
	Values          map[string]interface{}
	Atomic          bool          // rollback on failure
	CreateNamespace bool          // create namespace if missing
	Timeout         time.Duration // helm install timeout
	// PlainHTTP allows OCI chart pulls from a plain HTTP registry (dev
	// fixture only — the local registry is loopback/unauthenticated). Defaults
	// false so production OCI pulls stay HTTPS.
	PlainHTTP bool
}

// UpgradeOptions holds parameters for the Helm SDK Upgrade method.
type UpgradeOptions struct {
	Namespace              string
	ReleaseName            string
	ChartPath              string
	ChartVersion           string
	Values                 map[string]interface{}
	ExpectedRevision       int           // if > 0, must match current revision (AC-021-02)
	Atomic                 bool          // rollback on failure
	MaxHistory             int           // max history to keep
	Timeout                time.Duration // helm upgrade timeout
	OperationID            string
	CommandID              string
	BundleDigest           string
	ChartDigest            string
	EffectiveValuesDigest  string
	SecretSnapshotDigest   string
	ExpectedManifestDigest string
	// PlainHTTP allows OCI chart pulls from a plain HTTP registry (dev
	// fixture only). Defaults false so production stays HTTPS.
	PlainHTTP     bool
	ResetValues   bool
	ReuseValues   bool
	CleanupOnFail bool
	WaitForJobs   bool
	TakeOwnership bool
}

// RollbackOptions holds parameters for Rollback.
type RollbackOptions struct {
	Namespace      string
	ReleaseName    string
	TargetRevision int
	Timeout        time.Duration // helm rollback timeout
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
	AllValues   bool // if true, include computed values
	Version     int  // specific revision version
}

package helmengine

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"
)

// Fake is an in-memory implementation of Engine for testing.
// It never makes subprocess calls or contacts a real cluster.
type Fake struct {
	mu       sync.Mutex
	releases map[string]*Release // key: namespace/releaseName
	history  map[string][]ReleaseHistoryEntry
	counter  int

	// UpgradeError, if set, causes Upgrade to fail with this error.
	// Used to test Atomic rollback (AC-021-04).
	UpgradeError error

	// RollbackError, if set, causes Rollback to fail with this error
	// BEFORE mutating state (AC-063-03).
	RollbackError error
	// RenderedManifestDigest overrides the next rendered manifest digest.
	RenderedManifestDigest string

	// HistoryError, if set, causes History to fail with this error.
	HistoryError error

	// ArtifactUnavailableRevisions is a set of revisions whose historical
	// chart artifacts are unavailable. Rollback to these revisions returns
	// ErrArtifactUnavailable (AC-063-02).
	ArtifactUnavailableRevisions map[int]bool
}

// NewFake creates a new Fake engine.
func NewFake() *Fake {
	return &Fake{
		releases:                     make(map[string]*Release),
		history:                      make(map[string][]ReleaseHistoryEntry),
		ArtifactUnavailableRevisions: make(map[int]bool),
	}
}

func (f *Fake) key(namespace, name string) string {
	return namespace + "/" + name
}

// Install creates a release if it doesn't already exist.
func (f *Fake) Install(ctx context.Context, opts InstallOptions) (*Release, error) {
	if err := ctx.Err(); err != nil {
		return nil, ErrCancelled
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	key := f.key(opts.Namespace, opts.ReleaseName)
	if _, exists := f.releases[key]; exists {
		return nil, ErrAlreadyExists
	}

	f.counter++
	rel := &Release{
		Name:           opts.ReleaseName,
		Namespace:      opts.Namespace,
		Revision:       1,
		Status:         "deployed",
		Chart:          opts.ChartPath,
		ManifestDigest: fmt.Sprintf("fake-digest-%d", f.counter),
		Notes:          fmt.Sprintf("installed %s/%s rev=1", opts.Namespace, opts.ReleaseName),
		Provenance:     "legacy",
	}
	f.releases[key] = rel
	f.history[key] = append(f.history[key], ReleaseHistoryEntry{
		Revision:    1,
		Status:      "deployed",
		Chart:       opts.ChartPath,
		Description: "Install complete",
	})
	return rel, nil
}

// Upgrade increments the release revision.
// If opts.ExpectedRevision > 0, it must match the current revision (AC-021-02).
// If opts.Atomic is true and the upgrade fails (UpgradeError set), the release is
// rolled back to its previous revision (AC-021-04).
func (f *Fake) Upgrade(ctx context.Context, opts UpgradeOptions) (*Release, error) {
	if err := ctx.Err(); err != nil {
		return nil, ErrCancelled
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	key := f.key(opts.Namespace, opts.ReleaseName)
	existing, exists := f.releases[key]
	if !exists {
		return nil, ErrNotFound
	}

	// AC-021-02: ExpectedRevision mismatch → ErrConflict, no mutation
	if opts.ExpectedRevision > 0 && existing.Revision != opts.ExpectedRevision {
		return nil, fmt.Errorf("%w: expected revision %d, got %d", ErrConflict, opts.ExpectedRevision, existing.Revision)
	}

	prevRelease := *existing
	prevRelease.Labels = maps.Clone(existing.Labels)
	inputDigest := digestString(strings.Join([]string{
		opts.BundleDigest,
		opts.ChartDigest,
		opts.EffectiveValuesDigest,
		opts.SecretSnapshotDigest,
	}, "|"))
	description := fmt.Sprintf("release-manager operation=%s command=%s", opts.OperationID, opts.CommandID)
	if existing.Description == description && existing.Labels["rm_input_digest"] == inputDigest {
		replayed := *existing
		replayed.Labels = maps.Clone(existing.Labels)
		return &replayed, nil
	}

	f.counter++
	newRev := existing.Revision + 1
	manifestDigest := fmt.Sprintf("fake-digest-%d", f.counter)
	if f.RenderedManifestDigest != "" {
		manifestDigest = f.RenderedManifestDigest
		f.RenderedManifestDigest = ""
	}
	if opts.ExpectedManifestDigest != "" &&
		strings.TrimPrefix(opts.ExpectedManifestDigest, "sha256:") != strings.TrimPrefix(manifestDigest, "sha256:") {
		return nil, fmt.Errorf("manifest digest mismatch: %w", ErrRenderDrift)
	}
	rel := &Release{
		Name:                  opts.ReleaseName,
		Namespace:             opts.Namespace,
		Revision:              newRev,
		Status:                "deployed",
		Chart:                 opts.ChartPath,
		ManifestDigest:        manifestDigest,
		Notes:                 fmt.Sprintf("upgraded %s/%s rev=%d", opts.Namespace, opts.ReleaseName, newRev),
		Description:           description,
		Labels:                map[string]string{"rm_input_digest": inputDigest},
		BundleDigest:          opts.BundleDigest,
		ChartDigest:           opts.ChartDigest,
		EffectiveValuesDigest: opts.EffectiveValuesDigest,
		Provenance:            "managed",
	}
	f.releases[key] = rel
	f.history[key] = append(f.history[key], ReleaseHistoryEntry{
		Revision:    newRev,
		Status:      "deployed",
		Chart:       opts.ChartPath,
		Description: description,
	})

	if f.UpgradeError != nil {
		err := f.UpgradeError
		f.UpgradeError = nil
		f.history[key][len(f.history[key])-1].Status = "failed"
		if opts.Atomic {
			if f.RollbackError != nil {
				f.RollbackError = nil
				rel.Status = "failed"
				return rel, fmt.Errorf("rollback failed: %w", ErrAtomicRollbackFailed)
			}
			f.releases[key] = &prevRelease
			f.history[key][len(f.history[key])-1].Description = "Upgrade failed, rolled back"
			rolledBack := prevRelease
			rolledBack.Labels = maps.Clone(prevRelease.Labels)
			return &rolledBack, err
		}
		return rel, err
	}

	return rel, nil
}

// Rollback reverts to a previous revision.
// Validates that the target revision exists in history before executing.
// If RollbackError is set, fails before mutating state (AC-063-03).
func (f *Fake) Rollback(ctx context.Context, opts RollbackOptions) (*Release, error) {
	if err := ctx.Err(); err != nil {
		return nil, ErrCancelled
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	key := f.key(opts.Namespace, opts.ReleaseName)
	_, exists := f.releases[key]
	if !exists {
		return nil, ErrNotFound
	}

	// Validate target revision exists in history.
	hist := f.history[key]
	targetFound := false
	for _, e := range hist {
		if e.Revision == opts.TargetRevision {
			targetFound = true
			break
		}
	}
	if !targetFound {
		return nil, ErrRevisionNotFound
	}

	// Check if historical artifact is unavailable for target revision (AC-063-02).
	if f.ArtifactUnavailableRevisions[opts.TargetRevision] {
		return nil, ErrArtifactUnavailable
	}

	// AC-063-03: simulate rollback failure before state mutation.
	if f.RollbackError != nil {
		err := f.RollbackError
		f.RollbackError = nil // one-shot
		return nil, err
	}

	f.counter++
	newRev := f.releases[key].Revision + 1
	rel := &Release{
		Name:           opts.ReleaseName,
		Namespace:      opts.Namespace,
		Revision:       newRev,
		Status:         "deployed",
		Chart:          f.releases[key].Chart,
		ManifestDigest: fmt.Sprintf("fake-digest-%d", f.counter),
		Notes:          fmt.Sprintf("rolled back %s/%s to rev=%d", opts.Namespace, opts.ReleaseName, opts.TargetRevision),
	}
	f.releases[key] = rel
	f.history[key] = append(f.history[key], ReleaseHistoryEntry{
		Revision:    newRev,
		Status:      "deployed",
		Chart:       rel.Chart,
		Description: fmt.Sprintf("Rollback to %d", opts.TargetRevision),
	})
	return rel, nil
}

// Status returns the current release state.
func (f *Fake) Status(ctx context.Context, opts StatusOptions) (*Release, error) {
	if err := ctx.Err(); err != nil {
		return nil, ErrCancelled
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	key := f.key(opts.Namespace, opts.ReleaseName)
	rel, exists := f.releases[key]
	if !exists {
		return nil, ErrNotFound
	}
	// Return a copy to avoid mutating stored state.
	cp := *rel
	return &cp, nil
}

// History returns the revision history.
func (f *Fake) History(ctx context.Context, opts HistoryOptions) ([]ReleaseHistoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, ErrCancelled
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.HistoryError != nil {
		err := f.HistoryError
		f.HistoryError = nil // one-shot
		return nil, err
	}

	key := f.key(opts.Namespace, opts.ReleaseName)
	entries := f.history[key]
	if entries == nil {
		return nil, ErrNotFound
	}

	result := make([]ReleaseHistoryEntry, len(entries))
	copy(result, entries)
	return result, nil
}

// GetValues returns a copy of the stored values (empty for fake).
func (f *Fake) GetValues(ctx context.Context, opts GetValuesOptions) (map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, ErrCancelled
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	key := f.key(opts.Namespace, opts.ReleaseName)
	if _, exists := f.releases[key]; !exists {
		return nil, ErrNotFound
	}
	return map[string]interface{}{}, nil
}

// List returns all releases in a namespace.
func (f *Fake) List(ctx context.Context, namespace string) ([]*ReleaseListItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, ErrCancelled
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	prefix := namespace + "/"
	var items []*ReleaseListItem
	for key, rel := range f.releases {
		if strings.HasPrefix(key, prefix) {
			items = append(items, &ReleaseListItem{
				Namespace:    rel.Namespace,
				Name:         rel.Name,
				Chart:        rel.Chart,
				ChartVersion: rel.Chart, // Fake stores chart name only; version not tracked
				Revision:     rel.Revision,
				Status:       rel.Status,
				ValuesDigest: "",
			})
		}
	}
	return items, nil
}

// Compile-time interface check.
var _ Engine = (*Fake)(nil)

// Package helmengine defines the Helm SDK adapter contract (REQ-041).
package helmengine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/client-go/rest"
)

// RealEngine implements Engine using the Helm Go SDK.
// It never makes os/exec or subprocess calls.
type RealEngine struct {
	namespace string
	newConfig func(namespace string) (*action.Configuration, error)
}

// NewRealEngine creates a RealEngine connected to the given cluster.
// The restGetter is called on each operation to create a fresh
// action.Configuration, ensuring no mutable state leaks across concurrent operations.
func NewRealEngine(namespace string, restGetter func() *rest.Config) *RealEngine {
	return &RealEngine{
		namespace: namespace,
		newConfig: func(operationNamespace string) (*action.Configuration, error) {
			return newActionConfiguration(operationNamespace, restGetter)
		},
	}
}

func newRealEngineWithConfig(
	namespace string,
	newConfig func(namespace string) (*action.Configuration, error),
) *RealEngine {
	return &RealEngine{namespace: namespace, newConfig: newConfig}
}

func newActionConfiguration(
	namespace string,
	restGetter func() *rest.Config,
) (*action.Configuration, error) {
	cfg := new(action.Configuration)
	rc := restGetter()
	if rc == nil {
		return nil, errors.New("kubernetes rest config is required")
	}
	if err := cfg.Init(
		NewRESTClientGetter(rc, namespace),
		namespace,
		"secret",
		func(string, ...interface{}) {},
	); err != nil {
		return nil, fmt.Errorf("helm config init: %w", err)
	}
	return cfg, nil
}

// Install installs a chart. Returns ErrAlreadyExists if the release exists.
func (e *RealEngine) Install(ctx context.Context, opts InstallOptions) (*Release, error) {
	ns := e.resolveNS(opts.Namespace)
	cfg, err := e.newConfig(ns)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrActionFailed, err)
	}

	client := action.NewInstall(cfg)
	client.Namespace = ns
	client.ReleaseName = opts.ReleaseName
	client.CreateNamespace = false
	client.Timeout = 300 * time.Second

	chartPath, err := client.LocateChart(opts.ChartPath, cli.New())
	if err != nil {
		return nil, fmt.Errorf("%w: locate chart: %w", ErrActionFailed, err)
	}

	ch, err := loader.Load(chartPath)
	if err != nil {
		return nil, fmt.Errorf("%w: load chart: %w", ErrActionFailed, err)
	}

	rel, err := client.RunWithContext(ctx, ch, opts.Values)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ErrCancelled
		}
		if isReleaseExists(err) {
			return nil, fmt.Errorf("%w: %w", ErrAlreadyExists, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrActionFailed, err)
	}

	return toRelease(rel), nil
}

// Upgrade upgrades an existing release.
// If opts.ExpectedRevision > 0, it must match the current revision (AC-021-02).
// If opts.Atomic is true, a failed upgrade rolls back automatically (AC-021-04).
func (e *RealEngine) Upgrade(ctx context.Context, opts UpgradeOptions) (*Release, error) {
	ns := e.resolveNS(opts.Namespace)
	cfg, err := e.newConfig(ns)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrActionFailed, err)
	}

	// AC-021-02: check expected revision before touching anything
	if opts.ExpectedRevision > 0 {
		statusClient := action.NewStatus(cfg)
		rel, err := statusClient.Run(opts.ReleaseName)
		if err != nil {
			if isReleaseNotFound(err) {
				return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
			}
			return nil, fmt.Errorf("%w: status check: %w", ErrActionFailed, err)
		}
		if rel.Version != opts.ExpectedRevision {
			return nil, fmt.Errorf("%w: expected revision %d, got %d",
				ErrConflict, opts.ExpectedRevision, rel.Version)
		}
	}

	client := action.NewUpgrade(cfg)
	client.Namespace = ns
	client.Atomic = opts.Atomic
	if opts.Timeout > 0 {
		client.Timeout = time.Duration(opts.Timeout) * time.Second
	} else {
		client.Timeout = 300 * time.Second
	}

	chartPath, err := client.LocateChart(opts.ChartPath, cli.New())
	if err != nil {
		return nil, fmt.Errorf("%w: locate chart: %w", ErrActionFailed, err)
	}

	ch, err := loader.Load(chartPath)
	if err != nil {
		return nil, fmt.Errorf("%w: load chart: %w", ErrActionFailed, err)
	}

	vals := opts.Values
	if vals == nil {
		vals = map[string]interface{}{}
	}

	rel, err := client.RunWithContext(ctx, opts.ReleaseName, ch, vals)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ErrCancelled
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %w", ErrTimeout, err)
		}
		if isReleaseNotFound(err) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrActionFailed, err)
	}

	return toRelease(rel), nil
}

// Rollback rolls back a release to a target revision.
func (e *RealEngine) Rollback(ctx context.Context, opts RollbackOptions) (*Release, error) {
	ns := e.resolveNS(opts.Namespace)
	cfg, err := e.newConfig(ns)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrActionFailed, err)
	}

	client := action.NewRollback(cfg)
	client.Version = opts.TargetRevision
	client.Timeout = 300 * time.Second

	err = client.Run(opts.ReleaseName)
	if err != nil {
		if isReleaseNotFound(err) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrActionFailed, err)
	}

	// After rollback, get the current state
	return e.Status(ctx, StatusOptions{Namespace: opts.Namespace, ReleaseName: opts.ReleaseName})
}

//nolint:revive // Engine keeps context in every operation signature for interface consistency.
func (e *RealEngine) Status(_ context.Context, opts StatusOptions) (*Release, error) {
	ns := e.resolveNS(opts.Namespace)
	cfg, err := e.newConfig(ns)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrActionFailed, err)
	}

	client := action.NewStatus(cfg)
	rel, err := client.Run(opts.ReleaseName)
	if err != nil {
		if isReleaseNotFound(err) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrActionFailed, err)
	}

	return toRelease(rel), nil
}

//nolint:revive // Engine keeps context in every operation signature for interface consistency.
func (e *RealEngine) History(_ context.Context, opts HistoryOptions) ([]ReleaseHistoryEntry, error) {
	ns := e.resolveNS(opts.Namespace)
	cfg, err := e.newConfig(ns)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrActionFailed, err)
	}

	client := action.NewHistory(cfg)
	if opts.MaxRevisions > 0 {
		client.Max = opts.MaxRevisions
	}

	rels, err := client.Run(opts.ReleaseName)
	if err != nil {
		if isReleaseNotFound(err) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrActionFailed, err)
	}

	entries := make([]ReleaseHistoryEntry, len(rels))
	for i, r := range rels {
		entries[i] = ReleaseHistoryEntry{
			Revision:    r.Version,
			Status:      r.Info.Status.String(),
			Chart:       r.Chart.Metadata.Name,
			Description: r.Info.Description,
		}
	}
	return entries, nil
}

//nolint:revive // Engine keeps context in every operation signature for interface consistency.
func (e *RealEngine) GetValues(_ context.Context, opts GetValuesOptions) (map[string]interface{}, error) {
	ns := e.resolveNS(opts.Namespace)
	cfg, err := e.newConfig(ns)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrActionFailed, err)
	}

	client := action.NewGetValues(cfg)
	client.AllValues = false // only user-supplied values, not defaults

	vals, err := client.Run(opts.ReleaseName)
	if err != nil {
		if isReleaseNotFound(err) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrActionFailed, err)
	}

	return vals, nil
}

//nolint:revive // Engine keeps context in every operation signature for interface consistency.
func (e *RealEngine) List(_ context.Context, namespace string) ([]*ReleaseListItem, error) {
	ns := e.resolveNS(namespace)
	cfg, err := e.newConfig(ns)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrActionFailed, err)
	}

	client := action.NewList(cfg)
	rels, err := client.Run()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrActionFailed, err)
	}

	items := make([]*ReleaseListItem, len(rels))
	for i, r := range rels {
		items[i] = &ReleaseListItem{
			Namespace:    r.Namespace,
			Name:         r.Name,
			Chart:        r.Chart.Metadata.Name,
			ChartVersion: r.Chart.Metadata.Version,
			Revision:     r.Version,
			Status:       r.Info.Status.String(),
			ValuesDigest: "", // not available from Helm SDK list
		}
	}
	return items, nil
}

func (e *RealEngine) resolveNS(ns string) string {
	if ns == "" {
		return e.namespace
	}
	return ns
}

// toRelease converts a Helm release to our Release type.
func toRelease(rel *release.Release) *Release {
	return &Release{
		Name:      rel.Name,
		Namespace: rel.Namespace,
		Revision:  rel.Version,
		Status:    rel.Info.Status.String(),
		Chart:     rel.Chart.Metadata.Name,
		// ManifestDigest and Notes are populated by the caller when needed.
		ManifestDigest: "",
		Notes:          rel.Info.Notes,
	}
}

func isReleaseNotFound(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, driver.ErrReleaseNotFound) ||
		errors.Is(err, driver.ErrNoDeployedReleases) ||
		hasSubstring(err.Error(), "not found")
}

func isReleaseExists(err error) bool {
	if err == nil {
		return false
	}
	return hasSubstring(err.Error(), "already exists") ||
		hasSubstring(err.Error(), "already installed")
}

func hasSubstring(s, substr string) bool {
	return len(s) >= len(substr) && containsSubstring(s, substr)
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Compile-time interface check.
var _ Engine = (*RealEngine)(nil)

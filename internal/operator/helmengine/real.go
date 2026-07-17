// Package helmengine defines the Helm SDK adapter contract (REQ-041).
package helmengine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
)

// RealEngine implements Engine using the Helm Go SDK (helm.sh/helm/v3/pkg/action).
// It never shells out to the helm CLI binary.
type RealEngine struct {
	settings       *cli.EnvSettings
	logger         *slog.Logger
	configFactory  func(namespace string) (*action.Configuration, error)
	releaseStorage *storage.Storage
}

// NewRealEngine creates a new RealEngine.
// kubeConfig is the path to a kubeconfig file; if empty, in-cluster config is used.
func NewRealEngine(kubeConfig string, logger *slog.Logger) *RealEngine {
	settings := cli.New()
	if kubeConfig != "" {
		settings.KubeConfig = kubeConfig
	}

	return &RealEngine{
		settings: settings,
		logger:   logger,
	}
}

// actionConfig creates a new action.Configuration for a single operation.
// Production uses a Kubernetes Secret driver. Tests may inject an isolated
// configuration factory while preserving the same Helm SDK action path.
func (r *RealEngine) actionConfig(namespace string) (*action.Configuration, error) {
	if r.configFactory != nil {
		return r.configFactory(namespace)
	}

	configFlags := r.settings.RESTClientGetter()

	cfg := new(action.Configuration)
	if err := cfg.Init(configFlags, namespace, "secret", r.helmLog); err != nil {
		return nil, fmt.Errorf("initialize Helm action configuration: %w", err)
	}

	registryClient, err := registry.NewClient(
		registry.ClientOptCredentialsFile(r.settings.RegistryConfig),
	)
	if err != nil {
		return nil, fmt.Errorf("initialize Helm registry client: %w", err)
	}
	cfg.RegistryClient = registryClient
	if r.releaseStorage != nil {
		cfg.Releases = r.releaseStorage
	}

	return cfg, nil
}

// helmLog bridges Helm SDK logging to slog.
func (r *RealEngine) helmLog(format string, args ...interface{}) {
	if r.logger != nil {
		r.logger.Debug(fmt.Sprintf(format, args...))
	}
}

// Install installs a chart and returns the release.
// Returns ErrAlreadyExists if the release exists.
func (r *RealEngine) Install(ctx context.Context, opts InstallOptions) (*Release, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	cfg, err := r.actionConfig(opts.Namespace)
	if err != nil {
		return nil, fmt.Errorf("initialize Helm install: %w", err)
	}
	install := action.NewInstall(cfg)
	install.Version = opts.ChartVersion
	chartPath, err := install.LocateChart(opts.ChartPath, r.settings)
	if err != nil {
		return nil, fmt.Errorf("locate Helm chart %q: %w", opts.ChartPath, err)
	}

	chrt, err := loader.Load(chartPath)
	if err != nil {
		return nil, fmt.Errorf("load Helm chart %q: %w", chartPath, err)
	}

	install.Namespace = opts.Namespace
	install.ReleaseName = opts.ReleaseName
	install.Atomic = opts.Atomic
	install.CreateNamespace = opts.CreateNamespace
	install.Wait = opts.Atomic
	if opts.Timeout > 0 {
		install.Timeout = opts.Timeout
	}

	rel, err := install.RunWithContext(ctx, chrt, opts.Values)
	if err != nil {
		return nil, mapActionError(ctx, "install Helm release", err)
	}
	return toEngineRelease(rel), nil
}

// Upgrade upgrades an existing release.
func (r *RealEngine) Upgrade(ctx context.Context, opts UpgradeOptions) (*Release, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	cfg, err := r.actionConfig(opts.Namespace)
	if err != nil {
		return nil, fmt.Errorf("initialize Helm upgrade: %w", err)
	}
	upgrade := action.NewUpgrade(cfg)
	upgrade.Version = opts.ChartVersion
	chartPath, err := upgrade.LocateChart(opts.ChartPath, r.settings)
	if err != nil {
		return nil, fmt.Errorf("locate Helm chart %q: %w", opts.ChartPath, err)
	}

	chrt, err := loader.Load(chartPath)
	if err != nil {
		return nil, fmt.Errorf("load Helm chart %q: %w", chartPath, err)
	}

	upgrade.Namespace = opts.Namespace
	upgrade.Atomic = opts.Atomic
	if opts.MaxHistory > 0 {
		upgrade.MaxHistory = opts.MaxHistory
	}
	if opts.Timeout > 0 {
		upgrade.Timeout = opts.Timeout
	}

	rel, err := upgrade.RunWithContext(ctx, opts.ReleaseName, chrt, opts.Values)
	if err != nil {
		return nil, mapActionError(ctx, "upgrade Helm release", err)
	}
	return toEngineRelease(rel), nil
}

// Rollback rolls back a release to a target revision.
func (r *RealEngine) Rollback(ctx context.Context, opts RollbackOptions) (*Release, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	cfg, err := r.actionConfig(opts.Namespace)
	if err != nil {
		return nil, fmt.Errorf("initialize Helm rollback: %w", err)
	}

	rollback := action.NewRollback(cfg)
	rollback.Version = opts.TargetRevision
	if opts.Timeout > 0 {
		rollback.Timeout = opts.Timeout
	}

	if err := rollback.Run(opts.ReleaseName); err != nil {
		return nil, mapActionError(ctx, "roll back Helm release", err)
	}
	return statusWithConfig(ctx, cfg, opts.Namespace, opts.ReleaseName)
}

// Status returns the current status of a release.
func (r *RealEngine) Status(ctx context.Context, opts StatusOptions) (*Release, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	cfg, err := r.actionConfig(opts.Namespace)
	if err != nil {
		return nil, fmt.Errorf("initialize Helm status: %w", err)
	}
	return statusWithConfig(ctx, cfg, opts.Namespace, opts.ReleaseName)
}

func statusWithConfig(
	ctx context.Context,
	cfg *action.Configuration,
	namespace string,
	releaseName string,
) (*Release, error) {
	status := action.NewStatus(cfg)
	rel, err := status.Run(releaseName)
	if err != nil {
		return nil, mapActionError(ctx, "get Helm release status", err)
	}
	if rel.Namespace == "" {
		rel.Namespace = namespace
	}
	return toEngineRelease(rel), nil
}

// History returns the revision history for a release.
func (r *RealEngine) History(ctx context.Context, opts HistoryOptions) ([]ReleaseHistoryEntry, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	cfg, err := r.actionConfig(opts.Namespace)
	if err != nil {
		return nil, fmt.Errorf("initialize Helm history: %w", err)
	}

	history := action.NewHistory(cfg)
	if opts.MaxRevisions > 0 {
		history.Max = opts.MaxRevisions
	}

	rels, err := history.Run(opts.ReleaseName)
	if err != nil {
		return nil, mapActionError(ctx, "get Helm release history", err)
	}

	entries := make([]ReleaseHistoryEntry, len(rels))
	for i, rel := range rels {
		chartRef := ""
		if rel.Chart != nil && rel.Chart.Metadata != nil {
			chartRef = rel.Chart.Metadata.Name + "-" + rel.Chart.Metadata.Version
		}
		entries[i] = ReleaseHistoryEntry{
			Revision:    rel.Version,
			Status:      rel.Info.Status.String(),
			Chart:       chartRef,
			Description: rel.Info.Description,
		}
	}
	return entries, nil
}

// GetValues returns the current values for a release.
func (r *RealEngine) GetValues(ctx context.Context, opts GetValuesOptions) (map[string]interface{}, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	cfg, err := r.actionConfig(opts.Namespace)
	if err != nil {
		return nil, fmt.Errorf("initialize Helm get values: %w", err)
	}

	getValues := action.NewGetValues(cfg)
	getValues.AllValues = opts.AllValues
	if opts.Version > 0 {
		getValues.Version = opts.Version
	}

	vals, err := getValues.Run(opts.ReleaseName)
	if err != nil {
		return nil, mapActionError(ctx, "get Helm release values", err)
	}
	return vals, nil
}

// List returns all releases in a namespace.
func (r *RealEngine) List(ctx context.Context, namespace string) ([]*ReleaseListItem, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	cfg, err := r.actionConfig(namespace)
	if err != nil {
		return nil, fmt.Errorf("initialize Helm list: %w", err)
	}

	list := action.NewList(cfg)
	list.All = true
	list.AllNamespaces = namespace == ""

	rels, err := list.Run()
	if err != nil {
		return nil, mapActionError(ctx, "list Helm releases", err)
	}

	items := make([]*ReleaseListItem, len(rels))
	for i, rel := range rels {
		chartName := ""
		chartVersion := ""
		if rel.Chart != nil && rel.Chart.Metadata != nil {
			chartName = rel.Chart.Metadata.Name
			chartVersion = rel.Chart.Metadata.Version
		}
		items[i] = &ReleaseListItem{
			Namespace:    rel.Namespace,
			Name:         rel.Name,
			Chart:        chartName,
			ChartVersion: chartVersion,
			Revision:     rel.Version,
			Status:       rel.Info.Status.String(),
			ValuesDigest: digestValues(rel.Config),
		}
	}
	return items, nil
}

// Compile-time check: RealEngine implements Engine.
var _ Engine = (*RealEngine)(nil)

// toEngineRelease converts a Helm SDK release.Release to our Release type.
// ManifestDigest is computed from the rendered manifest. Callers must treat it
// as sensitive metadata because rendered manifests can include Secret data.
func toEngineRelease(rel *release.Release) *Release {
	chartRef := ""
	if rel.Chart != nil && rel.Chart.Metadata != nil {
		chartRef = rel.Chart.Metadata.Name + "-" + rel.Chart.Metadata.Version
	}

	status := ""
	notes := ""
	if rel.Info != nil {
		status = rel.Info.Status.String()
		notes = rel.Info.Notes
	}

	return &Release{
		Name:           rel.Name,
		Namespace:      rel.Namespace,
		Revision:       rel.Version,
		Status:         status,
		Chart:          chartRef,
		ManifestDigest: digestString(rel.Manifest),
		Notes:          notes,
	}
}

func digestString(value string) string {
	if value == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", hash)
}

func digestValues(vals map[string]interface{}) string {
	if vals == nil {
		return ""
	}

	encoded, err := json.Marshal(vals)
	if err != nil {
		return ""
	}
	return digestString(string(encoded))
}

func contextError(ctx context.Context) error {
	switch ctx.Err() {
	case context.Canceled:
		return ErrCancelled
	case context.DeadlineExceeded:
		return ErrTimeout
	default:
		return nil
	}
}

func mapActionError(ctx context.Context, operation string, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s: %w", operation, ErrTimeout)
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return fmt.Errorf("%s: %w", operation, ErrCancelled)
	case errors.Is(err, driver.ErrReleaseExists), strings.Contains(err.Error(), "cannot re-use a name that is still in use"):
		return fmt.Errorf("%s: %w", operation, ErrAlreadyExists)
	case errors.Is(err, driver.ErrReleaseNotFound), errors.Is(err, driver.ErrNoDeployedReleases):
		return fmt.Errorf("%s: %w", operation, ErrNotFound)
	default:
		return fmt.Errorf("%s: %w: %v", operation, ErrActionFailed, err)
	}
}

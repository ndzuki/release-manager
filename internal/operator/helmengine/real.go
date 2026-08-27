// Package helmengine defines the Helm SDK adapter contract (REQ-041).
package helmengine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// SetReleaseStorage injects an isolated release storage for testing.
// Production code MUST NOT call this — the default Kubernetes Secret driver is used.
func (r *RealEngine) SetReleaseStorage(s *storage.Storage) {
	r.releaseStorage = s
}

// SetConfigFactory injects a custom action.Configuration factory for testing.
// Production code MUST NOT call this — the default Kube client is used.
func (r *RealEngine) SetConfigFactory(fn func(namespace string) (*action.Configuration, error)) {
	r.configFactory = fn
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
	// The dev fixture registry is plain HTTP (localhost / cluster-local
	// registry.dev.release-manager.local). Helm's OCI downloader defaults to
	// HTTPS; without PlainHTTP the fetch attempts HTTPS against a plain-HTTP
	// registry and fails (real smoke 2026-08-27: helm_install_failed "Get
	// https://... connection refused / EOF").
	install.PlainHTTP = true
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

// Upgrade upgrades an existing release using the frozen typed command inputs.
//
//nolint:gocyclo // The ordered Helm safety gates mirror the approved execution sequence.
func (r *RealEngine) Upgrade(ctx context.Context, opts UpgradeOptions) (*Release, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	cfg, err := r.actionConfig(opts.Namespace)
	if err != nil {
		return nil, fmt.Errorf("initialize Helm upgrade: %w", err)
	}
	current, err := helmStatusRelease(ctx, cfg, opts.ReleaseName)
	if err != nil {
		return nil, err
	}
	if current.Info == nil || current.Info.Status != release.StatusDeployed {
		if current.Info != nil && current.Info.Status.IsPending() {
			return nil, fmt.Errorf("release busy: %w", ErrReleaseBusy)
		}
		return nil, fmt.Errorf("release is not deployed: %w", ErrReleaseNotDeployed)
	}

	inputDigest := digestString(strings.Join([]string{
		opts.BundleDigest,
		opts.ChartDigest,
		opts.EffectiveValuesDigest,
		opts.SecretSnapshotDigest,
	}, "|"))
	description := fmt.Sprintf("release-manager operation=%s command=%s", opts.OperationID, opts.CommandID)
	if current.Info.Description == description && current.Labels["rm_input_digest"] == inputDigest {
		return decorateRelease(toEngineRelease(current), current, opts, inputDigest), nil
	}
	if opts.ExpectedRevision > 0 && current.Version != opts.ExpectedRevision {
		return nil, fmt.Errorf("expected revision %d, got %d: %w", opts.ExpectedRevision, current.Version, ErrConflict)
	}

	upgrade := newUpgradeAction(cfg, opts, description, inputDigest)
	chartPath, err := upgrade.LocateChart(opts.ChartPath, r.settings)
	if err != nil {
		return nil, fmt.Errorf("locate Helm chart %q: %w", opts.ChartPath, err)
	}
	if opts.ChartDigest != "" && !strings.HasPrefix(opts.ChartPath, "oci://") {
		chartBytes, err := os.ReadFile(chartPath)
		if err != nil {
			return nil, fmt.Errorf("read Helm chart %q: %w", chartPath, err)
		}
		if !digestMatches(opts.ChartDigest, chartBytes) {
			return nil, fmt.Errorf("chart digest mismatch: %w", ErrDigestMismatch)
		}
	}

	chrt, err := loader.Load(chartPath)
	if err != nil {
		return nil, fmt.Errorf("load Helm chart %q: %w", chartPath, err)
	}
	expectedManifestDigest, err := renderUpgradeManifestDigest(ctx, cfg, opts, description, inputDigest, chrt)
	if err != nil {
		return nil, mapUpgradePreparationError(ctx, err)
	}
	if opts.ExpectedManifestDigest != "" {
		expectedManifestDigest = opts.ExpectedManifestDigest
	}
	upgrade.PostRenderer = &manifestGate{expectedDigest: expectedManifestDigest}

	rel, err := upgrade.RunWithContext(ctx, opts.ReleaseName, chrt, opts.Values)
	if err != nil && rel == nil {
		return nil, mapUpgradePreparationError(ctx, err)
	}
	if err != nil {
		active, statusErr := helmStatusRelease(context.WithoutCancel(ctx), cfg, opts.ReleaseName)
		if statusErr != nil {
			return nil, fmt.Errorf("upgrade failed and active release lookup failed: %w", ErrAtomicRollbackFailed)
		}
		activeResult := decorateRelease(toEngineRelease(active), active, opts, inputDigest)
		if rel != nil && active.Info != nil && active.Info.Status == release.StatusDeployed && active.Version != rel.Version {
			return activeResult, mapUpgradePreparationError(ctx, err)
		}
		return activeResult, fmt.Errorf("upgrade and atomic rollback failed: %w", ErrAtomicRollbackFailed)
	}
	return decorateRelease(toEngineRelease(rel), rel, opts, inputDigest), nil
}

func newUpgradeAction(cfg *action.Configuration, opts UpgradeOptions, description, inputDigest string) *action.Upgrade {
	upgrade := action.NewUpgrade(cfg)
	upgrade.Version = opts.ChartVersion
	// Plain HTTP for the dev fixture registry (see Install/PlainHTTP).
	upgrade.PlainHTTP = true
	upgrade.Namespace = opts.Namespace
	upgrade.Atomic = true
	upgrade.CleanupOnFail = false
	upgrade.ResetValues = true
	upgrade.ReuseValues = false
	upgrade.Force = false
	upgrade.TakeOwnership = false
	upgrade.DisableHooks = false
	upgrade.WaitForJobs = true
	upgrade.MaxHistory = 10
	upgrade.Description = description
	upgrade.Labels = map[string]string{"rm_input_digest": inputDigest}
	if opts.MaxHistory > 0 {
		upgrade.MaxHistory = opts.MaxHistory
	}
	if opts.Timeout > 0 {
		upgrade.Timeout = opts.Timeout
	}
	return upgrade
}

func renderUpgradeManifestDigest(
	ctx context.Context,
	cfg *action.Configuration,
	opts UpgradeOptions,
	description string,
	inputDigest string,
	chrt *chart.Chart,
) (string, error) {
	dryRun := newUpgradeAction(cfg, opts, description, inputDigest)
	dryRun.DryRun = true
	dryRun.DryRunOption = "client"
	rel, err := dryRun.RunWithContext(ctx, opts.ReleaseName, chrt, opts.Values)
	if err != nil {
		return "", err
	}
	return digestString(rel.Manifest), nil
}

func mapUpgradePreparationError(ctx context.Context, err error) error {
	if strings.Contains(err.Error(), "values don't meet the specifications of the schema") {
		return fmt.Errorf("prepare Helm upgrade: %w", ErrSchemaFailed)
	}
	return mapActionError(ctx, "upgrade Helm release", err)
}

// Rollback rolls back a release to a target revision.
// Performs pre-rollback validation: history check and historical artifact
// availability check before delegating to the Helm SDK.
func (r *RealEngine) Rollback(ctx context.Context, opts RollbackOptions) (*Release, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	cfg, err := r.actionConfig(opts.Namespace)
	if err != nil {
		return nil, fmt.Errorf("initialize Helm rollback: %w", err)
	}

	// Pre-rollback: verify target revision exists in history (AC-063-02).
	history := action.NewHistory(cfg)
	rels, histErr := history.Run(opts.ReleaseName)
	if histErr != nil {
		if errors.Is(histErr, driver.ErrReleaseNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get Helm release history: %w", histErr)
	}
	targetFound := false
	for _, rel := range rels {
		if rel.Version == opts.TargetRevision {
			targetFound = true
			break
		}
	}
	if !targetFound {
		return nil, ErrRevisionNotFound
	}

	// Pre-rollback: verify historical artifact is still retrievable.
	// If Helm storage cannot return the target release, the artifact is unavailable.
	if _, getErr := cfg.Releases.Get(opts.ReleaseName, opts.TargetRevision); getErr != nil {
		if errors.Is(getErr, driver.ErrReleaseNotFound) {
			return nil, ErrArtifactUnavailable
		}
		return nil, fmt.Errorf("get historical release: %w", getErr)
	}

	rollback := action.NewRollback(cfg)
	rollback.Version = opts.TargetRevision
	if opts.Timeout > 0 {
		rollback.Timeout = opts.Timeout
	}
	rollback.Wait = true           // wait for resources to be ready
	rollback.CleanupOnFail = false // AC-063-03: preserve original release on failure

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

type manifestGate struct {
	expectedDigest string
}

func (g *manifestGate) Run(rendered *bytes.Buffer) (*bytes.Buffer, error) {
	if g.expectedDigest != "" && !digestMatches(g.expectedDigest, rendered.Bytes()) {
		return nil, fmt.Errorf("manifest digest mismatch: %w", ErrRenderDrift)
	}
	return bytes.NewBuffer(bytes.Clone(rendered.Bytes())), nil
}

func helmStatusRelease(ctx context.Context, cfg *action.Configuration, releaseName string) (*release.Release, error) {
	status := action.NewStatus(cfg)
	rel, err := status.Run(releaseName)
	if err != nil {
		return nil, mapActionError(ctx, "get Helm release status", err)
	}
	return rel, nil
}

func digestMatches(expected string, data []byte) bool {
	expected = strings.TrimPrefix(expected, "sha256:")
	sum := sha256.Sum256(data)
	return strings.EqualFold(expected, fmt.Sprintf("%x", sum))
}

func decorateRelease(result *Release, rel *release.Release, opts UpgradeOptions, inputDigest string) *Release {
	if result == nil || rel == nil {
		return result
	}
	result.BundleDigest = opts.BundleDigest
	result.ChartDigest = opts.ChartDigest
	result.EffectiveValuesDigest = opts.EffectiveValuesDigest
	result.Description = ""
	if rel.Info != nil {
		result.Description = rel.Info.Description
	}
	result.Labels = maps.Clone(rel.Labels)
	result.Provenance = "legacy"
	if rel.Info != nil && strings.HasPrefix(rel.Info.Description, "release-manager operation=") && rel.Labels["rm_input_digest"] != "" {
		result.Provenance = "managed"
	}
	if result.Labels == nil {
		result.Labels = map[string]string{}
	}
	result.Labels["rm_input_digest"] = inputDigest
	return result
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
		Workloads:      ExtractWorkloads(rel.Manifest, rel.Namespace),
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
	case apierrors.IsForbidden(err):
		return fmt.Errorf("%s: %w: %v", operation, ErrForbidden, err)
	case errors.Is(err, ErrRenderDrift):
		return fmt.Errorf("%s: %w", operation, ErrRenderDrift)
	case errors.Is(err, driver.ErrReleaseExists), strings.Contains(err.Error(), "cannot re-use a name that is still in use"):
		return fmt.Errorf("%s: %w", operation, ErrAlreadyExists)
	case errors.Is(err, driver.ErrReleaseNotFound), errors.Is(err, driver.ErrNoDeployedReleases):
		return fmt.Errorf("%s: %w", operation, ErrNotFound)
	default:
		return fmt.Errorf("%s: %w: %v", operation, ErrActionFailed, err)
	}
}

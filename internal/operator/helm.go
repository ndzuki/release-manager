// Package operator 使用 Helm v4 Go SDK 实现 Helm Chart 生命周期操作。
//
// 所有操作（install/upgrade/rollback/status/list/history/OCI pull）
// use the Helm v4 SDK directly — no CLI exec. This gives us type safety, proper
// error handling, and no shell escaping concerns.
//
// Helm v4 SDK 相比 v3 的主要变更:
//   - release.Releaser and chart.Charter are interfaces (any), use Accessor helpers
//   - Upgrade.Install is purely informative — must check release existence and
//     create an Install action explicitly for install-or-upgrade
//   - Wait field replaced by WaitStrategy (kube.StatusWatcherStrategy)
//   - Atomic field removed from Upgrade; use Install.RollbackOnFailure
//   - Logging is via slog.Handler
package operator

import (
	"context"
	cryptotls "crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/loader"
	"helm.sh/helm/v4/pkg/cli"
	"helm.sh/helm/v4/pkg/kube"
	"helm.sh/helm/v4/pkg/registry"
	"helm.sh/helm/v4/pkg/release"

	"github.com/ndzuki/release-manager/internal/config"
)

// HelmClient 封装 Helm v4 SDK 的 chart 生命周期操作。
type HelmClient struct {
	cfg       *config.HelmConfig
	harborCfg *config.HarborConfig
	settings  *cli.EnvSettings
	log       logr.Logger
}

// NewHelmClient 创建新的 HelmClient。
func NewHelmClient(helmCfg *config.HelmConfig, harborCfg *config.HarborConfig, log logr.Logger) *HelmClient {
	settings := cli.New()

	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		settings.KubeConfig = kc
	}
	if helmCfg.CacheDir != "" {
		settings.RepositoryCache = helmCfg.CacheDir
		settings.RepositoryConfig = filepath.Join(helmCfg.CacheDir, "repositories.yaml")
	}

	return &HelmClient{
		cfg:       helmCfg,
		harborCfg: harborCfg,
		settings:  settings,
		log:       log.WithName("helm"),
	}
}

// ---------------------------------------------------------------------------
// 辅助函数: 为指定 namespace 创建已初始化的 action.Configuration。
// ---------------------------------------------------------------------------

func (c *HelmClient) newActionConfig(namespace string) (*action.Configuration, error) {
	helmDriver := os.Getenv("HELM_DRIVER")
	if helmDriver == "" {
		helmDriver = "secret"
	}

	slogHandler := newLogrSlogHandler(c.log)

	actionCfg := action.NewConfiguration(
		action.ConfigurationSetLogger(slogHandler),
	)

	if err := actionCfg.Init(
		c.settings.RESTClientGetter(),
		namespace,
		helmDriver,
	); err != nil {
		return nil, fmt.Errorf("init action config: %w", err)
	}

	return actionCfg, nil
}

// ---------------------------------------------------------------------------
// Upgrade 选项和结果类型
// ---------------------------------------------------------------------------

type UpgradeOptions struct {
	ChartPath       string
	ReleaseName     string
	Namespace       string
	Values          map[string]any
	Timeout         int64 // seconds
	RollbackOnFail  bool
	Wait            bool
	CreateNamespace bool
	MaxHistory      int
}

type UpgradeResult struct {
	ReleaseName  string
	ChartName    string
	ChartVersion string
	Revision     int
	Status       string
	Description  string
}

// ---------------------------------------------------------------------------
// Upgrade — 基于 SDK 的 install-or-upgrade
// ---------------------------------------------------------------------------

// Upgrade 使用 v4 SDK 执行 helm install-or-upgrade。
func (c *HelmClient) Upgrade(ctx context.Context, opts UpgradeOptions) (*UpgradeResult, error) {
	c.log.Info("helm install-or-upgrade (SDK v4)",
		"release", opts.ReleaseName,
		"namespace", opts.Namespace,
		"chart", opts.ChartPath,
	)

	timeout := time.Duration(opts.Timeout) * time.Second
	if timeout <= 0 {
		timeout = c.cfg.UpgradeTimeout
	}

	ch, err := loader.Load(opts.ChartPath)
	if err != nil {
		return nil, fmt.Errorf("load chart %s: %w", opts.ChartPath, err)
	}

	existing, err := c.GetRelease(ctx, opts.ReleaseName, opts.Namespace)
	isInstalled := err == nil && existing != nil

	if !isInstalled {
		return c.install(ctx, ch, opts, timeout)
	}
	return c.upgrade(ctx, ch, opts, timeout)
}

func (c *HelmClient) install(ctx context.Context, ch chart.Charter, opts UpgradeOptions, timeout time.Duration) (*UpgradeResult, error) {
	actionCfg, err := c.newActionConfig(opts.Namespace)
	if err != nil {
		return nil, err
	}

	client := action.NewInstall(actionCfg)
	client.Namespace = opts.Namespace
	client.ReleaseName = opts.ReleaseName
	client.CreateNamespace = opts.CreateNamespace
	client.RollbackOnFailure = opts.RollbackOnFail
	client.Timeout = timeout

	if opts.Wait {
		client.WaitStrategy = kube.StatusWatcherStrategy
	}

	rel, err := client.RunWithContext(ctx, ch, opts.Values)
	if err != nil {
		return nil, fmt.Errorf("install %s: %w", opts.ReleaseName, err)
	}

	return unpackResult(rel)
}

func (c *HelmClient) upgrade(ctx context.Context, ch chart.Charter, opts UpgradeOptions, timeout time.Duration) (*UpgradeResult, error) {
	actionCfg, err := c.newActionConfig(opts.Namespace)
	if err != nil {
		return nil, err
	}

	client := action.NewUpgrade(actionCfg)
	client.Namespace = opts.Namespace
	client.Timeout = timeout
	client.ResetValues = false
	client.ReuseValues = true

	if opts.Wait {
		client.WaitStrategy = kube.StatusWatcherStrategy
	}
	if opts.MaxHistory > 0 {
		client.MaxHistory = opts.MaxHistory
	} else {
		client.MaxHistory = c.cfg.MaxHistory
	}

	rel, err := client.RunWithContext(ctx, opts.ReleaseName, ch, opts.Values)
	if err != nil {
		return nil, fmt.Errorf("upgrade %s: %w", opts.ReleaseName, err)
	}

	return unpackResult(rel)
}

// unpackResult extracts metadata from a v4 release.Releaser (any) via Accessor.
func unpackResult(rel release.Releaser) (*UpgradeResult, error) {
	acc, err := release.NewAccessor(rel)
	if err != nil {
		return nil, fmt.Errorf("release accessor: %w", err)
	}

	chartAccessor, err := chart.NewAccessor(acc.Chart())
	if err != nil {
		return nil, fmt.Errorf("chart accessor: %w", err)
	}

	meta := chartAccessor.MetadataAsMap()
	version, _ := meta["version"].(string)

	return &UpgradeResult{
		ReleaseName:  acc.Name(),
		ChartName:    chartAccessor.Name(),
		ChartVersion: version,
		Revision:     acc.Version(),
		Status:       acc.Status(),
	}, nil
}

// ---------------------------------------------------------------------------
// Rollback 回滚操作
// ---------------------------------------------------------------------------

func (c *HelmClient) Rollback(ctx context.Context, releaseName, namespace string) error {
	c.log.Info("helm rollback (SDK v4)", "release", releaseName, "namespace", namespace)

	actionCfg, err := c.newActionConfig(namespace)
	if err != nil {
		return err
	}

	client := action.NewRollback(actionCfg)
	client.WaitStrategy = kube.StatusWatcherStrategy
	client.Timeout = 5 * time.Minute
	client.CleanupOnFail = true
	client.MaxHistory = c.cfg.MaxHistory

	if err := client.Run(releaseName); err != nil {
		return fmt.Errorf("rollback %s: %w", releaseName, err)
	}

	c.log.Info("rollback completed", "release", releaseName)
	return nil
}

// ---------------------------------------------------------------------------
// Query: 查询操作（状态/release/历史/列表/已安装版本）
// ---------------------------------------------------------------------------

func (c *HelmClient) GetStatus(ctx context.Context, releaseName, namespace string) (string, error) {
	rel, err := c.GetRelease(ctx, releaseName, namespace)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return "not_installed", nil
		}
		return "", err
	}

	acc, err := release.NewAccessor(rel)
	if err != nil {
		return "", fmt.Errorf("release accessor: %w", err)
	}
	return acc.Status(), nil
}

func (c *HelmClient) GetRelease(ctx context.Context, releaseName, namespace string) (release.Releaser, error) {
	actionCfg, err := c.newActionConfig(namespace)
	if err != nil {
		return nil, err
	}

	client := action.NewGet(actionCfg)
	return client.Run(releaseName)
}

func (c *HelmClient) GetHistory(ctx context.Context, releaseName, namespace string) ([]release.Releaser, error) {
	actionCfg, err := c.newActionConfig(namespace)
	if err != nil {
		return nil, err
	}

	client := action.NewHistory(actionCfg)
	client.Max = c.cfg.MaxHistory
	return client.Run(releaseName)
}

func (c *HelmClient) ListReleases(ctx context.Context, namespace string) ([]release.Releaser, error) {
	actionCfg, err := c.newActionConfig(namespace)
	if err != nil {
		return nil, err
	}

	client := action.NewList(actionCfg)
	client.All = true
	client.SetStateMask()
	return client.Run()
}

func (c *HelmClient) GetInstalledVersion(ctx context.Context, releaseName, namespace string) (string, error) {
	rel, err := c.GetRelease(ctx, releaseName, namespace)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return "", nil
		}
		return "", err
	}

	acc, err := release.NewAccessor(rel)
	if err != nil {
		return "", err
	}

	chartAcc, err := chart.NewAccessor(acc.Chart())
	if err != nil {
		return "", err
	}

	meta := chartAcc.MetadataAsMap()
	version, _ := meta["version"].(string)
	return version, nil
}

func (c *HelmClient) IsVersionDeployed(ctx context.Context, releaseName, namespace, version string) (bool, error) {
	deployed, err := c.GetInstalledVersion(ctx, releaseName, namespace)
	if err != nil {
		return false, err
	}
	return deployed == version, nil
}

// ---------------------------------------------------------------------------
// OCI Pull — SDK-based via registry.Client + action.Pull
// ---------------------------------------------------------------------------

// PullChart 使用 Helm v4 SDK 从 OCI registry 拉取 Helm chart。
func (c *HelmClient) PullChart(ctx context.Context, chartURL, version, destDir string) (string, error) {
	c.log.Info("pulling chart from OCI registry (SDK v4)",
		"url", chartURL,
		"version", version,
		"dest", destDir,
	)

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", destDir, err)
	}

	// Parse the OCI URL to extract the registry host.
	// chartURL format: oci://harbor.example.com/helm/magic-sandbox
	registryHost, err := extractRegistryHost(chartURL)
	if err != nil {
		return "", fmt.Errorf("parse chart URL %s: %w", chartURL, err)
	}

	// 使用 Harbor 凭据构建 registry 客户端。
	regClient, err := c.newRegistryClient()
	if err != nil {
		return "", fmt.Errorf("create registry client: %w", err)
	}

	// 登录 OCI registry。
	if c.harborCfg.Username != "" && c.harborCfg.Password != "" {
		loginOpts := []registry.LoginOption{
			registry.LoginOptBasicAuth(c.harborCfg.Username, c.harborCfg.Password),
		}
		if c.harborCfg.InsecureSkipVerify {
			loginOpts = append(loginOpts, registry.LoginOptInsecure(true))
		}
		if err := regClient.Login(registryHost, loginOpts...); err != nil {
			return "", fmt.Errorf("registry login %s: %w", registryHost, err)
		}
		c.log.V(1).Info("logged in to OCI registry", "host", registryHost)
	}

	// 使用 settings 和 registry 客户端创建 action.Pull。
	actionCfg, err := c.newActionConfig("") // namespace irrelevant for pull
	if err != nil {
		return "", fmt.Errorf("init action config for pull: %w", err)
	}
	pullClient := action.NewPull(action.WithConfig(actionCfg))
	pullClient.Settings = c.settings
	pullClient.DestDir = destDir
	pullClient.ChartPathOptions.Version = version
	pullClient.SetRegistryClient(regClient)

	if c.harborCfg.InsecureSkipVerify {
		pullClient.ChartPathOptions.InsecureSkipTLSVerify = true
	}

	// 执行 pull 操作，返回下载文件路径。
	resultPath, err := pullClient.Run(chartURL)
	if err != nil {
		return "", fmt.Errorf("pull chart %s@%s: %w", chartURL, version, err)
	}

	c.log.Info("chart pulled successfully via SDK", "path", resultPath)
	return resultPath, nil
}

// newRegistryClient creates an OCI registry client configured for Harbor.
func (c *HelmClient) newRegistryClient() (*registry.Client, error) {
	opts := []registry.ClientOption{registry.ClientOptWriter(c.logWriter())}

	if c.harborCfg.Username != "" && c.harborCfg.Password != "" {
		opts = append(opts, registry.ClientOptBasicAuth(c.harborCfg.Username, c.harborCfg.Password))
	}
	if c.harborCfg.InsecureSkipVerify {
		opts = append(opts, registry.ClientOptPlainHTTP())
	}

	// Harbor 自签 CA 证书：构建自定义 HTTP 客户端，信任指定 CA
	if c.harborCfg.CAFile != "" {
		if caData, err := os.ReadFile(c.harborCfg.CAFile); err == nil {
			pool, err := x509.SystemCertPool()
			if err != nil {
				pool = x509.NewCertPool()
			}
			if pool.AppendCertsFromPEM(caData) {
				httpClient := &http.Client{
					Transport: &http.Transport{
						TLSClientConfig: &cryptotls.Config{
							RootCAs:    pool,
							MinVersion: cryptotls.VersionTLS12,
						},
					},
					Timeout: c.harborCfg.Timeout,
				}
				opts = append(opts, registry.ClientOptHTTPClient(httpClient))
				c.log.V(1).Info("Harbor registry: loaded custom CA", "ca_file", c.harborCfg.CAFile)
			}
		}
	}

	if credFile := c.settings.RegistryConfig; credFile != "" {
		if _, err := os.Stat(credFile); err == nil {
			opts = append(opts, registry.ClientOptCredentialsFile(credFile))
		}
	}

	return registry.NewClient(opts...)
}

// extractRegistryHost extracts the registry host from an OCI chart URL.
// "oci://harbor.example.com/helm/magic-sandbox" → "harbor.example.com"
func extractRegistryHost(chartURL string) (string, error) {
	u, err := url.Parse(chartURL)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	return u.Host, nil
}

// logWriter returns an io.Writer that writes to the logger at debug level.
func (c *HelmClient) logWriter() *logWriter {
	return &logWriter{log: c.log}
}

type logWriter struct {
	log logr.Logger
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	w.log.V(2).Info(strings.TrimSpace(string(p)))
	return len(p), nil
}

// ---------------------------------------------------------------------------
// slog adaptation for Helm v4's logging
// ---------------------------------------------------------------------------

type logrSlogHandler struct {
	log logr.Logger
}

func newLogrSlogHandler(log logr.Logger) slog.Handler {
	return &logrSlogHandler{log: log.WithName("helm-sdk")}
}

func (h *logrSlogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return true
}

func (h *logrSlogHandler) Handle(_ context.Context, r slog.Record) error {
	kvs := []any{"helm.level", r.Level.String(), "helm.msg", r.Message}
	r.Attrs(func(a slog.Attr) bool {
		kvs = append(kvs, a.Key, a.Value.String())
		return true
	})

	switch r.Level {
	case slog.LevelDebug:
		h.log.V(2).Info("helm sdk", kvs...)
	case slog.LevelInfo:
		h.log.V(1).Info("helm sdk", kvs...)
	case slog.LevelWarn:
		h.log.Info("helm sdk warning", kvs...)
	case slog.LevelError:
		h.log.Info("helm sdk error", kvs...)
	}
	return nil
}

func (h *logrSlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *logrSlogHandler) WithGroup(name string) slog.Handler {
	return h
}

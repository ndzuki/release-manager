// Package operator 实现 release 控制器的异步状态机。
package operator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/ndzuki/release-manager/internal/config"
	releasev1 "github.com/ndzuki/release-manager/api/gen/release/v1"
)

// ReleaseStatus 表示发布操作的当前状态。
type ReleaseStatus string

const (
	StatusPending        ReleaseStatus = "PENDING"
	StatusPullingChart   ReleaseStatus = "PULLING_CHART"
	StatusPullFailed     ReleaseStatus = "PULL_FAILED"
	StatusUpgrading      ReleaseStatus = "UPGRADING"
	StatusUpgradeFailed  ReleaseStatus = "UPGRADE_FAILED"
	StatusSucceeded      ReleaseStatus = "SUCCEEDED"
	StatusRollingBack    ReleaseStatus = "ROLLING_BACK"
	StatusRolledBack     ReleaseStatus = "ROLLED_BACK"
	StatusRollbackFailed ReleaseStatus = "ROLLBACK_FAILED"
)

// ReleaseRequest 表示一个待处理或进行中的发布请求。
type ReleaseRequest struct {
	RequestID    string
	ChartName    string
	ChartURL     string
	ChartVersion string
	ReleaseName  string
	Namespace    string
	CustomerID   string
	Images       map[string]string
	Timeout      int64
}

// ReleaseResult 保存发布操作的最终结果。
type ReleaseResult struct {
	RequestID    string
	ChartName    string
	ChartVersion string
	Status       ReleaseStatus
	ErrorMessage string
	DurationSecs int64
	StartedAt    time.Time
	CompletedAt  time.Time
}

// Controller 编排 Helm release 的生命周期。
type Controller struct {
	helm     *HelmClient
	reporter StatusReporter
	cfg      *config.Config
	log      logr.Logger

	// requests 是待处理发布请求的 channel。
	requests chan ReleaseRequest

	// mu 保护 active map 的并发访问。
	mu sync.RWMutex
	// active 跟踪进行中操作，用于去重。
	active map[string]bool

	// wg 跟踪进行中操作，用于优雅关闭。
	wg sync.WaitGroup
}

// StatusReporter 向 notification 服务上报发布状态。
type StatusReporter interface {
	ReportStatus(ctx context.Context, customerID string, result ReleaseResult) error
}

// NewController 创建一个新的 release Controller。
func NewController(helm *HelmClient, reporter StatusReporter, cfg *config.Config, log logr.Logger) *Controller {
	return &Controller{
		helm:     helm,
		reporter: reporter,
		cfg:      cfg,
		log:      log.WithName("controller"),
		requests: make(chan ReleaseRequest, 100),
		active:   make(map[string]bool),
	}
}

// Submit 将发布请求入队处理。
// 如果请求已在处理中则返回 false（去重）。
func (c *Controller) Submit(req ReleaseRequest) bool {
	key := req.RequestID + ":" + req.ReleaseName

	c.mu.Lock()
	if c.active[key] {
		c.mu.Unlock()
		c.log.Info("release already in progress, skipping",
			"request_id", req.RequestID,
			"release", req.ReleaseName,
		)
		return false
	}
	c.active[key] = true
	c.mu.Unlock()

	// 非阻塞发送: 请求队列满时拒绝
	select {
	case c.requests <- req:
		c.log.Info("release request enqueued",
			"request_id", req.RequestID,
			"chart", req.ChartName,
			"version", req.ChartVersion,
		)
		return true
	default:
		c.mu.Lock()
		delete(c.active, key)
		c.mu.Unlock()
		c.log.Info("release request queue full, rejecting",
			"request_id", req.RequestID,
		)
		return false
	}
}

// Start 在后台 goroutine 中开始处理发布请求。
func (c *Controller) Start(ctx context.Context) {
	c.log.Info("starting release controller")
	go c.processLoop(ctx)
}

// Shutdown 等待所有进行中的发布操作完成。
// 必须在传递给 Start 的 context 取消后调用。
func (c *Controller) Shutdown() {
	c.wg.Wait()
	c.log.Info("release controller stopped")
}

// processLoop 持续处理发布请求。
func (c *Controller) processLoop(ctx context.Context) {
	for {
		select {
		case req := <-c.requests:
			c.wg.Add(1)
			go func(r ReleaseRequest) {
				defer c.wg.Done()
				result := c.processRelease(ctx, r)

				// 清理去重条目
				key := r.RequestID + ":" + r.ReleaseName
				c.mu.Lock()
				delete(c.active, key)
				c.mu.Unlock()

				// 状态上报使用独立超时 context，避免被关闭信号中断，同时防止无限重试
				reportCtx, reportCancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer reportCancel()
				customerID := r.CustomerID
				if customerID == "" {
					customerID = "unknown"
				}
				if err := c.reporter.ReportStatus(reportCtx, customerID, result); err != nil {
					c.log.Error(err, "failed to report release status",
						"request_id", r.RequestID,
						"status", result.Status,
					)
				}
			}(req)

		case <-ctx.Done():
			c.log.Info("release controller draining, waiting for in-flight operations")
			// 暂不返回 — 让 Shutdown() 处理 WaitGroup
			return
		}
	}
}

// processRelease 执行完整的发布状态机。
func (c *Controller) processRelease(ctx context.Context, req ReleaseRequest) ReleaseResult {
	result := ReleaseResult{
		RequestID:    req.RequestID,
		ChartName:    req.ChartName,
		ChartVersion: req.ChartVersion,
		StartedAt:    time.Now(),
		Status:       StatusPending,
	}

	log := c.log.WithValues(
		"request_id", req.RequestID,
		"chart", req.ChartName,
		"version", req.ChartVersion,
	)

	// 默认值处理
	namespace := req.Namespace
	if namespace == "" {
		namespace = c.cfg.Helm.DefaultNamespace
	}

	releaseName := req.ReleaseName
	if releaseName == "" {
		releaseName = req.ChartName
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = int64(c.cfg.Helm.UpgradeTimeout.Seconds())
	}

	// 步骤 1: 检查版本是否已部署
	if deployed, err := c.helm.IsVersionDeployed(ctx, releaseName, namespace, req.ChartVersion); err == nil && deployed {
		result.Status = StatusSucceeded
		result.CompletedAt = time.Now()
		result.DurationSecs = int64(time.Since(result.StartedAt).Seconds())
		log.Info("version already deployed, skipping")
		return result
	}

	// 步骤 2: 从 Harbor OCI 拉取 chart
	result.Status = StatusPullingChart
	log.Info("pulling chart")

	chartPath, err := c.helm.PullChart(ctx, req.ChartURL, req.ChartVersion, "/tmp/helm-charts")
	if err != nil {
		result.Status = StatusPullFailed
		result.ErrorMessage = fmt.Sprintf("pull chart: %v", err)
		result.CompletedAt = time.Now()
		result.DurationSecs = int64(time.Since(result.StartedAt).Seconds())
		log.Error(err, "failed to pull chart")
		return result
	}

	// 步骤 3: 执行 helm upgrade
	result.Status = StatusUpgrading
	log.Info("upgrading release",
		"release", releaseName,
		"namespace", namespace,
		"chart_path", chartPath,
	)

	values := buildValuesOverrides(req.Images)

	upgradeOpts := UpgradeOptions{
		ChartPath:       chartPath,
		ReleaseName:     releaseName,
		Namespace:       namespace,
		Values:          values,
		Timeout:         timeout,
		RollbackOnFail:  c.cfg.Helm.Atomic,
		Wait:            c.cfg.Helm.Wait,
		CreateNamespace: c.cfg.Helm.CreateNamespace,
		MaxHistory:      c.cfg.Helm.MaxHistory,
	}

	_, upgradeErr := c.helm.Upgrade(ctx, upgradeOpts)

	if upgradeErr != nil {
		result.Status = StatusUpgradeFailed
		result.ErrorMessage = fmt.Sprintf("upgrade: %v", upgradeErr)
		log.Error(upgradeErr, "upgrade failed")

		if c.cfg.Helm.Atomic {
			// Install 的 RollbackOnFailure 自动处理回滚。
			// Upgrade 没有 RollbackOnFailure，atomic 通过手动 rollback 模拟。
			result.Status = StatusRolledBack
		} else {
			log.Info("attempting manual rollback")
			result.Status = StatusRollingBack
			if rollErr := c.helm.Rollback(ctx, releaseName, namespace); rollErr != nil {
				result.Status = StatusRollbackFailed
				result.ErrorMessage = fmt.Sprintf("upgrade: %v; rollback: %v", upgradeErr, rollErr)
				log.Error(rollErr, "rollback also failed")
			} else {
				result.Status = StatusRolledBack
				result.ErrorMessage = fmt.Sprintf("upgrade failed, rolled back: %v", upgradeErr)
				log.Info("rollback successful")
			}
		}
	} else {
		result.Status = StatusSucceeded
		log.Info("upgrade successful")
	}

	result.CompletedAt = time.Now()
	result.DurationSecs = int64(time.Since(result.StartedAt).Seconds())

	return result
}

// buildValuesOverrides converts image map to Helm values structure.
func buildValuesOverrides(images map[string]string) map[string]any {
	values := make(map[string]any)
	for component, tag := range images {
		values[component] = map[string]any{
			"imageTag": tag,
		}
	}
	return values
}

// MapStatusToProto 将内部 ReleaseStatus 映射到 proto 枚举。
func MapStatusToProto(s ReleaseStatus) releasev1.ReleaseStatus {
	switch s {
	case StatusPending:
		return releasev1.ReleaseStatus_RELEASE_STATUS_PENDING
	case StatusPullingChart:
		return releasev1.ReleaseStatus_RELEASE_STATUS_PULLING_CHART
	case StatusUpgrading:
		return releasev1.ReleaseStatus_RELEASE_STATUS_UPGRADING
	case StatusSucceeded:
		return releasev1.ReleaseStatus_RELEASE_STATUS_SUCCEEDED
	case StatusPullFailed, StatusUpgradeFailed:
		return releasev1.ReleaseStatus_RELEASE_STATUS_FAILED
	case StatusRollingBack:
		return releasev1.ReleaseStatus_RELEASE_STATUS_ROLLING_BACK
	case StatusRolledBack:
		return releasev1.ReleaseStatus_RELEASE_STATUS_ROLLED_BACK
	case StatusRollbackFailed:
		return releasev1.ReleaseStatus_RELEASE_STATUS_ROLLBACK_FAILED
	default:
		return releasev1.ReleaseStatus_RELEASE_STATUS_UNSPECIFIED
	}
}

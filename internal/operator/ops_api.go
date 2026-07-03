// Package operator 实现 release-operator 运维管理 gRPC API (OperatorService)。
//
// 提供以下远程运维能力:
//   - GetOperatorStatus:   查询 operator 运行状态和版本
//   - ListReleases:        列出管理的所有 Helm releases
//   - RollbackRelease:     强制回滚 release 到指定版本
//   - GetCertificateInfo:  查询当前 mTLS 证书的到期时间和指纹
//   - UpdateConfiguration: 热更新 Harbor/Helm 配置参数
//
// 所有 API 通过 mTLS 保护，仅 release-manager 可调用。
package operator

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"

	"helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/release"

	releasev1 "github.com/ndzuki/release-manager/api/gen/release/v1"
)

// operatorManagementServer 实现 OperatorServiceServer 接口。
type operatorManagementServer struct {
	releasev1.UnimplementedOperatorServiceServer
	server    *Server
	log       logr.Logger
	startTime time.Time
}

func timeNow() time.Time { return time.Now() }

// =============================================================================
// GetOperatorStatus — 查询 operator 自身状态
// =============================================================================

func (s *operatorManagementServer) GetOperatorStatus(ctx context.Context, req *releasev1.GetOperatorStatusRequest) (*releasev1.GetOperatorStatusResponse, error) {
	s.server.controller.mu.RLock()
	activeCount := len(s.server.controller.active)
	s.server.controller.mu.RUnlock()

	queued := len(s.server.controller.requests)

	return &releasev1.GetOperatorStatusResponse{
		Version:              "1.0.0",
		HelmVersion:          "helm (installed)",
		UptimeSeconds:        int64(time.Since(s.startTime).Seconds()),
		ActiveReleases:       int32(activeCount),
		QueuedReleases:       int32(queued),
		CustomerId:           s.server.customerID,
		NotificationEndpoint: s.server.cfg.NotificationEndpoint,
	}, nil
}

// =============================================================================
// ListReleases — 列出管理的所有 Helm releases
// =============================================================================

func (s *operatorManagementServer) ListReleases(ctx context.Context, req *releasev1.ListOperatorReleasesRequest) (*releasev1.ListOperatorReleasesResponse, error) {
	namespace := req.Namespace
	if namespace == "" {
		namespace = s.server.cfg.Helm.DefaultNamespace
	}

	rels, err := s.server.controller.helm.ListReleases(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}

	var releases []*releasev1.OperatorRelease
	for _, rel := range rels {
		acc, err := release.NewAccessor(rel)
		if err != nil {
			s.log.V(1).Info("skipping release, accessor error", "error", err)
			continue
		}
		chartAcc, err := chart.NewAccessor(acc.Chart())
		if err != nil {
			continue
		}
		meta := chartAcc.MetadataAsMap()
		version, _ := meta["version"].(string)

		releases = append(releases, &releasev1.OperatorRelease{
			Name:         acc.Name(),
			Namespace:    acc.Namespace(),
			Chart:        chartAcc.Name(),
			ChartVersion: version,
			Status:       acc.Status(),
			Revision:     int32(acc.Version()),
			Updated:      acc.DeployedAt().Format(time.RFC3339),
		})
	}

	return &releasev1.ListOperatorReleasesResponse{Releases: releases}, nil
}

// =============================================================================
// RollbackRelease — 强制回滚 release
// =============================================================================

func (s *operatorManagementServer) RollbackRelease(ctx context.Context, req *releasev1.RollbackReleaseRequest) (*releasev1.RollbackReleaseResponse, error) {
	if req.ReleaseName == "" {
		return &releasev1.RollbackReleaseResponse{
			Success: false,
			Message: "release_name is required",
		}, nil
	}

	namespace := req.Namespace
	if namespace == "" {
		namespace = s.server.cfg.Helm.DefaultNamespace
	}

	s.log.Info("manual rollback via API",
		"release", req.ReleaseName,
		"namespace", namespace,
		"target_revision", req.Revision,
	)

	if err := s.server.controller.helm.Rollback(ctx, req.ReleaseName, namespace); err != nil {
		return &releasev1.RollbackReleaseResponse{
			Success: false,
			Message: fmt.Sprintf("rollback failed: %v", err),
		}, nil
	}

	return &releasev1.RollbackReleaseResponse{
		Success: true,
		Message: "rollback completed",
	}, nil
}

// =============================================================================
// GetCertificateInfo — 查询当前 mTLS 证书信息
// =============================================================================

func (s *operatorManagementServer) GetCertificateInfo(ctx context.Context, req *releasev1.GetCertificateInfoRequest) (*releasev1.GetCertificateInfoResponse, error) {
	cert, err := loadCertFromFile(s.server.cfg.TLS.CertFile)
	if err != nil {
		return nil, fmt.Errorf("load current certificate: %w", err)
	}

	daysUntilExpiry := int64(time.Until(cert.NotAfter).Hours() / 24)

	return &releasev1.GetCertificateInfoResponse{
		Fingerprint:     certFingerprint(cert),
		Subject:         cert.Subject.CommonName,
		NotAfter:        cert.NotAfter.Format(time.RFC3339),
		DaysUntilExpiry: daysUntilExpiry,
		Issuer:          cert.Issuer.CommonName,
	}, nil
}

// =============================================================================
// UpdateConfiguration — 热更新运行配置
// =============================================================================

func (s *operatorManagementServer) UpdateConfiguration(ctx context.Context, req *releasev1.UpdateConfigurationRequest) (*releasev1.UpdateConfigurationResponse, error) {
	cfg := s.server.cfg
	var changed []string

	if req.HarborUrl != nil {
		cfg.Harbor.URL = *req.HarborUrl
		changed = append(changed, "harbor_url")
	}
	if req.HarborUsername != nil {
		cfg.Harbor.Username = *req.HarborUsername
		changed = append(changed, "harbor_username")
	}
	if req.HarborPassword != nil {
		cfg.Harbor.Password = *req.HarborPassword
		changed = append(changed, "harbor_password")
	}
	if req.HelmUpgradeTimeoutSeconds != nil {
		cfg.Helm.UpgradeTimeout = time.Duration(*req.HelmUpgradeTimeoutSeconds) * time.Second
		changed = append(changed, "helm_upgrade_timeout")
	}
	if req.HelmMaxHistory != nil {
		cfg.Helm.MaxHistory = int(*req.HelmMaxHistory)
		changed = append(changed, "helm_max_history")
	}
	if req.HelmAtomic != nil {
		cfg.Helm.Atomic = *req.HelmAtomic
		changed = append(changed, "helm_atomic")
	}

	s.log.Info("configuration updated via API", "changed_fields", changed)

	return &releasev1.UpdateConfigurationResponse{
		Accepted:      true,
		Message:       fmt.Sprintf("updated %d fields", len(changed)),
		ChangedFields: changed,
	}, nil
}

// =============================================================================
// 辅助函数
// =============================================================================

func loadCertFromFile(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cert file: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM from %s", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

func certFingerprint(cert *x509.Certificate) string {
	hash := sha256.Sum256(cert.Raw)
	return fmt.Sprintf("%x", hash)
}

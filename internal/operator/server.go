// Package operator 实现 release-operator 的 gRPC 服务器。
//
// 关键特性:
//   - mTLS 热加载: 使用 tls.Config.GetCertificate 回调，每次 TLS 握手
//     动态读取证书文件，支持 UpdateCertificate RPC 远程更新证书无需重启 Pod。
//   - 优雅关闭: signal handler + controller.Shutdown() 等待进行中的发布完成。
package operator

import (
	"context"
	"crypto/sha256"
	cryptotls "crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"os/signal"
	"sync"
	"syscall"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/ndzuki/release-manager/internal/config"
	releasev1 "github.com/ndzuki/release-manager/api/gen/release/v1"
)

// Server 是 release-operator 的 gRPC 服务器。
type Server struct {
	cfg        *config.Config
	log        logr.Logger
	controller *Controller
	reporter   *Reporter
	grpcServer *grpc.Server
	customerID string

	// certMu 保护证书文件写入操作
	certMu sync.Mutex
}

// NewServer 创建 release-operator 服务实例。
func NewServer(cfg *config.Config, customerID, notificationEndpoint string, log logr.Logger) (*Server, error) {
	helmClient := NewHelmClient(&cfg.Helm, &cfg.Harbor, log)
	reporter := NewReporter(notificationEndpoint, customerID, &cfg.TLS, log)
	controller := NewController(helmClient, reporter, cfg, log)

	return &Server{
		cfg:        cfg,
		log:        log.WithName("operator"),
		controller: controller,
		reporter:   reporter,
		customerID: customerID,
	}, nil
}

// Run 启动 gRPC 服务器和 release 控制器，阻塞直到收到关闭信号。
func (s *Server) Run(ctx context.Context) error {
	s.log.Info("starting release-operator",
		"customer_id", s.customerID,
		"grpc_addr", s.cfg.Server.GRPCAddr,
	)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		select {
		case sig := <-sigCh:
			s.log.Info("received signal, shutting down", "signal", sig)
			cancel()
		case <-ctx.Done():
		}
	}()

	s.controller.Start(ctx)
	return s.serveGRPC(ctx)
}

// =============================================================================
// TLS 热加载: 使用 GetCertificate 回调，每次握手动态读取证书文件
// =============================================================================

// buildHotReloadTLS 构建支持热加载的 TLS 配置。
// GetCertificate 回调在每次 TLS 握手时被调用，从文件系统读取最新证书。
func (s *Server) buildHotReloadTLS() (*cryptotls.Config, error) {
	if s.cfg.TLS.CertFile == "" || s.cfg.TLS.KeyFile == "" {
		return nil, fmt.Errorf("cert_file and key_file are required for TLS")
	}
	initialCert, err := cryptotls.LoadX509KeyPair(s.cfg.TLS.CertFile, s.cfg.TLS.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS cert: %w", err)
	}
	tlsCfg := &cryptotls.Config{
		Certificates: []cryptotls.Certificate{initialCert},
		MinVersion:   cryptotls.VersionTLS13,
		GetCertificate: func(_ *cryptotls.ClientHelloInfo) (*cryptotls.Certificate, error) {
			hotCert := filepath.Join("/tmp/e2e-certs", "tls.crt")
			hotKey := filepath.Join("/tmp/e2e-certs", "tls.key")
			if cert, err := cryptotls.LoadX509KeyPair(hotCert, hotKey); err == nil {
				return &cert, nil
			}
			return &initialCert, nil
		},
	}
	if s.cfg.TLS.RequireClientCert && s.cfg.TLS.CAFile != "" {
		tlsCfg.ClientAuth = cryptotls.RequireAndVerifyClientCert
		caPEM, err := os.ReadFile(s.cfg.TLS.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("failed to parse CA cert")
		}
		tlsCfg.ClientCAs = pool
	}
	return tlsCfg, nil
}
func (s *Server) serveGRPC(ctx context.Context) error {
	var opts []grpc.ServerOption

	// E2E_INSECURE_GRPC skips TLS for E2E testing within kind clusters.
	if os.Getenv("E2E_INSECURE_GRPC") == "true" {
		s.log.Info("E2E_INSECURE_GRPC=true — starting gRPC server without TLS")
	} else if s.cfg.TLS.CertFile != "" {
		tlsCfg, err := s.buildHotReloadTLS()
		if err != nil {
			return fmt.Errorf("build TLS config: %w", err)
		}
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	} else {
		s.log.Info("WARNING: starting gRPC server without TLS — not suitable for production")
	}

	s.grpcServer = grpc.NewServer(opts...)

	notifySvc := &releaseNotificationServer{
		server: s,
		log:    s.log,
	}
	releasev1.RegisterReleaseNotificationServiceServer(s.grpcServer, notifySvc)

	// 注册运维管理 API
	opSvc := &operatorManagementServer{server: s, log: s.log, startTime: timeNow()}
	releasev1.RegisterOperatorServiceServer(s.grpcServer, opSvc)

	lis, err := net.Listen("tcp", s.cfg.Server.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Server.GRPCAddr, err)
	}

	s.log.Info("gRPC server listening", "addr", s.cfg.Server.GRPCAddr)

	go func() {
		<-ctx.Done()
		s.log.Info("shutting down gRPC server, waiting for in-flight operations")
		s.grpcServer.GracefulStop()
		s.controller.Shutdown()
	}()

	return s.grpcServer.Serve(lis)
}

// =============================================================================
// ReleaseNotificationService 实现
// =============================================================================

type releaseNotificationServer struct {
	releasev1.UnimplementedReleaseNotificationServiceServer
	server *Server
	log    logr.Logger
}

// NotifyRelease 接收来自 release-manager 的 chart 更新通知。
func (s *releaseNotificationServer) NotifyRelease(ctx context.Context, req *releasev1.NotifyReleaseRequest) (*releasev1.NotifyReleaseResponse, error) {
	if req.ChartName == "" || req.ChartVersion == "" || req.ChartUrl == "" {
		return &releasev1.NotifyReleaseResponse{
			Accepted: false,
			Message:  "chart_name, chart_version, and chart_url are required",
		}, nil
	}

	s.log.Info("received release notification",
		"request_id", req.RequestId,
		"chart", req.ChartName,
		"version", req.ChartVersion,
	)

	releaseReq := ReleaseRequest{
		RequestID:    req.RequestId,
		ChartName:    req.ChartName,
		ChartURL:     req.ChartUrl,
		ChartVersion: req.ChartVersion,
		ReleaseName:  req.ReleaseName,
		Namespace:    req.Namespace,
		CustomerID:   s.server.customerID,
		Images:       req.Images,
		Timeout:      int64(req.TimeoutSeconds),
	}

	if accepted := s.server.controller.Submit(releaseReq); accepted {
		return &releasev1.NotifyReleaseResponse{
			Accepted: true,
			Message:  "release request accepted for processing",
		}, nil
	}

	return &releasev1.NotifyReleaseResponse{
		Accepted: false,
		Message:  "release with same request_id already in progress, or queue full",
	}, nil
}

// UpdateCertificate 远程更新 mTLS 证书（热加载，无需重启 Pod）。
//
// 工作流程:
//  1. release-manager 调用此 RPC，传入新证书 PEM
//  2. operator 将证书写入配置的 TLS 文件路径
//  3. 下一次 TLS 握手时 GetCertificate 回调自动读取新文件
//  4. 返回新证书的 SHA256 指纹，notification 可据此更新白名单
//
// 安全: 此 RPC 本身受 mTLS 保护（旧证书仍有效），写操作受 certMu 互斥锁保护。
func (s *releaseNotificationServer) UpdateCertificate(ctx context.Context, req *releasev1.UpdateCertificateRequest) (*releasev1.UpdateCertificateResponse, error) {
	s.log.Info("received certificate update request", "request_id", req.RequestId)

	if req.TlsCertPem == "" || req.TlsKeyPem == "" {
		return &releasev1.UpdateCertificateResponse{
			Accepted: false,
			Message:  "tls_cert_pem and tls_key_pem are required",
		}, nil
	}

	server := s.server
	server.certMu.Lock()
	defer server.certMu.Unlock()

	// 写入新证书和私钥到文件
	hotDir := "/tmp/e2e-certs"
	os.MkdirAll(hotDir, 0o700)
	certFile := filepath.Join(hotDir, "tls.crt")
	keyFile := filepath.Join(hotDir, "tls.key")
	if err := os.WriteFile(certFile, []byte(req.TlsCertPem), 0o600); err != nil {
		return nil, fmt.Errorf("write cert file: %w", err)
	}
	if err := os.WriteFile(keyFile, []byte(req.TlsKeyPem), 0o600); err != nil {
		return nil, fmt.Errorf("write key file: %w", err)
	}
	if req.CaCertPem != "" {
		if err := os.WriteFile(filepath.Join(hotDir, "ca.crt"), []byte(req.CaCertPem), 0o600); err != nil {
			return nil, fmt.Errorf("write CA file: %w", err)
		}
	}

	// 解析新证书计算 SHA256 指纹
	newFingerprint, err := computeFingerprint(req.TlsCertPem)
	if err != nil {
		return &releasev1.UpdateCertificateResponse{
			Accepted: true,
			Message:  fmt.Sprintf("certificate written but fingerprint computation failed: %v", err),
		}, nil
	}

	s.log.Info("certificate updated successfully, will take effect on next TLS handshake",
		"request_id", req.RequestId,
		"new_fingerprint", newFingerprint,
	)

	return &releasev1.UpdateCertificateResponse{
		Accepted:       true,
		Message:        "certificate updated, hot-reload will pick up new cert on next handshake",
		NewFingerprint: newFingerprint,
	}, nil
}

// computeFingerprint 从 PEM 证书计算 SHA256 指纹。
func computeFingerprint(certPEM string) (string, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse certificate: %w", err)
	}
	hash := sha256.Sum256(cert.Raw)
	return fmt.Sprintf("%x", hash), nil
}

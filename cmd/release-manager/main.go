// Package main 是 release-manager 中心通知服务的入口。
//
// release-manager 是 Release Manager 系统的中心枢纽，负责:
//   - 接收 Harbor webhook 通知（PUSH_HELMCHART 事件）
//   - 管理客户白名单（REST API + gRPC）
//   - 向客户集群的 release-operator 转发更新通知
//   - 收集更新结果并通过钉钉通知运维团队
//
// @title           Release Notification API
// @version         1.0.0
// @description     Release Manager 系统中心通知服务 — 接收 Harbor webhook，管理客户白名单，转发更新通知。
// @termsOfService  https://example.com/terms
//
// @contact.name    Release Ops Team
// @contact.email   ops@example.com
//
// @host            localhost:8080
// @BasePath        /api/v1
//
// @securityDefinitions.apikey ApiKeyAuth
// @in                         header
// @name                       X-API-Key
// @description                管理 API 认证密钥。webhook 端点使用 HMAC 签名验证，不需要此认证。
//
// @tag.name 客户管理
// @tag.description 客户白名单的 CRUD 操作 — 注册、查询、更新、删除客户。
//
// @tag.name 发布管理
// @tag.description 查询发布记录的更新状态。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/manager"
	"github.com/ndzuki/release-manager/internal/pkg/log"
)

func main() {
	configPath := flag.String("config", "", "配置文件路径 (YAML)")
	flag.Parse()

	// 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// 初始化结构化日志
	logger, err := log.New(cfg.Log.Level, cfg.Log.Format, cfg.Log.Output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		os.Exit(1)
	}

	logger.Info("starting release-manager",
		"grpc_addr", cfg.Server.GRPCAddr,
		"http_addr", cfg.Server.HTTPAddr,
	)

	// 创建并运行服务
	srv, err := manager.NewServer(cfg, logger)
	if err != nil {
		logger.Error(err, "failed to create server")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Run(ctx); err != nil {
		logger.Error(err, "server exited with error")
		os.Exit(1)
	}

	logger.Info("release-manager stopped")
}

# Release Manager

[![Go Version](https://img.shields.io/badge/Go-1.26-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Helm](https://img.shields.io/badge/Helm-v4-0F1689?style=flat&logo=helm)](https://helm.sh/)
[![Vue](https://img.shields.io/badge/Vue-3.5-4FC08D?style=flat&logo=vuedotjs)](https://vuejs.org/)

**Release Manager** — 企业级 Helm Chart 自动化发布管理平台。
为私有化部署的客户 K8s 集群提供从 CI 构建到自动部署、mTLS 证书远程热更新、
监控告警的全链路运维自动化。

## 架构

```
┌───────────────────────────────────────────────────────────────────┐
│                    Release Manager (中心平台)                       │
│  ┌─────────────┐  ┌──────────┐  ┌───────────┐  ┌──────────────┐  │
│  │  Web UI     │  │ REST API │  │ gRPC API  │  │ DingTalk Bot │  │
│  │  (Vue 3)    │  │ (net/http)│  │ (mTLS)    │  │ (Markdown)   │  │
│  └─────────────┘  └──────────┘  └───────────┘  └──────────────┘  │
│  ┌────────────────────────────────────────────────────────────────┐│
│  │ Casbin RBAC · OIDC/LDAP/钉钉扫码 · Cache · Dashboard · 热更新││
│  └────────────────────────────────────────────────────────────────┘│
└──────────────────────┬────────────────────────────────────────────┘
                       │ mTLS gRPC (双向认证 + TLS 热加载)
    ┌──────────────────┼──────────────────────┐
    ▼                  ▼                      ▼
┌──────────┐    ┌──────────┐          ┌──────────┐
│customer-1│    │customer-2│   ...    │customer-N│
│release-op│    │release-op│          │release-op│
│Helm SDK  │    │Helm SDK  │          │Helm SDK  │
│K8s       │    │K8s       │          │K8s       │
└────┬─────┘    └────┬─────┘          └────┬─────┘
     │               │                    │
     └───────────────┼────────────────────┘
                     │ localhost:5000
              ┌──────▼──────┐    proxy    ┌─────────┐
              │ registry:3  │───────────→ │ Harbor  │ ← 唯一源
              │ (proxy mode)│             │ (OCI)   │
              └─────────────┘             └─────────┘
```

## 快速启动

```bash
# 1. 一键部署本地开发环境 (kind 集群 + registry proxy + release-operator)
make dev-up

# 2. 启动 release-manager (另开终端)
make dev-manager

# 3. 启动前端 (另开终端) 
make dev-web
# → 前端: http://localhost:3000
# → API:  http://localhost:8080/health
```

## 自动化链路

```
Harbor webhook (PUSH_HELMCHART)
  → release-manager (HMAC 验证)
    → gRPC NotifyRelease (mTLS)
      → release-operator
        → helm pull oci://localhost:5000/helm/xxx (registry proxy)
        → helm upgrade
        → ReportStatus (gRPC)
          → release-manager
            → DingTalk 通知（失败附原因）
```

## 核心能力

| 能力 | 描述 |
|------|------|
| **发布自动化** | CI → Harbor → webhook → manager → operator → helm upgrade |
| **Chart 定制** | 按客户分配 Chart，支持客户专属 values 覆盖，部署顺序控制 |
| **多租户** | Organization 级隔离，Casbin RBAC (admin/operator/viewer) |
| **认证** | OIDC / LDAP / 钉钉扫码 / API Key 四种登录方式 |
| **证书热更新** | gRPC `UpdateCertificate` RPC + `tls.Config.GetCertificate`，秒级生效 |
| **Registry 代理** | 客户本地 registry:3 代理到 Harbor，唯一源 + 本地缓存 |
| **监控面板** | 客户状态、版本一览、证书到期预警、发布成功率 |
| **存储** | SQLite (开发) / PostgreSQL (生产) |

## Makefile 目标

```bash
# 快速启动
make dev-up          # 一键: kind + registry proxy + operator + 证书
make dev-manager     # 本地启动 release-manager
make dev-web         # 本地启动 Vue 前端
make dev-down        # 一键清理

# 构建
make build           # go build → bin/release-operator + bin/release-manager
make test            # 单元测试 (50+ tests)
make lint            # golangci-lint

# 镜像
make image-operator  # docker build release-operator
make image-manager   # docker build release-manager

# 文档
make docs            # swag init → OpenAPI v3
make docs-serve      # Swagger UI 预览
```

## 项目结构

```
release-manager/
├── cmd/
│   ├── release-operator/         # 客户集群 operator 入口
│   └── release-manager/          # 中心管理平台入口
├── internal/
│   ├── config/                   # YAML 配置 + mTLS
│   ├── manager/                  # release-manager 核心
│   │   ├── server.go             # HTTP + gRPC
│   │   ├── webhook.go            # Harbor webhook (HMAC-SHA256)
│   │   ├── forwarder.go          # gRPC 批量转发
│   │   ├── dingtalk.go           # 钉钉通知
│   │   ├── store.go              # 持久化接口 + Memory/SQLite
│   │   ├── pgstore.go            # PostgreSQL 实现 (生产)
│   │   ├── models.go             # 多租户 & Chart 配置模型
│   │   ├── auth.go               # 认证中间件
│   │   ├── auth_providers.go     # LDAP/OIDC/钉钉/Casbin RBAC
│   │   ├── init.go               # 首次初始化 (SMTP)
│   │   ├── chart_config.go       # Chart 分配 & 监控面板
│   │   └── cache.go              # 内存缓存层
│   ├── operator/                 # release-operator 核心
│   │   ├── server.go             # gRPC + TLS 热加载
│   │   ├── controller.go         # 异步发布状态机
│   │   ├── helm.go               # Helm v4 SDK 封装
│   │   ├── reporter.go           # 状态上报客户端
│   │   └── ops_api.go            # OperatorService 运维 API
│   └── pkg/                      # 共享库
│       ├── log/                  # 结构化日志
│       ├── retry/                # 指数退避重试
│       └── tls/                  # mTLS 证书工具
├── web/                          # 前端 Vue 3
├── deployments/
│   ├── release-operator/         # operator Helm chart
│   ├── release-manager/          # manager Kustomize
│   └── registry-proxy/           # registry:3 proxy chart
├── api/proto/release/v1/         # Protobuf
├── api/gen/release/v1/           # 生成的 gRPC 代码
├── configs/                      # 配置模板
├── scripts/                      # dev-setup + CI/CD + 证书
├── docs/                         # 设计 & 部署文档
└── Makefile
```

## 文档

| 文档 | 内容 |
|------|------|
| [设计文档](docs/design.md) | 系统架构、多租户、安全设计、自动化链路 |
| [部署指南](docs/deployment.md) | 生产部署、证书轮换、Harbor 自签证书、存储配置 |
| [开发文档](docs/development.md) | 本地开发、Makefile 目标、前端启动 |
| [API 文档](api/proto/release/v1/swagger.md) | gRPC + REST API 详细说明 |

## License

Proprietary

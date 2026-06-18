# Release Manager

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Helm](https://img.shields.io/badge/Helm-v4-0F1689?style=flat&logo=helm)](https://helm.sh/)
[![Vue](https://img.shields.io/badge/Vue-3.5-4FC08D?style=flat&logo=vuedotjs)](https://vuejs.org/)

**Release Manager** — 企业级 Helm Chart 自动化发布管理平台。
为私有化部署的客户 K8s 集群提供从 CI 构建到自动部署、mTLS 证书远程热更新、
监控告警的全链路运维自动化。支持多租户、OIDC/LDAP/钉钉扫码登录。

## 架构

```
┌──────────────────────────────────────────────────────────────────┐
│                    Release Manager (中心平台)                      │
│                                                                  │
│  ┌─────────────┐  ┌──────────┐  ┌───────────┐  ┌──────────────┐ │
│  │  Web UI     │  │ REST API │  │ gRPC API  │  │ DingTalk Bot │ │
│  │  (Vue 3)    │  │ (net/http)│  │ (mTLS)    │  │ (Markdown)   │ │
│  └─────────────┘  └──────────┘  └───────────┘  └──────────────┘ │
│  ┌───────────────────────────────────────────────────────────────┐│
│  │ 多租户 · OIDC/LDAP/钉钉扫码 · Cache · Dashboard · 证书热更新 ││
│  └───────────────────────────────────────────────────────────────┘│
└──────────────────────┬───────────────────────────────────────────┘
                       │ mTLS gRPC (双向认证 + 证书热加载)
    ┌──────────────────┼──────────────────────┐
    ▼                  ▼                      ▼
┌──────────┐    ┌──────────┐          ┌──────────┐
│customer-1│    │customer-2│   ...    │customer-N│
│release-op│    │release-op│          │release-op│
│Helm SDK  │    │Helm SDK  │          │Helm SDK  │
│K8s       │    │K8s       │          │K8s       │
└──────────┘    └──────────┘          └──────────┘
```

## 快速启动 (3 条命令)

```bash
# 1. 一键部署本地开发环境 (kind + operator + 证书)
make dev-up

# 2. 启动 release-manager (另开终端)
make dev-manager

# 3. 启动前端 (另开终端)
make dev-web
# → 前端: http://localhost:3000
# → API:  http://localhost:8080/health
```

## Makefile 完整目标

### 开发工作流
```bash
make dev-up        # 一键部署: kind 集群 + ingress-nginx + 证书 + release-operator
make dev-manager   # 本地启动 release-manager (HTTP :8080, gRPC :8443)
make dev-web       # 本地启动 Vue 前端 (localhost:3000)
make dev-register  # 注册 localhost001 客户到 manager
make dev-operator  # 仅重建并部署 operator (改代码后快速验证)
make dev-down      # 清理整个开发环境
```

### 构建与测试
```bash
make tools         # 安装开发工具 (buf, swag, golangci-lint)
make proto         # 生成 protobuf 代码
make deps          # 下载 Go 依赖
make build         # 构建 release-operator + release-manager
make test          # 运行单元测试
make test-e2e      # E2E 测试 (需要 kind 集群)
make lint          # 代码静态检查
make vet           # go vet
make fmt           # 代码格式化
```

### 镜像与文档
```bash
make image-operator  # 构建 operator Docker 镜像
make image-manager   # 构建 manager Docker 镜像
make docs            # 生成 OpenAPI v3 文档 (swag init)
make docs-serve      # Docker Swagger UI 预览
make clean           # 清理构建产物
```

## 项目结构

```
release-manager/
├── cmd/
│   ├── release-operator/         # 客户集群内 operator 入口
│   └── release-manager/          # 中心管理平台入口 (swag 注解)
├── internal/
│   ├── config/                   # YAML 配置 + mTLS TLS 构建
│   ├── manager/                  # release-manager 核心
│   │   ├── server.go             # HTTP + gRPC 双协议服务
│   │   ├── webhook.go            # Harbor webhook (HMAC-SHA256)
│   │   ├── forwarder.go          # gRPC 批量转发
│   │   ├── dingtalk.go           # 钉钉通知
│   │   ├── store.go              # 持久化 (SQLite/Memory)
│   │   ├── models.go             # 多租户 & Chart 配置数据模型
│   │   ├── auth.go               # OIDC/LDAP/钉钉认证中间件
│   │   ├── chart_config.go       # Chart 分配 & 监控面板 API
│   │   └── cache.go              # 内存缓存层 (TTL + 淘汰)
│   ├── operator/                 # release-operator 核心
│   │   ├── server.go             # gRPC + TLS 热加载 (GetCertificate)
│   │   ├── controller.go         # 异步发布状态机 (8 态)
│   │   ├── helm.go               # Helm v4 SDK 封装
│   │   ├── reporter.go           # 状态上报客户端
│   │   └── ops_api.go            # OperatorService 运维管理 API
│   └── pkg/                      # 共享库
│       ├── log/                  # 结构化日志 (zap → logr)
│       ├── retry/                # 指数退避重试
│       └── tls/                  # mTLS 证书生成/验证工具
├── web/                          # 前端 (Vue 3 + TypeScript)
│   ├── src/
│   │   ├── views/                # 页面: Dashboard, Customers, Charts, Releases, Certs, Login
│   │   ├── components/           # AppLayout (侧边栏+导航)
│   │   ├── composables/         # useApi (API 客户端+认证)
│   │   ├── stores/              # Pinia auth store
│   │   └── router/              # Vue Router + 认证守卫
│   ├── package.json
│   ├── vite.config.ts
│   └── index.html
├── deployments/
│   ├── release-operator/         # operator Helm chart (一键部署 + values.schema.json)
│   └── release-manager/          # manager Kustomize (base + prod/staging overlays)
├── api/proto/release/v1/         # Protobuf + buf 配置
├── api/openapi/                  # swag 生成的 OpenAPI v3 文档
├── configs/                      # 配置模板
├── scripts/                      # 证书生成 + CI/CD + 本地开发环境
├── docs/                         # 设计 & 部署文档
└── Makefile                      # 统一构建入口
```

## 组件

| 组件 | 位置 | 描述 |
|------|------|------|
| **release-operator** | `cmd/release-operator/` | 部署在客户 K8s 集群，Helm v4 SDK `helm upgrade` |
| **release-manager** | `cmd/release-manager/` | 中心平台，webhook + 白名单 + 转发 + 钉钉 |
| **Web UI** | `web/` | Vue 3 前端，Dashboard / 客户 / Chart / 发布 / 证书管理 |

## 文档

| 文档 | 内容 |
|------|------|
| [API 文档](api/proto/release/v1/swagger.md) | gRPC + REST API 详细说明 |
| [设计文档](docs/design.md) | 系统架构、多租户、安全设计 |
| [部署指南](docs/deployment.md) | 生产部署、证书轮换、CI/CD |
| [开发文档](docs/development.md) | 本地开发、代码规范、前端启动 |

## License

Proprietary

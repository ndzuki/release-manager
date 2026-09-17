# release-manager

面向多客户 Kubernetes 集群的 **Helm 发布管理控制面**：把「发布什么、发到哪个客户的哪个集群、谁批准的、结果如何」收敛为一条可审计、可回滚、可显式收敛的执行链。

控制面负责编排、授权、审批、审计与状态权威；客户集群侧只运行一个 **Operator**，以**出站**连接回控制面，并在集群内使用 **Helm Go SDK** 执行安装/升级/回滚（运行时禁止调用命令行）。

> 需求、任务与架构决策的**权威记录**在知识库 `myNote/Projects/001-release-manager/`（Requirements / Tasks / Design / Notes）。本仓库的 `docs/` 是面向代码读者的**发布面**文档，`docs/decisions/` 是 ADR 的导出副本。

## 关键设计约束

| 约束 | 说明 |
|---|---|
| 执行边界 | 中心控制面不直接访问客户集群；Operator 出站连接 + 持久命令 Outbox 与本地重放 |
| SDK-only | 集群内执行一律走 Helm Go SDK；`make sdk-check` 静态门禁拦截 `os/exec` 类路径 |
| 单一契约面 | protobuf + Connect 单端口统一协议；Go/TS 客户端由 `buf` 生成，手写客户端不入库 |
| 数据权威 | 生产 PostgreSQL 为单一权威；SQLite 仅用于 dev/test，**schema 必须双引擎兼容** |
| 不可变输入 | ValuesRevision 不可变；Secret 只以引用形式进入执行链 |
| 一致性 | Operation 状态机 CAS + 事务 Outbox，幂等与重放语义明确 |
| 审计 | 独立、脱敏、异步入库的审计流水线，支持归档与导出 |
| 紧急变更 | 受控紧急变更必须显式收敛，不允许「发完即结束」 |

每条约束的完整决策记录见 `docs/decisions/`（ADR-000 ~ ADR-021）。

## 服务与工具

`cmd/` 下每个目录一个 `package main`。

**服务**（Makefile 变量 `SERVICES`，构建产物在 `bin/release-*`）：

| 二进制 | 职责 | dev 端口 |
|---|---|---|
| `release-orchestrator` | 发布编排：Operation 生命周期、客户/集群/发布定义、Values、紧急变更 | 8083 |
| `release-operator` | 客户集群侧 agent：mTLS 双向流、集群内 Helm 执行 | 8084 |
| `release-auth` | 认证、组织与成员、客户绑定、授权快照 | 8085 |
| `release-notifier` | 通知投递与死信 | 8086 |
| `release-api` | 审计查询/导出与只读 API | 8087 |
| `release-webhook` | Harbor webhook 入口（bundle ingress） | 8082 |

**工具**（非长期运行服务）：

| 二进制 | 用途 |
|---|---|
| `devseed` | 经正式 API 播种/重置确定性开发夹具 |
| `e2e` | 分阶段 E2E runner（`run` / `cleanup`） |
| `imagecheck` | 按可执行策略校验镜像归档 |
| `installgate` | Install SDK 门禁失败时的限时 quarantine |
| `reqcheck` | 校验原子需求文档是否满足十段模板 |
| `sdkcheck` | SDK-only 静态分析器 |
| `store-migrate` | 一次性 SQLite → PostgreSQL 数据迁移 |
| `notification-sink` | **dev-only** 通知落点服务，供本地验证投递 |

## 仓库结构

```
cmd/          每个二进制的 main 包
internal/     业务实现（45 个包；对外契约见 internal/store、internal/contracts）
api/proto/    protobuf 单一契约源；api/gen、web/src/gen 为生成产物（禁止手改）
api/kulala/   Kulala/Neovim HTTP 调试集合
configs/      各服务 dev 配置（生产配置由部署侧注入）
deploy/       本地环境（deploy/dev）、kustomize 清单、Dockerfile、夹具
migrations/   PostgreSQL 迁移（golang-migrate，编号连续）
test/         集成测试与分阶段 E2E
web/          Vue 3 + Pinia + Vite 控制台
docs/         本文档目录
```

## 快速开始（本地开发环境）

前置：Go 版本以 `go.mod` 为准（当前 `go 1.27.1`）、Docker、k3d、kubectl、`buf`；前端另需 Node（版本见 `web/package.json`）。环境脚本的前置检查以 `deploy/dev/dev.sh` 为准。

```bash
make dev-up        # 起 k3d 集群 + registry + 全部服务（容器）
make dev-seed      # 播种 canonical 开发夹具
make dev-status    # 打印状态并写 data/dev-status.json
make dev-down      # 停止（保留数据卷）
make dev-purge CONFIRM=1   # 彻底清理（不可逆）
```

控制台（本地 Vite dev server）：

```bash
cd web && npm ci && npm run dev
```

本地逐服务调试（`go run`，一次一个服务）见 `docs/dev-environment.md`。

## 开发工作流

```bash
make help          # 列出全部目标（每个目标自带说明）
make test          # go test -race ./...
make lint          # golangci-lint
make quality       # 聚合质量门禁
make sdk-check     # SDK-only 静态门禁
make check-reqs    # 原子需求模板校验
make check-licenses # 依赖许可门禁（GPL/AGPL/LGPL 等一律拒绝）
make check-docs    # 文档事实门禁（文档写下的 make 目标、仓库路径、链接、行号引用必须真实）
make lint-proto    # buf lint（契约命名与布局规则；COMMENT_* 故意不开，理由见 buf.yaml）
make proto         # buf generate（生成 Go/TS 契约代码）
```

需要真实 PostgreSQL 的测试带 `//go:build integration` 标签，并依赖 `POSTGRES_TEST_DSN`；未设置时自动 skip。完整分层说明见 `docs/testing.md`。

## 文档

| 文档 | 内容 |
|---|---|
| `docs/architecture.md` | 系统组成、执行边界、数据权威与交付波次 |
| `docs/dev-environment.md` | 本地环境生命周期、夹具与逐服务调试 |
| `docs/testing.md` | 单元/集成/E2E 分层、命令矩阵与 CI 关系 |
| `docs/decisions/` | ADR-000 ~ ADR-021 导出副本与索引 |
| `docs/glossary.md` | 领域词汇表（含禁用说法） |
| `docs/api.md` | Connect 契约面：服务、RPC、认证与错误约定 |
| `docs/configuration.md` | 各服务配置键、层级与默认值 |
| `docs/cli.md` | `cmd/*` 全部可执行文件的 flag、退出码与调用方式 |
| `docs/runbook.md` | 运维手册：故障定位与处置步骤 |
| `docs/observability.md` | 日志、指标与健康检查的可观测面 |
| `docs/http-collections.md` | `api/kulala/*.http` 调试集合与当前可用性 |
| `docs/dependencies.md` | 依赖许可清单（生成产物，`make check-licenses` 校验） |
| `docs/atomic-requirement-template.md` | 原子需求十段模板（`make check-reqs` 校验） |
| `.github/SECRETS.md` | CI 需要的 secret / variable 清单与配置步骤 |
| `SECURITY.md` | 漏洞披露渠道与机密处理约定 |
| `CHANGELOG.md` | 已合入 `main` 的变更按月归纳（首个 tag 前不用版本号） |
| `CODE_OF_CONDUCT.md` | 协作行为约定与报告渠道 |
| `CONTRIBUTING.md` | 交付模型、分支/提交/PR 规范与门禁 |
| `AGENTS.md` | 面向自动化代理的项目约束 |

目录级说明另有 `web/README.md`（前端构建与 feature flag）、`deploy/README.md`（k3d 与 kustomize 布局）、`migrations/README.md`（迁移编号与双引擎对齐）、`test/e2e/README.md`（分阶段 E2E runner）。

## 许可

Apache License 2.0，见 `LICENSE`。第三方依赖的许可证清单见 `docs/dependencies.md`，其中自带 NOTICE 的上游声明汇总在根目录 `NOTICE`。

# deploy/ — 部署清单、dev 环境生命周期与镜像构建

本目录包含：k3d 多集群 dev 环境的唯一生命周期模块（`dev/dev.sh`）、各服务的 Dockerfile（`docker/`、`fixtures/`）、Kustomize 清单（`kustomize/`）与 registry 配置（`k3d/`）。
dev 环境的操作细节（前置工具、清理范围矩阵、故障排查）以 **`docs/dev-environment.md` 为权威**，本文不重复，只给结构与入口。每条结论附 `文件:行号`。

## 1. 目录结构

```
deploy/
├── dev/                       dev 环境生命周期（REQ-065）
│   ├── dev.sh                 唯一入口：up|down|seed|reset-data|status|purge（dev.sh:1927-1933）
│   ├── dev_test.go            用 fake CLI 断言错误码/锁/ownership 的 Go 测试（dev_test.go:1-4）
│   ├── lib/{errors,host,lock,ownership}.sh   错误码表、宿主预检、flock、ownership 白名单
│   └── testdata/fake-k3d.sh   dev_test 的 k3d 替身
├── docker/                    8 个服务 Dockerfile + 各自 .dockerignore
├── fixtures/                  dev 夹具工作负载：Helm chart（chart/）、静态文件 server（cmd/server/main.go）、Dockerfile
├── k3d/registries.yaml        k3d registry 配置（docker.io mirror），dev-up 建集群时以 --registry-config 注入（dev.sh:700）
└── kustomize/
    ├── base/                  namespace + 共享 Secret + PVC（base/kustomization.yaml:3-6）
    ├── dev/                   管理集群 overlay：聚合 base/postgres/redis/services + configMap/secretGenerator（dev/kustomization.yaml:3-70）
    ├── services/              6 个 Deployment/Service：webhook、orchestrator、auth、notifier、notification-sink、web（services/kustomization.yaml:3-8）
    ├── postgres/ redis/       dev 集群内 PostgreSQL / Redis（postgres 用 emptyDir，无 PVC——dev.sh:1521-1527 注释）
    └── customer-agent/        客户集群 operator agent overlay：base + c1-direct/c2-cache/c3-replicated/c4-mixed
```

## 2. dev 环境（k3d 多集群）生命周期

入口是 Makefile 转发到 `deploy/dev/dev.sh`（Makefile:106 `DEV_SCRIPT := deploy/dev/dev.sh`），锁、错误码、宿主预检、ownership 门控全部在 dev.sh 内（dev.sh:12-14 注释）：

| make 目标（存在性已核对） | dev.sh 子命令 | 语义 |
| --- | --- | --- |
| `make dev-up`（Makefile:109） | `up` | 幂等收敛完整环境 |
| `make dev-seed`（Makefile:117） | `seed` | 经正式 Connect API 写入/验证 Development Fixture |
| `make dev-status`（Makefile:125） | `status` | 写并打印 `data/dev-status.json` |
| `make dev-down`（Makefile:113） | `down` | 删除 5 个受管 k3d 集群，保留 registry 与镜像缓存（dev.sh:1535） |
| `make dev-reset-data`（Makefile:121） | `reset-data` | 双库 dump → schema 重建 → re-seed，需 `CONFIRM=1`（dev.sh:1682） |
| `make dev-purge`（Makefile:129） | `purge` | 按 ownership 白名单删除全部受管资源（含 registry、数据卷、`data/` 运行时文件），需 `CONFIRM=1`（dev.sh:1877） |

拓扑：1 个管理集群 `release-manager-control` + 4 个客户集群 `dev-customer-a-direct|a-cache|b-replicated|b-mixed`（dev.sh:43-47），k3d 托管 registry `k3d-release-manager-registry` 端口 5001（dev.sh:57-59）。

`dev-up` 的固定阶段（cmd_up，dev.sh:1456-1478）：前置电池 → 取排他锁 → 生成/复用 JWT signing key、webhook 服务令牌、dev mTLS CA（必须在 kustomize build 前就位，dev.sh:1462-1470）→ registry → 5 集群 → 逐服务构建并推送镜像（`[4/7]`，dev.sh:1075-1094，默认串行，`DEV_BUILD_PARALLELISM=2/4` 可并行）→ kustomize apply（`[5/7]`，dev.sh:1098）→ readiness → seed（`[7/7]`，dev.sh:1416-1417；enrollment 令牌未就绪时中间插入 `[6.5/7]` agents_up，dev.sh:1271-1276）。seed 分两段跑 `go run ./cmd/devseed/`（dev.sh:1400-1440）：先 `--stop-after enrollment` 产出单用令牌，再 `agents_up` 部署客户集群 agent，最后续跑 install+verify。

状态文件都在仓库根 `data/`（dev-purge 的清理清单见 dev.sh:42 `PURGE_DATA_PATHS`）：`dev-status.json`、`dev-fixture.json`、`dev-seed-progress.json`（九阶段续跑进度）、`kubeconfig.yaml` + `kubeconfigs/<cluster>.yaml`、`dev.lock`（flock，Makefile:137）、`dev-credentials.env`（Makefile:138）、`dev-jwt/`、`dev-service-tokens/`、`dev-ca/`、`dev-enrollment-tokens/`、`dev-ownership.json`、失败诊断 `diagnostics/<UTC时间戳>/`（dev.sh:119-158）、`backups/`（reset-data 的 pg_dump，dev.sh:1736-1739）。

退出码约定：`0` 成功 / `1` 操作失败（stderr 稳定错误码）/ `2` 缺 `CONFIRM=1` / `3` 环境被锁（`environment_locked`）——见 docs/dev-environment.md「生命周期命令」节与 dev.sh 的 `require_confirm`/`acquire_lock`。

清理方式：日常 `make dev-down`（保留 registry/镜像缓存以加速下次 up）；彻底清理 `make dev-purge CONFIRM=1`（破坏性，先确认）。ci profile 失败时自动 purge 并先落盘诊断（dev.sh:95-110）。**Docker 对象探测一律显式限定类型**（`docker container inspect`，dev.sh:544；network 用 `docker network inspect`），这是项目 Anti-pattern 约束的落点。

## 3. 镜像构建入口与 Dockerfile 清单

dev-up 的镜像集合固定为 8 个 service（dev.sh:1009、1023）：`webhook orchestrator operator auth notifier web fixture notification-sink`。Dockerfile 路径按 `deploy/docker/Dockerfile.<service>` 动态拼接，`fixture` 例外用 `deploy/fixtures/Dockerfile`（dev.sh:911-914）：

| 镜像 | Dockerfile | 构建入口 |
| --- | --- | --- |
| `release-webhook` | `deploy/docker/Dockerfile.webhook` | `make dev-up`（images_up） |
| `release-orchestrator` | `deploy/docker/Dockerfile.orchestrator` | 同上 |
| `release-operator` | `deploy/docker/Dockerfile.operator` | 同上；另有独立入口 `make docker-build-operator`（Makefile:497-501，build + docker save tarball）与 `make test-operator-image-sdk-only`（Makefile:503-517，用 `cmd/imagecheck` 按 `imagecheck.operator.yaml` 校验镜像，CI job test.yml:104-116） |
| `release-auth` | `deploy/docker/Dockerfile.auth` | `make dev-up` |
| `release-notifier` | `deploy/docker/Dockerfile.notifier` | `make dev-up` |
| `release-notification-sink` | `deploy/docker/Dockerfile.notification-sink` | `make dev-up` |
| `release-web` | `deploy/docker/Dockerfile.web` | `make dev-up`；两阶段：`node:24-alpine` 内 `npm ci && npm run build` → `nginx:1.27-alpine` 托管 `dist/` + `web/nginx.conf`，EXPOSE 8087（Dockerfile.web:4-15） |
| `release-fixture` | `deploy/fixtures/Dockerfile` | `make dev-up`；额外 re-tag 并 push `:dev`，因为夹具 chart 固定引用 `localhost:5001/release-fixture:dev`（dev.sh:972-983；fixtures/chart/Chart.yaml） |

`deploy/docker/Dockerfile.api`（构建 `cmd/api`，EXPOSE 8087）**未找到任何构建入口**：不在 dev.sh 的 service 列表（dev.sh:1009），Makefile 与 CI 也无引用（`grep -rn Dockerfile.api Makefile deploy/ scripts/ .github/` 无匹配）。`api` 目前只有本地进程入口 `make run-api`（Makefile:100-101）。

镜像 tag 契约：dev-up 推送 `content-sha256:<hex>` 摘要 tag，apply 前在内存中把清单里的静态 `release-<svc>:dev` 替换为摘要 tag（dev.sh:1107-1115），不落盘改文件。

## 4. 配置注入：kustomize 与 `configs/*.dev.yaml` 的关系

- **集群内 dev 的权威是 `deploy/kustomize/dev/configs/*.dev.yaml`**：`configMapGenerator` 逐服务生成 `webhook-config`、`orchestrator-config`、`auth-config`、`notifier-config`、`notification-sink-config`（deploy/kustomize/dev/kustomization.yaml:11-31），由 `deploy/kustomize/services/*.yaml` 的 Deployment 挂载。
- **仓库根 `configs/*.dev.yaml` 是本地进程配置**（`make run-webhook` 等直接 `--config configs/<svc>.dev.yaml`，Makefile:79-101），不是集群配置的副本。两者语义同源、值不同：例如 auth 的数据库，根配置是 `driver: sqlite` + `dsn: data/auth.db`（configs/auth.dev.yaml:3-4），kustomize 配置是 `driver: postgres` + `postgres://...@postgres:5432/release_manager`（deploy/kustomize/dev/configs/auth.dev.yaml:3-6）；orchestrator 的 `authorization.auth_url` 分别是 `http://localhost:8085`（configs/orchestrator.dev.yaml:6）与 `http://auth:8085`（deploy/kustomize/dev/configs/orchestrator.dev.yaml:6）。这正是「dev=SQLite / prod=PostgreSQL」双引擎约束（docs/architecture.md:121-123）在配置层的体现。
- **Secret 注入**：`secretGenerator` 从 `data/` 下的运行时分段文件取内容——JWT key pair（`data/dev-jwt/jwt-private-key.pem` 只给 auth、`jwt-public-key.pem` 只给 orchestrator，REQ-065 AC-065-01）、webhook 服务令牌（`data/dev-service-tokens/webhook-service-token`）、CI/Harbor ingress 凭据（`data/dev-service-tokens/ci-api-key`、`data/dev-service-tokens/harbor-service-token`，TASK-102）、dev mTLS CA（`data/dev-ca/ca.{key,crt}`）（deploy/kustomize/dev/kustomization.yaml:39-70）。kustomize 的 hash 后缀使轮换自动滚动消费方 Deployment；ci profile 下这些文件由环境变量瞬态物化、apply 后即删（deploy/kustomize/dev/kustomization.yaml:32-37 注释、dev.sh:81-86）。共享 dev 凭据（POSTGRES_* 等）在 `deploy/kustomize/base/secret.yaml`（deploy/kustomize/base/secret.yaml:1-10）。曾有过一个装静态 env 的 `deploy/kustomize/base/configmap.yaml`——零消费者，TASK-094 已删（见 `docs/configuration.md` §7-7）。<!-- check-docs:ignore 陈述已删除文件不存在 -->
- **客户集群 agent 配置**：`deploy/kustomize/customer-agent/*/configs/operator.dev.yaml` 随 overlay 生成（c1-direct…c4-mixed，与集群的映射在 dev.sh:1198-1203）；单用 enrollment token 与 gateway CA 两个 Secret 由 `agents_up` 命令式注入，不走 secretGenerator（deploy/kustomize/customer-agent/base/kustomization.yaml:9-13 注释、dev.sh:1330 起）。

## 5. 改部署清单后的校验方式（只读）

```bash
# 管理集群 overlay（与 dev.sh:1104 完全一致的调用形态；secretGenerator 引用
# data/ 下的文件，故必须放开 load restrictor）：
kustomize build --load-restrictor LoadRestrictionsNone deploy/kustomize/dev

# 客户集群 overlay（无运行时 secret，默认限制即可，对应 dev.sh:1308）：
kustomize build deploy/kustomize/customer-agent/c1-direct   # 或 c2-cache / c3-replicated / c4-mixed

# 生命周期模块的行为回归（fake CLI，不需要 Docker/k3d）：
go test ./deploy/dev/...
```

`kustomize` 需在 PATH（CI 安装 v5.8.1，test.yml:351、419）。`kustomize build` 只渲染不 apply，安全；**不要**用 `kubectl apply` 验证——那会改动真实集群。

## 6. 生产部署

仓库内**未找到**生产发布流水线：`.github/workflows/` 只有 `test.yml`（测试门禁）与 `sync-to-gitcode.yaml`（镜像同步），没有任何 deploy/publish job；`deploy/kustomize/` 也只有 dev 与 customer-agent 两套 overlay，无 production overlay。生产部署的实际发布流程以知识库（`myNote/Projects/001-release-manager/`）与其它文档为准，本 README 不做描述。

> 事实源：`deploy/dev/dev.sh`、`deploy/dev/dev_test.go`、`deploy/dev/probes_gate_test.go`、`deploy/kustomize/**/kustomization.yaml`、`deploy/kustomize/base/{secret,pvc}.yaml`、`deploy/kustomize/services/*.yaml`、`deploy/kustomize/customer-agent/base/kustomization.yaml`、`deploy/docker/Dockerfile.*`、`deploy/fixtures/**`、`deploy/k3d/registries.yaml`、`Makefile`、`configs/*.dev.yaml`、`.github/workflows/test.yml`、`docs/dev-environment.md`、`docs/architecture.md`、`cmd/imagecheck/main.go`

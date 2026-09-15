# 开发环境上手

本项目的本地开发环境由 `deploy/dev/dev.sh` 单一生命周期模块管理，Makefile 只做薄转发
（target + 环境变量）。锁、退出码、错误码、ownership 白名单与宿主门禁全部内聚在脚本内，
因此**排查问题时以 `deploy/dev/dev.sh` 的行为为准**，Makefile 中的 target 只提供入口。

拓扑：1 个管理集群 `release-manager-control` + 4 个客户集群（`dev-customer-a-direct`、
`dev-customer-a-cache`、`dev-customer-b-replicated`、`dev-customer-b-mixed`），
全部经 `--registry-use` 接入 k3d 托管 registry `k3d-release-manager-registry`
（宿主 `127.0.0.1:5001`，明文 HTTP、仅 loopback）。

## 前置依赖

`dev.sh up` 在创建任何资源前跑完整前置电池（`deploy/dev/lib/host.sh` 的 `preflight_up` +
`stage_preflight`）：

| 依赖 | 要求 | 失败错误码 |
| --- | --- | --- |
| 操作系统 | Linux-only | `docker_unavailable` |
| Docker | CLI 在 PATH 且 daemon 可达 | `docker_unavailable` |
| k3d | ≥ 5.8，且 `k3d version` 行可解析出 `vX.Y.Z` | `k3d_unavailable` |
| flock | util-linux `flock` 在 PATH（环境锁） | `docker_unavailable` |
| CPU | ≥ 4 核 | `host_memory_insufficient` |
| 磁盘 | `data/` 所在文件系统可用空间 ≥ 20 GiB | `host_disk_insufficient` |
| 内存 | `MemAvailable` ≥ 12 GiB | `host_memory_insufficient` |
| 宿主端口 | 8082–8087 空闲 | `port_conflict` |

两条检查是**续跑感知**的：当任一受管集群已存在时，内存门禁与端口门禁**跳过**——5 个 k3d
集群自身的负载均衡器按设计占用 8082–8087，中断后重跑 `dev-up` 不应被自己已提交的占用判失败。

以下工具不在 `preflight_up` 电池内，但会被后续阶段调用，缺失时表现为**阶段级失败而非前置失败**：

| 工具 | 用途 | 缺失时的表现 |
| --- | --- | --- |
| `kubectl` | `ctl_kubectl()` 的 apply/readiness/诊断收集；E2E 的临时 port-forward | 阶段失败；`collect_diagnostics` 跳过并打印 `diagnostics skipped (kubectl missing)` |
| `kustomize` | `kustomize_apply()` 构建 `deploy/kustomize/dev` | `kustomize_build_failed` |
| `jq` | `make e2e-env-config`、`dev-status` 的 `fixture_entity_counts`、`smoke.sh` | `e2e-env-config` 退出码 2；`dev-status` 的 `fixture_entities` 退化为 `{}` 并告警 |
| `pg_dump` / `pg_restore` | 仅 `dev-reset-data` 的双库安全网 | `docker_unavailable` |
| Go 工具链 | 构建服务镜像、`go run ./cmd/devseed/ -print-fixture-version` | `FIXTURE_VERSION` 回退到脚本内置默认值 |
| `buf` | `make proto` 生成 protobuf 代码（缺失时 target 自动 `go install ...@latest`） | proto 生成失败 |

Go 版本以 `go.mod` 为准（当前声明 `go 1.26.4`）；CI 使用 `actions/setup-go` 的 `1.26`。
k3d 必须使用 release 二进制而非 `go install` 构建：后者报告 `v5-dev`，会让版本解析误读
随后的 k3s 版本行并报「k3d 过旧」。其余工具版本要求以 `deploy/dev/dev.sh` 的前置检查为准。

## 生命周期命令

| Makefile target | `dev.sh` 子命令 | 语义 | 锁 | `CONFIRM=1` |
| --- | --- | --- | --- | --- |
| `make dev-up` | `up` | 幂等收敛完整环境（含 install 阶段 seed） | 排他 | — |
| `make dev-seed` | `seed` | 经正式 Connect API 写入/验证 canonical fixture | 排他 | — |
| `make dev-status` | `status` | 写并打印 `data/dev-status.json` | 共享 | — |
| `make dev-down` | `down` | 删除 5 个受管集群，保留 registry 与镜像缓存 | 排他 | — |
| `make dev-reset-data` | `reset-data` | 双库 dump → schema 重建 → 4 客户集群重建 → 强制 re-seed | 排他 | 必需 |
| `make dev-purge` | `purge` | 按 ownership 白名单删除全部受管资源（含 registry）+ `data/` 运行时文件 | 排他 | 必需 |

`dev-up` 的收敛顺序固定：前置电池 → 取排他锁 → 生成/复用 JWT signing key、webhook 服务令牌、
dev mTLS CA（三者必须在 kustomize build 前就位，`secretGenerator` 会读取它们）→ registry →
5 个集群 → 逐服务构建并推送镜像 → kustomize apply → readiness → seed。

幂等性：`dev-up` 全程幂等——镜像 tag = `content-sha256:<hex>`，registry 已有相同 manifest
digest 时跳过 build/push；`dev-seed` 通过 `data/dev-seed-progress.json` 的九阶段进度续跑
（从首个非 `committed` 阶段开始）；`dev-status`、`dev-down`、`dev-purge` 对已删除资源不报错。

`dev-seed` 自身**不生成**密钥：local profile 下缺少 JWT signing key、服务令牌或 mTLS CA 时
直接以 `service_unhealthy` 失败并提示先跑 `make dev-up`。

退出码：`0` 成功 / `1` 操作失败（stderr 带稳定错误码）/ `2` 缺少 `CONFIRM=1` / `3` 环境被锁
（`environment_locked`，锁文件 `data/dev.lock`）。

## `dev-down` 与 `dev-purge` 的清理范围差异

构建 D-017 与 D-019 的拆除契约后，两者的差别是「是否连 registry、镜像缓存与 `data/` 运行时
文件一起删除」：

| 资源 | `make dev-down` | `make dev-purge` |
| --- | --- | --- |
| 5 个受管 k3d 集群 | 删除 | 删除 |
| `data/kubeconfigs/<cluster>.yaml` 与合并的 `data/kubeconfig.yaml` | 删除已删集群条目；无集群剩余时删除合并文件 | 删除 |
| `data/dev-ownership.json` 的 `k3d_clusters[]` / `docker_networks[]` 条目 | 移除 | 移除 |
| `data/dev-ownership.json` 的 registry/profile/`created_at` 元数据 | 保留 | 随文件一并删除 |
| 集群网络 | 先 `docker network disconnect` 断开 registry，再删除网络 | 同左 |
| k3d registry 容器与其 `/var/lib/registry` 数据卷（镜像缓存） | **保留** | 删除 |
| 集群绑定种子状态：`data/dev-seed-progress.json`、`data/dev-fixture.json`、`data/dev-enrollment-tokens/` | 删除（D-019） | 删除 |
| 其余 `data/` 运行时文件：`dev-credentials.env`、`dev-trust-root/`、`dev-jwt/`、`dev-service-tokens/`、`dev-ca/`、`diagnostics/`、`backups/` 等 | 保留 | 删除 |
| `data/archive/`（fixture 版本归档） | 保留 | **保留** |

拆除顺序（D-017，真实 smoke 发现）：删集群 → 清理对应 kubeconfig 与 ownership 条目 → 清理
集群绑定种子状态 → 断开 registry 与集群网络的连接 → 删除网络并移除 `docker_networks[]` 条目
→（仅 `dev-purge`）删除 registry 容器、数据卷与 `data/` 运行时文件。

两条规则解释了这个顺序：

- k3d 的 `registry_up` 会把 registry 容器接入**每个**集群网络；registry 仍连接时
  `docker network rm` 必然失败并残留网络，因此必须先 `disconnect`。已删除的集群/网络不报错，
  断开不存在的连接视为成功。
- 集群内 PostgreSQL 为 emptyDir（无持久卷），数据随 `dev-down` 删集群即清空。若
  `dev-seed-progress.json` / `dev-fixture.json` / `dev-enrollment-tokens/` 残留，下次
  `dev-up` 会读旧进度跳过重建，报 `fixture_conflict`（identity drift）或 `stage_unavailable`
  （no operator）。因此种子状态与集群同生命周期，随集群销毁一并清理；跨 `dev-down` 的恢复
  手段统一为重新 `make dev-up` + `make dev-seed`。

## 开发夹具（Development Fixture）

canonical fixture 的权威实现在 `internal/devfixture`（`Run(ctx, cfg) → Manifest`），
九阶段为私有实现，顺序固定：

```
identity → routing → accounts → trust → bundle → values → enrollment → install → verify
```

- **唯一写入路径是正式 Connect API**（生成客户端 + 稳定 `Idempotency-Key`
  `devseed-<phase>-<logical-key>`）。禁止数据库直写与测试旁路；创建请求不传业务主键，实体以
  稳定 logical key 查询匹配，服务端生成的 ID 落 `data/dev-fixture.json`。
- `fixture_version` 的权威 = devseed 内置常量（`go run ./cmd/devseed/ -print-fixture-version`）。
  `dev-up`/`dev-seed` **不会**自动递增版本；仅实体结构/语义变更时由维护者递增。
- 每阶段原子写 `data/dev-seed-progress.json`；已 `committed` 阶段只做存在性 + logical key
  一致性检查。漂移 → `fixture_conflict`（退出码 1）。
- **重建边界**：唯一恢复手段是 `make dev-reset-data CONFIRM=1`（maintenance 停写 → `pg_dump`
  双库安全网 → migrate down -all/up → 重建 4 个客户集群 → 强制 re-seed；失败即 `pg_restore`
  双库并把环境标记 partial）。不做逐实体修补，也不自动修复漂移。
- 旧版本进度归档到 `data/archive/archive-<ISO8601>-<fixture_version>.json`，保留最近 3 代；
  `dev-purge` 与 `dev-reset-data` 都不清理 `data/archive/`。
- 消费面：`data/dev-fixture.json`（逻辑键 → 服务端 ID）与 `data/dev-status.json`
  （`environment_id`、端点、集群状态、fixture 实体计数、restart 目标）。
- `dev-reset-data` 需要宿主 `pg_dump`/`pg_restore`。脚本已知宿主工具（PostgreSQL 18.x）与
  集群内 `postgres:16` 的版本偏差，restore 时会剔除 dump 头部的 `SET transaction_timeout = 0;`
  这一 PG17+ 专属 GUC 后再以 `psql` 重放。

## 分阶段本地起服务（`dev-stage-*`）

这一组 target 是**裸进程**本地调试入口（除 `dev-stage-shared` 与 `dev-stage-full` 外，每个 target
先跑 `proto`，再用 `fuser -k` 清端口，最后 `go run`）。它们不需要 k3d 环境。

| target | 服务与端口 | 备注 |
| --- | --- | --- |
| `make dev-stage-shared` | 无运行时服务 | 只跑 proto 生成，并提示 `golangci-lint run` |
| `make dev-stage-artifact` | webhook `8082` | 使用 `configs/webhook.dev.yaml`；集合 `api/kulala/webhook.http` |
| `make dev-stage-tenancy` | orchestrator `8083` | 使用 `configs/orchestrator.dev.yaml`；集合 `api/kulala/manager.http`（客户与集群） |
| `make dev-stage-operator` | operator `8084` | 使用 `configs/operator.dev.yaml` |
| `make dev-stage-config` | orchestrator `8083` | 同上（ReleaseDefinition 与 ValuesRevision 同属 orchestrator） |
| `make dev-stage-publish` | orchestrator `8083` | 使用 `configs/orchestrator.dev.yaml` |
| `make dev-stage-auth` | auth `8085` | 使用 `configs/auth.dev.yaml` |
| `make dev-stage-audit` | api `8087` | 使用 `configs/api.dev.yaml` + `--db data/api.db` |
| `make dev-stage-full` | 无（导航目标） | 只打印两种全量启动方式，不启动任何进程 |

推荐顺序（按业务依赖，来自各 target 的 REQ 标注）：shared → artifact → tenancy → config →
operator → publish → auth → audit。tenancy / config / publish 三个 target 都启动 orchestrator 并占用
同一个端口 `8083`，`fuser -k` 会先杀掉占用者，因此**一次只能跑一个**。

等价的常驻入口是 `make run-<service>`：它先 `go build` 到 `bin/` 再跑二进制，`dev-stage-*` 则用
`go run` 并自动清端口。两者传参一致——只有定义了 `--db` 的服务才带该 flag：
`run-operator`/`dev-stage-operator` 传 `--db data/release-manager.db`，
`run-api`/`dev-stage-audit` 传 `--db data/api.db --signing-key change-me-in-production`；
orchestrator 与 auth 的 `run-*`/`dev-stage-*` 都只传 `--config`。

## 常见故障与边界

- **两套 dev 持久化路径，不要混淆**。`configs/*.dev.yaml` 与 `make run-*`/`dev-stage-*` 是
  宿主机裸进程路径，`auth`/`orchestrator`/`notifier` 的 `database.driver` 是 `sqlite`
  （`data/*.db`）；而 `make dev-up` 走 `deploy/kustomize/dev` overlay，在集群内运行
  **PostgreSQL（单实例双库 `release_manager` + `release_notifier`）+ Redis**，且不使用
  `configs/` 下的文件（容器不 COPY 它们，配置由 kustomize generator 提供）。两者出现行为差异时
  先确认自己跑在哪条路径上——例如 `ListReleaseInventory` 在 definition 存在非终态 operation 时，
  PostgreSQL 引擎会因列数不一致返回 `internal`，而 SQLite 路径已对齐。
- **残留状态导致的误判**：`dev-down` 后若手工保留 `data/dev-seed-progress.json` 等种子状态，
  下次 `dev-up` 会因 identity drift 报 `fixture_conflict`。不要手工修补，跑
  `make dev-reset-data CONFIRM=1`。
- **端口冲突**：8082–8087 被外部进程占用 → `port_conflict`（`/proc` 可解析时脚本会报占用 pid）。
  `dev-reset-data` 的 PostgreSQL port-forward 默认用 5432，若宿主已有别的 postgres 占用，
  必须设 `DEV_RESET_PG_PORT` 换端口，否则 `pg_dump` 会连到错误的实例。
- **锁冲突**：任何 `dev-*` 排他操作与 E2E 的共享锁互斥，冲突立即以退出码 3 +
  `environment_locked` 退出（stderr 含持有者 PID 与 started_at），不留半成品。
- **不要用裸 `docker inspect <name>` 判断对象存在**：容器的同名 network/volume 会让判断
  失真。脚本统一使用 `docker container inspect` / `docker network inspect`，手工排查时保持一致。
- **端点全部 loopback-only**：registry `127.0.0.1:5001`、k3d API `127.0.0.1:6443`、
  管理面 `127.0.0.1:8082–8087`（→ NodePort 30082–30087）。`release_operator`(8084) 是 mTLS
  agent gateway，只有 OperatorService + SyncInventory 路由，**没有** `/health`、`/readyz`、
  `/environment`，只能用 TCP 可达性探测。
- **失败诊断自动落盘**：`dev-up`/`dev-seed`/`dev-reset-data` 失败（以及 ci profile 下任意非零
  退出）会把 `kubectl describe/get/logs` 收集到 `data/diagnostics/<ISO8601>/`（目录 0700、
  文件 0600），stderr 只留一行摘要。
- **local 与 ci profile 的凭据处理不同**：local 写 0600 文件（`data/dev-credentials.env`、
  `data/dev-jwt/`、`data/dev-ca/`、`data/dev-trust-root/`、`data/dev-service-tokens/`）；
  ci 从环境变量注入同名物料、不落盘，且失败/中断路径自动清理受管资源。

> 事实源：`deploy/dev/dev.sh`（cmd_up/cmd_down/cmd_seed/cmd_status/cmd_reset_data/cmd_purge、cleanup_trap、kustomize_apply、control_plane_restart_deployments）、
> `deploy/dev/lib/host.sh`、`deploy/dev/lib/errors.sh`、
> `Makefile`（dev-* / dev-stage-* / run-* 目标）、`configs/*.dev.yaml`、
> `.github/workflows/test.yml`（e2e/e2e-prerequisite job 的工具安装与 env）、
> `Projects/001-release-manager/Requirements/REQ-065-dev-environment.md`、
> `Design/contracts/dev-environment-lifecycle.md`、`Design/contracts/dev-fixture-canonical.md`、
> `Design/decisions/D-013`、`D-014`、`D-017`、`D-019`、`D-020`。

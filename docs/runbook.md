# release-manager 故障处置手册（runbook）

本文面向**值班处置**：发生异常时按「症状 → 先查什么 → 确认依据 → 处置动作 → 处置后验证」走完一个闭环。

与既有文档的分工：`docs/architecture.md` 讲边界与契约，`docs/dev-environment.md` 讲环境搭建与生命周期命令，`docs/testing.md` 讲测试分层与 E2E，`docs/cli.md` 讲每个 `cmd/*` 二进制怎么起。本文不重复这些内容，只回答「出事时先看哪里、能做什么、做完怎么确认」。

标记约定：

- **现状**：来自代码/脚本/manifest 的读码结论，每条都带 `文件:行号`。
- **建议**：当前不存在、需要改动才能得到的能力，一律显式标注，不作为处置依据。
- ⚠️ **破坏性**：会造成数据丢失、资源删除或集群副作用的动作为，执行前必须完成该条列出的前置确认。

## 0. 值班速览

### 0.1 当前实际部署的进程（现状）

| 组件 | 容器端口 | 宿主端口（控制面集群） | 探针 | 备注 |
| --- | --- | --- | --- | --- |
| release-webhook | 8082 | 8082 → NodePort 30082 | `/readyz` + `/health` | `deploy/kustomize/services/webhook.yaml:47-57` |
| release-orchestrator | 8083 | 8083 → 30083 | `/readyz` + `/health` | `deploy/kustomize/services/orchestrator.yaml:56-75` |
| operator 网关（orchestrator 的第二个监听） | 8084 | 8084 → 30084 | 无 HTTP 路由 | mTLS only，`cmd/orchestrator/main.go:194-205` |
| release-auth | 8085 | 8085 → 30085 | `/readyz` + `/health` | `deploy/kustomize/services/auth.yaml:43-53` |
| release-notifier | 8086 | 8086 → 30086 | `/readyz` + `/health` | `deploy/kustomize/services/notifier.yaml:36-46` |
| release-web | 8087 | 8087 → 30087 | `/`（SPA 首页） | `deploy/kustomize/services/web.yaml:27-50` |
| release-notification-sink | 8088 | 无（仅 ClusterIP） | `/readyz` + `/health` | `deploy/kustomize/services/notification-sink.yaml:32-42` |
| 客户集群 operator agent | 8084 | 无 | `/readyz` + `/health` | `deploy/kustomize/customer-agent/base/deployment.yaml:57-67` |
| postgres / redis | 5432 / 6379 | 无 | `pg_isready` / `redis-cli ping` | `deploy/kustomize/postgres/deployment.yaml:38-47`、`deploy/kustomize/redis/deployment.yaml:27-35` |

现状要点：

- 管理面 services overlay 只包含 webhook、orchestrator、auth、notifier、notification-sink、web 六个 Deployment（`deploy/kustomize/services/kustomization.yaml:3-9`）。**没有独立的 `operator` Deployment，也没有 `release-api` Deployment**：operator 网关折叠进 orchestrator（`deploy/kustomize/services/orchestrator.yaml:60-64`），审计服务 `release-api` 只有本地进程入口（`Makefile:342-348`）。
- 宿主 8082–8087 端口段由控制面集群的 loadbalancer 映射（`deploy/dev/dev.sh:721-727`），端口表来自 `deploy/dev/lib/host.sh:16`。
- 管理面 kubectl 入口固定为合并 kubeconfig + 显式 context（`deploy/dev/dev.sh:48-54`）：

  ```bash
  KUBECONFIG=data/kubeconfig.yaml kubectl --context k3d-release-manager-control -n release-manager-dev <verb>
  ```

  客户集群用各自的 kubeconfig（`deploy/dev/dev.sh:1205-1214`），例如 `data/kubeconfigs/dev-customer-a-direct.yaml`，集群名单见 `deploy/dev/dev.sh:46`。

### 0.2 症状索引

| 症状 | 章节 |
| --- | --- |
| Pod 不 Ready / CrashLoopBackOff / 探针反复失败 | §1 |
| 服务起来就退、报 DSN 或迁移错误 | §2 |
| operator 显示 offline/suspect、命令不投递、紧急通道报 offline | §3 |
| 审计事件缺失、内存增长、spool 文件出现 | §4 |
| 大面积 401 / 登录被限流 / 令牌刚发就失效 | §5 |
| Operation 长时间非终态、`cancelling` 不消失、锁 stuck | §6 |
| 要回滚、要放行紧急变更、要解 stuck lock | §7 |
| 升级到新版本、镜像/pin 配置、升级失败回退 | §8 |
| 只读诊断命令、该找谁 | §9 |

## 1. 服务不可用、探针失败、CrashLoopBackOff

**症状**：Pod `READY 0/1`、`RESTARTS` 增长，或 `kubectl rollout status` 超时（dev 生命周期里这一步报 `management-plane rollout did not converge within 300s`，`deploy/dev/dev.sh:1166-1170`）。

**先查什么**

1. 该 Pod 的 `kubectl logs --previous`（当前镜像是 distroless，无 shell，日志只能从 API Server 取）。
2. 该 Pod 的 `/readyz` 与 `/health` 响应体。
3. `kubectl describe pod` 的 Events 与探针配置。

**确认依据（现状）**

- `/readyz` 是唯一有失败语义的探针：全部门禁通过返回 200，任一失败返回 503 且 body 是 `{"status":"degraded","checks":{"<name>":"<err>"}}`（`internal/handler/ready.go:11-34`）。检查项由各服务贡献：
  - orchestrator：`database`（2s 超时的 ping）+ `cleanup_gc`（`cmd/orchestrator/main.go:248-266`）；
  - auth：`database` + `redis`（`cmd/auth/main.go:67-87`）；
  - notifier：`database`（`cmd/notifier/main.go:50-61`）；
  - **webhook、notification-sink、operator agent 现已实现 `ReadinessChecks`**（TASK-099；`grep ReadinessChecks cmd/` 命中六个 main）：webhook 检查上游 orchestrator 的 `/readyz`（`cmd/webhook/main.go:90`）——**orchestrator NotReady 会级联使 webhook NotReady，这是设计**（它没有转发对象时接客无意义）；operator agent 检查 gateway 会话存活（`cmd/operator/main.go:382`，重连窗口内 NotReady 是真实状态）；notification-sink 检查配置可用（`cmd/notification-sink/main.go:151`）。未实现检查的进程仍回 `noop` 恒 200（`internal/app/app.go:134-136,152-158`），但 kustomize 内已无此类服务。
- `/health`（liveness 目标）**没有任何失败路径**（REQ-099 裁定：可失败性一律归 `/readyz`）：无条件 `WriteHeader(200)` + `{"status":"ok"}`（`internal/handler/health.go:12-28`）。orchestrator 额外挂一个 `gc` 子对象（`internal/app/app.go:139-143` + `cmd/orchestrator/main.go:268-289`，字段 `status`/`last_success_at`/`last_attempt_at`，Unix 秒）。结论：liveness 失败只可能是**进程已死、启动未完成（超出 startupProbe 预算）、或 3 秒内没答完**，不代表依赖健康。
- 启动期任何一步失败都会直接退出进程：配置加载失败 `failed to load config` → `os.Exit(1)`；`Register` 失败（含 store 打开、PostgreSQL 迁移、Redis ping）`failed to register service` → `os.Exit(1)`（`internal/app/app.go:125-149`）。这两条日志就是 CrashLoop 的第一现场。
- 探针时间预算是显式的（TASK-099）：每个应用容器有 `startupProbe`（httpGet `/health`，period 5s，HTTP `timeoutSeconds: 3`）——orchestrator/auth/notifier/webhook/notification-sink `failureThreshold: 120`（10 分钟启动预算），customer agent 60；startup 通过后 liveness 才有发言权。`readinessProbe` 指 `/readyz`（timeout 3s、failureThreshold 3；agent 为 12 以容忍重连窗），`livenessProbe` 指 `/health`（timeout 3s、failureThreshold 3）。防漂移：`make check-probes`。**曾经的形态（迁移前的风险）**：全树无 `startupProbe`、K8s 默认 `timeoutSeconds=1`，而迁移在 `Register` 内同步跑完才开始监听（`internal/app/app.go:146` → `cmd/orchestrator/main.go:524-545`）⇒ 一次慢迁移可能被 liveness 打断，表现为反复 CrashLoop 且每轮日志都从头重放迁移——若再次看到该形态，说明 manifest 被回退。
- 关停预算只有 5 秒（HTTP server + extra 网关 + `Shutdowner` + `Close`，`internal/app/app.go:211-227`）。审计刷盘超过 5s 会留下 `audit emitter shutdown: context deadline exceeded`（`internal/audit/emitter.go:115-120`）。
- 8084 网关不是 HTTP 服务：只有 OperatorService 与 SyncInventory 两条路由，`ReadHeaderTimeout: 10s`、TLS1.3、`VerifyClientCertIfGiven`（`cmd/orchestrator/main.go:166-205`）。用普通 HTTP 探测它会得到 `Client sent an HTTP request to an HTTPS server`，dev 生命周期因此只做 TCP 连通性探测（`deploy/dev/dev.sh:1176-1180`）。**对 8084 做 HTTP 探针失败不是故障**。
- web 的探针指向 `/`（`deploy/kustomize/services/web.yaml:27-50`），nginx 才把 `/health`、`/readyz`、`/environment` 反代到 orchestrator（`web/nginx.conf:77-101`）：所以 **8087 的 `/readyz` 报的是 orchestrator 的健康度**，不要据此判断 web 自身。

**处置动作**

1. 若日志是 `failed to load config` / `failed to register service`：定位是配置还是依赖（DSN/Redis/CA/迁移）。配置来自 ConfigMap（由 kustomize generator 从 `deploy/kustomize/dev/configs/*.dev.yaml` 生成，`deploy/kustomize/dev/kustomization.yaml:11-31`），**必须改仓库文件后 `make dev-up` 收敛**；直接 `kubectl edit configmap` 会被下一次 apply 覆盖。
2. 若是上面「慢迁移撞 liveness」的形态（每轮日志都在重放 `migrations`）：先按 §2 判断迁移为何慢/失败，处理根因，不要靠反复删 Pod 赌运气。
3. 若仅 readiness 失败而进程健康（例如 `database` 503）：这是**期望行为**——摘流量而不是重启。等依赖恢复即可自愈，无需处置 Pod。
4. 需要人工重启单个控制面组件时，用滚动重启（只影响该 Deployment）：

   ⚠️ **破坏性（会短暂中断该服务）**：`kubectl rollout restart deployment/<name>`
   前置确认：① 当前是否有非终态 Operation 在跑（§6）；② 是否处于维护窗口（§7.4）；③ orchestrator 重启会**清空进程内紧急流注册表**（§3.4），重启窗口内紧急变更必然失败。

**处置后验证**

```bash
KUBECONFIG=data/kubeconfig.yaml kubectl --context k3d-release-manager-control -n release-manager-dev \
  get pods -o wide
KUBECONFIG=data/kubeconfig.yaml kubectl --context k3d-release-manager-control -n release-manager-dev \
  rollout status deployment/orchestrator --timeout=300s
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8083/readyz   # 期望 200
curl -sS http://127.0.0.1:8083/readyz                                     # 期望 {"status":"ok",...}
```

环境级验证用 `make dev-status`（写并打印 `data/dev-status.json`，含集群状态、端点、`restart_targets`，`deploy/dev/dev.sh:1583-1636`）。

## 2. 数据库：连接池、DSN、迁移与迁移失败

**症状**：`/readyz` 里 `database` 报 503；或进程启动即退，日志含 `dsn_invalid` / `connection_unavailable` / `migration_failed`。

**先查什么**

1. 跑在哪条路径上（决定引擎与配置来源）：宿主机裸进程 = `configs/*.dev.yaml` = SQLite；`make dev-up` 的集群 = `deploy/kustomize/dev/configs/*.dev.yaml` = PostgreSQL。同一份文件名、两套引擎（`docs/dev-environment.md:157-163` 已说明，此处只作判定入口）。
2. 生效的 DSN/driver：`kubectl get deploy orchestrator -o jsonpath='{.spec.template.spec.containers[0].env}'` + ConfigMap `orchestrator-config`。
3. 迁移是否失败（`migration_failed` 前缀）。

**确认依据（现状）**

- 配置键与位置：`database.driver`、`database.dsn`、`database.max_open_conns`、`database.max_idle_conns`、`database.conn_max_lifetime`、`database.conn_max_idle_time`（`internal/config/config.go:24-31`）。环境变量覆盖映射：`DATABASE_DRIVER`、`DATABASE_DSN`、`DATABASE_MAX_OPEN_CONNS`、`DATABASE_MAX_IDLE_CONNS`、`DATABASE_CONN_MAX_LIFETIME`（`internal/config/config.go:272-279`）。
- 集群内取值：`deploy/kustomize/dev/configs/orchestrator.dev.yaml:3-5`（`postgres://release_manager:dev-release-manager@postgres:5432/release_manager?sslmode=disable`）、`deploy/kustomize/dev/configs/auth.dev.yaml:3-5`、`deploy/kustomize/dev/configs/notifier.dev.yaml:3-5`（后者指向独立库 `release_notifier`）。凭据来自静态 Secret `release-manager-dev-credentials`（`deploy/kustomize/base/secret.yaml:4-17`）；`release_notifier` 库由 `deploy/kustomize/postgres/init.sql:4` 创建。
- **连接池参数只对 PostgreSQL 生效**：`internal/postgres/db.go:46-49` 才调用 `SetMaxOpenConns/SetMaxIdleConns/SetConnMaxLifetime/SetConnMaxIdleTime`；SQLite 路径的 `Open(dsn)` 只接受 DSN（`internal/store/sqlite/db.go:70-95`），并固定注入 `_pragma=busy_timeout(5000)&_txlock=immediate` + `journal_mode=WAL` + `foreign_keys=ON`（`internal/store/sqlite/db.go:75-89`）。**在 SQLite 路径上调池参数是无效操作**。
- 池默认值：`max_open_conns` 缺省 25、`max_idle_conns` 缺省 10（`internal/postgres/config.go:26-34`）；`conn_max_lifetime` / `conn_max_idle_time` 无默认（0 = 不限制）。当前 `deploy/kustomize/dev/configs/*.dev.yaml` 都没写这些键，即跑默认值。
- DSN 校验很硬：必须以 `postgres://` 或 `postgresql://` 开头且 scheme/host/path 完整（`internal/postgres/config.go:12-24`），driver 只接受 `postgres|sqlite`（`internal/config/config.go:41-56`）。任一不满足 → `dsn_invalid: ...` → 服务不起。
- 失败文案：连不上库是 `connection_unavailable: ping PostgreSQL: ...`（`internal/postgres/db.go:50-53`）或 `connection_unavailable: initialize GORM: ...`（`internal/postgres/db.go:55-59`）。这两个前缀就是「依赖不可达」的确证，不是迁移问题。
- **迁移的真实入口**（现状）：
  1. PostgreSQL：**每个服务进程启动时自动跑**。`postgresstore.Open(ctx, cfg.Database, migrations.FS)`（`cmd/orchestrator/main.go:531`、`cmd/auth/main.go:96`），notifier 用另一个 embed 源 `migrations.ReleaseNotifierFS()`（`cmd/notifier/main.go:107`）。底层是 golang-migrate `m.Up()`（`internal/postgres/migrate.go:25-29`），迁移文件由 `//go:embed *.sql` 打进二进制（`migrations/embed.go:12-13,19-20`）——**镜像里没有 `migrations/` 目录，改迁移必须重建镜像**。
  2. 失败后果：所有失败统一 `migration_failed` 前缀（含 dirty 版本状态，`internal/postgres/migrate.go:23-24,42-52`），返回给 `Register` → `os.Exit(1)`（`internal/app/app.go:146-149`）→ CrashLoop。**没有单独的「只跑迁移」通道，也不会有半启动的服务对外接客**。
  3. SQLite：**不走 `migrations/`**。启动时执行内嵌 Go DDL/ALTER（`internal/store/sqlite/db.go:91-94` → `internal/store/sqlite/db.go:1116+`）。所以「`migrations/` 里加了列但 SQLite 没有」是真实可能，双引擎必须两边都改（`docs/architecture.md:119-126` 的分环境引擎约束）。
  4. 显式回滚：`RunMigrationsDown`（`internal/postgres/migrate.go:31-40`，注释明确「never normal service startup」），在 dev 生命周期里唯一使用者是 `devseed --reset`（`internal/devfixture/reset.go:47-75`），入口 `make dev-reset-data CONFIRM=1`（`deploy/dev/dev.sh:1681-1861`）。
  5. `cmd/store-migrate` 是 **SQLite → PostgreSQL 的数据搬迁 CLI**（`--source`、`--target-dsn`、`--migrations`；目标 DSN 走 `RELEASE_MANAGER_DATABASE_DSN` 或 flag，`cmd/store-migrate/main.go:35-41`），错误分类 `connection_unavailable|migration_failed|data_import_mismatch`（`cmd/store-migrate/main.go:81-90`），校验不过整体事务回滚（`internal/migration/migrate.go:41-45,105-135`）。它**不在 dev 生命周期内**，且 `--target-dsn` 含明文口令，注意不要写进共享终端历史。
- 持久性现状（决定处置顺序）：postgres 的数据目录是 `emptyDir`（`deploy/kustomize/postgres/deployment.yaml:56-60`），redis 是 `--appendonly no`（`deploy/kustomize/redis/deployment.yaml:23`）。⇒ **`dev-down`（删集群）即丢全部业务数据**；只有 orchestrator 有 PVC `release-manager-orchestrator-data`（`deploy/kustomize/base/pvc.yaml:1-13`，挂到 `/data`，`deploy/kustomize/services/orchestrator.yaml:102,118-120`），其中存放 agent 网关 CA（`/data/gateway-ca.*`）与审计 spool（§4）。

**处置动作**

1. `dsn_invalid` → 改 ConfigMap 源文件后 `make dev-up`；不要手改集群对象。
2. `connection_unavailable` → 先看 `deployment/postgres` 是否 Ready 与 `pg_isready`；再确认没有别的进程占用/改写过 Service（`deploy/kustomize/postgres/service.yaml:13-15`）。
3. `migration_failed`：
   - 若是脏版本（dirty）：这是需要人工决策的状态，`migrations/` 下对应的 `.down.sql` 是唯一的官方回退单元（`internal/postgres/migrate.go:31-40`）。
   - 若数据可丢弃：⚠️ **破坏性** `make dev-reset-data CONFIRM=1`。前置确认：① 该命令会在 `data/backups/` 落 `pg_dump` 再重建（`deploy/dev/dev.sh:1735-1749`），确认磁盘与备份可用；② 它会**重建 4 个客户集群**（`deploy/dev/dev.sh:1751-1763`）并短暂把控制面置于 `MAINTENANCE=true`（`deploy/dev/dev.sh:1698`）；③ 端口 `DEV_RESET_PG_PORT` 若与宿主已有 postgres 冲突必须换（`docs/dev-environment.md:166-169`）。
4. 池参数打满（连接排队、延迟上升）：在 ConfigMap 源文件里加 `database.max_open_conns` 等键后 `make dev-up`；**没有指标可证实这一点**（见 `docs/observability.md` §2 的池使用率缺口），当前只能靠 `pg_stat_activity` 侧证。

**处置后验证**

```bash
KUBECONFIG=data/kubeconfig.yaml kubectl --context k3d-release-manager-control -n release-manager-dev \
  exec deployment/postgres -- pg_isready                      # 期望 accepting
curl -sS http://127.0.0.1:8083/readyz                          # 期望 "database":"ok"
curl -sS http://127.0.0.1:8085/readyz                          # auth：database + redis 均 ok
```

只读 SQL 侧证（不写任何数据）：

```bash
KUBECONFIG=data/kubeconfig.yaml kubectl --context k3d-release-manager-control -n release-manager-dev \
  exec deployment/postgres -- psql -U release_manager -d release_manager -Atc \
  'select status,count(*) from operations group by status'
```

## 3. Operator 失联、会话过期、心跳超时

**症状**：控制台/operator 列表显示 `offline` 或 `suspect`；Operation 停在 `pending`/`queued`；紧急变更返回 `operator stream is offline`。

**先查什么**

1. agent 侧日志：`operator agent disconnected; reconnecting`（含 `error` 与 `retry_after`，`cmd/operator/main.go:104-105`）。
2. orchestrator 侧日志：`operator stream established via mTLS`（`internal/operator/service.go:466-472`，带 `operator_id`/`session_id`/`cluster_id`/`last_seen_sequence`）与 `heartbeat failed`（`internal/operator/service.go:580`）。
3. 会话行的 `status` / `status_reason` / `last_heartbeat`。
4. 网关拒绝原因：`X-Reason-Code` 响应元数据（`internal/operator/errors.go:28-32`）。

**确认依据（现状）**

- 超时常量（TASK-098 起**来自配置**）：`sessionTTL = 15 * time.Minute` 仍是构造默认，但心跳阈值走 `operator_session.heartbeat_interval` / `suspect_after` / `offline_after`（默认 15s / 45s / 90s，`internal/config/config.go` 的 `OperatorSessionCfg.WithDefaults`，`configs/orchestrator.dev.yaml` 与 dev overlay 均写出）。`SessionEstablished` 向 agent 广播 `HeartbeatIntervalSeconds = 15`、`HeartbeatTimeoutSeconds = 45`（`internal/operator/service.go`），Establish 路径写的 `ExpiresAt = now + 15m`。
- 状态机词汇：`SessionStatus` = `online|suspect|offline|revoked`（`internal/store/store.go:653-661`），`SessionStatusReason` = `no_session|heartbeat_timeout|heartbeat_delayed|certificate_revoked|operator_superseded|session_replaced|unknown`（`internal/store/store.go:663-674`）；`OperatorStatus` = `active|superseded|revoked`（`internal/store/store.go:644-651`）。
- 读侧投影：`ListOperators` 的 summary 直接映射 session 的 `status` 与 `status_reason`，**查不到会话时写 `SESSION_STATUS_REASON_NO_SESSION`**（`internal/orchestrator/operator.go:310-316`），枚举互转在 `internal/orchestrator/operator.go:470-533` ⇒ 控制台上的 `suspect/offline` 就是库里的列值，不是前端算出来的。
- 落库语义：`Heartbeat` 只在 `status IN ('online','suspect')` 时更新，并把 `status_reason` 置回 NULL；影响 0 行返回 `ErrNotFound`（`internal/store/postgres/operators.go:743-759`）。`UpdateStatus` 把 `suspect → heartbeat_delayed`、`offline → heartbeat_timeout`，且跳过 `revoked` 行（`internal/store/postgres/operators.go:761-785`）。
- **判定的真实形状（重要，容易误判）**：
  - **TASK-098 后的形状：agent 是 `last_heartbeat` 的唯一写入者。** `internal/operator/agent/agent.go` 在收到 `SessionEstablished` 后按协商周期（`HeartbeatIntervalSeconds`）发送 `Heartbeat{session_id}`，与接收循环共用一把 `Send` 互斥（Connect 双向流不允许并发 Send）。orchestrator 侧的帧处理分支把心跳写库并喂给 `SessionRegistry`（`internal/operator/service.go` 的 `case req.GetHeartbeat() != nil:`）。
  - `SessionRegistry` 已接线：网关 operator service 构造时注入（`cmd/orchestrator/main.go:98`），`Run(ctx)` 随进程启动（`cmd/orchestrator/main.go:779`）。它按 `age >= suspectAfter/offlineAfter` 推进会话状态；进入 `offline` 时同时从进程内表移除（`internal/operator/session_registry.go:86-122`）。⇒ `last_heartbeat` 陈旧现在会真的把会话推到 `suspect`/`offline`。
  - **重启窗口**：orchestrator 重启后进程内 stream 表为空，但会话行可能仍写 `online`。因此紧急路径除了看 `status`，还要求 `last_heartbeat` 足够新（`internal/orchestrator/emergency.go:531`），否则直接按 `operator_offline` 拒绝；即便行还新鲜而进程内流已丢失，dispatch 失败也会归一到同一个 `operator_offline`（`internal/orchestrator/emergency.go:273`）。
  - ⇒ **现状结论（触发 `online` → 非 online）**：① agent 停止心跳超过阈值（registry 推进，主路径）；② 重连时新会话替换旧会话（`session_replaced`）；③ 读侧还会把心跳陈旧的 `online` 行当作离线（紧急路径，见上）。`RolloutProgress`（`internal/operator/agent/rollout_progress.go`）仍是标准 Operation 期间的有效进度信号，与心跳无关。
- 重连与恢复（现状）：
  - agent 重连循环：退避 1s 起、倍增、上限 30s（`cmd/operator/main.go:96-115`）。
  - 重连后第一个动作是 `handleReconnect`：取 `GetNextSequence()`（全局 outbox 序列）与 agent 上报的 `LastSeenSequence` 比对，**有 gap 只发 `ResyncRequest` 并等待，不立即重投**（`internal/operator/service.go:1149-1188`）；无 gap 才重投 `delivered-not-acked`（`internal/operator/service.go:1192-1232`），已完成的下发 `DuplicateResponse`。
  - 由于序列是全局的（`internal/operator/service.go:1156`），任一 operator 收到过命令都会让别的 operator 判定为 gap ⇒ 「sequence gap detected」日志（`internal/operator/service.go:1168-1171`）**在当前拓扑下是常态噪声**，不是故障证据。真正投递靠 5s 一轮的 `deliverPending`（`internal/operator/service.go:1309-1345`，受 `max_inflight=1` 约束）。
  - 会话冲突：同一 operator 已有 `online`/`suspect` 且 `instance_id` 不同 → `Establish` 返回 `ErrDuplicateKey` → `already_exists: another session for this operator is already online`（`internal/operator/service.go:458-463`、`internal/store/sqlite/operators.go:422-440`）。
- 拒绝原因清单（判 `X-Reason-Code`，`internal/operator/errors.go:9-26`）：enroll 侧 `invalid_token|enroll_token_expired|token_reused|scope_mismatch|customer_disabled|cluster_disabled|csr_invalid|csr_san_mismatch|duplicate_operator_name|operator_name_cross_cluster`；流侧 `operator_superseded`（文案 `operator superseded: re-enroll required`）、`operator_revoked`、`cert_replaced`，另有 `certificate_invalid` 与 `internal`（`internal/operator/errors.go:20,24-25`）（`certificate serial does not match registered operator`）；证书续期过早 `renew_too_early`（`internal/operator/service.go:1395`），阈值是 `ca.cert_ttl × ca.renew_before_ratio`（dev：`cert_ttl: 168h`、`renew_before_ratio: 0.5`，`configs/orchestrator.dev.yaml:35-39`）。
- 紧急通道（独立于 outbox）：`DispatchEmergency` 只查**进程内** `emergencyStreams[operatorID]`，没有就直接失败 `operator stream is offline`（`internal/operator/service.go:157-174`），通道容量 8（`internal/operator/service.go:545-548`）。⇒ **orchestrator 重启会清空该 map**，重启窗口内紧急变更必然失败，与 agent 是否在线无关。

**处置动作**

1. 先分层定位：agent Pod 是否 Running（客户集群）→ agent 日志是否有 `reconnecting` → orchestrator 日志是否有 `established` → 会话行是否 `online`。四层里断在哪一层，处置对象就是哪一层。
2. agent 持续重连且日志含 `certificate`/`serial`：证书与注册身份不匹配 ⇒ 需要重新 enroll（⚠️ 破坏性低，但会让旧会话 `session_replaced`）：在控制面用 `CreateEnrollmentToken` 取单用 token，替换客户集群 Secret `operator-enrollment` 的 `token` 键后重启 agent（该 Secret 由生命周期脚本以命令式方式写入，`deploy/dev/dev.sh:1331-1335`；kustomize 无法表达运行期注入，`deploy/kustomize/customer-agent/base/kustomization.yaml:9-15`）。
3. `renew_too_early`：不是故障，等到剩余有效期低于阈值再续（`internal/operator/service.go:1395`）。
4. `already_exists: another session for this operator is already online`：同一 operator 有两个实例在连（常见于旧 Pod 未终止）。处置 = 收敛到单副本（agent Deployment `replicas: 1`，`deploy/kustomize/customer-agent/base/deployment.yaml`），确认没有手工跑的第二份 agent。
5. 卡在「agent 活着但命令不投递」：查会话 `status`、`operator_id`，以及该 definition 是否有非终态 Operation 占住 `max_inflight`（§6）。
6. **不要**用重启 orchestrator 来「刷新」operator 状态：重连会替换会话，但紧急通道会被清空（上一条）。

**处置后验证**

```bash
KUBECONFIG=data/kubeconfig.yaml kubectl --context k3d-release-manager-control -n release-manager-dev \
  exec deployment/postgres -- psql -U release_manager -d release_manager -x -c \
  "select id,operator_id,status,status_reason,last_heartbeat,expires_at from sessions order by started_at desc limit 5"
KUBECONFIG=data/kubeconfigs/dev-customer-a-direct.yaml kubectl -n release-manager-customer \
  logs deployment/operator --tail=100 | grep -E 'disconnected|reconnect|session'
```

业务侧验证：`ListOperators` / `GetOperator`（`api/gen/orchestrator/v1/orchestratorv1connect`，procedure 路径 `/orchestrator.v1.OrchestratorService/ListOperators`），请求样例在 `api/kulala/orchestrator.http`。

## 3bis. 制品准入（漏洞）从 shadow 切到 enforce

TASK-105 把「漏洞准入」接成了真实步骤（`CreateOperation` 内，对 bundle 的每个 image digest 调
`vulnerability.Evaluator`），并由 `vulnerability_admission.mode` 决定它如何影响发布：

| 模式 | 评估 | 结论为 reject / 不可用 | 证据 |
| --- | --- | --- | --- |
| `off` | 不评估 | 放行 | 只有 `skipped` 计数 |
| `shadow`（默认） | 评估 | **放行** | 审计 `admit_artifact` + `status=would_block`、`would_block` 计数、WARN 日志 |
| `enforce` | 评估 | **拒绝**（`failed_precondition` / `unavailable`） | 审计 `status=blocked` + `blocked` 计数 + WARN 日志 |

**切换流程（建议）**：

1. 保持默认 `shadow` 跑一段真实流量（预检/发布照常）。
2. 看证据：审计里 `action=admit_artifact` 的事件按 `metadata.reason` 聚合——`vulnerability_policy_failed`
   是策略拒绝，`vulnerability_policy_unavailable` 是「评估不可用」。
3. **先消灭 unavailable**：仓库当前只有 `vulnerability.Scanner` 接口、**没有生产 scanner 实现**，
   而且 `SetVulnerabilityEvaluator` 尚无生产调用者 ⇒ 现状下 `vulnEval == nil`，每次评估都落
   `vulnerability_policy_unavailable`；此时切 `enforce` 等于**全量拒绝发布**。
   注意区分另一类：若接入了 evaluator 但扫描结果缺失/过期或 scanner 报错，`Evaluate` 会返回
   **reject**（原因里带 `scanner_unavailable`/`sbom_missing`/`no scan result`/`scan_stale`），
   enforce 下按 `vulnerability_policy_failed` 拒绝。两类都要先看影子证据清零再切。
4. 把 `vulnerability_admission.mode` 改成 `enforce`（配置文件变更 + 滚动重启），并观察
   `blocked` 计数与发布成功率。
5. **逃生门**：把模式改回 `shadow` 即可立即恢复放行；这是一次**需要留痕**的降级（配置变更本身
   走正常变更流程），不要用关闭审计或放宽策略来绕过。

**判读要点**：`warn` 在任何模式下都放行（策略的「可接受但需注意」）；`pass`/`warn` 不产生证据事件。
`off` 与 `shadow` 都不改变发布结果——`shadow` 与 `off` 的唯一差别是前者留下证据。
`Evaluate` 本身是**读存储的扫描结果 + 套策略**（`Evaluator.Evaluate` → `ResultStore.GetLatest`），
不是同步扫描，所以每次 `CreateOperation` 对每个镜像只多一次 DB 读，不引入扫描延迟。

## 4. 审计异步刷盘积压与丢失

**症状**：审计查询里事件缺失；orchestrator 内存持续增长；PVC 上出现 spool 文件；日志有 `audit buffer full` 或 `audit batch persistence failed`。

**先查什么**：orchestrator 日志（两个关键文案，见下）→ `/data` 上的 spool 文件 → `audit_events` 行数增量。

**确认依据（现状）**

- 全部参数是**硬编码默认值，不可配置**：`BufferSize: 4096`、`FlushInterval: 5 * time.Second`、`BatchSize: 200`、`SpoolPath: "data/audit_spool.jsonl"`（`internal/audit/emitter.go:43-46`），三个装配点都只用 `audit.DefaultConfig()`（`cmd/orchestrator/main.go:343`、`cmd/operator/main.go:254`、`cmd/api/main.go:59`）。
- `Emit` 非阻塞：归一化失败 → `invalid_event`；已关停 → `store_unavailable`；缓冲满 → `buffer_full` 并打 Warn `audit buffer full`（带 `event_id`/`resource_type`/`action`）后**丢弃该事件**（`internal/audit/emitter.go:69-98`）。
- worker 行为：攒够 200 条或每 5s 落一次；`CreateBatch` 失败打 Error `audit batch persistence failed`（带 `count`/`error`），并**把失败批次重新插回队首无界重试**（`internal/audit/emitter.go:123-157`）⇒ 库长时间不可写时进程内存单调上涨。通道关闭时先 flush，残余写 spool（`internal/audit/emitter.go:141-147`）。
- spool 落盘：JSONL、文件 0600、目录 0700、显式 `Sync`（`internal/audit/emitter.go:168-198`）。路径是**相对**的，解析到容器 `WORKDIR /data`（`deploy/docker/Dockerfile.orchestrator:14`）⇒ 实际写入 `/data/data/audit_spool.jsonl`，落在 orchestrator 的 PVC 上（`deploy/kustomize/services/orchestrator.yaml:100-102`）。
- **spool 只写不回灌**：`NewSpoolRecoverer(...).Recover(ctx, path)` 只在测试里被调用（`internal/audit/spool.go:20-28`；全仓非测试调用者为 0）⇒ 一旦事件进了 spool，就永久留在文件里，不会自动补进 `audit_events`。
- 审计是 best-effort：业务写路径提交后 `Emit`，被拒只 Warn（`internal/orchestrator/service.go:1537-1549`），例如 `emergency timeout audit event rejected`（`internal/orchestrator/emergency.go:355-357`）。**这与 `docs/decisions/ADR-011-controlled-emergency-change-and-convergence.md` 里「审计写入失败会阻塞业务提交」的表述不一致**，见 §10。
- 三个「整体没有审计」的现状：
  1. 维护模式下**根本不创建 emitter**（`cmd/orchestrator/main.go:342-344`），此时所有 `emitAudit` 直接 return（`internal/orchestrator/service.go:1537-1540`）⇒ 维护窗口内的写操作无审计。
  2. ~~worker 签名不匹配不启动~~ **TASK-094 §7-9 已修复**：`apiSvc.Run(context.Context)`/`Close() error` 现与 `internal/app/app.go:42-48` 的 `backgroundService`/`closeService` 精确匹配并有编译期断言（`cmd/api/main.go:45-48`），本地 `make run-api` 归档循环与关停 flush 真实执行。剩余现实见下条：集群里没有 release-api Deployment。
  3. 归档 worker 只有在 `retention_days > 0` 时运行（`internal/audit/archive_config.go:19-29,31+`，`internal/audit/archive_worker.go:26-50`），且**唯一的配置来源是 `configs/api.dev.yaml:3-10`**（`audit.archive.*`，由 `cmd/api/main.go:123-136` 读取）。集群里没有 release-api Deployment ⇒ **部署形态**下没有归档执行者（本地 `make run-api` 已有，见上条）。
- 积压的可观测信号只有日志与文件（`internal/audit/metrics.go:6-26` 的计数器没有任何生产者导出，见 observability 文档）。

**处置动作**

1. 先判断是「写库失败」还是「产生速度超上限」：有 `audit batch persistence failed` ⇒ 数据库侧（回到 §2）；只有 `audit buffer full` ⇒ 产出风暴，先按 §5 处理认证风暴/重试风暴源头。
2. 内存持续上涨：说明正处于失败批次重插循环 ⇒ 根因消除后会自动收敛（不丢数据，只在内存里）。若逼近 limit（orchestrator `memory: 256Mi`，`deploy/kustomize/services/orchestrator.yaml:76-82`）会先被 OOMKill；⚠️ 处置 = 重启进程，代价是丢弃未落盘事件（缓冲上限 4096 条）与关停不 flush 的部分。
3. 发现 `/data/data/audit_spool.jsonl` 非空：⚠️ **手工回灌是破坏性候选**（会把事件写进权威表，可能产生重复 ID 冲突）。现状**没有官方回灌入口**；若必须回灌，先按 §10 记录为「需产品决策的缺口」，不要现场写脚本直插 `audit_events`。
4. 维护模式造成的审计空洞是**设计后果**，不是待修的运行时故障：在维护窗口外补记说明（建议做法，见 §10）。

**处置后验证**

```bash
KUBECONFIG=data/kubeconfig.yaml kubectl --context k3d-release-manager-control -n release-manager-dev \
  exec deployment/orchestrator -- ls -l /data/data/audit_spool.jsonl 2>/dev/null || echo no-spool
KUBECONFIG=data/kubeconfig.yaml kubectl --context k3d-release-manager-control -n release-manager-dev \
  exec deployment/postgres -- psql -U release_manager -d release_manager -Atc \
  'select count(*), max(created_at) from audit_events'
KUBECONFIG=data/kubeconfig.yaml kubectl --context k3d-release-manager-control -n release-manager-dev \
  logs deploy/orchestrator --tail=500 | grep -Ec 'audit buffer full|audit batch persistence failed'
```

判据：`max(created_at)` 随写入推进、两个 grep 计数不再增长、spool 不新增。

## 5. 认证失败风暴、令牌过期、签名密钥

**症状**：控制台大面积 401；登录报 `resource_exhausted`；刚登录就掉线；服务间调用（webhook → orchestrator）报 `permission_denied: invalid service token`。

**先查什么**

1. 401 的具体 message（下表直接决定分支）。
2. auth 与 orchestrator 两个 Deployment 的 `JWT_SIGNING_KEY` 是否同源。
3. `auth_policy_health` / `auth_snapshot_stale_total`（auth 与 orchestrator 的 `/metrics`）。
4. `/readyz` 的 `redis` 检查项（auth）。

**确认依据（现状）**

| 客户端可见错误 | 产生条件 | 证据 |
| --- | --- | --- |
| `unauthenticated: missing authentication credentials` | 既无 `Authorization` bearer 也无 `rm_access` cookie | `internal/auth/interceptor.go:55-63` |
| `unauthenticated: invalid token: ...`（含过期） | `ValidateAccessToken` 失败：签名不符、过期、claims 缺字段 | `internal/auth/interceptor.go:65-68`、`internal/auth/jwt.go:64-80` |
| `unauthenticated: session revoked` | 用户非 active，或 `auth_sessions` 无有效会话 | `internal/auth/interceptor.go:113-124` |
| `internal: session validation failed` | 会话有效性查询本身失败（含 Redis 不可用） | `internal/auth/interceptor.go:117-121` |
| `permission_denied: csrf token mismatch` | cookie 认证下的写操作缺/错 `X-CSRF-Token` | `internal/auth/interceptor.go:82-88` |
| `resource_exhausted: too many login attempts` | 按用户名限流命中 | `internal/auth/service.go:66`、`internal/auth/browser_session.go:21` |
| `unauthenticated: authentication required`（服务侧） | 无 JWT 且无 service token | `internal/orchestrator/rollback.go:35` |
| `permission_denied: invalid service token` | bearer 哈希与 current/previous 都不同 | `internal/auth/service_token.go:50-59` |

配置与常量（现状）：

- 签名密钥来源：`--signing-key`，默认值取环境变量 `JWT_SIGNING_KEY`，再默认 `change-me-in-production`（`cmd/auth/main.go:237`、`cmd/orchestrator/main.go:848-854`）。集群里由 Secret `release-manager-jwt` 注入（`deploy/kustomize/services/orchestrator.yaml:34-39`、`deploy/kustomize/services/auth.yaml:35-39`），Secret 内容由 `data/dev-jwt/jwt-signing-key.pem` 生成（`deploy/kustomize/dev/kustomization.yaml:39-43`）。
- ⇒ **auth 与 orchestrator 各持一个 `JWTManager`，必须同 key**（`cmd/auth/main.go:140`、`cmd/orchestrator/main.go:425`）。两者不一致 ⇒ 全站 401 `invalid token`，且**只有 401 一个症状**（`/readyz` 全绿）。
- TTL 硬编码：access 15 分钟、refresh 7 天（`cmd/auth/main.go:140`），无配置键。cookie 名 `rm_access` / `rm_refresh` / `rm_csrf` + header `X-CSRF-Token`（`internal/auth/service.go:17-20`）。
- 限流默认 5 次/60s（按用户名），可用 `login_rate_limit.max_attempts` / `.window` 覆盖（`cmd/auth/main.go:141-154`；键定义 `internal/config/config.go:123-126`）。集群 dev 配置已把它调高，dev 播种重试因此不会触发（`deploy/kustomize/dev/configs/auth.dev.yaml`）。
- 会话缓存 fail-closed（ADR-019）：Redis 只作缓存 + refresh 黑名单，键前缀 `auth:sess:` / `auth:blacklist:` / `auth:user:`（`internal/store/redis/adapter.go:17-21`）。`Create` 在 Redis 发布失败时**回滚权威行的 family**（`internal/store/redis/adapter.go:39-56`）；黑名单读取的非 `redis.Nil` 错误转成 `unavailable(...)`（`internal/store/redis/adapter.go:63-81`）。Redis 客户端超时是 1s 级（dial/read/write，`cmd/auth/main.go:113-127`），启动 ping 失败 ⇒ `Register` 失败 ⇒ 进程退出（`cmd/auth/main.go:128-130`）。
- 只有 `redis.address` 非空才启用 Redis（`cmd/auth/main.go:113`）；宿主机 `configs/auth.dev.yaml` 没有该段 ⇒ 本地进程路径本来就不依赖 Redis，**不要把集群内的 Redis 故障形态套到本地**。
- 授权快照陈旧是另一类「鉴权风暴」：`auth_policy_health = 0` 与 `auth_snapshot_stale_total` 增长（`internal/authorization/metrics.go:33-48`、`internal/authorization/module.go:286-295`），业务侧表现为 `unavailable: authorization_snapshot_stale`（`internal/orchestrator/rollback.go:166-169,188-190`）。拉取节奏由 `authorization.pull_interval` / `pull_backoff_max` 控制（dev 1s / 30s，`deploy/kustomize/dev/configs/orchestrator.dev.yaml:6-9`）。
- 维护模式（`maintenance: true` 或 `MAINTENANCE=true`，`internal/config/config.go:19,281`）返回 `unavailable: maintenance`，且**只包 unary RPC**（`internal/app/maintenance.go:14-28` 用的是 `connect.UnaryInterceptorFunc`）⇒ 流式（`WatchOperation`、`CommandStream`）不受门禁。

**处置动作**

1. `invalid token` 大面积出现：先比对两个 Deployment 的 key 指纹（下面验证段）；若不同 ⇒ 以 `release-manager-jwt` Secret 为准，改 `data/dev-jwt/jwt-signing-key.pem`（轮转 = 删文件后重跑 `make dev-up`，`deploy/dev/dev.sh:311-317`）。⚠️ **轮转是破坏性的**：所有已签发 access token 立即失效，在线用户全部被踢到登录页；前置确认：① 无人正在跑 E2E；② 不在维护窗口中；③ 通知会掉登录。
2. `too many login attempts`：是**保护机制生效**，优先查上游客户端是否在循环重登（前端刷新逻辑/脚本死循环）。确需放宽时改 `login_rate_limit`（有配置键，安全）而不是改代码。
3. `session revoked` 但用户确实 active：说明 `auth_sessions` 里会话没了——按 `auth.StartSessionCleanup`（1h 周期，`cmd/auth/main.go:167`）与 access TTL 判断是否只是过期；若刚登录就出现，查 Redis 是否被清空（`redis --appendonly no`，`deploy/kustomize/redis/deployment.yaml:23` ⇒ 重启即全丢）。
4. `session validation failed` / Redis 抖动：确认 `deployment/redis` 状态（`redis-cli ping` 判据 `PONG`，`deploy/dev/dev.sh:1145-1156`）。⚠️ 不要「顺手重启 Redis 恢复缓存」——重启即清空 ⇒ 所有 refresh 会话失效。
5. `invalid service token`（bundle 上传链路）：orchestrator 同时接受 current + previous 两个 token（`DEV_WEBHOOK_SERVICE_TOKEN` / `..._PREVIOUS`，`cmd/orchestrator/main.go:898-911`；Secret 键见 `deploy/kustomize/services/orchestrator.yaml:45-55`），发送端只用 current（`deploy/kustomize/services/webhook.yaml:38-43`）。⇒ **轮换必须两键并存**，只换一键会造成 ingress 全 403。
6. `authorization_snapshot_stale`：auth 侧策略版本追不上 ⇒ 检查 auth 是否刚重启/策略是否在 reload（`enforcer.StartPolicyReloader`，`cmd/auth/main.go:168`；`cmd/orchestrator/main.go:721-723`）。这是 fail-closed 的预期行为，**不要试图绕过**。

**处置后验证**

```bash
curl -sS http://127.0.0.1:8085/readyz | head -c 400
curl -sS http://127.0.0.1:8085/metrics | grep -E '^auth_(policy_health|snapshot_stale_total|source_version|decisions_total)'
curl -sS http://127.0.0.1:8083/metrics | grep -E '^auth_(policy_health|source_version|checkpoint_version)'
for d in auth orchestrator; do
  KUBECONFIG=data/kubeconfig.yaml kubectl --context k3d-release-manager-control -n release-manager-dev \
    get deploy $d -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="JWT_SIGNING_KEY")].valueFrom.secretKeyRef.name}{"\n"}'
done   # 两行必须同名 Secret，且该 Secret 只存在一份
KUBECONFIG=data/kubeconfig.yaml kubectl --context k3d-release-manager-control -n release-manager-dev \
  get secret -o name | grep -E 'release-manager-jwt|webhook-service-token'
```

行为级验证：用 `api/kulala/auth.http` 的登录/校验请求各跑一次（集合是仓库内既有资产，`Makefile:247-252` 有打开入口）。

## 6. 发布卡住：Operation 长期不收敛

**症状**：Operation 长时间停在 `pending` / `preflight` / `queued` / `running` / `cancelling`；控制台进度不动；`WatchOperation` 只有 `Heartbeat` 没有新 timeline。

**先查什么（顺序即判定顺序）**

1. `GetOperation`：`state`、`state_version`、`deadline`、`updated_at`、`last_error`、`effect_status`（`api/proto/orchestrator/v1/orchestrator.proto`，`Operation` 消息字段 1-20）。
2. timeline 的最后一条及其 `kind`（`STATE_TRANSITION|ACK|ROLLOUT_PROGRESS|ERROR|EMERGENCY_EFFECT_RESOLVED`，`internal/store/store.go:361-370`；proto `TimelineEntryKind` 同步）。
3. operator 是否在线（§3）。
4. outbox 中该 Operation 的投递状态（`pending|delivered|succeeded|failed`，`internal/store/store.go:676-680`）。
5. 该 definition 是否有 `pending_promotion` 收敛任务或 stuck lock。

**确认依据（现状）**

- 合法状态转移只有这些边：`pending → preflight|queued|cancelled|timeout`、`preflight → queued|failed|cancelled|timeout`、`queued → running|cancelled|timeout`、`running → succeeded|failed|cancelling|timeout`、`cancelling → cancelled|failed|timeout`（`internal/orchestrator/operation/state.go:33-62`）；终态吸收一切后续事件（`internal/orchestrator/operation/state.go:66-69`）。标准 `INSTALL/UPGRADE/ROLLBACK` 必经 preflight，`EMERGENCY` 跳过（`internal/orchestrator/operation/state.go:84-98`）。⇒ **停在 `preflight` 是设计内的等待**（`docs/architecture.md:127-136` 的 CAS + outbox 语义）。
- **只有 EMERGENCY Operation 有 `deadline`**：`deadline = now + emergency.operation_timeout` 并写进 Operation（`internal/orchestrator/emergency.go:184-187,213-219`），超时判定也只挑带 deadline 的 EMERGENCY（`internal/orchestrator/emergency.go:297-333`，1 秒一轮扫，`cmd/orchestrator/main.go:747-756`）。⇒ **标准 Operation 的 `deadline` 为空，永远不会被系统判 timeout**，只能等 operator 重连或人工 `CancelOperation`。这是「发布卡住」与「真故障」的第一分水岭。
- 恢复扫描：启动时跑一次，**TASK-098 起按 `operation.recovery_interval`（默认 1m）周期跑**（`cmd/orchestrator/main.go:743-760`、`:785`）；标准 Operation 现在带 `operation.deadline`（默认 30m，`internal/orchestrator/service.go:394`、`internal/orchestrator/rollback.go:164`），超期由扫描转 `timeout`。其行为（`internal/orchestrator/operation/recover.go:14-30,58-87`）：
  - `cancelling` 超过 `CancellingTimeout = 5m` ⇒ 转 `failed`，日志 `recovery: stale cancelling operation, transitioning to failed`；
  - `deadline + DeadlineGracePeriod(30s)` 已过 ⇒ 转 `timeout`；
  - 其余 ⇒ **有意保留**，日志 `recovery: non-terminal operation left running for operator reconnect`。看到这条日志 = 系统认为「不算故障」。
  - preflight 的恢复同理：`ResumePreflights` 只在启动时（`cmd/orchestrator/main.go:418`，日志 `preflight operations resumed on restart`）。⇒ **没有周期性收敛**：不重启就不会推进。
- 投递停滞的真实原因分层：`max_inflight = 1` 会阻塞后续命令下发（`internal/operator/service.go:1323-1332`）；agent 执行超时默认 5 分钟（`internal/operator/agent/agent.go:31` `defaultInstallTimeout`，可被命令 `TimeoutSeconds` 覆盖，`internal/operator/agent/agent.go:727-729`；进程 flag `--install-timeout`，`cmd/operator/main.go:359-366`）；`RolloutProgress`（`internal/operator/service.go:742-745`）缺失意味着 agent 侧 observer 未上报。
- 「设计内的等待态」清单（现状）：`preflight`（等 preflight 通过/重启恢复）、`queued`/`running` 且 operator 处于重连退避窗口、`cancelling` 且未超 5m、`convergence_tasks.status = pending_promotion`（`internal/orchestrator/rollback.go:95-102` 会因此拒绝新回滚并报 `release_convergence_pending`）、values revision 处于 `pending_approval`（`internal/store/store.go:207`）。
- 「真故障」清单（现状）：outbox 长期 `pending`/`delivered` 且会话 `offline`；EMERGENCY 越过 deadline 后 `effect_status = UNKNOWN` 且超过 `effect_observe_timeout`（dev 24h，`configs/orchestrator.dev.yaml:47-48`）⇒ stuck lock，日志 `emergency target lock is stuck`（`internal/orchestrator/emergency_stuck.go:290-299`，60s 一轮，`cmd/orchestrator/main.go:812-824`，**只告警+审计，绝不自动解锁**）；`cleanup_gc` 不健康拖垮 readiness（§1）。
- 枚举能力的现状缺口：**`ListOperations` 服务端未实现**，返回 `unimplemented`（`internal/orchestrator/service.go:1552-1554`），Web 也没有调用它（只存在生成的类型）。⇒ 值班没有「列出所有非终态 Operation」的 API 路径，只能按已知 `operation_id` 查，或直接跑只读 SQL（§2、§9）。
- timeline 的读取路径只有 `WatchOperation`（服务端流，`api/proto/orchestrator/v1/orchestrator.proto:851`）；`last_error` 与 timeline 里的错误摘要都经 `redact.Sanitize` + `redact.Truncate(..., 500)` 脱敏（`internal/store/store.go:2611-2627`）。⇒ 日志/时间线里看到 `****REDACTED****` 是预期，不是数据损坏。

**处置动作**

1. `running` 且无 deadline、operator 在线、outbox 已 ACK：等待 agent 侧 Helm 结果（其自身超时上限 5m/命令）。**不要**在这个形态下重启 orchestrator：重启会跑 `RecoverNonTerminal`，但因无 deadline 依旧不推进（`internal/orchestrator/operation/recover.go:71-87`），只是白白清空紧急通道（§3）。
2. `cancelling` 超过 5m：现状**必须重启 orchestrator** 才会被转成 `failed`（周期扫描不存在）。⚠️ 重启前确认 §3 的紧急通道代价与 §1 的启动序列。
3. 卡在 `pending`/`queued` 且 operator `offline`：先修连接（§3），命令会经 outbox 重投放行（§3.重连语义）。
4. `release_convergence_pending`：这不是发布故障，是需要**人工收敛决策**——按 §7.3 走 `ReleaseEmergencyLock` / 正式提单，不要靠重试绕过。
5. stuck lock：只有一条正式出口，即 §7.3 的 `ReleaseEmergencyLock`（两种模式）。**禁止**直接改 `emergency_intents` 行。
6. 需要「让这一单结束」的最小动作永远是 `CancelOperation`（`running → cancelling → cancelled|failed`）。注意取消**不会**回滚已产生的集群副作用。

**处置后验证**

```bash
KUBECONFIG=data/kubeconfig.yaml kubectl --context k3d-release-manager-control -n release-manager-dev \
  exec deployment/postgres -- psql -U release_manager -d release_manager -x -c \
  "select id,operation_type,status,state_version,deadline,updated_at,terminal_at,last_error from operations where id='<operation_id>'"
KUBECONFIG=data/kubeconfig.yaml kubectl --context k3d-release-manager-control -n release-manager-dev \
  exec deployment/postgres -- psql -U release_manager -d release_manager -Atc \
  "select sequence,kind,from_state,to_state,error_code,timestamp from operation_timeline where operation_id='<operation_id>' order by sequence"
KUBECONFIG=data/kubeconfig.yaml kubectl --context k3d-release-manager-control -n release-manager-dev \
  exec deployment/postgres -- psql -U release_manager -d release_manager -Atc \
  "select id,status,sequence,delivered_at,acked_at from outbox where operation_id='<operation_id>' order by sequence"
```

终态判据：`status` 进入 `succeeded|failed|cancelled|timeout` 且 `terminal_at` 非空（终态吸收后续转移，`internal/orchestrator/operation/state.go:66-69`）。

## 7. 回滚与紧急处置

> 通则：**只有 `RollbackRelease` / `ExecuteEmergencyChange` / `CancelOperation` / `ReleaseEmergencyLock` 是正式处置通道**。直接 `kubectl patch`、`helm rollback`、改库都违反项目的 SDK-only 执行边界与审计契约（`docs/architecture.md:23-38`、`docs/testing.md:123`）。

### 7.1 回滚到上一个 revision

**症状 / 触发**：新版本造成故障，需要退回已知可用 revision。
**先查什么**：当前 revision 与目标 revision；是否有非终态 Operation 占位；是否有 pending 收敛任务。
**确认依据（现状）**

- 入口：`POST /orchestrator.v1.OrchestratorService/RollbackRelease`（`api/proto/orchestrator/v1/orchestrator.proto:849`）。请求字段：`release_definition_id`、`target_revision`（回到哪个 revision）、`expected_current_revision`（乐观锁）、`reason`（必填）、`values_revision_id`/`values_patch` 已废弃且**服务端拒绝**（`api/proto/orchestrator/v1/orchestrator.proto:153-163`）。
- 必须带 `Idempotency-Key` header（缺失 → `invalid_argument: idempotency_key is required`，`internal/orchestrator/rollback.go:104-109`；同 key 不同请求体 → `already_exists: idempotency_conflict`，`internal/orchestrator/rollback.go:116-121`）。header 名在服务端统一从 `Idempotency-Key` 读取（`internal/orchestrator/values_revision.go:83,405` 有 1–64 字符约束）。
- 门禁顺序与错误码（现状，全部在 `internal/orchestrator/rollback.go`）：认证（:35）→ values 参数被拒（:41）→ 参数校验（:46-58）→ definition 存在（:66）→ 紧急 effect 门禁（:81）→ 收敛门禁 `release_convergence_pending`（:95-102）→ 幂等键（:104-127）→ definition 可用（:130）→ customer 未禁用（:134）→ 无其他非终态 Operation（:138）→ ROLLBACK 需要 active inventory（:142）→ 授权快照版本（:166-169）；并发冲突映射：`release_busy`→`failed_precondition`、`idempotency_conflict`→`already_exists`、`authorization_snapshot_stale`→`unavailable`（:180-194）。
- ⚠️ **破坏性等级：中**。它会在客户集群里执行真实的 Helm rollback（经 outbox 投递给 agent，至少一次 + 本地幂等重放，`docs/architecture.md:30-38`）。前置确认：① `expected_current_revision` 与实读 revision 一致；② `reason` 写清事故编号（会进审计 `change_summary`）；③ 目标 revision 的 bundle/镜像仍在 registry 里存在（GC 会按 `bundle_retention_days` 回收，`internal/orchestrator/cleanup.go:43-52`）；④ 当前无并发操作。
**处置动作**：`RollbackRelease` → 按 §6 跟到终态。
**处置后验证**：`GetOperation` 终态 + `ListReleaseInventory` / `ListReleases` 的 revision 已变（procedure 列表见 `api/gen/orchestrator/v1/orchestratorv1connect`）。

### 7.2 紧急变更（绕开正常流程的那一条）

**先查什么**：`emergency.enabled` 开关当前值、`operation_timeout`、是否有同名 definition 的锁。
**确认依据（现状）**

- 入口：`ExecuteEmergencyChange`（`internal/orchestrator/emergency.go:77-110`）。**kill switch 是最高优先级门禁**：`enabled = false` ⇒ `failed_precondition` + `EMERGENCY_REASON_CODE_KILL_SWITCH_DISABLED`（`internal/orchestrator/emergency.go:96-110`）。
- 开关没有 RPC：唯一的写入口是 orchestrator 启动时把配置 `emergency.*` upsert 进 `app_settings`（`cmd/orchestrator/main.go:300-312`、`seedEmergencyConfig` `cmd/orchestrator/main.go:643-668`）。配置缺失时保留既有值、新库默认 fail-closed（`internal/config/config.go:147-156`）。⇒ **改开关 = 改 `deploy/kustomize/dev/configs/orchestrator.dev.yaml:41-44` 再 `make dev-up`**。
- deadline = `emergency.operation_timeout`（dev `30s`）；过期后 `pending` ⇒ `effect = NOT_APPLIED`（可证明未投递，释放锁），`queued/running` ⇒ `effect = UNKNOWN`（保留锁观察，`internal/orchestrator/emergency.go:290-333`）。
- 投递通道是**进程内**紧急流，agent 不在流上就直接失败（见 §3 的 `DispatchEmergency` 语义）。
**处置动作**：调用 `ExecuteEmergencyChange`，随后按 §6/§7.3 跟踪收敛。

- ⚠️ **破坏性等级：高**（直接改工作负载/镜像）。前置确认：① 目标 workload 身份（kind/name/namespace/uid）已由 REQ-085 校验（缺失即 `invalid_workload_ref`，`internal/orchestrator/emergency.go:137-140`）；② 收敛策略已确定（是否需要后续正式 promotion，`internal/orchestrator/emergency.go:220-227`）；③ 已知 `operation_timeout` 的值并计划在窗口内观察；④ 有 `Idempotency-Key`。

**处置后验证**：`GetOperation` 的 `effect_status`；`ListEmergencyTargets` / `CheckEmergencyConflict`；若 `effect` 长期 `UNKNOWN`，等过 `effect_observe_timeout` 后按 §7.3 处置。

### 7.3 stuck lock 的人工放行

**先查什么**：`ListStuckLocks`（`internal/orchestrator/emergency_stuck.go:77`）。
**确认依据**：`ReleaseEmergencyLockRequest{intent_id, reason(必填 1–1000), mode, evidence(≤500)}`（`api/proto/orchestrator/v1/orchestrator.proto`）；模式只有 `NOT_APPLIED_PROVEN` 与 `AUDITED_OVERRIDE`（`internal/orchestrator/emergency_stuck.go:186-195`）。错误：`intent_not_found`、`operation_not_terminal`（提示改走 `CancelOperation`）、`lock_not_stuck_or_already_released`（`internal/orchestrator/emergency_stuck.go:197-204`）；参数与鉴权错误在 `internal/orchestrator/emergency_stuck.go:133-147`。成功时写**恰好一条**审计事件（`emitEmergencyLockReleaseAudit`，`internal/orchestrator/emergency_stuck.go:176-177,214-220`）。
**处置动作**：`ReleaseEmergencyLock`（先 `NOT_APPLIED_PROVEN`，证明不了再考虑 `AUDITED_OVERRIDE`）。

- ⚠️ **破坏性等级：高（语义上等于承认未知副作用）**。前置确认：① 已取到 `intent_id` 与 `terminal_at`；② `reason` 与 `evidence` 有事实依据（会被脱敏后写入审计）；③ 选 `AUDITED_OVERRIDE` 前已确认无法证明 NOT_APPLIED。

**处置后验证**：`ListStuckLocks` 不再包含该 intent；60s 扫描不再打 `emergency target lock is stuck`（`cmd/orchestrator/main.go:812-824`）；`QueryAuditEvents` 能看到 `emergency_lock_release`（注意 §10 的可见性缺口）。

### 7.4 维护窗口（把写路径整体停下来）

**确认依据（现状）**：只有配置键 `maintenance`（`internal/config/config.go:135`）与环境变量 `MAINTENANCE`（`internal/config/config.go:281`）两个入口，**没有 `--maintenance` flag**（`grep maintenance cmd/orchestrator/main.go` 命中的是只读白名单与拦截器挂载）⇒ 生效时非白名单 unary 写全部 `unavailable: maintenance`（`internal/app/maintenance.go:14-28`），白名单是只读 procedures（`orchestratorReadOnlyProcedures`，`cmd/orchestrator/main.go:688`）。副作用必须同时知道：

1. 不创建审计 emitter ⇒ 窗口内无审计（`cmd/orchestrator/main.go:342-344`）；
2. 不跑启动期恢复（`cmd/orchestrator/main.go:335-340`）、不跑 GC ticker、不跑紧急超时扫与 stuck 扫（`cmd/orchestrator/main.go:734-746`）；
3. 流式 RPC 不受门禁（unary-only，`internal/app/maintenance.go:14`）。

**处置动作**：`kubectl set env deployment/orchestrator MAINTENANCE=true`（进）/ `MAINTENANCE-`（出），随后 `kubectl rollout status`。

- ⚠️ **破坏性等级：低但影响面大**。前置确认：① 通知在线值班与自动化（E2E 会整体失败）；② 窗口内不承诺审计完整性；③ 退出方式：dev 生命周期用 `MAINTENANCE` env 进出（`deploy/dev/dev.sh:1698,1783,1848`），人工操作请沿用同一方式。

**处置后验证**：`curl -sS http://127.0.0.1:8083/health` 仍 200（liveness 不看维护态），写请求返回 `maintenance`，只读请求仍成功。

### 7.5 环境级不可逆处置（最后手段）

只有当「数据本身不可信」或「集群拓扑需要重来」时才用；两者都**不是**修复业务故障的手段。

**先查什么**：是否已经排除 §2（迁移/依赖）、§6（不收敛）的可修复路径；是否已留诊断包（`data/diagnostics/`，`deploy/dev/dev.sh:114-159`）。

**确认依据（现状）**：`make dev-reset-data` = `deploy/dev/dev.sh reset-data`（`Makefile:120-122`），流程是 进入维护 → `pg_dump` 备份两个库 → 重建 4 个客户集群 → 前滚/回滚 schema → 退维护 → 重播种（`deploy/dev/dev.sh:1681-1861`）；`make dev-purge` = `dev.sh purge`（`Makefile:128-130`）删除**全部受管资源含 registry**（`deploy/dev/dev.sh:1876-1893`）。两者都要求显式 `CONFIRM=1`（缺 `CONFIRM=1` 时以退出码 2 失败，见 `docs/dev-environment.md:48,68`）。

**处置动作 + ⚠️ 破坏性等级：最高（数据与拓扑不可逆）**。前置确认：① 已确认备份文件存在且可读（`data/backups/`）；② 已确认没人正在跑 E2E 或发布（生命周期锁冲突会以 `environment_locked` 失败，`deploy/dev/lib/lock.sh:29-68`）；③ 已确认客户集群可以被重建（重建依赖 k3d 与 registry，`deploy/dev/dev.sh:1751-1763`）；④ 有明确的批准人与时间窗。

**处置后验证**：`make dev-status` 全绿 + `curl -sS http://127.0.0.1:8083/readyz` 200 + `make dev-seed` 幂等复跑成功（`deploy/dev/dev.sh:1538-1556`）。

## 8. 升级到新版本

**症状 / 触发**：要把环境收敛到当前工作树（或某个提交）的代码与配置版本。

**先查什么**

1. 当前生效镜像 tag：`kubectl get deploy -o jsonpath='{...image}'`（现状是 `content-sha256-<hash>`，不是 `:dev`）。
2. 本次改动是否触及 `migrations/`、`deploy/kustomize/dev/configs/`、`deploy/kustomize/**`、`api/proto/`（决定要不要重建镜像 + 跑迁移 + 重新生成码）。

**确认依据（现状：镜像的唯一真实构建/推送入口）**

- 生命周期阶段 `[4/7]`：`images_up`（`deploy/dev/dev.sh:1075-1093`）顺序构建清单 `webhook orchestrator operator auth notifier web fixture notification-sink`（`deploy/dev/dev.sh:1009`），并行度 `DEV_BUILD_PARALLELISM=2|4`（`deploy/dev/dev.sh:1019-1073`）。
- tag = 内容哈希：`content_hash()` 对 `go.mod`、`go.sum`、`cmd/<svc>`、`internal`（web 另加 `web/package*.json`，fixture 另加 `deploy/fixtures/{cmd,chart}`）求 sha256（`deploy/dev/dev.sh:809-828`），记入 `IMAGE_TAGS`（`deploy/dev/dev.sh:880-898`），命中即跳过构建（`docker manifest inspect`）。
- 构建与推送：`docker build --file deploy/docker/Dockerfile.<svc> --tag localhost:5001/release-<svc>:content-sha256-<hash>` + `docker push`（`deploy/dev/dev.sh:960-1000`）；集群侧靠 registry 镜像拉取，**全仓没有 `k3d image import`**。
- apply 时才把 manifest 里的 `release-<svc>:dev` 就地替换成内容寻址 tag（内存里 sed，仓库文件不变，`deploy/dev/dev.sh:1104-1118`），客户 agent overlay 同理（`deploy/dev/dev.sh:1298-1330`）。⇒ **`deploy/kustomize/**` 里永远写着 `:dev`，不要据此判断线上镜像**。
- 已核实存在的 `make` 入口（`Makefile`）：`dev-up`（:108-110）、`dev-down`（:112-114）、`dev-seed`（:116-118）、`dev-status`（:124-126）、`dev-reset-data`（:120-122）、`dev-purge`（:128-130）、`build-all`/`build-<svc>`（:50-76）、`run-<svc>`（:78-101）、`proto`（:274-275）、`dev-stage-*`（:285-357）、`e2e-*`（:155-243）、`api-*`（:247-268）、`quality`（:517-518）、`docker-build-operator`（:496-500）、`test-operator-image-sdk-only`（:502-516）、`check-docs`（:490-492）、`clean`（:533）、`help`（:540）。
- ⚠️ **`make docker-build-operator` 不是升级入口**：它 build 后 `docker save` 成 tarball，产物与 tag 是 `$(OPERATOR_IMAGE)`，供 REQ-061 的 SDK-only 镜像门禁（`Makefile:496-516`，门禁变量见 `Makefile:37-38`），不 push、不参与 `deploy/kustomize` 渲染。全仓**没有** `make docker-build`/`make docker-push`/`make images-up` 这类目标（`grep -E '^[a-z][a-zA-Z0-9_.-]*:' Makefile` 全量核对）。 <!-- check-docs:ignore 在陈述这些 make 目标不存在，不是可运行入口 -->
- 配置兼容性现状：viper 只反序列化已知键，**未写的键取 Go 零值/默认**（`internal/config/config.go:15-40`）；`log_level` 已接线（TASK-094：`startupLogger`/`applyLogLevel`，`internal/app/app.go:123/133`——空/非法值回落 debug）。`gc.*` 是 GC 唯一真读的段（`cmd/orchestrator/main.go:547-560`），`retention:` 块**没有任何消费者**（全仓 `UnmarshalKey` 只有 `gc`/`emergency`/`trust`）——dev overlay 曾恰好只写 `retention:` 导致集群 GC 跑默认值，TASK-094 已换成规范 `gc:` 块（`deploy/kustomize/dev/configs/orchestrator.dev.yaml:23-31`，§7-3），写错键名会被 `make check-config-keys` 拒绝。
- 迁移与回退风险：升级会在新进程启动时自动 `m.Up()`（§2.现状）；**没有与镜像同批的 schema 降级通道**（`RunMigrationsDown` 只被 dev 重置路径使用）。因此「新 schema + 旧二进制」通常是**不可逆**的（旧代码不认识新列/新约束）。

**处置动作（推荐顺序）**

1. `make proto`（若 `api/proto/**` 有变，`Makefile:274-275`）→ `make quality`（含 `check-docs`，`Makefile:517-518`）→ `make build-all` 级别的本地编译确认（`Makefile:50-51`）。
2. ⚠️ **破坏性等级：中**：`make dev-up`（幂等收敛：会重建/推送镜像并 `kubectl apply` 整套 manifest）。前置确认：① 无非终态 Operation 在跑（§6）；② 无人在跑 E2E（生命周期锁会以 `environment_locked` 拒绝并发，`deploy/dev/lib/lock.sh:29-68`，退出码 3）；③ 已知postgres 数据在 emptyDir 上，集群存活即数据存活（`deploy/kustomize/postgres/deployment.yaml:56-60`）。
3. 需要换 JWT/service token/CA（都是 kustomize secretGenerator 输入）：改动会让 Secret 哈希变化 ⇒ **Deployment 自动滚动**（`deploy/kustomize/dev/kustomization.yaml:39-70`），升级前先确认 §5 的掉线代价。

**升级失败的回退**

1. 首选镜像级回退：把该 Deployment 指回旧的内容寻址 tag（registry 卷保留旧镜像，`dev-down` 不清 registry，`deploy/dev/dev.sh:1480-1536`）：⚠️ 破坏性 `kubectl set image`，前置确认：旧 tag 仍在（`curl -s http://127.0.0.1:5001/v2/release-orchestrator/tags/list`）。
2. 或 `kubectl rollout undo`（同样只回退镜像，不回退 schema）。
3. 若 schema 已前滚且不可兼容 ⇒ 只剩两条：人工执行对应 `.down.sql`（有官方回滚单元但无执行入口，属**建议**范畴），或 ⚠️ `make dev-reset-data CONFIRM=1`（全量丢数据，前置确认见 §2.处置 3）。
4. 回退后必须重跑 `make dev-status` 与 `make dev-seed`（幂等，`deploy/dev/dev.sh:1538-1556`）。

**处置后验证**

```bash
KUBECONFIG=data/kubeconfig.yaml kubectl --context k3d-release-manager-control -n release-manager-dev \
  get deploy -o custom-columns=NAME:.metadata.name,IMAGE:.spec.template.spec.containers[0].image,READY:.status.readyReplicas
curl -sS http://127.0.0.1:8087/environment      # 经 web 反代，期望 environment_id/profile 一致
curl -sS http://127.0.0.1:8083/environment
make dev-status
```

## 9. 值班者速查

### 9.1 只读诊断命令（逐条已核对可运行对象存在）

```bash
# 0) 环境总览（幂等，只读；写 data/dev-status.json 并打印）
make dev-status

# 1) 管理面 Pod / 事件 / 描述（context 名来自 deploy/dev/dev.sh:45）
export KCFG=data/kubeconfig.yaml
export CTX=k3d-release-manager-control
KUBECONFIG=$KCFG kubectl --context $CTX -n release-manager-dev get pods -o wide
KUBECONFIG=$KCFG kubectl --context $CTX -n release-manager-dev get events --sort-by=.lastTimestamp | tail -30
KUBECONFIG=$KCFG kubectl --context $CTX -n release-manager-dev describe pod <pod>

# 2) 探针与元数据（宿主端口段 8082-8087，deploy/dev/lib/host.sh:16）
for p in 8082 8083 8085 8086 8087 8088; do printf '%s ' $p; curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:$p/readyz; done
curl -sS http://127.0.0.1:8083/health          # {"status":"ok","gc":{...}}
curl -sS http://127.0.0.1:8087/environment     # 反代 orchestrator（web/nginx.conf:93-101）

# 3) 指标（只有 auth 与 orchestrator 有 /metrics，见 observability.md）
curl -sS http://127.0.0.1:8083/metrics | grep -E '^(auth_|identity_)' | head -40
curl -sS http://127.0.0.1:8085/metrics | grep -E '^auth_' | head -40

# 4) 依赖直查（在 pod 内用镜像自带客户端）
KUBECONFIG=$KCFG kubectl --context $CTX -n release-manager-dev exec deployment/postgres -- pg_isready
KUBECONFIG=$KCFG kubectl --context $CTX -n release-manager-dev exec deployment/redis   -- redis-cli ping

# 5) 只读 SQL：会话 / 非终态 Operation / outbox / 审计量
KUBECONFIG=$KCFG kubectl --context $CTX -n release-manager-dev exec deployment/postgres -- \
  psql -U release_manager -d release_manager -Atc "select status,count(*) from operations group by status"
KUBECONFIG=$KCFG kubectl --context $CTX -n release-manager-dev exec deployment/postgres -- \
  psql -U release_manager -d release_manager -Atc "select operator_id,status,status_reason,last_heartbeat from sessions order by started_at desc limit 10"
KUBECONFIG=$KCFG kubectl --context $CTX -n release-manager-dev exec deployment/postgres -- \
  psql -U release_manager -d release_manager -Atc "select status,count(*) from outbox group by status"

# 6) 日志关键文案（与本文各章的「确认依据」一一对应）
KUBECONFIG=$KCFG kubectl --context $CTX -n release-manager-dev logs deploy/orchestrator --tail=1000 |
  grep -E 'failed to register service|connection_unavailable|migration_failed|heartbeat failed|sequence gap detected|audit buffer full|audit batch persistence failed|emergency target lock is stuck|recovery:' | tail -40

# 7) 客户集群 agent（kubeconfig 按集群分文件，deploy/dev/dev.sh:757）
KUBECONFIG=data/kubeconfigs/dev-customer-a-direct.yaml kubectl -n release-manager-customer get pods -o wide
KUBECONFIG=data/kubeconfigs/dev-customer-a-direct.yaml kubectl -n release-manager-customer \
  logs deployment/operator --tail=200 | grep -E 'disconnected|reconnect|session|helm'

# 8) 生命周期失败时脚本已自动落盘的诊断包（0700/0600）
ls -1 data/diagnostics/ | tail -5
# 内含 host-context.txt / pods.txt / resources.txt / events.txt / describe-pods.txt / log-<hash>.txt（deploy/dev/dev.sh:114-159）

# 9) Docker 侧只存在性判定（对象类型必须显式，项目红线）
docker container inspect k3d-release-manager-control-server-0 >/dev/null && echo control-node-ok
docker network inspect k3d-dev-customer-a-direct >/dev/null && echo customer-net-ok
curl -sS http://127.0.0.1:5001/v2/release-orchestrator/tags/list | head -c 500
```

**注意（现状）**：`dev.sh` **没有 `logs` 子命令**（verb 只有 `up|down|seed|reset-data|status|purge`，`deploy/dev/dev.sh:1922-1935`），也没有 `restart`/`migrate`；仓库里另有 `test/e2e/prerequisite/capture-logs.sh` 只在 E2E 门禁里被调用（`Makefile:189-197`）。

### 9.2 什么时候叫谁（现状可判定的分工）

| 现象 | 判定归属 | 依据 |
| --- | --- | --- |
| `/readyz` 报 `database`、日志 `connection_unavailable`/`migration_failed` | 平台/存储 owner（§2） | `internal/postgres/db.go:50-59`、`internal/postgres/migrate.go:23-52` |
| 会话 offline、证书被拒、enroll 失败 | 客户集群 operator owner（需该集群 kubeconfig 与新 enrollment token） | `internal/operator/errors.go:9-26`、`deploy/dev/dev.sh:1331-1335` |
| 401 风暴但探针全绿 | 认证 owner（§5） | `internal/auth/interceptor.go:55-124` |
| Operation 无 deadline 长期非终态 | 发布 owner（**属设计内等待**，先别升级级处理） | `internal/orchestrator/operation/recover.go:71-87` |
| `emergency target lock is stuck` | 需要 `release_admin`/`platform_admin` 级别决策（§7.3） | `internal/orchestrator/emergency_stuck.go:290-309`、`docs/glossary.md:112` |
| 审计缺失且日志 `audit buffer full` | 平台 owner + 合规 owner（审计完整性影响评估） | `internal/audit/emitter.go:86-98` |
| 通知没送达 | 通知 owner；先看 sink（`GET http://127.0.0.1:8088/notifications`，含 `dropped_count`）再看出站重试 | `cmd/notification-sink/main.go:87-132`、`internal/notifier/consumer.go:42-52` |

叫级判据（现状）：影响「写路径正确性」（错误回滚、错误紧急放行、审计丢失）→ 立即升级；只影响「可读性/及时性」（timeline 不动、通知延迟、`sequence gap detected` 噪声）→ 记录并观察。通知侧现状参数：轮询 10s、投递上限 10 次、指数退避 5s→24h、任务 deadline 24h 后 dead-letter、dead-letter 保留 30 天（`internal/notifier/consumer.go:42-52`、`internal/notifier/retry.go:24-32`）。

## 10. 已知不一致与需核实项（不要按文档想象行为）

以下是读码过程中发现的「文档/配置与代码不一致」，值班时以**代码**为准：

1. **审计失败是否阻塞业务提交**：`docs/decisions/ADR-011-controlled-emergency-change-and-convergence.md` 主张审计写入失败会阻塞对应业务提交；实现是异步 best-effort（`internal/orchestrator/service.go:1537-1549`，失败仅 Warn）。影响：合规叙述比实现更强。
2. ~~**`retention:` 死配置**~~ **TASK-094 已闭环**（§7-3）：dev overlay 已改为规范 `gc:` 块（`deploy/kustomize/dev/configs/orchestrator.dev.yaml:23-31`），集群 GC 真实按文件值跑（当前与默认同值：6h、90/30/90/7，`internal/orchestrator/cleanup.go:43-52`）；写回 `retention:` 会被 `make check-config-keys` 拒绝。
3. ~~**`log_level` 有键无实现**~~ **TASK-094 已闭环**：级别经 `applyLogLevel` 生效（`internal/app/app.go:133`），空/非法回落 debug；现网可用配置收敛日志量（详见 observability 文档 §1）。
4. ~~**`release-api` 的归档 worker 与关停刷盘不会被触发**~~ **TASK-094 已闭环**（签名匹配 + 编译期断言，`cmd/api/main.go:93,103`）；**部署形态仍无执行者**：集群里没有 `release-api` Deployment（`deploy/kustomize/services/kustomization.yaml:3-9`）。影响：审计 retention 与导出在集群环境仍无人跑——这是产品决策缺口，不是签名缺陷。
5. **spool 只写不读**：`internal/audit/spool.go:20-28` 的恢复器无生产调用者 ⇒ 落进 `audit_spool.jsonl` 的事件目前没有任何官方回灌路径。
6. **`ListOperations` 未实现**：`internal/orchestrator/service.go:1552-1554` ⇒ 没有「列出非终态 Operation」的 API，跨单排查只能走只读 SQL 或已知 ID。
7. **维护模式不覆盖流式 RPC**：`internal/app/maintenance.go:14-28` 使用 `connect.UnaryInterceptorFunc`（仅包 unary）⇒ 维护窗口内 `WatchOperation`、`CommandStream` 不受门禁（`api/proto/orchestrator/v1/orchestrator.proto:851`、`api/proto/operator/v1/operator.proto:238`）。
8. **审计查询响应字段被截断**：`internal/audit/audit_service_handler.go:164-174` 只回填 `id/action/status/duration_ms`，proto 里的 `actor`、`resource_type`、`resource_id`、`change_summary`、`metadata`、`created_at`（`api/proto/audit/v1/audit.proto`）**不返回**。影响：不能把 `QueryAuditEvents` 当作完整取证面。
9. **审计导出没有消费者**：`ExportAuditEvents` 只写入一条 `pending` 导出记录（`internal/audit/audit_service_handler.go:111-162`），`AuditExportStore` 接口只有 `CreateWithEvent`（`internal/store/store.go`），全仓无读取/推进该状态的代码 ⇒ 「导出」当前是占位能力。
10. **Web 侧审计调用无路由**：`web/src/connect/client.ts:57-61` 用同一 transport 建 `auditClient`，但 `web/nginx.conf:17-71` 没有 `/audit.v1.` location ⇒ 浏览器发起的审计查询会落到 SPA fallback。需核实是否有意（配合第 4 条看，更像缺口）。
11. **未接线组件**（现状 = 代码存在但生产不启用，排查时不要以它们为依据）：`cmd/operator/main.go:303-336` 的 `runSessionExpiry`（agent 模式下 `s.st == nil` 直接 continue）、`internal/operator/session_client.go`（带心跳的会话客户端，无 `cmd/` 引用）、`internal/store/postgres/operators.go:787` 的 `UpdateStatusReason` 与 `internal/store/sqlite/operators.go:897` 同名方法（仅测试调用）。注意 `internal/operator/session_registry.go` **已不在本清单**：TASK-098 起由网关 operator service 构造并 `Run`。
12. **需核实（无法从代码判定）**：① 「全局 outbox 序列导致跨 operator 误判 gap」是否为有意设计（`internal/operator/service.go:1156`）；② `agent.mode` 何时切到 `gateway`（`cmd/operator/main.go:28-38` 注释称 TASK-065 将移除该路径）；③ 生产形态（本仓只有 `deploy/kustomize/dev` 一套 overlay）下的 retention/告警责任方。

> 事实源：`internal/handler/health.go`、`internal/handler/ready.go`、`internal/app/app.go`、`internal/app/maintenance.go`、`internal/config/config.go`、`internal/postgres/db.go`、`internal/postgres/config.go`、`internal/postgres/migrate.go`、`internal/migration/migrate.go`、`internal/store/sqlite/db.go`、`internal/store/sqlite/operators.go`、`internal/store/postgres/operators.go`、`internal/store/postgres/audit.go`、`internal/store/store.go`、`internal/orchestrator/service.go`、`internal/orchestrator/rollback.go`、`internal/orchestrator/emergency.go`、`internal/orchestrator/emergency_stuck.go`、`internal/orchestrator/emergency_queries.go`、`internal/orchestrator/cleanup.go`、`internal/orchestrator/gc_health.go`、`internal/orchestrator/operation/state.go`、`internal/orchestrator/operation/recover.go`、`internal/orchestrator/audit.go`、`internal/operator/service.go`、`internal/operator/errors.go`、`internal/operator/session_registry.go`、`internal/operator/session_client.go`、`internal/operator/identity_metrics.go`、`internal/operator/agent/agent.go`、`internal/audit/emitter.go`、`internal/audit/spool.go`、`internal/audit/event.go`、`internal/audit/normalize.go`、`internal/audit/sanitize.go`、`internal/audit/archive_config.go`、`internal/audit/archive_worker.go`、`internal/audit/archiver.go`、`internal/audit/audit_service_handler.go`、`internal/audit/metrics.go`、`internal/redact/sanitize.go`、`internal/auth/interceptor.go`、`internal/auth/service.go`、`internal/auth/jwt.go`、`internal/auth/browser_session.go`、`internal/auth/service_token.go`、`internal/authorization/metrics.go`、`internal/authorization/module.go`、`internal/authorization/tracing.go`、`internal/notifier/consumer.go`、`internal/notifier/retry.go`、`internal/store/redis/adapter.go`、`cmd/api/main.go`、`cmd/auth/main.go`、`cmd/orchestrator/main.go`、`cmd/operator/main.go`、`cmd/notifier/main.go`、`cmd/webhook/main.go`、`cmd/notification-sink/main.go`、`cmd/store-migrate/main.go`、`cmd/devseed/main.go`、`migrations/embed.go`、`api/proto/audit/v1/audit.proto`、`api/proto/operator/v1/operator.proto`、`api/proto/orchestrator/v1/orchestrator.proto`、`api/gen/orchestrator/v1/orchestratorv1connect/orchestrator.connect.go`、`deploy/dev/dev.sh`、`deploy/dev/lib/host.sh`、`deploy/dev/lib/errors.sh`、`deploy/dev/lib/lock.sh`、`deploy/kustomize/base/pvc.yaml`、`deploy/kustomize/base/secret.yaml`、`deploy/kustomize/dev/kustomization.yaml`、`deploy/kustomize/dev/configs/orchestrator.dev.yaml`、`deploy/kustomize/dev/configs/notifier.dev.yaml`、`deploy/kustomize/services/kustomization.yaml`、`deploy/kustomize/services/orchestrator.yaml`、`deploy/kustomize/services/auth.yaml`、`deploy/kustomize/services/webhook.yaml`、`deploy/kustomize/services/notifier.yaml`、`deploy/kustomize/services/web.yaml`、`deploy/kustomize/services/notification-sink.yaml`、`deploy/kustomize/postgres/deployment.yaml`、`deploy/kustomize/postgres/init.sql`、`deploy/kustomize/redis/deployment.yaml`、`deploy/kustomize/customer-agent/base/deployment.yaml`、`deploy/kustomize/customer-agent/base/kustomization.yaml`、`deploy/kustomize/customer-agent/c1-direct/configs/operator.dev.yaml`、`deploy/docker/Dockerfile.operator`、`deploy/docker/Dockerfile.orchestrator`、`configs/orchestrator.dev.yaml`、`configs/auth.dev.yaml`、`configs/notifier.dev.yaml`、`configs/api.dev.yaml`、`web/nginx.conf`、`web/src/connect/client.ts`、`Makefile`、`docs/architecture.md`、`docs/dev-environment.md`、`docs/testing.md`、`docs/glossary.md`、`docs/cli.md`、`docs/dependencies.md`、`docs/decisions/ADR-011-controlled-emergency-change-and-convergence.md`、`docs/decisions/ADR-016-prometheus-otel.md`

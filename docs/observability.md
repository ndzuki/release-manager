# release-manager 可观测性现状（as-built）

本文只回答一个问题：**当前这套代码在运行时到底能观测到什么**。逐条以读码为据，行号即证据。

写作纪律：

- **现状**：有 `文件:行号` 支撑的既有行为。
- **建议**：当前不存在、需要实现后才成立的内容，全部显式标注，不与现状混写。
- 本文出现的 `建议` 不构成处置依据；处置依据见 `docs/runbook.md`。
- 与 `docs/architecture.md`（边界与契约）、`docs/dev-environment.md`（环境与命令）、`docs/testing.md`（门禁）、`docs/runbook.md`（值班）互补，不重复其结论。

## 1. 日志

**现状**

- 库与格式：标准库 `log/slog`，**JSON handler，输出到 `os.Stderr`**（`internal/app/app.go:123` 的 `startupLogger()`）。所有常驻服务共用这一个入口（`app.Run` 是每个 `cmd/*/main.go` 的唯一启动函数，例如 `cmd/auth/main.go:240`、`cmd/orchestrator/main.go:836`、`cmd/operator/main.go:368`、`cmd/webhook/main.go:65`、`cmd/notifier/main.go:126`、`cmd/api/main.go:143`、`cmd/notification-sink/main.go:142`）⇒ 采集侧就是容器 stderr，无文件日志、无日志级别路由、无异步 sink。
- **级别可配置（TASK-094 闭环）**：`startupLogger` 用 `slog.LevelVar` 构造 handler 并经 `slog.SetDefault` 安装为进程默认 logger（此前 `slog.SetDefault` 全仓零调用），`applyLogLevel`（`internal/app/app.go:133`）在 `LoadService` 之后把 `ServiceConfig.LogLevel`（`internal/config/config.go:131`）解析并写进 LevelVar（`config.ParseLogLevel`，`internal/config/loglevel.go`）：debug/info/warn/error 大小写不敏感；**空或非法 = debug + Warn 一条**——与接线前的硬编码行为一致，属向后兼容的缺省。行为测试 `internal/app/loglevel_test.go`；`make check-config-keys` 保证 `log_level` 不再有"有键无实现"的漂移。各服务配置里的 `log_level: debug`（例如 `deploy/kustomize/dev/configs/orchestrator.dev.yaml:2`、`configs/auth.dev.yaml:2`）从此是真实开关。
- 结构化字段约定（观察自实际调用点，非文档规定）：
  - 关系标识：`operator_id`、`session_id`、`cluster_id`、`customer_id`、`last_seen_sequence`（`internal/operator/service.go:466-472`）、`outbox_id`、`command_id`、`sequence`（`internal/operator/service.go:600-603`、`internal/operator/service.go:629-633`）、`op_id`（`internal/orchestrator/operation/recover.go:62-64`）、`operation_id`（`internal/orchestrator/emergency.go:325`）、`definition_id`（`internal/orchestrator/emergency_stuck.go:293`）、`intent_id`、`lock_path`、`terminal_at`（`internal/orchestrator/emergency_stuck.go:290-297`）、`command_id`/`retry_after`（`cmd/operator/main.go:104-105`）、`db`/`driver`（`cmd/orchestrator/main.go:299`、`cmd/api/main.go:55`）。
  - 通用：`error`（绝大多数 Warn/Error）、`count`、`code`、`status`。
  - request-id：拦截器 `contractsinterceptor.NewRequestIDInterceptor` 同时覆盖 unary 与 streaming（`internal/contracts/interceptor/requestid.go:26-33,50-59`），头名 `X-Request-ID`（`internal/contracts/errors.go:14`）；它把 request-id 写回响应头与错误 metadata，并**只在请求失败时**打日志：`request failed`（字段 `request_id`/`procedure`/`error`，`internal/contracts/interceptor/requestid.go:67-71`）⇒ **成功请求没有统一 request-id 日志行**，跨组件只能用 `operation_id` / `operator_id` / `session_id` 关联。
  - 审计 metadata 里带 `request_id`（`internal/operator/service.go:1405,1413,1431`），但只有 operator 侧审计这么做。
- 采样日志事件（可直接 grep 的真实文案，值班判据来自 `docs/runbook.md`）：
  - 启动：`store opened`（`cmd/orchestrator/main.go:299`）、`emergency config seeded`（`cmd/orchestrator/main.go:312`）、`operator agent wired`（`cmd/operator/main.go:235`）、`operator gateway runtime wired`（`cmd/operator/main.go:299`，gateway 模式）、`server error`、`shutting down`（`internal/app/app.go:209,212`）、`release-orchestrator started`（统一模板 `svc.Name()+" started"`，`internal/app/app.go:190`）。
  - 致命：`failed to load config`、`failed to register service`（`internal/app/app.go:127,147`，两条都紧跟 `os.Exit(1)`）。
  - operator/会话：`operator stream established via mTLS`、`heartbeat failed`、`sequence gap detected`、`resync request sent`、`delivering command`、`failed to mark session offline`（`internal/operator/service.go:466,580,1123,1128,599,582`）。
  - 恢复：`recovery: found non-terminal operations`、`recovery: stale cancelling operation, transitioning to failed`、`recovery: deadline exceeded, transitioning to timeout`、`recovery: non-terminal operation left running for operator reconnect`（`internal/orchestrator/operation/recover.go:47,62,75,83`）、`preflight operations resumed on restart`、`operations recovered on restart`（`cmd/orchestrator/main.go:422,338`）。
  - 审计：`audit buffer full`（`internal/audit/emitter.go:95`）、`audit batch persistence failed`（`internal/audit/emitter.go:135`）、`audit event rejected`（`internal/orchestrator/service.go:1522`）。
  - 鉴权/授权：`access denied`（`internal/auth/interceptor.go:92,103`，字段含 `user_id`、`organization_id`、`procedure`、`reason_code`）、`initial policy load failed`（`cmd/auth/main.go:162`）、`check active auth session failed`（`internal/auth/interceptor.go:119`）、`invalid service token hex`（`internal/auth/service_token.go:36`）。
  - 维护：`maintenance write rejected`（`internal/app/maintenance.go:20`）。
  - 通知：`notification consumer started`（含 `poll_interval`、`delivery_limit`、`job_deadline`、`dl_cleanup_max_age`，`internal/notifier/consumer.go:88-93`）、`notification consumer stopped`（`internal/notifier/consumer.go:98`）。
  - 生命周期/关停：`gc_ticker_stopped`（`internal/orchestrator/cleanup.go:265`）、`archive worker disabled (retention_days=0)`、`archive worker stopped`（`internal/audit/archive_worker.go:31,44`）。
- 脱敏：RPC 错误响应经 `contractsinterceptor.NewErrorSanitizeInterceptor` 规整（挂载点 `cmd/orchestrator/main.go:372,515`、`cmd/auth/main.go:183`），request-id 拦截器同时把错误 metadata 附上 `X-Request-ID`（`internal/contracts/interceptor/requestid.go:33-39`）⇒ 响应侧被规整；**但服务日志本身没有 DSN 级脱敏**：`redactDSN` 只存在于 store-migrate CLI 的 stderr 输出（`cmd/store-migrate/main.go:44,59,73,107`）⇒ DSN 口令若被包进启动错误，会原样进入 stderr 日志（`internal/app/app.go:147` 直接打印 `err`）；**审计与 timeline 的脱敏是另一条独立强制路径**（见 §5）。
- 环境元数据：`GET /environment` 返回 `{"service","environment","environment_id","production"}`（`internal/app/app.go:74-117`），值来自部署注入的 `APP_ENVIRONMENT` / `ENVIRONMENT_ID` / `DEV_PROFILE` / `APP_PRODUCTION`（`internal/app/app.go:91-116`）⇒ 可用于「这条日志/这个端点属于哪个环境实例」的判定，是现成的环境指纹面。

**建议（当前不存在）**

1. ~~把 `log_level` 真正接到 handler~~ **已实现（TASK-094）**：`startupLogger` + `applyLogLevel`（`internal/app/app.go:123/133`）。当初提示的实现陷阱如实留档：`Run` 被若干文档按行号引证（`docs/cli.md:7,35,42-49,76,387`、本文件 `:16,17,93,197,220`、`docs/configuration.md:365`），在 `:123` 附近插入行会整体下移这些锚点且 `make check-docs` 不校验语义——因此接线采用「`:123` 原位单行替换 + 空行槽位放 `applyLogLevel` 调用 + helper 追加到文件尾部」的零位移方案，上方引证行号全部保持。
2. 在日志侧统一注入 request-id（与 `NewRequestIDInterceptor` 同源），使「一次写请求 → 授权 → 审计 → outbox」可只用日志还原。
3. 为致命启动错误加退出码约定与 `os.Exit` 前的最后一条结构化摘要（当前只有两行文本，见 §2 无指标可替代）。

## 2. 指标

**先给结论：指标面存在，但只覆盖授权与 workload 身份收敛两条链路。**

**现状**

- 依赖确实存在：`go.mod:19` 引入 `github.com/prometheus/client_golang v1.24.1`；`docs/dependencies.md:111-114` 把它列进「会随产物分发」的 Go 模块清单（同表 `docs/dependencies.md:133-137` 亦列 OTel）。
- 端点：**只有两个服务暴露 `/metrics`**（全仓 `grep 'GET /metrics'` 只有这两处非测试命中）：
  - `cmd/auth/main.go:136-137`（私有 registry，挂在 8085 的单端口 mux 上）；
  - `cmd/orchestrator/main.go:316-319`（**共享 registry**，同时注册授权与身份指标）。
  路径挂在既有 HTTP 监听上，符合 `docs/decisions/ADR-016-prometheus-otel.md` 的「现有单端口 ServeMux 上注册 /metrics」决策。
- **webhook、notifier、notification-sink、operator agent、release-api 没有 `/metrics`**（同上 grep 结论）⇒ 这五类进程没有任何指标面。
- 指标清单一（授权，ADR-016 规定，实现见 `internal/authorization/metrics.go:23-71`）：

  | 指标 | 类型/标签 | 定义位置 |
  | --- | --- | --- |
  | `auth_decisions_total{result,actor_type}` | CounterVec | `internal/authorization/metrics.go:29-32` |
  | `auth_snapshot_stale_total` | Counter | `internal/authorization/metrics.go:33-36` |
  | `auth_source_version` | Gauge | `internal/authorization/metrics.go:37-40` |
  | `auth_checkpoint_version` | Gauge | `internal/authorization/metrics.go:41-44` |
  | `auth_policy_health` | Gauge（1 健康 / 0 不可用） | `internal/authorization/metrics.go:45-48` |
  | `auth_enforce_duration_seconds` | Histogram（`ExponentialBuckets(0.00005,2,12)`） | `internal/authorization/metrics.go:49-53` |
  | `auth_snapshot_rpc_duration_seconds` | Histogram（`ExponentialBuckets(0.0005,2,12)`） | `internal/authorization/metrics.go:54-58` |

  写入点：裁决计数与 stale 计数（`internal/authorization/module.go:349-351`）、enforce 耗时（`internal/authorization/module.go:135`）、快照 RPC 耗时（`internal/authorization/module.go:219`）、版本/健康 gauge（`internal/authorization/module.go:286-291`）。含义：`auth_source_version` 与 `auth_checkpoint_version` 的差值就是「本地快照落后多少」，`auth_policy_health == 0` 是授权链路 fail-closed 的直接信号。
- 指标清单二（workload 身份收敛，REQ-088）：`identity_report_buffered_total`、`identity_bound_after_inventory_total`、`identity_conflict_total`、`identity_report_dropped_total`、`identity_pending_purged_total`（`internal/operator/identity_metrics.go:42-58`，结构体注释 `internal/operator/identity_metrics.go:13-30`）；与授权指标共用 orchestrator 的 registry（注册点 `cmd/orchestrator/main.go:318`，注入 operator service 经 `newGatewayOperatorService`，`cmd/orchestrator/main.go:364`）⇒ **在 orchestrator 的 `/metrics` 一条路径上同时可读**。
- **没有 Go 运行时与进程指标**：全仓无 `collectors.NewGoCollector` / `NewProcessCollector` 调用（`grep 'collectors\.'` 零命中）⇒ `go_goroutines`、`go_memstats_*`、`process_resident_memory_bytes` 全都不可得。两个 Deployment 的内存 limit 只有 256Mi（`deploy/kustomize/services/orchestrator.yaml:76-82`）、notification-sink 128Mi（`deploy/kustomize/services/notification-sink.yaml:49`）⇒ **OOM 前兆在指标面完全不可见**。
- **没有采集侧配置**：`deploy/kustomize/` 下无 `ServiceMonitor`/`PodMonitor`/Prometheus 部署，也没有任何 scrape 配置（`grep monitoring.coreos.com` 零命中）⇒ `/metrics` 只是「可被抓」，当前没有任何东西在抓。可达性现状：web 的 nginx 没有 `/metrics` location（`web/nginx.conf:17-101`），所以宿主侧只能直连服务端口（`http://127.0.0.1:8083/metrics`、`http://127.0.0.1:8085/metrics`，映射依据 `deploy/dev/lib/host.sh:16`）。
- 已经实现但**没被导出**的指标：审计发射器的计数器 `received/accepted/persisted/rejected/bufferFull/storeFailure/spooled` 只有进程内 `Emitter.Metrics()` 读取口（`internal/audit/metrics.go:6-26`、`internal/audit/emitter.go:101`），全仓非测试调用者为 0 ⇒ 审计健康度既无指标也无日志型计数（只能靠 `audit buffer full` 文案 grep）。
- 指标面没有覆盖的核心链路（现状缺口，全部**无指标**）：Operation 状态机迁移、outbox 积压/投递延迟、会话在线数、Helm 执行时长与失败率、审计刷盘、通知投递与 dead-letter、DB 池使用率（`internal/postgres/db.go:46-49` 设了池但无 `sql.DBStats` 暴露）。

**建议（最小指标落点，未实现）**

1. 先补齐导出通道：`internal/app/app.go` 统一注册 `collectors.NewGoCollector()` + `NewProcessCollector()` 到每服务的 registry，并在 `webhook`/`notifier`/`notification-sink`/`operator agent`/`release-api` 上挂 `GET /metrics`（复用 `internal/authorization/metrics.go:73-76` 的 handler 模式）。理由：这是让「探针绿灯但进程在缓慢泄漏」可被发现的前提。
2. 审计：把 `internal/audit/metrics.go` 的快照做成 Prometheus collector（`release_audit_events_total{outcome}`、`release_audit_buffer_capacity/length` gauge、`release_audit_flush_failures_total`、`release_audit_spool_events_total`、`release_audit_spool_file_bytes` gauge）。
3. 投递：`release_outbox_pending`（gauge，按 status）、`release_command_delivery_seconds`（histogram，`created_at → delivered_at → acked_at`，三列已存在于 `outbox` 表，见 `internal/store/postgres/commands.go:15`）、`release_session_status{status,reason}` gauge。
4. 发布：`release_operation_transitions_total{type,from,to}`、`release_operation_age_seconds`（非终态 Operation 的 `now - created_at` gauge）、`release_operation_timeout_total{reason}`、`release_stuck_locks` gauge（数据源已是 `internal/orchestrator/emergency_stuck.go:21-45` 的扫描结果）。
5. 数据库：`release_db_pool_*`（来自 `sql.DBStats`）与 `release_migration_apply_seconds`（`internal/postgres/migrate.go:25-29` 外层计时）。
6. 基数纪律沿用 ADR-016：标签只用有界枚举（result/actor_type/status/from/to），禁止把 `operation_id`、`user_id`、token、membership 一类高基数或敏感值做成标签。
7. 部署侧：加 Prometheus（或本环境的临时 `curl` + 日志）抓取，否则上述指标依旧「可读但无人看」。

## 3. 追踪

**现状**

- OTel 依赖存在（`go.mod:24-26`，`go.opentelemetry.io/otel{,/sdk,/trace} v1.44.0`；`docs/dependencies.md:134-137`）。
- 装配只有一处：`authorization.InstallTracing()`（`internal/authorization/tracing.go:15-27`）——本地 `trace.NewTracerProvider(ParentBased(AlwaysSample()))` + W3C `TraceContext{}` 与 `Baggage{}` propagator，返回 `provider.Shutdown`。调用方：`cmd/auth/main.go:138`、`cmd/orchestrator/main.go:321`。**其它服务连 provider 都没装**。注释明确写着「Exporters remain deployment concerns」（`internal/authorization/tracing.go:16`）。
- **没有 exporter、没有 collector 配置**：全仓无 `otlptrace`/`stdouttrace`/`BatchSpanProcessor` 之类装配，`deploy/kustomize/` 里也没有 collector ⇒ span 只存在于进程内、随 provider 关闭丢弃 ⇒ **当前不存在跨进程可追踪能力**。
- 传播能力是真的存在一半：`TraceInterceptor()`（注释「unary」，`internal/authorization/tracing.go:29-30`）被装在 auth（`cmd/auth/main.go:184`）、orchestrator 管理面（`cmd/orchestrator/main.go:448,515`）以及 orchestrator→auth 的**客户端**（`cmd/orchestrator/main.go:329`）⇒ auth 与 orchestrator 之间的 unary 调用可携带/透传 W3C traceparent；但：
  - 流式 RPC 不被 tracing 覆盖：`TraceInterceptor()` 返回 `connect.UnaryInterceptorFunc`（`internal/authorization/tracing.go:30`），因此 `WatchOperation`、`CommandStream` 不产生 span；网关 handler 只挂 request-id 与 error-sanitize（`cmd/orchestrator/main.go:371-374`）；
  - agent ↔ 网关这条最关键链路没有安装 tracing（`cmd/operator/main.go` 无 `InstallTracing` 调用）；
  - trace id **不进日志**（`startupLogger` 构造的 handler 无 trace 关联，`internal/app/app.go:123`）⇒ 即使将来有 exporter，也无法从日志跳到 trace。
- 结论：**无可用追踪面**。可用的是「两个服务之间的 trace context 透传骨架」，其余为空白。

**建议（未实现）**

1. 先做「能看见」的最小闭环：在 `InstallTracing()` 内按配置决定 exporter（默认 `stdout`，可选 OTLP/HTTP），加 `OTEL_EXPORTER_OTLP_ENDPOINT`/`OTEL_TRACES_SAMPLING` 风格的环境开关；没有 endpoint 时保持今天的本地 provider 行为。
2. 给 agent 与网关装上 provider + 拦截器（含流式：用 Connect 的 streaming interceptor 在 `CommandStream` 建立时创建 root span，并把 `command_id`/`operation_id` 作为 span 属性）。
3. 加日志↔trace 关联：在 slog handler 外层注入 `trace_id`/`span_id`（仅当 span recording）。
4. 采样策略：`ParentBased(AlwaysSample)`（`internal/authorization/tracing.go:19`）在无 head 采样器时等于全采，接入 exporter 前需要显式降采样，否则量级不可控。

## 4. 健康与就绪

**现状**

| 端点 | 语义 | 是否可能失败 | 证据 |
| --- | --- | --- | --- |
| `GET /health` | liveness 目标；`{"status":"ok"}`，orchestrator 附 `gc` 子对象 | **不可能**（无条件 200） | `internal/handler/health.go:12-28`、`internal/app/app.go:139-143` |
| `GET /readyz` | readiness 目标；全通过 200，任一失败 503 + `{"status":"degraded","checks":{...}}` | 可能 | `internal/handler/ready.go:11-34`、`internal/app/app.go:152-158` |
| `GET /environment` | 环境指纹（service/environment/environment_id/production） | 不可能 | `internal/app/app.go:91-116,144` |
| `GET /metrics` | Prometheus 文本 | 不可能 | `cmd/auth/main.go:136-137`、`cmd/orchestrator/main.go:316-319` |
| `GET /notifications` | notification-sink 的 dev 投递观测（含 `dropped_count`） | 可能 5xx | `cmd/notification-sink/main.go:87-132` |

- readiness 贡献项（TASK-099 后**七个进程全部有真实检查**）：orchestrator `database`（2s 超时 ping）+ `cleanup_gc`（`cmd/orchestrator/main.go:248-266`，GC 不健康的定义是「距上次成功 ≥ 2×interval」，`internal/orchestrator/gc_health.go:113`）；auth `database` + `redis`（`cmd/auth/main.go:67-87`）；notifier `database`（`cmd/notifier/main.go:50-61`）；**webhook `orchestrator`**——GET 上游 `/readyz`，非 200/不可达即 NotReady（`cmd/webhook/main.go:90`，超时 2s，上游地址与 Register 客户端同源 `orchestratorBaseURL` `cmd/webhook/main.go:79`）；**operator agent `gateway_session`**——agent 与网关的 CommandStream 存活才 Ready，重连退避期间如实 NotReady（`cmd/operator/main.go:382` + `Agent.Connected()`，`internal/operator/agent/connected_test.go` 锁定生命周期；gateway 模式无出站会话，保持无检查）；**notification-sink `config`**——dev 测试替身无外部依赖，唯一前置是解码出的 `http_port` 可用，缺失即 fail-closed（`cmd/notification-sink/main.go:151`）。
- **`noop` 假就绪已退出集群路径**：没有实现 `ReadinessChecks` 的进程仍会得到 `{"noop": ok}`（`internal/app/app.go:134-136`），但 kustomize 里的六个 Deployment（含 customer agent）现已全部贡献真实检查；剩余 noop 只影响非集群进程（如本地 `cmd/api`）⇒ 历史上「Pod Ready 不代表 operator 在线」的误判面已闭环（TASK-099 AC3）。
- `/health` 的 `gc` 子对象语义：`disabled` 被改写为 `healthy` 上报（`cmd/orchestrator/main.go:275-277`），`status` 取值 `healthy|degraded|disabled`（`internal/orchestrator/gc_health.go:12-14`）⇒ 「GC 关掉」和「GC 正常」在 `/health` 上不可区分。**语义裁定（REQ-099 AC1 修订）**：`/health` 定位为纯 liveness——进程活着就无条件 200 是设计而非缺陷，「可失败性」一律落 `/readyz`（把依赖失败塞进 liveness 会在依赖抖动时引发重启风暴而非摘流量）。
- 探针配置现状（TASK-099 重写）：每个应用容器都有 **`startupProbe`（httpGet `/health`）**吸收启动/同步迁移窗口（`deploy/kustomize/services/orchestrator.yaml:65-90`：period 5s × failureThreshold 120 = 最长 10 分钟启动预算；auth/notifier/webhook/notification-sink/customer-agent 同形，见各 yaml），`readinessProbe` 指向 `/readyz`、`livenessProbe` 指向 `/health`，全部探针**显式 `timeoutSeconds`**（HTTP 3s、exec 5s）与显式 `failureThreshold`（startup 120/60，其余 3；customer agent readiness 12 以容忍重连窗）。此前全树零 `startupProbe`/零显式超时（K8s 默认 `timeoutSeconds=1`），叠加启动期同步跑迁移（`internal/app/app.go:146` → `cmd/orchestrator/main.go:524-545`）构成「慢迁移被 liveness 打断」的结构性风险，已消除。门禁：`make check-probes`（`deploy/dev/probes_gate_test.go`）遍历 `deploy/kustomize` 断言 startupProbe/显式超时/readiness-liveness 路径分离，负控制 `TestProbeGateRejectsHistoricalShape` 证明其可失败。
- postgres/redis 用镜像自带客户端探测（`pg_isready`、`redis-cli ping`）：`deploy/kustomize/postgres/deployment.yaml:40-59`、`deploy/kustomize/redis/deployment.yaml:28-45`——这两个仍是**全环境里唯二带真实失败语义的 liveness**（应用侧 liveness 恒 200 是上面的显式裁定）。
- 网关端口 8084 没有任何 HTTP 观测面（只有 OperatorService + SyncInventory 两条路由，`cmd/orchestrator/main.go:166-192`）⇒ 观测只能靠 TCP（`deploy/dev/dev.sh:1176-1180`）。
- 授权新鲜度在就绪之外**没有任何观测**：`auth_policy_health`（`internal/authorization/metrics.go:45-48`）是唯一信号，且 `/readyz` 不含它 ⇒ 授权快照陈旧时 Pod 仍 100% Ready，只有客户端拿到 `unavailable: authorization_snapshot_stale` 才知道（`internal/orchestrator/rollback.go:166-169`）。

**建议（未实现）**

1. `/health` 加一个「进程不再前进」的判据（例如最近一次成功推进后台循环的时间戳超龄），否则它只回答「进程还在」。注意与上面的 liveness 裁定保持一致：新判据若引入，应进 `/readyz` 或独立端点，而不是把失败语义塞回 liveness。
2. ~~为慢启动补 `startupProbe`、显式化 readiness `failureThreshold`~~ **已实现（TASK-099）**：全 kustomize 覆盖 + `make check-probes` 门禁（见上方现状）。
3. ~~给 operator agent 的 `/readyz` 加「与网关的流是否存活」检查项~~ **已实现（TASK-099）**：`gateway_session`（`cmd/operator/main.go:382`）。
4. 把 `auth_policy_health` 与「审计落盘是否前进」做成 readiness 或独立的 `/healthz/dependency`（谨慎：会让依赖抖动直接摘流量，需先定 SLO）。

## 5. 审计事件

**现状**

- 事件模型（`internal/store/store.go:1030-1044`）：`ID`、`ActorKind`、`ActorID`、`OrganizationID`、`Role`、`ResourceType`、`ResourceID`、`Action`、`Status`、`DurationMs`、`ChangeSummary`、`Metadata map[string]string`、`CreatedAt`。actor 种类枚举：`anonymous|user|service|api_key|system`（`internal/store/store.go` 的 `AuditActorKind` 常量块，紧随 `OperatorStatus` 定义之后）。查询过滤器 `AuditEventFilter` 只有 `OrganizationID/ResourceType/ResourceID/ActorID/Action/Status/Since/Until`（`internal/store/store.go:1047-1056`）⇒ **没有按 operation、也没有按 `request_id` 的过滤维度**。构造入口 `audit.NewEvent(...)`（`internal/audit/event.go:9-24`，`CreatedAt` 固定 UTC now）。
- 写路径强制校验与脱敏：`Normalize` 要求 actor kind + `resource_type`/`action`/`status` 非空，自动补 UUID，**并在此处完成脱敏**（`internal/audit/normalize.go:12-28`：`ChangeSummary` 走 `Sanitize`、metadata 走 `sanitizeMetadata`）⇒ 明文秘密不可能经由 emitter 落库（AGENTS 红线 6 的实现点）。
- 脱敏规则（`internal/redact/sanitize.go`）：`password|passwd|pwd|secret|token|api_key|apikey|private_key|privkey|cert_key|tls_key` 的 `k=v`/`k:v` 形态替换为 `${1}=****REDACTED****`（`internal/redact/sanitize.go:23`）；SQL `(...) VALUES ('...')` 形态（`internal/redact/sanitize.go:24`，字段名额外含 `credential`）；PEM 私钥块 → `****REDACTED PRIVATE KEY****`（`internal/redact/sanitize.go:25`）；证书块 → `****REDACTED CERTIFICATE****`（`internal/redact/sanitize.go:26`）；按字段名整体替换 `Sensitive()`（`internal/redact/sanitize.go:64-71`，名单 `internal/redact/sanitize.go:30-34`）；长度约束另有 `redact.Truncate`（`internal/redact/sanitize.go:73-75`，timeline 的 `last_error` 用它截到 500 码点，`internal/store/store.go:2611-2627`）。
- 观测到的 `resource_type` / `action` 清单（emit 点）：
  - `operator`：action `enrolled` / `superseded` / `renewed`（`internal/operator/service.go:1407-1429`）、`operator.revoked`（`internal/orchestrator/operator.go:139`；客户下线级联 `internal/orchestrator/customer.go:254`）；`enrollment_token`：action `operator.enrollment_token.revoked`（`internal/orchestrator/operator.go:206`）与 `created|replaced`（`internal/orchestrator/enrollment.go:75-77`）；
  - `enrollment token` 相关动作（`internal/orchestrator/enrollment.go:77`）；
  - `operation`：`emergency_change`（status 可为 `timeout` + metadata `error_code=operation_timeout`，`internal/orchestrator/emergency.go:335-357`）、`emergency_lock_release`（`internal/orchestrator/emergency_stuck.go:177,217`）、`emergency_lock_stuck`（status `stuck`，`internal/orchestrator/emergency_stuck.go:312-330`）；
  - `gc_cycle` + `cleanup.gc`、`bundle` + `bundle.unarchived`（`internal/orchestrator/audit.go:29,46`）；
  - trust root：`create_root|rotate_root|end_grace|retire_root|revoke_root`（`internal/trust/service.go:75,147,201,251,303`）与 `verify_trust`（`internal/orchestrator/service.go:1468,1504`）；
  - `audit_export` + `export.created`（`internal/audit/audit_service_handler.go:142-151`）。
  ⇒ 这份清单来自代码扫描，**不是枚举契约**：并非所有写路径都有审计（例如标准 Operation 的每次状态迁移不产审计事件，只有 timeline 事件，见 `internal/store/store.go:361-370`）。
- 传输与落库：全部进程内异步（`internal/audit/emitter.go`，参数与积压语义见 `docs/runbook.md` §4），错误码枚举 `invalid_event|buffer_full|store_unavailable|spool_failed`（`internal/audit/types.go:12-17`）。
- 保留与归档（现状）：
  - 配置键与默认：`audit.archive.retention_days`（默认 90）、`poll_interval`（6h）、`batch_size`（1000）、`archive_dir`（`data/archives`）、`compression`（只接受 `gzip_jsonl`）、`checksum_algorithm`（只接受 `sha256`）（`internal/audit/archive_config.go:8-29`）；`retention_days <= 0` 即关闭（`internal/audit/archive_config.go:19-29` + `internal/audit/archive_worker.go:28-33`）。
  - 动作：按 `created_at < cutoff` 分批读出 → gzip JSONL + sha256 sidecar → 已存在且校验匹配则只补删除（幂等）→ 任何编码/IO/校验失败**不删除任何事件**（`internal/audit/archiver.go:38-120`，归档前再次 `sanitizeAuditEvent`，`internal/audit/archiver.go:80` + `internal/audit/sanitize.go:29-42`）。
  - **执行者的启动面已修复（TASK-094 §7-9）**：`apiSvc` 原 `RunBackground(ctx, *slog.Logger)` 与 `internal/app/app.go:42-44` 的 `backgroundService`（`Run(context.Context)`）签名不匹配 ⇒ worker 从不启动；现已改为精确匹配并加编译期断言（`cmd/api/main.go:45-48/93`）。剩余现实：**集群里没有 `release-api` Deployment**（`deploy/kustomize/services/kustomization.yaml:3-9`）⇒ 归档只在本地 `make run-api` 进程里运行（详见 `docs/runbook.md` §10 第 4 条）。
- 查询与导出（现状有接口、能力不完整）：
  - 契约：`AuditService { Emit; QueryAuditEvents; ExportAuditEvents }`（`api/proto/audit/v1/audit.proto`），唯一挂载点 `cmd/api/main.go:65-73`（整个方法集用 bearer access token 保护：`audit.NewJWTInterceptor`）；`internal/audit/service.go:16-19` 的轻量变体只实现 `Emit`，Query/Export 由内嵌 `UnimplementedAuditServiceHandler` 返回 unimplemented。
  - 存储侧能力齐全：`Create/CreateBatch/Query/GetByID/Count/ListByResource/ListOlderThan/DeleteByIDs`（`internal/store/postgres/audit.go:18,86,133,190,196,269,300`；SQLite 同名方法齐全），过滤维度 `organization/resource_type/resource_id/actor_id/action/status/time range` + cursor 分页（`internal/audit/audit_service_handler.go:56-108`）。
  - **查询响应丢字段**：`toProtoAuditEvent` 只返回 `id/action/status/duration_ms`（`internal/audit/audit_service_handler.go:164-174`），proto 里声明的 `actor`、`resource_type`、`resource_id`、`change_summary`、`metadata`、`created_at` 全部不填（`api/proto/audit/v1/audit.proto` 的 `AuditEvent`）⇒ 取证必须回到 SQL 层（见 `docs/runbook.md` §9.1 第 5 组命令）。
  - **导出是占位**：`ExportAuditEvents` 只 insert 一条 `status="pending"` 记录 + 一条 `export.created` 审计事件（`internal/audit/audit_service_handler.go:111-162`，默认时间窗 30 天）；`AuditExportStore` 接口只有 `CreateWithEvent`，全仓没有任何 worker 读取或推进该状态 ⇒ 没有文件、没有下载入口、没有状态查询。
  - **部署形态不可达**：`web/nginx.conf:17-71` 只反代 `/auth.v1.`、`/orchestrator.v1.`、`/webhook.v1.`、`/operator.v1.`、`/notifier.v1.`，没有 `/audit.v1.` location（SPA fallback 在 `web/nginx.conf:103`），而前端确实用同一 transport 构造了 `auditClient` 并调用 query/export（`web/src/connect/client.ts:57-61`，调用点 `web/src/stores/audit.ts`）⇒ 浏览器侧审计页拿不到数据。
  - 另有 `internal/store/{sqlite,postgres}/audit_exports.go`（导出记录表双引擎实现）与 `api/kulala/audit.http`（本地 `make dev-stage-audit`（`Makefile:342-348`）跑起 release-api 后可手工查询）。

**建议（未实现）**

1. 补齐 `toProtoAuditEvent`（一行字段映射即可让查询面与契约一致），并让导出有执行者（消费 `audit_exports` 的 worker + 状态回写 + 产物落 `archive_dir`）。
2. 在 orchestrator 进程内也挂 `AuditService` 的只读子集（或补 `web/nginx.conf` 的 `/audit.v1.` location + 部署 release-api），否则「审计可查询」只是本地能力。
3. 把「审计是否前进」变成信号：导出 `release_audit_persisted_total` 与 `max(created_at)` 滞后秒数（§2 建议 2），并对 timeline 与 audit 的差集做定期核对。
4. 事件覆盖审计：为终态 Operation 迁移补一条最小审计（含 `request_id`），当前只有 timeline（`internal/store/store.go:361-370`），而 timeline 没有组织级过滤与保留策略。

## 6. 告警

**现状：没有告警面。**

- 仓库内没有任何告警规则、Alertmanager、通知规则文件或 SLO 定义（`grep -i "alert\|slo" deploy/` 无告警产物；`docs/dependencies.md` 亦无可观测后端依赖）。
- 唯一与「告警」同名的代码是 stuck-lock 的**进程内去重集合**（`internal/orchestrator/emergency_stuck.go:8-16` 的 `AlertedStuckLocks`）与其副作用：**一条 Warn 日志 + 一条审计事件**（`internal/orchestrator/emergency_stuck.go:290-299,312-330`，日志文案 `emergency target lock is stuck`，审计 `resource_type=operation`/`action=emergency_lock_stuck`/`status=stuck`）。扫描 60s 一轮、按 `intent_id` 去重、重启后去重集清空（`cmd/orchestrator/main.go:746,781-798`）。⇒ 「告警」的落地形态是**日志与审计**，没有任何东西会主动找人。
- 其他内置观测副产物：`make dev-status` 的 `data/dev-status.json`（`deploy/dev/dev.sh:1583-1636`）与生命周期失败时自动落盘的 `data/diagnostics/<ISO8601>/`（`deploy/dev/dev.sh:114-159`）；两者都是**事后取证**，不是告警。
- 探针是唯一「自动发现」机制：`/health` 按裁定恒 200（纯 liveness），`/readyz` 已全服务真实化（§4），startupProbe 兜住慢启动，`make check-probes` 防漂移。
- 授权链路的「健康」只有指标（`auth_policy_health`），无人消费 ⇒ 等同于没有。
- 现状下可用的「人肉告警路径」只有通知子系统本身：notifier 消费 `notification_jobs`（轮询 10s、上限 10 次、退避 5s→24h、24h 后 dead-letter、dead-letter 保留 30 天，`internal/notifier/consumer.go:42-52`、`internal/notifier/retry.go:24-32`），sender 只有 webhook 一种实现，投递目标取自 job 的 `recipient` 字段（`cmd/notifier/main.go:80-81`、`internal/notifier/webhook.go:46-47,60-77`，HTTP 客户端超时 30s 见 `internal/notifier/webhook.go:51-53`），非 webhook 通道一律 `ErrCodeInvalidRecipient`（`internal/notifier/webhook.go:75-77`）⇒ dev 里事件能落到 sink，只是因为 fixture 把 recipient 写成了 sink 地址（sink 缓冲容量 100，`cmd/notification-sink/main.go:142`）⇒ **它是「发布事件的用户通知」，不是运维告警**，且 sink 有容量上限并会报 `dropped_count`（`cmd/notification-sink/main.go:108-131`）。

**建议（最小告警集，未实现）**

| 告警 | 触发条件 | 数据源现状 |
| --- | --- | --- |
| 控制面不可用 | Pod not ready > 2m 或 liveness 重启 | K8s（现成） |
| 依赖失联 | `/readyz` 503 且 `checks` 含 `database`/`redis` | 现成（§4） |
| 启动即失败 | `failed to load config` / `failed to register service` / CrashLoopBackOff | 现成日志（§1） |
| 迁移失败 | 日志 `migration_failed` 前缀 | 现成日志 |
| operator 大面积失联 | 会话非 online 计数 > 阈值 | **需新指标**（§2 建议 3） |
| 审计积压/丢失 | `audit buffer full`、`audit batch persistence failed`、spool 文件 > 0 字节 | 现成日志 + 文件（§2 建议 2 补指标） |
| 鉴权风暴 | `auth_decisions_total{result="deny"}` 速率、`auth_policy_health == 0`、401 比例 | 指标现成，抓取缺失 |
| 发布不收敛 | 非终态 Operation 年龄 > 阈值（区分有无 `deadline`） | **需新指标** |
| stuck lock 新增 | `emergency target lock is stuck` / `emergency_lock_stuck` 审计事件 | 现成日志+审计 |
| 通知 dead-letter 增长 | dead-letter 计数上升 | **需新指标** |

落地顺序建议：先解决「抓取/保留」这一层（没有任何东西在抓 `/metrics`），再补 §2 的最小指标，最后才谈阈值。阈值必须由 REQ 拥有，不在本文凭空定。

## 7. 信号总表

| 信号 | 现状 | 证据 | 缺口 |
| --- | --- | --- | --- |
| 日志 | slog JSON → stderr，级别由 `log_level` 控制（缺省/非法=debug） | `internal/app/app.go:123`（`startupLogger`/`applyLogLevel`）、`internal/config/loglevel.go` | 无 request-id 字段；级别收敛已可用（TASK-094 闭环） |
| 指标 | 仅授权 + 身份收敛，仅 auth/orchestrator 两个 `/metrics` | `internal/authorization/metrics.go:23-71`、`internal/operator/identity_metrics.go:42-58`、`cmd/auth/main.go:136-137`、`cmd/orchestrator/main.go:316-319` | 无运行时/进程指标（`grep collectors\.` 零命中）；无抓取配置；其余 5 类进程 0 指标 |
| 追踪 | 只装 provider + W3C 传播，无 exporter | `internal/authorization/tracing.go:15-27` | 无跨进程追踪能力；agent 侧未装配；trace id 不进日志 |
| liveness | `/health` 恒 200＝纯 liveness（REQ-099 裁定），orchestrator 附 `gc` | `internal/handler/health.go:12-28` | 无「前进性」判据（§4 建议 1）；`gc` 的 disabled 被报成 healthy（`cmd/orchestrator/main.go:275-277`） |
| readiness | `/readyz` 200/503 + checks；七进程全真实 | `internal/handler/ready.go:11-34` | 探针层已有 startupProbe + 显式超时（`make check-probes`）；`auth_policy_health` 未入检查 |
| readiness 覆盖面 | database / redis / cleanup_gc / orchestrator-upstream / gateway_session / sink-config | `cmd/orchestrator/main.go:248-266`、`cmd/auth/main.go:67-87`、`cmd/notifier/main.go:50-61`、`cmd/webhook/main.go:90`、`cmd/operator/main.go:382`、`cmd/notification-sink/main.go:151` | 本地 `cmd/api` 仍是 `noop`（`internal/app/app.go:134-136`，非集群路径） |
| 环境指纹 | `GET /environment` | `internal/app/app.go:91-116` | 无版本/commit 字段（镜像用内容寻址 tag，`deploy/dev/dev.sh:809-828`，但运行时读不到自身版本） |
| 审计事件 | 写路径强制脱敏 + 异步批量落库 | `internal/audit/normalize.go:12-28`、`internal/redact/sanitize.go:23-26`、`internal/audit/emitter.go:123-157` | 无指标、spool 无回灌（`internal/audit/spool.go:20-28`）、维护模式不创建 emitter（`cmd/orchestrator/main.go:342-344`） |
| 审计查询 | RPC + 双引擎 store 齐全 | `api/proto/audit/v1/audit.proto`、`internal/store/postgres/audit.go:133,196` | 响应只回 4 字段（`internal/audit/audit_service_handler.go:164-174`）；部署形态无路由/无服务（`web/nginx.conf:17-71`） |
| 审计导出 | 只落一条 `pending` 记录 | `internal/audit/audit_service_handler.go:111-162` | 无消费者、无产物、无状态推进 |
| 审计保留/归档 | 实现完整（gzip+sha256+幂等），worker 随 api 进程启动（`cmd/api/main.go:93`，TASK-094 闭环） | `internal/audit/archiver.go:38-120`、`internal/audit/archive_config.go:19-29` | 集群无 release-api Deployment ⇒ 只在本地进程跑 |
| Timeline（单 Operation） | snapshot + timeline + heartbeat 流；`ROLLOUT_PROGRESS` 由 agent 的 rollout reporter 上报（`internal/operator/agent/rollout_progress.go:56,73-74`，接线 `cmd/operator/main.go:220`） | `api/proto/orchestrator/v1/orchestrator.proto`（`WatchOperation`）、`internal/store/store.go:361-370` | 只有 `WatchOperation` 一条路；`ListOperations` 未实现（`internal/orchestrator/service.go:1531-1533`）⇒ 无全局视角 |
| 授权新鲜度 | 2 个 gauge + 1 个 counter | `internal/authorization/metrics.go:37-48`、`internal/authorization/module.go:286-291` | 不在 readiness 内 ⇒ Pod 全绿也可能整片 `authorization_snapshot_stale` |
| 会话/投递健康 | 只有日志 | `internal/operator/service.go:580,1123` | 无指标；在线性评估器未接线（`internal/operator/session_registry.go:27-104`） |
| 通知投递 | 队列语义齐全（退避/dead-letter/清理） | `internal/notifier/consumer.go:42-52`、`internal/notifier/retry.go:24-32` | 无指标；dev 侧 sink 缓冲 100 且会丢（`cmd/notification-sink/main.go:111-130`） |
| 告警 | **不存在** | 全仓无规则文件；`AlertedStuckLocks` 只产日志+审计（`internal/orchestrator/emergency_stuck.go:8-16,290-299`） | 见 §6 建议表 |
| 事后取证 | `data/diagnostics/<ISO8601>/`、`data/dev-status.json` | `deploy/dev/dev.sh:114-159,1583-1636` | 仅生命周期失败时生成；无 `logs` 子命令（`deploy/dev/dev.sh:1922-1935`） |

## 8. 判定：哪些能力「不存在」

明确判定为**当前不存在**（每条都经全仓检索验证，非「我没找到」）：

1. **分布式追踪导出**：无 exporter / 无 collector / 无采样策略配置（`internal/authorization/tracing.go:15-27` 是全部装配）。
2. **跨进程 trace↔log 关联**：无（`internal/app/app.go:123` 的 handler 无任何注入）。
3. **Go 运行时与进程指标**：无（`collectors.*` 零命中）。
4. **除 auth、orchestrator 之外的 `/metrics` 端点**：无（`grep 'GET /metrics'` 只有两处，均在 `cmd/`）。
5. **指标抓取 / 告警规则 / SLO**：无（`deploy/` 内无任何可观测后端对象）。
6. **审计发射器指标的对外暴露**：无（`internal/audit/metrics.go:6-26` 无生产调用者）。
7. **审计 spool 自动回灌**：无（`internal/audit/spool.go:20-28` 仅测试调用）。
8. ~~**审计归档的运行时执行**：无~~ **TASK-094 闭环**：`apiSvc.Run/Close` 已匹配 app 生命周期接口（`cmd/api/main.go:93/103` + 编译期断言），本地 api 进程内归档循环真实启动；「集群环境无归档」仍成立（无 release-api Deployment）。
9. **审计导出的产物与状态机**：无（`AuditExportStore` 只有 `CreateWithEvent`）。
10. **审计查询的完整字段返回**：无（`internal/audit/audit_service_handler.go:164-174`）。
11. **Operation 全局枚举能力**：无（`internal/orchestrator/service.go:1531-1533`）。
12. **会话在线性的生产判定（suspect/offline 推进器）**：无（`internal/operator/session_registry.go:27-104` 的构造与 `Run`/`evaluate` 无生产调用者；`cmd/operator/main.go:303-336` 在 agent 模式下 `s.st == nil` 空转）。
13. **DB 池使用率指标、迁移耗时指标**：无（证据见 §2）。日志级别控制与 startupProbe 已不再是缺口——TASK-094/TASK-099 分别落地（§1、§4）。

无法从代码判定（需环境验证或产品决策）：

- 生产形态下这些信号由谁抓取、保留多久、阈值归属哪条 REQ（本仓只有 `deploy/kustomize/dev` 一套 overlay）。
- `NewRequestIDInterceptor` 是否另有日志输出（未逐行读完该文件）。
- 「全局 outbox sequence 造成跨 operator gap 误判」是设计还是缺陷（`internal/operator/service.go:1111`）。

> 事实源：`internal/app/app.go`、`internal/config/loglevel.go`、`internal/app/loglevel_test.go`、`internal/operator/agent/connected_test.go`、`deploy/dev/probes_gate_test.go`、`internal/handler/health.go`、`internal/handler/ready.go`、`internal/config/config.go`、`internal/authorization/metrics.go`、`internal/authorization/module.go`、`internal/authorization/tracing.go`、`internal/operator/identity_metrics.go`、`internal/operator/service.go`、`internal/operator/session_registry.go`、`internal/operator/agent/agent.go`、`internal/orchestrator/service.go`、`internal/orchestrator/emergency_stuck.go`、`internal/orchestrator/gc_health.go`、`internal/orchestrator/cleanup.go`、`internal/orchestrator/operation/recover.go`、`internal/audit/emitter.go`、`internal/audit/metrics.go`、`internal/audit/spool.go`、`internal/audit/normalize.go`、`internal/audit/sanitize.go`、`internal/audit/event.go`、`internal/audit/archive_config.go`、`internal/audit/archive_worker.go`、`internal/audit/archiver.go`、`internal/audit/audit_service_handler.go`、`internal/audit/service.go`、`internal/redact/sanitize.go`、`internal/notifier/consumer.go`、`internal/notifier/retry.go`、`internal/store/store.go`、`internal/store/postgres/audit.go`、`internal/store/postgres/operators.go`、`internal/auth/interceptor.go`、`internal/auth/service_token.go`、`internal/postgres/db.go`、`internal/migration/migrate.go`、`cmd/auth/main.go`、`cmd/orchestrator/main.go`、`cmd/operator/main.go`、`cmd/notifier/main.go`、`cmd/api/main.go`、`cmd/notification-sink/main.go`、`go.mod`、`migrations/embed.go`、`api/proto/audit/v1/audit.proto`、`api/proto/orchestrator/v1/orchestrator.proto`、`web/nginx.conf`、`web/src/connect/client.ts`、`web/src/stores/audit.ts`、`deploy/kustomize/services/orchestrator.yaml`、`deploy/kustomize/services/kustomization.yaml`、`deploy/kustomize/postgres/deployment.yaml`、`deploy/kustomize/redis/deployment.yaml`、`deploy/kustomize/customer-agent/base/deployment.yaml`、`deploy/dev/dev.sh`、`deploy/dev/lib/host.sh`、`Makefile`、`docs/dependencies.md`、`docs/decisions/ADR-016-prometheus-otel.md`、`docs/decisions/ADR-011-controlled-emergency-change-and-convergence.md`、`docs/architecture.md`

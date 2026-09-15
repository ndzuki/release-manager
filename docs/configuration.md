# 配置文档（configuration.md）

本文是 release-manager 仓库配置面的权威说明：每个二进制如何读配置、有哪些配置文件、每个配置键的含义与取值来源、环境变量的实际引用点、Secret 约定，以及改配置的正确姿势。所有事实均以当前代码与配置文件逐条核实（文末「事实源」列出文件清单）；核不到读取点的键全部如实标注「未在代码中找到对应读取」，并写明搜索范围。

## 1. 配置加载层次

### 1.1 统一入口与优先级

除 `cmd/e2e`、`cmd/devseed`、`cmd/store-migrate` 与各 CI 质量工具外，所有服务二进制都经 `internal/app/app.go` 的 `Run(configPath, svc)` 启动（`internal/app/app.go:122`），真实加载器是 `config.LoadService`（`internal/config/config.go:307`，基于 viper）。生效优先级：

1. **CLI flag**（显式传入的 flag 值）——注意：flag 只决定「读哪个文件」和少量进程参数（signing key、db 路径等），**没有** `BindPFlag`，flag 不进 viper。
2. **环境变量**——`LoadService` 内的 `bindDatabaseEnvironment`（`internal/config/config.go:271`）对 24 个键做 `viper.BindEnv`，绑定键一旦在环境中存在即**覆盖文件值**（viper 语义：env > config file）。
3. **YAML 文件**（`--config` 指定路径）。
4. **代码默认值**——各配置块的 `WithDefaults()`（如 `internal/config/config.go:176/195/235/251`）。

部分 flag 的**默认值本身**来自环境变量（`envOr`/`os.Getenv` 模式，如 `--signing-key` 默认 `envOr("JWT_SIGNING_KEY", "change-me-in-production")`，`cmd/auth/main.go:237`），所以这类参数上的链条是：显式 flag > 对应环境变量 > flag 默认常量。

**例外（原始二次读取，env 不参与）**：`cmd/orchestrator` 在 `LoadService` 之外另起裸 viper 直接重读同一个 YAML 文件解析 `gc`（`cmd/orchestrator/main.go:555`）、`emergency`（`:625`）、`trust`（`:677`）三个块（`UnmarshalKey`）。这三块只能改文件，不能用环境变量覆盖。

**死代码（勿作依据）**：`config.Load` 与包级 `Config` 结构体（含 `RuntimePullPreflight` 字段，`internal/config/config.go:66/93`）无任何调用方；`WatchConfigFile`（`internal/config/config.go:330`，fsnotify 防抖 500ms 的热加载工具）在 `cmd/` 中无调用方——本仓库**没有**运行时热改配置机制，改文件必须重启进程或滚动 Deployment。

### 1.2 各二进制的 CLI flag（摘自 `cmd/*/main.go` 的 `flag.` 定义）

| 二进制 | flag | 默认值 | 用途 |
|---|---|---|---|
| release-webhook | `--config` | `configs/webhook.dev.yaml`（`cmd/webhook/main.go:57`） | 配置文件路径 |
| | `--orchestrator-url` | ""（空则回退 `http://localhost:8083`，`cmd/webhook/main.go:32-34`） | BundleService 上游 |
| | `--service-token` | `envOr("DEV_WEBHOOK_SERVICE_TOKEN", "")`（`cmd/webhook/main.go:62`） | 包入库服务令牌 |
| release-orchestrator | `--config` | `configs/orchestrator.dev.yaml`（`cmd/orchestrator/main.go:827`） | 配置文件路径 |
| | `--target-env` | `staging`（`cmd/orchestrator/main.go:828`） | 目标环境标签（传入 `orchestrator.NewService`） |
| | `--signing-key` | `envOr("JWT_SIGNING_KEY", "change-me-in-production")`（`:833`） | JWT 签名密钥 |
| release-operator | `--config` | `configs/operator.dev.yaml`（`cmd/operator/main.go:359`） | 配置文件路径 |
| | `--db` | `data/operator.db`（gateway 模式） | 本地 SQLite |
| | `--command-db` | `data/operator-commands.db` | 命令/身份持久化（agent 与 gateway 都用） |
| | `--orchestrator-addr` | `https://operator-gateway.dev.release-manager.local:30084` | agent 连管理面网关 |
| | `--kubeconfig` | ""（空则 in-cluster 或默认配置） | Helm/k8s 客户端 |
| | `--install-atomic` | true | Helm install atomic |
| | `--install-timeout` | 5m | Helm 超时 |
| release-auth | `--config` | `configs/auth.dev.yaml`（`cmd/auth/main.go:232`） | 配置文件路径 |
| | `--signing-key` | `envOr("JWT_SIGNING_KEY", ...)` | JWT 签名密钥 |
| release-notifier | `--config` | `configs/notifier.dev.yaml`（`cmd/notifier/main.go:124`） | 配置文件路径（仅此一个） |
| release-api | `--config` | `configs/api.dev.yaml`（`cmd/api/main.go:111`） | 配置文件路径 |
| | `--db` | `data/api.db` | SQLite |
| | `--signing-key` | `"change-me-in-production"`（**无** env 回退，`cmd/api/main.go:113`） | JWT 签名密钥 |
| release-notification-sink | `--config` | `deploy/kustomize/dev/configs/notification-sink.dev.yaml`（`cmd/notification-sink/main.go:139`） | 配置文件路径（注意默认值直接指向 kustomize 目录） |
| e2e | `--env-config`（必填）、`--stages`（all）、`--timeout` 5m、`--total-timeout` 25m、`--output-dir` `./e2e-results`、`--parallel`、`--keep-on-failure`、`--snapshot-full`；cleanup 子命令 `--env-config`、`--output-dir`、`--baseline-file`（`cmd/e2e/main.go:637-680`） | | E2E harness |
| devseed | 见 `cmd/devseed/main.go:36-58`（`-print-fixture-version`、`-ensure-mtls-ca`、`-mtls-ca-dir`、`-operator-timeout`、`-seed-retries`、`-stop-after`、`-reset`、`-orchestrator/-webhook/-auth`、`-admin-user`、`-admin-password`、`-deployer-user`、`-deployer-password`、`-reader-password`、`-e2e-runner-password`、`-trust-root-private-key`、`-data-dir`、`-database-dsn`） | 多个默认值取 `envOr`/`os.Getenv`（见 §4.2） | dev 播种 |
| store-migrate | `--source`（必填）、`--target-dsn`（默认 `os.Getenv("RELEASE_MANAGER_DATABASE_DSN")`，仍为空则报错）、`--migrations`（`migrations`）（`cmd/store-migrate/main.go:18/35-39`） | | SQLite→PostgreSQL 搬迁 |
| imagecheck | `--dockerfile`（`deploy/docker/Dockerfile.operator`）、`--policy`（`imagecheck.operator.yaml`）、`--archive`（`-`=stdin） | | 镜像门禁 |
| installgate | `--quarantine`（`install-sdk.quarantine.yaml`）、`--scenario`/`--rule-id`/`--message`（必填） | | install-sdk 例外门禁 |
| sdkcheck | `--exceptions`（`sdkcheck.exceptions.yaml`）、`--build-tags` + 包参数（`cmd/sdkcheck/main.go:19-21`） | | SDK-only 静态门禁 |
| reqcheck | 位置参数（markdown 路径），无 flag | | 需求追溯门禁 |

## 2. 配置文件清单与权威关系

| 文件 | 叶子键数 | 使用方 |
|---|---|---|
| `configs/webhook.dev.yaml` | 2 | 本地 `make run-webhook` |
| `configs/orchestrator.dev.yaml` | 30 | 本地 `make run-orchestrator` |
| `configs/operator.dev.yaml` | 16 | 本地 `make run-operator` |
| `configs/auth.dev.yaml` | 6 | 本地 `make run-auth` |
| `configs/notifier.dev.yaml` | 4 | 本地 `make run-notifier` |
| `configs/api.dev.yaml` | 8 | 本地 `make run-api`（api 无集群 Deployment，见 §2.3） |
| `configs/e2e.dev.yaml` | 27 | **非运行时配置源**，见 §3.9 |
| `deploy/kustomize/dev/configs/webhook.dev.yaml` | 2 | 集群 webhook Deployment（configMapGenerator） |
| `deploy/kustomize/dev/configs/orchestrator.dev.yaml` | 23 | 集群 orchestrator |
| `deploy/kustomize/dev/configs/auth.dev.yaml` | 9 | 集群 auth |
| `deploy/kustomize/dev/configs/notifier.dev.yaml` | 4 | 集群 notifier |
| `deploy/kustomize/dev/configs/notification-sink.dev.yaml` | 2 | 集群 notification-sink（**只存在于 kustomize 目录**，`configs/` 下无对应文件） |
| `deploy/kustomize/customer-agent/c{1-direct,2-cache,3-replicated,4-mixed}/configs/operator.dev.yaml` | 各 8 | 四个客户集群的 release-operator（agent 模式） |

合计叶子键出现次数 165；不同点分键路径 84（计数命令见 §8 自检）。

### 2.1 权威关系

- **集群（dev k3d）内权威**是 `deploy/kustomize/dev/configs/*.yaml` 与 `deploy/kustomize/customer-agent/*/configs/*.yaml`：`deploy/kustomize/dev/kustomization.yaml:11-31` 用 `configMapGenerator` 把它们逐服务生成为 `*-config` ConfigMap，由 `deploy/kustomize/services/*.yaml` 挂到 `/configs/` 并以 `--config /configs/<svc>.dev.yaml` 启动。`configs/` 根目录同名文件是**本地进程配置**，不是集群配置的副本。
- **本地 `make run-*` / `dev-stage-*` 权威**是 `configs/*.dev.yaml`（Makefile 中显式传参，如 `make run-api` 即 `release-api --config configs/api.dev.yaml --db data/api.db --signing-key change-me-in-production`，`Makefile:100-101`）。

### 2.2 逐文件 diff 实测结论（`diff configs/<svc>.dev.yaml deploy/kustomize/dev/configs/<svc>.dev.yaml`）

- **webhook**：完全相同。
- **auth**：kustomize 版把 `database.driver: sqlite`+`dsn: data/auth.db` 换成 `driver: postgres`+`dsn: postgres://release_manager:dev-release-manager@postgres:5432/release_manager?sslmode=disable`；新增 `redis.address: redis:6379`、`login_rate_limit: {max_attempts: 1000, window: 1m}`（文件内注释：dev fixture 复位时高频重登，生产默认 5/min 会触发 `resource_exhausted`）。
- **notifier**：仅换 postgres，DSN 指向**独立库** `.../release_notifier?sslmode=disable`。
- **orchestrator**：driver/dsn→postgres；`authorization.auth_url` `http://localhost:8085`→`http://auth:8085`；本地 `gc:` 块在 kustomize 版被 `retention:` 块取代（**注意：`retention.*` 无代码读取，集群 GC 实际跑默认值，见 §3.5 与 §7**）；`gateway.enabled: false→true` 且删除 `gateway.ca_*_path` 两行（防漂移测试 `TestOrchestratorDevConfigWiresTopLevelCA` 锁定这两键不得出现在 overlay）；`ca.key_path/cert_path` 从 `data/gateway-ca.*` 改为 `/data/gateway-ca.key|crt`（由 Secret `release-manager-mtls-ca` 以 subPath 挂入）。
- **api、e2e**：无 kustomize 对应文件。**notification-sink**：反向——只有 kustomize 文件。
- **customer-agent 各 overlay 对比 `configs/operator.dev.yaml`**：只保留 `http_port`、`log_level`、`agent.{mode,customer_id,cluster_id,operator_name}`、`ca.cert_path`，加顶层 `registry_plain_http: true`；`agent.customer_id` 用 seed 的 UUID（c1/c2=dev-customer-a `11111111-1111-4111-8111-111111111111`，c3/c4=dev-customer-b `22222222-...`）；`ca.cert_path: /data/gateway-ca.crt`（Secret `operator-gateway-ca` 挂载）；`enrollment_token_file` 与 `runtime_pull_preflight.*` 不出现。

### 2.3 不部署 / 不构建的面

- `cmd/api` 无集群 Deployment：`deploy/kustomize/services/` 里没有 api，dev.sh 的镜像构建清单也不含 `Dockerfile.api`（详见 `deploy/README.md` 的核查记录）。api 仅本地进程。
- `web`（nginx，端口 8087）无 YAML 配置：运行时行为在镜像里的 `web/nginx.conf`（同源反代 `auth:8085`/`orchestrator:8083`/`webhook:8082`/`notifier:8086`），构建期开关是 Vite 的 `VITE_*` 环境变量（§4.2），改它要重建 web 镜像。

## 3. 逐键说明

约定：**默认值**列指代码缺省（`WithDefaults` / `DefaultXxx`）；「必填」指缺失/非法即启动失败（错误路径见 §6.3）。「安全」列标注机密性。dev 与 prod 差异指 `configs/`（本地 SQLite）与 kustomize overlay（集群 postgres）两处实测差异。

### 3.1 release-webhook（`configs/webhook.dev.yaml`，与 kustomize 版相同）

| 键 | 类型/取值 | 默认值 | 含义 | 备注（安全/必填/dev 与 prod 差异） |
|---|---|---|---|---|
| `http_port` | int | 无（缺失=0，启动即绑 :0 失败） | Connect 监听端口 | dev 8082；集群 NodePort 30082。必填 |
| `log_level` | string | — | 意图是日志级别 | **未在代码中找到对应读取**（§7-1） |

上游地址不在配置文件：`--orchestrator-url`（集群 arg `http://orchestrator:8083`）与服务令牌走 `DEV_WEBHOOK_SERVICE_TOKEN` 环境变量。

### 3.2 release-auth（本地 6 键 + overlay 新增 3 键）

| 键 | 类型/取值 | 默认值 | 含义 | 备注 |
|---|---|---|---|---|
| `http_port` | int | 无 | 监听端口 | dev 8085。必填 |
| `log_level` | string | — | 同上 | 无读取（§7-1） |
| `database.driver` | `sqlite`\|`postgres` | 无 | 存储引擎 | `Validate()` 白名单，非法即退出（`cmd/auth/main.go:90`）。必填。本地 sqlite、集群 postgres |
| `database.dsn` | string | 无 | sqlite 文件路径或 postgres URL | sqlite 需非空；postgres 必须 `postgres://`/`postgresql://` 前缀（`internal/postgres/config.go:12`）。DSN 含口令=机密；集群值 `...@postgres:5432/release_manager` |
| `authorization.policy_reload_interval` | duration | 5s（`AuthorizationCfg.WithDefaults`） | Casbin 策略热重载周期 | env `AUTHORIZATION_POLICY_RELOAD_INTERVAL` |
| `maintenance` | bool | false | 维护模式：auth 只放通只读过程 | 拦截器 `cmd/auth/main.go:185`；env `MAINTENANCE` |
| `redis.address` | string | ""（不启用） | 会话 Redis 地址 | 仅 overlay 有（`redis:6379`）。非空则启动即 Ping，失败退出（`cmd/auth/main.go:128`）；空=会话走 SQLite |
| `login_rate_limit.max_attempts` | int | 5 | 登录限速窗口内最大尝试 | 仅 overlay（1000，注释说明 dev fixture 高频重登）；`>0` 才覆盖默认（`cmd/auth/main.go:146-153`） |
| `login_rate_limit.window` | duration | 1m | 登录限速窗口 | 仅 overlay（1m） |

代码支持但任何配置文件都未出现的同族键：`redis.password`、`redis.db`（env `REDIS_PASSWORD`/`REDIS_DB`）、`database.max_open_conns`/`max_idle_conns`/`conn_max_lifetime`/`conn_max_idle_time`（池默认 25/10，`internal/postgres/config.go:25`）。不计入键数自检。

### 3.3 release-notifier（4 键，两处仅 driver/dsn 不同）

| 键 | 类型/取值 | 默认值 | 含义 | 备注 |
|---|---|---|---|---|
| `http_port` | int | 无 | 监听端口 | dev 8086。必填 |
| `log_level` | — | — | | 无读取（§7-1） |
| `database.driver` | `sqlite`\|`postgres` | 无 | | 必填；`cmd/notifier/main.go:101` 校验 |
| `database.dsn` | string | 无 | | 集群指独立库 `release_notifier`；postgres 路径启动跑 `migrations.ReleaseNotifierFS()`，迁移失败即退出 |

### 3.4 release-operator：本地 `configs/operator.dev.yaml`（16 键）

| 键 | 类型/取值 | 默认值 | 含义 | 备注 |
|---|---|---|---|---|
| `http_port` | int | 无 | 监听端口 | 8084。必填 |
| `log_level` | — | — | | 无读取（§7-1） |
| `runtime_pull_preflight.enabled` | bool | — | 运行时拉取预检开关 | 整块 **无运行时读取路径**（§7-2），6 键同此 |
| `runtime_pull_preflight.namespace` | string | — | 预检 Pod 命名空间 | 同上 |
| `runtime_pull_preflight.service_account` | string | — | 预检 Pod SA | 同上 |
| `runtime_pull_preflight.timeout` | duration | — | 预检超时 | 同上 |
| `runtime_pull_preflight.cleanup_policy` | string | — | 预检清理策略 | 同上 |
| `runtime_pull_preflight.probe_command` | []string | — | 探针命令 | 同上 |
| `agent.mode` | `agent`\|`gateway` | `agent`（`AgentCfg.WithDefaults`，`internal/config/config.go:176`） | 运行模式 | `gateway` 为管理面遗留模式（`cmd/operator/main.go:38`）；env 无绑定 |
| `agent.customer_id` | string/UUID | 无 | 客户身份 | **agent 模式必填**：与 cluster_id 任一为空则 Register 失败（`cmd/operator/main.go:154`）；env `CUSTOMER_ID` |
| `agent.cluster_id` | string | 无 | 集群身份 | 同上；env `CLUSTER_ID` |
| `agent.operator_name` | string | 无 | 操作器显示名 | env `OPERATOR_NAME`；空则 bootstrap 用 cluster_id 兜底 |
| `agent.enrollment_token_file` | path | 无 | 一次性注册令牌文件 | dev 用 `data/enrollment.token`；bootstrap 成功后删除该文件（`internal/operator/bootstrap/bootstrap.go:96-99`）；env `ENROLLMENT_TOKEN_FILE`；机密 |
| `ca.cert_path` | path | 无 | agent 的网关 CA 信任锚 | 机密性低（公钥证书）；env `CA_CERT_PATH` |
| `ca.cert_ttl` | duration | 168h（`CAConfig.WithDefaults`，`internal/config/config.go:195`） | CA 证书有效期 | **operator 进程无消费**——CAConfig 只有 orchestrator 经 `ca.LoadConfigured` 使用（§3.5）；此处为共享结构体字段残留 |
| `ca.renew_before_ratio` | float (0,1] | 0.5 | 提前续期比例 | 同上，operator 侧无消费 |

### 3.5 release-orchestrator（本地 30 键；含 overlay 差异，共 35 个不同键路径）

| 键 | 类型/取值 | 默认值 | 含义 | 备注 |
|---|---|---|---|---|
| `http_port` | int | 无 | 管理面监听端口 | dev 8083（集群 NodePort 30083）。必填 |
| `log_level` | — | — | | 无读取（§7-1） |
| `database.driver` | `sqlite`\|`postgres` | 无 | | 必填；`cmd/orchestrator/main.go:525` 校验；postgres 路径跑 `migrations.FS` |
| `database.dsn` | string | 无 | | 机密（含口令）；本地 `data/orchestrator.db`，集群 `postgres://...@postgres:5432/release_manager` |
| `authorization.auth_url` | URL | `http://localhost:8085`（`internal/config/config.go:251`） | 授权快照拉取源（release-auth） | 集群 `http://auth:8085`；env `AUTHORIZATION_AUTH_URL` |
| `authorization.pull_interval` | duration | 1s | 授权快照轮询周期 | env `AUTHORIZATION_PULL_INTERVAL` |
| `authorization.pull_backoff_max` | duration | 30s | 拉取失败退避上限 | env `AUTHORIZATION_PULL_BACKOFF_MAX` |
| `values.max_document_bytes` | int | 1 MiB（`ValuesConfig.WithDefaults`，`internal/config/config.go:82`） | ValuesRevision 文档大小上限 | 必填性无——缺省安全；env `VALUES_MAX_DOCUMENT_BYTES` |
| `values.secret_patterns` | []string | [] | Values 明文机密拦截模式 | 消费点 `internal/orchestrator/values_revision.go:154`；env `VALUES_SECRET_PATTERNS`（逗号分隔） |
| `trust.verification_timeout` | duration | 5s（`trust.DefaultVerificationTimeout`，`internal/trust/ed25519.go:22`） | 信任策略解析/根查找超时 | 仅文件（裸 viper `UnmarshalKey("trust")`，`cmd/orchestrator/main.go:677`），无 env 绑定 |
| `maintenance` | bool | false | 维护模式：拦截写 RPC、跳过启动恢复与策略热载 | `cmd/orchestrator/main.go:449/467/516` 三类拦截器；env `MAINTENANCE` |
| `gc.interval` | duration | 6h（`DefaultGcConfig`，`internal/orchestrator/gc_config.go:28`） | 周期 GC 间隔 | 0=停用周期 GC；非法（0<x<5m）启动失败；仅文件，无 env 绑定 |
| `gc.bundle_retention_days` | int | 90 | Bundle 保留天数 | `Validate()` 最小 7（`internal/orchestrator/gc_config.go:41`），低于即启动失败 |
| `gc.archive_grace_days` | int | 30 | 已发布包归档宽限 | min 1 |
| `gc.candidate_artifact_retention_days` | int | 30 | 候选 artifact 保留 | min 1 |
| `gc.preflight_retention_days` | int | 90 | 预检结果保留 | min 1 |
| `gc.orphan_preflight_retention_days` | int | 7 | 孤儿预检保留 | min 1 |
| `gc.gc_max_duration_minutes` | int | 55 | 单轮 GC 时长预算 | min 5 |
| `gc.cleanup_idempotency_retention_hours` | int | 24 | cleanup 幂等记录保留 | min 1 |
| `gateway.enabled` | bool | false | 是否启 agent mTLS 网关（:8084） | dev 本地 false、集群 overlay true（agent 接入必需）；env `GATEWAY_ENABLED` |
| `gateway.port` | int | 8084（`GatewayCfg.WithDefaults`，`internal/config/config.go:235`） | 网关端口 | 集群 NodePort 30084；env `GATEWAY_PORT` |
| `gateway.ca_key_path` | path | — | 遗留字段 | **死键，零消费者**（§7-4）；env `GATEWAY_CA_KEY_PATH` 绑定存在但无人读 |
| `gateway.ca_cert_path` | path | — | 遗留字段 | 同上（§7-4）；overlay 已删并被测试锁定 |
| `ca.key_path` | path | 无 | CA 私钥文件（dev 文件模式） | **机密**；与 `ca.cert_path` 成对，`ca.LoadConfigured` 缺失即 Register 失败（`cmd/orchestrator/main.go:87`、`CAConfig.Validate` `internal/config/config.go:204-216`）；集群由 Secret `release-manager-mtls-ca` 挂到 `/data/gateway-ca.key`（0600） |
| `ca.cert_path` | path | 无 | CA 证书（网关信任锚） | 与 key_path 成对；网关启用时两者都必须可得 |
| `ca.cert_ttl` | duration | 168h | 签发操作器证书有效期 | 消费方仅 orchestrator（`LoadConfigured`） |
| `ca.renew_before_ratio` | float (0,1] | 0.5 | 到期前续租比例 | 越界报 `ca_invalid` 启动失败 |
| `emergency.enabled` | bool | 缺块=fail-closed false | 紧急变更 kill switch | dev 本地与集群都 true（REQ-081 D2=A）；启动种入 app_settings（`cmd/orchestrator/main.go:305-309`）；仅文件 |
| `emergency.operation_timeout` | duration | 30s（`store.DefaultEmergencyOperationTimeout`，`internal/store/store.go:1394`） | 非终态 EMERGENCY 操作时限 | 解析失败回落默认；仅文件 |
| `emergency.effect_observe_timeout` | duration | 24h（`internal/store/store.go:1398`） | 卡锁观察窗 | 仅文件 |
| `retention.bundle_days` | int | — | 历史遗留键 | **无读取**（§7-3），5 键同此 |
| `retention.candidate_artifact_days` | int | — | 同上 | 同上 |
| `retention.preflight_result_hours` | int | — | 同上 | 同上 |
| `retention.prepare_session_hours` | int | — | 同上 | 同上 |
| `retention.gc_interval_hours` | int | — | 同上 | 同上 |

代码支持但未在任何文件出现的键：`ca.vault_path`（生产 CA 源，Vault KV，客户端走 `VAULT_ADDR` 环境，`internal/operator/ca/config.go:19`、`vault.go:38`）。

### 3.6 客户集群 agent overlay（`deploy/kustomize/customer-agent/*/configs/operator.dev.yaml`，每文件 8 键）

| 键 | 类型/取值 | dev 值 | 含义 | 备注 |
|---|---|---|---|---|
| `http_port` | int | 8084 | agent 本地监听 | |
| `log_level` | — | debug | | 无读取（§7-1） |
| `agent.mode` | `agent` | agent | | |
| `agent.customer_id` | UUID | c1/c2=dev-customer-a、c3/c4=dev-customer-b 的固定 UUID | 注册身份 | 必填（§3.4）；seed 清单派生，勿手改 |
| `agent.cluster_id` | string | dev-customer-a-direct / -a-cache / -b-replicated / -b-mixed | 集群身份 | 必填 |
| `agent.operator_name` | string | = cluster_id | 操作器名 | |
| `ca.cert_path` | path | /data/gateway-ca.crt | 网关 CA 信任锚 | Secret `operator-gateway-ca` 挂载 |
| `registry_plain_http` | bool | true（顶层） | 意图允许 HTTP OCI 拉取 | **放置位置与读取路径不符**（§7-5）：代码读的是 `agent.registry_plain_http` |

令牌走环境变量 `ENROLLMENT_TOKEN`（Secret `operator-enrollment`，`agents_up` 命令式创建，文件名固定不参与 hash）；`enrollment_token_file` 键在这四个 overlay 中不存在。

### 3.7 release-api（`configs/api.dev.yaml`，8 键）

| 键 | 类型/取值 | 默认值 | 含义 | 备注 |
|---|---|---|---|---|
| `http_port` | int | 无 | 监听端口 | dev 8087（仅本地；集群 8087 是 web/nginx）。必填 |
| `log_level` | — | — | | 无读取（§7-1） |
| `audit.archive.retention_days` | int | 90（`audit.DefaultArchiveConfig`，`internal/audit/archive_config.go:20`；**注意**：文件能读到但键缺失时按零值拷贝即 0=停用归档，见 `archiveConfigFromService` `cmd/api/main.go:96`） | 保留天数，0 关闭归档 | 读取仅发生在 `cmd/api.Register` 的二次 `LoadService`（`cmd/api/main.go:60`），失败只 Warn 回落默认 |
| `audit.archive.poll_interval` | duration | 6h | 归档轮询周期 | `Validate()` 要求 >0（`internal/audit/archive_config.go:32`） |
| `audit.archive.batch_size` | int | 1000 | 单批归档条数 | 要求 >0 |
| `audit.archive.archive_dir` | path | data/archives | 归档输出目录 | 要求非空 |
| `audit.archive.compression` | `gzip_jsonl` | gzip_jsonl | 归档编码 | 仅支持此值，其它值 Validate 报错 |
| `audit.archive.checksum_algorithm` | `sha256` | sha256 | 校验算法 | 仅支持此值 |

### 3.8 release-notification-sink（kustomize 唯一副本，2 键）

| 键 | 类型/取值 | 默认值 | 含义 | 备注 |
|---|---|---|---|---|
| `http_port` | int | 无 | dev 通知接收桩端口 | 8088，仅 ClusterIP（无 NodePort）。必填 |
| `log_level` | — | — | | 无读取（§7-1） |

### 3.9 E2E 模板（`configs/e2e.dev.yaml`，27 键）——非第二配置源

文件头注释即声明：运行时 env-config 由 Makefile 私有目标 `e2e-env-config` 从 dev-status.json / dev-fixture.json / dev-credentials.env / kubeconfig 汇编到 `data/e2e-env-config.yaml`（0600）。本模板只为在无活环境时验证 `cmd/e2e` 的严格 schema（`test/e2e/config.go:225-233`，`KnownFields(true)`——**未知键直接报错**）。下表值列给出模板占位/示例；schema 校验列给出 `test/e2e/config.go` 的 Validate 规则。

| 键 | 类型/取值 | 模板值 | 含义 | 备注（安全/schema 校验） |
|---|---|---|---|---|
| `environment` | string | development | 环境标签 | 必填非空 |
| `environment_id` | string | dev-local-e2e-template | 环境实例 id | 必填非空 |
| `endpoints.release_orchestrator` | URL | http://localhost:8083 | orchestrator 地址（集群经 dev.sh 端口转发到 localhost:8083） | 必填非空（URL 字符串） |
| `endpoints.release_webhook` | URL | http://localhost:8082 | webhook 地址 | 必填非空 |
| `endpoints.release_operator` | URL | http://localhost:8084 | operator 地址 | 必填非空 |
| `endpoints.release_auth` | URL | http://localhost:8085 | auth 地址 | 必填非空 |
| `endpoints.release_notifier` | URL | http://localhost:8086 | notifier 地址 | 必填非空 |
| `endpoints.release_api` | URL | http://localhost:8087 | api 地址 | 必填非空 |
| `credentials.e2e_runner.username` | string | e2e-runner | E2E 账号 | 必填 |
| `credentials.e2e_runner.password_env` | string | E2E_RUNNER_PASSWORD | **口令只以环境变量名进入配置**，值经 `os.LookupEnv` 解析（`test/e2e/config.go:309-316`）；YAML 中永不含口令 | 机密间接引用；须是合法 env 名且运行时已设置 |
| `k3d.kubeconfig` | path | data/kubeconfig.yaml | 合并 kubeconfig | 必填 |
| `k3d.context` | string | k3d-release-manager-control | 管理集群 context | 必填（合并 context 的 current-context 可能是客户集群，必须显式） |
| `k3d.test_namespace` | string | release-manager-dev | 测试命名空间 | 必填 |
| `k3d.restart_targets.namespace` | string | release-manager-dev | 重启目标 ns | 必须等于 `k3d.test_namespace`（`test/e2e/config.go:355`） |
| `k3d.restart_targets.deployments` | []string | auth, orchestrator, webhook | 重启目标 | **恰好 3 个**且过 DNS 名校验（`test/e2e/config.go:358`）；不得与 RBAC resourceNames 白名单漂移 |
| `seed.customers` | []string | dev-customer-a, dev-customer-b | 期望客户清单 | 非空 |
| `seed.clusters_per_customer` | int | 2 | 每客户集群数 | |
| `seed.fixture_version` | string | vN 占位 | 种子清单版本 | 与 devseed 常量对齐（AC-066-29） |
| `seed.expected_identity.customers` | int | 2 | seed 身份基数 | 由汇编从 seed manifest 推导，模板仅示例形状 |
| `seed.expected_identity.clusters` | int | 4 | 同上 | |
| `seed.expected_identity.routes_basic` | int | 8 | 同上 | |
| `seed.expected_identity.definitions_basic` | int | 0 | 同上 | |
| `seed.expected_identity.bundles` | int | 1 | 同上 | |
| `seed.expected_identity.e2e_definition_ids` | []string | 4 个占位 | E2E 定义 id 集 | 必须与 `seed.e2e_upgrade_targets` 的 id 加 `seed.e2e_emergency_definition_id` 完全一致（模板注释；schema 拒绝两边不一致） |
| `seed.e2e_upgrade_targets` | list（每项 logical_key/definition_id/bundle_id/values_revision_id） | 3 项占位 | 逻辑键→服务端 id 绑定 | 子字段为列表元素内部键，计数时列表整体算 1 个键 |
| `seed.e2e_emergency_definition_id` | string | 占位 | 紧急变更目标定义 id | |
| `seed.e2e_operator_id` | string | 占位 | 观测的操作器 id | 注册时服务端铸造，不可从逻辑键推导 |

运行时配置另含 `k3d.cluster_contexts`（map，`test/e2e/config.go:107`）——schema 有此键但模板文件未写，不计入 84 键。

## 4. 环境变量

### 4.1 viper 绑定（所有走 `LoadService` 的服务可覆盖；env > 文件）

`internal/config/config.go:273-298` 的完整映射（点号键 → env 名）：`database.driver`→`DATABASE_DRIVER`、`database.dsn`→`DATABASE_DSN`（机密）、`database.max_open_conns`→`DATABASE_MAX_OPEN_CONNS`、`database.max_idle_conns`→`DATABASE_MAX_IDLE_CONNS`、`database.conn_max_lifetime`→`DATABASE_CONN_MAX_LIFETIME`、`redis.address`→`REDIS_ADDRESS`、`redis.password`→`REDIS_PASSWORD`（机密）、`redis.db`→`REDIS_DB`、`maintenance`→`MAINTENANCE`、`authorization.auth_url`→`AUTHORIZATION_AUTH_URL`、`authorization.pull_interval`→`AUTHORIZATION_PULL_INTERVAL`、`authorization.pull_backoff_max`→`AUTHORIZATION_PULL_BACKOFF_MAX`、`authorization.policy_reload_interval`→`AUTHORIZATION_POLICY_RELOAD_INTERVAL`、`gateway.enabled`→`GATEWAY_ENABLED`、`gateway.port`→`GATEWAY_PORT`、`gateway.ca_key_path`→`GATEWAY_CA_KEY_PATH`（死键，§7-4）、`gateway.ca_cert_path`→`GATEWAY_CA_CERT_PATH`（死键）、`agent.customer_id`→`CUSTOMER_ID`、`agent.cluster_id`→`CLUSTER_ID`、`agent.operator_name`→`OPERATOR_NAME`、`agent.enrollment_token_file`→`ENROLLMENT_TOKEN_FILE`（机密）、`ca.cert_path`→`CA_CERT_PATH`、`values.max_document_bytes`→`VALUES_MAX_DOCUMENT_BYTES`、`values.secret_patterns`→`VALUES_SECRET_PATTERNS`（逗号分隔）。

### 4.2 Go 代码直接读取（`os.Getenv`/`LookupEnv`）

| 变量 | 读取点 | 用途 | 机密 |
|---|---|---|---|
| `JWT_SIGNING_KEY` | `cmd/auth/main.go:237`、`cmd/orchestrator/main.go:833` | flag 默认值 | 是 |
| `DEV_WEBHOOK_SERVICE_TOKEN` | `cmd/webhook/main.go:62` | flag 默认值 | 是 |
| （orchestrator 侧）`DEV_WEBHOOK_SERVICE_TOKEN` + `DEV_WEBHOOK_SERVICE_TOKEN_PREVIOUS` | `cmd/orchestrator` serviceTokens | 校验入站服务令牌（双令牌=零停机轮换） | 是 |
| `ENROLLMENT_TOKEN` | `internal/operator/bootstrap/token.go:24-27`（`TokenEnv`，`cmd/operator/main.go:161`） | 一次性注册令牌（文件缺位时） | 是 |
| `E2E_RUNNER_PASSWORD` | `cmd/e2e` 经 `credentials.e2e_runner.password_env` 间接；Makefile/devseed 直接 | e2e-runner 口令 | 是 |
| `E2E_RUN_ID` | `cmd/e2e/main.go:748`（DNS-1123 校验）；`internal/app/app.go` environmentHandler | E2E 运行标识 | 否 |
| `E2E_LOCK_FILE` | `cmd/e2e/main.go:730` | 可选锁文件 | 否 |
| `APP_ENVIRONMENT` / `ENVIRONMENT_ID` / `DEV_PROFILE` / `APP_PRODUCTION` | `internal/app/app.go:91-120`（GET /environment） | 环境自述端点 | 否；注意 kustomize 未给业务 Pod 注入这些（§7-7） |
| `DEV_ADMIN_USER` / `DEV_DEPLOYER_USER`（及三个 `*_PASSWORD`、`DEV_TRUST_ROOT_PRIVATE_KEY`、`RELEASE_MANAGER_DATABASE_DSN`） | `cmd/devseed/main.go:50-58` | flag 默认值 | 口令与 trust root 机密 |
| `RELEASE_MANAGER_DATABASE_DSN` | `cmd/store-migrate/main.go:18/37` | postgres 目标 DSN | 是 |
| `VAULT_ADDR`（及 Vault token） | `internal/operator/ca/vault.go:38` | 生产 CA 源 | 间接机密 |
| `GOFLAGS` | `cmd/sdkcheck/main.go:27-33` 写回自身环境 | build tags 传递 | 否 |

### 4.3 `deploy/dev/` 生命周期脚本读取（dev.sh + lib/*.sh）

| 变量 | 默认 | 用途 | 机密 |
|---|---|---|---|
| `DEV_DATA_DIR` | `data/` | 状态/密钥/诊断根目录（`deploy/dev/dev.sh:33`、lib/lock.sh） | 否 |
| `DEV_PROFILE` | `local`（另一取值 `ci`） | 机密来源模式：local=生成进 `data/`，ci=env 注入瞬态物化（`deploy/dev/lib/ownership.sh`、host.sh、Makefile、devseed） | 否（模式开关） |
| `DEV_LOCK_FILE` / `DEV_STAGE_FILE` | `data/dev.lock` / `data/dev-stage.json`（lib/lock.sh:18-19） | 环境 flock 与阶段记录 | 否 |
| `OWNERSHIP_FILE` | `data/dev-ownership.json`（lib/ownership.sh:19） | 资源归属清单 | 否 |
| `DEV_TIMEOUT_READY` | 300s | Pod ready 等待 | 否 |
| `DEV_TIMEOUT_OPERATOR` | 180s | seed enrollment 后 operator 上线等待（经 devseed `--operator-timeout` 传入） | 否 |
| `DEV_TIMEOUT_SEED_RETRIES` | 3 | seed 阶段写重试（devseed `--seed-retries`） | 否 |
| `DEV_SEED_RETRY_DELAY` | 5s | 重试间隔 | 否 |
| `DEV_PORTS_OVERRIDE` | （unset） | 测试 seam：覆盖端口探测表（lib/host.sh:16-22 的 `DEV_PORTS=(8082..8087)`） | 否 |
| `DEV_K3D_NODE_MEMORY` / `DEV_K3D_NODE_CPU` | 内置 | k3d 节点资源 | 否 |
| `DEV_BUILD_PARALLELISM` | 1/2/4 | 镜像并行构建度 | 否 |
| `DEV_DOCKER_MIRROR` | 空 | 基础镜像 registry 前缀（`deploy/docker/Dockerfile.web` 的 NODE_IMAGE/NGINX_IMAGE build-args） | 否 |
| `DEV_OPERATOR_GATEWAY_PORT` | 30084 | 网关 TCP 探测端口（测试 seam） | 否 |
| `DEV_RESET_PG_PORT` | 5432 | reset-data port-forward 本地端口 | 否 |
| `DEV_JWT_SIGNING_KEY` | （unset） | **ci profile 必注入**的 JWT 密钥，瞬态写 `data/dev-jwt/jwt-signing-key.pem` 后删除 | 是 |
| `DEV_WEBHOOK_SERVICE_TOKEN` | （unset） | ci profile 令牌（同上瞬态物化） | 是 |
| `DEV_M_TLS_CA_KEY` / `DEV_M_TLS_CA_CERT` | （unset） | ci profile dev CA 对（瞬态物化 `data/dev-ca/`） | 是 |
| `E2E_RUN_ID` | （unset） | ci profile 必需，DNS-1123（lib/host.sh:128-137），environment_id=`ci-<run_id>` | 否 |
| `FIXTURE_VERSION` | devseed `-print-fixture-version` 输出（兜底 v2） | seed 清单版本解析（AC-065-30） | 否 |
| `CONFIRM` | （unset） | `require_confirm` 门：`reset-data`/`purge` 必须 `CONFIRM=1`（lib/errors.sh） | 否（破坏性闸门） |
| `HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY`/`GOPROXY` | （unset） | 构建代理透传 | 否 |

### 4.4 Makefile 与 CI

- E2E 段 Makefile 变量（可 env 覆盖）：`E2E_DATA_DIR`、`E2E_ENV_CONFIG`（=`data/e2e-env-config.yaml`）、`E2E_LOCK_FILE`（=`data/dev.lock`）、`E2E_CREDENTIALS_FILE`（=`data/dev-credentials.env`，recipe 里 `source` 后要求 `E2E_RUNNER_PASSWORD` 非空）、`E2E_TEST_NAMESPACE`、`E2E_RESTART_DEPLOYMENTS`、`E2E_ENVIRONMENT`、`OUTPUT_DIR`、`STAGES`、`TIMEOUT`、`TOTAL_TIMEOUT`、`PARALLEL`、`KEEP_ON_FAILURE`、`SNAPSHOT_FULL`、`BASELINE_FILE`、`ENV_CONFIG`。`e2e-env-config` recipe 内的 `E2E_ENV_CONFIG_PATH`/`E2E_OUTPUT_DIR`/`E2E_STAGES`/`E2E_TIMEOUT`/`E2E_TOTAL_TIMEOUT`/`E2E_PARALLEL`/`E2E_KEEP_ON_FAILURE`/`E2E_SNAPSHOT_FULL` 是跨 flock 子 shell 的瞬态传参。`E2E_KUBECONFIG` 被 export 但**未找到消费者**（§7-8）。`e2e-all` 认 `E2E_SKIP_PREFLIGHT_CLEANUP=1` 跳过预清理。
- `.github/workflows/test.yml` 把 CI Secrets 映射为 env：`E2E_RUNNER_PASSWORD`、`DEV_ADMIN_PASSWORD`、`DEV_DEPLOYER_PASSWORD`、`DEV_READER_PASSWORD`、`DEV_JWT_SIGNING_KEY`、`DEV_WEBHOOK_SERVICE_TOKEN`、`DEV_M_TLS_CA_KEY`、`DEV_M_TLS_CA_CERT`、`DEV_TRUST_ROOT_PRIVATE_KEY`、`E2E_RUN_ID`、`E2E_ENVIRONMENT`、`DEV_PROFILE=ci`。CI Secret 名录见 `.github/SECRETS.md`。
- kustomize 注入到 Pod 的 env：`JWT_SIGNING_KEY`（auth/orchestrator，Secret `release-manager-jwt`）、`DEV_WEBHOOK_SERVICE_TOKEN`（webhook/orchestrator）、`DEV_WEBHOOK_SERVICE_TOKEN_PREVIOUS`（orchestrator，可选）、`ENROLLMENT_TOKEN`（customer agent，Secret `operator-enrollment`）、postgres 容器经 `envFrom` 得 `POSTGRES_USER`/`POSTGRES_PASSWORD`/`POSTGRES_DB`/`DATABASE_DRIVER`（Secret `release-manager-dev-credentials`；其中 `DATABASE_DRIVER` 对 postgres 镜像无意义、业务服务也不 envFrom 它，§5）加静态 `POSTGRES_INITDB_ARGS`。
- 集成测试专用：`POSTGRES_TEST_DSN`（`//go:build integration` 用例的 DSN 门，AGENTS.md 质量门禁；未设即 skip）。
- 前端构建期：`VITE_ENABLE_RELEASE_INVENTORY`、`VITE_ENABLE_VALUES_REVISION`、`VITE_ENABLE_RELEASE_OPERATIONS`、`VITE_FEATURE_CLUSTER_ROUTING`、`VITE_FEATURE_OPERATOR_MANAGEMENT`、`VITE_ARTIFACT_CACHE_ENDPOINT`（`web/src/` 内 `import.meta.env`；未设即默认值，仅影响 web 镜像）。

## 5. Secret 约定

**机密清单（实测）**：JWT 签名密钥；webhook 服务令牌（含 PREVIOUS）；enrollment 一次性令牌；dev mTLS CA 私钥（网关侧 `ca.key`）与集群侧挂载的 `ca.crt`（公钥件，低风险）；各账号口令（dev-admin/dev-deployer/dev-reader/e2e-runner）；数据库 DSN 内嵌口令；E2E trust-root 私钥。

**「Secret 只以引用进入执行链」在配置层的实现**（`AGENTS.md` 约束 5）：

1. **文件不落库**：所有运行时生成的机密都在 `data/`（`.gitignore` 的 `data/`、`certs/`、`.env`、`e2e-results/` 条目），仓库只提交生成物路径的**约定**。
2. **kustomize `secretGenerator` 走内容 hash**（`deploy/kustomize/dev/kustomization.yaml:39-70`）：`JWT_SIGNING_KEY` ← `data/dev-jwt/jwt-signing-key.pem`；`WEBHOOK_SERVICE_TOKEN` ← `data/dev-service-tokens/webhook-service-token`；`ca.key`/`ca.crt` ← `data/dev-ca/`。名字带 hash ⇒ 轮换密钥/令牌即滚动消费方 Deployment，无需手工 restart。文件路径引用（`../../../data/...`）也是 dev kustomize 需要 `--load-restrictor LoadRestrictionsNone` 的原因（dev.sh apply 处）。
3. **Pod 侧只以 env/挂载引用出现**：orchestrator 的 CA 不写在 YAML 值里，而是 Secret 以 subPath 挂到 `ca.key_path=/data/gateway-ca.key`（0600）与 `ca.cert_path=/data/gateway-ca.crt`（0644），配置只引用挂载点；agent 令牌经 `ENROLLMENT_TOKEN` 注入；一次性令牌消费即删（`internal/operator/bootstrap/bootstrap.go:96-99`）。
4. **E2E 口令零落盘**：`credentials.e2e_runner.password_env` 只存**环境变量名**，值 `os.LookupEnv` 现取（§3.9）；`data/e2e-env-config.yaml` 0600。
5. **prod 路径**：CA 支持 `ca.vault_path`（Vault KV）替代文件对（ADR-017）；Values 明文机密由 `values.secret_patterns` + SecretRef 校验拦截（§3.5）。

**如实标注的明文例外（dev-only，均实测）**：
- `deploy/kustomize/base/secret.yaml` 把 `POSTGRES_PASSWORD: dev-release-manager` 等明文提交进仓库——文件自述为 dev-only 共享凭据；它只喂 postgres 容器（`deploy/kustomize/postgres/deployment.yaml:26` envFrom），且其中 `DATABASE_DRIVER` 键只被 postgres 容器忽略、服务侧无 envFrom（viper 可经同名 env 覆盖，但无人注入）。
- kustomize overlay 的 auth/orchestrator/notifier DSN 内嵌 `dev-release-manager` 口令（§2.2）。
- `deploy/dev/dev.sh` reset-data 段硬编码 `PGPASSWORD=dev-release-manager`。
- `configs/api.dev.yaml` 侧 `--signing-key` 默认 `change-me-in-production`（api/orchestrator/auth 同款哨兵值）。这些在引入真实生产部署前都应被 Secret 后端替代。

## 6. 如何正确地改配置

### 6.1 改哪里

| 目标 | 编辑对象 |
|---|---|
| 本地进程（`make run-webhook`/`run-auth`/…、`dev-stage-*`、`make run-api`） | `configs/<svc>.dev.yaml` |
| dev 集群管理面（webhook/orchestrator/auth/notifier/notification-sink） | `deploy/kustomize/dev/configs/<svc>.dev.yaml` |
| dev 集群客户 agent（c1–c4） | `deploy/kustomize/customer-agent/<cX>/configs/operator.dev.yaml` |
| 轮换 JWT 密钥 / webhook 令牌 / dev CA | 换 `data/dev-jwt/`、`data/dev-service-tokens/`、`data/dev-ca/` 下的源文件（本地 profile 直接改文件；ci profile 改注入 env），**不要**改任何提交的 YAML |
| 端口、k8s 资源、挂载 | `deploy/kustomize/services/*.yaml`、`deploy/kustomize/customer-agent/base/deployment.yaml` |
| E2E 形状 | 改 `cmd/e2e` flag / Makefile 变量；`configs/e2e.dev.yaml` 只随 schema 演进同步（它不是运行时源） |

注意 `configs/` 与 `deploy/kustomize/dev/configs/` 是**两套语义同源、值不同**的配置（§2.1），改一边不会传导到另一边；跨环境一致的键（如 `values.max_document_bytes`）要同步两处。

### 6.2 如何生效

- 本地：改文件后重启该进程（Ctrl-C 再 `make run-*`）。无热加载（`WatchConfigFile` 无调用方，§1.1）。
- 集群：`make dev-up`（幂等重跑：kustomize build + apply + `kubectl rollout status`）即生效。`configMapGenerator` 未禁用 hash（`deploy/kustomize/dev/kustomization.yaml:11-31`），改任一 configs YAML 会换掉 ConfigMap 的内容 hash 名、同步改写 Deployment 的 volume 引用——重新 apply 后**自动滚动**；只改集群里活 ConfigMap 不会触发滚动（需 `kubectl rollout restart deployment/<svc>`）。改 secretGenerator 的**源文件**（`data/dev-jwt/` 等）同理自动滚动（§5-2）。手工等价命令：`kustomize build --load-restrictor LoadRestrictionsNone deploy/kustomize/dev | kubectl apply -f -`（另对 agent：`kustomize build deploy/kustomize/customer-agent/<cX>`，hostAliases 占位 IP 由 `agents_up` sed 注入）。
- 环境级重置：`make dev-reset-data` / `make dev-purge` 需 `CONFIRM=1`。

### 6.3 写错会直接起不来的键（实测错误路径）

| 错误 | 表现 |
|---|---|
| `--config` 路径不存在 / YAML 语法错 | `LoadService` 报 `reading config` → `internal/app/app.go:125` 记 fatal `failed to load config` 退出 |
| `database.driver` 缺失或不在 `sqlite`/`postgres` 白名单；`database.dsn` 空/前缀不对 | auth/orchestrator/notifier `openStore` 里 `DatabaseConfig.Validate()`（`cmd/auth/main.go:90`、`cmd/orchestrator/main.go:525`、`cmd/notifier/main.go:101`）报错退出；postgres 细节校验含 URL scheme（`internal/postgres/config.go:12`） |
| `redis.address` 非空但不可达 | auth 启动即 Ping，失败 `ping redis` 退出（`cmd/auth/main.go:128`） |
| `ca:` 缺 key/cert 对且无 `vault_path`（orchestrator） | `ca.LoadConfigured` fail-closed `ca_invalid` → Register 失败退出（`cmd/orchestrator/main.go:87`）；文件对模式会 LoadOrCreate，目录不可写同样失败 |
| `ca.renew_before_ratio` 越出 (0,1] | `ca_invalid` 启动失败 |
| `agent.mode=agent` 而 `agent.customer_id`/`agent.cluster_id` 任一为空 | `agent mode requires agent.customer_id and agent.cluster_id` 退出（`cmd/operator/main.go:154`） |
| agent 无令牌（文件与 `ENROLLMENT_TOKEN` 皆空）或网关不可达 | `operator bootstrap` 错误退出（`internal/operator/bootstrap/token.go:29`） |
| `gc.*` 低于最小界（如 `interval` 非 0 且 <5m） | `loadRetentionConfig` → `Validate()` 失败，Register 报错退出（`cmd/orchestrator/main.go:457/547`） |
| notifier/auth 的 postgres 迁移失败 | 启动中止（迁移在 openStore 内跑 `migrations` FS） |
| api 的 `audit.archive.*` 非法 | 仅 Warn 回落默认，不阻塞启动（`cmd/api/main.go:60-64` 的二次加载失败也只是 `logger.Warn`） |
| `values.max_document_bytes`、`authorization.*`、`maintenance`、`trust.*`、`emergency.*` 写错 | 多数被 `WithDefaults`/解析回落吞掉（trust/emergency/gc 无 env 覆盖，`Diagnostics` 仅告警），启动不炸——改后要自测 |

## 7. 未核实 / 疑似漂移清单

以下如实报告「写了但查不到读取点」或「值放错位置」的项（均给搜索方法）：

1. **`log_level`（全部 16 个文件都有）**：`ServiceConfig.LogLevel` 有 mapstructure 标签、被解析，但全仓无消费者——`internal/app/app.go:123` 硬编码 `Level: slog.LevelDebug`，`cmd/*/main.go` 无一处读 `cfg.LogLevel`（搜索：`grep -rn "\.LogLevel" --include='*.go'`）。改它不改变任何行为。
2. **`runtime_pull_preflight.*`（`configs/operator.dev.yaml` 6 键）**：对应结构体在 `config.Config`/`RuntimePullConfig`（`internal/config/config.go:66`），但 `config.Load`（`:93`）无任何调用方；`NewRuntimePullExecutor`/`NewRuntimeCommandExecutor` 生产代码无调用方；`ServiceConfig` 无此字段，`LoadService` 解析时静默丢弃。→ **未在代码中找到运行时读取**。
3. **`retention.*`（`deploy/kustomize/dev/configs/orchestrator.dev.yaml` 5 键）**：orchestrator 只 `UnmarshalKey` `gc`/`emergency`/`trust`（`cmd/orchestrator/main.go:555/625/677`）；`LoadService` 丢弃未知键；`ServiceConfig` 无 `retention` 字段，键名（`bundle_days` 等）与 `gc.*` mapstructure 全不匹配。→ 集群 GC 实际跑 `DefaultGcConfig`；该块是 `gc` 改名前的遗留。
4. **`gateway.ca_key_path` / `gateway.ca_cert_path`（`configs/orchestrator.dev.yaml`）**：字段定义于 `GatewayCfg`（`internal/config/config.go:226-231`）但零消费者（搜索：`grep "CAKeyPath\|CACertPath"` 仅命中定义与 env 绑定）；CA 实际走顶层 `ca:` 块（`ca.LoadConfigured`）。dev overlay 已删这两键并由测试 `TestOrchestratorDevConfigWiresTopLevelCA` 锁定不得回流；本地 `configs/` 里的两行是同类遗留。
5. **`registry_plain_http`（4 个 customer-agent overlay，顶层）**：唯一读取点是 `AgentCfg.RegistryPlainHTTP`（路径 `agent.registry_plain_http`，消费 `cmd/operator/main.go:69`）。实测探针（临时测试 `config.LoadService(<overlay>)`）显示该键解码为 false——**顶层放置不生效**。overlay 注释声明的意图（放行 plain-HTTP fixture registry）与当前加载语义不符，疑似漂移；修复前不要把该顶层键当作已生效配置。
6. **`ca.cert_ttl`/`ca.renew_before_ratio` 出现在 `configs/operator.dev.yaml`**：解析进共享 `CAConfig`，但 operator 进程只读 `CA.CertPath`（`cmd/operator/main.go:70`）——TTL/ratio 仅 orchestrator 的 `ca.LoadConfigured` 消费。
7. **`deploy/kustomize/base/configmap.yaml`（`release-manager-config`：`TARGET_ENV`/`REGISTRY_HOST`/`DEV_ENVIRONMENT`/`APP_ENVIRONMENT`）**：没有任何 Deployment 引用该 ConfigMap（搜索：`grep -rn release-manager-config deploy/` 仅命中定义处）；`TARGET_ENV`/`REGISTRY_HOST`/`DEV_ENVIRONMENT` 在 Go 代码里也无读取点。api 的 `--target-env` 是 flag 默认 `staging`，与此 ConfigMap 无关。
8. **`E2E_KUBECONFIG`**：`e2e-env-config` recipe 里 export，但 `grep -rn E2E_KUBECONFIG`（含 cmd/、test/、deploy/）未发现消费者。
9. **api 审计归档 worker 的启动面**：`apiSvc.RunBackground(ctx, *slog.Logger)`（`cmd/api/main.go:72`）存在，`archiveWorker.Run` 在其内调用，但 `internal/app/app.go:42` 的可选接口是 `Run(context.Context)`（签名不匹配）且全仓无 `RunBackground` 调用方——本地跑 `make run-api` 时归档循环当前不会被 app 层拉起；`audit.archive.*` 的读取本身是真的（`cmd/api/main.go:60-64`）。
10. **`POSTGRES_*` 之外的 base Secret 键**：`release-manager-dev-credentials` 里的 `DATABASE_DRIVER` 无任何工作负载读到（服务不 envFrom 它；见 §5 明文例外）。

## 8. 键数自检

**口径**：叶子键=YAML 走到标量/列表即止（列表如 `k3d.restart_targets.deployments` 整体记 1 个键）；「不同键」取点分路径全集；统计范围 `configs/*.yaml` + `deploy/kustomize/**/configs/*.yaml`（16 个文件）。

配置文件侧命令：

```bash
python3 - <<'PY'
import glob, yaml
files = sorted(glob.glob('configs/*.yaml')) + sorted(glob.glob('deploy/kustomize/**/configs/*.yaml', recursive=True))
allpaths = []
for f in files:
    ks = []
    def walk(node, prefix):
        if isinstance(node, dict):
            for k, v in node.items():
                walk(v, f"{prefix}.{k}" if prefix else k)
        else:
            ks.append(prefix)
    walk(yaml.safe_load(open(f)), '')
    print(len(ks), f)
    allpaths += ks
u = sorted(set(allpaths))
print("TOTAL", len(allpaths), "DISTINCT", len(u))
PY
```

实测输出：`TOTAL 165 DISTINCT 84`（逐文件计数与 §2 表一致）。

文档侧命令：本文 §3 各表首列的反引号点分键（表格行首单元格即键名），与上面 84 个不同键路径一一对应：

```bash
sed -n '/^## 3\./,/^## 4\./p' docs/configuration.md \
  | grep -oE '^\| `[a-z0-9_.]+`' | sed -E 's/^\| `//; s/`$//' | LC_ALL=C sort -u | wc -l   # 84
```

**核对结论：文档键行数（不同点分路径）= 84 = 配置文件中出现的不同键数；逐文件叶子出现数合计 165 与 §2 清单表合计一致。**（§3 各表内 `http_port`、`log_level`、`agent.*` 等跨服务重复出现，但按「不同键路径」口径只计一次；`redis.password`、`ca.vault_path`、`k3d.cluster_contexts`、`agent.registry_plain_http` 等「代码支持但无文件出现」的键不计入，均已在对应小节备注。）

> 事实源：
> - `internal/config/config.go`、`internal/app/app.go`、`internal/postgres/config.go`、`internal/postgres/db.go`
> - `cmd/api/main.go`、`cmd/auth/main.go`、`cmd/notifier/main.go`、`cmd/notification-sink/main.go`、`cmd/orchestrator/main.go`、`cmd/operator/main.go`、`cmd/webhook/main.go`、`cmd/devseed/main.go`、`cmd/e2e/main.go`、`cmd/store-migrate/main.go`、`cmd/sdkcheck/main.go`
> - `configs/api.dev.yaml`、`configs/auth.dev.yaml`、`configs/e2e.dev.yaml`、`configs/notifier.dev.yaml`、`configs/operator.dev.yaml`、`configs/orchestrator.dev.yaml`、`configs/webhook.dev.yaml`
> - `deploy/kustomize/dev/kustomization.yaml`、`deploy/kustomize/dev/configs/auth.dev.yaml`、`deploy/kustomize/dev/configs/notification-sink.dev.yaml`、`deploy/kustomize/dev/configs/notifier.dev.yaml`、`deploy/kustomize/dev/configs/orchestrator.dev.yaml`、`deploy/kustomize/dev/configs/webhook.dev.yaml`、`deploy/kustomize/customer-agent/c1-direct/configs/operator.dev.yaml`（及 c2-cache、c3-replicated、c4-mixed 同型文件）、`deploy/kustomize/customer-agent/base/deployment.yaml`、`deploy/kustomize/base/configmap.yaml`、`deploy/kustomize/base/secret.yaml`、`deploy/kustomize/postgres/deployment.yaml`、`deploy/kustomize/services/orchestrator.yaml`（及 auth/notifier/webhook/web/notification-sink 同目录文件）
> - `deploy/dev/dev.sh`、`deploy/dev/lib/errors.sh`、`deploy/dev/lib/host.sh`、`deploy/dev/lib/lock.sh`、`deploy/dev/lib/ownership.sh`、`deploy/dev/dev_test.go`
> - `internal/audit/archive.go`、`internal/audit/archive_config.go`、`internal/devfixture/devfixture.go`、`internal/devfixture/files.go`、`internal/devfixture/runner.go`、`internal/operator/bootstrap/bootstrap.go`、`internal/operator/bootstrap/token.go`、`internal/operator/ca/config.go`、`internal/operator/ca/vault.go`、`internal/operator/helmengine/real.go`、`internal/orchestrator/gc_config.go`、`internal/orchestrator/values_revision.go`、`internal/store/store.go`、`internal/trust/ed25519.go`
> - `test/e2e/config.go`、`test/e2e/prerequisite/smoke.sh`
> - `Makefile`、`.github/workflows/test.yml`、`.github/SECRETS.md`、`.gitignore`
> - `web/nginx.conf`、`web/src/router/index.ts`、`deploy/docker/Dockerfile.web`
> - `AGENTS.md`、`deploy/README.md`、`docs/dev-environment.md`

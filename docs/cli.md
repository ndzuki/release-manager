# 命令行可执行文件手册（cmd/*）

本文覆盖 `cmd/` 下**全部 14 个可执行目录**：7 个常驻服务与 7 个一次性工具。每条事实附
`文件:行号`（以 `task/092-project-docs-align` 分支工作树为准）；核实不到的写明「未找到」。

分类依据：常驻 = 走 `internal/app` 的 `app.Run`，启动 HTTP 监听并阻塞到收到信号
（`internal/app/app.go:122-229`）；一次性 = `main` 跑完即退。

## 总览

| 可执行 | 类型 | 职责 | 仓库入口 |
| --- | --- | --- | --- |
| `cmd/webhook` | 常驻服务 | 接收 CI bundle 提交，转发 orchestrator | `make run-webhook`（`Makefile:79`） |
| `cmd/orchestrator` | 常驻服务 | 控制面核心：Operation/Bundle/ReleaseDefinition/Operator 管理 + 可选 agent gateway | `make run-orchestrator`（`Makefile:83`） |
| `cmd/operator` | 常驻服务 | 客户集群内执行代理（agent 模式）或管理面 OperatorService（gateway 模式） | `make run-operator`（`Makefile:88`） |
| `cmd/auth` | 常驻服务 | 认证/RBAC/会话（Auth/Organization/Binding/Authorization 四个 Connect 服务） | `make run-auth`（`Makefile:92`） |
| `cmd/notifier` | 常驻服务 | 通知消费与投递 + NotifierService | `make run-notifier`（`Makefile:96`） |
| `cmd/api` | 常驻服务 | AuditService（Query/Emit/Export）+ 审计归档 worker | `make run-api`（`Makefile:100`） |
| `cmd/notification-sink` | 常驻服务（dev-only） | webhook 通知汇聚端（环形缓冲 + 读回） | 无 make target（未找到）；随 dev 集群部署 |
| `cmd/devseed` | 一次性工具 | 播种/重置开发夹具（REQ-065） | `make dev-seed`、`make dev-reset-data`（经 `deploy/dev/dev.sh`） |
| `cmd/e2e` | 一次性工具 | 分阶段 E2E 运行器（`run` 为默认子命令）与恢复（`cleanup`） | `make e2e-stage`、`make e2e-cleanup`（`Makefile:200,230`） |
| `cmd/store-migrate` | 一次性工具 | SQLite → PostgreSQL 一次性数据迁移 | 无 make target（未找到） |
| `cmd/sdkcheck` | 质量工具 | SDK-only 静态门禁（go/analysis） | `make sdk-check`（`Makefile:414`） |
| `cmd/reqcheck` | 质量工具 | 原子需求文档 10 节结构校验 | `make check-reqs`（`Makefile:478`） |
| `cmd/imagecheck` | 质量工具 | operator 镜像合规校验 | `make test-operator-image-sdk-only`（`Makefile:507`） |
| `cmd/installgate` | 质量工具 | Install/Upgrade SDK 门禁失败的一次性检疫裁决 | 由 `make test-install-sdk` / `make test-upgrade-sdk` 调用（`Makefile:418,448`） |

`make build-all` 只构建 6 个服务（`SERVICES := webhook orchestrator operator auth notifier api`，
`Makefile:47`，target `Makefile:51`）；`notification-sink` 与全部工具不在其中。工具类里只有
`sdkcheck`/`reqcheck` 有 build target（`make build-sdkcheck`/`make build-reqcheck`，`Makefile:525,529`），
其余用 `go run ./cmd/<name>/ ...` 直接跑。

## 常驻服务的公共运行模型

7 个服务共用 `internal/app.Run`（`internal/app/app.go:122`）：

- 读 `--config` 指向的 YAML（`internal/config.LoadService`，`internal/config/config.go:474-492`，viper）。
  多个键支持环境变量覆盖（`bindDatabaseEnvironment`，`internal/config/config.go:272-303`）：
  `DATABASE_DRIVER`、`DATABASE_DSN`、`REDIS_ADDRESS`、`MAINTENANCE`、`AUTHORIZATION_AUTH_URL`、
  `GATEWAY_ENABLED`、`GATEWAY_PORT`、`CUSTOMER_ID`、`CLUSTER_ID`、`OPERATOR_NAME`、
  `ENROLLMENT_TOKEN_FILE`、`CA_CERT_PATH`、`VALUES_MAX_DOCUMENT_BYTES` 等。
- 监听 `http_port` 单端口，同时服务 Connect/gRPC/gRPC-Web（`app.go:159-164`；协议面约定见 `Makefile:3-4`、
  `docs/architecture.md:87`）。
- 自动注册 `GET /health`（`app.go:139-143`）、`GET /environment`（`app.go:144`）、
  `GET /readyz`（依赖检查版，`app.go:158`）。
- 退出码：配置文件读失败 → `os.Exit(1)`（`app.go:126-129`）；`Register` 失败 → `os.Exit(1)`
  （`app.go:146-149`）。除此之外**未定义显式退出码**；正常收尾（SIGINT/SIGTERM，`app.go:171`）与
  监听失败（`app.go:207-213` 仅记日志后走关闭路径）都以 0 退出——端口冲突不会让进程以非零码失败，排查时看日志。
- 优雅停机统一 5 秒预算（`app.go:214-231`）。

## cmd/webhook

常驻服务。`webhook.v1.WebhookService/SubmitReleaseBundle`（`cmd/webhook/main.go:55-66`；
proto 见 `api/proto/webhook/v1/webhook.proto:46-57`），把请求连同 service token 以
`Authorization: Bearer` 转发给 orchestrator BundleService（`internal/webhook/service.go:64`）。
该上游同时是 readiness 检查 `orchestrator` 的目标（GET `<base>/readyz`，TASK-099，`cmd/webhook/main.go:90`）。

| flag | 默认值 | 必填 | 含义 | 出处 |
| --- | --- | --- | --- | --- |
| `--config` | `configs/webhook.dev.yaml` | 否 | 配置文件（`http_port: 8082`，`configs/webhook.dev.yaml:1`） | `main.go:57` |
| `--orchestrator-url` | `""`（空时代码回退 `http://localhost:8083`） | 否 | orchestrator Connect URL | `main.go:58`，回退 `main.go:79-85` |
| `--service-token` | `env DEV_WEBHOOK_SERVICE_TOKEN`，缺省 `""` | 否 | dev bundle-ingress token（REQ-065 D-100 选项 B） | `main.go:62` |

- 无数据库 flag（该服务不落库）。
- 前置：orchestrator 可达；dev 里 token 由 `dev-up` 生成注入，orchestrator 侧以
  `DEV_WEBHOOK_SERVICE_TOKEN` / `DEV_WEBHOOK_SERVICE_TOKEN_PREVIOUS` 的 SHA-256 哈希做常数时间比对
  （`cmd/orchestrator/main.go:898-911`）。
- 退出码：同公共服务（无自定义码）。
- 调用：`make run-webhook`（`Makefile:79-81`）、`make dev-stage-artifact`（`Makefile:292`）或
  `go run ./cmd/webhook/ --config configs/webhook.dev.yaml`。

## cmd/orchestrator

常驻服务，控制面核心。挂载 Orchestrator/Bundle/Cleanup 等处理器（`cmd/orchestrator/main.go` 内
`orchestratorv1connect`、`operatorv1connect` 注册），可选第二监听：operator agent mTLS gateway
（`buildGatewayServer`，`main.go:140-209`；经 `ExtraServers` 由 app.Run 统一拉起，
`internal/app/app.go:50-56,174-183`）。

| flag | 默认值 | 必填 | 含义 | 出处 |
| --- | --- | --- | --- | --- |
| `--config` | `configs/orchestrator.dev.yaml` | 否 | 配置文件（`http_port: 8083`，`configs/orchestrator.dev.yaml:1`） | `main.go:827` |
| `--target-env` | `staging` | 否 | trust policy 的目标环境名（production/staging） | `main.go:828`；用于 `internal/orchestrator/service.go:56,96,252` |
| `--signing-key` | `env JWT_SIGNING_KEY`，缺省 `change-me-in-production` | 否 | JWT 签名 key | `main.go:829-833` |

- 无 `--db` flag：数据库来自配置文件 `database:` 段或环境变量覆盖
  （本地 dev 为 sqlite `data/management.db`——与 release-auth 共享的权威库，`configs/orchestrator.dev.yaml`）。
- gateway 与 CA 也全部来自配置：`gateway.enabled/port`（本地默认关，`configs/orchestrator.dev.yaml:29-31`；
  dev 集群里开在 8084 并暴露 NodePort 30084，`deploy/kustomize/dev/configs/orchestrator.dev.yaml:32-34`），
  `ca.cert_path/key_path`（`configs/orchestrator.dev.yaml:33-37`；CA 缺失 fail-closed，`main.go:85-90`）。
- 鉴权快照拉取走配置 `authorization.auth_url`（`configs/orchestrator.dev.yaml:7`；
  默认 `http://localhost:8085`，`internal/config/config.go:251-266`——`--auth-url` flag **不存在**，
  `authURL` 字段没有任何赋值来源，见 `main.go:45,323-324`）。
- 环境变量：`JWT_SIGNING_KEY`、`DEV_WEBHOOK_SERVICE_TOKEN`、`DEV_WEBHOOK_SERVICE_TOKEN_PREVIOUS`
  （`main.go:808-819`）。
- 前置：sqlite 文件目录可写（`make run-orchestrator` 会 `mkdir -p data`，`Makefile:84`）；
  auth 服务在线（否则鉴权快照拉取失败）；开 gateway 时须有 CA key/cert。
- 退出码：同公共服务。
- 调用：`make run-orchestrator`（`Makefile:83-85`）、`make dev-stage-tenancy/dev-stage-config/dev-stage-publish`
  （`Makefile:300,316,324`）、`go run ./cmd/orchestrator/ --config configs/orchestrator.dev.yaml`。

## cmd/operator

常驻服务，双模式：配置 `agent.mode: gateway` 走管理面 OperatorService（含 SQLite 权威库），
其它值走 agent 运行时（`cmd/operator/main.go:35-38,63-72,125-133`）。dev 配置默认 `agent`
（`configs/operator.dev.yaml:3-4`）。agent 模式经 mTLS 双向流 `CommandStream` 连接 gateway，
断线以 1s→30s 指数退避重连（`main.go:93-115`；`CommandStream` 定义
`api/proto/operator/v1/operator.proto:335`）。HTTP 端口启用 h2c 以支持明文 gRPC
（`main.go:74-81`）。

| flag | 默认值 | 必填 | 含义 | 出处 |
| --- | --- | --- | --- | --- |
| `--config` | `configs/operator.dev.yaml` | 否 | 配置文件（`http_port: 8084`，`configs/operator.dev.yaml:1`） | `main.go:359` |
| `--db` | `data/operator.db` | 否 | gateway 模式的 SQLite 权威库（agent 模式不用） | `main.go:360` |
| `--command-db` | `data/operator-commands.db` | 否 | 持久 command/identity 库 | `main.go:361` |
| `--orchestrator-addr` | `https://operator-gateway.dev.release-manager.local:30084` | 否 | agent 连的 gateway Connect URL | `main.go:362` |
| `--kubeconfig` | `""` | 否 | 空则用 in-cluster 或默认 kubeconfig | `main.go:363`，解析 `main.go:338-356` |
| `--install-atomic` | `true` | 否 | 失败时原子卸载 release | `main.go:364` |
| `--install-timeout` | `5m` | 否 | Helm install 默认超时 | `main.go:365` |

- agent 模式硬性前置：`agent.customer_id` 与 `agent.cluster_id` 必须非空，否则启动失败
  （`main.go:152-155`；可用环境变量 `CUSTOMER_ID`/`CLUSTER_ID` 覆盖，`internal/config/config.go:289-290`）；
  enrollment token 来自 `agent.enrollment_token_file`（dev 为 `data/enrollment.token`，
  `configs/operator.dev.yaml:8`）或环境变量 `ENROLLMENT_TOKEN`（`cmd/operator/main.go:157-167`、
  `internal/operator/bootstrap/token.go:14-27`，用后删除 token 文件 `bootstrap.go:96-100`）；
  CA 证书 `data/gateway-ca.crt`（`configs/operator.dev.yaml:9-13`，校验 `main.go:158`）。
- agent 模式的 `/readyz` 挂 `gateway_session` 检查（CommandStream 存活才 Ready，TASK-099，`main.go:382`）。
- gateway 模式额外起一个 mTLS listener（`main.go:168-245`）并周期吊销过期 operator session
  （`main.go:303-336`，30s 一次）。
- 退出码：同公共服务。
- 调用：`make run-operator`（`Makefile:88-90`，传 `--db data/release-manager.db`）、
  `make dev-stage-operator`（`Makefile:308-314`）、`go run ./cmd/operator/ --config configs/operator.dev.yaml`。

## cmd/auth

常驻服务。挂载 `auth.v1` 的 Auth/Organization/Binding/Authorization 四个 Connect 服务
（`cmd/auth/main.go:189-203`；服务定义 `api/proto/auth/v1/auth.proto:182,392,516,599`），
外加 `GET /metrics`（`main.go:136-137`）。

| flag | 默认值 | 必填 | 含义 | 出处 |
| --- | --- | --- | --- | --- |
| `--config` | `configs/auth.dev.yaml` | 否 | 配置文件（`http_port: 8085`；sqlite `data/management.db`（与 release-orchestrator 共享的权威库），`configs/auth.dev.yaml`） | `main.go:232` |
| `--signing-key` | `env JWT_SIGNING_KEY`，缺省 `change-me-in-production` | 否 | JWT 签名 key（TTL 15m / refresh 7d，`main.go:140`） | `main.go:233-237` |

- **没有** `--db` flag：数据库由配置 `database:` 或 `DATABASE_DRIVER`/`DATABASE_DSN` 决定；
  postgres 驱动时启动即跑 golang-migrate（`main.go:94-99`）。
- 可选 Redis 会话存储：`redis.address` 为空则禁用（`main.go:113-134`）。
- 登录限流默认 5 次/分钟（`main.go:141-154`）。`maintenance: true` 时只保留 `Login`/`ValidateToken`
  与 health 面（`main.go:164-169`；配置键 `maintenance`，env `MAINTENANCE`）。
- `/readyz` 报 `database`、`redis` 两项依赖（`main.go:67-87`）。
- 退出码：同公共服务。
- 调用：`make run-auth`（`Makefile:92-94`）、`make dev-stage-auth`（`Makefile:334-340`）、
  `go run ./cmd/auth/ --config configs/auth.dev.yaml`。

## cmd/notifier

常驻服务。注册 `notifier.v1.NotifierService`（`cmd/notifier/main.go:176-188`），并跑通知消费循环
（`Run`，`main.go:45-48`）。

| flag | 默认值 | 必填 | 含义 | 出处 |
| --- | --- | --- | --- | --- |
| `--config` | `configs/notifier.dev.yaml` | 否 | 配置文件（`http_port: 8086`；sqlite `data/notifier.db`，`configs/notifier.dev.yaml:1-4`） | `main.go:124` |

- 无 `--db` flag；postgres 时对 `release_notifier` 库跑迁移（`main.go:94-109`）。
- dev 里 webhook sender 未配置（`NewWebhookSender(nil)`，`main.go:80-81`），未配置渠道在投递时被拒绝；
  dev 观测端点是 notification-sink。
- `/readyz` 报 `database`（`main.go:50-61`）。
- 退出码：同公共服务。
- 调用：`make run-notifier`（`Makefile:96-98`）、`go run ./cmd/notifier/ --config configs/notifier.dev.yaml`。

## cmd/api

常驻服务。注册 `audit.v1.AuditService`（Emit/Query/Export，`cmd/api/main.go:87-95`；
`api/proto/audit/v1/audit.proto:116,126,136,145`），后台跑审计归档 worker（`main.go:93-101`；归档参数见
`configs/api.dev.yaml:3-9`，输出 `data/archives`）。AuditService 全方法挂 JWT Bearer 拦截器，
缺 `Authorization` 即 Unauthenticated（`main.go:70`、`internal/audit/interceptor.go:23-44`）。

| flag | 默认值 | 必填 | 含义 | 出处 |
| --- | --- | --- | --- | --- |
| `--config` | `configs/api.dev.yaml` | 否 | 配置文件（`http_port: 8087`，`configs/api.dev.yaml:1`） | `main.go:138` |
| `--db` | `data/api.db` | 否 | SQLite 审计库 | `main.go:139` |
| `--signing-key` | `change-me-in-production` | 否 | 校验审计 API JWT 的签名 key（须与 auth 一致） | `main.go:140` |

- 退出码：同公共服务。
- 调用：`make run-api`（`Makefile:100-101`）、`make dev-stage-audit`（`Makefile:342-349`）、
  `go run ./cmd/api/ --config configs/api.dev.yaml --db data/api.db`。

## cmd/notification-sink

常驻服务，**dev-only**：notifier 的 webhook 汇聚端。`POST /webhook` 收通知（202/400 语义，
`cmd/notification-sink/main.go:98-106`），`GET /notifications` 读回容量 100 的环形缓冲与溢出计数
（环形缓冲 `main.go:37-67`，读回 `main.go:118-137`，容量写死 `main.go:142`）。dev 里 email/slack 渠道关闭，sink 是通知投递的
唯一观测点（`main.go:1-9`）。`/readyz` 检查 `config`（`http_port` 缺失即 fail-closed，TASK-099，`main.go:151`）。

| flag | 默认值 | 必填 | 含义 | 出处 |
| --- | --- | --- | --- | --- |
| `--config` | `deploy/kustomize/dev/configs/notification-sink.dev.yaml` | 否 | 配置文件（`http_port: 8088`，该文件 `:1`） | `main.go:140` |

- 退出码：同公共服务。
- 无 make build/run target（未找到）。镜像与部署：`deploy/docker/Dockerfile.notification-sink:10,16`、 <!-- check-docs:ignore 陈述该 target 不存在，不是可用性断言 -->
  `deploy/kustomize/services/notification-sink.yaml`；本地手跑
  `go run ./cmd/notification-sink/`（默认 config 路径相对仓库根）。

## cmd/devseed

一次性工具：经正式 Connect 接口播种/重置开发夹具（REQ-065），是 `internal/devfixture` 的薄 CLI
（`cmd/devseed/main.go:1-6`）。分阶段执行：`identity routing accounts trust bundle values enrollment
install verify`（`internal/devfixture/runner.go:68`），支持断点续跑（`--stop-after`）。

| flag | 默认值 | 必填 | 含义 | 出处 |
| --- | --- | --- | --- | --- |
| `-print-fixture-version` | `false` | 否 | 打印权威夹具版本常量后退出（dev.sh 用它取版本） | `main.go:36`；常量 `v2`（`internal/devfixture/devfixture.go:42`） |
| `-ensure-mtls-ca` | `-mtls-ca-dir` | `-mtls-ca-dir` 必填（否则报错） | 否 | dev mTLS CA 生成/复用后退出（AC-065-36） | `main.go:41-42,70-75`；实现 `mtls_ca.go`；dev.sh 调用 `deploy/dev/dev.sh:410` |
| `-operator-timeout` | `0`→包默认 180s（env `DEV_TIMEOUT_OPERATOR`） | 否 | 等待 operator 上线的秒数 | `main.go:43`；默认 `internal/devfixture/devfixture.go:50` |
| `-seed-retries` | `0`→包默认 3（env `DEV_TIMEOUT_SEED_RETRIES`） | 否 | 阶段写入重试（1s/2s/4s 退避） | `main.go:44`；默认 `devfixture.go:51` |
| `-stop-after` | `""` | 否 | 提交到该阶段为止干净退出（如 `enrollment`） | `main.go:45` |
| `-reset` | `false` | 否 | 重建数据库并重播种（dev-reset-data） | `main.go:46`；`-reset` 需要 PostgreSQL DSN（`internal/devfixture/reset.go:30-33`） |
| `-orchestrator` / `-webhook` / `-auth` | `http://localhost:8083` / `:8082` / `:8085` | 否 | 三个 Connect 入口 | `main.go:47-49`，默认常量 `main.go:20-25` |
| `-admin-user` / `-admin-password` | `dev-admin`（env `DEV_ADMIN_USER`）/ env `DEV_ADMIN_PASSWORD` | 否 | 平台管理员 | `main.go:50-51` |
| `-deployer-user` / `-deployer-password` | `dev-deployer`（env `DEV_DEPLOYER_USER`）/ env `DEV_DEPLOYER_PASSWORD` | 否 | deployer 账号 | `main.go:52-53` |
| `-reader-password` / `-e2e-runner-password` | env `DEV_READER_PASSWORD` / `E2E_RUNNER_PASSWORD` | 否 | reader 与 e2e-runner（release_admin） | `main.go:54-55` |
| `-trust-root-private-key` | env `DEV_TRUST_ROOT_PRIVATE_KEY` | ci profile 必填 | Dev Trust Root Ed25519 key | `main.go:56` |
| `-data-dir` | `data` | 否 | dev-fixture.json / 进度 / credentials 目录 | `main.go:57`；默认 `devfixture.go:43` |
| `-database-dsn` | env `RELEASE_MANAGER_DATABASE_DSN` | `--reset` 时必填 | PostgreSQL DSN | `main.go:58` |

- 环境变量：模式 `DEV_PROFILE`（仅 `local`/`ci`，非法值直接失败，`main.go:77-80`）；密码缺省时回读
  `data/dev-credentials.env`（四键：`DEV_ADMIN_PASSWORD`、`DEV_DEPLOYER_PASSWORD`、
  `DEV_READER_PASSWORD`、`E2E_RUNNER_PASSWORD`，`internal/devfixture/files.go:78-84`）。
- 前置：orchestrator/webhook/auth 服务在线（dev 集群里由 `dev-up` 保证；dev-seed 本身还要求 JWT key、
  service token、mTLS CA 已由 dev-up 生成，`deploy/dev/dev.sh:1538-1548`）。
- 成功输出契约 `development seed complete` + `fixture_version/namespace/orchestrator/webhook/auth` 行
  （`main.go:106-113`），供 dev.sh 解析。
- 退出码：失败 `os.Exit(1)`（`main.go:101-104`、`fail()` `main.go:125-127`）；其余 0。
- 调用：`make dev-seed`（`Makefile:117-119`）、`make dev-reset-data`（`Makefile:121-123`，需 `CONFIRM=1`）、
  `go run ./cmd/devseed/ -stop-after enrollment`。

## cmd/e2e

一次性工具：分阶段 E2E 运行器，两个子命令——`run`（默认，可省略）与 `cleanup`
（`cmd/e2e/main.go:91-99`）。输出三面分离：stdout 人类摘要、stderr slog、机器产物 JSON 落
`--output-dir`（`main.go:1-5`）。

`run` 的 flag（`main.go:635-655`）：

| flag | 默认值 | 必填 | 含义 |
| --- | --- | --- | --- |
| `--stages` | `all` | 否 | 逗号分隔阶段；`all` 不能与具名阶段混用；未知/重复名 → usage 错误（`main.go:677-714`） |
| `--timeout` | `5m` | 否 | 单阶段超时（必须为正，`main.go:115-117`） |
| `--total-timeout` | `25m` | 否 | 总预算 |
| `--output-dir` | `./e2e-results` | 否 | JSON 产物目录（不得为空，`main.go:152,716-727`） |
| `--parallel` | `false` | 否 | 允许 `inventory` 与 `artifact` 并发（`main.go:644`；`docs/testing.md:85`） |
| `--keep-on-failure` | `false` | 否 | 失败时保留阶段诊断（写 `diagnostics/<run_id>/<stage>/result.json`，`main.go:758-763`） |
| `--snapshot-full` | `false` | 否 | baseline 全量快照 |
| `--env-config` | `""` | **是** | 运行时环境配置（`main.go:108-110`） |

`cleanup` 子命令 flag（`main.go:657-675`）：`--env-config`（必填，`main.go:439-441`）、
`--output-dir`（`./e2e-results`）、`--baseline-file`（缺省 `<output-dir>/baseline.json`，`main.go:671-673`）。
cleanup 只经正式 Connect API 回收（`main.go:471-473` 注释）。

阶段全集（canonical 顺序）：`control-plane`、`inventory`、`artifact`、`release`、`isolation`、
`emergency`、`restart`（`test/e2e/stage.go:16-22`；顺序 `test/e2e/runner.go:26-34`）。

产物：`baseline.json`（`main.go:256-259`）、`residue.json`（`main.go:285`；名 `test/e2e/result.go:138`）、
每阶段 `<stage>.json`（`main.go:363-372`）、`run.json`（`main.go:423-426`）。

退出码（显式定义）：`0` success、`1` runtime、`2` usage、`3` 环境锁冲突
（常量 `main.go:28-34`；锁语义 `reportLockError` `main.go:737-745`）。阶段判定复用报告里的
`report.ExitCode`（`main.go:430`），越界值被归一为 1（`main.go:340-346`）。

环境变量与前置：

- `--env-config` 指向的 YAML 用严格 schema 解析（未知字段即拒绝，`test/e2e/config.go:225-233`）；
  正式来源是 `make e2e-env-config` 从 `data/dev-status.json`/`data/dev-fixture.json` 组装的
  `data/e2e-env-config.yaml`（`Makefile:156-180`；模板 `configs/e2e.dev.yaml:1-12`）。
- 凭据经 `credentials.e2e_runner.password_env` 间接取环境变量（`test/e2e/config.go:307-316`），
  make 层从 `data/dev-credentials.env` source `E2E_RUNNER_PASSWORD`（`Makefile:202,232`）。
- `E2E_RUN_ID` 可选：设定则做 run id（否则 `local-<UTC时间>-<pid>`，`cmd/e2e/main.go:747-756`；
  `test/e2e/config.go:431-440`）。
- `E2E_LOCK_FILE` 可选：设置时进程内 advisory lock（`main.go:729-735`）；make 层另用
  `flock -s -n -E 3 data/dev.lock`（`Makefile:206-207,236-237`）。
- dev 环境（`make dev-up dev-seed dev-status`）必须已就绪。

门禁角色：`make e2e-prerequisite` / `e2e-prerequisite-ci` 是 AC-066-17 前置冒烟（`Makefile:186-198`，
跑 `test/e2e/prerequisite/smoke.sh`，落 `data/smoke-result.json`）；CI 以 `e2e-prerequisite` job 全触发
（`.github/workflows/test.yml:324,357`），全量 E2E 由 `e2e` job 在 push main/手动触发时跑 `make e2e-all`
（`test.yml:376,433`）。

## cmd/store-migrate

一次性工具：SQLite → PostgreSQL 全量迁移 + 校验，成功时向 stdout 写 JSON 报告
（`cmd/store-migrate/main.go:1-4`）。

| flag | 默认值 | 必填 | 含义 | 出处 |
| --- | --- | --- | --- | --- |
| `--source` | `""` | 是 | SQLite 源文件 | `main.go:36` |
| `--target-dsn` | env `RELEASE_MANAGER_DATABASE_DSN` | 是（两者皆空则 usage 错误） | PostgreSQL 目标 DSN | `main.go:37-38,52-55` |
| `--migrations` | `migrations` | 否 | PG 迁移目录 | `main.go:39-40` |

- 退出码：`0` 成功、`1` 一切失败（含 usage 错误，常量 `main.go:24-29`）。stderr 行前缀分类：
  `usage_error` / `connection_unavailable` / `migration_failed` / `data_import_mismatch`
  （`main.go:44-75,84-92`）；DSN 在错误信息中脱敏（`main.go:104-124`）。
- 无 make target（未找到）；调用 `go run ./cmd/store-migrate/ --source data/api.db --target-dsn 'postgres://...'`。

## 质量工具与门禁

### cmd/sdkcheck

一次性工具：go/analysis 单分析器 CLI（`singlechecker.Main`，`cmd/sdkcheck/main.go:19-39`），检测
`os/exec` 到禁止二进制的调用路径（`helm`、`kubectl`、`istioctl`、`argocd`、`flux`、`terraform`、`tofu`，
`internal/quality/sdkcheck/analyzer.go:28-36`）。规则五条：`os_exec_import`、`fork_exec`、
`shell_wrapper`、`forbidden_binary_invocation`、`expired_exception`（`analyzer.go:42-46`）。
自身 flag 为两个：`-exceptions` 指向例外清单文件（默认 `sdkcheck.exceptions.yaml`，`main.go:20`），
`-build-tags` 指定 build tags（`main.go:21`）；Go 标准库 `flag` 同时接受 `--exceptions` /
`--build-tags` 双横线拼写，位置参数是包模式。

| flag | 默认值 | 含义 | 出处 |
| --- | --- | --- | --- |
| `-exceptions` | `sdkcheck.exceptions.yaml` | 例外清单路径 | `main.go:20` |
| `-build-tags` | `""` | 逗号分隔 build tags（经 `GOFLAGS=-tags=...` 注入） | `main.go:21,30-37` |
| 位置参数 | — | 包模式（如 `./...`） | `main.go:39` |

- 例外条目字段：`owner`/`reason`/`expires_at`（`YYYY-MM-DD`）/`path`/`rule`；过期例外在加载时即报
  `expired_exception` 失败（`analyzer.go:100-121`）。当前例外仅两条 test-only：
  `deploy/dev/dev_test.go` 与 `cmd/e2e/main_test.go`（`sdkcheck.exceptions.yaml:3-12`，到期 2099-12-31）。
- 退出码：例外文件加载失败 `2`（`main.go:24-28`）；其余委托 x/tools singlechecker——无包参数 `1`
  （`golang.org/x/tools@v0.48.0/go/analysis/singlechecker/singlechecker.go:67`）、分析/加载错误 `1`、
  **检出违例（diagnostics）`3`**（同版本 `go/analysis/internal/checker/checker.go:259-263`）。门禁只判非零。
- 门禁角色：`make sdk-check`（`Makefile:414-416`，命令 `go run ./cmd/sdkcheck/ -exceptions
  sdkcheck.exceptions.yaml ./...`）；`make quality` 的组成部分（`Makefile:522`）；CI `sdk-check` job
  跑同一命令（`.github/workflows/test.yml:37`），另有 `test-sdkcheck` job 单测分析器本体（`test.yml:245`）。
  `make test-rollout-watch` 还对 integration tag 的包跑一遍（`Makefile:387`：`-build-tags integration
  ./internal/operator/observer ./test/integration`）。

### cmd/reqcheck

一次性工具：无 flag，位置参数为一个或多个 REQ markdown（`cmd/reqcheck/main.go:4-6,16-24`），
校验 10 节模板结构（`internal/quality/reqcheck/check.go:1-17`）。

- 退出码：usage（无参数）`2`（`main.go:17-20`）；文档读取/校验违例 `1`（`main.go:26-41`）；全部 OK `0`。
- 门禁角色：`make check-reqs`（`Makefile:478-484`，`find . -path '*/Requirements/REQ-*.md'` 喂给
  `./bin/reqcheck`），属于 `make quality`；文档未接入 CI（`docs/testing.md:58`）。

### cmd/imagecheck

一次性工具：对 `docker save` 归档做策略校验（`cmd/imagecheck/main.go:1-9`）。

| flag | 默认值 | 必填 | 含义 | 出处 |
| --- | --- | --- | --- | --- |
| `--dockerfile` | `deploy/docker/Dockerfile.operator` | 否 | 反查构建配方 | `main.go:25` |
| `--policy` | `imagecheck.operator.yaml` | 否 | 策略文件 | `main.go:26` |
| `--archive` | `-`（stdin） | 否 | tarball 路径，`-` 读 stdin | `main.go:27` |

- 策略文件是 JSON（`imagecheck.operator.yaml:1-26`）：固定 `base_image` digest、`required_entrypoint`、
  `allowed_executables` 白名单、`forbidden_basenames`（helm/kubectl 等）、路径断言（`/release-operator`
  必须存在、`/bin/sh` 必须不存在）。
- 退出码（文件头注释与实现一致）：`0` 合规、`1` 违例（逐条 JSON 打到 stderr）、`2` 输入/解析错误
  （`main.go:7,30-73`）。
- 门禁角色：`make test-operator-image-sdk-only`（`Makefile:507-519`：`docker-build-operator` →
  `docker save` → `go run ./cmd/imagecheck --archive ... --policy imagecheck.operator.yaml`），
  CI `operator-image-sdk-only` job（`.github/workflows/test.yml:104`）。例外机制：无独立例外文件，
  白名单内嵌在 `allowed_executables`。

### cmd/installgate

一次性工具：为一次 Install/Upgrade SDK 门禁失败落「时间受限基础设施检疫」裁决（`cmd/installgate/main.go:1-3`）。

| flag | 默认值 | 必填 | 含义 | 出处 |
| --- | --- | --- | --- | --- |
| `--quarantine` | `install-sdk.quarantine.yaml` | 否 | 检疫文件路径（upgrade 门禁传 `upgrade-sdk.quarantine.yaml`） | `main.go:26`；`Makefile:30,36,428` |
| `--scenario` | — | 是 | 失败场景 | `main.go:27,32-35` |
| `--rule-id` | — | 是 | 稳定失败规则 ID | `main.go:28` |
| `--message` | — | 是 | 失败摘要 | `main.go:29` |

- 行为：精确匹配 `scenario`+`rule_id` 的未过期检疫条目 → 状态 `quarantined` 并 exit `0`；
  不匹配 → 打印 `failed` 诊断 JSON 并 exit `1`；缺 flag 或 JSON 序列化失败 exit `2`；
  检疫文件读取/校验失败 exit `1`（`main.go:32-41,63-66`）。
- 检疫条目仅允许基础设施规则 `cluster_unavailable`/`image_pull_failed`/`runner_environment_failed`，
  必填六字段且到期日 ≤ 今天+7 天（`internal/quality/installgate/quarantine.go:15-22,89-120`）。
  当前两个检疫文件都是空清单（`exceptions: []`，`install-sdk.quarantine.yaml:1-2`、
  `upgrade-sdk.quarantine.yaml:1-2`）——任何失败都会 exit 1 拦停门禁。
- 门禁角色：`make test-install-sdk` / `make test-upgrade-sdk` 在 kind 缺失或 `go test` 失败时调用它
  （`Makefile:422-446,452-476`）。注意 Makefile 在 kind 缺失/建集群失败分支里于 `installgate` 之后无条件
  `exit 0`（`Makefile:432,442`），最终检疫语义以日志 JSON 与门禁整体结果为准。

## 未找到 / 说明

- `cmd/store-migrate`、`cmd/devseed`、`cmd/e2e`、`cmd/notification-sink`、`cmd/imagecheck`、
  `cmd/installgate`：Makefile 无专属 build/run target（`grep Makefile store-migrate` 等无命中）；
  表中给出的 make 目标均已在当前 `Makefile` 逐行核实存在。
- 各服务「显式退出码」仅 `os.Exit(1)` 两处（`internal/app/app.go:128,148`）；没有为成功/失败定义的
  退出码常量表。
- `cmd/orchestrator` 的 `--auth-url` flag 未找到（鉴权地址只走配置/env，见上文）。

> 事实源：`cmd/api/main.go`、`cmd/auth/main.go`、`cmd/devseed/main.go`、`cmd/devseed/mtls_ca.go`、
> `cmd/e2e/main.go`、`cmd/imagecheck/main.go`、`cmd/installgate/main.go`、`cmd/notification-sink/main.go`、
> `cmd/notifier/main.go`、`cmd/operator/main.go`、`cmd/orchestrator/main.go`、`cmd/reqcheck/main.go`、
> `cmd/sdkcheck/main.go`、`cmd/store-migrate/main.go`、`cmd/webhook/main.go`、
> `internal/app/app.go`、`internal/config/config.go`、`internal/devfixture/devfixture.go`、
> `internal/devfixture/runner.go`、`internal/devfixture/reset.go`、`internal/devfixture/files.go`、
> `internal/audit/interceptor.go`、`internal/quality/sdkcheck/analyzer.go`、`internal/quality/installgate/quarantine.go`、
> `internal/quality/reqcheck/check.go`、`internal/quality/imagecheck/analyzer.go`、`internal/webhook/service.go`、
> `internal/operator/bootstrap/token.go`、`internal/operator/bootstrap/bootstrap.go`、`test/e2e/config.go`、
> `test/e2e/runner.go`、`test/e2e/stage.go`、`test/e2e/result.go`、`Makefile`、`.github/workflows/test.yml`、
> `configs/*.dev.yaml`、`deploy/kustomize/dev/configs/*`、`deploy/dev/dev.sh`、
> `sdkcheck.exceptions.yaml`、`imagecheck.operator.yaml`、`install-sdk.quarantine.yaml`、
> `upgrade-sdk.quarantine.yaml`、`docs/testing.md`、`docs/dev-environment.md`、`api/proto/**`。

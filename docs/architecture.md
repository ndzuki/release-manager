# release-manager 架构概览

`release-manager` 是面向多 Customer 的 Helm 发布管理控制面：中心控制面负责编排与权威状态，客户 Cluster 内的 release-operator 以**出站**方式连回中心，并在集群内用 Go SDK 执行 Helm/Kubernetes 操作。平台以 **Customer** 为租户边界、**Cluster** 为部署与 Operator 运行的隔离边界，并把 ReleaseDefinition、ReleaseBundle、ValuesRevision、Operation 四个对象分离为"发布到哪里 / 发布什么 / 以什么配置发布 / 一次执行记录"。全部正式接口以 protobuf 为单一契约源、通过 Connect 单端口暴露。

读者指引：新加入的工程师读第 1、2 节建立边界与服务地图；契约消费方（前端、Operator、CI）读第 3 节；运维与测试读第 4、5 节。领域词汇以 Vault 侧 `Notes/CONTEXT.md` 与 `Design/glossary.md` 为权威，本文不复述全部术语。

## 1. 执行边界与信任模型

### 1.1 中心控制面

- 浏览器只访问中心服务（Connect 单端口），从不直连 Cluster 或 Operator。
- 中心侧只保存 Customer、Cluster、Operator 身份、会话状态与待投递命令；**不持有客户集群的入站网络通道、长期 kubeconfig 或可跨客户复用的执行凭据**。
- 所有集群侧能力只能通过 Operator 协议演进；Operator 离线时控制面只能排队或 fail closed。
- Operator Session、证书吊销、命令重放与能力版本是平台级可靠性基础。

### 1.2 客户集群 Operator 出站

- release-operator 是**唯一集群执行边界**，由客户集群侧主动通过 mTLS Connect 双向流连接中心控制面。
- Helm/Kubernetes 操作、SecretRef 解析、DryRun、Runtime Pull 与 Inventory 采集全部在 Operator 侧完成。
- Operator 身份与证书严格绑定 Customer 与 Cluster；不得以文件名或递增序号充当证书身份。
- 证书身份权威 = `sha256(certDER)` 前 10 字节的 hex（ADR-018/D-010）；CA 证书与私钥 prod 用 Vault、dev 用文件，**启动加载、不重新生成**，renew 即时失效旧证书。

### 1.3 SDK-only（禁命令行）

- 运行时业务代码只调用 Helm Go SDK（`helm.sh/helm/v3/pkg/action`）与 `k8s.io/client-go`；**禁止** `os/exec`、`exec.Command`、shell wrapper、sidecar 或脚本间接调用 `helm`/`kubectl`。
- 每个 Operation 独立初始化 `action.Configuration`，不跨并发 Operation 共享可变 action client。
- `kind`/`docker`/`k3d` 等 CLI 只允许存在于 Makefile、CI 或人工开发环境生命周期中；集成测试本身仍通过生产 SDK 路径验证。
- 门禁由 `cmd/sdkcheck`（`go/analysis` + `types.Info` 静态分析）在 CI 中 fail closed 执行；例外必须有 owner、reason 与 `expires_at`，且不得用于 Helm/Kubernetes 业务。

### 1.4 命令投递与持久化语义

- 中心以数据库 **Command Outbox** 作为待投递命令的权威存储；命令携带全局单调 `sequence`、`command_id`、`operation_id`、`payload_version` 与 `deadline`。
- 投递状态按 `pending → delivered → persisted → running → terminal` 推进；MVP 每个 Operator `max_inflight=1`。
- Operator 在本地持久 Store（BoltDB 或等价实现）**fsync 命令后才发送 `ACK_PERSISTED`**；重启后重放未终态命令，并按 `command_id` 返回已完成结果而不重复执行。
- 重连时 Operator 上报 `last_seen_sequence`，中心重投未持久化命令并处理 sequence gap。
- 语义为**持久化 + at-least-once + 幂等执行**，不宣称跨网络 exactly-once；仅收到网络 ACK 不等于已持久化。
- 例外：EMERGENCY 命令走在线 Operator stream，**不属于**标准 Command Outbox（见 `Design/contracts/emergency-execution.md`）。

### 1.5 授权边界

- 服务端是唯一授权裁决点：请求先验证持久 Session/User，再确认 active OrganizationMembership、OrganizationCustomerBinding 与 Casbin `sub/domain/object/action` policy。
- 客户端传入的 Actor、role、organization **不能覆盖**认证上下文；scope mismatch 按 not found 或 permission denied 处理。
- 跨服务授权由身份上下文发布**版本化 Authorization Snapshot**，业务服务维护只读投影与 checkpoint；治理写操作仅在投影追平且新鲜时执行。
- policy/snapshot 不可用时写操作 fail closed，读操作仅在明确允许的场景降级；前端路由守卫与按钮隐藏只提供 UX，不构成安全边界。

### 1.6 发布输入模型分离

Operation 创建时校验并固化四者关系，后续对象变化不得改写既有 Operation 输入：

| 对象 | 语义 |
| --- | --- |
| ReleaseDefinition | 发布到哪里（Customer/Cluster、namespace、release name、chart） |
| ReleaseBundle | 发布什么；以 digest 固化的不可变制品快照 |
| ValuesRevision | 以什么非敏感期望配置发布；内容创建后不可变，审批走异人审批 |
| Operation | 一次 `INSTALL`/`UPGRADE`/`ROLLBACK`/`EMERGENCY` 动作，具有幂等键、`state_version` 与终态 |

现场 Helm Release 只作为 Inventory 观察结果，通过 `definition_id` 关联，不作为配置根对象。

## 2. 服务清单

下表的二进制与服务名逐项核对自 `cmd/*/main.go` 包注释、`Name()` 返回值与 `api/proto/**` 的 `service` 声明；端口取自 `configs/*.dev.yaml` 的 `http_port`（本地 dev 默认值，非生产端口）。所有 `app.Run` 启动的服务统一提供 `GET /health`、`GET /readyz`、`GET /environment`。

| 二进制 | 职责 | Connect service | dev 端口来源 |
| --- | --- | --- | --- |
| `release-orchestrator`（`cmd/orchestrator`） | 中心编排：Operation 状态机、Bundle 接入、Trust、Cleanup、Operator 网关与授权投影 | `OrchestratorService`、`BundleService`、`CleanupService`、`TrustService`、`OperatorService`（网关） | `configs/orchestrator.dev.yaml` → `http_port: 8083`；网关 `gateway.port: 8084`（dev 默认 `gateway.enabled: false`） |
| `release-operator`（`cmd/operator`） | 客户集群执行边界：agent 模式出站连接；保留管理面 `gateway` 模式（代码注释标注 TASK-065 移除该路径） | `OperatorService`（gateway 模式服务端；agent 模式为客户端） | `configs/operator.dev.yaml` → `http_port: 8084` |
| `release-auth`（`cmd/auth`） | 本地认证会话、组织与成员、Customer 绑定、RBAC 与授权快照发布 | `AuthService`、`OrganizationService`、`BindingService`、`AuthorizationService` | `configs/auth.dev.yaml` → `http_port: 8085` |
| `release-webhook`（`cmd/webhook`） | Harbor 等制品源 webhook 入口，转发至 `BundleService` | `WebhookService` | `configs/webhook.dev.yaml` → `http_port: 8082` |
| `release-notifier`（`cmd/notifier`） | 通知投递（webhook/email/Slack 通道）与通知任务消费 | `NotifierService` | `configs/notifier.dev.yaml` → `http_port: 8086` |
| `release-api`（`cmd/api`） | 审计查询服务与审计归档 worker（REQ-029/030） | `AuditService` | `configs/api.dev.yaml` → `http_port: 8087` |
| `release-notification-sink`（`cmd/notification-sink`） | **dev-only** 微服务：接收 notifier 的 webhook JSON 并以有界环形缓冲回读 | 无 Connect service；`GET /notifications` | `deploy/kustomize/dev/configs/notification-sink.dev.yaml` → `http_port: 8088` |
| `devseed`（`cmd/devseed`） | 通过正式公开服务 seam 播种/重置确定性 Development Fixture（REQ-065），thin CLI over `internal/devfixture` | 无 | 无 HTTP；默认目标 `:8083`/`:8082`/`:8085` |
| `e2e`（`cmd/e2e`） | 分阶段 E2E runner 单一入口，含 `run`（默认）与 `cleanup` 子命令，机器可读输出写入 `--output-dir` | 无 | 无 HTTP；`--env-config` 必填 |
| `store-migrate`（`cmd/store-migrate`） | 一次性 SQLite → PostgreSQL 数据迁移 CLI，成功时向 stdout 输出 JSON 报告 | 无 | 无 HTTP |
| `sdkcheck`（`cmd/sdkcheck`） | SDK-only 静态分析门禁：检出对 helm/kubectl/istioctl 等禁用二进制的 `os/exec` 调用（REQ-037） | 无 | 无 HTTP |
| `imagecheck`（`cmd/imagecheck`） | Docker 镜像归档对策略文件的合规校验（退出码 0/1/2） | 无 | 无 HTTP |
| `installgate`（`cmd/installgate`） | 对单次 Helm Install SDK gate 失败应用有时限的基础设施隔离策略 | 无 | 无 HTTP |
| `reqcheck`（`cmd/reqcheck`） | 按 10 节模板校验原子需求文档（REQ-039） | 无 | 无 HTTP |

说明：`reqcheck`/`sdkcheck`/`imagecheck`/`installgate`/`store-migrate`/`devseed`/`e2e` 是质量与运维 CLI，不提供 Connect 服务面；`notification-sink` 是 dev-only 的非 Connect HTTP 服务。`api/proto/**` 中的 `ExternalIdentityService` 目前**只有 proto 与只读 procedure 白名单条目，未见 handler 挂载**（见第 6 节遗留项）。

## 3. 契约面

单一来源与传输：

- `api/proto/**` 是协议唯一来源；Go/TypeScript 代码一律来自 buf 生成（`api/gen/**`、`web/src/gen/**`），**禁止手写** message/service、旧 RPC adapter、shim 或 alias。
- 每个服务通过标准 `net/http` ServeMux 的**单一端口**同时提供 Connect、gRPC 与 gRPC-Web；浏览器使用 `@connectrpc/connect` + `@connectrpc/connect-web`，Operator 使用 Connect stream，不引入 REST gateway、raw grpc-go 或第三方 router。
- buf 配置（`buf.yaml`）启用 `STANDARD` lint 与 `FILE` breaking，并豁免 `RPC_REQUEST_RESPONSE_UNIQUE`、`RPC_RESPONSE_STANDARD_NAME`、`RPC_REQUEST_STANDARD_NAME`。

写请求幂等：

- key 只经 HTTP header `Idempotency-Key` 传输，protobuf body 不含重复字段。
- `scope = organization_id + ":" + release_definition_id`；`hash = sha256(canonical typed request body)`。
- 同 scope+key+hash：返回首次结果、不重复执行；同 scope+key+**异 hash**：`idempotency_conflict`。
- key 不写 URL、Web Storage 或日志；页面卸载不持久化。授权检查先于幂等命中。

Typed Error Detail：

- 服务端附加 typed detail（如 `EmergencyErrorDetail`：`reasonCode`/`violations`/关联 IDs/`retryable`/`refreshRequired`）。
- 前端通过 `ConnectError.findDetails(...)` 解码，**禁止解析自由文本 message**。

分页与演进：

- 列表使用 opaque cursor：默认 `pageSize=50`、上限 `100`；cursor 绑定 scope、过滤条件、稳定排序键与 snapshot boundary。
- 过滤条件变化重置 cursor；cursor 过期返回 `invalid_cursor`，前端只重置对应列表、保留表单与已选 ID。
- 新增字段/RPC 向后兼容；未知字段忽略、缺失字段 fail closed。
- 旧 `EmergencyChange(payload, actor)` 为安全边界：proto、生成代码、handler、测试、调用方零匹配后才可干净删除，**禁止兼容保留**。

内部服务认证（service-to-service）：

- 受信服务间 RPC（如 `release-webhook → release-orchestrator` 的 `BundleService`）走独立静态 service token：`Authorization: Bearer <token>`，服务端以 SHA-256 hash + constant-time comparison 校验，并保存 `current` + `previous` 双 hash 支持无停机轮换，actor = `service:release-webhook`。
- 消费者不附加业务 JWT；服务端经 `TryAllInterceptor(JWT, ServiceTokenInterceptor)` 顺序尝试，二者皆失败返回 `unauthenticated`。
- 生产 Secret manager 注入与生产配置加载仍由 REQ-011 owner 承接；dev 范围内为 TASK-065 的最小接线。

消费方门禁：实现任何消费方之前，generated symbols 编译 fixture（`tsc --noEmit`）通过 + `buf lint`/`buf breaking` 通过 + 旧符号零匹配；FAIL 时停止实现并记录 dependency conflict。

## 4. 数据与一致性

### 4.1 分环境引擎与双引擎 schema

- **dev = SQLite**（`configs/*.dev.yaml` 的 `database.driver: sqlite`；`api`/`operator`/`webhook` 另有 `--db` 直连路径）；**test = 内存 SQLite**（`sqlitestore.OpenTest`）+ `sqlmock`，可选真 Postgres（`//go:build integration` + `POSTGRES_TEST_DSN`，默认 skip）。
- **prod = PostgreSQL**（`deploy/kustomize/postgres`、`migrations/*.sql`）。
- **新功能不得假设单一引擎，schema 必须双引擎兼容**：新字段需同时落 PostgreSQL 迁移（`migrations/*.up.sql|*.down.sql`，当前 26 组）与 SQLite 内建 Go schema（`internal/store/sqlite/db.go`）。
- 命名约定：表名 snake_case 复数；字段以 `_id`/`_at` 结尾；时间戳 `created_at`/`updated_at`。
- Postgres 迁移以 golang-migrate 版本化 SQL 为**唯一权威，禁止 GORM AutoMigrate**；SQLite schema 内建在 Go 中。

### 4.2 Operation 状态机、CAS 与事务 Outbox

- Operation 是发布执行的权威状态机，标准路径为 `pending → preflight → queued → running → succeeded|failed|cancelled|timeout`；EMERGENCY 使用明确的简化路径。
- 所有状态转换以 `state_version` **compare-and-swap** 执行；同 ReleaseDefinition 的不兼容非终态写 Operation 由数据库约束/事务门禁互斥。
- 写请求使用 scope + `Idempotency-Key` + request hash；Operation 创建在**同一事务**写 operation、idempotency record 与 preflight dispatch outbox；终态转换在同一事务写 `terminal_at`、时间线/状态事件及 lifecycle 回填。
- 审批、审计、通知等跨进程副作用使用**事务 Outbox**，消费者按稳定 identity 幂等处理；不提供通用 best-effort 回调钩子。
- 下游语义为 at-least-once，必须按 event/job identity 去重。
- Web 只消费 snapshot/Timeline，不推演状态；Execute 响应只等事务接受，不等待 Operator。
- Late Result：终态 Operation 的 `effectStatus` 从 `UNKNOWN` 解析时保持 state/`terminalAt`，`stateVersion+1`，写 `EMERGENCY_EFFECT_RESOLVED`，再按 policy 建 task 或进入 REVERT awaiting。

### 4.3 共享 PostgreSQL 连接池与事务 seam

- 使用 pgx stdlib 驱动的单一 `*sql.DB` 作为物理连接池，并用同一个 `*sql.DB` 构造 GORM（`internal/store/postgres` 的 `DB`/`Tx` 共享 GORM `ConnPool`）。
- 业务事务由 GORM 开启；必须与业务写原子提交的 raw SQL 通过该 transaction 绑定的 connection 执行。
- GC 可从同一 pool 获取专用连接执行 advisory lock 与批处理，但**不得声称加入已有业务事务**。
- 实现必须暴露明确 UnitOfWork / raw executor seam，不能在 callback 内另取连接；业务代码避免依赖隐式 GORM `Save`/upsert 行为。

### 4.4 维护窗口切换与单一数据权威

- Orchestrator 从 SQLite 迁移到 PostgreSQL 采用**维护窗口一次性切换**，不做在线双写。
- 维护模式按 Connect procedure allowlist 拒绝所有业务写入并返回 `CodeUnavailable`/HTTP 503（`app.MaintenanceInterceptor`）；健康、就绪与明确允许的读取继续可用。
- 停写后导出 SQLite → 执行 golang-migrate → 导入 PostgreSQL → 校验行数、外键与关键不变量 → 配置单一 PostgreSQL Store 重启。
- 每个进程任何时刻只打开一个权威业务后端；首次成功的 PostgreSQL 业务写入后 PostgreSQL 成为唯一权威，不提供 SQLite write-back 或自动双向回滚。

## 5. 交付波次

| 波次 | 主题（一句话） | Phase |
| --- | --- | --- |
| wave-0 | 契约与基础收敛：`common/v1` proto 规范化、values 域契约与 Prepare Session/异人审批收敛；契约一旦合入即冻结 | Phase 1 |
| wave-1 | 发布核心增量：`CreateOperation → Preflight → 执行`链路在真实信任与代理环境下可跑（Preflight 幂等投递、TrustService live mount、Operator agent bootstrap） | Phase 2 |
| wave-2 | Web 控制台与通知：客户管理、Operator 管理、Operation Timeline、紧急变更、审计查询五页 + 通知 PostgreSQL 存储 | Phase 3 |
| wave-3 | 环境与端到端验收：`make dev-up` 一键开发环境 + 分阶段 E2E 场景包全部通过，构成交付验收门 | Phase 4 |
| wave-4 | Operation 观测契约增量：TimelineEntry Kind（ACK/ROLLOUT_PROGRESS/ERROR）与 WorkloadObservation 上报契约 | Phase 5 |

E2E 阶段词汇（`test/e2e/stage.go` 的 canonical 顺序）：`control-plane`、`inventory`、`artifact`、`release`、`isolation`、`emergency`、`restart`。SDK Integration Gate（跨生产 seam 直连真实 kind API Server）**不属于** E2E Stage。

## 6. 已知资料缺口与需核实项

撰写本文时发现的、不影响上述结论但需 owner 处理的问题：

- `Requirements/REQ-002-micro-service.md` 正文称"release-api 服务已删除"，但 `cmd/api` 仍是可构建的 main 包并挂载 `AuditService`；二者需对齐（ADR-002 的原意是删除独立 REST **网关**，不是删除审计服务）。
- `Requirements/REQ-002-micro-service.md` 的持久化表述为"GORM — PostgreSQL（dev + prod 统一）"，与 `PROJECT-CONVENTIONS.md` 及 `CONTEXT.md`（dev=SQLite / prod=PostgreSQL，schema 必须双引擎兼容）冲突；本项目以**双引擎**约束为准。
- `Design/waves/wave-3-stage-e2e.md` 的"剩余阻塞面"称 `cmd/e2e`、`configs/e2e.dev.yaml` 尚不存在，当前代码仓中二者均已存在，该段已过期。
- `cmd/notification-sink/main.go` 的默认 `--config` 曾指向仓库根并不存在的 `configs/notification-sink.dev.yaml`；本次已改为实际被跟踪的 `deploy/kustomize/dev/configs/notification-sink.dev.yaml`（原先仅影响裸跑该服务）。 <!-- check-docs:ignore 该路径按定义不存在，本行在说明它被修正 -->
- `api/proto/auth/v1/auth.proto` 声明了 `ExternalIdentityService`，`cmd/auth` 仅在只读 procedure 白名单中引用其两个 RPC，未见对应 handler 挂载。
- Makefile 中 3 处 `./cmd/release-manager/` 引用与 `configs/manager.dev.yaml` 曾长期悬空（该命令在仓库中并不存在）；本次已把 `dev-stage-tenancy`、`dev-stage-config` 指向 release-orchestrator，把 `dev-stage-full` 改为导航目标，并删除孤儿配置。 <!-- check-docs:ignore 被删除的目录与配置，本行在记录其移除 -->

> 事实源：Notes/PROJECT-CONVENTIONS.md、Notes/CONTEXT.md、Notes/Docs-Inventory-2026-09-14.md、Notes/adr/ADR-001-control-plane-operator-outbound-boundary.md、Notes/adr/ADR-002-connect-protobuf-single-port-contract.md、Notes/adr/ADR-003-release-input-model-separation.md、Notes/adr/ADR-004-sdk-only-cluster-execution.md、Notes/adr/ADR-005-durable-command-outbox-and-operator-replay.md、Notes/adr/ADR-006-server-authoritative-organization-authorization.md、Notes/adr/ADR-009-operation-state-cas-and-transactional-outbox.md、Notes/adr/ADR-014-shared-postgresql-pool-and-transaction-seam.md、Notes/adr/ADR-015-maintenance-cutover-authority-boundary.md、Design/decisions/D-001-execution-boundaries.md、Design/decisions/D-005-operation-consistency.md、Design/decisions/D-010-platform-ops.md、Design/contracts/connect-surface.md、Design/waves/wave-0-contract-foundation.md、Design/waves/wave-1-core-release-increment.md、Design/waves/wave-2-web-console.md、Design/waves/wave-3-stage-e2e.md、Design/waves/wave-4-observation-contract.md、Requirements/REQ-002-micro-service.md

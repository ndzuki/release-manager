# Security Policy

## English summary

release-manager is a Helm release **control plane** for multi-customer Kubernetes clusters. Its
security model is boundary-first: the control plane never dials into a customer cluster, the
per-cluster Operator connects **outbound** over TLS 1.3 with a client certificate, cluster
execution happens only through Go SDKs (enforced by a static gate), and secret material only ever
travels as a `SecretRef` pointing at an object already inside the customer cluster.

- **Supported versions:** none. The repository has **0 git tags** and no release workflow, so only
  `main` (the HEAD of the branch you are reading) exists. `CHANGELOG.md` states the same fact and
  explains why its entries are month-based rather than version-based. See §2.
- **Reporting:** this repository has **no dedicated private disclosure channel**. GitHub
  private-vulnerability reporting is unavailable for it (open-source/free plan; the endpoint
  returns 404 for this repo), so reports must reach the maintainer (`ndzuki`) through contact
  information published on their GitHub profile — see §1.2 for the exact route and its gaps.
  **Do not open a public issue for a vulnerability.**
- **Known gaps** (not implemented today): management-plane HTTP has no TLS implementation wired up;
  the `NotifierService` face has no authentication interceptor and delivers caller-chosen URLs from
  inside the control plane (SSRF surface, §3.11); some audit rows are written by raw SQL that bypasses
  the sanitizing emitter (§3.6); the external-IdP service is declared but not mounted; the artifact
  vulnerability-admission seam is unwired; and there is no govulncheck / SBOM generation / image
  signing / attestation / dependency bot. Full list: §8.

---

## 0. 本文档的事实边界

本文只描述**仓库当前代码与配置的实际状态**，不重复需求语义（权威需求在知识库
`myNote/Projects/001-release-manager/`，见 `README.md:7`）。每条事实都带 `文件:行号`；无法从仓库得出的结论一律写成「**未见实现**」或标注「**建议**」。引用行号基于分支 `task/092-project-docs-align`。

状态词汇：**已实现** = 代码里存在且被挂载/被 CI 覆盖；**部分实现** = 代码存在但未被挂载或未被强制；**未见实现** = 在本仓库中找不到对应实现（不等于「生产环境没有」，部署侧可能由外部系统补齐）。

## 1. 报告漏洞

### 1.1 请勿为漏洞开公开 issue

任何会暴露边界、鉴权绕过、凭据格式或租户隔离缺陷的内容都不要写进 public issue、discussion、PR
review 或 commit message。本仓库是公开镜像（`https://github.com/ndzuki/release-manager`，GitCode 镜像见
`.github/workflows/sync-to-gitcode.yaml:22`），公开 issue 等同于 0-day 公告。

### 1.2 披露渠道现状：**无专用私密渠道**（GitHub 私有漏洞报告在本仓库不可用）

**结论**：本仓库当前**没有**专用私密披露渠道。GitHub 的 *Private vulnerability reporting*
对开源/免费计划下的仓库不可用，维护者已裁定不再把它算作渠道（实测：
`PUT /repos/ndzuki/release-manager/private_vulnerability_reporting` 返回 404，端点对该仓库不存在）。

**因此报告者可用的做法**（按优先级）：

1. 通过 GitHub 用户 `ndzuki` 个人页上公开的联系方式**私下**投递（该命名空间的依据见下方事实清单）。
2. 若一时找不到可用联系方式：先只投递一句「我发现一个边界缺陷，需要私密渠道」，
   **不要**在公开 issue、PR 或 commit message 里写任何细节（见 §1.1）。

这是一个**已知缺口**而不是可接受状态：它意味着报告能否送达完全取决于维护者是否在看 GitHub，
且没有任何投递回执。补齐动作见 §9 第 1 条。

- 仓库内**不存在**任何披露渠道配置文件：此前没有 SECURITY.md（本文即第一份）、
  没有 `.github/ISSUE_TEMPLATE/`、没有 `.github/CODEOWNERS`、没有 `.github/dependabot.yml`。 <!-- check-docs:ignore 这三项按定义不存在，本行在陈述其缺失 -->
  （`.github/` 下只有 `SECRETS.md` 与 `workflows/`）。
- `README.md` 全文不含邮箱、不含支持链接、不含徽章之外的联系方式。
- 可公开归属的身份只有：GitHub 命名空间 `ndzuki`（`go.mod:3` 的 module path
  `github.com/ndzuki/release-manager`、`git remote` 的 origin）、GitCode 命名空间 `ndmizuki`
  （`.github/workflows/sync-to-gitcode.yaml:9`）。GitCode 镜像侧同样没有私密报告入口。
- 提交者署名（`Nero Yang <281244945@qq.com>` 等）存在于 git 对象中，但那是提交元数据，
  **不是**已声明的披露渠道；本文不把它写成联系方式。

### 1.3 该缺口的影响与补齐建议（**建议**，非现状）

1. **无专用安全邮箱 / PGP**：投递依赖个人页信息，且无法验证报告者身份与消息完整性
   （**建议**：公开一个安全邮箱 + PGP 指纹，作为正式渠道写进本节）。
2. **无投递回执与时限**：没有确认/修复/披露 SLA，报告者无法判断是否送达
   （**建议**：写明「48 小时内确认、90 天内披露」，并说明是否接受非英语报告）。
3. **GitCode 镜像无对应机制**：镜像侧（`gitcode.com/ndmizuki/release-manager`）没有私密报告入口
   （**建议**：在镜像 README 指回本仓库的 §1.2）。
4. **建议**在报告时附上：受影响的服务二进制（`cmd/` 下的服务，清单见
   `docs/architecture.md:62-73`）、复现请求的 Connect procedure 名、以及是否跨越 Customer 边界。

## 2. 适用范围与版本支持

| 项 | 现状 | 证据 |
| --- | --- | --- |
| 已发布版本 | **无**。`git tag -l` 输出 0 行；CI 只有测试与镜像同步两条流水线，没有 release/publish job；也没有 `.goreleaser.*`。`CHANGELOG.md` 自身在「版本与发布策略」一节确认同一事实（无 tag、无版本注入、无 release 流水线） | `git tag -l`；`.github/workflows/test.yml:3-13`；`CHANGELOG.md:9-11` |
| 受支持分支 | 只有 `main`；`vars.RUNS_ON` 决定 runner，CI 触发为 `push: main` / `pull_request` / `workflow_dispatch` | `.github/workflows/test.yml:3-13` |
| 声明版本 | `web/package.json:4` 为 `0.1.0` 且 `private: true`；Go module 无版本标签 | `web/package.json:3-4`、`go.mod:1,3` |
| 制品分发 | 镜像不发布到任何 registry：operator 镜像只在 CI 里本地构建后做门禁校验 | `.github/workflows/test.yml:104-116` |
| 同步镜像 | `push: main` 时全量 `git push --mirror` 到 GitCode | `.github/workflows/sync-to-gitcode.yaml:3-6,25` |

因此：**不存在「受影响版本 / 不受影响版本」可对照的语义**，任何漏洞修复只能按 `main` HEAD 描述。
一旦开始发版，**建议**把本节改成带 LTS 窗口的版本支持表。

范围之外的内容（明确不接受报告也不承诺修复）：客户集群自身的 Kubernetes 发行版安全、
控制面所在基础设施（registry、反向代理、Vault、k3d/kind 宿主）、以及第三方 Chart 的内容
（Chart 由客户侧提供，见 §4）。

## 3. 架构与安全边界

### 3.1 信任边界：控制面 ↔ 客户集群

- 中心侧只保存 Customer、Cluster、Operator 身份、会话状态与待投递命令，**不持有客户集群的入站网络通道、
  长期 kubeconfig 或可跨客户复用的执行凭据**：`docs/architecture.md:12`；决策依据
  `docs/decisions/ADR-001-control-plane-operator-outbound-boundary.md:16-18,28`。
- 每个客户集群只运行一个 Operator，由集群侧**主动出站**通过 mTLS Connect 双向流连接中心：
  `docs/architecture.md:18`；URL 与 TLS 常量见 `cmd/operator/main.go:362`、
  `internal/operator/session_client.go:70-71`、`internal/operator/tls_clients.go:24-27,47-49`、
  `internal/operator/bootstrap/bootstrap.go:235-237`。
- 客户集群不可达时控制面只能排队或 fail closed，不能「绕过边界直接执行」：
  `docs/decisions/ADR-001-control-plane-operator-outbound-boundary.md:29`。

### 3.2 Operator 身份：证书即身份，且可撤销（已实现）

- 身份权威是证书 DER 摘要 `sha256(certDER)[:10]` 的 hex；用唯一索引让碰撞直接注册失败而不是让两个
  Operator 共用一张证书：`migrations/000013_operators_cert_serial_unique.up.sql:1-5`，
  决策 `docs/decisions/ADR-018-*`（标题即 "cert serial is the identity authority"）。
- 只有两条 procedure 强制客户端证书：`/operator.v1.OperatorService/CommandStream` 与
  `/operator.v1.OperatorService/RenewCertificate`，缺失即 401：
  `internal/operator/identity_handler.go:8-22`，且只接受 `VerifiedChains[0][0]`
  （`internal/operator/identity_handler.go:24-29`）。
- 网关面最小挂载：只注册 `OperatorService` 与**精确一条**
  `POST /orchestrator.v1.OrchestratorService/SyncInventory`（证书身份鉴权拦截器），其余 Orchestrator procedure 在该端口 404：
  `cmd/orchestrator/main.go:164-191`；服务端 `ClientAuth: tls.VerifyClientCertIfGiven` 与
  `MinVersion: tls.VersionTLS13` 见 `cmd/orchestrator/main.go:198-203`。
- 吊销后拒绝一切会话恢复与重连：`internal/operator/service.go:410-435,498-499,612`
  （`OperatorRevoked` → `permission_denied`）。
- 注册令牌只存不可逆 SHA-256，TTL 5–1440 分钟，一次性状态机 pending/used/revoked：
  `internal/orchestrator/enrollment.go:40-42,49-70`、`internal/orchestrator/enrollment.go:108-112`、
  `internal/store/store.go:715-718`、消费侧状态判定 `internal/operator/service.go:197-202,268,321-325`。
- CA 密钥来源可切（prod Vault / dev 文件，见 ADR-017）：接缝是 `Provider` 接口
  （`internal/operator/ca/provider.go:20-27`，注释明确「racing creators re-load instead of
  overwriting」），dev 实现把密钥写成 0600、证书 0644 且原子落盘
  （`internal/operator/ca/provider.go:54-61`、`internal/operator/ca/provider.go:79-90`）；选择逻辑在
  `internal/operator/ca/config.go:18-22`（`VaultPath` 非空即走
  `NewVaultProviderFromEnvironment`，`internal/operator/ca/vault.go:20-84`）。
  dev CA 必须是 **Ed25519 + PKCS#8**（`internal/operator/ca/ca.go:147-158`）、必须 `IsCA`、
  密钥与证书必须配对且自签通过（`internal/operator/ca/ca.go:160-180`）。

### 3.3 客户集群内的实际权限面（必须如实说明）

Operator 在集群内使用的不是 namespace 级 Role，而是 **ClusterRole**，并且**包含对全集群
Secrets/ConfigMaps 的读写**（Helm 3 把 release 状态存在 Secret 里，这是 Helm 官方推荐的 operator 权限面）：
`deploy/kustomize/customer-agent/base/rbac.yaml:2-24`（`secrets`,`configmaps` →
`get,list,watch,create,update,patch,delete`；`services`/工作负载同样全动词；`namespaces` → `get,list,create`；
`horizontalpodautoscalers` 只读），绑定见 `deploy/kustomize/customer-agent/base/rbac.yaml:26-37`
（ServiceAccount `release-manager-operator`，命名空间 `release-manager-customer`）。

含义（事实推论）：**控制面凭据或 operator 身份一旦被拿下，攻击者在该客户集群内可读取全部 Secret 并
创建/删除工作负载**。缓解现状：镜像为 distroless `nonroot:nonroot` 且基础镜像按 digest 锁定
（`deploy/docker/Dockerfile.operator:10-11`），preflight 探针 Pod 强制
`runAsNonRoot`/`readOnlyRootFilesystem`/`allowPrivilegeEscalation=false`
（`internal/operator/preflight/pod_builder.go:60-62,85,105-107`）。
**建议**：把 ClusterRole 收成按 namespace 的 Role 或加 resourceNames 限制，并在
`deploy/kustomize/customer-agent/base/deployment.yaml:21-22`（当前只设 `fsGroup`）补
`runAsNonRoot`、`readOnlyRootFilesystem`、`allowPrivilegeEscalation: false`、`drop: [ALL]`。

### 3.4 只读白名单与维护期写入冻结（已实现）

Connect 的读写都走 POST，因此按 procedure 名做白名单而不是按 HTTP method：
`internal/app/maintenance.go:11-28`（维护期外透传；维护期内不在白名单一律 `CodeUnavailable("maintenance")`）。

- auth 面只读集合 `authReadOnlyProcedures`：`cmd/auth/main.go:215-230`（引用点 `cmd/auth/main.go:180,185`）。
- orchestrator 面只读集合 `orchestratorReadOnlyProcedures`：`cmd/orchestrator/main.go:688-706`。
- trust 面只读集合 `trustReadOnlyProcedures`：**只有** `GetTrustPolicy`：
  `cmd/orchestrator/main.go:708-712`（挂载时传入 `cmd/orchestrator/main.go:512-519`）。
- 双引擎迁移（SQLite→PostgreSQL）被刻意限制在维护窗口内，禁止在线双写与自动回退：
  `docs/decisions/ADR-015-maintenance-cutover-authority-boundary.md:16-18,22-24`，切换后单一权威见
  `docs/architecture.md:144-149`。

### 3.5 Secrets 进入执行链的方式：只允许引用（已实现）

- Values 文档校验在入库前拒绝字面机密：`internal/values/values.go:60-77`
  （`ValidateWithRefs` → `ErrSecretLiteral`（定义 `internal/values/values.go:18`）/
  `ErrInvalidSecretRef`（`internal/values/values.go:19`）），SecretRef 数量上限 64
  （常量 `internal/values/values.go:22`，强制点 `internal/values/values.go:89-90`），并且 SecretRef 指向的 values 路径**必须存在且为 null**
  （`internal/values/values.go:88-106`，另含 DNS1123 名字与 key 长度校验）。
- 字面机密特征库（key 名正则 + 值形态：`AKIA…`、`BEGIN … PRIVATE KEY`、`scheme://user:pass@host`、
  高熵 hex/base64 ≥32）：`internal/values/secret.go:10-17`；双通道检测说明见 `internal/values/secret.go:19-21`；生产可追加正则
  `values.secret_patterns`（dev 默认为空：`configs/orchestrator.dev.yaml:12`；env 注入
  `VALUES_SECRET_PATTERNS`：`internal/config/config.go:295`）。
- 执行侧只在客户集群内解析引用，并绑定 `uid` / `resourceVersion` / 值指纹三重漂移检测，任一变化即
  `ErrSecretRefChanged` 拒绝执行：`internal/operator/k8s/secrets.go:27-58`、调用点
  `internal/operator/agent/agent.go:955`、错误语义 `internal/operator/helmengine/engine.go:79`、
  分支处理 `internal/operator/agent/agent.go:1229`。
- 设计依据与禁止项（中心不存明文/不解密、不允许原地编辑已批准 revision）：
  `docs/decisions/ADR-007-immutable-values-and-secret-reference-boundary.md:16-18,22-23`。
- 异人审批：创建者可以提交自己的 ValuesRevision，但**不能批准自己的**：
  `internal/orchestrator/values_approval.go:286-292`（`self_approval_forbidden`）。

### 3.6 审计与脱敏（部分实现，见状态标注）

- 脱敏规则中心：`internal/redact/sanitize.go:19-34`（字段名集合 + 值正则）、`:39-51`（Sanitize）、
  `:54-71`（`IsSensitiveField`/`Sensitive`）、`:76-88`（Truncate）；audit 包只是它的稳定转发层
  （`internal/audit/sanitize.go:12-25`）。
- 强制点在 emitter 边界：`Emit` 先 `Normalize`（`internal/audit/emitter.go:69-73`），`Normalize` 会
  拷贝事件并对 `ChangeSummary` 与递归 `Metadata` 脱敏（`internal/audit/normalize.go:13-26,57-64`）；
  spool 重放同样先 `Normalize`（`internal/audit/spool.go:54`）。全仓库对 `AuditEvents().CreateBatch`
  的调用只有这两处（`internal/audit/emitter.go:160`、`internal/audit/spool.go:71`）。
- **状态：部分实现（存在绕过 emitter 的审计直写）**。Operator 管理与审计导出两条链路在写事务内用裸 SQL
  直接插入 `audit_events`，不经过 `Normalize`：
  `internal/store/sqlite/operator_management.go:472-478`（同一文件另有 7 处调用点，如
  `internal/store/sqlite/operator_management.go:104,134,161,187,393,440`）、
  PostgreSQL 等价实现 `internal/store/postgres/operator_management.go:446`，
  以及导出事件 `internal/store/sqlite/audit_exports.go:57`（PG `internal/store/postgres/audit_exports.go:55`，
  由 `internal/audit/audit_service_handler.go:142-153` 触发）。
  这违反 `AGENTS.md:27`（硬约束 6「不要绕过 emitter 直接写审计表」）。
  已核实的缓解：这些事件的 payload 由服务端固定 key 构造——`internal/orchestrator/operator.go:321-352`
  把 `ChangeSummary` 写成常量、`internal/orchestrator/operator.go:139-144` 只记录
  `reason_present`/`reason_length` 而不记录 reason 原文；导出事件的 `ActorKind` 恒为 system 且无 metadata。
  另外归档写盘前会**二次脱敏**（`internal/audit/archiver.go:80` →
  `internal/audit/sanitize.go:29-42`），查询投影也不返回 `change_summary`/`metadata`
  （`internal/audit/audit_service_handler.go:164-174`）。因此目前**未见明文泄露证据**，但「所有审计写入都过脱敏」
  并非结构性保证。**建议**：把直写改为经过 `Normalize`，或在 store 层再兜一道。
- **状态：已实现（审计租户边界与角色判定由服务端强制；TASK-095 组织域 + TASK-103/ADR-021 角色判定）**。
  release-api 不内嵌 Casbin、不读 release-auth 的库；它把调用方自己的 Bearer 透传给 release-auth 的
  `AuthorizeAccess`（`internal/audit/decision.go:44-76`，200ms 超时），由对方按持久 membership + 版本化 policy
  裁决，再按返回的有效组织/窗口执行（`internal/audit/authorization.go:41-105`）。组织越权 → `permission_denied`，
  窗口超限 → `invalid_argument` + `range_too_large`，判定不可用 → `unavailable`（fail closed，不回落本地角色猜测）。
  `Emit` 另拒收 actor 组织与有效组织不一致的事件（`internal/audit/audit_service_handler.go:54-61`）。
- 落库前的最后一道：操作时间线的错误文本同样脱敏（`internal/store/store.go:2616-2617`）。
- 响应侧错误脱敏：`CodeInternal` 一律泛化为 `internal error`，`CodeUnavailable` 的 `%w` 链若不含已知
  稳定业务 sentinel 也降级为 internal，完整细节只写服务端日志：
  `internal/contracts/interceptor/errorsanitize.go:22-36,68-92,169-175`。
- **状态：未见实现（应用日志面）**。`slog` handler 为裸 JSON，无 `ReplaceAttr`：
  `internal/app/app.go:123`。因此 `logger.Info(...)` 直接打印的字段、以及上面那句「细节只写服务端日志」
  的内容**不经过脱敏管道**。CI 侧的 `kubectl logs` 抓取也是原文：
  `test/e2e/prerequisite/capture-logs.sh:30`，产物上传见 `.github/workflows/test.yml:359-365,435-442`。
- 设计依据：`docs/decisions/ADR-010-independent-sanitized-audit-pipeline.md:16-18,29`。

### 3.7 SDK-only 执行边界与静态门禁（已实现）

- 禁止执行子进程的规则：`internal/quality/sdkcheck/analyzer.go:42-46`（5 条规则）；禁用二进制清单
  `helm/kubectl/istioctl/argocd/flux/terraform/tofu`：`internal/quality/sdkcheck/analyzer.go:28-36`。
- 例外必须带 owner/reason/expires_at/path/rule：`internal/quality/sdkcheck/analyzer.go:51-58`；
  当前 `sdkcheck.exceptions.yaml` 共 **2 条**（`deploy/dev/dev_test.go`、`cmd/e2e/main_test.go`，
  均为 `os_exec_import`，`expires_at: 2099-12-31`）。
- 门禁命令与 CI：`Makefile:413-415`（`make sdk-check`）、`.github/workflows/test.yml:37-49`（`sdk-check` job），
  规则说明 `docs/testing.md:208-212`。
- 运行镜像侧二次校验：operator 镜像必须以 distroless 摘要基底、入口点固定、不得含
  `helm/kubectl/helmsman/kubectl-plugin`，且 **`/bin/sh` 必须不存在**：
  `imagecheck.operator.yaml:2-5,14,18-23`（CI job `.github/workflows/test.yml:104-116`）。
- 设计依据（禁止 CLI 与 shell wrapper，也禁止中心侧直接执行 SDK）：
  `docs/decisions/ADR-004-sdk-only-cluster-execution.md:16-18,24`。

### 3.8 管理面认证与授权

- 统一拦截链：JWT/Cookie → 组织域解析 → Cookie 写操作 CSRF 校验 → Casbin `Enforce` → 用户必须
  `UserActive` → 必须有活跃会话（store 出错即失败，不放行）：
  `internal/auth/interceptor.go:26-136`（CSRF 判定 `:80-86`，用户激活与会话 fail-closed `:111-122`）。
- 口令为 bcrypt cost 12：`internal/auth/password.go:9-19`；登录限流为**进程内** map
  （`internal/auth/ratelimit.go:18-54`），多副本部署时不共享（**建议**改用 Redis 或边缘限流）。
- 浏览器会话 Cookie：三个 cookie 一律 `SameSite=Strict`；access/refresh 为 `HttpOnly=true`，
  CSRF cookie 故意不带 `HttpOnly`（前端要读它回传 header）：
  `internal/auth/browser_session.go:137-139,215-216`。`Secure` 位取 `s.browser.SecureCookies`，
  默认值为 **true**（`internal/auth/service.go:43-44`），而 `cmd/auth/main.go:189` 构造
  `NewAuthService` 时没有传入该配置 → 生产路径恒为 Secure；仓库内唯一显式传 `false` 的地方是测试
  （`internal/auth/browser_session_test.go:33`）。代码里的 gosec 豁免注释把这一点写成
  「environment-configurable」（`internal/auth/browser_session.go:214,219`），但实际上没有 env/config
  通道——因此**浏览器路径要求入口是 HTTPS**，而本仓库的 dev 入口是明文 `listen 8087`
  （`web/nginx.conf:6-8`，见 §3.8 末条与 §7）。E2E 不依赖浏览器 cookie，它用服务端 client 直连
  （`test/e2e/clients.go:24-28,132-136`）。
- 会话存储以 PostgreSQL/SQLite 为权威、Redis 只做缓存与吊销黑名单，且**Redis 发布失败时立刻撤销持久会话族**：
  `internal/store/redis/adapter.go:31-58,110-121`（挂载点 `cmd/auth/main.go:132`，未配 Redis 时该层缺席）。
- RBAC 角色只有 4 个：`platform_admin` / `release_admin` / `deployer` / `viewer`
  （`internal/store/store.go:807-811`，校验 `internal/store/store.go:814-820`，
  且 `release_admin` 不能授予 `platform_admin`：`internal/store/store.go:824-833`）。
  模型是代码内联的 4 元组 `sub, dom, obj, act`（`internal/auth/casbin.go:20-35`，
  matcher 用 `keyMatch`）；role→policy 在代码里编译、无外置策略文件：
  `internal/auth/casbin.go:424-477`（`platform_admin` = `*,*` 通配在 `:429`），
  治理动作（紧急变更、Values 创建/审批）由
  `internal/auth/authorization_snapshot.go:248-268` 与
  `internal/auth/authorization_snapshot.go:235-246` 追加。
- **Casbin 自身也是 fail closed**：`Enforce` 在字段为空时返回 `invalid_actor_context`
  （`internal/auth/casbin.go:70-72`），策略快照不健康时返回 `policy_unavailable`
  （`internal/auth/casbin.go:84-86`，健康位由 `internal/auth/casbin.go:132-136,169,175` 的热重载维护）；
  procedure 未登记时直接拒绝（`internal/auth/interceptor.go:68-79`）。
  映射不再是前缀推断，而是 `internal/auth/procedure_policy.go:66-185` 的**逐 procedure 显式登记表**：
  新增 RPC 必须加一行，`TestProcedurePolicyRegistryIsExhaustive`（`internal/auth/procedure_policy_test.go:90`）
  会在漏配时失败，且 `TestProcedurePolicyPairsAreGranted`（`:121`）要求每个 Casbin 对的
  `(object, action)` 至少被一个非通配角色授予（否则必须显式标 `adminOnly`）——这正是 TASK-095 之前
  5 条 procedure 恒 403 的根因。
  7 条 procedure 走处理器自裁决、跳过 Casbin（`modeHandler`，`internal/auth/procedure_policy.go`）。
  策略持久化在 `casbin_rule` 表：`migrations/000011_authorization_persistence.up.sql:22-32`。
- 服务间凭据：`BundleService.SubmitBundle` 接受 service token（SHA-256 摘要 +
  `subtle.ConstantTimeCompare`）或 JWT，actor 固定为 `service:release-webhook`：
  `cmd/orchestrator/main.go:477-492`、`internal/auth/service_token.go:108-126`。
- **状态：未见实现（管理面 TLS）**。`internal/app/app.go:71,190-201` 只有 `TLSCertificateFiles`
  接缝声明与调用，全仓库没有任何实现，因此 6 个服务的 HTTP 面在生产必须由外部反向代理终结 TLS。
- **状态：部分实现（外部 IdP）**。`ExternalIdentityService`（LDAP/OIDC/DingTalk）在契约里声明
  （`api/proto/auth/v1/auth.proto:655-680`，proto 注释自己就写了 "No handler is mounted"）、在 Go 侧有实现
  （`internal/auth/external_idp_service.go:325` 接口断言）、也被 E2E 客户端引用（`test/e2e/clients.go:26,134`），
  但 `cmd/auth` 只挂载 Auth/Organization/Binding/Authorization 四个服务
  （`cmd/auth/main.go:189-203`），**未挂载该服务**；然而它的两个 URL 构造 RPC 却已写进维护期只读白名单
  （`cmd/auth/main.go:227-228`）。该缺口同时被 `docs/architecture.md:80,171` 记录。
- **状态：未见实现（HTTP 安全响应头）**。浏览器与 API 走**同一 origin**：`web/nginx.conf` 是唯一前端入口，
  以 same-origin 反向代理把五个 Connect procedure 前缀转发到集群内服务
  （`web/nginx.conf:1-5,17-18,28-29`，请求体上限 `client_max_body_size 10m` 在 `web/nginx.conf:13`），
  因此**不存在 CORS 跨源调用面**（Go 侧无 CORS 处理是设计结果，不是遗漏）。但该文件全篇没有一处
  `add_header`：`X-Content-Type-Options`、`Content-Security-Policy`、`X-Frame-Options`、
  `Strict-Transport-Security` 均未设置，且入口 `listen 8087` 为明文 HTTP、TLS 不在这一层
  （`web/nginx.conf:6-8`）。**建议**在真正的对外边界上补齐这些头与 TLS。
- **注意（不要把 web 入口当成「只暴露已鉴权面」）**：`web/nginx.conf:50` 也把 `/operator.v1.`
  代理到 `operator:8084`，而 gateway 模式的 operator 自身 mux 只挂了 request-id 与错误脱敏拦截器
  （`cmd/operator/main.go:259-266`，无 JWT/Casbin）——`OperatorService` 由令牌/证书/会话自证身份，
  不属于浏览器授权面。该模式在 `docs/architecture.md:66` 被标注为「TASK-065 待移除路径」，
  且 dev 清单里没有它的 Deployment（`deploy/kustomize/services/` 无 `operator.yaml`）。

### 3.9 制品信任与供应链边界

- Bundle 验签是**真实** Ed25519 验签（对活跃 trust root 逐条 `ed25519.Verify`），并带 5s 上界：
  `internal/trust/ed25519.go:21,44-145`；verifier 为 nil、根不可用或超时都产出
  `VerificationUnavailable` 而不是放行（同文件的 `unavailableOutput` 路径）。
- 两道闸门的强度不同，必须分开说：**preflight 无条件 fail closed**
  （`internal/preflight/service.go:145-169`：`Trusted`/`PolicyWarning` 放行，缺失/不可用/无效一律拒），
  而 **`SubmitReleaseBundle` 的闸门跟随 trust policy 的 `FailClosed` 位**
  （`internal/orchestrator/service.go:258-305`：`VerificationRejected` 恒拒；
  `SignatureMissing`/`VerificationUnavailable`/其他状态仅在 `FailClosed=true` 时拒，否则降级为
  `PolicyWarning` 放行）。默认策略里**只有 `env == "production"` 才 `FailClosed: true`**，
  staging/development 默认 fail-open：`internal/trust/policy.go:8-28`；
  紧急变更走同一条信任判定：`internal/orchestrator/emergency.go:662`。
  剩余风险：**目标环境标签由服务端配置决定**（`internal/orchestrator/service.go:268` 使用 `s.targetEnv`），
  非 production 标签下的未签名/不可验证制品会被放行到执行链（只留 `policy_warning` 审计）。
- 根轮换四态：`RotateTrustRoot` / `EndGrace` / `RetireTrustRoot` / `RevokeTrustRoot`
  （`internal/trust/service.go:84,157,210,260`）。
- **状态：部分实现（遗留验签器）**。`internal/trust/verifier.go:216-219` 只做格式检查，注释明确写
  「实际密码学校验交给 `CosignVerifier`」，而 `CosignVerifier` 在本仓库不存在（全仓仅此一处注释引用）。
  当前被挂载的是 §3.9 首条的 `Ed25519Verifier`（`cmd/orchestrator/main.go:353`）。
- **状态：未见实现（漏洞准入）**。`internal/vulnerability/` 只有 `NoopScanner`（恒返回 0 findings，
  `internal/vulnerability/scanner.go:5-18`），且 orchestrator 侧的
  `Service.EvaluateArtifactVulnerability` 既无调用者（全仓仅定义处出现），又在 evaluator 为 nil 时
  **直接返回 nil（放行）**：`internal/orchestrator/vulnerability.go:11-23`。因此包文档
  `internal/vulnerability/doc.go:10-11` 声称的「production default: fail closed」目前**未生效**。

### 3.10 数据层不变量与双引擎差异（部分实现）

安全相关的不变量确实落到了数据库约束，且**大多两侧等价**：

- Operator 证书序列唯一（防 80-bit DER 碰撞串号）：PostgreSQL
  `migrations/000013_operators_cert_serial_unique.up.sql:1-5`；SQLite 等价索引
  `internal/store/sqlite/db.go:1221`。
- 注册令牌**明文列已被删除**（不再存在可存明文的字段）：PostgreSQL
  `migrations/000023_operator_cert_lifecycle.up.sql:4-11`；SQLite 通过表重建去掉旧 `token` 列并把值当作
  hash 迁移：`internal/store/sqlite/db.go:826-831`；查询只按 hash：`internal/store/sqlite/operators.go:63-70`。
- 每个 Cluster 只允许一个 pending 令牌、只允许一个 active Operator：
  `internal/store/sqlite/db.go:1223-1225`、`migrations/000014_operator_management.up.sql:16-18`。
- 每个 Operator 只允许一个在线会话：`migrations/000001_legacy_baseline.up.sql:127`、
  `internal/store/sqlite/db.go:1249`。
- SQLite 显式开启外键：`internal/store/sqlite/db.go:86`；PostgreSQL 迁移是唯一 schema 权威、
  禁止 AutoMigrate：`docs/architecture.md:125`。

**未见实现 / 有偏差的三点：**

1. 「同一 ReleaseDefinition 只允许一个活跃标准 Operation」的**数据库级**部分唯一索引只存在于 PostgreSQL
   （`migrations/000001_legacy_baseline.up.sql:62-63`）；SQLite 侧没有该索引，两侧都靠应用层计数
   （`internal/store/sqlite/uow.go:89-100`、`internal/store/postgres/uow.go:84-95`）。
2. 迁移编号连续性与 up/down 成对只在**运行时**校验
   （`internal/postgres/migrate.go:53` 调用 `internal/postgres/migrate.go:205-212`），
   Makefile 与 CI 没有独立的迁移编号门禁（`grep -n "migrate" Makefile` 无该类 target）。
3. 没有使用 PostgreSQL RLS / `CREATE POLICY` / `GRANT` 做行级隔离（全仓检索 0 命中）；
   租户隔离完全依赖应用层的 `organization_id`/`customer_id` 过滤。

### 3.11 通知出站（部分实现，风险面明确）

- `NotifierService` 挂载时只带 request-id 与错误脱敏两个拦截器，**没有** JWT/Casbin 裁决：
  `cmd/notifier/main.go:71-78`（对比 `cmd/auth/main.go:181-187`、`cmd/orchestrator/main.go:512-519`）。
  同时同源入口把 `/notifier.v1.` 直接代理到 `notifier:8086`（`web/nginx.conf:61-62`），
  所以「能访问 web 入口」就等价于「能创建通知任务」；契约只有 `Send` 与 `GetStatus`
  两个 RPC（`api/proto/notifier/v1/notifier.proto:72,81,87`）。
- 投递目标与 payload 全部来自请求，且没有目标地址白名单：`internal/notifier/service.go:36-47`
  直接落 `Recipient`/`Metadata`，`internal/notifier/webhook.go:80-92` 把 `job.Metadata` 原样
  JSON POST 到 `job.Recipient`（在 `internal/notifier/` 内检索 `url.Parse`、allowlist、
  `127.0.0.1`、`169.254`、`localhost` 均 0 命中）。这意味着**控制面本身成为一个由调用方选择目标 URL 的
  HTTP 出站源**（SSRF 面），且出站 metadata 不经过 §3.6 的脱敏管道。
- **状态：未见实现（ADR-020 的 Vault 适配器）**。`docs/decisions/ADR-020-use-hashicorp-vault-go-api-for-notifier-secretresolver.md:3,14-15`
  已 accepted，要求用 Vault KV v2 + Kubernetes auth 实现 notifier 的 production SecretResolver 适配器，
  并规定「未配置 resolver 时投递**故意**保持无鉴权」（同文件 `:15`）；
  本仓库只有接缝与选项（`internal/notifier/delivery.go:16-21`、`internal/notifier/webhook.go:37-45`），
  构造点是 `sender := notifier.NewWebhookSender(nil)`（`cmd/notifier/main.go:81`）→
  适配器从未接线，因此现状正好落在 ADR 允许的「无鉴权」分支里，缺的是 production 路径本身
  （全仓唯一的 vault 客户端在 CA 侧，`internal/operator/ca/vault.go`）。
- **建议**（均未见实现）：给出站加目标白名单与私网/link-local 屏蔽（含 `169.254.169.254`）、
  把 metadata 过一遍 `redact.Sanitize`、并落实或显式撤回 ADR-020。

## 4. 威胁模型摘要

下表只列「攻击者可控的东西 → 本仓库里的控制点 → 剩余风险/状态」。状态含义见 §0。

| 攻击者可控 | 控制点（证据） | 剩余风险与状态 |
| --- | --- | --- |
| 客户集群内的任意工作负载与 API（含被攻陷的租户） | Operator 只出站、TLS1.3、身份=证书序列（`docs/architecture.md:18`；`internal/operator/tls_clients.go:24-27`；`migrations/000013_*.up.sql:1-5`）；控制面不存 kubeconfig（`docs/architecture.md:12`） | 剩余：集群内 ClusterRole 可读写全集群 Secret（§3.3）。**已实现**边界，但爆炸半径=该客户集群 |
| 单个 Customer 下的账号（低权用户） | 域绑定 + Casbin 逐 procedure 裁决（`internal/auth/interceptor.go:98-109`；`internal/auth/casbin.go:426-475`）；`viewer`/`deployer` 无 operator 写权限 | 剩余：`platform_admin` 为 `*,*` 通配（`internal/auth/casbin.go:429`），控制面内无二次制衡；异人审批只管 Values 批准（§3.5）。**已实现** |
| 已批准 ValuesRevision 被事后篡改 | 不可变 revision + `self_approval_forbidden`（`internal/orchestrator/values_approval.go:286-292`）+ 幂等键唯一（`migrations/000001_legacy_baseline.up.sql:46`）+ 「同一 ReleaseDefinition 只允许一个活跃标准 Operation」 | 剩余：**该互斥在两个引擎上强度不同**——PostgreSQL 有数据库级部分唯一索引（`migrations/000001_legacy_baseline.up.sql:62-63`），SQLite 侧**没有**对应索引（`internal/store/sqlite/db.go` 内无 `one_active_standard`），只靠应用层计数检查（`internal/store/sqlite/uow.go:89-100` 与 `internal/store/postgres/uow.go:84-95` 同一 SQL）。因此 dev/test 掩盖不了竞态，但**生产强度高于 dev 验证强度**，与 `AGENTS.md:25` 的双引擎等价要求存在偏差。**部分实现** |
| 注册令牌泄露 | 只存 SHA-256、短 TTL、一次性（§3.2） | 剩余：令牌在 `data/dev-enrollment-tokens/` 明文落盘（仅 dev，`.gitignore:35`）。**已实现**（生产不落盘） |
| Webhook 请求体（Harbor 等外部制品源） | **TASK-102 已补入站认证**：`SubmitReleaseBundle` 需 CI API key（`ServiceTokenInterceptor` 收窄到该 procedure），`POST /webhooks/harbor` 需独立的 Harbor key（`internal/webhook/token_auth.go` 的 `RequireToken`），两把 key 与两条 procedure 不可互相替换（AC-011-04/16/17，`internal/auth/service_token_routing_test.go`）；转发时原样复制 `Signature`/`Sbom`/`Provenance` 并保留 `Idempotency-Key`，出站另用 webhook/Harbor service token（§3.3）；`BundleService` 侧再验一遍 service token scope | 剩余：`signature`/`sbom`/`provenance` 是请求方可填的 `ArtifactReference`（`api/proto/webhook/v1/webhook.proto:28-30`），信任判定发生在 preflight/trust（§3.9），因此**持合法 CI key 的调用方**仍可造成 bundle 记录污染；非 production 标签下还能被降级为 `policy_warning` 放行。**部分实现** |
| 单条 procedure 被塞进非法输入 | 契约生成物唯一入口 `api/gen/**`、`web/src/gen/**`（禁止手改，`AGENTS.md:24`）；错误码泛化（§3.6） | 剩余：SQL 拼接只出现在编译期列名/占位符白名单，值全部参数化（`internal/store/postgres/commands.go:124-132`、`internal/store/sqlite/inventory.go:230-245`、`internal/store/postgres/inventory.go:239-245`、审计 where 构造 `internal/store/sqlite/audit.go:210-236`）；迁移工具的标识符插值带引号转义（`internal/migration/copy.go:470-480`）。**已实现** |
| 能访问 web 同源入口的任何调用方 | 入口把五个 Connect 前缀反向代理到集群内服务（`web/nginx.conf:17,28,39,50,61`），auth/orchestrator/trust 面各有 JWT+Casbin（§3.8） | 剩余：**`/notifier.v1.` 与 `/operator.v1.` 两条前缀后面没有 JWT/Casbin**（`cmd/notifier/main.go:71-78`、`cmd/operator/main.go:259-266`）；`/notifier.v1./Send` 可把控制面变成任意 URL 的 HTTP 出站源（§3.11）。**未见实现（该面的认证）** |
| 数据库快照（离线读到 audit / values / 令牌） | audit 文本入库前脱敏（§3.6）、口令 bcrypt（`internal/auth/password.go:9-19`）、令牌只存摘要（§3.2）、Values 无机密字面（§3.5） | 剩余：应用日志面不过脱敏（§3.6），CI artifact 含原始容器日志（`test/e2e/prerequisite/capture-logs.sh:30`）。**未见实现（日志面）** |
| CI 凭据（ Actions runner / 镜像同步） | 全 workflow `permissions` 审计见 `.github/SECRETS.md` 第 8 节；runner 通过 `vars.RUNS_ON` 间接化（`.github/workflows/test.yml:24-34`）；kind/k3d 下载都校 sha256/checksum（`.github/workflows/test.yml:295-309,345-348,413-416`） | 剩余：actions 全部按 tag 引用、**0 处 SHA 固定**；`sync-to-gitcode.yaml` 无 `permissions:` 块（`run-name`→job `:11` 直接 `git push --mirror`，`:25`）。**建议**收紧 |
| 恶意/失序依赖（Go module、npm、基础镜像） | 许可门禁（§6）+ distroless 摘要基底（`deploy/docker/Dockerfile.operator:10`）+ 镜像内容门禁（`imagecheck.operator.yaml:14,18-23`） | 剩余：无漏洞扫描、无 SBOM 生成、无签名/attestation（§6）。**未见实现** |
| 运维在事故期绕过流程 | 紧急变更 kill switch（关闭时最高优先级拒绝，配置缺失 fail closed 到 false）+ 客户停用拒绝 + workload 身份先校验后落库 + 授权快照缺失即 `authorization_snapshot_stale`：`internal/orchestrator/emergency.go:77-150`（kill switch `internal/orchestrator/emergency.go:96-110`、客户停用 `internal/orchestrator/emergency.go:120-131`、身份 `internal/orchestrator/emergency.go:137-145`、快照 `internal/orchestrator/emergency.go:146-150`） | 剩余：紧急变更仍需真实操作者身份与审计，但**它天然绕开 Values 审批**；卡死锁由后台扫描器发现（`cmd/orchestrator/main.go:780-797`、过期任务 `internal/orchestrator/emergency.go:297`）。**已实现** |

依据：`docs/decisions/ADR-011-controlled-emergency-change-and-convergence.md:16-18,22-23`（禁止任意 JSON Patch、紧急变更必须进 Operation）。

## 5. 刻意接受的风险与例外机制（真实数量）

| 例外清单 | 当前条目数 | 证据 | 审阅机制 |
| --- | --- | --- | --- |
| `sdkcheck.exceptions.yaml`（SDK-only 静态门禁豁免） | **2** | 两个测试文件、规则 `os_exec_import`、`expires_at: 2099-12-31` | 字段强制 owner/reason/expires_at/path/rule（`internal/quality/sdkcheck/analyzer.go:51-58`）；过期即失效，且 `make sdk-check` 每次全量跑（`Makefile:413-415`） |
| `license-exceptions.tsv`（许可豁免） | **0**（`license-exceptions.tsv` 10 行全部是 `#` 注释，无数据行） | `license-exceptions.tsv:1-10` | 允许把 `DENIED`/`MISSING` 降级为 `EXEMPT`（`scripts/check-licenses.sh:277-279`，登记格式 `:170-182`）；`CONTRIBUTING.md:70` 要求「登记会被评审」且禁止放宽默认集合 |
| `install-sdk.quarantine.yaml` / `upgrade-sdk.quarantine.yaml` | **0**（各 `exceptions: []`） | 两文件 | 隔离策略有时限（`cmd/installgate` 用途见 `docs/architecture.md:73`） |
| `//nolint` 抑制 | **320 处 / 131 个文件**（非测试 292/114；测试 28/17） | 统计命令：`grep -rn "//nolint" --include='*.go' .`；按 linter：errcheck 127、gocyclo 74、**gosec 65**、dupl 30、unparam 9、contextcheck 6、staticcheck 4、revive 3、nilerr 1、forbidigo 1 | 每条 `//nolint` 都带解释性注释（评审要求见 `CONTRIBUTING.md:73-79`）；另有 1 处 `#nosec G202`：`internal/store/sqlite/inventory_query.go:99` |
| golangci-lint 规则面 | **20** 个 linter（`enable:` 列表 `8-28` 行），含 `gosec`（`.golangci.yml:15`） | `.golangci.yml:8-28` | gosec 只排除 `G104`（`.golangci.yml:44-46`）；`_test.go` 豁免 dupl/gocyclo/gosec（`:65-69`）；生成码整体豁免（`:70-72`）；无 `issues.exclude` 白名单 |
| CI 增量 lint | `--new-from-rev` | `.github/workflows/test.yml:196-200` | 存量问题不阻塞 PR（**建议**：安全类 linter 走全量） |

其他**明确接受**的现状（不是缺陷，是范围选择）：

- dev 环境用固定弱凭据：`deploy/kustomize/base/secret.yaml:15-17` 提交了 dev-only PostgreSQL
  用户/口令字面量（文件头 `:7-11` 声明仅 dev），同一常量内联在 `deploy/dev/dev.sh:1739,1834,1836`；
  `http-client.env.json:7-8,29-30` 是占位口令与 `dev-key`/`test-key` 假 API key（`:16` 为假摘要）。
  CI 的 `DEV_PROFILE=ci` 永不把这些写进磁盘（凭据必须由 env 注入，见
  `.github/SECRETS.md` 第 5、7 节；校验实现 `internal/devfixture/files.go:154-168,199-226`）。
  **建议**：给这些文件加「仅 dev」的自动化断言，而不是只靠注释。
- 前端路由守卫与按钮隐藏**不构成**安全边界，服务端才是权威：`docs/architecture.md:41-44`。

## 6. 依赖与供应链

**已有（可验证）：**

- 许可门禁：`scripts/check-licenses.sh`（422 行）— 允许 15 个 permissive SPDX
  （`scripts/check-licenses.sh:62`），显式拒绝 `*GPL*`/`*SSPL*`/`*BUSL*`/`*lastic*`
  （`:64-69`），无 LICENSE 文件即 FAIL；`NOTICE` 新鲜度校验 `:344-351`。
- 命令与 CI：`Makefile:486-488`（`make check-licenses`，并纳入 `quality` 聚合 target `Makefile:521-522`）、
  `.github/workflows/test.yml:50-68`（`license-check` job）。
- 生成物：`docs/dependencies.md`（244 行，摘要表 `:10-14`：Go 构建闭包 158/158 通过、前端运行时 50/50 通过、
  0 例外、0 无许可证）；`NOTICE`（149 行，含 9 个上游 NOTICE 段落）。
- 政策：`AGENTS.md:28`（硬性约束 7「依赖许可」）、`CONTRIBUTING.md:63-71`（inbound=outbound=Apache-2.0）。

**没有（逐条核实，避免读者误以为有）：**

| 能力 | 现状 | 核实方式 |
| --- | --- | --- |
| `govulncheck` / 任何 Go 漏洞扫描 | **无** | 全仓 `grep -i govulncheck` 0 命中；Makefile 与两个 workflow 均无该 job |
| SBOM 生成（syft/buildx/provenance） | **无** | 无任何 Dockerfile/Makefile 步骤产出 SBOM |
| 镜像签名（cosign）/SLSA attestation | **无** | 全仓 `cosign` 仅出现在 `internal/trust/verifier.go:217` 的注释里；无 attestation 生成 |
| Dependabot / Renovate | **无** | 无 `.github/dependabot.yml`、无 `renovate.json` | <!-- check-docs:ignore 陈述缺失，非路径断言 -->
| `npm audit` / `dependency-review-action` / OpenSSF Scorecard | **无** | workflow 与 package scripts 中均无 |
| `vendor/` 锁定源码 | **无**（依赖 proxy/网络拉取） | 仓库无 `vendor/` 目录 |
| 二进制版本注入（`-ldflags` version/commit） | **无** version stamp | `Makefile:54-76` 的 6 个 `build-*` target 与 `grep -c ldflags Makefile`（=0）；镜像构建只传 `-s -w`（`deploy/docker/Dockerfile.operator:8`） |
| Actions 提交固定（SHA pin） | **无**：全部按 tag 引用（`checkout@v7` ×12、`setup-go@v7` ×11、`buf-setup-action@v1.50.0`、`upload-artifact@v7`、`cache@v6`、`golangci-lint-action@v9.3.0`，共 0 处 `@sha256:`） | `grep -n "uses:" .github/workflows/*` |
| 基础镜像按 digest 固定 | **1 / 16**：仅 `deploy/docker/Dockerfile.operator:10`；其余 `FROM` 仅 tag | 各 `deploy/docker/Dockerfile.*` |
| 产品侧漏洞扫描适配器 | 只有 `NoopScanner`（恒 0 findings）且未挂载 | §3.9 末条 |

## 7. 日志、诊断与机密处理（值班指引）

1. **不要把日志当干净输出。** 审计事件与操作时间线经过脱敏（§3.6），但 `slog` 应用日志没有：
   `internal/app/app.go:123` 是裸 JSON handler，无 `ReplaceAttr`。事故排查时把
   `error`、`detail`、`request body` 类字段贴进工单前，**建议**先人工过一遍
   `internal/redact/sanitize.go:19-34` 覆盖不到的形状（例如 Chart 内部字段名）。
2. **CI 产物含原始容器日志**：`test/e2e/prerequisite/capture-logs.sh:30` 直接 `kubectl logs`，
   上传为 artifact（`.github/workflows/test.yml:359-365,435-442`）。公开仓库的 artifact 对任何
   GitHub 用户可见（下载需登录但可下载）；**建议**给 e2e artifact 设更短保留期或在上传前过滤。
3. **本地 dev 机密只住在 gitignored 的 `data/` 下**：`dev-credentials.env`、`dev-jwt/`、
   `dev-service-tokens/`、`dev-ca/`、`dev-trust-root/`、`dev-enrollment-tokens/`
   （忽略规则 `.gitignore:35`，清单 `deploy/dev/dev.sh:42`）。诊断目录先 `umask 077` 再 `chmod 700`（目录 0700、其内文件 0600）：
   `deploy/dev/dev.sh:123-127`。销毁用 `make dev-purge CONFIRM=1`
   （`.github/workflows/test.yml:444-446` 是 CI 侧收尾；ci profile 的临时机密由
   `deploy/dev/dev.sh:323-326,371-374,422-425,1122-1124` 负责清理）。
4. **轮换/吊销的实际能力**（不要假设仓库里有脚本）：
   - JWT / webhook service token 支持双键并存（`WEBHOOK_SERVICE_TOKEN_PREVIOUS` 以
     `optional: true` 挂载：`deploy/kustomize/services/orchestrator.yaml:45-56`；
     kustomize `secretGenerator` 内容哈希自动滚动：`deploy/kustomize/dev/kustomization.yaml:39-43,54-57,66-70`）。
   - Trust root 有显式 Rotate/EndGrace/Retire/Revoke 四态 RPC（§3.9）。
   - **未见实现**：口令与 CA 的自动过期轮换、以及任何「机密泄露即自动失效」机制。
     轮换步骤与 `gh secret set` 命令形态见 `.github/SECRETS.md` 第 4、6 节。
5. **怀疑某条机密已泄露时的处置顺序（建议）**：先在 CI 侧覆盖同名 secret（`gh secret set`），
   再滚动依赖它的 kustomize Secret，最后在应用层吊销主体（service token 走摘要替换、
   Operator 走 revoke、信任根走 `RevokeTrustRoot`），并检查审计里是否存在该 actor 的历史动作。
6. **不要在本仓库提交任何真实值**：文档、测试夹具、issue 一律只写名字与用途（本仓库的机密名清单见
   `.github/SECRETS.md` 第 1、2 节）。

## 8. 已知缺口清单（汇总，便于跟踪）

| # | 缺口 | 类型 | 证据 |
| --- | --- | --- | --- |
| 1 | **无专用私密披露渠道**：GitHub 私有漏洞报告在本仓库不可用（开源/免费计划，端点 404），且未提供安全邮箱/PGP；无投递回执与 SLA | 需维护者补渠道 | §1.2、§1.3 |
| 2 | 无任何已发布版本，修复无版本语义 | 事实 | §2 |
| 3 | 管理面 HTTP 无 TLS 实现（只有接缝） | 未见实现 | `internal/app/app.go:71,190-201` |
| 4 | `ExternalIdentityService` 已声明已实现但未挂载 | 部分实现 | §3.8「未见实现（外部 IdP）」条 |
| 5 | 制品漏洞准入未接线，nil evaluator 会放行 | 未见实现 | `internal/orchestrator/vulnerability.go:11-23` |
| 6 | 遗留 `verifier.go` 仅格式校验，注释指向不存在的 `CosignVerifier` | 部分实现 | `internal/trust/verifier.go:216-219` |
| 7 | 应用日志与 CI artifact 不经脱敏 | 未见实现 | `internal/app/app.go:123`；`test/e2e/prerequisite/capture-logs.sh:30` |
| 8 | 无 govulncheck / SBOM / 签名 / attestation / 依赖机器人 | 未见实现 | §6 表 |
| 9 | Actions 无 SHA 固定；仅 1/16 基础镜像按 digest 固定 | 事实/建议 | §6 表 |
| 10 | `sync-to-gitcode.yaml` 无 `permissions:`、无 `concurrency`、无 `timeout-minutes` | 事实/建议 | `.github/workflows/sync-to-gitcode.yaml:11-25` |
| 11 | 登录限流为进程内、多副本不共享 | 事实/建议 | `internal/auth/ratelimit.go:18-54` |
| 12 | `release-api` 审计面按 ADR-021 接入 release-auth 的角色判定与窗口策略（TASK-103）：不内嵌 Casbin、不复制策略，判定不可用时 fail closed；release-api 仍不本地校验会话撤销（由 release-auth 的裁决覆盖） | 已实现 | `cmd/api/main.go:69-90`；`internal/audit/decision.go:44-76`；`internal/audit/authorization.go:41-105`；`internal/auth/authorization_decision.go:43-137` |
| 13 | 客户集群内 operator 用 ClusterRole 且可读写全集群 Secret（Helm release 存储模型的必然结果，未用 `resourceNames` 收窄） | 事实/建议 | §3.3 |
| 14 | **审计有绕过 emitter 的直写路径**，与 `AGENTS.md:27` 硬约束 6 不符（当前无明文泄露证据，但无结构性保证） | 部分实现 | §3.6 第 3 条 |
| 15 | 审计查询/导出的组织过滤取自请求，可为空；principal 未被使用（TASK-095 已修：principal 组织成为唯一可读写范围，跨组织 `permission_denied`） | 已实现 | `internal/audit/authorization.go:22-48`；`internal/audit/audit_service_handler.go:42-56,97,142`；回归 `internal/audit/authorization_test.go:21-128` |
| 16 | `NotifierService` 无认证拦截器 + 投递目标无白名单 → 控制面可被当作任意 URL 的 HTTP 出站源，metadata 原文外发 | 未见实现 | §3.11 |
| 17 | ADR-020 的 Vault SecretResolver 适配器未实现（notifier 出站因此恒在无鉴权分支） | 未见实现 | `docs/decisions/ADR-020-use-hashicorp-vault-go-api-for-notifier-secretresolver.md:14-15`；`cmd/notifier/main.go:81` |
| 18 | 「一个活跃标准 Operation」的数据库级唯一索引只在 PostgreSQL，SQLite 侧仅应用层计数（双引擎强度不等价） | 部分实现 | `migrations/000001_legacy_baseline.up.sql:62-63` ↔ `internal/store/sqlite/uow.go:89-100` |
| 19 | 迁移编号连续性/up-down 成对只在运行时校验，无 make/CI 静态门禁 | 未见实现 | `internal/postgres/migrate.go:53,205-212` |
| 20 | 无 HTTP 安全响应头（CSP / X-Content-Type-Options / X-Frame-Options / HSTS） | 未见实现 | `web/nginx.conf:1-106` 无 `add_header`；Go 侧无相关中间件 |
| 21 | 未使用 PostgreSQL RLS / `CREATE POLICY` / `GRANT`，租户隔离完全依赖应用层过滤 | 事实（设计选择） | 全仓检索 0 命中；§3.10 |
| 22 | `README.md:69` 称前端「Node 版本见 `web/package.json`」，但该文件没有 `engines` 字段（全文件无 `engines` 键） | 文档缺陷（未修，本文只报告） | `README.md:69`、`web/package.json:1-51` |

## 9. 需维护者补充（TODO）

1. **补一个真实可用的私密披露渠道**（§1.2 目前只能指向 GitHub 个人页联系方式）：安全邮箱 + PGP 指纹，
   并声明 SLA。GitHub 私有漏洞报告已确认对本仓库不可用，不再作为待办项。
   已完成的相邻设置（2026-09-15 实测）：`secret_scanning`、`secret_scanning_push_protection`、
   Dependabot security updates 均已启用——它们降低「凭据继续流入历史」的风险，但**不替代**报告渠道。
2. 期望的确认/修复/披露时限（当前无任何 SLA 声明），以及是否接受非英语报告（§1.3 第 2 条）。
3. 版本与 backport 政策：首次发版后把 §2 换成受支持版本表。
4. 生产部署侧的 TLS 终结由谁负责：Go 侧只有 `TLSCertificateFiles` 接缝（§3.8），web 入口
   `listen 8087` 为明文 HTTP（`web/nginx.conf:6-8`），同源设计本身不需要 CORS，但需要明确谁承担 TLS。
5. §8 第 14、15、16 条（审计写入绕过 emitter、审计查询租户过滤可空、notifier 面无认证与出站白名单）
   需要指派 owner 与修复批次；本文只报告，未改任何实现。
6. 是否同意把 §3.3 的 ClusterRole 收窄为 namespace 级 Role（或加 `resourceNames`），
   以及是否在 `deploy/kustomize/customer-agent/base/deployment.yaml:21-22` 补齐 Pod 安全字段。

---

> 事实源：`README.md`、`go.mod`、`Makefile`、`AGENTS.md`、`CONTRIBUTING.md`、`.gitignore`、
> `.github/workflows/test.yml`、`.github/workflows/sync-to-gitcode.yaml`、`.github/SECRETS.md`、
> `docs/architecture.md`、`docs/testing.md`、`docs/dev-environment.md`、`docs/dependencies.md`、
> `docs/decisions/ADR-001-*`、`ADR-004-*`、`ADR-007-*`、`ADR-010-*`、`ADR-011-*`、`ADR-015-*`、
> `ADR-017-*`、`ADR-018-*`、`api/proto/auth/v1/auth.proto`、`api/proto/webhook/v1/webhook.proto`、
> `cmd/api/main.go`、`cmd/auth/main.go`、`cmd/orchestrator/main.go`、`cmd/webhook/main.go`、
> `cmd/notifier/main.go`、`cmd/operator/main.go`、`cmd/devseed/main.go`、`cmd/devseed/mtls_ca.go`、
> `internal/auth/{interceptor,casbin,jwt,password,ratelimit,browser_session,service_token,external_idp_service,providers}.go`、
> `internal/app/{app,maintenance}.go`、`internal/audit/{emitter,normalize,sanitize,spool,interceptor,audit_service_handler,archive_sink}.go`、
> `internal/redact/sanitize.go`、`internal/values/{values,secret}.go`、`internal/operator/{service,identity_handler}.go`、
> `internal/operator/agent/agent.go`、`internal/operator/ca/{ca,config,provider,vault}.go`、
> `internal/operator/{k8s/secrets,helmengine/engine,preflight/pod_builder,tls_clients,session_client,workload_identity}.go`、
> `internal/operator/bootstrap/bootstrap.go`、`internal/orchestrator/{emergency,values_approval,enrollment,vulnerability,service}.go`、
> `internal/preflight/service.go`、`internal/trust/{ed25519,verifier,service}.go`、
> `internal/vulnerability/{doc,scanner,evaluator,service}.go`、`internal/store/{store,redis/adapter,postgres/commands,postgres/inventory,sqlite/inventory,sqlite/audit}.go`、
> `internal/quality/sdkcheck/analyzer.go`、`internal/contracts/interceptor/errorsanitize.go`、
> `internal/config/config.go`、`internal/devfixture/{files,accounts_trust,runner}.go`、
> `migrations/000001_legacy_baseline.up.sql`、`migrations/000013_operators_cert_serial_unique.up.sql`、
> `deploy/kustomize/base/secret.yaml`、`deploy/kustomize/dev/kustomization.yaml`、
> `deploy/kustomize/services/{orchestrator,webhook}.yaml`、`deploy/kustomize/customer-agent/base/{rbac,deployment,serviceaccount}.yaml`、
> `deploy/docker/Dockerfile.operator`、`deploy/dev/dev.sh`、`imagecheck.operator.yaml`、
> `sdkcheck.exceptions.yaml`、`license-exceptions.tsv`、`install-sdk.quarantine.yaml`、`upgrade-sdk.quarantine.yaml`、
> `NOTICE`、`LICENSE`、`http-client.env.json`、`configs/{orchestrator,api,e2e}.dev.yaml`、
> `scripts/check-licenses.sh`、`test/e2e/{clients,config_snapshot_blackbox_test}.go`、
> `test/e2e/prerequisite/{smoke,capture-logs}.sh`、`web/package.json`、`git tag -l` 与 `git remote -v` 输出。

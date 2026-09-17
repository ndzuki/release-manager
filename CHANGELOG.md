# CHANGELOG

本文件按月份归纳已合入 `main` 的变更。条目语义对应 Keep a Changelog 的 Added / Changed / Fixed，但不使用版本号（原因见下节）。每条附可核对锚点：`PR #NNN`、`(TASK-NNN)` 或 `REQ-NNN`，均可在 `git log` 中追溯。

## 版本与发布策略

截至本文件撰写时（`main` = `bd0a576`，最后一条交付为 2026-09-14）：

- 仓库**没有任何 git tag**，未发布过任何版本（含 pre-release）。
- 构建链**没有版本注入**：Makefile 中无 `VERSION` 变量、无 `-ldflags`、无 `-X` 符号注入；Go 源码中亦未检索到版本号字符串。
- CI 目前只有测试流水线（`.github/workflows/test.yml`）与镜像同步（`.github/workflows/sync-to-gitcode.yaml`），**没有 release 流水线**。
- 交付纪律为 ADR-000 约定的「一个任务、一个 PR、一个 merge commit」。`main` 全史共 600 次提交（2026-07-14 至 2026-09-14），其中 merge 168 次；真正代表交付的是 91 个 `Merge pull request #NNN` 提交，另有少量早期直接落 `main` 的 squash/直连提交；其余 merge 为分支同步/解冲突，不计入本清单。
- 因此本文件在首个 tag 之前按**月份/里程碑**归纳。打 tag 后应改回按版本（如 `v0.1.0`）组织，并同时补齐版本注入方式——建议在构建目标中使用 `-ldflags=-X` 注入版本符号（此项为**建议**，当前仓库并未实现）。

## 2026-09

里程碑主题：升级/回滚/紧急变更执行链路的真实集群收敛修复，以及分阶段 E2E 门禁落地。本月 19 个 PR 合入。

### 执行链路（升级 / 回滚）

- Operator inventory sync 走 gateway TLS：inventory sync 客户端加载 gateway CA，gateway 挂载 SyncInventory 并保留 inventory 链接（PR #91，TASK-080）。
- UPGRADE 执行链路：tolerant 的 image-override 合并与 `:execute-only` 命令分发（PR #93，TASK-082）。
- 升级终态链路收口：typed identity/results、取消确认与真实的 rollback 信号；附带 SQLite 全新库迁移的 template-clone 快速路径（PR #94，TASK-084）。
- Helm upgrade 输入摘要 label `rm_input_digest` 在两个引擎上编码至 K8s 63 字符 label 边界，并新增 kind upgrade SDK gate + make target + CI 接线（PR #96，TASK-086）。
- ROLLBACK 执行链路：orchestrator/operator 确定性终态驱动 + 幂等回滚重放（PR #100，TASK-090）。

### 紧急变更链路收敛（Fixed）

- 紧急变更的权威 workload identity 经控制流上报，配套迁移 `000024` 与 identity 选择加固（PR #95，TASK-085）。
- 紧急变更 finish race：结果收敛接线 + stuck-lock RPCs 与扫描（PR #97，TASK-087）。
- 按 release key 串行化 identity 收敛；`pending_workload_identity` 缓冲并重放早到的 workload identity 报告（PR #98，TASK-088）。
- PostgreSQL 侧紧急变更结果经合法状态 hop 收敛，消除漂移（PR #99，TASK-089）。
- dev fixture 为 `e2e-emergency-target` 播种 `max_emergency_replicas=4`（PR #92，TASK-083）。

### 分阶段 E2E 与门禁（Added）

- 分阶段（phased）E2E runner、环境清理收敛与 CI 门禁接线：`make dev-stage-*` 阶段目标与 `e2e-stage`/`e2e-prerequisite`（AC-066-17 前置冒烟门禁）（PR #101，TASK-066）。
- E2E 门禁 CI 接线修复：向 E2E gate 传递 `DEV_TRUST_ROOT_PRIVATE_KEY`、门禁变绿后恢复 push-main 触发、阻止尚不可通过的正式 E2E 门禁抢跑（2026-09-14 系列提交，TASK-066）。

### 授权与未兑现契约收口（Fixed）

- 前缀推断式授权映射换成逐 procedure 的显式登记表：修复 `SwitchOrganization`（目标组织域 + `organization/write`）、`SetCapabilityGrant`、`CheckEmergencyConflict`、`TriggerInventorySync`、`RecordArtifactEvent` 五个恒 403 的 procedure，并修复 `ChangePassword` 只有 `platform_admin` 能改本人口令的缺陷；新增「proto 新增 procedure 未登记即失败」与「授权对必须落在默认角色矩阵」两条门禁（PR #106，TASK-095）。
- `ListOperations` 由恒 `unimplemented` 桩改为真实实现（keyset 游标 + 状态过滤 + 租户校验），兑现 REQ-056 的依赖（PR #106，TASK-095）。
- release-api 审计面补 principal 组织域归属：查询/导出按 principal 组织收敛，`Emit` 拒收跨组织事件，导出记录按 principal 组织落库（PR #106，TASK-095）。

### 审计面角色授权（Added）

- 审计面按 ADR-021 接入服务端权威判定：`auth.v1.AuthorizationService/AuthorizeAccess` 由 release-auth 按持久 membership + 版本化 policy 回答 `(object, action)`，并返回有效组织、跨组织许可与 31/366 天窗口；release-api 透传调用方 JWT 消费该判定，判定不可用即 fail closed，不内嵌 Casbin、不用 JWT claims 推导角色。补齐 REQ-029 的 `platform_admin` 跨组织（AC-029-01）与窗口超限 `range_too_large`（AC-029-02）；`Emit`/`ExportAuditEvents` 收紧为需要 `audit/write`（`release_admin`/`platform_admin`）（PR #108，TASK-103）。

### Bundle ingress 认证（Added）

- `SubmitReleaseBundle` 由 CI API key 认证、新增 `POST /webhooks/harbor`（Harbor 独立 key）并挂载 Harbor adapter，出站以 `service:release-harbor` 调 `RecordArtifactEvent`（scope 仅该 procedure）；两把 key 与两条 procedure 不可互相替换（AC-011-04/16/17）。配套把 `ServiceTokenInterceptor` 对「不在本腿白名单的 token」改为 `unauthenticated` 以支持多凭证并存（保留「在白名单但越 scope → `permission_denied`」），并在 dev 生命周期/kustomize/CI 三处配齐凭据（PR #109，TASK-102）。

### 开发环境自举（Fixed）

- 修复 host-run dev 路径的授权自举死锁：release-auth 与 release-orchestrator 此前各用各的 SQLite 文件，而 orchestrator 的 Casbin 投影是**从本库的 `organizations`/`org_members` 行编译**的，于是策略为空、连 `ListCustomers`/`CreateCustomer` 都恒 `permission_denied`，`devseed` 第一步即失败。两者现共享 `data/management.db`（集群路径天然共享同一个 Postgres），并新增 `internal/config` 门禁钉死该契约（含 notifier 独立库的例外与负控制），运行时在投影为空时打印可操作警告（PR #122，TASK-104）。

### 通知面认证与出站（Fixed）

- notifier 面接入服务令牌：`NotifierService/Send`、`GetStatus` 由 `auth.ServiceTokenInterceptor` 守卫并收窄 scope，支持 current/previous 轮换；同源入口（`web/nginx.conf` 的 `/notifier.v1.` 代理）不再构成绕过（PR #120，TASK-096）。
- 出站投递改为**默认拒绝**的 `scheme+host+port` 白名单：未登记目标（含 `169.254.169.254`、回环、RFC1918、集群 DNS）在解析凭据前即被拒绝，job 以稳定错误码 `egress_blocked` 落库并计数、打结构化 WARN；外发 metadata 过 `internal/redact`（PR #120，TASK-096）。
- 落实 ADR-020：Vault KV v2 + Kubernetes auth 的 SecretResolver 适配器（引用缺失即启动失败、错误不携带秘密、租约续期），未启用时按 ADR/REQ 明文保持无鉴权投递（PR #120，TASK-096）。

### 调试集合（Fixed）

- `api/kulala` 六个集合从 Connect 迁移前的 REST/gRPC 形状重写为单端口 Connect 形状：路径改为 `<package>.<Service>/<Method>`、`Login` 的 post-request 脚本自动把 token 写进 `{{AUTH_TOKEN}}`、端口改由 `configs/*.dev.yaml` 与 kustomize NodePort 派生；`manager.http`（针对已移除的 Manager 进程）删除，新增 `notifier.http`，`operator.http` 明确声明 mTLS 不可达（PR #119，TASK-093）。
- 新增 `make api-check` 结构门禁：每条请求必须是 `api/proto` 里真实存在的 RPC、每个占位符必须可解析、env 端口必须来自事实源、空集合必须说明原因（含三类负控制），并入 `make quality` 且随 CI 的 `go test ./...` 执行；六个只做 `nvim` 的 `api-*` 目标删除（PR #119，TASK-093）。

### Operator 会话与生命周期（Fixed）

- 心跳归属修正：agent 收到 `SessionEstablished` 后按协商周期发送 `Heartbeat`（发送与接收共用一把 Send 互斥），orchestrator **不再自写** `last_heartbeat`；`SessionRegistry` 首次接线（网关 operator service 构造 + `Run`），心跳停止即推进 `suspect`/`offline`。心跳阈值、suspect/offline 阈值改为可配置（`operator_session.*`，默认 15s/45s/90s）（PR #118，TASK-098）。
- 紧急变更离线窗口确定性：除会话状态外还要求 `last_heartbeat` 足够新（重启后进程内流已空但会话行仍 `online`），dispatch 失败也归一到 `CodeUnavailable` + `operator_offline`，且拒绝不留非终态 Operation（REQ-032 AC-032-20）（PR #118，TASK-098）。
- 标准 Operation（INSTALL/UPGRADE/ROLLBACK）获得 `operation.deadline`（默认 30m），非终态恢复扫描从「仅启动一次」改为按 `operation.recovery_interval`（默认 1m）周期执行（PR #118，TASK-098）。

### 审计写入收敛（Fixed）

- 审计直写路径收敛：`internal/store/{sqlite,postgres}/operator_management.go` 的事务内审计写入改为经 `store.SanitizeAuditEvent` 兜底脱敏（字段名 + 内容双扫描，比异步 emitter 更严），并新增结构门禁——除登记的 6 个 store 文件外任何 `INSERT [OR IGNORE] INTO audit_events` 都失败，且事务写入者必须调用该兜底（含合成树负控制）（PR #117，TASK-097）。
- `AuditService/Emit` 按事件 id 幂等：两引擎分别改为 `INSERT OR IGNORE` 与 `ON CONFLICT (id) DO NOTHING`，重放同一事件不再失败也不再写第二行；契约注释显式声明去重键（PR #117，TASK-097）。

### 供应链与 CI 加固（Added）

- 依赖漏洞扫描接线：`make vulncheck` + CI `vulncheck` job，只对**代码实际调用**的漏洞失败；无上游修复的公告（Helm SDK 依赖的弃用 openpgp，`GO-2026-5932`）走 `vulncheck.exceptions.yaml` 的 owner + 补偿控制 + 复审期，过期即失败，并带「去掉例外即失败 / 例外过期即失败」两类负控制（PR #110，TASK-101）。
- 第三方 action 固定到 commit SHA（`actions/*` 保持主版本标签）、基础镜像固定到 index digest、新增 dependabot（actions/docker/gomod）承接轮换；`sync-to-gitcode` 补 `permissions`/`concurrency`/`timeout-minutes` 且 token 不再进 URL（PR #110，TASK-101）。
- 新增迁移静态门禁 `make check-migrations`：两个内嵌迁移集合编号连续、up/down 成对、文件名合规（PR #110，TASK-101）。

## 2026-08

里程碑主题：共享 API 契约统一、Web 控制台整页铺开、操作创建工作流与 ValuesRevision 交付、Operator 注册证书链路（REQ-015）、CI 迁移 self-hosted。本月 32 个 PR 合入。

### 契约与 API

- 共享 API 契约包收编交付，服务端与前端 wire 类型自单一 proto 源生成（PR #71，TASK-010；配套 08-12 审计 web 类型经 buf 从合并后契约再生成）。
- Operation 观察契约：agent 侧 `rollout_progress` 端到端持久化（PR #83、PR #84，TASK-077）。
- 规范化 `ExecuteEmergencyChange` 契约替换遗留调用面（PR #86，TASK-079）。
- ValuesRevision 链路：版本管理 v6 交付（PR #76，TASK-018）；values create/list Connect 端点打通（PR #77，TASK-071）；prepare session 契约对齐 REQ-018 D19/D22/D23（2026-08-13 提交）。

### 控制面服务

- Operation 创建工作流补全（v3，含 REQ gates、header 幂等、PostgreSQL UoW）（PR #51，TASK-067）。
- 发布 Preflight 生命周期持久化与取消（v3 实现）（PR #81，TASK-019）。
- 制品生命周期策略 v16：6 阶段 GC、持久幂等、unarchive CAS（PR #82，TASK-069）。
- Notifier 迁移至 PostgreSQL（PR #75，TASK-076）。
- 紧急变更后端缺口补齐（PR #90，TASK-081）。

### Operator 与信任

- Operator Enrollment v3：`EnrollRequest` 移除 `operator_id`（reserved）、中心侧签发身份、新增 `RenewCertificate`、hash-only token、Vault/file CA provider 与证书生命周期列（PR #88，TASK-015 / REQ-015）。
- Customer/Cluster 级联 revoke 事务化，含 per-operator revoked 审计（REQ-015 事务边界 4，2026-08-25 提交）。
- TrustService 证书 live mount（PR #72，TASK-074）；信任根轮换 v2 重新交付（PR #78，TASK-043）。
- Operator agent bootstrap：enrollment 身份、gateway mTLS、命令流身份守卫（PR #74，TASK-075）。

### 认证与授权

- 基于 capability 的组织域授权强制（Round 2 收口）（PR #65、PR #67，TASK-027）。
- 幂等 `CreateLocalUser`/`GetLocalUser`/`ListLocalUsers` 与角色绑定（PR #73，TASK-072）。
- Redis session adapter（PR #68，TASK-073）。

### Web 控制台

- 管理页面批量落地：客户管理（PR #52，TASK-051）、Operator enrollment 管理（PR #57，TASK-053）、ValuesRevision 编辑与审批 UI（PR #53，TASK-055）、发布操作工作流（PR #54，TASK-056）、操作时间线（PR #85，TASK-057）、紧急变更 UI（PR #87，TASK-058）、审计查询页接入 app shell（PR #55，TASK-059）、通知任务页加载时标记过期重放祖先（PR #69，TASK-060）。

### 开发环境、CI 与质量门禁

- dev 环境迭代交付（v2+）：seed 拆分围绕 enrollment、部署 customer operator agent（token+CA 注入、hostAliases）、k3d v5 参数修正、端口/就绪/内存门禁的确定性修复（PR #89 合入 08-28，此前 08-24 系列修复，TASK-065）。
- Rollout watch SDK 集成门禁（PR #66，TASK-064）。
- CI 全部作业切换到 self-hosted runner（含恢复被误删的 rollout watch 门禁步骤）（PR #79、PR #80）；修复 Go module cache 清理权限失败（PR #70）；actions 升至最新 major（checkout v7、setup-go v7、cache v6、buf v1.50、golangci v9.3）（2026-08-04 直连提交）。
- imagecheck 适配 Docker 29 OCI save layout，operator 镜像构建 GOPROXY 多源（2026-08-14 提交）。

## 2026-07

里程碑主题：从脚手架到端到端主干链路——发布 bundle、信任策略、Preflight 四连、Helm SDK 安装/升级/回滚、操作状态机三轮演进、审计与认证体系、PostgreSQL 迁移与 dev 环境。本月 48 个 PR 合入；上旬（PR 纪律完全生效前）另有若干直接合并/直连提交落 `main`。

### 项目骨架与契约

- 仓库初始化与项目脚手架：proto/buf、六服务骨架（`cmd/api`、`auth`、`notifier`、`operator`、`orchestrator`、`webhook`）、CI（PR #4，TASK-001）；随即迁移至 Connect 框架并配 Kulala `.http` 调试集合（2026-07-16 直连提交）。
- 早期领域索引与主干交付直接合入 `main`：微服务架构导航与 core pipeline、客户环境索引（TASK-002~004）、Helm Release Inventory 同步（TASK-017）、发布结果通知与 Dead Letter（TASK-031，2026-07-15~17）。
- HelmEngine SDK 适配契约（PR #28，TASK-041）；ReleaseDefinition 生命周期（PR #27，TASK-040）；ValuesRevision 版本管理首版（PR #16，TASK-018）。

### 发布输入与执行（Orchestrator / Webhook）

- ReleaseBundle 提交与候选制品登记（webhook 侧，PR #10）与 orchestrator 摄取流水线（PR #59，均 TASK-011）。
- 制品 Digest 与签名验证信任策略（PR #9，TASK-012）。
- Customer 生命周期管理（PR #11，TASK-013）；集群制品路由配置（PR #12，TASK-014）。
- 发布 Operation 核心状态机与幂等：v1（PR #20）→ v2 完备（PR #58）→ v3 收口 `WatchOperation`、Timeline、迟到 Result、EMERGENCY 取消（PR #64，TASK-023）。
- Helm SDK 安装/升级/回滚 Release（PR #18、PR #19、PR #35，TASK-020/021/022）；typed Helm SDK 升级流水线补全（2026-07-29 直连提交，TASK-021 v3）。
- Kubernetes Rollout 状态观察（PR #21，TASK-024）。

### Preflight 与制品准入

- 发布 Preflight 编排（PR #17，TASK-019）；四道检查相继落地：Artifact Preflight（PR #30）、Helm Render Preflight（PR #31）、Cluster DryRun Preflight（PR #32）、Runtime Image Pull Preflight（PR #42）（TASK-045~048）。
- SBOM 与漏洞准入策略（PR #40，TASK-042）；制品信任根与签名密钥轮换（PR #41，TASK-043）。
- 受控紧急变更与 Helm 收敛（PR #38，TASK-032）。

### 审计

- 通用审计采集与持久化（PR #34，TASK-050）；审计查询与组织授权（PR #37，TASK-029）；审计保留压缩与归档（PR #43，TASK-030）。

### Operator

- Operator Enrollment 与唯一身份（PR #13，TASK-015）；控制流命令投递与 ACK 重放（PR #14，TASK-016）；Operator Session 与心跳（PR #29，TASK-044）。

### 认证与授权

- 本地认证与持久 Session（PR #22，TASK-025）；组织与成员角色（PR #23，TASK-026）；Casbin 组织域 RBAC 强制（PR #24，TASK-027）；OIDC / LDAP / 钉钉外部身份接入（PR #36，TASK-028）；组织与客户授权绑定（PR #33，TASK-049）。

### 存储

- Orchestrator 持久化从 SQLite 迁移至 PostgreSQL（PR #56，TASK-070）。

### Web 控制台

- Vue 3 + Vite 前端脚手架与 Connect transport（2026-07-16 提交）；前端认证与应用壳层（PR #39，TASK-033）；集群与制品路由 UI（TASK-052，2026-07-24 落 `main`）；Release Inventory 前端（TASK-054，2026-07-27 squash 提交，`(#48)`）。

### 质量门禁、开发环境与治理

- 工程化与质量工具链：SDK 门禁、需求校验（`cmd/reqcheck`）、E2E 框架、CI 整合（PR #7，TASK-008）；SDK-only 静态门禁强制（PR #25，TASK-037，`cmd/sdkcheck`）。
- SDK 质量门禁续建：Upgrade SDK 质量（PR #44，TASK-062）、Rollback SDK 质量（PR #49，TASK-063）、Helm Install SDK 集成门禁（2026-07-29 直连提交，TASK-061，引入 `cmd/imagecheck` 接线）。
- 本地 dev 环境（k3d/registry 等）（PR #45，TASK-065）。
- 治理文档：原子需求切片规则（PR #8，TASK-009）与完整规格模板（PR #26，TASK-039，`docs/atomic-requirement-template.md`）；管理前端与审计通知领域索引（PR #6、PR #5，TASK-007/006）。

## 在途与后续

截至本文件撰写时以下事项**尚未合入 `main`**（`gh pr view 102 103` 确认状态均为 OPEN）：

- **PR #102**（OPEN）：`test(api): make the Connect audit test deterministic (drop the 5s flush-tick dependency)` —— flaky 测试修复。
- **PR #103**（OPEN）：`docs: publish the repository documentation surface and fix the local entry points` —— 文档面发布（README/CONTRIBUTING/LICENSE/NOTICE 与 `docs/decisions/ADR-000..020` 等）加依赖许可门禁（`scripts/check-licenses.sh` + `license-exceptions.tsv`）。`main` 上目前无 README/LICENSE/docs/adr，仅有 `docs/atomic-requirement-template.md`。
- 工作分支 `task/092-project-docs-align`（本 CHANGELOG 所属的文档面任务）仍在进行中。

明细查询命令：

```bash
git log --merges --first-parent main --oneline   # 交付面：main 上的合并
git log --merges --format='%cd %s' --oneline main | grep 'Merge pull request'  # 91 个 PR 交付
git log --grep 'TASK-066' --oneline              # 某任务的专项提交
git log --no-merges --format='%cd %s' main       # 全部非 merge 提交（含直连/squash 交付）
gh pr list --state open                          # 在途 PR
```

> 事实源：`git tag`（空）、`git rev-list --count bd0a576`（600）与 `--no-merges`（432）、`git log --merges`（168，其中 `Merge pull request` 91）、`git log --merges --first-parent bd0a576`（98）、逐条 `git show -s --format='%b'` 提取 PR 标题、`git log --format='%s' | grep -oE 'TASK-[0-9]+'`（主题分布）、`git show bd0a576:Makefile`（无 `VERSION`/`ldflags`）、`git ls-tree bd0a576`（无 README/LICENSE/docs/adr；工作流仅 `test.yml` 与 `sync-to-gitcode.yaml`）、`git merge-base --is-ancestor`（REQ-015 提交归属 PR #88 核验）、`gh pr view 102 103 --json state,title,files`；文件：`bd0a576` 快照下的 `Makefile`、`.github/workflows/`、`docs/`、`web/package.json`、`cmd/{reqcheck,sdkcheck,imagecheck}`。

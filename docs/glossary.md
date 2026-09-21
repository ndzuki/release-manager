# release-manager 领域词汇表（代码仓合并版）

本文件是 release-manager 领域术语的**合并版词汇表**，面向阅读与修改代码的工程师。

- **术语权威源**是知识库 `Notes/CONTEXT.md`（`## Language` 小节，及其引用的 ADR）；`Design/glossary.md` 是它的设计侧投影，条目更多（含 E2E 与开发环境专有词）。
- 两个来源都有的术语，定义**以 `Notes/CONTEXT.md` 为准**；只出现在设计词汇表中的术语按原义保留，并在「出处」列注明来源。
- `_Avoid_`（禁用/易混淆说法）在本表中保留为定义末尾的 `（避免：…）`：写代码、命名与评审时会实际用到这些反例，删掉会让术语边界的约束失效。
- 分组仅为便于查阅，不代表依赖顺序或调用关系；同一术语只出现一次。
- 两处说法**实质不一致**的术语已按仓库实现证据逐条裁定（D1–D5）：表格条目采用 CONTEXT.md 的定义；实现与设计词汇不一致的条目在行内标注「实现现状 ≠ 设计词汇」并附 `文件:行号` 证据，裁定结论与遗留问题见文末 `## 差异裁定`。

## 组织、授权与边界

| 术语 | 定义 | 出处 |
| --- | --- | --- |
| Organization | 发布管理平台中的组织边界，独立于 Customer 领域，拥有成员与角色；状态 active/disabled。（避免：租户、team、把组织等同于 Customer） | CONTEXT.md › Language；设计词汇表：ADR-006 |
| OrganizationMembership | 组织成员记录，(org, user) 唯一约束；角色 platform_admin/release_admin/deployer/viewer；release_admin 可管理本组织成员但不能授予 platform_admin；带乐观锁 version，变更发布 OrganizationMembershipChanged 事件。（避免：member row、user-org mapping） | CONTEXT.md › Language；设计词汇表：REQ-026 |
| OrganizationCustomerBinding | 组织与客户之间的授权绑定资源；org+customer 唯一约束，状态 active/revoked，客户禁用不自动删除 binding；撤销后业务服务授权消费路径返回 permission_denied，绑定变更发布 OrganizationCustomerBindingChanged 事件。 | CONTEXT.md › Language；设计词汇表：REQ-049, ADR-006 |
| Customer | 发布管理平台中的租户边界，拥有自己的 Cluster、ReleaseDefinition、Operator 与 Release 数据。（避免：tenant、account、客户账号） | CONTEXT.md › Language |
| Cluster | 归属于单个 Customer 的目标 Kubernetes 集群，是 Release 部署与 Operator 运行的隔离边界。（避免：customer cluster、target environment） | CONTEXT.md › Language |
| Local User | 以 username/password 凭据登录平台的本地身份（区别于 External Identity）；首个 platform_admin 由 Initialize 引导创建，其余本地用户经 CreateLocalUser 创建（仅 platform_admin，目标角色 ≤ 创建者权限且授权先于幂等命中）；username 为自然键，幂等命中返回既有用户且不更新密码；角色存于 OrganizationMembership（组织级）。（避免：外部身份、把 dev-admin 当作 CreateLocalUser 创建） | CONTEXT.md › Language；设计词汇表：REQ-025 |
| External Identity | 由 OIDC、LDAP 或 DingTalk 身份提供方签发，以唯一 `(provider, subject)` 映射到平台 User 的外部登录身份；外部组可映射为 Organization 成员角色。（避免：social login、third-party account、federated user） | CONTEXT.md › Language；设计词汇表：REQ-028 |
| Authorization Snapshot | 由身份授权上下文发布、在业务服务本地维护的版本化只读授权投影，包含 OrganizationMembership、OrganizationCustomerBinding、源版本和消费 checkpoint；治理写操作仅在投影追平且新鲜时使用。（避免：auth cache、permission cache、shared auth tables） | CONTEXT.md › Language；设计词汇表：ADR-006, REQ-027/033/049 |
| Capability Grant | 覆盖默认角色矩阵的显式授权记录（organization_id、subject、action），持久化于 capability_grant 表；active grant 优先于角色默认规则，revoke 为软删除（revoked=true 可重新 active）。emergency_resolver 是 capability 而非第五个 Role，任意角色经显式 grant 后可执行 release.emergency.resolve。（避免：role upgrade、把 capability 当作新 Role、默认矩阵 grant） | CONTEXT.md › Language；设计词汇表：REQ-027/049 |
| release_admin | 组织级发布管理角色（区别于平台级 platform_admin）：可查看审计事件中的完整 actor ID、role 与 displayName；普通成员仅见脱敏 actor；非 platform_admin 跨组织查询被服务端拒绝（REQ-059 AC-01/AC-05）。（避免：org admin、把 release_admin 当作 platform_admin） | CONTEXT.md › Language |
| EnrollmentToken | 一次性注册凭证，明文只在创建响应中返回一次且持久化仅存不可逆 hash；状态 pending → used/expired/revoked，同一 Cluster 至多一个有效 pending token，替换/作废走同事务原子语义（REQ-015/REQ-053）。（避免：token 明文持久化、多 pending token、enrollment secret） | CONTEXT.md › Language；设计词汇表：REQ-015/053 |
| Bundle Ingress Service Token | webhook→orchestrator 的静态服务身份令牌：webhook 转发 orchestrator BundleService 请求时附加 `Authorization: Bearer <token>`（`internal/webhook/service.go:63-64`），actor 解析为 service:release-webhook；orchestrator 以 current+previous 双 hash + constant-time comparison 校验支持无停机轮换（通用机制 `ServiceTokenInterceptor`，`internal/auth/service_token.go:29`，挂载于 `cmd/orchestrator/main.go:487`；REQ-011 §562）。dev 最小实现闭环经 D-100 裁决 B、由 TASK-065 v21 落地（webhook 透传 Bearer + orchestrator 双 hash 校验 + BundleService actor 分支，AC-33）；生产 Secret manager 通道仍归 REQ-011 owner。代码 flag/env/注释统一写作 bundle ingress service token（`cmd/webhook/main.go:62`、`cmd/orchestrator/main.go:826`）。设计词汇表对同一 seam 的旧名 `Internal Service Token` 与 `bundle ingress 服务令牌 seam` 不再单列条目，仓库内亦无 `Internal Service Token` 标识符。（避免：API key、把服务令牌当作用户凭据、生产 Secret 通道由 REQ-011 owner 承接） | CONTEXT.md › Language；设计词汇表：REQ-011, D-016, D-100 |
| seed identity | Initialize 首次引导阶段：创建组织 + platform_admin 用户 + 成员（dev 环境对应 dev-admin 账号）；此后本地用户创建走 CreateLocalUser 且拒绝 platform_admin 角色（D-16，REQ-025/REQ-065 共享）。（避免：dev admin bootstrap、直接 Create 首个管理员） | CONTEXT.md › Language；设计词汇表：REQ-025/065 |

## 发布输入与制品

| 术语 | 定义 | 出处 |
| --- | --- | --- |
| ReleaseDefinition | 描述目标 Customer/Cluster、namespace、release name 与 chart 的持久发布目标配置。（避免：release config、application definition） | CONTEXT.md › Language；设计词汇表：REQ-040 |
| ClusterStatus | 集群生命周期状态：ACTIVE / DISABLED；disabled 集群不可作为发布目标、不可配置路由（ConfigureClusterRoute 与 PublishRelease 双路径返回 permission_denied）。REQ-014 AC-014-04 | CONTEXT.md › Language |
| RouteRule | 绑定到 Cluster 的制品路由规则，按 artifactType（image/chart）独立维护，每条含 mode（image: direct/pull_through_cache/replicated；chart: direct/replicated，pull_through_cache 未通过能力测试前前端禁用且后端仍独立拦截）、sourcePrefix 与 targetPrefix；同一 artifactType 内 sourcePrefix 唯一，冲突返回 routing_conflict 并携带 conflictingRuleId。路由预览为纯前端计算（central URI→target URI），后端仍执行完整校验为最终权威。（避免：通用 route config、把 chart pull_through_cache 当可用模式、明文 registry credential 输入） | CONTEXT.md › Language；设计词汇表：REQ-014/052 |
| ClusterRoute | 制品路由规则的接口/持久化表示：orchestrator proto ClusterRoute message 与 store.ClusterRoute struct，领域语义同 RouteRule（同一 artifactType 内 sourcePrefix 唯一）；字段 id/cluster_id/artifact_type/mode/source_prefix/target_prefix。（避免：与 RouteRule 概念分裂为两套词汇） | CONTEXT.md › Language；设计词汇表：REQ-014/052 |
| ArtifactType | 制品路由的类型维度：image / chart；image 与 chart 路由独立校验、独立存储与查询（store 层另有 sbom/provenance/signature 枚举值，但路由域仅 image/chart）。REQ-014 AC-014-02。（避免：把全局候选制品 ArtifactType（common.v1）与路由域 ArtifactType（orchestrator.v1）混用） | CONTEXT.md › Language |
| ArtifactMode | 集群获取制品的方式：direct / pull_through_cache / replicated；pull_through_cache 仅 image 路由支持，chart 路由硬拒绝（能力测试未通过前的前端禁用 + 后端独立拦截）。REQ-014。（避免：把模式与传输协议混为一谈） | CONTEXT.md › Language |
| ReleaseBundle | CI 提交的不可变发布输入快照，固化 Chart、镜像及来源信息，并通过 digest 作为发布编排的制品边界。（避免：release package、artifact bundle、deployment bundle） | CONTEXT.md › Language；设计词汇表：REQ-011 |
| Bundle Ingestion Validation | ReleaseBundle 接入阶段执行、与 environment 无关的完整性校验；确认 canonical digest 可复算、Chart/Image digest 与引用格式合法且快照内部一致。通过后 Bundle 可进入 `validated`，但不代表任何 Trust Policy 已通过。（避免：bundle trust validation、environment validation） | CONTEXT.md › Language |
| archived_from_status | ReleaseBundle 两阶段归档（ADR-012）中记录归档前原始状态的字段：soft archive 时固化，CAS 恢复必须回到该状态，禁止把 received/rejected 提升为 validated；Operation 创建可在事务行锁/CAS 下自动恢复 archived_from_status='validated' 的 Bundle。（避免：恢复时状态提升、把 archived 当无条件可复用） | CONTEXT.md › Language |
| status_filter | ListBundlesRequest 的 BundleStatus 过滤数组：空 = {received, validated}（archived/rejected 隐藏），内部服务可传包括 archived 的状态集；替代 include_archived/include_rejected 布尔对（REQ-011 v2，D-18 同步）。（避免：include_archived、include_rejected 布尔参数） | CONTEXT.md › Language |
| Candidate Artifact | 控制面观测到、按 `(digest, artifact_type)` 全局唯一登记的候选制品身份；可关联多个 ReleaseBundle，无关联期间按 orphan TTL 清理。（避免：candidate image、temporary artifact、bundle artifact row） | CONTEXT.md › Language；设计词汇表：REQ-011 |
| Candidate Artifact 身份+多 Location | Candidate Artifact 存储模型：身份行 candidate_artifacts（全局唯一 (digest, artifact_type)，无 ref 列）+ 多位置行 candidate_artifact_locations（(artifact_id, ref, source_id, first_seen_at, last_seen_at)）；ref 不再作为身份行单值列（REQ-011 v2，D-18 同步）。（避免：ref 单值列、身份与位置混存） | CONTEXT.md › Language |
| Artifact Event | Harbor 等外部制品源产生的不可变原始观测事件，以来源和事件 ID 幂等登记；它可以更新 Candidate Artifact 索引，但绝不创建 ReleaseBundle 或 Operation。（避免：webhook log、candidate artifact event） | CONTEXT.md › Language |
| Trust Root | 用于验证制品签名的受信公钥及其 issuer/subject 约束，经历 pending → active → grace → retired\|revoked 生命周期；平台只保存公钥或受控公钥引用，不保存私钥。（避免：trust key、signing secret、registry credential） | CONTEXT.md › Language；设计词汇表：REQ-012/043 |
| Trust Policy | 按 environment 维护当前 Trust Root 集合、policy version 与 revocation epoch 的验证策略快照。（避免：static issuer list、verification config） | CONTEXT.md › Language；设计词汇表：REQ-012 |
| Artifact Trust Verification | Candidate Artifact 在特定 environment 下通过当前 Trust Policy 的版本化验证证明，至少关联 artifact digest、policy version、revocation epoch 与 verified time；它不同于仅证明接入完整性的 Bundle Ingestion Validation。（避免：validated artifact、用 `validated_at` 表示 Trust Policy 通过、bundle validation） | CONTEXT.md › Language；设计词汇表：REQ-012 |
| Revocation Epoch | Trust Policy 的单调递增撤销版本；紧急撤销时递增，用于使 VerificationRecord 缓存立即失效。（避免：cache TTL、soft revoke） | CONTEXT.md › Language；设计词汇表：REQ-012/043 |
| Vulnerability Policy | 按 environment 维护的版本化漏洞准入策略，包含 per-severity 最大数量阈值与 max_age；production 默认全 0 阈值 fail closed，切换 active version 即回滚（REQ-042/ADR-008）。（避免：security gate、静态 threshold 配置、绕过策略的硬编码白名单） | CONTEXT.md › Language；设计词汇表：REQ-042 |
| Vulnerability Scan Result | 按 (artifact_digest, scanner, result_version) 键控的不可变漏洞扫描结果，含 per-severity counts 与 findings；过期与否由 active policy 的 max_age 判定，过期触发以 (digest, scanner) 幂等的重扫（REQ-042）。（避免：scan output、latest scan row、以 scanned_at 单字段代替策略判定） | CONTEXT.md › Language |
| Vulnerability Exception | 有时限的漏洞 finding 豁免记录，必填 finding_id、artifact_digest、actor、reason、expires_at 并审计；仅未过期且作用域匹配的例外可豁免对应 finding，到期自动失效回到正常策略，不删除历史（REQ-042/ADR-010）。（避免：永久豁免、无 actor 的例外、删除例外历史） | CONTEXT.md › Language |

## 执行域

| 术语 | 定义 | 出处 |
| --- | --- | --- |
| Operator | 部署在目标 Cluster 中、通过控制流接收并执行发布命令的已注册 agent identity。（避免：worker、executor、cluster agent） | CONTEXT.md › Language；设计词汇表：ADR-001 |
| Operator Session | Operator 与控制面之间的有状态连接记录，包含 online、suspect、offline、revoked 生命周期。（避免：connection、agent session）四态口径的依据在实现侧：wire 枚举 `OPERATOR_SESSION_STATUS_REVOKED`（`api/proto/orchestrator/v1/orchestrator.proto:580`）、store 常量 `SessionRevoked`（`internal/store/store.go:660`）、证书吊销级联将会话直接置为 `revoked`（`internal/store/sqlite/operator_lifecycle.go:120`、`internal/store/postgres/operator_lifecycle.go:124`）、proto↔store 双向映射一等公民（`internal/orchestrator/operator.go:478-479`、`internal/orchestrator/operator.go:506-507`）——`revoked` 是会话**状态**而非仅状态原因。 | CONTEXT.md › Language；设计词汇表：REQ-044 |
| Operator Session Status Reason | Operator Session 当前在线状态的服务端权威原因枚举，用于解释 `online`、`suspect`、`offline` 或 `revoked`，前端不得仅凭时间戳自行推断。（避免：client-side offline reason、heartbeat message text） | CONTEXT.md › Language |
| Command Outbox | 控制面数据库中的持久化待投递命令队列，作为待投递命令的权威存储；每行携带全局单调 sequence、command_id、operation_id、payload_version 与 deadline，状态按 pending → delivered → persisted → running → terminal 推进，MVP 每 Operator max_inflight=1；ACK_PERSISTED 在 Operator 本地 fsync 完成后才发送，中心据此释放重投责任；重连时 Operator 重报 last_seen_sequence 以检测 gap（ADR-005、REQ-016）。（避免：memory channel、仅 ACK_RECEIVED、网络 exactly-once） | CONTEXT.md › Language；设计词汇表：ADR-005, REQ-016 |
| HelmEngine | Operator 内封装 Helm Go SDK（helm.sh/helm/v3/pkg/action）的执行引擎，提供 Install/Upgrade/Rollback/Status/History/GetValues/List 接口；每个 Operation 独立初始化 action.Configuration，不跨并发 Operation 共享可变 action client。（避免：helm CLI wrapper、共享 helm client、os/exec 调用 helm） | CONTEXT.md › Language；设计词汇表：ADR-004, REQ-041 |
| Preflight Lifecycle | 每个 Operation 至多一条的 Preflight 生命周期摘要，记录执行状态、实际阶段与关联 Operation 的终态时间，供生命周期保留与 GC 使用。（避免：preflight result、probe record、stage result） | CONTEXT.md › Language；设计词汇表：REQ-019 |
| Render Preflight | 发布 Preflight 的第二个 required 阶段（ADR-008）：operator 用 Helm Go SDK（chartutil/helmtemplate）在进程内以已校验 Chart + approved ValuesRevision 渲染 manifest 并做 values schema 校验，只返回 render_digest、resource summary 与 warnings；输入为 RenderOptions（ReleaseName/Namespace/Chart/ChartDigest/Values/ValuesDigest/ValuesPatch/ImageOverrides/CapabilitiesSnapshot/MaxManifestBytes/IncludeCRDs），SecretRef 不在本阶段解析（ADR-007，D-23）；稳定错误码 values_schema_failed、render_failed、deprecated_api（warning）、size_exceeded、cancelled（REQ-046，D-25）。`secret_output_forbidden` 已按 TASK-162 显式延后：本阶段只返回 digest/summary/warnings，raw manifest 由 cluster stage 消费且从不落库 ⇒ 持久化边界上的 Secret 检测**结构上不可达**，把守卫前移到渲染路径会违反 AC-046-02（渲染 Secret 必须成功且只留摘要）。（避免：helm template 子进程、持久化完整 manifest、render 阶段解析 SecretRef、与 Cluster DryRun 混淆） | CONTEXT.md › Language；设计词汇表：ADR-008, REQ-046 |
| Runtime Pull | 发布 Preflight 的第四个可选阶段（ADR-008）：operator 在目标 namespace 以目标 ServiceAccount 创建受限短生命周期 probe Pod（每镜像独立、restartPolicy: Never、imagePullPolicy: Always），由 kubelet/云 IAM 真实拉取 digest-pinned 镜像并观察 container waiting/terminated 状态；稳定错误码 image_pull_backoff、registry_unauthorized、network_unreachable、iam_denied、timeout、cleanup_failed；结果按镜像记录 pulled/error_code/node，不持久化 registry credential 或 Secret body。（避免：image pull check、runtime probe、pull preflight） | CONTEXT.md › Language；设计词汇表：REQ-048 |
| CapabilitySnapshot | Operator Session 上报的集群能力快照（KubeVersion + sorted APIVersions），其内容规范化摘要（sha256）即 cluster capability version，是 preflight 幂等缓存键 (render_digest, capability_version) 的组成部分；单一权威为 TASK-044 Operator Session 协议，禁止第二套 capability 上报协议（D-44 决策、REQ-044/046/047 共同契约、ADR-008）。（避免：手工版本号、独立上报协议） | CONTEXT.md › Language；设计词汇表：REQ-044/046 |
| preflight_passed | ADR-009 Operation 状态机中 preflight 阶段的通过事件（EventPreflightPassed）：orchestrator 在全部 preflight 阶段（Artifact/Render/DryRun/optional Runtime Pull）通过后发出，驱动 preflight → queued 状态转换；事件后 Operation 状态为 queued，preflight_passed 本身不是持久化状态（REQ-048 AC-048-05）。（避免：把 preflight_passed 当 Operation 终态或持久化状态） | CONTEXT.md › Language；设计词汇表：REQ-048 |
| preflight_dispatches | Operation 创建事务内写入的 preflight 命令首行出队表（REQ-067）：CreateOperation 在同一事务写 idempotency_records 与 preflight_dispatches 首行（PRECHECK_ARTIFACT），事务外 dispatcher 以该表为工作队列 at-least-once + 幂等投递；Preflight Coordinator 启动即消费首行、不再自行投递（REQ-019 消费契约，D-87 决策，对齐 ADR-009 字面）。（避免：coordinator 自行投递首行、独立双写） | CONTEXT.md › Language |
| InstallCommand | operation_type=INSTALL 的持久命令，复用 Command message legacy 字段（definition_id/namespace/release_name/bundle/values/create_namespace/timeout_seconds/atomic/values_revision_id），payload_version=1；无独立 typed protobuf message（与 UpgradeCommand 不同）。（避免：新建独立 InstallCommand message、把 UpgradeCommand 语义套用到 Install） | CONTEXT.md › Language |
| InstallResult | INSTALL 终态结果（agent.Result JSON：status/release 快照/inventory_sync_hint/resource_summary.manifest_digest/错误码），BoltDB 持久化并经 CommandStream result_json 回传，相同 command_id 重投返回缓存结果不重复安装。（避免：与 UpgradeResult 混用、结果含 Secret 明文） | CONTEXT.md › Language |
| UpgradeCommand | operation_type=UPGRADE 的 typed 执行命令（operator.v1 UpgradeCommand，Command.payload oneof 成员，payload_version=2）；经 Command Outbox 以 command_id=`{operation_id}:execute` 持久化，由 operator deliverPending 首投消费，agent 执行前要求 payload_version==2 且含 UpgradeCommand（否则返回 unsupported_command_version）；effective values 由 orchestrator 侧 mergeEffectiveValues 冻结（approved canonical → values_patch → bundle image override，中间对象缺失按需创建），以 EffectiveValuesJson/EffectiveValuesDigest 下发并校验（digest_mismatch 拒绝）。（避免：复用 InstallCommand legacy 字段语义、以 preflight stage payload 冒充 UPGRADE 执行命令、payload_version≠2） | CONTEXT.md › Language |
| UpgradeResult | UPGRADE 的 typed operator.v1.CommandResult.upgrade 终态结果，包含 from、attempted、active 快照、rollback_succeeded 与 resource_summary；成功、失败、取消路径均不可缺失。 | CONTEXT.md › Language |
| CANCELLING | CancelOperation 的中间状态；须等待 agent acknowledgement，不能由中心侧提前伪造 cancelled，确认后才进入规范终态。 | CONTEXT.md › Language |
| execute entry | UPGRADE 的权威执行 outbox entry，command_id 为 operation_id:execute、payload_version=2，并携带与 typed UpgradeCommand 一致的 release identity。 | CONTEXT.md › Language |
| Workload identity | Kubernetes workload 的 kind/name/namespace/uid 组合身份；Emergency target 与 operator command 必须引用同一权威 identity。 | CONTEXT.md › Language |
| Authoritative workload identity | 由可验证的 Kubernetes workload 来源提供的权威 kind/name/namespace/uid，不接受客户端自报或不稳定显示名替代。 | CONTEXT.md › Language |
| WorkloadUID | Emergency command 中用于与集群对象 metadata.uid 做一致性校验的 Kubernetes workload UID。 | CONTEXT.md › Language |
| SetReplicas | 紧急变更的强类型 workload 字段操作之一：将目标 workload 的 replicas 调整为指定值（0 ≤ replicas ≤ max_emergency_replicas，仅 DEPLOYMENT/STATEFUL_SET，HPA managed 拒绝）；通过 ExecuteEmergencyChangeRequest.set_replicas 引用，由 operator 以 client-go 执行并回报 EmergencyEffect。（避免：把 replicas 当标准 Operation 目标、绕过 EmergencyIntent 直接 patch K8s） | CONTEXT.md › Language |

## Operation 与时间线

| 术语 | 定义 | 出处 |
| --- | --- | --- |
| Operation | 针对 ReleaseDefinition 发起的一次 INSTALL、UPGRADE、ROLLBACK 或 EMERGENCY 发布动作，具有幂等键、state_version 与终态。（避免：job、deployment task、release task） | CONTEXT.md › Language；设计词汇表：ADR-009, REQ-023 |
| Operation Timeline Sequence | 同一 Operation 内所有时间线事件的严格递增序号，覆盖状态转换、ACK、rollout progress 和 error；它独立于 Operation 的 `state_version`，用于去重、排序和断线续传。（避免：用 state_version 作为 Timeline cursor、event version） | CONTEXT.md › Language；设计词汇表：REQ-023/077 |
| Operation Timeline Retention | 非终态 Operation 的 Timeline 事件不清理；进入终态后至少保留 30 天，供 Watch 回放与审计查询；保留期不是 Watch cursor 过期阈值（后者由 retained_from_sequence 判定，REQ-023 Timeline 不变量 5）。 | CONTEXT.md › Language |
| Operation Snapshot | `WatchOperation` 在连接一致性边界返回的 Operation 权威状态快照，带有对应的 Timeline sequence 边界；它用于初始化或恢复页面状态，不替代 TimelineEntry 历史事件。（避免：initial GetOperation snapshot、latest event） | CONTEXT.md › Language；设计词汇表：REQ-077 |
| TimelineEntry Kind | Operation Timeline 中正式生产的条目 kind 枚举：ACK（operator ACK_PERSISTED 确认，ack_stage=persisted）/ ROLLOUT_PROGRESS（rollout 进度观察，workload_ref/ready/desired）/ ERROR（Operation 失败脱敏摘要，error_code/error_message）；store 常量（ACK/ROLLOUT_PROGRESS/ERROR）到 wire 枚举（TIMELINE_ENTRY_KIND_ACK/ROLLOUT_PROGRESS/ERROR）一对一映射，不再落 UNSPECIFIED；每条独立 sequence，共享 operation_state_version（REQ-077）。（避免：落 UNSPECIFIED kind、把 kind 混入 state 事件、error_message 含敏感原文） | CONTEXT.md › Language；设计词汇表：REQ-077 |
| WorkloadObservation | operator 在标准 Operation（INSTALL/UPGRADE/ROLLBACK）执行中经 `CommandStreamRequest.rollout_progress`（oneof 字段 9）周期上报的逐 workload 进度观察（workload_ref/ready/desired，workload_ref 形如 `<gvr.resource>/<namespace>/<name>`，如 `deployments/app/default`）；观察输入来自 helmengine `Release.Workloads`（RealEngine 从 Helm SDK rel.Manifest 提取的四类 GVR 身份）；采集失败时跳过该条不阻塞 Operation 终态（rollout 观察是增强信息，非终态前置）。（避免：把观察当终态前置、跨多条记录含 Secret/完整 manifest、经 UpgradeResult 回传、workload_ref 用 Kind 大写形式） | CONTEXT.md › Language；设计词汇表：REQ-077 |
| Idempotency-Key | 写请求 HTTP header 幂等键；scope = organization:release_definition；hash = sha256(canonical typed body)。 | 设计词汇表：REQ-058, ADR-009 |

## Values 域

| 术语 | 定义 | 出处 |
| --- | --- | --- |
| ValuesRevision | 绑定到 ReleaseDefinition 的版本化非敏感期望配置快照；主生命周期为 draft → pending_approval → approved\|rejected，approved 可进入 superseded，创建者可将未提交 draft 终结为 discarded。内容创建后不可变；审批使用异人审批、state_version 乐观锁和 Idempotency-Key，禁止自批、撤回和重新激活。（避免：values config、configuration revision、parameter set、物理删除 draft） | CONTEXT.md › Language；设计词汇表：ADR-007, REQ-018 |
| values_digest | release values 内容的不可逆摘要；inventory 契约中 payload/store/log 只允许出现 digest，禁止明文。（避免：values hash、secret digest、加密后的 values） | CONTEXT.md › Language；设计词汇表：REQ-017/054 |
| render_digest | rendered manifest 内容序列的不可逆摘要（REQ-046 输出、REQ-047/ADR-008 preflight 幂等缓存键 (render_digest, capability_version) 的组成部分）；区别于 values_digest（仅 values 内容摘要）。 | CONTEXT.md › Language；设计词汇表：REQ-046 |
| resource summary | RenderResult.Resources 中单条渲染资源的持久化安全身份（api_version/kind/namespace/name）；namespaced kind 缺 namespace 时以渲染 namespace 为默认，集群级 kind（APIService/ClusterRole/ClusterRoleBinding/CustomResourceDefinition/Namespace/Node/PersistentVolume/PriorityClass/StorageClass/ValidatingWebhookConfiguration/MutatingWebhookConfiguration）不填充；Secret data/stringData 与 raw manifest 永不进入（REQ-046，D-25）。（避免：完整 manifest、Secret data、原始 rendered YAML） | CONTEXT.md › Language；设计词汇表：REQ-046 |

## 紧急变更与收敛

| 术语 | 定义 | 出处 |
| --- | --- | --- |
| Emergency Intent | 与单次 EMERGENCY Operation 同事务持久化的强类型集群变更意图，包含稳定 command identity、Workload UID、目标字段锁和收敛策略；它通过在线 Operator stream 投递，但不属于标准 Command Outbox。（避免：emergency command outbox、raw patch payload、best-effort stream message） | CONTEXT.md › Language；设计词汇表：REQ-032 |
| Emergency Target Lock | 非终态 EMERGENCY Operation 或 Unresolved Emergency Effect 对实际 Kubernetes 字段路径持有的互斥声明；image 按 workload/container、replicas 按 workload、annotation 按 workload/key 判定重叠，只有权威执行证据解析后才释放。（避免：Definition-wide emergency lock、action-type lock、Operation 终态即自动解锁） | CONTEXT.md › Language；设计词汇表：REQ-032 |
| Emergency Effect Status | EMERGENCY Operation 的集群效果权威状态枚举：NOT_STARTED（命令可证明未越过不可撤回投递边界）/ UNKNOWN（可能已被 Operator 接收或执行但无权威 Result，保留目标锁）/ APPLIED（权威 Result 证明已写入）/ NOT_APPLIED（权威 Result 或可证明未投递路径确认未写入）；与 Operation state 正交，终态也可保持 UNKNOWN，迟到 Result 解析时 stateVersion 递增并写 EMERGENCY_EFFECT_RESOLVED。（避免：把 effectStatus 等同于 Operation 状态、terminal means resolved） | CONTEXT.md › Language；设计词汇表：REQ-032/058 |
| Unresolved Emergency Effect | EMERGENCY Operation 已进入终态，但权威集群执行结果仍为 `UNKNOWN` 的领域状态；系统在结果解析为 `APPLIED` 或 `NOT_APPLIED` 前继续观察 late Result，并保留对应 Emergency Target Lock。（避免：把 timeout 等同于未生效、terminal means unlocked、unknown result） | CONTEXT.md › Language；设计词汇表：REQ-032/058 |
| Emergency Conflict | ReleaseSummary 的展示摘要字段，表示存在运行中非终态标准 Operation（INSTALL/UPGRADE/ROLLBACK）时，紧急变更被 ADR-011 双向互斥阻塞；携带 operation_id/type/state/started_at。（避免：emergency lock（后者指 EMERGENCY 侧持有的字段锁）、blocking operation） | CONTEXT.md › Language；设计词汇表：REQ-054/058 |
| Emergency stuck lock | Unresolved Emergency Effect 的 EMERGENCY Operation 在终态停留超过可配置观察窗（emergency.effect_observe_timeout，默认 24h）后，其 Emergency Target Lock 进入的派生状态（由查询推导，非存储列）；触发告警+审计，不自动 TTL 解锁，由 release_admin/platform_admin 经 ReleaseEmergencyLock RPC（NOT_APPLIED_PROVEN / AUDITED_OVERRIDE 模式）正式处置。（避免：终态即自动解锁、超窗自动判 NOT_APPLIED、把 stuck 落存储列） | CONTEXT.md › Language |
| emergencyChangeEnabled | 服务端全局运行时 kill switch，控制是否允许新建 EMERGENCY 表单/Operation；字段缺失视为 false。为 false 或缺失时新建 Emergency 入口隐藏或 404，既有 Operation、late Result、Convergence TasksPage 与 ValuesEditor 收敛路径继续可访问；不得用 Vite 构建变量作为业务开关。（避免：feature flag 构建变量、前端本地开关） | CONTEXT.md › Language；设计词汇表：REQ-033/058 |
| operationVersion | REQ-058/REQ-032 输入契约中每个被变更字段携带的版本化快照校验标识：服务端按 action 相关快照（Manifest Inventory / Candidate Artifact / Definition 配置）复检，字段值变化需同步更新；参与幂等 request_hash 计算。（避免：全局操作版本、把 operationVersion 当 request_id） | CONTEXT.md › Language；设计词汇表：REQ-032/058 |
| Convergence Task | 绑定一次已确认 `APPLIED + REQUIRE_PROMOTION` 的 EMERGENCY Operation 的持久原子收敛记录；它包含该 Operation 的全部 Promotion Mapping paths，至多绑定一个 active draft/pending ValuesRevision，只有同一个获批 ValuesRevision 完整吸收全部 paths 后才能进入 converged。一个 ValuesRevision 可同时收敛多个互不冲突的 tasks。（避免：promotion job、convergence job、前端收敛状态、按 path 拆分 task、跨多个 revision 部分收敛） | CONTEXT.md › Language；设计词汇表：ADR-011, REQ-032/058 |
| ConvergenceStrategy | EMERGENCY Operation 的收敛策略类型枚举：REVERT_ON_NEXT_RECONCILE（回退到下次 reconcile，不建 Convergence Task）或 REQUIRE_PROMOTION（创建持久 Convergence Task，需审批收敛）；请求必须显式选择，UNSPECIFIED 由服务端拒绝（ADR-011 / REQ-079 D12/D13）。（避免：服务端隐式选择、把枚举当 optional 字段） | CONTEXT.md › Language |
| Convergence Prepare Session | 服务端持久化的短期收敛准备快照，绑定 actor、Organization、ReleaseDefinition、精确 task set、parent version 与 locked-path hash；15 分钟内可重复读取，创建 ValuesRevision draft 时单次消费，不在消费前持有 Convergence Task 独占锁。（避免：draft revision、task lock、永久 prepare token、URL 中的完整 prepare result） | CONTEXT.md › Language；设计词汇表：REQ-068 |
| Promotion Mapping | ReleaseDefinition 上将受控 Kubernetes workload 字段映射到 Helm values path 的强类型配置，用于生成并验证可收敛的 ValuesRevision；缺少映射的目标只能选择 REVERT_ON_NEXT_RECONCILE。（避免：reverse mapping、values guess、手工收敛提示） | CONTEXT.md › Language；设计词汇表：ADR-011 |
| REQUIRE_PROMOTION | 收敛策略：须创建并批准 ValuesRevision 才标记 converged；APPLIED 后原子创建唯一 task。 | 设计词汇表：REQ-058, ADR-011 |
| REVERT_ON_NEXT_RECONCILE | EMERGENCY Operation 的收敛策略之一：紧急变更在下次标准操作/对账时被吸收覆盖，不创建 Convergence Task、不触发收敛门禁；标准 Operation succeeded 后，后端以实际 applied manifest/inventory 与该 Operation 使用的 approved rendered value 对 Emergency 目标字段对账，相等才标记 reconciled。（避免：当作持久收敛任务、跳过对账、与 REQUIRE_PROMOTION 混淆） | CONTEXT.md › Language；设计词汇表：ADR-011, REQ-058 |
| EmergencyOpType | SET_CONTAINER_IMAGE / SET_REPLICAS / SET_APPROVED_ANNOTATIONS。 | 设计词汇表：REQ-058 |
| AnnotationScope | 紧急 annotation 变更落点的元数据位置枚举：WORKLOAD_METADATA / POD_TEMPLATE_METADATA。实现现状：scope 只决定执行时写入哪一层元数据（`internal/operator/emergency_executor.go:286-291`），**不**进入目标锁重叠判定——锁按 workload + annotation key 判重叠、锁条目仅含 key（`internal/store/sqlite/emergency_intents.go:573-575`、`internal/store/sqlite/emergency_intents.go:578-605`；PostgreSQL 侧同构 `internal/store/postgres/emergency_intents.go:614`），与 CONTEXT.md（Emergency Target Lock）「annotation 按 workload/key 判定重叠」一致；设计词汇表「按 key+scope」为实现未采纳的设计意图，差异裁定见文末 D4。（避免：把 key+scope 当作现行锁语义） | 设计词汇表：REQ-058；CONTEXT.md › Language（Emergency Target Lock） |
| WorkloadKind | DEPLOYMENT / STATEFUL_SET / DAEMON_SET。 | 设计词汇表：REQ-058 |

## Inventory 与审计

| 术语 | 定义 | 出处 |
| --- | --- | --- |
| ReleaseInventory | 中心维护的 release 现场观察缓存，operator 通过 HelmEngine 只读接口上报；唯一键 customer+cluster+namespace+release_name，状态 active/missing/out_of_sync。（避免：inventory cache、release snapshot、部署清单） | CONTEXT.md › Language；设计词汇表：REQ-017 |
| InventorySyncLog | 每次 inventory 同步（full snapshot 或 targeted update）的持久化记录，以 sync_id 唯一约束实现幂等去重与重放应答。（避免：sync record、sync history） | CONTEXT.md › Language；设计词汇表：REQ-017 |
| InventoryStatus | ReleaseInventory 的服务端权威状态枚举：active（快照存在）/ missing（从新快照消失，历史保留）/ out_of_sync（上报 digest 与期望 ValuesRevision 不一致）。（避免：release status、部署状态） | CONTEXT.md › Language；设计词汇表：REQ-017 |
| InventorySyncHint | 成功安装/升级/回滚后由 operator Agent 发送的 targeted inventory 更新触发信号（agent.Result.inventory_sync_hint=true → NotifyOperationComplete → SyncInventory 单 release 快照，sync_id 幂等去重）；非全量快照。（避免：全量快照、独立 RPC、把 hint 当已同步证据） | CONTEXT.md › Language；设计词汇表：REQ-020 |
| Pending Workload Identity | orchestrator 侧持久化暂存的权威 workload identity 上报记录（表 pending_workload_identity，双引擎），当某 release 的 release_inventory 行尚不存在时入暂存、避免丢弃；唯一键 (customer_id, cluster_id, namespace, release_name)，同键再报 upsert 覆盖；该 release 行经 SyncInventory/InventoryStore.Upsert 建立后事件驱动回放绑定到行（kind/name/namespace 一致仅 uid 不同允许更新 uid，其余保留既有 fail-closed），绑定成功即删，TTL 5–10min 兜底清理孤儿。（避免：丢弃 unknown-release report、内存暂存重启即丢、无 TTL 永久占存、直接覆盖既有行身份） | CONTEXT.md › Language |
| AuditEvent | 由统一 AuditEmitter 发射、中央 sanitizer 脱敏后持久化的结构化审计事件，包含稳定 Actor/Organization/Resource/Action/Result/request_id；查询层只能返回服务端已脱敏投影并强制组织过滤（ADR-010，REQ-050/REQ-029）。（避免：raw audit log、未脱敏事件、用业务日志替代） | CONTEXT.md › Language；设计词汇表：ADR-010, REQ-050 |
| AuditActor | 审计事件中的稳定执行者身份，kind ∈ {anonymous, api_key, user, service, system}；api_key/user/service/system 必须携带稳定 id，anonymous 允许空 id 但不得伪造 user/service 身份（REQ-050 输入契约，ADR-010）。（避免：将 actor 与授权 subject 混用、为匿名事件伪造 user/service 身份） | CONTEXT.md › Language；设计词汇表：REQ-050 |
| AuditArchiveObject | 审计归档对象：gzip JSONL 文件 + 同名 .sha256 sidecar，对象名由 operation ID/截止时间/内容摘要确定性生成（同 cutoff 幂等）；先 fsync + 原子发布归档与 checksum，校验成功后才在 DB 事务内条件删除对应事件（ADR-010，REQ-030）。（避免：先删库后归档、首期引入 Parquet/object storage） | CONTEXT.md › Language；设计词汇表：ADR-010, REQ-030 |
| AuditQueryCursor | 审计查询游标（keyset cursor）：分页 page_token 编码为 base64(created_at + "\|" + id)，服务端解码后按 (created_at DESC, id DESC) 边界过滤，保证翻页稳定不重复（REQ-029 AC-03/AC-06，D-62）。（避免：offset 分页、内存分页、把游标当时间戳单独字段） | CONTEXT.md › Language；设计词汇表：REQ-029/059 |
| ExportTask | 审计导出的异步任务，状态 pending → processing → ready\|failed；ready 提供 downloadUrl，failed 提供 errorMessage；前端展示 taskId（审计关联 ID）并手动刷新状态，不缓存导出内容（REQ-059 AC-04）；同筛选条件（AuditQueryFilter）的重复导出请求幂等、不重复创建任务（REQ-029 AC-06）；创建响应 wire 字段为 export_id，导出状态查询 RPC 尚未在已合并 proto 提供（REQ-059 对齐注，D-62）。（避免：export receipt、一次性导出响应、把创建回执当完成） | CONTEXT.md › Language；设计词汇表：REQ-059 |

## 需求治理与质量门禁

| 术语 | 定义 | 出处 |
| --- | --- | --- |
| 原子需求 | 单一业务结果的可实施需求单元，必须包含十章节（目标/影响服务/输入契约/输出契约/状态与数据/错误模型/安全边界/验收标准/非目标/回滚方式），默认与一个 Task 一一对应，并经 reqcheck 质量门禁校验。 | CONTEXT.md › Language；设计词汇表：ADR-000, REQ-039 |
| 不适用标记 (naRe) | 原子需求模板十章节中某章节不适用的显式标记语法：章节内容行以「不适用」开头、同一行附非空原因（不适用<分隔符><原因>）；无原因触发 reqcheck CHK-02；AC-039-01 允许章节以该标记替代实际内容（REQ-039 详细技术规格 naRe 节，D-49 落地）。 | CONTEXT.md › Language |
| SDK Integration Gate | 质量流水线中直接跨生产模块 seam 连接真实外部依赖的集成验证，例如通过生产 `HelmEngine` 访问 kind API Server；它不经过正式 Connect API、Operation 状态机或 Operator 控制流，因此不属于 E2E Stage。（避免：direct SDK E2E、Install E2E、把 integration test 称为 E2E Stage） | CONTEXT.md › Language；设计词汇表：REQ-061~064 |

## E2E 编排与治理

| 术语 | 定义 | 出处 |
| --- | --- | --- |
| E2E Stage | 分阶段端到端测试中的独立可选择验证单元；当前规范名称为 control-plane、inventory、artifact、release、isolation、emergency、restart。（避免：test step、phase test） | CONTEXT.md › Language；设计词汇表：ADR-013, REQ-066 |
| Stage Result | 单个 E2E Stage 的机器可读结果，包含 pass/fail/skip、稳定 root cause、耗时及关联 artifact/operation identity。（避免：test output、stage log） | CONTEXT.md › Language；设计词汇表：REQ-066 |
| Fixture Snapshot | E2E Run 开始或 Stage 前采集的不可变测试基线，包含 Customer、Cluster、ReleaseDefinition、Release、Operator Session 与 Operation 的非敏感状态。（避免：mutable fixture、shared test state） | CONTEXT.md › Language；设计词汇表：REQ-066 |
| BaselineSnapshot | Run 开始时采集一次、全 Run 不可变的测试基线（含各 `e2e-*-target` 的 baseline revision/replicas）；采集后立即原子写 `{output-dir}/baseline.json`，`BaselineDigest` = 其稳定序列化摘要。 | 设计词汇表：REQ-066, D-021 |
| ObservedSnapshot | 需要断言环境变化的阶段（isolation）另行采集的只读快照；不写入、不修改 BaselineSnapshot。 | 设计词汇表：REQ-066 |
| run.json | Run 级汇总（RunID/SelectedStages/BaselineDigest/Pass/Fail/Skip/ExitCode/Fatal?/Stages[]）；CI 唯一解析对象；fatal 字段承载 `fixture_stale`/`snapshot_not_found`；`RunID` CI 下取环境变量 `E2E_RUN_ID`（DNS-1123 沿用 REQ-065，与 dev-up `environment_id=ci-<E2E_RUN_ID>`/诊断目录同源），未设置（local）由 cmd/e2e 自生成（D-026 D9）。 | 设计词汇表：REQ-066 |
| Stage DAG | control-plane→inventory/artifact/emergency/restart、inventory→release/isolation 的依赖图；未选择前置不自动执行，已选前置 fail/skip 传播 `stage_skipped`；`inventory`/`artifact` 是唯一 ParallelSafe。 | 设计词汇表：REQ-066, ADR-013 |
| e2e-runner | REQ-065 devseed 提供的 E2E 专用写账号（角色 release_admin，E2E_RUNNER_PASSWORD 凭据见 data/dev-credentials.env 或 ci Secret env）；REQ-066 全部写阶段与 Operation 归属判定（release_busy/cleanup/takeover）以 actor==e2e-runner 识别，deployer 不可创建 Operation。（避免：用 dev-admin 跑 E2E、把 e2e-runner 当普通开发账号） | CONTEXT.md › Language；设计词汇表：REQ-065, REQ-066, D-015 |
| 启动接管（startup takeover） | 写阶段创建新 Operation 前：目标 Definition 存在 actor == `e2e-runner` 的残留非终态 Operation 时先 `CancelOperation` 取消至合法终态再创建；失败/超时 → fail + `cleanup_timeout`。 | 设计词汇表：REQ-066, AC-066-27 |
| release_busy | 目标 `e2e-*-target` Definition 存在 actor ≠ `e2e-runner` 的非终态 Operation 时写阶段 fail 的错误码。 | 设计词汇表：REQ-066 |
| cleanup_timeout | 写阶段内补偿未在 30s grace 内完成、或启动接管超时追加的 cause；scope = cleanup \| takeover。该 30s 是 fail-fast 上界，**不**是独立 `cmd/e2e cleanup` 恢复命令的预算——后者必须跨越 Run 自身 restart 阶段造成的 agent 重连窗口，取值与理由见 D-032。 | 设计词汇表：REQ-066, D-032 |
| ParallelSafe | `--parallel` 下可并发执行的只读阶段标记；仅 `inventory` 与 `artifact`，写阶段始终串行。 | 设计词汇表：REQ-066 |
| e2e-cleanup | `make e2e-cleanup`：默认读 `{output-dir}/baseline.json`，经无过滤 `ListReleaseInventory` 发现 `e2e-*-target` 残留，仅经正式业务 API 回收（非终态 `CancelOperation`、revision `RollbackRelease`、replicas 正式 `EmergencyChange`，D-023 D9），不直接 patch K8s；`baseline.json` 缺失/不可解析时降级只回收 actor==`e2e-runner` 非终态 Operation，revision/replicas 跳过并在 stderr 告警，不静默（D-026 D4）。载体 = `cmd/e2e cleanup` 子命令（D-029 D1：复用同一 env-config 解析 / Connect clients / 共享锁封装，不引入独立二进制、不 shell/curl 旁路，AC-066-38）。恢复预算（D-032）：必须 ≥ Run 自造 agent 重连窗口 + 一个 emergency operation apply 窗口（当前 3 分钟）；恢复写入须确认 `GetOperation` 终态，未生效可在预算内以新幂等键重发，失败仍以 `residual` 如实上报。结果核实与动作核实分开（AC-066-34）：核实是独立第二阶段，回滚后重读环境再判定——replicas 经只读 observer 计入 `residual_replicas`/`unverified_replicas`，release 重读 inventory 判定是否仍停在 residue，计入 `residual_revisions`/`unverified_revisions`；未判定绝不记为已恢复。 | 设计词汇表：REQ-066, D-023, D-026, D-029, D-032, D-033 |
| residue（run residue / 残留采样） | Run 在全部阶段结束后（不论成败）对每个 release 采样的 revision，落盘 `{output-dir}/residue.json`。它是 cleanup 回滚触发的唯一可靠依据：`RollbackRelease` 前进版本号而非恢复编号（实测回滚到 21 后重读 23、再一轮 24），故「revision ≠ baseline」在回滚后仍成立，会让每次 cleanup 再回滚一次且版本号累加（实测 21→23→24…→31）；`values_digest` 对 Run 的变更不敏感（四个 release 共享同一 digest 而 revision 已变），会把漂移报成已恢复。cleanup 仅在 `observed == residue[definition]`（Run 的残留仍在）时回滚，故既敏感又收敛；`residue == baseline`（Run 未移动它）不参与判定，`run_id` 与 baseline 不一致的 residue 被拒绝使用；缺失时降级为 revision 比较（仍恢复但不收敛，D-033）。 | 设计词汇表：REQ-066, D-033 |
| cmd/e2e cleanup 子命令 | `cmd/e2e` 的 cleanup 子命令 = `make e2e-cleanup` 载体：与 `run`（默认命令）共享 `--env-config` 解析 / Connect clients / `data/dev.lock` 共享锁封装与 Makefile 目标级退出码 3 契约；参数 = `--env-config` + 可选 `--output-dir`（默认 `./e2e-results`）/`--baseline-file`（默认 `{output-dir}/baseline.json`）；不引入独立二进制、不 shell/curl 旁路（D-029 D1，AC-066-38）。 | 设计词汇表：REQ-066, D-029 |
| E2E 观测 RPC | REQ-066 补充的最小只读观测面：仅新增 Orchestrator `ListReleaseInventory`（行附 `active_operation`）+ Operator `GetActiveOperatorSession`；`GetOperation`/`CancelOperation`/`GetBundle` 为既有 API 仅消费；webhook `GetReleaseBundle` 不新增（REQ-011 §652 迁移 + D-022 accepted）。 | 设计词汇表：REQ-066, REQ-011, D-022 |
| fixture_stale 触发边界 | identity 全量比对仅在 `inventory` 阶段、或单独选择 release/isolation 的 fixture guard 执行（比对源 = env-config `seed.expected_identity`）；偏差 → run-fatal `fixture_stale`（退出码 1）。各写阶段运行中只做 `expected_current_revision` 动态读（= `inventory.revision`）+ readiness/operator 复核，不做全量 identity 比对（D-026 D2）。 | 设计词汇表：REQ-066, D-026 |
| run-fatal 不自动补偿 | Run 运行中触发 `fixture_stale`/`snapshot_not_found` 终止（退出码 1）时不做统一自动业务补偿：各写阶段 cleanup 已随阶段结束执行，且基线已失真、自动补偿会基于不可信 baseline 写业务 API；run.json 写 `fatal` 并提示运行 `make e2e-cleanup`，CI post-step `dev-purge` 兜底（D-026 D3）。 | 设计词汇表：REQ-066, D-026 |
| baseline.json 缺失降级 | `e2e-cleanup` 运行时 `baseline.json` 缺失/不可解析（如 run 在 baseline 写入前失败 `snapshot_not_found`、文件被删）：降级只回收残留——无过滤 `ListReleaseInventory` 发现并 `CancelOperation` 全部 actor==`e2e-runner` 非终态 Operation；revision/replicas 因缺恢复目标跳过并在 stderr 告警「baseline.json 缺失，revision/replicas 未恢复」，不静默（D-026 D4）。 | 设计词汇表：REQ-066, D-026 |
| e2e-env-config 私有 target | REQ-066 Makefile 私有 target `e2e-env-config`：从 REQ-065 既有输出组装唯一运行时 env-config `data/e2e-env-config.yaml`（0600，仅 env 名引用、不含 secret 内容）；`e2e-stage`/`e2e-all`/`e2e-cleanup` 共用该产物。`configs/e2e.dev.yaml` 只能作为非 secret 模板/schema fixture，不是第二运行时配置源（D-026 D6、D-031）。 | 设计词汇表：REQ-066, D-026, D-031 |
| control-plane guard（restart 单独选择） | restart 单独选择（`--stages restart`，同 Run 未选 control-plane）时自行执行的 control-plane guard：全部 6 服务 `/health`/`/readyz` 全绿 + Operator Session online + `/environment` 一致且 `production=false`；与 AC-066-14/15/19 的单独选择语义对称（D-026 D8/AC-066-35）。 | 设计词汇表：REQ-066, D-026 |
| env-config 组装 | REQ-066 Makefile target（e2e-stage/e2e-all/e2e-cleanup）从 REQ-065 既有输出组装 `--env-config`：local 可消费 `dev-credentials.env`，ci 消费已注入的 Secret env；`dev-status.json` / `dev-fixture.json` / `kubeconfig.yaml` 等非凭据输入仍按 profile 产出。REQ-065 不产运行时 YAML（D-023 D1、D-031）。schema 见知识库 `Design/contracts/e2e-environment-config.md`。 | 设计词汇表：REQ-066, D-023, D-031 |
| seed.expected_identity | env-config `seed` 块显式导出的 inventory 期望；所有实体数量、Route/Definition 分类与目标 ID 均由 seed manifest 组装时派生，并与 fixture_version 对齐，不硬编码。D-018 未完成需求侧裁决前，manifest 导出值是实现唯一权威，不因早期示例数字扩充 fixture。 | 设计词汇表：REQ-066, D-018, D-023 |
| seed.e2e_upgrade_targets | env-config `seed` 块导出的 UPGRADE 输入（release/isolation/restart 的 bundle_id/values_revision_id，取自 dev-fixture.json definitions 块）；`expected_current_revision` 不在配置内，运行前从 ListReleaseInventory 动态读取（D-023 D6）。 | 设计词汇表：REQ-066, D-023 |
| 控制面 operator gateway（restart 对象） | restart 阶段 patch 的三个控制面 Deployment 之一：`release-manager-control` 集群 8084 的 operator gateway，部署于 `k3d.test_namespace`（D-023 D2/D3）；另两个为 Orchestrator 与 Auth。三 Deployment 目标由 env-config `k3d.restart_targets` 静态绑定导出（D-029 D3，不 label 动态发现）。restart 单独选择（同 Run 未选 control-plane）时自行执行 control-plane guard（6 服务 `/health`/`/readyz` 全绿 + operator Session online + `/environment` 一致且 `production=false`，D-026 D8/AC-066-35）。 | 设计词汇表：REQ-066, D-023, D-026, D-029 |
| environment_locked | `data/dev.lock` 共享锁获取失败（另一 dev-* 进程持排他锁）的启动前错误；退出码 3，stderr 附持有者 PID/started_at；复用 REQ-065 锁契约。实现落点（D-026 D1）：在 Makefile 层经 flock 包装（`flock -s data/dev.lock -c …`）完成——退出码 3 属 Makefile 目标级，cmd/e2e CLI 进程退出码表（0/1/2）不含 3。 | 设计词汇表：REQ-065, REQ-066, D-023, D-026 |
| 上游修复任务（upstream fix task） | AC-066-17 前置 smoke 暴露已合入上游代码功能缺口时新建的承接任务：D-103 四缺口（TASK-080/081）、D-108 两缺口（TASK-082 UPGRADE 执行链路归 REQ-067/019 域、TASK-083 devseed `max_emergency_replicas` 归 REQ-065 域）、D-109 两缺口（TASK-084 UPGRADE identity/typed result/Cancel-Rollback 终态级联归 REQ-082/067/020 域、TASK-085 Emergency kind/uid 身份与 WorkloadUID 下发归 REQ-081/032/079 域）、D-111 三缺口（TASK-086 helmengine `rm_input_digest` label ≤63 归 REQ-021/063/067/084 域、TASK-087 Emergency finish 竞态/终态收敛与目标锁生命周期归 REQ-032/079 域、TASK-088 identity report/inventory result 排序归 REQ-085 域）、D-112 一缺口（TASK-089 postgres `ConvergeEmergencyResult` 推进循环跨引擎漂移对齐 sqlite 归 REQ-087/070/032/079 域）、D-113 一缺口（TASK-090 ROLLBACK 确定性终态驱动与 `executeRollback` 幂等 replay 归 REQ-063/067/019 rollback 域）；各自带单测/契约级验证，合入同一 main 后由 TASK-066 重跑组合 smoke 作最终复核（D-024/D-025/D-027/D-028/D-030）。 | 设计词汇表：REQ-066, D-030, D-103, D-108, D-109, D-111, D-112, D-113 |
| AC-066-17 prerequisite smoke | TASK-066 入口硬门禁的组合冒烟：Upgrade、Rollback、Emergency `SetReplicas`、`CancelOperation`、dev-up/dev-seed、声明 endpoints、seed manifest、Operator enrollment/reconnect、e2e-runner 登录、auth 跨重启 token 前置（`ValidateToken` + `RefreshToken` 成功，D-029 D4）全绿才允许进入实现规划；语义不变，前置条件随上游修复任务合入扩展（D-103/D-108/D-109/D-111/D-112/D-113，D-024/D-025/D-027/D-028/D-030）；复测点 = UPGRADE rev-2 真实创建、Emergency restore 快速连发收敛至合法终态且目标锁可释放、seed 后首个成功操作建立 identity、Rollback 稳定沿合法 `queued → running → succeeded` 终态完成且幂等 replay 不重复真实 Helm rollback。 | 设计词汇表：REQ-066, D-024, D-025, D-027, D-028, D-029, D-030 |
| e2e-*-target | REQ-065 devseed 提供的 4 个 E2E 专用 ReleaseDefinition：`e2e-release-target`/`e2e-isolation-target`/`e2e-emergency-target`/`e2e-restart-target`，均绑定 `dev-customer-a-direct`，供写阶段隔离操作。 | 设计词汇表：REQ-065, REQ-066 |
| k3d.restart_targets | env-config `k3d` 块导出的 restart patch 目标静态绑定：`namespace`（= `k3d.test_namespace`）+ 三个 Deployment 名（Auth / 控制面 operator gateway / Orchestrator），由 REQ-066 Makefile 从 REQ-065 dev-up 产物组装（不硬编码）；RBAC `resourceNames` 与该清单静态一致——不按 label selector 动态发现（label 动态发现与最小权限 patch 角色冲突）；缺失或与实际部署不符时 restart 阶段 fail-closed（D-029 D3，AC-066-39）。 | 设计词汇表：REQ-066, D-029 |
| output-dir 固定产物清理 | 新 Run 进入执行阶段前（参数/配置校验通过、Baseline 采集前）清空 `--output-dir` 内本 Run 将写的固定产物（run.json / 各 {stage}.json / baseline.json），防上一 Run 残留陈旧文件误导人工排查或按目录扫描工具；不删除目录内其他用户文件（D-029 D5，AC-066-40）。 | 设计词汇表：REQ-066, D-029 |
| skip 阶段落盘 | 因依赖传播（同 Run 显式选择的前置 fail/skip）而 skip 的已选阶段同样写独立 `{stage}.json`（`Status=skip`，`RootCause` 记录依赖原因如 `skipped: dependency control-plane fail`）；run.json `Stages` 引用逐一对应——「每所选阶段恒一文件」不变量（D-029 D7，AC-066-41）。 | 设计词汇表：REQ-066, D-029 |
| 运行期日志三面分离（slog） | 运行期日志载体 = stdlib `log/slog`（text handler）写 stderr（阶段进度/非结果诊断/调试）；stdout 仅供人类摘要；结构化结果只进 JSON artifact；不新增第三方 Go 依赖——CI 机器面只解析 run.json、不消费 stderr 文本（D-029 D6）。 | 设计词汇表：REQ-066, D-029 |
| diagnostics 诊断资源路径 | `--keep-on-failure` 且写阶段 fail 时保留诊断资源并落盘 `{output-dir}/diagnostics/{run_id}/{stage}/`（阶段 stderr 缓冲 + 关联 Operation/artifact 引用 JSON + 环境摘要）；随 CI artifact `e2e-results/**` 上传；不收集集群内 Pod 日志（K8s 权限边界：非 restart 仅 get/list/watch）（D-029 D8，AC-066-42）。 | 设计词汇表：REQ-066, D-029 |
| E2E_RUNNER_PASSWORD | `e2e-runner` 开发账号密码的 env 注入通道：local profile 的 Makefile target 可 source `data/dev-credentials.env`，ci profile 使用 REQ-065 已注入的 Secret env；两者均 export 进程环境，`credentials.e2e_runner.password_env` 指向该变量；secret 值仅存进程环境，不落 env-config YAML / 日志 / artifact（D-029 D9、D-031，AC-066-43）。 | 设计词汇表：REQ-065, REQ-066, D-029, D-031 |
| auth 跨重启 token 前置 | 「Auth 重启后旧 access/refresh token 仍有效」（`ValidateToken` + `RefreshToken` 成功）为 restart 阶段 AC-066-03/26 断言的显式运行前置并纳入 AC-066-17 prerequisite smoke；实测 FAIL 按既有上游承接先例归 auth 上游域新建修复任务，REQ-066 不吸收、不放宽断言语义（D-029 D4）。 | 设计词汇表：REQ-066, D-029 |

## 开发环境（dev / TASK-065）

| 术语 | 定义 | 出处 |
| --- | --- | --- |
| Development Fixture | REQ-065 创建并由 REQ-066 消费的版本化、确定性非生产数据集合；稳定 `dev-*` identity 与 canonical 内容共同构成契约，内容漂移时 `dev-seed` fail closed，只有显式 `dev-reset-data` 才可重建。（避免：seed sample、demo data、CUST-A/CL-A1 双重标识） | CONTEXT.md › Language；设计词汇表：REQ-065 |
| Environment Profile | dev 环境双轨：local（写 0600 凭据/密钥文件）与 ci（Secret env 注入、失败/中断自动清理、成功保留供 REQ-066）。 | 设计词汇表：REQ-065 |
| E2E_RUN_ID | ci profile 的全局唯一运行标识（调用方约定唯一，不做运行时注册）；DNS-1123 ≤63 字符，非法即 `e2e_run_id_invalid`。 | 设计词汇表：REQ-065 |
| content-sha256 | 镜像内容寻址 tag：hash 输入 = go.mod/go.sum + 服务 cmd/ + 共享 internal/（+ web lockfile / fixture chart）；digest 相同跳过 build/push。 | 设计词汇表：REQ-065 |
| fixture_version | canonical fixture 的递增版本 `vN`；仅实体结构/语义变更时由维护者递增，字段值级调整不递增；权威 = devseed 内置常量。 | 设计词汇表：REQ-065 |
| Dev Trust Root | dev-only 制品签名信任根：私钥由 devseed 内嵌 crypto/ed25519 生成/复用（0600 `data/dev-trust-root/`，ci 从 env）；公钥经 TrustService 激活。 | 设计词汇表：REQ-065, ADR-017 |
| ownership manifest | `data/dev-ownership.json`：受管 k3d 集群 / Docker 容器 / network 白名单；`dev-down`/`dev-purge` 唯一删除依据。 | 设计词汇表：REQ-065 |
| dev 拆除顺序（teardown order） | `dev-down`/`dev-purge` 的拆除顺序规则：删集群 → 清理对应 kubeconfig 与 ownership 条目 → `docker network disconnect` 先断开 registry 与集群网络的连接 → 删网络（purge 再删 registry 容器与 `data/` 运行时文件）；k3d `registry_up` 会把 registry 接入每个集群网络，直接删网络必失败残留（真实 smoke 发现 ②）。 | 设计词汇表：REQ-065, D-017 |
| notification-sink | dev-only 集群内 Go 服务：接收 Notifier webhook（`POST /webhook`）并提供最近 50 条通知查询；ClusterIP-only，不暴露 NodePort。 | 设计词汇表：REQ-065 |
| Dev Environment State | 环境生命周期状态机：absent → converging → seeding → ready；partial（失败收敛点）/ resetting / destroying / purging。 | 设计词汇表：REQ-065 |
| dev mTLS CA | dev 环境 operator gateway 的 mTLS CA（X.509 证书签发）：dev-up 部署前置生成 data/dev-ca/ 下 CA 私钥与证书（0600，缺失/不可解析生成、存在可解析复用，轮换=删除重跑），经 kustomize secretGenerator 注入 operator 的 ca.key_path/ca.cert_path，使 seed enrollment 前 CA 就绪；ci 从 Secret env DEV_M_TLS_CA_KEY/DEV_M_TLS_CA_CERT 注入不写文件（REQ-015 决策#2 prod Vault / dev 降级本地文件；ADR-017；REQ-065 批次 5 D1）。（避免：与 Dev Trust Root（Ed25519 制品签名密钥）混用密钥体系、Enroll 与 CA 首次生成竞争） | CONTEXT.md › Language；设计词汇表：REQ-065, ADR-017 |
| devseed Idempotency-Key | devseed 对全部创建/提交类写操作（Customer/Cluster/ClusterRoute/ReleaseDefinition/ValuesRevision Submit+Approve/ReleaseBundle/CreateEnrollmentToken/Enroll/bootstrap INSTALL）携带的稳定幂等键，格式 devseed-<phase>-<logical-key>（如 devseed-identity-dev-customer-a），使阶段内部分失败后按 DEV_TIMEOUT_SEED_RETRIES 重试幂等、不产生重复实体（REQ-010 scoped 幂等；REQ-065 批次 5 D9）。（避免：无 key 重试产生重复实体、key 含随机数） | CONTEXT.md › Language；设计词汇表：REQ-065, REQ-010 |
| DEV_TIMEOUT_* | dev 环境确定性超时/重试环境变量（覆盖不落盘）：`DEV_TIMEOUT_READY=300s`、`DEV_TIMEOUT_OPERATOR=180s`、`DEV_TIMEOUT_SEED_RETRIES=3`（指数退避 1s/2s/4s）。 | 设计词汇表：REQ-065 |
| DEV_K3D_NODE_MEMORY / DEV_K3D_NODE_CPU | k3d 节点资源覆盖环境变量；默认管理集群 3GiB/2CPU、客户集群 1.5GiB/1CPU。 | 设计词汇表：REQ-065 |
| DEV_BUILD_PARALLELISM | 镜像构建并行度（1/2/4），默认顺序执行；覆盖仅在当前 dev-up 调用内生效。 | 设计词汇表：REQ-065 |
| readiness probe（dev） | PG/Redis readiness 经 `kubectl exec` 进 pin 镜像 Pod 执行 `pg_isready` / `redis-cli ping`；输出仅用于 Makefile shell target 判定，不进入 E2E 断言。 | 设计词汇表：REQ-065 |
| data/diagnostics | `dev-up`/`dev-seed`/`dev-reset-data` 失败时自动收集的 `kubectl describe`/`get`/`logs`（`data/diagnostics/<ISO8601>/`，0600，stderr 仅摘要）；`dev-purge` 一并清理。 | 设计词汇表：REQ-065 |
| fixture /version 访问通道 | fixture chart `/version` 端点统一经临时 `kubectl port-forward` 访问（devseed verify/冒烟由 dev 脚本提供，REQ-066 同样临时 port-forward）；不启用 operator agent 内 proxy、不暴露 NodePort。 | 设计词汇表：REQ-065 |
| 开发账号（dev accounts） | devseed 创建的 4 个本地账号：dev-admin platform_admin / dev-deployer deployer / dev-reader viewer / e2e-runner release_admin；密码 32 字符 `[A-Za-z0-9]` 落 `data/dev-credentials.env`（0600）或 ci Secret env。 | 设计词汇表：REQ-065 |
| dev.lock 环境锁 | `data/dev.lock` 互斥锁文件：`dev-up`/`dev-down`/`dev-seed`/`dev-reset-data`/`dev-purge` 持排他锁（LOCK_EX）；`dev-status`/`e2e`/`e2e-cleanup` 持共享锁（LOCK_SH）；冲突立即退出码 3 `environment_locked`（stderr 附持有者 PID/started_at）。e2e 侧共享锁实现落点 = Makefile 层 flock 包装（退出码 3 为 Makefile 目标级，cmd/e2e 进程退出码 0/1/2，D-026 D1）。 | 设计词汇表：REQ-065, REQ-066, D-023, D-026 |

## 差异裁定（2026-09-15，按实现证据）

原「待核对差异」D1–D5 已逐条以仓库代码为证据裁定。分类含义：**A｜文档对齐** = 仓库措辞向权威术语修正，无实现分歧；**B｜已记录差异** = CONTEXT 描述设计意图、仓库实现确实不同，保留实现事实并已在条目行内标注证据；**C｜未决** = 证据不能裁定"应当如何"，留下可执行问题。

### D1. Convergence Task 绑定的 ValuesRevision 状态范围 — 裁定：无实质冲突（A）

- 两侧说法：`Notes/CONTEXT.md`「至多绑定一个 active draft/pending ValuesRevision」；`Design/glossary.md`「至多绑定一个 active revision」。
- 代码证据：生产代码唯一的绑定创建点在消费 Prepare Session 时写入 `active_revision_status='draft'`（`internal/store/sqlite/values_lifecycle.go:311`、`internal/store/postgres/values_lifecycle.go:308`）；draft 被丢弃即解绑（`internal/store/sqlite/values_lifecycle.go:170`、`internal/store/postgres/values_lifecycle.go:160`）；`'approved'` 只随任务收敛在同一语句写入（`internal/store/sqlite/convergence_tasks.go:151`、`internal/store/postgres/convergence_tasks.go:142`），且通用改写接口 `BindRevision`（`internal/store/store.go:1371`）在仓库生产代码中没有任何调用方（仅测试使用）。
- 结论：实现与 CONTEXT 一致——进行中绑定是 draft/pending，approved 仅是 converged 的终态戳而非长期驻留绑定；Design 侧「active revision」按此理解。表格条目维持 CONTEXT 定义，仓库侧无进一步改动。
- 遗留：无实现分歧可裁；若需在 `Design/glossary.md` 给「active revision」补限定语，归 REQ-032/058 owner（知识库修改不在本任务范围）。

### D2. Operator Session 生命周期是否包含 revoked — 裁定：已记录差异（B）

- 两侧说法：`Notes/CONTEXT.md` 生命周期 online/suspect/offline（revoked 只出现在 Status Reason 条目）；`Design/glossary.md` online/suspect/offline/revoked。
- 代码证据：`revoked` 是会话**状态**而非状态原因——wire 枚举 `OPERATOR_SESSION_STATUS_REVOKED`（`api/proto/orchestrator/v1/orchestrator.proto:580`）、store 常量 `SessionRevoked`（`internal/store/store.go:660`）、证书吊销级联将会话置为 revoked 并附 reason `certificate_revoked`（`internal/store/sqlite/operator_lifecycle.go:120`、`internal/store/postgres/operator_lifecycle.go:124`）、proto↔store 一等公民双向映射（`internal/orchestrator/operator.go:478-479`、`internal/orchestrator/operator.go:506-507`）；仓库运维文档已按实现记录四态（`docs/runbook.md:168`）。
- 处理：实现现状 ≠ 设计词汇。表格 Operator Session 条目保留 CONTEXT 定义并已在行内标注差异与证据。
- 遗留：**已闭合（2026-09-15）**——`Notes/CONTEXT.md` 的 Operator Session 条目已补入 `revoked`，改为四态。该修订不是改变设计意图，而是消除 CONTEXT.md 的内部矛盾：同一文件里的 **Operator Session Status Reason** 条目一直写着"解释 `online`、`suspect`、`offline` 或 `revoked`"，而生命周期条目只列三态。表格行与实现的依据见上。

### D3. 服务令牌的术语命名（同一 seam 三种名字） — 裁定：文档对齐（A）

- 两侧说法：`Notes/CONTEXT.md` 权威名 Bundle Ingress Service Token；`Design/glossary.md` 另用 Internal Service Token 与 bundle ingress 服务令牌 seam 指同一 seam。
- 代码证据：flag/env 与注释统一写 bundle ingress service token（`cmd/webhook/main.go:62`、`cmd/orchestrator/main.go:898`），通用校验机制命名 `ServiceTokenInterceptor`（`internal/auth/service_token.go:29`）；`Internal Service Token` 在代码与其余文档中零匹配（本词汇表旧条目除外）。
- 处理：三行合并为单条 Bundle Ingress Service Token（CONTEXT 权威名），两个设计侧旧名降为该条目内的别名说明；全仓 grep 确认其余文档（`docs/architecture.md` §3、`docs/api.md` §3.3、`docs/configuration.md`、`SECURITY.md`）本就未使用旧名，无需改动。
- 遗留：`Design/glossary.md` 仍保留两个旧名条目，应由 REQ-011 owner 按本结论合并/改指——知识库修改，不在本任务范围。

### D4. annotation 目标锁的判定粒度 — 裁定：仓库文档已对齐（A）+ 设计意图待确认（C）

- 两侧说法：`Notes/CONTEXT.md`（Emergency Target Lock）annotation 按 workload/key 判定重叠；`Design/glossary.md`（AnnotationScope）锁按 key+scope。
- 代码证据：锁条目结构仅含 `Key`（`internal/store/sqlite/emergency_intents.go:573-575`），重叠判定 = 同 workload（kind+name）且同 annotation key（`internal/store/sqlite/emergency_intents.go:578-605`，PostgreSQL 侧 `internal/store/postgres/emergency_intents.go:614`）；scope 只在执行期选择写入 workload metadata 还是 pod-template metadata（`internal/operator/emergency_executor.go:286-291`），不参与锁判定。
- 处理：实现与 CONTEXT 一致；原表格 AnnotationScope 条目照抄了设计侧「key+scope」的说法，与 CONTEXT 和实现都不符，已改为实现事实并标注差异。
- 遗留（需人工裁定，可执行问题）：REQ-058 owner 需确认「scope 不计入锁重叠」是有意的实现简化还是缺陷——若是有意（同一 key 在 workload metadata 与 pod-template metadata 允许并发变更），回填 `Design/glossary.md` 措辞为 workload/key；若是缺陷，立 REQ 修正 `emergencyIntentsConflict` 的判定键。两种走向不能由仓库代码单方面裁定。

### D5. Fixture Snapshot 与 BaselineSnapshot 的概念边界 — 裁定：分层概念，无冲突（A）

- 两侧说法：`Notes/CONTEXT.md`（Fixture Snapshot）Run 开始或 Stage 前采集、含各实体非敏感状态；`Design/glossary.md` 另设 BaselineSnapshot = Run 开始时采集一次、含 target revision/replicas、写 `{output-dir}/baseline.json`。
- 代码证据：同一类型族的两个层次——领域观察类型 `FixtureSnapshot`（`test/e2e/snapshot.go:14-22`），其身份投影 `SnapshotIdentity` 恰含 CONTEXT 列举的实体维度（customers/clusters/release_definitions/release_inventories/operator_sessions/operations，`test/e2e/snapshot.go:25-37`）；Run 开始的基线工件 `baselineArtifact` **嵌入** `e2e.FixtureSnapshot` 并附 run/environment/fixture_version（`cmd/e2e/main.go:60-61`），由 `collectBaseline` 采集 revision/replicas 后原子写 baseline.json（`cmd/e2e/main.go:223`、`cmd/e2e/main.go:256`）；「Stage 前采集」对应 fixture guard 的身份比对（`test/e2e/stages/inventory.go:73-78`）。
- 结论：二者是「领域概念（CONTEXT 词汇）」与「Run 开始工件载体（设计词汇 BaselineSnapshot/baseline.json）」的分层关系，不是互相矛盾的两说；表格两条目各按出处保留，无仓库改动。
- 遗留：无；若需在 `Design/glossary.md` 将 BaselineSnapshot 显式标注为 Fixture Snapshot 的工件特化，归 REQ-066 owner。

> 事实源：`/home/nd/src/repos/github.com/ndzuki/myNote/Projects/001-release-manager/Notes/CONTEXT.md`（权威）、`/home/nd/src/repos/github.com/ndzuki/myNote/Projects/001-release-manager/Design/glossary.md`（设计投影）；裁定证据为本仓库代码（2026-09-15，分支 task/092-docs-batch3）。

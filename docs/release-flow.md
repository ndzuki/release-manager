# 从接入到发布：完整流程（0 → 1）

本文件回答三类问题：

1. **怎么发版**：把「制品 → 发布定义 → Values 版本 → 操作 → 收敛 → 库存同步」串成一条可执行链路；
2. **bundle 怎么加**：制品的三条 ingress 通道与各自凭据；
3. **根证书 / 新集群**：三套「根」分别是什么、怎么生成，以及把新客户集群接进来的完整步骤。

一次性的控制面部署见 [production-deploy](production-deploy.md)；客户集群侧的 enrollment 细节见
[customer-onboarding](customer-onboarding.md)；控制台逐页操作与截图见 [user-manual](user-manual.md)；
RPC 契约见 [api](api.md)。本文只做「把它们连起来」，不重复细节。

## 0. 先分清对象：谁绑定谁

| 对象 | 一句话 | 关键字段 | 在哪维护 |
| --- | --- | --- | --- |
| **Customer（客户）** | 租户边界 | `id`、`name`（显示名）、`slug`（可读标识） | 控制台「客户」；`orchestrator.v1.CreateCustomer` |
| **Cluster（集群）** | 发布目标与 Operator 运行位置 | `id`、`name`、`customer_id`、路由规则 | 控制台「客户 → 集群」；`CreateCluster` |
| **Operator** | 跑在客户集群里的执行代理 | 一次性 enrollment token、证书身份、心跳 | 控制台「集群 → Operator」 |
| **ReleaseDefinition（发布定义）** | 把一个 Helm release 钉到「客户 + 集群 + 命名空间 + release 名 + chart 名」 | `customer_id`、`cluster_id`、`namespace`、`release_name`、`chart_name`、提升映射 | 控制台「发布定义」 |
| **ReleaseBundle（制品）** | 一次构建的不可变制品快照 | `name`、`chart_ref`、`chart_version`、`chart_digest`、`images[]`、证据引用 | **只能经 ingress 写入**（见 §3），控制台只读 |
| **ValuesRevision（Values 版本）** | 属于某个发布定义的不可变配置 | `document`、`secret_refs`、父版本、审批决策 | 控制台「发布 → Values」 |
| **Operation（操作）** | 一次实际的安装/升级/回滚 | 类型、bundle、已审批 Values 版本、补丁覆盖、状态机 | 控制台「创建操作」/ API |
| **TrustRoot（信任根）** | 制品签名信任的公钥链 | `environment`、`key_id`、`public_key_pem`、`issuer`、`subject_pattern` | 控制台「信任根」；`trust.v1.CreateTrustRoot` |

关系：**Customer 1─n Cluster 1─n Operator**；**ReleaseDefinition** 是发布目标的唯一绑定；**Bundle 与
ValuesRevision 是「输入」**（前者是制品快照，后者是配置快照，都不可变）；**Operation** 把「某个 definition +
某个 bundle + 某个已审批 values 版本 + 可选补丁」组合成一次执行；执行结果与 Helm 实际状态由 Operator 通过
库存同步回写，形成**发布清单**。

> 状态权威是 PostgreSQL；控制面**不直连**目标集群，集群内动作只由 Operator 用 Helm Go SDK 执行
> （见 [architecture](architecture.md)）。

## 1. 一次性准备：控制面与三份「根」

### 1.1 部署控制面

按 [production-deploy](production-deploy.md) 的 §3 创建 **9 个 Secret / 8 类物料**再 apply
kustomize overlay。其中三份私钥（JWT 私钥、mTLS CA 私钥、edge TLS 私钥）**必须异地备份**——自动备份
CronJob 只 dump 数据库，不含任何 Secret。

### 1.2 三套「根」不是一回事

| 名称 | 用途 | 算法/格式 | 存放位置 | 丢失后果 |
| --- | --- | --- | --- | --- |
| **JWT 密钥对** | auth 签发会话令牌，api/orchestrator 只验签 | **Ed25519 PKCS#8**（私钥 119 字节 PEM） | Secret `release-manager-jwt-private` / `-jwt-public` | 全员会话失效，重新登录即可恢复 |
| **mTLS CA** | Operator agent 网关签**operator 叶子证书**；客户侧用它校验网关 | **Ed25519 + `CA:TRUE` 自签**（RSA 会被网关拒绝，orchestrator 起不来） | Secret `release-manager-mtls-ca`（`ca.key`/`ca.crt`） | 所有已接入 Operator 无法再认证，需重签 CA 并全部重新 enroll |
| **edge TLS** | 控制台 HTTPS（`edge` nginx 终止） | 任意（实测 RSA 2048） | Secret `release-manager-edge-tls` | 只影响浏览器信任 |
| **制品签名信任根** | **创建操作时**判定制品可信（`CreateOperation` 用 Bundle 证据对信任根验签） | **Ed25519 公钥 PEM** | 数据库 trust policy（控制台「信任根」录入，可轮换/吊销/宽限） | 创建操作会返回 `verification_unavailable`/不受信任，操作被拒或（非生产）降级为告警 |

生成 JWT 与 mTLS CA（命令取自 [production-deploy](production-deploy.md) §3.1，可原样执行）：

```bash
mkdir -p /tmp/rm-secrets && chmod 700 /tmp/rm-secrets && cd /tmp/rm-secrets

# JWT：Ed25519 PKCS#8 ×1 对
openssl genpkey -algorithm ED25519 -out jwt-private.pem
openssl pkey -in jwt-private.pem -pubout -out jwt-public.pem

# Operator mTLS CA：Ed25519 + CA:TRUE 自签（CN 与实测一致）
openssl genpkey -algorithm ED25519 -out ca.key
openssl req -x509 -new -key ca.key -sha256 -days 3650 \
  -subj "/CN=release-manager-agent-ca" \
  -addext "basicConstraints=critical,CA:TRUE" -out ca.crt
```

**制品签名信任根**在控制台「信任根」页创建（仅平台管理员）：
`环境` + `key_id`（自定义稳定标识）+ `issuer` + `subject_pattern`（可选）+ **Ed25519 公钥 PEM**
（`openssl pkey -pubin -in signer.pub -inform DER -outform PEM` 或由你的签名工具导出）。创建后可
**轮换**（新旧并存宽限期）、**结束宽限**、**吊销**、**退役**；`GetTrustPolicy` 是读接口。

## 2. 把新客户集群接进来（0 → 1，一次性）

前置：控制面的 **Operator 网关**（`8444`）对客户集群出网可达；客户集群有可用 StorageClass（agent 需要 1Gi RWO）。

1. **建客户**：控制台「客户 → 新建客户」，填 `name` 与 `slug`（可读标识）。API 也允许显式给 `id`；
   UI 不填 id 时由服务端生成（见 §8 的标识说明）。
2. **建集群**：客户详情 →「集群」→「新建集群」，填**集群名**（如 `Customer A Direct`），并可填可选的
   **「集群标识」**（如 `prod-west`；留空则由服务端生成 UUID，不便于辨认）。API 形态等价：
   `CreateCluster{id:"prod-west", name:"Prod West", customer_id:"<customer-id>"}`。
3. **配路由规则**（可选但常见）：集群详情 →「编辑」→ 添加镜像/Chart 路由（直连 `/ 透传缓存 / 复制`，
   提供方 + 源前缀 + 目标前缀）。规则决定 Operator 从哪个 registry 拉 chart/镜像。
4. **生成一次性 enrollment token**：集群 →「Operator」→「生成令牌」。
   - `operator_name`：小写 DNS label（1–63，字母数字与 `-`，首尾非 `-`）；
   - `TTL`：5–1440 分钟（默认 60）；每个集群至多一个 pending token；
   - 弹窗只显示**一次**明文 token 与部署命令，关掉就无法再取（服务端只存 SHA-256）。
5. **在客户集群部署 agent**：按 [customer-onboarding](customer-onboarding.md) §1–§3：
   - 导入 `release-operator` 镜像（distroless、nonroot、无 shell）；镜像 tag 与控制面同批发布，取
     `release-operator:<TAG>`（控制面的发布镜像清单见 [production-deploy](production-deploy.md) 的发布记录）；
     本地 dev 由 `make dev-up` 自动构建并推进 registry；
   - apply `deploy/kustomize/customer-agent/base/` 下的 namespace / serviceaccount / rbac / pvc（再按
     Deployment 模板替换 `<CUSTOMER_ID>` / `<CLUSTER_ID>` / `<OPERATOR_NAME>` / `<TAG>`）；
   - 两个 Secret：`operator-enrollment`（token 明文，一次性）与 `operator-gateway-ca`
     （**网关 CA 证书** `.crt`，不是叶子证书；从控制面 Secret `release-manager-mtls-ca` 的 `ca.crt` 导出）；
   - 网络：客户集群 → 控制面 `8444` 出站可达（`nc -zv` + `openssl s_client -CAfile` 双验证）。
6. **确认在线**：Operator 列表出现该 operator 且会话「在线」；agent 日志出现 `operator enrolled`；
   心跳间隔 30 秒。离线时控制面所有依赖 Operator 的读会 fail closed（Values 的 Secret 列表、候选制品等
   返回 503，UI 显式降级，不伪造成功）。
7. **首次库存同步**：Operator 连上后会 `SyncInventory` 上报该集群已装的 release；对应发布随后出现在
   「发布清单」。新集群初始为空是正常的，**第一条 INSTALL 操作成功后才会有行**。

## 3. bundle 怎么加（制品 ingress）

**控制台的「发布 Bundle」页是只读查询**（需按发布定义 ID 过滤），没有上传入口。制品必须经两条带凭据的
ingress 之一写入，凭据互不替代：

### 3.1 通道 A：Harbor webhook（生产推荐）

Harbor 推送事件 → `release-webhook` 的 `POST /webhooks/harbor`（CloudEvents 1.0，`Authorization: Bearer
<HARBOR_SERVICE_TOKEN>`）→ webhook 转发 `BundleService.RecordArtifactEvent`。字段形状见
`api/kulala/webhook.http` 的 `HarborArtifactPush`。

### 3.2 通道 B：CI 直接提交（CI 流水线用）

```bash
curl -sk -X POST "https://<control-plane>:8443/webhook.v1.WebhookService/SubmitReleaseBundle" \
  -H "Authorization: Bearer $CI_API_KEY" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: release-0001" \
  -d '{
    "name": "api",
    "chartRef": "oci://registry.example.com/charts/api",
    "chartVersion": "1.2.3",
    "chartDigest": "sha256:0123…",
    "images": [{"ref": "registry.example.com/app/api:1.2.3", "digest": "sha256:4567…"}],
    "gitCommit": "0123456789abcdef…",
    "pipelineId": "pipeline-789",
    "signature": {"ref": "cosign://…", "digest": "sha256:89ab…"},
    "sbom":      {"ref": "spdx://…",   "digest": "sha256:cdef…"},
    "provenance":{"ref": "slsa://…",   "digest": "sha256:0123…"}
  }'
```

约束（不满足直接 `invalid_argument`，不会入库）：

- `chart_ref` 必须以 `oci://` 开头；
- chart 与镜像的 `digest` 必须是 `sha256:` 形式；证据字段在 ingress 只校验 **ref + digest 形状**（真正的
  信任判定发生在**创建操作**时：`CreateOperation` 调 `trust.Verify`，见 §1.2 与 §6）；
- 源码 allowlist：代码层有 `source_not_allowed` 校验（`internal/orchestrator/bundle_service.go`），但
  **当前没有接线**——`cmd/orchestrator/main.go` 的 `loadSourceRegistries` 直接 `return nil`，即生产上不限制
  来源；接上配置后才会真正拦截（这是一个已知缺口，不要按「已生效」理解）；
- `Idempotency-Key` **建议**带上：带了则同 key 同 body 重放返回首个 bundle；**不带也能提交**（服务端只在
  header 非空时写幂等记录），只是没有重放保护。生产 CI 用的 key 是 `CI_API_KEY`（Secret
  `release-manager-ci-api-key` 的这个键，创建命令见 [production-deploy](production-deploy.md) §3.1）。

### 3.3 通道 C：本地手测

用 `api/kulala/webhook.http`（用 Neovim/Kulala 直接打开该文件；`make api-<service>` 这类只负责打开的
目标已由 TASK-093 移除）。`make api-check` 校验集合与 `api/proto`、dev 端口的一致性。dev 环境下两把 key 在
`data/dev-service-tokens/ci-api-key` 与 `data/dev-service-tokens/harbor-service-token`，**不入库**。
用法与覆盖范围见 [http-collections](http-collections.md)。

写入后用控制台「发布 Bundle」（按发布定义 ID 过滤）或 `BundleService.GetBundle` 读回核对。

## 4. 发布定义：把制品钉到目标

控制台「发布定义 → 新建定义」需要：**客户 ID、集群 ID、命名空间、Release 名称、Chart 名称**；还可设置
`enabled`、`hpa_managed`、`max_emergency_replicas`、`approved_annotation_keys`、提升映射
（`promotion_mappings`）。

客户与集群在控制台里**从列表选择**（下拉显示名称 + 标识/短 ID，选中后显示完整 ID 便于核对；先选客户
才会列出该客户下的集群），不需要手抄 id。API（`CreateReleaseDefinition`）仍然直接传 id，便于脚本化。

## 5. Values 版本与审批

1. 进入「发布清单 → 某个发布 → Values」；
2. 编辑 YAML/JSON（真切换编辑器语言）；右侧是**并排语义 diff**；格式/注释/键顺序变化不算 diff；
3. `Secret 引用` 只保存 namespace 内 Secret 的**名称 / key / 目标路径**（不读也不传 value）；
4. 版本流程：`保存草稿` → `提交` → 审批人 `批准 / 拒绝`。**Revision 一旦创建不可变**；
5. 只有**已批准**的 Values 版本才会出现在「创建操作」的下拉里。

## 6. 创建操作 = 发版

1. 入口：发布清单 →「创建操作」（或操作历史页）；
2. 选择：**类型**（安装 / 升级 / 回滚）、**制品 Bundle**、**已审批 Values 版本**、可选**补丁覆盖**
   （只允许点分路径如 `spec.replicas`；Secret 字段必须用 Secret 引用）；
3. 「检查并确认」→ 右侧常显的最终确认面板复述类型/发布名/集群/制品/配置版本/补丁；**改任何字段都会解除确认**；
   API 直接调用时 `CreateOperation` **必须带 `Idempotency-Key`**（控制台自动生成；与 §3.2 里 SubmitBundle 的
   「可选」不同）；
4. 状态机：`PENDING → PREFLIGHT → QUEUED → RUNNING → SUCCEEDED | FAILED | TIMEOUT`；取消有两条边：
   `PENDING/PREFLIGHT/QUEUED → CANCELLED`、`RUNNING → CANCELLING → CANCELLED`；
   - **制品信任校验在「创建操作」时同步发生**：`CreateOperation` 调 `s.verifier.Verify`（信任根 + bundle 证据），
     不通过直接返回错误，操作不会进入 `PENDING`；`PREFLIGHT` 阶段本身只跑 artifact / render / cluster /
     runtime_pull 等预检（`internal/orchestrator/preflight/` 没有 trust 调用）；
   - 取消：`PENDING / PREFLIGHT / QUEUED / RUNNING` 都可取消；**只有运行中的紧急变更**（EMERGENCY）与
     已经在 `CANCELLING` 的操作返回 `cancel_not_allowed`。取消 ≠ 集群回滚——命令若已越过投递边界，
     可能得到 `cancelled + effectStatus=UNKNOWN`，需继续观察；
5. 下发：中心持久命令 Outbox → Operator 出站流 → 集群内 **Helm Go SDK** 执行 → 进度/结果回传时间线；
6. **回滚**：创建操作选择「回滚」，或 `orchestrator.v1.RollbackRelease{release_definition_id,
   target_revision, expected_current_revision, reason}`（reason 必填）；
7. 成功/失败后：发布清单同步率更新；操作详情有阶段条、预检原始 JSON、时间线。

## 7. 验证、回滚与应急

- **操作详情**：阶段条（预检 → 审批 → 下发 → 收敛）、预检原始响应（可折叠 JSON）、时间线；
- **操作中心**：跨 Release 的非终态操作；终态不在其中；
- **发布清单**：同步率 = 与集群一致 / 总数；空数据或读失败显示 `—` 而不是 0%；
- **紧急变更**（/`emergency`）：针对「常规流程来不及」的场景；确认时选收敛策略
  `需异人审批提升` 或 `下次对账时回退`；其结果会生成**收敛任务**；效果未知且已过观察窗口的进入
  **紧急锁**，需人工确认后释放或显式收敛（见 [user-manual](user-manual.md) §10）；
- **审计**：按组织检索脱敏事件；**导出是占位能力**（只写 `pending` 记录、无消费者，见 [runbook](runbook.md) §10）。

## 8. 客户 / 集群标识：ID、名称与别名（现状与建议）

**数据模型**（`api/proto`）：

- `Customer{id, name, slug}`：`id` 稳定键（UI 建客户时服务端生成 UUID），`name` 显示名，`slug` 可读标识；
- `Cluster{id, name, customer_id}`：常见实现里 `id` 也是 UUID，**没有 alias/slug 字段**。

**显示口径**：页面标题、面包屑、卡片标题用 **name**；URL 与 API 用 **id**。控制台里需要 id 的输入
（发布定义的新建/筛选、Bundle 查询）已经换成**显示名称的列表选择**，不再要求手抄 id。

**能加别名吗**：仍然没有独立的 alias 字段——`Cluster` 只有 `name`（可编辑）与 `id`（生成后不可改），
`Customer` 多了 `slug` 但也不能替代 id。不过控制台不再强迫你面对 UUID：

- **新建集群时可填可读的「集群标识」（可选）**：留空才由服务端生成 UUID，填 `prod-west` 这类标识后，
  列表、发布定义与 API 都用它；
- **发布定义的新建与筛选、Bundle 查询的发布定义**都改成了**列表下拉**（名称 + 标识/短 ID，选中后显示完整
  ID），不再手填 ID。

因此当前的推荐做法：新集群一律给可读标识；存量 UUID 集群用「集群名」做人读主键、需要 id 时从下拉项或
「客户/集群」页面复制。

**仍建议的后续改动**（未实现，可作为新 REQ）：

- 给 `Cluster` 增加 `alias` 字段并允许按 alias 解析（用于把存量 UUID 映射成可读名，且 id 保持不可变）；
- 集群列表/详情提供「复制 ID」按钮；
- 下拉支持按名称搜索（定义多时比原生 select 更好用）。

## 9. 常见失败对照

| 现象 | 原因 | 处置 |
| --- | --- | --- |
| 读接口 503 / 「网络或 Operator 暂不可用」 | 该集群 Operator 离线，控制面 fail closed | 看 Operator 列表与 agent 日志；客户集群出网 8444 |
| `permission_denied` | 角色/组织范围不足（部署者读不到 Bundle/绑定等） | 见 [user-manual](user-manual.md) §14 权限矩阵 |
| `bundle_not_reachable` | bundle 属于其它组织、或被他人定义占用 | 用该组织自己的 bundle，或经平台管理员/service token 提交 |
| `source_not_allowed` | 代码层 allowlist 校验拒绝（当前 `loadSourceRegistries` 返回 nil，未接线时不出现） | 若已接线：把来源加入 allowlist（见 `internal/orchestrator/bundle_service.go` 与部署配置） |
| `invalid_artifact` / `invalid_argument` | 证据 ref 为空或 digest 不是 `sha256:` | 检查 CI 提交体 |
| 创建操作返回 `verification_unavailable` / 不受信任 | 信任根未配置、不匹配，或验签后端不可用 | 信任根页核对 `key_id`/公钥/issuer/宽限期 |
| `invalid_cursor` | 分页游标过期（过滤条件变化会重置） | 重新查询 |
| 维护模式拒绝 | orchestrator 在维护窗口 | 见 [runbook](runbook.md) 维护窗口章节 |
| `schema dirty=true` | 迁移半应用 | 见 [runbook](runbook.md) §2 |

## 10. 文档索引

| 想做什么 | 看哪篇 |
| --- | --- |
| 部署/升级控制面、回滚镜像 | [production-deploy](production-deploy.md) |
| 把客户集群接入 / 排障 / 升级 agent | [customer-onboarding](customer-onboarding.md) |
| 控制台逐页怎么点（含截图） | [user-manual](user-manual.md) |
| RPC 契约、认证、错误约定 | [api](api.md) |
| 配置键与默认值 | [configuration](configuration.md) |
| 本地起环境、夹具、逐服务调试 | [dev-environment](dev-environment.md) |
| 故障处置、巡检、密钥备份 | [runbook](runbook.md) |
| 系统边界与一致性 | [architecture](architecture.md) |
| HTTP 调试集合（bundle 提交样例） | [http-collections](http-collections.md) |

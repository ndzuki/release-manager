# 交付证据与判据（D4/D5：REQ-056 / REQ-058 AC × 代码 + ux-review 冻结清单）

基线：`origin/main` @ `56df3f4`（含本会话 #366–#392 全部合入）。
方法：只读取证（`read` / `grep` / `git log`），逐条 AC 现场核代码与测试；**不采信任务卡自述**。
所有 `文件:行号` 引用均由本次会话读取该行确认；`make check-docs` 只验证「行存在」，不验证「内容相符」，
因此本文件的行号一律以「读到的那一行」为准，读者复核时按引用行就地核对。

范围声明：

- 本文只覆盖 **REQ-056（12 AC）**、**REQ-058（50 AC）**、**`docs/ux-review.md` §4 的 87 条清单**，
  以及任务要求的 **D2 / D10 / D11 / Wave-2 S 项**现状。
- REQ 文档本体（知识库）**未改动**；本文不改任何生成物、不放宽任何门禁、不改动既有测试与断言。
- 环境事实：本机 `make dev-up` 三次卡在基镜像 metadata 解析（TASK-293 `status: blocked`），
  故**浏览器真机走查当前只能在 CI 侧完成**（见 §5.2）。

---

## 0. 结论摘要（先看这一节）

| 对象 | 计数 | 备注 |
| --- | --- | --- |
| REQ-056（12 AC） | ✅ 11 / ⚠️ 1 / ❌ 0 | 唯一未闭合：AC-056-01 的**后端半边**（`non_bundle_image` 无发射者，已在 `errcodes.exceptions.yaml` 跟踪至 2026-12-31） |
| REQ-058（50 AC） | ✅ 38 / ⚠️ 9 / ❌ 1 / ⛔ 2 | ⛔ = AC-058-44/45（决策 D18=C 已延期）；❌ = AC-058-33（已裁定不可达）；**AC-058-27 于 2026-10-17 由 ❌ 改判 ⚠️**——实质需求已由 REQ-087 覆盖（**授权面相反**，且**无 APPLIED 人工解析**入口），字面「resolver capability 门禁 + 人工解析 APPLIED」为 owner 裁决 A1/V2 的显式否决面（见 §0-4 与 §2 该行） |
| `docs/ux-review.md` 87 条 | ✅ 27 / ⚠️ 4 / ⏳ 54 / ⛔ 2 | ⚠️ = 部分修复；⏳ = 按原判据仍成立；⛔ = 证据口径/度量类，非缺陷。**2026-10-17（TASK-325）**：UX-021 ⏳→✅、UX-039 ⚠️→✅ |
| `docs/ux-review.md` 覆盖度 | **62 / 87** | 该文 §4 用「`UX-017/018/019/023`」式简写覆盖 62 条；**25 条只存在于已归档清单**，正文无从核对（见 §3.1） |

四条**与卡面/文档自述相反**的实测更正（本会话重点核对项）：

1. **REQ-056 AC-056-08 不再是缺口**：`RollbackRelease` UI **已交付**并接线
   （`web/src/components/releases/RollbackReleaseDialog.vue`、`web/src/connect/rollback-api.ts:39`、
   `web/src/stores/operationForm.ts:296-303`）。REQ-056 变更记录里的「web 无 rollback UI」**已过期**。
2. **REQ-058 replicas / annotations 提交路径不再是缺口**：`web/src/stores/emergencyChange.ts` 已有
   `replicasValue`（`:157`）与 `annotationScope`（`:158`），
   `web/src/connect/emergency-api.ts:280-285` 会真的发送 `setReplicas` / `annotations`。
   这修正了 `docs/ux-revamp-plan.md` §5.3 T2「三条变更路径全断」的结论。
3. **87 条清单的载体不在仓库**：`docs/ux-review.md` 自述「原始清单归档在知识库
   `Notes/Audit-2026-09-28/web-console-static-ux-audit.md`」，该目录在当前 vault 中**已不存在**；
   本文件的 87 条逐条状态以 vault git 历史中该文件最后一版（`8d85daf`）为准冻结（见 §3.1）。
4. **AC-058-27 的「无 resolution RPC」不是待补实现缺口**（2026-10-17 补测）：原 W4 只看了 store 原语
   `ResolveEmergencyEffect`，漏看了 REQ-087 已合入的 `ReleaseEmergencyLock` 与既有的控制台入口。
   **实质的「受审计证据 ⇒ 解析为 NOT_APPLIED（或保留 UNKNOWN）并按规则释放锁」已交付**：
   `api/proto/orchestrator/v1/orchestrator.proto:1458`、`internal/orchestrator/emergency_stuck.go:127`、
   `web/src/pages/StuckLocksPage.vue`。字面的「release_admin 必被拒 + 人工解析为 APPLIED」是
   2026-09-28 owner 裁决 A1/V2 明确**不建**的面（REQ-027 变更记录：「不加 RPC…若将来确实需要解除流程
   ⇒ 应新开 REQ，不回填本 AC」），因此本版按真值改判 ⚠️ 并撤回 W4 的实现项（详见 §2 该行、§5.1、§5.3）。

---

## 1. REQ-056：AC × 代码证据表（12 条）

| AC | 判定 | 证据（本轮读取确认） | 缺口 / 最小改动落点 |
| --- | --- | --- | --- |
| **AC-056-01** 非 Bundle 镜像前后端拒绝 | ⚠️ 部分 | 前端：`web/src/stores/operationForm.ts:266`（`Patch 引用了 Bundle 外镜像`）+ 错误码映射 `web/src/connect/operation-api.ts:374` | **后端半边缺失**：`internal/**` 无 `non_bundle_image` 产生点，仅在 `errcodes.exceptions.yaml:14` 登记（`expires_at: 2026-12-31`）。落点：`CreateOperation` 的 patch 校验处比照 `internal/orchestrator/service.go:298` 的 bundle 加载结果做镜像归属校验 |
| **AC-056-02** 未批准 ValuesRevision 拒绝 | ✅ 已实现 | 前端只列出已审批项：`web/src/components/operations/OperationForm.vue:99`（`approvedRevisions.length === 0` 时禁用）；成员校验 `web/src/stores/operationForm.ts:219`；测试 `web/src/stores/operationForm.test.ts:202`、`web/src/components/operations/OperationForm.test.ts:138`。后端：`internal/orchestrator/service.go:1582`（`revision_not_approved`） | 轻微不一致：前端消息表的键是 `values_not_approved`（`web/src/connect/operation-api.ts:373`），服务端实际发 `revision_not_approved` ⇒ 该条专属文案不可达（走兜底文案）。可在 `operation-api.ts` 的 `messages` 增加 `revision_not_approved` 键 |
| **AC-056-03** Preflight 失败展示 | ✅ 已实现 | 面板 `web/src/components/operations/PreflightResultPanel.vue`（三态 overall，`:32`）；读失败单独态 `web/src/pages/OperationDetailPage.vue:133`、渲染于 `:357`；测试 `web/src/components/operations/PreflightResultPanel.test.ts`、`PreflightResultPanel.spec.ts` | — |
| **AC-056-04** 幂等（重复点击只建一个） | ✅ 已实现 | 稳定键 + 提交门：`web/src/stores/operationForm.ts:286`（`submitting` 早退）、`:291`（`idempotencyKey ??= crypto.randomUUID()`）；后端去重：`internal/orchestrator/service.go:268-275`（同 scope+key 命中即回既有 operation，`RequestHash` 不同则 `AlreadyExists`）；契约测试 `internal/orchestrator/idempotency_contract_test.go:18`、`:45` | 缺一条「同 key 两次 submit 只发一次 RPC」的前端用例（代码层守卫在，测试未钉住） |
| **AC-056-05** 二次确认取消不发 RPC | ✅ 已实现 | `web/src/stores/operationForm.ts:281`(`cancelConfirmation`) 只回 `step='form'`，不发请求；测试 `web/src/stores/operationForm.test.ts:155`（`does not submit when confirmation is cancelled`） | — |
| **AC-056-06** INSTALL 必填差异 | ✅ 已实现 | `web/src/components/operations/OperationForm.vue:62`（bundle，非 ROLLBACK）、`:87`（valuesRevision，非 ROLLBACK）、`:121`（currentRevision 仅非 INSTALL ⇒ INSTALL 不渲染）、`:142`（targetRevision 仅 ROLLBACK） | — |
| **AC-056-07** UPGRADE 必填差异 | ✅ 已实现 | 同上 `:121-139` renders currentRevision + `web/src/stores/operationForm.ts:223` 校验 | — |
| **AC-056-08** ROLLBACK 走独立 RPC `RollbackRelease` | ✅ 已实现 | 表单只渲染 `targetRevision`（`OperationForm.vue:142-160`），不渲染 bundle/patch（`:62`、`:163`）；派发 `web/src/stores/operationForm.ts:296-303`；独立入口 `web/src/components/releases/RollbackReleaseDialog.vue`（`:8` 注释即指 AC-056-08）+ `web/src/pages/ReleaseInventoryPage.vue:183-189`；API 层 `web/src/connect/rollback-api.ts:39`（`rollbackRelease`）；测试 `web/src/stores/rollbackForm.test.ts:99`（`dispatches ROLLBACK to RollbackRelease and never to CreateOperation`） | 一处细节偏差：`expectedCurrentRevision` 对 ROLLBACK 仍渲染并要求填写（`:121` 的条件是 `!== 'INSTALL'`），而规格表把 ROLLBACK 的该字段列为「禁用」。不影响 AC 通过（服务端 `RollbackReleaseRequest.expected_current_revision` 确实需要），但可考虑对 ROLLBACK 自动带入当前 revision |
| **AC-056-09** 表单草稿恢复 | ✅ 已实现 | 写：`web/src/stores/operationForm.ts:92-107`；读：`:329-351`；过期/失败兜底 `:346-348`；测试 `web/src/stores/operationForm.test.ts:120` | — |
| **AC-056-10** Preflight 轮询停止 | ✅ 已实现 | `web/src/stores/operationTimeline.ts:216`（`stopPolling`）、`:227` 注释即 AC-056-10、调用点 `:420`/`:434`；测试 `web/src/stores/operationTimeline.test.ts` | 措辞指向的孤儿 `web/src/stores/preflight.ts` 仍在，停轮询落在真正被使用的 store 上（REQ-056 变更记录已说明） |
| **AC-056-11** 空态引导 | ✅ 已实现 | `web/src/pages/OperationCreatePage.vue:77`（`store.isEmpty` → `EmptyState`）；`web/src/stores/operationForm.ts:75-77`（`isEmpty` 定义）、`web/src/components/operations/OperationForm.vue:26`（无可审批版本时的说明）；测试 `web/src/components/operations/OperationForm.test.ts:113` | — |
| **AC-056-12** `release_busy` 带已有操作链接 | ✅ 已实现 | 服务端发两个 header：`internal/orchestrator/service.go:183`（`X-Reason-Code: release_busy`）、`:185`（`X-Operation-ID`）；前端读取 `web/src/connect/operation-api.ts:367`、`:370`；门禁测试 `internal/orchestrator/creation_gate_test.go` | — |

**REQ-056 三态计数：✅ 11 / ⚠️ 1 / ❌ 0。**

---

## 2. REQ-058：AC × 代码证据表（50 条）

判定口径：AC 文本以 2026-09-28 两次 `scope-change` **之后的现行文本**为准（AC-058-18 的「漂移」断言已标 `deferred`、
AC-058-24 的要求码已改为 canonical `LOCKED_PATH`）。已由用户裁定「不可达/延期」的条目单列 ⛔。

| AC | 判定 | 证据（本轮读取确认） | 缺口 / 落点 |
| --- | --- | --- | --- |
| **058-01** HPA 阻断（web 展示范围） | ✅ | 服务端拒绝：`internal/orchestrator/emergency.go:724`（`hpa_managed`）；投影 `internal/orchestrator/emergency_queries.go:345`（`HpaManaged`）；测试 `internal/orchestrator/emergency_test.go:749`；前端可用性/校验 `web/src/stores/emergencyChange.ts:201-205` | — |
| **058-02** Annotation 白名单 | ✅ | 四个稳定码：`internal/orchestrator/emergency.go:669`（`duplicate_annotation_key`）、`:675`（`annotation_key_not_allowed`）、`:679`（`annotation_scope_mismatch`）、`:684`（`invalid_annotation_entries`）；测试 `internal/orchestrator/emergency_annotations_test.go:41`；前端白名单 `web/src/stores/emergencyChange.ts:227`；编辑器已接线 `web/src/pages/EmergencyChangePage.vue:237` | — |
| **058-03** REQUIRE_PROMOTION 成功闭环 | ✅ | 受理即建 task：`internal/orchestrator/emergency.go:257`（`Status: "pending_promotion"`）；审批收敛 `internal/orchestrator/values_approval.go:151`（注释即 `AC-058-41 (TASK-166)`）、`:530`（校验 approved document 覆盖 locked paths） | — |
| **058-04** 权限 403（而非 404） | ✅ | 前端 gate 三态含 `forbidden`：`web/src/stores/emergencyAuthorization.ts:64`；服务端 capability 由授权投影下发 `internal/auth/authorization_snapshot.go:110` | — |
| **058-05** Kill switch | ✅ | 前端：`web/src/stores/emergencyAuthorization.ts:39`（`emergencyChangeEnabled === true`）、`:64`（缺 capability ⇒ `not_found`）；服务端字段 `internal/auth/authorization_snapshot.go:121` | — |
| **058-06** 显式 deployer capability | ✅ | `internal/authorization/module.go:374`（`CanExecuteEmergency` 判定）、`internal/authorization/module.go:263`（投影装配）；动作常量 `internal/store/store.go:916`（`release.emergency.execute`） | — |
| **058-07** 授权投影过期 | ✅ | `internal/orchestrator/values_revision.go:170`、`:325`、`:432`、`:463`、`:494`（`authorization_snapshot_stale` fail-closed）；审批侧 `internal/orchestrator/values_approval.go:127` | — |
| **058-08** 标准 Operation 入口冲突 | ✅ | 服务端 `internal/orchestrator/emergency_queries.go:410`（`CheckEmergencyConflict`）；前端整页阻断 `web/src/pages/EmergencyChangePage.vue:170-177`（含 operation 链接） | 未实测「Inventory 行按钮同步置灰且无 N+1」的运行时行为（仅静态：入口受同一 capability 门） |
| **058-09** 目标可用性 | ✅ | `internal/orchestrator/emergency_queries.go:345`（replicas 动作投影）+ 前端 `web/src/stores/emergencyChange.ts:198-205` | — |
| **058-10** 同 repository Artifact | ⚠️ 部分 | 前端确实带上 `operationVersion`（`web/src/stores/emergencyChange.ts:340` 构造、`web/src/connect/emergency-api.ts:237` 发送）；候选加载在 `web/src/components/emergency/EmergencyArtifactSelector.vue` | 未逐条取证「服务端只返回同 logical repository 的 VERIFIED IMAGE」这条过滤链（`internal/` 侧本轮未读到对应断言）；`operationVersion` 是否为 `ListEmergencyTargets` 回填值也需在真机网络面板确认 |
| **058-11** Trust 撤销竞态 | ✅ | `internal/orchestrator/emergency.go:900`、`:903`、`:917`（三条 `artifact_not_trusted` 分支）；测试 `internal/orchestrator/emergency_artifact_trust_test.go` | — |
| **058-12** Reason 校验 | ⚠️ 部分 | 前端必填 + 长度校验（`web/src/stores/emergencyChange.ts:191` `reasonValid`）；**后端**：`ExecuteEmergencyChangeRequest` **无 reason 字段**（`api/proto/orchestrator/v1/orchestrator.proto:1718-1742` 只有 12 个字段，无 reason） | 决策 **D16=A** 已把它划为独立后端承接项；「伪造请求 ⇒ 服务端返回 VERSION_INVALID」这一半当前**不可验证**。补齐后 AC 才完整闭合 |
| **058-13** Annotation 批量边界 | ✅ | 0 条/超量/重复/跨 scope 四个码见 058-02 行；测试 `internal/orchestrator/emergency_annotations_test.go:41`（校验）+ `:133`（正例） | — |
| **058-14** Mapping 完整性 | ✅ | `internal/orchestrator/emergency.go:732`、`:764`（`promotion_not_supported`）；测试 `internal/orchestrator/emergency_test.go:774` | 前端「REQUIRE_PROMOTION 不可选并固定 REVERT」：`web/src/stores/emergencyChange.ts:243-263`（mapping 完整性 + `effectivePolicy`） |
| **058-15** 幂等响应丢失 | ✅ | 服务端按 key hash 回放：`internal/orchestrator/emergency.go:151`（`hashEmergencyIdempotencyKey`）、`:247`（落库 scope+key）；测试 `internal/orchestrator/emergency_test.go:317`（`TestExecuteEmergencyChangeIdempotency`）、`:801`（replicas 同族） | — |
| **058-16** 幂等冲突 | ✅ | 同 scope/key 异 intent ⇒ `idempotency_conflict`（`internal/orchestrator/` 内 13 处发射点，主判在 emergency 幂等分支） | 前端不自动换 key：`web/src/stores/emergencyChange.ts:86` 的 `KEY_INVALIDATING_CODES` 已按 AC-058-16 收窄（`idempotency_conflict` 不在其中） |
| **058-17** Intent 变化换 key | ✅ | 冻结/失效逻辑 `web/src/stores/emergencyChange.ts:630`（freeze）、`:645`（`randomUUID`）、`:653`（关窗保留 snapshot）、`:705`（按码失效）；测试 `web/src/stores/emergencyChange.test.ts` | — |
| **058-18** `operation_version` 形状校验 | ✅ | `internal/orchestrator/emergency.go:502-508`：非空且不匹配 `^v?\d+\.\d+\.\d+$` ⇒ `EMERGENCY_REASON_CODE_VERSION_INVALID`（`CodeInvalidArgument`）；为空跳过 | 原「目标漂移 ⇒ `target_changed`」断言已由 2026-09-28 `scope-change` 显式标 `deferred`；本次不改（`errcodes.exceptions.yaml` 仍跟踪该字面量） |
| **058-19** 异步接受响应 | ⚠️ 部分 | `ExecuteEmergencyChangeResponse` 只回 `result` / `operation_id` / `operation_version`（`api/proto/orchestrator/v1/orchestrator.proto:1744-1748`），`EmergencyResult.requested` 表示「已受理」 | AC 要求响应含 **`acceptedAt`** —— proto 无该字段；也不返回 task id（这一半符合）。落点：要么补 `accepted_at`，要么按已合入契约改写 AC |
| **058-20** 直达结果恢复 | ✅ | 详情页从 `GetOperation` 重建（`web/src/pages/OperationDetailPage.vue`）；EMERGENCY 结果面板 `web/src/components/emergency/EmergencyResultPanel.vue` | 未做真机复测（§5.2） |
| **058-21** Evidence 正交 | ✅ | `effect_status` 与 `state` 在 proto 分列（`api/proto/orchestrator/v1/orchestrator.proto:1811-1824`）；晚到解析路径 `internal/store/sqlite/emergency_intents.go:320` | — |
| **058-22** Late Result 事务 | ✅ | `internal/store/sqlite/emergency_intents.go:320`（`ResolveEmergencyEffect`）+ `:328`（effect 快照写入）；timeline `EMERGENCY_EFFECT_RESOLVED` 见 REQ-057 侧 | — |
| **058-23** UNKNOWN 继续观察 | ✅ | 指数退避 1s→30s + 抖动：`web/src/composables/useEmergencyEffectObservation.ts:4-5`；清理 `:84-86`（TASK-267 速查表亦列）；测试 `web/src/composables/useEmergencyEffectObservation.test.ts` | — |
| **058-24** UNKNOWN 同字段锁 | ✅ | `internal/orchestrator/emergency.go:189-196`（`HasPendingPromotionPath` ⇒ canonical `EMERGENCY_REASON_CODE_LOCKED_PATH`）；另一处 `:967` | 与 2026-09-28 `scope-change`（V9）一致 |
| **058-25** Annotation scope 锁 | ✅ | 同 key 跨 scope 不冲突、同 scope 被拒：`internal/orchestrator/emergency.go:679`（`annotation_scope_mismatch`）；测试 `internal/orchestrator/emergency_annotations_test.go` | — |
| **058-26** UNKNOWN 标准发布门禁 | ✅ | `internal/orchestrator/service.go:244`（`emergency_effect_unresolved` + `UnresolvedOperationIds`/`ConvergenceTaskIds`）；ROLLBACK 同门 `internal/orchestrator/rollback.go:91` | — |
| **058-27** UNKNOWN 人工处置权限 | ⚠️ 部分（2026-10-17 改判，原判 ❌） | **实质半条已交付 = 受审计证据 ⇒ NOT_APPLIED（或保留 UNKNOWN）并按规则释放锁**：RPC `ReleaseEmergencyLock`（`api/proto/orchestrator/v1/orchestrator.proto:1458`；消息 `:1905-1922`；`ReleaseMode` `:1867-1878`）+ handler `internal/orchestrator/emergency_stuck.go:127`（reason 必填 ≤1000 / evidence ≤500 / mode 枚举），审计含 who/when/mode/evidence/reason + before/after effect（`:217`）；授权 `release_admin/platform_admin + active binding`（`:45-50`），Connect 面 `internal/auth/procedure_policy.go:167`；非管理员被拒 `internal/orchestrator/emergency_stuck_handler_test.go:253`。**前端入口已存在**：路由 `web/src/router/index.ts:148`、页 `web/src/pages/StuckLocksPage.vue`、store `web/src/stores/stuckLocks.ts:21`、client `web/src/connect/stuck-lock-api.ts:84` | **字面半条不存在，且是 owner 裁决的显式否决面（不是待补缺口）**：`api/proto/**` 无任何 `Resolve*Emergency*` RPC（`grep` 空）；`ResolveEmergencyEffect` 只有 store 原语 `internal/store/sqlite/emergency_intents.go:320`，非测试调用者 **0**（4 处全在测试：`internal/store/sqlite/emergency_store_test.go:172`、`:191`、`:213`、`internal/orchestrator/service_test.go:2685`），且原语**只改 effect + 写 timeline，不释放锁、不建/撤收敛任务**（`:381-441`）⇒ 单独接一个 RPC 也补不齐本 AC。`release.emergency.resolve` 只有授权投影（`internal/auth/authorization_snapshot.go:111`、`internal/authorization/module.go:375`）**无 RPC 消费**（前端只把 `canResolveEmergency` 映射进快照模型、无 resolver 动作：`web/src/connect/emergency-api.ts:47`、`:176`）；REQ-027 变更记录（2026-09-28 A1/V2）：「**不加 RPC**…若将来确实需要解除流程 ⇒ 应**新开 REQ**（含新 RPC + 前端入口），不回填本 AC」。且 REQ-087 反向规定 `ReleaseEmergencyLock` 供 **release_admin** 使用（AC-087-08/09），与「release_admin 必被拒绝」冲突 ⇒ 补齐需知识库裁决，非本仓实现 |
| **058-28** Queued 取消不确定性 | ⚠️ 部分 | 取消路径 `internal/orchestrator/service.go:1067`（`CancelOperation`）存在；`cancel_not_allowed` 于 `:1102` | 未取证「cancelled 后 `effect_status` 保持 UNKNOWN、锁保留、页面继续观察」的联合断言（需一条 queued→cancelled 的 effect 测试） |
| **058-29** Running 取消拒绝 | ✅ | `internal/orchestrator/service.go:1102`（`cancel_not_allowed`）；前端按状态隐藏 Cancel（`web/src/components/operations/CancelOperationDialog.vue` + 详情页条件） | — |
| **058-30** APPLIED 后单 task | ✅ | 建 task 唯一入口 `internal/orchestrator/emergency.go:257`；重放走幂等分支不重复建（`internal/orchestrator/emergency_test.go:317`） | — |
| **058-31** NOT_APPLIED 无 task | ✅ | 同事务删除 pending task：`internal/store/sqlite/emergency_converge.go:249-259`（`abandonConvergenceTaskTx`，`WHERE operation_id = ? AND status = 'pending_promotion'`）；双引擎同构见 `internal/store/postgres/emergency_intents.go` | — |
| **058-32** REVERT 无门禁 | ✅ | REVERT 不建 task（建 task 分支只在 APPLIED+REQUIRE_PROMOTION：`internal/orchestrator/emergency.go:257` 上下文）；标准门禁只查 `pending_promotion`（`internal/orchestrator/service.go:248-253`） | — |
| **058-33** REVERT 实际对账 | ❌ 已裁定不可达 | 第 1 层已交付（store 列 + `SaveRevertReconciliation`，见 REQ-058 变更记录 2026-09-27）；第 2 层比对需要集群字段观测能力 | 用户裁决 **D-R / R-1**：当前架构不可达，转独立评估卡 TASK-154。**不计入本轮验收面** |
| **058-34** 前端预判与服务端裁决 | ✅ | 服务端裁决：`convergence_conflict` 发射点（`internal/` 内 6 处）；前端预判与冲突渲染：`web/src/stores/convergenceSelection.ts`；测试 `web/src/stores/convergenceSelection.test.ts:44`、`:89` | — |
| **058-35** URL 安全 | ✅ | 只传 mode + opaque token：`web/src/pages/ValuesEditorPage.vue:45-46`（`mode`/`prepareToken` 从 query 读） | — |
| **058-36** Prepare 可读多次/创建单次 | ✅ | 过期判定 `internal/orchestrator/values_revision.go:130`；消费冲突 `:443-444`（`prepare_token_consumed`） | — |
| **058-37** Prepare 过期 | ✅ | `internal/orchestrator/values_revision.go:130`（`ExpiresAt` 比较）+ `prepare_token_expired` 发射点（`internal/` 内 6 处） | — |
| **058-38** Draft 不自动提交 | ✅ | `CreateValuesRevision` 只建 draft（REQ-068 契约，服务端实现于 `internal/orchestrator/values_revision.go`）；前端显式 Submit 由 `web/src/components/values/ValuesRevisionActions.vue:68` 独立按钮承载 | — |
| **058-39** 显式 Discard | ✅ | `internal/orchestrator/values_revision.go:280-283`（`DiscardValuesRevision`）+ `:467`（`discard_not_allowed`） | — |
| **058-40** Reject 解绑 | ⚠️ 部分 | 绑定关系以 revision 的 `ConvergenceTaskIds` 表达（`internal/orchestrator/values_approval.go:484-485` 读取该字段） | **未找到** Reject 事务里清除 binding 的显式语句或专属测试 ⇒ 「tasks 保持 pending_promotion 且 active binding 清空」属推论，需一条 reject→task 可重新 Prepare 的测试钉住 |
| **058-41** Approve 收敛 | ✅ | `internal/orchestrator/values_approval.go:151`（注释直指 AC-058-41）、`:530`（canonical document 必须覆盖全部 locked paths，否则整次回滚） | — |
| **058-42** 多 task 上限 | ✅ | 服务端 `internal/orchestrator/prepare_sessions.go:24`（`maxTaskIDs = 50`）、`:48-50`（越界拒）；前端 `web/src/stores/convergenceSelection.test.ts:112`（越量选择被拒） | — |
| **058-43** 门禁优先级 | ✅ | `internal/orchestrator/service.go:238-253`：先 `emergency_effect_unresolved`，再 `release_convergence_pending`；授权类门在其前（同函数上文 `authorization_snapshot_stale` 分支） | — |
| **058-44** 稳定分页 | ⛔ 非目标 | 决策 **D18=C**：本轮无 cursor 载体，前端本地分页 | AC 已由用户裁定延期，不阻塞验收 |
| **058-45** Invalid cursor | ⛔ 非目标 | 同上 | 同上 |
| **058-46** 未绑定 Definition | ✅ | 前端对无 `releaseDefinitionId` 的行不渲染入口并给出说明：`web/src/components/releases/ReleaseInventoryTable.vue:110`（`title="未关联 ReleaseDefinition，紧急变更入口不可用"`） | — |
| **058-47** Convergence 路由权限 | ✅ | `web/src/pages/ConvergenceTasksPage.vue:105-108`（`ForbiddenState` + 403 文案，`canCreateValuesRevision`） | — |
| **058-48** Display projection | ⚠️ 部分 | 前端只消费服务端投影（`EmergencyTypedValues` / `EmergencyResult`，`api/proto/orchestrator/v1/orchestrator.proto:1811-1835`） | proto 无 `reason_display` 字段，且 Execute 无 reason（见 058-12）⇒「reason 被标记敏感后只渲染 masked projection」这一半**无载体**。落点：随 D16 的 reason 字段一并补 |
| **058-49** 性能容量 | ⚠️ 部分 | Web 侧 TTI 断言已落地：`web/e2e/emergency-smoke.spec.ts:36-40`（预算 `E2E_TTI_BUDGET_MS` 默认 1500）、`:124-125`（断言）；该 spec 已进 CI（`.github/workflows/test.yml:460` 注释列出 rollback + emergency-smoke） | 服务端 p95 三档仍**无验证载体**（决策 D19=A 归 REQ-065/066 + 指标）；「≤20 并发/同字段互斥」也只在服务端实现层，无观测断言 |
| **058-50** Canonical 契约门禁 | ⚠️ 部分 | 旧形态已零匹配：全仓无 `EmergencyChange(payload, actor)`，只有 typed `ExecuteEmergencyChange`（`api/proto/orchestrator/v1/orchestrator.proto:1411`） | **没有独立 fixture 工件**（`find` 未见 REQ-058 契约 fixture）；现由 `web/src/gen/**` + `vue-tsc`/`npm run build` 隐式承担「symbol 缺失即失败」。落点：若要严格满足 AC，补一个引用必需符号的编译 fixture 并进 `make web-check` |

**REQ-058 三态计数：✅ 38 / ⚠️ 9 / ❌ 1 / ⛔ 2（共 50）。**

---

## 3. `docs/ux-review.md` 的 87 条清单冻结

### 3.1 载体与条数核实（**「87」这个数字本身要打折**）

| 事实 | 证据 |
| --- | --- |
| 87 条清单**不在仓库内** | `docs/ux-review.md:20-21` 自述归档在知识库 `Notes/Audit-2026-09-28/web-console-static-ux-audit.md`；该目录**在当前 vault 中不存在**（`ls` 无此目录） |
| 原文可从 vault git 历史取回 | `git show 8d85daf:Projects/001-release-manager/Notes/Audit-2026-09-28/web-console-static-ux-audit.md` 取回 **289 行 / 87 条 `UX-\d{3}` 行**（本次冻结即基于此版本） |
| `docs/ux-review.md` 正文只覆盖 **62 / 87** | 该文 §4.1 用 `UX-017/018/019/023` 这类简写合并成 20 行；把简写展开去重后为 62 个 id，**25 个 id 只在归档清单里**（`UX-009/045/046/047/048/049/051/052/053/054/062/065/066/067/068/070/071/072/077/078/079/082/083/086/087`） |
| 严重度分布（原始口径） | blocker 4 / major 36 / minor 36 / nit 11 = 87 |

> ⇒ **冻结结论**：87 条是有效的问题集合，但**「`docs/ux-review.md` 的 87 条」这一说法不成立** ——
> 该文只有其中 62 条；要逐条追踪必须引用归档清单（或把 25 条补进仓库文档）。这是本文件存在的主要理由之一。

### 3.2 逐条冻结表（87 条）

状态定义：**✅ 已修**（原判据不再成立）· **⚠️ 部分**（主症状消失、原判据有一条未满足）·
**⏳ 待修**（原判据仍成立）· **⛔ 非目标**（证据口径/度量类，不属缺陷）。

| id | 原严重度 | 状态 | 冻结证据 / 落点 |
| --- | --- | --- | --- |
| UX-001 | blocker | ✅ | CI 已有 `web` job：`.github/workflows/test.yml:673`（`npm ci`/lint/test/build）；本地等价 `make web-check`（`Makefile:413`） |
| UX-002 | blocker | ✅ | `web/src/components/operations/PreflightResultPanel.vue:32`（`overallView` 三态 + unknown 中性色）；测试 `PreflightResultPanel.test.ts` |
| UX-003 | blocker | ✅ | `web/src/components/values/ValuesRevisionActions.vue:68`（`:disabled="approving \|\| submitDisabled"`）；`:16` 注释直指 UX-003 |
| UX-004 | blocker | ✅ | 统一 `AppDialog`：`web/src/components/common/AppDialog.vue:67`（Teleport）+ `useFocusTrap`；全仓 **17 个文件**引用 |
| UX-005 | major | ✅ | 已无原生 `window.confirm`（仅注释：`web/src/pages/ClusterDetailPage.vue:35`、`web/src/pages/OperatorEnrollPage.vue:54`） |
| UX-006 | major | ⏳ | `web/src/pages/ReleaseInventoryPage.vue:80` 仍是 `onActivated`，全仓无 `KeepAlive` ⇒ 该分支仍是死代码（挂载由 `:71` 的 watch immediate 覆盖） |
| UX-007 | major | ⚠️ | 已有退避 + 预算：`web/src/composables/useOperatorPolling.ts:55`（`operatorPollingRetryDelayMs`，上限 `:52`）、`:86`（预算用尽才停）、`:105`（成功清零）、`:109`（由失败计数派生 `stopped`）、`:114`（`resume()`，TASK-325）。**已做**「把自动刷新状态暴露给 UI」：`web/src/components/operators/OperatorPollingNotice.vue` 在列表页与详情页各呈现一处（`role="status"` + 恢复按钮）；**仍缺**「最后更新时间」 |
| UX-008 | major | ⏳ | key 仍只有 `values_draft:<releaseDefinitionId>`：`web/src/stores/valuesEditor.ts:27`、`:45-47`；无时间戳/TTL/scope。`setItem` 已进 try（`:189`、`:213`） |
| UX-009 | major | ⏳ | `web/src/components/operators/EnrollmentTokenModal.vue:52-53` `copy()` 仍无 try/catch、无失败反馈 |
| UX-010 | major | ✅ | `web/src/pages/AuditPage.vue:103-127` 收进单条 `v-if/v-else-if` 链，有数据时降级为页内 warning（`:126`） |
| UX-011 | major | ⏳ | `web/src/pages/AuditPage.vue:68-76` 仍 deep watch → `syncQuery()`，无 debounce |
| UX-012 | major | ✅ | `web/src/pages/AuditPage.vue:107`（`permission_denied` 分支）→ `ForbiddenState` |
| UX-013 | major | ✅ | `web/src/pages/OperationDetailPage.vue:133`（`preflightFailure`）→ 渲染 `:357-361`；注释即 TASK-281 |
| UX-014 | major | ⏳ | `web/src/pages/OperationDetailPage.vue` 仍无 `Forbidden` 分支（`grep Forbidden` 零命中） |
| UX-015 | major | ✅ | `web/src/connect/error-copy.ts:50`（读 requestId）、`:65-69`（`correlationLine`）；≥10 处调用点（如 `web/src/pages/OperationDetailPage.vue:157`） |
| UX-016 | major | ⏳ | `web/src/stores/releaseInventory.ts:103` 非 `Unavailable` 时仍返回 `connectError.rawMessage` |
| UX-017 | major | ⏳ | 全仓仍无 `<caption>`（`grep "<caption"` 零命中）；`scope="col"` 已补（如 `web/src/components/releases/ReleaseInventoryTable.vue:63`） |
| UX-018 | major | ⚠️ | 行可点仍是 `tabindex="0"` + 仅 Enter：`web/src/components/audit/AuditEventTable.vue:62`、`:64`；但 `:focus-visible` 已可见（`:165`） |
| UX-019 | major | ⏳ | `web/src/components/audit/AuditEventDetail.vue` 无 Esc 处理（`grep Escape/onKeydown` 零命中） |
| UX-020 | major | ⏳ | `web/src/components/operations/TimelineEntryItem.vue:46` 仍 `tabindex="0"`；`OperationTimeline.vue` 无 `aria-live` |
| UX-021 | major | ✅ | 已修（TASK-325）：`open` 由 false→true 时复位 `confirmed`（`web/src/components/customers/DisableCustomerDialog.vue:29-34`），`cancel()` 的复位保留（`:36-40`）⇒ 「成功关闭后仍勾选」不再成立；测试 `web/src/components/customers/DisableCustomerDialog.test.ts:32`（关闭后重开断言未勾选 + 确认键重新 disabled） |
| UX-022 | major | ⏳ | `web/src/components/operators/RevokeOperatorDialog.vue:19` 由空 reason 直接算 `violation`（`:50` 立即渲染 `role="alert"`） |
| UX-023 | major | ✅ | `web/src/components/emergency/EmergencyChangeForm.vue:59-60`（`aria-describedby` 含计数器 id，不再并入 label） |
| UX-024 | major | ⏳ | `web/src/components/operations/PatchOverrideEditor.vue:44` 仍 `props.errorIndex === index` 门控 ⇒ 无 index 的错误不渲染 |
| UX-025 | major | ⏳ | `web/src/components/operations/PatchOverrideEditor.vue:23` 仍 `:key="index"` |
| UX-026 | major | ⏳ | `web/src/components/operations/OperationForm.vue:78` 仍是 `<option :value="null">` |
| UX-027 | major | ✅ | 字段错误统一走 `FormField` 的 `describedBy`（`web/src/components/common/FormField.vue:58` 起；`web/src/components/operations/OperationForm.vue:72`、`:97`、`:133`、`:154`） |
| UX-028 | major | ⏳ | `web/src/components/clusters/RouteRuleEditor.vue:104` 的 provider `FormField` 无 `:error` ⇒ provider 违规仍不渲染 |
| UX-029 | major | ⏳ | `web/src/pages/InitPage.vue:58` `minlength="12"`，页面仍无密码规则提示 |
| UX-030 | major | ⏳ | `web/src/pages/OperatorEnrollPage.vue:154-155` 仍 `min="0" max="1440"`，与应用层 0-或-5–1440 规则不一致 |
| UX-031 | major | ⏳ | `web/src/components/operators/OperatorTable.vue:45` 仍 `white-space: nowrap`（仅 `small` 在 `:47` 复位） |
| UX-032 | major | ⏳ | `web/src/pages/ClusterDetailPage.vue:121` 三列 grid，无 media query |
| UX-033 | major | ⏳ | `web/src/components/emergency/EmergencyResultPanel.vue:102` `.result-grid dd` 仍无 `overflow-wrap` |
| UX-034 | major | ⏳ | `web/src/components/releases/ReleaseInventoryTable.vue:110`、`:114` 仍用不可聚焦 `<span title=…>` 承载阻断/未绑定原因 |
| UX-035 | major | ⏳ | `web/src/pages/ConvergenceTasksPage.vue:104` LoadingState 仍只覆盖 `gate === 'loading'` |
| UX-036 | major | ⏳ | `web/src/pages/ConvergenceTasksPage.vue:61-62` 仍 `catch { parentVersion.value = 0n }` |
| UX-037 | major | ⏳ | 三个状态组件仍用固定 id：`web/src/components/common/EmptyState.vue:22`、`web/src/components/common/ErrorState.vue:24`、`web/src/components/common/ForbiddenState.vue:22`（`AppDialog`/`FormField` 已用 `useId`，但状态组件未改） |
| UX-038 | major | ⏳ | `web/src/components/values/ValuesCodeEditor.vue:116` 仍只在宿主 div 上 `aria-label`；extensions 无 `history()`/`keymap` |
| UX-039 | major | ✅ | 已修（TASK-325，前一条 TASK-177/299 已迁 `AppDialog`）：Escape 仅在**没有明文可失**时可用——`:close-on-escape="escapeClosable"`（`web/src/components/operators/EnrollmentTokenModal.vue:94`），判据 `!generating && !plaintext`（`:39`）；成功分支仍以两个勾选为唯一出口。测试 `web/src/components/operators/EnrollmentTokenModal.test.ts:114`（失败分支 Esc 关闭）与 `:131`（成功分支 Esc 不关闭） |
| UX-040 | major | ✅ | 已接线：`web/src/pages/EmergencyChangePage.vue:237` 渲染 `EmergencyAnnotationEditor` |
| UX-041 | minor | ⏳ | 破坏性操作成功仍无可见反馈：`ClusterDetailPage`/`CustomerDetailPage`/`OperatorListPage`/`OperatorDetailPage` 均未引入 Toast（Toast 消费者只有 `DefinitionsPage`/`LocalUsersPage`/`OrganizationPage`/`ValuesEditorPage`） |
| UX-042 | minor | ✅ | `web/src/pages/OperationDetailPage.vue:230` 声明、`:380` 已绑定 `ref="cancelTrigger"` |
| UX-043 | minor | ⏳ | `web/src/pages/OperationDetailPage.vue:455` 仍在模板里 `.then(...)` |
| UX-044 | minor | ✅ | `web/src/components/operations/CancelOperationDialog.vue:38`（`close-on-escape="!submitting"`） |
| UX-045 | minor | ⏳ | `web/src/components/operations/DisconnectBanner.vue:6` 仍 `role="status"` + `aria-live="assertive"` |
| UX-046 | minor | ⏳ | `web/src/components/operations/OperationTimeline.vue:47` 只有 `historyGap` 带 `role="alert"`；500 条截断文案仍无 `role="status"` |
| UX-047 | minor | ⏳ | `web/src/components/operations/TimelineEntryItem.vue:8` 仍单个 `copied` 供两个按钮共用（`:81`、`:90`） |
| UX-048 | minor | ⏳ | `web/src/components/operations/TimelineEntryItem.vue:30` catch 块只清 timer，无失败提示 |
| UX-049 | minor | ⏳ | `web/src/components/values/ValuesEditorSkeleton.vue:6` 仍无 `role` |
| UX-050 | minor | ✅ | 全局兜底：`web/src/styles/base.css:62`（`prefers-reduced-motion` 覆盖所有动画）+ 门禁 `web/src/styles/a11y-baseline.test.ts:141` |
| UX-051 | minor | ⏳ | `web/src/components/operations/PatchOverrideEditor.vue:29,33,39` 仍只有英文 `aria-label`，无可见 label |
| UX-052 | minor | ✅ | `web/src/components/operations/OperationForm.vue` 已无 `aria-label`（grep 零命中） |
| UX-053 | minor | ⏳ | 表单仍只在 submit 校验、无 blur 校验、提交后不聚焦首个错误字段（`OperationForm.vue` 无 `blur`/`focus()`） |
| UX-054 | minor | ⏳ | `web/src/components/customers/CustomerForm.vue:27` `canSubmit` 仍只要求 name 非空 |
| UX-055 | minor | ⏳ | `web/src/composables/useSessionExpiry.ts:20-21` `delay` 仍无 2^31-1 clamp |
| UX-056 | minor | ⏳ | 两条过期入口仍在（`web/src/connect/client.ts` 的 401 路径 + `useSessionExpiry`） |
| UX-057 | minor | ⏳ | `web/src/pages/ReleaseInventoryPage.vue:120` 仍 `@click="inventory.refresh"`（传 MouseEvent），且刷新/加载更多标志未统一 |
| UX-058 | minor | ⏳ | `web/src/components/audit/AuditFilters.vue:17` 仍对 `model.actor` 写嵌套属性 |
| UX-059 | minor | ✅ | 分页控件由共享基元承载（`web/src/components/common/Pagination.vue`，`<nav>` 命名区域）；`web/src/components/audit/AuditEventTable.vue:89` 使用它 |
| UX-060 | minor | ✅ | `web/src/components/common/Pagination.vue:58`（`pageLabel` 计算，渲染于 `:79` 的 `role="status"`） |
| UX-061 | minor | ⏳ | `web/src/components/releases/ReleaseStatusBadge.vue:14` `default` 仍渲染 `release.status.active` |
| UX-062 | minor | ⏳ | `web/src/components/values/ValuesRevisionActions.vue:100-104` 仍无 `.status--discarded` 样式 |
| UX-063 | minor | ⚠️ | 已收编两处：`web/src/components/audit/AuditExportPanel.vue:21`、`web/src/components/emergency/EmergencyConvergenceCard.vue:38` 用 `statusLabel`；`web/src/components/operations/TimelineEntryItem.vue:46` 仍 `kindLabel[entry.kind] ?? entry.kind` 直出 |
| UX-064 | minor | ⏳ | `web/src/components/customers/CustomerHistory.vue:17` 仍机械 `replace()` |
| UX-065 | minor | ⏳ | `OperationTimeline`/`CustomerHistory`/`ConvergenceTaskList` 仍无分页或虚拟滚动（grep 零命中） |
| UX-066 | minor | ⏳ | `web/src/components/common/AppShell.vue` 仍无 skip-link（grep 零命中） |
| UX-067 | minor | ⏳ | `web/src/components/common/OrganizationSwitcher.vue:45` 仍静态 `aria-describedby` |
| UX-068 | minor | ⏳ | `web/src/pages/LoginPage.vue:27`、`web/src/pages/InitPage.vue:24` 仍把 `error.message` 原文上屏 |
| UX-069 | minor | ✅ | `web/src/pages/HomePage.vue` 已重写为工作台（入口栅格 + 待办聚合），不再是欢迎 EmptyState |
| UX-070 | minor | ⏳ | 组件更大：`web/src/pages/OperationDetailPage.vue` **481** 行（原 321）、`ValuesEditorPage.vue` 288、`EmergencyChangePage.vue` 301 |
| UX-071 | minor | ⏳ | `formatTimestamp` 仍各自一份：`web/src/pages/OperationDetailPage.vue:297`、`web/src/pages/OperationListPage.vue:50`、`web/src/pages/OrganizationPage.vue:193`、`web/src/pages/StuckLocksPage.vue:88`、`web/src/pages/TrustPolicyPage.vue:87`；仅 operator 侧抽了 `web/src/utils/operator-format.ts:24` |
| UX-072 | minor | ⏳ | `web/src/pages/ValuesEditorPage.vue:183`（`server-issue`）与 `:186`（`validation-message`）仍双份，无 `aria-describedby` 关联 |
| UX-073 | minor | ⏳ | `web/src/components/releases/ReleaseInventoryTable.vue:174-175` 仍 `overflow-x: auto` + `min-width: 58rem`；页面 media query（`web/src/pages/ReleaseInventoryPage.vue:218`）只改 header/filters |
| UX-074 | minor | ✅ | `web/src/components/common/EmptyState.vue:11` 默认标题已走 `t('state.noData')` |
| UX-075 | minor | ✅ | `web/src/components/common/ForbiddenState.vue:11-12` 默认文案已走 i18n |
| UX-076 | minor | ⏳ | 仍混用：`web/src/components/customers/CustomerHistory.vue:34`、`web/src/components/audit/AuditEventTable.vue:32`、`web/src/components/audit/AuditEventDetail.vue:25` 用 `toLocaleString()` |
| UX-077 | minor→nit | ⏳ | `new Intl.DateTimeFormat(...)` 仍在每次调用的函数体内：`web/src/components/operations/TimelineEntryItem.vue:16`、`web/src/components/releases/ReleaseInventoryTable.vue:54` |
| UX-078 | nit | ✅ | `OperatorEnrollPage.vue` 模板内已无 `fieldViolations.find`（grep 零命中） |
| UX-079 | nit | ⏳ | `web/src/pages/OperatorDetailPage.vue:101` 仍在模板内 `Object.keys(...)` |
| UX-080 | nit | ⏳ | `web/src/pages/OperatorEnrollPage.vue:164` 提交中仍只 disabled、文案不变 |
| UX-081 | nit | ⏳ | `web/src/pages/ValuesEditorPage.vue:151` language `<select :disabled="readOnly">` |
| UX-082 | nit | ✅ | z-index 已令牌化：`web/src/components/common/AppDialog.vue:98`、`:108`（`--z-dialog-backdrop`/`--z-dialog`） |
| UX-083 | nit | ✅ | `web/src/components/common/AppDialog.vue:67`（`<Teleport to="body">`） |
| UX-084 | nit | ⚠️ | **未引入 vue-i18n**（`web/package.json` 无 `vue-i18n`），但按决策 **G5** 改为「单一语种 + 文案集中化」：`web/src/i18n/messages.ts` + `copy-lint` 门禁（`web/src/i18n/messages.usage.test.ts:598` 的 `NOT_YET_MIGRATED` 台账仍在，收敛未完） |
| UX-085 | nit | ✅ | `web/package.json:25` 与 `web/package-lock.json` 同为 `^5.2.2`；CI 改用 `npm ci`（`.github/workflows/test.yml:690` 注释即指这条漂移）⇒ 漂移由门禁拦住。**本机未装 `node_modules`，未实测** |
| UX-086 | nit | ⛔ | 属**证据口径**问题（`web/dist` 是构建产物、不能当首屏证据），`docs/ux-review.md:257` 已带 `check-docs:ignore` 说明；无需修复 |
| UX-087 | nit | ⛔ | 属**度量任务**（build 后看 chunk 划分），非缺陷；当前无 `manualChunks` 配置即为其现状记录 |

**冻结计数：✅ 27 / ⚠️ 4 / ⏳ 54 / ⛔ 2（共 87）。**

按原严重度回看：4 条 blocker **全部** ✅ 或 ⚠️（UX-001/002/003 ✅、UX-004 ✅）⇒ **没有残留 blocker**。
剩余 54 条 ⏳ 中 blocker 为 0 条、major 为 24 条（3–5 节据此排序）。

---

## 4. 交付判据（两组，均由本文提出；Lead 认可即生效）

### 4.1 机检判据（可脚本化，无人参与）

| # | 判据 | 如何验证 | 谁来验证 | 本机可验？ |
| --- | --- | --- | --- | --- |
| M1 | 前端三关全绿：lint + test + build | `cd web && npm run lint && npm test && npm run build`（等价 `make web-check`，`Makefile:413`） | CI `web` job（`.github/workflows/test.yml:673`）/ 本机 | 可验（需 `make web-install` 一次） |
| M2 | 仓文档事实门禁 | `make check-docs` | CI `docs-check` job / 本机 | 可验 |
| M3 | 任务台账门禁 | `make check-tasks` | CI / 本机 | 可验 |
| M4 | 错误码可发射门禁（REQ-056 AC-01 的缺口就登记在此） | `make check-error-codes`（例外条目须带未过期 `expires_at`） | CI / 本机 | 可验 |
| M5 | 迁移与双引擎一致性 | `make check-migrations` | CI / 本机 | 可验 |
| M6 | 许可与依赖 | `make check-licenses` | CI / 本机 | 可验 |
| M7 | 聚合门禁（含 M1–M6 + `make test`/`lint`/`sdk-check`） | `make quality`（等价 `make quality-vault REQS_DIR=<vault>/Requirements`，`Makefile:697`/`:701`） | CI 全量 / 本机（需 Node） | 可验（`make quality-vault` 需 vault 路径） |
| M8 | 浏览器 E2E（含跨角色 emergency-smoke 与 TTI 预算断言） | CI `e2e-prerequisite` job：`make e2e-prerequisite-ci`（`Makefile:201`）会起真实 k3d 栈并跑 `smoke.sh` + `console.sh` + Playwright；spec 清单见 `.github/workflows/test.yml:460` | CI（**仅 CI 可验**，见 5.2） | **不可验**（本机 `dev-up` 卡在基镜像 metadata 解析，TASK-293） |
| M9 | 全链路 e2e | CI `e2e` job：`make e2e-all`（仅 push / workflow_dispatch） | CI | 不可验 |
| M10 | 生成物与契约同步 | CI `proto-check`：`make proto` 后 `git status --porcelain -- api/gen web/src/gen` 必须为空 | CI | 可验 |

### 4.2 人工核判据（一次浏览器真机走查，逐条留证）

| # | 判据 | 如何验证 | 谁来验证 | 本机可验？ |
| --- | --- | --- | --- | --- |
| H1 | **能登录**：凭证提交后进入受保护页，`Set-Cookie` 存在且 4 秒内 URL 离开 `/login` | 浏览器 + DevTools Network；对照 `docs/ux-review.md` B1 的复现口径 | 人工（或 CI Playwright） | 仅 CI |
| H2 | **关键页可达**：客户 → 集群 → Release 清单 → 操作创建 → 操作详情，每步都有可见入口（不靠手输 URL） | 逐页点击；对照 `docs/ux-revamp-plan.md` §5.1 N1–N10 | 人工 | 仅 CI |
| H3 | **危险操作有确认**：禁用客户/集群、撤销 operator、替换/吊销 token、回滚、解除卡锁 —— 都必须出现自定义对话框（非原生 `confirm`），且确认框可 Esc/backdrop 关闭（提交中除外） | 逐个点击 + 键盘 Esc | 人工 | 仅 CI |
| H4 | **失败原因可见**：制造一次权限不足（403）、一次读取失败、一次服务端拒绝，页面须分别给出可区分文案 + `requestId` 可复制，且不得同时出现「错误」与「空结果」 | DevTools 改请求/断网；对照 UX-010/013/014/015 | 人工 | 仅 CI |
| H5 | **焦点与可访问名**：任一对话框打开时 Tab 循环不出框、关闭后焦点回到触发按钮；表单字段错误有 `aria-describedby` | 键盘 + a11y 快照 | 人工 | 仅 CI |
| H6 | **紧急变更有入口且可用**：Release 清单行内「紧急变更」在 kill switch 关闭时不渲染；打开时有阻断/候选/表单，且能用 image / replicas / annotations 三条路径各提交一次 | 点入口 + 提交；对照 REQ-058 AC-01/02/05 | 人工 | 仅 CI |
| H7 | **收敛闭环**：从待收敛任务 Prepare → ValuesEditor 锁定路径只读 → Create draft → 异人 Submit/Approve → task converged | 双账号（devseed 第二管理员，TASK-270 夹具） | 人工 | 仅 CI |

> 判据使用方式：**M1–M10 是「不发生回归」的底线**（机器判），**H1–H7 是「服务真正可用」的验收面**（人判）。
> 二者都过才可对某条 REQ 声明 `delivered`；只有 M 组过 ⇒ 只能说「未回归」。

### 4.3 与 REQ 交付状态的关系（本文件不修改 REQ）

按 `requirement-lifecycle` 的证据门禁，REQ 置 `delivered` 需**逐条 AC 有证据**。因此：

- **REQ-056**：AC-056-01 仍 ⚠️ ⇒ 保持 `accepted`，**不置 `delivered`**（与 REQ 正文现状一致）。
- **REQ-058**：仍有 ⚠️9 / ❌1 ⇒ 保持 `accepted`，**不置 `delivered`**。
- ⛔ 的 AC-058-44/45 与 ❌ 的 AC-058-33 属**用户已裁定**的延期/不可达，计数时应单列，不计入「未达标」。

---

## 5. 剩余工作清单（按价值排序，可落卡）

排序依据：**是否在「服务真正可用」的关键路径上** > 阻塞面 > 成本。

### 5.1 关键路径（不做则控制台的核心承诺不成立）

| # | 一句话 | 证据 / 落点 | 估计 | 前置依赖 |
| --- | --- | --- | --- | --- |
| W1 | **浏览器真机走查 wave**（H1–H7）：本机 `dev-up` 不可用，只能在 CI/有栈环境做 | TASK-293（`status: blocked`）；判据见 §4.2 | **L**（大头是起栈与清理） | 需要可用 dev 栈 |
| W2 | REPLACE 级：**AC-056-01 后端半边**——`CreateOperation` 补 `non_bundle_image` 产生点 | `errcodes.exceptions.yaml:14`（2026-12-31 到期）；前端已就绪 `web/src/connect/operation-api.ts:374` | **S** | 无 |
| W3 | **AC-058-48/12 的 reason 载体**：Execute 请求补 `reason` 字段 + 服务端校验 + `reason_display` 投影 | `api/proto/orchestrator/v1/orchestrator.proto:1718-1742`（无 reason）；决策 D16=A | **M** | 契约变更（改 proto + 双端） |
| W4 | ~~**AC-058-27 resolution RPC**（原判「需契约 + 实现 + 前端接线」）~~ **2026-10-17 撤回**：实质已由 REQ-087 交付，字面半条为裁决否决面 ⇒ 不是实现项 | `api/proto/orchestrator/v1/orchestrator.proto:1458`；`internal/orchestrator/emergency_stuck.go:127`；`web/src/router/index.ts:148`；否决依据见 §2 的 `058-27` 行 | **0**（无需实现） | 若仍要字面 AC ⇒ 需知识库新开 REQ（含 RPC + 前端入口），**不回填 AC-058-27** |
| W5 | **AC-058-19 `accepted_at`**：响应补字段或按已合入契约改写 AC | `api/proto/orchestrator/v1/orchestrator.proto:1744-1748` | **S** | 契约变更 |
| W6 | **AC-058-10 同 repository 过滤取证**：补一条服务端断言（或确认已有测试名） | `web/src/connect/emergency-api.ts:237`；服务端过滤链本轮未取证 | **S** | 无 |
| W7 | **AC-058-28 联合断言**：queued→cancelled 后 `effect_status` 仍 UNKNOWN、锁保留 | `internal/orchestrator/service.go:1067`、`:1102` | **S** | 无 |
| W8 | **AC-058-40 Reject 解绑测试**：钉住 reject 后 task 可重新 Prepare | `internal/orchestrator/values_approval.go:484-485`（仅有读取） | **S** | 无 |
| W9 | **AC-058-50 编译 fixture**：把「必需符号缺失即失败」变成显式工件 | 现由 `web/src/gen/**` + `vue-tsc` 隐式承担 | **S** | 无 |

### 5.2 体验债（UX 冻结表里的 major，按用户可感知度）

| # | 一句话 | 证据 / 落点 | 估计 | 关键路径？ |
| --- | --- | --- | --- | --- |
| W10 | 破坏性操作成功无反馈（4 页） | UX-041；Toast 消费者仅 4 页 | **S** | 间接（H3 相关） |
| W11 | ~~统一确认/对话框残留：`EnrollmentTokenModal` 的 Esc、`DisableCustomerDialog` 的勾选复位~~ **2026-10-17 已交付（TASK-325）**：两条均改为 ✅（见 §3.2 的 `UX-021`/`UX-039` 行）；专项回归见 §9 | UX-039、UX-021 | **0**（无需再做） | 间接（H3 相关） |
| W12 | 表单错误可见性：无 index 的 patch 错误、provider 违规、blur 校验与提交后聚焦 | UX-024、UX-028、UX-053 | **S** | 否 |
| W13 | 假阳性显示：`ReleaseStatusBadge` 未知状态显示为 Active | UX-061 | **S** | 否 |
| W14 | 键盘/读屏：状态组件固定 id、Audit 详情无 Esc、时间线 500 个 tab 停靠点、表格 caption | UX-037、UX-019、UX-020、UX-017 | **M** | 间接（H5 相关） |
| W15 | 列表容量：三处无分页/虚拟滚动 + Inventory 表窄屏横滚 | UX-065、UX-073、UX-035 | **M** | 否 |
| W16 | 请求风暴：Audit 每次按键 `router.replace`、Inventory 刷新未去重 | UX-011、UX-057 | **S** | 否 |
| W17 | 双份/漂移：时间格式（`toLocaleString`）与 `formatTimestamp` 复制 6 份 | UX-076、UX-071、UX-077 | **M** | 否 |
| W18 | 巨型组件再拆分（`OperationDetailPage` 481 行） | UX-070 | **M** | 否 |
| W19 | 草稿安全：Values 草稿无 scope/TTL/时间戳 | UX-008 | **S** | 否 |
| W20 | 会话/剪贴板边界：`useSessionExpiry` 无 clamp、`copy()` 无失败反馈 | UX-055、UX-009 | **S** | 否 |
| W21 | 设计令牌收尾：`ClusterDetail` 三列无断点、`OperatorTable` nowrap、`EmergencyResultPanel` 溢出未换行 | UX-032、UX-031、UX-033 | **S** | 否 |
| W22 | 中文收敛（`NOT_YET_MIGRATED` 台账） | UX-084；决策 **G5/D9 延后** | **M** | 否（已裁定延后） |

### 5.3 明确不落卡（记录理由，防重复评估）

- **UX-086 / UX-087**：度量口径与度量任务，不是缺陷（§3.2 已给理由）。若要首屏体积基线，另立测量卡。
- **AC-058-44/45**：决策 D18=C 已延期，等后端 cursor 落地再启用。
- **AC-058-33**：已裁定「当前架构不可达」，归 TASK-154（独立评估）。
- **AC-058-27 的字面「resolver capability 门禁 + 人工解析为 APPLIED」**：2026-09-28 owner 裁决 A1/V2 已明确
  **不建 RPC**（REQ-027 变更记录：「不加 RPC…若将来确实需要解除流程 ⇒ 应新开 REQ，不回填本 AC」）；
  实质的受审计解锁由 REQ-087 `ReleaseEmergencyLock` 交付（证据见 §2 的 `058-27` 行）⇒ 不再作为剩余工作重复评估。

---

## 6. D2 / D10 / D11 与 Wave-2 S 项现状（各一句结论 + 证据）

| 项 | 结论 | 证据 |
| --- | --- | --- |
| **D2** 最小只读聚合 RPC | ✅ **已暴露**：`ListNonTerminalOperations` 已是 proto 一等 RPC，含聚合行消息 | `api/proto/orchestrator/v1/orchestrator.proto:1538`（rpc）、`:2002`（请求）、`:2018`（`NonTerminalOperationSummary`，行内带 cluster） |
| **D10** 已审批 ValuesRevision 选择器 | ✅ **已交付**：不再手输 UUID，改为只列 approved 的下拉 + 空/加载/失败三态 + 成员校验 | `web/src/components/operations/OperationForm.vue:93-113`；`web/src/stores/operationForm.ts:62`、`:219`；测试 `web/src/components/operations/OperationForm.test.ts:88` |
| **D11** Trust Root 建/轮换 UI | ✅ **已交付**：create/rotate 的 client 与页面/对话框均接线 | `web/src/connect/trust-api.ts:176`（create）、`:195`（rotate）；`web/src/pages/TrustPolicyPage.vue`；对话框 `web/src/components/trust/TrustRootEditor.vue` |
| Wave-2 S · `window.confirm` | ✅ **已清零** | 全仓仅注释（`web/src/pages/ClusterDetailPage.vue:35`、`web/src/pages/OperatorEnrollPage.vue:54`） |
| Wave-2 S · `DefinitionsPage` 手写对话框 | ✅ **已收编到 `AppDialog`**（3 处） | `web/src/pages/DefinitionsPage.vue:369`、`:480`、`:542` |
| Wave-2 S · `OperationDetailPage` 静默失败 | ✅ **已修**：preflight 读失败单独态 + ErrorState + 重试 + 关联行 | `web/src/pages/OperationDetailPage.vue:133`（`preflightFailure`）、`:357-361` |
| Wave-2 S · `useOperatorPolling` 永久停 | ✅ **已修且提示已补**：失败改为指数退避重排，预算用尽才停，成功清零、focus/visibility 可再触发；预算耗尽时页面显示「自动刷新已停止」（`role="status"`）并给出恢复按钮（TASK-325） | `web/src/composables/useOperatorPolling.ts:50`、`:86`、`:105`、`:109`、`:114`；`web/src/components/operators/OperatorPollingNotice.vue` |
| Wave-2 S · `requestId` 上屏 | ✅ **已上屏**：错误映射带 `requestId`，`correlationLine` 输出 `code · requestId`，经 `ErrorState` 的 `details` 折叠展示；≥10 处调用 | `web/src/connect/error-copy.ts:50`、`:65-69`；`web/src/components/common/ErrorState.vue:28-31`；`web/src/pages/OperationDetailPage.vue:157` |

---

## 7. 文档落点与理由

- **落点**：`docs/delivery-evidence.md`（本文件）。
- **理由**：`docs/` 已是本仓库「可验证断言」的正式区（`AGENTS.md`「文档即断言」），`make check-docs` 会扫描到它；
  同类交付/证据类文档已在此处（`docs/ux-review.md`、`docs/ux-revamp-plan.md`、`docs/testing.md`），
  与之并列便于交叉复核；**不放进 `docs/decisions/`**（那里是 ADR，本文不是决策），
  **也不新建目录**（避免为单篇文档增一层 IA）。

---

## 8. 没做 / 不确定（如实列出）

1. **浏览器真机走查未做**（H1–H7）。本机 `make dev-up` 三次卡在基镜像 metadata 解析，
   TASK-293 因同一原因 `blocked`。因此 H 组判据**当前仅 CI 可验**；我只做了静态与单测层取证。
2. **未跑本机门禁**：本轮只读 + 新增一份 markdown；未执行 `npm`（本机 worktree 无 `node_modules`）、
   未跑 `make quality`。文档最终以 `make check-docs` 收口（见 §9）。
3. **UX-085 未实测**：本机没有 `web/node_modules`，`node -p require('js-yaml/package.json').version` 不可执行； <!-- check-docs:ignore web/node_modules 本机未安装依赖，此处是「没有它」的否定陈述 -->
   结论基于 `web/package.json:25` 与 `web/package-lock.json` 一致 + CI `npm ci`（`.github/workflows/test.yml:690` 注释即该漂移）。
4. **REQ-058 若干条只做了「有实现」级取证，未做「行为等价」级取证**：058-10（同 repository 过滤）、
   058-20/21（真机直达恢复与 evidence 正交渲染）、058-22（Late Result 时序）、058-28（queued 取消的 effect 保持）、
   058-40（Reject 解绑）。这些我标了 ⚠️ 并写了落点，**不当作已验收**。
5. **AC-058-49 的服务端 p95 无任何可跑载体**：三档 p95（500ms / 1s）在仓库内没有断言或指标门禁，
   只有 web 侧 TTI 断言（`web/e2e/emergency-smoke.spec.ts:124-125`）。我按 D19=A 记为 ⚠️。
6. **87 条清单的第二来源已消失**：`Notes/Audit-2026-09-28/` 目录在当前 vault 不存在，本文件用 git 历史版本
   `8d85daf` 冻结。若 vault 侧恢复该目录，应以恢复版本再对一次（条数与 id 集合应一致：87 / `UX-001`–`UX-087`）。
7. **未核对的字段级事项**：AC-056-02 的前端消息键 `values_not_approved` 与服务端 `revision_not_approved`
   命名不一致（§1），我未改代码，也未把它算作 AC 不通过。
8. **AC-058-27 的「字面半条」我未实现，且判断不该实现**（2026-10-17 补测）：`api/proto/**` 无任何
   `Resolve*Emergency*` RPC；唯一相关 store 原语 `ResolveEmergencyEffect` 的非测试调用者为 0，
   且它既不释放锁、也不建/撤收敛任务（`internal/store/sqlite/emergency_intents.go:381-441`）
   ⇒ 单独接一个 RPC 也不足以闭合本 AC。而 owner 裁决 2026-09-28 A1/V2 已明确「**不加 RPC**…若将来确实
   需要解除流程 ⇒ 应新开 REQ，不回填本 AC」（REQ-027 变更记录），REQ-058 自身也写「不提供 resolver UI」
   （REQ-058 `:915`、`:995`）。因此本次只按真值改判状态，**未新增契约 / 服务端 / 前端代码**；
   若仍要字面闭合，前置是知识库裁决（新 REQ），不是本仓改造。

---

## 9. 门禁

- `make check-docs`：**PASS**（本文件所有 `文件:行号` 引用与路径均已就地读取确认）。
- 未改动 `web/**`、未改动任何测试/断言/生成物 ⇒ 无需触发前端三关与 `make proto`。
- **2026-10-17 修订（AC-058-27 取证更正）**：本次同样只改本文件，未动 Go/TS/proto/测试/生成物；
  新增的 `文件:行号` 引用均已就地 `read`/`grep` 确认；复跑 `make check-docs` **PASS**。
- **2026-10-17 同 worktree 基线门禁**（`origin/main` @ `da7244c`；**基线测量，非本次改动的专项回归**）：
  `make lint` 0 issues、`go test -race ./...` 全绿（70 包有测试 / 27 包无测试文件 / 0 `FAIL`，exit 0）、
  `make lint-proto`、`make sdk-check`、`make check-licenses`、`make check-store-surface`、
  `make check-dead-methods`、`make check-schema-parity`、`make check-migrations` 均 exit 0；
  `make check-error-codes` / `make check-reqs` / `make check-tasks` 需 `REQS_DIR=<vault>/Requirements`
  （缺省报「no REQ documents found」，是环境配置而非失败），传入后三者均 exit 0
  （`errcodecheck`：135 个断言错误码可发射 / 7 例外；taskcheck：306 卡 0 violation）。
- **2026-10-17 修订（TASK-325：UX-021 + UX-039 体验债）**：本次改动 `web/**` 两个组件 + 两处测试
  （`DisableCustomerDialog` 的勾选复位、`EnrollmentTokenModal` 的失败分支 Esc）。门禁在**本卡自己的 worktree**
  （基于 `origin/main` @ `f805596`）内量：`cd web && npm run lint`（0 error / 2 既有 warning）、
  `npm test`（119 files / 1257 tests 全绿，exit 0）、`npm run build`（vue-tsc + vite，exit 0）、
  `make check-docs` **PASS**。未动 Go/proto/生成物，未改 i18n catalog（无新增文案）。

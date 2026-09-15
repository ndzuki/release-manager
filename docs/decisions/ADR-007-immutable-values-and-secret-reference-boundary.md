# ADR-007：不可变 ValuesRevision 与 SecretRef 边界

- Status: accepted
- Date: created 2026-07-27 / updated 2026-07-27
- Scope: project
- Related: REQ-002, REQ-018, REQ-020, REQ-021, REQ-032, REQ-046, REQ-055, REQ-056, REQ-058, REQ-068 (Requirements); TASK-002, TASK-018, TASK-020, TASK-021, TASK-032, TASK-046, TASK-055, TASK-056, TASK-058, TASK-068 (Tasks)

## Status

accepted

## Context

发布配置需要版本、审批、diff、回滚和紧急变更后的收敛，但 values 中常包含敏感信息。直接存 Secret 明文或原地修改已批准配置，会破坏审计、浏览器安全和历史 Operation 可复现性。

## Decision

ValuesRevision 创建后内容不可变，canonical document、digest、SecretRefs、creator 和 parent 固化；状态通过 draft → pending_approval → approved|rejected → superseded 的受控状态机推进。Secret 只以强类型 {path,name,key} 引用存在，明文由 Operator 在目标集群本地解析，禁止进入中心数据库、API、日志、审计、浏览器 storage 或 diff。提交/审批使用 state_version CAS 和 scoped Idempotency-Key；禁止自批，批准新 revision 时同事务 supersede 旧批准版本并更新 ReleaseDefinition 指针。错误批准不改历史，必须创建新 revision 重新审批。

## Alternatives Considered

- **在中心保存 Secret 明文或加密 values**：仍扩大密钥管理、解密权限和泄露面，并把客户 Secret 带出集群。
- **原地编辑已批准 revision**：历史 Operation 和审计无法确定当时内容。
- **允许审批者覆盖同一记录纠错**：会擦除错误决策轨迹，无法证明变更过程。

## Consequences

- 配置历史、审批与回滚可验证，Secret 保持在客户集群边界。
- Chart 渲染和执行前必须在 Operator 侧解析 SecretRef，中心只能处理非敏感 canonical 数据。
- 前端草稿存储必须明确排除 SecretRef/明文并以服务端状态为权威。

## Requirement and Task Links

- Requirements: REQ-002-micro-service, REQ-018-values-revision-management, REQ-020-helm-install, REQ-021-helm-upgrade, REQ-032-emergency-change, REQ-046-render-preflight, REQ-055-web-values-revision, REQ-056-web-release-operation, REQ-058-web-emergency-change, REQ-068-values-approval-workflow
- Tasks: TASK-002-micro-service, TASK-018-values-revision-management, TASK-020-helm-install, TASK-021-helm-upgrade, TASK-032-emergency-change, TASK-046-render-preflight, TASK-055-web-values-revision, TASK-056-web-release-operation, TASK-058-web-emergency-change, TASK-068-values-approval-workflow

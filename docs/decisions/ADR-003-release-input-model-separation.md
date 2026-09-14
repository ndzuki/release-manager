# ADR-003：发布目标、制品、配置与执行记录分离

- Status: accepted
- Date: created 2026-07-27 / updated 2026-07-27
- Scope: project
- Related: REQ-002, REQ-011, REQ-017, REQ-018, REQ-020, REQ-021, REQ-022, REQ-023, REQ-040, REQ-045, REQ-055, REQ-056, REQ-067, REQ-068, REQ-069 (Requirements); TASK-002, TASK-011, TASK-017, TASK-018, TASK-020, TASK-021, TASK-022, TASK-023, TASK-040, TASK-045, TASK-055, TASK-056, TASK-067, TASK-068, TASK-069 (Tasks)

## Status

accepted

## Context

平台既要支持首次安装前定义发布目标，也要让同一目标复用不同制品与配置，并保证每次执行可审计、可重放且不受后续配置变更影响。把目标、制品、values 和执行状态放进同一模型会形成 Install 前置循环、可变历史和跨客户误绑定。

## Decision

采用四个独立领域对象：ReleaseDefinition 表示发布到哪里；ReleaseBundle 表示发布什么并以 digest 固化不可变制品；ValuesRevision 表示以什么非敏感期望配置发布；Operation 表示一次 INSTALL、UPGRADE、ROLLBACK 或 EMERGENCY 动作。Operation 创建时校验四者的 Customer/Cluster/Chart/状态关系，并固化 Bundle 摘要、ValuesRevision ID、patch digest、Actor 与 Organization；后续对象变化不得改写既有 Operation 输入。现场 Helm Release 仅作为 Inventory 观察结果，通过 definition_id 关联。

## Alternatives Considered

- **以现场 Helm Release 作为全部配置根对象**：首次安装前不存在现场对象，无法提前审批 ValuesRevision 或定义目标。
- **在 ReleaseDefinition 中直接存当前制品与 values**：会丢失版本历史，后续修改会污染运行中和历史 Operation。
- **Operation 运行时动态读取最新 Bundle/Values**：会破坏幂等、审计和可复现性。

## Consequences

- 首次安装、后续升级、回滚和审计具有稳定输入边界。
- 创建工作流必须执行跨对象一致性校验并在事务中固化摘要。
- 模型数量增加，但每个对象的所有权、生命周期和引用保护更清晰。

## Requirement and Task Links

- Requirements: REQ-002-micro-service, REQ-011-release-bundle-ingestion, REQ-017-release-inventory-sync, REQ-018-values-revision-management, REQ-020-helm-install, REQ-021-helm-upgrade, REQ-022-helm-rollback, REQ-023-operation-state-machine, REQ-040-release-definition, REQ-045-artifact-preflight, REQ-055-web-values-revision, REQ-056-web-release-operation, REQ-067-operation-creation-workflow, REQ-068-values-approval-workflow, REQ-069-artifact-lifecycle-policy
- Tasks: TASK-002-micro-service, TASK-011-release-bundle-ingestion, TASK-017-release-inventory-sync, TASK-018-values-revision-management, TASK-020-helm-install, TASK-021-helm-upgrade, TASK-022-helm-rollback, TASK-023-operation-state-machine, TASK-040-release-definition, TASK-045-artifact-preflight, TASK-055-web-values-revision, TASK-056-web-release-operation, TASK-067-operation-creation-workflow, TASK-068-values-approval-workflow, TASK-069-artifact-lifecycle-policy

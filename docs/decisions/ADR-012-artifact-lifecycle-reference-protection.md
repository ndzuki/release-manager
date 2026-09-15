# ADR-012：制品生命周期两阶段归档与引用保护

- Status: accepted
- Date: created 2026-07-27 / updated 2026-07-27
- Scope: project
- Related: REQ-011, REQ-019, REQ-023, REQ-040, REQ-045, REQ-067, REQ-069, REQ-070 (Requirements); TASK-011, TASK-019, TASK-023, TASK-040, TASK-045, TASK-067, TASK-069, TASK-070 (Tasks)

## Status

accepted

## Context

ReleaseBundle、Candidate Artifact 和 Preflight Lifecycle 会持续增长，但物理删除仍被 ReleaseDefinition 或非终态 Operation 引用的数据会破坏发布、回滚和审计。单纯 TTL 删除无法处理并发创建 Operation 与 GC 的竞争，也无法安全恢复误归档。

## Decision

ReleaseBundle 使用两阶段生命周期：满足 retention 且无 active Definition/非终态 Operation 引用时 soft archive，记录 archived_at 和 archived_from_status；宽限期后才物理删除。恢复必须回到归档前状态，禁止把 received/rejected 提升为 validated；Operation 创建或 Definition 更新仅可在事务行锁/CAS 下自动恢复原 validated Bundle。Candidate Artifact 以 (digest, artifact_type) 全局唯一，未关联后按 orphan TTL 清理；Preflight Lifecycle 在关联 Operation 终态后按策略清理。GC 使用 PostgreSQL advisory lock、固定锁顺序、FOR UPDATE SKIP LOCKED 和小批事务；保留 Operation 中固化的 bundle identity，不做级联历史删除。

## Alternatives Considered

- **到期直接物理删除**：无法观察误判，也无法恢复仍可能被引用的数据。
- **只依赖外键阻止删除**：不能表达非终态 Operation 的摘要引用、恢复语义和跨表保留策略。
- **SQLite 主 Store + PostgreSQL 生命周期 Store**：GC 与 Operation 创建无法共享事务，引用保护只能 best-effort。

## Consequences

- 存储增长可控，误归档可恢复，运行中发布与历史身份受保护。
- GC SQL 与业务创建事务必须共享 PostgreSQL 权威和锁顺序，测试并发交错。
- 物理删除仍不可逆，首次上线需使用较长宽限期和健康/审计观测。

## Requirement and Task Links

- Requirements: REQ-011-release-bundle-ingestion, REQ-019-release-preflight, REQ-023-operation-state-machine, REQ-040-release-definition, REQ-045-artifact-preflight, REQ-067-operation-creation-workflow, REQ-069-artifact-lifecycle-policy, REQ-070-orchestrator-postgresql-store
- Tasks: TASK-011-release-bundle-ingestion, TASK-019-release-preflight, TASK-023-operation-state-machine, TASK-040-release-definition, TASK-045-artifact-preflight, TASK-067-operation-creation-workflow, TASK-069-artifact-lifecycle-policy, TASK-070-orchestrator-postgresql-store

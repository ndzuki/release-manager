# ADR-015：维护窗口切换与单一数据权威

- Status: accepted
- Date: created 2026-07-27 / updated 2026-07-27
- Scope: project
- Related: REQ-069, REQ-070 (Requirements); TASK-069, TASK-070 (Tasks)

## Status

accepted

## Context

Orchestrator 从 SQLite 迁移到 PostgreSQL 时需要导出、导入和一致性校验。在线双写看似减少停机，但会引入两个数据库间的冲突解决和不可证明原子性；失败回退若没有明确边界会形成双重权威。

## Decision

采用维护窗口的一次性切换。维护模式按 Connect procedure allowlist 拒绝所有业务写入并返回 `CodeUnavailable`/HTTP 503，健康、就绪和明确允许的读取继续可用。停写后导出 SQLite、执行 golang-migrate、导入 PostgreSQL 并校验行数、外键与关键不变量，再配置单一 PostgreSQL Store 重启。每个进程任何时刻只打开一个权威业务后端。验证失败且尚无 PostgreSQL 业务写入时可恢复 SQLite 快照；首次成功 PostgreSQL 业务写入后，PostgreSQL 成为唯一权威，不提供双写、SQLite write-back 或自动双向回滚。

## Alternatives Considered

- **在线双写迁移**：跨库事务和冲突解决复杂，无法保证引用保护与 Operation 原子性。
- **应用同时打开两个 Store 并按失败回退**：读取和写入权威不清，可能静默分叉。
- **首次 PostgreSQL 写入后自动回 SQLite**：会丢失或覆盖 PostgreSQL 新写入，必须依靠 PostgreSQL 备份恢复。

## Consequences

- 切换边界明确、数据权威唯一，迁移失败路径可操作。
- 需要计划维护窗口，并在首次 PostgreSQL 写入前完成全部验证。
- 切换后灾难恢复依赖 PostgreSQL 备份或快照，不再依赖旧 SQLite 文件。

## Requirement and Task Links

- Requirements: REQ-069-artifact-lifecycle-policy, REQ-070-orchestrator-postgresql-store
- Tasks: TASK-069-artifact-lifecycle-policy, TASK-070-orchestrator-postgresql-store

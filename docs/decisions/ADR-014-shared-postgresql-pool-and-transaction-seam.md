# ADR-014：共享 PostgreSQL 连接池与事务 seam

- Status: accepted
- Date: created 2026-07-27 / updated 2026-07-27
- Scope: project
- Related: REQ-023, REQ-067, REQ-069, REQ-070 (Requirements); TASK-023, TASK-067, TASK-069, TASK-070 (Tasks)

## Status

accepted

## Context

PostgreSQL 迁移同时需要 GORM 业务访问和 advisory lock、`FOR UPDATE SKIP LOCKED`、批量 GC 等 raw SQL。GORM/`database/sql` 开启的事务不能让独立 `pgxpool` 连接加入；两个物理连接池还会产生连接上限和配置漂移。

## Decision

使用 pgx stdlib 驱动的单一 `*sql.DB` 作为物理连接池，并用同一个 `*sql.DB` 构造 GORM。业务事务由 GORM 开启；必须与业务写原子提交的 raw SQL 通过该 transaction 绑定的 `database/sql` connection 执行。GC 可从同一 `*sql.DB` pool 获取专用连接执行 advisory lock 和批处理，但不得声称加入已有业务事务。Schema 以版本化 golang-migrate SQL 为唯一权威，禁止 GORM AutoMigrate。

## Alternatives Considered

- **独立 `pgxpool.Pool` 与 GORM pool**：独立连接无法加入同一事务，并使连接配置与容量重复。
- **只使用 GORM**：难以清晰表达 advisory lock、`SKIP LOCKED` 和精确批处理 SQL。
- **GORM AutoMigrate**：无法审计完整 schema 版本、down 路径和跨服务一致迁移。

## Consequences

- GORM 与 raw SQL 共享连接限制和可证明的事务边界。
- 实现必须暴露明确 UnitOfWork/raw executor seam，不能在 callback 内另取连接。
- PostgreSQL 特定 GC 能力保留，但业务代码仍需避免依赖隐式 GORM `Save`/upsert 行为。

## Requirement and Task Links

- Requirements: REQ-023-operation-state-machine, REQ-067-operation-creation-workflow, REQ-069-artifact-lifecycle-policy, REQ-070-orchestrator-postgresql-store
- Tasks: TASK-023-operation-state-machine, TASK-067-operation-creation-workflow, TASK-069-artifact-lifecycle-policy, TASK-070-orchestrator-postgresql-store

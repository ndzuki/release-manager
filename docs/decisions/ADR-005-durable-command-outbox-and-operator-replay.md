# ADR-005：持久 Command Outbox 与 Operator 本地重放

- Status: accepted
- Date: created 2026-07-27 / updated 2026-07-27
- Scope: project
- Related: REQ-010, REQ-016, REQ-019, REQ-020, REQ-021, REQ-022, REQ-023, REQ-024, REQ-031, REQ-044, REQ-054, REQ-063, REQ-067 (Requirements); TASK-010, TASK-016, TASK-019, TASK-020, TASK-021, TASK-022, TASK-023, TASK-024, TASK-031, TASK-044, TASK-054, TASK-063, TASK-067 (Tasks)

## Status

accepted

## Context

中心与 Operator 之间存在断线、重启和重复投递。只依赖内存队列或“请求成功即已执行”会丢命令；仅收到网络 ACK 也不能证明 Operator 已持久化，重连后可能重复执行 Helm 写操作。

## Decision

中心以数据库 Command Outbox 作为待投递命令权威，命令带全局单调 sequence、command_id、operation_id、payload_version 和 deadline。状态按 pending → delivered → persisted → running → terminal 推进，MVP 每 Operator max_inflight=1。Operator 在本地 BoltDB/等价持久 Store 中 fsync 命令后才发送 ACK_PERSISTED；重启后重放未终态命令，并按 command_id 返回已完成结果而不重复执行。重连时 Operator 报 last_seen_sequence，中心重投未持久化命令并处理 sequence gap。

## Alternatives Considered

- **内存 channel 直接投递**：进程重启会丢命令且无法恢复顺序。
- **只用 ACK_RECEIVED**：网络已收不等于本地已持久化，崩溃后仍会丢失。
- **追求跨网络 exactly-once**：无法由单方保证；持久化 + at-least-once + 幂等执行更可证明。

## Consequences

- 断线和重启后可恢复，Helm 写操作按 command_id 去重。
- 需要维护中心 Outbox 与 Operator 本地 Store 两套相关状态及明确 ACK 语义。
- HA 演进必须保证 sequence 分配和 ACK 路由仍由数据库协调。

## Requirement and Task Links

- Requirements: REQ-010-shared-api-contracts, REQ-016-operator-control-stream, REQ-019-release-preflight, REQ-020-helm-install, REQ-021-helm-upgrade, REQ-022-helm-rollback, REQ-023-operation-state-machine, REQ-024-rollout-observation, REQ-031-notification-delivery, REQ-044-operator-session, REQ-054-web-release-inventory, REQ-063-rollback-sdk-quality, REQ-067-operation-creation-workflow
- Tasks: TASK-010-shared-api-contracts, TASK-016-operator-control-stream, TASK-019-release-preflight, TASK-020-helm-install, TASK-021-helm-upgrade, TASK-022-helm-rollback, TASK-023-operation-state-machine, TASK-024-rollout-observation, TASK-031-notification-delivery, TASK-044-operator-session, TASK-054-web-release-inventory, TASK-063-rollback-sdk-quality, TASK-067-operation-creation-workflow

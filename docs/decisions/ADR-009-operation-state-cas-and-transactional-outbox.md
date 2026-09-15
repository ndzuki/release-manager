# ADR-009：Operation 状态机、CAS 与事务 Outbox

- Status: accepted
- Date: created 2026-07-27 / updated 2026-07-27
- Scope: project
- Related: REQ-010, REQ-016, REQ-019, REQ-023, REQ-031, REQ-032, REQ-054, REQ-056, REQ-057, REQ-060, REQ-067, REQ-068, REQ-069 (Requirements); TASK-010, TASK-016, TASK-019, TASK-023, TASK-031, TASK-032, TASK-054, TASK-056, TASK-057, TASK-060, TASK-067, TASK-068, TASK-069 (Tasks)

## Status

accepted

## Context

发布包含创建、预检、排队、执行、取消、超时、通知和审计等跨进程步骤。直接在 handler 中同步调用下游或以普通 UPDATE 改状态，会在并发、重试和崩溃时产生重复 Operation、丢事件、状态倒退和“数据库已提交但消息未发送”。

## Decision

Operation 是发布执行的权威状态机，标准路径为 pending → preflight → queued → running → succeeded|failed|cancelled|timeout，EMERGENCY 使用明确的简化路径。所有转换以 state_version compare-and-swap 执行；同 ReleaseDefinition 的不兼容非终态写 Operation 由数据库约束/事务门禁互斥。写请求使用 scope + Idempotency-Key + request_hash，授权检查先于幂等命中。Operation 创建在同一事务写 operation、idempotency record 和 preflight dispatch outbox；终态转换在同一事务写 terminal_at、时间线/状态事件及已知 lifecycle 回填。审批、审计和通知等跨进程副作用使用事务 Outbox，消费者按稳定 identity 幂等处理；不提供通用 best-effort 回调钩子。

## Alternatives Considered

- **handler 中同步调用 Preflight/Notifier**：下游超时会扩大请求事务，崩溃时无法区分是否已提交。
- **普通 UPDATE 后异步发事件**：会出现状态已变但事件丢失，或事件重复但状态未变。
- **通用 OnTerminal 回调**：隐藏事务所有权，容易在不同数据库/连接上形成 best-effort 双写。

## Consequences

- 重试、并发和崩溃恢复有稳定语义，状态与事件不会分裂。
- Store 需要显式 UnitOfWork、CAS、唯一约束和 outbox worker，复杂度集中在持久层。
- 下游是 at-least-once，必须用 event/job identity 去重而不能宣称网络 exactly-once。

## Requirement and Task Links

- Requirements: REQ-010-shared-api-contracts, REQ-016-operator-control-stream, REQ-019-release-preflight, REQ-023-operation-state-machine, REQ-031-notification-delivery, REQ-032-emergency-change, REQ-054-web-release-inventory, REQ-056-web-release-operation, REQ-057-web-operation-timeline, REQ-060-web-notification-jobs, REQ-067-operation-creation-workflow, REQ-068-values-approval-workflow, REQ-069-artifact-lifecycle-policy
- Tasks: TASK-010-shared-api-contracts, TASK-016-operator-control-stream, TASK-019-release-preflight, TASK-023-operation-state-machine, TASK-031-notification-delivery, TASK-032-emergency-change, TASK-054-web-release-inventory, TASK-056-web-release-operation, TASK-057-web-operation-timeline, TASK-060-web-notification-jobs, TASK-067-operation-creation-workflow, TASK-068-values-approval-workflow, TASK-069-artifact-lifecycle-policy

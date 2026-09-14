# ADR-010：独立脱敏审计流水线与归档提交边界

- Status: accepted
- Date: created 2026-07-27 / updated 2026-07-27
- Scope: project
- Related: REQ-010, REQ-029, REQ-030, REQ-032, REQ-050, REQ-059, REQ-060, REQ-068, REQ-069 (Requirements); TASK-010, TASK-029, TASK-030, TASK-032, TASK-050, TASK-059, TASK-060, TASK-068, TASK-069 (Tasks)

## Status

accepted

## Context

认证、授权、发布、紧急变更、通知重放和 GC 都需要可追责记录。若审计依赖业务日志、通知数据库或查询服务，故障和保留策略会互相耦合；若保存原始请求则可能永久固化 token、Secret、证书或内部错误。

## Decision

采用独立 AuditEvent 模型和统一 AuditEmitter，所有服务按稳定 Actor、Organization、Customer、Resource、Action、Result、request_id 发射结构化事件。中央 sanitizer 在持久化前移除密码、token、Secret、私钥、完整 URL path/query、values 和堆栈；查询层只能返回服务端已脱敏投影并强制组织过滤。采集与查询/导出分离，通知不得通过审计查询补业务数据。过期事件以稳定顺序流式写 gzip JSONL，计算 SHA-256，临时文件 fsync 后原子发布 archive 与 checksum；只有发布和校验成功后才在数据库事务中条件删除，失败保留原数据。

## Alternatives Considered

- **只依赖 slog/应用日志**：格式、保留和授权查询不可控，且难以证明事件完整性。
- **Notifier/Audit 相互查询补数据**：会形成循环依赖，任一系统故障都会阻断另一方。
- **先删数据库再写归档**：归档失败会造成不可恢复的审计丢失。

## Consequences

- 审计采集可独立演进，查询和归档有明确授权与完整性边界。
- 所有业务模块必须维护稳定事件语义和中央脱敏规则。
- 归档对象发布与数据库删除无法共享单一事务，因此采用“先可验证发布、后条件删除”的可恢复顺序。

## Requirement and Task Links

- Requirements: REQ-010-shared-api-contracts, REQ-029-audit-pipeline, REQ-030-audit-archive, REQ-032-emergency-change, REQ-050-audit-capture, REQ-059-web-audit-query, REQ-060-web-notification-jobs, REQ-068-values-approval-workflow, REQ-069-artifact-lifecycle-policy
- Tasks: TASK-010-shared-api-contracts, TASK-029-audit-pipeline, TASK-030-audit-archive, TASK-032-emergency-change, TASK-050-audit-capture, TASK-059-web-audit-query, TASK-060-web-notification-jobs, TASK-068-values-approval-workflow, TASK-069-artifact-lifecycle-policy

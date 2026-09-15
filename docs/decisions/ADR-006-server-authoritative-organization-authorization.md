# ADR-006：服务端组织域授权与版本化授权投影

- Status: accepted
- Date: created 2026-07-27 / updated 2026-07-27
- Scope: project
- Related: REQ-010, REQ-013, REQ-025, REQ-026, REQ-027, REQ-028, REQ-029, REQ-049, REQ-051, REQ-052, REQ-053, REQ-055, REQ-056, REQ-057, REQ-058, REQ-059, REQ-060, REQ-067, REQ-068 (Requirements); TASK-010, TASK-013, TASK-025, TASK-026, TASK-027, TASK-028, TASK-029, TASK-049, TASK-051, TASK-052, TASK-053, TASK-055, TASK-056, TASK-057, TASK-058, TASK-059, TASK-060, TASK-067, TASK-068 (Tasks)

## Status

accepted

## Context

平台同时存在 User、Organization、Customer、Cluster 和服务身份。浏览器角色、路由参数或请求体中的 organization_id 都可能被伪造；业务服务若直接共享 auth 表或各自缓存无版本数据，会形成跨租户越权和授权漂移。

## Decision

服务端是唯一授权裁决点。请求先验证持久 Session/User，再确认 active OrganizationMembership、OrganizationCustomerBinding 和 Casbin sub/domain/object/action policy。客户端传入的 Actor、role、organization 不能覆盖认证上下文；scope mismatch 按 not found 或 permission denied 处理。跨服务需要授权数据时，由身份上下文发布版本化 Authorization Snapshot，业务服务维护只读投影和 checkpoint；治理写操作仅在投影追平且新鲜时执行。policy/snapshot 不可用时写操作 fail closed，读操作仅在明确允许的场景降级。前端守卫和按钮隐藏只提供 UX，不构成安全边界。

## Alternatives Considered

- **前端根据 JWT claims 决定权限**：可被绕过且无法覆盖资源归属与最新绑定状态。
- **所有业务服务直接查询 auth 数据库表**：造成 schema 耦合、跨服务事务假象和所有权不清。
- **无版本的本地权限缓存**：无法判断陈旧程度，治理写入可能基于过期权限。

## Consequences

- 租户隔离和角色语义集中且可审计，跨服务不会共享可写 auth 表。
- 授权投影必须有版本、健康和新鲜度监控；故障时部分写操作不可用。
- 所有 RPC、后台任务和导出/重放路径都必须使用同一授权链。

## Requirement and Task Links

- Requirements: REQ-010-shared-api-contracts, REQ-013-customer-lifecycle, REQ-025-local-auth-sessions, REQ-026-organization-membership, REQ-027-rbac-enforcement, REQ-028-external-identity-providers, REQ-029-audit-pipeline, REQ-049-organization-customer-binding, REQ-051-web-customer-management, REQ-052-web-cluster-routing, REQ-053-web-operator-enrollment, REQ-055-web-values-revision, REQ-056-web-release-operation, REQ-057-web-operation-timeline, REQ-058-web-emergency-change, REQ-059-web-audit-query, REQ-060-web-notification-jobs, REQ-067-operation-creation-workflow, REQ-068-values-approval-workflow
- Tasks: TASK-010-shared-api-contracts, TASK-013-customer-lifecycle, TASK-025-local-auth-sessions, TASK-026-organization-membership, TASK-027-rbac-enforcement, TASK-028-external-identity-providers, TASK-029-audit-pipeline, TASK-049-organization-customer-binding, TASK-051-web-customer-management, TASK-052-web-cluster-routing, TASK-053-web-operator-enrollment, TASK-055-web-values-revision, TASK-056-web-release-operation, TASK-057-web-operation-timeline, TASK-058-web-emergency-change, TASK-059-web-audit-query, TASK-060-web-notification-jobs, TASK-067-operation-creation-workflow, TASK-068-values-approval-workflow

# ADR-000：原子需求交付与架构决策治理

- Status: accepted
- Date: created 2026-07-27 / updated 2026-07-27
- Scope: project
- Related: REQ-001, REQ-003, REQ-004, REQ-005, REQ-006, REQ-007, REQ-008, REQ-009, REQ-039 (Requirements); TASK-001, TASK-003, TASK-004, TASK-005, TASK-006, TASK-007, TASK-008, TASK-009, TASK-039 (Tasks)

## Status

accepted

## Context

项目路线图横跨控制面、Operator、制品、认证、审计、前端和质量门禁。按领域索引或大 Epic 直接实施会同时修改多个服务和状态机，使验收、回滚、依赖和架构权衡无法独立审查。ADR 若仅散落在任务正文中，也无法从项目整体追踪决策影响。

## Decision

REQ-001 作为项目路线图，REQ-003～008 仅作为领域索引，不直接生成实现任务。可实施工作必须拆成单一业务结果的原子 Requirement，并默认一一关联 Task；每个 Requirement 同时包含目标、影响服务、输入/输出、状态与数据、错误、安全、验收、非目标和回滚。安全、测试、迁移和必要文档属于同一原子交付，不得延后为清理任务。预计跨越多个主要状态机、三个以上服务或超过约八个源文件的需求必须继续拆分或显式重新细化。满足“难逆转、非显然、存在真实权衡”的决定写入 Notes/adr，并在 ADR 索引中反向关联全部受影响 Requirement 与 Task。

## Alternatives Considered

- **按领域 Epic 一次性交付**：改动和验收过大，依赖冲突、回滚范围与失败归因不可控。
- **先写功能，安全/测试/迁移后补**：会交付不可验证或不可安全上线的半成品。
- **只在 Task 计划中保留决策说明**：未来维护者无法按项目和需求检索跨任务决策。

## Consequences

- 每个业务结果可独立计划、测试、发布和回滚，项目依赖图更明确。
- 需求和 Task 数量增加，跨需求契约必须通过索引与 ADR 管理。
- 需求模板与 reqcheck 成为交付门禁；架构变更需要同步更新 ADR 关联。

## Requirement and Task Links

- Requirements: REQ-001-release-manager, REQ-003-core-pipeline, REQ-004-multi-tenant, REQ-005-rbac-auth, REQ-006-audit-notify, REQ-007-frontend, REQ-008-devops-quality, REQ-009-delivery-slicing-rules, REQ-039-atomic-requirement-template
- Tasks: TASK-001-release-manager, TASK-003-core-pipeline, TASK-004-multi-tenant, TASK-005-rbac-auth, TASK-006-audit-notify, TASK-007-frontend, TASK-008-devops-quality, TASK-009-delivery-slicing-rules, TASK-039-atomic-requirement-template

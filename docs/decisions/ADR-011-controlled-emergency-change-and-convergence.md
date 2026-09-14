# ADR-011：受控紧急变更与显式配置收敛

- Status: accepted
- Date: created 2026-07-27 / updated 2026-07-27
- Scope: project
- Related: REQ-018, REQ-023, REQ-032, REQ-055, REQ-058, REQ-068 (Requirements); TASK-018, TASK-023, TASK-032, TASK-055, TASK-058, TASK-068 (Tasks)

## Status

accepted

## Context

事故期间需要快速调整受控 Kubernetes 字段，但绕过 Helm/ValuesRevision 会造成现场状态与期望配置永久漂移。把任意 JSON Patch 或 shell 操作开放给管理员又会突破资源、字段和审计边界。

## Decision

紧急变更只允许强类型、白名单化的操作和字段，通过 EMERGENCY Operation 记录 actor、reason、before/after 脱敏快照和终态。它跳过标准 Preflight/Helm action，执行由客户侧 Operator 使用 client-go 完成，但仍与标准 Operation 双向互斥并接受幂等和取消约束。请求必须选择 REVERT_ON_NEXT_RECONCILE 或 REQUIRE_PROMOTION；后者创建持久 Convergence Task，并通过 Promotion Mapping 将受控 workload 字段映射到 Helm values path，只有新 ValuesRevision 经正常提交、异人审批并由服务端验证吸收变更后才标记 converged。缺少安全映射的目标只能选择回退策略。

## Alternatives Considered

- **允许任意 JSON Patch/kubectl**：授权面过宽，字段语义、幂等、脱敏和回滚难以证明。
- **紧急变更不进入 Operation**：时间线、互斥、审计和通知会出现盲区。
- **变更后只提示人工修改 values**：无法持久跟踪是否真正收敛，漂移会长期存在。

## Consequences

- 事故响应快，同时保留可审计、可限制和可收敛的闭环。
- 需要维护 Promotion Mapping、Convergence Task 和审批集成。
- 直接 client-go 动作与标准 Helm 路径不同，必须明确限制可操作资源和字段。

## Requirement and Task Links

- Requirements: REQ-018-values-revision-management, REQ-023-operation-state-machine, REQ-032-emergency-change, REQ-055-web-values-revision, REQ-058-web-emergency-change, REQ-068-values-approval-workflow
- Tasks: TASK-018-values-revision-management, TASK-023-operation-state-machine, TASK-032-emergency-change, TASK-055-web-values-revision, TASK-058-web-emergency-change, TASK-068-values-approval-workflow

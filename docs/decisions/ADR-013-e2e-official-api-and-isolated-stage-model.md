# ADR-013：E2E 仅走正式接口与独立 Stage 模型

- Status: accepted
- Date: created 2026-07-27 / updated 2026-07-27
- Scope: project
- Related: REQ-009, REQ-037, REQ-061, REQ-062, REQ-063, REQ-064, REQ-065, REQ-066 (Requirements); TASK-009, TASK-037, TASK-061, TASK-062, TASK-063, TASK-064, TASK-065, TASK-066 (Tasks)

## Status

accepted

## Context

端到端测试需要覆盖控制面、Operator、真实 Kubernetes 和重启恢复。若测试直接读写数据库、调用 Helm SDK 或依赖固定的整条顺序，既会绕过正式契约，也使单个能力无法独立定位和复现。

## Decision

唯一正式 E2E 入口为 cmd/e2e，按 control-plane、inventory、artifact、release、isolation、emergency、restart 的 Stage DAG 执行。参数选择是集合语义，未选择的前置 Stage 不自动运行；目标 Stage 自己验证 readiness、fixture freshness 和权限。Run 开始采集不可变 BaselineSnapshot，需要观察变化时另采只读 ObservedSnapshot，禁止修改基线。所有业务写入和补偿只通过正式 Connect API、Operator 控制流和受限 client-go restart 权限；禁止数据库直读写、测试专用旁路、直接 Helm SDK 或 helm/kubectl 子进程。写 Stage 在 environment_id 不一致、production=true 或正式补偿失败时 fail closed。

## Alternatives Considered

- **线性全链路脚本**：前序失败会遮蔽独立能力，无法选择性运行和定位。
- **测试直接操作数据库或 Helm**：会绕过授权、状态机、Outbox 和 SDK 边界，得到虚假通过。
- **共享可变 fixture**：阶段顺序和并发会污染结果，重跑不可复现。

## Consequences

- 每个 Stage 可独立验证正式产品契约，失败归因和 CI artifact 稳定。
- Stage 必须显式实现外部 guard、清理和可观察结果，不能依赖隐含前序副作用。
- 上游正式 API 未就绪时 E2E 必须阻塞，而不是在测试内补业务能力。

## Requirement and Task Links

- Requirements: REQ-009-delivery-slicing-rules, REQ-037-sdk-quality-gates, REQ-061-install-sdk-quality, REQ-062-upgrade-sdk-quality, REQ-063-rollback-sdk-quality, REQ-064-rollout-watch-quality, REQ-065-dev-environment, REQ-066-stage-e2e
- Tasks: TASK-009-delivery-slicing-rules, TASK-037-sdk-quality-gates, TASK-061-install-sdk-quality, TASK-062-upgrade-sdk-quality, TASK-063-rollback-sdk-quality, TASK-064-rollout-watch-quality, TASK-065-dev-environment, TASK-066-stage-e2e

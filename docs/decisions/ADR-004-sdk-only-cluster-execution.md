# ADR-004：客户集群运行时采用 Go SDK-only

- Status: accepted
- Date: created 2026-07-27 / updated 2026-07-27
- Scope: project
- Related: REQ-002, REQ-020, REQ-021, REQ-022, REQ-024, REQ-037, REQ-041, REQ-045, REQ-046, REQ-047, REQ-048, REQ-061, REQ-062, REQ-063, REQ-064, REQ-065, REQ-066 (Requirements); TASK-002, TASK-020, TASK-021, TASK-022, TASK-024, TASK-037, TASK-041, TASK-045, TASK-046, TASK-047, TASK-048, TASK-061, TASK-062, TASK-063, TASK-064, TASK-065, TASK-066 (Tasks)

## Status

accepted

## Context

Operator 负责客户集群内的 Helm 和 Kubernetes 操作。通过 helm/kubectl 子进程实现虽然直观，但会引入二进制版本漂移、文本解析、shell 注入、不可控环境变量、较弱的 context 取消和难以精确测试的错误模型。

## Decision

所有运行时业务路径只调用 Helm Go SDK（helm.sh/helm/v3/pkg/action）和 client-go；禁止 os/exec、exec.Command、shell wrapper、sidecar 或脚本间接调用 helm/kubectl。每个 Operation 独立初始化 action.Configuration，不跨并发 Operation 共享可变 action client。kind/docker 等 CLI 仅可存在于 Makefile、CI 或人工开发环境生命周期中；集成测试本身仍通过生产 SDK 路径验证。使用 go/analysis + types.Info 的静态门禁在 CI 中 fail closed，例外必须有 owner、reason 和 expires_at，且不得用于 Helm/Kubernetes 业务。

## Alternatives Considered

- **调用 helm/kubectl CLI**：版本和输出格式不稳定，错误分类与取消不可控，并扩大镜像和供应链面。
- **封装 shell wrapper 作为统一接口**：只是隐藏子进程，没有消除上述风险。
- **中心服务直接执行 SDK**：会突破 Operator 的客户集群安全边界。

## Consequences

- 执行路径类型安全、可取消、可使用 fake/真实 API Server 分层验证。
- Operator 镜像无需携带 helm/kubectl，但必须管理 Helm/client-go 依赖和版本兼容。
- CI 和本地开发脚本仍可管理 kind 生命周期，但必须与运行时代码严格分离。

## Requirement and Task Links

- Requirements: REQ-002-micro-service, REQ-020-helm-install, REQ-021-helm-upgrade, REQ-022-helm-rollback, REQ-024-rollout-observation, REQ-037-sdk-quality-gates, REQ-041-helm-engine-contract, REQ-045-artifact-preflight, REQ-046-render-preflight, REQ-047-cluster-dryrun-preflight, REQ-048-runtime-pull-preflight, REQ-061-install-sdk-quality, REQ-062-upgrade-sdk-quality, REQ-063-rollback-sdk-quality, REQ-064-rollout-watch-quality, REQ-065-dev-environment, REQ-066-stage-e2e
- Tasks: TASK-002-micro-service, TASK-020-helm-install, TASK-021-helm-upgrade, TASK-022-helm-rollback, TASK-024-rollout-observation, TASK-037-sdk-quality-gates, TASK-041-helm-engine-contract, TASK-045-artifact-preflight, TASK-046-render-preflight, TASK-047-cluster-dryrun-preflight, TASK-048-runtime-pull-preflight, TASK-061-install-sdk-quality, TASK-062-upgrade-sdk-quality, TASK-063-rollback-sdk-quality, TASK-064-rollout-watch-quality, TASK-065-dev-environment, TASK-066-stage-e2e

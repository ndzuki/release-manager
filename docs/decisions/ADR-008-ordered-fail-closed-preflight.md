# ADR-008：严格有序且 fail-closed 的发布 Preflight

- Status: accepted
- Date: created 2026-07-27 / updated 2026-07-27
- Scope: project
- Related: REQ-012, REQ-019, REQ-023, REQ-042, REQ-043, REQ-045, REQ-046, REQ-047, REQ-048, REQ-056, REQ-067, REQ-069 (Requirements); TASK-012, TASK-019, TASK-023, TASK-042, TASK-043, TASK-045, TASK-046, TASK-047, TASK-048, TASK-056, TASK-067, TASK-069 (Tasks)

## Status

accepted

## Context

制品信任、Helm 渲染、目标集群 Admission/RBAC 和真实镜像拉取验证依赖不同数据与执行位置。并行或可随意跳过的检查会浪费客户集群资源、泄露未验证内容，且 production 在验证服务故障时可能误放行。

## Decision

标准 Operation 进入执行前按 Artifact → Render → Cluster DryRun → optional Runtime Pull 的固定顺序运行。Artifact 校验 digest、签名、SBOM 与路由；Render 只在本地 Helm SDK 渲染并保存脱敏摘要；DryRun 在目标 Operator 使用其 ServiceAccount 执行 server-side DryRun；Runtime Pull 以受限临时 Pod 验证 kubelet/Registry/IAM。任一 required stage 失败或不可用立即停止后续阶段并 fail closed；production required stage 不可跳过，optional stage 的跳过必须由版本化 policy 明示并记录。结果按相关 digest 与 policy/capability version 幂等缓存。

## Alternatives Considered

- **所有阶段并行执行**：会在制品或渲染已失败时仍访问客户集群，并使错误主因不稳定。
- **在中心执行 DryRun/Runtime Pull**：需要传输 kubeconfig或Secret，且无法复用真实 ServiceAccount/IAM。
- **验证后端不可用时警告放行 production**：把基础设施故障转化为供应链绕过。

## Consequences

- 错误定位稳定，生产安全边界可证明，昂贵集群检查只在前序成功后执行。
- 发布延迟增加，required 验证依赖故障会阻断发布。
- stage 版本、缓存键、取消传播和 lifecycle 记录必须作为编排契约维护。

## Requirement and Task Links

- Requirements: REQ-012-artifact-trust-policy, REQ-019-release-preflight, REQ-023-operation-state-machine, REQ-042-sbom-vulnerability-policy, REQ-043-trust-root-rotation, REQ-045-artifact-preflight, REQ-046-render-preflight, REQ-047-cluster-dryrun-preflight, REQ-048-runtime-pull-preflight, REQ-056-web-release-operation, REQ-067-operation-creation-workflow, REQ-069-artifact-lifecycle-policy
- Tasks: TASK-012-artifact-trust-policy, TASK-019-release-preflight, TASK-023-operation-state-machine, TASK-042-sbom-vulnerability-policy, TASK-043-trust-root-rotation, TASK-045-artifact-preflight, TASK-046-render-preflight, TASK-047-cluster-dryrun-preflight, TASK-048-runtime-pull-preflight, TASK-056-web-release-operation, TASK-067-operation-creation-workflow, TASK-069-artifact-lifecycle-policy

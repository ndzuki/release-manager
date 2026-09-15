# ADR-001：中心控制面与客户侧 Operator 出站边界

- Status: accepted
- Date: created 2026-07-27 / updated 2026-07-27
- Scope: project
- Related: REQ-002, REQ-013, REQ-014, REQ-015, REQ-016, REQ-017, REQ-020, REQ-021, REQ-022, REQ-024, REQ-032, REQ-044, REQ-045, REQ-046, REQ-047, REQ-048, REQ-053, REQ-054, REQ-058 (Requirements); TASK-002, TASK-013, TASK-014, TASK-015, TASK-016, TASK-017, TASK-020, TASK-021, TASK-022, TASK-024, TASK-032, TASK-044, TASK-045, TASK-046, TASK-047, TASK-048, TASK-053, TASK-054, TASK-058 (Tasks)

## Status

accepted

## Context

平台必须管理多个客户与集群，但中心控制面不应持有客户集群的入站网络通道、长期 kubeconfig 或可跨客户复用的执行凭据。发布执行、预检、Inventory 与紧急变更又必须在目标集群的真实 RBAC、Admission、Registry/IAM 条件下运行。

## Decision

客户集群中的 release-operator 作为唯一集群执行边界，主动通过 mTLS Connect 双向流连接中心控制面。中心侧只保存 Customer、Cluster、Operator 身份、会话状态和待投递命令；Helm/Kubernetes 操作、SecretRef 解析、DryRun、Runtime Pull 与 Inventory 采集均在 Operator 侧完成。浏览器只访问中心服务，不直连 Operator；Operator 身份和证书严格绑定 Customer 与 Cluster。

## Alternatives Considered

- **中心控制面主动连接每个客户集群**：需要保存 kubeconfig、打通入站网络并扩大凭据泄露与横向移动风险。
- **每个业务模块直接访问 Kubernetes API**：会形成多套集群连接、权限和重试协议，破坏租户隔离与可审计性。
- **浏览器直连 Operator 管理接口**：会绕过中心授权、审计和单一权威状态。

## Consequences

- 客户网络只需允许 Operator 出站连接，中心不保管集群长期凭据。
- 所有集群侧能力必须通过 Operator 协议演进，离线时控制面只能排队或 fail closed。
- Operator Session、证书吊销、命令重放和能力版本成为平台级可靠性基础。

## Requirement and Task Links

- Requirements: REQ-002-micro-service, REQ-013-customer-lifecycle, REQ-014-cluster-artifact-routing, REQ-015-operator-enrollment, REQ-016-operator-control-stream, REQ-017-release-inventory-sync, REQ-020-helm-install, REQ-021-helm-upgrade, REQ-022-helm-rollback, REQ-024-rollout-observation, REQ-032-emergency-change, REQ-044-operator-session, REQ-045-artifact-preflight, REQ-046-render-preflight, REQ-047-cluster-dryrun-preflight, REQ-048-runtime-pull-preflight, REQ-053-web-operator-enrollment, REQ-054-web-release-inventory, REQ-058-web-emergency-change
- Tasks: TASK-002-micro-service, TASK-013-customer-lifecycle, TASK-014-cluster-artifact-routing, TASK-015-operator-enrollment, TASK-016-operator-control-stream, TASK-017-release-inventory-sync, TASK-020-helm-install, TASK-021-helm-upgrade, TASK-022-helm-rollback, TASK-024-rollout-observation, TASK-032-emergency-change, TASK-044-operator-session, TASK-045-artifact-preflight, TASK-046-render-preflight, TASK-047-cluster-dryrun-preflight, TASK-048-runtime-pull-preflight, TASK-053-web-operator-enrollment, TASK-054-web-release-inventory, TASK-058-web-emergency-change

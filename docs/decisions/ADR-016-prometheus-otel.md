# ADR-016：以 Prometheus + OTel 作为授权可观测性基线

- Status: accepted
- Date: created 2026-07-31 / updated 2026-07-31
- Scope: project
- Related: REQ-027 (Requirements); TASK-027 (Tasks)

## Status
accepted

## Context
授权是治理写路径的安全门禁。Authorization Snapshot 引入跨服务版本追平、fail-closed 和 200ms RPC deadline 后，需要同时回答裁决结果、版本新鲜度、策略健康和跨服务耗时。仅依赖日志无法聚合 SLO，只有指标无法关联单次拒绝和调用链；项目当前也没有授权专用的稳定可观测性契约。

## Decision
Auth 与 Orchestrator 使用 Prometheus client 暴露低基数授权指标，并在现有单端口 net/http ServeMux 上注册 /metrics。固定指标为 auth_decisions_total{result,actor_type}、auth_snapshot_stale_total、auth_source_version、auth_checkpoint_version、auth_policy_health、auth_enforce_duration_seconds、auth_snapshot_rpc_duration_seconds。使用 OpenTelemetry trace context 跨 Connect 调用传播 trace-id，在授权 producer、consumer 和治理写门禁创建 span；结构化日志仅记录脱敏 actor_id、domain、action、result、reason、source_version、policy_version 和 checkpoint，不记录 token、membership、binding 或 capability 列表。可观测性不得改变授权结果；初始化或 exporter 不可用时授权仍按版本与策略健康 fail closed。

## Alternatives Considered
- 仅使用结构化日志：便于单次排障，但无法稳定计算吞吐、错误率、P99 和版本差距，也不适合作为告警数据源。
- 仅使用 Prometheus 指标：可聚合但无法关联单次跨服务调用及拒绝原因，故障定位需要猜测调用链。
- 引入 vendor-specific agent 或第二套授权遥测后端：增加部署耦合和数据权威，超出当前单端口 Go 服务的需要。

## Consequences
- 授权结果、延迟、策略健康和 checkpoint 新鲜度具有稳定、低基数的机器可读契约，可直接用于 SLO、告警和正式 API 冒烟。
- Auth 与 Orchestrator 增加 Prometheus/OpenTelemetry 直接依赖和显式装配；测试必须使用独立 registry/provider，避免全局注册冲突。
- 日志、指标和 trace 必须保持脱敏与低基数，新增 action/actor 类型时需要同步审查标签集合和仪表盘查询。
- 监控链路故障不能放宽授权；安全裁决继续由 ADR-006 的服务端权威和版本化 Authorization Snapshot 决定。

# 架构决策记录（ADR）

本目录是 release-manager 项目既有架构决策记录（ADR）的发布版副本，用于随代码一起阅读。
ADR 的权威源在知识库 Vault：`Projects/001-release-manager/Notes/adr/`；
本仓库副本不独立演进，决策内容的修订、状态变更与关联关系更新以知识库版本为准。

共 22 篇：ADR-000 ～ ADR-021，按编号升序索引如下。

| ADR | 标题 | 状态 | 日期 |
| --- | --- | --- | --- |
| [ADR-000](ADR-000-atomic-requirement-delivery-governance.md) | 原子需求交付与架构决策治理 | accepted | 2026-07-27 |
| [ADR-001](ADR-001-control-plane-operator-outbound-boundary.md) | 中心控制面与客户侧 Operator 出站边界 | accepted | 2026-07-27 |
| [ADR-002](ADR-002-connect-protobuf-single-port-contract.md) | Connect 与 protobuf 单端口统一协议面 | accepted | 2026-07-27 |
| [ADR-003](ADR-003-release-input-model-separation.md) | 发布目标、制品、配置与执行记录分离 | accepted | 2026-07-27 |
| [ADR-004](ADR-004-sdk-only-cluster-execution.md) | 客户集群运行时采用 Go SDK-only | accepted | 2026-07-27 |
| [ADR-005](ADR-005-durable-command-outbox-and-operator-replay.md) | 持久 Command Outbox 与 Operator 本地重放 | accepted | 2026-07-27 |
| [ADR-006](ADR-006-server-authoritative-organization-authorization.md) | 服务端组织域授权与版本化授权投影 | accepted | 2026-07-27 |
| [ADR-007](ADR-007-immutable-values-and-secret-reference-boundary.md) | 不可变 ValuesRevision 与 SecretRef 边界 | accepted | 2026-07-27 |
| [ADR-008](ADR-008-ordered-fail-closed-preflight.md) | 严格有序且 fail-closed 的发布 Preflight | accepted | 2026-07-27 |
| [ADR-009](ADR-009-operation-state-cas-and-transactional-outbox.md) | Operation 状态机、CAS 与事务 Outbox | accepted | 2026-07-27 |
| [ADR-010](ADR-010-independent-sanitized-audit-pipeline.md) | 独立脱敏审计流水线与归档提交边界 | accepted | 2026-07-27 |
| [ADR-011](ADR-011-controlled-emergency-change-and-convergence.md) | 受控紧急变更与显式配置收敛 | accepted | 2026-07-27 |
| [ADR-012](ADR-012-artifact-lifecycle-reference-protection.md) | 制品生命周期两阶段归档与引用保护 | accepted | 2026-07-27 |
| [ADR-013](ADR-013-e2e-official-api-and-isolated-stage-model.md) | E2E 仅走正式接口与独立 Stage 模型 | accepted | 2026-07-27 |
| [ADR-014](ADR-014-shared-postgresql-pool-and-transaction-seam.md) | 共享 PostgreSQL 连接池与事务 seam | accepted | 2026-07-27 |
| [ADR-015](ADR-015-maintenance-cutover-authority-boundary.md) | 维护窗口切换与单一数据权威 | accepted | 2026-07-27 |
| [ADR-016](ADR-016-prometheus-otel.md) | 以 Prometheus + OTel 作为授权可观测性基线 | accepted | 2026-07-31 |
| [ADR-017](ADR-017-ca-prod-vault-dev.md) | CA 证书与私钥持久化（prod Vault / dev 文件），启动加载不重新生成 | accepted | 2026-08-04 |
| [ADR-018](ADR-018-sha256-certder-10-hex-renew.md) | 证书序列号（sha256(certDER) 前 10 字节 hex）为身份权威，renew 即时失效旧证书 | accepted | 2026-08-04 |
| [ADR-019](ADR-019-use-redis-as-auth-session-cache-and-refresh-token-blacklist-with-postgresql-authority-and-fail-closed-semantics.md) | Use Redis as auth session cache and refresh-token blacklist with PostgreSQL authority and fail-closed semantics | accepted | 2026-08-05 |
| [ADR-020](ADR-020-use-hashicorp-vault-go-api-for-notifier-secretresolver.md) | Use HashiCorp Vault Go API for notifier SecretResolver | accepted | 2026-08-05 |
| [ADR-021](ADR-021-release-api-synchronous-authorization-decision.md) | release-api 审计授权经同步判定 RPC（透传用户 JWT） | accepted | 2026-09-16 |

## ADR → 相关任务

下表整理自知识库 `ADR-INDEX.md` 的 Tasks 关联（与 `ADR-COVERAGE.md` 的反向映射一致）；
各 ADR 关联的 Requirements 见对应文件顶部 `Related` 元数据。

| ADR | 相关任务 |
| --- | --- |
| ADR-000 | TASK-001, TASK-003, TASK-004, TASK-005, TASK-006, TASK-007, TASK-008, TASK-009, TASK-039 |
| ADR-001 | TASK-002, TASK-013, TASK-014, TASK-015, TASK-016, TASK-017, TASK-020, TASK-021, TASK-022, TASK-024, TASK-032, TASK-044, TASK-045, TASK-046, TASK-047, TASK-048, TASK-053, TASK-054, TASK-058 |
| ADR-002 | TASK-002, TASK-010, TASK-016, TASK-025, TASK-028, TASK-031, TASK-033, TASK-044, TASK-053, TASK-057, TASK-059, TASK-060, TASK-067, TASK-068, TASK-069 |
| ADR-003 | TASK-002, TASK-011, TASK-017, TASK-018, TASK-020, TASK-021, TASK-022, TASK-023, TASK-040, TASK-045, TASK-055, TASK-056, TASK-067, TASK-068, TASK-069 |
| ADR-004 | TASK-002, TASK-020, TASK-021, TASK-022, TASK-024, TASK-037, TASK-041, TASK-045, TASK-046, TASK-047, TASK-048, TASK-061, TASK-062, TASK-063, TASK-064, TASK-065, TASK-066 |
| ADR-005 | TASK-010, TASK-016, TASK-019, TASK-020, TASK-021, TASK-022, TASK-023, TASK-024, TASK-031, TASK-044, TASK-054, TASK-063, TASK-067 |
| ADR-006 | TASK-010, TASK-013, TASK-025, TASK-026, TASK-027, TASK-028, TASK-029, TASK-049, TASK-051, TASK-052, TASK-053, TASK-055, TASK-056, TASK-057, TASK-058, TASK-059, TASK-060, TASK-067, TASK-068 |
| ADR-007 | TASK-002, TASK-018, TASK-020, TASK-021, TASK-032, TASK-046, TASK-055, TASK-056, TASK-058, TASK-068 |
| ADR-008 | TASK-012, TASK-019, TASK-023, TASK-042, TASK-043, TASK-045, TASK-046, TASK-047, TASK-048, TASK-056, TASK-067, TASK-069 |
| ADR-009 | TASK-010, TASK-016, TASK-019, TASK-023, TASK-031, TASK-032, TASK-054, TASK-056, TASK-057, TASK-060, TASK-067, TASK-068, TASK-069 |
| ADR-010 | TASK-010, TASK-029, TASK-030, TASK-032, TASK-050, TASK-059, TASK-060, TASK-068, TASK-069 |
| ADR-011 | TASK-018, TASK-023, TASK-032, TASK-055, TASK-058, TASK-068 |
| ADR-012 | TASK-011, TASK-019, TASK-023, TASK-040, TASK-045, TASK-067, TASK-069, TASK-070 |
| ADR-013 | TASK-009, TASK-037, TASK-061, TASK-062, TASK-063, TASK-064, TASK-065, TASK-066 |
| ADR-014 | TASK-023, TASK-067, TASK-069, TASK-070 |
| ADR-015 | TASK-069, TASK-070 |
| ADR-016 | TASK-027 |
| ADR-017 | TASK-015 |
| ADR-018 | TASK-015 |
| ADR-019 | TASK-073 |
| ADR-020 | TASK-031 |
| ADR-021 | TASK-103 |

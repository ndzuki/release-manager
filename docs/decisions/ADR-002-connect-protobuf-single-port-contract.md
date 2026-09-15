# ADR-002：Connect 与 protobuf 单端口统一协议面

- Status: accepted
- Date: created 2026-07-27 / updated 2026-07-27
- Scope: project
- Related: REQ-002, REQ-010, REQ-016, REQ-025, REQ-028, REQ-031, REQ-033, REQ-044, REQ-053, REQ-057, REQ-059, REQ-060, REQ-067, REQ-068, REQ-069 (Requirements); TASK-002, TASK-010, TASK-016, TASK-025, TASK-028, TASK-031, TASK-033, TASK-044, TASK-053, TASK-057, TASK-059, TASK-060, TASK-067, TASK-068, TASK-069 (Tasks)

## Status

accepted

## Context

后端由多个 Go 服务组成，同时服务浏览器、服务间调用和 Operator 流式连接。若分别维护 REST、gRPC 与自定义 WebSocket/HTTP 协议，同一领域契约会重复定义并产生错误码、鉴权和生成客户端漂移。

## Decision

所有正式业务接口以 protobuf 为唯一契约源，使用 Buf 生成 Go/TypeScript 客户端和 Connect handler。每个服务通过标准 net/http ServeMux 的单一端口同时提供 Connect、gRPC 和 gRPC-Web 能力；浏览器使用生成的 Connect Web 客户端，Operator 使用 Connect stream。删除独立 release-api 网关，不新增 raw grpc-go、第三方 router 或手写 JSON RPC。共享 ID、时间、分页、错误、request_id 与幂等语义集中在 common contract 中。

## Alternatives Considered

- **保留独立 REST API 网关**：会复制 protobuf 映射、认证和错误处理，并增加一层部署与版本兼容。
- **服务内分别实现 REST 和 gRPC**：会让字段、状态码和幂等语义出现双重权威。
- **为实时页面另建 WebSocket/SSE 协议**：现有 Connect server streaming 已覆盖流式需求，新增协议只会扩大生命周期和鉴权面。

## Consequences

- 协议和生成代码有单一来源，浏览器与服务端共享类型。
- protobuf 变更必须遵守兼容演进；生成文件只能由 Buf 更新。
- 服务需要在 Connect interceptor 中统一完成身份、授权、request_id、CSRF 和错误清洗。

## Requirement and Task Links

- Requirements: REQ-002-micro-service, REQ-010-shared-api-contracts, REQ-016-operator-control-stream, REQ-025-local-auth-sessions, REQ-028-external-identity-providers, REQ-031-notification-delivery, REQ-033-web-auth-shell, REQ-044-operator-session, REQ-053-web-operator-enrollment, REQ-057-web-operation-timeline, REQ-059-web-audit-query, REQ-060-web-notification-jobs, REQ-067-operation-creation-workflow, REQ-068-values-approval-workflow, REQ-069-artifact-lifecycle-policy
- Tasks: TASK-002-micro-service, TASK-010-shared-api-contracts, TASK-016-operator-control-stream, TASK-025-local-auth-sessions, TASK-028-external-identity-providers, TASK-031-notification-delivery, TASK-033-web-auth-shell, TASK-044-operator-session, TASK-053-web-operator-enrollment, TASK-057-web-operation-timeline, TASK-059-web-audit-query, TASK-060-web-notification-jobs, TASK-067-operation-creation-workflow, TASK-068-values-approval-workflow, TASK-069-artifact-lifecycle-policy

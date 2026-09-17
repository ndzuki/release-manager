# Kulala HTTP 调试集合（api/kulala）

`README.md:58` 把 `api/kulala/` 定位为「Kulala/Neovim HTTP 调试集合」。TASK-093 把整套集合
从 Connect 迁移之前的 REST/gRPC 形状**重写为 ADR-002 的单端口 Connect 形状**，并加了结构性门禁
`make api-check`。本文写现状（§1）、怎么用（§2）、覆盖了什么（§3）、门禁校验什么（§4）、与旧集合
的差异（§5）、常见坑（§6）。

## 1. 现状

- 六个集合：`auth.http`、`orchestrator.http`、`audit.http`、`webhook.http`、`notifier.http`
  （新增）、`operator.http`（**不可达声明**，见 §3）。`manager.http` 已删除：它针对已移除的
  Manager 单进程，其中的 RPC 在 `orchestrator.http` 有对应请求。
- 每个请求都是 `METHOD {{ENV_VAR}}/<package>.<Service>/<Method>`，`Content-Type: application/json`，
  字段用 protobuf JSON 的 **camelCase** 名。
- 端口/主机来自 `http-client.env.json` 的两个 profile，且**由事实源派生**：`dev` 用
  `configs/*.dev.yaml` 的 `http_port`，`cluster` 用 `deploy/kustomize/services/*.yaml` 的 `nodePort`。
- `make api-check` 校验上述不变量（§4）；它已并入 `make quality`，也在 CI 的 `test` job 里随
  `go test ./...` 跑。
- 打开集合不需要 target：`nvim api/kulala/auth.http`，然后在 Kulala 里选 `dev` 或 `cluster` profile。
  **TASK-093 删除了六个只负责打开 `nvim` 的 `api-<service>` 目标**——它们让集合在无人校验的情况下
  烂掉；可复现的替代是 `make api-check`。

## 2. 怎么用

1. 起环境：`make dev-up`（host-run 形态，端口 8082–8087）或 k3d 集群形态（NodePort 30082–30087，
   见 `docs/dev-environment.md`）。profile 对应关系：`dev` → host-run，`cluster` → k3d。
2. 导出凭据（都不入库、不进集合文件）：
   - 管理员口令：`set -a; . data/dev-credentials.env; set +a`（`dev.sh` 生成，含 `DEV_ADMIN_PASSWORD`）
   - 服务凭据：`export DEV_CI_API_KEY="$(cat data/dev-service-tokens/ci-api-key)"`、
     `export DEV_HARBOR_API_KEY="$(cat data/dev-service-tokens/harbor-service-token)"`
3. 在 `auth.http` 先跑 `GetInitStatus`（public）与 `Login`：后者的 post-request 脚本把
   `accessToken` 写进 `{{AUTH_TOKEN}}`，其余集合复用同一个变量。`SwitchOrganization` 同样会刷新它。
4. 其余请求按文件顺序跑即可（`orchestrator.http` 的列表请求会捕获首个 `CUSTOMER_ID`）。
5. 只读优先：写请求（`CreateOperation`、`RunCleanup`、`SubmitReleaseBundle`、`Emit` 等）在文件里
   以注释形式给出形状，避免「一键跑全部」产生副作用。

## 3. 覆盖与不可达

| 集合 | 服务 | 代表性请求（只读优先） |
| --- | --- | --- |
| `auth.http` | `auth.v1.AuthService` | `GetInitStatus`、`Login`、`ValidateToken`、`GetLocalUser`、`ListLocalUsers`、`SwitchOrganization` |
| `auth.http` | `auth.v1.OrganizationService` | `ListOrganizations`、`GetOrganization`、`ListMembers` |
| `auth.http` | `auth.v1.BindingService` | `ListBindings`、`GetBinding` |
| `auth.http` | `auth.v1.AuthorizationService` | `GetAuthorizationSnapshot`、`AuthorizeAccess`（TASK-103/ADR-021 的判定 RPC） |
| `auth.http` | `auth.v1.ExternalIdentityService` | **不可达**：契约与 handler 在，但 `cmd/auth` 从不构造/挂载该服务（`docs/api.md` §3.8） |
| `orchestrator.http` | `orchestrator.v1.OrchestratorService` | 客户/集群/定义/路由、`ListOperators`、`ListOperations`、`GetOperation`、Values/Secret 列表、`ListEmergencyTargets`、`CheckEmergencyConflict`、`ListConvergenceTasks`、`ListStuckLocks`、`ListCandidateArtifacts`、`ListReleases`、`TriggerInventorySync` |
| `orchestrator.http` | `orchestrator.v1.BundleService` | `ListBundles`、`GetBundle` |
| `orchestrator.http` | `trust.v1.TrustService` | `GetTrustPolicy` |
| `orchestrator.http` | `orchestrator.v1.CleanupService` | **无只读 RPC**：`RunCleanup`/`UnarchiveBundle` 均为 platform_admin 写操作，只给形状 |
| `audit.http` | `audit.v1.AuditService` | `QueryAuditEvents`（`audit/read`，本组织 + 31 天窗口）；`Emit`/`ExportAuditEvents` 给形状 |
| `webhook.http` | `webhook.v1.WebhookService` | `SubmitReleaseBundle`（CI key + `Idempotency-Key`），以及非 Connect 的 `POST /webhooks/harbor`（Harbor key） |
| `notifier.http` | `notifier.v1.NotifierService` | `GetStatus`（该服务当前**没有任何认证**，`Send` 只给形状——这正是 TASK-096 要收的口子） |
| `operator.http` | `operator.v1.OperatorService` | **不可达**：只挂在 mTLS agent gateway 上，需要客户端证书，Kulala 无法提供 |

## 4. `make api-check` 校验什么

`internal/quality/httpcollections` 里的门禁（与 `check-probes`/`check-migrations` 同一形态）在**不
起任何服务**的前提下断言：

1. 每条请求行都必须是 `METHOD {{ENV}}/<package>.<Service>/<Method>`，且该 RPC 在 `api/proto/**`
   里真实存在（proto 的 `package` + `service` + `rpc` 三级解析）；`webhooks/harbor` 这类**非 Connect
   路由**走显式登记表，不能靠「跳过未知路径」蒙混。
2. 每个 `{{占位符}}` 都能解析：同文件的 `@文档变量`、`http-client.env.json` 每个 profile 的键，或
   文档化的进程变量（`DEV_CI_API_KEY`/`DEV_HARBOR_API_KEY`/`DEV_ADMIN_PASSWORD`）与脚本捕获变量
   （`AUTH_TOKEN`）。
3. `http-client.env.json` 的每个 `*_URL` 都是 `http://localhost:<port>`，且端口必须在事实源里：
   `dev` → `configs/*.dev.yaml` 的 `http_port`；`cluster` → kustomize 的 `nodePort`（唯一登记例外：
   `API_URL`，release-api 在 dev 是 host-run，见 `docs/api.md` §1 注 2）。
4. 没有集合会悄悄回到 `/api/v1` 这类已删除的 REST 面；空集合必须显式写 `NOT REACHABLE` 并说明原因。

负控制（门禁必须能失败）：`TestCollectionGateRejectsHistoricalShape` 用合成树注入 REST 路径、不存在的
procedure、硬编码 URL、未定义占位符；`TestCollectionGateRejectsDeadPorts` 注入死端口；
`TestUnreachableSurfaceIsAllowed` 断言「空集合必须说明原因」。

## 5. 与旧集合的差异（TASK-093）

| 旧 | 新 |
| --- | --- |
| `GET {{BASE_URL}}/api/v1/...`（REST 面已随 Manager 单进程移除） | Connect 路径 `<package>.<Service>/<Method>` |
| `GRPC host:8446` 线格式（无进程监听，且未注册 gRPC health） | 同一端口上的 Connect/JSON（单一端口契约，ADR-002） |
| 端口写死 8081/8080/8446/8447 | `http-client.env.json` 由 `configs/*.dev.yaml` 与 kustomize NodePort 派生 |
| `{{token}}` 等占位符无人赋值 | `Login` 的 post-request 脚本 `client.global.set("AUTH_TOKEN", ...)` |
| 6 个 `make api-<service>` 只做 `nvim` | 一个 `make api-check`；打开即 `nvim api/kulala/<file>.http` |
| `manager.http`（已删除服务的 REST 集合） | 删除；对应 RPC 在 `orchestrator.http` |
| 无覆盖校验 | `make api-check`（并入 `make quality`，CI 随 `go test ./...` 跑） |

## 6. 常见坑

- **camelCase**：proto3 JSON 映射的规范名是 camelCase（`orgId`、`releaseDefinitionId`、`pageSize`）；
  snake_case 也被接受，但集合统一用 camelCase，便于与 `web/src/gen/auth/v1/auth_pb.ts` 这类生成类型对照。
- **错误形状**：Connect 非 200 时响应体是 `{"code": "...", "message": "..."}`，业务原因码在
  `X-Reason-Code` 头（例如紧急通道离线是 `unavailable` + `operator_offline`）。
- **`Idempotency-Key`**：`CreateOperation`、`SubmitReleaseBundle` 等写 RPC 需要它；同 key 同 body
  会重放原结果，不是报错。
- **鉴权分层**：控制台路径用用户 JWT（`Login` 拿到的 `AUTH_TOKEN`）；`SubmitReleaseBundle` 用 CI key、
  `/webhooks/harbor` 用 Harbor key，两者**不可互换**（TASK-102）；审计面还要 release-auth 在线，
  否则按 ADR-021 fail closed 返回 `unavailable`。
- **release-api 只在 host-run 形态**：dev 里 8087 没有集群 NodePort，`cluster` profile 的 `API_URL`
  仍指向本机 8087（门禁里登记为例外）。
- **secret 不进集合**：所有口令/钥匙都来自环境变量或 `data/dev-service-tokens/*`，集合文件里只有占位符。

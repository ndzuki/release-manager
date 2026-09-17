# release-manager API 契约手册

本文是 `api/proto/**` 契约面与 Go 侧实现的对齐说明：谁在哪个端口提供哪些 RPC、怎么调用、如何认证鉴权、错误怎么表达、字段有什么约定、契约怎么改。所有结论均为本仓库静态核对结果并附 `文件:行号`；静态无法证实的一律标注「未见挂载」「未实现」「无法核实」并给出检索依据。本文不引入新规则，只描述现状。

## 1. 契约面总览

### 1.1 规模

14 个 proto 文件、8 个 proto 包（`audit.v1`、`auth.v1`、`common.v1`、`notifier.v1`、`operator.v1`、`orchestrator.v1`、`trust.v1`、`webhook.v1`）、13 个 `service`、104 个 RPC。统计方法：对 `api/proto/*/*/*.proto` 逐个匹配行首 `service X {` 与其中的 `rpc Y(` 声明；下表逐条列出，第 7 节的 13 张表与之总数一致（各服务方法名已用脚本与 proto 声明做过集合相等校验）。

| 包 | service | RPC 数 | service 声明位置 | 归属二进制 | dev 端口 |
| --- | --- | --- | --- | --- | --- |
| `audit.v1` | `AuditService` | 3 | `api/proto/audit/v1/audit.proto:116` | `release-api`（`cmd/api`） | 8087 |
| `auth.v1` | `AuthService` | 11 | `api/proto/auth/v1/auth.proto:182` | `release-auth`（`cmd/auth`） | 8085 |
| `auth.v1` | `OrganizationService` | 9 | `api/proto/auth/v1/auth.proto:392` | `release-auth` | 8085 |
| `auth.v1` | `BindingService` | 4 | `api/proto/auth/v1/auth.proto:516` | `release-auth` | 8085 |
| `auth.v1` | `AuthorizationService` | 3 | `api/proto/auth/v1/auth.proto:599` | `release-auth` | 8085 |
| `auth.v1` | `ExternalIdentityService` | 3 | `api/proto/auth/v1/auth.proto:668` | 无（未见挂载，见 3.8） | — |
| `notifier.v1` | `NotifierService` | 2 | `api/proto/notifier/v1/notifier.proto:80` | `release-notifier`（`cmd/notifier`） | 8086 |
| `operator.v1` | `OperatorService` | 4 | `api/proto/operator/v1/operator.proto:284` | `release-orchestrator` 网关/管理端口、`release-operator` gateway 模式 | 8083 / 8084 |
| `orchestrator.v1` | `BundleService` | 4 | `api/proto/orchestrator/v1/orchestrator.proto:156` | `release-orchestrator`（`cmd/orchestrator`） | 8083 |
| `orchestrator.v1` | `OrchestratorService` | 53 | `api/proto/orchestrator/v1/orchestrator.proto:981` | `release-orchestrator` | 8083（+ 网关 8084 仅 `SyncInventory`） |
| `orchestrator.v1` | `CleanupService` | 2 | `api/proto/orchestrator/v1/cleanup.proto:21` | `release-orchestrator` | 8083 |
| `trust.v1` | `TrustService` | 6 | `api/proto/trust/v1/trust.proto:158` | `release-orchestrator` | 8083 |
| `webhook.v1` | `WebhookService` | 1 | `api/proto/webhook/v1/webhook.proto:46` | `release-webhook`（`cmd/webhook`） | 8082 |

合计 3+11+9+4+2+3+2+4+4+53+2+6+1 = 104。RPC 类型：102 个 unary、1 个 server-streaming（`WatchOperation`，`api/proto/orchestrator/v1/orchestrator.proto:1041`）、1 个 bidirectional-streaming（`CommandStream`，`api/proto/operator/v1/operator.proto:335`），无 client-streaming。

`api/proto/common/v1/domain.proto`、`api/proto/common/v1/health.proto`、`api/proto/common/v1/trust.proto`、`api/proto/common/v1/types.proto`、`api/proto/operator/v1/upgrade_result.proto`、`api/proto/orchestrator/v1/vulnerability.proto` 只定义共享消息/枚举，`service` 计数为 0。

### 1.2 端口与监听器

dev 端口取 `configs/*.dev.yaml` 的 `http_port`：`configs/webhook.dev.yaml:1` 8082、`configs/orchestrator.dev.yaml:1` 8083、`configs/operator.dev.yaml:1` 8084、`configs/auth.dev.yaml:1` 8085、`configs/notifier.dev.yaml:1` 8086、`configs/api.dev.yaml:1` 8087。`release-orchestrator` 另有第二个监听器 `gateway.port: 8084`（`configs/orchestrator.dev.yaml`，host dev 默认 `gateway.enabled: false`）；k3d dev 环境里网关开启：`deploy/kustomize/dev/configs/orchestrator.dev.yaml:32-34`。

三点必须注意：

1. **8084 在 host dev 模式下属于 `release-operator`**（`configs/operator.dev.yaml:1`，`agent.mode: agent` 见 `configs/operator.dev.yaml:3-4`），与 `release-orchestrator` 的网关端口号相同但默认不同进程（网关本地关闭）。
2. **8087 在 k3d dev 环境里是 web SPA 的 nginx，不是 `release-api`**：k3d 把宿主 8082-8087 映射到 NodePort 30082-30087（`deploy/dev/dev.sh:726`），而 `deploy/kustomize/services/web.yaml:26` 的 `containerPort: 8087` 与 `:59` 的 `nodePort: 30087` 属于 web；`web/nginx.conf:7` 监听 8087。因此 `release-api` 的审计面只在 host 直跑模式（`make run-api`、`make dev-stage-audit`）下可达 8087。
3. **网关监听器不提供任何探测端点**：`GET /health`、`GET /readyz`、`GET /environment` 由共享启动包装注册在主 mux 上（`internal/app/app.go:140`、`:142`、`:144`、`:158`），网关用的是独立的 `gmux`（`cmd/orchestrator/main.go:166`），只做 TLS + 过程路径注册。`api/proto/common/v1/health.proto:5-10` 的文件注释也明确说明这一点。

`/metrics` 只有两个进程暴露：`cmd/auth/main.go:137`、`cmd/orchestrator/main.go:319`。

### 1.3 生成物

`api/proto/buf.gen.yaml:1-15`：`buf.build/protocolbuffers/go` 与 `buf.build/connectrpc/go`（`paths=source_relative`）产出 `api/gen/**`；`buf.build/bufbuild/es`（`target=ts`、`import_extension=none`）产出 `web/src/gen/**`。`api/proto/buf.gen.web.yaml:1-16` 是只跑 es 插件、且把输入限定到 6 个 path 的 web 专用变体。`buf.yaml:1-13`：module path `api/proto`，`lint.use: [STANDARD]` 且豁免 `RPC_REQUEST_RESPONSE_UNIQUE`、`RPC_RESPONSE_STANDARD_NAME`、`RPC_REQUEST_STANDARD_NAME`，`breaking.use: [FILE]`。豁免的存在是因为多个 RPC 共用同一响应类型（例如 `SubmitValuesRevision`/`Approve`/`Reject` 共用 `ValuesRevisionDecisionResponse`，`api/proto/orchestrator/v1/orchestrator.proto:1064-1082`）以及 `rpc GetValuesRevision(...) returns (common.v1.ValuesRevision)`（`api/proto/orchestrator/v1/orchestrator.proto:1102`）直接返回共享实体。

## 2. 调用方式

### 2.1 单端口三协议

按 ADR-002（`docs/decisions/ADR-002-connect-protobuf-single-port-contract.md:18`），每个业务服务用一个 `net/http` 的 `http.ServeMux` 单端口同时提供 Connect、gRPC、gRPC-Web；不引入 raw grpc-go server、不加第三方 router、不再维护 REST 影子接口。实现上 `connect.New*ServiceHandler` 返回的 handler 自带三种协议，仓库代码只 `mux.Handle(path, handler)`（例如 `cmd/orchestrator/main.go:454`）。

结论：**同一 URL 既可被 Connect 客户端调用，也可被 gRPC / gRPC-Web 客户端调用**，无需额外开关；未见任何只启用子集的 `connect.WithProtocols(...)` 调用。

### 2.2 URL、方法与请求头

- 路径固定为 `/<pkg>.<Service>/<Method>`，来自生成代码的 `*Procedure` 常量（例如 `cmd/orchestrator/main.go:191` 用 `orchestratorv1connect.OrchestratorServiceSyncInventoryProcedure` 注册为 `"POST /orchestrator.v1.OrchestratorService/SyncInventory"`）。
- **所有 104 个 RPC 只能 POST**。connect-go 只在 unary + `option idempotency = no_side_effects` 时才注册 GET 方法（`protocol_connect.go:70-76`，模块 `connectrpc.com/connect@v1.20.0`），而本仓库 `api/proto/**` 中 `option idempotency`/`option timeout`/任何 rpc 级 option 出现次数为 0（14 个 proto 文件、104 条 `rpc` 声明全部无 option）（检索 `api/proto/*/*/*.proto` 的 `option ` 行，只有 `syntax`/`go_package`/`file` 级 option）。因此 GET 一律 **405 + `Allow: POST`**，而不是 404：业务服务用生成代码返回的子树 pattern 注册（例如 `api/gen/auth/v1/authv1connect/auth.connect.go:383` 返回 `"/auth.v1.AuthService/"`，`cmd/auth/main.go:191` 原样 `mux.Handle`），请求会进到 connect handler 再由它按方法表拒绝（`handler.go:274-279`）；只有网关上 `cmd/orchestrator/main.go:191` 用了 `"POST "` 前缀 pattern，此时 405 由 `http.ServeMux` 自己给出。附带一条：`Content-Type` 不在协议表内会返回 **415 Unsupported Media Type**（`handler.go:291-294`），所以漏写 `-H 'Content-Type: application/json'` 的 curl 不会得到业务错误而是 415。
- 编码：`Content-Type: application/json`（Connect JSON）或 `application/proto`（Connect 二进制）；gRPC 用 `application/grpc(+proto)`；gRPC-Web 用 `application/grpc-web(+proto)`。仓库内真实用例统一用 `Content-Type: application/json`（`test/e2e/prerequisite/smoke.sh:164`、`:175`、`:180`）。
- 追踪/关联头：`X-Request-ID`（`internal/contracts/errors.go:14`）。入站带头则沿用、否则生成 UUID，并在响应头与错误 metadata 中回显（`internal/contracts/interceptor/requestid.go:28-42`）。
- 浏览器会话相关：Cookie `rm_access`/`rm_refresh` 与 CSRF 双提交头 `X-CSRF-Token`（`internal/auth/service.go:17-20`，`internal/auth/interceptor.go:80-86`）。仅当 cookie 认证且 action 非 `read` 时才校验 CSRF。
- 写幂等：`Idempotency-Key` 请求头，见 5.5。

### 2.3 可运行的 curl 示例

以下写法直接取自仓库内已跑通的冒烟脚本 `test/e2e/prerequisite/smoke.sh`（`:163`、`:174`、`:187`、`:234`、`:249`、`:287`）与 dev fixture（`internal/devfixture/bundle.go:259`），端口为 dev 默认。

```bash
# 1) 登录取 access token（release-auth :8085）
TOKEN=$(curl -sS --fail -X POST http://127.0.0.1:8085/auth.v1.AuthService/Login \
  -H 'Content-Type: application/json' \
  -d '{"username":"platform-admin","password":"..."}' | jq -r .accessToken)

# 2) 校验 token（公开方法，无需 Authorization）
curl -sS --fail -X POST http://127.0.0.1:8085/auth.v1.AuthService/ValidateToken \
  -H 'Content-Type: application/json' -d "{\"token\":\"$TOKEN\"}"

# 3) 读操作：列 operator（release-orchestrator :8083）
curl -sS --fail -X POST http://127.0.0.1:8083/orchestrator.v1.OrchestratorService/ListOperators \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"pageSize":20}'

# 4) 写操作：带 Idempotency-Key 的 emergency
curl -sS -X POST http://127.0.0.1:8083/orchestrator.v1.OrchestratorService/ExecuteEmergencyChange \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"operator_id":"...","customer_id":"...","cluster_id":"...","reason":"...","idempotency_key":"smoke-1"}'

# 5) 探测端点（非 Connect，纯 JSON）
curl -sS http://127.0.0.1:8083/health
curl -sS http://127.0.0.1:8083/readyz
curl -sS http://127.0.0.1:8083/environment
```

`/health` 恒返回 200 与 `{"status":"ok"}`，`release-orchestrator` 额外带 `gc` 段（`internal/handler/health.go:12-28`）。`/readyz` 按依赖检查返回 200 `{"status":"ok",...}` 或 503 `{"status":"degraded",...}`（`internal/handler/ready.go:11-42`）。`/environment` 回报 `APP_ENVIRONMENT`/`ENVIRONMENT_ID`/`DEV_PROFILE`/`E2E_RUN_ID`/`APP_PRODUCTION`（`internal/app/app.go:91-113`）。

### 2.4 代码客户端

- Go：`orchestratorv1connect.NewOrchestratorServiceClient(httpClient, url)`，需要 gRPC 线格式时加 `connect.WithGRPC()`——仓库内实例见 `cmd/webhook/main.go:39-43`（`release-webhook` → `release-orchestrator` 的 `BundleService`）。
- TypeScript：`@connectrpc/connect` 的 `createClient` + `createConnectTransport`，见 `web/src/connect/client.ts:50-61`（`useBinaryFormat: true`、`fetch: browserFetch`）。浏览器侧固定 `credentials: include`（`web/src/connect/client.ts:46-48`），并由 `sessionInterceptor` 从 `rm_csrf` cookie 注入 `X-CSRF-Token`（`web/src/connect/client.ts:29-33`），该拦截器在 `unauthenticated`/`permission_denied` 时回调 `authErrorHandler`（`web/src/connect/client.ts:37-43`）。
- 调试集合：`api/kulala` 下的 `.http` 文件，配合仓库根的 `http-client.env.json`（不在 `api/kulala/` 内）。集合已按 Connect 单端口契约重写（TASK-093）：选好 `dev`/`cluster` profile 后从 `auth.http` 的 `Login` 开始（它把 access token 写进 `{{AUTH_TOKEN}}`），再用 `make api-check` 校验路径与端口。打开方式与凭据来源见 `docs/http-collections.md`。

### 2.5 代理与同源

浏览器不直连各服务端口。k3d 下由 `web/nginx.conf:17-71` 按过程前缀反代：`/auth.v1.` → `auth:8085`、`/orchestrator.v1.` → `orchestrator:8083`、`/webhook.v1.` → `webhook:8082`、`/operator.v1.` → `operator:8084`、`/notifier.v1.` → `notifier:8086`，外加 `/health`、`/readyz`、`/environment` 三条代理到 `orchestrator:8083`（`web/nginx.conf:77-100`）。host dev 下由 Vite 代理同名前缀（`web/vite.config.ts:25-59`，审计前缀指 `127.0.0.1:8087`）。

两点差异必须知道：

- **`/audit.v1.` 与 `/trust.v1.` 未被 nginx 代理**（`web/nginx.conf:17-71` 只有上述 5 个前缀），会落进 SPA fallback `location /`（`web/nginx.conf:103-105`）。k3d 环境下审计与 trust 的 Connect 面从 8087 不可达；Vite 侧则配了 `/audit.v1.AuditService`（`web/vite.config.ts:48-51`）。
- **Go 侧没有任何 CORS 处理**：检索 `Access-Control-Allow-Origin` 在 `cmd/**`、`internal/**` 的非测试代码中零命中。跨源直连不可行，必须由同源反代承载。

### 2.6 流式与 HTTP/2

`CommandStream` 是唯一 bidi-streaming。connect-go 对 bidi 请求在 `request.ProtoMajor < 2` 时直接回 `505 HTTP Version Not Supported`（`handler.go:264-271`）。因此：

- `release-operator` 自身端口显式开启明文 HTTP/2（`cmd/operator/main.go:75-81` 的 `SetUnencryptedHTTP2(true)`）。
- `release-orchestrator` 网关监听器通过 TLS ALPN `NextProtos: {h2, http/1.1}` 提供 h2（`cmd/orchestrator/main.go:198-204`）。
- `release-orchestrator` 管理端口 8083 **未见**任何 h2/h2c 配置（`cmd/orchestrator/main.go` 无 `ConfigureServer`，而 `internal/app/app.go:165-170` 只在服务实现 `serverConfigurer` 时才配置）。按代码推导：在 8083 上以明文 HTTP/1.1 调 `CommandStream` 会得到 505；此项属静态推导，**未做运行时验证**（本任务禁止启动服务）。

同一原因也影响 `cmd/webhook/main.go:39-43`：它以 `connect.WithGRPC()` 拨明文 `http://localhost:8083`，而 gRPC 线格式需要 HTTP/2；`release-orchestrator` 主端口未启用明文 h2。该链路在 dev 默认配置下预期不可用，属**运行时未验证疑点**（webhook 的 `/readyz` 用普通 HTTP GET 探测同一上游的 `/readyz`，不受线格式约束，见 `cmd/webhook/main.go:90`）。

## 3. 认证与鉴权（逐服务事实）

### 3.1 拦截器栈清单

`connect.WithInterceptors` 的参数顺序即拦截顺序（外层在前）。

| 挂载点 | 拦截器（外 → 内） | 定义位置 |
| --- | --- | --- |
| `AuditService` @8087 | `RequestID`, `ErrorSanitize`, `audit.NewJWTInterceptor` | `cmd/api/main.go:65-72` |
| 4 个 `auth.v1` service @8085 | `RequestID`, `ErrorSanitize`, `TraceInterceptor`, `MaintenanceInterceptor`, `auth.NewAuthInterceptor` | `cmd/auth/main.go:181-187` |
| `NotifierService` @8086 | `RequestID`, `ErrorSanitize` | `cmd/notifier/main.go:71-77` |
| `WebhookService` @8082 | `RequestID`, `ErrorSanitize` | `cmd/webhook/main.go:43-49` |
| `OrchestratorService` @8083 | `RequestID`, `ErrorSanitize`, `TraceInterceptor`, `MaintenanceInterceptor`, `NewAuthInterceptor`, `NewAuthStreamInterceptor` | `cmd/orchestrator/main.go:443-453` |
| `CleanupService` @8083 | `RequestID`, `ErrorSanitize`, `MaintenanceInterceptor(nil 白名单)`, `NewAuthInterceptor` | `cmd/orchestrator/main.go:462-470` |
| `BundleService` @8083 | `RequestID`, `ErrorSanitize`, `TryAllInterceptor(JWT, ServiceToken)` | `cmd/orchestrator/main.go:477-491` |
| `TrustService` @8083 | `TraceInterceptor`, `MaintenanceInterceptor`, `NewAuthInterceptor`（**无** `RequestID`、**无** `ErrorSanitize`） | `cmd/orchestrator/main.go:512-519` |
| `OperatorService` @8083 管理端口 | `RequestID`, `ErrorSanitize` | `cmd/orchestrator/main.go:369-376` |
| `OperatorService` @8084 网关 | `RequestID`, `ErrorSanitize` + `NewCertificateIdentityHandler` | `cmd/orchestrator/main.go:167-174` |
| `SyncInventory` 单路径 @8084 网关 | `RequestID`, `ErrorSanitize`, `NewSyncInventoryCertAuthInterceptor` | `cmd/orchestrator/main.go:183-192` |
| `OperatorService` @8084（`release-operator` gateway 模式） | `RequestID`, `ErrorSanitize` | `cmd/operator/main.go:259-266` |

**没有任何鉴权的真实挂载面**：`OperatorService`（网关监听器，仅 `GetActiveOperatorSession` 真正零凭证）。`WebhookService` 自 TASK-102 起由 CI API key 认证，`NotifierService` 自 TASK-096 起由服务令牌认证（见 3.3），两者均已不属此列。直接后果是 `operator.v1.OperatorService/GetActiveOperatorSession` 可被匿名调用——handler 自己只做 `operator_id` 空值与存在性检查（`internal/operator/active_session.go:16-31`），而网关的证书身份中间件只守 `CommandStream` 与 `RenewCertificate` 两条路径（`internal/operator/identity_handler.go:8-19`）。注意 `OperatorService` 并非「完全无防护」：`Enroll` 以一次性 enrollment token 为凭证、`CommandStream` 在网关侧走证书、`RenewCertificate` 在 handler 内要求证书身份，细节见 7.8。真正零凭证可达的是 `GetActiveOperatorSession`。

### 3.2 JWT

HS256 对称签名，`internal/auth/jwt.go:22-29`（`NewJWTManager(signingKey, accessTTL, refreshTTL)`），claims 为 `sub`/`uid`/`roles`/`org_id` + `jti`（`:32-37`）。`cmd/auth/main.go:140` 以 15 分钟 access / 7 天 refresh 构造。签名密钥来源：`--signing-key` flag，回退到 `JWT_SIGNING_KEY` 环境变量，默认值 `change-me-in-production`（`cmd/orchestrator/main.go:833`；`cmd/api/main.go` 同样以 flag 接收）。

校验链在 `internal/auth/interceptor.go:53-66`：取 `Authorization: Bearer`，无则回退 `rm_access` cookie，两者皆空 `unauthenticated`；`ValidateAccessToken` 失败 `unauthenticated: invalid token`。放行前还要过两道会话检查：`internal/auth/interceptor.go:111-114` 查库确认用户存在且 `status == active`，`:115-122` 确认存在未过期的 auth session（查库失败本身返回 `internal: session validation failed`），否则都是 `unauthenticated: session revoked`。

审计服务用的是**另一套**独立实现：`internal/jwtauth/jwt.go` 的 `Manager` + `internal/audit/interceptor.go:23-45`，只验签；它不内嵌 Casbin，也不读 release-auth 的库（ADR-015 每库一个权威）。TASK-095 先补了域归属（principal 组织），TASK-103 按 **ADR-021** 把角色判定也接到 release-auth：审计 interceptor 把调用方**自己的** Bearer 一并注入 principal（`internal/audit/interceptor.go:36-45`），handler 每个请求经 `internal/audit/decision.go:44-76` 调 `auth.v1.AuthorizationService/AuthorizeAccess`（200ms 超时），拿到 `allowed`/`reason`/有效组织/`allow_cross_organization`/`max_window_days` 后执行（`internal/audit/authorization.go:41-84`），无法取得判定即 `unavailable`（fail closed，不回落本地角色猜测）。窗口上限来自 release-auth 的返回值而非本地硬编码，超限 → `invalid_argument` + `range_too_large`（`internal/audit/authorization.go:86-105`，REQ-029 AC-029-02）。`Emit` 仍拒收 actor 组织与有效组织不一致的事件。仍未做：会话撤销的本地校验（由 release-auth 侧裁决覆盖），见 3.8。

### 3.3 service token（bundle ingress 与 Harbor ingress）

TASK-102 之后 bundle/Harbor ingress 共三把独立、可分别轮换的凭据（REQ-011 §562）：

| 凭据 | 环境变量 | 作用面 | 落点 |
| --- | --- | --- | --- |
| webhook service token | `DEV_WEBHOOK_SERVICE_TOKEN(+_PREVIOUS)` | **出站**：webhook → orchestrator `SubmitBundle`（actor `service:release-webhook`） | `cmd/webhook/main.go`、`cmd/orchestrator/main.go` 的 `serviceTokens()` |
| CI API key | `DEV_CI_API_KEY(+_PREVIOUS)` | **入站**：只放行 `WebhookService/SubmitReleaseBundle` | `cmd/webhook/main.go` 的 `auth.ServiceTokenInterceptor("release-ci", …)` |
| Harbor API key | `DEV_HARBOR_API_KEY(+_PREVIOUS)` | **入站**：只放行 `POST /webhooks/harbor`（`internal/webhook/token_auth.go` 的 `RequireToken`） | `cmd/webhook/main.go` |
| Harbor service token | `DEV_HARBOR_SERVICE_TOKEN(+_PREVIOUS)` | **出站**：Harbor 入口 → orchestrator `RecordArtifactEvent`（actor `service:release-harbor`） | `cmd/orchestrator/main.go` 的 `harborServiceTokens()` |

`auth.ServiceTokenInterceptor`（`internal/auth/service_token.go:28-70`）语义：`Authorization: Bearer` 缺失 → `unauthenticated`；**不在本腿白名单** → `unauthenticated`（让 `TryAllInterceptor` 继续尝试下一腿，这是多凭证并存的前提）；**在白名单但 procedure 不在 scope** → `permission_denied: procedure not allowed for service token`；通过则注入 actor 并跳过 Casbin Enforce。服务端只存 SHA-256 摘要，明文只来自环境/Secret（`cmd/orchestrator/main.go` 的 `tokenHashesFromEnv`）。AC-011-04/16/17 由 `internal/auth/service_token_routing_test.go` 的交叉用例钉死：CI key 打 Harbor procedure、Harbor key 打 `SubmitBundle` 均 `permission_denied`。

`TryAllInterceptor`（`internal/auth/multi_auth.go:22-42`）逐腿尝试，仅在 `unauthenticated` 时回退；JWT 路径的 `permission_denied`（例如 Casbin 拒绝）直接返回。

### 3.4 客户端证书（网关）

`SyncInventory` 走证书身份：必须出示可验证的客户端证书 → `unauthenticated`；`ca.CertSerial(cert)` 在 `store.Operators().GetByCertSerial` 查不到 → `unauthenticated`；请求内 `operator_id`/`customer_id`/`cluster_id` 与登记记录不一致，或 operator 已吊销/被替换 → `permission_denied`（`internal/orchestrator/sync_inventory_auth.go:30-60`）。证书序列号即身份权威（ADR-018，`docs/decisions/ADR-018-sha256-certder-10-hex-renew.md`）。

### 3.5 Casbin 授权模型

`NewAuthInterceptor` 的裁决顺序（`internal/auth/interceptor.go:44-133`）：

1. `publicMethods[procedure]` → 直接放行（`:49-51`）。release-auth 声明 5 个公开方法：`GetInitStatus`、`Initialize`、`ValidateToken`、`Login`、`RefreshToken`（`cmd/auth/main.go:173-179`）。release-orchestrator 四处挂载全部传 `map[string]bool{}`，即**没有公开方法**（`cmd/orchestrator/main.go:450`、`:468`、`:482`、`:517`）。
2. 解析 JWT（`:53-66`：`Authorization: Bearer` 缺失则回退 `rm_access` cookie，两者皆空 `unauthenticated: missing authentication credentials`，验签失败 `unauthenticated: invalid token`）。
3. 查显式登记表并解析 domain（`:68-79`）：`lookupProcedure(procedure)`（`internal/auth/procedure_policy.go:187-190`）取该 procedure 的策略行，未登记 → `permission_denied` + `X-Reason-Code: invalid_actor_context`；`resolveDomain`（`internal/auth/interceptor.go:243-255`）默认要求请求 `org_id` 等于 token 的 `org_id`，只有登记为 `targetOrg` 的 procedure（仅 `SwitchOrganization`）允许取请求里的目标组织；两者都无 → `invalid_actor_context`。`BundleService` 两个读方法与 `GetOrganization`/`UpdateOrganization`/`DisableOrganization` 的归属由 `enforceRequestBinding`（`:257-274`）另行反查。
4. cookie 会话下的非 read 请求校验 CSRF（`:80-86`）。
5. `enforceRequestBinding`（customer 绑定/禁用一致性）→ `:88-97`。
6. 策略行 `mode == modeCasbin` 时执行 `enforcer.Enforce(userID, domain, object, action)` → `:98-109`；`modeHandler` 的 procedure 跳过 Casbin，由 handler 自证授权。

TASK-095 把旧的「服务名包含 + 方法名前缀」推断（`mapServiceToObject`/`mapMethodToAction`）整体删除，改为 `internal/auth/procedure_policy.go:66-186` 的**显式 procedure → 授权登记表**（105 行，一行一个 procedure；TASK-103 新增 `AuthorizationService/AuthorizeAccess`）。每行的 `mode` 取值：`modeCasbin`（拦截器裁决）、`modeHandler`（handler 自证）、`modePublic`、`modePrincipalScope`（release-api 审计面）、`modeMTLS`、`modeUnintercepted`。两条门禁测试锁死这张表：`internal/auth/procedure_policy_test.go:90` 遍历 proto registry，新增 procedure 未登记即失败；`:121` 断言每个 `modeCasbin` 的 `(object, action)` 必须落在默认角色矩阵（非通配角色）的授予集合内，或显式标注 `adminOnly`。

角色 → 策略规则（`internal/auth/casbin.go:425-477`，角色常量 `internal/store/store.go:807-812`，仅 4 个角色）：

| object/action | platform_admin | release_admin | deployer | viewer |
| --- | --- | --- | --- | --- |
| `*`/`*` | ✅ | — | — | — |
| organization read/write | ✅ | ✅ | — | read |
| member read/write | ✅ | ✅ | — | read |
| binding read/write | ✅ | ✅ | — | — |
| release read/write | ✅ | ✅ | ✅ | read |
| bundle read | ✅ | ✅ | — | — |
| bundle write | ✅ | — | — | — |
| operator read / enroll / revoke | ✅ | read+enroll+revoke | read | read |
| customer read/write | ✅ | read | read | read |
| trust_root read/write | ✅ | read | read | read |
| auth read/write | ✅ | — | — | — |
| cleanup read/write | ✅ | — | — | — |

读法与后果（逐条对照上表，均由 `internal/auth/casbin.go:428-471` 的分支直接得出）：

- `CleanupService` 的两个 RPC 映射到 `cleanup/write`，`BundleService.SubmitBundle`/`RecordArtifactEvent` 映射到 `bundle/write`，`AuthService` 的本地用户管理（`CreateLocalUser`/`GetLocalUser`/`ListLocalUsers`）映射到 `auth/read|write`，`customer` 写面：**四角色里只有 `platform_admin` 命中**（它拿的是 `*`/`*`，`internal/auth/casbin.go:429`）。登记表把这一类显式标为 `adminOnly`，门禁会拒绝把「有非通配角色授予」的对标成 `adminOnly`。
- `release_admin` 有 `bundle` 的 **read 但没有 write**（`:448`）——bundle 入库刻意保持 admin 专属，注释里写明了原因（`:440-447`）。
- `deployer` 与 `viewer` 完全读不到 `organization`/`member`/`binding`/`auth`/`cleanup`，只能在 `release`/`operator`/`customer`/`trust_root` 上按上表行动。
- `operator` 对象的三个 action（`read`/`enroll`/`revoke`）在登记表里逐条写明（`internal/auth/procedure_policy.go` 的 `ListOperators`/`GetOperator`/`CreateEnrollmentToken`/`GetEnrollmentTokenStatus`/`RevokePendingEnrollmentToken`/`RevokeOperator` 行），因此 `RevokeOperator` 是 `operator/revoke` 而不是 `release/write`。
- `ChangePassword` 与 `Logout` 是 `modeHandler`：拦截器只做认证与会话校验，handler 用旧口令/当前会话自证（`ChangePassword` 在 TASK-095 之前落在 `auth/write`，只有 `platform_admin` 能改自己的口令，属实测缺陷，已一并修复）。
- 这张表是**基线**：`roleRules` 之后还会追加 `roleCapabilityRules` 的细粒度授权（`internal/auth/casbin.go:473-475`），即 `SetCapabilityGrant` 可以在角色之外临时放行。
- 快照按 organization（Casbin 的 `dom`）构建，唯一调用点是 `internal/auth/casbin.go:380`；快照不健康时 `Enforce` 一律返回 `policy_unavailable`（`:84-86`），写操作 fail closed。

授权一致性遵循 ADR-006（`docs/decisions/ADR-006-server-authoritative-organization-authorization.md:18`）：服务端是唯一裁决点，客户端传入的 actor/role/organization 不覆盖认证上下文。

### 3.6 维护模式白名单

`app.MaintenanceInterceptor(enabled, readOnly, logger)` 在 `maintenance: true` 时对不在白名单内的 unary procedure 返回 `unavailable`，message 恰为 `"maintenance"`（`internal/app/maintenance.go:14-28`）。配置项 `maintenance`（`internal/config/config.go:135` 的 `ServiceConfig.Maintenance`），env 名 `MAINTENANCE` 在 `internal/config/config.go:282` 的绑定表里登记（该表由 `bindDatabaseEnvironment` `:272` 装载）。

- release-auth 白名单 12 条（`cmd/auth/main.go:215-230`），其中 `Login`/`RefreshToken`/`ChangePassword`/成员与绑定写操作被刻意挡住。
- release-orchestrator 的 `OrchestratorService` 白名单 15 条读方法（`cmd/orchestrator/main.go:688-706`）。
- `TrustService` 白名单仅 `GetTrustPolicy`（`cmd/orchestrator/main.go:708-712`）。
- `CleanupService` 传 `nil`（`cmd/orchestrator/main.go:467`）→ 维护期两个 RPC 全被拒。
- **`BundleService` 未挂 `MaintenanceInterceptor`**（`cmd/orchestrator/main.go:477-491`），维护期仍可提交 bundle。
- 它是 `UnaryInterceptorFunc`，**流式 RPC 绕过维护模式**；`OperatorService` 与 `NotifierService`/`WebhookService`/`AuditService` 均未挂该拦截器。

### 3.7 授权映射缺陷与修复证据（TASK-095）

TASK-095 之前，`(object, action)` 由服务名包含 + 方法名前缀推断，5 个 procedure 因此恒 `permission_denied`。穷尽审计（遍历 proto registry 跑登记表）测得 **104 个 procedure 中 7 个「有 object 无 action」**：`Login`/`Initialize` 属公开路径不受影响，其余 5 个是真缺陷；另有 1 个（`ChangePassword`）虽 action 非空但落在无人被授予的 `auth` 对象上。现已全部改为显式登记（`internal/auth/procedure_policy.go:66-185`）：

| procedure | 修复前 | 修复后与证据 |
| --- | --- | --- |
| `auth.v1.AuthService/SwitchOrganization` | 恒 `permission_denied`：`resolveDomain` 先拒绝「请求组织 ≠ token 组织」，即使过了这关 `mapMethodToAction` 也没有 `Switch` 前缀 | 登记为 `modeCasbin` + `targetOrg`：domain 取请求的目标组织，动作 `(organization, write)`，handler 再校验目标组织成员资格。集成证据 `internal/auth/switch_organization_test.go:23-71`（真实 JWT 切换成功、返回 token 的组织 claim 与后续请求都落在目标组织）；语义证据 `:74-109`（按目标组织的角色判定） |
| `auth.v1.AuthorizationService/SetCapabilityGrant` | `Set` 前缀未映射，恒 403；它在 handler 自证表里，但那一步在映射之后，永远走不到 | 登记为 `modeHandler`：拦截器只认证与会话校验，handler 要求 `platform_admin`/`release_admin` 成员资格（`internal/auth/authorization_snapshot.go:147-150`） |
| `orchestrator.v1.OrchestratorService/CheckEmergencyConflict` | `Check` 前缀未映射 → 恒 403（web 前端在调用，`web/src/connect/emergency-api.ts:151`） | 登记 `(release, read)`；集成矩阵 `internal/auth/interceptor_unmapped_test.go:88-96` |
| `orchestrator.v1.OrchestratorService/TriggerInventorySync` | `Trigger` 前缀未映射 → 恒 403 | 登记 `(release, write)`；集成矩阵 `internal/auth/interceptor_unmapped_test.go:97-112` |
| `orchestrator.v1.BundleService/RecordArtifactEvent` | JWT 路缺 `Record` 前缀；service-token 路 scope 只含 `SubmitBundle` | JWT 路登记 `(bundle, write)`（`adminOnly`，与 `SubmitBundle` 对称，`internal/auth/interceptor_unmapped_test.go:113-128`）。Harbor 独立 key 未落地，登记为 REQ-011 后续项（见 3.8） |
| `auth.v1.AuthService/ChangePassword` | 落在 `auth/write`，而 `auth` 对象没有任何非通配授予 → 只有 `platform_admin` 能改自己的口令 | 登记为 `modeHandler`（handler 校验旧口令并吊销本人会话）；回归 `internal/auth/change_password_test.go:21-53` |

`Login`/`Initialize` 是审计中另外两个「有 object 无 action」的 procedure，第 1 步 `publicMethods` 即放行，不受影响。

### 3.8 契约有声明、当前未见挂载 / 未实现（登记项）

以下每一项都是「不受 Casbin 管」或「尚未实现」的显式登记，不是推测：认证归属逐个核实过，未实现项注明归属 REQ。

| 项 | 认证归属 / 事实 | 检索依据 |
| --- | --- | --- |
| `auth.v1.ExternalIdentityService`（3 个 RPC：`AuthenticateLDAP`、`GetOIDCAuthURL`、`GetDingTalkAuthURL`） | **预认证** IdP 入口（登录前调用），登记为 `modePublic`；契约存在，Go 侧类型 `ExternalIdentityService` 已定义（`internal/auth/external_idp_service.go:28`）并实现了三个 handler 方法（`:55`、`:69`、`:80`），构造函数 `NewExternalIdentityService` 也在（`:36`），文件末尾还有接口断言（`:325`），但**从未被构造、从未被挂载**；归属 REQ-028 | `cmd/auth/main.go:189-203` 只 new/挂载 4 个 service；检索 `NewExternalIdentityService(`、`ExternalIdentityServiceHandler`、`mux.Handle` 于 `cmd/**`、`internal/**` 的非测试代码中零命中，唯一残留是维护白名单里两条永不命中的条目（`cmd/auth/main.go:227-228`）。`docs/architecture.md:80` 已把它记为遗留项 |
| `orchestrator.v1.OrchestratorService/PublishRelease` | 已挂载，校验通过后返回 `Status: "not_implemented"`、`OperationId: ""`，不产生 Operation；发布流水线属 REQ-014/REQ-040 的后续实现 | `internal/orchestrator/service.go:498-502` |
| `orchestrator.v1.OrchestratorService/SyncInventory` | 两条挂载：JWT 面按 `(release, write)` 裁决；agent 网关面走客户端证书身份（见 3.4） | `internal/auth/procedure_policy.go` 的 `SyncInventory` 行、`cmd/orchestrator/main.go:167-187` |
| `audit.v1.AuditService/ExportAuditEvents` | 只登记一行 `pending` 导出记录（按 release-auth 判定的有效组织归属），仓库内**没有消费者**，导出不会真正完成 | `internal/audit/audit_service_handler.go:144-209`；检索 `AuditExports()` 的非测试命中只有接口与实现自身 |
| `orchestrator.v1.BundleService/RecordArtifactEvent` 的 Harbor 入口 | **TASK-102 已实现**：`POST /webhooks/harbor` 由 Harbor key 认证并挂载 adapter，出站以 `service:release-harbor` 调 `RecordArtifactEvent`（scope 仅该 procedure） | `cmd/webhook/main.go`、`internal/webhook/harbor_adapter.go`、`internal/webhook/harbor_ingress_test.go`、`cmd/orchestrator/main.go` 的 Harbor 腿 |
| `webhook.v1.WebhookService/SubmitReleaseBundle` | **TASK-102 已实现入站认证**：CI API key（`DEV_CI_API_KEY`）经 `ServiceTokenInterceptor` 收窄到本 procedure，其它 procedure 不可达 | `cmd/webhook/main.go` 的 `NewWebhookServiceHandler` 拦截器链；`internal/auth/service_token_routing_test.go` |
| `notifier.v1.NotifierService/Send`、`GetStatus` | 登记为 `modeServiceToken`（TASK-096）：release-notifier 挂载 `auth.ServiceTokenInterceptor`，作用域恰为本服务两个 procedure | `cmd/notifier/main.go` 的 `newNotifierHandler` |
| `operator.v1.OperatorService/Enroll`、`RenewCertificate`、`CommandStream`、`GetActiveOperatorSession` | 登记为 `modeMTLS`：agent 网关以可验证客户端证书为身份（`Enroll` 在建证书前另带一次性 enrollment token）。`GetActiveOperatorSession` 在网关证书中间件下有零凭证可达面，见 3.1 | `cmd/orchestrator/main.go:167-174`、`internal/operator/identity_handler.go:8-19` |
| `common.v1.HealthCheckRequest`/`HealthCheckResponse` | 只有消息定义、无 `service`，且没有任何消费者 | `api/proto/common/v1/health.proto:18`、`:21`；检索 `HealthCheckRequest` 除 `api/gen/**` 外零命中。真实探活是 Go 手写 JSON handler（`internal/handler/health.go:12`） |
| 浏览器 cookie 会话分支 | `internal/auth/browser_session.go` 实现了 `rm_access`/`rm_refresh`/`rm_csrf` 下发，但 `cmd/auth/main.go:189` 调用 `NewAuthService` 时**未传**可选的 `BrowserSessionConfig`，使 `browserEnabled=false`（`internal/auth/service.go:43-49`）→ cookie 被拦截器接受却从不签发 | 参数为可变长 `browser ...BrowserSessionConfig`，`enabled := len(browser) > 0` |
| `auth.v1.BindingService/CreateBinding` | 已挂载，但 release-auth 注入的是 `auth.StubResolver{}`（`cmd/auth/main.go:155`），`Resolve` 恒 `ErrNotFound` → 恒 `not_found: customer_not_found`；`ConnectCustomerResolver` 无生产调用者 | `internal/auth/customer_resolver.go:20-53`、`:57-64` |
| ~~`api/kulala/*.http`~~ | **已由 TASK-093 修复**：六个集合重写为 Connect 单端口形状并按服务拆分（`manager.http` 删除，新增 `notifier.http`），`http-client.env.json` 的端口改由 `configs/*.dev.yaml` 与 kustomize NodePort 派生，`make api-check` 结构性校验「路径必须是真实 RPC / 占位符必须可解析 / 端口必须来自事实源」；`operator.http` 明确标注 mTLS 不可达 | 已修复 |

已修复、不再属于缺口的历史项：`cmd/api` 归档 worker 的 `Run`/`Close` 签名不匹配（TASK-094；现由 `cmd/api/main.go:45-48` 的编译期断言保证）。

## 4. 错误语义

### 4.1 统一约定

契约层不定义错误消息，一律使用 Connect 的 `code` + `message` + `details` 三元组；仓库自定义的 `common.v1.ErrorDetail` 与 `common.v1.RequestMetadata`（`api/proto/common/v1/types.proto:41`、`:62`）**没有任何生产者或消费者**，不是实际错误 envelope（检索两个类型名，除 `api/gen/**` 与自身定义外零命中）。

真实边界是 `ErrorSanitizeInterceptor`（`internal/contracts/interceptor/errorsanitize.go:38`，`sanitize` 主体 `:69-96`）：

- 非 `*connect.Error` 的错误（普通 `error`）→ 统一 `internal` + `"internal error"`（`:94-95`，经 `genericInternalError` `:177-182`），原始文本只进日志（`logDetail` `:169-175`）。
- `internal` → 同样抹成固定 `"internal error"`（`:88-91`）。
- 非 `internal` 的业务错误保留原 code 与 message；但 `unavailable` 额外核验 `%w` 包装链，链上出现未知内部错误即降级为 `internal`（`:79-83`、`hasInternalWrap` `:105-153`）。允许穿透的白名单是：`store` 哨兵 `ErrNotFound`/`ErrOptimisticLock`/`ErrDuplicateKey`/`ErrReleaseBusy`/`ErrInvalidCursor`/`ErrBindingRevoked`/`ErrApprovalPending`/`ErrIdempotencyConflict`/`ErrInvalidState`/`ErrDefinitionOwnerUnresolved`/`ErrNotAuthorized`/`ErrEmergencyConflict`/`ErrAuthorizationStale`（`:111-125`）、四个结构化 store 冲突错误（`:131-137`）、授权错误族（`:139-144`），以及无包装链的纯文本错误（`:146-149`）。
- `X-Request-ID` 在成功响应头上回显（`internal/contracts/interceptor/requestid.go:32-34`），在错误 metadata 里回显（`:35-39`）。

`internal/contracts/errors.go` 的 `NewAppError`（`:21`）、`NewAppErrorf`（`:28`）、`ToConnectError`（`:38`）在非测试代码中**零调用者**——它们不是现状语义，只有注释规范意义（`:16-20` 要求 message 稳定且不泄漏内部细节）。

### 4.2 code 使用分布（实测计数）

统计口径：`cmd/**` 与 `internal/**` 下非 `_test.go` 文件中 `connect.Code*` 的出现次数（含构造与比较），排除 `api/gen/**`、`web/**`、`test/**`。下表「计数」为总出现次数，括号内为 `connect.NewError(connect.CodeX, ...)` 的构造次数——两者之差说明该 code 更多是被读取/比较而非被抛出。

| Connect code | 计数 | 惯用场景 |
| --- | --- | --- |
| `InvalidArgument` | 180（构造 47） | 字段缺失/格式非法/枚举越界/cursor 非法 |
| `Internal` | 265（构造 225） | 存储与依赖异常；经 sanitizer 收敛为 `internal error` |
| `PermissionDenied` | 110（构造 45） | Casbin 拒绝、scope 不匹配、CSRF 失败、证书身份不符 |
| `Unauthenticated` | 92（构造 59） | 缺 token、token 无效、会话被吊销、service token 缺失 |
| `NotFound` | 78（构造 44） | 实体不存在；跨租户越权读取也返回 `not_found`（ADR-006 的 scope mismatch 语义） |
| `FailedPrecondition` | 90（构造 47） | 状态机不允许、乐观锁冲突（definition/customer 路径）、审批与门禁拒绝 |
| `Unavailable` | 53（构造 16） | 维护模式、授权快照不健康、依赖未接线 |
| `AlreadyExists` | 33（构造 13） | 幂等键冲突、重名、初始化已完成 |
| `Aborted` | 22（构造 9） | 乐观锁冲突（organization/cluster/`RevokeBinding` 路径） |
| `ResourceExhausted` | 5（构造 3） | 登录限流（`internal/auth/service.go:66`）、values 体积超限（`internal/orchestrator/values_revision.go:89`、`:415`）、cleanup 并发配额（`internal/orchestrator/cleanup.go:218`） |
| `OutOfRange` | 1（构造 1） | `WatchOperation` 游标过期（`internal/orchestrator/service.go:629`） |
| `Unimplemented` | **0**（构造 0） | 未使用：唯一的桩 `ListOperations` 已由 TASK-095 实现 |
| `DeadlineExceeded` | 2（构造 0） | 仅出现在比较/透传，不作主动返回 |
| `Conflict` | **0**（构造 0） | 未使用：冲突一律映射到 `Aborted`/`FailedPrecondition`/`AlreadyExists` |
| `Canceled` | **0**（构造 0） | 未使用：取消是业务动作（`CancelOperation`），不用 code 表达 |
| `DataLoss` | **0**（构造 0） | 未使用 |
| `Unknown` | 1（构造 0） | 仅比较，不作主动返回 |

### 4.3 结构化错误细节与 metadata

有 4 个类型化 detail，均通过 `connect.NewErrorDetail` 附着（消费方用 `ConnectError.findDetails` 解码，见 `docs/architecture.md:100`）：

| detail | proto 位置 | 构造点 |
| --- | --- | --- |
| `orchestrator.v1.OperatorErrorDetail` | `api/proto/orchestrator/v1/orchestrator.proto:719-721` | `internal/orchestrator/operator.go:426-436` |
| `orchestrator.v1.EmergencyErrorDetail` | `api/proto/orchestrator/v1/orchestrator.proto:1632-1635` | `internal/orchestrator/emergency.go:740-752` |
| `orchestrator.v1.CreateOperationGateDetail` | `api/proto/orchestrator/v1/orchestrator.proto:203-205` | `internal/orchestrator/service.go:1306-1314` |
| `orchestrator.v1.RouteValidationDetail` | `api/proto/orchestrator/v1/orchestrator.proto:759-763` | `internal/orchestrator/cluster.go:323-330` |

未见使用 Google `errdetails`（检索 `google.golang.org/genproto/googleapis/rpc/errdetails` 零命中）。更常见的是 **metadata 承载 reason**：

- `X-Reason-Code` — 写入点包括 `internal/operator/errors.go:30`、`internal/orchestrator/values_revision.go:476-479`、`internal/orchestrator/emergency.go:731-742`、`internal/orchestrator/service.go:630`、`internal/auth/interceptor.go:293-302`。取值如 `invalid_actor_context`、`policy_unavailable`、`cursor_expired`、`size_exceeded`、`optimistic_lock_conflict`。
- `X-Policy-Version`（授权策略版本，`internal/auth/interceptor.go:300`）、`X-Conflict-Task-Ids`、`X-Sync-Request-ID`、`X-Snapshot-Sequence`、`X-Retained-From-Sequence`、`X-Snapshot-Proto`（base64 protojson 快照，`internal/orchestrator/service.go:628-638`）。

reason code 风格不统一：同一份代码里存在三种做法——类型化 detail、纯 metadata、把 reason 拼进 message 前缀（`internal/orchestrator/bundle_service.go:589-591`）。写新代码时按所在包的既有风格对齐，不要跨包发明第四种。

### 4.4 存储层错误映射

`internal/store/store.go:31-80` 定义稳定哨兵（`ErrNotFound`、`ErrOptimisticLock`、`ErrDuplicateKey`、`ErrUnavailable`、`ErrReleaseBusy`、`ErrInvalidCursor`、`ErrBindingRevoked`、`ErrApprovalPending`、`ErrIdempotencyConflict`、`ErrCleanupAlreadyRequested`、`ErrInvalidState`、`ErrNotAuthorized`、`ErrPendingTokenExists`、`ErrDuplicateOperatorName`、`ErrTokenExpired`、`ErrCertificateConflict`、`ErrEmergencyConflict`、`ErrAuthorizationStale`、`ErrRootNotLive`、`ErrLastRootRemovalForbidden`、`ErrBundleNotReady`、`ErrBundleRejected` 等），另有 CAS 结构化错误（`:84-124`）。服务层负责翻译：`internal/auth/errors.go:85`（`mapStoreError`）、`internal/orchestrator/operator.go:438-454`（`mapOperatorStoreError`）。**新增哨兵必须同时进入 `internal/contracts/interceptor/errorsanitize.go:111-144` 的白名单**，否则该哨兵触发的 `unavailable`/`internal` 会被降级成通用 `internal error`，客户端拿不到可判别信息。

## 5. 分页、时间、枚举与 ID 约定

### 5.1 分页

契约里没有统一的 `page`/`offset`，全部是 opaque cursor。共享消息 `common.v1.Pagination{page_size, page_token}`（`api/proto/common/v1/types.proto:21-30`）与 `common.v1.PaginationResponse{next_page_token, total_size}`（`:33-38`）**只被两个 RPC 使用**（检索 `common.v1.Pagination`/`common.v1.PaginationResponse` 的全部声明点恰好 4 处：`api/proto/audit/v1/audit.proto:90`、`:96` 与 `api/proto/orchestrator/v1/orchestrator.proto:117`、`:123`）：`audit.v1.QueryAuditEvents` 与 `orchestrator.v1.BundleService/ListBundles`。其余分页 RPC 用内联字段，命名有三套：

| RPC | 请求字段 | 响应字段 | 出处 |
| --- | --- | --- | --- |
| `ListOperators` | `page_size`, `page_token` | `next_page_token`, `total_count` | `api/proto/orchestrator/v1/orchestrator.proto:632`、`:637-638`、`:643-644` |
| `ListReleases` | `page_size`, `cursor` | `next_cursor`, `total_count` | `api/proto/orchestrator/v1/orchestrator.proto:923`、`:928-929`、`:934-935` |
| `ListValuesRevisions` | `page_size`, `cursor` | `next_cursor` | `api/proto/orchestrator/v1/orchestrator.proto:842`、`:845-846`、`:851` |
| `ListOperations` | `limit`, `cursor` | `next_cursor` | `api/proto/orchestrator/v1/orchestrator.proto:1836`、`:1839-1840`、`:1845` |
| `ListLocalUsers` | `cursor`, `page_size` | `next_cursor` | `api/proto/auth/v1/auth.proto:164`、`:165-166`、`:171` |

`docs/architecture.md:104` 写的是「默认 `pageSize=50`、上限 `100`」，而共享 Go 助手 `internal/contracts/pagination.go:73-88` 的实际语义是：`NormalizePageSize(n)` → `n<=0` 得 **20**、`n>100` 静默夹到 100。二者不一致，且各 RPC 行为又分三种：

- 静默夹取：`ListReleases`（`internal/orchestrator/inventory_query.go:46`）、`QueryAuditEvents`（`internal/audit/audit_service_handler.go:79-84`）、`ListLocalUsers`（`internal/store/sqlite/users.go:192`、`internal/store/postgres/users.go:193`）、`ListBundles`（`internal/store/postgres/bundles.go:189`）。
- 越界报错：`ListOperators` 对 `<0 || >100` 返回 `invalid_argument: page_size must be between 1 and 100`（`internal/orchestrator/operator.go:54-60`）；`ListValuesRevisions` 同样报错（`internal/orchestrator/values_revision.go:246-248`）。
- 引擎差异：`ListBundles` 在 SQLite 下恒失败（`internal/store/sqlite/bundles.go:379-381` 返回 `sqlite bundle listing is unsupported`），即该 RPC **仅 PostgreSQL 可用**。

游标实现：`EncodeCursor` 把 `"<unixnano>|<id>"` 做 base64url（`internal/contracts/pagination.go:25-28`），`KeysetPredicate` 生成 `(created_at < ? OR (created_at = ? AND id < ?))` 形式的严格排除条件（`:65-71`），因此排序稳定、翻页期间新增行不影响已取页。游标与查询形状绑定（bundle/values/inventory/operators 都把 filter 或 snapshot 版本编进 query_hash，例如 `internal/store/postgres/bundles.go:293-301`、`internal/orchestrator/operator.go:377-391`），改 filter 会被拒绝而不是悄悄重定位。非法/过期游标一律 `invalid_argument`（`invalid_cursor`、`invalid_page_token`、`invalid or expired cursor`）。

流式续传是另一套机制：`WatchOperation` 的 `after_sequence`（`api/proto/orchestrator/v1/orchestrator.proto:331` 的 `WatchOperationRequest`，字段在 `:335`），游标过界返回 `out_of_range` + `X-Reason-Code: cursor_expired` 及快照 metadata（`internal/orchestrator/service.go:628-638`）。

**无分页的列表 RPC**（契约层就没有分页字段）：`ListOrganizations`、`ListMembers`、`ListBindings`、`ListReleaseDefinitions`、`ListCustomers`、`ListCustomerEvents`、`ListClusters`、`GetClusterRoutes`、`ListSecrets`、`ListEmergencyTargets`、`ListCandidateArtifacts`、`ListConvergenceTasks`、`ListStuckLocks`、`ListReleaseInventory`（后者请求为空消息，注释明确「返回全部行」）。新增列表 RPC 时应显式选择分页形态，不要再发明第四套字段名。

### 5.2 时间与时长

标准是 `google.protobuf.Timestamp`（`api/proto/common/v1/types.proto:16` 等 9 个文件导入）。JSON 线上渲染为 RFC 3339 `Z` 字符串；Go 侧统一 `time.Now().UTC()`（`cmd/**` 与 `internal/**` 非测试代码里 319 处，占全部 353 处 `time.Now()` 的绝大多数；余下多为时延测量与已在 UTC 值上的算术），SQLite 写 RFC3339(Nano) 文本、PostgreSQL 用 `TIMESTAMPTZ`。

四例非 Timestamp，务必区分：`auth.v1` 的 `int64 expires_at` 是 **Unix 秒**（`api/proto/auth/v1/auth.proto:45`）；`operator.v1.EnrollResponse.certificate_expires_at` 是 RFC3339 字符串；`SubmitBundle` 的 `occurred_at` 是必须为 RFC3339 的字符串，否则 `invalid_argument`（`internal/orchestrator/bundle_service.go:134-137`）；`observe_timeout_display` 是 `time.Duration.String()` 的展示值。全仓库唯一的 `google.protobuf.Duration` 字段是 `UpgradeCommand.timeout`（`api/proto/operator/v1/upgrade_result.proto:78`）。其余时长一律 `_seconds` 后缀标量（`ttl_seconds`、`heartbeat_interval_seconds`、`duration_ms`）。契约里**不存在毫秒 epoch 字段**。

### 5.3 枚举

29 个 enum（`api/proto/*/*/*.proto` 逐个 `^enum ` 声明计数），全部有 `*_UNSPECIFIED = 0`；值名一律 `ENUM_NAME_VALUE` 前缀式 SCREAMING_SNAKE，仅 4 个值例外（见下文命名豁免）。JSON 线上枚举以 **NAME 字符串**序列化（`api/kulala/audit.http:53` 的 `"kind": "ACTOR_KIND_USER"` 即为证据）。proto3 enum 是开放的，越界值会直达 handler，因此 handler 必须做显式 `switch + default` 并映射为 `invalid_argument`（例如 `internal/orchestrator/operator.go:457-483`、`internal/orchestrator/inventory_query.go:196-209`）。UNSPECIFIED 是「按字段」而非全局拒绝：`ConvergenceStrategy`/`ArtifactType`/`ImageValueKind`/`ReleaseMode` 拒绝它，`ValuesStatus`/`ReleaseInventoryStatus` 把它当「不过滤」，`OperatorSessionStatus` 把它重载为「无会话」。已知两处未收敛：`internal/orchestrator/route.go:152-187` 把未知值映射为空 store 值，`internal/orchestrator/emergency.go:759-764` 把任何非 `REVERT` 值（含未知）当成 `REQUIRE_PROMOTION`。反向（store → proto）映射未知一律回落 `*_UNSPECIFIED`。

命名豁免有 2 处：`ConvergenceStrategy` 的 `REVERT_ON_NEXT_RECONCILE`/`REQUIRE_PROMOTION` 带 `buf:lint:ignore ENUM_VALUE_PREFIX` 与理由注释（`api/proto/orchestrator/v1/orchestrator.proto:1616-1623`）；`ReleaseMode` 的 `NOT_APPLIED_PROVEN`、`AUDITED_OVERRIDE`（`api/proto/orchestrator/v1/orchestrator.proto:1731`、`:1736`）**没有** ignore 注释，`buf lint` 目前会因此报错。本仓库 CI 不跑 `buf lint`，属潜在破损；写文档时不要把当前契约描述成 lint-clean。

### 5.4 ID

格式是 **UUID v4 文本**（`github.com/google/uuid`），无 `def_`/`op_` 之类前缀，无 ULID/snowflake。auth 包有小助手 `internal/auth/id.go:5`（`newID()` = `uuid.New().String()`），其余就地 `uuid.NewString()`。ID 一律是 `string` 字段，契约里没有任何 `google.protobuf` 包装或自定义 ID 类型。复合字符串键有明文规则：`command_id = {operation_id}:{stage}`（`internal/orchestrator/preflight/coordinator.go:287`、`:90`），另有固定后缀形式 `{operation_id}:execute`（`internal/orchestrator/preflight/coordinator.go:228`）与 `{operation_id}:artifact`（`internal/orchestrator/service.go:384`）。

ID 归属不统一：多数由服务端生成（`CreateReleaseDefinitionRequest` 没有 `id` 字段；`EnrollRequest` 明确 `reserved 4; reserved "operator_id";`，`api/proto/operator/v1/operator.proto:33-34`，服务端生成后经 `EnrollResponse.operator_id` 返回），但 `CreateCustomerRequest.id`、`CreateClusterRequest.id`、`ConfigureClusterRouteRequest.id` 允许调用方自带且**没有格式校验**（只判空，例如 `internal/orchestrator/customer.go:34-37`）。新增契约时优先服务端生成；必须允许客户端提供 ID 时，同时补校验。

### 5.5 幂等

写 RPC 的幂等键是 **HTTP 头 `Idempotency-Key`**（`docs/architecture.md:92`），适用范围：`CreateOperation`（`internal/orchestrator/service.go:118`）、`RollbackRelease`（`internal/orchestrator/rollback.go:30`）、`CancelOperation`（`internal/orchestrator/service.go:825`）、`CreateValuesRevision`（`internal/orchestrator/values_revision.go:83`）、`SubmitValuesRevision`/`Approve`/`Reject`（`internal/orchestrator/values_approval.go:47`、`:62`、`:77`）、`SubmitBundle`（`internal/orchestrator/bundle_service.go:73`）。长度限制 1-64 字符（`internal/orchestrator/values_revision.go:402-408`、`internal/auth/local_users.go:41-43`）。

两个例外把键放在**请求体字段**：`ExecuteEmergencyChangeRequest.idempotency_key`（`internal/orchestrator/emergency.go:447-448`）与 `RunCleanupRequest.idempotency_key`（`api/proto/orchestrator/v1/cleanup.proto:50-51`，`internal/orchestrator/cleanup.go:185`）。同键同请求 → 静默重放首次结果；同键不同请求 → `already_exists` + `idempotency_conflict`。`CreateLocalUser` 只校验长度、未见幂等去重（`internal/auth/local_users.go:41-43`）。`RunCleanup` 的幂等在 SQLite 下不可用（`internal/store/sqlite/cleanup_idempotency_unsupported.go` 整文件返回 `ErrCleanupIdempotencyUnavailable`）。

### 5.6 并发与 patch 语义

并发控制是**请求内的期望版本字段**，不是 ETag/If-Match：`expected_version`、`expected_state_version`、`version`、`expected_current_revision`。错误 code 目前不统一：`UpdateReleaseDefinition` 用 `failed_precondition`（`internal/orchestrator/definition.go:181-184`），`UpdateCluster` 与 `UpdateOrganization` 用 `aborted`（`internal/orchestrator/cluster.go:86-94`、`internal/auth/org_service.go:128`），`UpdateMemberRole`/`AddMember`/`RevokeBinding` 用 `aborted`。`expected_version` 语义也有差异：definition 允许 0 跳过检查，customer 要求精确匹配。

更新 RPC 有四类不同 patch 语义，别按一个模板套：`UpdateReleaseDefinition` 是可选字段级 patch（`optional` 标量的 absent = 不变、显式 `false`/`0` = 写入，`internal/orchestrator/definition.go:186-206`）；`UpdateCustomer` 是「空字符串 = 不变、无法清空」；`UpdateCluster` 是全量替换（`routes` 整体覆盖，`internal/orchestrator/cluster.go:64`起）；`UpdateOrganization` 无条件赋值（`""` 会清空 name，`internal/auth/org_service.go:124`）。契约里没有任何 `google.protobuf.*Value` 包装类型，只有 4 个 proto3 `optional` 字段：`hpa_managed`、`max_emergency_replicas`、`lifecycle_status`、`session_status`（`api/proto/orchestrator/v1/orchestrator.proto:439-440`、`:635-636`）。唯一真正的「置 null 即清空」是 `google.protobuf.Struct values_patch`，按 RFC 7386 merge 执行（`internal/orchestrator/preflight/command.go:146-178`）。

字段归属要警惕「请求字段 ≠ 认证身份」两件事：`CreateReleaseDefinition` 的 owner/creator 取自请求的 `actor` 字段而不是 JWT（`internal/orchestrator/definition.go:73`、`:85`），省略后后续更新会以 `failed_precondition: release_definition_owner_unresolved` 失败（`internal/orchestrator/values_approval.go:245`）；`CreateOperationRequest.actor` 是 deprecated 的兼容入口，携带即拒（`internal/orchestrator/service.go:124-126`）。

JSON 命名：proto 字段 snake_case，**JSON 输出是 lowerCamelCase**（descriptor 里的 `json=` tag 权威，例如 `api/gen/orchestrator/v1/orchestrator.pb.go:4938` `name=page_size,json=pageSize`；TS 侧 `web/src/gen/orchestrator/v1/orchestrator_pb.ts:1961` 的 `pageSize`）。输入侧 protojson 同时接受 camelCase 与 snake_case，未知键静默丢弃；响应里零值字段被省略、int64 序列化为 JSON 字符串（所以 `version` 在 TS 里是 `bigint`）。`.pb.go` 结构体尾部的 `json:"page_size,omitempty"` tag 是历史遗留，protojson 不使用它。`Notes/PROJECT-CONVENTIONS.md:27` 那句「JSON/proto 字段 snake_case」对**输出命名**是错的，`docs/architecture.md:124` 的 snake_case 讲的是数据库命名。

## 6. 契约变更流程

1. **只改 `api/proto/**`，然后 `make proto`。** `make proto` 是唯一生成入口，实际执行 `buf generate --template api/proto/buf.gen.yaml`（`Makefile:274-279`）；`api/gen/**` 与 `web/src/gen/**` 是生成物，禁止手改（`AGENTS.md:24`、`CONTRIBUTING.md:42`）。`Makefile` 里没有 `proto-check`、`buf lint`、`buf breaking` 之类的目标，CI 也只 setup buf 后跑 `make proto`（`.github/workflows/test.yml:130-138`、`:214-222`），不做兼容与 lint 门禁：改契约时若要跑 `buf lint`/`buf breaking` 必须手工执行，不能声称「CI 会挡」。注意 `buf lint` 当前因 `ReleaseMode` 两处命名失败（见 5.3）。
2. **兼容演进规则**（`docs/decisions/ADR-002-connect-protobuf-single-port-contract.md:28-30`、`docs/architecture.md:106-107`）：新增字段/RPC 向后兼容；未知字段忽略、缺失字段 fail closed；删除必须做到 proto/生成代码/handler/测试/调用方**零匹配**后干净移除，禁止留兼容壳。
3. **字段号纪律**（观察到的既有做法，无 ADR 成文）：移除的编号与名字一律 `reserved`，绝不复用（`api/proto/operator/v1/operator.proto:33-34`；`api/proto/orchestrator/v1/orchestrator.proto` 多处）；新增字段用高位号段分组（例如 `artifacts = 20`）。有意保留的 `[deprecated = true]` 字段不 reserved，作为拒绝探测点——`RollbackRelease` 会主动拒绝带 deprecated 字段的请求（`internal/orchestrator/rollback.go:38-42`）。
4. **响应类型命名**：本仓库不要求 `<Method>Response` 唯一（`buf.yaml:6` 豁免了 `RPC_REQUEST_RESPONSE_UNIQUE` 与两个 STANDARD_NAME 规则），共享响应消息与直接返回 `common.v1.*` 实体是可接受的既有做法。
5. **改完必须同步的下游**：新增/改名 RPC → 必须在 `internal/auth/procedure_policy.go` 的显式登记表里加一行（`mode` + `object`/`action` 或非 Casbin 归属），否则 `TestProcedurePolicyRegistryIsExhaustive`（`internal/auth/procedure_policy_test.go:80-97`）失败，运行时也会以 `permission_denied(invalid_actor_context)` 拒绝；新增 store 哨兵 → 同步 `errorsanitize.go` 白名单；新增带写语义 RPC 且需要维护期可用 → 加入对应 `*ReadOnlyProcedures()`；web 侧 TS 由 `make proto` 全量生成（当前 `web/src/gen` 下 14 个 `_pb.ts`）；仓库里另有一份限定 6 个 path 的 web-only 子集模板 `api/proto/buf.gen.web.yaml`，但没有任何 Makefile 目标或脚本引用它（`grep -rn buf.gen.web Makefile scripts/ .github/` 零命中），不要以为改它就够了。
6. **验证**：`make sdk-check`、`make test`、`make lint`、`make check-licenses`、`make check-docs`。`make quality` 是聚合目标，依赖为 `sdk-check test-coverage lint check-reqs check-licenses check-docs`（`Makefile:522`）——注意它跑的是 `test-coverage`（带 coverprofile 的全量 `go test -race ./...`）而不是 `test`，且额外含 `check-reqs`。本文与 `docs/**` 里每个 `make <target>`、每个代码路径、每个 `path:line` 引用都受 `make check-docs` 校验，改代码后要重跑。
7. **消费方门禁**：实现任何新客户端之前，先过生成符号的编译 fixture（`tsc --noEmit`）+ `buf lint`/`buf breaking` + 旧符号零匹配（`docs/architecture.md:115`）。

## 7. 全量 RPC 清单

列含义：**鉴权** 一栏中，`authz(object/action)` 表示经 `NewAuthInterceptor` 的 Casbin 裁决；`JWT` 表示只验签不授权；`公开` 表示 `publicMethods`；`证书` 表示网关客户端证书；`service token` 表示 `Bearer` 静态令牌；`无` 表示该挂载面没有鉴权拦截器。`位置` 是 handler 首行（`文件:行号`）。

### 7.1 audit.v1.AuditService（3 个，`release-api` 8087）

| RPC | 作用 | 鉴权 | 位置 | 关键错误 |
| --- | --- | --- | --- | --- |
| `Emit` | 异步缓冲写入审计事件（**不是**同步落库，见 7.1 注） | JWT + release-auth 裁决 `audit/write`（actor 组织必须等于判定返回的有效组织） | `internal/audit/audit_service_handler.go:41` | `invalid_argument`（`:43`，空事件列表）、`unauthenticated`（`:47` 侧）、`unavailable`（`:47` 判定不可用）、`permission_denied`（`:56`） |
| `QueryAuditEvents` | 按 filter + 游标分页查询审计事件 | JWT + release-auth 裁决 `audit/read`（组织与窗口由判定返回） | `internal/audit/audit_service_handler.go:77` | `internal`（`:119`）、`unavailable`（判定不可用）、`permission_denied`（判定拒绝，`X-Reason-Code` 透传）、`invalid_argument` + `range_too_large`（`:107`） |
| `ExportAuditEvents` | 登记导出任务 | JWT + release-auth 裁决 `audit/write`（导出记录按判定组织落库） | `internal/audit/audit_service_handler.go:144` | `internal`（`:199`）、`unavailable`、`permission_denied`、`invalid_argument` + `range_too_large`（`:175`）；只插 `audit_exports` 行（`:177-186`），**未见消费者**，导出不会真正完成 → 未实现（见 3.8） |

`Emit` 的三点语义必须写清（都经代码核实）：

1. **异步**：handler 只把事件推进内存缓冲并立刻返回计数（`internal/audit/audit_service_handler.go:41-51` → `internal/audit/emitter.go:69-90`），`accepted` 表示「已入队」，不表示「已持久化」。落库发生在后台 worker 的批量 flush（`internal/audit/emitter.go:128-157`），失败时整批回灌重试（`:134-137`），进程退出时残余批次写入 spool 文件（`:145`、`spool` `:168-197`）。
2. **无幂等去重**：`id` 若由调用方给出则原样使用（`internal/audit/service.go:57`，仅空值才补 UUID，见 `internal/audit/normalize.go:30-33`），而 INSERT 语句没有 `ON CONFLICT`/`INSERT OR IGNORE`（`internal/store/sqlite/audit.go:93-120`、`internal/store/postgres/audit.go:94-98`）。因此重复 `id` 不是幂等重放，而是让整批事务失败并无限重试——不要把 `Emit` 当幂等接口用。
3. **按事件 id 幂等**（TASK-097）：`audit_events.id` 是去重键，两引擎分别是 `INSERT OR IGNORE`（SQLite）与 `ON CONFLICT (id) DO NOTHING`（PostgreSQL），重放同一事件不失败也不写第二行（首发内容保留）。
4. **永不返回业务错误码**：只要请求非空，恒返回 200 与 accepted/rejected 计数（`internal/audit/audit_service_handler.go:52`）；单条事件被拒只体现在 `rejection_codes` 里（`:49-50`）。

`QueryAuditEvents` 的响应只回传 `id`/`action`/`status`/`duration_ms`——`toProtoAuditEvent` 丢弃 actor、resource、organization、timestamp 与 metadata（`internal/audit/audit_service_handler.go:164-174`），所以调用方拿不到「谁在什么时间对哪个资源做了什么」，只能拿到动作名与结果。`ExportAuditEvents` 只登记一行 `audit_exports`（`:134-161`，status 恒为 `pending`），仓库内没有任何 worker 消费该表（检索 `AuditExports()` 的非测试命中只有接口与实现自身），导出永远不会真正完成。

### 7.2 auth.v1.AuthService（11 个，`release-auth` 8085）

| RPC | 作用 | 鉴权 | 位置 | 关键错误 |
| --- | --- | --- | --- | --- |
| `GetInitStatus` | 查询是否已初始化 | 公开 | `internal/auth/service.go:361` | `internal`（`:367`） |
| `Initialize` | 首次初始化（建组织 + platform_admin） | 公开 | `internal/auth/service.go:376` | `already_exists`（`:387`）、`internal`（`:384`） |
| `Login` | 用户名口令换取双 token | 公开 | `internal/auth/service.go:54` | `resource_exhausted`（`:66` 限流）、`unauthenticated`（`:72`）、`internal`（`:84`） |
| `Logout` | 撤销当前会话 | JWT，handler 自证（`modeHandler`；`internal/auth/interceptor.go:98` 只在 `modeCasbin` 时 Enforce） | `internal/auth/service.go:116` | `unauthenticated`（`:151`） |
| `RefreshToken` | 轮换 refresh token | 公开 | `internal/auth/service.go:161` | `internal`（`:184`） |
| `ValidateToken` | 供内部服务校验 access token | 公开 | `internal/auth/service.go:256` | `unauthenticated`（`:265`） |
| `SwitchOrganization` | 切换当前组织上下文 | authz(organization/write)，按**目标**组织裁决（登记为 `targetOrg`） | `internal/auth/service.go:496` | `permission_denied`（非目标组织成员，或对目标组织无写权限） |
| `ChangePassword` | 修改本人口令 | JWT，handler 自证（`modeHandler`：校验旧口令 + 吊销本人会话） | `internal/auth/service.go:276` | `unauthenticated`（`:284`）、`internal`（`:289`） |
| `CreateLocalUser` | 创建本地用户 | authz(auth/write，`adminOnly`) | `internal/auth/local_users.go:23` | `invalid_argument`（`:35`）、`permission_denied`（`:59`）、`internal`（`:69`）；`Idempotency-Key` 仅校验长度 |
| `GetLocalUser` | 查询单个本地用户 | authz(auth/read，`adminOnly`) | `internal/auth/local_users.go:206` | `not_found`（`:213`）、`internal`（`:215`） |
| `ListLocalUsers` | 分页列出租户用户 | authz(auth/read，`adminOnly`) | `internal/auth/local_users.go:225` | `invalid_argument`（`:235` 游标）、`internal`（`:237`） |

### 7.3 auth.v1.OrganizationService（9 个，`release-auth` 8085）

| RPC | 作用 | 鉴权 | 位置 | 关键错误 |
| --- | --- | --- | --- | --- |
| `CreateOrganization` | 创建组织 | authz(organization/write) | `internal/auth/org_service.go:33` | `unauthenticated`（`:53`）、`internal`（`:47`） |
| `GetOrganization` | 查询组织 | authz(organization/read) | `internal/auth/org_service.go:77` | `not_found`（`:83`） |
| `ListOrganizations` | 列出当前用户可见组织 | authz(organization/read) | `internal/auth/org_service.go:93` | `internal`（`:99`）；无分页 |
| `UpdateOrganization` | 改名（无条件赋值） | authz(organization/write) | `internal/auth/org_service.go:111` | `aborted`（`:128` 乐观锁）、`failed_precondition`（`:121`）、`not_found`（`:118`）、`internal`（`:130`） |
| `DisableOrganization` | 停用组织 | authz(organization/write) | `internal/auth/org_service.go:140` | `aborted`（`:154`）、`not_found`（`:147`）、`internal`（`:156`） |
| `AddMember` | 加入成员并赋角色 | authz(member/write) | `internal/auth/org_service.go:164` | `invalid_argument`（`:176`）、`not_found`（`:182`）、`failed_precondition`（`:185`）、`unauthenticated`（`:171`） |
| `RemoveMember` | 移除成员 | authz(member/write) | `internal/auth/org_service.go:220` | `not_found`（`:233`）、`failed_precondition`（`:248`）、`internal`（`:239`）、`unauthenticated`（`:227`） |
| `ListMembers` | 列出成员 | authz(member/read) | `internal/auth/org_service.go:265` | `internal`（`:271`）；无分页 |
| `UpdateMemberRole` | 变更成员角色 | authz(member/write) | `internal/auth/org_service.go:281` | `invalid_argument`（`:293`）、`permission_denied`（`:299`）、`not_found`（`:311`）、`unauthenticated`（`:288`） |

### 7.4 auth.v1.BindingService（4 个，`release-auth` 8085）

| RPC | 作用 | 鉴权 | 位置 | 关键错误 |
| --- | --- | --- | --- | --- |
| `CreateBinding` | 绑定组织与 Customer | authz(binding/write) | `internal/auth/binding_service.go:30` | `invalid_argument`（`:36`）、`already_exists`（`:60`）、`internal`（`:50`）；**恒 `not_found`**（`StubResolver`，见 3.8） |
| `GetBinding` | 查询绑定 | authz(binding/read) | `internal/auth/binding_service.go:70` | 由 `getBinding`/`authorize` 决定 |
| `ListBindings` | 列出绑定 | authz(binding/read) | `internal/auth/binding_service.go:87` | `invalid_argument`（`:93`）、`internal`（`:101`）；无分页 |
| `RevokeBinding` | 吊销绑定 | authz(binding/write) | `internal/auth/binding_service.go:113` | `aborted`（`:126`）、`failed_precondition`（`:129`）、`internal`（`:139`） |

### 7.5 auth.v1.AuthorizationService（3 个，`release-auth` 8085）

| RPC | 作用 | 鉴权 | 位置 | 关键错误 |
| --- | --- | --- | --- | --- |
| `GetAuthorizationSnapshot` | 向业务服务发布版本化授权快照 | JWT，跳过 Enforce，handler 自校验 | `internal/auth/authorization_snapshot.go:40` | `unauthenticated`（`:55`）、`invalid_argument`（`:60`）、`permission_denied`（`:63`）、`unavailable`（`:68`） |
| `SetCapabilityGrant` | 增删显式能力授权 | JWT，handler 自证（`modeHandler`：要求 platform_admin/release_admin） | `internal/auth/authorization_snapshot.go:128` | `unauthenticated`（`:134`）、`invalid_argument`（`:138`、`:145`）、`permission_denied`（`:141`、`:149`、`:152`） |
| `AuthorizeAccess` | 授权判定：按调用方持久 membership 与版本化 policy 回答 `(object, action)` 是否允许，并返回有效组织、跨组织许可与窗口上限（ADR-021） | JWT，`modeHandler`：handler 自己就是裁决点，拦截器只认证 + 校验会话 | `internal/auth/authorization_decision.go:43` | `unauthenticated`（`:49`）、`invalid_argument`（`:54`）、`permission_denied`（membership 缺失）、`unavailable`（`:57` 策略不可用）；普通拒绝返回 200 + `allowed=false` + reason |

### 7.6 auth.v1.ExternalIdentityService（3 个，**未见挂载**）

| RPC | 作用 | 鉴权 | 位置 | 关键错误 |
| --- | --- | --- | --- | --- |
| `AuthenticateLDAP` | LDAP 认证换取会话 | 未见挂载 | `internal/auth/external_idp_service.go:55` | 不适用 |
| `GetOIDCAuthURL` | 生成 OIDC 授权跳转 URL | 未见挂载 | `internal/auth/external_idp_service.go:69` | 不适用 |
| `GetDingTalkAuthURL` | 生成钉钉授权跳转 URL | 未见挂载 | `internal/auth/external_idp_service.go:80` | 不适用 |

契约见 `api/proto/auth/v1/auth.proto:668`。同一文件里的 `OIDCCallback`/`DingTalkCallback` 是 `http.HandlerFunc` 形态，同样未在任何 mux 注册（`cmd/auth/main.go:136-205` 全文只有 4 个 `mux.Handle`）。

### 7.7 notifier.v1.NotifierService（2 个，`release-notifier` 8086）

| RPC | 作用 | 鉴权 | 位置 | 关键错误 |
| --- | --- | --- | --- | --- |
| `Send` | 为终态 Operation 创建通知任务（按 `(operation_id, channel, recipient)` 去重）；recipient 必须命中出站白名单，否则任务记 `egress_blocked` 并 dead-letter | 服务令牌（scope = 本服务两 procedure，TASK-096） | `internal/notifier/service.go:29` | `internal`（`:55`）；投递期 `egress_blocked` |
| `GetStatus` | 查询通知任务投递状态 | 服务令牌（同上） | `internal/notifier/service.go:71` | `not_found`（`:78`）、`internal`（`:80`） |

### 7.8 operator.v1.OperatorService（4 个；8083 管理端口 / 8084 网关 / 8084 `release-operator` gateway 模式）

| RPC | 作用 | 鉴权 | 位置 | 关键错误 |
| --- | --- | --- | --- | --- |
| `Enroll` | 用一次性 enrollment token + CSR 注册 agent 并签发客户端证书 | 无（token 即凭证） | `internal/operator/service.go:181` | `unauthenticated`（`:192`）、`permission_denied`（`:217`）、`invalid_argument`（`:243`）、`internal`（`:194`） |
| `RenewCertificate` | 用既有证书续期（ADR-018） | 网关：证书；管理端口：无 | `internal/operator/service.go:1323` | `unauthenticated`（`:1329`）、`failed_precondition`（`:1350`）、`invalid_argument`（`:1354`）、`permission_denied`（`:1363`） |
| `CommandStream` | **bidi-streaming**：Hello/Heartbeat/Ack/Result/Resync/EmergencyStop + 命令下发 | 网关：客户端证书；管理端口：仅 `session_id` 查库存在性 | `internal/operator/service.go:375` | `invalid_argument`（`:387` 首帧必须是 Hello）；证书分支 `unauthenticated`（`:398`、`:403`）、`permission_denied`（`:433`、`:435`、`:438`）、`already_exists`（`:460` 单 operator 只允许一个在线会话）；非证书分支 `unauthenticated`（`:478` 会话不存在）、`permission_denied`（`:481` 会话与 operator 不符）；需 HTTP/2，见 2.6 |
| `GetActiveOperatorSession` | 读取 operator 当前活跃会话 | 无（网关中间件也不覆盖此路径） | `internal/operator/active_session.go:16` | `invalid_argument`（`:22`）、`not_found`（`:27`）、`internal`（`:30`） |

两条分流要写清楚，别把 `CommandStream` 说成完全无防护：

- **有 TLS 状态时**（网关监听器，`internal/operator/service.go:396-440`）：要求客户端证书，SAN 必须编码 operator 身份，并与登记记录的 `cert_serial` 一致；`operator_id` 由证书推导而非请求体。
- **无 TLS 状态时**（管理端口 8083 与 `release-operator` gateway 模式，同一 handler：`cmd/orchestrator/main.go:369-376`、`cmd/operator/main.go:259-266`）：走 `else` 分支，只做「`hello.session_id` 在库里存在且 `session.OperatorID` 匹配」的检查（`internal/operator/service.go:473-482`）——没有 JWT、没有证书。注释里写明该路径由后续任务移除（`internal/operator/service.go:474-475`），当前是既有事实。

网关的 `NewCertificateIdentityHandler` 只强制校验 `CommandStream` 与 `RenewCertificate` 两条路径（`internal/operator/identity_handler.go:8-19`，路径字符串比较在 `:10-11`），其余 procedure 直接 `next.ServeHTTP`。`RenewCertificate` 则在 handler 内部再要求证书身份上下文（`internal/operator/service.go:1327-1330`），所以它在管理端口上恒 `unauthenticated`。

### 7.9 orchestrator.v1.BundleService（4 个，`release-orchestrator` 8083）

| RPC | 作用 | 鉴权 | 位置 | 关键错误 |
| --- | --- | --- | --- | --- |
| `SubmitBundle` | 提交发布 bundle 入库（CI 入口） | service token（scope 仅此一条）或 JWT+authz(bundle/write) | `internal/orchestrator/bundle_service.go:54` | `already_exists`（`:110` 幂等冲突）；`Idempotency-Key` |
| `RecordArtifactEvent` | 记录制品生命周期事件 | authz(bundle/write，`adminOnly`)；service-token 路仍只放行 `SubmitBundle`（Harbor key 未落地，见 3.8） | `internal/orchestrator/bundle_service.go:120` | `invalid_argument`（`:126`）、`already_exists`（`:172`） |
| `ListBundles` | 分页查询 bundle | authz(bundle/read) | `internal/orchestrator/bundle_service.go:183` | `permission_denied`（`:189`）、`invalid_argument`（`:200`）；SQLite 下恒失败 |
| `GetBundle` | 查询单个 bundle（可带 definition 归属校验） | authz(bundle/read) | `internal/orchestrator/bundle_service.go:230` | `invalid_argument`（`:235`）、`permission_denied`（`:240`）、`not_found`（`:251`） |

### 7.10 orchestrator.v1.CleanupService（2 个，`release-orchestrator` 8083）

| RPC | 作用 | 鉴权 | 位置 | 关键错误 |
| --- | --- | --- | --- | --- |
| `RunCleanup` | 触发保留期清理（维护窗口独占） | authz(cleanup/write)，即仅 platform_admin | `internal/orchestrator/cleanup.go:181` | `invalid_argument`（`:183`）、`already_exists`（`:193`）、`resource_exhausted`（`:218` 并发配额）、`internal`（`:196`）；维护期恒 `unavailable` |
| `UnarchiveBundle` | 回滚误归档 | 同上 | `internal/orchestrator/cleanup.go:142` | `failed_precondition`（`:155`）、`not_found`（`:152`）、`internal`（`:161`） |

### 7.11 trust.v1.TrustService（6 个，`release-orchestrator` 8083）

| RPC | 作用 | 鉴权 | 位置 | 关键错误 |
| --- | --- | --- | --- | --- |
| `CreateTrustRoot` | 登记签名信任根 | authz(trust_root/write) | `internal/trust/service.go:40` | `invalid_argument`（`:49`）、`internal`（`:62`） |
| `RotateTrustRoot` | 轮换信任根（双活 + grace） | authz(trust_root/write) | `internal/trust/service.go:84` | `failed_precondition`（`:100`）、`invalid_argument`（`:106`）、`not_found`（`:95`）、`internal`（`:97`） |
| `EndGrace` | 提前结束宽限期 | authz(trust_root/write) | `internal/trust/service.go:157` | `failed_precondition`（`:171`）、`not_found`（`:166`）、`internal`（`:168`） |
| `RetireTrustRoot` | 退役信任根 | authz(trust_root/write) | `internal/trust/service.go:210` | `failed_precondition`（`:224`）、`not_found`（`:219`）、`internal`（`:221`） |
| `RevokeTrustRoot` | 吊销信任根 | authz(trust_root/write) | `internal/trust/service.go:260` | `failed_precondition`（`:275`）、`not_found`（`:270`）、`internal`（`:272`） |
| `GetTrustPolicy` | 读取生效信任策略 | authz(trust_root/read)；维护期唯一放行项 | `internal/trust/service.go:312` | `internal`（`:319`） |

`trust_roots` 表没有 organization 维度（`migrations/000006_schema_parity.up.sql`、`internal/store/sqlite/db.go` 的建表段均无 `organization_id` 列），因此信任根是**每环境全局**资源：任一组织的 `platform_admin` 都能改动全站签名信任。这是既有事实，调用前须知。

### 7.12 webhook.v1.WebhookService（1 个，`release-webhook` 8082）

| RPC | 作用 | 鉴权 | 位置 | 关键错误 |
| --- | --- | --- | --- | --- |
| `SubmitReleaseBundle` | 接收 CI 回调并转发到 `BundleService.SubmitBundle` | CI API key（`ServiceTokenInterceptor` 收窄到本 procedure；出站另用 webhook service token） | `internal/webhook/service.go:35` | `unauthenticated`（缺/错 key）、`permission_denied`（key 无权访问该 procedure）、`unavailable`（未配置下游 client）；其余透传 orchestrator 的 `bundleError` 形态；`Idempotency-Key` 透传 |

### 7.13 orchestrator.v1.OrchestratorService（53 个，`release-orchestrator` 8083）

| RPC | 作用 | 鉴权 | 位置 | 关键错误 |
| --- | --- | --- | --- | --- |
| `CreateOperation` | 创建发布 Operation（幂等 + 门禁） | authz(release/write) | `internal/orchestrator/service.go:112` | `invalid_argument`（`:124` `operation_type` 只接受 `INSTALL`/`UPGRADE`）、`unauthenticated`（`:131`，actor 必须来自认证上下文）、`not_found`（`:137`）、`internal`（`:141`）；`Idempotency-Key`；门禁拒绝带 `CreateOperationGateDetail` |
| `PublishRelease` | 直接发布（骨架） | authz(release/write) | `internal/orchestrator/service.go:461` | `not_found`（`:470`）、`permission_denied`（`:481`）、`internal`（`:474`）；成功时返回 `not_implemented` 且无 Operation → **未实现** |
| `RollbackRelease` | 回滚到指定 revision | authz(release/write) | `internal/orchestrator/rollback.go:24` | `unauthenticated`（`:35`）、`invalid_argument`（`:41`）、`not_found`（`:66`）、`internal`（`:70`）；`Idempotency-Key` |
| `GetOperation` | 查询 Operation 详情 | authz(release/read) | `internal/orchestrator/service.go:507` | `unauthenticated`（`:513`）、`not_found`（`:517`）、`internal`（`:521`） |
| `WatchOperation` | **server-streaming** 订阅状态流转 | authz(release/read)；流式由 `NewAuthStreamInterceptor` 裁决 | `internal/orchestrator/service.go:543` | `unauthenticated`（`:550`）、`invalid_argument`（`:553`）、`not_found`（`:557`）、`internal`（`:560`）；游标过界 `out_of_range`+`cursor_expired`（`:628-638`）；维护模式**不拦截**流式 |
| `CancelOperation` | 取消非终态 Operation | authz(release/write) | `internal/orchestrator/service.go:820` | `unauthenticated`（`:829`）、`not_found`（`:838`）、`failed_precondition`（`:855`）、`internal`（`:842`）；`Idempotency-Key` |
| `SubmitValuesRevision` | 提交 values revision 进入审批 | JWT，handler 自校验（`modeHandler`，登记表 `internal/auth/procedure_policy.go`） | `internal/orchestrator/values_approval.go:41` | 由 `handleValuesApproval` 决定：`unauthenticated`（`:99`）、`not_found`（`:108`）、`unavailable`（`:124`）、`invalid_argument`（`:187`）；`Idempotency-Key` |
| `ApproveValuesRevision` | 批准 | JWT，handler 自校验 | `internal/orchestrator/values_approval.go:56` | 同上 + `failed_precondition`（`:241`、`:245`、`:256`）、`permission_denied`（`:249`） |
| `RejectValuesRevision` | 驳回 | JWT，handler 自校验 | `internal/orchestrator/values_approval.go:71` | 同上；`reason` 必填（`:198`）；`Idempotency-Key` |
| `CreateValuesRevision` | 直接创建 values revision | authz(release/write) | `internal/orchestrator/values_revision.go:67` | `unauthenticated`（`:75`）、`invalid_argument`（`:78`）、`resource_exhausted`（`:89` 体积超限）、`not_found`（`:94`）；`Idempotency-Key` |
| `GetValuesRevision` | 查询单条 values revision | authz(release/read) | `internal/orchestrator/values_revision.go:217` | `invalid_argument`（`:222`）、`not_found`（`:226`）；响应直接返回 `common.v1.ValuesRevision` |
| `ListValuesRevisions` | 分页列出 values revision | authz(release/read) | `internal/orchestrator/values_revision.go:238` | `invalid_argument`（`:244` page_size、`:264` cursor） |
| `DiscardValuesRevision` | 丢弃 values revision | authz(release/write) | `internal/orchestrator/values_revision.go:281` | `unauthenticated`（`:289`）、`invalid_argument`（`:292`）、`not_found`（`:307`）、`failed_precondition`（`:325`）；`Idempotency-Key` |
| `CreatePrepareSession` | 创建预检/准备会话 | authz(release/write) | `internal/orchestrator/prepare_sessions.go:32` | `unauthenticated`（`:41`）、`invalid_argument`（`:45`）、`not_found`（`:62`）、`failed_precondition`（`:70`） |
| `GetPrepareSession` | 查询准备会话 | authz(release/read) | `internal/orchestrator/prepare_sessions.go:161` | `unauthenticated`（`:167`）、`invalid_argument`（`:171`）、`not_found`（`:178`）、`permission_denied`（`:183`） |
| `ListSecrets` | 列出脱敏 secret 引用（不返回值） | authz(release/read) | `internal/orchestrator/values_revision.go:692` | `invalid_argument`（`:695`）、`not_found`（`:699`）、`internal`（`:702`）；无分页 |
| `CreateReleaseDefinition` | 创建发布定义 | authz(release/write) | `internal/orchestrator/definition.go:21` | `invalid_argument`（`:28`）、`not_found`（`:38`）、`permission_denied`（`:43`）、`internal`（`:40`）；owner 取自请求 `actor` |
| `GetReleaseDefinition` | 查询定义 | authz(release/read) | `internal/orchestrator/definition.go:124` | `not_found`（`:131`）、`internal`（`:134`） |
| `ListReleaseDefinitions` | 列出定义 | authz(release/read) | `internal/orchestrator/definition.go:143` | `internal`（`:153`）；无分页 |
| `UpdateReleaseDefinition` | 字段级 patch 更新定义 | authz(release/write) | `internal/orchestrator/definition.go:167` | `not_found`（`:176`）、`failed_precondition`（`:182` 乐观锁）、`already_exists`（`:211`）、`internal`（`:178`） |
| `DisableReleaseDefinition` | 停用定义 | authz(release/write) | `internal/orchestrator/definition.go:229` | `not_found`（`:238`）、`failed_precondition`（`:262`）、`internal`（`:240`） |
| `CreateCustomer` | 创建 Customer | authz(release/write) | `internal/orchestrator/customer.go:24` | `unauthenticated`（`:30`）、`internal`（`:55`）；id 可由调用方提供 |
| `GetCustomer` | 查询 Customer | authz(release/read) | `internal/orchestrator/customer.go:67` | `not_found`（`:74`）、`internal`（`:77`） |
| `ListCustomers` | 列出 Customer | authz(release/read) | `internal/orchestrator/customer.go:90` | `unauthenticated`（`:96`）、`internal`（`:102`）；无分页 |
| `UpdateCustomer` | 更新 Customer | authz(release/write) | `internal/orchestrator/customer.go:133` | `not_found`（`:141`）、`internal`（`:143`）；`expected_version` 强制精确匹配 |
| `DisableCustomer` | 停用 Customer | authz(release/write) | `internal/orchestrator/customer.go:176` | `not_found`（`:183`）、`internal`（`:185`） |
| `ListCustomerEvents` | 列出 Customer 生命周期事件 | authz(release/read) | `internal/orchestrator/customer.go:273` | `not_found`（`:281`）、`internal`（`:284`）；无分页 |
| `CreateCluster` | 创建 Cluster | authz(release/write) | `internal/orchestrator/cluster.go:19` | `not_found`（`:29`）、`permission_denied`（`:34`）、`internal`（`:31`） |
| `UpdateCluster` | 全量替换 Cluster | authz(release/write) | `internal/orchestrator/cluster.go:64` | `invalid_argument`（`:71`）、`not_found`（`:82`）、`aborted`（`:88` 乐观锁）、`internal`（`:84`） |
| `GetCluster` | 查询 Cluster | authz(release/read) | `internal/orchestrator/cluster.go:204` | `not_found`（`:211`）、`internal`（`:214`） |
| `ListClusters` | 列出 Cluster | authz(release/read) | `internal/orchestrator/cluster.go:226` | `internal`（`:241`）；无分页 |
| `DisableCluster` | 停用 Cluster | authz(release/write) | `internal/orchestrator/cluster.go:259` | `not_found`（`:266`）、`internal`（`:268`） |
| `ListOperators` | 分页列出 operator | authz(operator/read) | `internal/orchestrator/operator.go:46` | `invalid_argument`（`:59`）、`internal`（`:71`）；维护期放行 |
| `GetOperator` | 查询 operator | authz(operator/read) | `internal/orchestrator/operator.go:90` | 由 `mapOperatorStoreError` 决定（`:438-454`）；维护期放行 |
| `RevokeOperator` | 吊销 operator（证书序列立即失效） | authz(operator/revoke) | `internal/orchestrator/operator.go:123` | `invalid_argument`（`:133`）、`unavailable`（`:154`） |
| `CreateEnrollmentToken` | 铸造一次性 enrollment token | authz(operator/enroll) | `internal/orchestrator/enrollment.go:24` | `invalid_argument`（`:41`）、`already_exists`（`:44`）、`unauthenticated`（`:55`）、`internal`（`:46`） |
| `GetEnrollmentTokenStatus` | 查询 enrollment token 状态 | authz(operator/enroll) | `internal/orchestrator/operator.go:165` | `internal`（`:180`）；维护期放行 |
| `RevokePendingEnrollmentToken` | 吊销未使用的 token（幂等，返回 `changed`/`final_state`） | authz(operator/enroll) | `internal/orchestrator/operator.go:198` | `invalid_argument`/`not_found`/`internal`/`permission_denied` 全部来自共享的 `validateOperatorScope`（`:226`、`:229`、`:233`、`:236`、`:239`）；存储错误经 `mapOperatorStoreError`（`:215`） |
| `ExecuteEmergencyChange` | 执行紧急变更（唯一权威入口） | authz(release/write) | `internal/orchestrator/emergency.go:77` | `unauthenticated`（`:87`）、`permission_denied`（`:122`）、`failed_precondition`（`:104`）、`internal`（`:100`）；幂等键在**请求体**；`EmergencyErrorDetail` |
| `ListEmergencyTargets` | 列出可执行紧急变更的目标 | authz(release/read) | `internal/orchestrator/emergency_queries.go:50` | `invalid_argument`（`:55`）、`not_found`（`:64`）、`internal`（`:67`）；无分页 |
| `CheckEmergencyConflict` | 探测与在途 Operation 的冲突 | authz(release/read) | `internal/orchestrator/emergency_queries.go:148` | `invalid_argument`（`:154`）、`internal`（`:161`）；handler 另要求 active binding |
| `ListCandidateArtifacts` | 列出候选制品 | authz(release/read) | `internal/orchestrator/emergency_queries.go:178` | `invalid_argument`（`:183`）、`internal`（`:195`）；无分页 |
| `ListConvergenceTasks` | 列出收敛任务 | authz(release/read) | `internal/orchestrator/emergency_queries.go:211` | `invalid_argument`（`:217`）、`internal`（`:227`）；无分页 |
| `ListStuckLocks` | 列出卡住的锁 | authz(release/read) | `internal/orchestrator/emergency_stuck.go:77` | `unauthenticated`（`:83`）、`internal`（`:87`）；无分页 |
| `ReleaseEmergencyLock` | 带审计理由地释放卡住的锁 | authz(release/write) | `internal/orchestrator/emergency_stuck.go:127` | `unauthenticated`（`:133`）、`invalid_argument`（`:137`）、`not_found`（`:152`）、`internal`（`:155`） |
| `ConfigureClusterRoute` | 配置集群路由（id 非空即更新） | authz(release/write) | `internal/orchestrator/route.go:16` | `not_found`（`:26`）、`permission_denied`（`:32`）、`invalid_argument`（`:42`）、`internal`（`:29`）；`RouteValidationDetail` |
| `GetClusterRoutes` | 读取集群路由 | authz(release/read) | `internal/orchestrator/route.go:113` | `internal`（`:119`）；无分页 |
| `DeleteClusterRoute` | 删除路由 | authz(release/write) | `internal/orchestrator/route.go:134` | `not_found`（`:140`）、`internal`（`:143`） |
| `ListReleases` | 分页查询发布清单视图 | authz(release/read) | `internal/orchestrator/inventory_query.go:31` | `invalid_argument`（`:37`）、`not_found`（`:54`）、`internal`（`:57`）；维护期**不放行**（白名单无此项） |
| `ListReleaseInventory` | 返回全部清单快照 | authz(release/read) | `internal/orchestrator/inventory_observation.go:19` | `unauthenticated`（`:25`）；无请求字段、无分页；维护期放行 |
| `ListOperations` | 分页查询 Operation（最新在前，keyset 游标） | authz(release/read) | `internal/orchestrator/operations_query.go:22` | `invalid_argument`（`:32`、`:39`、`:56`、`:135`）、`not_found`（`:44`）、`permission_denied`（binding/membership） |
| `TriggerInventorySync` | 主动触发清单同步 | authz(release/write) | `internal/orchestrator/inventory_query.go:112` | `invalid_argument`（`:116` 起）、`unavailable`（operator offline）、`already_exists`（sync in progress） |
| `SyncInventory` | agent 上报集群清单（快照 + 对账） | 网关 8084：证书；管理端口 8083：authz(release/write) | `internal/orchestrator/inventory.go:21` | `unauthenticated`/`permission_denied`（`internal/orchestrator/sync_inventory_auth.go:30-60`）、`invalid_argument`（`sync_id` 必填，`:40-42`） |

## 8. 事实源

统计口径：`service`/`rpc` 声明逐文件计数（13 个 service、104 个 RPC、8 个包）；挂载事实取 `cmd/*/main.go` 中 `connect.New*ServiceHandler` 与 `mux.Handle` 的全部出现处；错误 code 计数为非测试 Go 源码中 `connect.Code*` 的实测出现次数。

> 事实源：`.github/workflows/test.yml`、`AGENTS.md`、`CONTRIBUTING.md`、`Makefile`、`README.md`、`api/gen/auth/v1/authv1connect/auth.connect.go`、`api/gen/orchestrator/v1/orchestrator.pb.go`、`api/kulala/audit.http`、`api/kulala/auth.http`、`api/kulala/notifier.http`、`api/kulala/operator.http`、`api/kulala/orchestrator.http`、`api/kulala/webhook.http`、`api/proto/audit/v1/audit.proto`、`api/proto/auth/v1/auth.proto`、`api/proto/buf.gen.web.yaml`、`api/proto/buf.gen.yaml`、`api/proto/common/v1/domain.proto`、`api/proto/common/v1/health.proto`、`api/proto/common/v1/trust.proto`、`api/proto/common/v1/types.proto`、`api/proto/notifier/v1/notifier.proto`、`api/proto/operator/v1/operator.proto`、`api/proto/operator/v1/upgrade_result.proto`、`api/proto/orchestrator/v1/cleanup.proto`、`api/proto/orchestrator/v1/orchestrator.proto`、`api/proto/orchestrator/v1/vulnerability.proto`、`api/proto/trust/v1/trust.proto`、`api/proto/webhook/v1/webhook.proto`、`buf.yaml`、`cmd/api/main.go`、`cmd/auth/main.go`、`cmd/notifier/main.go`、`cmd/operator/main.go`、`cmd/orchestrator/main.go`、`cmd/webhook/main.go`、`configs/api.dev.yaml`、`configs/auth.dev.yaml`、`configs/notifier.dev.yaml`、`configs/operator.dev.yaml`、`configs/orchestrator.dev.yaml`、`configs/webhook.dev.yaml`、`deploy/dev/dev.sh`、`deploy/kustomize/dev/configs/orchestrator.dev.yaml`、`deploy/kustomize/services/web.yaml`、`docs/architecture.md`、`docs/decisions/ADR-002-connect-protobuf-single-port-contract.md`、`docs/decisions/ADR-006-server-authoritative-organization-authorization.md`、`docs/decisions/ADR-018-sha256-certder-10-hex-renew.md`、`http-client.env.json`、`internal/app/app.go`、`internal/app/maintenance.go`、`internal/audit/audit_service_handler.go`、`internal/audit/emitter.go`、`internal/audit/interceptor.go`、`internal/audit/normalize.go`、`internal/audit/service.go`、`internal/auth/authorization_snapshot.go`、`internal/auth/binding_service.go`、`internal/auth/browser_session.go`、`internal/auth/casbin.go`、`internal/auth/customer_resolver.go`、`internal/auth/errors.go`、`internal/auth/external_idp_service.go`、`internal/auth/id.go`、`internal/auth/interceptor.go`、`internal/auth/jwt.go`、`internal/auth/local_users.go`、`internal/auth/local_users_test.go`、`internal/auth/multi_auth.go`、`internal/auth/org_service.go`、`internal/auth/service.go`、`internal/auth/service_token.go`、`internal/config/config.go`、`internal/contracts/errors.go`、`internal/contracts/interceptor/errorsanitize.go`、`internal/contracts/interceptor/requestid.go`、`internal/contracts/pagination.go`、`internal/devfixture/bundle.go`、`internal/handler/health.go`、`internal/handler/ready.go`、`internal/jwtauth/jwt.go`、`internal/notifier/service.go`、`internal/operator/active_session.go`、`internal/operator/errors.go`、`internal/operator/identity_handler.go`、`internal/operator/service.go`、`internal/orchestrator/bundle_service.go`、`internal/orchestrator/cleanup.go`、`internal/orchestrator/cluster.go`、`internal/orchestrator/customer.go`、`internal/orchestrator/definition.go`、`internal/orchestrator/emergency.go`、`internal/orchestrator/emergency_queries.go`、`internal/orchestrator/emergency_stuck.go`、`internal/orchestrator/enrollment.go`、`internal/orchestrator/inventory.go`、`internal/orchestrator/inventory_observation.go`、`internal/orchestrator/inventory_query.go`、`internal/orchestrator/operator.go`、`internal/orchestrator/preflight/command.go`、`internal/orchestrator/preflight/coordinator.go`、`internal/orchestrator/prepare_sessions.go`、`internal/orchestrator/rollback.go`、`internal/orchestrator/route.go`、`internal/orchestrator/service.go`、`internal/orchestrator/sync_inventory_auth.go`、`internal/orchestrator/values_approval.go`、`internal/orchestrator/values_revision.go`、`internal/store/postgres/audit.go`、`internal/store/postgres/bundles.go`、`internal/store/postgres/users.go`、`internal/store/sqlite/audit.go`、`internal/store/sqlite/bundles.go`、`internal/store/sqlite/cleanup_idempotency_unsupported.go`、`internal/store/sqlite/db.go`、`internal/store/sqlite/users.go`、`internal/store/store.go`、`internal/trust/service.go`、`internal/webhook/service.go`、`migrations/000006_schema_parity.up.sql`、`test/e2e/prerequisite/smoke.sh`、`web/src/connect/client.ts`、`web/src/connect/emergency-api.ts`、`web/src/gen/orchestrator/v1/orchestrator_pb.ts`、`web/vite.config.ts`

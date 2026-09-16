# Kulala HTTP 调试集合（api/kulala）

`README.md:58` 把 `api/kulala/` 定位为「Kulala/Neovim HTTP 调试集合」。本文先给出**当前可用性判定**
（§1），再写机制与复用（§2–§5）、常见坑（§6）、待决策项（§7）。所有结论附 `文件:行号` 或
`git log` 证据；核实不到的写「未找到」，无法实测的写「不确定」。撰写本档时未运行任何服务与
Kulala 本体（会话纪律），端口/路径结论均来自代码与配置静态核对。

## 1. 当前可用性：整套集合停留在 Connect 迁移之前

**总判定：按现状直接执行集合内的请求，除 1 条 health 探测外全部失败。** 依据：

- 集合的 REST 面（`{{BASE_URL}}/api/v1/...`）指向已删除的 Manager 单二进制：`Makefile:41-42`
  注明 manager 单端口「随 cmd/release-manager 移除」<!-- check-docs:ignore 已删除的历史命令名，不是当前路径断言 -->；
  `/api/v1` 在 `internal/` 与 `cmd/` 的非测试代码中零命中（grep `"/api/v1` 核实）。
- 单端口 Connect 契约在集合停笔之后才确立：`api/kulala/auth.http`、`webhook.http` 的最后提交是
  `131b9c8`（2026-07-15，脚手架）；`operator.http` 是 `d95b865`（2026-07-16）；`audit.http` 是
  `389a757`（2026-07-17）。ADR-002 的创建日期为 2026-07-27
  （`docs/decisions/ADR-002-connect-protobuf-single-port-contract.md:4`），
  `docs/architecture.md:87` 明确「单一端口同时提供 Connect、gRPC 与 gRPC-Web，不引入 REST gateway」。
  `manager.http`/`orchestrator.http` 虽在 2026-08-14 还有过改动（`0ebe9d2`、`68d4712`），新增样本
  仍是 `GRPC` + 8446 形态（如 `manager.http:279,292,304`），未回写 REST 面与 env 端口。
- `http-client.env.json` 的两个 profile 全是死端口：`dev` 的 `BASE_URL=http://localhost:8081`
  （`:5`）与 `ORCH_GRPC_PORT=8446`、`OP_GRPC_PORT=8447`、`GRPC_PORT=8447`（`:17-22`）；`kind` 的
  8080/8443（`:26-36`）。这些端口在 `configs/` 与 `deploy/` 中**一个都不存在**（grep 核实）；
  现 dev 管理面为 8082–8087（各 `configs/*.yaml:1`；k3d 宿主映射 `docs/dev-environment.md:177`），
  另有 dev-only sink 8088（`deploy/kustomize/dev/configs/notification-sink.dev.yaml:1`）。
  集合内 `{{BASE_URL}}` 共 44 处引用（auth 11、manager 29、webhook 4），默认 profile 下全部打向死端口。

逐文件判定（请求规模：6 个文件、74 个 `# @name` 命名请求、77 个 `###` 块；计数以
`grep -c '# @name'` / `grep -c '^###'` 为准）：

| 文件 | 判定 | 依据 |
| --- | --- | --- |
| `auth.http` | **路径已失效 + 端口已失效**（11/11 条） | 全部为 `/api/v1/...` REST（`auth.http:12-98`），Connect 风格路径 0 条（grep `/auth.v1.` 无命中）；`{{BASE_URL}}`=8081 无监听 |
| `webhook.http` | **路径已失效 + 端口已失效**（4/4 条） | 3 条 Harbor REST（`webhook.http:13,40,67`）+ 1 条 `/api/v1/artifact-events/harbor`（`:82`）；现行 seam 是 `POST :8082/webhook.v1.WebhookService/SubmitReleaseBundle`（`api/proto/webhook/v1/webhook.proto:46-57`） |
| `manager.http` | **半迁移**（38 条：29 REST 失效 + 9 GRPC 端口/协议失效） | 客户/集群/操作等 REST 打 8081（如 `:28,68,318`）；ValuesRevision/PrepareSession 块（`:208-304`）用 `GRPC ... :8446`，方法名对应 RPC 仍在（`api/proto/orchestrator/v1/orchestrator.proto:1064-1137`），但 8446 无监听 |
| `orchestrator.http` | **协议已失效 + 端口已失效**（17 条 GRPC 打 8446；含 2 条 health 探测不成立） | 同上；`grpc.health.v1.Health/Check`（`:16-21`）全仓未找到注册（grep `grpc.health|HealthService` 零命中） |
| `operator.http` | **协议已失效 + 端口已失效 + 契约不符**（3 条） | 打 8447（`:12,26,34`）；`OperatorService/Enroll` 的现行挂载点在 orchestrator agent gateway（`cmd/orchestrator/main.go:140-209`），dev kustomize 为 **mTLS** NodePort 30084（`deploy/kustomize/dev/configs/orchestrator.dev.yaml:32-34`）；本地 `configs/orchestrator.dev.yaml:30` 甚至 `gateway.enabled: false` |
| `audit.http` | **端口/挂载点失效 + 缺鉴权**（`EmitAudit`）；`### Health` **现状可用** | `EmitAudit` 是集合中唯一 Connect 风格路径（`:8`），但打 8083/orchestrator——AuditService 只挂在 `cmd/api`（`cmd/api/main.go:65-73`，dev 8087，`configs/api.dev.yaml:1`），且全方法要求 `Authorization: Bearer <jwt>`（`internal/audit/interceptor.go:23-44`，该请求未带）。`GET http://localhost:8083/health`（`:42-43`）在 orchestrator 运行时成立（`internal/app/app.go:139-143`） |

## 2. 集合结构与服务覆盖

目录 `api/kulala/` 共 6 个 `.http`（全部 tracked，`git ls-files` 核实）：
`audit.http`(43 行)、`auth.http`(104)、`manager.http`(411)、`operator.http`(41)、
`orchestrator.http`(230)、`webhook.http`(106)。文件间没有 `pre.js`/`post.js`，也没有任何断言
operator（grep `{%`、`client.`、`< ./`、`> ./`、`@kulala-expect` 均零命中）。请求命名用
`### <Name>` + `# @name <alias>` 双轨（如 `auth.http:25-26`）。notifier / notification-sink
没有对应集合（未找到）。

## 3. 变量与鉴权

### 3.1 环境文件机制（真实存在的那部分）

- 变量来源是仓库根的 **`http-client.env.json`**（不在 `api/kulala/` 内，已入库），两个 profile：
  `dev`（`:2-23`）、`kind`（`:24-37`）。它提供 `BASE_URL`、`ADMIN_USER/ADMIN_PASS`、夹具常量
  `CUSTOMER_ID/CLUSTER_NAME/RELEASE_NAME/CHART_REF/CHART_VERSION/IMAGE_DIGEST` 与死端口
  `*_GRPC_PORT`（见 §1）。注意其值与现行 seed 并不一致（如 `CUSTOMER_ID=localhost001` `:9`
  vs devseed 夹具的 `dev-customer-a`，`configs/e2e.dev.yaml:51-56`）。
- 解析顺序（文档变量 → env JSON（private 覆盖）→ `.env` → 持久化变量 → magic variables）依据
  知识库参考 `References/core/tools/kulala-http-client.md:49-51`（`verified: true`，2026-09-04）。
  OS 环境变量是否并入普通 `{{NAME}}`，两份本地记录互相矛盾且**本仓库未实测**——稳妥做法是把所需
  变量显式写进 profile。集合内没有任何文档变量声明（`@VAR=` 形式，grep `^@` 零命中）。
- `http-client.private.env.json` 未找到；`.gitignore` 也没有该文件名的忽略条目（只有 `.env`，
  `.gitignore:65-66`）。若要放真实凭据，先自行加忽略规则。

### 3.2 env 文件未覆盖、必须人工提供的变量

逐项 grep 后确认以下引用既无 env 定义、也无文档变量/脚本来源：`token`（**45 处**引用，集合内
第一大变量）、`api_key`、`harbor_signature`、`DEFINITION_ID`、`REVISION_ID`、`CLUSTER_ID`、
`TASK_ID`、`PREPARE_TOKEN`、小写族的 `bundle_id`/`definition_id`/`revision_id`/`operation_id`/
`release_id`。它们指向的实体（Customer/Cluster/ReleaseDefinition/ValuesRevision/Operation）需先经
`make dev-up` + `make dev-seed` 播种存在，ID 从 `data/dev-fixture.json` 取后手工填入（夹具组装
语义 `Makefile:156-180`）。

### 3.3 令牌怎么获取（现状与正解）

- 集合内设计上的登录请求是 **`Login`**（`auth.http:25-33`）。但 `manager.http:6` 注释声称
  `{{token}}`「由 Login 响应处理器注入」——**整个集合没有任何 response handler**（§2 grep 核实），
  该机制未找到；`Login` 本身又打向已退役的 `POST {{BASE_URL}}/api/v1/auth/login`。
- 现行正式登录 seam：`auth.v1.AuthService/Login`（`api/proto/auth/v1/auth.proto:210`；挂载
  `cmd/auth/main.go:190-191`），Connect JSON 形态
  `POST http://localhost:8085/auth.v1.AuthService/Login` + `Content-Type: application/json`
  （单端口协议 `Makefile:3-4`；`docs/architecture.md:87`）。集合内**未找到**对应的现成请求，需自补；
  拿到响应中的 access token 后**手工**粘贴为 `token` 变量（或补一段 `> {% client.global.set(...) %}`，
  属 §7 重写决策的一部分，本档不代为实施）。
- 开发账号口令：`data/dev-credentials.env` 四键 `DEV_ADMIN_PASSWORD`、`DEV_DEPLOYER_PASSWORD`、
  `DEV_READER_PASSWORD`、`E2E_RUNNER_PASSWORD`（`internal/devfixture/files.go:78-84`）。
- 审计侧同样必须有 Bearer JWT（`internal/audit/interceptor.go:26-35`）。

## 4. 在 Neovim/Kulala 之外复用

- `.http` 语法以 JetBrains HTTP Client 为基础（`References/core/tools/kulala-http-client.md:17`，
  `verified: true`）。纯 REST 块 + `# @name` + `{{VAR}}` + `http-client.env.json` 属 JetBrains 家族
  语法，JetBrains / VS Code REST Client / Restfox 一类工具**通常可导入**——本仓库无实测证据，
  **不确定**；且 §1 已判定这些 REST 请求即使导入可解析，目标端点也已不存在。
- `GRPC host:port package.Service/Method` 请求行与 `@grpc-plaintext` 声明（`operator.http:5,12`、
  `orchestrator.http:13`）是 Kulala 在 JetBrains 基础上的扩展（同 KB `:17`）；其它客户端是否识别
  **不确定**。`@grpc-plaintext` 在当前 Kulala 版本按文件头作用域生效与否，**未验证**。
- 与客户端无关的可移植写法：Connect JSON unary 就是普通 `POST /<package>.<Service>/<Method>` +
  `Content-Type: application/json`（官方示例由 `make dev-stage-publish` 直接印出，
  `Makefile:327`）。改写请求走这条路即可用任何 HTTP 客户端复用。
- 协议迁移提示（若坚持 raw gRPC）：gRPC 线协议需要 HTTP/2。明文监听中只有 operator 自己显式启用
  unencrypted HTTP/2（`cmd/operator/main.go:74-81`）；`app.Run` 对其它服务是普通 `http.Server`
  （`internal/app/app.go:159-164`），未找到 h2c 配置（全仓 grep `h2c|SetUnencryptedHTTP2` 仅 operator
  命中）。因此把 `GRPC` 行只改端口到 8083/8085/8087 能否走通**不确定**（倾向不成立）；
  8084 的明文 gRPC 仅在 operator 挂出 OperatorService 的 gateway 模式下成立
  （`cmd/operator/main.go:138,246`），agent 模式的 OperatorService 挂载点在 orchestrator mTLS gateway。

## 5. 与 `make api-<service>` 目标的关系

六个目标全部存在（当前 `Makefile` 逐行核对；`KULALA_DIR := api/kulala`，`Makefile:245`）：

| target | 打开的文件 | 出处 |
| --- | --- | --- |
| `make api-auth` | `api/kulala/auth.http` | `Makefile:248-249` |
| `make api-manager` | `api/kulala/manager.http` | `Makefile:252-253` |
| `make api-webhook` | `api/kulala/webhook.http` | `Makefile:256-257` |
| `make api-operator` | `api/kulala/operator.http` | `Makefile:260-261` |
| `make api-orchestrator` | `api/kulala/orchestrator.http` | `Makefile:264-265` |
| `make api-audit` | `api/kulala/audit.http` | `Makefile:268-269` |

它们**只是 `nvim <file>` 打开集合**，不生成、不刷新任何请求，与 `make proto` 无关——不要指望
「跑一下 api-* 就能修好集合」。启动被调服务用 `make run-webhook/run-orchestrator/run-operator/
run-auth/run-notifier/run-api`（`Makefile:79,83,88,92,96,100`）或单服务模式 `make dev-stage-*`
（`Makefile:286-361`；端口对照 `docs/dev-environment.md:138-144`）。dev kustomize 环境不部署
`cmd/api`（`deploy/kustomize/services/` 无 api 清单），调试审计需本地起 `make dev-stage-audit`。

## 6. 常见坑（速查）

1. **选 `dev` profile 即全灭**：`BASE_URL` :8081 无监听（§1）；`kind` profile（8080/8443，
   `http-client.env.json:26-36`）对应更早的 kind 拓扑，当前本地集群是 k3d
   （`docs/dev-environment.md` 开头拓扑），勿选。
2. **gRPC 端口 8446/8447/8443 都不存在**：现行是单端口（8082–8087，`Makefile:3-4`），
   且明文 raw gRPC 只在 operator 的 8084（h2c，`cmd/operator/main.go:74-81`）有代码依据（§4）。
3. **`/api/v1` 没有服务端**：manager.http:201 的注释自证 ValuesRevision REST 已退役
   （ADR-002/015），适用整个 REST 面。
4. **`audit.http` 双坑**：端口/挂载点（8083→8087）与缺 JWT（§1 表）。
5. **grpc.health 不存在**：现行健康检查是 HTTP `GET /health`（`internal/app/app.go:139-143`）。
6. **`{{token}}` 不会自动来**：45 处引用、0 个捕获脚本（§3.3）。
7. **JSON body 的字段名**：集合混用 camelCase（`audit.http:17`）与 proto 原蛇形
   （`orchestrator.http:40`）；protojson 解码官方行为是两者皆收，本仓库**未实测**。幂等键有两种载体：
   gRPC 块 header `Idempotency-Key`（`manager.http:210,222`）vs body 字段 `idempotency_key`
   （`orchestrator.http:42`）。
8. **夹具前置**：`CreateReleaseDefinition` 等要求 Customer/Cluster 已存在（`orchestrator.http:159-161`
   引用 `CUSTOMER_ID`/`CLUSTER_NAME`）；先 `make dev-seed`，ID 以 `data/dev-fixture.json` 为准。
9. **webhook 签名**：`{{harbor_signature}}` 依赖文件头注释里设想的 pre-request JS（`webhook.http:6-8`），
   脚本不存在；且现行 WebhookService 的入参是 proto JSON 消息（`api/proto/webhook/v1/webhook.proto:20-32` 的平铺字段），
   不是 Harbor 原始事件体。

## 7. 待决策项：这套过期集合怎么处置（本档只列选项，不动 `api/kulala/*.http` 本体）

| 选项 | 内容 | 量级参考 |
| --- | --- | --- |
| A. 重写 | 6 个文件全部改写为 Connect JSON（path `/<pkg>.<Service>/<Method>`、端口 8082–8087、鉴权头），补 `Login` 响应捕获脚本与夹具变量清单 | 现有 74 个命名请求需逐一重映射；权威方法面是 `api/proto` 的 **104 个 RPC**（grep `rpc ` 计数）；44 个 REST 请求没有等价端点，需按语义改用 RPC 重写（如 `ListOrgs`→`auth.v1.OrganizationService/ListOrganizations`，`api/proto/auth/v1/auth.proto:411`） |
| B. 删除 | 移除 6 个文件与 6 个 `api-*` 目标，Web 调试改走 `web/` 前端或 curl/Connect 客户端 | 改动最小，但丢失 Kulala 内的负例样本（如 `HarborPushInvalidSig`、`CreateOpOnDisabledDef`） |
| C. 标注过期保留 | 文件头加 STALE 横幅 + 指向本档 §1 | 零风险但读者仍会踩空；需同步改 `Makefile` help 文案与 `docs/dev-environment.md:138-139` 的集合引用，避免文档继续宣传失效入口 |

无论选哪项，`http-client.env.json` 的端口段（8081/844x）都必须随决策一并修正或删除，否则任何
保留的集合仍在默认 profile 下打向死端口。

## 8. 未找到 / 不确定清单

- 未找到：`/api/v1` 服务端实现；:8081/:8080/:8443/:8446/:8447 的任何监听配置；集合内 pre/post 脚本
  与断言；`{{token}}` 捕获机制（`manager.http:6` 注释与文件现状不符）；grpc.health 注册；
  AuditService 在 orchestrator 上的挂载；`http-client.private.env.json`；notifier/notification-sink
  的集合文件与 `api-*` 目标。
- 不确定：其它 HTTP 客户端对 `GRPC` 行 / `@grpc-*` / `# @name` 的兼容性；`@grpc-plaintext` 在当前
  Kulala 版本的生效方式；OS 环境变量并入 `{{NAME}}` 的行为；protojson 对 snake_case 输入的接受度；
  把 GRPC 请求改指 8083 等非 operator 端口后 raw gRPC 能否握手（代码依据倾向否，§4）。

> 事实源：`api/kulala/auth.http`、`api/kulala/manager.http`、`api/kulala/orchestrator.http`、
> `api/kulala/operator.http`、`api/kulala/webhook.http`、`api/kulala/audit.http`、
> `http-client.env.json`、`Makefile`、`configs/*.dev.yaml`、
> `deploy/kustomize/dev/configs/orchestrator.dev.yaml`、`deploy/kustomize/dev/configs/notification-sink.dev.yaml`、
> `deploy/kustomize/services/`、`deploy/dev/dev.sh`、`cmd/api/main.go`、`cmd/auth/main.go`、
> `cmd/operator/main.go`、`cmd/orchestrator/main.go`、`cmd/webhook/main.go`、`internal/app/app.go`、
> `internal/audit/interceptor.go`、`internal/devfixture/files.go`、`api/proto/auth/v1/auth.proto`、
> `api/proto/webhook/v1/webhook.proto`、`api/proto/orchestrator/v1/orchestrator.proto`、
> `docs/architecture.md`、`docs/decisions/ADR-002-connect-protobuf-single-port-contract.md`、
> `docs/dev-environment.md`、`docs/cli.md`、`README.md`、`.gitignore`、
> 知识库 `References/core/tools/kulala-http-client.md`（verified: true）、
> `git log -- api/kulala/`（文件级最后提交）。

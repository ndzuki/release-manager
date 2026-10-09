# web/ — Release Manager 控制台（Vue 3 + Pinia + Vite）

面向运营/管理用户的单页应用。本 README 以本目录与仓库源码为事实源，每条结论附 `文件:行号`。
运行时环境变量的清单见 [`.env.example`](./.env.example)。

## 1. 技术栈与版本

以下版本按 `web/package.json` 所写范围（caret range）记录，实际解析版本以 `web/package-lock.json` 为准：

| 类别 | 依赖 | 版本（package.json 行号） |
| --- | --- | --- |
| 框架 | `vue` | `^3.5.13`（package.json:27） |
| 状态管理 | `pinia` | `^2.3.1`（package.json:26） |
| 路由 | `vue-router` | `^4.5.1`（package.json:28） |
| 构建/开发服务器 | `vite` | `^6.3.5`（package.json:47）+ `@vitejs/plugin-vue` `^5.2.3`（package.json:38） |
| 类型检查 | `typescript` `~5.8.3`（package.json:45）、`vue-tsc` `^2.2.10`（package.json:49） |
| 单元测试 | `vitest` `^4.1.10`（package.json:48）+ `@vue/test-utils` `^2.4.11`（package.json:39）+ `happy-dom` `^20.10.6`（package.json:44） |
| 浏览器 E2E | `@playwright/test` `^1.62.1`（package.json:34） |
| Lint | `eslint` `^10.7.0`（package.json:40，flat config：eslint.config.js）+ `typescript-eslint` `^8.65.0`（package.json:46）+ `eslint-plugin-vue` `^10.9.2`（package.json:42） |
| RPC | `@connectrpc/connect` / `@connectrpc/connect-web` `^2.1.1`（package.json:22-23） |
| Protobuf (TS) | `@bufbuild/protobuf` `^2.2.5`（package.json:15），代码生成插件 `@bufbuild/protoc-gen-es` `^2.13.0`（package.json:32） |
| 编辑器组件 | `@codemirror/*`（package.json:16-21）；辅助 `js-yaml` `^5.2.1`（package.json:25）、`deep-diff` `^1.0.2`（package.json:24） |

TypeScript 编译配置为 `strict: true`、`noEmit: true`、`@/* → src/*`（tsconfig.json:7、16、18-19；vite 侧同名 alias 在 vite.config.ts:8-12）。

## 2. npm scripts 清单与 CI 门禁关系

`package.json:6-13` 共 6 个 script：

| script | 实际命令 | 含义与前置条件 | 是否参与仓库级 CI 门禁 |
| --- | --- | --- | --- |
| `npm run dev` | `vite` | 启动 dev server（端口固定 5173，vite.config.ts:24），并按 **proto 包前缀**代理 Connect 请求到本机 8082-8088 服务端口（vite.config.ts:46-90，见 §4）。前置：Node + `npm ci` 安装依赖；要用真实后端需先起对应服务（`make dev-up` 或本地进程）。 | 不参与（dev server 只在本地）。 |
| `npm run build` | `vue-tsc -b && vite build` | 先以 project references 做全量类型检查（含 `tsconfig.node.json` 引用的 vite.config.ts），再产出 `dist/`。 | 参与：CI 的 `web` job（`.github/workflows/test.yml`，Node 22 + `npm ci`）。 |
| `npm test` | `vitest run` | 跑 `src/**` 下全部 `*.test.ts`/`*.spec.ts`（当前 **96** 个文件）；环境 `happy-dom`、`globals: true`、`restoreMocks: true`（vite.config.ts:13-16）；`e2e/**`、`playwright/**`、`node_modules/**` 被排除，避免 Playwright spec 被 vitest 误跑（vite.config.ts:17-21 注释与 exclude）。 | 参与：CI 的 `web` job（`.github/workflows/test.yml`，Node 22 + `npm ci`）。**Makefile 有转发**：`web-check`（`Makefile:413-416`）依次跑 `npm run lint && npm test && npm run build`，`web-install`（`Makefile:408-410`）跑 `npm ci`；`npm test` 也可在 `web/` 内直接跑（`docs/testing.md:45-48` 明写「Makefile 转发其中三关」）。 |
| `npm run test:e2e` | `playwright test` | 跑 `web/e2e/` 下的 Playwright spec（playwright.config.ts:9）。默认打 `http://127.0.0.1:5173`（playwright.config.ts:17），且**必须**设置 `E2E_BACKEND=true`，否则整个 suite 显式 skip（e2e/emergency-smoke.spec.ts:12-17）。需要真实后端栈（ADR-013：只走正式 API，不打 mock）。 | **部分参与**：`web` job 本身不跑 E2E；CI 的 `e2e-prerequisite` job（`.github/workflows/test.yml:420-485`，**push / workflow_dispatch 触发，PR 上不跑**）会 `npm ci` + `npx playwright install chromium` + `make e2e-prerequisite-ci`，该 target 经 `test/e2e/prerequisite/console.sh:47` 默认只跑 `web/e2e/rollback.spec.ts`（见 `docs/testing.md:47-49`）。`emergency-smoke`/`navigation` 两个 spec 目前不在 CI 跑。 |
| `npm run preview` | `vite preview` | 本地预览 `dist/` 构建产物；未在本仓库配置 preview 端口/proxy（vite.config.ts 的 `server` 段只作用于 dev）。 | 不参与。 |
| `npm run lint` | `eslint .` | flat config：js/ts/vue recommended + prettier 兼容层；忽略 `dist/**`、`src/gen/**`、`*.d.ts`、`*.tsbuildinfo`；规则含 `@typescript-eslint/no-explicit-any: error`（eslint.config.js:7-23）。 | 参与：CI 的 `web` job（`.github/workflows/test.yml`，Node 22 + `npm ci`）（与 test/build 同一步）。 |

与 web 相关的 CI 触点有**三个**：① `web` job（Node 22 + `npm ci`，依次 `npm run lint`、`npm test`、`npm run build`，PR 与 push 都跑；见 `test.yml` 的 `web:` 段）；② `e2e-prerequisite` job（push/dispatch 上 `npm ci` + Playwright + `make e2e-prerequisite-ci`，只跑 `rollback.spec.ts`；`test.yml:420-485`）；③ `license-check` job 的 `make check-licenses` 读取前端 lockfile 的 license 字段做许可门禁（`test.yml:61-68`）。`make quality` 亦含 `web-check`（`Makefile`），故本地聚合门禁也会跑 web 的 lint/test/build。

结论：web 的 lint/test/build **已进仓库级 CI**（`web` job，无 `if` 守卫，PR 与 push 都跑）**且进了本地聚合门禁**（`make web-check` 已并入 `make quality`）；未进 CI 的是浏览器 E2E 的**另外两个 spec**（`emergency-smoke`/`navigation`）与覆盖率采集。

## 3. 目录结构与关键约定

```
web/
├── index.html                 Vite 入口（挂载 #app）
├── vite.config.ts             插件 / alias / vitest / dev server + Connect 代理
├── playwright.config.ts       浏览器 E2E（testDir ./e2e）
├── env.d.ts                   VITE_* 环境变量的类型声明（env.d.ts:3-13）
├── eslint.config.js           lint 配置
├── nginx.conf                 容器内静态服务 + Connect 反代（见 §4）
├── e2e/                       Playwright spec（当前 **3** 个：`emergency-smoke`/`navigation`/`rollback`）
├── prototype/                 一次性契约验证脚本（emergency-contract-gate.ts，头部注明 throwaway）
└── src/
    ├── main.ts                bootstrap：Pinia → auth.initialize() → router → mount（main.ts:8-17）
    ├── App.vue
    ├── pages/                 路由级页面（30 项；本项目不叫 views/）
    ├── components/            按域分组：audit / clusters / common / customers / emergency /
    │                          operations / operators / releases / values
    ├── stores/                Pinia setup-store（21 项，auth/cluster/operationTimeline/...）
    ├── composables/           useOperatorPolling / useSessionExpiry / useEmergencyEffectObservation
    ├── features/emergency/    紧急变更域逻辑（errors/model/validation）
    ├── connect/               手写 API 客户端层：client.ts（transport + 服务 client）+
    │                          {cluster,customer,emergency,operation,operator}-api.ts、values-revision.ts
    ├── gen/                   buf 生成的 TS 代码（14 个 *_pb.ts，禁止手改）
    ├── router/index.ts        路由 + 特性开关守卫（见 §6）
    ├── types/                 前端视图模型类型
    └── utils/                 纯函数工具（含单测）
```

约定与代码生成：

- **`src/gen/**` 是生成代码，禁止手改**；`.gitignore` 之外它被提交进仓库并由 eslint 忽略（eslint.config.js:7；AGENTS.md「生成代码不许手改」）。
- 改契约的正确流程：修改 `api/proto/**` → 在仓库根执行 `make proto`（Makefile:275-279）。它运行 `buf generate --template api/proto/buf.gen.yaml`，该模板同时产出 Go（`api/gen`，buf.gen.yaml:3-8）与 TS（`web/src/gen`，buf.gen.yaml:9-13，插件 `buf.build/bufbuild/es`，`target=ts`）。因此 **`make proto` 会重写 `web/src/gen`**；buf 缺失时 target 会先 `go install github.com/bufbuild/buf/cmd/buf@latest`（Makefile:276）。remote plugin 需要网络。
- 仓库里另有一份 web-only 子集模板 `api/proto/buf.gen.web.yaml`（同样的 es 插件、限定 paths）；未找到任何 Makefile 目标或脚本引用它（`grep -rn buf.gen.web Makefile scripts/ .github/` 无匹配）。以真实命令为准：重生成走 `make proto`。
- 契约变更的验收门（`buf lint` 已接 CI：由 CI 的 `proto-check` job 调用 `make lint-proto`，`test.yml:612-637`；`buf breaking` 仍是文档约定）：消费方实现前需 `tsc --noEmit` + `buf lint`/`buf breaking` 通过（`docs/architecture.md:115`；`buf lint` 已接入：`make lint-proto` → `buf lint`，见 `Makefile:279-283`；`buf breaking` 在 Makefile 中仍无目标）。

## 4. 与后端的对接

- **Connect transport**：`createConnectTransport({ baseUrl: import.meta.env.VITE_API_BASE ?? '', useBinaryFormat: true, fetch: browserFetch, interceptors: [sessionInterceptor] })`（connect/client.ts:50-55）。默认 `baseUrl` 为空串 = 同源相对路径；`browserFetch` 固定 `credentials: 'include'`（client.ts:46-48）。按 service 建的类型化 client 在 client.ts:57-61（auth/organization/orchestrator/bundle/audit），其余 API 封装在 `src/connect/*-api.ts`。
- **base URL 来自哪里**：只有 `VITE_API_BASE`（client.ts:51）一个环境变量；不设置就走同源。
  - dev：Vite 代理按 **proto 包前缀**转发到本机服务端口——`/auth.v1. → 8085`、`/orchestrator.v1. → 8083`、`/operator.v1. → 8084`、`/audit.v1. → 8088`、`/notifier.v1. → 8086`、`/webhook.v1. → 8082`、`/trust.v1. → 8083`，外加 `/health`、`/readyz`、`/environment` → 8083（vite.config.ts:46-90）。
  - 容器部署：`nginx.conf` 做同样的**包前缀**反代（`/auth.v1. → auth:8085`、`/orchestrator.v1. → orchestrator:8083`、`/webhook.v1. → webhook:8082`、`/operator.v1. → operator:8084`、`/notifier.v1. → notifier:8086`、`/audit.v1. → api:8088`、`/trust.v1. → orchestrator:8083`，并代理 `/health`、`/readyz`、`/environment` 到 orchestrator；`nginx.conf:17-147`），SPA fallback 在最后（`nginx.conf:149`）。
- **登录与会话**：不存 Bearer token。登录经 `authClient.login` Connect 调用完成（stores/auth.ts:100-103）；服务端以 HttpOnly cookie 下发会话（`rm_access`/`rm_refresh`，internal/auth/service.go:17-18；internal/auth/browser_session.go:216、222-224），前端只可读 CSRF cookie `rm_csrf`，由 `sessionInterceptor` 复制进 `X-CSRF-Token` 请求头（client.ts:7-8、29-33）。启动时 `initialize()` 依次 `getInitStatus` → `validateToken` → 失败则 `refreshToken`（auth.ts:64-88）；`Unauthenticated`/`PermissionDenied` 统一回调 `handleAuthError`（client.ts:38-42、auth.ts:131-141）。业务数据只存内存 ref，`localStorage`/`sessionStorage` 仅用于 Values 编辑器草稿与操作表单草稿（stores/valuesEditor.ts:36-38、stores/operationForm.ts:89、227），不是令牌。
- **权限投影**：角色从会话用户派生（`canWrite`、`canEnrollOperators` 等，auth.ts:36-43），前端只做 UI 门禁，服务端仍是权威。

## 5. 测试与类型检查现状

- 单元/组件测试：`npm test`（vitest run，happy-dom）。现有 **96** 个 `*.test.ts`/`*.spec.ts`（945 个用例） 分布在 `src/**`。
- 类型检查：无独立 script，`npm run build` 前半段 `vue-tsc -b` 即全量检查（package.json:8）。
- 浏览器 E2E：`npm run test:e2e`（Playwright，chromium-only project，`fullyParallel: false`、`retries: 0`、trace retain-on-failure；playwright.config.ts:10-27）；需要真实后端 + `E2E_BACKEND=true`，否则显式 skip（emergency-smoke.spec.ts:12-17）。可覆盖 `E2E_BASE_URL` 指向 staging（playwright.config.ts:17 注释与代码）。
- 覆盖率：**未配置**——vite.config.ts 的 `test` 段无 `coverage` 配置，CI 的 `web` job 会跑 `npm ci`/`lint`/`test`/`build`（但不采集覆盖率）（`make test-coverage` 只覆盖 Go，Makefile:404-408）。
- 门禁归属：如 §2 所述，web 的 **lint/test/build 已进仓库级 CI**（`web` job，PR 与 push 都跑）；**浏览器 E2E 只在 push/dispatch 的 `e2e-prerequisite` job 跑，且只跑 `rollback.spec.ts`**，`emergency-smoke`/`navigation` 仍不在 CI。

## 6. 常见坑（均可在代码中证实）

1. **新增 proto service 后代理失效**：代理 key 是 **proto 包前缀**（`/auth.v1.` 等，vite.config.ts:46-90），**同一包内新增服务无需改动**；只有当整个**包**未列出时才不会被转发，dev 下表现为请求打到 Vite 而 404（Vite 的 SPA fallback 只改 GET/HEAD）。生产镜像同理要按包加 nginx location（nginx.conf:17-147）。
2. **端口冲突**：dev server 固定 5173 但未设 `strictPort`（vite.config.ts:24）；若 5173 被占用 Vite 会换端口，而 Playwright 默认 baseURL 仍是 `127.0.0.1:5173`（playwright.config.ts:17）→ E2E 静默打错对象，需显式 `E2E_BASE_URL`。另外 `make dev-up` 把集群 8082-8088 映射到宿主同端口段（deploy/dev/dev.sh:900），与本地 `make run-*` 进程、`make run-api`（configs/api.dev.yaml 的 8087）互斥。
3. **生成代码被手改**：`src/gen/**` 已提交进 git（`git ls-files web/src/gen` 14 个文件），手改会在下一次 `make proto` 时被无声覆盖；lint 也忽略该目录（eslint.config.js:7），坏改动不会被 eslint 拦住。
4. **`.env` 忽略范围比想象的窄**：根 `.gitignore:66` 只忽略 `.env`（任意层级），`web/.gitignore:3` 的 `*.local` 覆盖 `.env.local`；实测 `web/.env.development`、`web/.env.production` **不被忽略**，把真实值写进这类文件会被提交。只用 `.env.local`（从 `.env.example` 复制），并保留 `.env.example` 入库（它本身未被忽略，属预期）。
5. **vitest 会捡起 src 下任意 `*.spec.ts`**：exclude 只挡了 `e2e/**` 与 `playwright/**`（vite.config.ts:21）；浏览器类 spec 必须放 `web/e2e/`，否则会把需要真实后端的用例混进 `npm test` 门禁（vite.config.ts:17-20 注释记录了这一动机）。
6. **`npm run test:e2e` 全 skip 不代表通过**：没有 `E2E_BACKEND=true` 时 suite 显式 skip（emergency-smoke.spec.ts:12-17），CI 不跑它是既定现状（§2），别把它当回归证据。
7. **tracked 的构建产物**：`web/tsconfig.tsbuildinfo`、`web/tsconfig.node.tsbuildinfo`、`web/vite.config.d.ts(.map)` 虽列在 `web/.gitignore:4-6`，但已被 git track（`git ls-files` 可见）——gitignore 对已跟踪文件无效，改动它们会出现在 diff 里；不要顺手删除或再提交。
8. **feature flag 只有字符串 `'false'` 才关闭**：所有开关读取都是 `!== 'false'` 语义（router/index.ts:7-9 与守卫 179、182、185；AppShell.vue:11；ClusterDetailPage.vue:17；OperationDetailPage.vue:32；ReleaseInventoryTable.vue:16；ReleaseInventoryPage.vue:24），写 `FALSE`/`0` 无效，默认全部启用。

> 事实源：`web/package.json`、`web/vite.config.ts`、`web/env.d.ts`、`web/eslint.config.js`、`web/tsconfig.json`、`web/playwright.config.ts`、`web/nginx.conf`、`web/e2e/emergency-smoke.spec.ts`、`web/src/main.ts`、`web/src/router/index.ts`、`web/src/stores/auth.ts`、`web/src/connect/client.ts`、`api/proto/buf.gen.yaml`、`api/proto/buf.gen.web.yaml`、`Makefile`、`.github/workflows/test.yml`、`.github/workflows/sync-to-gitcode.yaml`、`internal/auth/service.go`、`internal/auth/browser_session.go`、`deploy/dev/dev.sh`、`docs/testing.md`、`docs/architecture.md`、`web/.gitignore`、`.gitignore`

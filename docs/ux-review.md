# Web 控制台交互体验审查（2026-09-28）

审查对象：`web/`（Vue 3 + Pinia + Vite 控制台）与它所依赖的认证/授权/网关链路。
目的：回答两件事——**当前交互体验是否满足需求**、**哪里可以优化**。

## 0. 方法与证据强度

| 证据层 | 做法 | 强度 |
| --- | --- | --- |
| **浏览器实测**（本文 B1–B6） | 2026-09-28 起完整 dev 环境（5 集群 + fixture v2），用 Playwright 走 20 个页面/关键分支，抓 console、network、页面文本与整页截图 | **已验证**：命令/响应/日志可复现 |
| **需求映射** | 逐条读 REQ-033/051–060 的 UI 可验收 AC，映射到实现位置（182 行，`yes` 63 / `partial` 102 / `no` 17）。清单生成于 **2026-09-25**（同一会话的前一阶段），2026-09-28 重新复算过计数 | 静态 + 文件级锚点校验（引用逐条查文件存在与行号不越界，**0 处问题**），未逐条浏览器复测 |
| **静态代码审查** | 20 页面 / 50 组件 / 13 store / 4 composable，按 9 个维度出 87 条问题（4 blocker / 36 major / 36 minor / 11 nit）。清单生成于 **2026-09-25**，2026-09-28 重新复算过计数与分布 | 静态，标 `待核实` 的 14 处未确认 |
| 既有门禁 | `npm test` → 49 files / 323 tests 全绿（2026-09-28 复跑一致）；`npm run lint` → exit 0 | 已跑 |

**证据强度声明**：

- B1–B6 是 **2026-09-28** 在真实 dev 环境与真实浏览器上的实测结论，命令/输出/日志可复现。
- 第 2 节的「需求符合度」汇总基于 §0 的两份静态清单，**未经浏览器逐条复测**。
- 第 4 节的长尾优化清单来自静态审查，同样**未经浏览器逐条复现**，按此口径引用。
- 原始清单（全文，含每条的 `file:line`）归档在知识库
  `Notes/Audit-2026-09-28/`：`web-console-static-ux-audit.md`（87 条）与 `web-console-req-ac-map.md`（182 条）。
- 本文档在 2026-09-28 独立审查后修订过一次；修订前的缺陷与修订项见 §8。

## 1. 结论摘要

1. **控制台在交付版本下无法登录** —— 前端只实现 cookie 会话，后端服务从不启用 cookie 分支，
   登录 200 但 `user` 为空且无 `Set-Cookie`。除 `/login`、`/init`、404 外**所有页面不可达**
   （B1）。REQ-033 的 `AC-033-01` 被标记 `[x]` 已交付、11/11 覆盖，与真实浏览器行为不符。
2. **浏览器会话下所有 orchestrator 写操作被 fail-closed 拒绝** —— 授权快照的后台刷新复用了
   「入站 `Authorization` 头」，cookie 会话下该头为空，于是快照永远不新鲜；
   用户看到的是**别人的 token 过期消息**（B4）。REQ-056「控制台创建发布操作」在真实浏览器下不可用。
3. **操作时间线实时更新在浏览器会话下必然失败** —— 流式拦截器没有 cookie 回退（B3）。
   页面长期显示 `实时更新已断开，正在重连…` 且时间线为空，等于 REQ-057 的流式看板不可用。
4. **审计页在两条入口路径下都不可用** —— 缺少 `/audit.v1.` 反代（容器）与错误的 dev 代理目标，
   表现为 `HTTP 405`，且页面把错误同时渲染成「空结果」（B5 + 静态 UX-010）。
5. **前端零 CI 门禁**（评估时的事实）—— `.github/workflows/test.yml` 当时共 **15** 个 job（其中 13 处 `actions/setup-go`），
   **0 处 `actions/setup-node`、0 处 `npm`/`vitest`/`vue-tsc`/`playwright`**
   （当时全仓仅 `codeql.yml` 的一条注释里出现 `setup-node`）；
   > 2026-09-28 更新：`TASK-175` 已按本报告的建议新增 `web` job（`actions/setup-node@v7` + `npm ci` + `lint` + `test` + `build`），
   > 该文件现为 16 个 job。这条 blocker 已关闭，证据见 `Tasks/TASK-175-web-ci-gate.md`；
   也就是说 `vue-tsc`、`eslint`、`vitest`、`playwright` 都不在 CI 里跑。
   上述 1–4 类缺陷正是「没有任何自动化在浏览器里点过一次」的直接后果。
6. **假阳性风险**：Preflight 面板把任何非 `failed` 的 overall 显示为「通过」；
   ReleaseStatusBadge 把未知状态显示为 `Active`（与 Preflight 同一类「未知即放行」）。

## 2. 浏览器实测发现（B1–B6）

### B1【blocker】控制台无法登录：cookie 分支从未启用

**证据链**（三层互相印证）：

1. `cmd/auth/main.go:194` 调用 `NewAuthService(s.store, jwtMgr, limiter, logger, enforcer)`，
   **没有**传可选的 `BrowserSessionConfig` ⇒ `browserEnabled=false`（`internal/auth/service.go:58`）。
2. token 模式下 `Login` 只回 token：`curl -D -` 打登录接口，响应头**没有任何 `Set-Cookie`**，
   body 里 `user` 字段缺失。
3. token 模式下 `ValidateToken` 读的是 **body 里的 token**（`internal/auth/service.go:263`），
   而控制台按 cookie 契约发空 body ⇒ 永远 `401` ⇒ 守卫把用户弹回 `/login`。

**真实表现**：登录接口 200、服务端日志有 `user logged in`，但浏览器停在登录页；
控制台无任何错误文案（`auth.ts` 的 `applySession` 因 `user` 为空走 `clearSession`，不报错）。

**浏览器复现（未打补丁的交付版本）**：点 `Sign in` 后

| 观测 | 值 |
| --- | --- |
| `POST /auth.v1.AuthService/Login` 状态 | `200` |
| 该响应的 `Set-Cookie` 头数量 | `0` |
| 浏览器 cookie 列表 | `[]` |
| 4 秒后的 URL | 仍为 `/login` |
| 页面是否出现任何错误文案 | `false`（**零反馈**） |

留档：`docs/images/user-manual/21-login-unpatched-no-progress.png`（与登录前截图逐像素一致，
唯一差别是输入框已填好——这正是「点了没反应」本身）。

**为什么长期没被发现**：`docs/api.md:254` 早就记录了「cookie 被拦截器接受却从不签发」，
但把它当成一处未接线的可选分支；REQ-066 的浏览器 E2E 在无 `E2E_BACKEND=true` 时**整 suite skip**，
于是没人走到这条路径。

**修复方向**（本次为出图在 worktree 里验证过）：在 `cmd/auth/main.go` 传入
`BrowserSessionConfig`，并让浏览器分支**同时**返回 bearer token —— 只开 cookie 会打断
devseed / E2E runner / kulala 集合（它们读 `Login.AccessToken`），这一点在补丁里实测过：
`bash test/e2e/prerequisite/smoke.sh` 44 pass / 0 fail（含 auth 跨重启 `ValidateToken`/`RefreshToken`）。

### B2【blocker，同源】白屏而非错误页

后端不可达时，引导阶段的 `GetInitStatus` 抛错会让 `bootstrap()` 在 `app.mount` 之前失败
（`web/src/main.ts:12`），页面上**什么都没有**：没有错误文案、没有重试。

**复现（2026-09-28 实测，非事后推断）**：临时把 `auth` 缩到 0 replica（该动作与
`smoke.sh` 的 auth 重启预检同源），再从浏览器打开控制台：

| 观测 | 值 |
| --- | --- |
| `GetInitStatus`（经控制台入口 8087） | `502 Bad Gateway` |
| `#app` 的 `innerHTML` 长度 | `0`（**应用未挂载**） |
| `body` 可见文本长度 | `0` |
| DOM 节点数 | `1`（只有 `body`） |
| 浏览器控制台 | `Failed to load resource: 502` + **未捕获** `pageerror: ConnectError: [unavailable] HTTP 502` |

留档：`docs/images/user-manual/22-blank-page-backend-down.png`（1440×900 全白，2026-09-28 复现后重拍；
auth 已随即恢复为 1 replica 并 `readyz` 200）。

**建议**：`bootstrap()` 包 try/catch，失败时挂载一个最小「无法连接服务端 + 重试」页面；
这也是排障成本最低的一处修复。

### B3【blocker】操作时间线实时更新对 cookie 会话永久失败

`internal/auth/interceptor.go:172` 的 `streamAuthInterceptor.WrapStreamingHandler` 只从
`Authorization` 头取 token（`:178-181`），**没有**一元拦截器那样的 `rm_access` cookie 回退。

**实测**：打开操作详情页，页面显示 `实时更新已断开，正在重连…` + `暂无时间线事件`；
服务端日志每 1–3 秒一次
`WatchOperation … unauthenticated: missing authorization header`（重连风暴）。

**影响**：REQ-057 的实时看板在浏览器会话下不可用；且客户端无退避，形成持续的无效重连。

### B4【blocker】写操作被 fail-closed 拒绝，用户看到的是「别人的 token 过期」

`internal/authorization/module.go:150` 取 `authctx.AuthorizationHeaderFromContext(ctx)`，
而该值来自 `internal/auth/interceptor.go:131` 写入的**入站 `Authorization` 头原文**——
cookie 会话下是空串。于是：

- 该 actor 的快照条目 `entry.authorization` 永远为空（或仍是曾缓存的、早已过期的 bearer）；
- 后台 `pull` 与 `AuthorizeWrite` 的即时 `pull` 都拿不到可用凭证 → 快照不新鲜 → **fail-closed 拒绝**；
- `AuthorizeWrite` 直接把 pull 的错误返回给调用方，于是前端显示
  `操作创建失败 invalid token: parse access token: token has invalid claims: token is expired`
  （实测原文），而用户自己的会话其实完全正常。

**实测**：控制台点 `确认创建` → 上述错误；服务端 `authorization decision … result=deny
reason=unauthenticated` 且 `source_version=0 policy_version=0 checkpoint=0`。

**长期环境同样中招**：该 dev 环境已跑 3 天，在 orchestrator pod **现存日志窗口**内
（`--since=200h`，窗口内**最早一条** `authorization snapshot pull failed` 的时间戳是 `2026-09-26T22:43:58.923Z`；
该窗口自身的首行日志是 `2026-09-26T22:43:45.350Z`），`authorization snapshot pull failed`
累计 **9659 次**（测量时刻 `2026-09-28T02:30:57Z`；这是一个**单调递增**的计数器，
本文档首次测量时为 9473，两者是同一条证据的不同时刻）；也就是说该条件**在 27 小时前就已成立且不自愈**
—— 一个长跑部署会静默失去全部写能力，只有 WARN 日志。

**建议**：快照拉取要拿**可续期的服务凭证**（或把「已验证的凭据」而不是原始头写进 context），
并且 `AuthorizeWrite` 不能把内部 pull 错误原样透传给用户——应映射为稳定错误码 + request_id。

### B5【blocker】审计页在两个入口下都不可用，且错误被渲染成空结果

- 容器入口（**修复前**）：`web/nginx.conf:17` 起只代理了 auth/orchestrator/webhook/operator/notifier，
  **没有 `/audit.v1.`**，`POST /audit.v1.AuditService/QueryAuditEvents` 落到 SPA 静态处理器 → `405`。
  **现状**：`web/nginx.conf:91` 已有 `location ^~ /audit.v1.` → `api:8088`（`:107` 另有 `/trust.v1.`），
  该 405 已由 TASK-174 修复；下文页面表现与截图是修复前的记录。
- dev 入口：`web/vite.config.ts:62-63` 把 `/audit.v1.` 指向 `8088`（`:62` 是 key，`:63` 是 target），
  而集群路径下 8087 是 `release-web` 自己（`release-api` 在 8088）→ 同样 `405`。
- 直连 `release-api`（8088）同一请求返回 `401`（说明路由存在），对照清楚。

**页面表现**（实测文本）：`! Audit request failed HTTP 405 Retry` 与
`∅ No audit events / No events matched the active organization and filters.` **同时出现** ——
把一次路由故障解释成「没有事件」，并给了一个永远按不好的 `Retry`。

### B6【major】Preflight 面板把 stage detail 作为原始 JSON 直出，页面横向溢出

操作详情页的 Preflight 面板逐 stage 打印 detail，内容是未经格式化的 JSON 原文，例如

```
artifact passed {"status":"passed","chart_digest":"sha256:e4183295…","detail":"chart archive digest matches…"}
render passed {"render_digest":"a36d52d3…","resources":[{"api_version":"apps/v1","kind":"Deployment",…
```

**实测证据**：该页整页截图 `17-operation-detail-upgrade.png` 的宽度是 **2121px**，
而视口是 1440px —— 即文档产生了横向滚动，右侧内容在默认窗口下不可见。
用户视角：这是发布准入的**核心证据面板**，却既不可读也不可复制（没有换行、没有 key/value 排版、
没有折叠）。

**建议**：stage detail 解析为 key/value 列表（`status` / `chart_digest` / `detail`），
长值 `overflow-wrap: anywhere` + 可展开原文；未知形状才退回 `<pre>` 并允许横向滚动（局部滚动，不是整页）。

## 3. 需求符合度
按 REQ-033 / REQ-051~060 的 UI 可验收 AC 逐条映射（182 行）：

| 判定 | 条数 | 含义 |
| --- | --- | --- |
| `yes` | 63 | 实现存在且与 AC 字面一致 |
| `partial` | 102 | 部分成立：缺分支、缺联动、入口条件与 AC 不一致 |
| `no` | 17 | 未实现（其中 12 条集中在 REQ-060；**该 REQ 已撤回，见下**） |

> ⚠️ **2026-09-28 修正**：`REQ-060`（通知任务与 dead-letter 前端）已于 **2026-09-19 由用户裁定撤回**
> （`status: withdrawn`，撤回取证见该文「变更记录」），其 22 条 AC **不属于验收面**。
> 因此上表 17 条 `no` 中属于 REQ-060 的 12 条**不是缺陷**；做前端需求面统计时必须剔除。
> 并入本次重新评估后，该 REQ 不再计入「必须补充」清单（见 `docs/ux-revamp-plan.md` §0.3、§12 G12）。

需要重点关注的**不符项**（静态审查发现，未全部浏览器复测）：

| 项 | 与 AC 的差异 |
| --- | --- |
| ~~`REQ-060`（通知任务前端）~~ | **已撤回，不是缺陷**（原记为「前端面整体缺失」）|
| `AC-033-01` | 初始化成功后直接 `replace({name:'Home'})`，不过登录页；且真实浏览器下登录路径根本走不通（B1） |
| `AC-033-06` / `AC-058-05` | Inventory 行内「紧急变更」入口只绑 `canExecuteEmergency`，**未与 kill switch `featureEnabled` 合取** ⇒ 开关关闭时入口仍可能渲染 |
| `AC-056-08` | 要求 rollback 入口，但 Web 无任何 rollback UI；`OperationForm` 对 ROLLBACK 只隐藏 patch，仍走 `CreateOperation` |
| `AC-057-26` | 要求首载不调 `GetOperation`，实现仍调用 |
| `AC-058-02/13/25` | `EmergencyAnnotationEditor.vue` **全仓零引用**，注解编辑在 UI 上无处可观测（组件自身还写着「后端契约暂不支持提交」） |

## 4. 可优化点清单

严重度：blocker（阻断主流程）/ major（明显伤体验或正确性）/ minor / nit。
维度：1 状态完整性 · 2 交互反馈 · 3 表单体验 · 4 可访问性 · 5 响应式布局 · 6 数据与网络 ·
7 Vue 工程实践 · 8 前端门禁 · 9 性能。「验证方式」是可机检的最小动作。

### 4.1 blocker / major（按维度归并，含 file:line）

| 编号 | 严重度 | 维度 | 问题 | 建议修复 | 验证方式 |
| --- | --- | --- | --- | --- | --- |
| B1–B5 | blocker | 认证/授权/网关 | 见第 2 节 | 见第 2 节 | 浏览器实测 |
| B6 | major | 5/2 | Preflight stage detail 原始 JSON 直出导致整页横向溢出（截图宽 2121px > 视口 1440px） | 解析为 key/value + 局部滚动 | 该页截图宽度 == 视口宽度 |
| UX-001 | ~~blocker~~ **已关闭** | 8 | 评估时：`.github/workflows/test.yml` 15 个 job（13 处 `actions/setup-go`）中**零** npm/node 步骤；`docs/testing.md:47` 自认需手工在 `web/` 内跑 | 已按建议新增 `web` job：`actions/setup-node@v7` + `npm ci` + `npm run lint` + `npm test` + `npm run build`（TASK-175） | `grep -c 'actions/setup-node' .github/workflows/test.yml` = **1**（TASK-175 前为 0） |
| UX-002 | blocker | 1/2 | `PreflightResultPanel.vue:11-15` 用 `overall === 'failed' ? '失败' : '通过'`，而类型是 `'passed' \| 'failed' \| string`（`types/operation.ts:252`）⇒ 未知取值（`pending`/`skipped`/空串）全部显示「通过」 | 显式三态，未知态中性色并显示原始值 | 单测传 `overall:'pending'` 断言**不**出现「通过」 |
| UX-003 | blocker | 2/7 | `ValuesRevisionActions.vue:46-54` 的 Submit 只受 `approving` 约束；`stores/valuesEditor.ts:94` 的 `saveDisabled` 只作用于 Save ⇒ 未保存/非法内容时提交的是**上一次持久化**的 revision | Submit 加 `\|\| saving \|\| saveDisabled`，或在 `submit()` 内检测未保存差异 | 单测：有未保存改动时 Submit 不可点 |
| UX-004 | blocker | 4 | 除 `CancelOperationDialog.vue:25-27,43` 外，6 个 `role="dialog" aria-modal="true"` 全无 Esc/初始焦点/焦点陷阱（`RevokeOperatorDialog`、`DisableCustomerDialog`、`RejectRevisionDialog`、`ValuesConflictDialog`、`EmergencyConfirmDialog`、`EnrollmentTokenModal`）；`ValuesEditorPage.vue:228` 内联面板也挂 `aria-modal` | 抽 `<AppDialog>`（Teleport + backdrop + focus trap + Esc + 关闭回焦），破坏性用 `role="alertdialog"` | 键盘：Tab 不出框、Esc 关闭、焦点回触发按钮 |
| UX-005 | major | 2 | 仍有 3 处原生 `window.confirm`：`ClusterDetailPage.vue:26`、`OperatorEnrollPage.vue:37,43` | 复用既有 dialog 模式 | 点击后断言出现自定义对话框 |
| UX-006 | major | 2 | `ReleaseInventoryPage.vue:56-60` 用 `onActivated`，但全仓无 `<KeepAlive>` ⇒ 5 分钟新鲜度刷新是**死代码** | 改 `onMounted` + 路由 `watch`，或包 `<KeepAlive>` | `grep -rn KeepAlive src` 零命中 |
| UX-007 | major | 6 | `useOperatorPolling.ts:43` `if (succeeded) schedule()` + `stores/operator.ts:41-53` 失败返回 `false` ⇒ 一次失败**永久停止**轮询且界面无提示 | 失败也退避重排（有上限），并暴露「最后更新时间/自动刷新状态」 | mock 一次失败，断言仍会重排 |
| UX-008 | major | 6 | `stores/valuesEditor.ts:45-46` 草稿 key 只有 `values_draft:<releaseId>`：无作用域、无时间戳、无 TTL、跨标签页互踩；`:136` `setItem` 无 try/catch（配额溢出即静默停写）；无「丢弃本地草稿」入口 | key 加 org/customer/cluster + 写入时间 + parentRevisionId；恢复时比对 parent；`setItem` 包 try/catch；提供丢弃入口 | 换 cluster/parent 后刷新，断言不静默采用旧草稿 |
| UX-010 | major | 1/2 | `AuditPage.vue:100-112` 的 ErrorState 与 Loading/Empty/Table 是两条并列 `v-if` ⇒ 错误与空态同屏（B5 已实测） | 收进同一条链；有数据时错误降级为页内 warning（`OperatorListPage.vue:88-90` 是好模式） | mock 失败，断言不同时出现空态文案 |
| UX-011 | major | 6 | `AuditPage.vue:66-74` deep watch(`form`) → 每次按键都 `router.replace`，无 debounce | 加 300ms debounce，或只在 submit 同步 URL | 输入 5 字符断言 `router.replace` ≤1 次 |
| UX-012 | major | 1/6 | `AuditPage.vue:100-106` 无 `ForbiddenState`，而 `stores/audit.ts:174-175` 已把 PermissionDenied 映射为 `reason:'permission_denied'` | 403 分支渲染 ForbiddenState | mock PermissionDenied 断言 403 文案 |
| UX-013 | major | 1/6 | `OperationDetailPage.vue:54-57` `catch {}` 吞掉 preflight 读取失败并置 `null`；`PreflightResultPanel.vue:11` `v-if="result"` ⇒ 准入证据读取失败时页面无痕迹 | 区分「尚未产生」与「读取失败」，失败给 ErrorState + 重试 | mock reject 断言有错误与重试 |
| UX-014 | major | 1/6 | `OperationDetailPage.vue:197-203` 无 Forbidden 分支，权限不足与瞬时失败同文案 | 按 `initialError.code` 分流 | mock PermissionDenied |
| UX-015 | major | 2/6 | 错误对象普遍不含 `requestId`（`connect/operation-api.ts:45-57`、`stores/operationTimeline.ts:19-23`、`utils/valuesErrors.ts:3-7`）⇒ 用户报障没有追踪号 | 错误映射带上 requestId 并在 `ErrorState` details 中可复制 | 错误分支断言 DOM 含 request id |
| UX-016 | major | 1 | 原始服务端消息直出（`stores/releaseInventory.ts:103`、`stores/audit.ts:198`、`utils/valuesErrors.ts:31` 的 `rawMessage`/`raw`）⇒ 中文界面里出现 `rpc error: …` 之类实现细节（B4 就是实例） | 默认给稳定文案，原始信息放折叠 details | mock 未知错误断言主文案稳定 |
| UX-020 | major | 4 | `TimelineEntryItem.vue:45` 每个 `<li>` 都 `tabindex="0"`，时间线上限 500 ⇒ 最多 500 个 tab 停靠点；列表无 `aria-live` | 去掉 li 的 tabindex；列表加 `aria-live="polite"` | Tab 遍历计数 |
| UX-021 | major | 4 | `DisableCustomerDialog.vue:17` 的 `confirmed` 只在 `cancel()` 复位 ⇒ 下次打开仍是勾选态，破坏性操作一键误确认 | `watch(() => props.open)` 复位，或状态移入 `v-if` 子组件 | 关闭后重开断言未勾选 |
| UX-022 | major | 3 | 打开就报错：`RevokeOperatorDialog.vue:17,34` 的 `violation` 由空 reason 直接算出 ⇒ 用户没动就被判「填错了」；`EmergencyChangeForm.vue:25,51` 同类 | 用 touched/submitted 门控（`CancelOperationDialog.vue:15,23` 已有模式） | 打开对话框断言无错误文案 |
| UX-026 | major | 3 | `OperationForm.vue:35` `<select required>` 的首个 option 是 `:value="null"` ⇒ 浏览器 required 不生效，两套校验并存 | 占位 option 用 `value=""`，或去掉 required 只留应用层校验 | 不选制品提交，断言被拦且来源明确 |
| UX-027 | major | 3/4 | 字段错误只有相邻 `<small>`，无 `aria-describedby`（`ClusterEditPage.vue:91`、`CustomerForm.vue:42,56`、`RouteRuleEditor.vue:72,98,109`、`OperationForm.vue:41,53,59`、`PatchOverrideEditor.vue:29,39`） | 错误元素加 id 并关联 | a11y 快照：invalid 字段有 description |
| UX-029 | major | 3 | `InitPage.vue:57` `minlength="12"` 但页面无密码规则提示 | 加 helper 文案 + 实时计数 | 文案检查 |
| UX-030 | major | 3 | `OperatorEnrollPage.vue:101-102` `min=0 max=1440` 与提示「0 uses 60min; otherwise 5–1440」及应用层规则（`utils/operator-validation.ts:17-19`）不一致 ⇒ 1–4 在 HTML 层合法、只有提交后才提示 | 输入约束与规则对齐 + 即时字段提示 | 填 3 断言即时提示 |
| UX-031 | major | 4 | `OperatorTable.vue:44` 全部 `th,td` `white-space: nowrap` ⇒ 长名称只能横向滚动 | 名称列 `overflow-wrap: anywhere` | 窄视口截图 |
| UX-032 | major | 5 | `ClusterDetailPage.vue:86` 三列摘要无 media query（同类 `OperationDetailPage.vue:320`、`ValuesEditorPage.vue:277` 都有） | 加窄屏单列 | 窄视口截图 |
| UX-034 | major | 4/5 | `ReleaseInventoryTable.vue:78-88` 用不可聚焦 `<span title=...>` 承载「未绑定 Definition」「紧急变更（阻断）」⇒ 阻断原因键盘/触屏不可达 | 改 `aria-disabled` 按钮或把原因写成可见文本 | 键盘/触屏检查 |
| UX-035 | major | 1 | `ConvergenceTasksPage.vue:38-47` LoadingState 只覆盖 `gate==='loading'`，`selection.load` 期间直接渲染空表体 | 为 `selection.loading` 加加载态 | 慢网络观察首屏 |
| UX-036 | major | 6 | `ConvergenceTasksPage.vue:55-63` `catch { parentVersion.value = 0n }` 吞错并用错误父版本发起 Prepare | 区分「无 revision」与「读取失败」，失败禁用 Prepare 并提示 | mock 失败断言按钮禁用 |
| UX-037 | major | 4/1 | `EmptyState.vue:20`、`ErrorState.vue:22`、`ForbiddenState.vue:20` 用固定 DOM id ⇒ 同页两个同类组件产生重复 id 与错误 `aria-labelledby` | 用 `useId()` 生成唯一 id | 同页渲染两个 EmptyState 断言 id 唯一 |
| UX-038 | major | 4/7 | `ValuesCodeEditor.vue:115` 只在宿主 div 上放 `aria-label`（CodeMirror 的 contenteditable 在内层）；`:64-86` 的 extensions 无 `history()`/`keymap` ⇒ **YAML 编辑器没有 Ctrl+Z** | 加 `history()` + `defaultKeymap`/`historyKeymap`，用 `EditorView.contentAttributes` 命名编辑器 | 编辑器内 Ctrl+Z 生效 |
| UX-039 | major | 4 | `EnrollmentTokenModal.vue:72` 无 Esc/初始焦点/焦点陷阱，`:71` backdrop 无 `@click.self`，唯一出口是勾选确认 ⇒ 未勾选时像卡死 | 加 Esc 或明确「稍后处理」出口 | Esc 行为实测 |
| UX-040 | major | 7/1 | `EmergencyAnnotationEditor.vue` 零引用（仅被自身 spec 引用），组件还向用户声明「后端契约暂不支持提交」 | 接线或连同 spec 删除 | `grep -rn EmergencyAnnotationEditor web/src` |
| UX-017/018/019/023 | major | 4 | 表格缺 `caption`/`scope`（`AuditEventTable.vue:43-52`、`OperatorTable.vue:22`、`ConvergenceTaskList.vue:29-38`）；可点行只有 Enter 无 Space 且 `outline: none`（`AuditEventTable.vue:55-62,139-142`）；详情面板无 Esc（`AuditEventDetail.vue:28-34`）；`EmergencyChangeForm.vue:39-52` 把计数器与错误包进 `<label>` ⇒ 可访问名随输入变化 | 补语义、补键盘、错误用 `aria-describedby` | a11y 快照 + 键盘走查 |
| UX-024/025/028 | major | 3/7 | `PatchOverrideEditor.vue:43` 无 index 的错误永不渲染；`:22` 用 `:key="index"` 于可增删列表；`RouteRuleEditor.vue:87-90` Provider 输入既不显示违规也无 `aria-invalid` | 无 index 时给表单级摘要；改用稳定 id 作 key；补 Provider 违规展示（**待核实**服务端是否会返回该字段） | 单测 + mock 违规 |
| UX-033/041/042/043/044 | major/minor | 2/5 | `EmergencyResultPanel.vue:100-102` 长 image ref 泄出网格；破坏性操作成功后无可见反馈（`ClusterDetailPage.vue:25-28` 等 4 处）；`OperationDetailPage.vue:115` 死模板 ref；`:295` 模板内写 `.then(...)`；`CancelOperationDialog.vue:37,43` 提交中仍可 Esc/点 backdrop 关闭 | 补 `overflow-wrap`；统一 toast；删死代码；抽 async 函数；关闭路径统一判 `submitting` | 视觉 + 单测 |
| UX-050/055/056/057 | minor | 4/6 | 骨架动画忽略 `prefers-reduced-motion`；`useSessionExpiry.ts:20-21` 无上限的 `setTimeout`（>24.8 天会被当 1ms **立刻登出**）；401 与定时到期两条过期路径并存（可能重复跳转或丢 returnUrl）；`ReleaseInventoryPage.vue:96` 把 MouseEvent 当 refresh 首参传入 | 加 media query；clamp 到 2^31-1；统一过期入口；改为 `@click="inventory.refresh()"` | 单测（30 天有效期不立即触发）+ 连点计数 |
| UX-058/059/060/061/063/064/069/073/074/075/076/080/081/084/085 | minor~nit | 1/2/4/5/7 | 长尾：`AuditFilters.vue:4` 对 `defineModel` 写嵌套属性（父层改只读即静默失效）；`<div aria-label>` 无 role；分页无页码；`ReleaseStatusBadge.vue:13` 未知状态显示为 `Active`；原始枚举直出；`HomePage.vue:18-24` 用 EmptyState 做欢迎提示且每次进入重现；窄屏表格只能横滚；多处 EmptyState/ForbiddenState 用英文默认文案；时间格式 `toLocaleString()` 与固定 `zh-CN` 混用；提交中按钮文案不变；只读用户无法切换 YAML/JSON；**无 i18n 框架**（非 gen 的 `.ts`/`.vue` 共 160 个文件，其中 68 个含中文字面量，同页中英混排）；`package.json` 声明 `js-yaml ^5.2.2` 而 `node_modules` 是 5.2.1 | 见下节「修复顺序」 | 逐项视觉/a11y 检查 |

### 4.2 前端门禁缺口（UX-001 是根因）

现状：仓库级 CI 只跑 Go 门禁；`web/` 的 `npm test` / `npm run lint` / `npm run build` / `npm run test:e2e`
全部靠本地人工执行，而 `npm run test:e2e` 在没有 `E2E_BACKEND=true` 时**整 suite skip**（skip ≠ 通过）。

**最小可行修复**：在 `.github/workflows/test.yml` 增加一个 `web` job
（`actions/setup-node` 锁 `web/package.json` 的 `engines` → `npm ci` → `npm run lint` → `npm test` → `npm run build`），
并把浏览器 E2E 作为**可选** job（需要真实栈，建议 nightly 或 `workflow_dispatch`）。
理由：B1/B4/B5 都是「没有任何自动化在浏览器里点过一次」的直接产物。

**顺带发现**：`web/dist` 是 2026-07-16 的陈旧产物（仅 4 个 chunk，当前 20 个页面），<!-- check-docs:ignore web/dist 构建产物目录，gitignore 掉，干净检出中本就不存在 -->
**不能**用作首屏体积证据；首屏体积需在沙箱内 `npm run build` 后重新测量。

## 5. 已经做对、不要回退的部分

- 路由**全部懒加载**（`router/index.ts` 所有 `component: () => import(...)`）；`v-html` 零使用；非 gen 代码 `any` 零使用。
- 生命周期清理普遍到位（`useOperatorPolling.ts:66-72`、`useEmergencyEffectObservation.ts:84-86`、`useSessionExpiry.ts:26`、`ValuesCodeEditor.vue:108-111` 等）。
- 幂等与防重复提交：`stores/operationForm.ts:192,197`（`submitting` 门 + 稳定 idempotency key）、`stores/valuesEditor.ts:259`。
- **部分失败专门呈现**（保留已有数据 + 页内 warning + 重试）：`OperatorListPage.vue:88-90`、`OperatorDetailPage.vue:73-75`、`ReleaseInventoryPage.vue:135-138`、`ValuesEditorPage.vue:170-173`。
- Secret 边界正确：`EnrollmentTokenModal.vue:66-67` 生成即显示、卸载即清空；`stores/operationForm.ts:82,236` 持久化前过滤 `isSecretPath` 的 LITERAL patch。
- 长文本处理有正确示范（`OperatorDetailPage.vue:137`、`OperationDetailPage.vue:319`、`AuditEventDetail.vue:133` 等）。

## 6. 建议修复顺序

| 优先级 | 内容 | 理由 |
| --- | --- | --- |
| **P0** | B1（认证接线 + dual-mode）、B4（快照凭证透传）、B3（流式 cookie 回退）、B5（audit 反代/dev 代理） | 这四条决定「控制台能不能用」；未修复前 REQ-033/056/057/059 的浏览器面均不成立 |
| **P0** | UX-001 前端 CI job | 否则上述问题会再次静默回归 |
| **P1** | B2（白屏兜底）、UX-002/003（准入与提交的假阳性）、UX-010（错误≠空结果） | 都是「把失败显示成正常」的高危假阳性 |
| **P1** | UX-004 统一对话框（含焦点管理）与 UX-005 去原生 confirm | 破坏性操作的安全底线 |
| **P2** | UX-006/007/008/011/013~016 等状态与反馈类；UX-028~040 表单/表格/a11y 类 | 体验与可访问性 |
| **P3** | i18n 框架与语种统一（UX-084/085）、性能与 chunk 划分测量 | 交付非中文客户的前提 |

## 7. 证据与复现

- 环境：`REGISTRY_PORT=5011 DEV_K3D_API_PORT=6449 make dev-up`（详见 `docs/user-manual.md` 第 2 与第 15 节）。
- 回归证据：`bash test/e2e/prerequisite/smoke.sh` → `44 pass / 0 fail`（含 Upgrade / Cancel / Rollback / auth 跨重启 token 校验）。
- 页面截图：`docs/images/user-manual/`（22 张，均为真实 dev 环境整页截图；其中 20 张取自打了出图补丁的环境，
  第 21 张 `21-login-unpatched-no-progress.png` 取自**回滚后的交付版本**，
  第 22 张 `22-blank-page-backend-down.png` 是 B2 的白屏留档）。
- 关键复现命令：

```bash
# B1：登录不下发 cookie、user 为空
curl -s -D - -o /dev/null -X POST -H 'Content-Type: application/json' \
  -d '{"username":"dev-admin","password":"<见 data/dev-credentials.env>"}' \
  http://127.0.0.1:8085/auth.v1.AuthService/Login | grep -ci '^set-cookie'   # 未打补丁时为 0

# B5：三个入口的对照
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Content-Type: application/json' -d '{}' \
  http://127.0.0.1:8088/audit.v1.AuditService/QueryAuditEvents   # 401（路由存在）
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Content-Type: application/json' -d '{}' \
  http://127.0.0.1:8087/audit.v1.AuditService/QueryAuditEvents   # 405（未被反代）

# B3/B4：服务端日志
KUBECONFIG=data/kubeconfig.yaml kubectl --context k3d-release-manager-control \
  -n release-manager-dev logs deploy/orchestrator --tail=20 | grep -E 'WatchOperation|snapshot pull'
```

> 事实源：`web/src/**`、`web/vite.config.ts`、`web/nginx.conf`、`web/package.json`、
> `.github/workflows/test.yml`、`docs/testing.md`、`cmd/auth/main.go`、`internal/auth/`、
> `internal/authorization/module.go`、`test/e2e/prerequisite/smoke.sh`、
> 以及两份静态清单 —— 87 条问题与 182 条 AC 映射，均带 `file:line` 且行号已做越界校验。
> 这两份清单**生成于 2026-09-25**（同一会话的前一阶段，早于本文档的浏览器实测），
> 全文归档在知识库 `Notes/Audit-2026-09-28/`；本文档 2026-09-28 修订时重新复算过其中的计数。

## 8. 独立审查与修订记录

本文档与配套手册在 2026-09-28 由**独立上下文**的审查员复核过（只读、未改文件、未停服务）。
审查要求「证伪」，结论为 **PASS-with-issues**：核心事实链（B1/B4/B5 复现、代码行号、182 条统计、
`smoke.sh` 44/0、`npm test` 49/323、worktree 补丁范围、`check-docs`）全部可独立复现，无 blocker 级造假；
但文档自述存在若干数字与描述缺陷。**已按下列清单修订**：

| # | 审查发现 | 修订 |
| --- | --- | --- |
| 1 | `user-manual.md` 的「集群编辑」小节误用集群列表截图，编辑页真实截图缺失 | **补拍真实编辑页** `23-cluster-edit.png`（含 `Cluster name` / `Enabled as release target` / `Add rule` / `Save cluster`），手册 §5 改为引用它 |
| 2 | `test.yml` 的 job 数写作 16，实测 **15**（13 处 setup-go） | 本文 §1 第 5 条与 UX-001 行均改为 15 / 13 |
| 3 | store 数写作 14，实测 **13**（非测试文件） | §0 表改为 13 |
| 4 | 「530 个 `file:line`」不可复算 | 删除该计数，改为「引用逐条查存在性与行号越界，0 处问题」 |
| 5 | 「68 / 143」的分母错，实测非 gen `.ts`/`.vue` = **160** | 改为 68 / 160 |
| 6 | B4 计数 9473 无测量时刻、随日志单调增长 | 改为「**9659 次 @ 2026-09-28T02:30:57Z**」，并说明 9473 是本文档首次测量时刻的值 |
| 7 | B2 的白屏留档取自 2026-09-25 的另一次走查，强度不足 | **2026-09-28 实测复现**（auth 临时缩到 0 → `502`、`#app` 为空、DOM 1 节点、未捕获 `ConnectError`）并重拍留档 |
| 8 | 支撑 87/182 的清单是 2026-09-25 产物，原文写作「本次子代理的」 | §0 与§7 明确标注生成日期与归档位置 |
| 9 | 交付物在审查期间被持续修改（每次「通过」只对某个 hash 成立） | 修订后冻结基线并记录 hash（见 TASK-169 的「冻结基线」） |
| 10 | `web/vite.config.ts:62` 应指 `:63` 的 target | 改为 `:62-63` 并注明 key/target |

修订后，其中 8 条由审查员的同一份复核口径确认（数字复算 + 实测），2 条由本次实测重做
（补拍 `23-cluster-edit.png`、重拍 `22-blank-page-backend-down.png`）。
未采纳的审查建议：无。


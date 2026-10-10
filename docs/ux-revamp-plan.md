# Release Manager 控制台 UI/UX 全面优化评估

> 本文以**完整产品设计文档**为唯一权威来源，重新评估 Web 控制台的 UI/UX：
> 哪些是设计文档已经规定、实现却没有做到的「优化项」，哪些是设计文档从未规定、
> 需要补设计决策的「补充项」。评估口径与证据强度见 §0，结论见 §1。

## 0. 方法与证据强度

### 0.1 语料（设计权威）

| 语料 | 规模 | 用途 |
| --- | --- | --- |
| `Requirements/`（83 篇；含本轮新增的 REQ-100） | 22 993 行 | 需求与 AC 权威；前端面 = `REQ-033` + `REQ-051~060` + `REQ-067/068` 共 13 篇（`REQ-007` 是索引） |
| `Notes/contracts/`（13 篇） | 124 KB | UI 硬约束（投影/脱敏、持久化禁令、分页容量、错误码、授权快照） |
| `Notes/decisions/`（71 篇，ADR-000~027 + D-000~） | 732 KB | 已裁决的必须/禁止项 |
| `Notes/CONTEXT.md` / `PROJECT-CONVENTIONS.md` | 68 KB / 23 KB | 术语与工程基线 |
| 仓库 `docs/architecture.md`、`docs/api.md`、`docs/glossary.md` | 1 015 行 | 系统边界、契约面、术语 |

### 0.2 判定口径

| 判定 | 含义 |
| --- | --- |
| **优化（optimize）** | 设计文档已经规定（有 AC 或规格原文），实现未达成或只达成一半 |
| **补充（supplement）** | 设计文档**未规定**该面（IA、设计系统、i18n、响应式、视觉规范），或对象/能力在后端已定义但控制台无面 → 需要先补设计决策或新面 |
| **合规（compliant）** | 设计文档规定且实现达成，列为回归基线 |

证据强度标注：**实测**（本次命令/浏览器可复现）· **代码核对**（`文件:行号` 双向证据）· **静态清单**（子代理产出，标注来源文件，未逐条浏览器复测）。

### 0.3 本次评估的来源文件（原始清单归档）

| 来源 | 内容 |
| --- | --- |
| 知识库 `Notes/Audit-2026-09-28/web-console-req-ac-map.md` | 182 条 UI AC ↔ 实现映射（2026-09-25 生成，本次复算） |
| 知识库 `Notes/Audit-2026-09-28/web-console-static-ux-audit.md` | 87 条界面问题（4 blocker / 36 major / 36 minor / 11 nit） |
| 本次评估的 **8 份**语料分析 | 产品与 IA、Web 页面需求全集与 251 条 AC、发布核心、治理与紧急、契约层、决策层、现状 UI 基线、8 个 web REQ 的 202 条规格↔实现审计（见 §14，原文归档在 `Notes/Audit-2026-09-28/corpus-1..8-*.md`） |
| `docs/ux-review.md` | 2026-09-28 的浏览器实测 blocker（B1–B6） |
| `docs/user-manual.md` + `docs/images/user-manual/`（22 张） | 现状界面的一手视觉证据 |

> ⚠️ **本次评估修正了一条既有结论**：`REQ-060`（通知任务与 dead-letter 前端）已于
> **2026-09-19 由用户裁定撤回**（`status: withdrawn`，撤回取证见该文 `变更记录`），
> 其 22 条 AC **不属于验收面**。因此「通知前端缺失」**不是缺陷**；
> 做前端需求面统计时必须显式剔除这 22 条。`docs/ux-review.md` 的 §3 已同步修正。

## 1. 结论摘要

1. **设计文档规定了大量界面行为，实现达成率低**：对 8 个 web REQ 的 **202 条可观测条款**逐条审计，
   仅 **133 条（66%）已实现**，**47 条部分（23%）**、**10 条缺失（5%）**、**12 条与规格相反（6%）**；
   其中 `REQ-056` 与 `REQ-058`（都是 `accepted`）分别有 4/2 条缺失。见 §5.3。
2. **设计文档从未规定信息架构、视觉规范、设计系统、i18n、响应式**——全文检索 `设计系统|design token|组件库|i18n|响应式|移动端|WCAG` 在 `Requirements/`、`Notes/contracts/`、`Notes/decisions/` 中**零命中**（§3）。这是「UI 差」的**第一性原因**：没有规范，就没有一致性。
3. **样式基础设施为零**：`web/src` 下**没有任何 `.css`/`.scss`**，无 `:root`，`web/src/main.ts` 不引入任何样式表，`index.html` 无内联样式；但代码里有 **46 处 `var(--color-*, 硬编码)` 引用、0 处变量定义**（§4.1）。表面「令牌化」，实际全部走 fallback。
4. **视觉无尺度**：**609 处硬编码色值 / 62 个不同色值**、**16** 个字号取值、7 种断点写法、69 个 `<style scoped>` 各自为政（§4.1–§4.3）。
5. **语种边界 = 开发波次边界**：逐文件分类（50 组件 + 20 页面）为纯英文 **26**、纯中文 **27**、同文件混排 **4**、无可见文案 **13**（早期功能用英文、后期功能用中文）；`index.html` 却硬编码 `lang="zh-CN"`；**无 i18n 框架**，3 504 个中文字符硬编码（§4.4）。
6. **可访问性有规格、无落地**：`REQ-057` 明文要求断线 banner `aria-live`、对话框焦点移入/归还、时间线键盘可达；实测 `<label for>` 仅 **2/49（4%）**、`:focus-visible` 仅 **2** 处、`prefers-reduced-motion` **0**、8 个对话框**7 个无焦点陷阱**且 `role="dialog" aria-modal="true"` 与行为不符（§5.4）。
7. **导航是断的，且核心业务子系统零外部入口**：`HomePage` 内 **0 个 RouterLink**（唯一动作是 Dismiss）；
   release 子树 6 条路由（ReleaseInventory / ValuesEditor / OperationCreate / OperationDetail /
   EmergencyChange / ConvergenceTasks —— 即发布操作 + 紧急变更 + 收敛 + Values 编辑）的**全部入边都在子树内部**
   （唯一分叉点是 `web/src/components/releases/ReleaseInventoryTable.vue`），
   天然父页面 `ClusterDetailPage` 的动作区只有 Operators / Edit / Disable cluster ⇒ **只能手输五段式 URL 到达**；
   `Clusters` 入口只在路由含 `:customerId` 时出现；`OperationDetail` **没有任何列表入口**（§5.1）。
8. **治理面整体缺失**：组织/成员/角色授予、org↔customer 绑定、本地用户、Trust Root 与轮换、漏洞准入、artifact 生命周期、ReleaseBundle/Candidate Artifact 浏览、ChangePassword —— 35 项存在性探测中 **14 项未命中**，其中组织治理 6 项全部未命中（§6.1）。
9. **契约层自身有漂移与自相矛盾**，会直接误导界面实现：13 篇 contracts 标 `last_verified: 2026-09-22@4f820b7` 而该 SHA 在本地任何 ref 均不存在；`ReleaseSummary.emergency_conflict` 在 proto 是 `bool` 而契约要求结构化对象；幂等键「契约说 header-only」vs「proto 与 REQ-058 D14=A 说 body」；`docs/api.md` 对审计查询字段的陈述与实现相反（§5.7、§12.3）。
10. **没有门禁就没有「优化完成」**：`.github/workflows/test.yml` 当时 15 个 job 中 0 处 npm/Node 步骤；这既是 B1–B6 长期未被发现的根因，也是本轮优化无法证明不回归的根因（§5.8）。
    > 2026-09-28 更新：`TASK-175` 已新增 `web` job（`actions/setup-node@v7` + `npm ci` + `lint` + `test` + `build`），该文件现为 16 个 job，G8 已落地。

## 2. 设计文档已经规定了什么（对 UI 有约束的部分）

| 维度 | 规定内容（要点） | 出处 |
| --- | --- | --- |
| 交付面 | 控制台 = 五页（客户管理 / Operator 管理 / Operation Timeline / 紧急变更 / 审计查询）+ 认证壳层 | `docs/architecture.md:157` |
| 前端域拆分 | 6 段交付：Shell `033` → Tenancy `051→052` → Operator `053` → Release `054→055→056→057` → Operations `058→059→060` | `Requirements/REQ-001-release-manager.md:60-66` |
| 错误处理 | 必须用 `ConnectError.findDetails(...)` 解码 typed error detail，**禁止解析自由文本 message** | `docs/architecture.md:100` |
| 错误码→文案→动作 | `REQ-056` 给出 10 个错误码的中文消息模板与必须动作（「查看进行中操作」/冲突对话框+刷新/行级高亮/toast+重试/403 不渲染按钮） | `Requirements/REQ-056-web-release-operation.md:438` |
| 时间线 | 12 条必须项：stream 状态、断线 banner 文案、500 条截断提示、取消按钮与状态摘要同屏、EMERGENCY_EFFECT_RESOLVED 用蓝色 info 不归 ERROR、首载失败不渲染空时间线也不显示断线 banner、`not_found`/`permission_denied` 不提供重试 | `Requirements/REQ-057-web-operation-timeline.md:250-263` |
| 可访问性（唯一成文处） | 断线 banner 用 `aria-live`；取消对话框打开时焦点移入首元素、关闭后归还触发按钮；时间线条目/取消/复制按钮可 Tab 可达 + Enter 触发 | `Requirements/REQ-057-web-operation-timeline.md:262` |
| 状态组件 | `LoadingState`/`EmptyState`/`ErrorState`/`ForbiddenState`：typed props + action slot/emits + accessibility role/aria | `Requirements/REQ-033-web-auth-shell.md:68,125` |
| 分页语义 | opaque cursor，`pageSize` 默认 50 上限 100；过滤变化重置 cursor；`invalid_cursor` 只重置对应列表、保留表单与已选 ID | `docs/architecture.md:104-105` |
| 幂等键 | `Idempotency-Key` 不写 URL / Web Storage / 日志；页面卸载不持久化；scope+key+hash 语义 | `docs/architecture.md:92-95` |
| 状态权威 | **Web 只消费 snapshot/Timeline，不推演状态**；Execute 响应只等事务接受 | `docs/architecture.md:134` |
| 持久化禁令 | 表单、reason、annotation values、artifact selection、confirmed intent、Idempotency-Key、Operation evidence、task list、prepareToken context、editableDocument、lockedPaths 均不得写入浏览器存储 | `Notes/contracts/web-console-surface.md` |
| 授权投影 | 只渲染服务端 sanitizer 后的 display projection；Web 不实现脱敏 | `Notes/contracts/web-console-surface.md` |
| 性能（唯一有数字处） | TTI p95 ≤ 1.5 s（Playwright 断言）、三列表 RPC 服务端 p95 ≤ 500 ms、Execute 接受 p95 ≤ 1 s、同 Definition 并发 Emergency intents ≤ 20、单 revision 绑定 tasks 1–50 | `Requirements/REQ-058-web-emergency-change.md:680-688` |
| 共享组件（**从未交付**） | `Drawer.vue`（右侧滑出、Teleport、Esc、遮罩）与 `Toast.vue`（success/error/warning、3s 自动消失），"供后续页面复用" | `Requirements/REQ-060-web-notification-jobs.md:319-330`（该 REQ 已撤回，但组件规格仍可复用） |

## 3. 设计文档没有规定什么（本次评估的空白清单）

| # | 空白 | 证据（命令/结果） | 影响 |
| --- | --- | --- | --- |
| 3.1 | **信息架构 / 顶层导航** | `Requirements/` 中只有 `REQ-060` 提到「侧边栏菜单项」（该 REQ 已撤回）；`decisions/` 无 IA/导航决策 | 现状导航是开发顺序的副产品（§5.1） |
| 3.2 | **设计系统 / 设计令牌 / 组件库** | 全语料 `设计系统\|design token\|组件库\|Element Plus\|Ant Design` 仅命中 `REQ-060` 的「不引入第三方 UI 组件库」，且 `web/package.json` 无任何 UI 框架依赖 | 无视觉基准，69 个 scoped 样式各自为政（§4） |
| 3.3 | **i18n / 语种** | 13 篇 web REQ + `decisions/` 全文 `i18n\|国际化\|多语言\|locale` **零命中** | 中英混排且无法统一（§4.4） |
| 3.4 | **响应式 / 目标视口** | 全语料 `响应式\|移动端\|断点\|mobile` **零命中**（唯一命中是无关的「（不）可访问」） | 7 种断点写法、部分页面无断点（§4.3） |
| 3.5 | **可访问性目标等级** | `decisions/` 全文 `WCAG\|a11y\|可访问` 零命中；仅 `REQ-057` 有 3 条具体条款 | 「优化完成」不可判定（§5.4） |
| 3.6 | **首屏与体积预算** | 除 `REQ-058` 的 TTI 外无任何体积/首屏预算 | 优化是否「变好」无基线（§10） |
| 3.7 | **界面文案规范** | 英文界面文案（`Sign in`/`Create customer`/`Generate token`/`Audit events`）在任何设计文档中**零命中**；中文文案（`请选择制品`/`实时更新已断开，正在重连…`）由 `REQ-056/057` 规定 | 文案由实现者自由发挥（§4.4） |
| 3.8 | **视觉质量基线** | 无对比度、密度、排版、图标、动效规范 | 92 次 `#fff`、50 次 `#2563eb` 等硬编码自治（§4.1） |

> 结论：**「全面优化」的第一步不是改代码，而是补齐 3.1–3.5 这五项设计决策**（每项都有一个建议默认值，见 §12）。

## 4. 现状基线（as-is，可复算）

命令均在 `web/src` 下执行（`--include=*.vue --include=*.ts`，排除 `gen/`）。本节数字为本次实测。

### 4.1 设计令牌与颜色

| 指标 | 值 | 命令 |
| --- | --- | --- |
| CSS 自定义属性**定义** | **0** | `grep -rhoP '^\s*--[a-z-]+:' --include=*.vue . \| sort -u \| wc -l` |
| `var(--…)` **引用** | **46** | `grep -rhoP 'var\(--[a-z-]+' --include=*.vue . \| wc -l` |
| 独立样式表 | **0**（无 `.css`/`.scss`，无 `:root`，`main.ts` 不 import 样式） | `find src -name '*.css' -o -name '*.scss'` |
| 硬编码色值 | **609 次 / 62 个不同值** | `grep -rhoP '#[0-9a-fA-F]{3,8}\b'` |
| 色值 TOP5 | `#fff` 92 · `#64748b` 60 · `#2563eb` 50 · `#b91c1c` 48 · `#cbd5e1` 43 | 同上 + `sort \| uniq -c \| sort -rn` |

> **这是最结构性的一条**：`var(--color-muted, #64748b)` 这类写法出现 13 次，但 `--color-muted`
> 从未被定义，浏览器永远取 fallback。界面看起来「有令牌」，实际没有；改主题/暗色/对比度都无处下手。

### 4.2 排版与间距

| 指标 | 值 |
| --- | --- |
| `font-size` 不同取值 | **16**（0.65 / 0.7 / 0.75 / 0.78 / 0.8 / 0.85 / 0.875 / 0.88 / 0.9 / 1 / 1.1 / 1.125 / 1.2 / 1.5 / 2 / 3 rem） |
| 相近但不同的字号 | 0.75 / 0.8 / 0.85 / 0.875 rem 共 95 处 → 无排版尺度 |
| `<style scoped>` 块 | **69**（71 个 `.vue` 中） |

### 4.3 响应式

| 指标 | 值 |
| --- | --- |
| `@media` 总数 / 覆盖文件 | 14 条 / 13 个文件（共 71 个 `.vue`；即 58 个文件完全没有断点） |
| 断点写法 | **7 种**：`48rem`×6、`56rem`×2、`42rem`×2、`52rem`、`64rem`、`72rem`、`720px`（px 与 rem 混用） |

### 4.4 语种与 i18n

| 指标 | 值 |
| --- | --- |
| i18n 框架 | **无**（`web/package.json` 无 `vue-i18n`/`@intlify/*`；`useI18n`/`$t(` 零命中） |
| 含中文字面量的文件 | **68 / 160**（非 `gen` 的 `.ts`/`.vue`）；中文字面量合计 **3 504 个字符** |
| 逐文件语种分类（50 组件 + 20 页面） | 纯英文 **26** / 纯中文 **27** / 同文件混排 **4** / 无可见文案 **13** |
| `index.html` | `<html lang="zh-CN">`（与实际混排不符） |

> 语言边界 = 开发波次边界：`cluster/customer/operator/audit`（2026-07~08）用英文，
> `release inventory/values/operations/emergency`（2026-09）用中文；**唯一混排的 4 个文件恰好是接缝**。
> 用户从 `ClusterDetailPage` 的 `Disable cluster` 跳到 `OperationCreatePage` 的「创建发布操作」时
> 不会觉得这是同一个产品。

### 4.5 可访问性

| 指标 | 值 | 说明 |
| --- | --- | --- |
| `aria-*` 出现 | 96 | 其中 `aria-label` 34 / `aria-labelledby` 20 / `aria-hidden` 20 |
| `role=` 出现 | 56 | `alert` 27 / `status` 14 / `dialog` 8 / `presentation` 5 |
| `<label>` / 带 `for=` | **49 / 2（4%）** | 55 个表单控件 |
| `aria-invalid` | 10（/55 控件） | 字段错误与输入框无 ARIA 关联 |
| `aria-describedby` | **2** | 表单帮助文本基本未关联 |
| `:focus-visible` / `:focus` | **2 / 0** | 93 个 `<button>` 依赖浏览器默认 outline |
| `prefers-reduced-motion` | **0** | 但有 3 个 `@keyframes` |
| `<Teleport>` | **0** | 8 个对话框全部内联渲染 |

### 4.6 组件与结构

| 指标 | 值 |
| --- | --- |
| 路由 | **22 条**路由记录 / **20** 个页面文件（两个页面各服务两条路由） |
| 重复实现 | Dialog **8 份**（3 种开关协议、4 种 emit 词汇、a11y 行为不一致）、时间戳格式化 **9 份** + 1 util、状态徽章 **5 套**、`.primary` 按钮样式 ≥10 份、`.eyebrow` **16 处**、面包屑 **6 份** / 3 套 CSS |
| 状态组件复用 | LoadingState **13/20** 页、ErrorState **16/20**、ForbiddenState **11/20**、EmptyState **8/20**、Skeleton **2/20**；**4 个状态组件与 31 处手写变体并行** |
| CSS 重复度 | 样式声明 446 行 → 去重后 165（**重复率 63%**），仅 20 条被 ≥5 个文件共享 |
| 交互反馈语言 | 全仓 `:hover` **1 处**、`transition` **0 处**、无全局 toast |
| UI 框架依赖 | **无**（deps 仅 vue/pinia/vue-router/connect/codemirror/deep-diff/js-yaml/protobuf） |

## 5. 差距与问题（按维度）

### 5.1 信息架构与导航（最严重）

| # | 问题 | 证据 |
| --- | --- | --- |
| N1 | `HomePage` 是**死胡同**：全文 0 个 `RouterLink`，唯一动作是 `Dismiss`（欢迎空态） | `web/src/pages/HomePage.vue:18-24` |
| N2 | `Clusters` 入口依赖当前路由参数，`/`、`/customers`、`/customers/:id`、`/audit` 四处**都没有集群入口** | `web/src/components/common/AppShell.vue:15,40` |
| N3 | `Audit` 入口**无权限门禁**（同 header 其余入口都有 `canXxx` 判断）→ 「先给入口再 403」 | `web/src/components/common/AppShell.vue:41` |
| N4 | 无写权限访问 `/clusters/new`、`/clusters/:id/edit` 时**静默重定向**到列表页，无任何说明；同类问题 `requiresOperationCreate` 却跳 403 | `web/src/router/index.ts:183-185` vs `:220-222` |
| N5 | **Operation 详情没有列表入口**：只能由创建流程跳入，之后无法回到该操作 | `web/src/router/index.ts`（无 OperationList 路由） |
| N6 | `ReleaseInventory`、`ValuesEditor`、`OperationCreate`、`EmergencyChange` 均为**单入口**页面（仅 Release 表格行内链接） | `web/src/components/releases/ReleaseInventoryTable.vue` |
| N7 | 面包屑用 **原始 customer UUID** 当层级名（截图 `09-release-inventory.png`） | `docs/images/user-manual/09-release-inventory.png` |
| N8 | **release 子树零外部入口**：6 条核心业务路由的全部入边都在子树内部，唯一分叉点是发布清单表格；`ClusterDetailPage` 的动作区只有 Operators/Edit/Disable cluster | `web/src/components/releases/ReleaseInventoryTable.vue`、`web/src/pages/ClusterDetailPage.vue` |
| N9 | `AppShell.vue` 的 `<slot name="nav">` **零使用点**（死插槽，设计上本想给页面注入额外导航） | `web/src/components/common/AppShell.vue:42` |
| N10 | `ClusterDetailPage` 未提供 Releases 入口，用户从集群到发布清单要绕回客户 → 集群 → 手输 URL | 同 N8 |

### 5.2 页面级信息密度与工作流（视觉实测）

| # | 问题 | 证据 |
| --- | --- | --- |
| P1 | 首屏 85% 空白：运维平台落地页不给「待我处理」信息（进行中 Operation、UNKNOWN 效果、离线 Operator、待审批 revision 全部不可见） | `docs/images/user-manual/03-home.png` |
| P2 | 客户列表是无搜索/筛选/排序的卡片流；卡片标题折行、`ACTIVE` 徽章脱离标题行、`View` 与卡片标题重复 | `docs/images/user-manual/04-customers.png` |
| P3 | 发布清单表格：64 位 digest 直接换行占两行、release 名折行、中英表头混排、未绑定行用灰色文本充当禁用态 | `docs/images/user-manual/09-release-inventory.png` |
| P4 | 创建操作表单是窄列漂浮在大画布；`已审批 ValuesRevision ID` 要**手输 UUID**（规格说的是「选择已审批配置」）；三步流程（填写→确认→Preflight）无步骤指示 | `docs/images/user-manual/11-operation-create.png`、`Requirements/REQ-056-web-release-operation.md:405` |
| P5 | Preflight 面板把 stage detail 当**原始 JSON 直出**，整页宽 2 121 px > 视口 1 440 px（横向滚动） | `docs/images/user-manual/17-operation-detail-upgrade.png`、`docs/ux-review.md` B6 |
| P6 | 列表**没有分页 UI 控件**（`Pagination`/`pageSize` 只出现在 store、connect 客户端与 3 个测试文件共 7 个非 gen 文件里，**没有任何分页组件**），而契约要求 cursor 分页语义 | `docs/architecture.md:104-105` |

### 5.3 规格符合度（设计已规定、实现未达成）

**逐条审计结果（8 个 web REQ，202 条可观测条款；来源见 §14）**：

| REQ | 已实现 | 部分 | 缺失 | 不符 |
| --- | --- | --- | --- | --- |
| REQ-056 发布操作与 Preflight | 16 | 8 | 4 | 2 |
| REQ-057 状态时间线与取消 | 34 | 4 | 1 | 1 |
| REQ-058 紧急变更与收敛 | 21 | 11 | 2 | 2（+1 待核实） |
| REQ-053 Operator enrollment | 18 | 3 | 0 | 1 |
| REQ-055 ValuesRevision | 13 | 7 | 2 | 2 |
| REQ-054 Release inventory | 11 | 4 | 0 | 1 |
| REQ-059 审计查询 | 7 | 8 | 1 | 2 |
| REQ-033 认证与应用壳层 | 13 | 2 | 0 | 1（+1 待核实） |
| **合计** | **133（66%）** | **47（23%）** | **10（5%）** | **12（6%）** |

**Top 5 缺失/严重不符**（每条都是「用户可感知」级别）：

| # | 问题 | 证据 |
| --- | --- | --- |
| T1 | **kill switch 关闭后，发布清单仍渲染可点击的「紧急变更」入口**（点进去 404）——入口只判 `canExecuteEmergency`，未与 `featureEnabled` 合取 | `web/src/pages/ReleaseInventoryPage.vue:150`；违 `REQ-054`/`AC-033-06` |
| T2 | **紧急变更新建流程当前不可提交**：容器列表恒空 ⇒ `canConfirm` 恒 false；且无 replicas 输入、`EmergencyAnnotationEditor` 无页面挂载 —— **三条变更路径全断** | `web/src/pages/EmergencyChangePage.vue:146-177`、`web/src/stores/emergencyChange.ts:170-177` |
| T3 | `revision_conflict` **无冲突对话框、无「刷新页面」按钮**（`grep -rn "刷新页面" web/src` 零命中，且不在 retryable 集合 ⇒ 创建页渲染不出任何动作） | `web/src/connect/operation-api.ts:202-206`、`web/src/pages/OperationCreatePage.vue:113` |
| T4 | 紧急变更**错误详情丢弃** `violations`/`existingOperationIds`/`convergenceTaskIds`/`refreshRequired` ⇒ 字段级定位与关联跳转全部缺失 | `web/src/features/emergency/errors.ts:8-14` |
| T5 | **ROLLBACK 表单与规格相反**且无可提交路径（仍渲染 `bundleId`、无 `target_revision`；提交仍走只接受 INSTALL/UPGRADE 的 `CreateOperation`） | `web/src/components/operations/OperationForm.vue:9,56-67`、`web/src/stores/operationForm.ts:199-207` |

**其余需注意的不符**：`REQ-055` 的 Reject 原因被实现为「可选」且不校验（与决策 D-20 相反）；
`REQ-058` 把 `idempotency_conflict` 放进 `KEY_INVALIDATING_CODES`（与 `AC-058-16`「不自动换 key」相反）；
`REQ-059` 审计页整页英文、无 toast、导出失败无面板内重试；`REQ-057` 的 stream 三态
（connecting/connected/disconnected）在界面上完全不可见。

**本表之外仍需定向核对的项**：

| # | 规格 | 现状 |
| --- | --- | --- |
| S1 | Preflight 未启用阶段应为 `StageSkipped`（ADR-025），fail-closed（ADR-008） | `PreflightResultPanel.vue` 把**任何非 `failed` 的 overall 显示为「通过」**；而 `StageSkipped` 服务端**已实现**（`internal/orchestrator/preflight/result.go`） |
| S2 | 错误胶囊必须经 typed detail 解码、禁止解析自由文本 | B4 把内部 `invalid token … token is expired` 原样显示给用户（`docs/ux-review.md`） |
| S9 | **Preflight 应展示「逐层检查结果」**：`REQ-045/046/047/048` 规定 resolved URI、digest parity、signature、逐资源 accepted/rejected、逐镜像 pulled/node | 前端只拿到 `{stage,status,detail}`：`StageResult` 只有 3 字段（`internal/orchestrator/preflight/result.go:15-20`），coordinator 把 operator 返回的 JSON 反序列化进该结构时**丢弃其余字段**（`internal/orchestrator/preflight/coordinator.go:561-575`）⇒ 证据在**传输层就没了** |
| S10 | 回滚点应可发现 | 无操作历史列表（路由只有 Create/Detail）⇒ 选不出回滚目标；`CreateOperation` proto 注释明写 "INSTALL or UPGRADE only" |
| S11 | 「变了什么 / 回滚是否成功 / rollout 是否就绪」应在界面可见 | `operation_execution_results`、`rollout_trackings`、`verification_result` 三张证据表**只写不读**（非测试调用方为零），proto 亦无 `UpgradeResult` |
| S7 | 审计导出应可闭环 | `ExportAuditEvents` 只登记 `audit_exports` 行，**全仓无消费者** ⇒ 导出永远 `pending` |

### 5.4 可访问性（有规格、无落地）

见 §4.5 数字；核心三条：**字段错误无 ARIA 关联**（`aria-describedby` 仅 2）、
**焦点可见性近零**（`:focus-visible` 仅 2、`:focus` 0）、**对话框声明与行为不符**
（`role="dialog" aria-modal="true"` 已声明，但 7/8 无焦点陷阱、Tab 可达背景内容）。

### 5.5 状态与反馈

| # | 问题 |
| --- | --- |
| F1 | `Preflight` 读取失败被 `catch {}` 吞掉 → 页面无任何痕迹（准入证据静默消失） |
| F2 | 审计页在错误时**同时**渲染「请求失败」与「没有事件」，把故障解释成空结果 |
| F3 | 破坏性操作（禁用客户/集群、撤销 Operator）成功后无可见反馈 |
| F4 | 原生 `window.confirm` 仍有 3 处（集群禁用、令牌替换/吊销） |
| F5 | 错误对象普遍不带 `requestId`（用户报障无法提供追踪号） |
| F6 | Operator 轮询一旦失败即**永久停止**且界面无提示 |

### 5.6 治理面缺失（详见 §6.1）

组织/成员/角色授予/org↔customer 绑定/本地用户/外部身份/Trust Root/漏洞准入/artifact 生命周期/
Bundle 与 Candidate Artifact 浏览/ChangePassword —— 35 项探测 **14 项未命中**。

### 5.7 契约与决策层的漂移（会误导界面实现）

| # | 漂移 | 证据 |
| --- | --- | --- |
| C1 | 13 篇 contracts 标 `last_verified: 2026-09-22@4f820b7`，该 SHA 在本地任何 ref 均不存在 | `git cat-file -t 4f820b7` → `Not a valid object name` |
| C2 | `ReleaseSummary.emergency_conflict` proto 是 `bool`、`revert_status_summary` 是 `string`，契约要求结构化对象 | `api/proto/orchestrator/v1/orchestrator.proto:943,945` |
| C3 | 幂等键位置三方不一致：`connect-surface.md`/`emergency-execution.md` 说 header-only；proto 与 `REQ-058` D14=A 说 body（`idempotency_key = 8`、`request_hash = 9`） | `api/proto/orchestrator/v1/orchestrator.proto`（ExecuteEmergencyChangeRequest）、`Requirements/REQ-058-web-emergency-change.md` |
| C4 | `ListEmergencyTargetsResponse` 只有 `targets`，无契约要求的 availability/cursor | `api/proto/orchestrator/v1/orchestrator.proto:1629-1631` |
| C5 | `docs/api.md` 关于审计查询字段的陈述与实现相反 | `docs/api.md`（审计章节）vs `internal/audit/audit_service_handler.go:228-259` |
| C6 | 契约说「`QueryAuditEvents` 只回 4 字段」→ 据此会误判审计页不可用 | 同上 |
| C7 | `contracts/convergence-flow.md` 仍写 cursor 分页，与 `REQ-058` D18=C「前端本地分页」相反 | 静态清单（`Requirements/REQ-058` 决策记录） |

### 5.8 前端门禁缺口

`.github/workflows/test.yml` 共 **15** 个 job（13 处 `actions/setup-go`），**0 处 npm/Node 步骤**；
`vue-tsc`、`eslint`、`vitest`、`playwright` 均不在 CI 内。`npm test` 本地全绿（2026-09-28 复跑：49 files / 323 tests），
但无人保证它们被跑过。

## 6. 必须补充（SUPPLEMENT）

### 6.1 缺失的功能面（后端/领域已有，控制台无面）

| # | 面 | 设计依据 | 现状探测 |
| --- | --- | --- | --- |
| A1 | 组织与成员管理（创建/改名/禁用、成员与角色） | `REQ-026` 定义 4 个成员 RPC 与 `CanGrant` 门禁 | `AddMember\|ListMembers\|UpdateMemberRole` 0 命中 |
| A2 | org↔customer 授权绑定 | `REQ-049` | `OrganizationCustomerBinding` 0 命中 |
| A3 | Capability Grant（含 `emergency_resolver` 授予） | `REQ-027` | `capability_grant` 0 命中 |
| A4 | 本地用户管理 / 改密 | `REQ-025`（`CreateLocalUser`/`ChangePassword`） | 0 命中（会话安全相关缺口） |
| A5 | Trust Root / Trust Policy / 轮换 | `REQ-012`/`REQ-043`（6 个 RPC、platform_admin-only、grace→retired→revoked） | `TrustRoot` 0 命中 |
| A6 | 漏洞准入与例外 | `REQ-042`（production fail-closed） | `Vulnerability` 0 命中 |
| A7 | Artifact 生命周期（GC / 归档 Bundle） | `REQ-069` | 0 命中 |
| A8 | ReleaseBundle / Candidate Artifact 浏览 | `docs/architecture.md:3`（四大发布输入之一） | 仅作为操作表单下拉项 |
| A9 | ReleaseDefinition 管理与 Promotion Mapping 配置 | `REQ-040`、收敛前提（Promotion Mapping） | 无定义列表/编辑页；Only 展示 |
| A10 | 紧急变更 stuck-lock 正式处置 | `REQ-087`（`ListStuckLocks`/`ReleaseEmergencyLock`；该文自陈 UI 归「后续 REQ」，该 REQ 不存在） | 0 命中 |
| A11 | **操作历史 / 回滚点** | `REQ-056 AC-056-08`（ROLLBACK 必填 `target_revision`，走独立 `RollbackRelease`） | 无操作列表、无 rollback UI（`RollbackRelease` 非 gen 零命中） |

### 6.2 缺失的共享基元（设计文档点名、从未交付）

| # | 基元 | 依据 | 现状 |
| --- | --- | --- | --- |
| B1 | `Drawer.vue`（右侧滑出、Teleport、Esc、遮罩） | `Requirements/REQ-060-web-notification-jobs.md:321-323` | 不存在 |
| B2 | `Toast.vue`（success/error/warning、3s 自动消失） | 同上 `:324-326` | 不存在（值编辑器有一个局部 toast 机制） |
| B3 | 统一对话框（Teleport + 焦点陷阱 + Esc + 回焦） | 由 `REQ-057:262` 的焦点要求推得 | 8 份各自实现，7 份无焦点管理 |
| B4 | 数据表格（排序/分页/列配置/空态一致） | 由 `architecture.md:104-105` 的 cursor 语义推得 | 5 套表格各自实现，无分页 |
| B5 | 表单字段（label + 错误关联 + 帮助文本） | 由 `REQ-033 AC-033-03` 的 role/aria 要求推得 | 55 个控件、`aria-describedby` 仅 2 |

### 6.3 需要补写规范（本次建议，设计文档未规定）

1. **信息架构规范**（顶层导航 + 对象层级 + 跨对象跳转）——否则导航永远是开发顺序的副产品。
2. **设计系统规范**（令牌、排版尺度、间距尺度、色彩语义含状态色、密度、图标）。
3. **状态与反馈规范**（loading/empty/error/forbidden/partial-failure/toast/inline/字段错误）。
4. **文案与语种规范**（单一语种 + 术语表 + 错误文案模板）。
5. **可访问性基线**（目标等级 + 可机检断言）。
6. **响应式基线**（目标视口 + 最小保证）。
7. **前端性能预算**（首屏体积、TTI）。

## 7. 必须优化（OPTIMIZE，按维度）

| 维度 | 条目（摘要） | 依据 | 量级 |
| --- | --- | --- | --- |
| 状态正确性 | Preflight 三态（含 `StageSkipped`）、Submit 受未保存约束、审计错误≠空结果、preflight 读取失败可见 | `docs/ux-review.md` UX-002/003/010/013 | S |
| 错误呈现 | typed detail 解码 + 稳定文案 + `requestId` + 折叠原文（B4 是反例） | `docs/architecture.md:100`、UX-015/016 | S |
| 可访问性 | 3 条基线：键盘可达、名称-角色-值、焦点管理 | `REQ-057:262`、UX-004/005/020/027/037 | M |
| 反馈闭环 | 破坏性操作确认/成功反馈、去原生 confirm、轮询失败可见 | UX-005/041/007 | M |
| 表格与密度 | 统一表格 + 分页 + 列宽策略 + 长值截断/复制 | §5.2 P3/P6 | M |
| 表单 | 字段级错误关联、blur 校验、提交后聚焦首个错误、去掉手输 UUID | §5.2 P4 | M |
| 导航 | 修复 4 条断层（Home 死胡同、Clusters 条件入口、Audit 无门禁、静默重定向） | §5.1 N1–N4 | S |
| 导航（大） | 新增 Operation 中心（跨 Release 的 Operation 列表/筛选）；ReleaseDefinition 与治理面入口 | §6.1 | L |
| 视觉 | 令牌化 + 全局样式基线 + 排版/间距/色彩尺度 + 删重复样式 | §4.1–§4.3 | L |
| 语种 | 中英混排收敛为单一语种 + 文案集中化 | §4.4 | M |
| 契约 | 修复 C1–C7 漂移（契约重锚、字段形状对齐、幂等键载体统一、审计文档纠错） | §5.7 | M |
| 门禁 | 前端 CI job（lint/test/build）+ 可选浏览器 E2E | §5.8 | S |

## 8. 目标信息架构提案（文档未规定，本次建议）

现状导航 = `Customers` / `Clusters`（条件） / `Audit` + 品牌回首页，其余靠深层链接。
提案按「对象 + 工作流」重构为四区（**保留现有 20 条路由为兼容层，新增区为聚合视图**）：

| 区 | 内容 | 依据 |
| --- | --- | --- |
| **① 工作台** `/` | 待我处理：非终态 Operation、`effect=UNKNOWN`、离线/suspect Operator、待审批 ValuesRevision、失败通知、最近发布活动 | 对象与状态在 `Notes/CONTEXT.md` 已定义；**首页内容文档未规定（本次建议）** |
| **② 发布** `/operations`、`/releases` | Operation 中心（跨客户/集群筛选，按状态/时间/Release）、Release 总表、创建操作入口 | `REQ-023/077` 的 Operation 是一等对象；**中心列表页文档未规定（本次建议）** |
| **③ 资源** `/customers` → 客户 → 集群 → { Operators / Routes / ReleaseDefinitions / Releases } | 现有层级保留，补 ReleaseDefinition 与 Promotion Mapping 页 | `REQ-007:22-27`、`REQ-040` |
| **④ 治理** `/governance/*` | Organization & Members、Bindings、Capability Grants、Trust Roots、Vulnerability Policy、Artifacts（Bundles）、Audit | `REQ-025/026/027/042/043/049/069`、`docs/architecture.md:157` |

跨对象跳转按文档语义补齐：Release ↔ Operation ↔ ValuesRevision ↔ ConvergenceTask 双向可达；
Audit 事件可跳回其 `resource_id` 对应对象（`AuditEvent.resource_type/resource_id` 已具备）。

## 9. 设计系统提案（文档未规定，本次建议）

### 9.1 令牌层（先做最小可用的 3 组）

| 组 | 令牌（示例） | 理由 |
| --- | --- | --- |
| 色彩语义 | `--color-bg` / `--surface` / `--border` / `--text` / `--text-muted` / `--primary` / `--danger` / `--warning` / `--success` / `--info` | 现状 62 个硬编码色值，其中 `#fff`/`#64748b`/`#2563eb`/`#b91c1c` 已事实上充当语义色 |
| 排版尺度 | `--font-size-xs/sm/md/lg/xl`（5 级） | 现状 **16** 个取值，且 0.75/0.8/0.85/0.875 四个「差不多」的字号共占 95 处 |
| 间距与圆角 | `--space-1..6`、`--radius-sm/md/lg` | 现状 padding/gap/radius 取值分散 |

**状态色必须成对定义（前景+背景+边框）**，因为状态徽章现在有 5 套实现、
且 `ReleaseStatusBadge` 把未知状态显示为绿色 `Active`（UX-061）。

### 9.2 组件优先级（按「被重复实现次数 × 影响页面数」排序）

| 优先级 | 组件 | 替换对象 |
| --- | --- | --- |
| P0 | `AppDialog`（Teleport + backdrop + 焦点陷阱 + Esc + 回焦 + `alertdialog` 变体） | 8 份对话框实现 |
| P0 | `DataTable`（列定义、排序、cursor 分页、空/错/加载态、行操作） | 5 套表格 |
| P0 | `FormField`（label + `aria-describedby` + `aria-invalid` + 帮助文本 + 字段错误） | 55 个控件 |
| P1 | `Toast` / `Drawer` | 设计文档点名、从未交付（§6.2） |
| P1 | `AppButton`（variant/size/pending 态） | `.primary` ≥10 份 |
| P1 | `StatusBadge`（枚举→语义色，未知=中性） | 5 套徽章 |
| P2 | `PageHeader`（标题 + 面包屑 + 动作区） | 6 份面包屑 / 各页手搓骨架 |
| P2 | `Timestamp`（统一 locale 与相对/绝对） | 9 份格式化实现 |

## 10. 度量与验收基线

| 类别 | 指标 | 目标（本次建议；无文档依据者为建议值） |
| --- | --- | --- |
| 可访问性 | 键盘可达：全部交互元素 Tab 可达 + Enter/Space 触发 | 100%（`REQ-057:262` 已要求时间线，本建议扩到全站） |
| 可访问性 | 名称-角色-值：`<label for>` 或等价关联覆盖表单控件；字段错误 `aria-describedby` | ≥ 95% 控件 |
| 可访问性 | 焦点管理：对话框焦点陷阱 + 关闭回焦 | 100% 对话框 |
| 可访问性 | `prefers-reduced-motion` 兜底 | 全部动画 |
| 视觉 | 硬编码色值 | 从 609 → 0（只允许令牌） |
| 视觉 | `font-size` 取值 | 16 → ≤5 |
| 视觉 | 断点写法 | 7 → 1 套（建议 3 档） |
| 文案 | 语种一致性 | 单语种 100%（或 i18n 覆盖 100% 词条） |
| 性能 | TTI p95（路由进入→表单可操作） | ≤ 1.5 s（`REQ-058:680`，已成文） |
| 性能 | 首屏 JS（gzip） | **先测量后定**（当前无基线） |
| 一致性 | 重复实现 | Dialog 8→1、Timestamp 9→1、Badge 5→1、Button 1；CSS 重复率 63%→低 |
| 一致性 | 状态组件覆盖 | Loading/Error/Forbidden/Empty 在 20 页的覆盖率 → 100%（消除 31 处手写变体） |
| 一致性 | 交互反馈 | `:hover` 1→全部可交互元素；`transition` 0→统一动效令牌（含 reduced-motion 兜底） |
| 门禁 | 前端 CI | lint + test + build 必过；浏览器 E2E 可选 job |

## 11. 波次计划

| 波次 | 内容 | 前置 | 可独立验收 |
| --- | --- | --- | --- |
| **W0 治理** | 把 87 条问题与 182 条 AC 映射接到 REQ/TASK 载体；补齐 §12 的裁决；前端 CI job | 用户裁决 G2/G3/G5/G6 | ✅（CI 绿 + 卡片存在） |
| **W1 可用性修复** | B1–B6（认证接线、白屏兜底、流式 cookie、写授权凭证、审计入口、Preflight 溢出）+ UX-002/003/010 | W0 | ✅（浏览器可登录并完成一次发布操作） |
| **W2 基元与令牌** | §9.1 令牌 + §9.2 P0 组件 + 全局样式基线；替换 8 对话框 / 5 表格 / 表单字段 | W1 | ✅（令牌 0 硬编码色值、a11y 三条断言） |
| **W3 导航与工作台** | §8 四区 IA + 修复 4 条断层 + Operation 中心 + 首页待办 | W2 | ✅（每条路由有 ≥2 个入口） |
| **W4 治理面补齐** | §6.1 A1–A10（按裁决顺序，需对应后端 RPC 就绪） | W2 + 后端 | ✅（逐面 AC） |
| **W5 语种与文案** | 语种收敛 + 文案集中化 + 错误文案模板 | W0(G5) | ✅（混排归零） |

## 12. 需裁决项（阻塞开工，各带建议默认值）

| # | 裁决 | 为什么必须先定 | 建议默认值 |
| --- | --- | --- | --- |
| G1 | 浏览器草稿持久化白名单 | 现有 `sessionStorage`/`localStorage` 用法与契约禁令措辞冲突 | 允许**非敏感** values 文本草稿；禁 SecretRef 明文/reason/locked values/display projection；key 加作用域+时间+parentRevisionId+TTL，`setItem` 包 try/catch，提供「丢弃草稿」 |
| G2 | 设计系统 / 是否引入组件库 | 决定 100+ 条 UX 问题的修法 | **不引入第三方 UI 框架**，仓库内抽原语（已有 `EmptyState` 等先例） |
| G3 | 可访问性目标等级 | 否则「优化完成」不可判定 | WCAG 2.2 AA 的 3 条子集（键盘可达 / 名称-角色-值 / 焦点管理）作为 P1 门禁 |
| G4 | `v-html` 与原始错误直出禁令是否成文 | 防止优化过程中重新引入 | 成文：禁 `v-html`；错误主文案稳定，原文折叠 + `requestId` |
| G5 | i18n 策略 | 动文案前不定就要写两遍 | 本轮不引入 i18n 框架，收敛为单一语种 + 文案集中化，预留接口。**目标语种已裁定（2026-09-28，用户）：中文**；共享文案目录与「目录必须为中文」门禁已就位，收敛按波次推进 |
| G6 | 响应式目标视口 | 决定「窄屏算不算合格」 | 桌面优先 1440×900；≥1280 保证；1024–1280 不横向溢出；<1024 不保证 |
| G7 | IA 是否本轮重构 | 跨多张 web REQ 的 AC，风险最大 | 本轮做 §8 的**新增聚合区**（不动现有 20 条路由语义）；全量重构另立 REQ |
| G8 | 前端是否纳入 CI | 没有门禁无法证明不回归 | 加 `web` job（setup-node + npm ci + lint + test + build） |
| G9 | 性能预算 | 无基线则不可判定 | 先只测量记录，不设硬阈值 |
| G10 | `target_changed` 死显示键 | UI 重写错误文案表会碰到 | 按既有建议删死键 + 改写 AC 对齐 `operation_version` |
| G11 | **契约层重锚**（C1–C7） | 照契约实现会引用不存在的字段/错误载体 | 由契约 owner 一次性重锚 `last_verified`、对齐 proto 形状、统一幂等键载体、修正审计文档陈述 |
| G12 | 是否补通知/ dead-letter 面 | 已撤回但仍是能力空白 | 暂不补；如要补，需新 REQ（含后端 service/表/迁移） |
| G13 | **Preflight 逐层证据是否补回传输契约** | 现状 `StageResult` 只承载 3 字段，`REQ-045/046/047/048` 要求的 resolved URI / digest parity / signature / 逐资源 / 逐镜像明细在 coordinator 反序列化时被丢弃（§5.3 S9） | 需要后端把证据字段纳入 stage 结果契约，否则界面永远只能显示三字段；建议先由契约 owner 评估字段形状（与 §5.7 C2 的重锚一并做） |

## 13. 风险与冲突

1. **与 ADR-000 的原子交付冲突**：「全面优化」跨 20 页面/50 组件，必须拆成原子 REQ（维护类可挂 REQ-008）。
2. **与前端禁令冲突**：乐观更新、客户端队列重试、前端推演状态均被既有决策禁止（D-005/D-006/D-012）；
   允许的形态只有「服务端确认后的展示层优化」（骨架屏、`aria-live`、禁用态文案）。
3. **与 ADR-002 冲突**：不得为实时体验新增 SSE/WebSocket，只能走 Connect 流。
4. **Preflight 修复不需新决策**：按 ADR-008（fail-closed）与 ADR-025（`StageSkipped`）直接改。
5. **后端依赖**：§6.1 的治理面多数需要对应 RPC 就绪；缺口是「文档未定义界面层」而非「后端缺失」，
   因此这些应先补设计决策（可能各需一个新 REQ）再排期。
6. **遗留环境**：`.worktrees/ux-manual` 保留了认证出图补丁（未合并），供 B1 修复任务参考。

## 14. 证据与事实源

- 设计语料：`Requirements/REQ-033/051~060/067/068`、`Notes/contracts/*`、`Notes/decisions/*`、`Notes/CONTEXT.md`、`Notes/PROJECT-CONVENTIONS.md`、`docs/architecture.md`、`docs/api.md`、`docs/glossary.md`。
- 现状证据：`web/src/**` 实测命令（§4）、`docs/user-manual.md` 的 22 张截图、`docs/ux-review.md` 的 B1–B6。
- 归档：知识库 `Notes/Audit-2026-09-28/`（182 条 AC 映射 + 87 条问题全文）。
- 本次评估的语料分析产出（8 份，逐条带 `文件:行号`）：
  ① 产品定位/角色/对象与应然 IA（366 行）② Web 页面需求全集与 251 条 AC（1 129 行）
  ③ 发布核心领域状态机与「必须可见」清单（325 行）④ 治理/审计/通知/紧急域（258 行）
  ⑤ 契约层 UI 硬约束 118 条与漂移 29 条（331 行）⑥ 决策层台账 40 条 / 禁令 18 条 / 阻塞空白 10 条（318 行）
  ⑦ 现状 UI 结构盘点（882 行，含可复算脚本）⑧ **8 个 web REQ 逐条规格↔实现审计 202 条**
  （133 已实现 / 47 部分 / 10 缺失 / 12 不符，§5.3）。
  其中**经本次独立复核**的结论已标注「实测/代码核对」；未复核者按「静态清单」口径引用。
- **本次评估纠正的结论（3 处）**：① `REQ-060` 实为 `withdrawn`（原判断为「缺失」）；
  ② `docs/api.md` 关于审计查询字段的陈述与实现相反（实现完整填充 actor/resource/created_at，
  `internal/audit/audit_service_handler.go:228-259`）；③ §6.1 的「未命中」按「作为独立入口/对象」判定，
  ReleaseBundle/Candidate Artifact 的字段本身仍被操作表单消费（不是字段缺失，是没有浏览面）。
- **仍待实现侧逐条验收**：`REQ-057` 的失败态分支（首载失败不得渲染空时间线/断线 banner）、
  `REQ-058` 的 50 条 AC、`REQ-053` 位于 h4 小节的 22 条 AC（顶层 `## 验收标准` 只有 4 条摘要，
  只认顶层标题的统计会漏 18 条）。

## 15. 独立复核记录

本文与配套卡由**独立上下文**的审查员复核两轮（只读、未改文件、未停服务；要求「证伪」）。

**第一轮（初稿）**：结论 **PASS-with-issues**。12 组重点抽查中 11 组通过、§5.3 的 202 条统计可完整复算、
`REQ-060` 撤回修正已在两处一致；发现并已修正 10 项：

| # | 复核发现 | 修订 |
| --- | --- | --- |
| 1 | 路由数写 21，实测 **22** | §4.6 改为 22 条路由记录 / 20 个页面文件 |
| 2 | `font-size` 取值写 12，实测 **16** | §4.2 改为 16 并列出全部取值（另见第二轮：3 处残留已清） |
| 3 | `@media` 写 15 条，实测 **14** | §4.3 改为 14 条 / 13 个文件 |
| 4 | §1 语种数字（38/18/4）与 §4.4（26/27/4/13）冲突 | §1 统一为 26/27/4/13 |
| 5 | P6 的 grep 证据被证伪（`Pagination`/`pageSize` 非零命中） | §5.2 P6 改述为「没有分页 UI 控件」并给出准确归属 |
| 6 | 语料份数三处互斥（6/7/8） | §0.3、§14、TASK-170 §1 统一为 **8 份** |
| 7 | `Requirements/` 篇数写 82，实测 **83** | §0.1 改为 83 篇（含本轮新增 REQ-100） |
| 8 | §5.3 抹掉了源表的「（+1 待核实）」 | 已恢复 |
| 9 | TASK-170 无可验收 AC | 补 AC-170-01..07 |
| 10 | §5.8 的 `npm test` 数字无出处 | 标注为 2026-09-28 复跑值 |

**第二轮（修订稿）**：结论 **PASS-with-issues**，10 项中 8 项完全到位；剩余 4 行残留
（§1/§9.1/§10 的 `font-size` 旧值 12、TASK-170 §1 的「7 份」）已按复核清单逐行修正，
`diff` 仅含这 4 处（外加 1 处措辞精度）；随后重新冻结 hash 并记录在 `Tasks/TASK-170-*` 的「冻结基线」。

**未采纳的复核建议**：无。

# Release Manager Web 控制台 · 使用教程

本教程面向**使用控制台的人**（平台管理员 / 发布管理员 / 部署者 / 只读观察者），按「第一次登录 → 认识导航 → 走完一条发布」
的顺序编排，并在每个页面说明「它是干什么的、怎么点、点完会发生什么、出错先看哪里」。
系统边界与契约见 `docs/architecture.md`，本地环境见 `docs/dev-environment.md`，故障处置见 `docs/runbook.md` —— 本文不重复这些内容。

## 0. 证据与适用边界

| 项 | 值 |
| --- | --- |
| 截图环境 | **生产** `https://10.10.33.122:8443`（k3d `rm-prod`，`web:5080198` rev22 + 6 个 Go 服务 `:f0f8d6b`） |
| 截图方式 | Playwright + 系统 Chrome，真实登录（`admin` / platform_admin），视口 1440×900、长页面整页截图；另含 375×812 两张 |
| 截图日期 | **2026-10-21**，共 **37 张**（暗色 29 张 + 亮色 6 张 + 375 两张），全部当日实测 |
| 夹具数据 | 生产库现状：1 个客户（RM Cust Demo）/ 1 个集群（rm-cust）/ 4 个 release / 6 个历史操作（5 succeeded、1 failed）/ 1 个已验证 Bundle |
| 关键限制 | **生产环境当前没有在线 Operator**（客户集群里未部署 operator）。因此所有依赖 Operator 的读都会被拒绝并**显式降级**（例如 Values 页的 Secret 列表、紧急变更的候选制品），本教程在对应位置标注了「本环境降级表现」 |
| 主题 | 控制台**暗色优先**；亮色样本见 `dc-03-home-light.png`、`dc-09-release-inventory-light.png`、`dc-10-values-editor-light.png`、`dc-14-audit-light.png`、`dc-17-operation-detail-light.png` |

> 本轮的界面是「方向 C」改版后的版本：统一 `PageHeader`（面包屑只显示**名称**）、语义化状态胶囊、`Steps` 阶段条、
> 并排 diff、可折叠原始 JSON。旧版截图仍保留在 `docs/images/user-manual/`（文件名不带 `dc-` 前缀）作为历史证据，
> 供 `docs/ux-revamp-plan.md` 与 `docs/ux-review.md` 引用；**本教程只引用 `dc-` 前缀的新截图**。

## 1. 术语与页面地图

| 术语 | 含义 |
| --- | --- |
| **客户 / Customer** | 租户边界，拥有自己的集群、发布定义、Operator 与发布数据 |
| **集群 / Cluster** | 归属单个客户的发布目标集群，也是 Operator 运行的边界 |
| **发布定义 / Definition** | 描述目标客户/集群、namespace、release name 与 chart 的持久发布配置（Promotion Mapping） |
| **发布 / Release** | Operator 从目标集群同步回来的 Helm Release 实况（revision、values digest、最近同步时间） |
| **Values 版本 / ValuesRevision** | 不可变的配置版本；发布操作必须绑定一个**已审批**版本 |
| **操作 / Operation** | 一次发布动作（安装 / 升级 / 回滚），有状态机、预检与时间线 |
| **预检 / Preflight** | 下发前的有序准入检查（制品 → 渲染 → 集群 → 镜像拉取） |
| **制品 Bundle** | 已校验的 chart + 镜像集合，发布操作的输入 |
| **紧急变更 / Emergency change** | 绕过常规流程、直接改副本或镜像的应急通道，会留下收敛任务 |
| **紧急锁 / Stuck lock** | 效果未知且已过观察窗口的紧急操作，需要显式收敛或人工放行 |

页面与路由（`web/src/router/index.ts`）：

| 侧栏分组 | 页面 | 路由 |
| --- | --- | --- |
| 发布与运维 | 操作中心 | `/operations` |
| 发布与运维 | 发布定义 | `/definitions` |
| 发布与运维 | 发布 Bundle | `/bundles` |
| 发布与运维 | 制品生命周期 | `/artifacts/lifecycle` |
| 发布与运维 | 紧急锁 | `/emergency/stuck-locks` |
| 资产与审计 | 客户 | `/customers` |
| 资产与审计 | 集群 | `/customers/:customerId/clusters` |
| 资产与审计 | 审计 | `/audit` |
| 治理与账号 | 信任根 | `/settings/trust` |
| 治理与账号 | 授权绑定 | `/settings/bindings` |
| 治理与账号 | 组织成员 | `/settings/organization` |
| 治理与账号 | 本地用户 | `/settings/users` |
| 治理与账号 | 修改密码 | `/settings/password` |

页内层级（都在客户/集群/发布之下）：`发布清单` → `/customers/:customerId/clusters/:clusterId/releases`，
`Values` → `.../releases/:releaseId/values`，`操作历史` → `.../releases/:releaseId/operations`，
`创建操作` → `.../operations/new`，`操作详情` → `.../operations/:operationId`，
`紧急变更` → `.../emergency`，`收敛任务` → `.../emergency/convergence`。

## 2. 登录、登出与主题

![登录页](images/user-manual/dc-01-login.png)

1. 打开 `/login`，填**用户名**与**密码**。表单用服务端管理的 HttpOnly 会话，浏览器里不存 bearer token。
2. 点**登录**；成功后落到 `/`。若之前被守卫拦下，会回到原来的 return URL。
3. 会话过期会跳回 `/login?reason=expired`，登录后回到原页面。
4. 登出在侧栏左下角（`退出登录`），会撤销服务端会话。

**主题**：侧栏左下角有一个循环按钮，顺序是 **跟随系统 → 浅色 → 深色**，按钮文案会写成「切换主题（当前：跟随系统）」。
选择存在浏览器 `localStorage['rm-theme']`，刷新后保持；`跟随系统` 会跟随操作系统的 `prefers-color-scheme` 实时切换。

![首页·亮色](images/user-manual/dc-03-home-light.png)

## 3. 外壳与导航

改版后的外壳是**固定 212px 侧栏 + 内容区（最大 1180px）**：

- **侧栏顶部**：品牌 `Release Manager`（点击回首页）。
- **三个分组**：`发布与运维`、`资产与审计`、`治理与账号`；没有权限的条目**不渲染**（不是禁用）。
  `集群` 只有在进入某个客户之后才出现（它需要 `customerId`）。
- **侧栏左下角**：当前用户与组织名、主题按钮、`退出登录`。
- **窄屏（≤1024px）**：侧栏收进顶部条，点「导航菜单」打开抽屉（`aria-expanded`、`Esc` 关闭、焦点留在抽屉内）。
- **页内页头**：每个页面顶部是统一的页头——**面包屑（只显示名称，不显示 UUID）**、标题、副标题、右侧动作。
  面包屑的层级就是数据层级：`客户 / 集群 / 页面`。

![首页·暗色](images/user-manual/dc-03-home.png)

## 4. 首页总览

首页回答四个问题，下面再给两块「正在发生什么」：

| 卡片 | 口径 |
| --- | --- |
| **待收敛发布** | 发布清单里状态为「不同步」「缺失」的条数；下方提示「共 N 个发布」 |
| **进行中操作** | 跨发布非终态操作数（有下一页时显示 `N+`） |
| **发布同步率** | `状态为生效中 ÷ 发布总数`；没有发布时显示 `—`（不会显示 0%） |
| **紧急变更** | 效果未知且已过观察窗口、需要显式收敛的操作数 |

> 这四张卡**只显示真实读到的数据**：任何一个读失败，卡片显示 `—` 并在提示行写原因（加载失败 / 功能未启用），
> 而不是显示 0。历史版本里的「7 天成功率」因缺少跨发布终态聚合接口**已下线**，避免长期空卡。

- **进行中的操作**：按等待时长升序列出，行内是操作类型 + 发布名 + 所在集群 + `StatusTag`。
  它只包含**非终态**操作（接口只提供这个 feed），成功/失败的历史请去「操作历史」。
- **集群健康**：按集群显示「已同步 N/M」进度条与在线 Operator 数；没有 Operator 时写「暂无 Operator」。
- **快速入口**：12 个磁贴，是窄屏抽屉之外的第二条导航路径（宽屏下与侧栏功能重叠，属刻意保留的兜底）。

## 5. 资产：客户与集群

### 5.1 客户

![客户列表](images/user-manual/dc-04-customers.png)

客户卡片显示名称、标识（slug）、启用状态、配置版本与创建时间。有写权限才出现「新建客户」。

![客户详情](images/user-manual/dc-05-customer-detail.png)

- 编辑名称/标识后保存（提交中按钮变「保存中…」）。
- **停用客户是级联破坏性操作**：会吊销该客户下 Operator 的证书与会话，需要勾选确认。
- 下方「变更历史」是该客户的事件流。

### 5.2 集群

![集群列表](images/user-manual/dc-06-cluster-list.png)

集群卡片显示集群名、是否作为发布目标、配置版本、路由规则条数；页面下方是「发布目标预览」。

![集群详情](images/user-manual/dc-07-cluster-detail.png)

摘要三项（版本 / 状态 / 路由规则数），下面分「镜像路由」「Chart 路由」两块**只读**呈现
（模式 / 提供方 / 源前缀 / 目标前缀，以及派生出的中央 URI 与目标 URI）。`编辑` 进编辑页；停用会弹确认框。

![集群编辑](images/user-manual/dc-23-cluster-edit.png)

编辑页可改集群名、是否作为发布目标，并用「添加规则 / 删除」维护路由规则
（模式：直连 / 透传缓存 / 复制；以及提供方与前缀）。

### 5.3 Operator

Operator 是跑在客户集群里的 agent：执行 Helm 操作并回传发布库存。

![Operator 列表](images/user-manual/dc-08-operator-list.png)

- 顶部可刷新、生成一次性注册令牌（仅平台管理员/发布管理员）。
- 列表可按生命周期（全部/生效/已被取代/已撤销）与会话（全部/无会话/在线/可疑/离线/已撤销）过滤。
- 注册流程：生成令牌 → 按弹窗给出的部署命令在客户集群部署 → 勾选「已保存到密钥库」后关闭（不勾选则关闭会吊销待用令牌）。

> **本环境降级表现**：生产当前没有 Operator，列表是空态，因此没有「Operator 详情」截图。
> 详情页内容包括状态、身份与运行时、能力集合（只有 Operator 上报了能力才显示）。

## 6. 发布清单

![发布清单](images/user-manual/dc-09-release-inventory.png)

- 顶部说明数据来源（Operator 最近一次同步）与条数，动作是「刷新」「触发同步」。
- 筛选：状态（全部/生效中/缺失/不同步/未知）+ 名称搜索。
- 表格列：**发布 / 集群 / 当前版本 / 库存状态 / 紧急变更 / 配置摘要 / 操作**。
  - 「发布」单元格：`namespace/name`、`chart@version`、最近同步时间，以及「操作历史」入口。
  - 「库存状态」是**服务端枚举**的语义色胶囊：生效中（绿）/ 不同步（琥珀）/ 缺失（红）/ 未知（灰）。
  - 「配置摘要」显示 values digest 的**前 8 位**，右侧「复制」拷贝完整 64 位（`title` 里也是完整值）。
  - 「紧急变更」是**小号红色标签**（不是整列红字），未绑定发布定义的行显示「未绑定发布定义」。
- 空态是「没有匹配的发布」，与错误态互斥：加载失败会显示带「重试」的错误面板，而不是空表。

![发布清单·亮色](images/user-manual/dc-09-release-inventory-light.png)

## 7. Values 编辑与审批

![Values 编辑器](images/user-manual/dc-10-values-editor.png)

- 顶部是页头 + 动作区：语言切换（**YAML / JSON 分段按钮，真的切换编辑器语言**）、「保存为新版本」。
- 左栏是编辑器（保留语法高亮，暗色/亮色两套高亮）、右栏是**并排差异**（父级 → 当前，`N 处变更` 或「无变更」）。
  只有**语义差异**会展示：格式、注释、键顺序的变化不产生无意义 diff。
- 下方「Secret 引用」只保存 namespace 内 Secret 的**名称 / key / 目标路径**，**不读取也不传输 value**。
- 版本流程：`保存草稿` → `提交` → 审批人 `批准 / 拒绝`。草稿存在浏览器本地，刷新后提示「已恢复未保存的编辑」。
  只读角色会看到「当前角色为只读。服务端仍会独立执行授权。」

![Values·亮色](images/user-manual/dc-10-values-editor-light.png)

> **本环境降级表现**：因为 Operator 不在线，Secret 列表读（`ListSecrets`）返回 503，
> 页面顶部显示「网络或 Operator 暂不可用，请检查连接后重试」+「重试」按钮 —— 这是**预期降级**，不是白屏。

## 8. 创建操作

![创建操作](images/user-manual/dc-11-operation-create.png)

1. **操作类型**：`安装 / 升级 / 回滚` 三个**分段按钮**（`aria-pressed`），切换会改变需要填写的字段。
2. **制品 Bundle**：下拉选制品（显示版本与 digest）。
3. **已审批的 Values 版本**：下拉选已审批版本。
4. **补丁覆盖**（可选）：只允许点分路径（如 `spec.replicas`）；Secret 类字段必须用 Secret 引用，不允许明文。
5. **检查并确认**：先做本地必填校验，再 arm 右侧「确认创建」。

![必填项未选时的校验](images/user-manual/dc-15-operation-create-validation.png)

漏选必填项时，字段下方出现红色错误文案（例如「请选择制品」「请选择已审批的配置版本」），
右侧确认按钮保持禁用（禁用态用专门的 disabled token，不是降低不透明度的灰按钮）。

右侧「最终确认」面板**常显**并复述五件事：类型、发布名、集群、制品、配置版本、补丁覆盖。
只有通过检查后「确认创建」才可用；**任何字段修改都会自动解除确认**（防止提交陈旧值）。

> **本环境限制**：生产没有在线 Operator，`检查并确认` 之后的预检链路无法真实推进，
> 因此本教程不提供「确认创建成功」截图；操作详情见下一节（历史真实操作）。

## 9. 操作历史与操作详情

![操作历史](images/user-manual/dc-34-operation-list.png)

单个发布下的操作历史：操作类型、状态、开始/结束时间、操作人，行内可进入详情。
跨发布的视图在侧栏「操作中心」（`/operations`）。

![操作中心](images/user-manual/dc-33-operation-center.png)

操作中心是跨发布的非终态操作列表（终态操作不在此），可按状态与发布过滤。

![操作详情（成功）](images/user-manual/dc-17-operation-detail.png)

详情页自上而下：

1. **页头**：标题是「操作类型 · 发布名」（这里是「安装操作」），副标题是「状态 · 状态版本 5」；右侧是「取消操作 / 刷新」。
2. **阶段条**：预检 → 审批 → 下发 → 收敛，四段都由状态机派生（不是写死的 UI）。
3. **摘要**：操作 ID、发布定义（都是**缩略 + 复制**，`title` 为完整值）、状态版本、目标版本、创建/更新/终止时间。
4. **两栏**：左「预检结果」（每项检查「通过/失败」+「查看原始响应」折叠 JSON），右「状态时间线」（事件按时间倒序）。
   时间线里的操作 ID / 请求 ID 同样是缩略 + 复制。

![操作详情·亮色](images/user-manual/dc-17-operation-detail-light.png)

![操作详情（失败）](images/user-manual/dc-18-operation-detail-failed.png)

失败/取消的操作同样可回看；失败操作的错误信息与错误码显示在预检区与时间线里。

![无效 id 的错误态](images/user-manual/dc-21-operation-invalid-error.png)

⭐ **无效的操作 id 现在会明确报错**：直接访问 `/operations/<不存在的 uuid>` 会显示
「操作加载失败」+ 稳定错误码（例如 `not_found`）+ 返回操作历史/操作中心的链接 + 「重试」，
**不再永久停在「正在加载 Operation…」**。这是 2026-10-20 改版修掉的一个真实缺陷。

## 10. 紧急变更、收敛任务与紧急锁

![紧急变更](images/user-manual/dc-12-emergency-change.png)

紧急变更用于「常规流程来不及」的场景，页面按四步编排：**选择目标工作负载 → 选容器与制品 → 填变更信息 → 确认变更**。
顶部有三个状态指示决定你能做多少：镜像可变更 / 副本可变更；平台尚未采集到工作负载信息、
或没有 `VERIFIED` 候选制品时，对应控件会禁用并给出原因。确认变更时要选收敛策略
（`需异人审批提升` / `下次对账时回退`）并勾选风险确认。

![收敛任务](images/user-manual/dc-13-convergence-tasks.png)

收敛页把紧急变更「欠的账」列成任务（目标 / 类型 / 原因 / 提升路径 / 状态）；勾选 1–50 个任务后「准备收敛」，
选择数为 0 时按钮禁用并提示需要选择 1–50 个。页头右侧会用缩略 + 复制的形式显示当前发布定义 id。

![紧急锁](images/user-manual/dc-32-stuck-locks.png)

紧急锁页列出**效果未知且已过观察窗口**的紧急操作，需要人工确认实际状态后再决定「释放锁」或显式收敛。
释放锁要求填写原因并勾选确认；处置流程与值班口径见 `docs/runbook.md` 的「紧急变更 / stuck lock」章节。

## 11. 审计

![审计事件](images/user-manual/dc-14-audit.png)

- 筛选栏六项：操作人、资源类型、资源 ID、动作、状态、时间范围（起/止）。
- 结果表：时间（只显示 `HH:MM:SS`，完整时间在 `title` 里）、操作人、动作、资源（**缩略 + 复制**）、状态、耗时。
- 状态列用语义色胶囊（成功/被拒/中性），动作与资源类型都显示为**中文标签**（未知值显示「未知事件」）。
- 「导出当前查询」生成导出任务；**操作人筛选是私有条件，不写进 URL**。

![审计·亮色](images/user-manual/dc-14-audit-light.png)

> 三种异步态**互斥**：加载中只显示骨架屏；失败只显示错误面板 + 重试；成功但为空才显示空态。
> 不会出现「同时显示错误和空结果」这种把故障解释成没数据的情况。

## 12. 发布定义、发布 Bundle 与制品生命周期

![发布定义](images/user-manual/dc-24-definitions.png)

发布定义页维护 Promotion Mapping 与定义设置（新建/编辑/包含已停用的开关、表格）。

![发布 Bundle](images/user-manual/dc-25-bundles.png)

Bundle 页列出已校验的制品（chart + 镜像集合）与内容摘要，行内可展开明细（镜像、证据引用）。

![制品生命周期](images/user-manual/dc-26-artifact-lifecycle.png)

制品生命周期页管理保留期清理与归档恢复：可以手动触发清理、从归档恢复；页面顶部说明
「仅平台管理员可用，维护模式下会被拒绝」。清理是同步执行的（服务端预算可达约 1 小时），
重复提交同一请求会在保留窗口内被拒绝。

## 13. 治理与账号

![签名信任根](images/user-manual/dc-27-trust-policy.png)

信任根页维护签名信任策略与版本（信任根编辑器、吊销、轮换）。环境选择器决定你看哪一套策略。

![授权绑定](images/user-manual/dc-28-bindings.png)

授权绑定页把组织与客户绑定，并授予能力（capability grants）。只有平台管理员/发布管理员能读。

![组织成员](images/user-manual/dc-29-organization.png)

组织成员页管理组织内的成员与角色（行内可改角色、移除成员）。

![本地用户](images/user-manual/dc-30-local-users.png)

本地用户页管理平台本地账号（创建、禁用、改角色），仅平台管理员可见。

![修改密码](images/user-manual/dc-31-change-password.png)

修改密码页改当前账号口令；**改后会吊销该账号的全部会话**，需要用新口令重新登录。

## 14. 权限与角色

前端只做 UI 门禁，**服务端 Casbin 才是唯一裁决点**（`internal/auth/casbin.go`、`internal/auth/procedure_policy.go`）。

| 门禁（`web/src/stores/auth.ts`） | 判定 | 谁能通过 |
| --- | --- | --- |
| `canWrite` | 角色里没有 `viewer` | platform_admin、release_admin、deployer |
| `canEnrollOperators` / `canRevokeOperators` | 含 `platform_admin` 或 `release_admin` | 平台管理员、发布管理员 |
| `canCreateReleaseOperation` | 同上 | 平台管理员、发布管理员 |
| `canReadBundles` | 有 bundle read 规则 | 平台管理员、发布管理员、部署者 |
| `canReadBindings` | 有 binding read 规则 | 平台管理员、发布管理员、部署者 |
| `canRunCleanup` / `canManageLocalUsers` | 含 `platform_admin` | 平台管理员 |

无权限时表现分三层：**入口不渲染**（不是禁用）、**路由 404**（特性开关关闭）、**页面 403**。

![403](images/user-manual/dc-20-forbidden.png)

## 15. 特性开关

`web/.env.example` 是完整清单；所有开关的语义都是 `!== 'false'`：**默认全开，只有精确字符串 `false` 才关闭**
（写 `FALSE` / `0` 无效）。

| 变量 | 关掉后 |
| --- | --- |
| `VITE_ENABLE_RELEASE_INVENTORY` | 发布清单与 Values、操作、紧急变更路由整组消失 |
| `VITE_ENABLE_VALUES_REVISION` | Values 编辑器路由与入口消失 |
| `VITE_ENABLE_RELEASE_OPERATIONS` | 操作中心/创建/详情、紧急变更、收敛、紧急锁、发布定义、Bundle、制品生命周期路由消失 |
| `VITE_FEATURE_CLUSTER_ROUTING` | 集群路由与侧栏「集群」入口消失 |
| `VITE_FEATURE_OPERATOR_MANAGEMENT` | Operator 入口消失 |
| `VITE_OPERATION_LIVE_UPDATES` | 操作详情不做实时刷新（只保留「刷新」） |

## 16. 常见问题与错误态

| 症状 | 先查什么 | 说明 |
| --- | --- | --- |
| 登录成功却停在 `/login` | 登录响应是否带 `Set-Cookie`、`user` 是否非空 | 控制台只实现 cookie 会话模式；服务端跑 token 模式时前端进不去 |
| 操作详情一直「正在加载」 | 操作 id 是否存在 | **改版后已修**：不存在的 id 会显示带错误码的错误态（见第 9 节截图） |
| 页面顶部提示「网络或 Operator 暂不可用」 | 客户集群的 Operator 是否在线 | 依赖 Operator 的读（Secret 列表、候选制品）会返回 503；这是**显式降级**，联系值班按 `docs/runbook.md` 处理 |
| 审计页没有数据 | 筛选条件是否过窄、时间范围是否为空 | 空态与错误态互斥：有错误会显示错误面板而不是「暂无事件」 |
| 发布清单行显示「未绑定发布定义」 | 该 release 是否已有发布定义绑定 | 未绑定的行不能创建操作/紧急变更，这是数据前置而不是权限问题 |
| 首页某张卡显示 `—` | 卡片提示行的原因文案 | 该指标读失败或本环境不提供；**不会用 0 冒充健康** |
| 主题切换无效 | 浏览器是否禁用 localStorage | 选择存在 `rm-theme`；禁用存储时只能跟随系统 |

## 17. 截图索引（全部为 `docs/images/user-manual/dc-*.png`）

| 文件 | 页面 | 主题 | 尺寸 |
| --- | --- | --- | --- |
| `dc-01-login.png` | 登录 | 暗 | 1440×900 |
| `dc-03-home.png` / `dc-03-home-light.png` | 首页总览 | 暗 / 亮 | 1440×900 |
| `dc-04-customers.png` | 客户列表 | 暗 | 1440×900 |
| `dc-05-customer-detail.png` | 客户详情 | 暗 | 1440×900 |
| `dc-06-cluster-list.png` | 集群列表 | 暗 | 1440×900 |
| `dc-07-cluster-detail.png` | 集群详情 | 暗 | 1440×900 |
| `dc-23-cluster-edit.png` | 集群编辑 | 暗 | 1440×900 |
| `dc-08-operator-list.png` | Operator 列表 | 暗 | 1440×900 |
| `dc-09-release-inventory.png` / `-light.png` | 发布清单 | 暗 / 亮 | 1440×900 |
| `dc-10-values-editor.png` / `-light.png` | Values 编辑与并排 diff | 暗 / 亮 | 1440×1088 |
| `dc-11-operation-create.png` | 创建操作（初始态） | 暗 | 1440×900 |
| `dc-15-operation-create-validation.png` | 创建操作（必填校验） | 暗 | 1440×900 |
| `dc-17-operation-detail.png` / `-light.png` | 操作详情（安装·成功） | 暗 / 亮 | 1440×1476 |
| `dc-18-operation-detail-failed.png` | 操作详情（紧急变更·失败） | 暗 | 1440×1258 |
| `dc-21-operation-invalid-error.png` | 操作详情的无效 id 错误态 | 暗 | 1440×900 |
| `dc-33-operation-center.png` | 操作中心 | 暗 | 1440×900 |
| `dc-34-operation-list.png` | 单发布的操作历史 | 暗 | 1440×900 |
| `dc-12-emergency-change.png` | 紧急变更 | 暗 | 1440×1043 |
| `dc-13-convergence-tasks.png` | 收敛任务 | 暗 | 1440×900 |
| `dc-32-stuck-locks.png` | 紧急锁 | 暗 | 1440×900 |
| `dc-14-audit.png` / `-light.png` | 审计 | 暗 / 亮 | 1440×1425 |
| `dc-24-definitions.png` | 发布定义 | 暗 | 1440×900 |
| `dc-25-bundles.png` | 发布 Bundle | 暗 | 1440×900 |
| `dc-26-artifact-lifecycle.png` | 制品生命周期 | 暗 | 1440×900 |
| `dc-27-trust-policy.png` | 签名信任根 | 暗 | 1440×900 |
| `dc-28-bindings.png` | 授权绑定 | 暗 | 1440×900 |
| `dc-29-organization.png` | 组织成员 | 暗 | 1440×900 |
| `dc-30-local-users.png` | 本地用户 | 暗 | 1440×900 |
| `dc-31-change-password.png` | 修改密码 | 暗 | 1440×900 |
| `dc-20-forbidden.png` | 403 | 暗 | 1440×900 |
| `dc-36-home-375.png` | 首页（375px） | 暗 | 375×2040 |
| `dc-37-audit-375.png` | 审计（375px） | 暗 | 375×2084 |

## 18. 复现方式（截图与巡检脚本）

截图由 Playwright 驱动真实浏览器、真实登录采集，口令只经环境变量传入（不落盘）：

```bash
cd web
RM_ADMIN_PW='<admin 口令>' node <采集脚本>.mjs   # 脚本把 BASE 指向 https://10.10.33.122:8443
```

- 采集前先确认生产可达：`curl -sk -o /dev/null -w '%{http_code}' https://10.10.33.122:8443/` ⇒ `200`。
- 深/浅主题通过 `localStorage['rm-theme']` 切换后再 reload，保证与用户实际操作一致。
- 每张图都断言 `document.documentElement.scrollWidth === window.innerWidth`（不允许横向溢出）；
  本轮 37 张全部通过。
- 值班巡检命令、故障处置与回滚步骤见 `docs/runbook.md`；部署与镜像级回滚见 `docs/production-deploy.md` 的 §8.16。

> 事实源：`web/src/router/index.ts`、`web/src/components/common/AppShell.vue`、`web/src/components/common/PageHeader.vue`、
> `web/src/stores/auth.ts`、`web/src/i18n/messages.ts`、`web/.env.example`、`internal/auth/casbin.go`、
> `internal/auth/procedure_policy.go`、`docs/runbook.md`、`docs/production-deploy.md`。

# 测试指南

本项目的测试分四层：单元测试、打 `integration` 标签的集成测试、kind 集群上的 SDK 门禁、
以及分阶段 E2E 运行器。所有 Go 测试入口都收敛在 Makefile；前端测试在 `web/` 内独立运行。

## 测试分层

### 单元测试

- 统一入口 `make test`（= `go test -race ./...`），不加 build tag、不需要任何外部依赖。
- CI 把两个包从「全仓一次跑」中拆出来（`go list ./... | grep -vE '/(internal/store/sqlite|internal/quality/sdkcheck)(/|$)'`），
  由 `test-sqlite` 与 `test-sdkcheck` 两个 job 分别执行（后者需预取 analysistest fixture 模块）。
  **`make test` 本身不做这个排除**——本地跑全仓会同时覆盖这两个包。
- `make test-coverage` 额外生成 `coverage.out` 并打印 `go tool cover -func` 结果。

### 集成测试

- 文件首行打 `//go:build integration`，用 `-tags=integration` 激活。归档位置：
  `internal/store/postgres/*_test.go`（12 个文件）、`internal/migration/migrate_integration_test.go`、
  `internal/postgres/migrate_integration_test.go`、`test/integration/{install,upgrade,rollback,rollout_watch}_sdk_test.go`。
- 需要真实 PostgreSQL 的用例读 `POSTGRES_TEST_DSN`，**未设置时 `t.Skip`**（不是失败）；
  `internal/store/postgres` 的用例会在该 DSN 上按测试建独立 schema 并跑 migrations。
- **易混点**：`cmd/auth/main_test.go` 与 `cmd/orchestrator/main_test.go` 同样读 `POSTGRES_TEST_DSN`
  并在缺失时 skip，但它们**没有 `integration` build tag**——会随 `make test` 一起编译执行。
  给 live-DB 测试加标签时不要漏掉这类文件的既有约定。

### kind 集群 SDK 门禁

下表中三个门禁需要**真实 kind 集群**（各自创建后清理，`trap` 保证中断也清理）；Rollback 门禁用
in-memory storage + `kubefake`，**不需要集群**：

| target | 覆盖 | 测试入口 | 集群 |
| --- | --- | --- | --- |
| `make test-install-sdk` | REQ-061 Helm Install SDK 链路（无 helm/kubectl 镜像完成 Install） | `-run '^TestInstallSDK$'` | `rm-install-sdk`（固定名，先 cleanup） |
| `make test-upgrade-sdk` | REQ-086/REQ-062 SDK Upgrade、ValuesRevision 合并、并发锁、非目标隔离 | `-run '^TestUpgradeSDK$'` | `rm-upgrade-sdk`（固定名） |
| `make test-rollback-sdk` | REQ-063 独立 Rollback 验证 | `-run 'TestRollbackSDK'` | **无**（`kubefake` + in-memory storage） |
| `make test-rollout-watch` | REQ-064 client-go watch 的就绪/断线/超时 | `-run '^TestRolloutWatch'` | `rm-rollout-watch-<ts>-<pid>`（唯一名，发现同名即拒绝复用） |
| `make test-operator-image-sdk-only` | REQ-061 operator 镜像 SDK-only 合规（不为 helm/kubectl 开后门） | `cmd/imagecheck` 比对 `imagecheck.operator.yaml` | 无 |

`test-install-sdk` / `test-upgrade-sdk` 在 **kind 不可用或集群创建失败**时不报失败：它们调用
`cmd/installgate` 按 `install-sdk.quarantine.yaml` / `upgrade-sdk.quarantine.yaml` 落一条时间受限的
隔离记录并以 0 退出。「绿」不总等于「验过」，读日志时要确认走的是哪条分支。`test-rollout-watch`
另有从集群创建到测试结束 ≤ `ROLLOUT_WATCH_MAX_SECONDS`（默认 120 秒）的时长门禁，超时即失败。

### 前端测试

`web/package.json` 提供 `test`（`vitest run`）、`test:e2e`（`playwright test`）、`lint`（`eslint .`）
与 `build`（`vue-tsc -b && vite build`）。Makefile 转发其中三关（TASK-175，见命令矩阵的
`make web-install` / `make web-check`）；`make web-check` 已并入 `make quality`，所以本地的
「全量质量门禁」覆盖前端三关。`test:e2e` 需要完整 dev 栈，仍由 CI 的
`e2e`/`e2e-prerequisite` job 承担（PR 上不跑，push 与 `workflow_dispatch` 跑）。

### 跨两侧契约测试（「读侧要求 X / 写侧生产 Y」）

**规则**：任何**读侧要求某字段非空或处于特定状态**的查询（`WHERE x IS NOT NULL`、
`status = 'pending'`、按 digest/id 精确匹配等），必须有一条测试**从生产写入路径触发**，
再断言读侧能看到它（或断言对应的门禁真的会触发）。

**为什么**：两侧各自都有测试时 CI 全绿，而功能仍可能不可用 —— 读侧用例常用手写的夹具行
（直接 `store.X().Create`），写侧的用例只验证自己写的那部分，于是「没有写入者」或
「写入键与查询键不一致」都能长期不被发现。2026-09-29 一次系统性排查
（TASK-221）分四遍扫描（读侧谓词 21 项、扩展表 5 张、store 接口 225 个方法、契约面 105 个 rpc）
共查出 **3 处**这类缺陷（另有 1 处曾被判为同类、经复核为**假阳性**并已撤回，见下）：

| 缺陷 | 读侧要求 | 写侧实际 | 方向 |
| --- | --- | --- | --- |
| TASK-218 | `candidate_artifacts.validated_at IS NOT NULL` | **修复前**生产代码无人写入（现已由 `MarkValidatedForBundleTx` + validation worker 补齐） | fail-closed（功能不可用） |
| TASK-220 | `verifications` 按**镜像** digest 查 `Trusted` | 唯一写入者按 **bundle** digest 写 | fail-closed（功能不可用） |
| TASK-223 | `bundle_aliases` 的 `GetByAlias` 回落 | 生产代码无任何 INSERT（唯一 INSERT 是钉住该行为的测试；REQ-011 曾承诺别名解析但从未交付写入者，2026-09-29 记 breaking 变更并撤回该承诺） | 死路（回落已删除；未知 id ⇒ `NOT_FOUND`，与 RPC 注释一致） |

**落地方式**（写这类测试时的最小形态）：
1. 用**生产写入路径**造数据（如 `validation_worker` 的 outbox 周期、bundle 提交的 UoW），
   而不是直接 `Create` 夹具行；确实只能直接构造时，在测试名/注释里写明「这是夹具捷径」；
2. 断言**读侧**（门禁函数、列表接口、RPC 响应）能看到或拒绝；
3. 至少一条**负控制**：不该写入/不该触发的情况必须不写入、不触发。

> **撤回的一例（方法论教训，2026-09-29）**：曾有第 4 处判断认为 `convergence_tasks` 在生产中无人创建
> （`ConvergenceTasks().Create` 只有测试调用）⇒ 待收敛门禁 fail-open。复核发现该功能经**操作创建 UoW** 落库：
> `internal/store/postgres/uow.go:127` 调 `insertConvergenceTask`（两引擎同形），而 `Create` 只是**未被使用的重复方法**。
> 教训：按「接口方法的调用点」审计时，**同包内被 UoW/helper 调用的写路径不会出现在接口方法的调用点上**，
> 会把「功能正常」误判为「无人实现」；下结论前必须回到 `INSERT`/`UPDATE` 语句本身核对。

## 命令矩阵

| target | 覆盖什么 | 外部依赖 | 本地 / CI |
| --- | --- | --- | --- |
| `make test` | 全部包 `go test -race ./...` | 无 | 本地 + CI（CI 拆包见上） |
| `make test-coverage` | 同上 + coverage profile | 无 | 本地 + CI（`test`/`test-sqlite`/`test-sdkcheck` job 各自产覆盖） |
| `make lint-changed` | 与 CI 完全同口径的 lint（同工具版本 + `--new-from-rev=$(LINT_BASE)`，默认 `origin/main`）；只 lint 变更范围，所以不会让无关的既有告警阻塞 PR | `golangci-lint`（v2.13.2） | 本地；等价于 CI 的 `Run lint (changed code only)` |
| `make lint` | `golangci-lint run`（全量，0 issues）。TASK-109 的 Go 1.27 + golangci-lint v2.13.2 升级曾暴露约 45 条既有告警（gocyclo/dupl/gocritic 等），TASK-110 已逐条修复或定点抑制，全量 lint 重新成为可用信号 | `golangci-lint`（v2.13.2） | 本地；CI 用 `golangci-lint-action`（`version: v2.13.2`）且仅 lint 变更（`--new-from-rev`） |
| `make sdk-check` | SDK-only 静态门禁（REQ-037）：`os_exec_import`、`fork_exec`、`shell_wrapper`、`forbidden_binary_invocation`、`expired_exception` | 无 | 本地 + CI `sdk-check` job（同一命令、同一例外文件、同一扫描范围） |
| `make check-reqs` | 校验原子需求文档结构（`find . -path '*/Requirements/REQ-*.md'` → `cmd/reqcheck`）；**找不到 REQ 文档时失败**（ζ-1 / D-ζ 裁定）：设 `REQS_DIR` 指向 vault 的 `Requirements/`，或显式 `ALLOW_NO_REQS=1` 才跳过 | 无 | 本地（CI 未接入该 target） |
| `make audit-citations` | **只读审计**（不是门禁）：当 `文件:行号` 引证旁的正文点名了符号时，断言该符号出现在被引行 **±5 行**窗口内。**为什么不进 `make check-docs`**：实测本仓库 427 条带符号的引用里 **233 条命中（55%）**，其中主导的两类假阳性是**语义的、机械过滤不掉** —— ① 正文把符号说成**不存在**（如「无 `ReplaceAttr` 脱敏」，范围里没有它正是正文要表达的）；② 正文点名的是**调用方**而非定义处。已机械缓解的两类：非符号锚点（单词 CamelCase / YAML 键）与**简写链**（`A:84`、`Sym`（`B:157`）中 `Sym` 属后一条引用）。⇒ 结论交给人工；若要让它阻塞，需先解决上述两类假阳性 | 无 | 本地（`make audit-citations`）；**不在 CI、不在 `make quality`** |
| `make audit-task-premises` | **只读审计**（不是门禁）：扫 vault `Tasks/` 中 `status: ready` 的规划/调查卡，核对其是否记录「前提最后一次被核对」的两个**可选**字段 `verified_at`（`YYYY-MM-DD`）与 `verified_head`（核对时的 **`main`** HEAD sha；**参考基准是 `main`** —— `origin/main` 优先，否则本地 `main`，两者都取不到时不做 HEAD 比对并打印 SKIP，**不失败** —— **不是当前 checkout 的 `HEAD`**，所以在特性分支上跑不会把分支提交计入 N）。四类输出：**未记录**（缺字段，或 `verified_at` 不是 `YYYY-MM-DD`）、**过期**（`verified_at` 早于 `-max-age-days`，默认 14 天；`verified_at` 是日期，比较前把 `Now` 截断到 UTC 当日 00:00 即**天粒度**，**恰好等于阈值不算过期**，比较是严格大于）、**HEAD 已前进 N**（`verified_head` 是 **`main`** 祖先且 `git rev-list --count <sha>..<main>` > 0）、**无法比对**（`verified_head` 未知或不是 `main` 祖先 ⇒ 不猜 N）。发现**从不改变退出码**（审计跑起来即 0；只有工具自身失败才 2）。**为什么不是门禁**：两个字段由人写，`verified_head` 之后 `main` 前进 N 个提交**不等于**前提失效（无关提交也会推进 HEAD），`verified_at` 变旧也只是启发式 —— 一张卡可以 `ready` 很久而前提仍然成立。**盲区**：审计**读不到前提本身**，无法判断卡所假设的事（某功能缺失 / 某 PR 未合 / 某门禁未实现）是否仍成立；缺字段只说明「没有记录」，不等于「从未核对」。⇒ 结论交人工；实现 `internal/quality/taskpremises` + `cmd/taskpremises` | 无（`git`；`-commits` 数据由 Makefile 注入，命令本身不 `os/exec`） | 本地（`make audit-task-premises TASKS_DIR=<vault>/Tasks`）；**不在 CI、不在 `make quality`** |
| `make check-error-codes` | 领域错误码可发射性门禁：对每个 REQ，取「**错误模型表首列码**」∩「**AC 行反引号码**」，逐个核对必须可发射（Go 字符串字面量或 `deploy/` 下的 shell 脚本）。**能力边界（两条，务必知晓）**：① 只覆盖**同时出现在错误表与 AC 里**的码 —— AC 文本里的反引号也含字段名（如 `payload_sha256`、`e2e_run_id`），纳入会大量误报；「错误表有、AC 无」的码属错误表/AC 覆盖面不一致，**不在覆盖范围内**。② 判定是「**该串在仓库里存在发射点**」，**不是**「该条件真的会发射它」—— 同一个码在别处有发射点即可通过（负控制须**全仓**移除该串才能变红，见下）。例外登记在 `errcodes.exceptions.yaml`（owner + reason + `expires_at`，**过期即不再豁免**）。REQ 文档在知识库而非本仓库 ⇒ **需设 `REQS_DIR`**，否则**失败**（ζ-1 / D-ζ）；`ALLOW_NO_REQS=1` 可显式跳过 | 无 | 本地（需 `REQS_DIR` 指向 vault 的 `Requirements/`；CI 无 vault 检出，故 CI 只跑该逻辑的单元测试）；`make quality` 已含 |
| `make vulncheck` | 对模块跑 govulncheck（REQ-008 §8-8），只对**代码实际调用**的漏洞失败；无上游修复的公告在 `vulncheck.exceptions.yaml` 登记（owner + 补偿控制 + 复审日期，过期即失败），CI 另跑 `vulncheck` job | `govulncheck`（`make vulncheck` 自动安装 `GOVULNCHECK_VERSION`）+ 漏洞库网络 | 本地 + CI `vulncheck` job（`scripts/vulncheck.sh`） |
| `make check-migrations` | 静态门禁：`migrations/` 两个内嵌集合（release_manager、release_notifier）编号连续、每个版本 up/down 成对、文件名合规（REQ-008 §8-19）；负控制在 `migrations/continuity_test.go` 内 | 无（读 `//go:embed` 的文件系统） | 本地 + `make quality` + CI `test` job 的全量 `go test` |
| `make check-schema-parity` | 双引擎 schema parity 门禁（D-ε/ε-1，兑现 `AGENTS.md` 硬约束 4）：生成两侧「表 + 列 + 存储类 + **可空性**」规范化快照并 diff。**SQLite 侧**执行 `internal/store/sqlite` 的真实迁移路径到**进程内 in-memory 库**再读 `pragma_table_info`（不落盘、不联网、只读 schema 元数据；比解析 `db.go` 文本更忠实，能看到增量 `ALTER`），并从 `notnull`/`pk` 推出有效可空性（**PRIMARY KEY 隐含非空**，与 PG 一致）；**PG 侧**只解析 `migrations/*.up.sql`，**不连库**，且**按文件名顺序逐条应用** `CREATE TABLE`、`ALTER TABLE`（`ADD/DROP/RENAME COLUMN`、`SET/DROP NOT NULL`、`SET/DROP DEFAULT`）、`RENAME TO`、`DROP TABLE` —— **按序应用是硬要求**：`values_revisions.parent_revision_id`（`DROP NOT NULL`）与 `candidate_artifacts.last_seen_at`（只经 `ADD COLUMN` 加入、PostgreSQL 侧为 `NOT NULL`）都靠后续迁移决定可空性，只读 `CREATE TABLE` 会分别**假报**与**漏报**（Lead 首测即因此假阴性）。注意：当前 `migrations/*.up.sql` 里**没有任何 `SET NOT NULL`**（`DROP NOT NULL` 只有 `values_revisions` 一处）⇒ 该解析能力目前由**合成用例**钉住，真实数据无覆盖。类型按「SQLite 存储类 vs PG 类型应有的存储类」归一化（`TIMESTAMPTZ`/`TEXT`→text、`BIGINT`/`BOOLEAN`→integer、`BYTEA`→blob、`JSONB`→text 或 blob），因此不会因两引擎类型词汇不同而误报。可空性判定只看 `NOT NULL` 与 PK：**`CHECK` 不蕴含全局非空**（`CHECK (parent_revision_id IS NOT NULL OR version = 1)` 就是反例），单引号字面量会被屏蔽（`DEFAULT 'NOT NULL'` 不是约束）。**同名表**的列缺失/类型不一致/**可空性不一致** → 失败；**仅存在于一侧的表** → 只报告不失败（两引擎表集合本就不同）。**范围外（不承诺）**：默认值**文本**（只比「有无默认」的布尔，两引擎的默认表达式写法本就不同）、`CHECK`/`UNIQUE`/`FOREIGN KEY` 等约束、索引。负控制：`TestDiffNegativeControlTypeMutation`（改一个列类型即变红）与 `TestSnapshotPostgresAppliesNullabilityInMigrationOrder`（不按序应用 `DROP NOT NULL` 即变红，实测变异过）+ 测试内其它变异用例。实现：`internal/quality/schemaparity` + `cmd/schemaparity` | 无 | 本地；`make quality` 已含（CI 未接入） |
| `make check-tasks` | 台账↔git 一致性门禁（D-η/η-1，兑现 `PROJECT-CONVENTIONS.md` 的「应由门禁校验」承诺）：vault `Tasks/*.md` 中 `status: done|closed` 的卡片必须 `merge_status: merged`、`pr_url` 非空，且它引用的**每一个** PR 在 git 中**真实合并**（一卡可列多个 PR，TASK-227）。证据两路：`gh pr list --json number,state,mergeCommit` 的 mergeCommit OID 必须存在于本地 `git log`；无 `gh` 时降级为本地 `git log` 的 `Merge pull request #N from` 匹配。拿不到证据 → `UNVERIFIED` 且**失败**，`ALLOW_UNVERIFIED_TASKS=1` 才降级为报告（**不会**放过台账矛盾）。缺 TASK 文档时与 `check-reqs` 同策略失败（`ALLOW_NO_REQS=1` 显式跳过）；`TASKS_DIR` 指向 vault `Tasks/`，未设时从 `REQS_DIR` 的兄弟目录推导。实现：`cmd/taskcheck` + `internal/quality/taskcheck`；负控制 `TestCheckNegativeControlDoneButUnmerged`（`done` + `unmerged` 必须报违规） | 无（git；`gh` 可选） | 本地；`make quality` 已含（CI 未接入） |
| 审计写入结构门禁 | `go test ./internal/store/` 的 `TestAuditWritesStayOnSanctionedPaths`：除登记的 6 个 store 文件外，任何 `INSERT [OR IGNORE] INTO audit_events` 都失败；事务写入者必须调用 `store.SanitizeAuditEvent` 兜底脱敏（负控制在同文件） | 无 | CI `test` job 的全量 `go test` |
| `make web-install` | 按 `web/package-lock.json` 安装前端依赖（`npm ci`）：锁文件即契约，避免 `node_modules` 漂移 | Node（`web/package.json` 的 `engines`: ^20 \|\| ^22 \|\| >=24）+ 网络 | 本地；CI 的 `web` job 用同一 `npm ci`（`.github/workflows/test.yml`，`pull_request` 触发） |
| `make web-check` | 前端三关：`npm run lint`（eslint）+ `npm test`（vitest）+ `npm run build`（`vue-tsc -b && vite build`）。缺 `web/node_modules` 时**明确报错并指向 `make web-install`**，不以其它失败形式掩盖 <!-- check-docs:ignore web/node_modules npm 本地安装产物，gitignore 掉，干净检出中本就不存在 --> | 同上（需先 `make web-install`） | 本地 + `make quality` 已含；CI 的 `web` job 跑等价的三步（15 分钟超时，`pull_request` 触发） |
| `make check-store-surface` | 门禁 `internal/store` 的**接口面**：每个声明的方法必须在**非测试代码里、通过它的接口（或被**显式绑定**到该接口的具体类型）被调用**，否则要登记到 `storesurface.exceptions.yaml`（含理由与复核日期）。判定用 **`go/types`**（`golang.org/x/tools/go/packages`）解析真实调用点：**注释/字符串不算**、**同名方法跨接口不互相掩盖**（绑定判定刻意不用结构的 `types.Implements`：方法集相同时它会互相掩盖，反例已固化为单测）、被**显式绑定**到该接口的具体类型上的调用算活（不限 store 包内）。这是 TASK-221 排查的机检化（旧版按裸方法名 grep，漏报了 31 个真死方法且一行注释即可绕过）。检查项：未登记的零调用方法、已失效的例外条目、缺理由、复核日期已过 | `go`（纯静态，无外部依赖） | 本地 + `make quality` 已含；CI `store-surface` job |
| `make check-dead-methods` | 门禁**另一类盲区**：**未导出接收类型上的导出方法**。`check-store-surface` 只看接口方法，`golangci-lint` 的 `unused` 又**故意不报**这一形态（它保守假设该类型可能实现某个接口）⇒ 这类死代码两道门都看不见（TASK-226 手工删掉的 7 个正是如此，加回去两道门仍全绿）。判定用 **`go/types`** 回答三问：①**引用**——方法对象是否出现在 `TypesInfo.Uses`/`Selections`（覆盖调用、方法值、方法表达式；注释/字符串/同名方法不算）；②**接口成员**——加载图里**任一**接口（**含 stdlib 与第三方**：`database/sql.Result`、Casbin `persist.Adapter`、client-go `RESTClientGetter`、universe 的 `error`）是否以同名同签名声明它且接收者类型实现该接口；③**晋升与按名派发**——被内嵌（方法被提升）或名字出现在 `reflect` `MethodByName("X")` 字面量里的，视为存活。**②是核心防线**：变异实测把它关掉，本仓 691 个候选里 **671 个**被误报（生成 connect 客户端等接口实现），合成模块测试里 `sql.Result`/第三方风格接口/`error` 用例同步变红。未登记的不可达方法、已失效条目、缺理由、复核日期已过都会失败；例外登记在 `deadmethods.exceptions.yaml`（reason + review_by）。边界（**大多是**安全方向，**但有一条会过报**）：结构式接口成员判定会让「恰好满足某无关接口」的类型存活、跨模块消费者不可见；按名派发**只承认字符串字面量** ⇒ 运行时拼接的名字（`MethodByName("By"+"Computed")`）看不见，**确实被这样调用**的方法会被报出（唯一指向不安全方向的边界，独立复核 2026-09-30 合成模块实测；正因如此该 job **不进 required 清单**（仓库当前无分支保护；将来引入时应保持非必需））；默认**不加载测试文件**（只有测试引用的方法需登记理由）。实现：`internal/quality/deadmethods` + `cmd/deadmethods` | `go`（纯静态，无外部依赖） | CI 的 `dead-methods` job（**不在 required 清单上**——仓库当前尚无分支保护，故今天没有任何检查阻塞合并；将来引入分支保护时应继续保持它非必需）+ 本地 `make check-dead-methods`；**不在 `make quality`**（刻意保留） |
| `make check-licenses` | 校验所有**会进入产物**的依赖许可证（Go 默认构建闭包 + 前端生产依赖）：拒绝 GPL/AGPL/LGPL、SSPL、BUSL、Elastic 以及无许可证文件的依赖；同时校验根目录 `NOTICE` 未过期 | `go`（模块缓存）；前端部分需 `jq`，缺失时**显式报「未检查」**而非静默通过 | 本地 + CI `license-check` job（同一脚本、同一策略、同一例外文件 `license-exceptions.tsv`） |
| `make check-docs` | 文档事实门禁：`docs/**` 与各级 README 里写出的 `make <target>`、仓库路径、相对链接、`文件:行号` 引用必须与当前代码一致；无匹配即失败，陈述"某物不存在"的行用同行 `<!-- check-docs:ignore 理由 -->` 豁免 | 无 | 本地（CI 未接入该 target）；`make quality` 已含 |
| `make check-config-keys` | 配置键真实性门禁（REQ-094/TASK-094）：双向——①每个服务配置文件（`configs/*.dev.yaml`、`deploy/kustomize/**/configs/*.yaml`）的叶子键必须解析到 `ServiceConfig` 的 mapstructure 路径或 orchestrator 自有结构体 raw 段（`gc`/`emergency`/`trust`）；②`ServiceConfig` 每个叶子字段必须在 `cmd/`+`internal/` 非测试代码中存在选择器引用（allowlist 需附理由）。`TestFakeKeyFailsTheGate` 为负控制。实现：`internal/config/configkeys_gate_test.go` | 无（Go 测试） | 本地；`make quality` 已含（CI 未接入） |
| `make check-probes` | kustomize 探针真实性门禁（REQ-099/TASK-099）：`deploy/kustomize` 下每个 Deployment 容器必须有 `startupProbe`、每个探针显式 `timeoutSeconds`、readiness/liveness 路径分离（`/readyz` vs `/health`；配对例外须登记）。`TestProbeGateRejectsHistoricalShape` 用 REQ-099 前的真实形态作负控制。实现：`deploy/dev/probes_gate_test.go` | 无（Go 测试） | 本地；`make quality` 已含（CI 未接入） |
| `make lint-proto` | `buf lint`（`buf.yaml` 的 `STANDARD` 减去 3 条命名规则）。**注意**：`STANDARD` 不含 `COMMENT_*`，因此它通过**不代表** proto 注释完整；注释覆盖靠人工与 `api/proto` 变更评审保证，`buf.yaml` 内记录了不开 `COMMENT_*` 的理由。当前基线干净（`ReleaseMode` 的两个历史枚举值用同行 `buf:lint:ignore` 定点豁免并写明原因） | `buf`（缺失时 `make` 会 `go install`） | 本地（`make quality` 已含；CI 未接入该 target） |
| `make pr-merge-check PR=<n>` | 合并前门禁（**helper，不是 `make quality` 成员**）：`gh pr checks` 中任何 check 既非 `pass` 也非 `skipping` 即**拒绝合并**并列出阻塞项。为什么机械化：项目里"合并前必查 `gh pr checks`"被写下过两次、也被违反过两次（2026-09-18；2026-09-27 合并 #198 时误读了输出）—— 只写在散文里的规则不成立 | `gh`（需网络与已登录） | 本地；合并前手动运行 |
| `make quality` | `sdk-check` + `test-coverage` + `lint` + `check-reqs` + `check-error-codes` + `check-tasks` + `check-schema-parity` + `check-licenses` + `check-docs` + `check-config-keys` + `check-migrations` + `check-probes` + `lint-proto` + `web-check` 的聚合门禁 | 同各子项 | 本地；CI 不直接调用，而是分 job 跑等价命令 |
| `make test-install-sdk` / `test-upgrade-sdk` / `test-rollout-watch` | Helm Install / Upgrade / Rollout watch SDK 链路 | Docker + kind（rollout 另有 120 秒时长门禁） | 本地 + CI 对应 job（各 15 分钟超时） |
| `make test-rollback-sdk` | Rollback SDK 链路 | 无（in-memory storage + `kubefake`） | 本地（CI 未接入） |
| `make test-operator-image-sdk-only` | operator 镜像合规（内部先调 `make docker-build-operator` 产出并 `docker save` 镜像 tarball） | Docker | 本地 + CI `operator-image-sdk-only` job |

## 死代码盲区：`make check-dead-methods` 与 `make check-store-surface` 的分工

两个门禁回答的是**不同问题**，都建立在 `go/types` 上，都不看注释与字符串：

- `make check-store-surface` 问：**接口方法**有没有被调用（或有没有登记例外）。范围只限 `internal/store`
  声明的接口，判定要求调用**通过该接口**或通过**显式绑定**到它的具体类型。
- `make check-dead-methods` 问：**未导出接收类型上的导出方法**有没有被引用。这类方法不在任何接口上，
  所以第一个门禁看不见；`golangci-lint` 的 `unused` 也**故意不报**（它保守地假设该类型可能实现某个
  接口，而 Go 里确实无法从名字判断）—— 两道门都看不见正是本门禁存在的理由。

判定由三个条件合成，任一成立即视为存活：**引用**（`TypesInfo.Uses`/`Selections` 里出现该方法对象，
覆盖调用 / 方法值 / 方法表达式）、**接口成员**（加载图内任一接口 —— 含 stdlib 与第三方 —— 以同名同签名
声明它且接收者类型实现该接口）、**晋升或按名派发**（接收类型被内嵌；或方法名出现在 `reflect`
`MethodByName("X")` 字面量里）。第二条是防误报的核心：把它关掉做变异，本仓 691 个候选里会多出 **671** 个
误报（生成 connect 客户端等大量接口实现）。

边界（大多是"宁可不报、不可误报"的安全方向，**但有一条会过报**，必须知道）：结构式接口判定会让"恰好满足
某个无关接口"的类型存活；跨模块消费者不可见（因此**接收类型导出**的方法不在范围内）；按名派发**只承认
字符串字面量**——`MethodByName("X")` 算存活，而运行时拼接出的名字（如 `MethodByName("By"+"Computed")`）
**看不见**，因此**确实被这样调用**的方法会被报出（这是唯一指向不安全方向的边界，独立复核 2026-09-30
用合成模块实测；正因如此该门禁**不接 required CI**，直到调用点或前缀启发式被复核过）；默认
**不加载测试文件**，所以只有测试引用的方法会被报出，需要像 `check-store-surface` 的 `[test-utility]`
一样在 `deadmethods.exceptions.yaml` 登记理由与复核日期。真实动作（删除 / 接线 / 登记）仍由人决定：
门禁只保证"沉默不等于通过"。

## E2E（分阶段 runner）

唯一正式入口是 `cmd/e2e`（`make e2e-*` 只做薄转发与环境组装）。运行时业务写入**只经正式 Connect
API 与受限 client-go（restart 专用 patch 权限）**，不做数据库直写、不走测试旁路、不调用
helm/kubectl 子进程。

### 覆盖范围：`cmd/api` 已纳入 dev 环境（REQ-065 D2）

e2e（含 `make e2e-prerequisite`）覆盖 `deploy/kustomize/services/kustomization.yaml` 部署的**全部**服务：
webhook / orchestrator / auth / notifier / notification-sink / web **以及 `cmd/api`**（release-api，
`deploy/kustomize/services/api.yaml`）。api 是除 orchestrator 之外唯一的管理面**验签方**，纳入后它的
Ed25519 公钥验签（`cmd/api/main.go:64` 的 `jwtauth.ParseEd25519PublicKeyPEM`，以及 `-jwt-public-key`
缺失或非 Ed25519 时的启动 fail-closed）由 e2e 真实执行：`make dev-up` 等待它的 rollout 并探它的
`/readyz`，冒烟再带 e2e-runner 的 bearer 调 `QueryAuditEvents`（并断言无 bearer 时 401）。

> ✅ **live smoke 已跑通（2026-09-24）：`SMOKE SUMMARY: 44 pass, 0 fail`**，命令
> `REGISTRY_PORT=5009 DEV_K3D_API_PORT=6449 make e2e-prerequisite-ci`（非冲突端口），退出码 0；
> 4 条 api 断言在真实环境全部通过：`api /readyz 200 (http://localhost:8088/readyz)`、
> `api verified the EdDSA bearer against the public key (HTTP 200)`、
> `api rejects a tampered bearer (invalid token)`、`api rejects a missing bearer`；
> `/environment` 一致性也已扩到 **6 个服务**（含 api）。
>
> 途中修掉两个真实缺陷（都曾让 live smoke 跑不通，均非"环境不便"）：
> ① `deploy/docker/Dockerfile.api` **缺 `ARG GOPROXY`**（其它 Go Dockerfile 都有）⇒ 容器内回落到不可达的
> `proxy.golang.org`，`go mod download` 约 6 分钟后 `i/o timeout`（`docker_build_failed: build failed for release-api`）；
> ② fixture 的镜像/chart 引用**硬编码 `localhost:5001`**（`internal/devfixture/bundle.go`、`runner.go`）⇒
> `REGISTRY_PORT` 覆盖时 seed 报 `resolve fixture image localhost:5001/release-fixture:dev: not found`；
> 现由 `DEV_REGISTRY_HOST` seam 派生（`dev.sh` 从 `REGISTRY_PORT` 传入，**默认 5001 行为不变**）。

- **端口**：8082–8087 由原来那六个服务占满（8087 是 `web`），因此 api 用 **8088**，NodePort **30088**
  （`deploy/dev/lib/host.sh` 的 `DEV_PORTS` 含 8088，`dev.sh` 的 loadbalancer 映射为
  `8082-8088:30082-30088`）。
- **挂载**：只挂公钥 `release-manager-jwt-public`（与 orchestrator 同口径），**不挂**私钥；webhook /
  notifier 仍不挂任何 JWT key。该 least-privilege 划分由 `deploy/dev/dev_test.go` 在真实
  `kustomize build` 产物上断言。
- 单元测试仍是第一道防线（例如 `cmd/api/main_test.go:144` 的
  `TestAPIRegisterFailsClosedOnNonEd25519Key`）。

### 阶段模型

七个 canonical 阶段，注册顺序与依赖固定（未显式选择的前置**不会**自动执行；已选前置 fail/skip
会让下游记 `stage_skipped`）：

```
control-plane ─┬─> inventory ─┬─> release
               │              └─> isolation
               ├─> artifact
               ├─> emergency
               └─> restart
```

`inventory` 与 `artifact` 是唯一允许并发的只读阶段（`--parallel` 时两者同批执行），写阶段始终串行。
`release`/`isolation`/`restart` 被单独选择时各自执行 external guard（readiness、fixture 身份校验、
6 服务健康 + operator session online），因此可以独立运行。

`cmd/e2e` 的 flag 与默认值：`--stages=all`、`--timeout=5m`（restart 阶段覆盖为 10m）、
`--total-timeout=25m`、`--output-dir=./e2e-results`、`--parallel=false`、`--keep-on-failure=false`、
`--snapshot-full=false`、`--env-config`（**必填、无默认**）。

### `make e2e-env-config` 与 env-config

`e2e-env-config` 是**私有** target，由 `e2e-stage`/`e2e-all`/`e2e-cleanup` 在持锁后调用，把
REQ-065 的产物组装成唯一运行时配置 `data/e2e-env-config.yaml`：

| 字段 | 来源 |
| --- | --- |
| `environment_id`、`seed.fixture_version`、`clusters.control.name`、`restart_targets` | `data/dev-status.json`（先跑一次 `dev-status` 生成） |
| 逻辑键 → 服务端 ID（definitions/bundle/values_revision）、`operators` → `seed.e2e_operator_id` | `data/dev-fixture.json` |
| `k3d.kubeconfig` | `data/kubeconfig.yaml` |
| `credentials.e2e_runner.password_env` | 只写**环境变量名**，不写值 |

它必须是 `0600`：由 `mktemp` 生成后 `chmod 600` 再原子 `mv` 落位。内容是端点、集群 context 与
**env 变量名**引用——密码只存在于进程环境，绝不写入 YAML、日志或 artifact（契约禁止内联明文）。

它同时是 **fail-fast 门禁**，以下任一情形一律**退出码 2** 且不产出配置：`jq` 缺失、
`data/dev-status.json`/`data/dev-fixture.json` 不可读、缺 `environment_id`/`fixture_version`/任一端点、
缺 `e2e_operator_id`、缺控制集群名、`restart_targets.deployments` 不是**恰好 3 个互不相同**的名字、
或三个 `e2e-*-target` 定义缺 `id`/`bundle_id`/`values_revision_id`。错误信息本身给出修复指令
（如 `missing e2e operator id; run make dev-seed to publish data/dev-fixture.json operators`）。

注：`restart_targets` 的首选来源 `data/dev-deployments.json` 当前**没有任何生产者**，实际取自
`data/dev-status.json` 的 `restart_targets` 块（由 `dev-status` 从管理集群 Deployment 端口
8082–8085 动态派生）。

### `e2e-stage`、`e2e-all`、`e2e-cleanup`

| target | 行为 |
| --- | --- |
| `make e2e-stage STAGES=<list>` | 跑指定阶段（逗号分隔，省略即 `all`）。`STAGES` 会做集合语义校验：未知/重复阶段名 → 启动拒绝、退出码 2 |
| `make e2e-all` | 先做 pre-flight 自愈：若 `$(OUTPUT_DIR)/baseline.json` 存在，先跑一次 `make e2e-cleanup` 回收上一轮残留（`E2E_SKIP_PREFLIGHT_CLEANUP=1` 可跳过；无 baseline 则跳过）。**该 pre-flight 是 best-effort，失败不中止**，Run 仍以自身 fail-closed 检查判定。随后等价于 `STAGES=all` |
| `make e2e-cleanup` | 经 `cmd/e2e cleanup` 子命令、**只经正式业务 API**（`CancelOperation` / `RollbackRelease` / `EmergencyChange`）回收残留并恢复 baseline；缺 baseline 时降级为「只取消 runner 拥有的非终态 operation」并在 stderr 显式告警，不静默 |

三个 target 都在 `cmd/e2e` 启动前对 `data/dev.lock` 取**共享锁**（`flock -s`），与 `dev-*` 的排他锁
互斥；冲突立即以**退出码 3** 退出并打印 `environment_locked`（不写 `run.json`）。凭据来自
`data/dev-credentials.env`（存在则 source）或已注入的 `E2E_RUNNER_PASSWORD`，缺失时 target 直接失败。

回收语义有两处关键约束：

- **恢复预算不是 30 秒**。阶段内补偿的 grace 是 30 秒（fail-fast 上界，超时追加 `cleanup_timeout`
  cause）；独立 `cmd/e2e cleanup` 命令的预算是 **3 分钟**——必须跨越 Run 自身 `restart` 阶段造成的
  operator agent 重连窗口（实测约 32 秒）加至少一个 emergency operation 的 apply 窗口。两者不是
  同一个数，不得互相套用。
- **是否回滚以 Run 自己采样到的 residue 为准**。Run 在最后一个阶段结束后写 `residue.json`，cleanup
  只在「当前 revision == residue」时回滚。不能用「与 baseline 比较」代替——`RollbackRelease` 是推进
  版本号而非恢复编号，该比较在回滚后依然成立，会导致每次 cleanup 都再回滚一次、版本号持续累加。

### 失败产物与退出码

- 固定产物：`{output-dir}/run.json`（Run 级汇总，CI 只解析它）、`{stage}.json`（每个**已选**阶段
  一份，因依赖传播而 skip 的阶段也写，状态 `skip`）、`baseline.json`、`residue.json`。Run 进入执行前
  清空本 Run 将写的固定产物（不删其他用户文件），避免陈旧文件误导排查；`e2e-results/` 已在
  `.gitignore` 中。
- `--keep-on-failure=true` 时，失败阶段的诊断写 `{output-dir}/diagnostics/{run_id}/{stage}/`
  （阶段 stderr 缓冲 + 关联 Operation/artifact 引用 + 环境摘要），随 CI artifact 上传。业务补偿与
  非终态取消**始终执行**，不受该 flag 影响；Runner 不收集集群内 Pod 日志（权限边界）。
- 日志三面分离：stdout 只有人类摘要、stderr 是 `log/slog` 诊断、结构化结果只进 JSON artifact。
- 退出码：`0` = 至少一个已选阶段 pass 且无 fail（允许部分 skip）；`1` = 至少一个阶段 fail，或
  run-fatal（`fixture_stale` / `snapshot_not_found`，`run.json` 写 `fatal` 字段）；`2` = 全部已选阶段
  skip，或启动校验/配置错误（不写 `run.json`）。`3` 是 **Makefile 目标级**退出码
  （`environment_locked`），不属于 `cmd/e2e` 进程退出码表。
- 脱敏：`RootCause` / `ErrorCause.Message` 禁止输出堆栈、JWT、Secret payload、Values 内容、内部 IP
  与连接串；`safeErrorMessage` 命中敏感词时只回一句通用文案。

### AC-066-17 前置冒烟

`make e2e-prerequisite`（= `dev-up` + `dev-seed` + `dev-status` 后跑 `test/e2e/prerequisite/smoke.sh`）
是版本化的上游链路门禁：只经正式 Connect API 复核 Upgrade、CancelOperation、Rollback、
Emergency `SetReplicas`、operator enrollment/reconnect、e2e-runner 登录，以及「Auth 重启后旧
access/refresh token 仍有效」这一 restart 阶段前置。它是一条 **target 级依赖链**——`dev-status`
不隐含于 `dev-up`/`dev-seed`，漏掉它会让冒烟读到陈旧或缺失的 `data/dev-status.json`。结果落
`data/smoke-result.json`。

`make e2e-prerequisite-ci` 是它的 CI 变体：失败时先 `capture-logs.sh`、把 `smoke-result.json` 复制到
`e2e-results/`，最后无条件 `make dev-purge CONFIRM=1`。**它是破坏性的**，本地跑会清掉自己的 dev 环境。

## CI（`.github/workflows/test.yml`）

触发条件：`push` 到 `main`、任意 `pull_request`、以及 `workflow_dispatch`（带 boolean 输入
`run-e2e`，默认 `true`）。`concurrency` 组按分支/PR 取消在跑的旧 run；runner 通过
`vars.RUNS_ON || 'ubuntu-latest'` 选择，便于在私有仓额度受限时切自托管。

| job | 跑什么 | 触发范围 |
| --- | --- | --- |
| `sdk-check` | `go run ./cmd/sdkcheck/ -exceptions sdkcheck.exceptions.yaml ./...` | 全部触发 |
| `vulncheck` | `make vulncheck`（安装固定版本 govulncheck 后跑 `scripts/vulncheck.sh`） | 全部触发 |
| `license-check` | `make check-licenses`（10 分钟超时；读模块缓存里的 LICENSE 文本与前端 lockfile，不安装额外扫描器） | 全部触发 |
| `install-sdk` | `make test-install-sdk` | 全部触发 |
| `upgrade-sdk` | `make test-upgrade-sdk` | 全部触发 |
| `operator-image-sdk-only` | `make test-operator-image-sdk-only` | 全部触发 |
| `test` | 全仓（排除 sqlite/sdkcheck 两包）`go test -race` + coverage + 变更行 lint | 全部触发 |
| `test-sqlite` | `go test -race ./internal/store/sqlite/...` | 全部触发 |
| `test-sdkcheck` | `go test -race ./internal/quality/sdkcheck/...` | 全部触发 |
| `rollout-watch` | `make test-rollout-watch` | 全部触发 |
| `e2e-prerequisite` | `make e2e-prerequisite-ci`（45 分钟超时，`if: always()` 上传 artifact） | 全部触发（不需要任何 secret） |
| `e2e` | `make dev-up` → `make dev-seed` → `make e2e-all` → `if: always()` 上传 `e2e-results/` → `make dev-purge CONFIRM=1` | **push main + 手动触发；PR 不跑** |
| `docs-check` | `make check-docs`（5 分钟超时；文档写出的 `make <target>`、仓库路径、相对链接、`文件:行号` 引用必须为真） | 全部触发（只需 bash/grep/git，不需要 Go 与任何 secret） |
| `proto-check` | `make lint-proto` + `make proto` 后要求 `api/gen`、`web/src/gen` 与提交内容一致（10 分钟超时） | 全部触发（`GITHUB_TOKEN` 仅用于 buf 版本查询） |

`proto-check` 存在的原因不是「多跑一次生成」，而是补齐一个真实的检查缺口：`test` 与
`test-sqlite` 都会在测试前执行 `make proto`，于是**忘提交生成物**时它们测的是新生成的代码，
永远绿；真正落到产物里的却是仓库里那份过期的 `api/gen`。这里用 `git status --porcelain`
而非 `git diff` 判定，因为新增一个 proto 会带出**未跟踪**的生成文件，`git diff` 看不见它。

`e2e` job 的触发条件是 `github.event_name == 'push' || (github.event_name == 'workflow_dispatch' && inputs.run-e2e)`，
并设 `DEV_PROFILE=ci`、`E2E_RUN_ID`、`E2E_ENVIRONMENT=ci` 与 9 个 repository secret（4 个账号密码 +
`DEV_JWT_PRIVATE_KEY`（PKCS#8 Ed25519 PEM；REQ-065 AC-065-01，公钥由 devseed helper 派生）+ `DEV_WEBHOOK_SERVICE_TOKEN` + `DEV_M_TLS_CA_KEY`/`DEV_M_TLS_CA_CERT` +
`DEV_TRUST_ROOT_PRIVATE_KEY`）。清理兜底在 `if: always()` 的 post-step 执行，失败/取消也会尝试拆环境。

关于 D-034：该 job 曾因上述 secret 未配置、而原 `if:` 会在 push main 时并行独立执行，被**临时**
收窄为仅手动触发——目的是不让 main 因「交付前置条件缺失」而非代码缺陷转红，而不是取消门禁。
偏离已于 2026-09-14 解除：9 个 secret 配齐、手动实跑 10 个 job 全部 success、`e2e` job 13m44s
落在 30 分钟硬上限内，`if:` 恢复为现在的形态。这段历史留下的两个教训仍适用：**门禁必须从干净
状态验证**（本地残留的 k3d 版本、陈旧的 `dev-status.json` 会造成假通过，比失败更危险），以及
**「flake」结论必须由日志证据支撑**。

## 环境型 CI 门禁的归属（2026-09-28 策略）

`e2e-prerequisite`（AC-066-17 前置冒烟，`make e2e-prerequisite-ci`）**只在 `push main` 与 `workflow_dispatch` 运行，不在 PR 上运行** —— 与正式 `e2e` gate 同属 **REQ-066 决策 ⑧** 的模式。

**为什么**：它要起真实的 k3d 环境（`dev-up dev-seed dev-status` → `smoke.sh` → `dev-purge`），实测单次 **15–55 分钟**。2026-09-22 有**四个已批准的改动**同时卡在它后面，而它当次失败是**环境原因**（一次 55 分钟被取消且 `--log-failed` 无根因；一次 seed 撞上正在终止的 pod，`unavailable: unexpected EOF`）。

**覆盖没有减少**：`push main` 与手动触发仍然跑它、仍然上传 `e2e-results/` 证据；环境侧根因（seed 收敛重试预算）在 `internal/devfixture` 修复。

**这不算"放宽门禁"**：门禁本身没变（同样的命令、同样的 job、同样的 `timeout-minutes: 45`、同样的证据上传），变的是**它挂在哪条触发路径上** —— 与项目对 `e2e` 的既有处理完全一致。任何**删除**该 job 或**放宽其断言**的做法仍然禁止（`AGENTS.md`）。

## 写测试的约定

- **table-driven + testify**：用例表驱动，断言用 `github.com/stretchr/testify`（`require` 用于必须
  中止的前置断言，`assert` 用于可继续的取值断言）。
- **断言必须排除"没有响应"这一分支**：只断言"响应里没有错误串"的检查，在 **HTTP 000 / 空响应**
  （服务没起来、端口不通、curl 直接失败）时会**假通过** —— 空 body 天然不含任何错误串，于是"没有
  验证"被记成了 PASS。实测例：`test/e2e/prerequisite/smoke.sh` 的 `release-api` 公钥验签断言最初
  写成"body 不含 `invalid token` / `missing authorization header`"，指向**死端口**时它照样 PASS；
  加上 `code != 000` 守卫后才 FAIL。凡"没有 X 就算通过"的断言，都必须同时钉住"**确实拿到了响应**"
  （HTTP 状态码、非空 body、或明确的成功码），否则它与不检查等价。
- **live-DB 测试必须打 `//go:build integration`**，并在 `POSTGRES_TEST_DSN` 未设置时
  `t.Skip("POSTGRES_TEST_DSN is not set")`；两者都要——缺标签会让它在无数据库的机器上被编译执行，
  缺 skip 会让它在 CI 的默认 job 里失败。
- **集成命令禁用 Go test cache**：各 SDK 门禁以 `-count=1` / `-test.count=1` 运行。
- **SDK-only 约束对测试也生效**：`sdkcheck` 扫描 `cmd/`、`internal/`、`pkg/` 的 AST 与依赖图，规则
  为 `os_exec_import`、`fork_exec`、`shell_wrapper`、`forbidden_binary_invocation`、`expired_exception`。
  例外只能写在 `sdkcheck.exceptions.yaml`，每条必须含 `owner`/`reason`/`expires_at`/`path`/`rule`；
  任一字段缺失、规则未知、日期非法或已过期一律**不抑制违规（fail closed）**。kind/docker 等 CLI
  只允许出现在 Makefile/CI/开发环境生命周期脚本中，不得进入运行时镜像或运行时业务代码。
- **脱敏与清理**：测试与 runner 输出不得含密码、DSN、JWT、Secret payload、Values 内容或内部 IP；
  临时集群/容器/凭据必须由创建者清理（`trap` 保证中断也执行），优先复用 `make dev-purge CONFIRM=1`，
  不得为换取门禁通过而停用常驻服务。

> 事实源：`Makefile`（test* / sdk-check / lint / check-reqs / quality / e2e-* 目标逐条核对）、
> `.github/workflows/test.yml`（13 个 job 与触发条件）、
> `cmd/e2e/main.go`（flag、退出码 0/1/2 与 `exitLock=3`、cleanup 语义）、
> `test/e2e/runner.go`（`canonicalStageOrder`、`CanonicalDependencies`、`batchFor`）、
> `test/e2e/prerequisite/smoke.sh`、`test/integration/`、
> `internal/store/postgres/`、`web/package.json`；
> `Projects/001-release-manager/Requirements/REQ-037`、`REQ-061`~`REQ-064`、`REQ-066`；
> `Notes/contracts/e2e-runner-surface.md`、`Notes/contracts/e2e-environment-config.md`；
> `Notes/decisions/D-021`、`D-032`、`D-033`、`D-034`。

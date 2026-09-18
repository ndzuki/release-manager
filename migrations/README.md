# migrations/ — PostgreSQL 版本化迁移（golang-migrate）

**先说清边界**：本目录只承载 **PostgreSQL 侧**的版本化 schema 迁移（`migrations/embed.go:1` 的包注释与 `:12-13` 的 `//go:embed *.sql` 即写明 "versioned PostgreSQL schema migrations"）。
dev/test 使用的 **SQLite** 引擎的 schema **不在这里**——它内建在 Go 代码 `internal/store/sqlite/db.go` 中（权威表述：docs/architecture.md:123-125「Postgres 迁移以 golang-migrate 版本化 SQL 为唯一权威，禁止 GORM AutoMigrate；SQLite schema 内建在 Go 中」；AGENTS.md 硬性约束 4）。每条结论附 `文件:行号`。

## 1. 迁移工具与应用方式

- 工具：`github.com/golang-migrate/migrate/v4`，版本 `v4.19.1`（go.mod:15），经 `iofs` 源 + 嵌入式 `embed.FS` 运行（internal/postgres/migrate.go:15-17、59；migrations/embed.go:12-13）。文件不依赖磁盘路径，随二进制编译进各服务。
- **何时自动执行**：任何以 postgres 驱动打开 Store 的服务在**启动时**跑 `Up()`——`internal/store/postgres/db.go:182` 在 `Open()` 内调用 `RunMigrations`，调用方：`cmd/auth/main.go:96`、`cmd/orchestrator/main.go:531`（二者共用 `migrations.FS`，即共享的 `release_manager` 库）与 `cmd/notifier/main.go:107`（用 `migrations.ReleaseNotifierFS()`，独立 `release_notifier` 库；迁移失败即中止启动，见其注释 cmd/notifier/main.go:92-95）。`ErrNoChange` 视为成功（internal/postgres/migrate.go:23-24、79-81）。
- 一次性数据搬迁：`cmd/store-migrate`（SQLite → PostgreSQL 全量搬迁：先对目标跑 golang-migrate，再拷数据并校验；flag `--source`、`--target-dsn`（env `RELEASE_MANAGER_DATABASE_DSN`）、`--migrations` 默认 `migrations`——cmd/store-migrate/main.go:35-39、57-66；整体流程 docs/architecture.md:144-149）。
- dev 环境重建：`make dev-reset-data` 通过 `devseed --reset` 对两个 PostgreSQL 库执行 **migrate down -all → up**（deploy/dev/dev.sh:1687-1691 注释），seam 在 `internal/devfixture/reset.go:64、71`。
- 不存在独立的 `migrate` CLI 入口 make 目标（Makefile 中与迁移相关的只有 `dev-reset-data`）；验证请用 §5 的测试命令。

## 2. 编号规则与现状

命名：`NNNNNN_snake_case_name.{up,down}.sql`。规则由代码强制校验（internal/postgres/migrate.go:158-215 `validateMigrationNames`，服务启动跑迁移前必经）：

- 文件名前缀必须是正整数版本号（:178-182）；
- 同一版本号不得有重复的 up 或 down（:184-194）；
- **版本号必须从 1 连续**（:204-208 `migration versions must be contiguous from 1`）；
- **每个版本号必须同时有 up 与 down**（:210-212）。

现状（本次统计，`ls migrations/*.sql` + 逐一核对）：

- 主集合（`release_manager` 库）：`000001_legacy_baseline` … `000026_emergency_lock_release`，**26 个 `.up.sql` + 26 个 `.down.sql`**；000001–000026 每个编号的 up/down 文件均存在，**编号连续、无缺号**（与 docs/architecture.md:123 「当前 26 组」一致）。
- `release_notifier/` 子集合：仅 `000001_create_notification_jobs.{up,down}.sql` 1 组（REQ-031 PostgreSQL 契约，embed.go:15-18）。
- 新增迁移取对应库的下一个连续编号（主集合当前应取 `000027_*`）。

## 3. 双引擎约束：新增变更必须两侧同时覆盖

两套引擎**不是同一目录里的两组文件**，而是两种机制：

| 引擎 | schema 载体 | 触发点 |
| --- | --- | --- |
| PostgreSQL（prod / 集群内 dev） | 本目录版本化 SQL（golang-migrate，`schema_migrations` 表记录版本） | 服务启动 `Open()`（§1） |
| SQLite（dev/test） | `internal/store/sqlite/db.go` 内嵌 DDL：`migrateLegacy` 有序增量步（:492、:501 起）+ 空库快速路径 `migrateFresh`/`cloneFreshSchema`（:348-366、:565） | `sqlitestore.Open` → `migrate()`（:91） |

AGENTS.md 约束 4 要求「迁移写在 `migrations/` 且编号连续，SQLite 侧结构在同一变更内对齐」。**照着的真实例子**——`notification_jobs` 表两侧的同构写法：

- PostgreSQL 侧：`migrations/release_notifier/000001_create_notification_jobs.up.sql:1-5` 的文件头注释就是对齐声明（"Column set mirrors the SQLite schema (internal/store/sqlite/db.go) with PostgreSQL types: TIMESTAMPTZ time columns, JSONB metadata, table-level UNIQUE"）；表级类型见 :6-27（如 `next_retry_at TIMESTAMPTZ`、`metadata JSONB NOT NULL DEFAULT '{}'`、`UNIQUE (operation_id, channel, recipient)`）。
- SQLite 侧同一变更：`internal/store/sqlite/db.go:1415` 起 `CREATE TABLE IF NOT EXISTS notification_jobs`，时间列用 `TEXT`、BLOB/TEXT 存 JSON，去重用**独立部分唯一索引** `idx_notification_jobs_dedup`（:1439，`WHERE status NOT IN ('delivered', 'dead_letter')` —— 仅非终态去重，REQ-031 AC-031-10），后续列增补写成 `ALTER TABLE notification_jobs ADD COLUMN next_retry_at TEXT` 等（:1442-1445）。

类型映射约定（由上述对照归纳，均可在两侧文件中逐行验证）：`TIMESTAMPTZ ↔ TEXT`、`JSONB ↔ BLOB/TEXT`、表级 `UNIQUE(...) ↔ CREATE UNIQUE INDEX`、`BIGINT ↔ INTEGER`。

## 4. 回滚策略现状

- **每个版本都有 down 文件**（主集合 26 组 + notifier 1 组），且这是代码强制：缺 down 直接拒绝加载（internal/postgres/migrate.go:210-212）。因此 down 迁移是**既定策略**，不是缺口。
- 回滚执行路径：`RunMigrationsDown` 按步数回退（migrate.go:31-40），其注释明确「intended for explicit operator rollback, never normal service startup」；当前唯一的仓库内调用方是 dev 的 reset 流程（down 到 baseline 后重新 up，internal/devfixture/reset.go:64、71）。生产回滚没有自动化入口（未找到；不要臆造）。
- 小体量参考例：`000002_add_archived_columns.up.sql:1-2` 加两列，`000002_add_archived_columns.down.sql:1-2` 逆序 DROP——down 要能抵消 up 的全部对象（表/列/索引/约束）。

## 5. 校验与常见失败

- **校验命令**（只读源码/测试即可跑）：
  - `go test ./internal/postgres/...`（`migrate_test.go` 覆盖文件名/连续性/pairing 校验；`migrate_integration_test.go` 需真库）；
  - `go test -race ./internal/store/sqlite/...`（SQLite 侧；CI 里对应 `test-sqlite` **job**（`.github/workflows/test.yml:202`），本仓库**没有**同名 make 目标）；
  - 真 PostgreSQL 测试必须带 `//go:build integration` 且提供 `POSTGRES_TEST_DSN`，默认 skip（AGENTS.md「质量门禁」、docs/architecture.md:121）；
  - 服务启动即校验：任何不连续/缺 pair/重名都会以 `migration_failed` 中止启动（migrate.go:181-213）。
- **历史编号复用坑**：000007/000008 曾承载过不同内容，`repairHistoricalMigrationVersion` 在版本 ≥7/8/9/10 且对应特征表缺失时把 `schema_migrations.version` 回退到最早安全重放点（migrate.go:92-155）；被重放的迁移刻意写成幂等——`ADD COLUMN IF NOT EXISTS` / `CREATE TABLE IF NOT EXISTS`（见 `000009_upgrade_result.up.sql:1-6`、`000010_operation_timeline_effect_status.up.sql`）。**新写重放段内的迁移请沿用该幂等风格**（重放文件清单：000009/000010/000016/000021/000022/000024/000025/000026 含 `IF NOT EXISTS`）。
- **方言差异（代码可证实）**：
  - SQLite 无 `TIMESTAMPTZ`/`JSONB`，PostgreSQL 侧时间戳/JSON 列在 SQLite 用 `TEXT`/`BLOB`（§3 对照）；SQLite 里 `ALTER TABLE ADD COLUMN` **非幂等**，legacy 循环靠 `PRAGMA table_info` 预检跳过已存在列（db.go:529-534、952）——写 SQLite 步不要依赖报错兜底。
  - 结构性改造（改列形状）在 SQLite 侧一律走 **RENAME 到 `_legacy` + 建新表 + 回填**的 rebuild 模式（db.go:723-724、806、854、916），不是原地 ALTER；PostgreSQL 侧则可直接 `ALTER ... ADD/DROP`（`000002`）。
  - SQLite 连接强制 `PRAGMA journal_mode=WAL` 与 `PRAGMA foreign_keys=ON`（db.go:81-86）：外键在 SQLite 默认不开，跨引擎 SQL 不能假设外键行为一致。
- **禁止**：绕过 `migrations/` 用 GORM AutoMigrate 建/改 schema（docs/architecture.md:125）；只改一侧引擎的 schema（AGENTS.md 约束 4）；跳号或只加 up 不加 down（migrate.go 校验会直接拒绝）。

> 事实源：`migrations/embed.go`、`migrations/*.sql`（数量与编号连续性统计）、`migrations/release_notifier/000001_create_notification_jobs.up.sql`、`go.mod`、`internal/postgres/migrate.go`、`internal/store/postgres/db.go`、`internal/store/sqlite/db.go`、`internal/devfixture/reset.go`、`internal/migration/migrate.go`、`cmd/store-migrate/main.go`、`cmd/auth/main.go`、`cmd/orchestrator/main.go`、`cmd/notifier/main.go`、`Makefile`、`deploy/dev/dev.sh`、`.github/workflows/test.yml`、`docs/architecture.md`、`AGENTS.md`

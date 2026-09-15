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

`web/package.json` 提供 `test`（`vitest run`）、`test:e2e`（`playwright test`）、`lint`（`eslint .`）；
Makefile 内没有对应的转发 target，需在 `web/` 目录内直接运行。

## 命令矩阵

| target | 覆盖什么 | 外部依赖 | 本地 / CI |
| --- | --- | --- | --- |
| `make test` | 全部包 `go test -race ./...` | 无 | 本地 + CI（CI 拆包见上） |
| `make test-coverage` | 同上 + coverage profile | 无 | 本地 + CI（`test`/`test-sqlite`/`test-sdkcheck` job 各自产覆盖） |
| `make lint` | `golangci-lint run` | `golangci-lint` | 本地；CI 用 `golangci-lint-action` v2.12.2 且仅 lint 变更（`--new-from-rev`） |
| `make sdk-check` | SDK-only 静态门禁（REQ-037）：`os_exec_import`、`fork_exec`、`shell_wrapper`、`forbidden_binary_invocation`、`expired_exception` | 无 | 本地 + CI `sdk-check` job（同一命令、同一例外文件、同一扫描范围） |
| `make check-reqs` | 校验原子需求文档结构（`find . -path '*/Requirements/REQ-*.md'` → `cmd/reqcheck`）；**找不到 REQ 文档时打印提示并跳过** | 无 | 本地（CI 未接入该 target） |
| `make check-licenses` | 校验所有**会进入产物**的依赖许可证（Go 默认构建闭包 + 前端生产依赖）：拒绝 GPL/AGPL/LGPL、SSPL、BUSL、Elastic 以及无许可证文件的依赖；同时校验根目录 `NOTICE` 未过期 | `go`（模块缓存）；前端部分需 `jq`，缺失时**显式报「未检查」**而非静默通过 | 本地 + CI `license-check` job（同一脚本、同一策略、同一例外文件 `license-exceptions.tsv`） |
| `make check-docs` | 文档事实门禁：`docs/**` 与各级 README 里写出的 `make <target>`、仓库路径、相对链接、`文件:行号` 引用必须与当前代码一致；无匹配即失败，陈述"某物不存在"的行用同行 `<!-- check-docs:ignore 理由 -->` 豁免 | 无 | 本地（CI 未接入该 target）；`make quality` 已含 |
| `make lint-proto` | `buf lint`（`buf.yaml` 的 `STANDARD` 减去 3 条命名规则）。**注意**：`STANDARD` 不含 `COMMENT_*`，因此它通过**不代表** proto 注释完整；注释覆盖靠人工与 `api/proto` 变更评审保证，`buf.yaml` 内记录了不开 `COMMENT_*` 的理由。当前基线干净（`ReleaseMode` 的两个历史枚举值用同行 `buf:lint:ignore` 定点豁免并写明原因） | `buf`（缺失时 `make` 会 `go install`） | 本地（`make quality` 已含；CI 未接入该 target） |
| `make quality` | `sdk-check` + `test-coverage` + `lint` + `check-reqs` + `check-licenses` + `check-docs` + `lint-proto` 的聚合门禁 | 同各子项 | 本地；CI 不直接调用，而是分 job 跑等价命令 |
| `make test-install-sdk` / `test-upgrade-sdk` / `test-rollout-watch` | Helm Install / Upgrade / Rollout watch SDK 链路 | Docker + kind（rollout 另有 120 秒时长门禁） | 本地 + CI 对应 job（各 15 分钟超时） |
| `make test-rollback-sdk` | Rollback SDK 链路 | 无（in-memory storage + `kubefake`） | 本地（CI 未接入） |
| `make test-operator-image-sdk-only` | operator 镜像合规（内部先调 `make docker-build-operator` 产出并 `docker save` 镜像 tarball） | Docker | 本地 + CI `operator-image-sdk-only` job |

## E2E（分阶段 runner）

唯一正式入口是 `cmd/e2e`（`make e2e-*` 只做薄转发与环境组装）。运行时业务写入**只经正式 Connect
API 与受限 client-go（restart 专用 patch 权限）**，不做数据库直写、不走测试旁路、不调用
helm/kubectl 子进程。

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
`DEV_JWT_SIGNING_KEY` + `DEV_WEBHOOK_SERVICE_TOKEN` + `DEV_M_TLS_CA_KEY`/`DEV_M_TLS_CA_CERT` +
`DEV_TRUST_ROOT_PRIVATE_KEY`）。清理兜底在 `if: always()` 的 post-step 执行，失败/取消也会尝试拆环境。

关于 D-034：该 job 曾因上述 secret 未配置、而原 `if:` 会在 push main 时并行独立执行，被**临时**
收窄为仅手动触发——目的是不让 main 因「交付前置条件缺失」而非代码缺陷转红，而不是取消门禁。
偏离已于 2026-09-14 解除：9 个 secret 配齐、手动实跑 10 个 job 全部 success、`e2e` job 13m44s
落在 30 分钟硬上限内，`if:` 恢复为现在的形态。这段历史留下的两个教训仍适用：**门禁必须从干净
状态验证**（本地残留的 k3d 版本、陈旧的 `dev-status.json` 会造成假通过，比失败更危险），以及
**「flake」结论必须由日志证据支撑**。

## 写测试的约定

- **table-driven + testify**：用例表驱动，断言用 `github.com/stretchr/testify`（`require` 用于必须
  中止的前置断言，`assert` 用于可继续的取值断言）。
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
> `Design/contracts/e2e-runner-surface.md`、`Design/contracts/e2e-environment-config.md`；
> `Design/decisions/D-021`、`D-032`、`D-033`、`D-034`。

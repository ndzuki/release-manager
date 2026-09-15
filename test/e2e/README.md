# test/e2e/ — 分阶段 E2E runner（REQ-066）

Go 包，提供**基于正式 Connect API 的分阶段端到端验证框架**与七个 canonical 阶段的实现。可执行入口是仓库根的 `cmd/e2e`（`go run ./cmd/e2e run|cleanup`），它 import 本包组装阶段图（cmd/e2e/main.go:22-23）。浏览器端 Playwright E2E 在 `web/e2e/`，与本目录无关。阶段模型的文档权威在 `docs/testing.md:65-167`，本文不重复，只标注目录内证据 `文件:行号`。

## 1. 目录内容

```
test/e2e/
├── *.go                框架本体（package e2e）：
│   ├── stage.go        Stage/StageSpec 抽象 + canonical 阶段名与顺序（:14-36）+ fail-closed 契约（:62-97）
│   ├── runner.go       Harness 调度与依赖图 CanonicalDependencies（:14-24）
│   ├── config.go       env-config 严格 YAML 加载/校验（LoadConfig :222 起，KnownFields(true)）
│   ├── clients.go / session.go / live.go   Connect client 束与 e2e-runner 会话（REQ-065 账号，session.go:16）
│   ├── cleanup.go / recovery.go / compensation.go   残留回收与补偿（只经正式 API，cleanup.go:45-54）
│   ├── snapshot*.go / result.go / lock.go   快照比对、工件 schema（run.json/baseline/residue，result.go:134-142）、锁
│   └── readiness.go / lifecycle.go / prerequisite 辅助
├── stages/             7 个 canonical 阶段的实现（读写适配器 + 错误码，如 write.go:12-22）
├── livewire/           真实环境的阶段图组装：读端点（readers.go）、Connect 写路径、client-go 观察器；
│                       SpecsForRun() 是 cmd/e2e 的唯一装配入口（livewire/specs.go:72-79）
└── prerequisite/       AC-066-17 前置冒烟：smoke.sh（上游链路端到端复核）+ capture-logs.sh（诊断留存）
```

这里没有测试数据夹具目录：Development Fixture 的权威实现在 `internal/devfixture`（由 `cmd/devseed` 执行，docs/dev-environment.md「开发夹具」节）；`data/dev-fixture.json` 是它的运行时产物。本目录的 `Fixture`（fixture.go:6-11）只是阶段间传递快照数据的内存结构。

与 `cmd/e2e` 的分工：`cmd/e2e/main.go` 只做 CLI 装配（flag 解析 :105-121、阶段选择 `parseStages` :677-700、baseline/residue/stage/run 四类工件写入 :256/:285/:364/:423）；阶段语义全部在本包。

## 2. 阶段清单（canonical 顺序与依赖）

顺序定义 `test/e2e/stage.go:28-36`；依赖边 `test/e2e/runner.go:16-24`；每个 canonical 名必须绑定实现，否则组装失败（livewire/specs.go:124-133）。验证需求见各阶段文件头部注释：

| 阶段 | 依赖 | 验证什么 | 证据 |
| --- | --- | --- | --- |
| `control-plane` | — | readiness、环境一致性（`GET /environment`）、operator 会话存活；**不改任何状态** | stages/control_plane.go:192-193；readers.go:36-45 |
| `inventory` | control-plane | 完整观测 inventory 与 seed manifest 身份一致；不一致 = run-fatal `fixture_stale` | stages/inventory.go:77-78 |
| `artifact` | control-plane | `BundleService.GetBundle` 验证 bundle + 经 `ListReleaseInventory` 核对发布身份；只读 | stages/artifact.go:54-56 |
| `release` | inventory | 对 release target 做一次可逆 UPGRADE 并登记恰好一条 LIFO rollback 补偿（AC-066-02/13/22/31） | stages/release.go:12-19 |
| `isolation` | inventory | 对专用 isolation target 重复可逆升级，并断言 release target 行**未被移动**（跨租户/目标隔离；AC-066-14/15/18） | stages/isolation.go:15-18、62、105-116 |
| `emergency` | control-plane | 一次可逆 `SET_REPLICAS` 紧急变更（固定 2 副本，理由见 livewire/specs.go:14-38），经只读 observer 验证 applied effect，登记一条恢复补偿 | stages/emergency.go:111-113 |
| `restart` | control-plane | 对三个控制面 Deployment 打 restart 注解并等待完整恢复屏障（rollout 收敛 + 服务就绪 + operator 重新上线）；不登记补偿，屏障本身即恢复 | stages/restart.go:49-52、26-30 |

注：Helm SDK 门禁（`make test-install-sdk` / `test-upgrade-sdk` / `test-rollout-watch` 等 kind 集群直连测试）**不属于** E2E 阶段词汇（docs/architecture.md:161）。

## 3. 本地怎么跑

前置：完整 dev 环境（`make dev-up`，deploy/README.md §2）；jq（`e2e-env-config` 硬性要求，Makefile:158）；`data/dev.lock` 可 flock。

```bash
make e2e-all                                        # 全部阶段（先做 pre-flight 残留自愈）
make e2e-stage STAGES=control-plane,release        # 只跑所选阶段（逗号分隔；未知/重复名启动即拒绝，退出码 2）
make e2e-cleanup                                    # 经正式 API 回收上一轮残留
make e2e-prerequisite                               # AC-066-17 冒烟（依赖 dev-up dev-seed dev-status，Makefile:186）
```

核对过的目标存在性：`e2e-all`（Makefile:213）、`e2e-stage`（:200）、`e2e-cleanup`（:230）、`e2e-prerequisite`（:186）、`e2e-prerequisite-ci`（:190，**本地禁用**——它在 trap 里无条件 `make dev-purge CONFIRM=1`，Makefile:193）、私有目标 `e2e-env-config`（:156）。

运行链（Makefile:199-210）：source `data/dev-credentials.env` 或要求已导出 `E2E_RUNNER_PASSWORD`（:203）→ 对 `data/dev.lock` 取共享锁（冲突立即退出码 3 `environment_locked`，:206-209）→ `e2e-env-config` 从 `data/dev-status.json` + `data/dev-fixture.json` 组装唯一运行时配置 `data/e2e-env-config.yaml`（0600，:174-179；configs/e2e.dev.yaml 只是 schema 模板，其文件头 :1-10 明确不是第二配置源）→ `go run ./cmd/e2e run --stages=... --timeout=5m --total-timeout=25m --output-dir=./e2e-results ...`（默认值 :145-152，可覆盖）。

产物落在 `e2e-results/`（`OUTPUT_DIR ?= ./e2e-results`，Makefile:145；已被 .gitignore:38 忽略）：`run.json`（CI 解析的 Run 级汇总，cmd/e2e/main.go:423）、`baseline.json`（:256）、`residue.json`（:285）、每个已选阶段一份 `<stage>.json`（skip 的也写，:364；docs/testing.md:141-145）。`e2e-prerequisite*` 的结果另落 `data/smoke-result.json`（smoke.sh:35），CI 变体复制到 `e2e-results/` 并由 capture-logs.sh 收集 `e2e-results/operator-logs`（Makefile:193-197；capture-logs.sh:7-8）。

退出码表与失败诊断目录（`--keep-on-failure=true` → `e2e-results/diagnostics/{run_id}/{stage}/`）见 docs/testing.md:139-155。

## 4. 与 CI 的关系（`.github/workflows/test.yml`）

| job | 命令 | 触发范围 |
| --- | --- | --- |
| `e2e-prerequisite`（test.yml:324） | `make e2e-prerequisite-ci`（:357），`if: always()` 上传 `e2e-results/`（:359-365） | **push main / 所有 PR / 手动**——是无 secret 的版本化合并门禁（job 注释 :320-323） |
| `e2e`（test.yml:376） | `make dev-up`（:425）→ `make dev-seed`（:428）→ `make e2e-all`（:433）→ 上传 `e2e-results/`（:435-442）→ `make dev-purge CONFIRM=1`（:444-446） | **仅 push main，或 workflow_dispatch 且 `run-e2e=true`**（:377；输入默认 true :9-13）；**PR 不跑**（:367-369 注释） |

`e2e` job 需要 9 个 repository secret 并以 `DEV_PROFILE=ci`、`E2E_ENVIRONMENT=ci` 运行（:385-397），超时 30 分钟（:379），同仓并发互不取消（:382-384）。历史上曾因 secret 未配置收窄为仅手动（D-034），偏离已于 2026-09-14 解除（:371-375 注释）。除此之外没有任何 npm/浏览器 E2E job（§ web/README 已核实）。

## 5. 写新 E2E 阶段的约束

1. **业务写入只能走正式 Connect API**：`live.go:14-17`（LiveRecovery "performs no kubectl/helm/db access"）、`stages/release.go:18-19`（"every write and every observation goes through the formal Connect seams"）；cleanup 的恢复面固定为 `CancelOperation` / `RollbackRelease` / `EmergencyChange`（`cleanup.go:45-54`）。写操作统一使用 `e2e-runner` 账号（cleanup.go:14-16；session.go:16），幂等 key 按 run 作用域（livewire/specs.go:72-76 注释）。
2. **kubectl 只允许两种例外**：restart 屏障经 client-go 给 Deployment 打 restart 注解并等待收敛，禁止删 Pod / shell out（stages/restart.go:26-30；stages/restart_probe.go:11-15）；prerequisite 冒烟在 dev-script 层允许一次 kubectl 副本数只读观察（prerequisite/smoke.sh:16-19）。
3. **fail-closed，禁止空过**：canonical 阶段缺实现必须报 `not_implemented` 而非 vacuous pass（stage.go:62-67、92-97；契约测试 not_implemented_test.go、stage_error_code_test.go）；阶段图组装不全即整体失败且不写工件（cmd/e2e/main.go:154-163）。
4. **每个可逆写登记恰好一条补偿**，补偿身份稳定、注册表拒绝重复（stages/release.go:12-14、stages/emergency.go:111-113；compensation.go）。
5. **错误码进工件、文本不进**：阶段根因用稳定 code 常量（stages/write.go:12-22），`RootCause`/`ErrorCause.Message` 必须脱敏（result.go:21-25；docs/testing.md:150-155）；harness 不 import `internal/**`，需要的 wire 契约就地重述并指向 REQ 来源（livewire/readers.go:36-38）。
6. **env-config 是唯一配置面**：新阶段需要的环境事实应扩展 `e2e.Config` schema（config.go:211-218 起，严格解码 + KnownFields），由 `e2e-env-config` 的组装规则供给（Makefile:155-179），不要新造配置文件，也不要把密码写进 YAML（config.go:222-223 注释：secret 只经进程环境解析）。
7. 新阶段名要同步 `CanonicalStages` 与 `CanonicalDependencies`（stage.go:28-36、runner.go:16-24）与 `livewire/specs.go` 的实现映射（specs.go:99-108），否则组装失败。

> 事实源：`test/e2e/stage.go`、`test/e2e/runner.go`、`test/e2e/result.go`、`test/e2e/config.go`、`test/e2e/cleanup.go`、`test/e2e/live.go`、`test/e2e/session.go`、`test/e2e/fixture.go`、`test/e2e/stages/*.go`、`test/e2e/livewire/{specs,readers}.go`、`test/e2e/prerequisite/{smoke.sh,capture-logs.sh}`、`cmd/e2e/main.go`、`Makefile`、`configs/e2e.dev.yaml`、`.github/workflows/test.yml`、`docs/testing.md`、`docs/architecture.md`、`docs/dev-environment.md`、`deploy/dev/dev.sh`

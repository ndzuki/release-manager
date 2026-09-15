# 贡献指南

本仓库按**原子需求 → 原子任务 → 分支 → PR** 的链路交付。需求与任务卡记录在知识库 `myNote/Projects/001-release-manager/`，本仓库只承载代码与发布面文档。

## 交付模型

1. **原子需求（REQ）**：一份需求只描述一个可独立验收的语义单元，结构遵循十段模板 `docs/atomic-requirement-template.md`，可用 `make check-reqs` 校验。
2. **原子任务（TASK）**：每份需求默认对应一个任务卡，卡内含计划、验收标准（AC）与验证记录。
3. **分支**：`task/NNN-<slug>`（`NNN` 为任务号，`slug` 为短横线连接的英文摘要）。
4. **PR**：一个任务一个 PR，标题用提交信息的单行形式，正文说明动机、变更点与**验证证据**。
5. **合并**：仓库使用 **merge commit**（不 squash、不删分支）；合并后把 `merge_status`、`pr_url`、`checkpoint_commit` 回填到任务卡。

## 提交信息

格式：

```
<type>(<scope>): <summary> (TASK-NNN)
```

- `type`：`feat` / `fix` / `refactor` / `test` / `docs` / `chore` / `perf` / `build` / `ci`。
- `scope`：受影响的服务或包（如 `orchestrator`、`store`、`api`），跨包可用 `orchestrator,operator`。
- 摘要用英文、祈使句、不加句号；正文说明**为什么**改以及验证方式（跑了什么、观察到什么）。
- 一个提交只做一件事；不要混入无关格式化。

示例：

```
fix(orchestrator,operator): drive the rollback chain to a terminal state (TASK-090)

The rollback preflight coordinator stopped after enqueueing the command, so a
failed rollback stayed in `running` forever. Re-read the bounded convergence
result and drive the operation to its terminal state instead.

Verified: go test -race ./internal/orchestrator/... ; make sdk-check
```

## 代码约定

- Go：`gofmt` 干净；导出符号要有英文 doc comment；新增接口/构造函数的注释不可省。
- 注释语言与既有代码一致（英文）；提交信息英文；与人沟通用中文。
- 不许手改生成代码（`api/gen/**`、`web/src/gen/**`）；契约变更走 `api/proto/**` + `make proto`。
- 不许提交构建产物（`bin/` 已忽略；不要在仓库根留下 `go build` 产物）。
- 迁移文件放 `migrations/`，编号连续，`up`/`down` 成对；双引擎兼容（SQLite dev / PostgreSQL prod）。

## 测试要求

- 生产代码改动必须带回归测试；缺陷修复要能复现原缺陷（先红后绿）。
- 需要真实 PostgreSQL 的测试加 `//go:build integration`，DSN 由 `POSTGRES_TEST_DSN` 提供，未设置时 skip。
- 不要用放宽断言、加大超时掩盖偶发失败；先定位根因（时间预算、异步刷盘、共享状态竞争等）。

## 提交前检查

```bash
make test        # go test -race ./...
make lint        # golangci-lint run
make sdk-check   # SDK-only 静态门禁
make check-docs  # 文档事实门禁（改动了文档或被文档引用的路径时必跑）
make quality     # 聚合门禁
```

CI（`.github/workflows/test.yml`）在 `push` 到 `main` 与 PR 上运行，全部 job 必须通过才能合并。E2E 门禁目前按设计为**手动触发**（workflow_dispatch 的 `run-e2e`），原因与解除条件见 `docs/decisions/ADR-013` 与知识库中的 D-034 记录。

## 许可与依赖

- 本仓库以 **Apache-2.0** 发布（`LICENSE`）。按 Apache-2.0 §5，贡献默认以同一许可证授权（inbound = outbound）；提交 PR 即表示你同意这一点。
- **新增依赖先过许可门禁**：`make check-licenses` 校验所有会进入产物的依赖（Go 默认构建闭包 + 前端生产依赖），拒绝 GPL/AGPL/LGPL、SSPL、BUSL、Elastic License 以及**没有许可证文件**的依赖。
- `NOTICE` 与 `docs/dependencies.md` 是**生成产物**，不要手工编辑：
  `bash scripts/check-licenses.sh --write docs/dependencies.md`、
  `bash scripts/check-licenses.sh --write-notice NOTICE`。
- 确有必要引入清单外的许可证时，在 `license-exceptions.tsv` 登记模块与理由（会被评审）；**不要**放宽 `scripts/check-licenses.sh` 的默认允许集合。
- AI 辅助生成的代码由提交者负责：确认所用工具的条款允许你授权该产出，并避免引入与既有第三方实现高度雷同的代码（尤其是 copyleft 代码）。

## 评审关注点

- **边界**：是否越过执行边界（控制面直连集群）或引入命令行执行路径。
- **一致性**：状态机的 CAS/幂等/重放语义是否被破坏；并发下是否有共享状态竞争。
- **安全**：Secret 是否只以引用出现；审计事件是否脱敏；失败路径是否 fail-closed。
- **双引擎**：SQLite 与 PostgreSQL 行为是否等价，差异是否显式记录。
- **可验证性**：AC 是否可判定，验证证据是否可复现。

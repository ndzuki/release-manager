# AGENTS.md

面向在本仓库工作的自动化代理（以及人类协作者）的项目级约束。**这些约束优先于通用默认做法**。

## 语言与表达

- 解释、分析、汇报用**中文**；代码、标识符、命令、路径、日志保持**英文原样**。
- 代码注释与本仓库既有惯例一致：**英文**（Go 注释、proto 注释、脚本注释均为英文）。
- Git 提交信息用**英文**，格式 `<type>(<scope>): <summary>`，并在结尾附任务号 `(TASK-NNN)`。参考 `CONTRIBUTING.md`。

## 必须先读的基线

- `docs/architecture.md`：系统边界与数据权威。
- `docs/dev-environment.md`：本地环境如何起、如何清。
- `docs/testing.md`：测试分层与如何跑门禁。
- `docs/decisions/`：已接受的架构决策（ADR-000 ~ ADR-020）。

需求、任务与决策的**权威记录在知识库** `myNote/Projects/001-release-manager/`。改变需求语义必须回知识库改 REQ/TASK，而不是在本仓库就地改文档。

## 硬性架构约束（违反即拒绝合并）

1. **SDK-only**：客户集群内的执行路径只允许 Helm Go SDK。禁止 `os/exec`、`exec.Command`、`sh -c` 等任何命令行执行路径；`make sdk-check` 会静态拦截（例外清单见 `sdkcheck.exceptions.yaml`，需有到期时间与理由）。
2. **协议面**：只用 `connectrpc.com/connect` + protobuf。禁止引入 raw `grpc-go` 或第三方 HTTP router 作为服务框架。
3. **生成代码不许手改**：`api/gen/**`、`web/src/gen/**` 由 `buf` 生成；改契约要改 `api/proto/**` 后重新 `make proto`。
4. **双引擎 schema**：dev/test 用 SQLite、生产用 PostgreSQL。新增表/字段/查询必须同时兼容两者；迁移写在 `migrations/` 且编号连续，SQLite 侧结构在同一变更内对齐。
5. **不可变输入与 Secret 边界**：ValuesRevision 一旦创建不可变；Secret 只以引用（SecretRef）形式进入执行链，禁止把明文写入库、日志或审计事件。
6. **审计与脱敏**：审计事件必须经脱敏路径；不要绕过 emitter 直接写审计表。
7. **依赖许可**：只接受宽松许可（Apache-2.0、MIT、BSD、ISC、MPL-2.0 等）。禁止引入 GPL/AGPL/LGPL、SSPL、BUSL、Elastic License 或**没有许可证文件**的依赖 —— Go 会把整个模块静态链接进二进制，`make check-licenses` 会拦截。`NOTICE` 与 `docs/dependencies.md` 是生成产物，改动依赖后用 `bash scripts/check-licenses.sh --write-notice NOTICE` 与 `--write docs/dependencies.md` 重生成。

## 质量门禁

提交前至少跑通与本改动相关的门禁；声称「完成」前必须全绿：

```bash
make test        # go test -race ./...
make lint        # golangci-lint run
make sdk-check   # SDK-only 静态门禁
make check-licenses # 依赖许可门禁（GPL/AGPL/LGPL 等一律拒绝）
make quality     # 聚合门禁（含上述）
```

- 需要真实 PostgreSQL 的测试必须打 `//go:build integration` 标签，并通过 `POSTGRES_TEST_DSN` 提供 DSN；**不得**为了本地通过而移除该标签或把断言放宽。
- 生产路径的改动必须附回归测试；flutter/偶发失败要定位到根因（例如时间预算与异步刷盘竞争），不要靠加大超时掩盖。
- `gofmt`：**你改动过的文件**必须干净（`gofmt -l <改动的文件>` 无输出）。仓库存在历史遗留的注释对齐差异（由不同 gofmt 版本产生，例如 `internal/devfixture/bundle.go`），CI 也尚未接入 gofmt 门禁，因此**不要顺手格式化无关文件**。

## 环境与资源纪律

- **不得停止或删除常驻用户服务**（例如 `kb-reranker`、`ollama-sycl`）来换取测试资源；需要端口/内存时改用自己的隔离环境。
- 会话结束前清理本次创建的一切临时资源：k3d 集群、容器、registry、临时 kubeconfig、临时数据目录。清理证据（快照/清单）要能复述。
- 需要保留的环境必须显式声明留给哪个后续任务及其清单。
- **破坏性操作先确认**：`rm -rf`、`kubectl delete`、`helm uninstall`、`git push --force`、`make dev-purge`、重写历史等，未经用户明确同意不得执行。

## 工作方式

- 改动前先读既有决策（ADR）与相邻实现，避免与既有契约冲突；发现冲突要显式提出，不要静默选择。
- 一次只交付一个可验证的原子变更：小步提交、每步跑对应包的测试、最后跑全量。
- 提交/推送只在用户明确要求时进行；提交前确认工作树里没有构建产物（`bin/` 之外不应出现编译出的二进制）。
- 汇报时区分「已验证」与「推测」；引用证据（命令、输出、文件:行号），不要用「应该没问题」代替验证。

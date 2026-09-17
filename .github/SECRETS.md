# CI 机密与变量清单（Secrets / Variables）

本文只回答一个问题：**要让本仓库的 CI 跑起来，需要在 GitHub 上配置哪些机密与变量、谁来配、怎么配、取值必须长什么样。**

- 读者：需要维护本仓库 Actions 的维护者。
- 事实范围：`.github/workflows/test.yml`（504 行，13 个 job）与 `.github/workflows/sync-to-gitcode.yaml`（25 行，1 个 job）。两个文件之外的 workflow 不存在（`.github/` 下只有 `workflows/`）。
- 本文**不包含任何机密取值**。所有示例值一律写成 `<...>` 占位。
- 生产部署侧的机密注入**不在本清单内**：`docs/architecture.md:113` 明确「生产 Secret manager 注入与生产配置加载仍由 REQ-011 owner 承接」，dev 范围只做了最小接线。

## 1. 全部 `secrets.*` 引用（逐个从 workflow 提取）

`on:` 触发范围：`push` 到 `main`、任意 `pull_request`、`workflow_dispatch`（输入 `run-e2e`，默认 `true`）——`test.yml:3-13`；镜像 workflow 只监听 `push` 到 `main`——`sync-to-gitcode.yaml:3-6`。

| 名称 | 用在哪个 job / step（文件:行号） | 用途 | 是否敏感 | 缺失时的后果 |
| --- | --- | --- | --- | --- |
| `GITCODE_TOKEN` | `gitcode-sync` → step `Push to GitCode`（`sync-to-gitcode.yaml:19-25`，引用在 `:22`） | 拼进 HTTPS remote 的口令段，向 `gitcode.com/ndmizuki/<repo>` 执行 `git push --mirror`（`:25`） | **是**（外部镜像仓库的写凭据） | 仅该 job 失败（认证被拒）；`test.yml` 的 13 个 job 完全不受影响。镜像与 GitHub 主干从此静默漂移，无告警 |
| `GITHUB_TOKEN` | `test` → step `bufbuild/buf-setup-action`（`test.yml:130-135`）；`test-sqlite` → 同一 action（`test.yml:214-219`）；`proto-check` → 同一 action（`test.yml:476-481`） | 给 buf 发行版查询做 GitHub API 认证，规避匿名限流（注释见 `test.yml:133-134`） | 否（运行时自动签发；本仓库权限被 `test.yml:15-16` 收敛为 `contents: read`） | 不会缺失。若被显式清空，最坏是匿名 API 限流导致 action 取版本失败，属可重试的基础设施失败 |
| `E2E_RUNNER_PASSWORD` | `e2e` job 级 env（`test.yml:389`）+ step `Run all E2E stages` env 再注入一次（`test.yml:432`） | E2E 写身份 `e2e-runner` 的口令（角色 `release_admin`，`internal/devfixture/accounts_trust.go:29`）；`make e2e-all` 只从进程环境读它（`Makefile:203-204`） | **是**（但作用域仅 dev/CI 夹具环境） | `make dev-seed` 阶段即失败：CI profile 要求四个口令齐全（`internal/devfixture/files.go:210-215`）；即便跳过 seed，`make e2e-all` 也会在 `Makefile:203` 直接退出 |
| `DEV_ADMIN_PASSWORD` | `e2e` job 级 env（`test.yml:390`） | `dev-admin` 账号口令（用户名默认 `dev-admin`，`cmd/devseed/main.go:50`），seed 用它登录并初始化系统（`internal/devfixture/runner.go:315`） | **是** | `make dev-seed`（`test.yml:427-428`）以 `ci profile requires env-injected passwords: DEV_ADMIN_PASSWORD` 失败（`internal/devfixture/files.go:200-215`） |
| `DEV_DEPLOYER_PASSWORD` | `test.yml:391` | `dev-deployer` 口令（`cmd/devseed/main.go:52-53`），deployer token 用于 `GetOperation` 轮询（`internal/devfixture/accounts_trust.go:170-172`） | **是** | 同上（四个口令一次性汇总报错） |
| `DEV_READER_PASSWORD` | `test.yml:392` | `dev-reader`（viewer 角色，无写权限）口令，`cmd/devseed/main.go:54`、`internal/devfixture/runner.go:27-28` | **是** | 同上 |
| `DEV_JWT_SIGNING_KEY` | `test.yml:393` | HS256 JWT 签名密钥。ci profile 把它临时写成 kustomize 源文件（`deploy/dev/dev.sh:293-304`），经 `secretGenerator`（`deploy/kustomize/dev/kustomization.yaml:39-43`）成为 Secret `release-manager-jwt` 的 `JWT_SIGNING_KEY`，再注入 orchestrator（`deploy/kustomize/services/orchestrator.yaml:35-39`）与 webhook/auth（`envFrom`，`deploy/kustomize/services/webhook.yaml:31-34`） | **是**（泄露即可伪造任意身份 token） | `make dev-up`（`test.yml:424-425`）以 `ERR_SERVICE_UNHEALTHY: ci profile requires DEV_JWT_SIGNING_KEY` 失败（`deploy/dev/dev.sh:294-295`） |
| `DEV_WEBHOOK_SERVICE_TOKEN` | `test.yml:394` | bundle ingress 的服务令牌：webhook 侧作为 `Authorization: Bearer <token>` 转发（`cmd/webhook/main.go:61-62`），orchestrator 侧取 SHA-256 摘要做常量时间比对（`cmd/orchestrator/main.go:898-911`、`internal/auth/service_token.go:108-126`） | **是** | `make dev-up` 以 `ci profile requires DEV_WEBHOOK_SERVICE_TOKEN` 失败（`deploy/dev/dev.sh:342-343`） |
| `DEV_M_TLS_CA_KEY` | `test.yml:395` | dev mTLS CA 私钥，签 operator 客户端证书与网关服务端证书（`internal/operator/ca/ca.go:205`、`internal/operator/ca/ca.go:252`、`cmd/orchestrator/main.go:154-157`） | **是**（集群侧身份的信任锚） | `make dev-up` 以 `ci profile requires DEV_M_TLS_CA_KEY and DEV_M_TLS_CA_CERT` 失败（`deploy/dev/dev.sh:394-395`） |
| `DEV_M_TLS_CA_CERT` | `test.yml:396` | 与上配对的 CA 证书；同一 Secret 挂载为网关 `/data/gateway-ca.crt`（`deploy/kustomize/services/orchestrator.yaml:94-98,107-113`），并被复制给客户集群 agent 做校验（`deploy/dev/dev.sh:1296`） | 证书本身是公开材料，但**必须与私钥成对**，故与 KEY 同级管理 | 同 `DEV_M_TLS_CA_KEY`；只给证书不给私钥同样失败（`deploy/dev/dev.sh:394` 用 `-z ... || -z ...` 同时判定） |
| `DEV_TRUST_ROOT_PRIVATE_KEY` | `test.yml:397` | Dev Trust Root Ed25519 私钥：seed 把它的公钥经 `TrustService.CreateTrustRoot` 激活（`internal/devfixture/accounts_trust.go:96-107`），并用它对 bundle digest 签名（`internal/devfixture/accounts_trust.go:149-151`） | **是** | `make dev-seed` 以 `ci profile requires DEV_TRUST_ROOT_PRIVATE_KEY` 失败（`internal/devfixture/files.go:258-261`） |

合计：**14 处 `secrets.*` 引用，去重后 11 个名字**（`GITHUB_TOKEN` 独占 3 处，别把"引用数"读成"需配置数"）。其中 `GITHUB_TOKEN` 是自动提供、无需配置；**需要人工配置的 repository secret 共 10 个**：`GITCODE_TOKEN` + `test.yml:389-397` 的 9 个。这 9 个与既有权威口径一致（`docs/testing.md:188-191`：「4 个账号密码 + `DEV_JWT_SIGNING_KEY` + `DEV_WEBHOOK_SERVICE_TOKEN` + `DEV_M_TLS_CA_KEY`/`DEV_M_TLS_CA_CERT` + `DEV_TRUST_ROOT_PRIVATE_KEY`」）。

## 2. 全部 `vars.*` 引用

| 名称 | 用在哪个 job / step | 用途 | 是否敏感 | 缺失时的后果 |
| --- | --- | --- | --- | --- |
| `RUNS_ON` | `test.yml` 33 处引用：13 个 job 的 `runs-on`（`:38,51,71,88,105,119,203,246,285,325,378,455,471`）、`actions/setup-go` 的 `cache:`（`:45,59,79,96,113,128,212,255,293,333,404`）、缓存清理/恢复步骤的 `if:`（`:157,166,227,235,260,268`）、自托管分支判断（`:301`）与 `WORKLOAD_IMAGE` 摘要选择（`:317`）；另 `sync-to-gitcode.yaml:12`。**注**：33 里含 `:26` 那处位于注释块内的示例文本（非真实求值点），所以可求值引用是 32 处；13 与 11 两个 job 数分别是 `runs-on` 与 `setup-go` 的出现次数，`docs-check`/`proto-check` 不装 Go 因此没有 `cache:` 项 | 选 runner：未设置 → `ubuntu-latest`；设为 `self-hosted` → 切到本地自托管 runner，并跳过 Go 模块缓存的擦除与读写（避免污染共享 `~/go/pkg/mod`） | 否（是配置，不是凭据） | 不会失败：所有引用都带 `|| 'ubuntu-latest'` 兜底。**但自托管切换的全部理由就是绕开私有仓额度限制，见 `test.yml:24-34`** |

切换命令（`test.yml:29-34` 的注释已给出同一形态）：

```bash
gh variable set RUNS_ON --body self-hosted --repo ndzuki/release-manager
gh variable delete RUNS_ON --repo ndzuki/release-manager
```

**不是 secret 的固定值，勿误配成 secret**：`GITCODE_USER: ndmizuki`（`sync-to-gitcode.yaml:9`，是 workflow 级 `env`，非 `vars`）、`KIND_SHA256`（`test.yml:297`）、`WORKLOAD_IMAGE` 的 busybox digest（`test.yml:317`）、`DEV_PROFILE=ci` / `E2E_ENVIRONMENT=ci` / `E2E_RUN_ID`（`test.yml:385-388`）。注意 GitCode 用户名 `ndmizuki` 与 GitHub 所有者 `ndzuki` **不同**（镜像地址是 `gitcode.com/ndmizuki/release-manager`）。

## 3. 哪些 job 因缺 secret 而不会跑 / 会失败

逐条核实 `if:` 后的事实：

1. **`e2e`（`test.yml:376-377`）是唯一消费 9 个 DEV_*/E2E_* secret 的 job。** 它的 `if:` 是 `github.event_name == 'push' || (github.event_name == 'workflow_dispatch' && inputs.run-e2e)`，而 workflow 的 `push` 只监听 `main`（`test.yml:4-5`）→ **只有 push main 与手动触发会跑，`pull_request` 一律跳过**（设计意图见 `test.yml:367-375` 注释：PR 刻意不跑这个特权 job）。
2. **secret 缺失不会让 `e2e` job 被跳过，只会让它失败。** GitHub 对未定义的 secret 注入空字符串（平台行为，非仓库内证据），因此失败点在脚本内部，而不是在表达式求值：`make dev-up` → `deploy/dev/dev.sh:294-295/342-343/394-395`；`make dev-seed` → `internal/devfixture/files.go:200-215,258-261`；`make e2e-all` → `Makefile:203`。三处都有显式的「ci profile requires X」文案，**这是有意的**：缺机密必须报成配置缺陷，不能被误读成代码回归。
3. **`e2e-prerequisite`（`test.yml:324-365`）在所有触发上跑，且不需要任何仓库 secret**（`docs/testing.md:185` 同口径）。它没有设 `DEV_PROFILE`，因此走 local profile：口令与密钥由 `dev-seed`/`dev-up` 自行生成到 `data/`（`internal/devfixture/files.go:183-195`），smoke 再从 `data/dev-credentials.env` source 回来（`test/e2e/prerequisite/smoke.sh:86-94`）。**它上传的 artifact（`test.yml:359-365`）来自本地生成的夹具，不含 CI secret 值。**
4. **其余 10 个 job（`sdk-check`、`license-check`、`install-sdk`、`upgrade-sdk`、`operator-image-sdk-only`、`test`、`test-sqlite`、`test-sdkcheck`、`docs-check`、`proto-check`）无 `if:`，全部触发都跑，除自动的 `GITHUB_TOKEN` 外不消费任何 secret**（逐个 job 核对步骤：`test.yml:37-49,50-68,70-85,87-102,104-116,118-200,202-243,245-282,454-469,470-504`）。`install-sdk`/`upgrade-sdk`/`operator-image-sdk-only`/`rollout-watch` 依赖的是 Docker 与 kind，而非机密。`docs-check` 与 `proto-check` 是本次新增的静态门禁，二者同样只在 `contents: read` 下工作，不引入新的机密依赖。
5. **`gitcode-sync`（`sync-to-gitcode.yaml:11`）只在 push main 跑，且只依赖 `GITCODE_TOKEN`。** 该 job 没有 `timeout-minutes`、没有 `concurrency`（对比 `test.yml:20-22,52,72,...`）→ 两个连续 push 可能并发 `--mirror` 互踩（**建议**：加 `concurrency` 组与超时）。

**fork PR 的影响（GitHub 平台行为，仓库内无证据）**：`pull_request` 事件对 fork 不注入仓库 secret。因为唯一消费 secret 的 job 已经排除 PR 触发（第 1 条），当前配置天然安全；**建议**新增依赖 secret 的 job 时保持同样的 `if:` 收窄，别把它放到 PR 路径上。

## 4. 配置步骤

### 4.1 命令行（推荐，可审计、可复制）

```bash
# 前置：登录一个对该仓库具备 admin 的账号（Actions secrets 读写需要 admin 角色 —— 平台行为）
gh auth login
gh repo view ndzuki/release-manager --json nameWithOwner   # 确认目标仓库

# 变量
gh variable set RUNS_ON --body ubuntu-latest --repo ndzuki/release-manager

# 机密：三种输入形态按需选用
gh secret set GITCODE_TOKEN --repo ndzuki/release-manager                       # 交互式隐藏输入
printf '%s' "$LOCAL_VALUE" | gh secret set DEV_JWT_SIGNING_KEY --repo ndzuki/release-manager
gh secret set DEV_M_TLS_CA_CERT --body "$(cat data/dev-ca/ca.crt)" --repo ndzuki/release-manager

# 核对（只列名字，永不返回值；GitHub 也不提供任何读回明文的 API）
gh secret list --repo ndzuki/release-manager
gh variable list --repo ndzuki/release-manager
```

一次性配齐 9 个 e2e secret 的循环形态（值来自**当前**本地 dev 夹具，仅供理解字段对应关系；生产/长期 CI 值应另建，见第 6 节）：

```bash
set -a; . data/dev-credentials.env; set +a   # data/ 被 .gitignore:35 忽略
for name in DEV_ADMIN_PASSWORD DEV_DEPLOYER_PASSWORD DEV_READER_PASSWORD E2E_RUNNER_PASSWORD; do
  printf '%s' "${!name}" | gh secret set "$name" --repo ndzuki/release-manager
done
printf '%s' "$(cat data/dev-trust-root/dev-trust-root.key)" | base64 -w0 \
  | gh secret set DEV_TRUST_ROOT_PRIVATE_KEY --repo ndzuki/release-manager
gh secret set DEV_JWT_SIGNING_KEY     --body "$(cat data/dev-jwt/jwt-signing-key.pem)"   --repo ndzuki/release-manager
gh secret set DEV_WEBHOOK_SERVICE_TOKEN --body "$(cat data/dev-service-tokens/webhook-service-token)" --repo ndzuki/release-manager
gh secret set DEV_M_TLS_CA_KEY        --body "$(cat data/dev-ca/ca.key)"   --repo ndzuki/release-manager
gh secret set DEV_M_TLS_CA_CERT       --body "$(cat data/dev-ca/ca.crt)"   --repo ndzuki/release-manager
```

### 4.2 Web 界面

- 仓库级：**Settings → Secrets and variables → Actions**
  - **Secrets** 标签页 → *New repository secret* → 填 Name / Value → Add secret。
  - **Variables** 标签页 → *New repository variable* → 填 Name / Value → Add variable。
- 环境级：同一页面下的 **Environments** 标签页（或在 Settings → Environments 新建 environment 后，在其 *Secrets* 里按环境添加）。
- 已存在的 secret 只能 *Update*（覆盖）或 *Delete*；平台不提供查看旧值。

### 4.3 仓库级 vs 环境级

| 维度 | 仓库级 secret / variable | 环境级 secret / variable |
| --- | --- | --- |
| 可见范围 | 仓库内**所有** job、所有 workflow | 只有声明了 `jobs.<id>.environment: <name>` 的 job |
| 同名优先级 | 低 | 高（环境级覆盖仓库级，平台行为） |
| 附加门禁 | 无 | 可配 required reviewers / deployment branch policy |
| 本仓库现状 | **全部 10 个需人工配置的 secret 与 `RUNS_ON` 都是仓库级** | **未使用**：两个 workflow 文件里 `environment:` 关键字 0 处（已 grep 核实） |

现状的含义：`GITCODE_TOKEN` 与 9 个 dev/e2e secret 对**任何**能触发 workflow 的 job 都可见。**建议**把 `GITCODE_TOKEN`（唯一具备外部写能力的凭据）改挂到一个只有 push main 才进入的 environment，并在该 environment 上开 required reviewer；同时给 `e2e` job 加 `environment: ci`，让 9 个 secret 从仓库级收窄到环境级。改法本身要动 workflow 文件，属独立变更。

## 5. 每个 secret 的取值来源与格式要求

**格式要求全部以消费方代码为准**，逐条给出行号；核实不到的按第 5.11 条处理。

### 5.1 `DEV_ADMIN_PASSWORD` / `DEV_DEPLOYER_PASSWORD` / `DEV_READER_PASSWORD` / `E2E_RUNNER_PASSWORD`

- **硬性格式**：长度**恰好 32 字符**，字符集 `[A-Za-z0-9]`；CI profile 逐个校验并在错误里点名（`internal/devfixture/files.go:216-224`，校验函数 `:154-168`，常量 `passwordLength = 32` 见 `:76`）。
- 为什么限字母数字：值必须能**不加引号**安全出现在 shell env 文件里（生成器注释 `internal/devfixture/files.go:88-97`，同款约束说明 `deploy/dev/dev.sh:358-360`）。带 `$`、反引号、空格、换行的「强口令」会被直接拒。
- 熵：32 位字母数字 ≈ 190 bit（`internal/devfixture/files.go:154-156` 注释）。
- 取值来源：本地 dev 自动生成的 `data/dev-credentials.env`（渲染模板 `internal/devfixture/files.go:79-86`；生成路径 `:183-195`）。CI 必须显式注入，**CI profile 永不写这个文件**（`internal/devfixture/files.go:197-199`）。
- 四个值必须两两不同（`internal/devfixture/files.go:90-96` 分别独立生成）。
- 口令强度不在 auth 侧二次校验：seed 走的是正式 `Login`/`CreateLocalUser` API（`internal/devfixture/accounts_trust.go:40-59`），格式契约只在 devfixture 里强制。

### 5.2 `DEV_JWT_SIGNING_KEY`

- **格式要求：非空任意字节串即可**。dev.sh 只判 `-z`（`deploy/dev/dev.sh:294`），随后原样写成 0600 文件（`:300-303`）；auth 侧把它当 raw bytes 直接喂给 HS256（`internal/auth/jwt.go:22-27,56`），**没有长度、编码或字符集校验**。
- 仓库内的参照格式：本地 `dev-up` 生成 **64 随机字节的 base64**（`deploy/dev/dev.sh:311-317`，含换行 88 字符），注释同时声明「任何非空值都有效」与「轮换 = 删文件重跑 dev-up」（`:311-314`）。
- **建议**：CI 值沿用同一生成方式（≥32 字节随机、base64 承载），并**不要**依赖 `change-me-in-production` 兜底默认——该默认仍留在 `cmd/auth/main.go:237`、`cmd/orchestrator/main.go:851`、`cmd/api/main.go:113` 三处 flag 默认值里；`cmd/api` 那一处**没有** env 回退。
- 落盘语义：只在 kustomize build/apply 期间存在，apply 完立即删除（`deploy/dev/dev.sh:323-326` 由 `:1122` 调用）。

### 5.3 `DEV_WEBHOOK_SERVICE_TOKEN`

- dev 侧只要求非空（`deploy/dev/dev.sh:342`），原样写成 0600 文件（`:347-350`）。
- 仓库内的参照格式：本地生成 **32 字符 `[A-Za-z0-9]`**，`head -c 1024 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 32`（`deploy/dev/dev.sh:357-363`），注释说明与 `dev-credentials.env` 的口令同一字符集契约（`:358-360`）。
- 服务端对值本身无校验：orchestrator 取 **SHA-256 hex 摘要**保存（`cmd/orchestrator/main.go:898-911`），入站 Bearer 也摘要后做**常量时间**比对（`internal/auth/service_token.go:108-126`，`subtle.ConstantTimeCompare` 见 `:120`）。因此唯一要求是 webhook 侧与 orchestrator 侧同源（两者都来自同一个 Secret：`deploy/kustomize/services/webhook.yaml:39-43` 与 `deploy/kustomize/services/orchestrator.yaml:45-49`）。
- 作用域是收窄的：该 token 只被允许调用 `BundleService.SubmitBundle` 一个 procedure，actor 记为 `service:release-webhook`（`cmd/orchestrator/main.go:477-492`，作用域声明在 `:487-489`）。
- **建议**：按 32 字符字母数字生成，与 dev 契约一致；长度上限无约束（不进 DB，只存摘要）。

### 5.4 `DEV_M_TLS_CA_KEY` / `DEV_M_TLS_CA_CERT`

- **必须是 PEM 原文，不是 base64(PEM)**：dev.sh 用 `printf '%s'` 原样落盘为 `ca.key` / `ca.crt` 并 chmod 600（`deploy/dev/dev.sh:397-401`），随后由 `secretGenerator` 直接读这两个文件（`deploy/kustomize/dev/kustomization.yaml:66-70`）。
- **可加载性 = 硬格式要求**，消费方是 `internal/operator/ca.Load`：
  - key：PEM block type 必须是 `PRIVATE KEY`（`internal/operator/ca/ca.go:147-150`）、PKCS#8 解析（`:151`）、**必须是 Ed25519**，其它类型直接拒（`:155-158`）。
  - cert：PEM block type 必须是 `CERTIFICATE`（`:160-163`）、`IsCA` 必须为真（`:167-169`）、且**与私钥匹配 + 自签有效**（`:171-181`）。
  - 结论：塞一把 RSA/ECDSA 的 CA 会让 `make dev-up` 在网关启动前失败（CA 不合法时 `ca.Load` fail closed，见 `deploy/dev/dev.sh:404-414` 的复用/重生成语义）。
- 仓库内的参照产物：`cmd/devseed/mtls_ca.go:78-115` 生成的自签 CA——`CommonName: release-manager-dev-ca`（`:90`）、`KeyUsage: CertSign|CRLSign`（`:96`）、有效期 10 年（`:91-95`）、serial 为 ≤128 bit 随机（`:83-86`）；生成后**先用真实 loader 验一遍再落盘**（`:111-115`）。
- 网关签出的客户端证书 TTL 默认 7 天（`internal/operator/ca/ca.go:41-42,46-48`），CA 自身是长期锚点（`cmd/devseed/mtls_ca.go:91-93` 注释）。
- 复用条件：已存在且可解析则原样复用，损坏则重生成（`cmd/devseed/mtls_ca.go:60-72`）。CI 场景下没有本地文件，只能靠这两个 secret 提供成对材料。

### 5.5 `DEV_TRUST_ROOT_PRIVATE_KEY`

- **两种形态都被接受**：PKCS#8 PEM 原文，**或** 该 PEM 的 base64 编码（专门为 CI secret 注入留的口子，注释见 `internal/devfixture/files.go:299-301`）。解析顺序：先试 PEM，失败再试 base64→PEM（`:301-312`）。
- **算法必须是 Ed25519**：`x509.ParsePKCS8PrivateKey` 后做类型断言，不是 Ed25519 直接报 `dev trust root key must be Ed25519`（`internal/devfixture/files.go:313-320`）。
- 仓库内的参照格式：本地生成走 `ed25519.GenerateKey` + `MarshalPKCS8PrivateKey` + PEM `PRIVATE KEY`（`internal/devfixture/files.go:276-297`），落盘 `data/dev-trust-root/dev-trust-root.key`（`:22-23,249-252`，0600 见 `:34-48`）。
- 服务端侧对应契约：seed 提交的公钥是 PKIX PEM（`internal/devfixture/accounts_trust.go:153-159`），key id 固定 `dev-trust-root`（`internal/devfixture/runner.go:50-52`）。**注意一致性风险**：CI 里配的私钥必须与已经在该环境激活的 trust root 公钥配对，否则签名验证失败——重配 secret 后需要重新播种/轮换 trust root（见第 6.3 条）。

### 5.6 `GITCODE_TOKEN`

- **格式要求未在仓库中找到，按上游 GitCode 的要求配置**：仓库内没有任何脚本或 Go 代码校验它的长度/前缀/字符集。
- 仓库内可核实的事实只有：它被当作 HTTPS basic auth 的**口令段**使用（`sync-to-gitcode.yaml:22`，`https://<user>:<token>@gitcode.com/...`），因此值中**不得含 `@`、`:`、`/` 或空白**，否则 URL 解析会被破坏（这是从该行 URL 模板推出的约束，非仓库明文规定）。
- **建议**：在 GitCode 侧新建仅对 `ndmizuki/release-manager` 有写权限的最小范围个人访问令牌并设置过期时间；GitCode 的 PAT 页面通常提供 `read`/`write` 两类，镜像只需要写。上游具体命名需在 GitCode 站点核实。

### 5.7 `GITHUB_TOKEN`

- 无需配置，自动提供；权限由 `permissions:` 决定（见第 7 节）。当前两处用法只要求只读的 release 元数据。

### 5.8 `RUNS_ON`

- 取值语义由代码判定，**必须精确匹配字符串**：`!= 'self-hosted'`（`test.yml:45,157,166,...`）与 `== 'self-hosted'`（`test.yml:301,317`）；`runs-on` 直接用其值或 `ubuntu-latest` 兜底（`test.yml:38`）。因此可用值实际是 `{未设置, ubuntu-latest, self-hosted, <runner 标签>}`；写错不会报错，只会**静默把缓存步骤全跳过**。
- runner 标签需与自托管 runner 注册名一致。仓库注释里给出的自托管机器与 service 名在 `test.yml:30-32`（`arch-dev-1` / systemd user service `actions-runner-release-manager`）。**建议**：对外公开文档里不重复主机名；本文按项目纪律只引用位置。

## 6. 轮换与撤销：仓库真实支持到什么程度

### 6.1 已实现（有代码/配置支撑）

1. **内容变化即滚动重启**：四个 `secretGenerator` 条目按文件内容哈希命名 Secret（`deploy/kustomize/dev/kustomization.yaml:30-38,39-70`），改值后滚动消费方 Deployment——这是仓库内唯一的「自动生效」机制。
2. **服务令牌支持双密钥并存（零停机轮换）**：Secret 的可选 key `WEBHOOK_SERVICE_TOKEN_PREVIOUS` → env `DEV_WEBHOOK_SERVICE_TOKEN_PREVIOUS`（`deploy/kustomize/services/orchestrator.yaml:50-56`，`optional: true`），verifier 同时接受 current+previous 两个摘要（`cmd/orchestrator/main.go:898-911`）。dev-test 显式把这条接缝当契约测（`deploy/dev/dev_test.go:1970-1972`）。注意：`secretGenerator` 只生成一个 key（`deploy/kustomize/dev/kustomization.yaml:54-57`），PREVIOUS 需由外部 Secret manager 叠加（注释 `:51-53`），**GitHub Actions 侧没有这条叠加路径**。
3. **Trust Root 有完整的轮换/宽限/退休/撤销 API**：`RotateTrustRoot`（新 root 立即 active、旧 root 进入 `grace_until` 窗口，`internal/trust/service.go:84-135`）、`EndGrace`（`:157`）、`RetireTrustRoot`（`:210`）、`RevokeTrustRoot`（`:260`）、`GetTrustPolicy`（`:312`），每次变更都走审计（`:483`）。契约声明见 `api/proto/trust/v1/trust.proto:115-120`，服务真实挂载在 orchestrator 上（`cmd/orchestrator/main.go:505-519`）。
4. **CI 侧的清理兜底**：`make dev-purge CONFIRM=1` 在 `if: always()` 的 post-step 执行（`test.yml:444-446`），`PURGE_DATA_PATHS` 含 `dev-credentials.env`、`dev-trust-root`、`dev-jwt`、`dev-service-tokens`、`dev-enrollment-tokens`、`dev-ca`、`backups`（`deploy/dev/dev.sh:42`）。**这只清 CI runner 上的临时文件，不撤销 GitHub secret。**
5. **ci profile 不落盘**：JWT key / service token / mTLS CA 只在 kustomize build 期间存在，apply 后删除（`deploy/dev/dev.sh:323-326,371-374,422-425`，调用点 `:1122-1124`）。

### 6.2 缺口（明确说没有）

- **没有任何轮换入口**：无轮换脚本、无 schedule workflow（`.github/workflows` 只有 `test.yml` 与 `sync-to-gitcode.yaml`，两者 `on:` 均无 `schedule`）、无到期登记。GitHub repository secret 本身也不带过期字段。
- **没有撤销通道**：`GITCODE_TOKEN` 的撤销只能去 GitCode 侧吊销 PAT；仓库内无相关 runbook。**建议**：在第 8 节登记固定周期（例如季度）人工轮换 9 个 dev/e2e secret + `GITCODE_TOKEN`，并在 PR 描述里留痕。
- **dev/e2e 夹具类 secret 不做「撤销」语义**：它们只作用于一次 CI run 临时起的 k3d 环境，环境随 `dev-purge` 销毁（`test.yml:444-446`），因此真正的风险面是**这些值被复用**——见第 7.3 条。
- **JWT signing key 无轮换接缝**：只有「换 secret 值 + 滚动重启」一条路，且**没有** current/previous 双 key；换 key 会让所有在册 JWT 立刻失效（`internal/auth/jwt.go:15-27` 只持单 key）。**建议**：把 JWT key 轮换排到维护窗口（维护模式按 procedure allowlist 停写，`docs/architecture.md:147`）。
- **mTLS CA 轮换是破坏性操作**：本地契约写的是「删 `data/dev-ca/` 重跑 dev-up」（`deploy/dev/dev.sh:384-386`）。CI 换 `DEV_M_TLS_CA_KEY/CERT` 等价于换信任锚——已注册 agent 的证书立即不再被网关接受，必须走重新 enrollment。仓库内**没有**CA 双锚并存的实现。
- **dev trust root 没有自动轮换接线**：seed 只调用 `CreateTrustRoot` 与 `GetTrustPolicy`（`internal/devfixture/accounts_trust.go:83-116`），`Rotate/EndGrace/Retire/Revoke` 在产品侧可用但未接入任何脚本。

## 7. 本地开发对应关系与「本地值 ≠ CI 值」约定

### 7.1 同名物料的两条路径

`DEV_PROFILE` 决定走哪条（校验见 `cmd/devseed/main.go:77-80`，只接受 `local` / `ci`）；权威口径在 `docs/dev-environment.md:183-185`：local 写 0600 文件，ci 从环境变量注入同名物料、**不落盘**。

| CI secret | 本地对应物（`data/` 下，全部 gitignore：`.gitignore:35`） | 谁生成 |
| --- | --- | --- |
| `DEV_ADMIN_PASSWORD` / `DEV_DEPLOYER_PASSWORD` / `DEV_READER_PASSWORD` / `E2E_RUNNER_PASSWORD` | `data/dev-credentials.env` | `dev-seed`（`internal/devfixture/files.go:183-195`，渲染 `:79-86`） |
| `DEV_JWT_SIGNING_KEY` | `data/dev-jwt/jwt-signing-key.pem`（`deploy/dev/dev.sh:288`） | `dev-up`（`deploy/dev/dev.sh:306-318`） |
| `DEV_WEBHOOK_SERVICE_TOKEN` | `data/dev-service-tokens/webhook-service-token`（`deploy/dev/dev.sh:336`） | `dev-up`（`deploy/dev/dev.sh:353-365`） |
| `DEV_M_TLS_CA_KEY` / `DEV_M_TLS_CA_CERT` | `data/dev-ca/ca.key` + `data/dev-ca/ca.crt`（`deploy/dev/dev.sh:388-389`） | `dev-up` → `cmd/devseed -ensure-mtls-ca`（`deploy/dev/dev.sh:404-417`、`cmd/devseed/main.go:41-42`） |
| `DEV_TRUST_ROOT_PRIVATE_KEY` | `data/dev-trust-root/dev-trust-root.key`（`internal/devfixture/files.go:22-23,249-252`） | `dev-seed`（`internal/devfixture/files.go:264-289`） |
| （无 CI 对应） | `data/dev-enrollment-tokens/<clusterID>.token`（`internal/devfixture/files.go:26,324-327`） | seed 的 enrollment 阶段 |

文件名易混点：**`http-client.env.json` 是 Kulala 调试集合的变量文件，与 Actions secrets 无关**，里面的值全是本地占位（`http-client.env.json:7-8,29-30`）；Kulala 侧的 secret 管理器集成方式另见 `kulala-http` 相关集合说明，不在本文范围。

### 7.2 其它只在本地/测试出现的 env（不需要配到 GitHub）

- `POSTGRES_TEST_DSN`：live-DB 集成测试的 DSN，**未设置即 `t.Skip`**（`docs/testing.md:21-23,204-206`；`CONTRIBUTING.md:49`）。CI 的 13 个 job 都没设它，所以这些用例在 CI 里是 skip 而非 fail（**建议**：若要真正跑 PG 侧门禁，需在 CI 起 Postgres 服务并注入 DSN —— 目前该缺口未闭合）。
- `RELEASE_MANAGER_DATABASE_DSN`：`devseed --reset` 的 PostgreSQL DSN（`cmd/devseed/main.go:58`）。
- `E2E_RUNNER_PASSWORD` 之外的 e2e 可调项：`E2E_ENV_CONFIG` / `OUTPUT_DIR` / `STAGES` / `TIMEOUT` / `TOTAL_TIMEOUT` / `PARALLEL` / `KEEP_ON_FAILURE` / `SNAPSHOT_FULL` / `BASELINE_FILE`（`Makefile:135-153`），均非机密。
- 服务配置的可注入 env（`internal/config/config.go:270-297`）：`DATABASE_DRIVER`、`DATABASE_DSN`、`REDIS_ADDRESS`、`REDIS_PASSWORD`、`GATEWAY_*`、`VALUES_SECRET_PATTERNS` 等。**这些是本仓库「配置文件 + env 覆盖」的通道，dev 环境由 kustomize 提供，生产由部署侧提供，不在 CI secrets 清单内。**

### 7.3 「本地值与 CI 值分开管理」的约定

- **约定**：CI 里的 9 个 secret **必须不是**任何开发者本地 `data/dev-credentials.env` 的副本；两者独立生成、独立轮换。依据是仓库既有语义：CI profile 从不写凭据文件、也不读它（`internal/devfixture/files.go:170-179,197-199`），本地文件被 gitignore（`.gitignore:35`），二者本就不该相遇。
- 上面第 4.1 节的循环示例只是**搬运机制演示**；**建议**实际配置时改为临时生成一套一次性 CI 值（例如把 `deploy/dev/dev.sh:316` 与 `internal/devfixture/files.go:99-110` 的生成方式各跑一次），把生成的文件当次用完即弃，不要让 CI 值在个人机器上长期驻留。
- **纪律**：这些值只用于 ephemeral 的 CI k3d 环境，因此**它们不是生产凭据，任何情况下都不要把生产凭据填进这些名字**——`e2e` job 会把整套环境跑在 runner 上并把日志作为 artifact 上传（`test.yml:435-442`），日志脱敏只覆盖审计与 operation 错误路径（`internal/redact/sanitize.go:19-34`），不覆盖容器原始 stdout（`internal/app/app.go:123` 的 slog handler 无 `ReplaceAttr` 脱敏）。
- 仓库里确实存在**已提交的 dev-only 明文口令**：集群内 PostgreSQL 的固定口令出现在 `deploy/kustomize/base/secret.yaml:12-13`（头部 `:7-11` 注释声明 dev-only，且 JWT key 另有独立 Secret），并在 `deploy/dev/dev.sh:1739`、`deploy/dev/dev.sh:1834`、`deploy/dev/dev.sh:1836` 以内联 env 形态重复出现（此处按纪律只给位置不复述值）。它不参与本清单的 secret 配置，但**建议**：把它收敛到 `secretGenerator` 或 dev 生成文件，避免三处硬编码漂移。

## 8. 最小权限与纪律：workflow `permissions:` 逐 job 现状

`test.yml` 共 13 个 job，`sync-to-gitcode.yaml` 共 1 个 job。逐块核实结果：

| workflow | 作用域 | 声明 | 行号 | 评价 |
| --- | --- | --- | --- | --- |
| `test.yml` | workflow 级 | `permissions: contents: read` | `:15-16` | ✅ 全 job 默认只读 |
| `test.yml` → `sdk-check` | job | 继承 | `:37`（无块） | 只读 |
| `license-check` | job | 继承 | `:50` | 只读 |
| `install-sdk` | job | 继承 | `:70` | 只读 |
| `upgrade-sdk` | job | 继承 | `:87` | 只读 |
| `operator-image-sdk-only` | job | 继承 | `:104` | 只读 |
| `test` | job | 继承 | `:118` | 只读；`GITHUB_TOKEN` 仅传给 buf action（`:135`） |
| `test-sqlite` | job | 继承 | `:202` | 只读（`:219` 同上） |
| `test-sdkcheck` | job | 继承 | `:245` | 只读 |
| `rollout-watch` | job | 继承 | `:284` | 只读 |
| `e2e-prerequisite` | job | 继承 | `:324` | 只读；上传 artifact（`:359-365`） |
| `e2e` | job | **显式重复** `contents: read` | `:380-381` | 只读，与继承值一致（冗余但无害；可理解为「特权 job 处再声明一次」的自觉） |
| `docs-check` | job | 继承 | `:454` | 只读；不装 Go、不取任何 secret（`make check-docs` 只用 bash/grep/git） |
| `proto-check` | job | 继承 | `:470` | 只读；`GITHUB_TOKEN` 仅传给 buf action（`:481`），与 `test`/`test-sqlite` 同形 |
| `sync-to-gitcode.yaml` | workflow / job | **完全没有 `permissions:` 块** | 全文件（`:1-25`） | ⚠️ **实际写外部系统的唯一 job 反而没有显式收敛**。`GITHUB_TOKEN` 的权限将继承组织/仓库默认设置（平台行为），一旦默认是「Read and write」，这个 run 就在环境里带了一把未被使用的可写 token，而它同时持有 `GITCODE_TOKEN` |

据此的纪律性结论与建议：

1. **现状：两个文件里没有任何写权限 job、没有任何 `pull-requests: write` / `id-token: write` / `deployments: write`**（逐 job 核实，见上表），所以「按需最小」在 `test.yml` 这条线上成立（`test.yml:15-16,380-381`）。**建议**：给 `sync-to-gitcode.yaml` 补上 workflow 级 `permissions: contents: read`，与 `test.yml` 对齐。
2. **`GITCODE_TOKEN` 走 env 而非命令行**，注释与实现一致（`sync-to-gitcode.yaml:20-22`，命令行只出现 `$GITCODE_REMOTE` 变量名，`:25`）。**残留风险**：凭据被拼进 URL 字符串，git 在报错时可能回显 remote URL；Actions 会按 secret 名做日志掩码，但**建议**改用 `git -c http.extraHeader=` 或 `GIT_ASKPASS` 形式，让 token 不出现在 URL 里。
3. **表达式内插进 shell 脚本的注入面**：`test.yml:301` 把 `${{ vars.RUNS_ON }}` 直接写进 `if [ "..." = "self-hosted" ]`，`test.yml:200` 把 `${{ github.event.* }}` 写进 action 的 `args`。`vars.*` 由仓库管理员控制，风险有限；**建议**统一改为 `env:` 传值（GitHub 官方推荐的脚本注入规避方式），`test.yml:317` 已经是这种更安全的写法。
4. **第三方 action 全部按 tag 引用，0 个 commit SHA pin**（两文件合计 34 处 `uses:`：`actions/checkout@v7` ×13、`actions/setup-go@v7` ×11、`bufbuild/buf-setup-action@v1.50.0` ×3、`actions/upload-artifact@v7` ×2、`actions/cache/restore@v6` ×2、`golangci/golangci-lint-action@v9.3.0` ×1、`actions/cache@v6` ×1）。**建议**：对非 `actions/*` 官方 action 至少锁定 `v1.50.0` / `v9.3.0` 对应的 commit SHA。
5. **下载物完整性**：kind 与 k3d 二进制都做了 sha256 校验（`test.yml:295-309` 的 `KIND_SHA256`、`test.yml:345-348` 与 `:413-416` 的 `checksums.txt`），集群节点镜像与 workload 镜像按 digest pin（`Makefile:13-14`、`test.yml:317`）。这是仓库里现存的正面控制。
6. **制品与日志**：`e2e`/`e2e-prerequisite` 用 `if: always()` 上传 `e2e-results/`（`test.yml:359-365`、`:435-442`，保留 7 天），内容包括**未脱敏的容器日志**（`test/e2e/prerequisite/capture-logs.sh:30` 直接 `kubectl logs`）。**建议**：上传前对日志跑一遍 `internal/redact` 的同款模式（`internal/redact/sanitize.go:19-34`），或把 artifact 权限收窄到维护者。
7. **本文自身的纪律**：第 4.1 节的示例命令都不回显值；`gh secret list` / `gh variable list` 只返回名字。**不要**在本仓库任何 workflow 里加 `gh secret list` 之外的读值操作——GitHub 也不提供读回明文的 API。

## 9. 需人工补充 / 未找到清单

| 条目 | 现状 | 需要谁做什么 |
| --- | --- | --- |
| `GITCODE_TOKEN` 的取值来源与格式 | 仓库内无校验代码（见 5.6） | 维护者去 GitCode 侧建最小权限 PAT 并记录命名规范 |
| 环境级 secret 的引入 | `environment:` 关键字 0 处（见 4.3） | 需产品/运维决策，改动 workflow |
| secret 轮换周期与责任人 | 无脚本、无 schedule、无到期登记（见 6.2） | 建议在知识库 REQ 面登记 owner + 周期 |
| live-PostgreSQL 门禁在 CI 的执行 | `POSTGRES_TEST_DSN` 未注入 → CI 上恒 skip（见 7.2） | 建议补 service container 或专用 runner |
| 自托管 runner 的标签与主机名口径 | 注释含主机名（`test.yml:30-32`） | 对外文档建议只写「本地自托管 runner」 |

> 事实源：
> `.github/workflows/test.yml`、`.github/workflows/sync-to-gitcode.yaml`、`Makefile`（`dev-*`、`e2e-*`、`check-licenses` 目标）、
> `deploy/dev/dev.sh`、`deploy/dev/dev_test.go`、`cmd/devseed/main.go`、`cmd/devseed/mtls_ca.go`、
> `internal/devfixture/files.go`、`internal/devfixture/accounts_trust.go`、`internal/devfixture/runner.go`、
> `deploy/kustomize/dev/kustomization.yaml`、`deploy/kustomize/services/orchestrator.yaml`、`deploy/kustomize/services/webhook.yaml`、`deploy/kustomize/base/secret.yaml`、
> `internal/operator/ca/ca.go`、`internal/auth/jwt.go`、`internal/auth/service_token.go`、`internal/trust/service.go`、`api/proto/trust/v1/trust.proto`、
> `cmd/orchestrator/main.go`、`cmd/webhook/main.go`、`cmd/auth/main.go`、`cmd/api/main.go`、
> `internal/config/config.go`、`internal/redact/sanitize.go`、`internal/app/app.go`、
> `test/e2e/prerequisite/smoke.sh`、`test/e2e/prerequisite/capture-logs.sh`、`configs/e2e.dev.yaml`、`http-client.env.json`、`.gitignore`、
> `docs/dev-environment.md`、`docs/testing.md`、`docs/architecture.md`、`CONTRIBUTING.md`、`AGENTS.md`

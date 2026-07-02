# E2E 全链路测试设计

## 概述

为 Release Manager 构建覆盖全链路的自动化 E2E 测试体系，支持本地开发和 CI 流水线双重场景。测试编排采用 Go test + kind Go API，全组件部署于 kind 集群内，通过 NodePort 与测试进程交互。

## 设计目标

1. **一键运行**：本地 `make test-e2e-full`，CI `go test -tags=e2e ./test/e2e/...`
2. **环境灵活**：有 Harbor 走完整 webhook HMAC 链路，无 Harbor 自动降级为本地 registry + 直接 API
3. **全量覆盖**：happy path、异常场景、多客户并发、证书热更新、钉钉通知
4. **可调试性**：失败时自动 dump 集群状态，支持集群复用/保留加速迭代

## 架构

### 测试基础设施拓扑

```
┌─────────────────────────────────────────────────────────────┐
│                    go test 进程                              │
│  ┌─────────────────────────────────────────────────────────┐ │
│  │              E2E Test Suite (test/e2e/)                   │ │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────────────────────┐│ │
│  │  │ Kind     │  │ Registry │  │  Scenarios               ││ │
│  │  │ Provider │  │ Deployer │  │  (table-driven tests)    ││ │
│  │  └────┬─────┘  └────┬─────┘  └────────────┬─────────────┘│ │
│  │       │             │                     │              │ │
│  │       ▼             ▼                     ▼              │ │
│  │  ┌────────────────────────────────────────────────────┐  │ │
│  │  │                  Test Harness                       │  │ │
│  │  │  (clients, config, wait helpers, assertions)       │  │ │
│  │  └────────────────────────────────────────────────────┘  │ │
│  └─────────────────────────────────────────────────────────┘ │
│                              │                                │
│         localhost:30500      │     localhost:8080             │
│         (registry)           │     (manager NodePort)         │
│                              ▼                                │
│  ┌─────────────────────────────────────────────────────────┐ │
│  │                  Kind Cluster                            │ │
│  │  ┌──────────┐   ┌──────────┐   ┌──────────────────────┐ │ │
│  │  │ registry │   │ release- │   │ release-manager      │ │ │
│  │  │ :3       │◄──│ operator │◄──│ (NodePort)           │ │ │
│  │  │ NodePort │   │ (Helm    │   │ webhook :8080        │ │ │
│  │  │          │   │  SDK)    │   │ gRPC   :8443         │ │ │
│  │  └──────────┘   └──────────┘   └──────────────────────┘ │ │
│  └─────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

### 核心设计决策

1. **全组件入 Kind 集群**：manager、operator、registry 全部部署在 kind 内，通过 NodePort 暴露。测试代码通过 `localhost:PORT` 与集群内服务交互，避免 `host.docker.internal` 跨平台问题，与生产部署拓扑一致。

2. **两阶段启动**：
   - **Infra (Suite 级别)**：kind 集群创建、registry 部署、TLS 证书生成、镜像构建，`TestMain` 中执行一次，所有 scenario 共享。
   - **Scenario (Test 级别)**：每个测试独立部署 operator + manager、推送 chart、执行验证，`-run` 可筛选单个场景。

3. **Harbor / 本地双模式**：通过环境变量自动切换。

## 包结构

```
test/e2e/
├── main_test.go                # TestMain: 全局 setup/teardown
├── harness.go                  # Harness 结构体 + 工厂方法
├── kind_setup.go               # kind 集群创建/连接
├── registry_setup.go           # registry 部署 (proxy / standalone 双模式)
├── operator_setup.go           # release-operator Helm 部署
├── manager_setup.go            # release-manager Kustomize 部署
├── certs_setup.go              # mTLS 证书生成 (调用 openssl)
├── wait.go                     # 等待辅助函数
├── scenario_happy_path_test.go # Scenario 1: 核心链路
├── scenario_failures_test.go   # Scenario 2: 异常场景
├── scenario_multi_cust_test.go # Scenario 3: 多客户并发
├── scenario_cert_reload_test.go# Scenario 4: 证书热更新
├── scenario_dingtalk_test.go   # Scenario 5: 钉钉通知
├── mock_dingtalk.go            # 钉钉 mock server
├── fixtures_testchart.go       # embed 内置测试 chart
└── testdata/testchart/
    ├── Chart.yaml              # name: test-chart, version: 0.1.0
    ├── values.yaml             # replicaCount: 1, image: nginx:alpine
    └── templates/
        └── deployment.yaml     # Deployment + ConfigMap (含 chart version)
```

## 组件设计

### Harness 结构体

```go
type Harness struct {
    ClusterName string
    Kubeconfig  string
    RestConfig  *rest.Config
    K8sClient   kubernetes.Interface

    // Endpoints (NodePort)
    RegistryAddr string
    ManagerAddr  string

    // mTLS
    CustomerID  string
    CABundle    []byte
    ClientCert  tls.Certificate
    Fingerprint string

    // Harbor mode
    HarborURL      string
    HarborUser     string
    HarborPassword string

    // Cleanup stack
    cleanupFns []func()
    mu         sync.Mutex
}
```

### 关键方法

| 方法 | 作用 |
|------|------|
| `NewHarness(opts ...HarnessOpt)` | 创建并初始化全套基础设施 |
| `h.DeployOperator(customerID string)` | 为指定客户部署 operator |
| `h.DeployManager()` | 部署 manager（含 Kustomize 渲染） |
| `h.PushChart(chartPath, version)` | 推送测试 chart 到 registry |
| `h.TriggerWebhook(chartName, version)` | 触发 webhook（Harbor 模式算 HMAC，本地模式直接 POST） |
| `h.WaitForRelease(customerID, chartName)` | 等待 release 状态变为 success |
| `h.WaitForPodReady(namespace, labels)` | 等待 Pod Ready |
| `h.Close()` | 逆序执行 cleanup stack |

### 等待机制

```go
func RetryUntil(ctx context.Context, interval, timeout time.Duration, fn func() (bool, error)) error
```

底层使用 `k8s.io/apimachinery/pkg/util/wait.PollUntilContextTimeout`，默认 interval 2s。

| 操作 | 默认超时 |
|------|---------|
| kind 集群创建 | 5 min |
| registry 部署 | 2 min |
| operator 部署 | 2 min |
| manager 部署 | 2 min |
| helm upgrade | 120 s |
| 等待 release 成功 | 180 s |
| 等待 Pod Ready | 120 s |

### 内置测试 Chart

用 `embed` 嵌入编译进测试二进制。极简结构：一个 `nginx:alpine` Deployment 加一个含 chart version 的 ConfigMap。每次场景修改 version 字段后重新打包推送，模拟真实发版。

### Harbor / 本地双模式

| 步骤 | Harbor 模式 | 本地模式 |
|------|------------|---------|
| Chart 推送目标 | Harbor OCI | `localhost:30500` (standalone registry) |
| Chart 推送方式 | `helm push` 到 Harbor OCI（需 login） | `helm push` 到 localhost:30500（无认证） |
| Webhook 触发 | 计算 HMAC-SHA256 签名后 POST | 直接 POST JSON（无签名验证） |
| 镜像拉取源 | Harbor → registry proxy cache | registry:3 本地独立存储 |
| 启用条件 | `HARBOR_URL` + `HARBOR_ROBOT_TOKEN` 均设 | 任一未设 |

Standalone registry 模式下 registry:3 配置为独立 registry（不设 `proxy.remoteurl`），测试通过 `helm registry login localhost:30500` + `helm push` 直接将 chart 推送到本地 registry。Webhook 触发时跳过 HMAC 签名验证（manager 配置 `hmac_key=""`），测试代码直接 POST JSON payload 到 `/api/v1/webhook/harbor`。

## 测试场景

### Scenario 1: 核心 Happy Path

**Given** kind 集群已运行，registry/operator/manager 已部署，客户已注册
**When** 推送 test-chart v0.1.0 到 registry，触发 webhook (`POST /api/v1/webhook/harbor`)
**Then**
- manager 返回 200 `{"message":"accepted"}`
- 120s 内 operator 完成 helm upgrade
- `GET /api/v1/releases?customer_id=localhost001` 返回 status=success, version=v0.1.0
- 目标 namespace 中 Pod `app=test-chart` Ready
- 再次推送 v0.1.1，验证升级成功（helm history 有 2 个 revision）

### Scenario 2: 异常场景

**2a. Operator 不可达**
**Given** operator 的 `notificationEndpoint` 指向不存在的地址
**When** 触发 webhook
**Then** forwarder 返回 dial 错误，release 状态为 failed，错误信息包含 "dial operator"

**2b. Helm Upgrade 失败**
**Given** operator 正常运行
**When** 推送 `image: invalid-image:no-exist` 的 chart，helm upgrade 带 `--atomic --wait`
**Then** helm upgrade 失败回滚，release 状态为 failed，错误信息包含 pull/upgrade 失败原因

**2c. 客户未启用**
**Given** 客户 `enabled=false`
**When** 触发 webhook
**Then** 该客户不会被转发通知，forwarder 只向 enabled 客户发送

### Scenario 3: 多客户并发转发

**Given** 3 个 operator 实例部署在同一 kind 集群的不同 namespace（customer-001, customer-002, customer-003），通过不同 NodePort 暴露 gRPC，均已启用
**When** 触发一次 webhook
**Then**
- forwarder 并发向 3 个 operator 发送 NotifyRelease
- 3 个 release 均成功（status=success）
- 总耗时 ≈ max(单次耗时)，而非 sum（验证并发非串行）
- `GET /api/v1/releases` 返回 3 条记录

### Scenario 4: 证书热更新

**Given** operator 使用 cert-A 启动，manager 信任 cert-A
**When**
1. 生成 cert-B（新密钥对）
2. 调用 `UpdateCertificate` gRPC RPC 传入 cert-B
**Then**
- operator 热加载 cert-B（无需重启）
- manager 使用 cert-B 可重新建立 gRPC 连接
- 旧的 cert-A 被拒绝（mTLS 握手失败）

### Scenario 5: 钉钉通知

**Given** 测试进程内启动 mock DingTalk HTTP server（记录收到的请求 body）
**When** 触发 webhook → release 完成（含成功和失败各一条）
**Then**
- mock server 收到 2 条 POST 请求
- 成功通知包含：chart 名、版本、客户 ID、耗时
- 失败通知包含：chart 名、版本、客户 ID、失败原因
- Markdown 格式符合 dingtalk.go 中定义的消息模板

## 错误处理与可调试性

### TestMain 生命周期

```go
func TestMain(m *testing.M) {
    // 0. 可选：复用已有集群 (KIND_CLUSTER_REUSE=name)
    // 1. 创建 kind 集群 (或连接已有)
    // 2. 部署 registry → 等待 Ready
    // 3. 生成 mTLS 证书
    // 4. 构建并加载 operator/manager 镜像
    // 5. 运行所有测试
    code := m.Run()
    // 6. 清理 (KEEP_CLUSTER=1 时跳过)
    os.Exit(code)
}
```

### 失败状态 Dump

测试失败时自动收集：
- 所有 Pod 状态
- 相关 Helm release 状态
- operator 日志（最近 50 行）
- manager release records（通过 API）

`sigs.k8s.io/kind` 自身在集群创建失败时保留日志。CI 中 `kind export logs` 作为 artifact 上传。

### 调试环境变量

| 变量 | 作用 |
|------|------|
| `KIND_CLUSTER_REUSE=name` | 跳过创建，重用已有集群 |
| `KEEP_CLUSTER=1` | 测试结束后不删除集群 |
| `SKIP_BUILD=1` | 跳过镜像构建（仅重新部署） |
| `E2E_TIMEOUT_MULTIPLIER=2` | 超时倍率（CI 中网络慢时使用） |

### Debug 工作流

```bash
# 第一次：完整运行，保留现场
go test -tags=e2e -run TestHappyPath ./test/e2e/ -v
# → 失败，集群保留

# 排查
kubectl get pods -A
kubectl logs -n release-operator deploy/release-operator

# 改代码后仅重新构建部署（复用已有集群+跳过镜像构建）
KIND_CLUSTER_REUSE=release-e2e-test KEEP_CLUSTER=1 go test -tags=e2e -run TestHappyPath ./test/e2e/

# 修好了，清理
kind delete cluster --name release-e2e-test
```

## Makefile 集成

### 新增目标

```makefile
.PHONY: test-e2e-full
test-e2e-full: ## 全链路 E2E 测试 (本地一键)
    $(GO) test -tags=e2e -v -timeout 30m -count=1 ./test/e2e/...

.PHONY: test-e2e-local
test-e2e-local: build image-operator image-manager ## 本地快速 E2E (跳过镜像构建)
    SKIP_BUILD=1 $(GO) test -tags=e2e -v -timeout 30m -count=1 ./test/e2e/...

.PHONY: test-e2e-scenario
test-e2e-scenario: ## 运行单个 E2E 场景 (需 SCENARIO=TestHappyPath)
    $(GO) test -tags=e2e -v -timeout 10m -count=1 -run $(SCENARIO) ./test/e2e/...
```

### 与现有目标的关系

```
make test          → 单元测试 (不变)
make test-e2e      → operator E2E (不变，保留原有)
make test-e2e-full → 全链路 E2E (新增)
```

### 本地迭代流程

```bash
make test-e2e-full                                                 # 首次完整运行
SCENARIO=TestOperatorUnreachable make test-e2e-scenario             # 快速重跑某场景
KEEP_CLUSTER=1 go test -tags=e2e -run TestHappyPath ./test/e2e/ -v # 保留集群调试
```

## CI 集成

```yaml
# .github/workflows/e2e.yml
name: E2E Tests
on:
  pull_request:
    paths-ignore: ['docs/**', '*.md']
  push:
    branches: [main]

jobs:
  e2e:
    runs-on: ubuntu-latest
    timeout-minutes: 45
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - name: Run E2E tests
        run: go test -tags=e2e -v -timeout 30m ./test/e2e/...
        env:
          HARBOR_URL: ${{ secrets.HARBOR_URL }}
          HARBOR_ROBOT_TOKEN: ${{ secrets.HARBOR_ROBOT_TOKEN }}
      - name: Export kind logs (on failure)
        if: failure()
        run: kind export logs /tmp/kind-logs
      - uses: actions/upload-artifact@v4
        if: failure()
        with:
          name: kind-logs
          path: /tmp/kind-logs
```

## 文件变更清单

### 新增

```
test/e2e/                         # E2E 测试包 (14 文件)
├── main_test.go
├── harness.go
├── kind_setup.go
├── registry_setup.go
├── operator_setup.go
├── manager_setup.go
├── certs_setup.go
├── wait.go
├── scenario_happy_path_test.go
├── scenario_failures_test.go
├── scenario_multi_cust_test.go
├── scenario_cert_reload_test.go
├── scenario_dingtalk_test.go
├── mock_dingtalk.go
├── fixtures_testchart.go
└── testdata/testchart/           # 内置测试 chart
    ├── Chart.yaml
    ├── values.yaml
    └── templates/deployment.yaml

.github/workflows/e2e.yml          # CI E2E 流水线
```

### 修改

| 文件 | 改动 |
|------|------|
| `Makefile` | 新增 `test-e2e-full`、`test-e2e-local`、`test-e2e-scenario` 目标 |
| `.gitignore` | 追加 `test/e2e/testdata/*.tgz` |

## 技术依赖

- `sigs.k8s.io/kind` — kind 集群 Go API
- `k8s.io/client-go` — K8s 客户端
- `helm.sh/helm/v4` — Helm Go SDK（operator 部署、chart 推送）
- `github.com/stretchr/testify` — 断言 + suite（项目已使用）
- `embed` — 内置测试 chart（标准库）
- `crypto/tls`, `crypto/x509` — 证书生成与验证（标准库）

# Release Manager 设计文档

## 1. 设计目标

为私有化部署的客户 K8s 集群构建自动化 Helm chart 更新系统，
实现从 CI 构建到客户集群自动更新的全链路自动化。

### 核心需求

1. CI 构建镜像 → 推送 Harbor → 打包 Helm chart → 推送 Harbor
2. Harbor webhook 触发 → release-manager 接收
3. release-manager 管理客户白名单，按需推送
4. release-operator 自动拉取 chart 并执行 `helm upgrade`
5. 结果回传 → 钉钉通知运维团队
6. 全程 mTLS 加密通信

## 2. 系统架构

```
┌───────────┐   push image+chart   ┌──────────┐  webhook    ┌─────────────────────┐
│ CI/CD     │ ────────────────────→│  Harbor  │ ─────────→  │ release-manager │
│ Pipeline  │                      │ (OCI)    │  (HTTP)     │ (中心服务)            │
└───────────┘                      └──────────┘             │                      │
                                                            │ - /api/v1/webhook    │
                                                            │ - 客户白名单 CRUD     │
                                                            │ - gRPC 转发到客户    │
                                                            │ - 钉钉通知           │
                                                            └──────┬──────────────┘
                                                                   │ mTLS gRPC
                                                            ┌──────▼──────────────┐
                    ┌────────────────────────────┐          │ release-operator    │
                    │  客户私有化 K8s 集群         │          │ (客户集群 × N)       │
                    │                            │          │                      │
                    │  K8s Pod                   │          │ - gRPC server :8443  │
                    │    ↓ imagePullPolicy       │          │ - Helm v4 SDK 操作   │
                    │  localhost:5000             │◄─────────│ - 异步状态机          │
                    │    ↓ proxy.remoteurl       │  helm    │ - 状态上报            │
                    │  Harbor (缓存+代理)         │  upgrade └──────────────────────┘
                    └────────────────────────────┘
```

### 镜像拉取链路

```
K8s Pod (image: localhost:5000/xxx/yyy:tag)
  → localhost:5000 (registry:2, proxy mode)
    → proxy.remoteurl = https://harbor.example.com
      → Harbor (actual image)
```

- registry:2 配置 `proxy.remoteurl` 指向 Harbor
- 镜像缓存到本地，加速后续拉取
- release-operator 只更新 chart values 中的 `imageTag`

## 3. 组件设计

### 3.1 release-operator (客户集群)

**定位:** 轻量级 gRPC 服务，部署在客户集群内，负责接收更新通知并执行 Helm 操作。

**状态机:**

```
                  ┌──────────────┐
                  │   Pending    │ ◄── 收到 NotifyRelease
                  └──────┬───────┘
                         │
                  ┌──────▼───────┐
                  │ PullingChart │
                  └──────┬───────┘
                    ╱         ╲
           ┌───────▼──┐   ┌───▼──────────┐
           │ Upgrading│   │ PullFailed   │
           └───────┬──┘   └──────────────┘
              ╱       ╲
    ┌────────▼─┐  ┌───▼───────────┐
    │Succeeded │  │UpgradeFailed  │
    └──────────┘  └───┬───────────┘
                       │
                ┌──────▼───────┐
                │ RollingBack  │
                └──────┬───────┘
                  ╱         ╲
         ┌───────▼──┐  ┌────▼──────────┐
         │RolledBack│  │RollbackFailed │
         └──────────┘  └───────────────┘
```

**核心机制:**

- **异步处理:** gRPC handler 收到请求后立即返回 ACK，入队异步处理
- **幂等去重:** 相同 `request_id + release_name` 的重复请求直接跳过
- **版本跳过:** 当前已部署版本与目标版本一致时，直接标记成功
- **自动回滚:** Install 用 `RollbackOnFailure`，Upgrade 失败时使用 Helm 的 `--atomic` 等价逻辑手动 rollback

### 3.2 release-manager (中心服务)

**定位:** 接收 Harbor webhook，管理客户白名单，向各客户 operator 推送更新通知，
收集结果并通过钉钉通知。

**核心流程:**

1. Harbor webhook → 解析 `PUSH_HELMCHART` 事件
2. 提取 chart 信息 → 查询 customer store
3. 筛选 `enabled=true` 的客户
4. 并发 gRPC 调用各客户 operator 的 `NotifyRelease`
5. 各 operator 异步处理，完成后通过 `ReportStatus` 上报
6. 收集结果 → 钉钉 Markdown 通知

## 4. 数据模型

### Customer（客户）

| 字段 | 类型 | 描述 |
|------|------|------|
| id | string | 唯一标识，如 `customer-001` |
| name | string | 客户名称 |
| operator_endpoint | string | operator gRPC 地址 |
| cert_fingerprint | string | 客户端证书 SHA256 指纹 |
| enabled | bool | 是否启用自动更新 |
| labels | map | 分组/筛选标签 |

### ReleaseRecord（发布记录）

| 字段 | 类型 | 描述 |
|------|------|------|
| request_id | string | 唯一请求 ID |
| customer_id | string | 客户 ID |
| chart_name | string | Chart 名称 |
| chart_version | string | Chart 版本 |
| status | string | 状态 |
| error_message | string | 错误信息 |
| duration_secs | int64 | 耗时 |
| started_at | time | 开始时间 |
| completed_at | time | 完成时间 |

## 5. 安全设计

### 5.1 mTLS

```
┌─────────────────────┐         ┌─────────────────────┐
│ release-manager│         │ release-operator    │
│                     │         │                     │
│ CA: certs/ca/ca.crt │ ─────→  │ CA: certs/ca/ca.crt │
│ Cert: server/tls.*  │ ←─────  │ Cert: customer/tls.*│
│ Key:  server/tls.*  │  mTLS   │ Key:  customer/tls.*│
└─────────────────────┘         └─────────────────────┘
```

- **CA 证书**统一签发，10 年有效期
- **服务端证书**（notification）SAN 含 DNS 和 IP
- **客户端证书**（operator）CN 为 `customer-<id>`，1 年有效期
- **指纹验证:** notification 端校验客户端证书的 SHA256 指纹是否在白名单中

### 5.2 凭证管理

- Harbor 凭证：K8s Secret → 环境变量 → SDK `ClientOptBasicAuth`
- 钉钉 Secret：配置文件（K8s ConfigMap）

## 6. 超时策略

| 层级 | 超时设置 | 默认值 |
|------|----------|--------|
| Helm upgrade | `helm upgrade --timeout` | 10 分钟 |
| Image pull | kubelet `--image-pull-progress-deadline` | 1 分钟 |
| Registry proxy | registry:2 `proxy.remoteurl` HTTP timeout | 30 秒 |
| gRPC forward | `context.WithTimeout` | 60 秒 |
| gRPC report status | retry with backoff | 5 次，最大 30s 等待 |

---
[← 返回 README](../README.md)

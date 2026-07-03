# Release Manager 设计文档

## 1. 设计目标

为私有化部署的客户 K8s 集群构建自动化 Helm chart 更新系统，
实现从 CI 构建到客户集群自动更新的全链路自动化。

## 2. 业务逻辑拓扑图

### 2.1 全链路拓扑

```
                          ┌─────────────────────────────────┐
                          │        CI/CD Pipeline           │
                          │  docker build → Harbor (image) │
                          │  helm package → Harbor (chart) │
                          └──────────────┬──────────────────┘
                                         │ push
                                  ┌──────▼──────┐
                                  │    Harbor   │ ← 唯一镜像源 & Chart 源
                                  │  (OCI Hub)  │
                                  └──────┬──────┘
                                         │ PUSH_HELMCHART webhook
                                         │ HMAC-SHA256 签名
                                  ┌──────▼──────────────┐
                                  │  release-manager    │
                                  │  (中心管理平台)      │
                                  │                     │
                                  │  ┌───────────────┐  │
                                  │  │ Webhook 验证   │  │
                                  │  │ (HMAC-SHA256)  │  │
                                  │  └───────┬───────┘  │
                                  │          │          │
                                  │  ┌───────▼───────┐  │
                                  │  │ 客户白名单匹配  │  │
                                  │  │ (Store:        │  │
                                  │  │  enabled=true) │  │
                                  │  └───────┬───────┘  │
                                  │          │          │
                                  │  ┌───────▼───────┐  │
                                  │  │ gRPC 并发转发  │  │
                                  │  │ (mTLS)        │  │
                                  │  └───────┬───────┘  │
                                  │          │          │
                                  │  ┌───────▼───────┐  │
                                  │  │ 状态收集+钉钉  │  │
                                  │  │ (DingTalk Bot)│  │
                                  │  └───────────────┘  │
                                  └──────┬──────────────┘
                                         │ mTLS gRPC (证书指纹白名单)
                         ┌───────────────┼───────────────┐
                         │               │               │
                  ┌──────▼──────┐ ┌──────▼──────┐ ┌──────▼──────┐
                  │ customer-001│ │ customer-002│ │ customer-N  │
                  │ (私有化集群) │ │ (私有化集群) │ │ (私有化集群) │
                  └──────┬──────┘ └──────┬──────┘ └──────┬──────┘
                         │               │               │
                         └───────────────┼───────────────┘
                                         │
                              ┌──────────▼──────────┐
                              │   release-operator  │
                              │   ┌──────────────┐  │
                              │   │ gRPC Server   │  │
                              │   │ (TLS 热加载)  │  │
                              │   └──────┬───────┘  │
                              │          │           │
                              │   ┌──────▼───────┐  │
                              │   │ 异步状态机    │  │
                              │   │ Pending→Pull  │  │
                              │   │ →Upgrade→Done │  │
                              │   └──────┬───────┘  │
                              │          │           │
                              │   ┌──────▼───────┐  │
                              │   │ Helm v4 SDK  │  │
                              │   │ pull oci://   │  │
                              │   │ upgrade --atomic│ │
                              │   └──────┬───────┘  │
                              └──────────┼──────────┘
                                         │ helm pull
                              ┌──────────▼──────────┐
                              │  registry:3         │
                              │  (localhost:5000)   │
                              │  proxy.remoteurl =  │
                              │  https://harbor.xxx │
                              └──────────┬──────────┘
                                         │ proxy
                                  ┌──────▼──────┐
                                  │    Harbor   │ ← 唯一源
                                  │  (OCI Hub)  │
                                  └─────────────┘
```

### 2.2 数据流时序

```
CI       Harbor    Manager   Operator(cust-001)  Registry    DingTalk
│          │         │           │                 │           │
│─push────→│         │           │                 │           │
│          │─webhook→│           │                 │           │
│          │         │─gRPC───────────────────→    │           │
│          │         │  NotifyRelease(chart)       │           │
│          │         │←────ACK────────────         │           │
│          │         │           │                 │           │
│          │         │           │─helm pull oci───→│           │
│          │         │           │  localhost:5000  │─proxy────→│
│          │         │           │←────chart.tgz────│←──cached──│
│          │         │           │                 │           │
│          │         │           │─helm upgrade───→│           │
│          │         │           │  --atomic       │           │
│          │         │           │←────deployed────│           │
│          │         │           │                 │           │
│          │         │←──gRPC───│                 │           │
│          │         │ ReportStatus(result)        │           │
│          │────────────────────────────────────────────────→│
│          │         │           │         DingTalk Markdown  │
│          │         │           │                 │           │
```

### 2.3 认证与授权流程

```
用户访问 release-manager Web UI:

  1. 首次访问 → GET /api/v1/init
     ├─ dev_mode=true → 自动初始化 admin/admin
     └─ 生产环境 → 显示初始化表单 (用户名+密码+邮箱+SMTP验证)

  2. 登录 → AuthMiddleware 链式认证:
     ├─ API Key (X-API-Key header)
     ├─ Session (Bearer token → Cache 查找)
     ├─ LDAP (Basic Auth → 绑定 → 组映射)
     ├─ OIDC (ID Token → JWT 验证)
     └─ 钉钉 (扫码 → code换token → userid匹配)

  3. 授权 → Casbin RBAC:
     sub=userID, org=orgID, obj=path, act=method
     → admin: 全部 CRUD
     → operator: 客户/Chart/发布/证书 CRUD
     → viewer: 全部只读
```

### 2.4 TLS 证书热更新流程

```
release-manager                     release-operator (客户集群)
     │                                      │
     │── gRPC UpdateCertificate ──────────→ │
     │   {tls_cert_pem, tls_key_pem}        │
     │                                      │
     │                                  ┌───▼─────────────────────┐
     │                                  │ certMu.Lock()           │
     │                                  │ os.WriteFile(certFile)  │
     │                                  │ os.WriteFile(keyFile)   │
     │                                  │ certMu.Unlock()         │
     │                                  └───┬─────────────────────┘
     │                                      │
     │                                      │ 下一次 TLS 握手:
     │                                      │ GetCertificate() 回调
     │                                      │ → LoadX509KeyPair(certFile, keyFile)
     │                                      │ → 新证书自动生效
     │                                      │
     │←── { new_fingerprint } ───────────── │
     │                                      │
     │── PUT /customers/{id} ──→ 更新白名单指纹
```

## 3. Registry Proxy 代理架构

```
客户私有化 K8s 集群内:

  所有镜像拉取和 Chart 下载:
  ┌──────────┐
  │ K8s Pod  │ image: localhost:5000/aliyun-zcb/xxx:tag
  └────┬─────┘
       │ kubelet 拉取
  ┌────▼────────────────────────────────────────────┐
  │  registry:3 (registry-proxy Deployment)          │
  │  NodePort :30500                                 │
  │                                                  │
  │  REGISTRY_PROXY_REMOTEURL=https://harbor.xxx     │
  │  REGISTRY_PROXY_USERNAME=robot$xxx               │
  │  REGISTRY_PROXY_PASSWORD=<token>                 │
  │  REGISTRY_HTTP_TLS_INSECURE=true                 │
  │                                                  │
  │  本地缓存: /var/lib/registry (PVC)               │
  └────┬────────────────────────────────────────────┘
       │ proxy.remoteurl (pull-through cache)
  ┌────▼──────┐
  │  Harbor   │ ← 唯一源 (所有镜像/Chart 的真实存储)
  │  (OCI)    │
  └───────────┘

  Helm SDK 拉取:
  release-operator → helm pull oci://localhost:5000/helm/magic-sandbox
    → registry:3 proxy → Harbor OCI
```

## 4. 组件设计

### 4.1 release-operator (客户集群)

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
- **异步处理:** gRPC handler ACK 后入队，后台 goroutine 执行
- **幂等去重:** `sync.RWMutex` 保护 active map
- **并发安全:** 每个 release 独立 goroutine，`sync.WaitGroup` 优雅关闭
- **状态上报:** 3 分钟超时 `context.WithTimeout` + 5 次指数退避重试

### 4.2 release-manager (中心服务)

**核心流程:**
1. Harbor webhook → HMAC-SHA256 验证 → 解析 `PUSH_HELMCHART`
2. 提取 chart 信息 → Store.ListCustomers(enabled=true) → 匹配客户
3. `errgroup` 并发 gRPC → 各客户 operator `NotifyRelease`
4. operator 异步处理 → `ReportStatus` 上报
5. 收集结果 → DingTalk Markdown 通知

### 4.3 registry-proxy (Registry 代理)

**配置:**
```yaml
# registry:3 作为 Harbor pull-through cache proxy
REGISTRY_PROXY_REMOTEURL: https://harbor.example.com
REGISTRY_PROXY_USERNAME: robot$xxx
REGISTRY_PROXY_PASSWORD: <token>
REGISTRY_HTTP_TLS_INSECURE: "true"  # Harbor 自签证书
```

## 5. 数据模型

### Customer（客户）
| 字段 | 类型 | 描述 |
|------|------|------|
| id | string | 唯一标识，如 `customer-001` |
| name | string | 客户名称 |
| operator_endpoint | string | operator gRPC 地址 |
| cert_fingerprint | string | 客户端证书 SHA256 指纹 |
| enabled | bool | 是否启用自动更新 |

### ChartDefinition（Chart 定义）
| 字段 | 类型 | 描述 |
|------|------|------|
| id | string | Chart 唯一标识 |
| org_id | string | 所属组织 |
| name | string | Chart 名称 |
| oci_url | string | Harbor OCI URL |
| default_values | json | 默认 Helm values |

### CustomerChartBinding（客户-Chart 绑定）
| 字段 | 类型 | 描述 |
|------|------|------|
| customer_id | string | 客户 ID |
| chart_id | string | Chart ID |
| custom_values | json | 客户定制 values（覆盖默认） |
| deploy_order | int | 部署顺序 |
| namespace | string | 部署 namespace |

## 6. RBAC 权限管理

使用 casbin-go 框架:

```
[matchers]
g(r.sub, p.sub) && (p.org == r.org || p.org == "*")
&& keyMatch(r.obj, p.obj) && regexMatch(r.act, p.act)
```

| 角色 | 权限 | 继承 |
|------|------|------|
| admin | 全部 CRUD | admin → operator → viewer |
| operator | 客户/Chart/发布/证书 CRUD | operator → viewer |
| viewer | 全部 GET 只读 | - |

## 7. 认证体系

链式 `AuthMiddleware` 依次尝试:

| 提供者 | 方式 |
|--------|------|
| API Key | `X-API-Key` header |
| Session | Bearer token + Cache (24h TTL) |
| LDAP | 搜索→绑定→组映射→角色 |
| OIDC | Authorization Code Flow + JWT |
| 钉钉 SSO | OAuth 扫码→code换token→userid |

## 8. 安全设计

### mTLS
- CA 统一签发，10 年有效期
- 客户端证书 CN=`customer-<id>`，3 年有效期
- `tls.Config.GetCertificate` 回调实现热加载
- 指纹白名单校验: `hmac.Equal` 常数时间比较

### Webhook
- HMAC-SHA256 签名验证 (`Authorization: Harbor-Signature <base64>`)
- 请求体限制 1 MiB (`http.MaxBytesReader`)
- Content-Type 强制 `application/json`

## 9. 存储

| 环境 | 驱动 | DSN |
|------|------|-----|
| 开发 | SQLite | `data/release-manager.db` |
| 生产 | PostgreSQL | `host=... port=5432 user=... password=... dbname=... sslmode=require` |

## 10. 超时策略

| 层级 | 默认值 |
|------|--------|
| Helm upgrade | 10 分钟 (`--timeout`) |
| Image pull | kubelet `--image-pull-progress-deadline` (1 分钟) |
| Registry proxy | registry:3 HTTP timeout |
| gRPC 转发 | `context.WithTimeout` 60s |
| 状态上报 | `context.WithTimeout` 3 分钟 + 5 次重试 |

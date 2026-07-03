# Release Manager API 文档

## 概述

Release Manager 系统提供三组 API：

| API | 协议 | 方向 | 描述 |
|-----|------|------|------|
| `ReleaseNotificationService` | gRPC | notification → operator | 通知客户集群有新 chart 发布 |
| `StatusReportService` | gRPC | operator → notification | operator 上报更新结果 |
| `CustomerManagementService` | gRPC + REST | 管理员 → notification | 客户白名单 CRUD |

---

## 1. ReleaseNotificationService

**服务端:** release-operator（客户集群）  
**客户端:** release-manager（中心）

### NotifyRelease

通知 operator 有新版本 chart 可用。

```protobuf
rpc NotifyRelease(NotifyReleaseRequest) returns (NotifyReleaseResponse);
```

**请求:**

| 字段 | 类型 | 必填 | 描述 |
|------|------|------|------|
| `chart_name` | string | ✅ | Helm chart 名称，如 `magic-sandbox` |
| `chart_version` | string | ✅ | Chart 版本，如 `0.0.15` |
| `chart_url` | string | ✅ | OCI URL，如 `oci://harbor.example.com/helm/magic-sandbox` |
| `images` | map<string,string> | ❌ | 组件名→镜像 tag 映射 |
| `release_notes` | string | ❌ | 发布说明 |
| `request_id` | string | ✅ | 唯一请求 ID（UUID v4），用于幂等 |
| `namespace` | string | ❌ | K8s namespace，默认 `default` |
| `release_name` | string | ❌ | Helm release 名称，默认同 chart_name |
| `timeout_seconds` | int32 | ❌ | 升级超时秒，0 使用默认值（600s） |

**响应:**

| 字段 | 类型 | 描述 |
|------|------|------|
| `accepted` | bool | 是否接受处理 |
| `message` | string | 响应消息 |

**幂等性:** 相同 `request_id` 的重复请求返回 `accepted: false`。

---

## 2. StatusReportService

**服务端:** release-manager（中心）  
**客户端:** release-operator（客户集群）

### ReportStatus

上报 release 更新结果。

```protobuf
rpc ReportStatus(ReportStatusRequest) returns (ReportStatusResponse);
```

**请求:**

| 字段 | 类型 | 必填 | 描述 |
|------|------|------|------|
| `customer_id` | string | ✅ | 客户标识 |
| `request_id` | string | ✅ | 对应的通知请求 ID |
| `chart_name` | string | ✅ | Chart 名称 |
| `chart_version` | string | ✅ | Chart 版本 |
| `status` | ReleaseStatus | ✅ | 最终状态 |
| `error_message` | string | ❌ | 失败原因描述 |
| `duration_seconds` | int64 | ✅ | 操作耗时（秒） |
| `started_at` | int64 | ✅ | 开始时间（Unix 时间戳） |
| `completed_at` | int64 | ✅ | 完成时间（Unix 时间戳） |

**ReleaseStatus 枚举:**

| 值 | 含义 |
|----|------|
| `RELEASE_STATUS_PENDING` | 等待处理 |
| `RELEASE_STATUS_PULLING_CHART` | 正在拉取 chart |
| `RELEASE_STATUS_UPGRADING` | 正在升级 |
| `RELEASE_STATUS_SUCCEEDED` | 升级成功 |
| `RELEASE_STATUS_FAILED` | 升级失败 |
| `RELEASE_STATUS_ROLLING_BACK` | 正在回滚 |
| `RELEASE_STATUS_ROLLED_BACK` | 已回滚 |
| `RELEASE_STATUS_ROLLBACK_FAILED` | 回滚失败 |

---

## 3. CustomerManagementService

**服务端:** release-manager  
**协议:** gRPC + REST（双协议）

### REST API

#### 列出客户

```
GET /api/v1/customers?enabled=true
```

**响应:** `200 OK` — Customer 数组

#### 获取客户

```
GET /api/v1/customers/{id}
```

**响应:** `200 OK` — Customer | `404 Not Found`

#### 创建客户

```
POST /api/v1/customers
Content-Type: application/json

{
  "id": "customer-001",
  "name": "某客户",
  "operator_endpoint": "10.0.0.5:8443",
  "cert_fingerprint": "ABCDEF1234567890...",
  "enabled": true,
  "labels": {"region": "east"}
}
```

**响应:** `201 Created` — Customer

#### 更新客户

```
PUT /api/v1/customers/{id}
Content-Type: application/json

{
  "name": "新名称",
  "enabled": false
}
```

**响应:** `200 OK` — Customer

#### 删除客户

```
DELETE /api/v1/customers/{id}
```

**响应:** `200 OK` — `{"deleted": "true"}`

#### 查询发布记录

```
GET /api/v1/releases/{request_id}
```

**响应:** `200 OK` — ReleaseRecord 数组

---

## 4. Harbor Webhook

### Endpoint

```
POST /api/v1/webhook/harbor
```

Harbor 配置 webhook 时选择 **PUSH HELMCHART** 事件类型。

**Payload 格式 (Harbor 原生):**

```json
{
  "type": "PUSH_HELMCHART",
  "occur_at": 1718000000,
  "operator": "admin",
  "event_data": {
    "resources": [{
      "digest": "sha256:...",
      "tag": "0.0.15",
      "resource_url": "oci://harbor.example.com/helm/magic-sandbox"
    }],
    "repository": {
      "name": "helm/magic-sandbox",
      "namespace": "library",
      "repo_full_name": "library/helm/magic-sandbox",
      "repo_type": "CHART"
    }
  }
}
```

### 认证

可选 HMAC 签名验证（通过 `Authorization: Harbor-Signature <base64>` header）。

---

## 5. Health Check

```
GET /health
```

**响应:** `200 OK` — `{"status": "ok"}`

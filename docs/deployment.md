# 部署指南

## release-manager 部署方式

release-manager 支持三种部署模式:

### 方式 1: Binary 运行 (本地开发)

```bash
go build -o bin/release-manager ./cmd/release-manager/
./bin/release-manager --config configs/manager.example.yaml
# HTTP: http://localhost:8080  |  gRPC: localhost:8443
```

### 方式 2: Docker Compose (单机/演示)

```yaml
# docker-compose.yaml
services:
  release-manager:
    image: release-manager:dev
    ports: ["8080:8080", "8443:8443"]
    volumes:
      - ./configs/manager.example.yaml:/etc/release-manager/config.yaml
      - ./certs:/etc/release-manager/tls:ro
      - ./data:/data
    command: ["--config", "/etc/release-manager/config.yaml"]
```

### 方式 3: K8s 部署 (生产)

```bash
kubectl apply -k deployments/release-manager/overlays/prod/
```

---

## Harbor 自签证书部署

当 Harbor 使用自签证书时，K8s 集群和各组件都需要信任该 CA。

### 原理

```
本地开发机:                      K8s 集群内:
  curl -k → 下载 cert              kubectl create secret harbor-ca
  update-ca-trust                  → Pod volume mount
                                   → Helm SDK TLSClientConfig.RootCAs
release-manager (Binary):         release-operator (K8s):
  --config harbor.ca_file          --set harbor.caCert="$(cat harbor-ca.crt)"
```

### Step 1: 获取 Harbor CA 证书

```bash
# 方式 A: 通过 Harbor API
./scripts/fetch-harbor-ca.sh https://harbor.example.com --output certs/harbor-ca.crt

# 方式 B: 手动
curl -k https://harbor.example.com/api/v2.0/systeminfo/getcert > certs/harbor-ca.crt
```

### Step 2: 部署到 release-operator

```bash
# 自动获取 + 注入 K8s Secret
./scripts/fetch-harbor-ca.sh https://harbor.example.com --inject release-operator

# 或 Helm 部署时指定
helm install release-operator ./deployments/release-operator \
  --set customerID=customer-001 \
  --set harbor.url=https://harbor.example.com \
  --set harbor.caCert="$(base64 certs/harbor-ca.crt)" \
  --set harbor.insecureSkipVerify=false \
  ...
```

### Step 3: 部署 release-manager 到 K8s

```bash
# 注入 Harbor CA 到 manager 所在 namespace
./scripts/fetch-harbor-ca.sh https://harbor.example.com --inject release-manager

# ConfigMap 中配置 ca_file:
# harbor:
#   url: https://harbor.example.com
#   ca_file: /etc/release-manager/harbor-ca/ca.crt
```

### Step 4: 验证

```bash
# 检查 Pod 中 CA 是否正确挂载
kubectl exec -it deploy/release-operator -n release-operator -- cat /etc/release-operator/harbor-ca/ca.crt

# 查看 operator 日志确认 CA 已加载
kubectl logs -l app.kubernetes.io/name=release-operator | grep "loaded custom CA"
```

---


### 前置条件

- Kubernetes 1.27+
- Helm v4 客户端
- 已从运维团队获取:
  - CA 证书 `ca.crt`
  - 客户端证书 `tls.crt` 和私钥 `tls.key`
  - Harbor 登录凭证

### 安装

```bash
# 方式 1: 直接指定证书内容（推荐）
helm install release-operator ./deployments/release-operator \
  --set customerID=customer-001 \
  --set notificationEndpoint=release-manager.example.com:8443 \
  --set tls.cert="$(base64 -w0 tls.crt)" \
  --set tls.key="$(base64 -w0 tls.key)" \
  --set tls.ca="$(base64 -w0 ca.crt)" \
  --set harbor.url=https://harbor.example.com \
  --set harbor.username=admin \
  --set harbor.password='<harbor-password>'

# 方式 2: 使用已有 K8s Secret
helm install release-operator ./deployments/release-operator \
  --set customerID=customer-001 \
  --set notificationEndpoint=release-manager.example.com:8443 \
  --set tls.existingCertSecret=release-operator-tls \
  --set tls.existingCaSecret=release-operator-ca \
  --set harbor.existingSecret=harbor-creds
```

### 升级

```bash
helm upgrade release-operator ./deployments/release-operator \
  --set image.tag=v1.1.0
```

### 验证

```bash
# Pod 状态
kubectl get pods -l app.kubernetes.io/name=release-operator

# 日志
kubectl logs -l app.kubernetes.io/name=release-operator -f

# gRPC 端口检查
kubectl port-forward svc/release-operator 8443:grpc
```

### 卸载

```bash
helm uninstall release-operator
```

### 配置参考

| 参数 | 默认值 | 描述 |
|------|--------|------|
| `customerID` | (必填) | 客户唯一标识 |
| `notificationEndpoint` | `release-manager:8443` | 中心通知服务地址 |
| `image.repository` | `harbor.example.com/release-operator/release-operator` | 镜像仓库 |
| `image.tag` | `latest` | 镜像版本 |
| `tls.enabled` | `true` | 启用 mTLS |
| `tls.cert` | `""` | 客户端证书 PEM |
| `tls.key` | `""` | 客户端私钥 PEM |
| `tls.ca` | `""` | CA 证书 PEM |
| `harbor.url` | `https://harbor.example.com` | Harbor 地址 |
| `harbor.username` | `""` | Harbor 用户名 |
| `harbor.password` | `""` | Harbor 密码 |
| `helm.upgradeTimeout` | `10m` | Helm upgrade 超时 |
| `helm.defaultNamespace` | `default` | 默认 namespace |
| `helm.atomic` | `true` | 失败自动回滚 |
| `helm.maxHistory` | `10` | 最大保留版本数 |
| `resources.limits.cpu` | `500m` | CPU 限制 |
| `resources.limits.memory` | `512Mi` | 内存限制 |

---

## release-manager（中心）

### 前置条件

- Kubernetes 集群
- kubectl 访问权限
- 已生成 CA 和服务端证书

### 安装

```bash
# 开发/测试环境
kubectl apply -k deployments/release-manager/overlays/staging/

# 生产环境
kubectl apply -k deployments/release-manager/overlays/prod/
```

### 配置钉钉

编辑 `deployments/release-manager/overlays/prod/patch-config.yaml`:

```yaml
dingtalk:
  webhook_url: "https://oapi.dingtalk.com/robot/send?access_token=YOUR_TOKEN"
  secret: "YOUR_SECRET"
  enabled: true
```

### 注册客户

```bash
curl -X POST http://release-manager:8080/api/v1/customers \
  -H 'Content-Type: application/json' \
  -d '{
    "id": "customer-001",
    "name": "某客户",
    "operator_endpoint": "10.0.0.5:8443",
    "cert_fingerprint": "<fingerprint>",
    "enabled": true
  }'
```

### 配置 Harbor Webhook

Harbor UI → 项目 → Webhooks → 添加:

| 字段 | 值 |
|------|-----|
| 名称 | Release Notification |
| 事件类型 | PUSH HELMCHART |
| 通知类型 | HTTP |
| URL | `http://release-manager:8080/api/v1/webhook/harbor` |
| Auth Header | (可选) |

---

## CI/CD Pipeline

### 构建并推送镜像

```bash
export HARBOR_URL=harbor.example.com
export HARBOR_PROJECT=release-operator
export HARBOR_USERNAME=admin
export HARBOR_PASSWORD=<password>

./scripts/build-and-push.sh release-operator v1.0.0
./scripts/build-and-push.sh release-manager v1.0.0
```

### 打包并推送 Helm Chart

```bash
./scripts/package-chart.sh ./magic-sandbox 0.0.15
```


### 证书轮换

mTLS 证书有有效期（默认 3 年）。release-manager 提供**远程热更新**能力，无需运维到客户现场，无需重启 Pod。

#### TLS 热加载原理

```
release-operator gRPC server 用 tls.Config.GetCertificate 回调:

  每次 TLS 握手:
    GetCertificate() → LoadX509KeyPair(certFile, keyFile)
    GetConfigForClient() → ReadFile(CAFile) + NewCertPool()

  UpdateCertificate RPC:
    写入新 cert/key PEM 到文件 → 下一次握手自动读取

  旧方式: 更新 Secret → kubectl rollout restart Pod
  新方式: gRPC UpdateCertificate → 秒级生效，零停机
```

#### 方式 1: 远程热更新 (推荐，无需重启)

```bash
# 1. 重新生成客户证书
./scripts/generate-certs.sh customer-001 --renew --client-validity 1095

# 2. 通过 gRPC 推送新证书到客户 operator (mTLS 保护)
grpcurl -cert certs/server/tls.crt -key certs/server/tls.key \
  -cacert certs/ca/ca.crt \
  -d "{\"tls_cert_pem\":\"$(cat certs/customer-001/tls.crt)\",
       \"tls_key_pem\":\"$(cat certs/customer-001/tls.key)\",
       \"request_id\":\"$(uuidgen)\"}" \
  <customer-ip>:8443 \
  release.v1.ReleaseNotificationService/UpdateCertificate
# → { "accepted": true, "new_fingerprint": "ABCDEF1234..." }

# 3. 验证新证书已生效 (无需重启)
grpcurl ... <customer-ip>:8443 \
  release.v1.OperatorService/GetCertificateInfo
# → { "fingerprint": "...", "days_until_expiry": 1095, ... }

# 4. 更新白名单指纹 (新证书 = 新指纹，必须同步)
FINGERPRINT=$(cat certs/customer-001/fingerprint.txt)
curl -X PUT http://release-manager:8080/api/v1/customers/customer-001 \
  -H 'X-API-Key: <key>' -H 'Content-Type: application/json' \
  -d "{\"cert_fingerprint\": \"${FINGERPRINT}\"}"
```

#### 方式 2: Helm 升级 (备用，需要重启 Pod)

```bash
helm upgrade release-operator ./deployments/release-operator \
  --reuse-values \
  --set tls.cert="$(base64 certs/customer-001/tls.crt)" \
  --set tls.key="$(base64 certs/customer-001/tls.key)"
```

#### 验证命令

```bash
# 本地检查证书到期
openssl x509 -in certs/customer-001/tls.crt -noout -dates

# 远程查询 operator 证书状态
grpcurl ... <customer-ip>:8443 \
  release.v1.OperatorService/GetCertificateInfo

# Dashboard 批量查询所有客户证书预警
curl http://release-manager:8080/api/v1/dashboard/overview \
  -H 'Authorization: Bearer <token>' | jq '.certificate_warnings'
```

#### 证书到期前准备清单

- [ ] 到期前 **60 天**: Dashboard 黄色预警 (warning)，开始规划
- [ ] 到期前 **30 天**: Dashboard 红色预警 (critical)，生成新证书
- [ ] 远程推送新证书 (UpdateCertificate RPC)
- [ ] 更新白名单指纹
- [ ] 验证: GetCertificateInfo 确认 `days_until_expiry` 已更新
- [ ] 钉钉通知: "customer-001 证书已轮换，新到期日 YYYY-MM-DD"

#### CA 证书轮换

CA 有效期 10 年。CA 轮换属于重大操作:
1. 生成新 CA → 用新 CA 重签所有 Server + Client 证书
2. 更新 manager 端 server 证书
3. 逐客户推送 client 证书 + 更新白名单
4. 建议提前 6 个月规划窗口

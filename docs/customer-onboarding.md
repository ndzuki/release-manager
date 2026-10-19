# 客户集群接入手册（operator enrollment）

本手册面向**客户集群运维**与**我方交付/值班**：交付前检查 → 客户前置 → 发令牌 → 部署 agent → 验证 → 浏览器访问 → 排障 → 升级回滚 → 安全边界。系统边界见 [architecture](architecture.md)，控制面自身的部署/升级见 [production-deploy](production-deploy.md)。

**适用与依赖**

- 控制面：单节点 k3d `rm-prod`，7 个 Deployment（`api`/`auth`/`orchestrator`/`notifier`/`webhook`/`web`/`edge`）加 `postgres`/`redis` 两个 StatefulSet（`deploy/kustomize/production/kustomization.yaml:20-43`）。客户集群对控制面只需**出站**访问宿主 8444（发布目标所需的镜像/chart 仓库另计）。
- **代码依赖**：第 3 节“直接用 IP”依赖 #474（`gateway.ip_addresses` + `gateway.advertise_url`）与 #475（控制台把网关占位符替换成 `operator_endpoint`）。控制面部署这两个 PR 的代码并重启 orchestrator 之前，网关叶子只有 DNS SAN，必须走 §6.1 的兼容路径。
- 实测口径：2026-10-19 在真实 k3d 客户集群走通 enroll → `operator enrolled` → Pod Ready → `inventory sync completed accepted=2 missing=0 status=applied` → 控制台在线。未复测项标为“验证项”。

## 0. 交付前检查清单（我方）

以下 `KUBECONFIG` 指向控制面。

**0.1 工作负载**：`kubectl -n release-manager get deploy` 与 `get pods`，7 个 Deployment 全部 Ready。`orchestrator` 必须是**重启过**的，新网关叶子只在进程启动时签发（`cmd/orchestrator/main.go:442-450`）。

**0.2 网关配置与证书**

```bash
kubectl -n release-manager get cm orchestrator-config \
  -o jsonpath='{.data.orchestrator\.production\.yaml}' | sed -n '/^gateway:/,/^[^ ]/p'
kubectl -n release-manager get secret release-manager-mtls-ca \
  -o jsonpath='{.data.ca\.crt}' | base64 -d > gateway-ca.crt
openssl x509 -in gateway-ca.crt -noout -subject -dates
echo | openssl s_client -connect 10.10.33.122:8444 -CAfile gateway-ca.crt \
  -verify_ip 10.10.33.122 2>&1 | grep 'Verify return code'
echo | openssl s_client -connect 10.10.33.122:8444 2>/dev/null \
  | openssl x509 -noout -dates -ext subjectAltName
```

判据：配置含 `hostnames: [rm-prod.local]`、`ip_addresses: [10.10.33.122]`、`advertise_url: https://10.10.33.122:8444`（`deploy/kustomize/production/configs/orchestrator.production.yaml:35-43`）；CA 可解析（production-deploy 记录的实测 subject 为 `CN=release-manager-agent-ca`；代码在 CA 缺失时自建的是另一种 subject，见 `internal/operator/ca/ca.go:62-72`，以 `openssl` 输出为准）；网关校验 `0 (ok)`，SAN 含 `IP Address:10.10.33.122`，`Not After` 在一年后（**验证项**：部署新代码后实测，不要拿旧叶子外推）。

**0.3 控制台证书**：`echo | openssl s_client -connect 10.10.33.122:8443 2>/dev/null | openssl x509 -noout -subject -dates -ext subjectAltName`。判据：`CN=localhost, O=release-manager-prod`；SAN 含 `localhost`/`rm-prod.local`/`127.0.0.1`/`10.10.33.122`；到期 2029-10-18（当前实测值）。客户浏览器要信任的是这张叶子证书，见 §5。

**0.4 宿主暴露**：`docker container inspect rm-edge-forwarder --format '{{.State.Status}} restart={{.HostConfig.RestartPolicy.Name}} net={{.HostConfig.NetworkMode}}'` 应为 `running restart=always net=host`；`ss -ltn | grep -E '8443|8444'` 可见 10.10.33.122 与 172.23.0.1；`curl --noproxy '*' -ksS -o /dev/null -w '%{http_code}\n' https://10.10.33.122:8443/` 返回 200。该容器是纯 L4 转发，不终止 TLS、不做访问控制，且不在本仓库内（重建口径见 §8 的 IP 漂移条目）。

**0.5 私钥已异地备份**：三个 Secret 必须存在，托管文件权限 600：

```bash
kubectl -n release-manager get secret \
  release-manager-jwt-private release-manager-mtls-ca release-manager-edge-tls
ls -l ~/rm-escrow/jwt-private.pem ~/rm-escrow/mtls-ca.key ~/rm-escrow/edge-tls.key
```

自动备份 CronJob 只 dump 两个数据库（`deploy/kustomize/production/backup-cronjob.yaml:1-11`），**不含任何 Secret**；导出与丢失后果按 [production-deploy §3.2](production-deploy.md) 执行。丢失 mTLS CA 私钥 = 所有客户 agent 重新 enroll。

**0.6 地址稳定**：`ip -4 -o addr show wlan0` 应仍为 10.10.33.122。wlan0 是 DHCP，漂移处置见 §8。当前 8443/8444 对 10.10.32.0/20 开放且宿主无防火墙/ACL，交付时应把来源限定到客户网段。

## 1. 客户侧前置

**1.1 集群与存储**：`kubectl cluster-info` 正常；有可用 StorageClass（agent 需要 1Gi `ReadWriteOnce`，`deploy/kustomize/customer-agent/base/pvc.yaml:1-13`）。先跑 `kubectl get storageclass` 与 `kubectl -n release-manager-customer get pvc`；没有默认 StorageClass 时 PVC 会一直 `Pending`。本项目只在 k3d/k3s 上端到端验证，其他发行版不承诺版本下限。

**1.2 取得 operator 镜像**：镜像是 `release-operator:<TAG>`，distroless、`nonroot`、无 shell（`deploy/docker/Dockerfile.operator:10-16`）。离线 k3d：

```bash
make docker-build-operator OPERATOR_IMAGE=release-operator:<TAG>
k3d image import -c <customer-cluster> .tmp-release-operator.tar
docker exec k3d-<customer-cluster>-server-0 crictl images | grep release-operator
```

有私有 registry 时 `docker tag`/`docker push` 到 registry，并把 Deployment 的 `image` 换成 registry 地址；需要认证时再给 Deployment 加 `imagePullSecrets`（上面的模板未预置）。

**1.3 出站连通性与信任根**：在客户集群内发起（不要从宿主代替）：

```bash
kubectl -n release-manager-customer run nettest --rm -i --restart=Never \
  --image=busybox:1.36.1 --command -- nc -zv -w3 10.10.33.122 8444
openssl s_client -connect 10.10.33.122:8444 -CAfile gateway-ca.crt \
  -verify_ip 10.10.33.122 </dev/null
```

判据：`open` 与 `Verify return code: 0 (ok)`。**用 IP 不需要 DNS/hostAliases**，这是新代码的推荐路径。

## 2. 在控制台生成 token

1. 先按 §5 信任控制台证书，打开 `https://10.10.33.122:8443` 登录。
2. 客户 → 集群 → Operator → `Generate token`（`web/src/pages/OperatorListPage.vue:81-85`）。
3. 填 `Operator name`（小写 DNS label，1–63，字母数字与 `-`，首尾非 `-`，`internal/orchestrator/operator.go:111-123`）与 `TTL`（缺省 60 分钟，范围 5–1440，`internal/orchestrator/operator.go:27-29`、`internal/orchestrator/enrollment.go:36-41`）。
4. 弹窗只显示**一次**明文 token，并给出模板版本 `v2`、`operator_endpoint` 与“部署命令”；把 token 和部署命令都复制出来，关闭后明文从浏览器永久移除（`web/src/components/operators/EnrollmentTokenModal.vue:120-133`）。
5. ⚠️ **网关占位符**：服务端模板故意保留 `<CONTROL_PLANE_OPERATOR_GATEWAY>`（`internal/orchestrator/operator.go:56-59`）。#475 合入后控制台会用 `operator_endpoint` 替换它；**本分支（基于 #474、未含 #475）上弹窗只替换 `${ENROLLMENT_TOKEN}`**（`web/src/components/operators/EnrollmentTokenModal.vue:34-37`），所以必须手工把占位符替换成弹窗显示的 `operator_endpoint`（配置了 `advertise_url` 时即 `https://10.10.33.122:8444`，`internal/orchestrator/enrollment.go:92-101`、`cmd/orchestrator/main.go:135-143`）。无论 #475 是否已合入，命令里的地址都必须是客户集群可达的 TLS 地址；正常交付下它等于 `operator_endpoint`，不要写控制面监听地址。
6. 服务端只存 SHA-256，明文不落库；token 一次性，过期/使用后不可复用（`internal/orchestrator/enrollment.go:67`、`:104-109`）。
7. 每个 Cluster 至多一个 pending token；注册页可“替换”或“丢弃”，弹窗的“丢弃令牌”会调用 `RevokePendingEnrollmentToken` 立即作废（`web/src/components/operators/EnrollmentTokenModal.vue:88-100`、`:158-178`）。

## 3. 部署 agent

**3.1 Namespace、RBAC、PVC**：

```bash
kubectl apply -f deploy/kustomize/customer-agent/base/namespace.yaml
kubectl apply -f deploy/kustomize/customer-agent/base/serviceaccount.yaml
kubectl apply -f deploy/kustomize/customer-agent/base/rbac.yaml
kubectl apply -f deploy/kustomize/customer-agent/base/pvc.yaml
```

这些文件固定 `release-manager-customer`；换 namespace 时必须同时改四份文件、Deployment 与 `ClusterRoleBinding` 的 subject。RBAC 是**集群级**的（`deploy/kustomize/customer-agent/base/rbac.yaml:10-24`），风险见 §8。

**3.2 两个 Secret**：

```bash
kubectl -n release-manager-customer create secret generic operator-enrollment \
  --from-literal=token='<一次性 token>'
kubectl -n release-manager-customer create secret generic operator-gateway-ca \
  --from-file=gateway-ca.crt=./gateway-ca.crt
```

`gateway-ca.crt` 是**信任根**（约 530 B 的 CA 证书），不是网关叶子；agent 用 `ca.cert_path` 校验网关（`internal/operator/bootstrap/bootstrap.go:226-242`），不要配私钥。

**3.3 完整 YAML**：存为 `operator.yaml`，替换 `<CUSTOMER_ID>`/`<CLUSTER_ID>`/`<OPERATOR_NAME>`/`<TAG>` 后 `kubectl apply -f operator.yaml`。

```yaml
apiVersion: v1
kind: ConfigMap
metadata: {name: operator-config, namespace: release-manager-customer}
data:
  operator.yaml: |
    http_port: 8084
    log_level: info
    agent:
      mode: agent
      customer_id: <CUSTOMER_ID>
      cluster_id: <CLUSTER_ID>
      operator_name: <OPERATOR_NAME>
    ca:
      cert_path: /data/gateway-ca.crt
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: operator
  namespace: release-manager-customer
  labels: {app.kubernetes.io/name: operator, app.kubernetes.io/part-of: release-manager}
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector:
    matchLabels: {app.kubernetes.io/name: operator}
  template:
    metadata:
      labels: {app.kubernetes.io/name: operator, app.kubernetes.io/part-of: release-manager}
    spec:
      serviceAccountName: release-manager-operator
      securityContext: {fsGroup: 65532}
      containers:
        - name: operator
          image: release-operator:<TAG>
          imagePullPolicy: IfNotPresent
          args: [--config, /configs/operator.yaml, --command-db, /data/operator-commands.db, --orchestrator-addr, https://10.10.33.122:8444]
          env:
            - name: ENROLLMENT_TOKEN
              valueFrom:
                secretKeyRef: {name: operator-enrollment, key: token}
          ports: [{name: http, containerPort: 8084}]
          startupProbe:
            httpGet: {path: /health, port: http}
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 60
          readinessProbe:
            httpGet: {path: /readyz, port: http}
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 12
          livenessProbe:
            httpGet: {path: /health, port: http}
            initialDelaySeconds: 10
            periodSeconds: 10
            timeoutSeconds: 3
            failureThreshold: 3
          resources:
            requests: {cpu: 10m, memory: 32Mi}
            limits: {cpu: 250m, memory: 256Mi}
          volumeMounts:
            - {name: config, mountPath: /configs/operator.yaml, subPath: operator.yaml}
            - {name: identity, mountPath: /data}
            - {name: gateway-ca, mountPath: /data/gateway-ca.crt, subPath: gateway-ca.crt, readOnly: true}
      volumes:
        - name: config
          configMap: {name: operator-config}
        - name: identity
          persistentVolumeClaim: {claimName: operator-identity}
        - name: gateway-ca
          secret:
            secretName: operator-gateway-ca
            items: [{key: gateway-ca.crt, path: gateway-ca.crt}]
```

关键约束：

- `customer_id`/`cluster_id` 必须与 token 的 scope 完全一致；CSR 的 canonical SAN 由它们派生（`internal/operator/bootstrap/csr.go:37`），不一致会以 `csr_san_mismatch` 拒绝（`internal/operator/service.go:279-288`）。
- `operator_name` 必须与 token 绑定的名字一致，否则 `csr_invalid`（`internal/operator/service.go:289-294`）。
- `strategy: Recreate` **不能改**：bbolt 单写者，`RollingUpdate` 会卡在 store 锁上、旧 Pod 永不替换（`deploy/kustomize/customer-agent/base/deployment.yaml:11-20`）。
- `--command-db` 必须落在 PVC 的 `/data` 下；身份与待执行命令都在这个文件里（`cmd/operator/main.go:150-159`）。

**3.4 就绪后清理一次性 token**：`kubectl -n release-manager-customer rollout status deployment/operator --timeout=180s`，确认日志出现 `operator enrolled` 后，从 `operator.yaml` 删除 `ENROLLMENT_TOKEN` 的 `env` 块并重新 apply，再 `delete secret operator-enrollment`。顺序不能反：Secret 先删而 Deployment 仍引用，Pod 重启会停在 `CreateContainerConfigError`。token 用后即失效，删除只是清理明文。

## 4. 验证

**4.1 agent 日志**（镜像无 shell，只能看日志或 `kubectl port-forward`）：

```bash
kubectl -n release-manager-customer logs deployment/operator --tail=100 \
  | grep -E 'operator enrolled|inventory sync completed|identity found|reconnecting'
```

期望（`accepted` 随 Helm release 数变化）：

```json
{"msg":"operator enrolled","operator_id":"...","customer_id":"...","cluster_id":"..."}
{"msg":"inventory sync completed","accepted":2,"missing":0,"status":"applied"}
```

首次全量同步在启动约 5 秒后、之后每 5 分钟一次（`internal/operator/inventory.go:18`、`:108-120`、`:192-197`）。Pod 重启后出现 `operator identity found; skipping enrollment`，说明 token 只消费一次、身份在 PVC 里（`internal/operator/bootstrap/bootstrap.go:76-79`）。服务端对应日志是 `operator enrolled`（`internal/operator/service.go:380-387`）与 `operator stream established via mTLS`（`internal/operator/service.go:502-508`）。

**4.2 控制台在线**：Operator 列表 `SESSION=Online`、`LAST HEARTBEAT` 持续刷新；详情页生命周期 `ACTIVE` 且显示证书到期时间（这就是 agent 客户端证书的到期时间）。agent 的 `/readyz` 只在网关会话存活时通过（`cmd/operator/main.go:450-467`），readiness 的 `failureThreshold: 12` 意味着断线后最多还能容忍约 60 秒才转 NotReady；**Pod Ready 表示探测时网关会话存活**。offline 阈值来自 `operator_session`（默认 15s 心跳、45s suspect、90s offline，`deploy/kustomize/production/configs/orchestrator.production.yaml:66-69`）；短暂 `suspect` 不用处理。

**4.3 库存同步**：客户集群必须**真有 Helm release**，清单才会出现行；没有 release 时同步成功但 `accepted=0`，这是空清单而非故障。`helm list -A` 核对；长期为 0 时再确认 RBAC 能读 `secrets`（Helm v3 把 release 存在 Secret 里，`deploy/kustomize/customer-agent/base/rbac.yaml:6-12`）。

**4.4 服务端只读复核**（我方）：

```bash
kubectl -n release-manager exec deployment/postgres -- psql -U release_manager -d release_manager -Atc \
  "select status,status_reason,last_heartbeat from sessions order by started_at desc limit 3"
```

期望一行 `online` 且 `last_heartbeat` 新鲜。

**4.5 常见失败对照**

| 现象 / 日志 | 原因 | 处置 |
| --- | --- | --- |
| `invalid enrollment token` | token 不存在/已撤销/状态异常（`internal/operator/service.go:223-228`、`:236-239`） | 重新生成并更新 Secret |
| `enrollment token already used` / `expired` | 已消费或超 TTL（`:230-243`） | 重新生成；必要时调大 TTL |
| `certificate signing request SAN does not match enrollment scope` | ID 与 token 不一致（`:279-288`） | 按控制台 ID 修正 ConfigMap |
| `operator name is already active` / `active on another cluster` | 名字冲突（`:296-308`） | 换名字或先撤销旧 operator |
| `x509: certificate signed by unknown authority` | 挂错 CA（`internal/operator/bootstrap/bootstrap.go:226-242`） | 用 Secret `release-manager-mtls-ca` 的 `ca.crt` 重建 |
| `certificate is valid for ... not 10.10.33.122` | 网关叶子无 IP SAN（旧代码） | 部署新代码并重启 orchestrator；临时走 §6.1 |
| `no live gateway command session`（`/readyz`） | 未 enroll 或正在重连（`cmd/operator/main.go:460-464`） | 按上面 token/证书分支处理 |
| Pod `Pending` / `ImagePullBackOff` | 无 StorageClass / 镜像没导入 | 补 StorageClass；按 §1.2 导入并复核 |
| `connection refused` / 超时 | 8444 未暴露、forwarder 挂了或 ACL 拦截 | 按 §0.4、§1.3 逐层测 |

完整错误码见 `internal/operator/errors.go:9-27`。

## 5. 浏览器访问控制台

**5.1 地址与证书**：访问 `https://10.10.33.122:8443`。控制台与 auth 必须同源、HTTPS：会话 cookie 固定 `Secure` + `SameSite=Strict`，HTTP 或换端口都会丢会话（`internal/auth/browser_session.go:284-286`、`deploy/kustomize/production/edge.yaml:13-15`）。证书由我方导出，私钥不出控制面：

```bash
kubectl -n release-manager get secret release-manager-edge-tls \
  -o jsonpath='{.data.tls\.crt}' | base64 -d > rm-console.crt
openssl x509 -in rm-console.crt -noout -subject -dates -ext subjectAltName
```

**5.2 各平台信任**：

- **Windows（Chrome/Edge 用系统库）**，管理员执行 `certutil -addstore -f Root rm-console.crt`；或双击证书 → 安装 → 本地计算机 → “受信任的根证书颁发机构”。装完重启浏览器。
- **macOS**：`sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain rm-console.crt`；或在“钥匙串访问”里导入系统钥匙串并把“信任”设为“始终信任”。
- **Linux（Chrome/Chromium 用 NSS）**：`certutil -d sql:$HOME/.pki/nssdb -A -t "P,," -n "rm-console" -i rm-console.crt`。`certutil` 在 Debian/Ubuntu 来自 `libnss3-tools`，Fedora 来自 `nss-tools`；NSS 库不存在时先 `mkdir -p "$HOME/.pki/nssdb"` 再 `certutil -d sql:$HOME/.pki/nssdb -N --empty-password`。Firefox 用自己的库，需在“设置 → 隐私与安全 → 证书 → 服务器”单独导入。

**5.3 证书错误对照**

| 浏览器报错 | 含义 | 处置 |
| --- | --- | --- |
| `ERR_CERT_AUTHORITY_INVALID` | 证书不在信任库 | 按 §5.2 导入**叶子证书** |
| `ERR_CERT_INVALID` | 把 `CA:FALSE` 的叶子按 CA（`C,,`）导入 | 删除后以 `P,,`（受信任的对端/服务器证书）重导 |
| `ERR_CERT_COMMON_NAME_INVALID` | 访问的 IP/名字不在 SAN | 用 SAN 内的地址，或重签补 SAN |

自签证书无自动轮换。交付前必须按 §0.3 实测到期日（当前证书为 2029-10-18）；到期前重签并重启 `edge`。

## 6. 排障

**6.1 节点 `/etc/hosts` 不传给 Pod**：Pod 用自己的 `/etc/hosts`。新代码下推荐直接用 IP，不需要 hosts/hostAliases。只有控制面尚未部署 IP SAN 代码时，才给 Deployment 加 `hostAliases`，把原 SAN 名指到宿主（形态见 `deploy/kustomize/customer-agent/base/deployment.yaml:33-43`），并把 `--orchestrator-addr` 改回 `https://operator-gateway.dev.release-manager.local:8444`。

**6.2 `k3d image import` 假成功**：失败时也可能打印 `Successfully imported` 并以 0 退出；判据是输出里有没有 `ERRO`。逐个导入、逐个复核：

```bash
k3d image import -c <customer-cluster> .tmp-release-operator.tar 2>&1 | tee /tmp/import.log
grep -q ERRO /tmp/import.log && { echo 'import failed' >&2; exit 1; }
docker exec k3d-<customer-cluster>-server-0 crictl images | grep release-operator
```

多平台/带 attestation 的镜像先 `docker save --platform linux/amd64`。仓库的 `make docker-import-all` 对 8 个镜像做了同样的守卫（`Makefile:768-782`），客户集群只需 operator。

**6.3 离线集群缺 k3s 系统镜像**：新建离线 k3d 集群若没有 `rancher/mirrored-pause`、`rancher/mirrored-coredns-coredns`、`rancher/local-path-provisioner`、`rancher/mirrored-library-busybox`（以及启用的 `rancher/mirrored-library-traefik`、`rancher/klipper-lb`），Pod 会卡在 `ContainerCreating`/`ImagePullBackOff`。tag 随 k3s 版本变化，以 `kubectl describe pod` 的 Events 里实际镜像名为准。

**6.4 kubeconfig 写成 `0.0.0.0`**：kubectl 会直接 `EOF`。修正（`docs/production-deploy.md:403-421`）：

```bash
PORT="$(docker port k3d-<customer-cluster>-serverlb 6443/tcp | head -1 | sed 's/.*://')"
kubectl config set-cluster k3d-<customer-cluster> --server="https://127.0.0.1:${PORT}"
kubectl get nodes
```

**6.5 宿主 `HTTPS_PROXY` 导致 Broken pipe**：`NO_PROXY` 不含 10.10.33.122 时，宿主 `curl` 会去连代理并报 Broken pipe——**不是证书问题**。宿主自测一律加 `--noproxy '*'`。agent 自身不用环境代理（`internal/operator/bootstrap/bootstrap.go:234-241` 的 Transport 未设 `Proxy`），此坑只影响人工验证。

**6.6 控制台信任方式错**：见 §5.3；最常见的误判是把 `ERR_CERT_AUTHORITY_INVALID` 当过期，或把叶子按 CA 导入后得到 `ERR_CERT_INVALID`。

**6.7 按症状分类**

| 类别 | 典型信号 | 先查 | 判据 |
| --- | --- | --- | --- |
| 证书 | 日志含 `x509`/`tls`/`certificate`/`handshake` | §0.2 的 SAN/CA/到期；§5 的浏览器信任 | `openssl` 校验为 0；SAN 含实际访问地址 |
| 会话 | 控制台 Offline/Suspect；`operator agent disconnected; reconnecting` | agent 日志、Pod Ready、Operator 详情 | 心跳新鲜、无 `operator_revoked` |
| 库存 | `accepted=0`、清单为空 | `helm list -A`、RBAC 读 Secret、同步日志 | 有真实 release 且 `accepted > 0` |

agent 重连是 1s 起、倍增、上限 30s（`cmd/operator/main.go:101-120`），短时 `reconnecting` 属正常恢复。

## 7. 升级与回滚

**7.1 agent 镜像**：

```bash
make docker-build-operator OPERATOR_IMAGE=release-operator:<NEW_TAG>
k3d image import -c <customer-cluster> .tmp-release-operator.tar
kubectl -n release-manager-customer set image deployment/operator operator=release-operator:<NEW_TAG>
kubectl -n release-manager-customer rollout status deployment/operator --timeout=180s
```

`Recreate` 会带来一次短暂离线；身份在 PVC 里，重启后应看到 `operator identity found; skipping enrollment`，不需要重新生成 token（`deploy/kustomize/customer-agent/base/deployment.yaml:11-20`、`internal/operator/bootstrap/bootstrap.go:76-79`）。

**7.2 回滚**：`kubectl -n release-manager-customer rollout undo deployment/operator` 后 `rollout status`。只换镜像，不动 PVC/Secret；若新版本改过配置结构，回滚后一并恢复 ConfigMap。

**7.3 网关叶子重签**：叶子只在 orchestrator 启动时签发，没有运行期自动轮换（`cmd/orchestrator/main.go:442-450`；`deploy/kustomize/production/configs/orchestrator.production.yaml:50-58`）。执行 `kubectl -n release-manager rollout restart deployment/orchestrator` 并等 rollout 完成后重跑 §0.2。⚠️ 重启会清空进程内紧急变更流注册表，窗口内紧急变更必然失败（[runbook §3](runbook.md)）；agent 会自动重连。至少每年安排一次重启。

**7.4 撤销与重新接入**：控制台 Operator 详情 → `Revoke`（原因 5–500 字符），服务端同时撤销会话并关闭活动流（`internal/orchestrator/operator.go:160-199`）；待用 token 走注册页“丢弃令牌”（`internal/orchestrator/operator.go:235-259`）。撤销后旧身份仍在客户 PVC，agent 不会自动重新 enroll；需缩容、删除身份卷（**破坏性**）、按 §2/§3 重建后再扩容：

```bash
kubectl -n release-manager-customer scale deployment/operator --replicas=0
kubectl -n release-manager-customer delete pvc operator-identity
```

CA 轮换不属日常操作：换 CA 会让所有客户 agent 旧证书失效，必须全部重新 enroll（见 [production-deploy §3.2](production-deploy.md)）。另外，**agent 客户端证书与网关叶子共用 `ca.cert_ttl`**（生产 8760h，`internal/operator/ca/ca.go:219-225`）；当前 agent 没有自动续期调用点，证书到期后需要按本节重新 enroll，不能只重启 Pod。

## 8. 安全边界与残余风险

| 项 | 现状 | 影响 / 建议 |
| --- | --- | --- |
| 8443/8444 暴露面 | 对 10.10.32.0/20 开放；宿主无防火墙/ACL；forwarder 为纯 L4 | 网络侧限定客户网段 |
| 网关 TLS 策略 | `VerifyClientCertIfGiven`：无客户端证书也完成 TLS 握手，鉴权在 RPC 层（`cmd/orchestrator/main.go:217-223`） | 网络可达 ≠ 已认证 |
| 匿名 RPC | `GetActiveOperatorSession` 无需凭证（`docs/api.md:142`；`internal/operator/active_session.go:16-31`） | 泄露会话元数据；新 RPC 不要默认无鉴权 |
| 控制台证书 | 自签、无自动轮换，当前到 2029-10-18 | 到期前重签并重启 `edge` |
| 网关叶子 | 1 年 TTL，只在启动时重签 | 至少每年重启 orchestrator |
| agent 客户端证书 | 与叶子共用 `ca.cert_ttl`（生产 8760h）；当前没有自动续期调用点（`internal/operator/ca/ca.go:219-225`） | 到期前重新 enroll（§7.4），不能只重启 Pod |
| IP 漂移 | wlan0 为 DHCP；forwarder listener 绑旧 IP 会失效，`advertise_url`/`ip_addresses` 也要同步 | 重建 forwarder、改配置、重启 orchestrator 重签；客户看到的 endpoint 也会变 |
| 私钥备份 | 三份私钥不在自动备份内（`deploy/kustomize/production/backup-cronjob.yaml:1-11`） | 按 §0.5 异地备份；丢失 mTLS CA = 全量重 enroll |
| agent 权限 | ClusterRole 可读写 Secret/ConfigMap/Service/工作负载/Namespace（`deploy/kustomize/customer-agent/base/rbac.yaml:10-24`） | agent 被攻破等于集群级写权限 |
| 一次性 token | 明文只返回一次，服务端仅存哈希（`internal/orchestrator/enrollment.go:67`、`:104-109`） | 不贴工单/聊天/日志；enroll 后按 §3.4 删除 |
| 共享网关 | 所有客户共用 8444、同一个 CA 与网关进程，隔离靠证书身份 | 网络层没有按客户分段 |

**建议但当前未实现**：按客户网段的前置反代/防火墙、网关证书自动轮换、agent 最小权限 RBAC、每客户独立网关端点。不要当成现成能力。

## 事实源

`deploy/kustomize/production/configs/orchestrator.production.yaml`、`deploy/kustomize/production/backup-cronjob.yaml`、`deploy/kustomize/customer-agent/base/`、`deploy/docker/Dockerfile.operator`、`internal/operator/bootstrap/`、`internal/operator/ca/ca.go`、`internal/operator/inventory.go`、`internal/operator/service.go`、`internal/orchestrator/enrollment.go`、`internal/orchestrator/operator.go`、`cmd/operator/main.go`、`cmd/orchestrator/main.go`、`web/src/pages/OperatorListPage.vue`、`web/src/components/operators/EnrollmentTokenModal.vue`、`docs/production-deploy.md`、`docs/runbook.md`、`docs/api.md`。

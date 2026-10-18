# 生产部署（单节点 k3d `rm-prod`）

本文是 `deploy/kustomize/production` overlay 在**单节点 k3d 集群**上的部署与运维手册：前置条件、必填 Secret、启动顺序、TLS、备份恢复、升级回滚、可观测面与已知限制。manifest 的结构与逐条设计理由不在此重复，见 [production overlay README](../deploy/kustomize/production/README.md)；故障处置的通用路径见 [runbook](runbook.md)。

**口径**：文中数字来自 2026-10-17 在本机的一次真实部署与演练（集群仍在运行）。2026-10-18 的 TASK-362 全量发布把镜像 tag 前移到 `778ded6`、schema 到 35（§8.5），§0/§1 里受影响的实测值已随之更新。凡是「实测」都注明测量方式与时刻；「建议」表示当前**不存在**、需要改动才能获得的能力，不作为排障依据。命令里的 `<...>` 是要替换的占位符。

## 0. 速览

| 项 | 实测值 | 口径 |
| --- | --- | --- |
| 集群 | 单节点 k3d `rm-prod`，k3s `v1.31.5+k3s1` | `k3d cluster list`、`kubectl get nodes` |
| namespace | `release-manager` | `kubectl -n release-manager get pods` |
| Pod | 10 个，**10/10 Ready** | 2026-10-17 19:0x 快照 |
| PVC | **5 个全部 `Bound`**（含自动备份卷 `release-manager-backups`） | 同上 + `kubectl -n release-manager get pvc` |
| 渲染对象 | **45 个**（1 Namespace / 8 ConfigMap / 7 Deployment / 2 StatefulSet / 4 PDB / 10 Service / 3 PVC / 9 NetworkPolicy / 1 CronJob） | `kubectl kustomize`，纯本地渲染，不接触集群 |
| 自动备份 | `CronJob release-manager-backup`，每日 `02:00 UTC`，dump 落 `release-manager-backups`（5Gi） | 2026-10-17 实测，见 §7.4 |
| 镜像 tag | `778ded6`（6 个服务与 `web`；`edge` 仍 `nginx:1.27-alpine`） | 2026-10-18 TASK-362，见 §8.5 |
| 入口 | `https://localhost:8443`（同源，TLS） | 见 §6 |
| operator 网关 | `https://127.0.0.1:8444`（mTLS；2026-10-18 12:28 起仅 loopback，跨机接入见 §10-5） | 2026-10-18 实测 `bash /dev/tcp`、`openssl s_client`、`curl`，见 §4-⑦ |
| 宿主端口绑定 | 8443/8444 → `127.0.0.1`（无 `0.0.0.0`/`[::]`）；6443 API 仍是 `0.0.0.0:44609` | 2026-10-18 12:28 `docker port`，见 §10-5 |
| schema | `schema_migrations` = `35`, `dirty=f`（2026-10-18 前为 `34`） | 只读 SQL，见 §8.5 |

## 1. 现状与拓扑

### 1.1 工作负载

| 工作负载 | 类型 | 副本 | 容器端口 | 镜像（实测） | 说明 |
| --- | --- | --- | --- | --- | --- |
| `api` | Deployment | 1 | 8088 | `localhost:5001/release-api:778ded6`（2026-10-18 前为 `876df9a`，更早为 `3ecf69b`，见 §8.5 与 §10-1） | 审计读面；overlay 已注入 `DATABASE_DRIVER=postgres` + `DATABASE_DSN`（Secret），`--db`/PVC 保留给归档；默认组织视图为空仅指 2026-10-17 修好前/无事件时，见 §10-1（含 2026-10-18 时点更正） |
| `auth` | Deployment | 1 | 8085 | `release-auth` | 唯一持有 JWT **私钥**、Cookie 会话、Redis |
| `orchestrator` | Deployment | 1 | 8083 / 8084 | `release-orchestrator` | 管理面 8083；operator mTLS 网关 8084（仅两条路由，无 `/health`），经 `operator` Service 的 NodePort 30844 暴露给客户集群 |
| `notifier` | Deployment | 1 | 8086 | `release-notifier` | 独立库 `release_notifier`；生产 egress allowlist 为空 |
| `webhook` | Deployment | 1 | 8082 | `release-webhook` | CI/Harbor 回调入口，服务令牌鉴权 |
| `web` | Deployment | **2** | 8087 | `release-web` | nginx：SPA + 按 Connect 前缀反代；同源路由的唯一实现 |
| `edge` | Deployment | 1 | 443 | `nginx:1.27-alpine` | TLS 终止 → `web:8087`；两个 NodePort 之一（30443） |
| `postgres` | StatefulSet | 1 | 5432 | `postgres:16`（服务端 **16.15**） | 数据权威（ADR-014） |
| `redis` | StatefulSet | 1 | 6379 | `redis:7-alpine`（**7.4.11**） | 仅 auth 会话缓存 + refresh 黑名单；无 AUTH |
| `release-manager-backup` | CronJob | — | — | `postgres:16` | 每日 `02:00 UTC` 逻辑备份（`release_manager` + `release_notifier` → PVC `release-manager-backups`），见 §7.4 |

七个 Deployment 里只有 `edge` 不是 Go 进程；`api`/`auth`/`orchestrator`/`notifier`/`webhook` 各自暴露 `/health` 与 `/readyz`，`web` 的 `/health`、`/readyz` 由 nginx **反代到 orchestrator**（`web/nginx.conf`），`edge` 只有 `tcpSocket` 探针。

### 1.2 入口链路

```
浏览器 ── https://localhost:8443
      → k3d serverlb：宿主 8443/tcp → 容器 30443/tcp
      → Service edge（NodePort 30443，两个 NodePort 之一）
      → edge pod：nginx 终止 TLS（自签证书），proxy_pass http://web:8087
      → Service web:8087：nginx 托管 SPA，并按 /auth.v1. /orchestrator.v1. /webhook.v1.
        /operator.v1. /notifier.v1. /audit.v1. /trust.v1. 等前缀反代到各 ClusterIP 服务
      → auth:8085 / orchestrator:8083 / webhook:8082 / notifier:8086 / api:8088
```

客户集群的 operator 走另一条**独立的 TCP 通路**，不经过 edge：

```
客户集群 operator ── https://<宿主地址>:8444
      → k3d serverlb：宿主 8444/tcp → 容器 30844/tcp
      → Service operator（NodePort 30844，ClusterIP 10.43.196.238）
      → orchestrator pod:8084：进程内终止 mTLS，按 client cert 校验请求身份
```

> 2026-10-18 12:28 起宿主 8443/8444 只绑 `127.0.0.1`：上面两条「宿主」链路只在本机成立；客户集群接入需先把 8444 暴露到受信网段（反代/防火墙，见 §10-5）。

要点：

- 只有两个 Service 暴露 NodePort：`edge` 的 30443 与 `operator` 的 30844（客户集群 operator 的 mTLS 通路）；其余都是 `ClusterIP`（`deploy/kustomize/production/edge.yaml:167` 与 `kustomization.yaml` 的 operator patch）。
- 浏览器只面对**一个 origin**（`https://localhost:8443`）。这不是审美选择：`secure_cookies` 默认 `true` 且 `SameSite=Strict`，跨 origin 或纯 HTTP 会**静默丢 cookie**，登录看起来"成功"但刷新即掉线（§6）。
- 集群内没有可用的 Ingress：k3s 自带 Traefik 的 `svclb-traefik` 依赖 klipper-lb 镜像，该镜像不在离线镜像集里，Traefik Pod 停在 `ImagePullBackOff`。因此用自带 nginx edge 顶替（理由与取舍见 overlay README）。

### 1.3 存储

| PVC | 容量 | StorageClass | 来源 | 用途 |
| --- | --- | --- | --- | --- |
| `data-postgres-0` | 10Gi | `local-path` | StatefulSet `volumeClaimTemplates` | PostgreSQL 数据目录 |
| `data-redis-0` | 5Gi | `local-path` | 同上 | Redis AOF/RDB |
| `release-manager-api-data` | 1Gi | `local-path` | overlay `api-pvc.yaml` | `release-api` 的审计归档 `/data/archives`；`--db` 的 SQLite 文件仅在未选 PG 时使用 |
| `release-manager-orchestrator-data` | 1Gi | `local-path` | 继承自 `deploy/kustomize/base` | agent 网关 CA、审计 spool |
| `release-manager-backups` | 5Gi | `local-path` | overlay `backup-cronjob.yaml` | 每日 PostgreSQL 逻辑备份（`release_manager` / `release_notifier`），见 §7.4 |

`local-path` 是 k3d/k3s 默认 provisioner，卷**绑定节点**：这套 overlay 只适合单节点集群，多节点需要网络卷类（改 `deploy/kustomize/production/production-settings.yaml:14` 的 `storageClassName` 一处即可，见 overlay README 的「Storage class」）。

## 2. 前置

### 2.1 工具

| 工具 | 本机实测版本 | 要求 | 备注 |
| --- | --- | --- | --- |
| Docker | 29.8.2 | CLI 在 PATH、daemon 可达 | 构建镜像与 `k3d image import` 都走它 |
| k3d | 5.8.3 | **≥ 5.8** | 必须用 release 二进制，`go install` 版本会报 `v5-dev` |
| kubectl | 1.37.1（server 1.31.5） | 与 server 相差 ≤ ±1 minor | 本机客户端偏高，`kubectl version` 会打印 skew 警告；功能可用，但建议用 1.30–1.32 的客户端 |
| kustomize | 5.8.1 | 与 `kubectl kustomize` 同源即可 | `kustomize edit set image` 需要独立二进制 |
| openssl | 3.6.5 | ≥ 1.1.1（需要 `-addext`） | 生成 JWT/CA/edge 证书 |
| pg_dump / psql | 18.6 | 能连 PG 16 即可 dump | **restore 有版本坑**，见 §7 |
| helm | 4.3.0 | **不参与本部署** | overlay 是纯 kustomize；Helm 只在 operator 进程内以 Go SDK 形式操作客户集群 |

### 2.2 镜像必须先在本地存在

该环境**没有 docker.io 出口**：集群不会从公网拉镜像，节点里也不跑 registry。overlay 引用的 `localhost:5001/release-*:<tag>` 靠 `k3d image import` 直接写进节点 containerd（`localhost:5001` 只是历史命名，不表示真的有一个 registry）。

```bash
make docker-build-all                  # 8 个镜像，tag = git rev-parse --short HEAD
make docker-import-all CLUSTER=rm-prod # 内置 ERRO 守卫
```

- `make docker-build-all` 的 `IMAGE_TAG` 默认就是 `git rev-parse --short HEAD`（`Makefile:733-734`）；要固定 tag 就显式传 `IMAGE_TAG=<tag>`，后续 `kustomize edit set image` 必须用**同一个**值。
- ⚠️ **`k3d image import` 即使有节点导入失败，也会打印 `Successfully imported`**。真正的判据是输出里有没有 `ERRO`：`make docker-import-all` 已内置该守卫（`grep -q ERRO` 即非零退出），手工导入时请照抄同一模式：

```bash
docker save --platform linux/amd64 -o /tmp/nginx-127-alpine.tar nginx:1.27-alpine
k3d image import -c rm-prod /tmp/nginx-127-alpine.tar 2>&1 | tee /tmp/nginx-import.log
grep -q ERRO /tmp/nginx-import.log && { echo "k3d image import reported ERRO" >&2; exit 1; }
```

- **多平台镜像必须 `docker save --platform linux/amd64`**：默认 save 会带上 manifest list，导入后 containerd 里的架构元数据可能与节点不匹配，导致 Pod `ImagePullBackOff` 或 `exec format error`。
- `make docker-import-all` 导的是 8 个镜像，其中 `operator` 与 `notification-sink` **不参与**本 overlay（前者只跑在客户集群，后者是 dev 专用）；生产实际使用 `api`/`auth`/`orchestrator`/`webhook`/`notifier`/`web` 六个。
- `edge` 用的 `nginx:1.27-alpine` **不在**这 8 个里，必须单独导入（上面两条命令）。
- 导入结果自查（只读）：

```bash
docker exec k3d-rm-prod-server-0 crictl images | grep -E 'release-|nginx'
# 实测应能看到 localhost:5001/release-{api,auth,notifier,orchestrator,web,webhook}:a6707b6
# 以及 docker.io/library/nginx:1.27-alpine
```

### 2.3 渲染与镜像 tag 门禁

overlay 提交的默认 tag 是 `:dev`（`deploy/kustomize/production/kustomization.yaml:80` 起 6 行 `newTag: dev`），只是为了 `kubectl kustomize` 自包含。**绝不能把 `:dev` apply 到生产**。发布前把 6 个镜像改成真实 tag：

```bash
IMAGE_TAG=$(git rev-parse --short HEAD)
cd deploy/kustomize/production
for svc in release-api release-auth release-orchestrator \
           release-webhook release-notifier release-web; do
  kustomize edit set image "localhost:5001/$svc=localhost:5001/$svc:$IMAGE_TAG"
done
cd -
make check-production-images      # FAIL 即停，不要 apply
git checkout -- deploy/kustomize/production/kustomization.yaml   # 只提交 :dev 默认
```

`make check-production-images` 是机械门禁（`Makefile:624-635`）：本地渲染、零集群接触，只要还有任何 `image:` 以 `:dev` 结尾就非零退出并打印命中行。对未改 tag 的 overlay 运行，实测输出 6 行 `image: ...:dev` 并以 `Error 1` 结束；改完后打印 `OK check-production-images: no ':dev' image tag`。

## 3. 必填 Secret

overlay **不创建任何 Secret**（渲染结果里 Secret 数为 0；dev 的明文凭据 Secret 被 delete patch 删掉，缺生产 Secret 时 fail closed，不会退回去用 dev 凭据）。所以任何一把缺失都会让对应 Pod 卡在 `CreateContainerConfigError` 或启动即退。

共 **9 个 Secret 对象 / 8 类物料**（JWT 私钥与公钥是同一对物料，分两个 Secret 存放）：

| Secret | 键 | 消费方 |
| --- | --- | --- |
| `release-manager-credentials` | `POSTGRES_USER`、`POSTGRES_PASSWORD`、`POSTGRES_DB`、`DATABASE_DSN`、`NOTIFIER_DATABASE_DSN` | PostgreSQL（`envFrom` 初始化实例）、auth/orchestrator（`DATABASE_DSN`）、notifier（从 `NOTIFIER_DATABASE_DSN` 键取 `DATABASE_DSN`） |
| `release-manager-jwt-private` | `JWT_PRIVATE_KEY` | **仅** auth（签发） |
| `release-manager-jwt-public` | `JWT_PUBLIC_KEY` | api、orchestrator（只校验） |
| `release-manager-mtls-ca` | `ca.key`、`ca.crt` | orchestrator agent 网关（签发 operator 证书） |
| `release-manager-webhook-service-token` | `WEBHOOK_SERVICE_TOKEN`（可选 `WEBHOOK_SERVICE_TOKEN_PREVIOUS`） | webhook（发送端）、orchestrator（校验端） |
| `release-manager-ci-api-key` | `CI_API_KEY` | webhook 的 CI ingress |
| `release-manager-harbor-service-token` | `HARBOR_SERVICE_TOKEN`（可选 `…_PREVIOUS`） | webhook、orchestrator |
| `release-manager-notifier-service-token` | `NOTIFIER_SERVICE_TOKEN`（可选 `…_PREVIOUS`） | notifier |
| `release-manager-edge-tls` | `tls.crt`、`tls.key` | edge（TLS 终止，见 §6） |

`DATABASE_DSN` / `NOTIFIER_DATABASE_DSN` 必须是完整的 `postgres://` URL；notifier 指向 `release_notifier`（该库由 `deploy/kustomize/postgres/init.sql` 在 PostgreSQL 首次启动时创建）。口令建议用 `openssl rand -hex 32`（URL 安全），且两个 DSN 里要出现同一个口令：

```bash
PW="$(openssl rand -hex 32)"
kubectl -n release-manager create secret generic release-manager-credentials \
  --from-literal=POSTGRES_USER=release_manager \
  --from-literal=POSTGRES_PASSWORD="$PW" \
  --from-literal=POSTGRES_DB=release_manager \
  --from-literal=DATABASE_DSN="postgres://release_manager:${PW}@postgres:5432/release_manager?sslmode=disable" \
  --from-literal=NOTIFIER_DATABASE_DSN="postgres://release_manager:${PW}@postgres:5432/release_notifier?sslmode=disable"
```

### 3.1 密钥算法是硬约束

| 物料 | 必须 | 违反后果 |
| --- | --- | --- |
| JWT 密钥对 | **Ed25519 PKCS#8**（`BEGIN PRIVATE KEY`，119 字节） | auth 启动失败；非 Ed25519 直接 `os.Exit(1)` |
| mTLS CA | **Ed25519** + `CA:TRUE` 自签 | RSA CA 会被拒绝：`CA private key must be Ed25519, got *rsa.PrivateKey`（`internal/operator/ca/ca.go` 的 `Load`），orchestrator **CrashLoop** |
| edge TLS | 任意（实测 RSA 2048） | 只影响浏览器信任，见 §6 |

```bash
mkdir -p /tmp/rm-secrets && chmod 700 /tmp/rm-secrets && cd /tmp/rm-secrets

# JWT：Ed25519 PKCS#8。私钥只进 release-manager-jwt-private，公钥只进 -public。
openssl genpkey -algorithm ED25519 -out jwt-private.pem
openssl pkey -in jwt-private.pem -pubout -out jwt-public.pem

# mTLS CA：Ed25519 + CA:TRUE。CN 与实测一致（release-manager-agent-ca）。
openssl genpkey -algorithm ED25519 -out ca.key
openssl req -x509 -new -key ca.key -sha256 -days 3650 \
  -subj "/CN=release-manager-agent-ca" \
  -addext "basicConstraints=critical,CA:TRUE" -out ca.crt

# 服务令牌：高熵随机串（实测为 64 hex 字符）
openssl rand -hex 32 > webhook-service-token
openssl rand -hex 32 > ci-api-key
openssl rand -hex 32 > harbor-service-token
openssl rand -hex 32 > notifier-service-token

kubectl -n release-manager create secret generic release-manager-jwt-private \
  --from-file=JWT_PRIVATE_KEY=jwt-private.pem
kubectl -n release-manager create secret generic release-manager-jwt-public \
  --from-file=JWT_PUBLIC_KEY=jwt-public.pem
kubectl -n release-manager create secret generic release-manager-mtls-ca \
  --from-file=ca.key=ca.key --from-file=ca.crt=ca.crt
kubectl -n release-manager create secret generic release-manager-webhook-service-token \
  --from-file=WEBHOOK_SERVICE_TOKEN=webhook-service-token
kubectl -n release-manager create secret generic release-manager-ci-api-key \
  --from-file=CI_API_KEY=ci-api-key
kubectl -n release-manager create secret generic release-manager-harbor-service-token \
  --from-file=HARBOR_SERVICE_TOKEN=harbor-service-token
kubectl -n release-manager create secret generic release-manager-notifier-service-token \
  --from-file=NOTIFIER_SERVICE_TOKEN=notifier-service-token
```

`release-manager-edge-tls` 用 `kubectl create secret tls`（§6）。轮换服务令牌时 **current 与 previous 必须并存**：只换一键会让 webhook→orchestrator 的 bundle 链路整体 `unauthenticated`（HTTP 401），直到两端都更新。

### 3.2 三份私钥必须异地备份

集群里只存一份，PVC 也不含它们。**JWT 私钥、mTLS CA 私钥、edge TLS 私钥必须抄到集群之外**（离线介质/密码管理器），并 `chmod 600`：

```bash
mkdir -p ~/rm-escrow && chmod 700 ~/rm-escrow
kubectl -n release-manager get secret release-manager-jwt-private \
  -o jsonpath='{.data.JWT_PRIVATE_KEY}' | base64 -d > ~/rm-escrow/jwt-private.pem
kubectl -n release-manager get secret release-manager-mtls-ca \
  -o jsonpath='{.data.ca\.key}' | base64 -d > ~/rm-escrow/mtls-ca.key
kubectl -n release-manager get secret release-manager-mtls-ca \
  -o jsonpath='{.data.ca\.crt}' | base64 -d > ~/rm-escrow/mtls-ca.crt
kubectl -n release-manager get secret release-manager-edge-tls \
  -o jsonpath='{.data.tls\.key}' | base64 -d > ~/rm-escrow/edge-tls.key
chmod 600 ~/rm-escrow/*
```

⚠️ **`ca\.key` / `tls\.key` 里的反斜杠不能省**：jsonpath 用 `.` 分隔键路径，不转义会找不到键（报空值），而空值重定向出来的文件会被误当成"备份成功"。

丢失后果：

| 丢失 | 后果 | 恢复 |
| --- | --- | --- |
| JWT 私钥 | auth **无法启动**（私钥是启动必需），整站无法登录/签发 | 重新生成一对并替换两个 Secret；已签发 access token 立即失效，但 refresh token 是不透明串（DB 存 SHA-256），**不等同于全员强制重登**，最坏是 access TTL（15 分钟）抖动 |
| mTLS CA 私钥 | orchestrator 网关**无法启动**；现存 operator 证书到期后无法续期 | 重新生成 CA 并替换 Secret，所有 customer 集群 operator 必须**重新 enroll**（旧证书链不再被信任） |
| edge TLS 私钥 | 可重新自签（无业务数据依赖） | 重签证书、更新 Secret、`kubectl rollout restart deployment/edge` |

## 4. 启动顺序（有依赖，乱序会失败）

顺序不能随意交换：**PostgreSQL 是 auth/orchestrator/notifier 的启动硬依赖**。跳过第 ②③ 步直接 apply 全量，实测这三者各自 CrashLoop 2 次后才恢复，`kubectl logs --previous` 的第一现场是：

```
"msg":"failed to register service","error":"connection_unavailable: ping PostgreSQL: ..."
```

### ① Namespace + 全部 Secret

```bash
kubectl apply -f deploy/kustomize/production/namespace.yaml
# 然后按 §3 创建 9 个 Secret（含 release-manager-edge-tls，见 §6）
```

`release-manager-edge-tls` 必须在 ④ 之前存在：`edge` Deployment 以 Secret 卷挂载它，缺失时 Pod 停在 `ContainerCreating`（`FailedMount: secret "release-manager-edge-tls" not found`）；④ 之后补建也能自愈，但会多一次无谓的重启。

### ② 只 apply PostgreSQL，等它就绪

overlay 没有单独的 postgres overlay，所以从一次**本地渲染**结果里挑出 PostgreSQL 的 4 个对象（Namespace、`postgres-init` ConfigMap、`postgres` Service、`postgres` StatefulSet）。这段 awk 不引入额外工具，`grep -c` 是自检：

```bash
kubectl kustomize deploy/kustomize/production > /tmp/rm-prod.yaml
awk 'BEGIN { RS = "\n---\n" }
  /\nkind: StatefulSet\n/ && /\n  name: postgres\n/ { print; next }
  /\nkind: ConfigMap\n/   && /\n  name: postgres-init/     { print; next }
  /\nkind: Service\n/     && /\n  name: postgres\n/ { print; next }
  /\nkind: Namespace\n/                            { print }' \
  /tmp/rm-prod.yaml | tee /tmp/rm-pg.yaml | kubectl apply -f -
grep -c '^kind:' /tmp/rm-pg.yaml   # 期望 4

kubectl -n release-manager wait --for=condition=Ready pod/postgres-0 --timeout=300s
kubectl -n release-manager rollout status statefulset/postgres --timeout=300s
```

### ③ 迁移 schema

两条路都可行，**首次建库推荐显式迁移**（可单独观察结果）；服务启动时也会自动 `m.Up()`。

```bash
kubectl -n release-manager port-forward --address 127.0.0.1 svc/postgres 15432:5432 &
PW="$(kubectl -n release-manager get secret release-manager-credentials \
  -o jsonpath='{.data.POSTGRES_PASSWORD}' | base64 -d)"
make migrate DATABASE_DSN="postgres://release_manager:${PW}@127.0.0.1:15432/release_manager?sslmode=disable"
```

- 对已是 34 的库执行，实测输出 `no change`。
- `make migrate` 用 `go run -tags postgres` 跑 golang-migrate CLI，**宿主机要有 Go 工具链**；它只做 `up`，不接受任意 SQL。
- ⚠️ `make migrate`（golang-migrate CLI）**不等于**服务的启动迁移：服务路径会先跑 `repairHistoricalMigrationVersion()` 重放历史上被复用的 7/8 号迁移，CLI 不会。**对已有历史的库（恢复出来的 dump、长期存在的 dev 库）**，先让服务启动一次完成修复，再用 `make migrate` 做后续显式步骤；全新库两条路收敛。
- DSN 含明文口令，`make` 会把整条命令回显到终端，别贴进共享终端或工单。

### ④ apply 其余对象（含真实镜像 tag）

先完成 §2.3 的 tag 改写与 `make check-production-images`，再：

```bash
kubectl apply -k deploy/kustomize/production
kubectl -n release-manager rollout status deployment/orchestrator --timeout=300s
kubectl -n release-manager rollout status deployment/auth --timeout=300s
```

`default-deny-ingress` 下**探针必须显式放行**：k3s 默认 kube-router 把 NetworkPolicy 装进 filter **INPUT** 链，kubelet → Pod 的探针包来自节点地址，会被 default-deny 丢掉，结果是**所有应用 Pod 永远不 Ready**（k3s issue #10030）。overlay 因此有 `allow-kubelet-probes`，源地址是 Pod CIDR（`nodeCidr`，默认 `10.42.0.0/16`，见 `deploy/kustomize/production/networkpolicies.yaml:60` 与 `production-settings.yaml`），端口是 8082–8088。换 CIDR 只改 `production-settings.yaml` 一处（kustomization 的 replacement 负责写进 ipBlock）。（⚠️ 这整套策略在本集群上是否被**真正执行**未证实：没有 CNI DaemonSet 可作为证据，见 §10-12——「探针仍能通」不能单独证明策略生效。）

- **k3s < v1.28.9 / < v1.29.4** 命中 #10030 的旧行为（探针被策略拦截），这些版本上本 overlay 不起作用；本集群实测 **v1.31.5 无此问题**。
- 非 k3s CNI（不在节点路径上评估策略）可能不需要这条规则，但也**不会因此出错**。

### ⑤ edge TLS 与证书信任

若 ① 已按 §3 建好 `release-manager-edge-tls`，本步只剩验签与浏览器信任：自签证书需要在浏览器里做**一次性信任例外**（自动化脚本用 `ignoreHTTPSErrors: true`），证书 SAN 必须含 `localhost`/`127.0.0.1`。重签或轮换后的 `kubectl rollout restart deployment/edge` 与完整命令见 §6。

### ⑥ 把宿主 8443 映射到 NodePort 30443

```bash
# 新集群（该端口尚无映射）：直接加，显式绑 loopback
k3d node edit k3d-rm-prod-serverlb --port-add "127.0.0.1:8443:30443"
docker port k3d-rm-prod-serverlb
# 30443/tcp -> 127.0.0.1:8443

# 已有 0.0.0.0 映射时（本集群 2026-10-18 12:27 之前的形态）：先删再加。
# --port-delete 需要 k3d >= v5.9.0；v5.8.3 没有该 flag，且它的
# --port-add "127.0.0.1:8443:30443" 会被 IsPortBindingEqual 判重静默跳过。
k3d node edit k3d-rm-prod-serverlb --port-delete "8443:30443"
k3d node edit k3d-rm-prod-serverlb --port-add "127.0.0.1:8443:30443"
```

这一步只改 k3d 负载均衡节点，**不重建集群、不动 PVC 与 Secret**。每次 `k3d node edit` 都会替换 serverlb 容器（rename → create → stop old → start new → delete old），因此删除到重新添加的整个窗口里 8443/8444 **同时中断**：2026-10-18 12:27:39.054–12:28:06.378 +08 实测，200 ms 探针的断开时长为 **27.32 s**（K8s 侧 Pod/PVC 不受影响）。收窄后的绑定只有 `127.0.0.1`，不再有 IPv4 `0.0.0.0` 或 IPv6 `[::]` 监听。

**回滚到对外绑定**（确需跨机访问时；k3d ≥ v5.9.0，同样是两步——`--port-delete` 与 `--port-add` 不能混在一条命令里，否则 k3d 直接报 `Cannot combine port addition and deletion`）：

```bash
# 回滚到对外绑定（k3d >= v5.9.0；两步，不能混用）
k3d node edit k3d-rm-prod-serverlb \
  --port-delete "127.0.0.1:8443:30443" --port-delete "127.0.0.1:8444:30844"
k3d node edit k3d-rm-prod-serverlb \
  --port-add "8443:30443" --port-add "8444:30844"
docker port k3d-rm-prod-serverlb
# 预期：30443/tcp -> 0.0.0.0:8443（HostIP 留空 = 不再限 loopback）
```

⚠️ **这会重新对外暴露**控制台（8443）与 operator 网关（8444），只在确实需要跨机访问时做，且应优先用前置反代（只监听受信网段）或宿主防火墙限制来源，不要为图方便长期开放。**停机预期**：每次 `node edit` 替换一次 serverlb 容器，收窄时两次合计实测 **27.32 s** 中断（见上）；回滚是同形状的两次编辑，**未单独复测**，按同一量级预期，不要安排在业务时段。回滚后用 `docker port` 复核 HostIP，并从另一台主机实测端口可达性。

### ⑦ 把宿主 8444 映射到 operator NodePort 30844

客户集群的 operator 走控制面 mTLS 网关（`orchestrator:8084`）。该监听器在 orchestrator **进程内**终止 TLS，并按 client cert 校验每个请求的身份（`ClientAuth: VerifyClientCertIfGiven` + `ClientCAs`），因此**不能**经 edge 反代：`web/nginx.conf` 对 `/operator.v1.` 是明文 HTTP，客户端证书在那一跳就丢了。客户集群又无法访问本 namespace 的 ClusterIP，所以 overlay 给 `operator` Service 单独保留 NodePort `30844`（`deploy/kustomize/production/kustomization.yaml` 的 operator patch），再把宿主端口映射进来：

```bash
k3d node edit k3d-rm-prod-serverlb --port-add "127.0.0.1:8444:30844"
docker port k3d-rm-prod-serverlb
# 30844/tcp -> 127.0.0.1:8444
```

已有 `0.0.0.0` 映射时的先删再加（`--port-delete`，k3d ≥ v5.9.0）、容器替换与停机口径同 ⑥。

> ⚠️ 2026-10-18 起 8444 只绑 `127.0.0.1` ⇒ **客户集群不能直连**（包括 §12 的 `host.k3d.internal` 路径）。要接客户集群，先按 §10-5 用反代/防火墙把网关暴露给受信网段，不要改回 `0.0.0.0`。

客户集群侧把 `--orchestrator-addr` 指向宿主：

```bash
release-operator --orchestrator-addr https://<宿主地址>:8444 ...
```

⚠️ `<宿主地址>` 必须通过**网关服务端证书的主机名校验**：当前证书 SAN 固定为 `operator-gateway.dev.release-manager.local` 与 `localhost`（`cmd/orchestrator/main.go:173-176`），**不含节点 IP**。实测（2026-10-18）：

```bash
openssl s_client -connect 127.0.0.1:8444 -CAfile ca.crt -verify_hostname 127.0.0.1
# Verify return code: 62 (hostname mismatch)
openssl s_client -connect 127.0.0.1:8444 -CAfile ca.crt -verify_hostname localhost
# Verify return code: 0 (ok)
```

因此客户集群要么把 SAN 名（`operator-gateway.dev.release-manager.local`）解析到宿主（DNS 或 Pod `hostAliases`，形态与 `deploy/kustomize/customer-agent/base/deployment.yaml` 一致），要么扩展服务端证书的 SAN 列表（`cmd/orchestrator/main.go` 的代码改动，不在本 overlay 范围）。

从宿主验证（2026-10-18 实测）：

```bash
# CA 取自控制面 Secret（只用于校验服务端证书，不要留在宿主）
kubectl -n release-manager get secret release-manager-mtls-ca \
  -o jsonpath='{.data.ca\.crt}' | base64 -d > /tmp/rm-ca.crt

timeout 3 bash -c 'exec 3<>/dev/tcp/127.0.0.1/8444' && echo "tcp ok"   # tcp ok
openssl s_client -connect 127.0.0.1:8444 -CAfile /tmp/rm-ca.crt -brief </dev/null
# CONNECTION ESTABLISHED / Protocol version: TLSv1.3 / Verification: OK

# 不带 client cert 的请求在请求层被拒（TLS 握手仍然完成）
curl -sk -X POST https://127.0.0.1:8444/orchestrator.v1.OrchestratorService/SyncInventory \
  -H 'Content-Type: application/json' -d '{"requestId":"task-347"}'
# HTTP 401 {"code":"unauthenticated","message":"client certificate required"}

rm -f /tmp/rm-ca.crt
```

这证明「路通了、认证仍由 CA 把关」：TCP/TLS 到达 orchestrator，但没有控制面 CA 签发的 client cert 就进不了业务面。

**重启口径（带时间窗，采样 2026-10-18 00:47–00:49 +08）**：本次 Service 变更 apply 前后，**变更窗口内未新增重启**——10/10 Pod 全程 `Running/Ready`，Pod 名与 IP 未变。四个 Pod 显示的 `RESTARTS=2` 是**变更之前**的启动期崩溃，与本变更无关：`lastState.terminated.reason=Error`、`exitCode=1`，最后终止时刻 `2026-10-17T10:57:15Z`（auth/notifier/orchestrator）与 `2026-10-17T14:36:54Z`（api），各与自己的 Pod 创建时刻相差 <2 min，均早于变更窗口。复核采样 2026-10-18 01:16 +08 计数与 `lastState` 未变。所以**别把「10/10 Ready」读成「从未重启」**：判断有无新重启要看 `RESTARTS` 计数与 `lastState.terminated.finishedAt`，不能只看 `Ready`。

⚠️ **安全面**：宿主 8443/8444 自 2026-10-18 起只绑 `127.0.0.1`（§10-5），非 loopback 地址连不上；要跨机接入时**不要**改回 `0.0.0.0`，应前置反代或宿主防火墙放行受信来源。网关自身的 mTLS 仍是最终门槛。

## 5. kubeconfig 坑（实测）

k3d 可能把 server 写成 `https://0.0.0.0:<port>`。`kubectl` 连 0.0.0.0 会直接 `EOF`，症状看起来像"集群没起来"：

```
Couldn't get current server API group list ... Get "https://0.0.0.0:44609/api?timeout=32s": EOF
```

检查与修正：

```bash
kubectl config view -o jsonpath='{range .clusters[*]}{.name}{" -> "}{.cluster.server}{"\n"}{end}' | grep k3d-rm-prod

PORT="$(docker port k3d-rm-prod-serverlb 6443/tcp | head -1 | sed 's/.*://')"
kubectl config set-cluster k3d-rm-prod --server="https://127.0.0.1:${PORT}"
kubectl -n release-manager get pods      # 应正常返回
```

本机已把默认 `~/.kube/config` 修成 `https://127.0.0.1:<port>`（修前备份在 `/tmp/kubeconfig-backup-*`）。这是一次性修复，`k3d cluster delete` 之后重建集群可能再次写入 `0.0.0.0`，届时重复本节。

## 6. TLS 与证书

`release-auth` 的浏览器会话固定 `Secure` + `SameSite=Strict`（`internal/auth/browser_session.go`；`secure_cookies` 默认 `true`，生产配置未覆盖）。直接后果：

- 纯 HTTP 访问时浏览器**不会回传** `rm_access`/`rm_refresh`/`rm_csrf`，表现为"登录接口 200 但立刻未认证"；
- 换端口/换主机名也算跨 origin，同样丢 cookie；
- ⇒ 必须 **HTTPS + 单 origin**，这正是 edge 存在的理由。

自签证书（SAN 必须同时含 `localhost`、`rm-prod.local`、`127.0.0.1`，否则 `https://localhost:8443` 报主机名不匹配）：

```bash
mkdir -p /tmp/rm-edge-tls
openssl req -x509 -newkey rsa:2048 -sha256 -days 825 -nodes \
  -keyout /tmp/rm-edge-tls/tls.key -out /tmp/rm-edge-tls/tls.crt \
  -subj "/CN=localhost/O=release-manager-prod" \
  -addext "subjectAltName=DNS:localhost,DNS:rm-prod.local,IP:127.0.0.1" \
  -addext "basicConstraints=critical,CA:FALSE" \
  -addext "keyUsage=critical,digitalSignature,keyEncipherment" \
  -addext "extendedKeyUsage=serverAuth"
chmod 600 /tmp/rm-edge-tls/tls.key
kubectl -n release-manager create secret tls release-manager-edge-tls \
  --cert=/tmp/rm-edge-tls/tls.crt --key=/tmp/rm-edge-tls/tls.key
```

轮换/重签后用 `kubectl -n release-manager rollout restart deployment/edge` 让 nginx 重新读取（Secret 挂载本身会更新，但 nginx 只在启动时加载证书）。

验证：

```bash
openssl s_client -connect localhost:8443 -servername localhost </dev/null \
  | openssl x509 -noout -subject -ext subjectAltName
curl -k -s -o /dev/null -w '%{http_code}\n' https://localhost:8443/
curl -k -s -X POST https://localhost:8443/auth.v1.AuthService/GetInitStatus \
  -H 'Content-Type: application/json' -d '{}'
```

实测：`subject=CN=localhost, O=release-manager-prod`，SAN 三项齐全，根路径 200，`GetInitStatus` 返回 `{"initialized":true}`。

**建议（当前未做）**：自签证书只适合一次性/内网环境，浏览器需要一次性信任例外（自动化脚本要 `ignoreHTTPSErrors`）。上真实流量前应换成 CA 签发证书，或把 edge 换成真正的 Ingress controller + 证书管理。

## 7. 备份与恢复（已演练）

### 7.1 备份

**自动备份已存在**（TASK-340）：`CronJob/release-manager-backup` 每日 `02:00 UTC` 对
`release_manager`（必需）与 `release_notifier`（确证存在且有基表时）各做一次 `pg_dump -Fc`，
写入 PVC `release-manager-backups`（5Gi），保留 7 天。schedule/保留/位置/手动触发见 §7.4。

手工 dump 仍可用于临时取样（在宿主执行）：

```bash
kubectl -n release-manager port-forward --address 127.0.0.1 svc/postgres 15432:5432 &
export PGPASSWORD="$(kubectl -n release-manager get secret release-manager-credentials \
  -o jsonpath='{.data.POSTGRES_PASSWORD}' | base64 -d)"
pg_dump -h 127.0.0.1 -p 15432 -U release_manager -d release_manager \
  > /tmp/release_manager-$(date +%Y%m%dT%H%M%S).dump
```

- 同时要备份 `release_notifier` 库（`-d release_notifier`），以及 §3.2 的三份私钥。
- 手工演练实测：dump **93,201 B / 58 张表**；同日复核同一库为 **101,630 B / 58 张表**（体积随数据增长，**不要把字节数当断言**，判据用表数与迁移版本）。
- ⚠️ 宿主 `pg_dump` 是 **18.6**，与集群 PostgreSQL **16.15** 跨大版本；自动备份在 `postgres:16` 镜像内执行，客户端/服务端同大版本，不受 §7.2 的坑影响。

### 7.2 恢复（版本坑）

本机 `pg_dump` 是 **18.6**，集群里是 PostgreSQL **16.15**。PG17+ 的客户端会在 dump 头部写 `SET transaction_timeout = 0;`，**PG 16 不认识这个 GUC，直接报错中断 restore**。恢复前必须剔除该行（**只适用于 plain SQL、且由宿主 18.x 客户端生成的 dump**；自动备份的 `-Fc` 归档由 16.15 生成，不包含该行）：

```bash
grep -v '^SET transaction_timeout' /tmp/release_manager-YYYYMMDDTHHMMSS.dump \
  > /tmp/release_manager-restore.sql

psql -h 127.0.0.1 -p 15432 -U release_manager -d postgres \
  -c 'CREATE DATABASE release_manager_restore_check'
psql -h 127.0.0.1 -p 15432 -U release_manager -d release_manager_restore_check \
  -v ON_ERROR_STOP=1 -f /tmp/release_manager-restore.sql
```

自动备份是 custom format，用 `pg_restore` 恢复，不做上面的文本替换：

```bash
pg_restore -h 127.0.0.1 -p 15432 -U release_manager -d release_manager_restore_check \
  --no-owner --no-privileges /tmp/release_manager-YYYYMMDDTHHMMSS.dump
```

### 7.3 恢复后必须校验

```bash
PSQL="psql -h 127.0.0.1 -p 15432 -U release_manager -d release_manager_restore_check -Atc"
$PSQL "select version, dirty from schema_migrations"                       # 期望 34|f
$PSQL "select count(*) from audit_events"                                  # 与源库一致（演练时 2）
$PSQL "select count(*) from information_schema.tables where table_schema='public'"  # 期望 58
$PSQL "select count(*) from pg_indexes where schemaname='public'"           # 期望 146
```

手工演练结果：dump 93,201 B / 58 表 → restore 到临时库 → `schema_migrations` = `34|f`，与源库一致；索引数、关键表行数一致。校验完删掉临时库。

**自动备份演练（2026-10-17 实测，TASK-340）**：`postgres-0` 不挂备份 PVC，先立即触发一次 CronJob，再用一个挂载同一 PVC 的临时 Job 做 restore（示例见 [production overlay README](../deploy/kustomize/production/README.md) 的 restore 片段）：

```bash
kubectl -n release-manager create job --from=cronjob/release-manager-backup backup-drill
kubectl -n release-manager wait --for=condition=complete job/backup-drill --timeout=600s
kubectl -n release-manager logs job/backup-drill
# 实测 5 s 完成：release_manager dump 133,173 B；release_notifier dump 4,592 B
```

restore 作业输出（逐项与源库对比，不一致即失败）：

```text
schema_migrations    source=34|false     restored=34|false     OK
users                source=1            restored=1            OK
audit_events         source=2            restored=2            OK
casbin_rule          source=6            restored=6            OK
auth_sessions        source=14           restored=14           OK
tables               source=58           restored=58           OK
indexes              source=146          restored=146          OK
all checks passed
```

演练全过程只读源库：结束后 `audit_events` 仍 **2 行**。校验完 `DROP DATABASE rm_backup_check`、删演练 Job；**保留 CronJob 与备份 PVC**。

⚠️ **恢复到已有历史的库之后**，先让服务启动一次（跑 `repairHistoricalMigrationVersion`）再考虑 `make migrate`，理由见 §4-③。

### 7.4 自动备份（已存在）

`deploy/kustomize/production/backup-cronjob.yaml` 定义 `CronJob/release-manager-backup` 与 PVC `release-manager-backups`：

| 项 | 值 |
| --- | --- |
| 调度 | `0 2 * * *`（02:00 UTC），`concurrencyPolicy: Forbid`，`startingDeadlineSeconds: 600` |
| 库 | `release_manager`（必需）+ `release_notifier`（探针确证存在且有基表时；空库跳过；**探针或 dump 失败一律 Job 红**，不再把连接失败误记成“库不存在”） |
| 格式/位置 | `pg_dump -Fc`，PVC `release-manager-backups`（5Gi，与 PG 同一 `storageClassName` 旋钮），容器内 `/backups` |
| 文件名 | `release_manager-<UTC 时间戳>.dump` / `release_notifier-<UTC 时间戳>.dump`（`YYYYmmddTHHMMSSZ`）；每个库先写 `….dump.tmp`，成功后才 rename 成该名字 |
| 保留 | 同一次运行内 `find … -mtime +7 -delete`：删除 **7 天前**的文件（不是“保留 7 个文件”，漏跑一次不会把最后一份好 dump 删掉）；另有一次年龄保护的 `*.dump.tmp` 孤儿清理 |
| 失败处理 | `backoffLimit: 2`、`activeDeadlineSeconds: 1800`；保留 3 成功 + 3 失败 Job |
| 凭据 | 从 `release-manager-credentials` 取 `POSTGRES_USER`/`POSTGRES_PASSWORD`（`PGPASSWORD`）/`POSTGRES_DB`；清单与日志中没有口令 |
| 运行身份 | `postgres:16`，`runAsNonRoot` uid/gid **999**，`fsGroup: 999`，drop ALL caps，只读 rootfs |

**备份原子性（TASK-342）**：每个库先写 `release_manager-<stamp>.dump.tmp`（notifier 同理），只有
`pg_dump` 返回成功、归档**非空**且已 `sync` 落盘后，才在 `/backups` 内把它 `mv` 成最终的 `*.dump`。
任何一步失败都会先 `rm -f` 该临时文件再让 Job 变红，因此**失败的运行不会留下与好备份同名的 0 字节
`*.dump`**（TASK-340 期间实测过该缺陷：`pg_dump` 认证失败留下的 0 字节文件与成功产物同名，只能等
7 天 retention 清掉；若被误当备份 restore，只会得到空库）。retention 仍只匹配 `*.dump`；被
`SIGKILL`/节点崩溃跳过的孤儿 `*.dump.tmp` 由同一次运行里的
`find … -name '*.dump.tmp' -mtime +1 -delete` 清理（`*.dump` 与 `*.dump.tmp` 不会互相误匹配）。

**手动触发**：

```bash
kubectl -n release-manager create job --from=cronjob/release-manager-backup backup-manual
kubectl -n release-manager wait --for=condition=complete job/backup-manual --timeout=600s
kubectl -n release-manager logs job/backup-manual
```

**恢复**：见 §7.2–§7.3；从 PVC 恢复要点是临时 Pod/Job 必须挂载 `release-manager-backups`。

**运维注意**：

- `concurrencyPolicy: Forbid` 只约束 CronJob 控制器**自动调度**的运行：`create job --from=cronjob/...` 生成的是独立 Job，控制器不追踪它 ⇒ **手动触发不受防重约束**。请避开 02:00 UTC 的自动场次（两者共用同一 PVC，且都会执行 7 天清理）。
- Job Pod 启动瞬间，新 Pod IP 可能尚未被节点 NetworkPolicy 放行（k3s kube-router 实测约 **2 s**）。脚本先 `pg_isready` 有界等待（60 × 2 s）再 dump；这不是多余的重试，去掉会在 02:00 偶发 `connection refused`。
- 备份与 PostgreSQL **同节点同盘**（`local-path`）：节点丢失会同时丢库和备份；且没有 WAL 归档，RPO 最长为 24 h。生产使用前必须加异地拷贝，并单独备份 §3.2 的三份私钥。
- 镜像 `postgres:16` 必须已在节点本地（本集群无 docker.io 出口），否则 CronJob 会 `ImagePullBackOff`。

## 8. 升级与回滚

### 8.1 升级（镜像 tag 化发布）

镜像 tag 就是版本。流程 = §2.3 的 tag 改写 + 门禁 + apply + 逐服务观察：

```bash
IMAGE_TAG=$(git rev-parse --short HEAD)
make docker-build-all IMAGE_TAG="$IMAGE_TAG"
make docker-import-all CLUSTER=rm-prod IMAGE_TAG="$IMAGE_TAG"
# 按 §2.3 改写 6 个镜像 tag
make check-production-images
kubectl apply -k deploy/kustomize/production
kubectl -n release-manager rollout status deployment/orchestrator --timeout=300s
kubectl -n release-manager rollout status deployment/web --timeout=300s
```

- 迁移在**新进程启动时**自动执行（`m.Up()`），失败即 `migration_failed` + `os.Exit(1)` → CrashLoop，不会有半启动服务接客。
- **schema 只能前滚**：仓库没有与镜像配套的降级通道。`migrations/*.down.sql` 文件存在，但生产进程从不执行 `RunMigrationsDown`（只有 dev 重置路径用它）⇒ 「新 schema + 旧二进制」通常不可逆。
- **升级前先备份（§7）并评估 schema 兼容性**：若新版本会前滚 schema，`rollout undo` 只回退镜像、**不会回退迁移**；旧二进制若读不懂新列/新约束，回滚后会起不来。

#### 8.1.1 实测：单服务升级（2026-10-18，TASK-349）

选择 `web` 作为演练对象：`ceda261..b96d964` 之间 api 没有源码变更，web 有真实前端修复（`1851552`，TASK-344），且 web 只依赖未改动的后端、2 副本滚动，影响面最小。**只动 `deployment/web` 一个对象**。

```bash
# 构建：worktree 在 origin/main（b96d964）
cd /tmp/rm-wt349
docker build \
  --build-arg NODE_IMAGE=docker.1ms.run/library/node:24-alpine \
  --build-arg NGINX_IMAGE=nginx:1.27-alpine \
  -f deploy/docker/Dockerfile.web \
  -t localhost:5001/release-web:b96d964 .
# 实测 33 s

# 保存 + 导入（多平台/带 attestation 的镜像必须 --platform linux/amd64；判据是 ERRO 而不是 Successfully）
docker save --platform linux/amd64 -o /tmp/rm349/release-web-b96d964.tar \
  localhost:5001/release-web:b96d964          # < 1 s，21 MiB
k3d image import -c rm-prod /tmp/rm349/release-web-b96d964.tar 2>&1 | tee /tmp/rm349/import-web.log
grep -q ERRO /tmp/rm349/import-web.log && { echo "k3d image import reported ERRO" >&2; exit 1; }
# 实测 4 s，无 ERRO；节点 containerd 可见 release-web:b96d964
```

本次只 build/import 了一个服务，没有走 `make docker-build-all`（它会构建全部 8 个镜像）；使用的 Dockerfile、tag 规则和 ERRO 守卫与 Makefile 一致。

```bash
# 升级（只改一个 Deployment）
kubectl -n release-manager set image deploy/web web=localhost:5001/release-web:b96d964
kubectl -n release-manager rollout status deploy/web --timeout=300s
# output: deployment "web" successfully rolled out        # 8 s
```

三份基线（同一套命令在升级前/升级后/回滚后各取一次）：

| 项 | 升级前 | 升级后 | 回滚后 |
| --- | --- | --- | --- |
| `web` Deployment 镜像 | `release-web:ceda261` | `release-web:b96d964` | `release-web:ceda261` |
| `web` Deployment revision | `2` | `3` | `4`（见 §8.2.1） |
| Pod Ready | 10/10 | 10/10 | 10/10 |
| 6 个服务 `/readyz` | 全 200 | 全 200 | 全 200 |
| 浏览器登录（真 Chrome） | 11/11，离开 `/login` 672 ms | 11/11，651 ms | 11/11，649 ms |
| `schema_migrations` | `34\|false` | `34\|false` | `34\|false` |
| `audit_events` / `users` / `tables` | `12 / 2 / 58` | `12 / 2 / 58` | `12 / 2 / 58` |

验证命令（`/readyz` 逐服务取，`web` 的 `/readyz` 反代的是 orchestrator）：

```bash
kubectl -n release-manager port-forward svc/web 18087:8087 &   # 其余服务换 svc/端口
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18087/readyz
kubectl -n release-manager get deploy web \
  -o jsonpath='{.spec.template.spec.containers[0].image}{"\t"}{.metadata.annotations.deployment\.kubernetes\.io/revision}{"\n"}'
kubectl -n release-manager exec postgres-0 -- psql -U release_manager -d release_manager -Atc \
  "select version||'|'||dirty from schema_migrations; select count(*) from audit_events; select count(*) from users"
```

- 升级恰好替换了 2 个 web Pod（旧 Pod `Terminating` → 新 RS 2/2 Ready）；其余 8 个 Pod 的名字和 `RESTARTS` 计数都没变。
- **本次没有跑数据库迁移**：web 镜像只有 nginx，不执行 `m.Up()`；`schema_migrations` 三次都是 `34|false`。这是“镜像升级”而不是“schema 升级”，边界见 §8.4。

### 8.2 回滚

```bash
kubectl -n release-manager rollout undo deployment/auth
kubectl -n release-manager rollout status deployment/auth --timeout=300s
```

- 只回退镜像，**不回退 schema**。若新版本已经前滚了不兼容的 schema，回滚镜像后会起不来，唯一退路是从 §7 的 dump 恢复。
- 实测（2026-10-18 演练前）`auth`/`notifier`/`orchestrator`/`webhook` 仍是 `REVISION 1`，`api` 已到 `4`、`web` 到 `2`（前几次镜像 tag 发布累积）⇒ **`rollout undo` 只在发布过至少两次的 Deployment 上有可退版本**，别把它当成第一次部署的保险。
- 更长久的做法是把上一版 tag 记在发布记录里，回滚时重新 `kustomize edit set image` 到旧 tag 再 apply（与 8.1 同一条链路，可审计）。

#### 8.2.1 实测：同一次演练的回滚（2026-10-18，TASK-349）

```bash
kubectl -n release-manager rollout undo deploy/web
# Warning: resource deployments/web was previously managed with 'kubectl apply'. ...
kubectl -n release-manager rollout status deploy/web --timeout=300s
# output: deployment "web" successfully rolled out        # 8 s
```

回滚后镜像回到 `release-web:ceda261`，2 个新 web Pod Ready，`/readyz` 全 200，登录 11/11（649 ms），DB 三次数值不变（见 §8.1.1 表）。

⚠️ **revision 陷阱：undo 是“新开一个 revision”，不是把 revision 号退回去。**

```text
# 升级后
REVISION  CHANGE-CAUSE
1         <none>
2         <none>
3         <none>

# 回滚后
REVISION  CHANGE-CAUSE
1         <none>
3         <none>
4         <none>
```

revision 2 的 ReplicaSet 被复用并把注解改成 `4`（Pod 模板 = 旧镜像 `ceda261`），revision 3 是被回滚掉的新镜像。后果：

- **回滚后再执行一次 `rollout undo` 会回到 revision 3（新镜像 `b96d964`）**，等于把升级又做一遍。要“撤销回滚”必须显式 `--to-revision=<旧号>`，或走 §8.1 的 kustomize 链路把 tag 改回去。
- `rollout undo` 不更新 `kubectl.kubernetes.io/last-applied-configuration`（上面的 Warning 就是它）。经 kustomize 管理的 Deployment，回滚也建议 `kustomize edit set image` + `apply`，保持清单、注解、集群三者一致。
- 本次演练前 web 的 `last-applied-configuration` 仍是首次部署的 `a6707b6`，而活体镜像在演练前已是 `ceda261` ⇒ 两者本来就不一致；这不是本次演练引入的，但说明 live tag 与 Git 清单的对应关系必须靠发布记录维护。

### 8.3 停机/重启验证

```bash
k3d cluster stop rm-prod && k3d cluster start rm-prod
kubectl -n release-manager get pods
```

实测停服约 **57 s**，重启后 10/10 Pod 恢复 Ready，PostgreSQL/Redis 数据完好（`local-path` 卷留在宿主，随 `k3d cluster delete` 才会丢）。注意 `k3d cluster stop` 之后 `kubectl` 会连接失败，属预期。

### 8.4 本次演练的边界（未验证项，不要外推）

- **只升级/回滚了 `web` 一个 Deployment**：`api`/`auth`/`orchestrator`/`notifier`/`webhook` 全程未动（镜像与 revision 不变）。
- **没有跑任何数据库迁移**：web 镜像是 nginx，不执行 `m.Up()`；升级前后 `schema_migrations` 都是 `34|false`。因此本次**没有**验证「新 schema + 旧镜像」的兼容性，也没有验证“回滚后的迁移兼容”。要验证那条路径，需要一个会前滚 schema 的新版本，先按 §7 备份、再升级、再回滚镜像，单独做一次演练。
- 新 web 镜像包含 TASK-344 的前端修复（`1851552`），但本次只验证了登录、会话刷新、审计页和 `/readyz` 仍正常，**没有逐项验证该修复的行为**。
- 镜像构建用了本机 CN 镜像前缀 `docker.1ms.run/library/{node,nginx}:…`；Dockerfile 默认是 digest 固定的官方镜像，两者不保证逐字节相同。CI 或可复现构建应使用默认参数。
- 演练用 `kubectl set image` + `rollout undo`，**绕过了 kustomize 的 tag 默认值**：仓库里的 6 个 tag 仍是 `:dev`，本次也没有改 `last-applied-configuration`。正式发布仍以 §2.3 + `kubectl apply -k` 为准；本节的命令只用于单服务、可回滚的紧急升级。
- 演练结束后 `web` Deployment 的镜像与 Pod 数已回到演练前（`ceda261`、2/2），但 **revision 注解从 2 变成 4**、revision 序列变成了 `1,3,4`；这是 `rollout undo` 的记账副作用，不是配置漂移。

### 8.5 实测：`778ded6` 全量发布与迁移 34→35（2026-10-18，TASK-362）

TASK-349 只换过 `web` 一个 Deployment，且没有跑迁移；本次把 `origin/main` 的 `778ded6`（#432 owner 校验、#436 `sessions` 身份列 + 迁移 000035、#437 web W9/W10/W12/W21）**全量**上线，第一次在生产路径上验证「6 个 Go 服务 + web 一起换镜像、由新进程前滚迁移」。

**基线（发布前实测）**

| 项 | 值 |
| --- | --- |
| Deployment 镜像 | `api=localhost:5001/release-api:876df9a`；`auth`/`orchestrator`/`notifier`/`webhook=…:a6707b6`；`web=…:ceda261`；`edge=nginx:1.27-alpine` |
| revision | `api=9`、`web=7`、`auth`/`orchestrator`/`notifier`/`webhook=1`、`edge=1` |
| Pod | 10 个工作负载 Pod `1/1` + 1 个 `Completed` 的备份 Job Pod |
| schema | `34\|false` |
| `sessions` | 9 行，其中 6 行的 `customer_id`/`cluster_id` 为空串 |
| 入口 | `https://localhost:8443/` → 200 |
| 备份 | `release_manager-20261018T020000Z.dump`，**159,481 B**（02:00 UTC 的 CronJob 产出，取自 Job 日志） |

**构建与导入**

宿主是 `x86_64`、Docker server arch `amd64`；构建目标 8 个（含 `operator` 与 `notification-sink`，`edge` 用现成的 `nginx:1.27-alpine` 不动）：

```bash
git worktree add /tmp/rm-wt362 -b chore/362-deploy-fixes origin/main   # HEAD = 778ded6
cd /tmp/rm-wt362
make docker-build-all IMAGE_TAG=778ded6
for svc in api auth orchestrator operator webhook notifier notification-sink web; do
  docker save --platform linux/amd64 -o "/tmp/rm362/$svc.tar" "localhost:5001/release-$svc:778ded6"
done
k3d image import -c rm-prod /tmp/rm362/*.tar 2>&1 | tee /tmp/rm362/import-all.log
grep ERRO /tmp/rm362/import-all.log      # 0 行
```

- `docker image inspect` 显示 8 个镜像全是 `linux/amd64`；`docker save` 产物的 `index.json` 只有 amd64 一个 manifest（构建走 buildx，attestation 不随 `--platform` 进 tar）。
- 8 个 tar 合计约 **116 MiB**，导入 **29 s**；输出 `Successfully imported 8 image(s)` 且 `grep ERRO` **0 命中**；节点内 `crictl images` 可见 8 个 `:778ded6`。

**滚动（逐个 `set image`，不 `apply -k`，每步等 Ready）**

| 服务 | 耗时 | 备注 |
| --- | --- | --- |
| `api` | **5 s** | 最先滚；启动时执行迁移 |
| `orchestrator` | **5 s** | #432/#436 的代码在这里 |
| `auth` | **6 s** | |
| `notifier` | **6 s** | |
| `webhook` | **6 s** | |
| `web` | **8 s** | 2 副本，最后滚 |

命令模板（`$SVC` 为服务名）：

```bash
kubectl -n release-manager set image deploy/$SVC $SVC=localhost:5001/release-$SVC:778ded6
kubectl -n release-manager rollout status deploy/$SVC --timeout=300s
```

每步 Ready 后立即取 `/health` 与 `/readyz`（port-forward 到 ClusterIP）。发布后 revision 变成 `api=10`、`web=8`、其余 4 个 Go 服务 `2`（`edge` 仍 1）。

**迁移 34→35 与 #436 回填**

`api` 的启动路径是 `cmd/api/store.go:80` → `postgresstore.Open(..., migrations.FS)` → `internal/store/postgres/db.go:179` 的 `RunMigrations`；`orchestrator`/`auth`/`notifier` 也各自在启动时跑同一个 `migrations.FS`。

```bash
kubectl -n release-manager exec postgres-0 -- psql -U release_manager -d release_manager -Atc \
  "select version,dirty from schema_migrations"                       # 35|f
kubectl -n release-manager exec postgres-0 -- psql -U release_manager -d release_manager -Atc \
  "select count(*) filter (where customer_id='' or cluster_id=''), count(*) from sessions"   # 0|9
```

- `migrations/000035_session_identity_backfill.up.sql` 把 6 行空串 `sessions` 按 `operators` 的权威 `customer_id`/`cluster_id` 回填为 `56846183-…`/`rm-cust`；回填后 9 行全部非空（**6 → 0**）。
- 它的 down 是空注释：这是数据修复，旧值 `''` 就是缺陷本身，没有可恢复的信息；生产进程也从不执行 `RunMigrationsDown`。
- `internal/operator/service.go:488` 现在是新会话写入 scope 的落点（`CustomerID: op.CustomerID, ClusterID: op.ClusterID`）。本集群 9 行 `sessions` 全是历史行（3 `revoked` + 6 `offline`），**没有在线 operator**，所以「新写入带 scope」只有源码与回填证据，没有在线会话实测。

**冒烟（真 Chrome：playwright-core + 系统 Chrome + `ignoreHTTPSErrors`）**

| 项 | 结果 |
| --- | --- |
| 登录 | 成功，离开 `/login` **923 ms** |
| `/audit` | 200，**20 行 / 共 34 条** |
| `/operations` | 200，空队列（当下没有非终态 Operation） |
| Release 清单 | 200，**2 行**（`e2e-release/…`、`e2e-plan/…`） |
| Release 操作历史 + Operation 详情 | 200；详情页渲染 Intent/Before/After（真实紧急操作 `40696e12…`） |
| 控制台 | 3 条 error，均已归因：2 条登录前的 `ValidateToken`/`RefreshToken` **401**（未登录时的预期探测）、1 条本次故意构造的 `actor` 不一致请求 **400**；页面自身零未归因错误 |

截图落在 `/tmp/rm362-evidence/`：`01-login.png`、`02-audit.png`、`02-operations.png`、`02-release-inventory.png`、`02-release-operations.png`、`02-operation-detail.png`。

**#432（definition owner 由 JWT 组织填充）**

- 不带 `actor` 建 definition（`namespace=task362-smoke`、`release=task362-owner-smoke`）→ **200**；DB 里 `owner_organization_id = 2142e4dd-2408-44c0-a247-364a95034fe6`（JWT 组织）、`created_by = 72687459-…`（管理员）。响应体没有 owner 字段，结论以 DB 为准。修复前这里会静默写 `NULL`。
- 带矛盾的 `actor.organization` → **400 invalid_argument**：`actor.organization "00000000-…" does not match the authenticated organization "2142e4dd-…"`，且不落库。
- 存量里 `owner_organization_id` 为空的两条（`acme-web`、`release-fixture`）是修复前的死定义，本次**没有改**。测试对象已用 `DisableReleaseDefinition` 置成 `disabled`（默认列表不再返回），但**它仍留在库里**，是本任务的已知残留。

**回滚预案（本次未执行）**

```bash
kubectl -n release-manager rollout undo deploy/<svc>
kubectl -n release-manager rollout status deploy/<svc> --timeout=300s
# <svc> ∈ api orchestrator auth notifier webhook web
```

- 只回镜像，**不回迁移**。`000035` 的 down 为空，且生产进程从不执行 `RunMigrationsDown`；回退到 `876df9a`/`a6707b6`/`ceda261` 的旧代码后，新列与回填值会被旧读取路径忽略（旧代码只经 `sessions.operator_id` + `operators` join），所以「schema 35 + 旧镜像」预期可运行，但**没有数据级回退**。
- 真要数据级回退只能用 §7 的 dump（`release_manager-20261018T020000Z.dump`，159,481 B）；代价是丢掉 02:00 UTC 之后写入的行。
- 回滚同样受 §8.2.1 的 revision 陷阱约束：`undo` 是再开一个 revision。

**本次边界（未验证，别外推）**

- 没有在线 operator，`#436` 的新写入路径没有实跑；只验证了迁移回填。
- 只做页面级冒烟，没有故障注入（PG/Redis/edge 重启）、没有并发/长连接压测。
- `web` 镜像包含 #437 的 W9/W10/W12/W21，本次只验证页面可加载与数据可见，**没有逐项复验每个 UX 条目**。
- 用 `kubectl set image` 直接改 Deployment，绕过了 kustomize 的 tag 默认值：仓库清单里的 tag 仍是 `:dev`，`kubectl apply -k` 会把它改回去（正式发布走 §2.3 的改 tag + apply）。
- `operator` 镜像构建了但**没有部署**（它跑在客户集群里，本集群没有对应工作负载）；`notification-sink` 同理。

## 9. 可观测

| 面 | 现状 | 证据 |
| --- | --- | --- |
| `/health` | 5 个 Go 服务恒 200（liveness 无失败路径）；web 8087 反代到 orchestrator；edge 只有 `tcpSocket` | 实测 8082/8083/8085/8086/8087/8088 `/health` 均 200 |
| `/readyz` | 有失败语义（数据库/Redis/上游），失败 503 + `{"status":"degraded","checks":{...}}`；web 的同名路径报的是 **orchestrator** 的健康度。**`api` 的 `database` 检查是 TASK-352 补的**：修复前它只报 `noop`，PG 故障时仍 200（见 §10-13） | 实测 6 个端口 `/readyz` 均 200；TASK-352 的 PG 重启窗口内 api `/readyz` **503**（+172 ms 首现、≈5.6 s 恢复，镜像 `release-api:0f76163`，§10-13） |
| `/metrics` | **只有 auth(8085) 与 orchestrator(8083)** 暴露 Prometheus 文本 | 实测：8083、8085 → 200；8082、8086、8088 → **404** |
| web 的 `/metrics` | ⚠️ 返回 **200，但内容是 SPA 的 HTML**（`location /` 的 `try_files … /index.html` 兜底） | 实测 `Content-Type: text/html`、4,666 B |
| 日志 | JSON（slog）写 stderr，`kubectl logs` 直接可读；字段含 `time/level/msg/request_id/procedure/error` | 实测 auth 启动/请求日志 |
| 审计 | `audit_events` 表在 PostgreSQL；控制台 `/audit` 走 `release-api`（`web/nginx.conf:91` → `api:8088`），api 已按 `DATABASE_DRIVER=postgres` 读 PG | 见 §10-1（含 2026-10-18 时点更正）：2026-10-17 修好前默认组织视图为空；2026-10-18 恢复后页面显示 **12/12** 条事件（§9.2） |
| Prometheus / Grafana | **不存在**：`deploy/` 下无 `ServiceMonitor`、无 scrape 注解、无 dashboard、无告警规则 | `grep` 0 命中 |

**建议（当前未做）**：补 Prometheus 抓取配置（至少 auth/orchestrator）、Grafana dashboard、告警规则，以及给 release-api 补 `/metrics` 或明确其不参与抓取。

### 9.1 Prometheus scrape 示例（仅文档，仓库未部署）

**只有 `auth`(8085) 与 `orchestrator`(8083) 暴露 `/metrics`。** 不要给 `api`(8088)、`webhook`(8082)、`notifier`(8086) 加抓取：它们返回 **404**；`web`(8087) 的 `/metrics` 返回 **200 + SPA HTML**（`Content-Type: text/html`），抓了也只会得到解析失败或空指标。这是已记录的坑，不是待修配置。

最小 `scrape_configs`（Prometheus 与这两个 Service 在同一集群内）：

```yaml
scrape_configs:
  - job_name: release-manager
    metrics_path: /metrics
    static_configs:
      - targets:
          - auth.release-manager.svc.cluster.local:8085
          - orchestrator.release-manager.svc.cluster.local:8083
```

用 Pod 注解做服务发现时，只给这两个 Deployment 的 Pod template 加注解（示例为 auth；orchestrator 把端口改成 8083）：

```yaml
spec:
  template:
    metadata:
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8085"
```

```yaml
# Prometheus 侧（Kubernetes SD）
- job_name: release-manager-pods
  kubernetes_sd_configs:
    - role: pod
      namespaces:
        names: [release-manager]
  relabel_configs:
    - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_scrape]
      action: keep
      regex: "true"
    - source_labels: [__meta_kubernetes_pod_ip, __meta_kubernetes_pod_annotation_prometheus_io_port]
      action: replace
      target_label: __address__
      regex: (.+);(.+)
      replacement: ${1}:${2}
```

⚠️ **NetworkPolicy**：`allow-namespace-service-ports` 只放行同 namespace 且带 `app.kubernetes.io/part-of=release-manager` 标签的 Pod 访问 8083/8085。Prometheus 若在别的 namespace，现有策略会拦截；需要单独加一条 ingress 规则（按 Prometheus namespace 或 Pod 选择），再验证 `curl -sS http://auth:8085/metrics | head`。这些注解/策略都**没有**写进 overlay——上面的片段是操作者接线时的起点。

### 9.2 故障注入演练（实测）

> 演练时间：2026-10-18 02:15–02:25 +08（UTC 2026-10-17 18:15–18:24），集群 `rm-prod`。目标是对照 DoD ④「停 Pod / 断 operator 验证自愈或告警」。

⚠️ **本环境没有告警系统**：仓库没有部署 Prometheus / Grafana / Alertmanager，`deploy/` 下没有 `ServiceMonitor`、scrape 注解和告警规则（§10-9）。所以本节验证的是**自愈**；**「告警」没有被验证**，本次也没有产生或观察到任何 alert。

**探针口径**（全节一致，避免把不同粒度混着读）：

- 用户面探针从宿主经 `https://localhost:8443` 连打，每轮依次请求 `GET /`（edge→web 静态 SPA）、`GET /readyz`（edge→web→orchestrator 就绪）、`POST /audit.v1.AuditService/QueryAuditEvents`（无凭据；正常 401，api 不可达时 502）。单端点采样间隔约 **0.22 s**。
- 服务面探针用 `kubectl port-forward svc/<svc>` + `GET /readyz`，6 个服务顺序采样，间隔约 **0.5 s**。
- Pod 状态用 `kubectl get pod -o jsonpath=…` 轮询；有效粒度约 **0.4–0.5 s**（0.25 s 间隔 + 每次 kubectl 往返）。
- 下文的 `000` 表示 curl 在 `--max-time 2` 内没拿到 HTTP 响应（多为 `/readyz` 卡在数据库连接上），**不是** TCP 拒绝；`502` 是 edge→web 的 nginx 找不到 api upstream。
- 时刻均为 **UTC**（括号内为 +08）。

#### 9.2.1 删单个 `api` Pod（Deployment，1 副本）

```bash
kubectl -n release-manager delete pod <api-pod>   # 默认 grace，非 SIGKILL
kubectl -n release-manager get pod -l app.kubernetes.io/name=api -o wide
```

| 轮次 | 删除时刻 | 新 Pod 就绪 | 删除→Ready | 期间 `/audit` 失败 | `/`、`/readyz` 失败 |
| --- | --- | --- | --- | --- | --- |
| A | 18:15:56.018 | 18:16:01.714（`api-85869d7f74-px7fc`） | **5.696 s**（容器首启失败 1 次后重启） | 15/43 次 502：首次 18:15:56.202，最后 18:16:01.441，首个 401 18:16:01.674 | 0 |
| B | 18:20:55.941 | 18:21:22.167（`api-85869d7f74-rlmxz`） | **26.226 s**（容器重启 2 次，事件 `BackOff x4`） | 60/84 次 502：首次 18:20:56.177，最后 18:21:20.889，首个 401 18:21:22.146 | 0 |

- Pod 事件：旧 Pod 只有 `Killing`；新 Pod 同一秒内 `SuccessfulCreate` → `Scheduled` → `Pulled`（镜像已在节点）→ `Created` → `Started`。B 轮另有 `BackOff`（`spec.containers{api}`），见下条。
- **自愈成立，但单副本有代价**：`/`（静态页）与经 edge 的 `/readyz` 全程 200（A 43/43、B 84/84 采样），用户感知是「审计页请求失败」，不是「控制台打不开」。`/audit` 没有副本接管，api 不在就 502；两次的不可用窗口（首坏→首好）约 **5.5 s / 26.0 s**。
- ⚠️ **异常（未定位，必须记录）**：两次删除中，新 api 容器**首次启动**都以 `failed to register service … ping PostgreSQL: … connection refused` 退出（exit 1），被 kubelet 重启后才成功（A 1 次、B 2 次）。同一时刻用同 namespace、同 `app.kubernetes.io/part-of=release-manager` 标签启动的探针 Pod **可立即连上** PostgreSQL（18:22:05 UTC、18:23:04 UTC 两次实测，首次尝试即成功），所以**不是** PG 不可用；根因未在本任务定位（怀疑与网络策略/连接建立的时序有关，但未取证）。结论：**api 的 Pod 级 RTO 不能按「镜像已在节点上」估算**，实测有 5.7 s 与 26.2 s 两档；复验若看到 `BackOff`，先看 `kubectl logs <pod> --previous` 里是否有这条 ping 错误。
  **时点更新（2026-10-18 03:00 +08）**：本条「根因未定位」已被 **TASK-351** 推翻并修复——根因是 kube-router 的策略源 ipset 尚未写入新 Pod 地址（**不是** PG 不可用，探针 Pod 也从没踩进那个窗口），修法与修复前后 RTO 见 §9.2.1.1。

#### 9.2.1.1 根因与修复：新 Pod 首启连 PG 被拒（TASK-351，2026-10-18 02:52–03:06 +08 复测）

**结论**：不是 PostgreSQL 宕机，也不是 NetworkPolicy 配得过严，而是**新 Pod 的地址还没进入 kube-router 的策略源 ipset，容器就已经开始连库**。表现是「新 Pod 首启撞一次 `connection refused` → `exit 1` → CrashLoopBackOff 重启」，往下一层是 `REJECT --reject-with icmp-port-unreachable`。

取证（每条均可复验）：

1. **拒绝发生在 `postgres` Pod 的 NetworkPolicy 链上，不在数据库**。在节点上 `iptables-save` 可看到 `postgres-0` 的 per-Pod 链在未命中任何 allow 规则后落到：
   ```text
   -A KUBE-POD-FW-XDAFXEN63UPBNKZY … -m mark ! --mark 0x10000/0x10000 -j REJECT --reject-with icmp-port-unreachable
   ```
   `icmp-port-unreachable` 在客户端就是 `connect: connection refused`（**不是**超时，所以 `--max-time` 类重试不会兜住它）。放行规则 `allow-postgres` 同时匹配目标集 `KUBE-DST-…`（`postgres` Pod 自己的 IP）和**源**集 `KUBE-SRC-CXUVBLCR4AU3QSVB`（带 `app.kubernetes.io/part-of=release-manager` 的 Pod IP 集合）。

2. **源集的成员来自 apiserver 里 Pod 对象的 `status.podIP`，而 kubelet 是异步上报的**；容器在 sandbox 就绪后立刻启动，此时 Pod 对象可能还没有 IP，kube-router 无从把地址加进集合。用一个「启动后立刻连库」的探针 Pod（同 namespace、同 `part-of` 标签、镜像 `postgres:16`）复现，首次连接被拒、集合加入其 IP 后立刻成功：

   | 事件 | 时刻（UTC 2026-10-17） |
   | --- | --- |
   | 容器内首次 `pg_isready` | `18:52:19.600` → **REFUSED** |
   | apiserver 的 Pod 对象出现 `status.podIP` | `18:52:19.652` |
   | 该 IP 进入 `KUBE-SRC-…` | `18:52:19.658` |
   | 下一次 `pg_isready` | `18:52:19.669` → **OK** |

   实测窗口 **60–800 ms**（多轮不同），足以让「启动即 ping」的容器必踩一次。

3. **TASK-350 里「同刻探针可立即连上」不构成 PG 健康的证据**：那条连接是在 Pod 已经 Running 之后用 `kubectl run`/`kubectl exec` 发起的，等到它真正拨号时集合早已更新——探针从未踩进那个窗口。§9.2.1 的「排除 PG 宕机」结论仍然成立，但理由要换成这条。

4. **同一个 bug 影响所有「启动即连库」的服务**：`auth`、`orchestrator`、`notifier` 的 `--previous` 日志里是同一条错误（2026-10-17 10:57:15 UTC，即本次部署时），三者当前 `restartCount=2`（重启次数含该次，但两次的成因没有逐个取证）。

**修法**：`internal/postgres/db.go` 的 `Open` 改为调用 `internal/postgres/connect.go` 的**有界等待**——最多 5 次、指数退避 1 s→8 s（±20% jitter）、总预算 20 s、单次 ping 5 s 超时；**只重试传输层错误**（refused/reset/unreachable/DNS/单次超时），服务端已经应答的错误（`28P01` 密码错、`3D000` 库不存在等）**立即失败不重试**；预算耗尽仍不可达则照旧 `exit 1`，错误串仍是 `connection_unavailable: ping PostgreSQL: … after N attempts`。日志区分「重试中」与「最终失败」：
```text
WARN  PostgreSQL not reachable yet, retrying  attempt=1 of=5 retry_in=1.105555444s error="… connect: connection refused"
INFO  PostgreSQL answered after startup retry  attempt=2
ERROR PostgreSQL still unreachable after startup retries  attempts=5 error="…"
```
（第三行是耗尽分支，本次真机未触发，由 `internal/postgres/connect_test.go` 覆盖。）

**修复前后对比**（同一集群 `rm-prod`、同一方法：`kubectl delete pod` → 新 Pod `Ready`；UTC 2026-10-17）：

| 轮次 | 镜像 | 删除时刻 | 新 Pod Ready | RTO | 容器重启 | 首启是否撞 refused |
| --- | --- | --- | --- | --- | --- | --- |
| A1 | 修复前 `b618062` | 18:52:36.164 | 18:52:41.799 | **5.635 s** | 1 | 是 |
| A2 | 修复前 | 18:52:43.810 | 18:52:49.719 | **5.909 s** | 1 | 是 |
| A3 | 修复前 | 18:52:51.962 | 18:53:17.827 | **25.866 s** | 2 | 是 |
| A4 | 修复前 | 18:53:22.778 | 18:53:28.740 | **5.962 s** | 1 | 是 |
| F1 | 修复后 | 18:57:17.990 | 18:57:24.090 | **6.100 s** | **0** | 是（第 2 次尝试成功） |
| F2 | 修复后 | 18:57:26.457 | 18:57:32.072 | **5.615 s** | **0** | 是（第 2 次尝试成功） |
| F3 | 修复后 | 18:57:34.483 | 18:57:40.132 | **5.649 s** | **0** | 是（第 2 次尝试成功） |
| F4 | **交付镜像 `876df9a`** | 19:05:07.063 | 19:05:12.817 | **5.754 s** | **0** | 是（第 2 次尝试成功） |
| F5 | 交付镜像（镜像重导入后复验） | 19:06:11.626 | 19:06:17.875 | **6.250 s** | **0** | 未记录 |

F4/F5 是**交付镜像**上的补测（UTC 19:05–19:06 ≡ +08 03:05–03:06）：F4 用交付镜像 `876df9a` 原样删 Pod（首启撞 `refused`、第 2 次尝试成功，与 F1–F3 同），F5 把同一镜像重导入后再做一遍；两轮 `RESTARTS` 均为 **0**。F5 只记了 RTO 与重启次数，没有保留「首启是否撞 `refused`」的取证，故该列记 `未记录`。

F1 轮把失败时刻与集合更新对齐：首 ping 被拒 `18:57:18.469` → 新 IP `10.42.0.108` 进入 `KUBE-SRC-…` `18:57:19.114` → 退避后第 2 次 ping 成功 `18:57:19.577`。

**读法**：竞态**没有消失**（它属于策略面数据同步，任何 CNI 都有），消失的是「竞态 → 容器重启 → 退避」这条放大链。修复后 **5/5 轮 RTO 稳定在约 5.6–6.3 s**（5 轮实测 **5.615–6.250 s**）、**0 次容器重启**，**26 s 那一档不再出现**；剩余 ~5 s 主要是 `readinessProbe.periodSeconds: 5` 的探测相位，不是连库等待。修复前 25.9 s 那轮 = 两次失败 + kubelet 重启退避。

**为什么不改 PG/CNI/策略**：把 `allow-postgres` 放宽或换选择器都不能消除「新 Pod 地址尚未入集」这个窗口，只会削弱策略；正确做法是把「启动顺序」在应用侧等掉。**未做/风险**：等待参数目前是常量（未做成配置项）；`auth`/`orchestrator`/`notifier` 只按同一路径受益，未做各自的真机删 Pod 复测；**坏 DSN/解析不到的主机名会多等约 20 s 才失败**（此前立即失败），这是换取启动窗口的代价；单次 ping 5 s 超时之外的「黑洞路由」场景未实测。

#### 9.2.2 删 `web` 的一个 Pod（Deployment，2 副本）

```bash
kubectl -n release-manager get endpointslices -l kubernetes.io/service-name=web   # 删除前: 10.42.0.86, 10.42.0.87
kubectl -n release-manager delete pod web-66cfd65774-57lvw
```

- 删除时刻 **18:16:36.945**；`web-66cfd65774-57lvw` 进入 `Killing`，幸存副本 `web-66cfd65774-zx666` 保持 Ready；替换 Pod `web-66cfd65774-7bwtt` 于 **18:16:40.808** Ready（**3.863 s**）。
- 三路并发探针在 **10.04 s** 窗口内共采样 **399 次**（每端点 133 次，约 13.3 req/s/端点）：**0 失败**（`/`=200、`/readyz`=200、`/audit`=401 全为预期）。
- 口径边界：探针都是新建连接；**没有**度量在途请求/长连接是否被切断（`web` 是 nginx，关闭时 graceful shutdown）。不要把「0 失败」外推成「任何时刻零影响」。

#### 9.2.3 重启 PostgreSQL 主 Pod（StatefulSet，单副本）

```bash
kubectl -n release-manager delete pod postgres-0
kubectl -n release-manager exec postgres-0 -- psql -U release_manager -d release_manager -Atc \
  "select version,dirty from schema_migrations"
```

连续做了 3 次（时刻 UTC）：

| 轮次 | 删除时刻 | PostgreSQL Ready | 删除→PG Ready | 6 个 `/readyz` 全 200 | 数据校验 |
| --- | --- | --- | --- | --- | --- |
| A | 18:17:23.747 | 18:17:30.864 | **7.117 s** | **+7.174 s** | version=34 / dirty=false；`audit_events=12`；`sessions=3`；`release_inventory=2` |
| B | 18:18:22.462 | 18:18:28.995 | **6.533 s** | **+6.581 s** | 同上；且 4 张关键表（`schema_migrations`/`audit_events`/`release_inventory`/`sessions`）的内容哈希（逐行 `t::text` 排序后取 md5）前后完全一致 |
| C | 18:19:01.664 | 18:19:08.163 | **6.499 s** | **+7.233 s** | 同上；`audit_events` 12 行未变 |

**期间各服务 `/readyz` 的变化**（A 轮，B/C 同型）：

| 服务 | 首个非 200 | 恢复 200 | 降级响应 |
| --- | --- | --- | --- |
| `auth` | +0.06 s | +7.68 s | 503，`checks.database` = `connection refused` |
| `orchestrator` | +0.06 s | +7.68 s | 503，`checks.database` = `connection refused` |
| `notifier` | +0.07 s | +7.15 s | 503，`checks.database` = `connection refused` |
| `webhook` | +0.07 s | +7.15 s | 503（`orchestrator … 503`）；部分采样 000（>2 s 超时） |
| `web` | +0.08 s | +7.15 s | 503/000：`web` 的 `/readyz` 代理的是 orchestrator 的健康度 |
| **`api`** | **无变化** | — | **全程 200 `{"checks":{"noop":"ok"}}`：api 的 `/readyz` 不检查数据库**（`cmd/api/main.go` 未实现 `ReadinessChecks`） |

- **RTO ≈ 6.5–7.2 s**：PG 容器 Pending→Running 约 **1.1 s**，Running→Ready 约 **5.0 s**（readinessProbe `initialDelaySeconds: 5`、`periodSeconds: 5`），其余是服务重连。单副本 PG 没有故障转移，这段时间是**真实不可用窗口**。
- **RPO = 0（仅限本次口径）**：这是**优雅删除**（默认 30 s grace、SIGTERM），不是 `kill -9`/节点掉电；B 轮前后 4 张关键表的行数与内容哈希完全相同。没有 WAL 归档/异地副本，节点级故障的 RPO 仍按 §7.4/§10-9 的「≤24 h + 同盘」口径，**不要**用本节外推。
- **用户可见影响**（连续探针）：
  - A 轮：`GET /` 58/58 次 200（控制台静态页可打开）；edge `/readyz` 从 18:17:23.962 起出现 503/000（两路探针各 2 次 503 + 3 次 000），18:17:31.096 恢复 200。
  - B 轮：数据库不可用期间，**登录返回 `401 invalid credentials`**（不是 503；`internal/auth/browser_session.go:23` 把存储错误折叠成凭据错误，用户会看到「密码错误」），已登录会话调用 `ListCustomers` 返回 `401 session revoked`，调用 `/audit` 返回 `500 internal error`。恢复后 `login=200`、`ListCustomers=200`、`/audit=200`。
  - C 轮（0.5 s 粒度连续采样）：已登录 `/audit` 从 **+0.29 s** 起连续 500，最后一次 500 在 **+5.81 s**，**+6.52 s** 恢复 200；`GET /` 25/25 次 200、0 失败。
  - ⚠️ B 轮里 `/audit` 在 **PG Ready 后约 2.8 s** 仍返回 500（同一时刻 auth 日志出现 `authorization policy unavailable`），紧接着的 `ListCustomers` 已 200。认证读路径的恢复受 auth 的 **5 s 策略刷新**相位影响，**RTO 不是「PG Ready 即结束」**。
- 恢复后浏览器复验（真实 Chrome，TASK-350 会话）：登录用时 **666 ms**，`/audit` RPC **200**，页面显示 **12/12** 条事件。

#### 9.2.4 无 operator 时的控制面表现（**不是**「断连」演练）

现场已没有客户集群：`k3d cluster list` 只剩 `rm-prod`，`rm-cust` 的 k3d 集群与 kubeconfig context 都已删除。控制面的 `operator` Service 指向 orchestrator 的 8084（客户集群 agent 的 mTLS 接入点），本 namespace 没有 operator 工作负载。因此**无法在本环境模拟「operator 断连」**；这里只验证**控制面在没有 operator 时是否稳定、是否保留状态**：

只读实测（18:20:16–18:20:28 UTC，真实 Chrome + 下列 RPC）：

| 读取 | 结果 |
| --- | --- |
| `sessions` 表 | 3 行，**全部 `offline`**（`session_replaced` / `heartbeat_timeout`）；这是演示集群删除后的历史状态，不是运行时断连 |
| `release_inventory` 表 | **2 行保留**（`rm-cust` 的 `e2e-plan` / `e2e-release`） |
| `ListCustomers` | 200，2 个客户（`RM Cust Demo`、`Acme Weekday`） |
| `ListClusters` | 200，`rm-cust` 仍为 `CLUSTER_STATUS_ACTIVE` |
| `ListOperators` | 200，1 条：lifecycle `ACTIVE`、session `OFFLINE`、reason `heartbeat_timeout`、lastHeartbeat `2026-10-17T17:37:52Z` |
| `ListReleaseInventory` | 200，2 行 |
| 页面 | `/customers`、客户详情、`/customers/<id>/clusters`、集群详情、`.../operators`、`/audit` 全部渲染；无 4xx/5xx（登录前的 `ValidateToken`/`RefreshToken` 401 是 SPA 引导的预期行为） |

结论：**没有 operator 时控制面不会报错或丢状态**，operator 页面会如实显示「离线 / 心跳超时」。但这只是「状态读取」；**没有**验证断连瞬间的在途 `CommandStream`、重连退避或 operator 侧自愈——这些要重新接入一个客户集群（§12）才能测，**本次没有做，也不要用本节数据声称「operator 断连已验证」**。

#### 9.2.5 未做项

- **中断进行中的备份 Job**：跳过。演练时 `CronJob/release-manager-backup` 的 `LAST SCHEDULE` 仍是 `<none>`（`0 2 * * *` 为 UTC，节点时间也是 UTC，部署后尚未到点），没有 Job 可中断；这一项对自愈的增量信息很小，且会额外制造一次不完整的备份。
- **Redis 主 Pod**：未注入。Redis 单副本且无 AUTH（§10-10），删它会让依赖缓存/限流的服务短暂降级；不在本任务四项范围内。
- **节点/磁盘故障、`kill -9`、多节点**：未做。本集群是单节点 k3d + `local-path`，节点丢失属于另一类演练。

#### 9.2.6 诚实边界（不要外推）

1. **「告警」未被验证**：本环境没有告警系统（§10-9），只有 Pod 重建、`/readyz` 降级和日志错误；本次没有产生也没有观察到任何 alert。
2. **单副本 = 真实窗口**：api 删除时 `/audit` 中断约 5.5 s / 26.0 s；PG 重启时 DB 不可用 6.5–7.2 s，认证读路径还要叠加最多一个 5 s 的策略刷新相位。不要写「零中断」。
3. **api 的 `/readyz` 不反映数据库**：PG 宕机时 api 仍 200。用它判断 DB 健康会误判；DB 健康看 auth/orchestrator/notifier 的 `/readyz` 或直连 `pg_isready`。
4. **api 新 Pod 首启有 `connection refused` 风险**：2/2 次删除都出现，1–2 次容器重启，根因未定位（同刻探针 Pod 可连 PG，排除 PG 宕机）。这是当前最值得单独排查的自愈延迟来源。
5. **operator 断连未真正测试**：没有客户集群，演示集群是之前被删除的；本节只验证控制面在「无 operator」时的读取稳定性。
6. **探针口径**：`/`、`/readyz`、`/audit` 都是新连接、短请求；长连接/在途请求、浏览器渲染耗时、客户端重试未覆盖。`0 失败` 只在这组口径下成立。
7. **只做了 Pod 级故障**：没有做节点 drain、镜像拉取失败、PVC 不可用、PG 崩溃恢复（crash recovery）等场景。

## 10. 已知限制

逐条都是实测或读码确认，**不是猜测**。排障时不要假设这些能力存在。

1. **`release-api` 已接 PostgreSQL，但控制台 `/audit` 默认组织视图在 2026-10-17 时点为空（数据范围问题，不是引擎问题；2026-10-18 时点更正见本段末）**：生产 overlay 已为 `api` 注入 `DATABASE_DRIVER=postgres` 与 `DATABASE_DSN`（Secret `release-manager-credentials`；`deploy/kustomize/production/kustomization.yaml`，TASK-338），`--db /data/api.db` 与 PVC 保留（审计归档目录 `/data/archives` 仍需持久卷）。实测（2026-10-17，TASK-338 会话，演练时的 app 版本：当时 api 为 `release-api:3ecf69b`；当前为 `ceda261`，见 §1）：api 启动日志为 `{"msg":"store opened","driver":"postgres"}`；PG `audit_events` 仍 **2 行**且 `organization_id` 全部为 `system`（orchestrator GC），按控制台管理员所属组织过滤为 **0 行**；同一登录会话直连 `POST /audit.v1.AuditService/QueryAuditEvents` 时，`filter.organization_id="system"`（platform_admin 跨组织）返回 **2 条**（这 2 行由 orchestrator 直接写入 PG），证明 api 读的是 PG；空 filter 与显式登录组织均返回 **0 条**，浏览器 `/audit` 响应零事件并渲染「暂无审计事件」。结论：**引擎选择缺口已关闭**；`/audit` 仍空是因为 PG 里只有 `system` 范围的系统事件，而控制台按登录组织过滤（`web/src/pages/AuditPage.vue:29`）。要看到这些事件需要产品决策（给 platform_admin 提供全组织/系统范围查询，或把系统事件写入可查询的组织范围）；在此之前不要删 `release-manager-api-data` PVC，也不要把「/audit 空」再归因于 SQLite。

   > 时点更正（2026-10-18）：TASK-338 修好 api→PG 后，TASK-349 演练期间已产生 12 条事件，platform_admin 在全组织视图可见（见 §9.2）；「默认组织视图为空」仅描述 2026-10-17 修好前/无事件时的状态。
2. **探针放行依赖 k3s 版本与 Pod CIDR**：`allow-kubelet-probes` 解决了 issue #10030，但 k3s **< v1.28.9 / < v1.29.4** 仍会拦掉探针；换 CIDR 必须改 `production-settings.yaml` 的 `nodeCidr`。本集群 v1.31.5 实测无问题。（⚠️ 这条规则所在的那套策略在本集群是否被**真正执行**，见第 12 条——「探针能通」本身不能证明策略生效。）
3. **授权策略热重载窗口约 5 s**：auth 的 `authorization.policy_reload_interval: 5s`（`deploy/kustomize/production/configs/auth.production.yaml`）。首次 `Initialize` 后 5 s 内，控制台可能被弹到 `/forbidden`——刷新即可，不是权限配错。
4. **edge 单副本 + 自签证书**：浏览器需要一次性信任例外；edge 重启期间控制台不可用（后端仍在跑）。且 edge 的 `server_name _` 不区分主机名。
5. **宿主 8443/8444 已收窄到 `127.0.0.1`（2026-10-18 12:28 +08 实测）**：`docker port` 当前只有 `30443/tcp -> 127.0.0.1:8443` 与 `30844/tcp -> 127.0.0.1:8444`，**无** `0.0.0.0`/`[::]`；从宿主非 loopback 地址 `10.10.33.122` 访问 `https://10.10.33.122:8443` 与 `:8444` 均连接失败（curl 退出码非 0，`http_code=000`），loopback 上仍是 `200`/`401`。收窄用 `k3d node edit --port-delete` 先删 `0.0.0.0` 映射，再 `--port-add "127.0.0.1:..."`；两次 `node edit` 各替换一次 serverlb 容器，**实测 UI 与网关同时中断 27.32 s**（12:27:39.054–12:28:06.378，200 ms 探针）。⚠️ **`--port-delete` 需要 k3d ≥ v5.9.0**：本机 `/usr/bin/k3d` 是 v5.8.3（`node edit --help` 只有 `--port-add`），且 v5.8.3 的 `--port-add "127.0.0.1:8443:30443"` 会被 `util.IsPortBindingEqual` 判重跳过（`127.0.0.1`/`0.0.0.0`/`""` 视为同一 HostIP；源码 `pkg/util/ports.go` + `pkg/client/node.go`），所以**旧版「把映射改成 `127.0.0.1:...`」的建议对已建集群不生效**，本条已更正。**按需对外开放**：前置反代（只监听受信网段）或宿主防火墙放行来源，不要改回 `0.0.0.0`；确需回滚到对外绑定的命令、暴露面警告与停机口径见 §4-⑥；8444 还有 mTLS 兜底——网络可达不等于通过认证，没有控制面 CA 签发的 client cert 时请求层返回 `401 client certificate required`（§4-⑦）。同一 `docker port` 里的 `6443/tcp -> 0.0.0.0:44609`（k3s API）**未收窄**，不在本次范围。
6. **无 Vault CA 供给**：ADR-017/ADR-020 的 Vault 路径未接线，CA 与令牌都是静态 Secret（notifier 的 `vault.enabled: false`）。轮换靠手工替换 Secret。
7. **无 HA / 多副本**：只有 `web` 是 2 副本；api/auth/orchestrator/notifier/webhook 单副本且 PDB `minAvailable: 1` ⇒ `kubectl drain` 会在这些 Pod 上**永久阻塞**（这是刻意设计，别删 PDB 绕过）。也没有多节点/反亲和/跨可用区。
8. **无镜像签名、无 CI 推送镜像**：`.github/workflows/` 里没有 build/push 这 6 个镜像的 job（`sync-to-gitcode.yaml` 只镜像 git 仓库），镜像靠本机构建 + `k3d image import`；`make docker-push-all` 只在你已运行的 registry 上手工执行。没有 cosign/attestation。
9. **无 schema 降级通道、无 Prometheus/Grafana；自动备份已做但不异地**：自动逻辑备份见 §7.4（每日一次、保留 7 天），但没有 WAL 归档 ⇒ RPO ≤ 24 h，且备份与库同节点同盘 ⇒ 节点丢失会一起丢；异地拷贝仍需操作者补。Prometheus/Grafana 仍不存在，抓取示例见 §9.1（未部署）；故障注入下的自愈实测见 §9.2（告警未验证）。schema 仍只能前滚（§8.1）。
10. **Redis 无 AUTH**：只靠 NetworkPolicy 限制为同 namespace 可达（`allow-redis`）。Redis 一旦离开这个边界就必须补 AUTH。
11. **交付的 `rm-prod` 里没有客户集群**：TASK-347 已把宿主 `8444 → Service operator NodePort 30844 → orchestrator:8084` 的 TCP 通路打通（TLS 握手与请求层 401 实测见 §4-⑦），但本环境没有 customer cluster/agent，所以 enroll、CommandStream、rollback / 已安装 Release / operator 在线状态这类旅程**默认没有数据可测**，`sessions` 表没有在线会话（本次实测 3 行均为 `offline` 历史行）是预期，不是故障。接入一个客户集群的完整路径（网关可达性、SAN/`hostAliases` 约束、2 个 Secret、plan→apply 实测）见 §12；演示集群是一次性的，删除后控制面**保留**它的 customer/cluster/operator/inventory 行（没有删除 RPC），会话转 `offline`、inventory 停止更新；无 operator 时的控制面只读实测见 §9.2.4。证书 SAN 约束见 §4-⑦ 与 §12.2。
12. **NetworkPolicy 的强制执行在本集群上未证实**：`kubectl get ds -A` 只列出 `svclb-traefik-*`，**没有任何 CNI DaemonSet**（2026-10-18 01:16 +08 采样）。k3s 用内嵌 flannel，并把网络策略控制器编进 `k3s server` 进程（kube-router），所以没有独立 DaemonSet 可查；**从集群外部无法判断该控制器是否启用、策略是否真的被下到数据面**。结论：本 overlay 的 **9 条 NetworkPolicy 是声明，不是已证实的防护**，「`default-deny-ingress` 是否真的挡住东西」**没有验证过**。第 2 条记的「本集群 v1.31.5 探针放行无问题」同样有两种解释——进程内 kube-router + k3s issue #10030 已修复，或策略根本没被执行——现有证据不能区分。把它当安全边界之前：换到**明确强制执行 NP 的 CNI**（Calico/Cilium 等），并做一次**负向验证**（从一个本该被拒的源发起连接，确认被拒）；本集群上不要据此宣称「已隔离」。
13. **`api` 的 `/readyz` 曾完全不查数据库（TASK-352 已修）**：修复前 `apiSvc` 没实现 `app.Run` 的 `readinessContributor`，`/readyz` 只剩预置的 `noop`，实测 `{"checks":{"noop":"ok"},"status":"ok"}`；PG 故障期间 k8s 仍判 Ready、流量继续打到连不上库的 api 上，用户看到 502（TASK-350 故障注入里 api 是唯一全程 200 的服务）。现在 `apiSvc.ReadinessChecks()` 用引擎连接 ping（Postgres `SQLDB().PingContext` / SQLite `DB().PingContext`，2 s 超时，与 auth/orchestrator 同规格）暴露 `database` 检查，失败 503；`/health` 仍是纯 liveness，语义不变。单测 `TestRegisterWiresStorePing` 走的是 SQLite 分支（`dbPath` + 空 driver），Postgres 分支的 `SQLDB().PingContext` 没有单测——它的证据就是下面这次真机重启窗口。
    实测（2026-10-18 02:39:54.894 +08 重启 `postgres-0`，镜像 `release-api:0f76163`（与本提交 `0f76163..b618062` 的生产代码逐字节相同：`git diff` 仅涉及测试文件与文档））：**+172 ms 首次 `/readyz` 503**（`database: driver: bad connection`，随后 `connection refused`），最后 503 在 02:40:00.462，**02:40:00.673 恢复 200**，降级窗口 **≈5.6 s**；api Pod 全程未重启（`RESTARTS` 不变），PG 恢复后 `schema_migrations` 仍为 `34|false`、`audit_events=12`、`users=2`、`public` 表 58 张，与注入前一致。
    ⚠️ **边界**：readiness probe 是 `periodSeconds: 5` + `failureThreshold: 3`，所以需要 **3 次连续失败**，摘流延迟约 **10–15 s（最坏 15 s）**（故障恰在探针周期后发生 ⇒ ≈10 s；恰在上次成功探测后发生 ⇒ ≈15 s）。更短的抖动里 `/readyz` 已 503（这是本次修复带来的可观测信号），但摘流尚未发生，用户仍可能看到少量 502；要缩短摘流延迟就调这两个探针参数，**不要动 liveness**。

## 11. 验收清单（本次实测结果）

复验时按表逐条对照；命令都在本机跑过。

| 检查 | 实测结果 | 复验命令 |
| --- | --- | --- |
| Pod 就绪 | **10/10 Ready**（api/auth/orchestrator/notifier/webhook ×1、web ×2、edge ×1、postgres-0、redis-0） | `kubectl -n release-manager get pods` |
| PVC | **5/5 `Bound`**，容量 10Gi/5Gi/1Gi/1Gi/5Gi，`local-path` | `kubectl -n release-manager get pvc` |
| 自动备份 | `CronJob/release-manager-backup` 存在且立即触发成功：5 s 完成，`release_manager` **133,173 B** + `release_notifier` **4,592 B** | §7.4 的三条命令；`kubectl -n release-manager get cronjob,pvc` |
| schema | `34`，`dirty=f` | `kubectl -n release-manager exec postgres-0 -- psql -U release_manager -d release_manager -Atc "select version,dirty from schema_migrations"` |
| 入口 | `https://localhost:8443/` → 200；`GetInitStatus` → `{"initialized":true}` | §6 的两条 curl |
| operator 网关 | 宿主 `127.0.0.1:8444` TCP 可达；TLSv1.3 握手且证书链校验 `Verification: OK`；无 client cert 时请求层 `401 {"code":"unauthenticated","message":"client certificate required"}` | §4-⑦ 的 `bash /dev/tcp`、`openssl s_client`、`curl` |
| 登录 | `Set-Cookie` 三个（`rm_access`/`rm_refresh`/`rm_csrf`），登录后 **653 ms** 离开 `/login`，刷新保持会话 | 浏览器访问 `https://localhost:8443`，用 `Initialize` 时创建的管理员账号登录 |
| 审计页 | `/audit` HTTP 200，列表 **12 行**（2026-10-18 恢复后复验，见 §9.2；2026-10-17 修好前为 0 行，见 §10-1 时点更正；**不是** 500/404） | 浏览器打开 Audit 页面；或按 `api/kulala/audit.http` 的 `QueryAuditEvents` 请求复验 |
| 停机/重启 | `k3d cluster stop/start` 约 **57 s**，数据完好 | §8.3 |
| 升级/回滚 | web `ceda261 → b96d964 → ceda261`；升级 **8 s**、回滚 **8 s**；三次都是 10/10 Pod、6 个 `/readyz` 200、登录 11/11（672/651/649 ms）、DB 行数 `12 / 2 / 58` 与 `schema 34` 不变 | §8.1.1、§8.2.1 |
| 备份/恢复 | 手工：dump 93,201 B / 58 表；自动：133,173 B + 4,592 B → restore `rm_backup_check` → `34|false`、58 表/146 索引/关键表行数全部与源库一致 | §7.1–§7.4 |
| 镜像门禁 | 未改 tag 时 `make check-production-images` FAIL 并打印 6 行 `:dev`；改后 PASS | §2.3 |

## 12. 客户集群接入（operator enroll）

本节是**拿第二个 k3d 集群当客户集群**接入 `rm-prod` 的最小可行路径，2026-10-18 在本机实测走通：
enroll → 会话 → `release_inventory`（含 000024 身份四列 + 000030 观测投影）→ plan（预检）→ apply → observe。
命令里的 `<...>` 是要替换的占位符。

### 12.1 网关必须先「到得了」

入口（宿主 `8444` → NodePort `30844` → `orchestrator:8084` 的 mTLS 网关、为什么不能走明文反代）见 §4-⑦；
客户集群侧需用 `hostAliases` 把 SAN 名指到宿主，见 §12.2。

> ⚠️ 2026-10-18 12:28 起宿主 8444 只绑 `127.0.0.1` ⇒ **客户集群（包括下面的 `host.k3d.internal` 路径）连不上**。执行本节前先按 §10-5 用反代/防火墙把网关暴露给客户集群所在网段。§12.5 记录的实测是在 8444 仍绑 `0.0.0.0` 时做的（2026-10-18 当天早些时候），不代表当前绑定。

### 12.2 SAN 约束：必须用 `hostAliases`（最容易踩的坑）

网关服务端证书的 SAN 是**硬编码**的，没有配置项、也不含任何 IP（`cmd/orchestrator/main.go:173-176`）：

```go
serverCertPEM, serverKeyPEM, err := caInst.SignServerCert([]string{
    "operator-gateway.dev.release-manager.local",
    "localhost",
})
```

⇒ `--orchestrator-addr https://<宿主 IP>:<端口>` 一定 `hostname mismatch`，只能用 SAN 名访问。
客户集群里没有这条 DNS 记录，用 **`hostAliases`** 把 SAN 名指到宿主网关（k3d 里是 `host.k3d.internal`）：

```yaml
      hostAliases:
        - ip: 172.24.0.1        # 实测：k3d 容器网络里 host.k3d.internal 的地址
          hostnames:
            - operator-gateway.dev.release-manager.local
```

`172.24.0.1` 是**本机** k3d 容器网络里 `host.k3d.internal` 的地址；换环境（不同的 docker 网络/网段）必须
按自己的 docker 网络替换这一行，不要照抄这个 IP。

- **方案 A（`hostAliases`，本任务采用）**：零代码改动、保留证书主机名校验；代价是每个客户集群写一条
  hostAliases，且控制面地址被钉死在这个 SAN 名上。`deploy/kustomize/customer-agent/base/deployment.yaml:33-43`
  已有同形状的占位块（dev 用 `172.18.0.2/3`），改 IP 行即可。
- **方案 B（扩展 SAN）**：给 `SignServerCert` 的 hostname 列表加配置项 / 加节点 IP。需要改代码 + 重签服务端
  证书 + 所有客户集群改地址。**本任务没有做**。

前置校验（**在客户集群里**发起，不要从宿主）：

```bash
kubectl -n release-manager-customer run nettest --image=busybox:1.36.1 --restart=Never --command \
  -- sh -c 'nc -zv -w3 host.k3d.internal 8444; sleep 3600'
kubectl -n release-manager-customer logs nettest
# 2026-10-18 早些时候（8444 仍绑 0.0.0.0）实测 "host.k3d.internal (172.24.0.1:8444) open"；
# 8444 收窄到 loopback 后直接跑会 connection refused，先按 §12.1 暴露网关。
```

控制面证书链校验：

```bash
kubectl --context k3d-rm-prod -n release-manager get secret release-manager-mtls-ca \
  -o jsonpath='{.data.ca\.crt}' | base64 -d > /tmp/gateway-ca.crt
openssl x509 -in /tmp/gateway-ca.crt -noout -subject -dates   # 期望 CN=release-manager-agent-ca
echo | openssl s_client -CAfile /tmp/gateway-ca.crt \
  -verify_hostname operator-gateway.dev.release-manager.local -connect 127.0.0.1:8444 2>&1 \
  | grep 'Verify return code'                                 # 期望 0 (ok)
```

### 12.3 最小 Demo 步骤

**1) 控制面：客户 + 集群 + 一次性 token。** UI 路径是「客户 → 新建客户 / 新建集群 → 集群 Operator 页 → 生成令牌」；
RPC 等价（Connect JSON + `Authorization: Bearer`，写操作还要 `Idempotency-Key`）：

```bash
curl -k -sS -X POST https://localhost:8443/orchestrator.v1.OrchestratorService/CreateCustomer \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"RM Cust Demo","slug":"rm-cust-demo"}'
curl -k -sS -X POST https://localhost:8443/orchestrator.v1.OrchestratorService/CreateCluster \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"id":"rm-cust","name":"rm-cust","customerId":"<CUSTOMER_ID>"}'
curl -k -sS -X POST https://localhost:8443/orchestrator.v1.OrchestratorService/CreateEnrollmentToken \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"customerId":"<CUSTOMER_ID>","clusterId":"rm-cust","operatorName":"rm-cust","ttlMinutes":120}'
```

`CreateEnrollmentToken` 只存 SHA-256（`internal/orchestrator/enrollment.go`），明文 token **只返回一次**。
响应里的 `installCommandTemplate` 是 **v2**（TASK-346，`#421` 已合入 main；`internal/orchestrator/operator.go:43-59`），与 `cmd/operator` 的 flag
集合对齐：身份写进 `--config` 的 `agent.customer_id` / `agent.cluster_id`，一次性 token 走 **`ENROLLMENT_TOKEN`**
环境变量，网关地址保留 `<CONTROL_PLANE_OPERATOR_GATEWAY>` 占位符——客户集群视角的可达地址是部署事实，
**必须由部署方替换**（控制面监听地址不能复用）。控制台弹窗复制时会自动把 `${ENROLLMENT_TOKEN}` 替换成
一次性明文 token（`web/src/components/operators/EnrollmentTokenModal.vue:34-37`）；**直接用 RPC 原始响应**
（不经控制台）时没有任何替换，必须自己替换成真实 token 再执行。

**2) 客户集群：只要 2 个 Secret。**

```bash
kubectl --kubeconfig <cust> create namespace release-manager-customer
kubectl --kubeconfig <cust> -n release-manager-customer create secret generic operator-enrollment \
  --from-literal=token='<一次性 token>'
kubectl --kubeconfig <cust> -n release-manager-customer create secret generic operator-gateway-ca \
  --from-file=gateway-ca.crt=/tmp/gateway-ca.crt
```

**不需要** client cert / client key / registry 凭据 / kubeconfig Secret：agent 自己生成 Ed25519 私钥并用 CSR
换 clientAuth 证书，身份落 bbolt（`/data/operator-commands.db`），token 消费后从环境里消失。

**3) operator Deployment**：复用 `deploy/kustomize/customer-agent/` 的 base + 一个 overlay，只改四处：

- `--orchestrator-addr` 换成 SAN 名 + **宿主映射端口**（base 预置的是 dev 的 `:30084`，
  `deploy/kustomize/customer-agent/base/deployment.yaml:53-54`）；
- config（`deploy/kustomize/customer-agent/c1-direct/configs/operator.dev.yaml:5-7`）的
  `agent.customer_id / cluster_id / operator_name` 必须与 token 的 scope **完全一致**——CSR 的 SAN 由它们派生为
  `<cluster>.<customer>.rm`（`internal/operator/bootstrap/csr.go:37`），控制面按同一规则校验
  （`internal/operator/service.go:1684-1697`，不匹配是 `csr_san_mismatch`）；
- `ca.cert_path` 指向挂进来的 CA（同上 `:14`）；
- `hostAliases` 换成 12.2 的宿主地址。

`strategy: Recreate` **不能改**：bbolt 是单写者，RollingUpdate 会让新 Pod 卡在 store 锁上、旧 Pod 永不替换
（`deploy/kustomize/customer-agent/base/deployment.yaml:19-20` 的注释即此）。

期望日志（实测）：

```json
{"msg":"operator enrolled","operator_id":"<UUID>","customer_id":"<CUSTOMER_ID>","cluster_id":"rm-cust"}
{"msg":"inventory sync completed","accepted":1,"missing":0,"status":"applied"}
```

重启 Pod 后应出现 `{"msg":"operator identity found; skipping enrollment"}`——token 只消费一次，身份在 PVC 里。

**4) 客户集群里必须有一个真实 Helm release。** `release_inventory` 的唯一键是
`(customer_id, cluster_id, namespace, release_name)`，且只能由 `SyncInventory` 的全量快照写入
（`internal/orchestrator/inventory.go`）⇒ 没有真 release 就没有 inventory 行、也没有可操作的「已安装 Release」：

```bash
helm --kubeconfig <cust> install release-fixture ./deploy/fixtures/chart -n e2e-release --create-namespace
```

**5) plan → apply → observe。** `CreateReleaseDefinition`（owner 取自已验证的登录身份，请求里的
`actor.organization` 只是兼容字段：非空且与身份不同会以 `invalid_argument` 拒绝；漏传不会再写出
NULL owner，也不会拖到批准时才暴露）→ `CreateValuesRevision` →
`SubmitValuesRevision` → 由**另一个用户** `ApproveValuesRevision`（创建者不能批准自己的 revision）→
`CreateOperation`（`INSTALL`）→ `GetOperation` 读 `preflightResult`。

- `PublishRelease` 仍是骨架（返回 `not_implemented`），流程里不要等它。
- `CreateOperation` 返回 `state=preflight`；预检四段 `artifact → render → cluster → runtime_pull` 全 `passed`
  后**自动**进 `queued → running → succeeded`，没有「人工点 apply」这一步 ⇒ **「plan」就是 `preflightResult`**；
  想只到 plan、不落 apply 就得抢时间：`CancelOperation` 在 `pending`/`preflight`/`queued` 都合法
  （`internal/orchestrator/operation/state.go` 的 `validTransitions`），但 `queued` 之后 operator 可能已经开跑，
  取消会走 `cancelling` 而不是干净地停在 plan。
- chart 必须写成 `oci://` 且 `chart_digest` 是 chart 归档的 sha256（`internal/orchestrator/bundle_service.go`），
  由**客户集群**去拉。本机 demo 在客户集群里跑了一个 `registry:3` + `helm push --plain-http`；生产应指向客户可达的
  chart 仓库。

### 12.4 离线客户集群的三个硬约束（本机 demo 踩过）

1. **`k3d image import` 会假成功**：节点导入失败也打印 `Successfully imported`，判据是输出里有没有 `ERRO`
   （照 `make docker-import-all` 的守卫写法）。多平台镜像先 `docker save --platform linux/amd64` 再导 tar。
2. **k3s 基础镜像必须一起导**：客户集群没有 docker.io 出口时，`pause` / `coredns` / `local-path-provisioner` /
   `metrics-server` 拉不到会让**所有** Pod 卡在 `ContainerCreating`；local-path 动态供给还要它自己的 helper
   `rancher/mirrored-library-busybox:1.36.1`，否则 PVC 永远 `Pending`，operator 连调度都过不去。
   本机做法：把本地的 `localhost:5001/rancher/*` 重新 tag 成 `docker.io` 的同一名字后导入。
3. **bundle 的图像覆盖是 `<ref>@<sha256>`**：operator 会把 bundle `images[].values_path` 覆盖成 `ref@digest`
   （`internal/operator/agent/agent.go` 的 `applyBundleImageOverrides`）。离线节点必须让这个 digest 引用
   **在本地 containerd 可解析**（`ctr -n k8s.io images tag <repo:tag> '<repo@sha256:...>'`），否则 Pod 会
   `ImagePullBackOff` 并反复去 pull 一个不存在的 registry。生产里应从客户可达的镜像仓库拉，不要把这条 hack 带进去。

### 12.5 实测结果与未做

实测（2026-10-18，客户集群 = 单节点 k3d `rm-cust`，operator 镜像 `localhost:5001/release-operator:a6707b6`）：

| 验证 | 结果 |
| --- | --- |
| enroll | 成功，`operator_id` 落库；重启 Pod 后 `identity found; skipping enrollment` |
| `operators` / `sessions` | 1 行 active（`cert_serial`、`certificate_expires_at` 非空）/ 1 行 `online` |
| `release_inventory` | 2 行（手工 helm 的 `e2e-release` + operator 安装的 `e2e-plan`），000024 的 `workload_kind/name/namespace/uid` 全非空 |
| 000024 身份四元组 / 000030 观测投影 | 两组的终态都非空（`workload_kind/name/namespace/uid`；`observed_containers` / `observed_image_refs` / `observed_replicas` / `observed_at`）。**但排序语义不同**：身份四元组（000024）若早于 `release_inventory` 行到达，会缓冲进 `pending_workload_identity`（`migrations/000025_pending_workload_identity.up.sql`，D5=A），并在 `SyncInventory` 的 Upsert 建行时回放（`internal/orchestrator/inventory.go:135` 的 `applyInventoryItems`，回放在 `:194`）；**000030 的 observed 投影不缓冲**，只在行已存在时写入（`internal/operator/workload_identity.go:332-335` 行在则应用、行不在只缓冲身份；`:397` 的 `applyWorkloadObservation` 只走行存在那条路径）⇒ 操作完成那次上报若早于行建立会被丢弃，之后只能等周期扫描：`defaultObservationInterval` = **5 分钟**（`internal/operator/agent/agent.go:380`），`startWorkloadObservation` 只在 ticker 上跑、没有首轮立即上报（重连时的 startup scan 会立刻补一次） |
| plan（预检） | `artifact` / `render` / `cluster` 全 passed，`runtime_pull` skipped（`runtime_pull_disabled`） |
| apply | 成功：`pending → preflight → queued → running → succeeded`，rollout `1/1` 就绪 |
| UI | 客户列表、集群 Operator 在线、发布清单、操作详情（预检四段 + 状态时间线）都能看到该集群 |

**未做**（如实）：

- 只到 INSTALL；**没做** upgrade / rollback / 紧急变更（image 路径）旅程；
- 未接 VKE 或任何真实客户集群；未做生产 CA 轮换；
- 未改 SAN 生成逻辑（`cmd/orchestrator/main.go:173-176` 仍是 2 个固定 DNS 名），也没加配置项；
- `PublishRelease` 未实现 ⇒ definition 没有「当前 bundle」；UI 发布清单里手工 helm 装的那条显示「生效中」但
  「未绑定 Definition」，operator 装的那条绑定 Definition 并显示「不同步」（同时提供「创建操作 / 紧急变更」）；
- ~~`sessions` 的 `customer_id` / `cluster_id` 只有 enroll 时创建的那一行有值，`CommandStream` 新建的行是空串~~
  **已修（TASK-361）**：建会话时从 operator 行写入两列（`internal/operator/service.go:488`），迁移 `migrations/000035_session_identity_backfill.up.sql` 回填存量空串行。

> **2026-10-18 后续实测（TASK-353）**：upgrade / rollback 已在上面的客户集群上真实走通，
> 紧急变更（image 路径）被可信门禁拒绝；细节与证据见 §12.6。上面的「未做」保留为 §12.5 时点的记录。

### 12.6 客户集群 release 的 upgrade / rollback（TASK-353 实测）

§12.5 的 INSTALL 之后，又用同一个客户集群（TASK-348 的 definition `release-fixture`、namespace `e2e-plan`）
把 **INSTALL → UPGRADE → ROLLBACK** 走完，并用真实资源变化验证。本节只写**做过的**与**没做的**，
不重复 §12.3 的接入步骤。

#### 12.6.1 入口与语义（先取证，再实测）

| 动作 | 入口 | 语义 |
| --- | --- | --- |
| UPGRADE | `CreateOperation`，`operation_type=UPGRADE` | 只接受 `INSTALL` / `UPGRADE`（`internal/orchestrator/service.go:203-207`）。没有独立的 upgrade 变更接口，`PublishRelease` 仍是 `not_implemented`。**对同一 release 再发一次 operation 是被支持的**：换 `Idempotency-Key` 即新操作；重复 key + 同 hash 重放首次结果，同 key 异 hash 返回 `AlreadyExists`（`internal/orchestrator/service.go:263-280`）。`expected_current_revision` 必须等于 inventory 里的 revision，否则 `revision_conflict`；inventory 不是 `active` 时是 `release_not_found`（`internal/orchestrator/service.go:1649-1684`）。 |
| ROLLBACK | `RollbackRelease`（proto `api/proto/orchestrator/v1/orchestrator.proto:1115`，实现 `internal/orchestrator/rollback.go:24`） | **产品有 rollback 入口**，不是「只能手工 helm rollback」：它新建一条 `ROLLBACK` operation（不是原地改失败的那条），要求 `reason`、`target_revision`、`expected_current_revision`（target 必须 < expected），拒绝携带 values（`rollback_values_not_allowed`），并与 upgrade 共用 inventory / 并发 / 客户禁用门禁。控制台入口见 `web/src/components/releases/RollbackReleaseDialog.vue:115`（发布清单在 `revision > 1` 时给按钮）。 |

UPGRADE 走**完整 preflight**（artifact → render → cluster；`runtime_pull` 未启用时 `skipped`），
dispatch 分支在 `internal/orchestrator/preflight/coordinator.go:221`。`internal/orchestrator/service.go:506-512`
那段注释里「UPGRADE never runs preflight stages」一句是 TASK-082 的旧结论，已被 ADR-027 取代
（该段其余关于首行 dispatch 的说明不受影响）；本次时间线证明确实跑完了三段。

#### 12.6.2 一次真实升级（chart 与 values 都变）

基线：definition `973456b9…`、已批准 values revision `b679c855…`（内容 `{"replicaCount":1}`）、
客户集群内跑 chart registry（`registry:3`，`rm353-registry…:5000`，plain HTTP，operator 配
`agent.registry_plain_http: true`）。bundle 的 `chart_digest` 取 **`.tgz` 的 sha256**（`sha256sum`），
不是 `helm push` 打印的 manifest digest——两者不同（本次 `a60734…` vs `5bfa6b…`），写错会在 artifact
阶段报 chart archive digest mismatch。

| 步骤 | 请求 | 结果 |
| --- | --- | --- |
| INSTALL | 新 bundle `8963d3ea…`（chart 0.1.0，digest `a60734…`）+ operation | `preflight → queued → running → succeeded`，24 s；三段 passed、`runtime_pull` skipped；客户集群 Helm release **v1**、Deployment `replicas=1`、Pod 1/1 |
| UPGRADE | 新 bundle `f1722029…`（chart **0.2.0**：Deployment 加 annotation `task353.demo/revision=upgrade-0.2.0`，digest `0e31c1…`）+ `values_patch={"replicaCount":2}` + `expected_current_revision=1` | `succeeded`，33 s；三段 passed；Helm release **v2**、`replicas=2`、annotation 出现、Pod 2/2；inventory 变 `revision=2 / chart 0.2.0 / observed_replicas=2` |
| ROLLBACK | `RollbackRelease{target_revision:1, expected_current_revision:2, reason:…}` | `preflight → queued → running → succeeded`，5 s；Helm release **v3**（内容复用 v1）、`replicas=1`、annotation 消失、Pod 1/1；inventory 变 `revision=3 / chart release-fixture-0.1.0` |

三个容易误判的点：

- **`values_patch` 是必要的**：已批准 revision 里钉了 `replicaCount:1`，只改 chart 的 `values.yaml` 不会生效。
  本次同时改 chart 内容（annotation）并用 `values_patch` 改副本数，两个变化都可从集群对象上直接看到。
- **rollback 不是把 Helm revision 号退回去**：它**追加**一个新 revision（v1,v2 → v3），v3 的内容等于 v1。
  所以回滚后 inventory 的 `revision` 是 **3** 而不是 1，`deployment.kubernetes.io/revision` 也是 3。
  判断「回滚到升级前」要看**内容**（副本数 / 镜像 / annotation），不能看 revision 号。
- **`RollbackRelease` 响应的 `to_revision` 是「内容目标 revision」**：本次请求 `target_revision=1`，修复前响应是
  `{fromRevision:2, toRevision:3}`，因为 `rollbackResponse` 用了 `ExpectedRevision+1` 而不是 `TargetRevision`。
  **已修（TASK-354）**：`internal/orchestrator/rollback.go:250-258` 现在投影
  `from_revision` = 请求的 `expected_current_revision`（乐观锁）与 `to_revision` = 请求的 `target_revision`
  （**回到哪个版本的内容**），判别式断言见 `internal/orchestrator/rollback_test.go:91`。
  回滚**追加**的新 Helm revision 号（上面的 v3）在该 RPC 里不可知——handler 只创建 operation，operator 之后才执行
  Helm rollback——只能从 inventory `revision` / Helm history 观察，**不要**从 `to_revision` 推断新 revision 号。

#### 12.6.3 紧急变更（image 路径）：入口有，本环境不可达

`ExecuteEmergencyChange` 存在，kill switch `emergency.enabled=true`（`app_settings` 实查）。image 分支的
解析在 `internal/orchestrator/emergency.go:871`：`artifact_ref` 必须是**已校验**的 candidate image，
且必须有 **trusted** 的 verification verdict。实测用已校验的 image candidate `7621986b…` 对
`deployments/e2e-plan/release-fixture` / container `release-fixture` 发起，被拒：

```json
{"code":"failed_precondition","message":"candidate artifact is not trusted"}
```

这不是入口缺失，而是**本环境没有可信签名**：`verification_records` 只有 `signature_missing`，没有 `trusted`
（本次没有伪造 verdict 去「跑通」）。⇒ 要在真实环境做 image 紧急变更，需要先完成签名与信任根接线
（REQ-043 / REQ-079），不在本演示范围。

> TASK-355 随后用产品代码路径把这条门禁走通了（全程未手工改库）：门禁条件、合法步骤与残留缺口见 §12.7。

#### 12.6.4 未做与残留

- 未在 UI 里实际点一遍 rollback 按钮（只取证了 RPC、代码接线与列表按钮条件；UI 行为未实测）；
- 未验证 upgrade 的**失败/回退**路径，也未验证「新建 values revision + 另一用户批准」后的升级
  （本次复用已批准 revision + `values_patch`）；
- **控制面残留**（客户集群删除后没有删除 RPC）：本次新增 `operators` 1 行（`rm-cust353`，active）、
  `sessions` 3 行、`operations` 3 行（INSTALL / UPGRADE / ROLLBACK 各一）、`release_bundles` 2 行、
  candidate artifacts 若干；`release_inventory` 的 `e2e-plan` 行停在 `revision=3 / active`，集群删除后
  不会再更新。`rm-prod` 未动（10/10 Pod、edge 200、网关 TLS `Verify return code: 0`）。

### 12.7 紧急变更的信任门禁与合法路径（TASK-355 验证，示例为推导）

§12.6.3 的 `candidate artifact is not trusted` 是**信任门禁**拒绝，不是入口缺失。TASK-355 用产品路径
（无手工改库）把 image 紧急变更走通了一次；本节写清门禁判什么、合法路径怎么走、以及它**不**证明什么。
行号基于 `0623788`。

#### 12.7.1 门禁条件（`resolveEmergencyArtifact`）

`internal/orchestrator/emergency.go:871-924`，按顺序：

| # | 条件 | 依据 |
| --- | --- | --- |
| 1 | candidate 是 image 且**已校验**（`ArtifactType=image`、`ValidatedAt != nil`） | `:883-889` |
| 2 | policy version 与 verifier 同源：`trust.PolicyVersion(GetPolicy(targetEnv).Version)` | `:890-897` |
| 3 | 必须存在一条 **trusted** verdict（只查 `verification_records`，不重算签名） | `:898-901` |
| 4 | verdict 的 `RevocationEpoch` 不低于当前 epoch | `:902-904` |
| 5 | 签发根**仍然活着**：`GetActiveByEnvironment` + `verdictRootIsLive`（有 `root_id` 按 root 匹配，没有则退回 `key_id`；两者皆空即拒） | `:912-918`、`:784-800` |
| 6 | ref 规范化：已是 `ref@sha256:…` 直接用，否则拼成 `ref@digest` | `:919-923` |

`targetEnv` 是 orchestrator 的 `--target-env`，默认 `staging`（`cmd/orchestrator/main.go:977`）；**生产 overlay
只替换了 args 里的 `--config`**（`deploy/kustomize/production/kustomization.yaml:214-228`；`- target:` 起于
`:214`，args 替换块在 `:219-222`，其中 `--config` 与配置路径在 `:221-222`），没有覆盖这个
flag ⇒ 现网生产 orchestrator 的信任环境也是 `staging`。登记信任根时 `environment` 必须与它逐字一致，
否则查的是另一个环境的 policy。

⚠️ 门禁**不验签**、**不要求 attestation/SBOM**、**不要求镜像自身有签名**：它只断言「存在一条 trusted、
未撤销、签发根还活着的行」，不判断这行是谁、以什么流程写进来的。

#### 12.7.2 bundle 回退：为什么「签 bundle」能授权 image

`trustedVerificationForArtifact`（`internal/orchestrator/emergency.go:825-868`）：

- 先按 **image digest** 查 verdict（`:828`）；命中即用，**且 artifact 自己的 verdict 说了算**——即使它是
  rejected，也不会被一个 trusted 的 bundle 覆盖（`:832-834`）。
- 未命中才回退：`BundlesForArtifact` 列出「真正交付过这个 image 的 bundle」（`:835-851`，走
  `bundle_candidate_artifacts` 链接表），再用 bundle 的 `DigestAlg:DigestValue` 查同一 policy 的 verdict
  （`:848-852`），**只接受 trusted**（`:859`）。
- 原因是产品里唯一的 trusted 写入者签的是 **bundle** digest（见 12.7.3），而 `common.v1.BundleImage`
  没有自己的签名。

#### 12.7.3 最小合法步骤（命令级；示例为读码推导，见 12.7.5）

前置：`platform_admin` 的 bearer token（`TrustService` 写 RPC 要 `trust_root/write`），以及一个**在线**的
operator（离线会留失败操作，见 12.7.5）。管理面用 port-forward 打通（生产 overlay 只有 ClusterIP）：

```bash
kubectl -n release-manager port-forward svc/orchestrator 8083:8083 &
```

**① 登记信任根**（产品 RPC `TrustService.CreateTrustRoot`，`api/proto/trust/v1/trust.proto:168`，实现
`internal/trust/service.go:40-81`；返回新 policy，version +1）：

```bash
curl -sS -X POST http://127.0.0.1:8083/trust.v1.TrustService/CreateTrustRoot \
  -H "Authorization: Bearer $PLATFORM_ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{
    "environment": "staging",
    "keyId": "task355-demo-key",
    "issuer": "release-manager-ci",
    "subjectPattern": "pipeline-",
    "publicKeyPem": "-----BEGIN PUBLIC KEY-----\n...\n-----END PUBLIC KEY-----\n"
  }'
```

必填：`environment` + `keyId` + `issuer` + 可解析的 `publicKeyPem`（`internal/trust/types.go:53-76`）；
`valid_from` 缺省为 now，`subjectPattern` 可省（空 = 任意 subject）。匹配规则是 **issuer 全等 + subject
前缀**（`internal/trust/types.go:100-105`），所以 `signatureRef.issuer` 必须与登记值逐字相等。
**不幂等**：重放会因 `key_id`/issuer 与现有 active root 重叠而 `INVALID_ARGUMENT`
（`internal/trust/service.go:55-58`），重试不能「修复」，先读 policy 确认根是否已存在。

**② 取要签的 digest —— 是 bundle 的服务端 canonical digest**

```bash
curl -sS -X POST http://127.0.0.1:8083/orchestrator.v1.BundleService/ListBundles \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"releaseDefinitionId":"<definition>"}'
# → bundles[].digest = {"algorithm":"sha256","value":"<64 hex>"}
```

要签的字符串是 `sha256:<64 hex>`，写进 `signatureRef.digest`；`CreateOperation` 用的正是
`DigestAlg + ":" + DigestValue`（`internal/orchestrator/service.go:386-389`）。注意这个 hex 是**服务端对
提交字段算的 canonical hash**（`internal/orchestrator/bundle_service.go:541-558`），不是镜像或 chart 的字节。

**③ 签名（外部流水线义务，仓库里没有 signer）**

```bash
printf '%s' 'sha256:<64 hex>' > /tmp/bundle-digest.txt   # 必须无换行：签名对象是 []byte(digest)
openssl pkeyutl -sign -inkey /path/to/ed25519-signing.key -rawin \
  -in /tmp/bundle-digest.txt | base64 -w0 > /tmp/bundle-digest.sig.b64
```

裸 Ed25519，消息 = digest 的 ASCII 字节（`internal/trust/ed25519.go:156`），签名的标准 base64 放进
`signatureRef.signature`。⚠️ 私钥不在仓库、不在集群，`cmd/` 下也没有 signer 子命令——**「谁签、私钥放
哪」是流水线侧必须自己补的一环**。（这两条命令 2026-10-18 用 openssl 3.6.5 实测：openssl 产出的签名能
被 Go 的 `ed25519.Verify` 验过；**多一个换行就验不过**。）

**④ 触发 verifier 落 trusted（产品侧唯一写入路径）**

`CreateOperation` 带 `signatureRef`（`api/proto/orchestrator/v1/orchestrator.proto:251`；字段
`digest`/`signature`/`issuer`/`subject`，见 `api/proto/common/v1/trust.proto:30-37`）：

```bash
curl -sS -X POST http://127.0.0.1:8083/orchestrator.v1.OrchestratorService/CreateOperation \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"operationType":"INSTALL","bundleId":"<bundle>","releaseDefinitionId":"<def>",
       "valuesRevisionId":"<approved rev>",
       "signatureRef":{"digest":"sha256:<64 hex>","signature":"<base64>",
                       "issuer":"release-manager-ci","subject":"pipeline-355"}}'
```

`verificationResult` 应为 `TRUSTED`。`Ed25519Verifier`（`internal/trust/ed25519.go:58-79`，装配在
`cmd/orchestrator/main.go:372-377`）按当前 policy 现算并 `persist` 一行（`:173-189`；`signature_identity`
是 digest|signature|issuer|subject 的 sha256）。两个必须知道的点：

- **没有独立的 verify/sign RPC**：要让 verdict 落库就必须建一条真操作（INSTALL/UPGRADE），它不是 dry-run，
  预检通过后会继续走到 apply。不要为了「试一下」对生产 definition 发它。
- **顺序不能反**：`create`/`rotate`/`retire` 会 bump **policy version**（verdict 的查询键），`revoke` 只 bump
  **revocation epoch**（`internal/trust/service.go:281`）——**两者都使旧 verdict 失效**：前者让行查不到
  （`internal/orchestrator/emergency.go:890-897` 与 `:898-901` 按 version 查），后者被 `:902-904` 的 epoch
  门拦下。结论不变：任何 root mutation 之后（先签后建根、或中途 rotate/revoke）都要重签/重发，必须重新走
  一次本步。

**⑤ 发起紧急变更**（`artifactRef` 用 `ListCandidateArtifacts` 返回的 candidate id）：

```bash
curl -sS -X POST http://127.0.0.1:8083/orchestrator.v1.OrchestratorService/ExecuteEmergencyChange \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"releaseDefinitionId":"<def>",
       "workloadRef":"deployments/<namespace>/<name>",
       "container":"<container>",
       "artifactRef":"<candidate artifact id>",
       "convergenceStrategy":"REVERT_ON_NEXT_RECONCILE",
       "operationVersion":"v1.0.0",
       "idempotencyKey":"<uuid>"}'
```

`workloadRef` 形如 `<gvr.resource>/<namespace>/<name>`（`api/proto/orchestrator/v1/orchestrator.proto:1718-1742`）；
`REQUIRE_PROMOTION` 还要带 `targetLocks`（同文件 `:1725`）。成功时 intent 的 `image_reference` 是
`ref@sha256:…`、操作终态 `succeeded`。

#### 12.7.4 三条缺口（别把「能跑通」当成能力）

1. **没有 signer 工具/文档**：签名、私钥托管、CI 里怎么调用都没有实现；`cmd/` 下没有 signer 子命令，
   12.7.3 ③ 是手工/流水线命令，不是产品能力。
2. **没有 per-image 签名**：`common.v1.BundleImage` 不自带签名，image 的 trusted 只能靠 12.7.2 的 bundle
   回退。于是 **`RecordArtifactEvent` 产生的 candidate 永远不可 trusted**：它不经过 bundle 提交，没有
   `bundle_candidate_artifacts` 链接，也就没有可回退的 verdict。
3. **`trusted ≠ 内容一致性`**：被签的 digest 是服务端对提交字段算的 canonical hash
   （`internal/orchestrator/bundle_service.go:541-558`），而校验「声明内容 vs 实际制品字节」的
   `verifyDigestParity` 是**空实现**（`internal/orchestrator/validation_worker.go:150-153`，唯一调用点
   `:97` 恒返回 nil）。所以 trusted 的含义只有：「有人用一把已登记的 key，对这份**声明**的 hash 签了字」。
   它不证明 chart/镜像的字节与声明一致，也不证明构建来源；§10 第 8 条（无镜像签名、无
   cosign/attestation/SBOM）仍然成立。

#### 12.7.5 实测边界与 operator 离线注意

> 注意：本节 12.7.3 的 curl 示例使用 port-forward 到 8083（ClusterIP 明文端口）——该通路是读码推导，未对
> 活的 orchestrator 实跑；TASK-355 的实测走的是宿主 `https://localhost:8443` 网关（edge 的 NodePort
> 30443；web 侧则以 `http://orchestrator:8083` 反代，见 `web/nginx.conf:33`）。

实测（2026-10-18，TASK-355）：建根 `42616b42…`（staging / keyId `task355-demo-key`，`audit_events` 有
`create_root|succeeded`）→ 用它的私钥对 bundle `1483e50f…` 的 canonical digest `sha256:170a4a42…` 签名 →
`CreateOperation(INSTALL)` 让 `Ed25519Verifier` **由产品写入** trusted 行（`verification_records` 里 trusted
行的 digest = bundle digest；**image digest `sha256:b7817ff0…` 的 verdict 行数 = 0** ⇒ 12.7.2 的回退得到
实证）→ `ExecuteEmergencyChange` 返回 SUCCEEDED、`effectStatus=APPLIED`：Deployment 镜像
`…release-fixture:dev` → `…:dev@sha256:b7817ff0…`，新 Pod 1/1 Running，`emergency_intents` persisted/APPLIED，
`audit_events` 有 `emergency_change|succeeded`。**全程没有手工改库。**

边界（别把它们当成可复算的证据）：

- **客户集群已删除**：上面「镜像真的变了 / Pod 1/1」只有当时的记录，**现在无法现场复验**。
- **签名私钥未留存**：无法用密码学方法复算那条 verdict；「它由产品写入、不是手工插入」的依据是 `summary`
  模板文案 + 非空 `signature_identity` + `create_root` 审计 + 时间线，属**推断**。
- **operator 离线会留下失败操作**：`ExecuteEmergencyChange` **先**建 operation + intent
  （`internal/orchestrator/emergency.go:271-289`）**后**才查 operator 是否在线（`:290-294`）。所以只要信任门
  已过而 operator 离线，就会留下一条 `failed` 的 EMERGENCY operation + 一条 intent（实测 `5a2410e5…`，
  `operator_offline`），且 intent 的 `image_reference` 已经是 `ref@sha256:…`（信任门在更早的 `:152` 就过了）。
  重试是**另一次** operation，不是对这条的续跑。

交叉引用：§10 已知限制（第 8 条：无镜像签名 / 无 attestation）；§12.6.3 的拒绝是 TASK-353 时点的结论，
现在应理解为「可走通，但按 12.7.4 的缺口理解它的含义」。

## 事实源

- 清单与设计理由：`deploy/kustomize/production/README.md`、`deploy/kustomize/production/kustomization.yaml`、`edge.yaml`、`networkpolicies.yaml`、`production-settings.yaml`、`deploy/kustomize/production/configs/*.production.yaml`、`deploy/kustomize/postgres/init.sql`
- 构建与门禁：`Makefile`（`docker-build-all`、`docker-import-all`、`docker-push-all`、`check-production-images`、`migrate`、`check-docs`）
- 运行期证据（2026-10-17 实测）：`kubectl -n release-manager get pods,pvc,secret,svc`、`kubectl logs --previous`、`crictl images`、`docker port k3d-rm-prod-serverlb`、`openssl s_client`、`pg_dump`/`psql`、`kubectl rollout history`
- 代码事实：`cmd/api/main.go`、`internal/operator/ca/ca.go`、`internal/auth/browser_session.go`、`internal/auth/service.go`、`web/nginx.conf`
- 相邻文档：`docs/dev-environment.md`（dev 生命周期）、`docs/runbook.md`（故障处置）、`docs/configuration.md`（配置键）、`docs/observability.md`（可观测面）

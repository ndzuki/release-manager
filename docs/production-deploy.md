# 生产部署（单节点 k3d `rm-prod`）

本文是 `deploy/kustomize/production` overlay 在**单节点 k3d 集群**上的部署与运维手册：前置条件、必填 Secret、启动顺序、TLS、备份恢复、升级回滚、可观测面与已知限制。manifest 的结构与逐条设计理由不在此重复，见 [production overlay README](../deploy/kustomize/production/README.md)；故障处置的通用路径见 [runbook](runbook.md)。

**口径**：文中数字来自 2026-10-17 在本机的一次真实部署与演练（集群仍在运行）。凡是「实测」都注明测量方式与时刻；「建议」表示当前**不存在**、需要改动才能获得的能力，不作为排障依据。命令里的 `<...>` 是要替换的占位符。

## 0. 速览

| 项 | 实测值 | 口径 |
| --- | --- | --- |
| 集群 | 单节点 k3d `rm-prod`，k3s `v1.31.5+k3s1` | `k3d cluster list`、`kubectl get nodes` |
| namespace | `release-manager` | `kubectl -n release-manager get pods` |
| Pod | 10 个，**10/10 Ready** | 2026-10-17 19:0x 快照 |
| PVC | **5 个全部 `Bound`**（含自动备份卷 `release-manager-backups`） | 同上 + `kubectl -n release-manager get pvc` |
| 渲染对象 | **45 个**（1 Namespace / 8 ConfigMap / 7 Deployment / 2 StatefulSet / 4 PDB / 10 Service / 3 PVC / 9 NetworkPolicy / 1 CronJob） | `kubectl kustomize`，纯本地渲染，不接触集群 |
| 自动备份 | `CronJob release-manager-backup`，每日 `02:00 UTC`，dump 落 `release-manager-backups`（5Gi） | 2026-10-17 实测，见 §7.4 |
| 镜像 tag | `a6707b6`（`git rev-parse --short HEAD`） | `kubectl get pods -o jsonpath=…image`、`crictl images` |
| 入口 | `https://localhost:8443`（同源，TLS） | 见 §6 |
| schema | `schema_migrations` = `34`, `dirty=f` | 只读 SQL，见 §11 |

## 1. 现状与拓扑

### 1.1 工作负载

| 工作负载 | 类型 | 副本 | 容器端口 | 镜像（实测） | 说明 |
| --- | --- | --- | --- | --- | --- |
| `api` | Deployment | 1 | 8088 | `localhost:5001/release-api:3ecf69b` | 审计读面；overlay 已注入 `DATABASE_DRIVER=postgres` + `DATABASE_DSN`（Secret），`--db`/PVC 保留给归档；默认组织视图仍空，见 §10-1 |
| `auth` | Deployment | 1 | 8085 | `release-auth` | 唯一持有 JWT **私钥**、Cookie 会话、Redis |
| `orchestrator` | Deployment | 1 | 8083 / 8084 | `release-orchestrator` | 管理面 8083；operator mTLS 网关 8084（仅两条路由，无 `/health`） |
| `notifier` | Deployment | 1 | 8086 | `release-notifier` | 独立库 `release_notifier`；生产 egress allowlist 为空 |
| `webhook` | Deployment | 1 | 8082 | `release-webhook` | CI/Harbor 回调入口，服务令牌鉴权 |
| `web` | Deployment | **2** | 8087 | `release-web` | nginx：SPA + 按 Connect 前缀反代；同源路由的唯一实现 |
| `edge` | Deployment | 1 | 443 | `nginx:1.27-alpine` | TLS 终止 → `web:8087`；唯一的 NodePort |
| `postgres` | StatefulSet | 1 | 5432 | `postgres:16`（服务端 **16.15**） | 数据权威（ADR-014） |
| `redis` | StatefulSet | 1 | 6379 | `redis:7-alpine`（**7.4.11**） | 仅 auth 会话缓存 + refresh 黑名单；无 AUTH |
| `release-manager-backup` | CronJob | — | — | `postgres:16` | 每日 `02:00 UTC` 逻辑备份（`release_manager` + `release_notifier` → PVC `release-manager-backups`），见 §7.4 |

七个 Deployment 里只有 `edge` 不是 Go 进程；`api`/`auth`/`orchestrator`/`notifier`/`webhook` 各自暴露 `/health` 与 `/readyz`，`web` 的 `/health`、`/readyz` 由 nginx **反代到 orchestrator**（`web/nginx.conf`），`edge` 只有 `tcpSocket` 探针。

### 1.2 入口链路

```
浏览器 ── https://localhost:8443
      → k3d serverlb：宿主 8443/tcp → 容器 30443/tcp
      → Service edge（NodePort 30443，本 overlay 唯一的 NodePort）
      → edge pod：nginx 终止 TLS（自签证书），proxy_pass http://web:8087
      → Service web:8087：nginx 托管 SPA，并按 /auth.v1. /orchestrator.v1. /webhook.v1.
        /operator.v1. /notifier.v1. /audit.v1. /trust.v1. 等前缀反代到各 ClusterIP 服务
      → auth:8085 / orchestrator:8083 / webhook:8082 / notifier:8086 / api:8088
```

要点：

- 除 `edge` 外所有 Service 都是 `ClusterIP`；`NodePort 30443` 是有记录的唯一例外（`deploy/kustomize/production/edge.yaml:167`）。
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

`default-deny-ingress` 下**探针必须显式放行**：k3s 默认 kube-router 把 NetworkPolicy 装进 filter **INPUT** 链，kubelet → Pod 的探针包来自节点地址，会被 default-deny 丢掉，结果是**所有应用 Pod 永远不 Ready**（k3s issue #10030）。overlay 因此有 `allow-kubelet-probes`，源地址是 Pod CIDR（`nodeCidr`，默认 `10.42.0.0/16`，见 `deploy/kustomize/production/networkpolicies.yaml:60` 与 `production-settings.yaml`），端口是 8082–8088。换 CIDR 只改 `production-settings.yaml` 一处（kustomization 的 replacement 负责写进 ipBlock）。

- **k3s < v1.28.9 / < v1.29.4** 命中 #10030 的旧行为（探针被策略拦截），这些版本上本 overlay 不起作用；本集群实测 **v1.31.5 无此问题**。
- 非 k3s CNI（不在节点路径上评估策略）可能不需要这条规则，但也**不会因此出错**。

### ⑤ edge TLS 与证书信任

若 ① 已按 §3 建好 `release-manager-edge-tls`，本步只剩验签与浏览器信任：自签证书需要在浏览器里做**一次性信任例外**（自动化脚本用 `ignoreHTTPSErrors: true`），证书 SAN 必须含 `localhost`/`127.0.0.1`。重签或轮换后的 `kubectl rollout restart deployment/edge` 与完整命令见 §6。

### ⑥ 把宿主 8443 映射到 NodePort 30443

```bash
k3d node edit k3d-rm-prod-serverlb --port-add "8443:30443"
docker port k3d-rm-prod-serverlb
# 30443/tcp -> 0.0.0.0:8443
```

这一步只改 k3d 负载均衡节点，**不重建集群、不动 PVC 与 Secret**。实测映射同时出现在 IPv4 与 IPv6（`[::]:8443`）。

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
`release_manager`（必需）与 `release_notifier`（有用户表时）各做一次 `pg_dump -Fc`，
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
| 库 | `release_manager`（必需）+ `release_notifier`（存在且有用户表时；空库跳过） |
| 格式/位置 | `pg_dump -Fc`，PVC `release-manager-backups`（5Gi，与 PG 同一 `storageClassName` 旋钮），容器内 `/backups` |
| 文件名 | `release_manager-<UTC 时间戳>.dump` / `release_notifier-<UTC 时间戳>.dump`（`YYYYmmddTHHMMSSZ`） |
| 保留 | 同一次运行内 `find … -mtime +7 -delete`：删除 **7 天前**的文件（不是“保留 7 个文件”，漏跑一次不会把最后一份好 dump 删掉） |
| 失败处理 | `backoffLimit: 2`、`activeDeadlineSeconds: 1800`；保留 3 成功 + 3 失败 Job |
| 凭据 | 从 `release-manager-credentials` 取 `POSTGRES_USER`/`POSTGRES_PASSWORD`（`PGPASSWORD`）/`POSTGRES_DB`；清单与日志中没有口令 |
| 运行身份 | `postgres:16`，`runAsNonRoot` uid/gid **999**，`fsGroup: 999`，drop ALL caps，只读 rootfs |

**手动触发**：

```bash
kubectl -n release-manager create job --from=cronjob/release-manager-backup backup-manual
kubectl -n release-manager wait --for=condition=complete job/backup-manual --timeout=600s
kubectl -n release-manager logs job/backup-manual
```

**恢复**：见 §7.2–§7.3；从 PVC 恢复要点是临时 Pod/Job 必须挂载 `release-manager-backups`。

**运维注意**：

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

### 8.2 回滚

```bash
kubectl -n release-manager rollout undo deployment/auth
kubectl -n release-manager rollout status deployment/auth --timeout=300s
```

- 只回退镜像，**不回退 schema**。若新版本已经前滚了不兼容的 schema，回滚镜像后会起不来，唯一退路是从 §7 的 dump 恢复。
- 实测当前集群每个 Deployment 只有 `REVISION 1`（`kubectl rollout history`）：**第一次发布之后 `rollout undo` 才有东西可退**，别把它当成第一次部署的保险。
- 更长久的做法是把上一版 tag 记在发布记录里，回滚时重新 `kustomize edit set image` 到旧 tag 再 apply（与 8.1 同一条链路，可审计）。

### 8.3 停机/重启验证

```bash
k3d cluster stop rm-prod && k3d cluster start rm-prod
kubectl -n release-manager get pods
```

实测停服约 **57 s**，重启后 10/10 Pod 恢复 Ready，PostgreSQL/Redis 数据完好（`local-path` 卷留在宿主，随 `k3d cluster delete` 才会丢）。注意 `k3d cluster stop` 之后 `kubectl` 会连接失败，属预期。

## 9. 可观测

| 面 | 现状 | 证据 |
| --- | --- | --- |
| `/health` | 5 个 Go 服务恒 200（liveness 无失败路径）；web 8087 反代到 orchestrator；edge 只有 `tcpSocket` | 实测 8082/8083/8085/8086/8087/8088 `/health` 均 200 |
| `/readyz` | 有失败语义（数据库/Redis/上游），失败 503 + `{"status":"degraded","checks":{...}}`；web 的同名路径报的是 **orchestrator** 的健康度 | 实测 6 个端口 `/readyz` 均 200 |
| `/metrics` | **只有 auth(8085) 与 orchestrator(8083)** 暴露 Prometheus 文本 | 实测：8083、8085 → 200；8082、8086、8088 → **404** |
| web 的 `/metrics` | ⚠️ 返回 **200，但内容是 SPA 的 HTML**（`location /` 的 `try_files … /index.html` 兜底） | 实测 `Content-Type: text/html`、4,666 B |
| 日志 | JSON（slog）写 stderr，`kubectl logs` 直接可读；字段含 `time/level/msg/request_id/procedure/error` | 实测 auth 启动/请求日志 |
| 审计 | `audit_events` 表在 PostgreSQL；控制台 `/audit` 走 `release-api`（`web/nginx.conf:91` → `api:8088`），api 已按 `DATABASE_DRIVER=postgres` 读 PG | 见 §10-1：默认组织视图仍空（PG 现有事件均为 `system` 范围） |
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

## 10. 已知限制

逐条都是实测或读码确认，**不是猜测**。排障时不要假设这些能力存在。

1. **`release-api` 已接 PostgreSQL，但控制台 `/audit` 默认组织视图仍为空（数据范围问题，不是引擎问题）**：生产 overlay 已为 `api` 注入 `DATABASE_DRIVER=postgres` 与 `DATABASE_DSN`（Secret `release-manager-credentials`；`deploy/kustomize/production/kustomization.yaml`，TASK-338），`--db /data/api.db` 与 PVC 保留（审计归档目录 `/data/archives` 仍需持久卷）。实测（2026-10-17，TASK-338 会话，镜像 `release-api:3ecf69b`）：api 启动日志为 `{"msg":"store opened","driver":"postgres"}`；PG `audit_events` 仍 **2 行**且 `organization_id` 全部为 `system`（orchestrator GC），按控制台管理员所属组织过滤为 **0 行**；同一登录会话直连 `POST /audit.v1.AuditService/QueryAuditEvents` 时，`filter.organization_id="system"`（platform_admin 跨组织）返回 **2 条**（这 2 行由 orchestrator 直接写入 PG），证明 api 读的是 PG；空 filter 与显式登录组织均返回 **0 条**，浏览器 `/audit` 响应零事件并渲染「暂无审计事件」。结论：**引擎选择缺口已关闭**；`/audit` 仍空是因为 PG 里只有 `system` 范围的系统事件，而控制台按登录组织过滤（`web/src/pages/AuditPage.vue:29`）。要看到这些事件需要产品决策（给 platform_admin 提供全组织/系统范围查询，或把系统事件写入可查询的组织范围）；在此之前不要删 `release-manager-api-data` PVC，也不要把「/audit 空」再归因于 SQLite。
2. **探针放行依赖 k3s 版本与 Pod CIDR**：`allow-kubelet-probes` 解决了 issue #10030，但 k3s **< v1.28.9 / < v1.29.4** 仍会拦掉探针；换 CIDR 必须改 `production-settings.yaml` 的 `nodeCidr`。本集群 v1.31.5 实测无问题。
3. **授权策略热重载窗口约 5 s**：auth 的 `authorization.policy_reload_interval: 5s`（`deploy/kustomize/production/configs/auth.production.yaml`）。首次 `Initialize` 后 5 s 内，控制台可能被弹到 `/forbidden`——刷新即可，不是权限配错。
4. **edge 单副本 + 自签证书**：浏览器需要一次性信任例外；edge 重启期间控制台不可用（后端仍在跑）。且 edge 的 `server_name _` 不区分主机名。
5. **宿主 8443 绑定 `0.0.0.0`**：实测 `docker port` 同时出现 `0.0.0.0:8443` 与 `[::]:8443`，同网段任何人都能访问。只给本机用时把映射改成 `127.0.0.1:8443:30443`。
6. **无 Vault CA 供给**：ADR-017/ADR-020 的 Vault 路径未接线，CA 与令牌都是静态 Secret（notifier 的 `vault.enabled: false`）。轮换靠手工替换 Secret。
7. **无 HA / 多副本**：只有 `web` 是 2 副本；api/auth/orchestrator/notifier/webhook 单副本且 PDB `minAvailable: 1` ⇒ `kubectl drain` 会在这些 Pod 上**永久阻塞**（这是刻意设计，别删 PDB 绕过）。也没有多节点/反亲和/跨可用区。
8. **无镜像签名、无 CI 推送镜像**：`.github/workflows/` 里没有 build/push 这 6 个镜像的 job（`sync-to-gitcode.yaml` 只镜像 git 仓库），镜像靠本机构建 + `k3d image import`；`make docker-push-all` 只在你已运行的 registry 上手工执行。没有 cosign/attestation。
9. **无 schema 降级通道、无 Prometheus/Grafana；自动备份已做但不异地**：自动逻辑备份见 §7.4（每日一次、保留 7 天），但没有 WAL 归档 ⇒ RPO ≤ 24 h，且备份与库同节点同盘 ⇒ 节点丢失会一起丢；异地拷贝仍需操作者补。Prometheus/Grafana 仍不存在，抓取示例见 §9.1（未部署）。schema 仍只能前滚（§8.1）。
10. **Redis 无 AUTH**：只靠 NetworkPolicy 限制为同 namespace 可达（`allow-redis`）。Redis 一旦离开这个边界就必须补 AUTH。
11. **客户集群 operator 未接入**：本环境没有 customer cluster/agent，因此「rollback / 已安装 Release / operator 在线状态」这类旅程**没有数据可测**；`sessions` 表为空是预期，不是故障。

## 11. 验收清单（本次实测结果）

复验时按表逐条对照；命令都在本机跑过。

| 检查 | 实测结果 | 复验命令 |
| --- | --- | --- |
| Pod 就绪 | **10/10 Ready**（api/auth/orchestrator/notifier/webhook ×1、web ×2、edge ×1、postgres-0、redis-0） | `kubectl -n release-manager get pods` |
| PVC | **5/5 `Bound`**，容量 10Gi/5Gi/1Gi/1Gi/5Gi，`local-path` | `kubectl -n release-manager get pvc` |
| 自动备份 | `CronJob/release-manager-backup` 存在且立即触发成功：5 s 完成，`release_manager` **133,173 B** + `release_notifier` **4,592 B** | §7.4 的三条命令；`kubectl -n release-manager get cronjob,pvc` |
| schema | `34`，`dirty=f` | `kubectl -n release-manager exec postgres-0 -- psql -U release_manager -d release_manager -Atc "select version,dirty from schema_migrations"` |
| 入口 | `https://localhost:8443/` → 200；`GetInitStatus` → `{"initialized":true}` | §6 的两条 curl |
| 登录 | `Set-Cookie` 三个（`rm_access`/`rm_refresh`/`rm_csrf`），登录后 **653 ms** 离开 `/login`，刷新保持会话 | 浏览器访问 `https://localhost:8443`，用 `Initialize` 时创建的管理员账号登录 |
| 审计页 | `/audit` HTTP 200，列表 0 行（原因见 §10-1，**不是** 500/404） | 浏览器打开 Audit 页面；或按 `api/kulala/audit.http` 的 `QueryAuditEvents` 请求复验 |
| 停机/重启 | `k3d cluster stop/start` 约 **57 s**，数据完好 | §8.3 |
| 备份/恢复 | 手工：dump 93,201 B / 58 表；自动：133,173 B + 4,592 B → restore `rm_backup_check` → `34|false`、58 表/146 索引/关键表行数全部与源库一致 | §7.1–§7.4 |
| 镜像门禁 | 未改 tag 时 `make check-production-images` FAIL 并打印 6 行 `:dev`；改后 PASS | §2.3 |

## 事实源

- 清单与设计理由：`deploy/kustomize/production/README.md`、`deploy/kustomize/production/kustomization.yaml`、`edge.yaml`、`networkpolicies.yaml`、`production-settings.yaml`、`deploy/kustomize/production/configs/*.production.yaml`、`deploy/kustomize/postgres/init.sql`
- 构建与门禁：`Makefile`（`docker-build-all`、`docker-import-all`、`docker-push-all`、`check-production-images`、`migrate`、`check-docs`）
- 运行期证据（2026-10-17 实测）：`kubectl -n release-manager get pods,pvc,secret,svc`、`kubectl logs --previous`、`crictl images`、`docker port k3d-rm-prod-serverlb`、`openssl s_client`、`pg_dump`/`psql`、`kubectl rollout history`
- 代码事实：`cmd/api/main.go`、`internal/operator/ca/ca.go`、`internal/auth/browser_session.go`、`internal/auth/service.go`、`web/nginx.conf`
- 相邻文档：`docs/dev-environment.md`（dev 生命周期）、`docs/runbook.md`（故障处置）、`docs/configuration.md`（配置键）、`docs/observability.md`（可观测面）

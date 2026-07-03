# 开发文档

## 目录

- [一键启动](#一键启动)
- [前端开发](#前端开发)
- [后端开发](#后端开发)
- [代码规范](#代码规范)
- [Makefile 说明](#makefile-说明)

## 一键启动

```bash
# 完整本地开发环境 (3 条命令)
make dev-up        # 终端 1: kind 集群 + registry proxy + operator + 证书
make dev-manager   # 终端 2: release-manager (HTTP :8080, gRPC :8443)
make dev-web       # 终端 3: Vue 前端 (http://localhost:3000)
```

`make dev-up` 自动完成:
1. 从 `.env` 读取 Harbor 配置
2. 创建 kind 集群 (模拟客户私有化环境)
3. 部署 registry:3 proxy (localhost:30500 → Harbor pull-through cache)
4. 生成 mTLS 证书 (CA 10 年, Client 3 年)
5. 自动获取 Harbor 自签 CA 并注入 K8s Secret
6. 构建 release-operator 镜像 + 加载到 kind
7. Helm 部署 release-operator (customerID=localhost001)

### 一键清理

```bash
make dev-down      # 删除 kind 集群
```

### 自动化链路验证

```bash
# 1. 注册客户
make dev-register
# 2. 模拟 Harbor webhook
curl -X POST http://localhost:8080/api/v1/webhook/harbor \
  -H 'Content-Type: application/json' \
  -d '{"type":"PUSH_HELMCHART","event_data":{"resources":[{"tag":"0.0.15","resource_url":"oci://localhost:5000/helm/magic-sandbox"}],"repository":{"name":"helm/magic-sandbox"}}}'
# 3. 查看结果
curl http://localhost:8080/api/v1/releases | jq .
```

## 前端开发

### 技术栈

| 技术 | 版本 | 用途 |
|------|------|------|
| Vue | 3.5 | 前端框架 (Composition API + `<script setup>`) |
| TypeScript | 5.x | 类型系统 |
| Vue Router | 4.x | 路由 (懒加载 + 认证守卫) |
| Pinia | 2.x | 状态管理 (auth store) |
| Vite | 6.x | 构建工具 + HMR |

### 启动

```bash
cd web
npm install        # 首次安装依赖
npm run dev        # 开发模式 (HMR + API 代理到 :8080)
npm run build      # 生产构建 → dist/
```

### 目录结构

```
web/src/
├── views/                    # 路由级页面
│   ├── DashboardView.vue     # 概览: 统计 + 客户状态 + 证书预警
│   ├── CustomersView.vue     # 客户 CRUD + Chart 分配查看
│   ├── ChartsView.vue        # Chart 定义管理
│   ├── ReleasesView.vue      # 发布历史 (状态过滤)
│   ├── CertificatesView.vue  # 证书到期监控 + 远程更新说明
│   └── LoginView.vue         # API Key / 钉钉扫码登录
├── components/
│   └── AppLayout.vue         # 侧边栏 + 导航 + 用户信息
├── composables/
│   └── useApi.ts             # API 客户端 (fetch + Bearer/API-Key)
├── stores/
│   └── auth.ts               # Pinia auth store (token, user, apiKey)
├── router/index.ts           # 路由表 + beforeEach 认证守卫
├── types.ts                  # 全部 TypeScript 类型
└── assets/main.css           # CSS 变量 + 全局样式
```

### API 代理

Vite 开发服务器自动代理 `/api` 到 `http://localhost:8080`:

```ts
// vite.config.ts
server: {
  proxy: { '/api': { target: 'http://localhost:8080' } }
}
```

### 前端页面路由

| 路径 | 页面 | 需要认证 | 说明 |
|------|------|----------|------|
| `/login` | LoginView | ❌ | API Key / 钉钉扫码登录 |
| `/` | DashboardView | ✅ | 系统概览 |
| `/customers` | CustomersView | ✅ | 客户管理 |
| `/charts` | ChartsView | ✅ | Chart 配置 |
| `/releases` | ReleasesView | ✅ | 发布历史 |
| `/certificates` | CertificatesView | ✅ | 证书管理 |

## 后端开发

### 技术栈

- Go 1.26, Helm v4 SDK, gRPC + protobuf, SQLite, logr + zap

### 本地运行

```bash
# release-manager
go run ./cmd/release-manager/ --config configs/manager.example.yaml

# release-operator (需要 kind 集群中的真实环境)
# 本地调试推荐用 make dev-operator 部署到 kind
```

### 代码生成

```bash
make proto         # buf generate → api/gen/
make docs          # swag init → api/openapi/
```

## 代码规范

### Go

- **包注释**: 中文，描述包的职责
- **函数注释**: 中文，公开函数必有注释
- **标识符**: 英文 (变量/函数/类型名)
- **错误处理**: `fmt.Errorf("...: %w", err)` 包装
- **Context**: 所有 I/O 操作第一参数
- **日志**: 结构化 key-value 对，使用 `logr.Logger`

### Vue / TypeScript

- **Composition API + `<script setup lang="ts">`**: 100% 使用
- **shallowRef for primitives**: `token`, `loading`, `filterStatus`
- **computed over template logic**: 筛选/排序在 computed 中
- **Props down / Events up**: `defineProps`, `defineEmits` typed
- **PascalCase components**: `AppLayout`, `DashboardView`
- **Scoped CSS + class selectors**: `<style scoped>`

### 提交规范

```
<type>(<scope>): <description>

type: feat, fix, docs, test, refactor, chore
scope: operator, manager, web, helm, tls, config
```

## Makefile 说明

### 快速启动 (开发日常)

```bash
make dev-up        # 一键创建完整本地开发环境
make dev-manager   # 启动 release-manager
make dev-web       # 启动 Vue 前端
make dev-down      # 清理 kind 集群
```

### 开发迭代

```bash
make dev-operator  # 改 operator 代码 → 重建镜像 → 重新部署
make dev-register  # 注册测试客户
```

### 构建与质量

```bash
make build         # go build → bin/release-operator + bin/release-manager
make test          # go test -race (50+ tests)
make test-e2e      # E2E 测试 (需 kind)
make lint          # golangci-lint
make proto         # buf generate
make docs          # swag init (OpenAPI v3)
```

### 镜像

```bash
make image-operator   # docker build release-operator
make image-manager    # docker build release-manager
```

### 环境变量覆写

```bash
CUSTOMER_ID=customer-002 make dev-up     # 不同 customer ID
CLUSTER_NAME=my-test make dev-up         # 不同 kind 集群名
MANAGER_PORT=9090 make dev-manager       # 不同端口
```

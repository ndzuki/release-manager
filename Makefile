# =============================================================================
# Release Operator - Build & Development Makefile
# =============================================================================

# Go configuration
GOPATH ?= $(shell go env GOPATH)
GOBIN  ?= $(GOPATH)/bin
GO     ?= go
GOFMT  ?= gofmt

# Docker configuration
DOCKER ?= docker
IMAGE_REGISTRY ?= harbor.example.com
IMAGE_TAG ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")

# Binary names
OPERATOR_BIN := release-operator
MANAGER_BIN := release-manager

# Proto configuration
BUF ?= $(GOBIN)/buf
PROTOC_GEN_GO ?= $(GOBIN)/protoc-gen-go
PROTOC_GEN_GO_GRPC ?= $(GOBIN)/protoc-gen-go-grpc
PROTO_DIR := api/proto
GEN_DIR := api/gen

# Colors
GREEN  := \033[32m
YELLOW := \033[33m
BLUE   := \033[34m
RED    := \033[31m
NC     := \033[0m

.PHONY: help
help: ## 显示帮助信息
	@echo "$(BLUE)Release Operator - Build & Development$(NC)"
	@echo "$(BLUE)======================================$(NC)"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "$(GREEN)%-24s$(NC) %s\n", $$1, $$2}'

# =============================================================================
# 依赖管理
# =============================================================================

.PHONY: deps
deps: ## 下载 Go 依赖
	@echo "$(YELLOW)Downloading Go dependencies...$(NC)"
	$(GO) mod download
	$(GO) mod tidy
	@echo "$(GREEN)Dependencies ready$(NC)"

.PHONY: tools
tools: ## 安装开发工具 (buf, protoc-gen-go, etc.)
	@echo "$(YELLOW)Installing development tools...$(NC)"
	$(GO) install github.com/bufbuild/buf/cmd/buf@latest
	$(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	$(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	$(GO) install github.com/swaggo/swag/cmd/swag@latest
	@echo "$(GREEN)Tools installed$(NC)"

# =============================================================================
# Proto 生成
# =============================================================================

.PHONY: proto
proto: ## 生成 protobuf Go 代码
	@echo "$(YELLOW)Generating protobuf code...$(NC)"
	@if [ ! -f "$(BUF)" ]; then \
		echo "$(RED)buf not found. Run 'make tools' first.$(NC)"; \
		exit 1; \
	fi
	cd $(PROTO_DIR) && $(BUF) generate
	@echo "$(GREEN)Proto code generated$(NC)"

.PHONY: proto-lint
proto-lint: ## 检查 proto 文件
	@echo "$(YELLOW)Linting proto files...$(NC)"
	cd $(PROTO_DIR) && $(BUF) lint
	@echo "$(GREEN)Proto lint passed$(NC)"

# =============================================================================
# 构建
# =============================================================================

.PHONY: build
build: build-operator build-notification ## 构建所有二进制文件

.PHONY: build-operator
build-operator: proto ## 构建 release-operator
	@echo "$(YELLOW)Building $(OPERATOR_BIN)...$(NC)"
	$(GO) build -o bin/$(OPERATOR_BIN) ./cmd/$(OPERATOR_BIN)/
	@echo "$(GREEN)$(OPERATOR_BIN) built$(NC)"

.PHONY: build-notification
build-notification: proto ## 构建 release-manager
	@echo "$(YELLOW)Building $(MANAGER_BIN)...$(NC)"
	$(GO) build -o bin/$(MANAGER_BIN) ./cmd/$(MANAGER_BIN)/
	@echo "$(GREEN)$(MANAGER_BIN) built$(NC)"

# =============================================================================
# 代码质量
# =============================================================================

.PHONY: fmt
fmt: ## 格式化代码
	@echo "$(YELLOW)Formatting Go code...$(NC)"
	$(GOFMT) -s -w .
	@echo "$(GREEN)Code formatted$(NC)"

.PHONY: lint
lint: ## 运行 golangci-lint
	@echo "$(YELLOW)Running linter...$(NC)"
	golangci-lint run --timeout 5m ./...
	@echo "$(GREEN)Lint passed$(NC)"

.PHONY: vet
vet: ## 运行 go vet
	@echo "$(YELLOW)Running go vet...$(NC)"
	$(GO) vet ./...
	@echo "$(GREEN)Vet passed$(NC)"

# =============================================================================
# 测试
# =============================================================================

.PHONY: test
test: ## 运行单元测试
	@echo "$(YELLOW)Running unit tests...$(NC)"
	$(GO) test -v -race -count=1 ./internal/...
	@echo "$(GREEN)Tests passed$(NC)"

.PHONY: test-e2e
test-e2e: ## 运行 E2E 测试（需要 kind 集群）
	@echo "$(YELLOW)Running E2E tests...$(NC)"
	@echo "$(YELLOW)Ensure kind cluster is running and KUBECONFIG is set$(NC)"
	$(GO) test -v -race -count=1 -tags=e2e -timeout 10m ./internal/operator/
	@echo "$(GREEN)E2E tests passed$(NC)"

.PHONY: test-e2e-full
test-e2e-full: ## 全链路 E2E 测试 (本地一键: kind + deploy + test)
	@echo "$(YELLOW)Running full E2E test suite...$(NC)"
	$(GO) test -tags=e2e -v -timeout 30m -count=1 ./test/e2e/...
	@echo "$(GREEN)E2E tests passed$(NC)"

.PHONY: test-e2e-local
test-e2e-local: build image-operator image-manager ## 本地快速 E2E (跳过镜像构建)
	@echo "$(YELLOW)Running E2E tests (reuse images)...$(NC)"
	SKIP_BUILD=1 $(GO) test -tags=e2e -v -timeout 30m -count=1 ./test/e2e/...
	@echo "$(GREEN)E2E tests passed$(NC)"

.PHONY: test-e2e-scenario
test-e2e-scenario: ## 运行单个 E2E 场景 (需 SCENARIO=TestHappyPath)
ifndef SCENARIO
	$(error SCENARIO is required, e.g. SCENARIO=TestHappyPath make test-e2e-scenario)
endif
	@echo "$(YELLOW)Running E2E scenario: $(SCENARIO)$(NC)"
	$(GO) test -tags=e2e -v -timeout 10m -count=1 -run $(SCENARIO) ./test/e2e/...
	@echo "$(GREEN)Scenario $(SCENARIO) passed$(NC)"

.PHONY: test-cover
test-cover: ## 运行测试并生成覆盖率报告
	@echo "$(YELLOW)Running tests with coverage...$(NC)"
	$(GO) test -v -race -count=1 -coverprofile=coverage.out ./internal/...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "$(GREEN)Coverage report: coverage.html$(NC)"

# =============================================================================
# 本地开发环境 (kind) — 一键部署整个开发环境
# =============================================================================

KIND_CLUSTER_NAME ?= release-operator-dev
CUSTOMER_ID ?= localhost001
OPERATOR_IMAGE ?= release-operator:dev
MANAGER_PORT ?= 8080
GRPC_PORT ?= 8443

.PHONY: dev-up
dev-up: ## 一键部署本地开发环境 (从 .env 读取 Harbor 配置)
	@chmod +x scripts/dev-setup.sh
	@if [ -f .env ]; then \
		eval $$(grep -v '^\s*#' .env | sed 's/^/export /'); \
	fi
	@CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
	 CUSTOMER_ID=$(CUSTOMER_ID) \
	 OPERATOR_IMAGE=$(OPERATOR_IMAGE) \
	 MANAGER_HTTP_PORT=$(MANAGER_PORT) \
	 MANAGER_GRPC_PORT=$(GRPC_PORT) \
	 HARBOR_URL="$${HARBOR_URL:-}" \
	 HARBOR_ROBOT_NAME="$${HARBOR_ROBOT_NAME:-}" \
	 HARBOR_ROBOT_TOKEN="$${HARBOR_ROBOT_TOKEN:-}" \
	 scripts/dev-setup.sh deploy

.PHONY: dev-down
dev-down: ## 清理本地开发环境
	@CLUSTER_NAME=$(KIND_CLUSTER_NAME) scripts/dev-setup.sh --cleanup

.PHONY: test-env
test-env: ## 部署本地测试环境 (kind + manager/operator)
	@chmod +x scripts/test-env.sh
	@CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
	 CUSTOMER_ID=$(CUSTOMER_ID) \
	 OPERATOR_IMAGE=$(OPERATOR_IMAGE) \
	 MANAGER_HTTP_PORT=$(MANAGER_PORT) \
	 MANAGER_GRPC_PORT=$(GRPC_PORT) \
	 scripts/test-env.sh deploy

.PHONY: test-env-destroy
test-env-destroy: ## 销毁本地测试环境
	@CLUSTER_NAME=$(KIND_CLUSTER_NAME) scripts/test-env.sh --cleanup

.PHONY: test-env-status
test-env-status: ## 查看本地测试环境状态
	@CLUSTER_NAME=$(KIND_CLUSTER_NAME) scripts/test-env.sh --status

.PHONY: dev-operator
dev-operator: image-operator ## 仅重新构建并部署 operator 到 kind
	@scripts/dev-setup.sh --operator

.PHONY: dev-manager
dev-manager: build-notification ## 本地启动 release-manager
	@echo "$(YELLOW)Starting release-manager locally...$(NC)"
	@echo "$(BLUE)  HTTP: http://localhost:$(MANAGER_PORT)/health$(NC)"
	@echo "$(BLUE)  gRPC: localhost:$(GRPC_PORT)$(NC)"
	./bin/$(MANAGER_BIN) --config configs/manager.example.yaml

.PHONY: dev-web
dev-web: ## 本地启动前端开发服务器
	@echo "$(YELLOW)Starting Vue dev server...$(NC)"
	@echo "$(BLUE)  http://localhost:3000$(NC)"
	cd web && npm install && npm run dev

.PHONY: dev-register
dev-register: ## 注册本地开发客户到 release-manager
	@FINGERPRINT=$$(cat certs/$(CUSTOMER_ID)/fingerprint.txt 2>/dev/null || echo ""); \
	if [ -z "$$FINGERPRINT" ]; then \
		echo "$(RED)证书指纹文件不存在，请先运行 'make dev-up'$(NC)"; \
		exit 1; \
	fi; \
	echo "$(YELLOW)注册客户 $(CUSTOMER_ID)...$(NC)"; \
	curl -X POST http://localhost:$(MANAGER_PORT)/api/v1/customers \
		-H 'Content-Type: application/json' \
		-d "{\"id\":\"$(CUSTOMER_ID)\",\"name\":\"本地开发\",\"operator_endpoint\":\"host.docker.internal:$(GRPC_PORT)\",\"cert_fingerprint\":\"$$FINGERPRINT\",\"enabled\":true}"

# =============================================================================
# Docker 镜像
# =============================================================================

.PHONY: image-operator
image-operator: ## 构建 release-operator Docker 镜像
	@echo "$(YELLOW)Building operator image...$(NC)"
	$(DOCKER) build -f Dockerfile.operator \
		-t $(OPERATOR_IMAGE) .
	@echo "$(GREEN)Operator image built$(NC)"

.PHONY: image-manager
image-manager: ## 构建 release-manager Docker 镜像
	@echo "$(YELLOW)Building manager image...$(NC)"
	$(DOCKER) build -f Dockerfile.manager \
		-t release-manager:dev .
	@echo "$(GREEN)Manager image built$(NC)"

.PHONY: image
image: image-operator image-notification ## 构建所有 Docker 镜像

# =============================================================================
# 清理
# =============================================================================

.PHONY: docs
docs: ## 生成 OpenAPI v3 文档 (swag init)
	@echo "$(YELLOW)Generating OpenAPI docs...$(NC)"
	swag init -g cmd/release-manager/main.go \
		--output api/openapi \
		--ot go,json,yaml \
		--parseDependency \
		--parseInternal \
		--requiredByDefault
	@echo "$(GREEN)OpenAPI docs generated: api/openapi/$(NC)"

.PHONY: docs-serve
docs-serve: ## 本地预览 Swagger UI
	@echo "Open http://localhost:8081/swagger/index.html"
	@docker run --rm -p 8081:8080 \
		-v $(PWD)/api/openapi:/usr/share/nginx/html \
		-e SWAGGER_JSON=/usr/share/nginx/html/swagger.json \
		swaggerapi/swagger-ui

.PHONY: docs-lint
docs-lint: ## 验证 OpenAPI spec 合法性
	@which swagger >/dev/null || go install github.com/go-swagger/go-swagger/cmd/swagger@latest
	swagger validate api/openapi/swagger.json

.PHONY: clean
clean: ## 清理构建产物
	@echo "$(YELLOW)Cleaning...$(NC)"
	rm -rf bin/
	rm -rf $(GEN_DIR)/
	rm -f coverage.out coverage.html
	@echo "$(GREEN)Clean complete$(NC)"

# 默认目标
.DEFAULT_GOAL := help

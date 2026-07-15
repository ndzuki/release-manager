# Release Manager — Development Makefile
# =============================================================================

GO          := go
BUF         := $(shell which buf 2>/dev/null || echo buf)
PROTO_DIR   := api/proto
GEN_DIR     := api/gen
GOBIN       := $(shell go env GOBIN 2>/dev/null || echo $(HOME)/go/bin)

# Colors (via printf for portable escape)
ESC         := $(shell printf '\e')
GREEN       := $(ESC)[32m
YELLOW      := $(ESC)[33m
BLUE        := $(ESC)[34m
RED         := $(ESC)[31m
NC          := $(ESC)[0m

# Ports
MANAGER_PORT := 8081
GRPC_PORT    := 8444

# ---------------------------------------------------------------------------
# Multi-service build & run
# ---------------------------------------------------------------------------
SERVICES := webhook orchestrator operator auth notifier api
BIN_DIR  := bin

.PHONY: build-all
build-all: $(addprefix build-,$(SERVICES)) ## Build all microservices

.PHONY: build-webhook
build-webhook: proto ## Build release-webhook
	@echo "$(BLUE)building release-webhook...$(NC)"
	$(GO) build -o $(BIN_DIR)/release-webhook ./cmd/webhook/

.PHONY: build-orchestrator
build-orchestrator: proto ## Build release-orchestrator
	$(GO) build -o $(BIN_DIR)/release-orchestrator ./cmd/orchestrator/

.PHONY: build-operator
build-operator: proto ## Build release-operator
	$(GO) build -o $(BIN_DIR)/release-operator ./cmd/operator/

.PHONY: build-auth
build-auth: proto ## Build release-auth
	$(GO) build -o $(BIN_DIR)/release-auth ./cmd/auth/

.PHONY: build-notifier
build-notifier: proto ## Build release-notifier
	$(GO) build -o $(BIN_DIR)/release-notifier ./cmd/notifier/

.PHONY: build-api
build-api: proto ## Build release-api
	$(GO) build -o $(BIN_DIR)/release-api ./cmd/api/

.PHONY: run-webhook
run-webhook: build-webhook ## Start release-webhook
	./$(BIN_DIR)/release-webhook --config configs/webhook.dev.yaml

.PHONY: run-orchestrator
run-orchestrator: build-orchestrator ## Start release-orchestrator
	@mkdir -p data
	./$(BIN_DIR)/release-orchestrator --config configs/orchestrator.dev.yaml --db data/orchestrator.db

.PHONY: run-operator
run-operator: build-operator ## Start release-operator
	./$(BIN_DIR)/release-operator --config configs/operator.dev.yaml

.PHONY: run-auth
run-auth: build-auth ## Start release-auth
	./$(BIN_DIR)/release-auth --config configs/auth.dev.yaml

.PHONY: run-notifier
run-notifier: build-notifier ## Start release-notifier
	./$(BIN_DIR)/release-notifier --config configs/notifier.dev.yaml

.PHONY: run-api
run-api: build-api ## Start release-api
	./$(BIN_DIR)/release-api --config configs/api.dev.yaml

.PHONY: dev
dev: proto build-all ## Start all 6 microservices in background
	@mkdir -p data
	@echo "$(YELLOW)Starting all services in background...$(NC)"
	@./$(BIN_DIR)/release-webhook --config configs/webhook.dev.yaml > data/webhook.log 2>&1 & echo $$! > data/webhook.pid
	@./$(BIN_DIR)/release-orchestrator --config configs/orchestrator.dev.yaml --db data/orchestrator.db > data/orchestrator.log 2>&1 & echo $$! > data/orchestrator.pid
	@./$(BIN_DIR)/release-operator --config configs/operator.dev.yaml > data/operator.log 2>&1 & echo $$! > data/operator.pid
	@./$(BIN_DIR)/release-auth --config configs/auth.dev.yaml > data/auth.log 2>&1 & echo $$! > data/auth.pid
	@./$(BIN_DIR)/release-notifier --config configs/notifier.dev.yaml > data/notifier.log 2>&1 & echo $$! > data/notifier.pid
	@./$(BIN_DIR)/release-api --config configs/api.dev.yaml > data/api.log 2>&1 & echo $$! > data/api.pid
	@sleep 1
	@echo ""
	@echo "$(GREEN)All 6 services started.$(NC)"
	@echo "$(BLUE)  webhook:       HTTP :8080  | gRPC :8443$(NC)"
	@echo "$(BLUE)  orchestrator:  HTTP :8083  | gRPC :8446$(NC)"
	@echo "$(BLUE)  operator:      HTTP :8084  | gRPC :8447$(NC)"
	@echo "$(BLUE)  auth:          HTTP :8085  | gRPC :8448$(NC)"
	@echo "$(BLUE)  notifier:      HTTP :8086  | gRPC :8449$(NC)"
	@echo "$(BLUE)  api:           HTTP :8082  | gRPC :8445$(NC)"
	@echo ""
	@echo "$(YELLOW)Kulala shortcuts:$(NC)"
	@echo "  make api-orchestrator  → CreateOperation / PublishRelease"
	@echo "  make api-manager       → REST APIs (Customers / Clusters / Releases)"
	@echo "  make api-operator      → Operator gRPC"
	@echo "  make api-auth          → Auth / Login"
	@echo "  make api-webhook       → Webhook simulation"
	@echo "  make api-audit         → Audit / Notifications"
	@echo ""
	@echo "$(YELLOW)Stop all:$(NC)  make stop-all"
	@echo "$(YELLOW)View logs:$(NC) tail -f data/*.log"

.PHONY: stop-all
stop-all: ## Stop all background microservices
	@echo "$(YELLOW)Stopping all services...$(NC)"
	@for pidf in data/*.pid; do \
		[ -f "$$pidf" ] && kill $$(cat "$$pidf") 2>/dev/null && rm "$$pidf" || true; \
	done
	@for port in 8080 8082 8083 8084 8085 8086 8443 8445 8446 8447 8448 8449; do \
		fuser -k $$port/tcp 2>/dev/null || true; \
	done
	@echo "$(GREEN)All services stopped$(NC)"
# ---------------------------------------------------------------------------
# Kulala integration — open .http files directly in Neovim
# ---------------------------------------------------------------------------
KULALA_DIR := api/kulala

.PHONY: api-auth
api-auth: ## 打开 Auth API 集合 (Kulala)
	@nvim $(KULALA_DIR)/auth.http

.PHONY: api-manager
api-manager: ## 打开 Manager API 集合 (Kulala)
	@nvim $(KULALA_DIR)/manager.http

.PHONY: api-webhook
api-webhook: ## 打开 Webhook 模拟集合 (Kulala)
	@nvim $(KULALA_DIR)/webhook.http

.PHONY: api-operator
api-operator: ## 打开 Operator gRPC 集合 (Kulala)
	@nvim $(KULALA_DIR)/operator.http

.PHONY: api-orchestrator
api-orchestrator: ## 打开 Orchestrator gRPC 集合 (Kulala)
	@nvim $(KULALA_DIR)/orchestrator.http

.PHONY: api-audit
api-audit: ## 打开 Audit/Notification 集合 (Kulala)
	@nvim $(KULALA_DIR)/audit.http

# ---------------------------------------------------------------------------
# Proto generation
# ---------------------------------------------------------------------------
.PHONY: proto
proto: ## Generate protobuf code and lint
	@command -v buf >/dev/null 2>&1 || { go install github.com/bufbuild/buf/cmd/buf@latest && export PATH="$(GOBIN):$$PATH"; }; \
	echo "$(YELLOW)Generating protobuf code...$(NC)"; \
	cd $(PROTO_DIR) && buf generate; \
	echo "$(GREEN)Proto code generated$(NC)"
# ---------------------------------------------------------------------------
# Stage-by-stage local deployment
# Each stage starts only the services needed for its atomic requirements.
# After starting, use the matching Kulala .http file to verify.
# ---------------------------------------------------------------------------

.PHONY: dev-stage-shared
dev-stage-shared: proto ## REQ-009,010,039 — Shared contracts
	@echo "$(YELLOW)Stage: Shared Contracts$(NC)"
	@echo "$(BLUE)No runtime services needed — verify proto generation and lint$(NC)"
	@echo "$(BLUE)Run: golangci-lint run$(NC)"

.PHONY: dev-stage-artifact
dev-stage-artifact: proto ## REQ-011,012 — Artifact ingestion
	@echo "$(YELLOW)Stage: Artifact$(NC)"
	@echo "$(BLUE)  Manager: http://localhost:$(MANAGER_PORT)/health$(NC)"
	@echo "$(BLUE)  ▸ api/webhook.http$(NC)"
	@fuser -k $(MANAGER_PORT)/tcp 2>/dev/null || true
	$(GO) build -o bin/release-manager ./cmd/release-manager/
	./bin/release-manager --config configs/manager.dev.yaml

.PHONY: dev-stage-tenancy
dev-stage-tenancy: proto ## REQ-013,014 — Customer & Cluster
	@echo "$(YELLOW)Stage: Tenancy$(NC)"
	@echo "$(BLUE)  Manager: http://localhost:$(MANAGER_PORT)/health$(NC)"
	@echo "$(BLUE)  ▸ api/manager.http → Customers / Clusters$(NC)"
	@fuser -k $(MANAGER_PORT)/tcp 2>/dev/null || true
	$(GO) build -o bin/release-manager ./cmd/release-manager/
	./bin/release-manager --config configs/manager.dev.yaml

.PHONY: dev-stage-operator
dev-stage-operator: proto ## REQ-015,044,016 — Operator control
	@echo "$(YELLOW)Stage: Operator$(NC)"
	@echo "$(BLUE)  Manager: http://localhost:$(MANAGER_PORT)/health$(NC)"
	@echo "$(BLUE)  ▸ api/manager.http → Operator Enrollment$(NC)"
	@echo "$(BLUE)  ▸ api/operator.http → gRPC calls$(NC)"
	@fuser -k $(MANAGER_PORT)/tcp $(GRPC_PORT)/tcp 2>/dev/null || true
	$(GO) build -o bin/release-manager ./cmd/release-manager/
	./bin/release-manager --config configs/manager.dev.yaml

.PHONY: dev-stage-config
dev-stage-config: proto ## REQ-040,018,068 — ReleaseDefinition & ValuesRevision
	@echo "$(YELLOW)Stage: Release Config$(NC)"
	@echo "$(BLUE)  Manager: http://localhost:$(MANAGER_PORT)/health$(NC)"
	@echo "$(BLUE)  ▸ api/manager.http → Release Definitions / ValuesRevision$(NC)"
	@fuser -k $(MANAGER_PORT)/tcp 2>/dev/null || true
	$(GO) build -o bin/release-manager ./cmd/release-manager/
	./bin/release-manager --config configs/manager.dev.yaml

.PHONY: dev-stage-publish
dev-stage-publish: proto build-orchestrator ## REQ-023,067 — Core pipeline CreateOperation
	@echo "$(YELLOW)Stage: Core Pipeline — Orchestrator$(NC)"
	@echo "$(BLUE)  Orchestrator gRPC: localhost:8446$(NC)"
	@echo "$(BLUE)  ▸ api/kulala/orchestrator.http → CreateOperation / PublishRelease$(NC)"
	@echo "$(BLUE)  ▸ Health: grpc.health.v1.Health/Check$(NC)"
	@mkdir -p data
	@fuser -k 8446/tcp 2>/dev/null || true
	./$(BIN_DIR)/release-orchestrator --config configs/orchestrator.dev.yaml --db data/orchestrator.db

.PHONY: dev-stage-auth
dev-stage-auth: proto ## REQ-025,026,049,027 — Auth & RBAC
	@echo "$(YELLOW)Stage: Auth & RBAC$(NC)"
	@echo "$(BLUE)  Manager: http://localhost:$(MANAGER_PORT)/health$(NC)"
	@echo "$(BLUE)  ▸ api/auth.http → Login / Orgs / Users$(NC)"
	@fuser -k $(MANAGER_PORT)/tcp 2>/dev/null || true
	$(GO) build -o bin/release-manager ./cmd/release-manager/
	./bin/release-manager --config configs/manager.dev.yaml

.PHONY: dev-stage-audit
dev-stage-audit: proto ## REQ-050,029 — Audit
	@echo "$(YELLOW)Stage: Audit$(NC)"
	@echo "$(BLUE)  Manager: http://localhost:$(MANAGER_PORT)/health$(NC)"
	@echo "$(BLUE)  ▸ api/audit.http → Audit Logs$(NC)"
	@fuser -k $(MANAGER_PORT)/tcp 2>/dev/null || true
	$(GO) build -o bin/release-manager ./cmd/release-manager/
	./bin/release-manager --config configs/manager.dev.yaml

.PHONY: dev-stage-full
dev-stage-full: proto ## All services (equivalent to old dev-manager)
	@echo "$(YELLOW)Stage: Full$(NC)"
	@echo "$(BLUE)  Manager: http://localhost:$(MANAGER_PORT)/health$(NC)"
	@echo "$(BLUE)  gRPC: localhost:$(GRPC_PORT)$(NC)"
	@echo "$(BLUE)  ▸ api/*.http — all collections$(NC)"
	@fuser -k $(MANAGER_PORT)/tcp $(GRPC_PORT)/tcp 2>/dev/null || true
	$(GO) build -o bin/release-manager ./cmd/release-manager/
	./bin/release-manager --config configs/manager.dev.yaml

# ---------------------------------------------------------------------------
# Quality
# ---------------------------------------------------------------------------
.PHONY: test
test: ## Run all tests
	$(GO) test -race ./...

.PHONY: lint
lint: ## Run linters
	golangci-lint run

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin/

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------
.PHONY: help
help: ## Show this help
	@echo "$(YELLOW)Release Manager — Dev Targets$(NC)"
	@echo ""
	@grep -E '^[a-zA-Z_.-]+:.*?## .*$$' $(lastword $(MAKEFILE_LIST)) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "$(BLUE)%-25s$(NC) %s\n", $$1, $$2}'

# Release Manager — Development Makefile
# =============================================================================
# Framework: Connect (connectrpc.com/connect)
# Single-port HTTP — serves gRPC, gRPC-Web, and Connect (JSON) from one handler.

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
	$(GO) build -buildvcs=false -o $(BIN_DIR)/release-webhook ./cmd/webhook/

.PHONY: build-orchestrator
build-orchestrator: proto ## Build release-orchestrator
	$(GO) build -buildvcs=false -o $(BIN_DIR)/release-orchestrator ./cmd/orchestrator/

.PHONY: build-operator
build-operator: proto ## Build release-operator
	$(GO) build -buildvcs=false -o $(BIN_DIR)/release-operator ./cmd/operator/

.PHONY: build-auth
build-auth: proto ## Build release-auth
	$(GO) build -buildvcs=false -o $(BIN_DIR)/release-auth ./cmd/auth/

.PHONY: build-notifier
build-notifier: proto ## Build release-notifier
	$(GO) build -buildvcs=false -o $(BIN_DIR)/release-notifier ./cmd/notifier/

.PHONY: build-api
build-api: proto ## Build release-api
	$(GO) build -buildvcs=false -o $(BIN_DIR)/release-api ./cmd/api/

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
	./$(BIN_DIR)/release-api --config configs/api.dev.yaml --db data/api.db --signing-key change-me-in-production

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
	@echo "$(GREEN)All 6 services started.$(NC)  Connect: gRPC + gRPC-Web + JSON on one port"
	@echo "$(BLUE)  webhook:       :8080$(NC)"
	@echo "$(BLUE)  orchestrator:  :8083$(NC)"
	@echo "$(BLUE)  operator:      :8084$(NC)"
	@echo "$(BLUE)  auth:          :8085$(NC)"
	@echo "$(BLUE)  notifier:      :8086$(NC)"
	@echo "$(BLUE)  api:           :8082$(NC)"
	@echo ""
	@echo "$(YELLOW)Kulala shortcuts:$(NC)"
	@echo "  make api-orchestrator  -> CreateOperation / PublishRelease"
	@echo "  make api-manager       -> REST APIs (Customers / Clusters / Releases)"
	@echo "  make api-operator      -> Operator gRPC"
	@echo "  make api-auth          -> Auth / Login"
	@echo "  make api-webhook       -> Webhook simulation"
	@echo "  make api-audit         -> Audit / Notifications"
	@echo ""
	@echo "$(YELLOW)Stop all:$(NC)  make stop-all"
	@echo "$(YELLOW)View logs:$(NC) tail -f data/*.log"

.PHONY: stop-all
stop-all: ## Stop all background microservices
	@echo "$(YELLOW)Stopping all services...$(NC)"
	@for pidf in data/*.pid; do \
		[ -f "$$pidf" ] && kill $$(cat "$$pidf") 2>/dev/null && rm "$$pidf" || true; \
	done
	@for port in 8080 8082 8083 8084 8085 8086; do \
		fuser -k $$port/tcp 2>/dev/null || true; \
	done
	@echo "$(GREEN)All services stopped$(NC)"

# ---------------------------------------------------------------------------
# Kulala integration — open .http files directly in Neovim
# ---------------------------------------------------------------------------
KULALA_DIR := api/kulala

.PHONY: api-auth
api-auth: ## Open Auth API collection (Kulala)
	@nvim $(KULALA_DIR)/auth.http

.PHONY: api-manager
api-manager: ## Open Manager API collection (Kulala)
	@nvim $(KULALA_DIR)/manager.http

.PHONY: api-webhook
api-webhook: ## Open Webhook simulation collection (Kulala)
	@nvim $(KULALA_DIR)/webhook.http

.PHONY: api-operator
api-operator: ## Open Operator gRPC collection (Kulala)
	@nvim $(KULALA_DIR)/operator.http

.PHONY: api-orchestrator
api-orchestrator: ## Open Orchestrator gRPC collection (Kulala)
	@nvim $(KULALA_DIR)/orchestrator.http

.PHONY: api-audit
api-audit: ## Open Audit/Notification collection (Kulala)
	@nvim $(KULALA_DIR)/audit.http

# ---------------------------------------------------------------------------
# Proto generation
# ---------------------------------------------------------------------------
.PHONY: proto
proto: ## Generate protobuf code (Connect + protobuf-go)
	@command -v buf >/dev/null 2>&1 || { go install github.com/bufbuild/buf/cmd/buf@latest && export PATH="$(GOBIN):$$PATH"; }; \
	echo "$(YELLOW)Generating Connect + protobuf code...$(NC)"; \
	buf generate --template $(PROTO_DIR)/buf.gen.yaml; \
	echo "$(GREEN)Proto code generated$(NC)"

# ---------------------------------------------------------------------------
# Stage-by-stage local deployment
# ---------------------------------------------------------------------------

.PHONY: dev-stage-shared
dev-stage-shared: proto ## REQ-009,010,039 — Shared contracts
	@echo "$(YELLOW)Stage: Shared Contracts$(NC)"
	@echo "$(BLUE)No runtime services needed — verify proto generation and lint$(NC)"
	@echo "$(BLUE)Run: golangci-lint run$(NC)"

.PHONY: dev-stage-artifact
dev-stage-artifact: proto ## REQ-011,012 — Artifact ingestion
	@echo "$(YELLOW)Stage: Artifact$(NC)"
	@echo "$(BLUE)  webhook: http://localhost:8080/health$(NC)"
	@echo "$(BLUE)  ▸ api/webhook.http$(NC)"
	@fuser -k 8080/tcp 2>/dev/null || true
	$(GO) run ./cmd/webhook/ --config configs/webhook.dev.yaml

.PHONY: dev-stage-tenancy
dev-stage-tenancy: proto ## REQ-013,014 — Customer & Cluster
	@echo "$(YELLOW)Stage: Tenancy$(NC)"
	@echo "$(BLUE)  Manager: http://localhost:$(MANAGER_PORT)/health$(NC)"
	@echo "$(BLUE)  ▸ api/manager.http -> Customers / Clusters$(NC)"
	@fuser -k $(MANAGER_PORT)/tcp 2>/dev/null || true
	$(GO) run ./cmd/release-manager/ --config configs/manager.dev.yaml

.PHONY: dev-stage-operator
dev-stage-operator: proto ## REQ-015,044,016 — Operator control
	@echo "$(YELLOW)Stage: Operator$(NC)"
	@echo "$(BLUE)  Operator: http://localhost:8084/health$(NC)"
	@echo "$(BLUE)  ▸ api/operator.http$(NC)"
	@fuser -k 8084/tcp 2>/dev/null || true
	$(GO) run ./cmd/operator/ --config configs/operator.dev.yaml

.PHONY: dev-stage-config
dev-stage-config: proto ## REQ-040,018,068 — ReleaseDefinition & ValuesRevision
	@echo "$(YELLOW)Stage: Release Config$(NC)"
	@echo "$(BLUE)  Manager: http://localhost:$(MANAGER_PORT)/health$(NC)"
	@echo "$(BLUE)  ▸ api/manager.http -> Release Definitions / ValuesRevision$(NC)"
	@fuser -k $(MANAGER_PORT)/tcp 2>/dev/null || true
	$(GO) run ./cmd/release-manager/ --config configs/manager.dev.yaml

.PHONY: dev-stage-publish
dev-stage-publish: proto ## REQ-023,067 — Core pipeline CreateOperation
	@echo "$(YELLOW)Stage: Core Pipeline — Orchestrator (Connect)$(NC)"
	@echo "$(BLUE)  Orchestrator: http://localhost:8083/health$(NC)"
	@echo "$(BLUE)  ▸ Connect/JSON: POST http://localhost:8083/orchestrator.v1.OrchestratorService/CreateOperation$(NC)"
	@echo "$(BLUE)  ▸ api/kulala/orchestrator.http$(NC)"
	@mkdir -p data
	@fuser -k 8083/tcp 2>/dev/null || true
	$(GO) run ./cmd/orchestrator/ --config configs/orchestrator.dev.yaml --db data/orchestrator.db

.PHONY: dev-stage-auth
dev-stage-auth: proto ## REQ-025,026,049,027 — Auth & RBAC
	@echo "$(YELLOW)Stage: Auth & RBAC$(NC)"
	@echo "$(BLUE)  Auth: http://localhost:8085/health$(NC)"
	@echo "$(BLUE)  ▸ api/auth.http -> Login / Orgs / Users$(NC)"
	@fuser -k 8085/tcp 2>/dev/null || true
	$(GO) run ./cmd/auth/ --config configs/auth.dev.yaml

.PHONY: dev-stage-audit
dev-stage-audit: proto ## REQ-050,029,030 — Audit, Export & Archive
	@echo "$(YELLOW)Stage: Audit$(NC)"
	@echo "$(BLUE)  API: http://localhost:8087/health$(NC)"
	@echo "$(BLUE)  ▸ api/kulala/audit.http -> Audit Query / Export$(NC)"
	@echo "$(BLUE)  Archive: retention=$(shell grep retention_days configs/api.dev.yaml 2>/dev/null | awk '{print $$2}')d → data/archives/$(NC)"
	@fuser -k 8087/tcp 2>/dev/null || true
	$(GO) run ./cmd/api/ --config configs/api.dev.yaml --db data/api.db --signing-key change-me-in-production

.PHONY: dev-stage-full
dev-stage-full: proto ## All services (equivalent to old dev-manager)
	@echo "$(YELLOW)Stage: Full$(NC)"
	@echo "$(BLUE)  Manager: http://localhost:$(MANAGER_PORT)/health$(NC)"
	@echo "$(BLUE)  ▸ api/*.http — all collections$(NC)"
	@fuser -k $(MANAGER_PORT)/tcp 2>/dev/null || true
	$(GO) run ./cmd/release-manager/ --config configs/manager.dev.yaml

# ---------------------------------------------------------------------------
# Quality
# ---------------------------------------------------------------------------
.PHONY: test
test: ## Run all tests
	$(GO) test -race ./...

.PHONY: test-coverage
test-coverage: ## Run tests with coverage report
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out
	@printf "$(GREEN)Coverage report written to coverage.out$(NC)\n"

.PHONY: lint
lint: ## Run linters
	golangci-lint run

.PHONY: sdk-check
sdk-check: build-sdkcheck ## Run SDK-only static gate (REQ-037)
	$(GO) run ./cmd/sdkcheck/ -exceptions sdkcheck.exceptions.yaml ./...

.PHONY: check-reqs
check-reqs: build-reqcheck ## Validate atomic requirement documents (REQ-039)
	@REQS=$$(find . -path '*/Requirements/REQ-*.md' 2>/dev/null); \
	if [ -n "$$REQS" ]; then \
		$(GO) run ./cmd/reqcheck/ $$REQS; \
	else \
		printf "$(YELLOW)check-reqs: no REQ docs found in repo, skipping$(NC)\n"; \
	fi

.PHONY: quality
quality: sdk-check test-coverage lint check-reqs ## Full quality gate run

.PHONY: build-sdkcheck
build-sdkcheck: proto ## Build sdkcheck
	$(GO) build -buildvcs=false -o $(BIN_DIR)/sdkcheck ./cmd/sdkcheck/

.PHONY: build-reqcheck
build-reqcheck: proto ## Build reqcheck
	$(GO) build -buildvcs=false -o $(BIN_DIR)/reqcheck ./cmd/reqcheck/

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin/ coverage.out

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------
.PHONY: help
help: ## Show this help
	@echo "$(YELLOW)Release Manager — Dev Targets (Connect framework)$(NC)"
	@echo ""
	@grep -E '^[a-zA-Z_.-]+:.*?## .*$$' $(lastword $(MAKEFILE_LIST)) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "$(BLUE)%-25s$(NC) %s\n", $$1, $$2}'

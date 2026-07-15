# Release Manager — Development Makefile
# =============================================================================

GO          := go
BUF         := $(HOME)/go/bin/buf
PROTO_DIR   := api/proto
GEN_DIR     := api/gen

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

.PHONY: api-audit
api-audit: ## 打开 Audit/Notification 集合 (Kulala)
	@nvim $(KULALA_DIR)/audit.http

# ---------------------------------------------------------------------------
# Proto generation
# ---------------------------------------------------------------------------
.PHONY: proto
proto: ## Generate protobuf code and lint
	@echo "$(YELLOW)Generating protobuf code...$(NC)"
	cd $(PROTO_DIR) && $(BUF) generate
	cd $(PROTO_DIR) && $(BUF) lint
	@echo "$(GREEN)Proto code generated and linted$(NC)"

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
dev-stage-publish: proto ## REQ-023,067,019,020,021 — Core publish
	@echo "$(YELLOW)Stage: Publish Core$(NC)"
	@echo "$(BLUE)  Manager: http://localhost:$(MANAGER_PORT)/health$(NC)"
	@echo "$(BLUE)  ▸ api/manager.http → Operations / Webhook$(NC)"
	@echo "$(BLUE)  ▸ api/webhook.http → Harbor simulation$(NC)"
	@fuser -k $(MANAGER_PORT)/tcp 2>/dev/null || true
	$(GO) build -o bin/release-manager ./cmd/release-manager/
	./bin/release-manager --config configs/manager.dev.yaml

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

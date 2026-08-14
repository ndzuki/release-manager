# Release Manager — Development Makefile
# =============================================================================
# Framework: Connect (connectrpc.com/connect)
# Single-port HTTP — serves gRPC, gRPC-Web, and Connect (JSON) from one handler.

GO          := go
BUF         := $(shell which buf 2>/dev/null || echo buf)
PROTO_DIR   := api/proto
GEN_DIR     := api/gen
GOBIN       := $(shell go env GOBIN 2>/dev/null || echo $(HOME)/go/bin)
KIND        ?= kind
DOCKER      ?= docker
KIND_NODE_IMAGE := kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5
WORKLOAD_IMAGE ?= busybox:1.36@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662
ROLLOUT_WATCH_MAX_SECONDS ?= 120


# Colors (via printf for portable escape)
ESC         := $(shell printf '\e')
GREEN       := $(ESC)[32m
YELLOW      := $(ESC)[33m
BLUE        := $(ESC)[34m
RED         := $(ESC)[31m
NC          := $(ESC)[0m
INSTALL_SDK_CLUSTER ?= rm-install-sdk
INSTALL_SDK_KUBECONFIG ?= $(CURDIR)/.tmp-install-sdk-kubeconfig
INSTALL_SDK_PATH ?= $(CURDIR)/.tmp-install-sdk-path
INSTALL_SDK_BINARY ?= $(CURDIR)/.tmp-install-sdk.test
INSTALL_SDK_HOME ?= $(CURDIR)/.tmp-install-sdk-home
INSTALL_SDK_QUARANTINE ?= $(CURDIR)/install-sdk.quarantine.yaml
OPERATOR_IMAGE ?= release-operator:local
OPERATOR_IMAGE_ARCHIVE ?= $(CURDIR)/.tmp-release-operator.tar

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
	./$(BIN_DIR)/release-orchestrator --config configs/orchestrator.dev.yaml --db data/release-manager.db

.PHONY: run-operator
run-operator: build-operator ## Start release-operator
	./$(BIN_DIR)/release-operator --config configs/operator.dev.yaml --db data/release-manager.db

.PHONY: run-auth
run-auth: build-auth ## Start release-auth
	./$(BIN_DIR)/release-auth --config configs/auth.dev.yaml --db data/release-manager.db

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
	@./$(BIN_DIR)/release-orchestrator --config configs/orchestrator.dev.yaml --db data/release-manager.db > data/orchestrator.log 2>&1 & echo $$! > data/orchestrator.pid
	@./$(BIN_DIR)/release-operator --config configs/operator.dev.yaml --db data/release-manager.db > data/operator.log 2>&1 & echo $$! > data/operator.pid
	@./$(BIN_DIR)/release-auth --config configs/auth.dev.yaml --db data/release-manager.db > data/auth.log 2>&1 & echo $$! > data/auth.pid
	@./$(BIN_DIR)/release-notifier --config configs/notifier.dev.yaml > data/notifier.log 2>&1 & echo $$! > data/notifier.pid
	@./$(BIN_DIR)/release-api --config configs/api.dev.yaml > data/api.log 2>&1 & echo $$! > data/api.pid
	@sleep 1
	@echo ""
	@echo "$(GREEN)All 6 services started.$(NC)  Connect: gRPC + gRPC-Web + JSON on one port"
	@echo "$(BLUE)  webhook:       :8082$(NC)"
	@echo "$(BLUE)  orchestrator:  :8083$(NC)"
	@echo "$(BLUE)  operator:      :8084$(NC)"
	@echo "$(BLUE)  auth:           :8085$(NC)"
	@echo "$(BLUE)  notifier:      :8086$(NC)"
	@echo "$(BLUE)  api:            :8087$(NC)"
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
	@for port in 8082 8083 8084 8085 8086 8087; do \
		fuser -k $$port/tcp 2>/dev/null || true; \
	done
	@echo "$(GREEN)All services stopped$(NC)"

# ---------------------------------------------------------------------------
# Development environment (single lifecycle module: deploy/dev/dev.sh)
# ---------------------------------------------------------------------------
DEV_SCRIPT := deploy/dev/dev.sh

.PHONY: dev-up
dev-up: ## Create/converge the full dev environment (idempotent)
	@$(DEV_SCRIPT) up

.PHONY: dev-down
dev-down: ## Delete the 5 managed k3d clusters; retain registry and images
	@$(DEV_SCRIPT) down

.PHONY: dev-seed
dev-seed: ## Write/verify the Development Fixture via the formal Connect API
	@$(DEV_SCRIPT) seed

.PHONY: dev-reset-data
dev-reset-data: ## Dump + rebuild databases and re-seed (requires CONFIRM=1)
	@$(DEV_SCRIPT) reset-data

.PHONY: dev-status
dev-status: ## Print machine-readable data/dev-status.json
	@$(DEV_SCRIPT) status

.PHONY: dev-purge
dev-purge: ## Delete every managed resource incl. registry (requires CONFIRM=1)
	@$(DEV_SCRIPT) purge
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
	$(GO) run ./cmd/operator/ --config configs/operator.dev.yaml --db data/release-manager.db

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
	$(GO) run ./cmd/orchestrator/ --config configs/orchestrator.dev.yaml --db data/release-manager.db

.PHONY: dev-stage-auth
dev-stage-auth: proto ## REQ-025,026,049,027 — Auth & RBAC
	@echo "$(YELLOW)Stage: Auth & RBAC$(NC)"
	@echo "$(BLUE)  Auth: http://localhost:8085/health$(NC)"
	@echo "$(BLUE)  ▸ api/auth.http -> Login / Orgs / Users$(NC)"
	@fuser -k 8085/tcp 2>/dev/null || true
	$(GO) run ./cmd/auth/ --config configs/auth.dev.yaml --db data/release-manager.db

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

.PHONY: test-rollout-watch
test-rollout-watch: ## Create a kind cluster, run integration tests, and tear down
	@set -eu; \
	CLUSTER_NAME="rm-rollout-watch-$$(date +%s)-$$$$"; \
	WORKLOAD_IMAGE="$(WORKLOAD_IMAGE)"; \
	WORKLOAD_IMAGE_DIGEST=$${WORKLOAD_IMAGE##*@}; \
	export ROLLOUT_WATCH_WORKLOAD_IMAGE="$$WORKLOAD_IMAGE"; \
	KUBECONFIG=$$(mktemp); \
	TEST_BINARY=$$(mktemp); \
	export KUBECONFIG; \
	owned=false; \
	cleanup() { \
		if [ "$$owned" = true ]; then $(KIND) delete cluster --name "$$CLUSTER_NAME" --kubeconfig "$$KUBECONFIG" >/dev/null 2>&1 || true; fi; \
		rm -f "$$KUBECONFIG" "$$TEST_BINARY"; \
	}; \
	trap cleanup EXIT INT TERM; \
	for existing in $$($(KIND) get clusters); do \
		if [ "$$existing" = "$$CLUSTER_NAME" ]; then printf "$(RED)refusing to reuse existing kind cluster %s$(NC)\n" "$$CLUSTER_NAME" >&2; exit 1; fi; \
	done; \
	$(DOCKER) pull "$(KIND_NODE_IMAGE)"; \
	$(DOCKER) pull "$$WORKLOAD_IMAGE"; \
	$(GO) run ./cmd/sdkcheck/ -exceptions sdkcheck.exceptions.yaml -build-tags integration ./internal/operator/observer ./test/integration; \
	$(GO) test -c -race -tags=integration -o "$$TEST_BINARY" ./test/integration; \
	owned=true; \
	STARTED_AT=$$(date +%s%N); \
	$(KIND) create cluster --name "$$CLUSTER_NAME" --image "$(KIND_NODE_IMAGE)" --kubeconfig "$$KUBECONFIG" --wait 5m; \
	$(KIND) load docker-image "$$WORKLOAD_IMAGE" --name "$$CLUSTER_NAME"; \
	$(DOCKER) exec "$$CLUSTER_NAME-control-plane" ctr -n k8s.io images tag "$$( $(DOCKER) image inspect "$$WORKLOAD_IMAGE" --format '{{.Id}}' )" "docker.io/library/busybox@$$WORKLOAD_IMAGE_DIGEST" >/dev/null 2>&1 || \
	$(DOCKER) exec "$$CLUSTER_NAME-control-plane" ctr -n k8s.io images tag "import-$$(date +%Y-%m-%d)@$$WORKLOAD_IMAGE_DIGEST" "docker.io/library/busybox@$$WORKLOAD_IMAGE_DIGEST" >/dev/null; \
	( cd test/integration && "$$TEST_BINARY" -test.run '^TestRolloutWatch' -test.count=1 -test.timeout=10m ); \
	ELAPSED_NS=$$(($$(date +%s%N) - $$STARTED_AT)); \
	MAX_NS=$$(( $(ROLLOUT_WATCH_MAX_SECONDS) * 1000000000 )); \
	if [ "$$ELAPSED_NS" -gt "$$MAX_NS" ]; then \
		printf "$(RED)test-rollout-watch exceeded %ss target (%s.%03ds)$(NC)\n" "$(ROLLOUT_WATCH_MAX_SECONDS)" "$$(( $$ELAPSED_NS / 1000000000 ))" "$$(( ($$ELAPSED_NS / 1000000) % 1000 ))" >&2; \
		exit 1; \
	fi; \
	printf "$(GREEN)test-rollout-watch pass (%s.%03ds)$(NC)\n" "$$(( $$ELAPSED_NS / 1000000000 ))" "$$(( ($$ELAPSED_NS / 1000000) % 1000 ))"
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

.PHONY: test-install-sdk
test-install-sdk: ## Run Helm Install SDK integration gate in an isolated kind cluster
	@set -eu; \
		cleanup() { \
			$(KIND) delete cluster --name "$(INSTALL_SDK_CLUSTER)" >/dev/null 2>&1 || true; \
			rm -rf "$(INSTALL_SDK_BINARY)" "$(INSTALL_SDK_KUBECONFIG)" "$(INSTALL_SDK_PATH)" "$(INSTALL_SDK_HOME)"; \
		}; \
		trap cleanup EXIT INT TERM; \
		cleanup; \
		if ! command -v $(KIND) >/dev/null 2>&1; then \
			$(GO) run ./cmd/installgate \
				--quarantine "$(INSTALL_SDK_QUARANTINE)" \
				--scenario cluster-readiness \
				--rule-id cluster_unavailable \
				--message "kind is required"; \
			exit 0; \
		fi; \
		if ! $(KIND) create cluster --name "$(INSTALL_SDK_CLUSTER)" --kubeconfig "$(INSTALL_SDK_KUBECONFIG)" --wait 120s; then \
			$(GO) run ./cmd/installgate \
				--quarantine "$(INSTALL_SDK_QUARANTINE)" \
				--scenario cluster-readiness \
				--rule-id cluster_unavailable \
				--message "kind cluster creation failed"; \
			exit 0; \
		fi; \
		$(GO) test -c -race -tags=integration -o "$(INSTALL_SDK_BINARY)" ./test/integration/; \
		mkdir -p "$(INSTALL_SDK_PATH)" "$(INSTALL_SDK_HOME)"; \
		PATH="$(INSTALL_SDK_PATH)" HOME="$(INSTALL_SDK_HOME)" KUBECONFIG="$(INSTALL_SDK_KUBECONFIG)" \
			"$(INSTALL_SDK_BINARY)" -test.v -test.count=1 -test.run '^TestInstallSDK$$'

.PHONY: check-reqs
check-reqs: build-reqcheck ## Validate atomic requirement documents (REQ-039)
	@REQS=$$(find . -path '*/Requirements/REQ-*.md' 2>/dev/null); \
	if [ -n "$$REQS" ]; then \
		$(GO) run ./cmd/reqcheck/ $$REQS; \
	else \
		printf "$(YELLOW)check-reqs: no REQ docs found in repo, skipping$(NC)\n"; \
	fi


.PHONY: test-rollback-sdk
test-rollback-sdk: ## Run Rollback SDK quality gate (REQ-063)
	$(GO) test -race -tags=integration -count=1 ./test/integration/ -run 'TestRollbackSDK'


.PHONY: docker-build-operator
docker-build-operator: ## Build and save operator image as Docker tarball
	@rm -f "$(OPERATOR_IMAGE_ARCHIVE)"; \
		docker build -f deploy/docker/Dockerfile.operator -t "$(OPERATOR_IMAGE)" .; \
		docker save "$(OPERATOR_IMAGE)" -o "$(OPERATOR_IMAGE_ARCHIVE)"

.PHONY: test-operator-image-sdk-only
test-operator-image-sdk-only: ## Run operator image SDK-only gate (REQ-061)
	@set -eu; \
		image_existed=false; \
		if docker image inspect "$(OPERATOR_IMAGE)" >/dev/null 2>&1; then image_existed=true; fi; \
		cleanup() { \
			rm -f "$(OPERATOR_IMAGE_ARCHIVE)"; \
			if [ "$$image_existed" = false ]; then docker image rm "$(OPERATOR_IMAGE)" >/dev/null 2>&1 || true; fi; \
		}; \
		trap cleanup EXIT INT TERM; \
		$(MAKE) docker-build-operator; \
		$(GO) run ./cmd/imagecheck \
			--archive "$(OPERATOR_IMAGE_ARCHIVE)" \
			--policy imagecheck.operator.yaml \
			--dockerfile deploy/docker/Dockerfile.operator
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

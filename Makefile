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
UPGRADE_SDK_CLUSTER ?= rm-upgrade-sdk
UPGRADE_SDK_KUBECONFIG ?= $(CURDIR)/.tmp-upgrade-sdk-kubeconfig
UPGRADE_SDK_PATH ?= $(CURDIR)/.tmp-upgrade-sdk-path
UPGRADE_SDK_BINARY ?= $(CURDIR)/.tmp-upgrade-sdk.test
UPGRADE_SDK_HOME ?= $(CURDIR)/.tmp-upgrade-sdk-home
UPGRADE_SDK_QUARANTINE ?= $(CURDIR)/upgrade-sdk.quarantine.yaml
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

# TASK-066 formal E2E wiring. The private target assembles the one runtime
# config from REQ-065 status/fixture artifacts; passwords stay in the process
# environment and are never written to this YAML.
E2E_DATA_DIR ?= data
E2E_ENV_CONFIG ?= data/e2e-env-config.yaml
E2E_LOCK_FILE ?= data/dev.lock
E2E_CREDENTIALS_FILE ?= data/dev-credentials.env
E2E_TEST_NAMESPACE ?=
# Restart targets must come from the REQ-065 deployment manifest; callers may
# provide the three concrete names when that artifact is available.
E2E_RESTART_DEPLOYMENTS ?=
E2E_ENVIRONMENT ?=

OUTPUT_DIR ?= ./e2e-results
TIMEOUT ?= 5m
TOTAL_TIMEOUT ?= 25m
PARALLEL ?= false
KEEP_ON_FAILURE ?= false
SNAPSHOT_FULL ?= false
STAGES ?= all
BASELINE_FILE ?= $(OUTPUT_DIR)/baseline.json
ENV_CONFIG ?= $(E2E_ENV_CONFIG)

.PHONY: e2e-env-config
e2e-env-config:
	@set -eu; \
	command -v jq >/dev/null 2>&1 || { echo 'e2e-env-config: jq is required' >&2; exit 2; }; \
	$(MAKE) --no-print-directory dev-status >/dev/null; \
	status_file="$(E2E_DATA_DIR)/dev-status.json"; \
	fixture_file="$(E2E_DATA_DIR)/dev-fixture.json"; \
	test -r "$$status_file" || { echo "e2e-env-config: missing $$status_file" >&2; exit 2; }; \
	test -r "$$fixture_file" || { echo "e2e-env-config: missing $$fixture_file" >&2; exit 2; }; \
	export E2E_RESTART_DEPLOYMENTS="$(E2E_RESTART_DEPLOYMENTS)" E2E_TEST_NAMESPACE="$(E2E_TEST_NAMESPACE)" E2E_ENVIRONMENT="$(E2E_ENVIRONMENT)" E2E_KUBECONFIG="$(abspath $(E2E_DATA_DIR)/kubeconfig.yaml)"; \
	if [ -z "$$E2E_RESTART_DEPLOYMENTS" ] && [ -f "$(E2E_DATA_DIR)/dev-deployments.json" ]; then \
		E2E_RESTART_DEPLOYMENTS="$$(jq -r '(.restart_targets.deployments // .k3d.restart_targets.deployments // []) | join(" ")' "$(E2E_DATA_DIR)/dev-deployments.json")"; export E2E_RESTART_DEPLOYMENTS; \
	fi; \
	if [ -z "$$E2E_RESTART_DEPLOYMENTS" ]; then \
		E2E_RESTART_DEPLOYMENTS="$$(jq -r '(.restart_targets.deployments // .k3d.restart_targets.deployments // []) | join(" ")' "$$status_file")"; export E2E_RESTART_DEPLOYMENTS; \
	fi; \
	if [ -z "$$E2E_RESTART_DEPLOYMENTS" ]; then \
		echo 'e2e-env-config: missing restart deployment manifest' >&2; exit 2; \
	fi; \
	mkdir -p "$$(dirname "$(ENV_CONFIG)")"; \
	tmp_config="$$(mktemp "$(ENV_CONFIG).tmp.XXXXXX")"; \
	trap 'rm -f "$$tmp_config"' EXIT; \
	jq -s --arg environment "$(E2E_ENVIRONMENT)" --arg namespace "$(E2E_TEST_NAMESPACE)" --arg kubeconfig "$(abspath $(E2E_DATA_DIR)/kubeconfig.yaml)" --arg deployments "$$E2E_RESTART_DEPLOYMENTS" '.[0] as $$s | .[1] as $$f | ($$f.definitions // {}) as $$definitions | ($$deployments | split(" ") | map(select(length > 0))) as $$restart | if ($$s.environment_id // "") == "" then error("missing environment_id") elif ($$s.fixture_version // "") == "" then error("missing fixture_version") elif (($$s.endpoints.orchestrator // "") == "" or ($$s.endpoints.webhook // "") == "" or ($$s.endpoints.operator // "") == "" or ($$s.endpoints.auth // "") == "" or ($$s.endpoints.notifier // "") == "" or ($$s.endpoints.web // "") == "") then error("missing endpoint") elif (($$restart | length) != 3 or ($$restart | unique | length) != 3) then error("restart deployment manifest must contain exactly three distinct names") elif (($$definitions["e2e-release-target"].id // "") == "" or ($$definitions["e2e-release-target"].bundle_id // "") == "" or ($$definitions["e2e-release-target"].values_revision_id // "") == "" or ($$definitions["e2e-isolation-target"].id // "") == "" or ($$definitions["e2e-isolation-target"].bundle_id // "") == "" or ($$definitions["e2e-isolation-target"].values_revision_id // "") == "" or ($$definitions["e2e-restart-target"].id // "") == "" or ($$definitions["e2e-restart-target"].bundle_id // "") == "" or ($$definitions["e2e-restart-target"].values_revision_id // "") == "") then error("missing e2e definition field") else {environment: (if $$environment != "" then $$environment else ($$s.profile // "local") end), environment_id: $$s.environment_id, endpoints: {release_orchestrator: $$s.endpoints.orchestrator, release_webhook: $$s.endpoints.webhook, release_operator: $$s.endpoints.operator, release_auth: $$s.endpoints.auth, release_notifier: $$s.endpoints.notifier, release_api: $$s.endpoints.web}, credentials: {e2e_runner: {username: "e2e-runner", password_env: "E2E_RUNNER_PASSWORD"}}, k3d: {kubeconfig: $$kubeconfig, test_namespace: (if $$namespace != "" then $$namespace else "release-manager-dev" end), restart_targets: {namespace: (if $$namespace != "" then $$namespace else "release-manager-dev" end), deployments: $$restart}}, seed: {customers: (($$f.customers // {}) | keys | sort), clusters_per_customer: (if (($$f.customers // {}) | length) == 0 then 0 else (((($$f.clusters // {}) | length) / (($$f.customers // {}) | length)) | floor) end), fixture_version: $$s.fixture_version, expected_identity: {customers: (($$f.customers // {}) | length), clusters: (($$f.clusters // {}) | length), routes_basic: ($$s.fixture_entities.routes // 0), definitions_basic: ([($$definitions | keys[]) | select(startswith("e2e-") | not)] | length), bundles: (if ($$f.bundle.id // "") != "" then 1 else ($$s.fixture_entities.bundles // 0) end), e2e_definition_ids: [($$definitions | keys[]) | select(startswith("e2e-"))] | sort}, e2e_upgrade_targets: [{definition_id: $$definitions["e2e-release-target"].id, bundle_id: $$definitions["e2e-release-target"].bundle_id, values_revision_id: $$definitions["e2e-release-target"].values_revision_id}, {definition_id: $$definitions["e2e-isolation-target"].id, bundle_id: $$definitions["e2e-isolation-target"].bundle_id, values_revision_id: $$definitions["e2e-isolation-target"].values_revision_id}, {definition_id: $$definitions["e2e-restart-target"].id, bundle_id: $$definitions["e2e-restart-target"].bundle_id, values_revision_id: $$definitions["e2e-restart-target"].values_revision_id}]}} end' "$$status_file" "$$fixture_file" > "$$tmp_config"; \
	chmod 600 "$$tmp_config"; \
	mv -f "$$tmp_config" "$(ENV_CONFIG)"

.PHONY: e2e-prerequisite
e2e-prerequisite: dev-up dev-seed ## AC-066-17 prerequisite smoke (versioned gate for upstream chain changes)
	@bash test/e2e/prerequisite/smoke.sh

.PHONY: e2e-prerequisite-ci
e2e-prerequisite-ci: ## AC-066-17 prerequisite smoke with artifact preservation and dev cleanup
	@set -e; \
	mkdir -p e2e-results; \
	trap 'bash test/e2e/prerequisite/capture-logs.sh >/dev/null 2>&1 || true; cp -f data/smoke-result.json e2e-results/ 2>/dev/null || true; make dev-purge CONFIRM=1 >/dev/null 2>&1 || true' EXIT; \
	$(MAKE) dev-up dev-seed; \
	bash test/e2e/prerequisite/smoke.sh; \
	bash test/e2e/prerequisite/capture-logs.sh >/dev/null 2>&1 || true; \
	cp -f data/smoke-result.json e2e-results/ 2>/dev/null || true

.PHONY: e2e-stage
e2e-stage: ## Run selected E2E stages (STAGES=comma-separated list)
	@set -eu; \
	if [ -f "$(E2E_CREDENTIALS_FILE)" ]; then set -a; . "$(E2E_CREDENTIALS_FILE)"; set +a; fi; \
	: "$${E2E_RUNNER_PASSWORD:?E2E_RUNNER_PASSWORD must be set or provided by CI}"; \
	export E2E_RUNNER_PASSWORD E2E_ENV_CONFIG_PATH="$(ENV_CONFIG)" E2E_OUTPUT_DIR="$(OUTPUT_DIR)" E2E_STAGES="$(STAGES)" E2E_TIMEOUT="$(TIMEOUT)" E2E_TOTAL_TIMEOUT="$(TOTAL_TIMEOUT)" E2E_PARALLEL="$(PARALLEL)" E2E_KEEP_ON_FAILURE="$(KEEP_ON_FAILURE)" E2E_SNAPSHOT_FULL="$(SNAPSHOT_FULL)"; \
	set +e; \
	flock -s -n -E 3 "$(E2E_LOCK_FILE)" sh -c '$(MAKE) --no-print-directory ENV_CONFIG="$$E2E_ENV_CONFIG_PATH" e2e-env-config && exec $(GO) run ./cmd/e2e run --stages="$$E2E_STAGES" --timeout="$$E2E_TIMEOUT" --total-timeout="$$E2E_TOTAL_TIMEOUT" --output-dir="$$E2E_OUTPUT_DIR" --parallel="$$E2E_PARALLEL" --keep-on-failure="$$E2E_KEEP_ON_FAILURE" --snapshot-full="$$E2E_SNAPSHOT_FULL" --env-config="$$E2E_ENV_CONFIG_PATH"'; \
	rc=$$?; \
	set -e; \
	if [ $$rc -eq 3 ]; then printf 'environment_locked: data/dev.lock is held by another dev operation\\n' >&2; fi; \
	exit $$rc

.PHONY: e2e-all
e2e-all: ## Run all E2E stages
	@$(MAKE) --no-print-directory e2e-stage STAGES=all

.PHONY: e2e-cleanup
e2e-cleanup: ## Recover E2E resources through the formal cleanup API
	@set -eu; \
	if [ -f "$(E2E_CREDENTIALS_FILE)" ]; then set -a; . "$(E2E_CREDENTIALS_FILE)"; set +a; fi; \
	: "$${E2E_RUNNER_PASSWORD:?E2E_RUNNER_PASSWORD must be set or provided by CI}"; \
	export E2E_RUNNER_PASSWORD E2E_ENV_CONFIG_PATH="$(ENV_CONFIG)" E2E_OUTPUT_DIR="$(OUTPUT_DIR)" E2E_BASELINE_FILE="$(BASELINE_FILE)"; \
	set +e; \
	flock -s -n -E 3 "$(E2E_LOCK_FILE)" sh -c '$(MAKE) --no-print-directory ENV_CONFIG="$$E2E_ENV_CONFIG_PATH" e2e-env-config && exec $(GO) run ./cmd/e2e cleanup --env-config="$$E2E_ENV_CONFIG_PATH" --output-dir="$$E2E_OUTPUT_DIR" --baseline-file="$$E2E_BASELINE_FILE"'; \
	rc=$$?; \
	set -e; \
	if [ $$rc -eq 3 ]; then printf 'environment_locked: data/dev.lock is held by another dev operation\\n' >&2; fi; \
	exit $$rc

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

.PHONY: test-upgrade-sdk
test-upgrade-sdk: ## Run Helm Upgrade SDK integration gate (REQ-086/REQ-062) in an isolated kind cluster
	@set -eu; \
		cleanup() { \
			$(KIND) delete cluster --name "$(UPGRADE_SDK_CLUSTER)" >/dev/null 2>&1 || true; \
			rm -rf "$(UPGRADE_SDK_BINARY)" "$(UPGRADE_SDK_KUBECONFIG)" "$(UPGRADE_SDK_PATH)" "$(UPGRADE_SDK_HOME)"; \
		}; \
		trap cleanup EXIT INT TERM; \
		cleanup; \
		if ! command -v $(KIND) >/dev/null 2>&1; then \
			$(GO) run ./cmd/installgate \
				--quarantine "$(UPGRADE_SDK_QUARANTINE)" \
				--scenario cluster-readiness \
				--rule-id cluster_unavailable \
				--message "kind is required"; \
			exit 0; \
		fi; \
		if ! $(KIND) create cluster --name "$(UPGRADE_SDK_CLUSTER)" --kubeconfig "$(UPGRADE_SDK_KUBECONFIG)" --wait 120s; then \
			$(GO) run ./cmd/installgate \
				--quarantine "$(UPGRADE_SDK_QUARANTINE)" \
				--scenario cluster-readiness \
				--rule-id cluster_unavailable \
				--message "kind cluster creation failed"; \
			exit 0; \
		fi; \
		$(GO) test -c -race -tags=integration -o "$(UPGRADE_SDK_BINARY)" ./test/integration/; \
		mkdir -p "$(UPGRADE_SDK_PATH)" "$(UPGRADE_SDK_HOME)"; \
		PATH="$(UPGRADE_SDK_PATH)" HOME="$(UPGRADE_SDK_HOME)" KUBECONFIG="$(UPGRADE_SDK_KUBECONFIG)" \
			"$(UPGRADE_SDK_BINARY)" -test.v -test.count=1 -test.run '^TestUpgradeSDK$$'

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

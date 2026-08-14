#!/usr/bin/env bash
# dev.sh — single-entry lifecycle module for the Release Manager dev
# environment (REQ-065). Operations:
#
#   dev.sh up          create/converge the full environment (idempotent)
#   dev.sh down        delete the 5 managed k3d clusters, keep registry
#   dev.sh seed        write/verify the Development Fixture via Connect API
#   dev.sh reset-data  dump + rebuild databases and re-seed (CONFIRM=1)
#   dev.sh status      emit machine-readable data/dev-status.json
#   dev.sh purge       delete every managed resource incl. registry (CONFIRM=1)
#
# The Makefile only forwards targets and environment variables — locking,
# error codes, host preflight and ownership gating live here (Deletion Test:
# removing this module would scatter those guarantees back into 6 targets).
set -euo pipefail

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/errors.sh
source "$SCRIPT_DIR/lib/errors.sh"
# shellcheck source=lib/host.sh
source "$SCRIPT_DIR/lib/host.sh"
# shellcheck source=lib/lock.sh
source "$SCRIPT_DIR/lib/lock.sh"
# shellcheck source=lib/ownership.sh
source "$SCRIPT_DIR/lib/ownership.sh"

DEV_DATA_DIR="${DEV_DATA_DIR:-$SCRIPT_DIR/../../data}"
CONTROL_CLUSTER="release-manager-control"
CUSTOMER_CLUSTERS=(dev-customer-a-direct dev-customer-a-cache dev-customer-b-replicated dev-customer-b-mixed)
ALL_CLUSTERS=("$CONTROL_CLUSTER" "${CUSTOMER_CLUSTERS[@]}")
# k3d-managed registry: k3d names the container k3d-<name> and cluster nodes
# reach it as k3d-<name>:5000 on the cluster network (REQ-065 registry).
REGISTRY_NAME="release-manager-registry"
REGISTRY_CONTAINER="k3d-$REGISTRY_NAME"
REGISTRY_PORT=5001
KUSTOMIZE_DIR="deploy/kustomize/dev"
# IMAGE_TAGS maps each service to the content-sha256 tag recorded by
# build_and_push so kustomize_apply can substitute the static `:dev`
# references with the exact digest tags pushed in Stage 4 (REQ-065 digest
# contract).
declare -A IMAGE_TAGS=()

# log — user-facing progress on stdout; diagnostics go to stderr (REQ-065).
log() {
  printf '%s\n' "$*"
}

# trap handler — release the flock and preserve the primary error code.
cleanup_trap() {
  local rc=$?
  release_lock
  # CI profile (REQ-065): dev-up exit auto-tries to clean managed resources
  # so a CI job never leaks clusters/registry. Cleanup runs in a subshell and
  # never overrides the primary exit code; on failure it prints
  # `dev_purge_failed` plus a JSON-lines residual manifest for the CI
  # post-step to act on.
  if [ "${DEV_PROFILE:-local}" = "ci" ] && [ -n "${E2E_RUN_ID:-}" ]; then
    (
      set +e
      if ! ci_auto_purge >/dev/null 2>&1; then
        ci_residual_manifest >&2
        printf 'dev_purge_failed: automatic cleanup of managed resources failed; run make dev-purge CONFIRM=1\n' >&2
      fi
    )
  fi
  exit "$rc"
}

# ci_auto_purge — delete every resource in the ownership whitelist.
ci_auto_purge() {
  local manifest name
  manifest="$(ownership_read)"
  printf '%s' "$manifest" | sed -nE 's/.*"docker_containers"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p' | tr ',' '\n' | sed -E 's/^[[:space:]]*"//; s/"[[:space:]]*$//' | while IFS= read -r name || [ -n "$name" ]; do
    [ -n "$name" ] || continue
    docker rm -f "$name" >/dev/null 2>&1 || true
  done
  printf '%s' "$manifest" | sed -nE 's/.*"docker_networks"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p' | tr ',' '\n' | sed -E 's/^[[:space:]]*"//; s/"[[:space:]]*$//' | while IFS= read -r name || [ -n "$name" ]; do
    [ -n "$name" ] || continue
    docker network rm "$name" >/dev/null 2>&1 || true
  done
  printf '%s' "$manifest" | sed -nE 's/.*"k3d_clusters"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p' | tr ',' '\n' | sed -E 's/^[[:space:]]*"//; s/"[[:space:]]*$//' | while IFS= read -r name || [ -n "$name" ]; do
    [ -n "$name" ] || continue
    k3d cluster delete "$name" >/dev/null 2>&1 || true
  done
}

# ci_residual_manifest — JSON-lines residual list of resources that still
# exist after an attempted cleanup (REQ-065 ci profile contract).
ci_residual_manifest() {
  local manifest name
  manifest="$(ownership_read)"
  printf '%s' "$manifest" | sed -nE 's/.*"docker_containers"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p' | tr ',' '\n' | sed -E 's/^[[:space:]]*"//; s/"[[:space:]]*$//' | while IFS= read -r name || [ -n "$name" ]; do
    [ -n "$name" ] || continue
    if docker container inspect "$name" >/dev/null 2>&1; then
      printf '{"resource_type":"docker_container","name":"%s"}\n' "$name"
    fi
  done
  printf '%s' "$manifest" | sed -nE 's/.*"k3d_clusters"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p' | tr ',' '\n' | sed -E 's/^[[:space:]]*"//; s/"[[:space:]]*$//' | while IFS= read -r name || [ -n "$name" ]; do
    [ -n "$name" ] || continue
    if k3d cluster list 2>/dev/null | grep -qw "$name"; then
      printf '{"resource_type":"k3d_cluster","name":"%s"}\n' "$name"
    fi
  done
}

# wait_for_endpoint <url> <seconds> — poll an HTTP endpoint until 200.
wait_for_endpoint() {
  local url="$1"
  local seconds="${2:-180}"
  local deadline=$((SECONDS + seconds))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if curl --fail --silent --show-error "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  return 1
}

# require_readyz <service> <port> — fail service_unhealthy with the service
# name when /readyz does not answer 200 within the timeout (AC-065-10).
require_readyz() {
  local service="$1"
  local port="$2"
  if ! wait_for_endpoint "http://127.0.0.1:$port/readyz" 180; then
    fail "$ERR_SERVICE_UNHEALTHY" "$service /readyz did not return 200 on port $port"
  fi
  log "  $service       http://localhost:$port/readyz  200"
}

# ---------------------------------------------------------------------------
# Stage 1: host preflight + environment lock
# ---------------------------------------------------------------------------
stage_preflight() {
  preflight_up
  ownership_init
}

# ---------------------------------------------------------------------------
# Stage 2: registry
# ---------------------------------------------------------------------------
registry_up() {
  local registries
  registries="$(k3d registry list 2>/dev/null || true)"
  if ! printf '%s' "$registries" | grep -qw "$REGISTRY_NAME"; then
    require_no_conflict container "$REGISTRY_CONTAINER"
    if ! k3d registry create "$REGISTRY_NAME" --port "127.0.0.1:${REGISTRY_PORT}:5000" --image registry:3; then
      fail "$ERR_REGISTRY_UNREACHABLE" "k3d failed to create registry $REGISTRY_NAME"
    fi
  elif ! docker container inspect --format '{{.State.Running}}' "$REGISTRY_CONTAINER" 2>/dev/null | grep -q '^true$'; then
    docker start "$REGISTRY_CONTAINER" >/dev/null || fail "$ERR_REGISTRY_UNREACHABLE" "cannot start registry container $REGISTRY_CONTAINER"
  fi
  ownership_add docker_containers "$REGISTRY_CONTAINER"
  if ! curl --fail --silent --show-error --retry 20 --retry-delay 1 "http://127.0.0.1:${REGISTRY_PORT}/v2/" >/dev/null; then
    fail "$ERR_REGISTRY_UNREACHABLE" "registry http://127.0.0.1:${REGISTRY_PORT}/v2/ is unavailable"
  fi
  log "  local registry .................... localhost:${REGISTRY_PORT}"
}

# ---------------------------------------------------------------------------
# Stage 3: k3d clusters (5, idempotent, kubeconfigs into data/kubeconfigs)
# ---------------------------------------------------------------------------
cluster_exists() {
  # Buffer the full CLI output before grepping: grep -q exits on the first
  # match and SIGPIPEs the writer (Go CLIs exit on stdout SIGPIPE), which
  # would surface as exit 141 in the lifecycle module.
  local json names
  json="$(k3d cluster list -o json 2>/dev/null || true)"
  if printf '%s' "$json" | grep -q "\"name\":\"$1\""; then
    return 0
  fi
  names="$(k3d cluster list 2>/dev/null || true)"
  printf '%s' "$names" | grep -qw "$1"
}
cluster_up() {
  local cluster="$1"
  if cluster_exists "$cluster"; then
    log "  $cluster (exists)"
    return 0
  fi
  local args=(
    cluster create --name "$cluster"
    --registry-use "$REGISTRY_NAME"
    --registry-config "$SCRIPT_DIR/../k3d/registries.yaml"
    --wait
  )
  if [ "$cluster" = "$CONTROL_CLUSTER" ]; then
    # The control cluster owns the fixed local API port and the
    # loadbalancer mapping 8082-8087 -> NodePort 30082-30087. Customer
    # clusters get k3d-assigned API ports (127.0.0.1-bound by default).
    args+=(--api-port "127.0.0.1:6443")
    args+=(--port "8082-8087:30082-30087@loadbalancer")
  fi
  # k3d-auto wrapper equivalent: when the host runs behind a proxy, inject
  # it into the node containers and force NO_PROXY to cover the registry
  # DNS name (Go httpproxy does not CIDR-match domains; without it nodes
  # route k3d-release-manager-registry through the proxy and fail). With
  # no proxy configured the cluster is created bare (REQ-065 framework).
  if [ -n "${HTTP_PROXY:-}${http_proxy:-}${HTTPS_PROXY:-}${https_proxy:-}" ]; then
    args+=(
      --env "HTTP_PROXY=${HTTP_PROXY:-${http_proxy:-}}"
      --env "HTTPS_PROXY=${HTTPS_PROXY:-${https_proxy:-}}"
      --env "NO_PROXY=k3d-$REGISTRY_NAME,localhost,127.0.0.1${NO_PROXY:+,$NO_PROXY}"
    )
  fi
  if ! k3d "${args[@]}"; then
    fail "$ERR_CLUSTER_CREATE_FAILED" "k3d failed to create $cluster"
  fi
  # kubeconfig into the project data dir, never ~/.kube/config.
  k3d kubeconfig get "$cluster" > "$DEV_DATA_DIR/kubeconfigs/$cluster.yaml"
  ownership_add k3d_clusters "$cluster"
  log "  $cluster (created)"
}

clusters_up() {
  mkdir -p "$DEV_DATA_DIR/kubeconfigs"
  log "[3/7] k3d clusters ...................... "
  for cluster in "${ALL_CLUSTERS[@]}"; do
    cluster_up "$cluster"
  done
  # Merged kubeconfig (REQ-065: kubeconfigs/<cluster>.yaml + kubeconfig.yaml).
  # Merge every cluster that exists; k3d keeps existing contexts untouched.
  local existing=()
  for cluster in "${ALL_CLUSTERS[@]}"; do
    if cluster_exists "$cluster"; then
      existing+=("$cluster")
    fi
  done
  if [ "${#existing[@]}" -gt 0 ]; then
    k3d kubeconfig merge "${existing[@]}" -o "$DEV_DATA_DIR/kubeconfig.yaml" >/dev/null
  fi
}

# ---------------------------------------------------------------------------
# Stage 4: images (content-sha256 tags, push skip when digest unchanged)
# ---------------------------------------------------------------------------
content_hash() {
  local service="$1"
  local inputs=("go.mod" "go.sum")
  inputs+=("cmd/$service")
  inputs+=(internal)
  if [ "$service" = "web" ]; then
    inputs+=(web/package.json web/package-lock.json)
  fi
  if [ "$service" = "fixture" ]; then
    inputs+=(deploy/fixtures/cmd deploy/fixtures/chart)
  fi
  local hash_input=""
  local path
  for path in "${inputs[@]}"; do
    if [ -e "$path" ]; then
      hash_input="$hash_input $(find "$path" -type f -not -name '*_test.go' -not -path '*/.git/*' -print0 2>/dev/null | sort -z | xargs -0 sha256sum 2>/dev/null | sha256sum | cut -d' ' -f1)"
    fi
  done
  printf '%s' "$hash_input" | sha256sum | cut -d' ' -f1
}

build_and_push() {
  local service="$1"
  local hash
  hash="$(content_hash "$service")"
  # Record the tag so kustomize_apply can pin the applied manifests to the
  # exact digest pushed here (recorded before the unchanged-skip return so a
  # cache hit still pins the same tag).
  IMAGE_TAGS["$service"]="$hash"
  local tag="content-sha256:$hash"
  local dockerfile="deploy/docker/Dockerfile.$service"
  if [ "$service" = "fixture" ]; then
    dockerfile="deploy/fixtures/Dockerfile"
  fi
  if docker manifest inspect "localhost:${REGISTRY_PORT}/release-$service:$tag" >/dev/null 2>&1; then
    log "  release-$service:$tag (unchanged)"
    return 0
  fi
  if ! docker build --file "$dockerfile" --tag "localhost:${REGISTRY_PORT}/release-$service:$tag" .; then
    fail "$ERR_DOCKER_BUILD_FAILED" "build failed for release-$service"
  fi
  if ! docker push "localhost:${REGISTRY_PORT}/release-$service:$tag"; then
    fail "$ERR_DOCKER_PUSH_FAILED" "push failed for release-$service:$tag"
  fi
  log "  release-$service:$tag (built & pushed)"
}

images_up() {
  log "[4/7] docker images ..................... "
  local service
  for service in webhook orchestrator operator auth notifier web fixture notification-sink; do
    build_and_push "$service"
  done
}

# ---------------------------------------------------------------------------

kustomize_apply() {
  log "[5/7] kustomize apply ................... "
  local manifest svc hash
  if ! manifest="$(kustomize build "$KUSTOMIZE_DIR")"; then
    fail "$ERR_KUSTOMIZE_BUILD_FAILED" "kustomize build failed for $KUSTOMIZE_DIR"
  fi
  # Substitute each static `release-<svc>:dev` reference with the recorded
  # content-sha256 digest tag so the applied manifests pin the exact images
  # built and pushed in Stage 4 (REQ-065 digest contract). The substitution
  # runs over the built manifest in memory; no files are rewritten.
  for svc in "${!IMAGE_TAGS[@]}"; do
    hash="${IMAGE_TAGS[$svc]:-}"
    [ -n "$hash" ] || continue
    manifest="$(printf '%s\n' "$manifest" | sed "s#release-$svc:dev#release-$svc:content-sha256:$hash#g")"
  done
  if ! printf '%s\n' "$manifest" | kubectl apply -f -; then
    fail "$ERR_SERVICE_UNHEALTHY" "kubectl apply failed for $KUSTOMIZE_DIR"
  fi
}

readiness() {
  log "[6/7] readiness ......................... "
  require_readyz webhook 8082
  require_readyz orchestrator 8083
  require_readyz operator 8084
  require_readyz auth 8085
  require_readyz notifier 8086
  # web has no /readyz; the root page is the probe.
  if ! wait_for_endpoint "http://127.0.0.1:8087" 180; then
    fail "$ERR_SERVICE_UNHEALTHY" "web did not answer on port 8087"
  fi
  log "  web           http://localhost:8087         200"
}

# ---------------------------------------------------------------------------
# Stage 6: seed (delegates to the devfixture runner)
# ---------------------------------------------------------------------------
seed() {
  log "[7/7] seed data .......................... "
  local seed_output
  if ! seed_output="$(go run ./cmd/devseed/ --orchestrator http://127.0.0.1:8083 --webhook http://127.0.0.1:8082 --auth http://127.0.0.1:8085 2>&1)"; then
    # AC-065-18: an operator session that never reached online reports its
    # own error code with the faulting cluster name (devfixture verify).
    if printf '%s' "$seed_output" | grep -q "operator_not_online"; then
      fail "$ERR_OPERATOR_NOT_ONLINE" "$seed_output"
    fi
    fail "$ERR_SEED_WRITE_FAILED" "devseed failed: $seed_output"
  fi
  log "  seed data .......................... DONE"
}

# ---------------------------------------------------------------------------
# Operations
# ---------------------------------------------------------------------------
cmd_up() {
  stage_preflight
  acquire_lock up
  trap cleanup_trap EXIT INT TERM
  log "=== Release Manager Dev Environment ==="
  log "Profile: ${DEV_PROFILE:-local} | environment_id: $(environment_id)"
  registry_up
  clusters_up
  images_up
  kustomize_apply
  readiness
  seed
  log "=== Environment ready ==="
}

cmd_down() {
  acquire_lock down
  trap cleanup_trap EXIT INT TERM
  local cluster
  for cluster in "${ALL_CLUSTERS[@]}"; do
    if ! ownership_contains k3d_clusters "$cluster"; then
      if cluster_exists "$cluster"; then
        printf 'unmanaged cluster %s exists without ownership record; skipped\n' "$cluster" >&2
      fi
      continue
    fi
    if cluster_exists "$cluster"; then
      k3d cluster delete "$cluster" || true
      ownership_remove k3d_clusters "$cluster"
      log "  removed cluster $cluster"
    fi
  done
  log "registry and image cache retained"
}

cmd_seed() {
  acquire_lock seed
  trap cleanup_trap EXIT INT TERM
  seed
}

cmd_status() {
  acquire_lock status shared
  local status_file="$DEV_DATA_DIR/dev-status.json"
  local fixture_file="$DEV_DATA_DIR/dev-fixture.json"
  # REQ-065 status schema keys for the five clusters.
  local -A cluster_keys=(
    [release-manager-control]=control
    [dev-customer-a-direct]=ca_direct
    [dev-customer-a-cache]=ca_cache
    [dev-customer-b-replicated]=cb_replicated
    [dev-customer-b-mixed]=cb_mixed
  )
  printf '{"environment_id":"%s","profile":"%s","clusters":{' \
    "$(environment_id)" "${DEV_PROFILE:-local}" > "$status_file"
  local first=1 cluster key status
  for cluster in "${ALL_CLUSTERS[@]}"; do
    [ "$first" -eq 1 ] || printf ',' >> "$status_file"
    key="${cluster_keys[$cluster]:-$cluster}"
    if cluster_exists "$cluster"; then status="ready"; else status="absent"; fi
    printf '"%s":{"name":"%s","status":"%s"}' "$key" "$cluster" "$status" >> "$status_file"
    first=0
  done
  printf '},"registry":"localhost:%s","endpoints":{' "$REGISTRY_PORT" >> "$status_file"
  local -a endpoint_names=(webhook orchestrator operator auth notifier web)
  local -a endpoint_ports=(8082 8083 8084 8085 8086 8087)
  first=1
  for i in "${!endpoint_names[@]}"; do
    [ "$first" -eq 1 ] || printf ',' >> "$status_file"
    printf '"%s":"http://localhost:%s"' "${endpoint_names[$i]}" "${endpoint_ports[$i]}" >> "$status_file"
    first=0
  done
  printf '},' >> "$status_file"
  # Fixture-derived counters come from data/dev-fixture.json when present;
  # a missing fixture (never seeded) reports zeros.
  local sessions installs customers clusters routes definitions values bundles
  sessions="$(fixture_counter "$fixture_file" operator_sessions)"
  installs="$(fixture_counter "$fixture_file" bootstrap_installs)"
  customers="$(fixture_counter "$fixture_file" customers)"
  clusters="$(fixture_counter "$fixture_file" clusters)"
  routes="$(fixture_counter "$fixture_file" routes)"
  definitions="$(fixture_counter "$fixture_file" definitions)"
  values="$(fixture_counter "$fixture_file" values_revisions)"
  bundles="$(fixture_counter "$fixture_file" bundles)"
  printf '"operator_sessions":%s,"fixture_version":"%s","fixture_entities":{' \
    "$sessions" "$FIXTURE_VERSION" >> "$status_file"
  printf '"customers":%s,"clusters":%s,"routes":%s,"definitions":%s,"values_revisions":%s,"bundles":%s},' \
    "$customers" "$clusters" "$routes" "$definitions" "$values" "$bundles" >> "$status_file"
  printf '"bootstrap_installs":%s}\n' "$installs" >> "$status_file"
  cat "$status_file"
}

fixture_counter() {
  local file="$1"
  local key="$2"
  local value=""
  if [ -f "$file" ]; then
    value="$(sed -nE "s/.*\"$key\"[[:space:]]*:[[:space:]]*([0-9]+).*/\1/p" "$file" | sed -n '1p')"
  fi
  printf '%s' "${value:-0}"
}

cmd_reset_data() {
  require_confirm "$ERR_CONFIRM_REQUIRED"
  require_pg_tools
  acquire_lock reset-data
  trap cleanup_trap EXIT INT TERM
  log "=== dev-reset-data ==="
  # The reset path rebuilds the two PostgreSQL databases from schema zero
  # (migrate down -all -> up, executed by devseed --reset through the
  # golang-migrate SDK) and re-seeds the canonical fixture. pg_dump is the
  # safety net: on seed failure both databases are restored and the
  # environment is marked partial (REQ-065 snapshot recovery).
  if ! kubectl -n release-manager-dev get deployment postgres >/dev/null 2>&1; then
    fail "$ERR_SERVICE_UNHEALTHY" "postgres deployment not found; dev-reset-data requires a running environment"
  fi
  # 1. maintenance: stop writes on the maintenance-capable services
  #    (ADR-015 procedure-level maintenance gate; MAINTENANCE env overrides
  #    the config file per internal/config env mapping).
  kubectl -n release-manager-dev set env deployment/orchestrator deployment/auth deployment/notifier MAINTENANCE=true >/dev/null \
    || fail "$ERR_SERVICE_UNHEALTHY" "cannot enable maintenance mode"
  kubectl -n release-manager-dev rollout status deployment/orchestrator deployment/auth deployment/notifier --timeout=180s >/dev/null \
    || fail "$ERR_SERVICE_UNHEALTHY" "maintenance rollout did not converge"
  log "  maintenance enabled (writes stopped)"

  # 2. dump both databases through a port-forward to the host pg_dump.
  local pf_pid=""
  kubectl -n release-manager-dev port-forward svc/postgres 5432:5432 >/dev/null 2>&1 &
  pf_pid=$!
  # The trap releases the environment lock; the port-forward dies with the
  # process group on exit, but kill it explicitly to avoid a stray listener.
  trap 'kill "$pf_pid" 2>/dev/null || true; cleanup_trap' EXIT INT TERM
  local ts dump_files=() db dump_file
  ts="$(date +%Y%m%dT%H%M%S%z)"
  mkdir -p "$DEV_DATA_DIR/backups"
  for db in release_manager release_notifier; do
    dump_file="$DEV_DATA_DIR/backups/dump-$ts-$db.sql"
    if ! PGPASSWORD=dev-release-manager pg_dump -Fc -h 127.0.0.1 -p 5432 -U release_manager -d "$db" -f "$dump_file" >/dev/null 2>&1; then
      fail "$ERR_SERVICE_UNHEALTHY" "pg_dump failed for $db (backup $dump_file); environment untouched"
    fi
    dump_files+=("$dump_file")
    log "  dumped $db -> $dump_file"
  done

  # 3. rebuild the 4 customer clusters (control cluster and registry stay).
  local cluster
  for cluster in "${CUSTOMER_CLUSTERS[@]}"; do
    if cluster_exists "$cluster"; then
      k3d cluster delete "$cluster" >/dev/null 2>&1 || true
      ownership_remove k3d_clusters "$cluster"
    fi
    cluster_up "$cluster"
  done

  # 4. schema rebuild + canonical re-seed via devseed --reset (golang-migrate
  #    SDK down -all -> up, then the nine phases; see internal/devfixture).
  if ! go run ./cmd/devseed/ --reset \
    --orchestrator http://127.0.0.1:8083 --webhook http://127.0.0.1:8082 --auth http://127.0.0.1:8085 \
    --database-dsn "postgres://release_manager:dev-release-manager@127.0.0.1:5432/release_manager?sslmode=disable"; then
    # 5a. failure: restore both databases, drop the half-built customer
    #     clusters, mark the environment partial; a later reset converges.
    log "  seed failed; restoring databases"
    for db in release_manager release_notifier; do
      dump_file="$DEV_DATA_DIR/backups/dump-$ts-$db.sql"
      if ! PGPASSWORD=dev-release-manager pg_restore -Fc -h 127.0.0.1 -p 5432 -U release_manager -d "$db" "$dump_file" >/dev/null 2>&1; then
        printf 'restore failed for %s (backup %s); manual recovery required\n' "$db" "$dump_file" >&2
      fi
    done
    for cluster in "${CUSTOMER_CLUSTERS[@]}"; do
      if cluster_exists "$cluster"; then
        k3d cluster delete "$cluster" >/dev/null 2>&1 || true
        ownership_remove k3d_clusters "$cluster"
      fi
    done
    kubectl -n release-manager-dev set env deployment/orchestrator deployment/auth deployment/notifier MAINTENANCE- >/dev/null 2>&1 || true
    fail "$ERR_FIXTURE_CONFLICT" "reset failed; databases restored and environment marked partial (rerun dev-reset-data to converge)"
  fi

  # 5b. success: drop the safety-net dumps and restore writes.
  for dump_file in "${dump_files[@]}"; do
    rm -f "$dump_file"
  done
  kubectl -n release-manager-dev set env deployment/orchestrator deployment/auth deployment/notifier MAINTENANCE- >/dev/null \
    || fail "$ERR_SERVICE_UNHEALTHY" "cannot disable maintenance mode"
  kubectl -n release-manager-dev rollout status deployment/orchestrator deployment/auth deployment/notifier --timeout=180s >/dev/null \
    || fail "$ERR_SERVICE_UNHEALTHY" "maintenance rollout did not converge"
  log "=== dev-reset-data complete ==="
}

cmd_purge() {
  require_confirm "$ERR_DEV_PURGE_CONFIRM_REQUIRED"
  acquire_lock purge
  trap cleanup_trap EXIT INT TERM
  local manifest
  manifest="$(ownership_read)"
  local name
  # Containers and networks from the whitelist, registry included.
  printf '%s' "$manifest" | sed -nE 's/.*"docker_containers"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p' | tr ',' '\n' | sed -E 's/^[[:space:]]*"//; s/"[[:space:]]*$//' | while IFS= read -r name || [ -n "$name" ]; do
    [ -n "$name" ] || continue
    docker rm -f "$name" >/dev/null 2>&1 || true
    log "  removed container $name"
  done
  printf '%s' "$manifest" | sed -nE 's/.*"docker_networks"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p' | tr ',' '\n' | sed -E 's/^[[:space:]]*"//; s/"[[:space:]]*$//' | while IFS= read -r name || [ -n "$name" ]; do
    [ -n "$name" ] || continue
    docker network rm "$name" >/dev/null 2>&1 || true
    log "  removed network $name"
  done
  printf '%s' "$manifest" | sed -nE 's/.*"k3d_clusters"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p' | tr ',' '\n' | sed -E 's/^[[:space:]]*"//; s/"[[:space:]]*$//' | while IFS= read -r name || [ -n "$name" ]; do
    [ -n "$name" ] || continue
    k3d cluster delete "$name" >/dev/null 2>&1 || true
    log "  removed cluster $name"
  done
  log "dev-purge complete"
}

usage() {
  printf 'usage: %s {up|down|seed|reset-data|status|purge}\n' "$0" >&2
  exit 1
}

case "${1:-}" in
  up) cmd_up ;;
  down) cmd_down ;;
  seed) cmd_seed ;;
  reset-data) cmd_reset_data ;;
  status) cmd_status ;;
  purge) cmd_purge ;;
  *) usage ;;
esac

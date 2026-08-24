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
# Fixture version authority = the devseed built-in constant (REQ-065 D1,
# AC-065-30): dev-up/dev-seed never auto-increment. The fallback keeps the
# preflight battery working when the Go toolchain is unavailable.
FIXTURE_VERSION="${FIXTURE_VERSION:-$(go run ./cmd/devseed/ -print-fixture-version 2>/dev/null || true)}"
FIXTURE_VERSION="${FIXTURE_VERSION:-v2}"
export FIXTURE_VERSION
# shellcheck source=lib/ownership.sh
source "$SCRIPT_DIR/lib/ownership.sh"

DEV_DATA_DIR="${DEV_DATA_DIR:-$SCRIPT_DIR/../../data}"
# DEV_TIMEOUT_* runtime knobs (REQ-065 D5, AC-065-28): defaults are the
# deterministic base; each may be overridden per invocation via the
# environment and is never persisted to any state file.
DEV_TIMEOUT_READY="${DEV_TIMEOUT_READY:-300}"
DEV_TIMEOUT_OPERATOR="${DEV_TIMEOUT_OPERATOR:-180}"
DEV_TIMEOUT_SEED_RETRIES="${DEV_TIMEOUT_SEED_RETRIES:-3}"
# Runtime files dev-purge removes (AC-065-26): credentials, keys, kubeconfigs
# and state documents. data/archive/ is explicitly preserved.
PURGE_DATA_PATHS=(dev-credentials.env dev-trust-root dev-jwt kubeconfigs kubeconfig.yaml dev-ownership.json dev-fixture.json dev-seed-progress.json dev-status.json backups)
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
  # The ci JWT key file is transient (D3): remove it on every exit path —
  # including failures that never reached the post-apply cleanup.
  jwt_ci_temp_cleanup
  # CI profile (REQ-065 D4, AC-065-27): auto-clean managed resources ONLY on
  # a non-zero exit (failure or INT/TERM — bash reports 128+signal). A
  # successful dev-up keeps the environment for REQ-066 consumption; the CI
  # post-step `make dev-purge CONFIRM=1` is the teardown backstop. Cleanup
  # runs in a subshell and never overrides the primary exit code; on failure
  # it prints `dev_purge_failed` plus a JSON-lines residual manifest for the
  # CI post-step to act on.
  if [ "$rc" -ne 0 ] && [ "${DEV_PROFILE:-local}" = "ci" ] && [ -n "${E2E_RUN_ID:-}" ]; then
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
# name when /readyz does not answer 200 within DEV_TIMEOUT_READY (AC-065-10/28).
require_readyz() {
  local service="$1"
  local port="$2"
  if ! wait_for_endpoint "http://127.0.0.1:$port/readyz" "$DEV_TIMEOUT_READY"; then
    fail "$ERR_SERVICE_UNHEALTHY" "$service /readyz did not return 200 on port $port"
  fi
  log "  $service       http://localhost:$port/readyz  200"
}

# ---------------------------------------------------------------------------
# JWT signing key (REQ-065 D3): local profile generates/reuses a 0600
# data/dev-jwt/jwt-signing-key.pem before deployment; the ci profile injects
# the DEV_JWT_SIGNING_KEY Secret env into the same source path transiently
# (removed right after apply, never persisted).
# ---------------------------------------------------------------------------
jwt_key_path() { printf '%s' "$DEV_DATA_DIR/dev-jwt/jwt-signing-key.pem"; }

jwt_signing_key_ensure() {
  local key_path
  key_path="$(jwt_key_path)"
  if [ "${DEV_PROFILE:-local}" = "ci" ]; then
    if [ -z "${DEV_JWT_SIGNING_KEY:-}" ]; then
      fail "$ERR_SERVICE_UNHEALTHY" "ci profile requires DEV_JWT_SIGNING_KEY (JWT signing key is not written to disk)"
    fi
    # ci: the Secret env value is materialized to the kustomize source path
    # only for the duration of the build/apply (D3: not persisted) and is
    # removed by jwt_ci_temp_cleanup on every exit path.
    mkdir -p "$DEV_DATA_DIR/dev-jwt"
    umask 077
    printf '%s' "$DEV_JWT_SIGNING_KEY" > "$key_path"
    chmod 600 "$key_path"
    return 0
  fi
  if [ -f "$key_path" ] && [ -s "$key_path" ]; then
    log "  jwt signing key (reused) .......... $key_path"
    return 0
  fi
  mkdir -p "$DEV_DATA_DIR/dev-jwt"
  # 88-char base64 of 64 random bytes; no external tool dependency. The key
  # is consumed as raw bytes by the auth JWT manager (any non-empty value is
  # valid); rotation = delete the file and re-run dev-up (kustomize then
  # regenerates the Secret hash and rolls the consuming Deployments).
  umask 077
  head -c 64 /dev/urandom | base64 > "$key_path"
  chmod 600 "$key_path"
  log "  jwt signing key (generated) ....... $key_path"
}

# jwt_ci_temp_cleanup — remove the transient ci JWT key file after apply
# (D3: the ci profile never leaves secret material on disk). No-op for local.
jwt_ci_temp_cleanup() {
  [ "${DEV_PROFILE:-local}" = "ci" ] || return 0
  rm -f "$(jwt_key_path)" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Stage 1: host preflight + environment lock
# ---------------------------------------------------------------------------
stage_preflight() {
  # ownership_init must run first: it mkdirs DEV_DATA_DIR, and the disk
  # preflight (require_disk -> df -Pk $DEV_DATA_DIR) emits nothing for a
  # missing directory, failing AC-065-21 on a pristine checkout where
  # data/ is gitignored and module-generated.
  ownership_init
  preflight_up
  # The port gate (AC-065-07) targets foreign occupiers on a clean host.
  # When a managed cluster already exists its loadbalancer owns 8082-8087
  # by design — an interrupted dev-up must resume idempotently (AC-065-02)
  # instead of failing its own port mapping. Port checks after preflight_up
  # so k3d availability is already verified.
  if ! clusters_exist; then
    require_ports_free
  fi
}

# clusters_exist — 1 when any managed k3d cluster is running (partial or
# full environment present).
clusters_exist() {
  local clusters
  clusters="$(k3d cluster list 2>/dev/null || true)"
  local cluster
  for cluster in "${ALL_CLUSTERS[@]}"; do
    if printf '%s' "$clusters" | grep -qw "$cluster"; then
      return 0
    fi
  done
  return 1
}

# ---------------------------------------------------------------------------
# Stage 2: registry
# ---------------------------------------------------------------------------
registry_up() {
  registries="$(k3d registry list 2>/dev/null || true)"
  if ! printf '%s' "$registries" | grep -qw "$REGISTRY_NAME"; then
    require_no_conflict container "$REGISTRY_CONTAINER"
    if ! k3d registry create "$REGISTRY_NAME" --port "127.0.0.1:${REGISTRY_PORT}" --image registry:3; then
      fail "$ERR_REGISTRY_UNREACHABLE" "k3d failed to create registry $REGISTRY_NAME"
    fi
  elif ! docker container inspect --format '{{.State.Running}}' "$REGISTRY_CONTAINER" 2>/dev/null | grep -q '^true$'; then
    docker start "$REGISTRY_CONTAINER" >/dev/null || fail "$ERR_REGISTRY_UNREACHABLE" "cannot start registry container $REGISTRY_CONTAINER"
  fi
  ownership_add docker_containers "$REGISTRY_CONTAINER"
  # First creation pulls registry:3 and starts the container; allow a wide
  # readiness window (connection reset/refused until the daemon listens).
  if ! curl --fail --silent --show-error --retry 90 --retry-delay 2 --retry-connrefused "http://127.0.0.1:${REGISTRY_PORT}/v2/" >/dev/null; then
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
    # k3d v5 takes the cluster name positionally: `k3d cluster create
    # <name> [flags]` — there is no --name flag (REQ-065 k3d >= 5.8).
    cluster create "$cluster"
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
  # docker distribution references forbid ':' inside the tag; REQ-065's
  # literal `content-sha256:<hex>` is not a valid docker tag (real smoke:
  # "invalid reference format"). Keep the content-addressed semantics with
  # the '-' separator.
  local tag="content-sha256-$hash"
  local dockerfile="deploy/docker/Dockerfile.$service"
  if [ "$service" = "fixture" ]; then
    dockerfile="deploy/fixtures/Dockerfile"
  fi
  if docker manifest inspect "localhost:${REGISTRY_PORT}/release-$service:$tag" >/dev/null 2>&1; then
    log "  release-$service:$tag (unchanged)"
    return 0
  fi
  # Proxy build-args (same contract as the k3d node injection below): a host
  # behind a proxy must pass it into the build container or `go mod download`
  # inside the Dockerfile fails (Go modules resolve through the proxy). The
  # value is the caller's, matching the node injection (REQ-065 framework).
  local build_args=()
  if [ -n "${HTTP_PROXY:-}${http_proxy:-}${HTTPS_PROXY:-}${https_proxy:-}" ]; then
    build_args+=(
      --build-arg "HTTP_PROXY=${HTTP_PROXY:-${http_proxy:-}}"
      --build-arg "HTTPS_PROXY=${HTTPS_PROXY:-${https_proxy:-}}"
      --build-arg "NO_PROXY=localhost,127.0.0.1${NO_PROXY:+,$NO_PROXY}"
    )
  fi
  # GOPROXY build-arg: the container's default proxy.golang.org is
  # unreachable from CN hosts (real-smoke failure); forward the host's go
  # module proxy so go mod download resolves (go env GOPROXY on CI hosts is
  # the default proxy.golang.org, so the fallback only guards missing go).
  build_args+=(--build-arg "GOPROXY=${GOPROXY:-$(go env GOPROXY 2>/dev/null || printf 'https://proxy.golang.org,direct')}")
  # Container-image mirror build-args (web): DEV_DOCKER_MIRROR is a registry
  # prefix such as docker.1ms.run/library/ used when Docker Hub is
  # unreachable from the host (CN); empty keeps the official tags (CI).
  if [ "$service" = "web" ] && [ -n "${DEV_DOCKER_MIRROR:-}" ]; then
    build_args+=(
      --build-arg "NODE_IMAGE=${DEV_DOCKER_MIRROR}node:24-alpine"
      --build-arg "NGINX_IMAGE=${DEV_DOCKER_MIRROR}nginx:1.27-alpine"
    )
  fi
  if ! docker build "${build_args[@]}" --file "$dockerfile" --tag "localhost:${REGISTRY_PORT}/release-$service:$tag" .; then
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
  # LoadRestrictionsNone: the JWT secret source lives in the data dir
  # (outside the kustomization dir); kustomize's default security loader
  # rejects it. The escape is scoped to the dev lifecycle build (D3).
  if ! manifest="$(kustomize build --load-restrictor LoadRestrictionsNone "$KUSTOMIZE_DIR")"; then
    fail "$ERR_KUSTOMIZE_BUILD_FAILED" "kustomize build failed for $KUSTOMIZE_DIR"
  fi
  # Substitute each static `release-<svc>:dev` reference with the recorded
  # content-sha256 digest tag so the applied manifests pin the exact images
  # built and pushed in Stage 4 (REQ-065 digest contract). The substitution
  # runs over the built manifest in memory; no files are rewritten.
  for svc in "${!IMAGE_TAGS[@]}"; do
    hash="${IMAGE_TAGS[$svc]:-}"
    [ -n "$hash" ] || continue
    manifest="$(printf '%s\n' "$manifest" | sed "s#release-$svc:dev#release-$svc:content-sha256-$hash#g")"
  done
  if ! printf '%s\n' "$manifest" | kubectl apply -f -; then
    fail "$ERR_SERVICE_UNHEALTHY" "kubectl apply failed for $KUSTOMIZE_DIR"
  fi
  # ci profile: the transient JWT key file served the kustomize build above;
  # the Secret lives in the cluster now and the file is removed (D3).
  jwt_ci_temp_cleanup
}

readiness() {
  log "[6/7] readiness ......................... "
  require_readyz webhook 8082
  require_readyz orchestrator 8083
  require_readyz operator 8084
  require_readyz auth 8085
  require_readyz notifier 8086
  # web has no /readyz; the root page is the probe.
  if ! wait_for_endpoint "http://127.0.0.1:8087" "$DEV_TIMEOUT_READY"; then
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
  # DEV_TIMEOUT_OPERATOR bounds the operator-online wait inside devseed;
  # DEV_TIMEOUT_SEED_RETRIES bounds phase-write retries (AC-065-28).
  if ! seed_output="$(go run ./cmd/devseed/ --orchestrator http://127.0.0.1:8083 --webhook http://127.0.0.1:8082 --auth http://127.0.0.1:8085 --operator-timeout "$DEV_TIMEOUT_OPERATOR" --seed-retries "$DEV_TIMEOUT_SEED_RETRIES" 2>&1)"; then
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
  # D3: the JWT signing key must exist before kustomize build/apply (the
  # secretGenerator sources it); generate/reuse happens here, before any
  # deployment stage.
  jwt_signing_key_ensure
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
  # dev-seed precondition (REQ-065 D3): the JWT signing key must already be
  # injected — dev-up generates it before deployment. Seed never generates
  # the key itself: a missing key means dev-up has not converged yet.
  if [ "${DEV_PROFILE:-local}" != "ci" ] && { [ ! -f "$(jwt_key_path)" ] || [ ! -s "$(jwt_key_path)" ]; }; then
    fail "$ERR_SERVICE_UNHEALTHY" "JWT signing key $(jwt_key_path) missing; run make dev-up first"
  fi
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
    --operator-timeout "$DEV_TIMEOUT_OPERATOR" --seed-retries "$DEV_TIMEOUT_SEED_RETRIES" \
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

# purge_data_runtime — remove the data/ runtime files (AC-065-26). The
# whitelist covers credentials, keys, kubeconfigs and state documents;
# data/archive/ (fixture progress generations) is always preserved. The
# dev.lock file is deleted last while still held — a third process cannot
# have observed a purged environment yet.
purge_data_runtime() {
  local path
  for path in "${PURGE_DATA_PATHS[@]}"; do
    rm -rf "$DEV_DATA_DIR/$path"
  done
  rm -f "$DEV_DATA_DIR/dev.lock"
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
  purge_data_runtime
  log "data/ runtime files removed (data/archive/ preserved)"
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

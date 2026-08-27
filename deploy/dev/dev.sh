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
PURGE_DATA_PATHS=(dev-credentials.env dev-trust-root dev-jwt dev-service-tokens dev-ca diagnostics kubeconfigs kubeconfig.yaml dev-ownership.json dev-fixture.json dev-seed-progress.json dev-status.json backups)
CONTROL_CLUSTER="release-manager-control"
# k3d names the kubeconfig context "k3d-<cluster>".
CONTROL_CTX="k3d-$CONTROL_CLUSTER"
CUSTOMER_CLUSTERS=(dev-customer-a-direct dev-customer-a-cache dev-customer-b-replicated dev-customer-b-mixed)
ALL_CLUSTERS=("$CONTROL_CLUSTER" "${CUSTOMER_CLUSTERS[@]}")
# ctl_kubectl — kubectl against the MANAGEMENT cluster: the merged project
# kubeconfig plus the explicit control context. Without both, kubectl falls
# back to ~/.kube/config (or its ambient context) and management manifests
# land on a customer cluster (real smoke 2026-08-24 regression).
ctl_kubectl() {
  KUBECONFIG="$DEV_DATA_DIR/kubeconfig.yaml" kubectl --context "$CONTROL_CTX" "$@"
}
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
# Chained traps (smoke_fixture_version / cmd_reset_data) run housekeeping
# commands before this handler; those commands would reset $?, so callers
# pass the ORIGINAL exit status via TRAP_RC. A bare `trap cleanup_trap` keeps
# the plain $? semantics (cleanup_trap is then the trap's first command).
cleanup_trap() {
  local rc="${TRAP_RC:-$?}"
  unset TRAP_RC
  release_lock
  # The ci JWT key / service token / mTLS CA files are transient (D3 /
  # 批次3 D2 / 批次5 D1): remove them on every exit path — including
  # failures that never reached the post-apply cleanup.
  jwt_ci_temp_cleanup
  service_token_ci_temp_cleanup
  mtls_ca_ci_temp_cleanup
  # Failure diagnostics (批次5 D6, AC-065-39): collect describe/get/logs
  # BEFORE any ci auto-purge so the evidence survives the teardown (REQ-065:
  # "ci profile 失败自动清理路径同样先落盘诊断再清理"). Collection runs in a
  # subshell, never overrides the primary exit code, and emits only a
  # one-line stderr summary.
  if [ "$rc" -ne 0 ]; then
    collect_diagnostics "$rc"
  fi
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

# collect_diagnostics — on a failed run capture kubectl describe/get/logs
# into data/diagnostics/<ISO8601>/ (0600, 批次5 D6, AC-065-39) for offline
# root-causing. Only the management cluster is inspected; nothing runs when
# no environment was ever applied (no merged kubeconfig). Failures here are
# best-effort: they must never override the primary error code.
collect_diagnostics() {
  local rc="$1"
  local dir="$DEV_DATA_DIR/diagnostics/$(date -u +%Y%m%dT%H%M%SZ)"
  [ -f "$DEV_DATA_DIR/kubeconfig.yaml" ] || return 0
  command -v kubectl >/dev/null 2>&1 || return 0
  (
    set +e
    umask 077
    mkdir -p "$dir"
    chmod 700 "$dir"
    ctl_kubectl -n release-manager-dev get pods -o wide > "$dir/pods.txt" 2>/dev/null
    ctl_kubectl -n release-manager-dev get deployments,services,configmaps,secrets > "$dir/resources.txt" 2>/dev/null
    ctl_kubectl -n release-manager-dev get events --sort-by=.lastTimestamp > "$dir/events.txt" 2>/dev/null
    ctl_kubectl -n release-manager-dev describe pods > "$dir/describe-pods.txt" 2>/dev/null
    # Per-pod recent logs; one file per pod (name hashed into the filename
    # to stay flat and shell-safe).
    local pod hash
    while IFS= read -r pod; do
      [ -n "$pod" ] || continue
      hash="$(printf '%s' "$pod" | sha256sum | cut -c1-12)"
      ctl_kubectl -n release-manager-dev logs "pod/$pod" --all-containers --tail=100 --timestamps \
        > "$dir/log-$hash.txt" 2>/dev/null || true
    done < <(ctl_kubectl -n release-manager-dev get pods -o name 2>/dev/null | sed 's#^pod/##' || true)
    find "$dir" -type f -exec chmod 600 {} + 2>/dev/null || true
  ) || true
  printf 'diagnostics collected to %s (exit %s)\n' "$dir" "$rc" >&2
}

# ci_auto_purge — delete every resource in the ownership whitelist.
# D-017 teardown order: clusters first, then their kubeconfigs/ownership
# entries, then the networks (the registry is disconnected from each network
# BEFORE removal — k3d registry_up joins the registry to every cluster
# network and docker refuses to remove a network with live endpoints, real
# smoke ②), then the containers incl. the registry and its data volume.
ci_auto_purge() {
  local manifest name registry_vol
  registry_vol=""
  if ownership_contains docker_containers "$REGISTRY_CONTAINER"; then
    registry_vol="$(registry_volume)"
  fi
  manifest="$(ownership_read)"
  printf '%s' "$manifest" | sed -nE 's/.*"k3d_clusters"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p' | tr ',' '\n' | sed -E 's/^[[:space:]]*"//; s/"[[:space:]]*$//' | while IFS= read -r name || [ -n "$name" ]; do
    [ -n "$name" ] || continue
    k3d cluster delete "$name" >/dev/null 2>&1 || true
    rm -f "$DEV_DATA_DIR/kubeconfigs/$name.yaml"
    ownership_remove k3d_clusters "$name"
  done
  rm -f "$DEV_DATA_DIR/kubeconfig.yaml"
  printf '%s' "$manifest" | sed -nE 's/.*"docker_networks"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p' | tr ',' '\n' | sed -E 's/^[[:space:]]*"//; s/"[[:space:]]*$//' | while IFS= read -r name || [ -n "$name" ]; do
    [ -n "$name" ] || continue
    network_teardown "$name"
    ownership_remove docker_networks "$name"
  done
  printf '%s' "$manifest" | sed -nE 's/.*"docker_containers"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p' | tr ',' '\n' | sed -E 's/^[[:space:]]*"//; s/"[[:space:]]*$//' | while IFS= read -r name || [ -n "$name" ]; do
    [ -n "$name" ] || continue
    docker rm -f "$name" >/dev/null 2>&1 || true
  done
  registry_volume_remove "$registry_vol"
}

# network_teardown <network> — remove one cluster network in the D-017
# order: disconnect the registry container AND the management node from the
# network FIRST (a network with live endpoints cannot be removed), then
# remove the network itself. Idempotent: an already-gone network is a no-op.
network_teardown() {
  local network="$1"
  if docker network inspect "$network" >/dev/null 2>&1; then
    if docker container inspect "$REGISTRY_CONTAINER" >/dev/null 2>&1; then
      docker network disconnect -f "$network" "$REGISTRY_CONTAINER" >/dev/null 2>&1 || true
    fi
    if docker container inspect "k3d-$CONTROL_CLUSTER-server-0" >/dev/null 2>&1; then
      docker network disconnect -f "$network" "k3d-$CONTROL_CLUSTER-server-0" >/dev/null 2>&1 || true
    fi
    docker network rm "$network" >/dev/null 2>&1 || true
  fi
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

# require_tcp_ready <service> <port> — readiness for non-HTTP listeners
# (the operator mTLS gateway): the port must accept TCP connections within
# DEV_TIMEOUT_READY (AC-065-10/28). DEV_OPERATOR_GATEWAY_PORT (test seam)
# redirects the probe without touching the preflight port band — fake-CLI
# tests bind a loopback listener there; unset uses the production port.
require_tcp_ready() {
  local service="$1"
  local port="$2"
  if [ -n "${DEV_OPERATOR_GATEWAY_PORT:-}" ]; then
    port="$DEV_OPERATOR_GATEWAY_PORT"
  fi
  local deadline=$((SECONDS + DEV_TIMEOUT_READY))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
      # The fd-close must live in a subshell: `exec` redirections are
      # PERMANENT for the shell, so a bare `exec 3>&- 2>/dev/null` would
      # silently redirect every later stderr write (fail messages,
      # diagnostics summary) into /dev/null (real trap-chain bug caught by
      # fake-CLI tests: the fixture /version smoke failure vanished).
      (exec 3>&-) 2>/dev/null || true
      log "  $service       localhost:$port            tcp-open"
      return 0
    fi
    sleep 2
  done
  fail "$ERR_SERVICE_UNHEALTHY" "$service gateway did not accept TCP on port $port"
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
# Bundle ingress service token (REQ-065 批次3 D2, AC-065-33): local profile
# generates/reuses a 0600 data/dev-service-tokens/webhook-service-token before
# deployment (rotation = delete the file and re-run dev-up); the ci profile
# injects the DEV_WEBHOOK_SERVICE_TOKEN Secret env transiently for the
# kustomize build (never persisted). Lifetime is independent from the JWT key
# and Dev Trust Root directories (REQ-065 security boundary).
# ---------------------------------------------------------------------------
service_token_path() { printf '%s' "$DEV_DATA_DIR/dev-service-tokens/webhook-service-token"; }

service_token_ensure() {
  local token_path
  token_path="$(service_token_path)"
  if [ "${DEV_PROFILE:-local}" = "ci" ]; then
    if [ -z "${DEV_WEBHOOK_SERVICE_TOKEN:-}" ]; then
      fail "$ERR_SERVICE_UNHEALTHY" "ci profile requires DEV_WEBHOOK_SERVICE_TOKEN (bundle ingress service token is not written to disk)"
    fi
    # ci: materialized only for the kustomize build/apply duration (D2: not
    # persisted) and removed by service_token_ci_temp_cleanup on every exit.
    mkdir -p "$DEV_DATA_DIR/dev-service-tokens"
    umask 077
    printf '%s' "$DEV_WEBHOOK_SERVICE_TOKEN" > "$token_path"
    chmod 600 "$token_path"
    return 0
  fi
  if [ -f "$token_path" ] && [ -s "$token_path" ]; then
    log "  webhook service token (reused) .. $token_path"
    return 0
  fi
  mkdir -p "$DEV_DATA_DIR/dev-service-tokens"
  # 32-char [A-Za-z0-9] token — same charset contract as dev-credentials.env
  # passwords (REQ-065 D2) so the value stays safe unquoted in env files and
  # Kubernetes Secrets. crypto-grade entropy via /dev/urandom, no external
  # tool dependency.
  umask 077
  head -c 1024 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 32 > "$token_path"
  chmod 600 "$token_path"
  log "  webhook service token (generated) . $token_path"
}

# service_token_ci_temp_cleanup — remove the transient ci service token file
# after apply (批次3 D2: the ci profile never leaves secret material on
# disk). No-op for local.
service_token_ci_temp_cleanup() {
  [ "${DEV_PROFILE:-local}" = "ci" ] || return 0
  rm -f "$(service_token_path)" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Dev mTLS CA (REQ-065 批次5 D1, AC-065-36): the operator gateway's mTLS CA
# is generated by dev-up BEFORE deployment into 0600 data/dev-ca/ (ca.key +
# ca.crt), injected by kustomize into the orchestrator gateway mount, so the
# seed enrollment phase never races the CA's first-time generation (REQ-015
# 决策#2 dev 文件降级). Generation/reuse semantics live in the devseed
# helper (cmd/devseed -ensure-mtls-ca): it reuses an existing parseable CA
# and regenerates a missing/corrupt one — the exact format the operator's
# ca.Load consumes. Rotation = delete data/dev-ca/ and re-run dev-up. The ci
# profile materializes DEV_M_TLS_CA_KEY / DEV_M_TLS_CA_CERT transiently for
# the kustomize build and never persists them.
# ---------------------------------------------------------------------------
mtls_ca_key_path() { printf '%s' "$DEV_DATA_DIR/dev-ca/ca.key"; }
mtls_ca_cert_path() { printf '%s' "$DEV_DATA_DIR/dev-ca/ca.crt"; }

mtls_ca_ensure() {
  local dir="$DEV_DATA_DIR/dev-ca"
  if [ "${DEV_PROFILE:-local}" = "ci" ]; then
    if [ -z "${DEV_M_TLS_CA_KEY:-}" ] || [ -z "${DEV_M_TLS_CA_CERT:-}" ]; then
      fail "$ERR_SERVICE_UNHEALTHY" "ci profile requires DEV_M_TLS_CA_KEY and DEV_M_TLS_CA_CERT (the dev mTLS CA is not written to disk)"
    fi
    mkdir -p "$dir"
    umask 077
    printf '%s' "$DEV_M_TLS_CA_KEY" > "$(mtls_ca_key_path)"
    printf '%s' "$DEV_M_TLS_CA_CERT" > "$(mtls_ca_cert_path)"
    chmod 600 "$(mtls_ca_key_path)" "$(mtls_ca_cert_path)"
    return 0
  fi
  # The helper owns the full contract (exists+parseable → reuse, missing or
  # corrupt → regenerate); dev-up always delegates so corrupt material is
  # regenerated instead of bricking the orchestrator gateway (ca.Load fails
  # closed on an unparseable pair). -ensure-mtls-ca is a bool flag: the
  # target dir travels in the separate -mtls-ca-dir flag (Go flag semantics
  # — a bool flag never consumes a positional value).
  if ! go run ./cmd/devseed/ -ensure-mtls-ca -mtls-ca-dir "$dir"; then
    fail "$ERR_SERVICE_UNHEALTHY" "cannot generate/reuse the dev mTLS CA in $dir (go toolchain required)"
  fi
  [ -s "$(mtls_ca_key_path)" ] && [ -s "$(mtls_ca_cert_path)" ] \
    || fail "$ERR_SERVICE_UNHEALTHY" "dev mTLS CA helper produced no files in $dir"
  chmod 600 "$(mtls_ca_key_path)" "$(mtls_ca_cert_path)" 2>/dev/null || true
  chmod 700 "$dir" 2>/dev/null || true
  log "  dev mTLS CA (ensured) ............ $dir"
}

# mtls_ca_ci_temp_cleanup — remove the transient ci mTLS CA files after
# apply (批次5 D1: the ci profile never leaves secret material on disk).
mtls_ca_ci_temp_cleanup() {
  [ "${DEV_PROFILE:-local}" = "ci" ] || return 0
  rm -f "$(mtls_ca_key_path)" "$(mtls_ca_cert_path)" 2>/dev/null || true
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
    # The memory gate (AC-065-20) has the same resume contract: 5 k3d
    # clusters commit ~3 GiB of the 12 GiB budget, so a running (or
    # partially created) environment's own footprint must not fail its own
    # re-run. On a clean host the gate still fires before any creation.
    require_memory
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
# registry_identity_labels — the --label arguments shared by registry
# creation and relabeling: the k3d identification labels (so `k3d registry
# list` and `k3d cluster create --registry-use` keep resolving the container)
# plus the REQ-065 managed/profile labels (AC-065-32). Mirrors k3d v5.8's own
# label set; the version label is read from the live k3d binary when possible.
registry_identity_labels() {
  local k3d_ver=""
  k3d_ver="$(k3d version 2>/dev/null | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)"
  printf -- "--label app=k3d --label k3d.cluster= --label k3d.role=registry --label k3d.version=%s --label k3d.registry.host=127.0.0.1 --label k3d.registry.hostIP=127.0.0.1 --label k3s.registry.port.external=%s --label k3s.registry.port.internal=5000 --label %s=true --label %s=%s" \
    "${k3d_ver:-v5.8.3}" "$REGISTRY_PORT" "$MANAGED_LABEL" "$PROFILE_LABEL" "${DEV_PROFILE:-local}"
}

# registry_volume — the /var/lib/registry mount source (image cache lives
# here): a named volume for dev.sh-created registries, the anonymous volume
# for legacy k3d-created ones. Empty output means the container had no mount.
registry_volume() {
  docker container inspect --format '{{range .Mounts}}{{if eq .Destination "/var/lib/registry"}}{{.Name}}{{end}}{{end}}' "$REGISTRY_CONTAINER" 2>/dev/null || true
}

# registry_volume_remove — delete the registry data volume when it is
# whitelist-managed (dev-purge / ci failure auto-purge only; dev-down keeps
# it as the image cache per the REQ-065 contract). The caller must capture
# the volume name BEFORE the registry container is removed.
registry_volume_remove() {
  local volume="$1"
  [ -n "$volume" ] || return 0
  docker volume rm "$volume" >/dev/null 2>&1 || true
}

# registry_create — create the registry container directly via Docker so the
# managed/profile labels are present from the start (AC-065-32). k3d
# `registry create` offers no label flag and Docker labels are immutable
# after creation, so k3d cannot produce a labeled container; the k3d
# identification labels above keep `k3d registry list` /
# `k3d cluster create --registry-use` working unchanged. The named volume
# makes the image cache survive dev-down and relabeling.
registry_create() {
  local volume_source="$REGISTRY_CONTAINER"
  # shellcheck disable=SC2086 # label arguments are deliberately word-split
  if ! docker container create \
    --name "$REGISTRY_CONTAINER" \
    $(registry_identity_labels) \
    --publish "127.0.0.1:${REGISTRY_PORT}:5000" \
    --volume "$volume_source:/var/lib/registry" \
    registry:3 >/dev/null; then
    fail "$ERR_REGISTRY_UNREACHABLE" "cannot create registry container $REGISTRY_CONTAINER"
  fi
  log "  registry container (created, labeled)"
}

# registry_relabel — a legacy registry container (created by k3d before the
# AC-065-32 label contract) is recreated in place with the identity labels:
# same image, same port binding, same /var/lib/registry volume (image cache
# preserved). Docker cannot update labels on an existing container, so
# stop/rm/create is the only faithful mechanism (REQ-065 决策 D8: 补打 label).
registry_relabel() {
  local image volume
  image="$(docker container inspect --format '{{.Image}}' "$REGISTRY_CONTAINER" 2>/dev/null || true)"
  [ -n "$image" ] || fail "$ERR_REGISTRY_UNREACHABLE" "cannot inspect registry container $REGISTRY_CONTAINER"
  volume="$(registry_volume)"
  docker container rm -f "$REGISTRY_CONTAINER" >/dev/null \
    || fail "$ERR_REGISTRY_UNREACHABLE" "cannot remove registry container $REGISTRY_CONTAINER for relabeling"
  # shellcheck disable=SC2086 # label arguments are deliberately word-split
  if ! docker container create \
    --name "$REGISTRY_CONTAINER" \
    $(registry_identity_labels) \
    --publish "127.0.0.1:${REGISTRY_PORT}:5000" \
    ${volume:+--volume "$volume:/var/lib/registry"} \
    "$image" >/dev/null; then
    fail "$ERR_REGISTRY_UNREACHABLE" "cannot recreate registry container $REGISTRY_CONTAINER"
  fi
  log "  registry container (relabeled)"
}

registry_up() {
  if docker container inspect "$REGISTRY_CONTAINER" >/dev/null 2>&1; then
    # Exists path: a foreign same-named container — unlabeled AND absent from
    # the whitelist — is a resource_conflict (AC-065-22). A whitelisted or
    # managed container is adopted, and a whitelisted container without the
    # labels is relabeled in place (AC-065-32 / D8).
    if ! docker_managed container "$REGISTRY_CONTAINER"; then
      if ! ownership_contains docker_containers "$REGISTRY_CONTAINER"; then
        fail "$ERR_RESOURCE_CONFLICT" \
          "container '$REGISTRY_CONTAINER' exists without label $MANAGED_LABEL=true and is not in the ownership whitelist; remove or label it manually, then retry"
      fi
      registry_relabel
    fi
  else
    require_no_conflict container "$REGISTRY_CONTAINER" docker_containers
    registry_create
  fi
  if ! docker container inspect --format '{{.State.Running}}' "$REGISTRY_CONTAINER" 2>/dev/null | grep -q '^true$'; then
    docker start "$REGISTRY_CONTAINER" >/dev/null || fail "$ERR_REGISTRY_UNREACHABLE" "cannot start registry container $REGISTRY_CONTAINER"
  fi
  ownership_add docker_containers "$REGISTRY_CONTAINER"
  # First creation pulls registry:3 and starts the container; allow a wide
  # readiness window. Real smoke (2026-08-24): before the daemon listens the
  # port accepts connections and then RESETS them — curl reports error 56
  # (Recv failure), which --retry-connrefused alone does not retry; the
  # readiness probe must therefore use --retry-all-errors (curl >= 7.71).
  if ! curl --fail --silent --show-error --retry 90 --retry-delay 2 --retry-connrefused --retry-all-errors "http://127.0.0.1:${REGISTRY_PORT}/v2/" >/dev/null; then
    fail "$ERR_REGISTRY_UNREACHABLE" "registry http://127.0.0.1:${REGISTRY_PORT}/v2/ is unavailable"
  fi
  k3s_images_prewarm
  log "  local registry .................... localhost:${REGISTRY_PORT}"
}

# k3s_images_prewarm — CN-host adaptation (real smoke 2026-08-24): k3d nodes
# pull k3s component images (pause/coredns/traefik/metrics-server/...) from
# docker.io at pod-start time, which is unreachable from CN hosts — every pod
# stays ContainerCreating on the pause sandbox. These images are therefore
# pre-warmed into the local registry under their full `rancher/mirrored-*`
# names; the docker.io mirror in deploy/k3d/registries.yaml routes node pulls
# through it. Already-present manifests are skipped (idempotent, AC-065-02).
k3s_images_prewarm() {
  local images=(
    "rancher/mirrored-pause:3.6"
    "rancher/mirrored-coredns-coredns:1.12.0"
    "rancher/mirrored-library-traefik:2.11.18"
    "rancher/mirrored-metrics-server:v0.7.2"
    "rancher/mirrored-library-busybox:1.36.1"
    "rancher/klipper-helm:v0.9.3-build20241008"
    "rancher/klipper-lb:v0.4.9"
    "rancher/local-path-provisioner:v0.0.30"
    # The management topology pins postgres:16 and redis:7-alpine (REQ-065);
    # the docker.io mirror routes their node-side pulls through the local
    # registry, so the `library/` names must be pre-warmed too (real smoke
    # 2026-08-24: without them postgres/redis stay ImagePullBackOff on CN
    # hosts where docker.io is unreachable).
    "library/postgres:16"
    "library/redis:7-alpine"
  )
  local image
  for image in "${images[@]}"; do
    if docker manifest inspect "localhost:${REGISTRY_PORT}/$image" >/dev/null 2>&1; then
      continue
    fi
    if ! docker image inspect "$image" >/dev/null 2>&1; then
      # pull through the user-configured mirror when Docker Hub is unreachable.
      if ! docker pull "$image" >/dev/null 2>&1; then
        fail "$ERR_REGISTRY_UNREACHABLE" "cannot obtain k3s component image $image (docker.io unreachable and not cached); pre-pull it or set a docker mirror"
      fi
    fi
    docker tag "$image" "localhost:${REGISTRY_PORT}/$image" >/dev/null
    if ! docker push "localhost:${REGISTRY_PORT}/$image" >/dev/null; then
      fail "$ERR_DOCKER_PUSH_FAILED" "push failed for $image"
    fi
    log "  prewarmed $image"
  done
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
# node_memory_for / node_cpu_for — deterministic server-node resources per
# cluster class (REQ-065 批次5 D2, AC-065-37): control cluster 3GiB/2CPU,
# customer clusters 1.5GiB/1CPU. DEV_K3D_NODE_MEMORY / DEV_K3D_NODE_CPU
# override both classes (DEV_TIMEOUT_* pattern: per-invocation, never
# persisted).
node_memory_for() {
  if [ -n "${DEV_K3D_NODE_MEMORY:-}" ]; then
    printf '%s' "$DEV_K3D_NODE_MEMORY"
  elif [ "$1" = "$CONTROL_CLUSTER" ]; then
    printf '3GiB'
  else
    printf '1.5GiB'
  fi
}

node_cpu_for() {
  if [ -n "${DEV_K3D_NODE_CPU:-}" ]; then
    printf '%s' "$DEV_K3D_NODE_CPU"
  elif [ "$1" = "$CONTROL_CLUSTER" ]; then
    printf '2'
  else
    printf '1'
  fi
}

# node_resources_apply <cluster> — enforce the node resource caps on the
# server node container. k3d exposes --servers-memory but no CPU flag at all
# (v5.8.3 --help 实测; the memory flag is passed at creation below), so both
# caps ride docker update on the server-0 container — applied to newly
# created clusters AND to owned clusters on resume, which keeps a
# DEV_K3D_NODE_MEMORY/CPU override effective across re-runs (AC-065-37:
# "Given 覆盖 Then 按覆盖值生效" — creation-only would drop the memory
# override on every resume; Spec 轴审查发现).
node_resources_apply() {
  local cluster="$1" cpu mem
  cpu="$(node_cpu_for "$cluster")"
  mem="$(node_memory_for "$cluster")"
  if ! docker update --cpus "$cpu" --memory "$mem" "k3d-$cluster-server-0" >/dev/null; then
    fail "$ERR_CLUSTER_CREATE_FAILED" "cannot apply node resource limits (cpu=$cpu memory=$mem) to cluster $cluster"
  fi
}

cluster_up() {
  local cluster="$1"
  if cluster_exists "$cluster"; then
    # Owned clusters keep their resource caps current on resume (AC-065-37);
    # a same-named foreign cluster is never touched (AC-065-22 boundary).
    if ownership_contains k3d_clusters "$cluster"; then
      node_resources_apply "$cluster"
      mgmt_node_connect "$cluster"
    fi
    log "  $cluster (exists)"
    return 0
  fi
  # AC-065-22 conflict gates for the Docker objects this cluster will own:
  # the server node container (labeled at creation via --runtime-label, so
  # whitelist fallback is unnecessary) and the k3d cluster network (k3d
  # creates it unlabeled — the whitelist is its managed marker).
  require_no_conflict container "k3d-$cluster-server-0"
  require_no_conflict network "$(cluster_network_name "$cluster")" docker_networks
  local args=(
    # k3d v5 takes the cluster name positionally: `k3d cluster create
    # <name> [flags]` — there is no --name flag (REQ-065 k3d >= 5.8).
    cluster create "$cluster"
    --registry-use "$REGISTRY_NAME"
    --registry-config "$SCRIPT_DIR/../k3d/registries.yaml"
    # AC-065-37 (批次5 D2): deterministic server-node memory. k3d has no
    # CPU flag — the CPU cap is applied via docker update after creation
    # (node_resources_apply).
    --servers-memory "$(node_memory_for "$cluster")"
    --wait
    # REQ-065 kubeconfig isolation (real smoke 2026-08-24): without these,
    # k3d rewrites ~/.kube/config and switches its current-context to the
    # newest cluster — every host-side kubectl (kustomize apply, reset-data)
    # then targets the WRONG cluster. Kubeconfigs live only in data/.
    --kubeconfig-update-default=false
    --kubeconfig-switch-context=false
    # REQ-065 ownership labels: k3d applies --runtime-label to every node
    # container it creates, so managed clusters are recognizable via
    # docker_managed without any post-hoc label hack. k3d rejects an
    # unfiltered runtime-label on multi-node clusters (server + loadbalancer
    # count as two nodes), so scope it to the server nodes — the ownership
    # gate only ever inspects k3d-<cluster>-server-0.
    --runtime-label "$MANAGED_LABEL=true@servers:*"
    --runtime-label "$PROFILE_LABEL=${DEV_PROFILE:-local}@servers:*"
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
  # The @servers:* node filter is REQUIRED: k3d rejects an unfiltered --env
  # mapping on multi-node clusters (server + loadbalancer count as two
  # nodes; real smoke 2026-08-27: `EnvVarMapping 'HTTPS_PROXY=...' lacks a
  # node filter, but there's more than one node`) — same class as the
  # --runtime-label pitfall fixed in e18e3ed. NO_PROXY additionally covers
  # the private CIDRs (k3d bridge 172.16.0.0/12, cluster CIDR 10.0.0.0/8):
  # k3s honors HTTP(S)_PROXY for internal pod↔pod and apiserver→kubelet
  # traffic, so without them every cluster-internal HTTP call routes
  # through the host proxy (real smoke 2026-08-27: orchestrator values RPCs
  # died with 502-style internal errors and `kubectl logs` got
  # "proxyconnect tcp: proxy error ... code 502").
  if [ -n "${HTTP_PROXY:-}${http_proxy:-}${HTTPS_PROXY:-}${https_proxy:-}" ]; then
    args+=(
      --env "HTTP_PROXY=${HTTP_PROXY:-${http_proxy:-}}@servers:*"
      --env "HTTPS_PROXY=${HTTPS_PROXY:-${https_proxy:-}}@servers:*"
      --env "NO_PROXY=k3d-$REGISTRY_NAME,localhost,127.0.0.1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16${NO_PROXY:+,$NO_PROXY}@servers:*"
    )
  fi
  if ! k3d "${args[@]}"; then
    fail "$ERR_CLUSTER_CREATE_FAILED" "k3d failed to create $cluster"
  fi
  node_resources_apply "$cluster"
  # kubeconfig into the project data dir, never ~/.kube/config. 0600:
  # the file carries full cluster admin credentials (批次5 D7, AC-065-40).
  k3d kubeconfig get "$cluster" > "$DEV_DATA_DIR/kubeconfigs/$cluster.yaml"
  chmod 600 "$DEV_DATA_DIR/kubeconfigs/$cluster.yaml"
  # Bridge the management node into the new network so the operator agent
  # reaches the management gateway (mgmt_node_connect is idempotent).
  mgmt_node_connect "$cluster"
  ownership_add k3d_clusters "$cluster"
  # The cluster network is created by k3d without labels; record it so the
  # conflict gate and dev-purge treat it as managed (AC-065-22/26).
  ownership_add docker_networks "$(cluster_network_name "$cluster")"
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
    # Merged file carries the same admin credentials: 0600 too (AC-065-40).
    chmod 600 "$DEV_DATA_DIR/kubeconfig.yaml"
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

# build_goproxy — the GOPROXY chain forwarded into image builds. CN hosts
# cannot reach the google default (proxy.golang.org) directly, and buildkit
# RUN steps cannot reach a LOOPBACK host proxy (real smoke 2026-08-27: both
# paths dead — proxied fetch refused, direct fetch i/o timeout). goproxy.cn
# is directly reachable from CN hosts, so it is prepended as the primary
# entry whenever the resolved chain is the google default (or empty); an
# explicit user GOPROXY is passed through untouched.
build_goproxy() {
  local resolved="${GOPROXY:-$(go env GOPROXY 2>/dev/null || printf '')}"
  case "$resolved" in
    "https://proxy.golang.org,direct" | "" | "proxy.golang.org,direct")
      printf 'https://goproxy.cn,%s' "${resolved:-https://proxy.golang.org,direct}"
      ;;
    *)
      printf '%s' "$resolved"
      ;;
  esac
}

# goproxy_no_proxy_host — the hostname of the first build GOPROXY entry,
# used to exempt module fetches from the build proxy (buildkit RUN steps
# cannot reach a loopback host proxy). Empty when no host can be parsed
# (NO_PROXY then keeps only its fixed entries).
goproxy_no_proxy_host() {
  printf '%s' "$(build_goproxy)" | sed -nE 's#^https?://([^/,:]+).*#\1#p' | sed -n '1p'
}

# image_record <service> — compute the content hash, record it in IMAGE_TAGS
# (so kustomize_apply can pin the exact digest), and report whether a build
# is needed (0) or the manifest already exists in the registry (1).
image_record() {
  local service="$1" hash
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
  if docker manifest inspect "localhost:${REGISTRY_PORT}/release-$service:$tag" >/dev/null 2>&1; then
    log "  release-$service:$tag (unchanged)"
    return 1
  fi
  return 0
}

# build_push_now <service> — build and push using the tag recorded by
# image_record. Exit codes 10 (build failure) / 11 (push failure) let the
# parallel scheduler join every job before mapping failures back to the
# stable docker_build_failed / docker_push_failed error codes.
build_push_now() {
  local service="$1" hash tag dockerfile
  hash="${IMAGE_TAGS[$service]:-}"
  [ -n "$hash" ] || return 10
  tag="content-sha256-$hash"
  dockerfile="deploy/docker/Dockerfile.$service"
  if [ "$service" = "fixture" ]; then
    dockerfile="deploy/fixtures/Dockerfile"
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
      # The GOPROXY host is added to NO_PROXY: buildkit RUN steps cannot
      # reach a LOOPBACK host proxy (127.0.0.1 inside the build container is
      # the container itself — real smoke 2026-08-27: `go mod download`
      # died with "dial tcp 127.0.0.1:7890: connect: connection refused"),
      # so module fetches bypass the proxy and go direct (goproxy.cn is
      # directly reachable from CN hosts, verified 200/52ms).
      --build-arg "NO_PROXY=localhost,127.0.0.1,$(goproxy_no_proxy_host)${NO_PROXY:+,$NO_PROXY}"
    )
  fi
  # GOPROXY build-arg: the container's default proxy.golang.org is
  # unreachable from CN hosts (real-smoke failure); forward the effective
  # build chain (goproxy.cn first when the google default resolves) so
  # go mod download resolves (go env GOPROXY on CI hosts is the default
  # proxy.golang.org, so the fallback only guards missing go).
  build_args+=(--build-arg "GOPROXY=$(build_goproxy)")
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
    return 10
  fi
  if ! docker push "localhost:${REGISTRY_PORT}/release-$service:$tag"; then
    return 11
  fi
  log "  release-$service:$tag (built & pushed)"
  return 0
}

# build_and_push <service> — sequential image build (record + build/push).
build_and_push() {
  local service="$1" rc
  if ! image_record "$service"; then
    return 0
  fi
  if ! build_push_now "$service"; then
    rc=$?
    if [ "$rc" -eq 11 ]; then
      fail "$ERR_DOCKER_PUSH_FAILED" "push failed for release-$service:content-sha256-${IMAGE_TAGS[$service]:-}"
    fi
    fail "$ERR_DOCKER_BUILD_FAILED" "build failed for release-$service"
  fi
}

# images_up_sequential — the deterministic default: every image is built in
# order (AC-065-38: 5 clusters stay resident while 8 Go builds run; parallel
# Go builds risk OOM).
images_up_sequential() {
  local service
  for service in webhook orchestrator operator auth notifier web fixture notification-sink; do
    build_and_push "$service"
  done
}

# images_up_parallel <n> — DEV_BUILD_PARALLELISM=2/4 scheduler (AC-065-38):
# tags are recorded in the parent shell first (associative arrays do not
# cross subshell boundaries), then pending images are built in chunks of n
# background jobs. Every job is joined before failure mapping so a slow
# sibling cannot be orphaned behind an early fail.
images_up_parallel() {
  local par="$1"
  log "[4/7] docker images (parallelism $par) .... "
  local service pending=()
  for service in webhook orchestrator operator auth notifier web fixture notification-sink; do
    if image_record "$service"; then
      pending+=("$service")
    fi
  done
  [ "${#pending[@]}" -gt 0 ] || return 0
  # first_fail / first_rc always move together: the FIRST failed job decides
  # the error report. Recording only the LAST failure's rc would misreport a
  # leading build failure (10) as docker_push_failed when a later job fails
  # with 11 (Spec 轴审查发现).
  local i=0 svc rc first_fail="" first_rc=0 pids=() names=()
  for svc in "${pending[@]}"; do
    build_push_now "$svc" &
    pids+=("$!")
    names+=("$svc")
    i=$((i + 1))
    if [ "$i" -ge "$par" ]; then
      local j=0
      for j in "${!pids[@]}"; do
        if ! wait "${pids[$j]}"; then
          rc=$?
          if [ -z "$first_fail" ]; then
            first_fail="${names[$j]}"
            first_rc=$rc
          fi
        fi
      done
      pids=()
      names=()
      i=0
    fi
  done
  if [ "${#pids[@]}" -gt 0 ]; then
    local j=0
    for j in "${!pids[@]}"; do
      if ! wait "${pids[$j]}"; then
        rc=$?
        if [ -z "$first_fail" ]; then
          first_fail="${names[$j]}"
          first_rc=$rc
        fi
      fi
    done
  fi
  if [ -n "$first_fail" ]; then
    if [ "$first_rc" -eq 11 ]; then
      fail "$ERR_DOCKER_PUSH_FAILED" "push failed for release-$first_fail:content-sha256-${IMAGE_TAGS[$first_fail]:-}"
    fi
    fail "$ERR_DOCKER_BUILD_FAILED" "build failed for release-$first_fail"
  fi
}

images_up() {
  # AC-065-38 (批次5 D5): 1/2/4 selects the parallel degree; the default
  # (unset) is sequential for determinism. An unrecognized value falls back
  # to sequential with a stderr note — the knob must never brick dev-up.
  case "${DEV_BUILD_PARALLELISM:-}" in
    2 | 4)
      images_up_parallel "$DEV_BUILD_PARALLELISM"
      ;;
    "" | 1)
      log "[4/7] docker images ..................... "
      images_up_sequential
      ;;
    *)
      printf 'DEV_BUILD_PARALLELISM=%s invalid (expected 1/2/4); falling back to sequential builds\n' \
        "$DEV_BUILD_PARALLELISM" >&2
      log "[4/7] docker images ..................... "
      images_up_sequential
      ;;
  esac
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
  if ! printf '%s\n' "$manifest" | ctl_kubectl apply -f -; then
    fail "$ERR_SERVICE_UNHEALTHY" "kubectl apply failed for $KUSTOMIZE_DIR"
  fi
  # ci profile: the transient JWT key / service token / mTLS CA files served
  # the kustomize build above; the Secrets live in the cluster now and the
  # files are removed (D3 / 批次3 D2 / 批次5 D1).
  jwt_ci_temp_cleanup
  service_token_ci_temp_cleanup
  mtls_ca_ci_temp_cleanup
}

# require_db_ready <service> — PostgreSQL/Redis readiness via kubectl exec
# into the pinned image pods (REQ-065 批次5 D3, AC-065-01): `pg_isready`
# (postgres:16) and `redis-cli ping` (redis:7-alpine) probe the databases
# directly instead of proxying through business-service connections. The
# output only drives this shell judgment and never enters E2E assertions;
# the wait is bounded by DEV_TIMEOUT_READY (AC-065-10/28).
require_pg_ready() {
  local deadline=$((SECONDS + DEV_TIMEOUT_READY))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if ctl_kubectl -n release-manager-dev exec deployment/postgres -- pg_isready >/dev/null 2>&1; then
      log "  postgresql    cluster-internal:5432         ready"
      return 0
    fi
    sleep 2
  done
  fail "$ERR_SERVICE_UNHEALTHY" "postgres did not pass pg_isready within ${DEV_TIMEOUT_READY}s"
}

require_redis_ready() {
  local deadline=$((SECONDS + DEV_TIMEOUT_READY)) out
  while [ "$SECONDS" -lt "$deadline" ]; do
    out="$(ctl_kubectl -n release-manager-dev exec deployment/redis -- redis-cli ping 2>/dev/null || true)"
    if printf '%s' "$out" | grep -q 'PONG'; then
      log "  redis         cluster-internal:6379         ready"
      return 0
    fi
    sleep 2
  done
  fail "$ERR_SERVICE_UNHEALTHY" "redis did not answer redis-cli ping PONG within ${DEV_TIMEOUT_READY}s"
}

readiness() {
  log "[6/7] readiness ......................... "
  # Wait for every management-plane rollout to converge BEFORE probing:
  # readyz can pass on a pod that the rolling update is about to terminate,
  # and a seed request routed to that pod dies with `unexpected EOF`
  # (real smoke 2026-08-27: seed authenticate hit the auth pod mid-rollout
  # right after apply). rollout status is the deterministic convergence
  # signal the probes alone are not.
  if ! ctl_kubectl -n release-manager-dev rollout status \
    deployment/webhook deployment/orchestrator deployment/auth deployment/notifier deployment/web deployment/notification-sink \
    --timeout="${DEV_TIMEOUT_READY}s" >/dev/null; then
    fail "$ERR_SERVICE_UNHEALTHY" "management-plane rollout did not converge within ${DEV_TIMEOUT_READY}s"
  fi
  # Probe ports come from DEV_PORTS (host.sh) so the DEV_PORTS_OVERRIDE
  # test-isolation seam applies to the readiness stage too — fake-CLI tests
  # probe the overridden ports instead of the production 8082-8087 band.
  require_readyz webhook "${DEV_PORTS[0]}"
  require_readyz orchestrator "${DEV_PORTS[1]}"
  # operator 8084 is the orchestrator's mTLS agent gateway (HTTPS), not an
  # HTTP /readyz endpoint (real smoke 2026-08-24: plain-HTTP probes get
  # "Client sent an HTTP request to an HTTPS server"). Readiness = the
  # gateway accepts TCP connections; agents enroll there.
  require_tcp_ready operator "${DEV_PORTS[2]}"
  require_readyz auth "${DEV_PORTS[3]}"
  require_readyz notifier "${DEV_PORTS[4]}"
  # web has no /readyz; the root page is the probe.
  if ! wait_for_endpoint "http://127.0.0.1:${DEV_PORTS[5]}" "$DEV_TIMEOUT_READY"; then
    fail "$ERR_SERVICE_UNHEALTHY" "web did not answer on port ${DEV_PORTS[5]}"
  fi
  log "  web           http://localhost:${DEV_PORTS[5]}         200"
  # Cluster-internal databases (批次5 D3): kubectl exec probes against the
  # pinned image pods, counted into the DEV_TIMEOUT_READY budget.
  require_pg_ready
  require_redis_ready
}

# ---------------------------------------------------------------------------
# Stage 6: customer operator agents (per-cluster overlay + token injection)
# ---------------------------------------------------------------------------
# customer overlay mapping: cluster id -> overlay directory.
declare -A CUSTOMER_OVERLAYS=(
  [dev-customer-a-direct]=c1-direct
  [dev-customer-a-cache]=c2-cache
  [dev-customer-b-replicated]=c3-replicated
  [dev-customer-b-mixed]=c4-mixed
)

# customer_kubectl <cluster> [args...] — kubectl against one customer
# cluster. The cluster is consumed for the KUBECONFIG path only: "$@" must
# NOT include it, or kubectl treats the cluster name as its subcommand
# (`unknown command "dev-customer-a-direct" for "kubectl"`, real smoke
# 2026-08-27 — latent until agents_up actually deployed agents).
customer_kubectl() {
  local cluster="$1"
  shift
  KUBECONFIG="$DEV_DATA_DIR/kubeconfigs/$cluster.yaml" kubectl "$@"
}

# node_ip_on <container> <network> — one container's IP on a specific Docker
# network. The old {{range .NetworkSettings.Networks}} form concatenated
# every network's IP with no separator (the registry is bridged into bridge
# + control + every customer network), producing an invalid hostAliases IP
# (real smoke 2026-08-27: "must be a valid IP address"). Each customer agent
# needs the IPs on ITS OWN cluster network.
node_ip_on() {
  local container="$1" network="$2"
  # (index ... ).IPAddress — the map-indexed field access needs parens;
  # `index ... "net".IPAddress` is a template parse error (real smoke
  # 2026-08-27: empty mgmt/registry IPs → hostAliases unresolvable).
  docker container inspect --format "{{(index .NetworkSettings.Networks \"$network\").IPAddress}}" "$container" 2>/dev/null
}

# mgmt_node_ip_on <cluster> — the control node's IP on the customer network.
mgmt_node_ip_on() {
  node_ip_on "k3d-$CONTROL_CLUSTER-server-0" "k3d-$1"
}

# registry_ip_on <cluster> — the registry container's IP on the customer
# network.
registry_ip_on() {
  node_ip_on "$REGISTRY_CONTAINER" "k3d-$1"
}

# mgmt_node_connect <cluster> — bridge the management node into a customer
# cluster's network so the operator agent can reach the management gateway
# (NodePort 30084) through a cluster-local address (real smoke 2026-08-27:
# without it the control node's 172.21.0.3 is not routable from customer
# pods and agents can never enroll). Idempotent — a second connect is a
# tolerated no-op.
mgmt_node_connect() {
  local cluster="$1"
  [ "$cluster" = "$CONTROL_CLUSTER" ] && return 0
  if docker network inspect "k3d-$cluster" >/dev/null 2>&1; then
    docker network connect "k3d-$cluster" "k3d-$CONTROL_CLUSTER-server-0" >/dev/null 2>&1 || true
  fi
}

# agents_deployed — 1 when every customer cluster runs the operator agent.
agents_deployed() {
  local cluster
  for cluster in "${CUSTOMER_CLUSTERS[@]}"; do
    if ! customer_kubectl "$cluster" -n release-manager-customer get deployment operator >/dev/null 2>&1; then
      return 1
    fi
  done
  return 0
}

# agents_up — deploy the agent-only overlay to every customer cluster and
# inject the per-cluster secrets: the single-use enrollment token (generated
# by devseed in the enrollment phase) and the management gateway CA (from the
# orchestrator's /data/gateway-ca.crt). Idempotent: already-deployed agents
# are left untouched (AC-065-02).
agents_up() {
  log "[6.5/7] customer agents ................. "
  if agents_deployed; then
    log "  customer agents (already deployed)"
    return 0
  fi
  # The gateway CA is the dev mTLS CA generated by mtls_ca_ensure into
  # data/dev-ca/; the kustomize secretGenerator release-manager-mtls-ca
  # packages it into the orchestrator's /data/gateway-ca.crt secret volume
  # (deploy/kustomize/services/orchestrator.yaml). agents_up copies the
  # LOCAL file — the orchestrator image is distroless (no cat/sh), so `exec
  # ... cat /data/gateway-ca.crt` can never work (real smoke 2026-08-27).
  if [ ! -s "$(mtls_ca_cert_path)" ]; then
    fail "$ERR_SERVICE_UNHEALTHY" "cannot read gateway CA from $(mtls_ca_cert_path)"
  fi
  cp "$(mtls_ca_cert_path)" "$DEV_DATA_DIR/dev-gateway-ca.crt"
  local cluster overlay ctx manifest token
  for cluster in "${CUSTOMER_CLUSTERS[@]}"; do
    overlay="${CUSTOMER_OVERLAYS[$cluster]}"
    ctx="k3d-$cluster"
    token="$(cat "$DEV_DATA_DIR/dev-enrollment-tokens/$cluster.token" 2>/dev/null || true)"
    if [ -z "$token" ]; then
      fail "$ERR_SEED_WRITE_FAILED" "enrollment token for $cluster missing; run the seed enrollment phase first"
    fi
    # kustomize build: the customer overlay has no runtime secrets (those are
    # applied imperatively below); LoadRestrictionsNone mirrors the JWT
    # secretGenerator pattern.
    if ! manifest="$(kustomize build "deploy/kustomize/customer-agent/$overlay")"; then
      fail "$ERR_KUSTOMIZE_BUILD_FAILED" "kustomize build failed for customer-agent/$overlay"
    fi
    # Pin the image digest tag and substitute the hostAliases placeholders
    # with the IPs ON THIS cluster's network: the control node and registry
    # are bridged into every customer network (mgmt_node_connect / k3d
    # --registry-use), and each network assigns its own addresses.
    local svc hash mgmt_ip reg_ip
    for svc in "${!IMAGE_TAGS[@]}"; do
      hash="${IMAGE_TAGS[$svc]:-}"
      [ -n "$hash" ] || continue
      manifest="$(printf '%s\n' "$manifest" | sed "s#release-$svc:dev#release-$svc:content-sha256-$hash#g")"
    done
    mgmt_ip="$(mgmt_node_ip_on "$cluster")"
    reg_ip="$(registry_ip_on "$cluster")"
    if [ -z "$mgmt_ip" ] || [ -z "$reg_ip" ]; then
      fail "$ERR_SERVICE_UNHEALTHY" "cannot resolve management/registry IP on $cluster network for customer agent hostAliases (mgmt=$mgmt_ip registry=$reg_ip)"
    fi
    manifest="$(printf '%s\n' "$manifest" | sed "s/172\.18\.0\.2/$mgmt_ip/g; s/172\.18\.0\.3/$reg_ip/g")"
    if ! printf '%s\n' "$manifest" | customer_kubectl "$cluster" apply -f -; then
      fail "$ERR_SERVICE_UNHEALTHY" "kubectl apply failed for customer agent $cluster"
    fi
    # Imperative secrets (fixed names, no kustomize hash): token + gateway CA.
    if ! customer_kubectl "$cluster" -n release-manager-customer create secret generic operator-enrollment \
      --from-literal=token="$token" --dry-run=client -o yaml \
      | customer_kubectl "$cluster" apply -f - >/dev/null; then
      fail "$ERR_SEED_WRITE_FAILED" "cannot inject enrollment token for $cluster"
    fi
    if ! customer_kubectl "$cluster" -n release-manager-customer create secret generic operator-gateway-ca \
      --from-file=gateway-ca.crt="$DEV_DATA_DIR/dev-gateway-ca.crt" --dry-run=client -o yaml \
      | customer_kubectl "$cluster" apply -f - >/dev/null; then
      fail "$ERR_SERVICE_UNHEALTHY" "cannot inject gateway CA for $cluster"
    fi
    # Wait for the agent pod to be ready (the enroll RPC happens at
    # bootstrap; the online wait is the verify phase's job).
    if ! customer_kubectl "$cluster" -n release-manager-customer rollout status deployment/operator --timeout=120s >/dev/null; then
      fail "$ERR_SERVICE_UNHEALTHY" "operator agent for $cluster did not become ready"
    fi
    log "  agent $cluster (deployed)"
  done
}

# smoke_fixture_version — assert the fixture workload reports fixture-vN
# through a temporary kubectl port-forward (REQ-065 批次5 D10, AC-065-01):
# the image content version is decoupled from the data fixture_version, no
# NodePort is exposed and no operator-side proxy is introduced. The forward
# is killed on every exit path.
smoke_fixture_version() {
  local pf_pid="" logf port version_out
  logf="$DEV_DATA_DIR/fixture-pf.log"
  # dev-customer-a-direct hosts every E2E target; e2e-release is the
  # e2e-release-target namespace (fixture chart deployment release-fixture).
  customer_kubectl dev-customer-a-direct -n e2e-release port-forward svc/release-fixture :8088 > "$logf" 2>&1 &
  pf_pid=$!
  # ${pf_pid:-} / ${logf:-}: the vars are local to this function and are out
  # of scope when the trap fires at script EXIT — plain "$pf_pid" would trip
  # `set -u` (unbound variable), abort the trap with exit 1 and skip
  # cleanup_trap entirely (real trap-chain bug caught by fake-CLI tests).
  trap 'TRAP_RC=$?; kill "${pf_pid:-}" 2>/dev/null || true; rm -f "${logf:-}" 2>/dev/null || true; cleanup_trap' EXIT INT TERM
  local deadline=$((SECONDS + 15))
  while [ "$SECONDS" -lt "$deadline" ]; do
    port="$(sed -nE 's#Forwarding from (127\.0\.0\.1|\[::1\]):([0-9]+) -> .*#\2#p' "$logf" 2>/dev/null | head -1 || true)"
    [ -n "$port" ] && break
    sleep 1
  done
  if [ -z "$port" ]; then
    kill "$pf_pid" 2>/dev/null || true
    fail "$ERR_SERVICE_UNHEALTHY" "fixture port-forward did not bind a local port (log: $(cat "$logf" 2>/dev/null || true))"
  fi
  if ! version_out="$(curl --fail --silent --show-error "http://127.0.0.1:$port/version" 2>/dev/null)"; then
    kill "$pf_pid" 2>/dev/null || true
    fail "$ERR_SERVICE_UNHEALTHY" "fixture /version unreachable via port-forward on 127.0.0.1:$port"
  fi
  kill "$pf_pid" 2>/dev/null || true
  rm -f "$logf"
  if ! printf '%s' "$version_out" | grep -Eq '"version"[[:space:]]*:[[:space:]]*"fixture-v[0-9]+"'; then
    fail "$ERR_SERVICE_UNHEALTHY" "fixture /version returned unexpected payload: $version_out"
  fi
  log "  fixture /version .................. $version_out"
}

# ---------------------------------------------------------------------------
# Stage 7: seed (delegates to the devfixture runner)
# ---------------------------------------------------------------------------
# run_seed_leg <devseed-args...> — run one devseed leg to completion with one
# retry on a transient failure. Seed legs are idempotent by design (progress
# file + stable idempotency keys), so a retry is safe. The retry covers the
# loadbalancer endpoint race: right after a maintenance rollout a fresh seed
# connection can still be routed to a just-terminated pod and reset with
# `unexpected EOF` even though require_readyz already saw 200 (real smoke
# 2026-08-27). The final output is printed on stdout and the function exits 1
# on exhaustion so callers can map the error code.
run_seed_leg() {
  local attempt output delay="${DEV_SEED_RETRY_DELAY:-5}"
  for attempt in 1 2; do
    if output="$(go run ./cmd/devseed/ "$@" 2>&1)"; then
      printf '%s\n' "$output"
      return 0
    fi
    if [ "$attempt" -lt 2 ]; then
      log "  devseed leg failed (attempt $attempt/2); retrying after ${delay}s endpoint convergence"
      sleep "$delay"
    fi
  done
  printf '%s\n' "$output"
  return 1
}

seed() {
  log "[7/7] seed data .......................... "
  local seed_output
  # Split seed around the enrollment phase: the single-use tokens must exist
  # before the customer agents can enroll, and the bootstrap INSTALL needs
  # the agents online. devseed's progress/resume makes the second run pick
  # up at the install phase.
  local tokens_ready=true
  local cluster
  for cluster in "${CUSTOMER_CLUSTERS[@]}"; do
    if [ ! -s "$DEV_DATA_DIR/dev-enrollment-tokens/$cluster.token" ]; then
      tokens_ready=false
      break
    fi
  done
  if [ "$tokens_ready" = false ]; then
    if ! seed_output="$(run_seed_leg --stop-after enrollment --orchestrator http://127.0.0.1:8083 --webhook http://127.0.0.1:8082 --auth http://127.0.0.1:8085 --operator-timeout "$DEV_TIMEOUT_OPERATOR" --seed-retries "$DEV_TIMEOUT_SEED_RETRIES")"; then
      fail "$ERR_SEED_WRITE_FAILED" "devseed enrollment failed: $seed_output"
    fi
    agents_up
  fi
  # Resume: install + verify (the operator-online wait lives in verify,
  # bounded by DEV_TIMEOUT_OPERATOR).
  if ! seed_output="$(run_seed_leg --orchestrator http://127.0.0.1:8083 --webhook http://127.0.0.1:8082 --auth http://127.0.0.1:8085 --operator-timeout "$DEV_TIMEOUT_OPERATOR" --seed-retries "$DEV_TIMEOUT_SEED_RETRIES")"; then
    # AC-065-18: an operator session that never reached online reports its
    # own error code with the faulting cluster name (devfixture verify).
    if printf '%s' "$seed_output" | grep -q "operator_not_online"; then
      fail "$ERR_OPERATOR_NOT_ONLINE" "$seed_output"
    fi
    fail "$ERR_SEED_WRITE_FAILED" "devseed failed: $seed_output"
  fi
  log "  seed data .......................... DONE"
  # AC-065-01 (批次5 D10): the installed fixture workload must report
  # fixture-vN via a temporary port-forward.
  smoke_fixture_version
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
  # deployment stage. Same for the bundle ingress service token (批次3 D2,
  # AC-065-33) and the dev mTLS CA (批次5 D1, AC-065-36): their
  # secretGenerators source data/dev-service-tokens/ and data/dev-ca/, and
  # the seed enrollment phase requires the CA to already be in place.
  jwt_signing_key_ensure
  service_token_ensure
  mtls_ca_ensure
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
  local cluster network
  # D-017 teardown order (real smoke ②): delete clusters → clean their
  # kubeconfigs + ownership entries → disconnect the registry from each
  # cluster network → remove the networks. Deleting a cluster while the
  # registry is still attached to its network leaves both behind.
  for cluster in "${ALL_CLUSTERS[@]}"; do
    if ! ownership_contains k3d_clusters "$cluster"; then
      if cluster_exists "$cluster"; then
        printf 'unmanaged cluster %s exists without ownership record; skipped\n' "$cluster" >&2
      fi
      continue
    fi
    if cluster_exists "$cluster"; then
      k3d cluster delete "$cluster" || true
    fi
    # AC-065-04: the deleted cluster's kubeconfig and ownership entries go
    # with it (registry/profile/created_at metadata is preserved).
    rm -f "$DEV_DATA_DIR/kubeconfigs/$cluster.yaml"
    ownership_remove k3d_clusters "$cluster"
    network="$(cluster_network_name "$cluster")"
    network_teardown "$network"
    ownership_remove docker_networks "$network"
    log "  removed cluster $cluster"
  done
  # Rebuild the merged kubeconfig from the clusters that remain; delete the
  # merged file when nothing is left (AC-065-04).
  local remaining=()
  for cluster in "${ALL_CLUSTERS[@]}"; do
    if cluster_exists "$cluster"; then
      remaining+=("$cluster")
    fi
  done
  if [ "${#remaining[@]}" -gt 0 ]; then
    k3d kubeconfig merge "${remaining[@]}" -o "$DEV_DATA_DIR/kubeconfig.yaml" >/dev/null
    chmod 600 "$DEV_DATA_DIR/kubeconfig.yaml"
  else
    rm -f "$DEV_DATA_DIR/kubeconfig.yaml"
  fi
  log "registry and image cache retained"
}

cmd_seed() {
  acquire_lock seed
  trap cleanup_trap EXIT INT TERM
  # dev-seed precondition (REQ-065 D3 / 批次3 D2 / 批次5 D1): the JWT signing
  # key, the webhook→orchestrator service token and the dev mTLS CA must
  # already be injected — dev-up generates all three before deployment. Seed
  # never generates them itself: a missing key/token/CA means dev-up has not
  # converged yet.
  if [ "${DEV_PROFILE:-local}" != "ci" ] && { [ ! -f "$(jwt_key_path)" ] || [ ! -s "$(jwt_key_path)" ]; }; then
    fail "$ERR_SERVICE_UNHEALTHY" "JWT signing key $(jwt_key_path) missing; run make dev-up first"
  fi
  if [ "${DEV_PROFILE:-local}" != "ci" ] && { [ ! -f "$(service_token_path)" ] || [ ! -s "$(service_token_path)" ]; }; then
    fail "$ERR_SERVICE_UNHEALTHY" "webhook service token $(service_token_path) missing; run make dev-up first"
  fi
  if [ "${DEV_PROFILE:-local}" != "ci" ] && { [ ! -s "$(mtls_ca_key_path)" ] || [ ! -s "$(mtls_ca_cert_path)" ]; }; then
    fail "$ERR_SERVICE_UNHEALTHY" "dev mTLS CA $DEV_DATA_DIR/dev-ca missing; run make dev-up first"
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
  if ! ctl_kubectl -n release-manager-dev get deployment postgres >/dev/null 2>&1; then
    fail "$ERR_SERVICE_UNHEALTHY" "postgres deployment not found; dev-reset-data requires a running environment"
  fi
  # 1. maintenance: stop writes on the maintenance-capable services
  #    (ADR-015 procedure-level maintenance gate; MAINTENANCE env overrides
  #    the config file per internal/config env mapping).
  ctl_kubectl -n release-manager-dev set env deployment/orchestrator deployment/auth deployment/notifier MAINTENANCE=true >/dev/null \
    || fail "$ERR_SERVICE_UNHEALTHY" "cannot enable maintenance mode"
  ctl_kubectl -n release-manager-dev rollout status deployment/orchestrator deployment/auth deployment/notifier --timeout=180s >/dev/null \
    || fail "$ERR_SERVICE_UNHEALTHY" "maintenance rollout did not converge"
  log "  maintenance enabled (writes stopped)"

  # 2. dump both databases through a port-forward to the host pg_dump.
  #    DEV_RESET_PG_PORT is the test seam AND the local port for the
  #    forward (real default 5432): the forward must not hardcode 5432,
  #    because a foreign host postgres may already hold it (real smoke
  #    2026-08-27: a concurrent task's pg-task015-001 occupied 127.0.0.1:5432
  #    and pg_dump connected to the WRONG postgres → role missing).
  local pf_pid="" pg_port="${DEV_RESET_PG_PORT:-5432}"
  ctl_kubectl -n release-manager-dev port-forward svc/postgres "$pg_port":5432 >/dev/null 2>&1 &
  pf_pid=$!
  # The trap releases the environment lock; the port-forward dies with the
  # process group on exit, but kill it explicitly to avoid a stray listener.
  # ${pf_pid:-}: local to this function — out of scope when the EXIT trap
  # fires after cmd_reset_data returns (set -u would abort the trap).
  trap 'TRAP_RC=$?; kill "${pf_pid:-}" 2>/dev/null || true; cleanup_trap' EXIT INT TERM
  # The port-forward binds its listeners asynchronously; pg_dump would hit
  # ECONNREFUSED when run immediately (real smoke 2026-08-27: reset-data
  # failed `pg_dump failed for release_manager` while a manual re-run after
  # 3s succeeded). Wait for the loopback listener before the first dump.
  local pf_deadline=$((SECONDS + 30))
  while [ "$SECONDS" -lt "$pf_deadline" ]; do
    if (exec 3<>"/dev/tcp/127.0.0.1/$pg_port") 2>/dev/null; then
      (exec 3>&-) 2>/dev/null || true
      break
    fi
    sleep 1
  done
  if [ "$SECONDS" -ge "$pf_deadline" ]; then
    kill "$pf_pid" 2>/dev/null || true
    fail "$ERR_SERVICE_UNHEALTHY" "postgres port-forward did not bind 127.0.0.1:$pg_port within 30s"
  fi
  local ts dump_files=() db dump_file
  ts="$(date +%Y%m%dT%H%M%S%z)"
  mkdir -p "$DEV_DATA_DIR/backups"
  for db in release_manager release_notifier; do
    dump_file="$DEV_DATA_DIR/backups/dump-$ts-$db.sql"
    if ! PGPASSWORD=dev-release-manager pg_dump -Fc -h 127.0.0.1 -p "$pg_port" -U release_manager -d "$db" -f "$dump_file" >/dev/null 2>&1; then
      # AC-065-13 "environment untouched": the dump stage failed before any
      # destructive step — restore writes before reporting. (Real smoke
      # 2026-08-27: the port-forward race failed the dump and left the
      # maintenance flag on, blocking every later seed write.)
      ctl_kubectl -n release-manager-dev set env deployment/orchestrator deployment/auth deployment/notifier MAINTENANCE- >/dev/null 2>&1 || true
      fail "$ERR_SERVICE_UNHEALTHY" "pg_dump failed for $db (backup $dump_file); environment untouched"
    fi
    dump_files+=("$dump_file")
    log "  dumped $db -> $dump_file"
  done

  # 3. rebuild the 4 customer clusters (control cluster and registry stay).
  #    cluster_up writes each cluster's kubeconfig; ensure the dir exists even
  #    on a pristine data dir (dev-up's clusters_up creates it, reset-data
  #    drives cluster_up directly).
  mkdir -p "$DEV_DATA_DIR/kubeconfigs"
  local cluster
  for cluster in "${CUSTOMER_CLUSTERS[@]}"; do
    if cluster_exists "$cluster"; then
      k3d cluster delete "$cluster" >/dev/null 2>&1 || true
      ownership_remove k3d_clusters "$cluster"
    fi
    cluster_up "$cluster"
  done

  # 3b. converge the image tags. reset-data rebuilds the customer clusters
  #     and re-seeds (agents_up pins each operator image to
  #     content-sha256-$hash from IMAGE_TAGS), but unlike dev-up it never
  #     runs images_up — with IMAGE_TAGS empty the agent manifest would fall
  #     back to the unpinned `:dev` tag, which the registry does not carry
  #     (real smoke 2026-08-27: ImagePullBackOff `manifest unknown`). images_up
  #     is idempotent: unchanged services skip the build/push.
  images_up

  # 4. schema rebuild + canonical re-seed via devseed --reset (golang-migrate
  #    SDK down -all -> up, then the nine phases; see internal/devfixture).
  #    Maintenance was protecting the dump; the re-seed is a WRITE pass
  #    (Initialize + all nine phases) and must run with writes enabled —
  #    keeping MAINTENANCE=true here blocks the very first Initialize RPC
  #    (real smoke 2026-08-27: "initialize system: unavailable: maintenance"
  #    left the reset dead-ended). The exclusive flock already serializes
  #    lifecycle operations; the migrate window between this point and the
  #    seed is dev-only acceptable exposure.
  ctl_kubectl -n release-manager-dev set env deployment/orchestrator deployment/auth deployment/notifier MAINTENANCE- >/dev/null \
    || fail "$ERR_SERVICE_UNHEALTHY" "cannot disable maintenance mode for re-seed"
  ctl_kubectl -n release-manager-dev rollout status deployment/orchestrator deployment/auth deployment/notifier --timeout=180s >/dev/null \
    || fail "$ERR_SERVICE_UNHEALTHY" "maintenance rollout did not converge"
  log "  maintenance disabled (re-seed writes enabled)"
  # rollout status is not connection-level convergence: the loadbalancer can
  # still route a fresh seed connection to a just-terminated pod
  # (`unexpected EOF`, real smoke 2026-08-27). Poll the host endpoints
  # until the new pods answer before the re-seed.
  require_readyz auth "${DEV_PORTS[3]}"
  require_readyz orchestrator "${DEV_PORTS[1]}"
  # 4b. Split the re-seed around the enrollment phase exactly like dev-up's
  #     seed(): the freshly rebuilt customer clusters hold no operator
  #     agents, and the bootstrap INSTALL requires an operator for the
  #     artifact stage — enroll first, deploy the agents with their
  #     single-use tokens, then resume install + verify (real smoke
  #     2026-08-27: the monolithic --reset failed the artifact stage with
  #     "no operator for cluster dev-customer-a-direct" → stage_unavailable).
  local reset_seed_failed=0
  local seed_dsn="postgres://release_manager:dev-release-manager@127.0.0.1:5432/release_manager?sslmode=disable"
  if ! run_seed_leg --reset --stop-after enrollment \
    --orchestrator http://127.0.0.1:8083 --webhook http://127.0.0.1:8082 --auth http://127.0.0.1:8085 \
    --operator-timeout "$DEV_TIMEOUT_OPERATOR" --seed-retries "$DEV_TIMEOUT_SEED_RETRIES" \
    --database-dsn "$seed_dsn"; then
    reset_seed_failed=1
  fi
  if [ "$reset_seed_failed" -eq 0 ]; then
    agents_up || reset_seed_failed=1
  fi
  if [ "$reset_seed_failed" -eq 0 ] && ! run_seed_leg \
    --orchestrator http://127.0.0.1:8083 --webhook http://127.0.0.1:8082 --auth http://127.0.0.1:8085 \
    --operator-timeout "$DEV_TIMEOUT_OPERATOR" --seed-retries "$DEV_TIMEOUT_SEED_RETRIES"; then
    reset_seed_failed=1
  fi
  if [ "$reset_seed_failed" -ne 0 ]; then
    # 5a. failure: restore both databases, drop the half-built customer
    #     clusters, mark the environment partial; a later reset converges.
    log "  seed failed; restoring databases"
    for db in release_manager release_notifier; do
      dump_file="$DEV_DATA_DIR/backups/dump-$ts-$db.sql"
      # --clean --if-exists: the failed re-seed already ran migrate up, so
      # the schema is non-empty — a plain restore would fail on "relation
      # already exists" (real smoke 2026-08-27: restore failed while the
      # dump file itself was perfectly readable).
      # The host tools are PostgreSQL 18.6 while the dev server is pinned
      # postgres:16 — pg_dump 18.6 emits `SET transaction_timeout = 0;` in
      # the archive header, which the PG16 server rejects on restore
      # (unrecognized configuration parameter; real smoke 2026-08-27, the
      # P3 prototype's "版本不兼容" failure). Restore therefore decodes the
      # custom archive to SQL, strips that one PG17+-only GUC line, and
      # replays it via psql with ON_ERROR_STOP — still the real host tools.
      if ! { PGPASSWORD=dev-release-manager pg_restore --clean --if-exists -Fc -f - "$dump_file" 2>"$dump_file.restore-err" \
        | sed -e '/SET transaction_timeout = 0;/d' \
        | PGPASSWORD=dev-release-manager psql -v ON_ERROR_STOP=1 -q -h 127.0.0.1 -p "$pg_port" -U release_manager -d "$db"; } then
        printf 'restore failed for %s (backup %s); manual recovery required\n' "$db" "$dump_file" >&2
        tail -5 "$dump_file.restore-err" >&2 2>/dev/null || true
      fi
      rm -f "$dump_file.restore-err"
    done
    for cluster in "${CUSTOMER_CLUSTERS[@]}"; do
      if cluster_exists "$cluster"; then
        k3d cluster delete "$cluster" >/dev/null 2>&1 || true
        ownership_remove k3d_clusters "$cluster"
      fi
    done
    ctl_kubectl -n release-manager-dev set env deployment/orchestrator deployment/auth deployment/notifier MAINTENANCE- >/dev/null 2>&1 || true
    fail "$ERR_FIXTURE_CONFLICT" "reset failed; databases restored and environment marked partial (rerun dev-reset-data to converge)"
  fi

  # 5b. success: drop the safety-net dumps and restore writes.
  for dump_file in "${dump_files[@]}"; do
    rm -f "$dump_file"
  done
  ctl_kubectl -n release-manager-dev set env deployment/orchestrator deployment/auth deployment/notifier MAINTENANCE- >/dev/null \
    || fail "$ERR_SERVICE_UNHEALTHY" "cannot disable maintenance mode"
  ctl_kubectl -n release-manager-dev rollout status deployment/orchestrator deployment/auth deployment/notifier --timeout=180s >/dev/null \
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
  local manifest registry_vol
  registry_vol=""
  if ownership_contains docker_containers "$REGISTRY_CONTAINER"; then
    registry_vol="$(registry_volume)"
  fi
  manifest="$(ownership_read)"
  local name
  # D-017 teardown order: clusters first (with kubeconfig + ownership
  # cleanup), then networks (registry disconnected before removal), then the
  # containers incl. the registry, its data volume and the data/ runtime
  # files.
  printf '%s' "$manifest" | sed -nE 's/.*"k3d_clusters"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p' | tr ',' '\n' | sed -E 's/^[[:space:]]*"//; s/"[[:space:]]*$//' | while IFS= read -r name || [ -n "$name" ]; do
    [ -n "$name" ] || continue
    if cluster_exists "$name"; then
      k3d cluster delete "$name" >/dev/null 2>&1 || true
      log "  removed cluster $name"
    fi
    rm -f "$DEV_DATA_DIR/kubeconfigs/$name.yaml"
    ownership_remove k3d_clusters "$name"
  done
  rm -f "$DEV_DATA_DIR/kubeconfig.yaml"
  printf '%s' "$manifest" | sed -nE 's/.*"docker_networks"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p' | tr ',' '\n' | sed -E 's/^[[:space:]]*"//; s/"[[:space:]]*$//' | while IFS= read -r name || [ -n "$name" ]; do
    [ -n "$name" ] || continue
    network_teardown "$name"
    ownership_remove docker_networks "$name"
    log "  removed network $name"
  done
  # Containers from the whitelist, registry included.
  printf '%s' "$manifest" | sed -nE 's/.*"docker_containers"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p' | tr ',' '\n' | sed -E 's/^[[:space:]]*"//; s/"[[:space:]]*$//' | while IFS= read -r name || [ -n "$name" ]; do
    [ -n "$name" ] || continue
    docker rm -f "$name" >/dev/null 2>&1 || true
    log "  removed container $name"
  done
  # The registry data volume (named by dev-up, anonymous by legacy k3d
  # creation) holds the image cache; purge deletes it together with the
  # container (dev-down retains both per the cache contract).
  registry_volume_remove "$registry_vol"
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

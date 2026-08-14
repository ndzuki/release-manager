#!/usr/bin/env bash
# host.sh — host preflight checks (REQ-065 validation rules).
#
# dev-up runs the full battery before touching any resource (AC-065-06/07/14/
# 20/21/22/25); dev-seed/dev-reset-data/dev-purge enforce their own narrow
# preconditions through the same helpers.
set -euo pipefail

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=lib/errors.sh
source "$SCRIPT_DIR/lib/errors.sh"

MIN_MEM_AVAILABLE_GB=12
MIN_DISK_AVAILABLE_GB=20
MIN_CPU_COUNT=4
DEV_PORTS=(8082 8083 8084 8085 8086 8087)
# Test isolation: DEV_PORTS_OVERRIDE env (space-separated) lets fake-CLI
# tests probe idle ports on hosts where the real dev environment is up.
# A distinct name avoids bash arrays shadowing a same-named scalar.
if [ -n "${DEV_PORTS_OVERRIDE:-}" ]; then
  read -r -a DEV_PORTS <<< "$DEV_PORTS_OVERRIDE"
fi
# require_linux — the dev environment is Linux-only (REQ-065 non-goal).
require_linux() {
  if [ "$(uname -s)" != "Linux" ]; then
    fail "$ERR_DOCKER_UNAVAILABLE" "the dev environment supports Linux hosts only"
  fi
}

# require_docker — docker CLI present and daemon reachable (AC-065-14).
require_docker() {
  require_command docker "$ERR_DOCKER_UNAVAILABLE" \
    "install Docker and ensure docker is on PATH"
  if ! docker info >/dev/null 2>&1; then
    fail "$ERR_DOCKER_UNAVAILABLE" "Docker daemon is unreachable; start it and retry"
  fi
}

# require_k3d — k3d >= 5.8 with a parseable version (AC-065-06).
require_k3d() {
  require_command k3d "$ERR_K3D_UNAVAILABLE" \
    "install k3d >= 5.8 (e.g. 'curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash') and ensure k3d is on PATH"
  local raw version
  raw="$(k3d version 2>/dev/null || true)"
  version="$(printf '%s\n' "$raw" | sed -nE 's/.*v([0-9]+\.[0-9]+\.[0-9]+).*/\1/p' | sed -n '1p')"
  if [ -z "$version" ]; then
    fail "$ERR_K3D_UNAVAILABLE" "cannot determine k3d version from: $raw"
  fi
  # Compare against the 5.8 minor floor.
  local major minor
  major="$(printf '%s' "$version" | cut -d. -f1)"
  minor="$(printf '%s' "$version" | cut -d. -f2)"
  if [ "$major" -lt 5 ] || { [ "$major" -eq 5 ] && [ "$minor" -lt 8 ]; }; then
    fail "$ERR_K3D_UNAVAILABLE" "k3d v$version is too old; install k3d >= 5.8"
  fi
}

# port_in_use <port> — probe with bash /dev/tcp (no external dependency).
port_in_use() {
  local port="$1"
  (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null && exec 3>&- && return 0
  return 1
}

# require_ports_free — 8082-8087 must be idle (AC-065-07). Reports the
# occupying PID when /proc lets us resolve it.
require_ports_free() {
  local port pid
  for port in "${DEV_PORTS[@]}"; do
    if port_in_use "$port"; then
      pid=""
      if command -v ss >/dev/null 2>&1; then
        pid="$(ss -tlnp 2>/dev/null | sed -nE "s/.*:${port}[[:space:]].*pid=([0-9]+).*/\1/p" | sed -n '1p' || true)"
      fi
      fail "$ERR_PORT_CONFLICT" "port $port is already in use${pid:+ by pid $pid}; free it and retry"
    fi
  done
}

# mem_available_mb — MemAvailable from /proc/meminfo in MiB.
mem_available_mb() {
  awk '/MemAvailable:/ { printf "%d", $2 / 1024 }' /proc/meminfo
}

# require_memory — MemAvailable >= 12 GiB (AC-065-20).
require_memory() {
  local available_mb
  available_mb="$(mem_available_mb)"
  if [ -z "$available_mb" ] || [ "$available_mb" -lt $((MIN_MEM_AVAILABLE_GB * 1024)) ]; then
    fail "$ERR_HOST_MEMORY_INSUFFICIENT" \
      "available memory is ${available_mb:-0} MiB (< ${MIN_MEM_AVAILABLE_GB} GiB); close memory-heavy applications and retry"
  fi
}

# disk_available_kb — free space on the dev data filesystem in KiB.
disk_available_kb() {
  local dir="${DEV_DATA_DIR:-$SCRIPT_DIR/../../data}"
  df -Pk "$dir" 2>/dev/null | awk 'NR==2 { print $4 }'
}

# require_disk — free disk >= 20 GiB (AC-065-21).
require_disk() {
  local available_kb
  available_kb="$(disk_available_kb)"
  if [ -z "$available_kb" ] || [ "$available_kb" -lt $((MIN_DISK_AVAILABLE_GB * 1024 * 1024)) ]; then
    fail "$ERR_HOST_DISK_INSUFFICIENT" \
      "available disk is ${available_kb:-0} KiB (< ${MIN_DISK_AVAILABLE_GB} GiB); free space and retry"
  fi
}

# require_cpu — at least 4 cores for 5 k3d clusters.
require_cpu() {
  local cores
  cores="$(nproc 2>/dev/null || printf '0')"
  if [ "$cores" -lt "$MIN_CPU_COUNT" ]; then
    fail "$ERR_HOST_MEMORY_INSUFFICIENT" \
      "host has $cores CPUs (< $MIN_CPU_COUNT); the dev environment needs at least $MIN_CPU_COUNT cores"
  fi
}

# require_e2e_run_id — DEV_PROFILE=ci needs a DNS-1123 E2E_RUN_ID (AC-065-25).
# DNS-1123 label: lowercase alnum, hyphens in the middle, <= 63 chars.
require_e2e_run_id() {
  local run_id="${E2E_RUN_ID:-}"
  if [ -z "$run_id" ]; then
    fail "$ERR_E2E_RUN_ID_INVALID" "DEV_PROFILE=ci requires E2E_RUN_ID (DNS-1123: ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$, <= 63 chars)"
  fi
  if ! printf '%s' "$run_id" | grep -Eq '^[a-z0-9]([-a-z0-9]*[a-z0-9])?$' || [ "${#run_id}" -gt 63 ]; then
    fail "$ERR_E2E_RUN_ID_INVALID" \
      "E2E_RUN_ID '$run_id' is not DNS-1123 (^[a-z0-9]([-a-z0-9]*[a-z0-9])?$, <= 63 chars)"
  fi
}

# require_flock — util-linux flock must be available (environment lock).
require_flock() {
  require_command flock "$ERR_DOCKER_UNAVAILABLE" \
    "install util-linux (flock) and ensure it is on PATH"
}

# require_pg_tools — pg_dump/pg_restore for dev-reset-data snapshot safety.
require_pg_tools() {
  require_command pg_dump "$ERR_DOCKER_UNAVAILABLE" \
    "pg_dump is required for dev-reset-data; install PostgreSQL client tools"
  require_command pg_restore "$ERR_DOCKER_UNAVAILABLE" \
    "pg_restore is required for dev-reset-data; install PostgreSQL client tools"
}

# preflight_up — full battery for dev-up. The ci profile parameter check runs
# first: it is pure argument validation (AC-065-25: no resources are created
# when the run id is invalid) and must not depend on host resource probes.
preflight_up() {
  require_linux
  if [ "${DEV_PROFILE:-local}" = "ci" ]; then
    require_e2e_run_id
  fi
  require_flock
  require_docker
  require_k3d
  require_ports_free
  require_memory
  require_disk
  require_cpu
}
environment_id() {
  if [ "${DEV_PROFILE:-local}" = "ci" ]; then
    printf 'ci-%s' "${E2E_RUN_ID:-unknown}"
  else
    printf 'dev-local'
  fi
}

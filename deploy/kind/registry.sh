#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME="${KIND_CLUSTER_NAME:-release-manager-dev}"
REGISTRY_NAME="${KIND_REGISTRY_NAME:-kind-registry}"
REGISTRY_PORT="${KIND_REGISTRY_PORT:-5001}"
REGISTRY_IMAGE="${KIND_REGISTRY_IMAGE:-registry:3}"
NETWORK_NAME="${KIND_NETWORK_NAME:-kind-registry}"
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

log() {
  printf '[kind] %s\n' "$*"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || {
    printf 'missing required command: %s\n' "$1" >&2
    exit 127
  }
}

registry_running() {
  docker inspect -f '{{.State.Running}}' "$REGISTRY_NAME" 2>/dev/null | grep -q '^true$'
}

ensure_network() {
  if ! docker network inspect "$NETWORK_NAME" >/dev/null 2>&1; then
    docker network create "$NETWORK_NAME" >/dev/null
  fi
}

ensure_registry() {
  ensure_network
  if docker inspect "$REGISTRY_NAME" >/dev/null 2>&1; then
    if ! registry_running; then
      docker start "$REGISTRY_NAME" >/dev/null
    fi
  else
    docker run -d --restart=unless-stopped \
      --name "$REGISTRY_NAME" \
      --network "$NETWORK_NAME" \
      -p "127.0.0.1:${REGISTRY_PORT}:5000" \
      "$REGISTRY_IMAGE" >/dev/null
  fi
}

cluster_exists() {
  kind get clusters 2>/dev/null | grep -Fxq "$CLUSTER_NAME"
}

configure_registry() {
  local node registry_dir
  registry_dir="/etc/containerd/certs.d/localhost:${REGISTRY_PORT}"
  for node in $(kind get nodes --name "$CLUSTER_NAME"); do
    docker exec "$node" mkdir -p "$registry_dir"
    printf '[host."http://%s:5000"]\n' "$REGISTRY_NAME" |
      docker exec -i "$node" cp /dev/stdin "$registry_dir/hosts.toml"
  done
  docker network connect kind "$REGISTRY_NAME" 2>/dev/null || true
}

up() {
  require_command docker
  require_command kind
  ensure_registry
  if cluster_exists; then
    log "cluster $CLUSTER_NAME already exists"
    configure_registry
    return
  fi
  kind create cluster --name "$CLUSTER_NAME" --config "$SCRIPT_DIR/config.yaml"
  configure_registry
  log "cluster $CLUSTER_NAME is ready"
  log "registry endpoint: localhost:${REGISTRY_PORT}"
}

down() {
  require_command kind
  if cluster_exists; then
    kind delete cluster --name "$CLUSTER_NAME"
  else
    log "cluster $CLUSTER_NAME does not exist"
  fi
  log "registry $REGISTRY_NAME retained"
}

status() {
  require_command docker
  require_command kind
  if cluster_exists; then
    log "cluster $CLUSTER_NAME exists"
  else
    log "cluster $CLUSTER_NAME absent"
  fi
  if registry_running; then
    log "registry $REGISTRY_NAME is running on 127.0.0.1:${REGISTRY_PORT}"
  else
    log "registry $REGISTRY_NAME is absent or stopped"
  fi
}

usage() {
  printf 'usage: %s {up|down|status}\n' "$0" >&2
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  status) status ;;
  *) usage; exit 2 ;;
esac

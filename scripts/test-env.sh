#!/usr/bin/env bash
# =============================================================================
# test-env.sh — 本地测试环境部署入口
#
# 用法:
#   scripts/test-env.sh deploy     # 创建/复用 kind 测试环境
#   scripts/test-env.sh --reuse    # 同 deploy，显式复用已有集群
#   scripts/test-env.sh --status   # 查看环境状态
#   scripts/test-env.sh --cleanup  # 销毁环境
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-release-operator-dev}"
MANAGER_PORT="${MANAGER_PORT:-8080}"
GRPC_PORT="${GRPC_PORT:-8443}"
CUSTOMER_ID="${CUSTOMER_ID:-localhost001}"

G='\033[0;32m'; Y='\033[1;33m'; B='\033[0;34m'; N='\033[0m'
log() { echo -e "${G}✓${N} $*"; }
info() { echo -e "${B}➤${N} $*"; }
warn() { echo -e "${Y}!${N} $*"; }

require_tools() {
  for tool in kind kubectl helm docker; do
    command -v "${tool}" >/dev/null 2>&1 || { echo "missing required tool: ${tool}" >&2; exit 1; }
  done
}

status() {
  info "kind cluster: ${CLUSTER_NAME}"
  if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
    log "cluster exists"
  else
    warn "cluster not found"
    return 0
  fi

  kubectl cluster-info --context "kind-${CLUSTER_NAME}" || true
  kubectl get pods -A --context "kind-${CLUSTER_NAME}" || true
  echo
  info "manager endpoints"
  echo "  HTTP health: http://localhost:${MANAGER_PORT}/health"
  echo "  gRPC:        localhost:${GRPC_PORT}"
  echo "  customer:    ${CUSTOMER_ID}"
}

deploy() {
  require_tools
  info "deploy test environment via scripts/dev-setup.sh"
  CLUSTER_NAME="${CLUSTER_NAME}" \
  CUSTOMER_ID="${CUSTOMER_ID}" \
  MANAGER_PORT="${MANAGER_PORT}" \
  GRPC_PORT="${GRPC_PORT}" \
    "${SCRIPT_DIR}/dev-setup.sh" deploy
  status
}

cleanup() {
  require_tools
  "${SCRIPT_DIR}/dev-setup.sh" --cleanup
}

case "${1:-deploy}" in
  deploy|--deploy|--reuse) deploy ;;
  --status|status) require_tools; status ;;
  --cleanup|cleanup|destroy) cleanup ;;
  *) echo "usage: $0 {deploy|--reuse|--status|--cleanup}" >&2; exit 2 ;;
esac

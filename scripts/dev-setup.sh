#!/usr/bin/env bash
# =============================================================================
# dev-setup.sh — Release Manager 本地开发环境一键部署+清理
#
# 自动化链路:
#   Harbor webhook → release-manager → gRPC → release-operator → helm upgrade
#   镜像拉取: K8s Pod → localhost:5000 (registry proxy) → Harbor (cache)
#
# 用法:
#   ./scripts/dev-setup.sh                 # 完整部署
#   ./scripts/dev-setup.sh --cleanup       # 一键清理
#   ./scripts/dev-setup.sh --manager       # 仅本地运行 manager
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
ENV_FILE="${PROJECT_DIR}/.env"

CLUSTER_NAME="${CLUSTER_NAME:-release-operator-dev}"
CUSTOMER_ID="${CUSTOMER_ID:-localhost001}"
MANAGER_PORT="${MANAGER_PORT:-8080}"
GRPC_PORT="${GRPC_PORT:-8443}"
REGISTRY_PORT="${REGISTRY_PORT:-30500}"

# Harbor (from .env)
HARBOR_URL=""; HARBOR_ROBOT_NAME=""; HARBOR_ROBOT_TOKEN=""; HARBOR_REGISTRY=""

G='\033[0;32m'; Y='\033[1;33m'; B='\033[0;34m'; C='\033[0;36m'; N='\033[0m'
log()   { echo -e "${G}  ✓${N} $*"; }
info()  { echo -e "${B}  ➤${N} $*"; }
warn()  { echo -e "${Y}  !${N} $*"; }
phase() { echo -e "\n${C}══ $* ══${N}"; }

load_env() {
    if [ -f "$ENV_FILE" ]; then
        set -a; source "$ENV_FILE"; set +a
        HARBOR_URL="${HARBOR_URL:-}"
        HARBOR_ROBOT_NAME="${HARBOR_ROBOT_NAME:-}"
        HARBOR_ROBOT_TOKEN="${HARBOR_ROBOT_TOKEN:-}"
    fi
    if [ -n "$HARBOR_URL" ]; then
        HARBOR_REGISTRY=$(echo "$HARBOR_URL" | sed 's|\(https\?://[^/]*\).*|\1|')
    fi
    HARBOR_REGISTRY="${HARBOR_REGISTRY:-https://harbor.example.com}"
    info "Harbor: ${HARBOR_REGISTRY}"
}

check() {
    phase "前置检查"
    for t in kind kubectl helm docker; do
        command -v "$t" &>/dev/null && log "$t: $(command -v "$t")" || { echo "  ✗ $t 未安装"; exit 1; }
    done
}

create_cluster() {
    phase "Step 1/6: kind 集群"
    if kind get clusters 2>/dev/null | grep -q "${CLUSTER_NAME}"; then
        info "集群已存在，跳过"; return 0
    fi
    cat <<EOF | kind create cluster --name "${CLUSTER_NAME}" --wait 2m --config -
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraPortMappings:
      - containerPort: ${REGISTRY_PORT}
        hostPort: ${REGISTRY_PORT}
        protocol: TCP
      - containerPort: ${GRPC_PORT}
        hostPort: ${GRPC_PORT}
        protocol: TCP
      - containerPort: ${MANAGER_PORT}
        hostPort: ${MANAGER_PORT}
        protocol: TCP
EOF
    log "集群就绪: ${CLUSTER_NAME}"
}

deploy_registry() {
    phase "Step 2/6: Registry Proxy (localhost:${REGISTRY_PORT} → Harbor)"
    # 预加载 registry 镜像到 kind（kind 内无网络拉取镜像）
    local reg_image="registry:3.0.0"
    if docker image inspect "${reg_image}" &>/dev/null; then
        info "registry 镜像已存在，跳过拉取"
    else
        info "拉取 ${reg_image}..."
        docker pull "${reg_image}" 2>&1 | tail -1
    fi
    kind load docker-image "${reg_image}" --name "${CLUSTER_NAME}"
    log "registry 镜像已加载到 kind"

    helm upgrade --install registry-proxy "${PROJECT_DIR}/deployments/registry-proxy" \
        --namespace registry --create-namespace \
        --set harbor.url="${HARBOR_REGISTRY}" \
        --set harbor.username="${HARBOR_ROBOT_NAME}" \
        --set harbor.password="${HARBOR_ROBOT_TOKEN}" \
        --set harbor.insecure=true \
        --set service.nodePort="${REGISTRY_PORT}" \
        --set persistence.enabled=false \
        --timeout 2m 2>&1 | tail -2
    kubectl wait --for=condition=Ready pod -l app=registry-proxy -n registry --timeout=60s 2>/dev/null || true
    log "Registry proxy: localhost:${REGISTRY_PORT}"
}

generate_certs() {
    phase "Step 3/6: mTLS 证书"
    local d="${PROJECT_DIR}/certs"
    mkdir -p "${d}/ca" "${d}/server" "${d}/${CUSTOMER_ID}"
    if [ ! -f "${d}/ca/ca.key" ]; then
        openssl genrsa -out "${d}/ca/ca.key" 4096
        openssl req -x509 -new -nodes -key "${d}/ca/ca.key" -sha256 -days 3650 \
            -subj "/O=Release Manager/CN=release-manager-ca" -out "${d}/ca/ca.crt"
    fi
    openssl genrsa -out "${d}/${CUSTOMER_ID}/tls.key" 2048
    openssl req -new -key "${d}/${CUSTOMER_ID}/tls.key" -out "${d}/${CUSTOMER_ID}/tls.csr" \
        -subj "/O=Customer/CN=${CUSTOMER_ID}"
    openssl x509 -req -in "${d}/${CUSTOMER_ID}/tls.csr" -CA "${d}/ca/ca.crt" \
        -CAkey "${d}/ca/ca.key" -CAcreateserial -out "${d}/${CUSTOMER_ID}/tls.crt" \
        -days 1095 -sha256; rm -f "${d}/${CUSTOMER_ID}/tls.csr"
    FINGERPRINT=$(openssl x509 -in "${d}/${CUSTOMER_ID}/tls.crt" -noout -fingerprint -sha256 | sed 's/.*=//' | tr -d ':')
    echo "${FINGERPRINT}" > "${d}/${CUSTOMER_ID}/fingerprint.txt"
    log "证书: ${d}/${CUSTOMER_ID}/tls.crt"
    log "指纹: ${FINGERPRINT}"
}

build_deploy_operator() {
    local force_build="${1:-false}"
    phase "Step 4/6: release-operator Helm → kind"
    cd "${PROJECT_DIR}"
    if [ "$force_build" = "true" ] || ! docker image inspect release-operator:dev &>/dev/null; then
        [ "$force_build" = "true" ] && info "强制重建 operator 镜像..." || info "构建 operator 镜像..."
        info "编译 Go 二进制..."
        CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/release-operator ./cmd/release-operator/
        docker build -f Dockerfile.operator -t release-operator:dev . 2>&1 | tail -1
    else
        info "operator 镜像已缓存，跳过构建"
    fi
    kind load docker-image release-operator:dev --name "${CLUSTER_NAME}"

    local d="${PROJECT_DIR}/certs/${CUSTOMER_ID}"
    local ca="${PROJECT_DIR}/certs/ca"
    local ns="release-operator"
    kubectl create ns "${ns}" --dry-run=client -o yaml | kubectl apply -f -
    kubectl create secret tls release-operator-tls --cert="${d}/tls.crt" --key="${d}/tls.key" \
        -n "${ns}" --dry-run=client -o yaml | kubectl apply -f -
    kubectl create secret generic release-operator-ca --from-file=ca.crt="${ca}/ca.crt" \
        -n "${ns}" --dry-run=client -o yaml | kubectl apply -f -

    # Harbor CA (自签)
    local ca_crt; ca_crt=$(curl -k -s --connect-timeout 5 "${HARBOR_REGISTRY}/api/v2.0/systeminfo/getcert" 2>/dev/null || true)
    [ -n "$ca_crt" ] && kubectl create secret generic harbor-ca --from-literal=ca.crt="$ca_crt" \
        -n "${ns}" --dry-run=client -o yaml | kubectl apply -f - && log "Harbor CA 注入" || warn "Harbor CA 获取失败(可能非自签)"

    kubectl create secret generic harbor-creds \
        --from-literal=username="${HARBOR_ROBOT_NAME}" --from-literal=password="${HARBOR_ROBOT_TOKEN}" \
        -n "${ns}" --dry-run=client -o yaml | kubectl apply -f -

    helm upgrade --install release-operator "${PROJECT_DIR}/deployments/release-operator" -n "${ns}" \
        --set customerID="${CUSTOMER_ID}" \
        --set notificationEndpoint="host.docker.internal:${GRPC_PORT}" \
        --set image.repository=release-operator --set image.tag=dev --set image.pullPolicy=IfNotPresent \
        --set tls.enabled=true --set tls.existingCertSecret=release-operator-tls --set tls.existingCaSecret=release-operator-ca \
        --set harbor.url="${HARBOR_REGISTRY}" --set harbor.existingSecret=harbor-creds --set harbor.insecureSkipVerify=true \
        --set rbac.managedNamespaces[0]=default --set networkPolicy.enabled=false \
        --timeout 2m 2>&1 | tail -2
    kubectl wait --for=condition=Ready pod -l app.kubernetes.io/name=release-operator -n "${ns}" --timeout=60s 2>/dev/null || true
    log "release-operator 已部署"
}

start_manager() {
    phase "Step 5/6: release-manager 本地运行"
    local fp; fp=$(cat "${PROJECT_DIR}/certs/${CUSTOMER_ID}/fingerprint.txt" 2>/dev/null)
    echo -e "${C}╔═══════════════════════════════════════════════════════╗${N}"
    echo -e "${C}║  环境就绪！                                           ║${N}"
    echo -e "${C}║  Registry:  localhost:${REGISTRY_PORT} → ${HARBOR_REGISTRY}    ${N}"
    echo -e "${C}║  Customer:  ${CUSTOMER_ID}  |  指纹: ${fp:0:16}...      ${N}"
    echo -e "${C}║                                                       ║${N}"
    echo -e "${C}║  启动 manager:  make dev-manager                       ${N}"
    echo -e "${C}║  启动前端:      make dev-web                           ${N}"
    echo -e "${C}║  注册客户:      make dev-register                      ${N}"
    echo -e "${C}║  清理:          make dev-down                          ${N}"
    echo -e "${C}║                                                       ║${N}"
    echo -e "${C}║  Harbor webhook → http://host.docker.internal:${MANAGER_PORT}/api/v1/webhook/harbor${N}"
    echo -e "${C}╚═══════════════════════════════════════════════════════╝${N}"
}

cleanup() {
    phase "清理"
    kind delete cluster --name "${CLUSTER_NAME}" 2>/dev/null && log "集群已删除" || info "不存在"
    log "完成"
}

main() {
    case "${1:-deploy}" in
        --cleanup|cleanup) cleanup; exit 0 ;;
        --manager)       check; load_env; start_manager; exit 0 ;;
        deploy|--deploy|"")
            check; load_env
            create_cluster
            deploy_registry
            generate_certs
            build_deploy_operator false
            start_manager ;;
        *) echo "用法: $0 {deploy|--cleanup|--manager}"; exit 1 ;;
    esac
}
main "$@"

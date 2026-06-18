#!/usr/bin/env bash
# =============================================================================
# dev-setup.sh — release-operator 本地开发环境一键部署
#
# 模拟客户私有化部署场景:
#   1. 创建 kind 集群 (模拟客户环境)
#   2. 安装 ingress-nginx (复用 gateway-api-crd_helmchart)
#   3. 从 .env 读取 Harbor 凭证
#   4. 生成自签名 mTLS 证书
#   5. 构建 release-operator 镜像并加载到 kind
#   6. Helm 部署 release-operator (customerID=localhost001)
#
# 用法:
#   ./scripts/dev-setup.sh               # 完整部署
#   ./scripts/dev-setup.sh --cleanup     # 清理
#   ./scripts/dev-setup.sh --operator    # 仅重新部署 operator
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
INGRESS_CHART="${PROJECT_DIR}/gateway-api-crd_helmchart/ingress-nginx"
ENV_FILE="${PROJECT_DIR}/.env"

# --- 默认值 ---
CLUSTER_NAME="${CLUSTER_NAME:-release-operator-dev}"
CUSTOMER_ID="${CUSTOMER_ID:-localhost001}"
OPERATOR_IMAGE="${OPERATOR_IMAGE:-release-operator:dev}"
OPERATOR_NAMESPACE="${OPERATOR_NAMESPACE:-release-operator}"
MANAGER_HTTP_PORT="${MANAGER_HTTP_PORT:-8080}"
MANAGER_GRPC_PORT="${MANAGER_GRPC_PORT:-8443}"

# Harbor 配置 (从 .env 读取)
HARBOR_URL=""
HARBOR_ROBOT_NAME=""
HARBOR_ROBOT_TOKEN=""

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; CYAN='\033[0;36m'; NC='\033[0m'
log()   { echo -e "${GREEN}  ✓${NC} $*"; }
info()  { echo -e "${BLUE}  ➤${NC} $*"; }
warn()  { echo -e "${YELLOW}  !${NC} $*"; }
phase() { echo -e "\n${CYAN}══ $* ══${NC}"; }

# =============================================================================
load_env() {
    # 加载 .env 文件中的 Harbor 配置
    if [ -f "$ENV_FILE" ]; then
        info "加载 .env: ${ENV_FILE}"
        # shellcheck source=/dev/null
        set -a; source "$ENV_FILE"; set +a
        HARBOR_URL="${HARBOR_URL:-}"
        HARBOR_ROBOT_NAME="${HARBOR_ROBOT_NAME:-}"
        HARBOR_ROBOT_TOKEN="${HARBOR_ROBOT_TOKEN:-}"
    else
        warn ".env 文件不存在: ${ENV_FILE} — Harbor 配置将为空"
    fi

    # 从 HARBOR_URL 提取 registry host (去掉路径，只保留 scheme://host)
    if [ -n "$HARBOR_URL" ]; then
        # e.g. https://120.77.206.182/harbor/robot-accounts → https://120.77.206.182
        HARBOR_REGISTRY=$(echo "$HARBOR_URL" | sed 's|\(https\?://[^/]*\).*|\1|')
        info "Harbor registry: ${HARBOR_REGISTRY}"
        info "Harbor robot:    ${HARBOR_ROBOT_NAME}"
    else
        HARBOR_REGISTRY="https://harbor.example.com"
        warn "HARBOR_URL 未设置，使用默认值: ${HARBOR_REGISTRY}"
    fi
}

# =============================================================================
check_prereqs() {
    phase "前置检查"
    for tool in kind kubectl helm docker go; do
        command -v "$tool" &>/dev/null && log "$tool: $(command -v "$tool")" || {
            echo "  ✗ $tool: 未安装"; exit 1
        }
    done
}

create_kind_cluster() {
    phase "Step 1/6: 创建 kind 集群 (模拟客户私有化环境)"
    if kind get clusters 2>/dev/null | grep -q "${CLUSTER_NAME}"; then
        info "集群 '${CLUSTER_NAME}' 已存在，跳过创建"
        return 0
    fi

    cat <<EOF | kind create cluster --name "${CLUSTER_NAME}" --wait 2m --config -
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraPortMappings:
      - containerPort: 30080
        hostPort: 30080
        protocol: TCP
      - containerPort: ${MANAGER_GRPC_PORT}
        hostPort: ${MANAGER_GRPC_PORT}
        protocol: TCP
EOF
    log "kind 集群就绪: ${CLUSTER_NAME}"
}

install_ingress_nginx() {
    phase "Step 2/6: 安装 ingress-nginx"

    if kubectl get pods -n kube-system -l app.kubernetes.io/name=ingress-nginx \
        --field-selector=status.phase=Running --no-headers 2>/dev/null | grep -q .; then
        info "ingress-nginx 已运行，跳过"
        return 0
    fi

    if [ -d "$INGRESS_CHART" ]; then
        info "使用本地 chart: ${INGRESS_CHART}"
        helm upgrade --install ingress-nginx "$INGRESS_CHART" \
            --namespace kube-system --create-namespace \
            --set controller.service.type=NodePort \
            --set controller.service.nodePorts.http=30080 \
            --set controller.ingressClassResource.default=true \
            --set controller.watchIngressWithoutClass=true \
            --timeout 5m 2>&1 | tail -3
    else
        info "本地 chart 不存在，使用 Helm repo"
        helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx 2>/dev/null || true
        helm repo update
        helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
            --namespace kube-system --create-namespace \
            --set controller.service.type=NodePort \
            --set controller.service.nodePorts.http=30080 \
            --timeout 5m 2>&1 | tail -3
    fi

    kubectl wait --for=condition=Ready pod -l app.kubernetes.io/name=ingress-nginx \
        -n kube-system --timeout=120s 2>/dev/null || true
}

generate_certs() {
    phase "Step 3/6: 生成本地开发 mTLS 证书"

    local cert_dir="${PROJECT_DIR}/certs"
    local ca_dir="${cert_dir}/ca"
    local server_dir="${cert_dir}/server"
    local client_dir="${cert_dir}/${CUSTOMER_ID}"

    mkdir -p "${ca_dir}" "${server_dir}" "${client_dir}"

    if [ ! -f "${ca_dir}/ca.key" ]; then
        info "生成 CA (10 年)..."
        openssl genrsa -out "${ca_dir}/ca.key" 4096
        openssl req -x509 -new -nodes -key "${ca_dir}/ca.key" -sha256 -days 3650 \
            -subj "/O=Release Manager Dev/CN=release-manager-ca" -out "${ca_dir}/ca.crt"
    fi

    info "生成 Server 证书..."
    openssl genrsa -out "${server_dir}/tls.key" 2048
    openssl req -new -key "${server_dir}/tls.key" -out "${server_dir}/tls.csr" \
        -subj "/O=Release Manager/CN=release-manager"
    openssl x509 -req -in "${server_dir}/tls.csr" -CA "${ca_dir}/ca.crt" -CAkey "${ca_dir}/ca.key" \
        -CAcreateserial -out "${server_dir}/tls.crt" -days 1095 -sha256
    rm -f "${server_dir}/tls.csr"

    info "生成 Client 证书 (${CUSTOMER_ID})..."
    openssl genrsa -out "${client_dir}/tls.key" 2048
    openssl req -new -key "${client_dir}/tls.key" -out "${client_dir}/tls.csr" \
        -subj "/O=Customer/CN=${CUSTOMER_ID}"
    openssl x509 -req -in "${client_dir}/tls.csr" -CA "${ca_dir}/ca.crt" -CAkey "${ca_dir}/ca.key" \
        -CAcreateserial -out "${client_dir}/tls.crt" -days 1095 -sha256
    rm -f "${client_dir}/tls.csr"

    FINGERPRINT=$(openssl x509 -in "${client_dir}/tls.crt" -noout -fingerprint -sha256 | sed 's/.*=//' | tr -d ':')
    echo "${FINGERPRINT}" > "${client_dir}/fingerprint.txt"

    log "证书生成完成"
    log "Client 指纹: ${FINGERPRINT}"
}

build_and_deploy_operator() {
    phase "Step 4/6: 构建并部署 release-operator 到 kind"

    info "构建 operator 镜像..."
    cd "${PROJECT_DIR}"
    docker build -f Dockerfile.operator -t "${OPERATOR_IMAGE}" . 2>&1 | tail -3
    kind load docker-image "${OPERATOR_IMAGE}" --name "${CLUSTER_NAME}"
    log "镜像加载到 kind"

    local cert_dir="${PROJECT_DIR}/certs/${CUSTOMER_ID}"
    local ca_dir="${PROJECT_DIR}/certs/ca"

    info "Helm 部署 release-operator (customerID=${CUSTOMER_ID})..."

    kubectl create namespace "${OPERATOR_NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

    # TLS secrets
    kubectl create secret tls release-operator-tls \
        --cert="${cert_dir}/tls.crt" --key="${cert_dir}/tls.key" \
        --namespace="${OPERATOR_NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
    kubectl create secret generic release-operator-ca \
        --from-file=ca.crt="${ca_dir}/ca.crt" \
        --namespace="${OPERATOR_NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

    # Harbor credential secret (from .env)
    if [ -n "$HARBOR_ROBOT_NAME" ] && [ -n "$HARBOR_ROBOT_TOKEN" ]; then
        kubectl create secret generic harbor-creds \
            --from-literal=username="${HARBOR_ROBOT_NAME}" \
            --from-literal=password="${HARBOR_ROBOT_TOKEN}" \
            --namespace="${OPERATOR_NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
        log "Harbor 凭证 Secret 已创建 (robot: ${HARBOR_ROBOT_NAME})"
    fi

    # Harbor 自签 CA 证书 (自动获取)
    local ca_crt
    ca_crt=$(curl -k -s --connect-timeout 5 "${HARBOR_REGISTRY}/api/v2.0/systeminfo/getcert" 2>/dev/null || true)
    if [ -n "$ca_crt" ]; then
        kubectl create secret generic harbor-ca \
            --from-literal=ca.crt="$ca_crt" \
            --namespace="${OPERATOR_NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
        log "Harbor 自签 CA 证书已获取并注入 K8s Secret"
    else
        warn "无法自动获取 Harbor CA 证书 (可能已使用公共 CA，跳过)"
    fi

    # Helm install operator
    helm upgrade --install release-operator "${PROJECT_DIR}/deployments/release-operator" \
        --namespace "${OPERATOR_NAMESPACE}" \
        --set customerID="${CUSTOMER_ID}" \
        --set notificationEndpoint="host.docker.internal:${MANAGER_GRPC_PORT}" \
        --set image.repository="${OPERATOR_IMAGE}" \
        --set image.tag=dev \
        --set image.pullPolicy=IfNotPresent \
        --set tls.enabled=true \
        --set tls.existingCertSecret=release-operator-tls \
        --set tls.existingCaSecret=release-operator-ca \
        --set harbor.url="${HARBOR_REGISTRY}" \
        --set harbor.existingSecret=harbor-creds \
        --set harbor.insecureSkipVerify=true \
        --set rbac.managedNamespaces[0]=default \
        --set rbac.managedNamespaces[1]="${OPERATOR_NAMESPACE}" \
        --set networkPolicy.enabled=false \
        --timeout 2m 2>&1 | tail -3

    kubectl wait --for=condition=Ready pod -l app.kubernetes.io/name=release-operator \
        -n "${OPERATOR_NAMESPACE}" --timeout=60s 2>/dev/null || true
    log "release-operator 部署完成"
    kubectl get pods -n "${OPERATOR_NAMESPACE}"
}

show_summary() {
    phase "Step 5/6: 开发环境就绪"

    local fingerprint_file="${PROJECT_DIR}/certs/${CUSTOMER_ID}/fingerprint.txt"
    local fingerprint
    fingerprint=$(cat "$fingerprint_file" 2>/dev/null || echo "unknown")

    echo ""
    echo -e "${CYAN}╔═══════════════════════════════════════════════════════╗${NC}"
    echo -e "${CYAN}║  本地开发环境就绪！                                    ║${NC}"
    echo -e "${CYAN}╠═══════════════════════════════════════════════════════╣${NC}"
    echo -e "${CYAN}║  Kind 集群:      ${CLUSTER_NAME}                       ${NC}"
    echo -e "${CYAN}║  Customer ID:    ${CUSTOMER_ID}                       ${NC}"
    echo -e "${CYAN}║  Harbor:         ${HARBOR_REGISTRY}                   ${NC}"
    echo -e "${CYAN}║  证书指纹:       ${fingerprint:0:16}...                ${NC}"
    echo -e "${CYAN}╠═══════════════════════════════════════════════════════╣${NC}"
    echo -e "${CYAN}║  启动 release-manager:                                 ${NC}"
    echo -e "${CYAN}║    cd ${PROJECT_DIR} && make dev-manager               ${NC}"
    echo -e "${CYAN}║                                                        ${NC}"
    echo -e "${CYAN}║  启动前端:                                             ${NC}"
    echo -e "${CYAN}║    cd ${PROJECT_DIR} && make dev-web                   ${NC}"
    echo -e "${CYAN}║                                                        ${NC}"
    echo -e "${CYAN}║  注册客户:     make dev-register                       ${NC}"
    echo -e "${CYAN}║  清理环境:     make dev-down                            ${NC}"
    echo -e "${CYAN}║                                                        ${NC}"
    echo -e "${CYAN}║  前端: http://localhost:3000                            ${NC}"
    echo -e "${CYAN}║  API:  http://localhost:${MANAGER_HTTP_PORT}/health         ${NC}"
    echo -e "${CYAN}╚═══════════════════════════════════════════════════════╝${NC}"
}

cleanup() {
    phase "清理环境"
    kind delete cluster --name "${CLUSTER_NAME}" 2>/dev/null && log "集群已删除" || info "集群不存在"
    log "清理完成"
}

# =============================================================================
main() {
    load_env

    case "${1:-deploy}" in
    --cleanup|cleanup)
        cleanup ;;
    --operator)
        check_prereqs
        build_and_deploy_operator ;;
    deploy|--deploy|"")
        check_prereqs
        create_kind_cluster
        install_ingress_nginx
        generate_certs
        build_and_deploy_operator
        show_summary ;;
    *)
        echo "用法: $0 {deploy|--cleanup|--operator}" ; exit 1 ;;
    esac
}

main "$@"

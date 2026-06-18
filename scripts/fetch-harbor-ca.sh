#!/usr/bin/env bash
# =============================================================================
# fetch-harbor-ca.sh — 获取 Harbor 自签 CA 证书
#
# 当 Harbor 使用自签证书时，需要在 K8s 集群中信任该 CA。
# 此脚本从 Harbor 获取 CA 证书并输出到 stdout 或文件。
#
# 用法:
#   # 输出到 stdout
#   ./scripts/fetch-harbor-ca.sh https://harbor.example.com
#
#   # 保存到文件
#   ./scripts/fetch-harbor-ca.sh https://120.77.206.182 --output certs/harbor-ca.crt
#
#   # 直接注入 K8s Secret (需要 kubectl 已配置)
#   ./scripts/fetch-harbor-ca.sh https://harbor.example.com --inject release-operator
#
# 在 K8s 中的使用:
#   Harbor CA Secret → volume mount → Helm SDK 信任 → OCI pull 成功
# =============================================================================
set -euo pipefail

HARBOR_URL="${1:-}"
OUTPUT_FILE=""
INJECT_NAMESPACE=""
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
log() { echo -e "${GREEN}  ✓${NC} $*"; }
warn() { echo -e "${YELLOW}  !${NC} $*"; }

if [ -z "$HARBOR_URL" ]; then
    echo "用法: $0 <harbor-url> [--output <file>] [--inject <namespace>]"
    echo ""
    echo "示例:"
    echo "  $0 https://harbor.example.com"
    echo "  $0 https://120.77.206.182 --output certs/harbor-ca.crt"
    echo "  $0 https://harbor.example.com --inject release-operator"
    echo ""
    echo "环境变量:"
    echo "  HARBOR_URL     Harbor 地址 (可从 .env 读取)"
    exit 1
fi

shift
while [ $# -gt 0 ]; do
    case "$1" in
        --output) OUTPUT_FILE="$2"; shift 2 ;;
        --inject) INJECT_NAMESPACE="$2"; shift 2 ;;
        *) echo "未知参数: $1"; exit 1 ;;
    esac
done

# 获取 Harbor CA 证书
# Harbor API: GET /api/v2.0/systeminfo/getcert 返回 PEM 格式证书
CERT_ENDPOINT="${HARBOR_URL}/api/v2.0/systeminfo/getcert"

log "从 Harbor 获取 CA 证书: ${CERT_ENDPOINT}"
CERT_PEM=$(curl -k -s --connect-timeout 10 "$CERT_ENDPOINT" 2>/dev/null)

if [ -z "$CERT_PEM" ]; then
    warn "Harbor API 无响应，尝试 OpenSSL s_client 方式..."
    HARBOR_HOST=$(echo "$HARBOR_URL" | sed 's|https\?://||; s|/.*||')
    CERT_PEM=$(echo | openssl s_client -connect "${HARBOR_HOST}:443" -servername "${HARBOR_HOST}" 2>/dev/null \
        | openssl x509 2>/dev/null || true)
fi

if [ -z "$CERT_PEM" ]; then
    echo "ERROR: 无法获取 Harbor CA 证书"
    echo "请手动执行:"
    echo "  curl -k ${CERT_ENDPOINT} > harbor-ca.crt"
    exit 1
fi

# 输出到文件
if [ -n "$OUTPUT_FILE" ]; then
    echo "$CERT_PEM" > "$OUTPUT_FILE"
    log "CA 证书已保存: ${OUTPUT_FILE}"
fi

# 注入到 K8s Secret
if [ -n "$INJECT_NAMESPACE" ]; then
    SECRET_NAME="harbor-ca"
    kubectl create secret generic "$SECRET_NAME" \
        --from-literal=ca.crt="$CERT_PEM" \
        --namespace="$INJECT_NAMESPACE" \
        --dry-run=client -o yaml | kubectl apply -f -
    log "CA 证书已注入 K8s Secret: ${SECRET_NAME} (ns: ${INJECT_NAMESPACE})"

    # 重启 release-operator 使新 Secret 生效
    kubectl rollout restart deployment/release-operator -n "$INJECT_NAMESPACE" 2>/dev/null || true
fi

# 如果没有 --output 和 --inject，输出到 stdout
if [ -z "$OUTPUT_FILE" ] && [ -z "$INJECT_NAMESPACE" ]; then
    echo "$CERT_PEM"
fi

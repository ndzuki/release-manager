#!/usr/bin/env bash
# =============================================================================
# 生成 mTLS 证书体系 — CA, Server (notification), Client (operator per customer)
#
# 证书生命周期由 release-notification 侧控制（持有 CA 私钥）。
# 默认有效期: CA=10年, Server=3年, Client=3年。
# 到期前通过此脚本重新生成证书并通过 Helm 升级推送到客户集群。
#
# 用法:
#   ./scripts/generate-certs.sh <customer-id> [options]
#
# 选项:
#   --ca-validity DAYS       CA 证书有效期（天），默认 3650（10 年）
#   --server-validity DAYS   Server 证书有效期（天），默认 1095（3 年）
#   --client-validity DAYS   Client 证书有效期（天），默认 1095（3 年）
#   --renew                  强制重新生成已有证书（覆盖）
#
# 示例:
#   ./scripts/generate-certs.sh customer-001
#   ./scripts/generate-certs.sh customer-001 --client-validity 1825
#   ./scripts/generate-certs.sh customer-001 --renew --server-validity 3650
#
# 输出:
#   certs/ca/ca.crt, ca.key                    - CA 证书和私钥
#   certs/server/tls.crt, tls.key              - 服务端证书 (notification)
#   certs/<customer-id>/tls.crt, tls.key       - 客户端证书 (operator)
#   certs/<customer-id>/fingerprint.txt        - SHA256 指纹
# =============================================================================

set -euo pipefail

# --- 默认有效期 ---
CA_VALIDITY=3650       # 10 年
SERVER_VALIDITY=1095   # 3 年
CLIENT_VALIDITY=1095   # 3 年
FORCE_RENEW=false
CUSTOMER_ID=""

# --- 解析参数 ---
while [[ $# -gt 0 ]]; do
    case "$1" in
        --ca-validity)
            CA_VALIDITY="$2"; shift 2 ;;
        --server-validity)
            SERVER_VALIDITY="$2"; shift 2 ;;
        --client-validity)
            CLIENT_VALIDITY="$2"; shift 2 ;;
        --renew)
            FORCE_RENEW=true; shift ;;
        -*)
            echo "Unknown option: $1"
            echo "Usage: $0 <customer-id> [--ca-validity DAYS] [--server-validity DAYS] [--client-validity DAYS] [--renew]"
            exit 1 ;;
        *)
            if [ -z "$CUSTOMER_ID" ]; then
                CUSTOMER_ID="$1"; shift
            else
                echo "Unexpected argument: $1"
                exit 1
            fi ;;
    esac
done

if [ -z "$CUSTOMER_ID" ]; then
    echo "Usage: $0 <customer-id> [--ca-validity DAYS] [--server-validity DAYS] [--client-validity DAYS] [--renew]"
    echo ""
    echo "Arguments:"
    echo "  customer-id              客户唯一标识（必填）"
    echo ""
    echo "Options:"
    echo "  --ca-validity DAYS       CA 证书有效期天数，默认 3650（10 年）"
    echo "  --server-validity DAYS   Server 证书有效期天数，默认 1095（3 年）"
    echo "  --client-validity DAYS   Client 证书有效期天数，默认 1095（3 年）"
    echo "  --renew                  强制重新生成已有证书"
    echo ""
    echo "Examples:"
    echo "  $0 customer-001"
    echo "  $0 customer-001 --client-validity 1825        # 客户端证书 5 年"
    echo "  $0 customer-001 --renew --server-validity 3650 # 重新生成 server 10 年"
    exit 1
fi

CERT_DIR="certs"
CA_DIR="${CERT_DIR}/ca"
SERVER_DIR="${CERT_DIR}/server"
CUSTOMER_DIR="${CERT_DIR}/${CUSTOMER_ID}"

echo "================================================"
echo "mTLS 证书生成"
echo "  Customer:       ${CUSTOMER_ID}"
echo "  CA 有效期:       ${CA_VALIDITY} 天 ($(python3 -c "print(f'{${CA_VALIDITY}/365:.1f}')" 2>/dev/null || echo "${CA_VALIDITY}/365") 年)"
echo "  Server 有效期:   ${SERVER_VALIDITY} 天 ($(python3 -c "print(f'{${SERVER_VALIDITY}/365:.1f}')" 2>/dev/null || echo "${SERVER_VALIDITY}/365") 年)"
echo "  Client 有效期:   ${CLIENT_VALIDITY} 天 ($(python3 -c "print(f'{${CLIENT_VALIDITY}/365:.1f}')" 2>/dev/null || echo "${CLIENT_VALIDITY}/365") 年)"
echo "================================================"

mkdir -p "${CA_DIR}" "${SERVER_DIR}" "${CUSTOMER_DIR}"

# ------------------------------------------------------------------
# Step 1: 生成 CA（若不存在或强制 renew）
# ------------------------------------------------------------------
if [ ! -f "${CA_DIR}/ca.key" ] || [ "$FORCE_RENEW" = true ]; then
    if [ -f "${CA_DIR}/ca.key" ]; then
        echo ">> 重新生成 CA（--renew）..."
    else
        echo ">> 生成 CA..."
    fi
    openssl genrsa -out "${CA_DIR}/ca.key" 4096
    openssl req -x509 -new -nodes \
        -key "${CA_DIR}/ca.key" \
        -sha256 -days "${CA_VALIDITY}" \
        -subj "/O=Release Operator/CN=release-operator-ca" \
        -out "${CA_DIR}/ca.crt"
    ca_enddate=$(openssl x509 -in "${CA_DIR}/ca.crt" -noout -enddate | sed 's/notAfter=//')
    echo "   CA 生成完成: ${CA_DIR}/ca.crt"
    echo "   到期时间: ${ca_enddate}"
else
    echo "   CA 已存在，跳过（--renew 可强制重新生成）。"
fi

# ------------------------------------------------------------------
# Step 2: 生成 Server 证书（notification 端）
# ------------------------------------------------------------------
echo ">> 生成 Server 证书..."
openssl genrsa -out "${SERVER_DIR}/tls.key" 2048

cat > "${SERVER_DIR}/csr.conf" <<EOF
[req]
default_bits = 2048
prompt = no
default_md = sha256
req_extensions = req_ext
distinguished_name = dn

[dn]
O = Release Operator
CN = release-notification

[req_ext]
subjectAltName = @alt_names

[alt_names]
DNS.1 = release-notification
DNS.2 = release-notification.default.svc.cluster.local
DNS.3 = localhost
IP.1 = 127.0.0.1
EOF

openssl req -new \
    -key "${SERVER_DIR}/tls.key" \
    -out "${SERVER_DIR}/tls.csr" \
    -config "${SERVER_DIR}/csr.conf"

openssl x509 -req \
    -in "${SERVER_DIR}/tls.csr" \
    -CA "${CA_DIR}/ca.crt" \
    -CAkey "${CA_DIR}/ca.key" \
    -CAcreateserial \
    -out "${SERVER_DIR}/tls.crt" \
    -days "${SERVER_VALIDITY}" \
    -sha256 \
    -extfile "${SERVER_DIR}/csr.conf" \
    -extensions req_ext

rm -f "${SERVER_DIR}/tls.csr" "${SERVER_DIR}/csr.conf"
server_enddate=$(openssl x509 -in "${SERVER_DIR}/tls.crt" -noout -enddate | sed 's/notAfter=//')
echo "   Server 证书生成完成: ${SERVER_DIR}/tls.crt"
echo "   到期时间: ${server_enddate}"

# ------------------------------------------------------------------
# Step 3: 生成 Client 证书（operator 端，每个客户一份）
# ------------------------------------------------------------------
echo ">> 生成 Client 证书（${CUSTOMER_ID}）..."
openssl genrsa -out "${CUSTOMER_DIR}/tls.key" 2048

openssl req -new \
    -key "${CUSTOMER_DIR}/tls.key" \
    -out "${CUSTOMER_DIR}/tls.csr" \
    -subj "/O=Customer/CN=${CUSTOMER_ID}"

openssl x509 -req \
    -in "${CUSTOMER_DIR}/tls.csr" \
    -CA "${CA_DIR}/ca.crt" \
    -CAkey "${CA_DIR}/ca.key" \
    -CAcreateserial \
    -out "${CUSTOMER_DIR}/tls.crt" \
    -days "${CLIENT_VALIDITY}" \
    -sha256

rm -f "${CUSTOMER_DIR}/tls.csr"

# 计算 SHA256 指纹
FINGERPRINT=$(openssl x509 -in "${CUSTOMER_DIR}/tls.crt" -noout -fingerprint -sha256 | sed 's/.*=//' | tr -d ':')
echo "${FINGERPRINT}" > "${CUSTOMER_DIR}/fingerprint.txt"

client_enddate=$(openssl x509 -in "${CUSTOMER_DIR}/tls.crt" -noout -enddate | sed 's/notAfter=//')
echo "   Client 证书生成完成: ${CUSTOMER_DIR}/tls.crt"
echo "   到期时间: ${client_enddate}"
echo "   指纹 (SHA256): ${FINGERPRINT}"

# ------------------------------------------------------------------
# 输出摘要
# ------------------------------------------------------------------
echo ""
echo "================================================"
echo "✅ 证书生成完成！"
echo ""
echo "生成的文件:"
echo "  CA cert:        ${CA_DIR}/ca.crt"
echo "  CA key:         ${CA_DIR}/ca.key（安全保管！）"
echo "  Server cert:    ${SERVER_DIR}/tls.crt"
echo "  Server key:     ${SERVER_DIR}/tls.key"
echo "  Client cert:    ${CUSTOMER_DIR}/tls.crt"
echo "  Client key:     ${CUSTOMER_DIR}/tls.key"
echo ""
echo "到期时间:"
echo "  CA:             ${ca_enddate:-unknown}"
echo "  Server:         ${server_enddate}"
echo "  Client:         ${client_enddate}"
echo ""
echo "后续步骤:"
echo "  1. 在客户集群创建 K8s Secrets:"
echo "     kubectl create secret tls release-operator-tls \\"
echo "       --cert=${CUSTOMER_DIR}/tls.crt --key=${CUSTOMER_DIR}/tls.key"
echo "     kubectl create secret generic release-operator-ca \\"
echo "       --from-file=ca.crt=${CA_DIR}/ca.crt"
echo ""
echo "  2. 将客户注册到白名单（需要指纹）:"
echo "     curl -X POST http://release-notification:8080/api/v1/customers \\"
echo "       -H 'X-API-Key: <your-api-key>' \\"
echo "       -H 'Content-Type: application/json' \\"
echo "       -d '{\"id\":\"${CUSTOMER_ID}\",\"name\":\"${CUSTOMER_ID}\","
echo "            \"operator_endpoint\":\"<customer-ip>:8443\","
echo "            \"cert_fingerprint\":\"${FINGERPRINT}\",\"enabled\":true}'"
echo ""
echo "  3. 证书到期前重新生成并更新客户集群:"
echo "     helm upgrade release-operator ./deployments/release-operator \\"
echo "       --reuse-values \\"
echo "       --set tls.cert=\"\$(base64 -w0 ${CUSTOMER_DIR}/tls.crt)\" \\"
echo "       --set tls.key=\"\$(base64 -w0 ${CUSTOMER_DIR}/tls.key)\""
echo "================================================"

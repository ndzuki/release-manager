# 示例: 构建并推送 Docker 镜像到 Harbor
# 用法: ./scripts/build-and-push.sh <service-name> <version-tag>
#
# 环境变量:
#   HARBOR_URL         - Harbor registry URL (e.g. harbor.example.com)
#   HARBOR_PROJECT     - Harbor project name (e.g. release-operator)
#   HARBOR_USERNAME    - Harbor 用户名
#   HARBOR_PASSWORD    - Harbor 密码

set -euo pipefail

SERVICE_NAME="${1:-}"
VERSION_TAG="${2:-latest}"

if [ -z "$SERVICE_NAME" ]; then
    echo "Usage: $0 <service-name> <version-tag>"
    echo "  service-name: release-operator | release-manager | release-webhook | release-api | release-orchestrator | release-auth | release-notifier"
    echo "  version-tag:  e.g. v1.0.0, latest"
    exit 1
fi

HARBOR_URL="${HARBOR_URL:-harbor.example.com}"
HARBOR_PROJECT="${HARBOR_PROJECT:-release-operator}"
HARBOR_USERNAME="${HARBOR_USERNAME:-}"
HARBOR_PASSWORD="${HARBOR_PASSWORD:-}"
IMAGE_NAME="${HARBOR_URL}/${HARBOR_PROJECT}/${SERVICE_NAME}:${VERSION_TAG}"

echo "================================================"
echo "Building and pushing Docker image"
echo "  Service: ${SERVICE_NAME}"
echo "  Image:   ${IMAGE_NAME}"
echo "================================================"

# Docker login to Harbor
if [ -n "$HARBOR_USERNAME" ] && [ -n "$HARBOR_PASSWORD" ]; then
    echo "$HARBOR_PASSWORD" | docker login "$HARBOR_URL" -u "$HARBOR_USERNAME" --password-stdin
fi

# Select Dockerfile based on service
case "$SERVICE_NAME" in
    release-operator)    DOCKERFILE="Dockerfile.operator" ;;
    release-manager)      DOCKERFILE="Dockerfile.manager" ;;
    release-webhook)      DOCKERFILE="Dockerfile.webhook" ;;
    release-api|release-orchestrator|release-auth|release-notifier) DOCKERFILE="Dockerfile.micro" ;;
    *)                   DOCKERFILE="Dockerfile.${SERVICE_NAME}" ;;
esac

docker build \
    -f "$DOCKERFILE" \
    --build-arg BINARY="${SERVICE_NAME}" \
    -t "$IMAGE_NAME" \
    -t "${HARBOR_URL}/${HARBOR_PROJECT}/${SERVICE_NAME}:latest" \
    .

# Push image
docker push "$IMAGE_NAME"
docker push "${HARBOR_URL}/${HARBOR_PROJECT}/${SERVICE_NAME}:latest"

echo ""
echo "✅ Image pushed successfully: $IMAGE_NAME"
echo "   Digest: $(docker inspect --format='{{index .RepoDigests 0}}' "$IMAGE_NAME" 2>/dev/null || echo "unknown")"

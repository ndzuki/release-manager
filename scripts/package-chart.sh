#!/usr/bin/env bash
# 示例: 打包 Helm chart 并推送到 Harbor OCI registry
# 用法: ./scripts/package-chart.sh <chart-dir> <version>
#
# 环境变量:
#   HARBOR_URL         - Harbor registry URL
#   HARBOR_PROJECT     - Harbor project (e.g. helm)
#   HARBOR_USERNAME    - Harbor 用户名
#   HARBOR_PASSWORD    - Harbor 密码

set -euo pipefail

CHART_DIR="${1:-}"
CHART_VERSION="${2:-}"

if [ -z "$CHART_DIR" ] || [ -z "$CHART_VERSION" ]; then
    echo "Usage: $0 <chart-dir> <version>"
    echo "  chart-dir:  Path to chart directory (e.g. ./magic-sandbox)"
    echo "  version:    Chart version (e.g. 0.0.15)"
    exit 1
fi

if [ ! -d "$CHART_DIR" ]; then
    echo "ERROR: Chart directory not found: $CHART_DIR"
    exit 1
fi

HARBOR_URL="${HARBOR_URL:-harbor.example.com}"
HARBOR_PROJECT="${HARBOR_PROJECT:-helm}"
HARBOR_USERNAME="${HARBOR_USERNAME:-}"
HARBOR_PASSWORD="${HARBOR_PASSWORD:-}"

CHART_NAME=$(grep '^name:' "$CHART_DIR/Chart.yaml" | awk '{print $2}')
OCI_URL="oci://${HARBOR_URL}/${HARBOR_PROJECT}"

echo "================================================"
echo "Packaging and pushing Helm chart"
echo "  Chart:   ${CHART_NAME}"
echo "  Version: ${CHART_VERSION}"
echo "  Target:  ${OCI_URL}/${CHART_NAME}"
echo "================================================"

# Login to Harbor OCI registry
if [ -n "$HARBOR_USERNAME" ] && [ -n "$HARBOR_PASSWORD" ]; then
    echo "$HARBOR_PASSWORD" | helm registry login "$HARBOR_URL" \
        -u "$HARBOR_USERNAME" --password-stdin
fi

# Update chart version
sed -i "s/^version:.*/version: ${CHART_VERSION}/" "$CHART_DIR/Chart.yaml"

# Build Helm dependencies
if [ -f "$CHART_DIR/Chart.lock" ]; then
    helm dependency build "$CHART_DIR"
fi

# Package chart
helm package "$CHART_DIR" --version "$CHART_VERSION" --destination /tmp/helm-packages

# Push to Harbor OCI
CHART_TGZ="/tmp/helm-packages/${CHART_NAME}-${CHART_VERSION}.tgz"
if [ ! -f "$CHART_TGZ" ]; then
    echo "ERROR: Chart package not found: $CHART_TGZ"
    exit 1
fi

helm push "$CHART_TGZ" "$OCI_URL"

echo ""
echo "✅ Chart pushed successfully: ${OCI_URL}/${CHART_NAME}:${CHART_VERSION}"

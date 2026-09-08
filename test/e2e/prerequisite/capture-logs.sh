#!/usr/bin/env bash
# capture-logs.sh — preserve cluster/operator logs for AC-066-17 diagnosis.
#
# Called by `make e2e-prerequisite-ci` before dev-purge so a failed smoke
# leaves enough evidence for the raw Helm error (the current smoke only stores
# the sanitized "Helm upgrade failed" in DB/outbox). It is deliberately
# read-only against the dev environment and safe to run after a tear-down.
set -u

OUT="${1:-e2e-results/operator-logs}"
if [ ! -f data/kubeconfig.yaml ]; then
  echo "capture-logs: no data/kubeconfig.yaml, skipping"
  exit 0
fi

mkdir -p "$OUT"
KUBECONFIG=data/kubeconfig.yaml kubectl config get-contexts -o name 2>/dev/null | while IFS= read -r ctx; do
  [ -n "$ctx" ] || continue
  dir="$OUT/$ctx"
  mkdir -p "$dir"
  {
    echo "===== pods ====="
    KUBECONFIG=data/kubeconfig.yaml kubectl --context "$ctx" get pods -A -o wide 2>&1 || true
    echo "===== events ====="
    KUBECONFIG=data/kubeconfig.yaml kubectl --context "$ctx" get events -A --sort-by=.lastTimestamp 2>&1 || true
  } > "$dir/context.txt"

  KUBECONFIG=data/kubeconfig.yaml kubectl --context "$ctx" get pods -A -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{"\n"}{end}' 2>/dev/null | while read -r ns pod; do
    logfile="$dir/${ns}-${pod}.log"
    KUBECONFIG=data/kubeconfig.yaml kubectl --context "$ctx" -n "$ns" logs "$pod" --all-containers > "$logfile" 2>&1 || true
  done
done
echo "capture-logs: saved to $OUT"

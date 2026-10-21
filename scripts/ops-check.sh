#!/usr/bin/env bash
#
# ops-check.sh — read-only health snapshot of the deployed release-manager (docs/runbook.md §11.1).
#
#   scripts/ops-check.sh                 human-readable PASS/FAIL report
#   scripts/ops-check.sh --verbose       also dump the raw command output for each check
#   scripts/ops-check.sh --no-http       skip the HTTP/cache-header checks (no network to the entry)
#   scripts/ops-check.sh --help          this text
#
# Exit codes: 0 = every check passed, 1 = at least one finding, 2 = usage/environment error.
#
# Semantics
#   * READ-ONLY: only kubectl get / curl / docker inspect. It never applies, deletes or patches
#     anything, and never prints a Secret value (existence only).
#   * Every check states its expectation in the failure text, so the output is actionable on its own.
#   * Env overrides: RM_KUBE_CONTEXT, RM_NAMESPACE, RM_BASE_URL, RM_EXPECTED_SCHEMA,
#     RM_MAX_RESTARTS, RM_TIMEOUT. Defaults describe the production environment (§8.18).
set -uo pipefail

cd "$(dirname "$0")/.."

RM_KUBE_CONTEXT="${RM_KUBE_CONTEXT:-k3d-rm-prod}"
RM_NAMESPACE="${RM_NAMESPACE:-release-manager}"
RM_BASE_URL="${RM_BASE_URL:-https://10.10.33.122:8443}"
RM_EXPECTED_SCHEMA="${RM_EXPECTED_SCHEMA:-36}"
RM_MAX_RESTARTS="${RM_MAX_RESTARTS:-0}"
RM_TIMEOUT="${RM_TIMEOUT:-20}"

VERBOSE=0
CHECK_HTTP=1
while [ $# -gt 0 ]; do
  case "$1" in
    --verbose) VERBOSE=1 ;;
    --no-http) CHECK_HTTP=0 ;;
    --help|-h) sed -n '2,23p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) printf 'ops-check: unknown argument %s (try --help)\n' "$1" >&2; exit 2 ;;
  esac
  shift
done

findings=0
checks=0
pass() { checks=$((checks + 1)); printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { checks=$((checks + 1)); findings=$((findings + 1)); printf '  \033[31mFAIL\033[0m %s\n' "$1"; printf '       → %s\n' "$2"; }
skip() { printf '  \033[33mSKIP\033[0m %s (%s)\n' "$1" "$2"; }
section() { printf '\n[%s]\n' "$1"; }
dump() { [ "$VERBOSE" -eq 1 ] && printf '%s\n' "$1" | sed 's/^/       | /'; }

kc() { timeout "$RM_TIMEOUT" kubectl --context "$RM_KUBE_CONTEXT" -n "$RM_NAMESPACE" "$@"; }

section "cluster access"

if ! command -v kubectl >/dev/null 2>&1; then
  printf 'ops-check: kubectl not found\n' >&2; exit 2
fi
if ! out="$(kc get namespace "$RM_NAMESPACE" -o name 2>&1)"; then
  fail "namespace $RM_NAMESPACE is reachable via context $RM_KUBE_CONTEXT" "kubectl failed: ${out//$'\n'/ }"
  printf '\nops-check: cannot continue without cluster access\n' >&2
  exit 2
fi
dump "$out"
pass "namespace $RM_NAMESPACE is reachable via context $RM_KUBE_CONTEXT"

section "deployments"

deploys="$(kc get deploy -o jsonpath='{range .items[*]}{.metadata.name}{"|"}{.spec.template.spec.containers[0].image}{"|"}{.status.readyReplicas}{"|"}{.spec.replicas}{"|"}{.metadata.annotations.deployment\.kubernetes\.io/revision}{"\n"}{end}' 2>&1)" || {
  fail "list deployments" "kubectl get deploy failed: ${deploys//$'\n'/ }"
  deploys=""
}
dump "$deploys"
for want in api auth orchestrator notifier webhook web; do
  line="$(printf '%s\n' "$deploys" | awk -F'|' -v n="$want" '$1==n')"
  if [ -z "$line" ]; then
    fail "deployment $want exists" "not found in namespace $RM_NAMESPACE"
    continue
  fi
  ready="$(printf '%s' "$line" | cut -d'|' -f3)"; want_ready="$(printf '%s' "$line" | cut -d'|' -f4)"
  image="$(printf '%s' "$line" | cut -d'|' -f2)"; rev="$(printf '%s' "$line" | cut -d'|' -f5)"
  if [ "${ready:-0}" = "$want_ready" ]; then
    pass "deployment $want ready ${ready}/${want_ready} (rev ${rev:-?}, image ${image##*/})"
  else
    fail "deployment $want ready ${ready:-0}/${want_ready}" "expected readyReplicas == spec.replicas; image ${image##*/} rev ${rev:-?}"
  fi
done

section "pods"

pods="$(kc get pods -o jsonpath='{range .items[*]}{.metadata.name}{"|"}{.status.phase}{"|"}{range .status.containerStatuses[*]}{.ready}{" "}{.restartCount}{" "}{end}{.status.containerStatuses[0].state.waiting.reason}{"\n"}{end}' 2>&1)" || {
  fail "list pods" "kubectl get pods failed: ${pods//$'\n'/ }"; pods=""
}
dump "$pods"
running_ready="$(printf '%s\n' "$pods" | awk -F'|' '$2=="Running" && $3 ~ /^true/ {n++} END{print n+0}')"
running_total="$(printf '%s\n' "$pods" | awk -F'|' '$2=="Running" {n++} END{print n+0}')"
completed="$(printf '%s\n' "$pods" | awk -F'|' '$2=="Succeeded" {n++} END{print n+0}')"
not_ready="$(printf '%s\n' "$pods" | awk -F'|' '$2=="Running" && $3 !~ /^true/ {print $1}')"
if [ "$running_total" -gt 0 ] && [ "$running_ready" -eq "$running_total" ]; then
  pass "running pods ready ${running_ready}/${running_total} (+${completed} completed backup job pods)"
else
  fail "running pods ready ${running_ready}/${running_total}" "not ready: ${not_ready//$'\n'/, }"
fi

bad_states="$(printf '%s\n' "$pods" | awk -F'|' '$4 ~ /CrashLoopBackOff|ImagePullBackOff|ErrImagePull|CreateContainerConfigError|OOMKilled/ {print $1"("$4")"}')"
if [ -z "$bad_states" ]; then
  pass "no pod in CrashLoopBackOff / ImagePullBackOff / config error"
else
  fail "pods in a terminal-ish waiting state" "$bad_states"
fi

restarts="$(printf '%s\n' "$pods" | awk -F'|' '{n=split($3,a," "); for(i=2;i<=n;i+=2) s+=a[i]} END{print s+0}')"
if [ "$restarts" -le "$RM_MAX_RESTARTS" ]; then
  pass "total container restarts ${restarts} <= RM_MAX_RESTARTS=${RM_MAX_RESTARTS}"
else
  fail "total container restarts ${restarts} > RM_MAX_RESTARTS=${RM_MAX_RESTARTS}" "check docs/runbook.md §1 and the previous logs (kubectl logs <pod> --previous)"
fi

section "backup"

cron="$(kc get cronjob release-manager-backup -o jsonpath='{.spec.schedule}|{.spec.suspend}' 2>&1)" || cron=""
dump "$cron"
if [ -n "$cron" ]; then
  sched="${cron%%|*}"; suspend="${cron##*|}"
  if [ "$suspend" = "true" ]; then
    fail "cronjob release-manager-backup is not suspended" "suspend=true; backups are paused"
  else
    pass "cronjob release-manager-backup scheduled '${sched}' (UTC), not suspended"
  fi
else
  fail "cronjob release-manager-backup exists" "not found in namespace $RM_NAMESPACE"
fi

lastbackup="$(kc get pods -o jsonpath='{range .items[*]}{.metadata.name}{"|"}{.status.phase}{"|"}{.status.startTime}{"\n"}{end}' 2>/dev/null | awk -F'|' '$1 ~ /^release-manager-backup-/ && $2=="Succeeded"' | sort -t'|' -k3 | tail -1)"
if [ -n "$lastbackup" ]; then
  pass "latest successful backup: $(printf '%s' "$lastbackup" | cut -d'|' -f1) at $(printf '%s' "$lastbackup" | cut -d'|' -f3)"
else
  fail "a successful backup Job exists" "no Succeeded release-manager-backup-* pod found; check §11.6"
fi

section "secrets (existence only, values never printed)"

for s in release-manager-jwt-private release-manager-mtls-ca release-manager-edge-tls; do
  if kc get secret "$s" -o name >/dev/null 2>&1; then
    pass "secret $s exists"
  else
    fail "secret $s exists" "missing; see docs/runbook.md §11.5 (off-site backup matters most)"
  fi
done

section "schema"

dsn_schema="$(kc exec postgres-0 -- psql -U release_manager -d release_manager -tAc 'select version||chr(124)||dirty from schema_migrations' 2>&1 | tr -d '[:space:]')"
dump "$dsn_schema"
if [ -z "$dsn_schema" ]; then
  fail "schema_migrations is readable" "psql via postgres-0 returned nothing"
else
  version="${dsn_schema%%|*}"; dirty="${dsn_schema##*|}"
  if [ "$dirty" = "false" ] && [ "$version" = "$RM_EXPECTED_SCHEMA" ]; then
    pass "schema version ${version}, dirty=${dirty}"
  elif [ "$dirty" != "false" ]; then
    fail "schema dirty=${dirty}" "a migration is half-applied; see docs/runbook.md §2"
  else
    fail "schema version ${version} != expected ${RM_EXPECTED_SCHEMA}" "either a migration was not applied or RM_EXPECTED_SCHEMA is stale (§2)"
  fi
fi

section "edge process"

if command -v docker >/dev/null 2>&1; then
  if out="$(docker inspect --type container rm-edge-forwarder 2>&1)"; then
    pass "edge forwarder container rm-edge-forwarder exists"
    dump "$(printf '%s' "$out" | head -c 400)"
  else
    fail "edge forwarder container rm-edge-forwarder exists" "docker inspect --type container failed; host entry may be down (§11.5 IP drift)"
  fi
else
  skip "edge forwarder container" "docker not available on this host"
fi

if [ "$CHECK_HTTP" -eq 1 ]; then
  section "entry + cache headers"
  root_headers="$(timeout "$RM_TIMEOUT" curl -skI --noproxy '*' "$RM_BASE_URL/" 2>&1)" || root_headers=""
  dump "$root_headers"
  root_code="$(printf '%s\n' "$root_headers" | awk 'NR==1{print $2}')"
  if [ "$root_code" = "200" ]; then
    pass "GET / returns 200"
  else
    fail "GET / returns 200" "got '${root_code:-no response}' from $RM_BASE_URL/"
  fi
  if printf '%s\n' "$root_headers" | grep -qi '^cache-control:.*no-cache'; then
    pass "shell is revalidated (Cache-Control: no-cache)"
  else
    fail "shell sends Cache-Control: no-cache" "without it a deploy can stay invisible in a browser (§11.7)"
  fi
  readyz_code="$(timeout "$RM_TIMEOUT" curl -sk -o /dev/null -w '%{http_code}' --noproxy '*' "$RM_BASE_URL/readyz" 2>&1)" || readyz_code=""
  if [ "$readyz_code" = "200" ]; then
    pass "GET /readyz returns 200"
  else
    fail "GET /readyz returns 200" "got '${readyz_code:-no response}'"
  fi
  asset="$(printf '%s\n' "$root_headers" >/dev/null; timeout "$RM_TIMEOUT" curl -sk --noproxy '*' "$RM_BASE_URL/" 2>/dev/null | grep -oE 'assets/index-[A-Za-z0-9_-]+\.js' | head -1)"
  if [ -n "$asset" ]; then
    asset_headers="$(timeout "$RM_TIMEOUT" curl -skI --noproxy '*' "$RM_BASE_URL/$asset" 2>&1)" || asset_headers=""
    dump "$asset_headers"
    if printf '%s\n' "$asset_headers" | grep -qi '^cache-control:.*immutable'; then
      pass "hashed asset $asset is cached immutably"
    else
      fail "hashed asset sends immutable" "$asset lacks 'Cache-Control: ... immutable' (§11.7)"
    fi
  else
    skip "hashed asset cache header" "could not extract an assets/index-*.js from /"
  fi
else
  section "entry + cache headers"
  skip "entry/cache-header checks" "--no-http"
fi

printf '\nops-check: %d check(s), %d finding(s)\n' "$checks" "$findings"
if [ "$findings" -gt 0 ]; then
  printf 'ops-check: FAIL — start from docs/runbook.md §0.2 (symptom index)\n'
  exit 1
fi
printf 'ops-check: PASS\n'

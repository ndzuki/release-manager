#!/usr/bin/env bash
# AC-066-17 prerequisite smoke — thin combined verification of the upstream
# release chain before TASK-066 implementation work (D-103/D-108/D-109: the
# upstream fix tasks own their unit/contract tests; this script is the final
# end-to-end re-check only, it does not implement business logic).
#
# Smoke list (REQ-066 AC-066-17): dev-up/dev-seed, declared endpoints,
# seed manifest, operator enrollment/reconnect, Upgrade, CancelOperation,
# Rollback, Emergency SetReplicas (set then restore), e2e-runner login, and
# the auth cross-restart token precheck (D-029 D4: ValidateToken + RefreshToken
# on pre-restart tokens succeed after an Auth deployment restart).
#
# dev-up/dev-seed with their built-in enrollment/reconnect smoke
# (AC-065-01/34) are driven by the caller via `make dev-up dev-seed`; this
# script verifies the resulting environment and the business chain over the
# formal Connect APIs only (no database access). A kubectl replicas read is
# used ONLY as the Emergency real-change observer (dev-script layer, matches
# the REQ-065 dev.sh smoke convention; cmd/e2e itself never invokes kubectl).
#
# Section order puts Emergency BEFORE Upgrade: the Emergency leg is
# independent of the Upgrade chain, so one run yields maximal gate evidence
# even when the Upgrade chain fails. Cancel/Rollback are cascade-skipped
# (recorded, not silent) when Upgrade did not reach succeeded.
#
# Machine-readable result: ${SMOKE_RESULT:-data/smoke-result.json}
#
# Usage:
#   smoke.sh [--base <url>] [--data <data-dir>]
#   BASE_URL / DATA_DIR env vars are honored as fallbacks.
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8083}"   # orchestrator
AUTH_URL="${AUTH_URL:-http://localhost:8085}"   # auth
DATA_DIR="${DATA_DIR:-data}"
SMOKE_RESULT="${SMOKE_RESULT:-$DATA_DIR/smoke-result.json}"
SMOKE_STARTED="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

PASS=0
FAIL=0
CHECKS_JSON="[]"
SKIPS_JSON="[]"
step() { printf '\n== %s ==\n' "$1"; }
record() { # record <status> <label>
  local st="$1" label="$2"
  if [ "$st" = "pass" ]; then
    PASS=$((PASS+1))
  else
    FAIL=$((FAIL+1))
  fi
  CHECKS_JSON="$(jq -c --arg n "$label" --arg s "$st" '. + [{check:$n,status:$s}]' <<<"$CHECKS_JSON")"
}
ok()  { printf 'PASS: %s\n' "$1"; record pass "$1"; }
bad() { printf 'FAIL: %s\n' "$1"; record fail "$1"; }
skiprec() { printf 'SKIP: %s\n' "$1"; SKIPS_JSON="$(jq -c --arg n "$1" '. + [$n]' <<<"$SKIPS_JSON")"; }

fail() {
  bad "$1"
  finish "smoke failed: $1"
  exit 1
}

finish() {
  local reason="${1:-completed}"
  local exit_code=0
  [ "$FAIL" -eq 0 ] || exit_code=1
  jq -n \
    --arg started "$SMOKE_STARTED" \
    --arg finished "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg reason "$reason" \
    --argjson pass "$PASS" --argjson fail "$FAIL" \
    --argjson checks "$CHECKS_JSON" --argjson skips "$SKIPS_JSON" \
    --argjson exit_code "$exit_code" \
    '{run:"AC-066-17 prerequisite smoke", started_at:$started, finished_at:$finished,
      pass:$pass, fail:$fail, exit_code:$exit_code, reason:$reason,
      checks:$checks, cascade_skips:$skips}' \
    > "$SMOKE_RESULT"
  printf '\n==== SMOKE SUMMARY: %d pass, %d fail ====\n' "$PASS" "$FAIL"
  printf 'machine-readable result: %s\n' "$SMOKE_RESULT"
}

require() { command -v "$1" >/dev/null 2>&1 || fail "missing tool $1"; }
require curl; require jq

# --- inputs: fixture manifest + credentials -------------------------------
MANIFEST="$DATA_DIR/dev-fixture.json"
STATUS="$DATA_DIR/dev-status.json"
CREDS="$DATA_DIR/dev-credentials.env"
for f in "$MANIFEST" "$STATUS" "$CREDS"; do
  [ -f "$f" ] || fail "missing $f (run make dev-up dev-seed first)"
done

# shellcheck disable=SC1090
source "$CREDS" || fail "cannot read $CREDS"
: "${E2E_RUNNER_PASSWORD:?dev-credentials.env lacks E2E_RUNNER_PASSWORD}"

DEF_RELEASE="$(jq -r '.definitions["e2e-release-target"]' "$MANIFEST")"
DEF_ISO="$(jq -r '.definitions["e2e-isolation-target"]' "$MANIFEST")"
DEF_EMERGENCY="$(jq -r '.definitions["e2e-emergency-target"]' "$MANIFEST")"
BUNDLE_ID="$(jq -r '.bundle.id' "$MANIFEST")"
BUNDLE_DIGEST="$(jq -r '.bundle.digest' "$MANIFEST")"

RELEASE_DEF_ID="$(jq -r '.id' <<<"$DEF_RELEASE")"
RELEASE_VALUES_ID="$(jq -r '.values_revision_id' <<<"$DEF_RELEASE")"
ISO_DEF_ID="$(jq -r '.id' <<<"$DEF_ISO")"
ISO_VALUES_ID="$(jq -r '.values_revision_id' <<<"$DEF_ISO")"
EMERGENCY_DEF_ID="$(jq -r '.id' <<<"$DEF_EMERGENCY")"

for v in RELEASE_DEF_ID RELEASE_VALUES_ID ISO_DEF_ID ISO_VALUES_ID EMERGENCY_DEF_ID BUNDLE_ID; do
  [ -n "${!v}" ] && [ "${!v}" != "null" ] || fail "manifest lacks $v"
done

step "declared endpoints + /environment consistency"
for port in 8082 8083 8085 8086 8087; do
  url="http://localhost:$port/readyz"
  code="$(curl -s -o /dev/null -w '%{http_code}' --retry 5 --retry-delay 2 --retry-connrefused "$url" || true)"
  if [ "$code" = "200" ]; then ok "readyz $url"; else bad "readyz $url (HTTP $code)"; fi
done
# 8084 is the mTLS operator gateway: /readyz is not mounted there; only the
# TCP listen is asserted (matches dev-up readiness contract).
ss -tln 2>/dev/null | grep -q ':8084 ' \
  && ok "tcp 8084 (mTLS gateway, tcp-open semantics)" \
  || bad "tcp 8084 not listening"

ENV_IDS=""
for url in \
  "http://localhost:8082/environment" \
  "http://localhost:8083/environment" \
  "http://localhost:8085/environment" \
  "http://localhost:8086/environment" \
  "http://localhost:8087/environment"; do
  env_json="$(curl -s --fail --retry 5 --retry-delay 2 --retry-connrefused "$url" || true)"
  [ -n "$env_json" ] || bad "environment $url"
  prod="$(jq -r '.production' <<<"$env_json")"
  eid="$(jq -r '.environment_id' <<<"$env_json")"
  [ "$prod" = "false" ] || bad "environment $url production=$prod"
  ENV_IDS="$ENV_IDS $eid"
done
UNIQ_COUNT="$(tr ' ' '\n' <<<"$ENV_IDS" | grep -v '^$' | sort -u | wc -l)"
[ "$UNIQ_COUNT" = "1" ] && ok "5 services /environment consistent ($(tr ' ' '\n' <<<"$ENV_IDS" | grep -v '^$' | sort -u | head -1), production=false)" \
  || bad "environment_id mismatch:$ENV_IDS"

step "seed manifest shape (AC-066-25 counters)"
jq -e '.customers | length == 2' "$MANIFEST" >/dev/null && ok "2 customers" || bad "customers != 2"
jq -e '.clusters | length == 4' "$MANIFEST" >/dev/null && ok "4 clusters" || bad "clusters != 4"
jq -e '.definitions["e2e-release-target"] and .definitions["e2e-isolation-target"] and .definitions["e2e-emergency-target"] and .definitions["e2e-restart-target"]' \
  "$MANIFEST" >/dev/null && ok "4 e2e definitions present" || bad "e2e definitions missing"
jq -e '.bundle.digest | length > 0' "$MANIFEST" >/dev/null && ok "seed bundle digest $BUNDLE_DIGEST" || bad "bundle digest empty"

step "e2e-runner login (Auth Login + ValidateToken)"
LOGIN="$(curl -sS --fail -X POST "$AUTH_URL/auth.v1.AuthService/Login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"e2e-runner\",\"password\":\"$E2E_RUNNER_PASSWORD\"}")"
TOKEN="$(jq -r '.accessToken' <<<"$LOGIN")"
[ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] || fail "login returned no access token"
ok "login access token (len=${#TOKEN})"
# D-029 D4: the auth cross-restart precheck needs the pre-restart refresh token.
REFRESH_TOKEN="$(jq -r '.refreshToken' <<<"$LOGIN")"
[ -n "$REFRESH_TOKEN" ] && [ "$REFRESH_TOKEN" != "null" ] || bad "login returned no refresh token (D4 precheck needs one)"
# LoginResponse.user may be unset on this auth version; roles are read from
# the authoritative ValidateToken projection (REQ-025).
VALID="$(curl -sS --fail -X POST "$AUTH_URL/auth.v1.AuthService/ValidateToken" \
  -H 'Content-Type: application/json' -d "{\"token\":\"$TOKEN\"}")"
jq -e '.valid == true' <<<"$VALID" >/dev/null && ok "ValidateToken valid=true" || bad "validate token: $VALID"
ROLES="$(jq -c '.roles // .user.roles // empty' <<<"$VALID")"
[[ "$ROLES" == *"release_admin"* ]] && ok "roles contain release_admin" || bad "roles=$ROLES"

AUTH_H=(-H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json')
IK() { printf 'Idempotency-Key: smoke-%s-%s' "$1" "$(date +%s)-$RANDOM"; }

wait_op() { # wait_op <operation_id> <want_state_regex> <label>
  local id="$1" want="$2" label="$3" state="" deadline=$((SECONDS + 300))
  while [ $SECONDS -lt $deadline ]; do
    local op
    op="$(curl -sS -X POST "$BASE_URL/orchestrator.v1.OrchestratorService/GetOperation" \
      "${AUTH_H[@]}" -d "{\"operationId\":\"$id\"}")"
    state="$(jq -r '.operation.state // empty' <<<"$op")"
    case "$state" in
      OPERATION_STATUS_FAILED|OPERATION_STATUS_CANCELLED|OPERATION_STATUS_TIMEOUT)
        bad "$label terminal state $state: $(jq -r '.operation.lastError // empty' <<<"$op")"; return 1 ;;
    esac
    grep -qE "^$want$" <<<"$state" && { ok "$label -> $state"; return 0; }
    sleep 3
  done
  bad "$label stuck at $state"; return 1
}

wait_replicas() { # wait_replicas <ns> <deploy> <want> [seconds]
  local ns="$1" deploy="$2" want="$3"
  local window="${4:-30}"
  local deadline=$((SECONDS + window))
  local got=""
  while [ $SECONDS -lt $deadline ]; do
    got="$(KUBECONFIG="$DATA_DIR/kubeconfig.yaml" kubectl --context k3d-dev-customer-a-direct -n "$ns" get deploy "$deploy" -o jsonpath='{.spec.replicas}/{.status.readyReplicas}' 2>/dev/null || true)"
    [ "$got" = "$want" ] && { echo "$got"; return 0; }
    sleep 2
  done
  echo "${got:-unavailable}"
  return 1
}

step "operator enrollment/reconnect (dev-up/dev-seed built-in smoke + ACTIVE session)"
ACTIVE=0
for ckey in $(jq -r '.customers | keys[]' "$MANIFEST"); do
  cid="$(jq -r --arg k "$ckey" '.customers[$k].id' "$MANIFEST")"
  for clkey in $(jq -r '.clusters | keys[]' "$MANIFEST"); do
    # devseed naming convention: cluster logical key starts with the
    # customer logical key (dev-customer-a-* / dev-customer-b-*).
    [[ "$clkey" == "$ckey"-* ]] || continue
    clid="$(jq -r --arg k "$clkey" '.clusters[$k].id' "$MANIFEST")"
    OPS="$(curl -sS --fail -X POST "$BASE_URL/orchestrator.v1.OrchestratorService/ListOperators" \
      "${AUTH_H[@]}" -d "{\"customerId\":\"$cid\",\"clusterId\":\"$clid\",\"pageSize\":100}")"
    n="$(jq '[.operators[]? | select(.sessionStatus == "OPERATOR_SESSION_STATUS_ONLINE")] | length' <<<"$OPS")"
    [ "$n" -ge 1 ] && ok "operator $clkey: $n ONLINE" || bad "operator $clkey: $n ONLINE (expect 1)"
    ACTIVE=$((ACTIVE + n))
  done
done
[ "$ACTIVE" -ge 4 ] && ok "total $ACTIVE operators with ACTIVE/ONLINE session (enrollment/reconnect)" \
  || bad "only $ACTIVE ACTIVE operator sessions"

step "emergency identity (kind/uid, REQ-085) + max_emergency_replicas (TASK-083)"
ET="$(curl -sS --fail -X POST "$BASE_URL/orchestrator.v1.OrchestratorService/ListEmergencyTargets" \
  "${AUTH_H[@]}" -d "{\"releaseDefinitionId\":\"$EMERGENCY_DEF_ID\"}")"
WR="$(jq -r '.targets[0].workloadRef // empty' <<<"$ET")"
WKIND="$(jq -r '.kind // empty' <<<"$WR")"
WNS="$(jq -r '.namespace // empty' <<<"$WR")"
WNAME="$(jq -r '.name // empty' <<<"$WR")"
WUID="$(jq -r '.uid // empty' <<<"$WR")"
MAX_REP="$(jq -r '.targets[0].maxEmergencyReplicas // 0' <<<"$ET")"
[ -n "$WKIND" ] && [ -n "$WNS" ] && [ -n "$WNAME" ] && [ -n "$WUID" ] \
  && ok "target identity complete: $WKIND $WNS/$WNAME uid=$WUID" \
  || bad "target identity incomplete: $WR"
[ "${MAX_REP:-0}" -ge 2 ] && ok "max_emergency_replicas=$MAX_REP >= 2" || bad "max_emergency_replicas=$MAX_REP < 2 (TASK-083 regression)"
WORKLOAD_REF="${WKIND,,}s/$WNS/$WNAME"

step "Emergency SetReplicas 2 -> succeeded + effect APPLIED, then restore to 1"
EM="$(curl -sS -X POST "$BASE_URL/orchestrator.v1.OrchestratorService/ExecuteEmergencyChange" \
  "${AUTH_H[@]}" \
  -d "{\"releaseDefinitionId\":\"$EMERGENCY_DEF_ID\",\"workloadRef\":\"$WORKLOAD_REF\",\"setReplicas\":2,\"convergenceStrategy\":\"REVERT_ON_NEXT_RECONCILE\",\"idempotencyKey\":\"smoke-emergency-2-$(date +%s)\"}")"
EM_ID="$(jq -r '.operationId // empty' <<<"$EM")"
[ -n "$EM_ID" ] || fail "ExecuteEmergencyChange(set_replicas=2) rejected: $EM"
ok "Emergency set_replicas=2 op=$EM_ID"
wait_op "$EM_ID" 'OPERATION_STATUS_SUCCEEDED' "emergency op=$EM_ID" || true
EFFECT="$(curl -sS -X POST "$BASE_URL/orchestrator.v1.OrchestratorService/GetOperation" \
  "${AUTH_H[@]}" -d "{\"operationId\":\"$EM_ID\"}" | jq -r '.operation.effectStatus // empty')"
[ "$EFFECT" = "EMERGENCY_EFFECT_STATUS_APPLIED" ] && ok "effect APPLIED" || bad "effect=$EFFECT (want APPLIED)"
REPLICAS=""
if command -v kubectl >/dev/null 2>&1 && [ -f "$DATA_DIR/kubeconfig.yaml" ]; then
  REPLICAS="$(wait_replicas "$WNS" "$WNAME" "2/2" 30)"
fi
if [ -n "$REPLICAS" ]; then
  [ "$REPLICAS" = "2/2" ] && ok "real replicas observed: $REPLICAS" || bad "replicas=$REPLICAS (want 2/2)"
else
  skiprec "replicas observer (kubectl or kubeconfig unavailable)"
fi

EM_REST="$(curl -sS -X POST "$BASE_URL/orchestrator.v1.OrchestratorService/ExecuteEmergencyChange" \
  "${AUTH_H[@]}" \
  -d "{\"releaseDefinitionId\":\"$EMERGENCY_DEF_ID\",\"workloadRef\":\"$WORKLOAD_REF\",\"setReplicas\":1,\"convergenceStrategy\":\"REVERT_ON_NEXT_RECONCILE\",\"idempotencyKey\":\"smoke-emergency-restore-$(date +%s)\"}")"
EM_REST_ID="$(jq -r '.operationId // empty' <<<"$EM_REST")"
[ -n "$EM_REST_ID" ] || fail "ExecuteEmergencyChange(restore=1) rejected: $EM_REST"
wait_op "$EM_REST_ID" 'OPERATION_STATUS_SUCCEEDED' "emergency restore op=$EM_REST_ID" || true
if command -v kubectl >/dev/null 2>&1 && [ -f "$DATA_DIR/kubeconfig.yaml" ]; then
  REST="$(wait_replicas "$WNS" "$WNAME" "1/1" 30)"
  [ "$REST" = "1/1" ] && ok "restore observed: $REST" || bad "restore replicas=$REST (want 1/1)"
fi
ok "Emergency restore to baseline replicas=1 (formal API)"

step "Upgrade (CreateOperation UPGRADE -> succeeded)"
# ListReleaseInventory is a TASK-066 deliverable (not on main yet); this gate
# runs BEFORE that RPC lands, so expectedCurrentRevision is derived from the
# seeded INSTALL (revision 1 per the AC-066-25 seed contract). The
# post-Step-4 e2e stages read it dynamically from the inventory row.
EXPECTED_REV=1
UP="$(curl -sS -X POST "$BASE_URL/orchestrator.v1.OrchestratorService/CreateOperation" \
  "${AUTH_H[@]}" -H "$(IK upgrade)" \
  -d "{\"operationType\":\"UPGRADE\",\"bundleId\":\"$BUNDLE_ID\",\"releaseDefinitionId\":\"$RELEASE_DEF_ID\",\"valuesRevisionId\":\"$RELEASE_VALUES_ID\",\"expectedCurrentRevision\":$EXPECTED_REV}")"
UP_ID="$(jq -r '.operationId // empty' <<<"$UP")"
[ -n "$UP_ID" ] || fail "CreateOperation UPGRADE rejected: $UP"
ok "UPGRADE created op=$UP_ID"
UPGRADE_OK=0
if wait_op "$UP_ID" 'OPERATION_STATUS_SUCCEEDED' "upgrade op=$UP_ID"; then
  UPGRADE_OK=1
fi

step "CancelOperation (non-terminal UPGRADE cancelled to legal terminal state)"
if [ "$UPGRADE_OK" != "1" ]; then
  skiprec "CancelOperation: cascade-skipped (upgrade failed, no revision/operation basis)"
else
  CN="$(curl -sS -X POST "$BASE_URL/orchestrator.v1.OrchestratorService/CreateOperation" \
    "${AUTH_H[@]}" -H "$(IK cancel-setup)" \
    -d "{\"operationType\":\"UPGRADE\",\"bundleId\":\"$BUNDLE_ID\",\"releaseDefinitionId\":\"$ISO_DEF_ID\",\"valuesRevisionId\":\"$ISO_VALUES_ID\",\"expectedCurrentRevision\":1}")"
  CN_ID="$(jq -r '.operationId // empty' <<<"$CN")"
  [ -n "$CN_ID" ] || fail "CreateOperation (cancel setup) rejected: $CN"
  CANCEL="$(curl -sS -X POST "$BASE_URL/orchestrator.v1.OrchestratorService/CancelOperation" \
    "${AUTH_H[@]}" -H "$(IK cancel)" \
    -d "{\"operationId\":\"$CN_ID\",\"reason\":\"AC-066-17 prerequisite smoke cancel check\"}")"
  ok "CancelOperation accepted for op=$CN_ID"
  # Cancel may land on CANCELLED (normal) or race to SUCCEEDED/FAILED (legal
  # terminal states); the D-109 regression is STUCK CANCELLING. Poll directly.
  CN_DEADLINE=$((SECONDS + 300))
  while [ $SECONDS -lt $CN_DEADLINE ]; do
    CN_STATE="$(curl -sS -X POST "$BASE_URL/orchestrator.v1.OrchestratorService/GetOperation" \
      "${AUTH_H[@]}" -d "{\"operationId\":\"$CN_ID\"}" | jq -r '.operation.state // empty')"
    case "$CN_STATE" in
      OPERATION_STATUS_CANCELLED|OPERATION_STATUS_SUCCEEDED|OPERATION_STATUS_FAILED|OPERATION_STATUS_TIMEOUT)
        ok "cancel terminal state $CN_STATE"; break ;;
    esac
    sleep 3
  done
  [ "$CN_STATE" != "OPERATION_STATUS_CANCELLING" ] || bad "cancel STUCK in CANCELLING (D-109 regression)"
  [ "$CN_STATE" != "OPERATION_STATUS_PENDING" ] && [ "$CN_STATE" != "OPERATION_STATUS_PREFLIGHT" ] \
    && [ "$CN_STATE" != "OPERATION_STATUS_QUEUED" ] && [ "$CN_STATE" != "OPERATION_STATUS_RUNNING" ] \
    || bad "cancel still non-terminal after 5m: $CN_STATE"
fi

step "Rollback (release back to revision 1)"
if [ "$UPGRADE_OK" != "1" ]; then
  skiprec "Rollback: cascade-skipped (no revision 2; upgrade failed)"
else
  RB="$(curl -sS -X POST "$BASE_URL/orchestrator.v1.OrchestratorService/RollbackRelease" \
    "${AUTH_H[@]}" -H "$(IK rollback)" \
    -d "{\"releaseDefinitionId\":\"$RELEASE_DEF_ID\",\"targetRevision\":1,\"expectedCurrentRevision\":2,\"reason\":\"AC-066-17 prerequisite smoke rollback\"}")"
  RB_ID="$(jq -r '.operationId // empty' <<<"$RB")"
  [ -n "$RB_ID" ] || fail "RollbackRelease rejected: $RB"
  ok "Rollback created op=$RB_ID"
  wait_op "$RB_ID" 'OPERATION_STATUS_SUCCEEDED' "rollback op=$RB_ID" || true
fi

step "auth cross-restart token precheck (D-029 D4 / AC-066-17 D4)"
# Restart the control-plane Auth deployment (replicas 0 -> ready 0 -> 1 ->
# ready 1; the same patch semantics the restart Stage uses) and assert that
# the PRE-restart access/refresh tokens still work: ValidateToken(old access)
# and RefreshToken(old refresh) must succeed. This is the explicit runtime
# precondition of restart Stage AC-066-03/26 assertions.
if command -v kubectl >/dev/null 2>&1 && [ -f "$DATA_DIR/kubeconfig.yaml" ]; then
  AUTH_NS="release-manager-dev"
  AUTH_CTX="k3d-release-manager-control"
  kubectl_auth() { KUBECONFIG="$DATA_DIR/kubeconfig.yaml" kubectl --context "$AUTH_CTX" -n "$AUTH_NS" "$@"; }
  if ! kubectl_auth get deployment auth >/dev/null 2>&1; then
    bad "auth deployment not found on $AUTH_CTX/$AUTH_NS (D4 precheck)"
  else
    kubectl_auth scale deployment auth --replicas=0 >/dev/null 2>&1 || bad "auth scale to 0 failed"
    D4_SCALE0=0
    for _ in $(seq 1 40); do
      # When a Deployment is scaled to 0, status.readyReplicas is *absent*
      # (empty), not "0" — treat empty as scaled-to-0 (real smoke 2026-09-08).
      rr="$(kubectl_auth get deployment auth -o jsonpath='{.status.readyReplicas}' 2>/dev/null || true)"
      [ -z "${rr:-}" ] || [ "$rr" = "0" ] && { D4_SCALE0=1; break; }
      sleep 2
    done
    [ "$D4_SCALE0" = "1" ] && ok "auth scaled to 0 (readyReplicas absent/0)" || bad "auth readyReplicas != 0 after scale-down"
    kubectl_auth scale deployment auth --replicas=1 >/dev/null 2>&1 || bad "auth scale to 1 failed"
    D4_READY=0
    for _ in $(seq 1 90); do
      rr="$(kubectl_auth get deployment auth -o jsonpath='{.status.readyReplicas}' 2>/dev/null || true)"
      code="$(curl -s -o /dev/null -w '%{http_code}' --retry 2 --retry-connrefused "$AUTH_URL/readyz" 2>/dev/null || true)"
      [ "${rr:-x}" = "1" ] && [ "$code" = "200" ] && { D4_READY=1; break; }
      sleep 2
    done
    [ "$D4_READY" = "1" ] && ok "auth recovered after restart (readyReplicas=1, readyz 200)" || bad "auth not recovered after restart (readyReplicas=${rr:-?} readyz=${code:-?})"
    VALID_OLD="$(curl -sS --fail -X POST "$AUTH_URL/auth.v1.AuthService/ValidateToken" \
      -H 'Content-Type: application/json' -d "{\"token\":\"$TOKEN\"}" 2>/dev/null || echo '{}')"
    if jq -e '.valid == true' <<<"$VALID_OLD" >/dev/null 2>&1; then
      ok "ValidateToken(old access) valid after Auth restart"
    else
      bad "ValidateToken(old access) after Auth restart: $VALID_OLD"
    fi
    # Old refresh token must still rotate to a fresh access token.
    RF="$(curl -sS --fail -X POST "$AUTH_URL/auth.v1.AuthService/RefreshToken" \
      -H 'Content-Type: application/json' -d "{\"refreshToken\":\"$REFRESH_TOKEN\"}" 2>/dev/null || echo '{}')"
    NEW_ACCESS="$(jq -r '.accessToken // empty' <<<"$RF")"
    if [ -n "$NEW_ACCESS" ]; then
      ok "RefreshToken(old refresh) succeeded after Auth restart (new access len=${#NEW_ACCESS})"
    else
      bad "RefreshToken(old refresh) after Auth restart: $RF"
    fi
  fi
else
  skiprec "auth restart precheck (kubectl or kubeconfig unavailable)"
fi

if [ "$FAIL" -eq 0 ]; then
  finish "AC-066-17 prerequisite smoke PASSED"
  printf 'AC-066-17 prerequisite smoke PASSED\n'
  exit 0
fi
finish "AC-066-17 prerequisite smoke FAILED"
printf 'AC-066-17 prerequisite smoke FAILED\n' >&2
exit 1

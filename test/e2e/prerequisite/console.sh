#!/usr/bin/env bash
# console.sh — console (browser) Playwright spec for the AC-066-17 prerequisite
# gate (TASK-250).
#
# Why the browser path belongs in this gate: smoke.sh drives the Connect surface
# with curl, so it cannot see a routing gap that makes a write entry disappear
# from the rendered console (TASK-240 added the browser spec; TASK-249 fixed the
# dev-proxy gap it exposed). The spec renders the real page against the real
# backend and its hard failure is the regression alarm.
#
# Contract:
#   * Runs INSIDE `e2e-prerequisite`/`e2e-prerequisite-ci`, AFTER smoke.sh: the
#     rollback spec needs the e2e-release-target at revision > 1 (smoke.sh's
#     UPGRADE + ROLLBACK leave that behind), and the emergency-smoke convergence
#     case needs a pending_promotion Convergence Task on a ReleaseDefinition
#     (smoke.sh's REQUIRE_PROMOTION step leaves exactly one). Against a fresh
#     `dev-seed` those specs fail/skip on those stated preconditions, not on the
#     console.
#   * Target is the CONTAINER console (:8087, the same nginx as production),
#     which CI and smoke already exercise. The Vite dev console (:5173) is a
#     developer-only path.
#   * E2E_BACKEND=true declares the stack up, so a missing credential, browser
#     or entry is a HARD FAILURE. This script fails closed before Playwright if
#     its own preconditions are missing; it never skips.
#
# Usage: console.sh            (every knob is an env var)
#   E2E_CONSOLE_BASE_URL  console base URL   (default http://127.0.0.1:8087)
#   E2E_CONSOLE_SPECS     web-relative specs (default: rollback + emergency-smoke;
#                         a space-separated list, run serially)
#   E2E_CONSOLE_SPEC      single-spec override for ad-hoc runs; when set it wins
#   E2E_CONSOLE_CHANNEL   Playwright channel; UNSET => chrome (system Chrome,
#                         the local convention); set to the EMPTY string to use
#                         Playwright's bundled Chromium (CI installs it)
#   E2E_ADMIN_B_USER      second-approver username (default e2e-runner, the
#                         seeded release_admin account); its password comes from
#                         E2E_RUNNER_PASSWORD in the credentials file
#   E2E_DEFINITION_ID     ReleaseDefinition the emergency scenario drives
#                         (default: e2e-release-target from the fixture manifest,
#                         the definition smoke.sh operates on last)
#   E2E_DATA_DIR          data dir           (default <repo>/data)
#   E2E_OUTPUT_DIR        artifact root      (default <repo>/e2e-results)
set -euo pipefail

REPO_ROOT="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
WEB_DIR="$REPO_ROOT/web"

resolve_dir() { # resolve_dir <value>
  case "$1" in
    /*) printf '%s' "$1" ;;
    *) printf '%s' "$REPO_ROOT/$1" ;;
  esac
}
DATA_DIR="$(resolve_dir "${E2E_DATA_DIR:-data}")"
OUTPUT_DIR="$(resolve_dir "${E2E_OUTPUT_DIR:-e2e-results}")"
CREDENTIALS_FILE="${E2E_CREDENTIALS_FILE:-$DATA_DIR/dev-credentials.env}"
FIXTURE_FILE="${E2E_FIXTURE_FILE:-$DATA_DIR/dev-fixture.json}"
BASE_URL="${E2E_CONSOLE_BASE_URL:-http://127.0.0.1:8087}"
# TASK-270: the lane runs BOTH browser specs. E2E_CONSOLE_SPEC stays the
# single-spec override for ad-hoc runs; E2E_CONSOLE_SPECS takes a list. The
# specs run with --workers=1 (see the invocation) because they mutate shared
# fixture state over one backend.
SPECS="${E2E_CONSOLE_SPECS:-e2e/rollback.spec.ts e2e/emergency-smoke.spec.ts}"
if [ -n "${E2E_CONSOLE_SPEC:-}" ]; then SPECS="$E2E_CONSOLE_SPEC"; fi
CHANNEL="${E2E_CONSOLE_CHANNEL-chrome}"
CUSTOMER_KEY="${E2E_CONSOLE_CUSTOMER_KEY:-dev-customer-a}"
CLUSTER_KEY="${E2E_CONSOLE_CLUSTER_KEY:-dev-customer-a-direct}"
# The cross-actor convergence case (AC-058-41) needs a SECOND approver distinct
# from the submitting dev-admin. The canonical fixture already seeds exactly
# that identity: e2e-runner holds release_admin, which carries
# release.values.approve (internal/authorization/store_authorizer.go). Reusing it
# keeps the credential in data/dev-credentials.env / the CI Secret — never in the
# repository — and needs no schema change.
ADMIN_B_USER="${E2E_ADMIN_B_USER:-e2e-runner}"

fail() { printf 'FAIL console-e2e: %s\n' "$*" >&2; exit 1; }

[ -x "$WEB_DIR/node_modules/.bin/playwright" ] \
  || fail "web/node_modules is missing or incomplete; run 'npm ci' in web/ first"
command -v jq >/dev/null 2>&1 || fail "jq is required"
command -v curl >/dev/null 2>&1 || fail "curl is required"
[ -r "$CREDENTIALS_FILE" ] \
  || fail "missing credentials file $CREDENTIALS_FILE (was dev-seed run?); it is sourced, never printed"
[ -r "$FIXTURE_FILE" ] || fail "missing fixture manifest $FIXTURE_FILE; run 'make dev-seed'"

if [ "$CHANNEL" = "chrome" ]; then
  found=""
  for candidate in google-chrome google-chrome-stable chromium chromium-browser; do
    if command -v "$candidate" >/dev/null 2>&1; then found="$candidate"; break; fi
  done
  [ -n "$found" ] || fail "no system Chrome/Chromium on PATH for E2E_CONSOLE_CHANNEL=chrome; install one, or set E2E_CONSOLE_CHANNEL to the empty string and run 'cd web && npx playwright install --with-deps chromium'"
fi

# Fail fast when the console is unreachable instead of burning a 60s timeout on
# a connection error that would read like a product failure.
curl -fsS -o /dev/null --max-time 10 "$BASE_URL/" \
  || fail "console not reachable at $BASE_URL (is the dev stack up? 'make dev-up dev-seed dev-status')"

# Credentials: sourced into this process's environment, never echoed.
set -a
# shellcheck disable=SC1090
. "$CREDENTIALS_FILE"
set +a
[ -n "${DEV_ADMIN_PASSWORD:-}" ] || fail "$CREDENTIALS_FILE did not set DEV_ADMIN_PASSWORD"
# The second approver's credential is the seeded e2e-runner password. Missing it
# is a fixture/credentials misconfiguration, not a reason to let the cross-actor
# case skip (console.sh fails closed before Playwright).
[ -n "${E2E_RUNNER_PASSWORD:-}" ] || fail "$CREDENTIALS_FILE did not set E2E_RUNNER_PASSWORD (the second-approver account '$ADMIN_B_USER')"

CUSTOMER_ID="$(jq -r --arg k "$CUSTOMER_KEY" '.customers[$k].id // empty' "$FIXTURE_FILE")"
CLUSTER_ID="$(jq -r --arg k "$CLUSTER_KEY" '.clusters[$k].id // empty' "$FIXTURE_FILE")"
[ -n "$CUSTOMER_ID" ] || fail "no customer '$CUSTOMER_KEY' in $FIXTURE_FILE"
[ -n "$CLUSTER_ID" ] || fail "no cluster '$CLUSTER_KEY' in $FIXTURE_FILE"

# The definition the single-actor emergency scenario drives. smoke.sh upgrades +
# rolls back exactly this target last, which is what leaves it with a FRESH
# operator observation (the read model drops observations older than 15 minutes,
# so a definition this run has not touched may render no container/artifact
# entry). Picking the first rendered row instead is order-dependent and was the
# measured failure mode when the spec first ran for real (TASK-270).
DEFINITION_ID="${E2E_DEFINITION_ID:-$(jq -r '.definitions["e2e-release-target"].id // empty' "$FIXTURE_FILE")}"
[ -n "$DEFINITION_ID" ] || fail "no e2e-release-target definition in $FIXTURE_FILE (set E2E_DEFINITION_ID to override)"

# Playwright's own output (trace on failure) lands under the ignored
# e2e-results/, so the CI artifact upload collects it with the smoke result.
PLAYWRIGHT_OUTPUT="$OUTPUT_DIR/console"
mkdir -p "$PLAYWRIGHT_OUTPUT"

# Bash array from the space-separated list; an empty entry would silently run
# Playwright's whole testDir, so an empty list is a configuration failure.
read -r -a SPEC_PATHS <<<"$SPECS"
[ "${#SPEC_PATHS[@]}" -gt 0 ] || fail "E2E_CONSOLE_SPECS/E2E_CONSOLE_SPEC expanded to an empty spec list"

printf 'console-e2e: %s on %s (channel=%s, output=%s)\n' \
  "$SPECS" "$BASE_URL" "${CHANNEL:-bundled-chromium}" "$PLAYWRIGHT_OUTPUT"

cd "$WEB_DIR"
# --workers=1: the two specs drive one shared dev stack and mutate shared
# fixture state (release inventory, emergency changes, convergence task); the
# default worker count parallelizes across FILES and would race them.
E2E_BACKEND=true \
E2E_BASE_URL="$BASE_URL" \
E2E_CHANNEL="$CHANNEL" \
E2E_ADMIN_A_USER="${E2E_ADMIN_A_USER:-dev-admin}" \
E2E_ADMIN_A_PASS="$DEV_ADMIN_PASSWORD" \
E2E_ADMIN_B_USER="$ADMIN_B_USER" \
E2E_ADMIN_B_PASS="$E2E_RUNNER_PASSWORD" \
E2E_CUSTOMER_ID="$CUSTOMER_ID" \
E2E_CLUSTER_ID="$CLUSTER_ID" \
E2E_DEFINITION_ID="$DEFINITION_ID" \
  npx playwright test --workers=1 --output="$PLAYWRIGHT_OUTPUT" "${SPEC_PATHS[@]}"

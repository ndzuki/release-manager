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
#     spec needs the e2e-release-target at revision > 1, which smoke.sh's
#     UPGRADE + ROLLBACK leave behind. Against a fresh `dev-seed` the spec fails
#     on that precondition (and says so) — not on the console.
#   * Target is the CONTAINER console (:8087, the same nginx as production),
#     which CI and smoke already exercise. The Vite dev console (:5173) is a
#     developer-only path.
#   * E2E_BACKEND=true declares the stack up, so a missing credential, browser
#     or entry is a HARD FAILURE. This script fails closed before Playwright if
#     its own preconditions are missing; it never skips.
#
# Usage: console.sh            (every knob is an env var)
#   E2E_CONSOLE_BASE_URL  console base URL   (default http://127.0.0.1:8087)
#   E2E_CONSOLE_SPEC      web-relative spec  (default e2e/rollback.spec.ts)
#   E2E_CONSOLE_CHANNEL   Playwright channel; UNSET => chrome (system Chrome,
#                         the local convention); set to the EMPTY string to use
#                         Playwright's bundled Chromium (CI installs it)
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
SPEC="${E2E_CONSOLE_SPEC:-e2e/rollback.spec.ts}"
CHANNEL="${E2E_CONSOLE_CHANNEL-chrome}"
CUSTOMER_KEY="${E2E_CONSOLE_CUSTOMER_KEY:-dev-customer-a}"
CLUSTER_KEY="${E2E_CONSOLE_CLUSTER_KEY:-dev-customer-a-direct}"

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

CUSTOMER_ID="$(jq -r --arg k "$CUSTOMER_KEY" '.customers[$k].id // empty' "$FIXTURE_FILE")"
CLUSTER_ID="$(jq -r --arg k "$CLUSTER_KEY" '.clusters[$k].id // empty' "$FIXTURE_FILE")"
[ -n "$CUSTOMER_ID" ] || fail "no customer '$CUSTOMER_KEY' in $FIXTURE_FILE"
[ -n "$CLUSTER_ID" ] || fail "no cluster '$CLUSTER_KEY' in $FIXTURE_FILE"

# Playwright's own output (trace on failure) lands under the ignored
# e2e-results/, so the CI artifact upload collects it with the smoke result.
PLAYWRIGHT_OUTPUT="$OUTPUT_DIR/console"
mkdir -p "$PLAYWRIGHT_OUTPUT"

printf 'console-e2e: %s on %s (channel=%s, output=%s)\n' \
  "$SPEC" "$BASE_URL" "${CHANNEL:-bundled-chromium}" "$PLAYWRIGHT_OUTPUT"

cd "$WEB_DIR"
E2E_BACKEND=true \
E2E_BASE_URL="$BASE_URL" \
E2E_CHANNEL="$CHANNEL" \
E2E_ADMIN_A_USER="${E2E_ADMIN_A_USER:-dev-admin}" \
E2E_ADMIN_A_PASS="$DEV_ADMIN_PASSWORD" \
E2E_CUSTOMER_ID="$CUSTOMER_ID" \
E2E_CLUSTER_ID="$CLUSTER_ID" \
  npx playwright test --output="$PLAYWRIGHT_OUTPUT" "$SPEC"

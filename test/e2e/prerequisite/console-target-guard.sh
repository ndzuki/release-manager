#!/usr/bin/env bash
# console-target-guard.sh — fail the console lane when its target case did not run.
#
# Why this exists (TASK-270, PR #343 review blocker ③): Playwright exits 0 when a
# case is SKIPPED. Measured on CI run 37970098364 (sha b7746a1): the console lane
# reported `4 passed (11.2s)` plus `1 skipped` and exit 0 — the skipped one was the
# kill-switch case, but nothing in the lane stopped the CROSS-ACTOR case from
# skipping the same way. A lane that relays only Playwright's exit code can
# therefore go green while the case it exists for never executed.
#
# This script makes the target case's FINAL RESULT STATUS the lane's precondition:
#
#   passed                             -> exit 0
#   skipped / failed / timedOut / ...  -> exit 1, printing the skip reason or error
#   not collected at all               -> exit 1, listing what WAS collected
#   report missing / unparsable        -> exit 1 (the report is the evidence)
#   title collected more than once     -> exit 1 (ambiguous: reported, not guessed)
#
# It deliberately does NOT compare a count of passes: that number drifts with
# every added or removed case and with the runner, while "this case's status is
# passed" is exactly the contract the lane claims. The report is Playwright's own
# JSON reporter output — the same data behind the list reporter's `N passed`.
#
# Every test recorded for the title (one per Playwright project) must pass.
#
# Usage: console-target-guard.sh <report.json> <spec-basename> <case-title>
# Exit:  0 the target case's final status is `passed`
#        1 it was skipped, failed, absent, or the report could not be read
#        2 usage/environment error (missing jq, missing arguments)
set -euo pipefail

REPORT=${1:-}
SPEC=${2:-}
TITLE=${3:-}
if [ -z "$REPORT" ] || [ -z "$SPEC" ] || [ -z "$TITLE" ]; then
  printf 'usage: %s <playwright-json-report> <spec-basename> <case-title>\n' "$(basename "$0")" >&2
  exit 2
fi
command -v jq >/dev/null 2>&1 || {
  printf 'FAIL console-e2e: jq is required by %s\n' "$(basename "$0")" >&2
  exit 2
}

fail() { printf 'FAIL console-e2e: %s\n' "$*" >&2; }

# The JSON reporter's spec.file is relative to the config's testDir
# (`emergency-smoke.spec.ts`) while the CLI argument names it as
# `e2e/emergency-smoke.spec.ts`; matching on the basename covers both spellings.
# `recurse(.suites[]?)` walks nested suites, which the JSON schema allows.
read -r -d '' JQ_QUERY <<'JQ' || true
def finalStatus: ((.results // []) | last | (.status // "no-result"));
[ recurse(.suites[]?)
  | .specs[]?
  | select((.title == $title) and (((.file // "") | endswith($spec))))
  | { file: (.file // ""),
      tests: [ .tests[]?
               | { status: (.status // "unknown"),
                   final: finalStatus,
                   skipReasons: [ .annotations[]?
                                  | select(.type == "skip")
                                  | (.description // "") ],
                   message: ((.results // []) | last | (.error.message // "")) } ] } ]
JQ

if [ ! -r "$REPORT" ]; then
  fail "no readable Playwright JSON report at $REPORT"
  printf '  target case: %s\n  target spec: %s\n' "$TITLE" "$SPEC" >&2
  printf '  The report is what proves the case ran; it is missing when the run died\n  before finishing (or when the json reporter was dropped from the lane).\n' >&2
  exit 1
fi

matches=$(jq -c --arg title "$TITLE" --arg spec "$SPEC" "$JQ_QUERY" "$REPORT") || {
  fail "could not parse the Playwright JSON report at $REPORT"
  printf '  target case: %s\n' "$TITLE" >&2
  exit 1
}
count=$(jq 'length' <<<"$matches")

if [ "$count" -eq 0 ]; then
  fail "target case was collected by ZERO tests in this run — the case must exist and pass"
  printf '  case:   %s\n  spec:   %s\n  report: %s\n' "$TITLE" "$SPEC" "$REPORT" >&2
  collected=$(jq -r --arg spec "$SPEC" \
    '[ recurse(.suites[]?) | .specs[]? | select(((.file // "") | endswith($spec))) | .title ] | .[]' "$REPORT")
  if [ -n "$collected" ]; then
    printf '  collected from %s in this run:\n' "$SPEC" >&2
    while IFS= read -r t; do [ -n "$t" ] && printf '    - %s\n' "$t" >&2; done <<<"$collected"
    printf '  A renamed or moved target case lands here: fix the name/spec in the lane,\n  do not delete the guard.\n' >&2
  else
    printf '  the report holds no case from %s at all — the spec was not part of the run\n' "$SPEC" >&2
  fi
  exit 1
fi

if [ "$count" -ne 1 ]; then
  fail "target case title matched $count collected cases in $SPEC — ambiguous, refusing to guess"
  printf '  case:   %s\n  report: %s\n' "$TITLE" "$REPORT" >&2
  exit 1
fi

printf 'console-e2e: target case final status %s — %s\n' \
  "$(jq -r '[.[0].tests[]? | (.status + "/" + .final)] | join(", ")' <<<"$matches")" "$TITLE"

not_passed=$(jq '[.[0].tests[]? | select(.final != "passed")] | length' <<<"$matches")
if [ "$not_passed" -ne 0 ]; then
  fail "target case did NOT pass — Playwright exits 0 for a skipped case, so this lane would otherwise be green with zero execution"
  printf '  case:   %s\n  spec:   %s\n' "$TITLE" "$SPEC" >&2
  while IFS= read -r line; do
    [ -n "$line" ] && printf '  test:   %s\n' "$line" >&2
  done < <(jq -r '.[0].tests[]? | select(.final != "passed") | (.status + " (final result: " + .final + ")")' <<<"$matches")
  reasons=$(jq -r '.[0].tests[]? | .skipReasons[]?' <<<"$matches")
  if [ -n "$reasons" ]; then
    printf '  the spec recorded this skip reason:\n' >&2
    while IFS= read -r r; do [ -n "$r" ] && printf '    %s\n' "$r" >&2; done <<<"$reasons"
    printf '  That reason is a PRECONDITION: restore it (or fix the fixture) instead of\n  accepting the skip — a skip here means the case never ran.\n' >&2
  fi
  msg=$(jq -r '[.[0].tests[]? | select(.final != "passed") | (.message // "")] | map(select(. != "")) | .[0] // ""' <<<"$matches")
  if [ -n "$msg" ]; then
    printf '  the case failed with:\n' >&2
    # Playwright colourises error messages; strip the escapes so the reason reads
    # the same in a CI log and in a terminal.
    while IFS= read -r m; do
      [ -n "$m" ] && printf '    %s\n' "$m" >&2
    done <<<"$(printf '%s' "$msg" | sed -E $'s/\033\\[[0-9;]*[A-Za-z]//g' | head -3)"
  fi
  printf '  report: %s\n' "$REPORT" >&2
  exit 1
fi

printf 'console-e2e: target case passed\n'
exit 0

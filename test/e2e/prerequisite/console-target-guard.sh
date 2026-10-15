#!/usr/bin/env bash
# console-target-guard.sh — fail the console lane when ANY case it ran did not
# reach the outcome the expected-case manifest declares.
#
# Why this exists (TASK-270, PR #343 review blocker ③): Playwright exits 0 when a
# case is SKIPPED. Measured on CI run 37970098364 (sha b7746a1): the console lane
# reported `4 passed (11.2s)` plus `1 skipped` and exit 0 — the skipped one was the
# kill-switch case, but nothing in the lane stopped the CROSS-ACTOR case from
# skipping the same way. A lane that relays only Playwright's exit code can
# therefore go green while the case it exists for never executed.
#
# TASK-307 widened that from ONE named case to a manifest of expected cases:
# test/e2e/prerequisite/console-expected-cases.json. Guarding a single case still
# reported green when any OTHER case of the lane was skipped (rollback.spec.ts's
# two cases and emergency-smoke.spec.ts's REQUIRE_PROMOTION case had no guard at
# all). The guard now answers one question — "did every case this lane ran end
# where the manifest says it must?"
#
# The manifest's titles are copied verbatim from the specs in console.sh's SPECS
# (`E2E_CONSOLE_SPECS`, default `e2e/rollback.spec.ts e2e/emergency-smoke.spec.ts`).
# console.sh refuses to start when a spec it was asked to run has no manifest
# entry, and the completeness gate below refuses to pass when the report collected
# a case no entry declares, so the manifest cannot silently fall behind the lane.
# The kill-switch case is declared as an expected skip WITH its reason: its gate
# exists only on a stack started with emergencyChangeEnabled=false, which this lane
# does not build. A declared skip is not a silent one — it is in the repository.
#
# Per declared case, the rules are:
#   expect: passed   -> every recorded final-result status must be `passed`
#   expect: skipped  -> `passed`, or `skipped` ONLY for a declared reason;
#                       failed / timedOut / interrupted / unknown -> fail
#   not collected at all              -> fail, listing what WAS collected from the spec
#   title collected more than once    -> fail (ambiguous: reported, not guessed)
#   collected with an empty `tests`   -> fail (nothing proves the case ran)
# Completeness gate: every case the report COLLECTED must be declared by the manifest.
#
# It deliberately does NOT compare a count of passes: that number drifts with
# every added or removed case and with the runner, while "this case's status is
# passed" is exactly the contract the lane claims. The report is Playwright's own
# JSON reporter output — the same data behind the list reporter's `N passed`.
#
# Usage: console-target-guard.sh <report.json> <expected-cases.json> <spec> [<spec>...]
#   <spec>... = the specs the lane asked Playwright to run (console.sh passes its
#   E2E_CONSOLE_SPECS/E2E_CONSOLE_SPEC expansion verbatim). A declared case whose
#   spec is NOT in that list was never asked to run and is not judged.
# Exit:  0 every declared case for a spec that ran reached its declared outcome
#        1 a case skipped/failed/was not collected, or an undeclared case was collected
#        2 usage/environment error (missing arguments, missing jq, unusable manifest)
set -euo pipefail

if [ $# -lt 3 ]; then
  printf 'usage: %s <playwright-json-report> <expected-cases.json> <spec> [<spec>...]\n' "$(basename "$0")" >&2
  exit 2
fi
REPORT=$1
MANIFEST=$2
shift 2
RUN_SPECS=("$@")

command -v jq >/dev/null 2>&1 || {
  printf 'FAIL console-e2e: jq is required by %s\n' "$(basename "$0")" >&2
  exit 2
}

fail() { printf 'FAIL console-e2e: %s\n' "$*" >&2; exit 1; }
env_fail() { printf 'FAIL console-e2e: %s\n' "$*" >&2; exit 2; }
note() { printf '  %s\n' "$*" >&2; }

# spec.file is relative to the config's testDir in some Playwright versions
# (`emergency-smoke.spec.ts`) and to a parent rootDir in others
# (`e2e/emergency-smoke.spec.ts`), while the caller may pass either spelling
# (console.sh passes the web-relative path). Compare the LAST path segment of both
# sides so every spelling matches, and a file that merely ENDS with the name
# (`not-emergency-smoke.spec.ts`) does not.
#
# `recurse(.suites[]?)` walks nested suites, which the JSON schema allows.
RUN_JSON=$(for s in "${RUN_SPECS[@]}"; do basename -- "$s"; done | jq -R . | jq -sc 'unique')
[ "$RUN_JSON" != "[]" ] \
  || env_fail "the spec list is empty — the guard judges the specs the lane ran, so it needs at least one"

[ -r "$MANIFEST" ] \
  || env_fail "no readable expected-case manifest at $MANIFEST — it is the list of cases this lane must reach its declared outcome"
# Structural gate on the manifest, BEFORE anything reads `.cases` from it: jq
# exits 0 with no output for `{}`, `null` or any shape without `cases`, so a
# manifest that lost its entries would otherwise judge nothing and pass.
if ! jq -e 'type == "object" and (.cases | type == "array") and (.cases | length > 0)' "$MANIFEST" >/dev/null 2>&1; then
  env_fail "not a usable expected-case manifest (want a JSON object with a non-empty 'cases' array): $MANIFEST"
fi

if [ ! -r "$REPORT" ]; then
  printf 'FAIL console-e2e: no readable Playwright JSON report at %s\n' "$REPORT" >&2
  note "the report is the evidence that the cases ran; it is missing when the run died"
  note "before finishing (or when the json reporter was dropped from the lane)."
  printf '  manifest: %s\n  specs:    %s\n' "$MANIFEST" "$RUN_JSON" >&2
  exit 1
fi

# Structural gate on the report. `jq` exits 0 with NO output for empty or blank
# input, so a 0-byte report used to leave the query result absent and the script
# fell through to its success branch. The report is the evidence that the cases
# ran, so a report that is not the JSON object these queries depend on fails.
if ! jq -e 'type == "object" and (.suites? | type == "array")' "$REPORT" >/dev/null 2>&1; then
  printf 'FAIL console-e2e: not a readable Playwright JSON report (expected a JSON object with a '"'"'suites'"'"' array)\n' >&2
  note "report: $REPORT"
  note "an empty, blank, truncated, or non-JSON file lands here: the report is the"
  note "evidence that the cases ran, and no evidence must never pass the lane."
  exit 1
fi

# One row per manifest case whose spec the lane actually ran. `finalStatus` reads
# the LAST result, which is the case's final state after retries.
JQ_CASES=$(cat <<'JQ'
def finalStatus: ((.results // []) | last | (.status // "no-result"));
[ recurse(.suites[]?) | .specs[]?
  | { file: ((.file // "") | split("/") | last),
      title: (.title // ""),
      tests: [ .tests[]?
               | { final: finalStatus,
                   skipReasons: [ .annotations[]? | select(.type == "skip") | (.description // "") ],
                   message: ((.results // []) | last | (.error.message // "")) } ] } ]
as $collected
| [ ($man[0].cases // [])[]
    | . as $c
    | select(.spec as $s | $run | index($s))
    | ($collected | map(select(.file == $c.spec and .title == $c.title))) as $m
    | { spec: $c.spec,
        title: $c.title,
        expect: ($c.expect // "passed"),
        reasonContains: [ ($c.skipReasonContains // [])[] ],
        matches: ($m | length),
        finals: [ $m[] | .tests[]? | .final ],
        skipReasons: ([ $m[] | .tests[]? | .skipReasons[]? ] | unique),
        message: ([ $m[] | .tests[]? | select(.final != "passed") | (.message // "") ]
                  | map(select(. != "")) | .[0] // "") } ]
| .[]
| [ .spec, .title, .expect, (.matches | tostring),
    (.finals | join(",")), (.skipReasons | join(" | ")),
    (.reasonContains | join(",")), .message ]
# @tsv first (it escapes tabs/newlines/backslashes INSIDE a field, so a multi-line
# error message cannot split the record), then swap its tab separators for US.
| @tsv | gsub("\t"; "\u001f")
JQ
)

case_rows=$(jq -r --argjson run "$RUN_JSON" --slurpfile man "$MANIFEST" "$JQ_CASES" "$REPORT") \
  || fail "could not query the Playwright JSON report at $REPORT"

violations=0
checked=0

judge_failure() { # judge_failure <spec> <title> <reason> <finals> <matches> <skipReasons>
  violations=$((violations + 1))
  printf 'FAIL console-e2e: %s :: %s\n' "$1" "$2" >&2
  note "$3"
  note "final status(es): ${4:-<none>} over $5 collected case(s)"
  [ -n "$6" ] && note "skip reason(s): $6"
  note "manifest: $MANIFEST"
}

# Fields are joined with ASCII unit separator (US, \x1f), NOT tab: `read` with
# `IFS=$'\t'` treats tab as an IFS *whitespace* character, so a run of tabs
# collapses into one delimiter and an empty field (e.g. an empty skipReasons when
# a skipped case recorded no reason) shifts every later column left. Measured:
# `printf 'x\ty\t\tE2E\t\n' | { IFS=$'\t' read -r a b c d; ...; }` yields
# c='E2E' instead of c='' — the guard then reported a no-reason skip as
# "skipped for a reason the manifest does not declare: E2E_EMERGENCY_DISABLED".
# US is not IFS whitespace, so empty fields survive.
while IFS=$'\x1f' read -r spec title expect matches finals reasons declared_reasons message; do
  [ -n "$spec" ] || continue
  checked=$((checked + 1))
  bad=""
  if [ "$matches" -eq 0 ]; then
    bad="the case was not collected by this run — relocated, renamed, or its spec did not run"
  elif [ "$matches" -ne 1 ]; then
    bad="the title matched $matches collected cases — ambiguous, refusing to guess"
  elif [ -z "$finals" ]; then
    bad="the case was collected with NO test result recorded — nothing proves it ran"
  fi

  if [ -z "$bad" ]; then
    IFS=',' read -r -a final_arr <<<"$finals"
    if [ "$expect" = "passed" ]; then
      for f in "${final_arr[@]}"; do
        [ "$f" = "passed" ] || bad="expected 'passed' but the final result status is '$f'"
      done
    else
      skipped=0
      for f in "${final_arr[@]}"; do
        case "$f" in
          passed) ;;
          skipped) skipped=$((skipped + 1)) ;;
          *) bad="declared as an expected skip, but the final result status is '$f'" ;;
        esac
      done
      if [ -z "$bad" ] && [ "$skipped" -gt 0 ]; then
        if [ -z "$reasons" ]; then
          bad="the case skipped WITHOUT recording a reason, and the manifest declares which reason is expected"
        else
          declared_hit=0
          IFS=',' read -r -a want_arr <<<"$declared_reasons"
          for want in "${want_arr[@]}"; do
            [ -n "$want" ] || continue
            case "$reasons" in *"$want"*) declared_hit=1 ;; esac
          done
          [ "$declared_hit" -eq 1 ] \
            || bad="the case skipped for a reason the manifest does not declare: $reasons"
        fi
      fi
    fi
  fi

  if [ -n "$bad" ]; then
    judge_failure "$spec" "$title" "$bad" "$finals" "$matches" "$reasons"
    if [ -n "$message" ]; then
      note "the case failed with:"
      # Playwright colourises error messages; strip the escapes so the reason reads
      # the same in a CI log and in a terminal.
      while IFS= read -r m; do
        [ -n "$m" ] && printf '    %s\n' "$m" >&2
      done <<<"$(printf '%s' "$message" | sed -E $'s/\033\\[[0-9;]*[A-Za-z]//g' | head -3)"
    fi
    if [ "$matches" -eq 0 ]; then
      collected=$(jq -r --arg spec "$spec" \
        '[ recurse(.suites[]?) | .specs[]? | select(((.file // "") | split("/") | last) == $spec) | .title ] | .[]' "$REPORT")
      if [ -n "$collected" ]; then
        note "collected from $spec in this run:"
        while IFS= read -r t; do [ -n "$t" ] && printf '    - %s\n' "$t" >&2; done <<<"$collected"
        note "a renamed or moved case lands here: fix the manifest, do not delete the guard."
      else
        note "the report holds no case from $spec at all — the spec was not part of the run"
      fi
    fi
  else
    printf 'console-e2e: %s :: %s -> %s\n' "$spec" "$title" "${finals:-<none>}"
  fi
done <<<"$case_rows"

if [ "$checked" -eq 0 ]; then
  fail "judged ZERO declared cases for the specs this lane ran ($RUN_JSON) — the manifest and the lane disagree, and no evidence must never pass"
fi

# Completeness gate: a case the report collected that the manifest does not
# declare means the lane grew a case nobody guards. Skipping that new case would
# otherwise look exactly like the silent skips this script exists to catch.
JQ_UNDECLARED_CASES=$(cat <<'JQ'
[ recurse(.suites[]?) | .specs[]?
  | { file: ((.file // "") | split("/") | last), title: (.title // "") } ]
as $collected
| [ $collected[] as $x
    | select(((($man[0].cases // []) | map(select(.spec == $x.file and .title == $x.title)) | length)) == 0)
    | [ $x.file, ($x.title | gsub("[\t\n\r]"; " ")) ] ]
| .[]
| @tsv
JQ
)

undeclared_cases=$(jq -r --slurpfile man "$MANIFEST" "$JQ_UNDECLARED_CASES" "$REPORT") \
  || fail "could not list the collected cases of the Playwright JSON report at $REPORT"

while IFS=$'\t' read -r uspec utitle; do
  [ -n "$uspec" ] || continue
  violations=$((violations + 1))
  printf 'FAIL console-e2e: %s :: %s\n' "$uspec" "$utitle" >&2
  note "this case was COLLECTED by the lane but is not declared in the manifest — an"
  note "undeclared skip would be indistinguishable from a pass, so it fails closed."
  note "declare it in $MANIFEST (passed, or skipped with its reason)."
done <<<"$undeclared_cases"

# Defence in depth for console.sh's pre-flight check: a spec the lane ran with no
# manifest entry at all would leave every case of that spec unjudged.
undeclared_specs=$(jq -nr --argjson run "$RUN_JSON" --slurpfile man "$MANIFEST" \
  '$run[] as $f | select(((($man[0].cases // []) | map(select(.spec == $f)) | length)) == 0) | $f') \
  || fail "could not compare the lane's specs with the manifest at $MANIFEST"

while IFS= read -r uspec; do
  [ -n "$uspec" ] || continue
  violations=$((violations + 1))
  printf 'FAIL console-e2e: spec %s has no entry in the expected-case manifest\n' "$uspec" >&2
  note "every spec the lane runs must declare its cases in $MANIFEST (TASK-307)."
done <<<"$undeclared_specs"

if [ "$violations" -ne 0 ]; then
  printf 'console-e2e: %s case(s) judged, %s violation(s) — Playwright exits 0 for a skipped case, so this lane would otherwise be green with zero execution\n' \
    "$checked" "$violations" >&2
  exit 1
fi

printf 'console-e2e: %s declared case(s) for the specs in this run all reached their declared outcome\n' "$checked"
exit 0

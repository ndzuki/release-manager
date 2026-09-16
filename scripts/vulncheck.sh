#!/usr/bin/env bash
#
# vulncheck.sh — gate the module against the Go vulnerability database (REQ-008 §8-8).
#
#   scripts/vulncheck.sh            gate: exit 1 on a *called* vulnerability that is
#                                   not covered by vulncheck.exceptions.yaml
#
# Scope and semantics
#   * Only vulnerabilities govulncheck proves this module's code reaches are gated
#     (the "Symbol Results" section). Advisories that merely exist in the module
#     graph are printed by govulncheck but are not failures here.
#   * An advisory with no upstream fix (for example GO-2026-5932: the deprecated
#     golang.org/x/crypto/openpgp package pulled in by the Helm Go SDK, which
#     ADR-004 forbids replacing with a subprocess) is accepted in
#     vulncheck.exceptions.yaml with an owner, a compensating control and a review
#     date. An expired exception is a failure: the team re-decides, it does not
#     inherit silence.
#
# Only bash and the Go toolchain are needed beyond govulncheck itself (installed
# by `make vulncheck`).
set -euo pipefail

cd "$(dirname "$0")/.."

exceptions="vulncheck.exceptions.yaml"

if ! command -v govulncheck >/dev/null 2>&1; then
  printf 'vulncheck: govulncheck is not installed; run `make vulncheck`\n' >&2
  exit 1
fi

report="$(mktemp)"
trap 'rm -f "$report"' EXIT

set +e
govulncheck ./... >"$report" 2>&1
scan_status=$?
set -e

# 0 = clean, 3 = findings (which may be accepted below); anything else is a
# scanner failure and must not be mistaken for a pass.
if [ "$scan_status" -ne 0 ] && [ "$scan_status" -ne 3 ]; then
  printf 'vulncheck: govulncheck failed to run (exit %s)\n' "$scan_status" >&2
  cat "$report" >&2
  exit 1
fi

# Called vulnerabilities only: everything under "=== Symbol Results ===".
called="$(awk '
  /^=== Symbol Results ===$/ { section = "symbol"; next }
  /^=== /                     { section = ""; next }
  section == "symbol" && /^Vulnerability #/ { print $3 }
' "$report" | sort -u)"

allowed="$(grep -oE '^[[:space:]]*-[[:space:]]*id:[[:space:]]*"[^"]+"' "$exceptions" 2>/dev/null |
  sed -E 's/.*"([^"]+)".*/\1/' | sort -u || true)"

if [ -z "$called" ]; then
  printf 'vulncheck: PASS (no vulnerability is called by this module)\n'
  exit 0
fi

unexpected="$(comm -23 <(printf '%s\n' "$called") <(printf '%s\n' "$allowed"))"
suppressed="$(comm -12 <(printf '%s\n' "$called") <(printf '%s\n' "$allowed"))"
stale="$(comm -13 <(printf '%s\n' "$called") <(printf '%s\n' "$allowed"))"

# An expired exception is a failure: the review date is the whole point.
today="$(date +%Y-%m-%d)"
expired=""
while IFS= read -r id; do
  [ -n "$id" ] || continue
  entry_expiry="$(awk -v id="\"$id\"" '
    $0 ~ "- *id: *" id { found = 1; next }
    found && /expires_at:/ { gsub(/.*expires_at: *"?/, ""); gsub(/".*/, ""); print; exit }
  ' "$exceptions")"
  if [ -n "$entry_expiry" ] && [ "$today" \> "$entry_expiry" ]; then
    expired="${expired}${id} (expired ${entry_expiry})\n"
  fi
done <<<"$suppressed"

if [ -n "$expired" ]; then
  printf 'vulncheck: FAIL — accepted advisories whose review date has passed:\n%b' "$expired" >&2
  printf 'Re-review %s, then refresh or remove the entry.\n' "$exceptions" >&2
  exit 1
fi

if [ -n "$unexpected" ]; then
  printf 'vulncheck: FAIL — this module calls vulnerabilities that are not accepted:\n' >&2
  printf '%s\n' "$unexpected" | sed 's/^/  - /' >&2
  printf '\nFull scanner report:\n' >&2
  cat "$report" >&2
  printf '\nFix the dependency, or add a reviewed entry to %s.\n' "$exceptions" >&2
  exit 1
fi

printf 'vulncheck: PASS (accepted advisories: %s)\n' "$(printf '%s' "$suppressed" | tr '\n' ' ')"
if [ -n "$stale" ]; then
  printf 'vulncheck: note — exception entries no longer reported: %s\n' "$(printf '%s' "$stale" | tr '\n' ' ')" >&2
fi

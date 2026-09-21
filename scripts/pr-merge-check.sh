#!/usr/bin/env bash
# pr-merge-check — refuse to merge a PR whose checks are not all green.
#
# Why this exists: "run gh pr checks before merging" was written down twice in
# this project and violated twice — the 2026-09-18 session merged a PR without
# checking, and the 2026-09-27 session merged #198 with a failing `test` job
# because the merge output was misread (the "Aborting" lines came from a
# following `git checkout`, not from the merge). A rule that lives only in prose
# does not hold; this makes it mechanical.
#
# Usage: make pr-merge-check PR=198
set -euo pipefail

PR="${1:-}"
if [ -z "$PR" ]; then
  echo "usage: pr-merge-check <pr-number>" >&2
  exit 2
fi

# gh pr checks exits non-zero when any check fails, so the output is what matters.
out="$(gh pr checks "$PR" 2>&1 || true)"
if [ -z "$out" ]; then
  echo "pr-merge-check: no checks reported for PR $PR (is it open, and is CI configured?)" >&2
  exit 2
fi

blocking="$(printf '%s\n' "$out" | awk -F'\t' '$2 != "pass" && $2 != "skipping" {printf "  %s -> %s\n", $1, $2}')"
if [ -n "$blocking" ]; then
  printf 'pr-merge-check: REFUSING to merge PR %s — these checks are not green:\n%s\n' "$PR" "$blocking" >&2
  printf 'pr-merge-check: re-run the failing jobs, or explain the failure in the PR before merging.\n' >&2
  exit 1
fi

printf 'pr-merge-check: PR %s — all checks green\n' "$PR"

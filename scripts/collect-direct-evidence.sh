#!/usr/bin/env bash
# Collect machine-checkable evidence for `direct:` (non-PR) deliveries.
#
# Not every delivery is a pull request. The dotfiles and DSH plugins this machine
# manages with chezmoi live in a different repository whose watcher commits and
# pushes straight to main (TASK-314/319/322), so those cards cite
#
#     merge_status: direct
#     pr_url: direct:<40-hex-sha>@<repo-path>
#     evidence_paths: <repo-relative path>, ...
#
# instead of a PR. `internal/quality/taskcheck` must not import os/exec (the
# SDK-only gate scans cmd/ and internal/), so the package only consumes evidence
# files; this script is what turns the cited (repo, sha) pairs into the records
# its ParseDirectEvidence reads -- one per line, tab-separated:
#
#     commit\t<repo>\t<full-sha>          sha resolves to a commit reachable from a ref
#     file\t<repo>\t<full-sha>\t<path>    a path that commit changed
#
# Input is the `<repo>\t<sha>` list printed by `taskcheck -print-direct-refs`.
#
# Records are keyed by the repo string exactly as the card wrote it, so the gate
# looks up what the card said, not a local absolute path. A repo that is not a
# git work tree on this machine, or a sha that is not reachable here, contributes
# NO record -- the gate then reports the card UNVERIFIED instead of verified.
# Failing closed is the point: silently inventing evidence would be worse than
# reporting that this checkout cannot prove the claim.
#
# Usage: scripts/collect-direct-evidence.sh <refs-file>

set -uo pipefail

refs="${1:-}"
if [ -z "$refs" ]; then
	printf "usage: %s <refs-file>\n" "$0" >&2
	exit 2
fi
if [ ! -f "$refs" ]; then
	printf "collect-direct-evidence: no such refs file: %s\n" "$refs" >&2
	exit 2
fi

while IFS=$'\t' read -r repo sha; do
	[ -n "${repo:-}" ] && [ -n "${sha:-}" ] || continue

	# The refs file is written by `taskcheck -print-direct-refs`, which only emits
	# 40-hex shas; re-check it here so a hand-edited file can never smuggle
	# something git would read as an option.
	case "$sha" in
	*[!0-9a-fA-F]*) continue ;;
	esac

	# Resolve the repo the way a card may write it: an absolute path or a ~/ path.
	case "$repo" in
	"~/"*) dir="$HOME/${repo#\~/}" ;;
	/*) dir="$repo" ;;
	*) continue ;;
	esac
	[ -d "$dir" ] || continue

	# The sha must resolve to a commit ...
	full="$(git -C "$dir" rev-parse --verify --quiet "${sha}^{commit}" 2>/dev/null)" || continue
	# ... and be reachable from a ref: an object left behind by a rebase or a
	# garbage-collected side branch is in the object database but is not part of
	# any published history, so it cannot witness a delivery.
	if [ -z "$(git -C "$dir" for-each-ref --contains "$full" --count=1 --format='%(refname)' 2>/dev/null)" ]; then
		continue
	fi
	printf 'commit\t%s\t%s\n' "$repo" "$full"

	# --no-renames + -z: raw, unambiguous, NUL-separated paths (a path with a tab
	# or a newline would otherwise split into a malformed record).
	git -C "$dir" show --no-renames --name-only --format= -z "$full" 2>/dev/null |
		while IFS= read -r -d '' path; do
			[ -n "$path" ] && printf 'file\t%s\t%s\t%s\n' "$repo" "$full" "$path"
		done
done <"$refs"

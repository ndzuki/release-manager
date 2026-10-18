#!/usr/bin/env bash
# check-docs.sh — verify that documented commands and paths still exist.
#
# Why this gate exists: an audit of this project found documentation that named a
# `make` target nobody could run, pointed at a config file that lives elsewhere,
# and referenced files that were never in the tree. Those are not wording
# problems — a reader following the instructions fails. Each of them is
# mechanically detectable, so they are checked here rather than in review.
#
# Checks, over markdown that is tracked or newly added (ignored files are never
# scanned, so node_modules and build output stay out of the way):
#   1. every `make <target>` a document mentions exists as a Makefile rule
#   2. every repository path written in a code span exists (a glob must match
#      at least one entry)
#   3. every relative markdown link target exists
#   4. every `path:123` citation points at lines the file actually has
#   5. every repository path and cited line inside vulncheck.exceptions.yaml
#      exists (that file is not markdown; see the section below)
#   6. `Foo.vue:123` (filename-only, no separator) and `:123` (bare line number)
#      citations are attributed to a file and checked the same way — see "Two
#      more citation shapes" below
#
# This repository documents by evidence (`文件:行号`), which only works if those
# line numbers survive edits; a citation that has drifted off the end of its file
# is the same class of defect as a `make` target that does not exist.
#
# Check 6 covers two citation shapes check 4 cannot see:
#   * `Foo.vue:123` has no separator, so check 4 never sees it. When exactly one
#     tracked file has that basename the citation is resolved to that file; an
#     ambiguous basename (`main.go`, `README.md`) is left alone, because guessing
#     would invent findings.
#   * `:123` is a bare line number. It is attributed to the last repository file
#     path earlier on the same line (a table row is a line), and checked against
#     that file. Attribution is same-line on purpose: a path two lines up is not
#     assumed. The citations that cannot be attributed are COUNTED AND REPORTED
#     as unverified — not failed, and not silently dropped. Failing on them is
#     not viable: the bulk of them are legitimate prose ("… `web/nginx.conf` 的
#     `:91`"), and a hard failure would only produce blanket exemptions, which
#     is worse than an explicit "not verified" count.
#     Known edge: a bare PORT written as a code span (`:8087`) on a line that
#     already named a file is indistinguishable from a line number. The
#     repository writes ports without backticks next to their context
#     (`端口 8087`) for this reason; if a port must be quoted, do not use a
#     backticked bare `:NNNN`, or cite the port's source file explicitly.
#
# Frozen snapshots. An immutable point-in-time record (an audit snapshot) cannot
# have its line numbers corrected without falsifying the record. Such a file
# declares itself, on its own line within the first 10 lines (the declaration is
# part of the document's identity, so it belongs at the top):
#   <!-- check-docs:snapshot-frozen date=YYYY-MM-DD reason="..." -->
# The marker exempts LINE-NUMBER checks in that file only: path existence, links,
# make targets and the yaml check still apply. Both a date and a reason are
# required — a marker missing either is an ERROR, never a silent pass. Every
# citation the marker actually saves from failing is counted and named in the
# output, so the exemption never hides what it is hiding. It is for frozen
# records only; a living document's stale line numbers get fixed, not exempted.
#
# Precision over recall. A code span is treated as a path only when it contains a
# separator and either starts with a real source root (cmd/, internal/, api/,
# web/, docs/, deploy/, migrations/, scripts/, test/, configs/, .github/) or ends
# with a known file extension. Bare names (`dev.sh`, `baseline.json`), Go and npm
# import paths (`os/exec`, `@connectrpc/connect`) and
# knowledge-base references (`Notes/CONTEXT.md`, `Notes/decisions/ADR-*.md`) are
# therefore not asserted about, and neither are generated output roots (bin/,
# dist/, data/, coverage/), which exist only after a build.
#
# When a mention is intentionally not a repo path — a file that was deleted and
# is being described as deleted, or an external document — put
# `<!-- check-docs:ignore <what it is> -->` **on that same line**, reason
# included. The exemption is line-local on purpose: exempting a whole file or
# pattern would let real drift back in unnoticed. If the same string also
# appears on a line that is not exempt, it is still reported.
#
# This spelling is markdown-only. Non-markdown files use `cite_ignore:` instead
# (see check 5), but the invariant is the same in both: an exemption without a
# reason is an ERROR, never a silent pass. Do not "unify" the two spellings.
#
# Usage:
#   scripts/check-docs.sh                  check every markdown file
#   scripts/check-docs.sh docs README.md   check the given files or directories
set -euo pipefail

# Findings must come out in the same order on every machine.
export LC_ALL=C

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

# These are matched with [[ =~ ]]: a quoted variable inside a `case` pattern is
# compared literally and would silently disable the check it looks like it drives.
REPO_ROOTS='^\.?/?(cmd|internal|api|web|docs|deploy|migrations|scripts|test|configs|\.github)(/|$)'
# Paths and names a document may quote from the knowledge base; they live outside
# this repository, so their absence here proves nothing.
EXTERNAL='^(Design|Notes|Requirements|Tasks|References|Projects|myNote)(/|$)'
EXTERNAL_NAMES='^(ADR-INDEX|ADR-COVERAGE|CONTEXT|PROJECT-CONVENTIONS|glossary|REVISION)\.md$'
GENERATED='^(bin|dist|node_modules|coverage|data|tmp|e2e-results|out|build|\.vite)(/|$)'
EXT_RE='\.(go|md|mdx|yml|yaml|json|ts|tsx|vue|js|jsx|cjs|mjs|sql|sh|proto|mod|sum|html|http|txt|example|env|tsv|lock|png|svg|ico|tf|rs|py|rb|c|h)$'
# Words that read like `make <token>` but are English prose.
MAKE_PROSE='^(it|sure|sense|clear|work|believe|attempt|money|noise|room|way|both|either|neither|one|someone|anyone|everyone|nobody|something|nothing|everything|state|history|available|certain|possible|obvious|evident|no|a|an|the|this|that|up|off|us|them|you|your|their|its|his|her|our|my|target|targets|rule|rules|recipe|recipes|command|commands|variable|variables)$'

# ---------------------------------------------------------------------------
# Markdown to scan: tracked plus new-but-not-ignored.
# ---------------------------------------------------------------------------
md=()
while IFS= read -r f; do [ -n "$f" ] && md+=("$f"); done < <(
  { git ls-files -z '*.md' | tr '\0' '\n'
    git ls-files -o --exclude-standard -z '*.md' 2>/dev/null | tr '\0' '\n'
  } | sort -u
)
if [ "${#md[@]}" -gt 0 ] && [ $# -gt 0 ]; then
  kept=()
  for want in "$@"; do
    for f in "${md[@]}"; do
      case "$f" in "$want"|"$want"/*) kept+=("$f") ;; esac
    done
  done
  md=("${kept[@]:-}")
fi
if [ "${#md[@]}" -eq 0 ] || [ -z "${md[0]:-}" ]; then
  echo "check-docs: no markdown files found — nothing was checked" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# The Makefile's real rules. `name:` with an empty prerequisite list counts
# (e2e-env-config is exactly that shape); `VAR := value` must not.
# ---------------------------------------------------------------------------
targets=()
if [ -f Makefile ]; then
  while IFS= read -r t; do targets+=("$t"); done < <(
    grep -E '^[A-Za-z0-9._%-]+:' Makefile 2>/dev/null \
      | grep -v ':=' | grep -v '^\.' | sed 's/:.*//' | sort -u || true
  )
fi
if [ "${#targets[@]}" -eq 0 ]; then
  echo "check-docs: NO Makefile rules found — the make-target check is inactive" >&2
fi
has_target() {
  local q=$1 t
  for t in "${targets[@]:-}"; do [ "$t" = "$q" ] && return 0; done
  return 1
}

# ---------------------------------------------------------------------------
# Existence helpers.
# ---------------------------------------------------------------------------
glob_match() {
  local p=$1 n=0 m
  shopt -s nullglob
  for m in $p; do n=$((n + 1)); done
  shopt -u nullglob
  [ "$n" -gt 0 ]
}

# A candidate may name a directory, a file, or a glob that has to match
# something. Names pointing at a document family (`docs/decisions/ADR-013`) pass
# on prefix match, because citing an ADR by number is normal in this repository.
exists_as() {
  local p=${1%/} base=$2
  [ -n "$p" ] || return 1
  case "$p" in *'*'*|*'?'*|*'['*) glob_match "$p" && return 0; glob_match "$base/$p" && return 0; return 1 ;; esac
  [ -e "$p" ] && return 0
  [ -e "$base/$p" ] && return 0
  glob_match "${p}*" && return 0
  return 1
}

# Per-file state for the exemption helpers.
CURFILE=""
ignore_lines=" "

is_exempt_line() { case "$ignore_lines" in *" $1 "*) return 0 ;; *) return 1 ;; esac; }

# ---------------------------------------------------------------------------
# Owning-file resolution for the citation shapes check 4 cannot see.
#
# A filename-only citation (`OperationDetailPage.vue:319`) is resolved only when
# exactly one tracked (or new, non-ignored) file carries that basename; `main.go`
# and `README.md` are ambiguous and are deliberately left alone. A bare line
# number (`:295`) is attributed by check 6 to the last resolvable path earlier on
# the same line, so this helper is also how that context is established.
# ---------------------------------------------------------------------------
declare -A basename_path=()
declare -A basename_count=()
while IFS= read -r p; do
  [ -n "$p" ] || continue
  b=${p##*/}
  basename_count[$b]=$(( ${basename_count[$b]:-0} + 1 ))
  basename_path[$b]=$p
done < <(
  { git ls-files -z
    git ls-files -o --exclude-standard -z 2>/dev/null
  } | tr '\0' '\n' | sort -u
)

# Echo the file a path-shaped span names, or fail. Same precision rules as
# check 4: repository roots, a real file, and a known extension for a basename.
# Result goes in RESOLVED instead of stdout: this runs for every code span, and
# a command substitution per span would fork tens of thousands of times.
resolve_cited_file() {
  local p=$1 base=$2 n
  RESOLVED=""
  [ -n "$p" ] || return 1
  case "$p" in *' '*|*'<'*|*'>'*|*'$'*|*'='*|*'|'*|*'&'*|*'{'*|*'}'*) return 1 ;; esac
  case "$p" in http://*|https://*|mailto:*|/*|~/*) return 1 ;; esac
  case "$p" in
    */*)
      [[ $p =~ $REPO_ROOTS ]] || return 1
      if [ -f "$p" ]; then RESOLVED=$p; return 0; fi
      if [ -f "$base/$p" ]; then RESOLVED=$base/$p; return 0; fi
      return 1 ;;
    *)
      [[ $p =~ $EXT_RE ]] || return 1
      n=${basename_count[$p]:-0}
      [ "$n" = 1 ] || return 1
      RESOLVED=${basename_path[$p]}
      return 0 ;;
  esac
}

# First line mentioning the literal that is NOT exempt. Empty output means every
# occurrence is exempt; `?` means the literal could not be located, which is
# reported with an unknown line rather than silently dropped.
offending_line_for() {
  local lit=$1 l found=0
  while IFS= read -r l; do
    [ -n "$l" ] || continue
    found=1
    if ! is_exempt_line "$l"; then printf '%s' "$l"; return 0; fi
  done < <(grep -nF -- "$lit" "$CURFILE" 2>/dev/null | cut -d: -f1 || true)
  [ "$found" = 0 ] && printf '?'
  return 0
}

# Same, for `make <target>` assertions. The match has to end at a word boundary,
# otherwise a document's valid `make build-all` line gets blamed for a missing
# `make build`, and the exemption the author then writes sits on the wrong line.
offending_line_for_target() {
  local tok=$1 l found=0 re
  re=$(printf '%s' "$tok" | sed -E 's/([.[\()*+?|^$])/\\\1/g')
  while IFS= read -r l; do
    [ -n "$l" ] || continue
    found=1
    if ! is_exempt_line "$l"; then printf '%s' "$l"; return 0; fi
  done < <(grep -nE "make( +[^ \`]+)* +${re}([^-A-Za-z0-9._%]|\$)" "$CURFILE" 2>/dev/null \
             | cut -d: -f1 || true)
  [ "$found" = 0 ] && printf '?'
  return 0
}

bad_target=()
bad_path=()
bad_link=()
bad_cite=()
bad_bare=()
bad_name=()
warn_bare=()
bad_exempt=()
n_spans=0
n_cites=0
n_exempt=0
n_bare=0
n_bare_attr=0
n_bare_unattr=0
n_name=0
n_snapshot_exempt=0
declare -A unattr_by_file=()
declare -A file_lines=()
# Parallel arrays: one entry per frozen-snapshot file that actually exempted a
# citation that would otherwise have failed.
snap_files=()
snap_dates=()
snap_counts=()
snap_reasons=()
# Set by resolve_cited_file (no subshell per span).
RESOLVED=""

for f in "${md[@]}"; do
  CURFILE=$f
  docdir=$(dirname "$f")

  # Line-local exemptions, keyed by line number so a comment can never shelter a
  # different line than the one it sits on.
  ignore_lines=" "
  while IFS= read -r ln; do
    [ -n "$ln" ] || continue
    ignore_lines="$ignore_lines$ln "
    n_exempt=$((n_exempt + 1))
  done < <(grep -n 'check-docs:ignore' "$f" 2>/dev/null | cut -d: -f1 || true)

  # A frozen snapshot declares itself once, with a freeze date and a reason. A
  # marker missing either is an ERROR and is NOT honoured (fail closed): the
  # stale citations are then reported as usual.
  snapshot_frozen=0
  snapshot_date=""
  snapshot_reason=""
  snap_file_exempt=0
  # Anchored to the start of a line and to an HTML comment, and required to be
  # in the first 10 lines: a document that merely DOCUMENTS the marker (this
  # repository's testing.md does, inline or in a deeper example block) must not
  # acquire a file-level exemption by mentioning it.
  snap_re='^[[:space:]]*<!--[[:space:]]*check-docs:snapshot-frozen'
  smarks=$(grep -cE "$snap_re" "$f" 2>/dev/null || true)
  if [ "${smarks:-0}" -gt 0 ]; then
    sline=$(grep -nE "$snap_re" "$f" | head -1 | cut -d: -f1 || true)
    stext=$(sed -n "${sline}p" "$f")
    sdate=$(printf '%s' "$stext" | sed -nE 's/.*date=([0-9]{4}-[0-9]{2}-[0-9]{2}).*/\1/p')
    sreason=$(printf '%s' "$stext" | sed -nE 's/.*reason="([^"]+)".*/\1/p')
    if [ "$smarks" -gt 1 ]; then
      bad_exempt+=("$f:$sline: more than one check-docs:snapshot-frozen marker")
    elif [ "${sline:-0}" -gt 10 ]; then
      bad_exempt+=("$f:$sline: snapshot marker must be in the first 10 lines")
    elif [ -z "$sdate" ]; then
      bad_exempt+=("$f:$sline: snapshot marker has no date=YYYY-MM-DD")
    elif [ -z "$sreason" ]; then
      bad_exempt+=("$f:$sline: snapshot marker has no reason=\"...\"")
    else
      snapshot_frozen=1
      snapshot_date=$sdate
      snapshot_reason=$sreason
    fi
  fi

  # --- 1. make targets -------------------------------------------------------
  while IFS= read -r tok; do
    [ -n "$tok" ] || continue
    case "$tok" in *-) continue ;; esac                 # `make run-<thing>` template
    printf '%s' "$tok" | grep -qE "$MAKE_PROSE" && continue
    has_target "$tok" && continue
    line=$(offending_line_for_target "$tok")
    [ -z "$line" ] && continue
    bad_target+=("$f:$line: make $tok")
  done < <(grep -oE '(^|[^A-Za-z-])make( +-[^ `]+)* +[a-z][a-z0-9._%-]*' "$f" 2>/dev/null \
             | grep -oE 'make( +-[^ `]+)* +[a-z][a-z0-9._%-]*$' | awk '{print $NF}' | sort -u || true)

  # --- 2. code spans ---------------------------------------------------------
  while IFS= read -r span; do
    [ -n "$span" ] || continue
    n_spans=$((n_spans + 1))
    case "$span" in
      *' '*|*'<'*|*'>'*|*'$'*|*'='*|*'|'*|*'&'*|*'{'*|*'}'*) continue ;;
      -*|'+'*) continue ;;
      http://*|https://*|mailto:*|/*|~/*) continue ;;
    esac
    raw=$span
    # A citation may carry line numbers (`internal/auth/jwt.go:22-27`). Split them
    # off: the path part is then checked for existence and the numbers for fitting
    # inside the file, which is what makes a cited line falsifiable rather than
    # decorative.
    lines_part=""
    if [[ $span =~ ^(.*):([0-9][0-9,./-]*)$ ]]; then
      span=${BASH_REMATCH[1]}
      lines_part=${BASH_REMATCH[2]}
    fi
    if [[ $span =~ $EXTERNAL || $span =~ $EXTERNAL_NAMES || $span =~ $GENERATED ]]; then continue; fi
    # A path assertion needs a separator. Bare names (`dev.sh`, `baseline.json`,
    # `checksums.txt`) usually denote a file kind or a runtime artifact written
    # outside the tree, and asserting on them produced only noise.
    case "$span" in */*) ;; *) continue ;; esac
    # Only paths inside the repository's own source roots are assertions. A span
    # like `golang.org/x/tools@v0.48.0/go/analysis/singlechecker/singlechecker.go:67`
    # cites upstream source, not our tree, and asserting on it would be a false
    # positive; root-level files (Makefile, go.mod, buf.yaml) carry no separator
    # and are covered by the make-target check instead.
    [[ $span =~ $REPO_ROOTS ]] || continue
    # A dotted final segment that is not a known extension names a symbol, not a
    # file: `internal/operator/ca.Load` means package Load in package ca.
    last=${span##*/}
    case "$last" in *.*) [[ $last =~ $EXT_RE ]] || continue ;; esac
    line=$(offending_line_for "\`$raw")
    if ! exists_as "$span" "$docdir"; then
      [ -z "$line" ] && continue
      bad_path+=("$f:$line: \`$raw\`")
      continue
    fi
    # Line numbers are only checkable against a real file. The +1 tolerates a
    # file whose last line has no trailing newline, which wc would not count.
    if [ -n "$lines_part" ] && [ -f "$span" ]; then
      n_cites=$((n_cites + 1))
      max=$(printf '%s' "$lines_part" | tr -c '0-9' '\n' | sort -n | tail -1)
      [ -n "$max" ] || continue
      flen=$(wc -l < "$span")
      if [ "$max" -gt $((flen + 1)) ]; then
        if [ "$snapshot_frozen" = 1 ]; then
          n_snapshot_exempt=$((n_snapshot_exempt + 1))
          snap_file_exempt=$((snap_file_exempt + 1))
          continue
        fi
        [ -z "$line" ] && continue
        bad_cite+=("$f:$line: \`$raw\` — ${span} has $flen lines")
      fi
    fi
  done < <(grep -oE '`[^`]+`' "$f" 2>/dev/null \
             | sed -E 's/^`//; s/`$//' | sort -u || true)

  # --- 3. relative markdown links -------------------------------------------
  while IFS= read -r link; do
    case "$link" in http://*|https://*|mailto:*|'#'*|/*) continue ;; esac
    link=${link%%#*}
    [ -n "$link" ] || continue
    [[ $link =~ $EXTERNAL ]] && continue
    exists_as "$link" "$docdir" && continue
    line=$(offending_line_for "]($link)")
    [ -z "$line" ] && continue
    bad_link+=("$f:$line: ]($link)")
  done < <(grep -oE '\]\([^) ]+\)' "$f" 2>/dev/null | sed -E 's/^\]\(//; s/\)$//' | sort -u || true)

  # --- 6. citations that carry no repository path ----------------------------
  # Check 4 only sees spans that contain a separator (`web/src/x.vue:12`). Two
  # legitimate shapes therefore slip past it:
  #   * `OperationDetailPage.vue:319` — filename only, no separator;
  #   * `:295` — bare line number, with the file named earlier on the same line.
  # Here they are attributed and checked. Attribution is same-line only: the last
  # resolvable file path earlier on the line owns the following bare numbers. A
  # bare number with no such path is counted as UNVERIFIED, never failed and
  # never guessed at (see the header).
  #
  # Confidence decides whether a mismatch FAILS or is only reported:
  #   * same table cell (or a non-table line) -> the owning file is unambiguous,
  #     so an out-of-range number fails;
  #   * across a cell boundary -> a row like `| B2 | Toast.vue … | 同上 :324 |`
  #     cites the previous cell's Requirements doc, not Toast.vue, so a mismatch
  #     there is reported with WARN and not failed. Failing would punish rows
  #     that use "同上"/"见上" shorthand; silently dropping them would hide them.
  # A filename-only citation names its own file, so it always fails when broken.
  bctx_line=-1
  bcur=""
  bcur_cell=-1
  while IFS=$'\t' read -r bline bcell bspan; do
    [ -n "$bspan" ] || continue
    if [ "$bline" != "$bctx_line" ]; then bctx_line=$bline; bcur=""; fi
    is_exempt_line "$bline" && continue
    bpath=$bspan
    bnums=""
    if [[ $bspan =~ ^(.*):([0-9][0-9,./-]*)$ ]]; then
      bpath=${BASH_REMATCH[1]}
      bnums=${BASH_REMATCH[2]}
    fi
    if [ -z "$bpath" ]; then
      n_bare=$((n_bare + 1))
      if [ -z "$bcur" ]; then
        n_bare_unattr=$((n_bare_unattr + 1))
        unattr_by_file[$f]=$(( ${unattr_by_file[$f]:-0} + 1 ))
        continue
      fi
      n_bare_attr=$((n_bare_attr + 1))
      bmax=$(printf '%s' "$bnums" | tr -c '0-9' '\n' | sort -n | tail -1)
      [ -n "$bmax" ] || continue
      blen=${file_lines[$bcur]:-}
      if [ -z "$blen" ]; then blen=$(wc -l < "$bcur"); file_lines[$bcur]=$blen; fi
      if [ "$bmax" -gt $((blen + 1)) ]; then
        if [ "$snapshot_frozen" = 1 ]; then
          n_snapshot_exempt=$((n_snapshot_exempt + 1))
          snap_file_exempt=$((snap_file_exempt + 1))
          continue
        fi
        if [ "$bcell" = "$bcur_cell" ]; then
          bad_bare+=("$f:$bline: \`$bspan\` — $bcur has $blen lines")
        else
          warn_bare+=("$f:$bline: \`$bspan\` — $bcur has $blen lines (owner cited in another table cell)")
        fi
      fi
      continue
    fi
    resolve_cited_file "$bpath" "$docdir" || continue
    bcur=$RESOLVED
    bcur_cell=$bcell
    # Full paths are check 4's job; this section only validates basenames.
    case "$bpath" in */*) continue ;; esac
    [ -n "$bnums" ] || continue
    n_name=$((n_name + 1))
    bmax=$(printf '%s' "$bnums" | tr -c '0-9' '\n' | sort -n | tail -1)
    [ -n "$bmax" ] || continue
    blen=${file_lines[$bcur]:-}
    if [ -z "$blen" ]; then blen=$(wc -l < "$bcur"); file_lines[$bcur]=$blen; fi
    if [ "$bmax" -gt $((blen + 1)) ]; then
      if [ "$snapshot_frozen" = 1 ]; then
        n_snapshot_exempt=$((n_snapshot_exempt + 1))
        snap_file_exempt=$((snap_file_exempt + 1))
        continue
      fi
      bad_name+=("$f:$bline: \`$bspan\` — $bcur has $blen lines")
    fi
  # The awk pass also emits the markdown table-cell index (0 outside a table) so
  # attribution confidence can be judged; see the comment above.
  done < <(awk '{ s=$0; off=0; tab=(s ~ /^[ \t]*\|/); while ((i=index(substr(s,off+1),"`"))>0) { st=off+i; r=substr(s,st+1); j=index(r,"`"); if (j==0) break; c=0; if (tab) c=split(substr(s,1,st), parts, "|"); printf "%d\t%d\t%s\n", NR, c, substr(r,1,j-1); off=st+j } }' "$f")

  # Every frozen file is named even when it exempted nothing: the exemption
  # must never be silent, and a zero is information too.
  if [ "$snapshot_frozen" = 1 ]; then
    snap_files+=("$f")
    snap_dates+=("$snapshot_date")
    snap_counts+=("$snap_file_exempt")
    snap_reasons+=("$snapshot_reason")
  fi
done

# ---------------------------------------------------------------------------
# 5. citations inside vulncheck.exceptions.yaml
#
# That file is not markdown, so the span walk above never sees it, and
# `scripts/vulncheck.sh` reads only its `id:` fields. A compensating_control that
# cites code which no longer exists therefore rots in silence (recorded as V14 in
# the audit). This closes that hole: every repository path the file names must
# exist, and a cited line number must fit inside its file.
#
# Two exemption forms, BOTH requiring a reason -- an exemption without one is an
# error, never a silent pass:
#   * `# check-cite-ignore: <reason>` on the citation's own line, and
#   * an exact path under the entry's `cite_ignore:` list, with `# <reason>` on
#     that same line.
# The second form exists because a citation inside a `>-` block scalar cannot
# carry a YAML comment: a `#` there is literal content, so an inline marker would
# silently become part of the recorded compensating_control text.
#
# DO NOT "tighten" this to inline-only. A citation inside a block scalar would
# then be unexemptable, and the only ways to satisfy the check would be to corrupt
# the recorded reason (append a literal `#`) or to delete the citation -- both
# worse than an exemption that is an EXACT PATH (never a pattern) and is unusable
# without a reason.
#
# How this relates to the markdown exemption above -- two spellings, ON PURPOSE:
#   * markdown documents        -> `<!-- check-docs:ignore: <reason> -->` on the line
#   * vulncheck.exceptions.yaml -> an exact path under the entry's `cite_ignore:`
#     list, with `# <reason>` on that same line
# They are not historical drift; the two formats force them. An HTML comment
# cannot exist in YAML at all, and a `#` inside a `>-` block scalar is literal
# content rather than a comment. Unifying the spelling would mean changing the
# gate's mechanism for no gain beyond cosmetics.
# The INVARIANT they share is what matters, and it is identical in both: an
# exemption MUST carry a reason, and one without a reason is an ERROR rather than
# a silent pass. Never let either spelling become a silent bypass.
# ---------------------------------------------------------------------------
bad_cite_yaml=()
n_yaml_cites=0
# Overridable so the mechanism can be exercised against a fixture: the real file
# may legitimately have no dangling citation, which would otherwise make this
# check unprovable.
VULN_EXC=${CHECK_DOCS_CITE_FILE:-vulncheck.exceptions.yaml}
if [ -f "$VULN_EXC" ]; then
  cite_ignore_paths=" "
  exempt_lines=" "
  # Pass 1: read the exemption declarations, and reject any that carries no reason.
  lno=0
  in_ignore_block=0
  while IFS= read -r text; do
    lno=$((lno + 1))
    if printf '%s' "$text" | grep -qE '^[[:space:]]*cite_ignore:'; then
      in_ignore_block=1
      continue
    fi
    if [ "$in_ignore_block" = 1 ]; then
      if printf '%s' "$text" | grep -qE '^[[:space:]]*-[[:space:]]*"'; then
        ip=$(printf '%s' "$text" | sed -E 's/^[[:space:]]*-[[:space:]]*"([^"]+)".*/\1/')
        ireason=$(printf '%s' "$text" | sed -nE 's/^[^#]*#[[:space:]]*(.+)$/\1/p')
        if [ -z "$ireason" ]; then
          bad_exempt+=("$VULN_EXC:$lno: cite_ignore entry \"$ip\" carries no reason (append: # <reason>)")
        fi
        cite_ignore_paths="$cite_ignore_paths$ip "
      elif [ -n "$(printf '%s' "$text" | tr -d '[:space:]')" ]; then
        in_ignore_block=0
      fi
    fi
    if printf '%s' "$text" | grep -q 'check-cite-ignore:'; then
      lreason=$(printf '%s' "$text" | sed -nE 's/^.*check-cite-ignore:[[:space:]]*(.+)$/\1/p')
      if [ -z "$lreason" ]; then
        bad_exempt+=("$VULN_EXC:$lno: check-cite-ignore carries no reason")
      fi
      exempt_lines="$exempt_lines$lno "
    fi
  done < "$VULN_EXC"

  # Pass 2: every repository path the file names must exist.
  while IFS= read -r hit; do
    [ -n "$hit" ] || continue
    hlno=${hit%%:*}
    raw=${hit#*:}
    case "$exempt_lines" in *" $hlno "*) continue ;; esac
    span=$raw
    lines_part=""
    if [[ $span =~ ^(.*):([0-9][0-9,./-]*)$ ]]; then
      span=${BASH_REMATCH[1]}
      lines_part=${BASH_REMATCH[2]}
    fi
    span=${span%/}
    case "$cite_ignore_paths" in *" $span "*) continue ;; esac
    n_yaml_cites=$((n_yaml_cites + 1))
    if ! exists_as "$span" "."; then
      bad_cite_yaml+=("$VULN_EXC:$hlno: \`$raw\`")
      continue
    fi
    if [ -n "$lines_part" ] && [ -f "$span" ]; then
      max=$(printf '%s' "$lines_part" | tr -c '0-9' '\n' | sort -n | tail -1)
      [ -n "$max" ] || continue
      ylen=$(wc -l < "$span")
      if [ "$max" -gt $((ylen + 1)) ]; then
        bad_cite_yaml+=("$VULN_EXC:$hlno: \`$raw\` — $span has $ylen lines")
      fi
    fi
  done < <(grep -noE '(cmd|internal|api|web|docs|deploy|migrations|scripts|test|configs)/[A-Za-z0-9_./*-]+(:[0-9][0-9,./-]*)?' "$VULN_EXC" 2>/dev/null || true)
fi

echo "check-docs: ${#md[@]} markdown files; ${#targets[@]} Makefile rules; $n_spans code spans inspected; $n_exempt line exemptions"
# Weak guarantee, stated so nobody reads more into the count than it earns: the
# line-number citations are checked for the LINE EXISTING, not for the cited line
# actually being about the named symbol (V13).
echo "check-docs: $n_cites path:line citations checked for line existence ONLY (weak guarantee: cited-line content is not verified)"
echo "check-docs: $n_name filename-only citations (Foo.vue:12) resolved by unique basename and checked the same way"
if [ "$n_bare" -gt 0 ]; then
  echo "check-docs: $n_bare bare :NNN citations; $n_bare_attr attributed to a file earlier on the same line and checked; $n_bare_unattr unattributed and NOT checked"
fi
for idx in "${!snap_files[@]}"; do
  echo "check-docs: frozen snapshot ${snap_files[$idx]} (frozen ${snap_dates[$idx]}): ${snap_counts[$idx]} line-number citation(s) exempted — ${snap_reasons[$idx]}"
done
if [ "$n_bare_unattr" -gt 0 ]; then
  echo "check-docs: WARN $n_bare_unattr bare :NNN citation(s) could not be attributed to a repository file on their line (no path, or an ambiguous/external one), so they were NOT verified (report only; attribution is same-line only)"
  if [ "${#unattr_by_file[@]}" -gt 0 ]; then
    top=$(for k in "${!unattr_by_file[@]}"; do printf '%s\t%s\n' "${unattr_by_file[$k]}" "$k"; done \
            | sort -rn | head -5 | awk '{printf "%s %s, ", $1, $2}' || true)
    echo "check-docs:      most: ${top%, }"
  fi
fi
if [ "${#warn_bare[@]}" -gt 0 ]; then
  echo "check-docs: WARN ${#warn_bare[@]} bare :NNN citation(s) whose owning path is in another table cell are beyond their file — NOT failed, because the number may refer to the previous cell:"
  printf '    %s\n' "${warn_bare[@]}"
fi
echo "check-docs: $n_snapshot_exempt line-number citation(s) exempted by frozen snapshots in total"
echo "check-docs: $n_yaml_cites citations checked in $VULN_EXC"
fail=0
if [ ${#bad_target[@]} -gt 0 ]; then
  fail=1; echo "  missing make target  ${#bad_target[@]}"
  printf '    %s\n' "${bad_target[@]}"
fi
if [ ${#bad_path[@]} -gt 0 ]; then
  fail=1; echo "  path mentioned but not present  ${#bad_path[@]}"
  printf '    %s\n' "${bad_path[@]}"
fi
if [ ${#bad_link[@]} -gt 0 ]; then
  fail=1; echo "  broken relative link  ${#bad_link[@]}"
  printf '    %s\n' "${bad_link[@]}"
fi
if [ ${#bad_cite[@]} -gt 0 ]; then
  fail=1; echo "  citation beyond the end of its file  ${#bad_cite[@]}"
  printf '    %s\n' "${bad_cite[@]}"
fi
if [ ${#bad_bare[@]} -gt 0 ]; then
  fail=1; echo "  bare :NNN citation beyond the end of the file it is attributed to  ${#bad_bare[@]}"
  printf '    %s\n' "${bad_bare[@]}"
fi
if [ ${#bad_name[@]} -gt 0 ]; then
  fail=1; echo "  filename-only citation beyond the end of its file  ${#bad_name[@]}"
  printf '    %s\n' "${bad_name[@]}"
fi
if [ ${#bad_cite_yaml[@]} -gt 0 ]; then
  fail=1; echo "  citation in $VULN_EXC with no such path/line  ${#bad_cite_yaml[@]}"
  printf '    %s\n' "${bad_cite_yaml[@]}"
fi
if [ ${#bad_exempt[@]} -gt 0 ]; then
  fail=1; echo "  invalid citation exemption or snapshot marker  ${#bad_exempt[@]}"
  printf '    %s\n' "${bad_exempt[@]}"
fi
if [ "$fail" = 1 ]; then
  echo "check-docs: FAIL"
  echo "  Either fix the document, or ship what it promises. When a mention is"
  echo "  intentionally not a repository path, mark that same line with"
  echo "  <!-- check-docs:ignore <what it is> --> and say why."
  echo "  A frozen snapshot that cannot be corrected declares itself with"
  echo "  <!-- check-docs:snapshot-frozen date=YYYY-MM-DD reason=\"...\" -->."
  echo "  In $VULN_EXC, use '# check-cite-ignore: <reason>' on the citation's line,"
  echo "  or list the exact path under 'cite_ignore:' with '# <reason>' — the"
  echo "  reason is required in both forms."
  exit 1
fi
echo "check-docs: PASS"

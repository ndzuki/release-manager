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
#
# This repository documents by evidence (`文件:行号`), which only works if those
# line numbers survive edits; a citation that has drifted off the end of its file
# is the same class of defect as a `make` target that does not exist.
#
# Precision over recall. A code span is treated as a path only when it contains a
# separator and either starts with a real source root (cmd/, internal/, api/,
# web/, docs/, deploy/, migrations/, scripts/, test/, configs/, .github/) or ends
# with a known file extension. Bare names (`dev.sh`, `baseline.json`), Go and npm
# import paths (`os/exec`, `@connectrpc/connect`) and
# knowledge-base references (`Design/glossary.md`, `Notes/CONTEXT.md`) are
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
n_spans=0
n_cites=0
n_exempt=0

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
bad_exempt=()
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
echo "check-docs: $n_cites line-number citations checked for line existence ONLY (weak guarantee: cited-line content is not verified)"
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
if [ ${#bad_cite_yaml[@]} -gt 0 ]; then
  fail=1; echo "  citation in $VULN_EXC with no such path/line  ${#bad_cite_yaml[@]}"
  printf '    %s\n' "${bad_cite_yaml[@]}"
fi
if [ ${#bad_exempt[@]} -gt 0 ]; then
  fail=1; echo "  citation exemption without a reason  ${#bad_exempt[@]}"
  printf '    %s\n' "${bad_exempt[@]}"
fi
if [ "$fail" = 1 ]; then
  echo "check-docs: FAIL"
  echo "  Either fix the document, or ship what it promises. When a mention is"
  echo "  intentionally not a repository path, mark that same line with"
  echo "  <!-- check-docs:ignore <what it is> --> and say why."
  echo "  In $VULN_EXC, use '# check-cite-ignore: <reason>' on the citation's line,"
  echo "  or list the exact path under 'cite_ignore:' with '# <reason>' — the"
  echo "  reason is required in both forms."
  exit 1
fi
echo "check-docs: PASS"

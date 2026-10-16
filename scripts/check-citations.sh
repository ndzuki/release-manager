#!/usr/bin/env bash
# check-citations.sh — verify that `test.yml:<line>` citations still point at what
# the citing sentence claims, not merely at a line that happens to exist.
#
# Why this gate exists (and why it is not `check-docs`): `check-docs.sh` proves a
# cited LINE EXISTS inside its file and nothing more — it says so itself ("weak
# guarantee: cited-line content is not verified", V13). A citation can therefore
# point at `go-version: "1.27.2"` while claiming to point at the `e2e-prerequisite`
# job and every gate stays green. This repository did exactly that (TASK-309):
# `test/e2e/README.md` cited `e2e-prerequisite` at `324` when it lived at `420`,
# `SECURITY.md` cited the artifact uploads at the module-cache lines, and
# `.github/SECRETS.md` cited the `web` job at `658` when it lived at `673`.
#
# The closest existing tool, `make audit-citations` (cmd/docscheck), is a
# read-only symbol-proximity audit, not a gate: measured on this tree on
# 2026-10-16 it reported 293 findings — and all 8 of its `test.yml` findings were
# false positives (Go symbol names it attributed to a YAML `env:` line) while it
# missed every drifted citation this script covers. It cannot see a citation that
# is not in a code span (`（test.yml:324）`) or a bare `:658` table cell, and its
# heuristic has no notion of "what the sentence claims". This script does.
#
# Two layers:
#
#   1. STRUCTURAL — every citation to .github/workflows/test.yml in every
#      markdown file, in any spelling (`test.yml:420`, `.github/workflows/test.yml:420`,
#      `` `test.yml`:487-493``, `（:359-365）`, comma lists, full-width punctuation).
#      Checked: `1 <= start <= end <= lines`, and BOTH the start and the end line
#      of every range are NON-BLANK. A citation must begin and end on content; a
#      range that ends on the blank separator before the next job is not "the
#      whole job" (that is how `:37-49` drifts from `:37-48`).
#
#   2. SEMANTIC — the curated expectation table between the RULES markers below.
#      Each rule names a document line (`line_re`), the citation that line must
#      still carry (`spec`), and regular expressions the cited range must match:
#      `start_re` for the range's first line, `end_re` for its last, `must_re`
#      anywhere inside. A rule whose `line_re` matches zero or several lines is a
#      violation — an anchor that rotted must fail loudly, never silently skip.
#
#      The table is a SPOT CHECK, not full coverage: `check-docs` remains the
#      cheap structural net for every other citation. Citations without a passing
#      rule are counted as UNCOVERED and listed in the dry-run inventory so the
#      gap stays visible instead of implied.
#
#      Bare `:NNN` citations (e.g. the `行号` column of the `.github/SECRETS.md`
#      permissions table) cannot be attributed to a file automatically; they are
#      covered only where an explicit rule declares them.
#
# Rules are stored one per line as   doc;line_re;spec;start_re;end_re;must_re
# with `;` as the separator, so a pattern must NOT contain `;` (a literal `|`,
# e.g. a markdown table cell delimiter, is written `\|`).
#
# Usage:
#   scripts/check-citations.sh           dry run: inventory + findings; exit 0
#   scripts/check-citations.sh --check    exit 1 when anything is wrong
set -euo pipefail

# Findings must come out in the same order on every machine.
export LC_ALL=C

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

TARGET=.github/workflows/test.yml
if [ ! -f "$TARGET" ]; then
  echo "check-citations: $TARGET not found — run from the repository root" >&2
  exit 1
fi
flen=$(wc -l < "$TARGET")

mode=dry
for arg in "$@"; do
  case "$arg" in
    --check) mode=check ;;
    --dry-run) mode=dry ;;
    *) echo "check-citations: unknown argument: $arg (use --check for a non-zero exit)" >&2; exit 2 ;;
  esac
done

tmpd=$(mktemp -d)
trap 'rm -rf "$tmpd"' EXIT INT TERM
: >"$tmpd/cites"    # doc|line|spec — extracted from the documents
: >"$tmpd/rules"    # doc|line|spec — declared by a passing rule
: >"$tmpd/fails"    # human-readable findings
: >"$tmpd/failkeys" # doc|line|spec — keys that must render as FAIL

fail() {
  printf '%s\n' "$1" >>"$tmpd/fails"
  [ -n "${2:-}" ] && printf '%s\n' "$2" >>"$tmpd/failkeys" || true
  return 0
}

# Full-width punctuation -> ASCII, so every pattern below can stay ASCII and the
# multibyte forms cannot be mistaken for one another under LC_ALL=C.
norm() { sed 's/：/:/g; s/（/(/g; s/）/)/g; s/，/,/g; s/、/,/g'; }

# The cited line-number specs written on one (already normalised) line: an
# explicit `test.yml:N` citation, plus bare `:N` forms (`（:357）`, `` `:658` ``)
# whose colon is not preceded by a path or word character. Both are split on
# commas so a multi-part citation compares part by part.
specs_on_line() {
  {
    printf '%s\n' "$1" | grep -oE 'test\.yml[`"'"'"']?:[0-9][0-9,-]*' | sed -E 's/^[^:]*://'
    printf '%s\n' "$1" | grep -oE '[^A-Za-z0-9]:[0-9][0-9,-]*' | sed -E 's/^[^:]*://'
  } | tr ',' '\n' | grep -E '^[0-9]+(-[0-9]+)?$' | sort -u || true
}

# ---------------------------------------------------------------------------
# Markdown universe: tracked plus new-but-not-ignored, same as check-docs.sh.
# ---------------------------------------------------------------------------
md=()
while IFS= read -r f; do [ -n "$f" ] && md+=("$f"); done < <(
  { git ls-files -z '*.md' | tr '\0' '\n'
    git ls-files -o --exclude-standard -z '*.md' 2>/dev/null | tr '\0' '\n'
  } | sort -u
)
if [ "${#md[@]}" -eq 0 ]; then
  echo "check-citations: no markdown files found — nothing was checked" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Layer 1: extract every citation and check it structurally.
# ---------------------------------------------------------------------------
n_cites=0
for f in "${md[@]}"; do
  norm <"$f" >"$tmpd/norm"
  while IFS= read -r hit; do
    lno=${hit%%:*}
    spec=${hit#*:}
    spec=${spec#*:}
    spec=$(printf '%s' "$spec" | sed -E 's/,+$//')
    n_cites=$((n_cites + 1))
    for cpart in ${spec//,/ }; do
      printf '%s|%s|%s\n' "$f" "$lno" "$cpart" >>"$tmpd/cites"
    done
    ok=1
    why=""
    for part in ${spec//,/ }; do
      a=${part%%-*}
      b=${part##*-}
      if ! printf '%s' "$a" | grep -qE '^[0-9]+$' || ! printf '%s' "$b" | grep -qE '^[0-9]+$'; then
        ok=0; why="malformed range '$part'"; break
      fi
      if [ "$a" -lt 1 ] || [ "$a" -gt "$b" ] || [ "$b" -gt "$flen" ]; then
        ok=0; why="range $a-$b outside 1..$flen"; break
      fi
      la=$(sed -n "${a}p" "$TARGET")
      lb=$(sed -n "${b}p" "$TARGET")
      if [ -z "${la//[[:space:]]/}" ]; then ok=0; why="start line $a is blank"; break; fi
      if [ -z "${lb//[[:space:]]/}" ]; then ok=0; why="end line $b is blank"; break; fi
    done
    if [ "$ok" = 0 ]; then
      fail "  [FAIL] $f:$lno cites $TARGET:$spec — $why" "$f|$lno|$spec"
    fi
  done < <(grep -noE 'test\.yml[`"'"'"']?:[`"'"'"']?[0-9][0-9,-]*' "$tmpd/norm" || true)
done

# ---------------------------------------------------------------------------
# Layer 2: the curated expectation table.
# ---------------------------------------------------------------------------
n_rules=0
n_rule_parts=0
while IFS=';' read -r doc line_re spec start_re end_re must_re; do
  n_rules=$((n_rules + 1))
  if [ ! -f "$doc" ]; then
    fail "  [FAIL] rule for $doc is stale — file does not exist (/$(printf '%s' "$line_re" | cut -c1-60)/)" ""
    continue
  fi
  norm <"$doc" >"$tmpd/norm"
  hits=$(grep -nE -e "$line_re" "$tmpd/norm" | cut -d: -f1 || true)
  nhits=$(printf '%s\n' "$hits" | grep -cE '^[0-9]+$' || true)
  if [ "$nhits" != 1 ]; then
    fail "  [FAIL] rule anchor matches $nhits lines in $doc (must be exactly 1): /$(printf '%s' "$line_re" | cut -c1-60)/" ""
    continue
  fi
  dline=$hits
  line_text=$(sed -n "${dline}p" "$tmpd/norm")
  present=$(specs_on_line "$line_text")

  for part in ${spec//,/ }; do
    n_rule_parts=$((n_rule_parts + 1))
    key="$doc|$dline|$part"
    # (a) the document must still write this citation.
    if ! printf '%s\n' "$present" | grep -qxF -e "$part"; then
      fail "  [FAIL] $doc:$dline no longer cites $TARGET:$part (its rule says it does) — line: $(printf '%s' "$line_text" | cut -c1-110)" "$key"
      continue
    fi
    a=${part%%-*}
    b=${part##*-}
    if [ "$a" -lt 1 ] || [ "$a" -gt "$b" ] || [ "$b" -gt "$flen" ]; then
      fail "  [FAIL] $doc:$dline cites $TARGET:$part outside 1..$flen" "$key"
      continue
    fi
    la=$(sed -n "${a}p" "$TARGET")
    lb=$(sed -n "${b}p" "$TARGET")
    # (b) the range START must look like the thing claimed. For a one-line
    #     citation the same line also has to satisfy the end predicate.
    if ! printf '%s\n' "$la" | grep -qE -e "$start_re"; then
      fail "  [FAIL] $doc:$dline cites $TARGET:$part — start line $a does not match /$start_re/; it is: $(printf '%s' "$la" | cut -c1-110)" "$key"
      continue
    fi
    # (c) the range END must look like the claimed end.
    if ! printf '%s\n' "$lb" | grep -qE -e "$end_re"; then
      fail "  [FAIL] $doc:$dline cites $TARGET:$part — end line $b does not match /$end_re/; it is: $(printf '%s' "$lb" | cut -c1-110)" "$key"
      continue
    fi
    # (d) an optional anchor anywhere inside the range.
    if [ -n "$must_re" ]; then
      if ! sed -n "${a},${b}p" "$TARGET" | grep -qE -e "$must_re"; then
        fail "  [FAIL] $doc:$dline cites $TARGET:$part — /$must_re/ not found in $a-$b" "$key"
        continue
      fi
    fi
    printf '%s\n' "$key" >>"$tmpd/rules"
  done
done < <(sed -n '/^#RULES-BEGIN$/,/^#RULES-END$/p' "${BASH_SOURCE[0]}" | sed '1d;$d' | sed 's/^#//' | grep -v '^[[:space:]]*$')

# ---------------------------------------------------------------------------
# Report: one inventory line per citation (dry run), then the findings.
# ---------------------------------------------------------------------------
inventory=$(cat "$tmpd/cites" "$tmpd/rules" | sort -u -t'|' -k1,1 -k2,2n -k3,3)
n_inv=$(printf '%s\n' "$inventory" | grep -cE '.' || true)
n_verified=$(sort -u "$tmpd/rules" | grep -cE '.' || true)
n_fail=$(sort -u "$tmpd/failkeys" | grep -cE '.' || true)

if [ "$mode" = dry ]; then
  echo "check-citations: citation inventory for $TARGET ($flen lines)"
  while IFS='|' read -r doc lno spec; do
    [ -n "$doc" ] || continue
    if grep -qxF -e "$doc|$lno|$spec" "$tmpd/failkeys"; then
      verdict="FAIL"
    elif grep -qxF -e "$doc|$lno|$spec" "$tmpd/rules"; then
      verdict="verified"
    else
      verdict="uncovered"
    fi
    printf '  %-10s %s:%s -> %s:%s\n' "$verdict" "$doc" "$lno" "$TARGET" "$spec"
  done <<<"$inventory"
fi

echo "check-citations: ${#md[@]} markdown files; $n_cites citations to $TARGET ($n_inv doc/spec pairs); $n_rules semantic rules over $n_rule_parts cited ranges; $n_verified ranges semantically verified; $((n_inv - n_verified)) without a passing rule"
if [ -s "$tmpd/fails" ]; then
  sort -u "$tmpd/fails"
  echo "check-citations: FAIL ($n_fail cited range(s) contradict their rule; anchor/file failures are listed above)"
  if [ "$mode" = check ]; then exit 1; fi
  echo "check-citations: dry run — exit 0 (pass --check to make this fail)"
  exit 0
fi
echo "check-citations: PASS"

# ---------------------------------------------------------------------------
# The expectation table. Format: doc;line_re;spec;start_re;end_re;must_re
# `line_re` must match exactly one line of `doc`; `spec` is the citation that
# line must still carry; start_re/end_re/must_re are EREs the cited range must
# satisfy. Keep patterns on stable claims (job header, `make` invocation, step
# name), never on incidental wording.
# ---------------------------------------------------------------------------
#RULES-BEGIN
#test/e2e/README.md;^\| `e2e-prerequisite`\(test\.yml;420;^  e2e-prerequisite:$;^  e2e-prerequisite:$;
#test/e2e/README.md;^\| `e2e-prerequisite`\(test\.yml;421;^    if: github\.event_name;^    if: github\.event_name;== 'push'
#test/e2e/README.md;^\| `e2e-prerequisite`\(test\.yml;485;^        run: make e2e-prerequisite-ci$;^        run: make e2e-prerequisite-ci$;
#test/e2e/README.md;^\| `e2e-prerequisite`\(test\.yml;487-493;^      - name: Upload smoke result;^          if-no-files-found: ignore$;^        if: always\(\)$
#test/e2e/README.md;^\| `e2e-prerequisite`\(test\.yml;407-419;^  # AC-066-17 prerequisite smoke;^  # fixed in internal/devfixture;Pull requests intentionally do NOT run it
#test/e2e/README.md;^\| `e2e`\(test\.yml;519;^  e2e:$;^  e2e:$;
#test/e2e/README.md;^\| `e2e`\(test\.yml;520;^    if: github\.event_name;^    if: github\.event_name;inputs\.run-e2e
#test/e2e/README.md;^`e2e` job 需要 12 个 repository secret;528-554;^    env:$;^      DEV_TRUST_ROOT_PRIVATE_KEY:;secrets\.DEV_TRUST_ROOT_PRIVATE_KEY
#test/e2e/README.md;^\| `e2e`\(test\.yml;582;^        run: make dev-up$;^        run: make dev-up$;
#test/e2e/README.md;^\| `e2e`\(test\.yml;585;^        run: make dev-seed$;^        run: make dev-seed$;
#test/e2e/README.md;^\| `e2e`\(test\.yml;590;^        run: make e2e-all$;^        run: make e2e-all$;
#test/e2e/README.md;^\| `e2e`\(test\.yml;592-599;^      - name: Upload E2E artifacts;^          retention-days: 7$;^        if: always\(\)$
#test/e2e/README.md;^\| `e2e`\(test\.yml;601-603;^      - name: Purge CI development environment;^        run: make dev-purge CONFIRM=1$;
#test/e2e/README.md;^\| `e2e`\(test\.yml;510-512;^  # TASK-066 formal E2E gate;^  # demand\.$;Pull requests intentionally do not run this
#test/e2e/README.md;^`e2e` job 需要 12 个 repository secret;522;^    timeout-minutes: 30$;^    timeout-minutes: 30$;
#test/e2e/README.md;^`e2e` job 需要 12 个 repository secret;525-527;^    concurrency:$;^      cancel-in-progress: false$;
#test/e2e/README.md;^`e2e` job 需要 12 个 repository secret;514-518;^  # D-034 narrowed this to dispatch-only;^  # cap, on a hosted runner\.$;
#SECURITY.md;产物上传见;487-493;^      - name: Upload smoke result;^          if-no-files-found: ignore$;^        if: always\(\)$
#SECURITY.md;产物上传见;592-599;^      - name: Upload E2E artifacts;^          retention-days: 7$;^        if: always\(\)$
#SECURITY.md;^   上传为 artifact\(;487-493;^      - name: Upload smoke result;^          if-no-files-found: ignore$;
#SECURITY.md;^   上传为 artifact\(;592-599;^      - name: Upload E2E artifacts;^          retention-days: 7$;
#SECURITY.md;sha256/checksum\(`\.github;382-387;^      - name: Install kind v0\.32\.0$;sha256sum --check;KIND_SHA256
#SECURITY.md;sha256/checksum\(`\.github;448-451;^          base="https://github\.com/k3d-io/k3d/releases;sha256sum -c -;k3d-checksums\.txt
#SECURITY.md;sha256/checksum\(`\.github;570-573;^          base="https://github\.com/k3d-io/k3d/releases;sha256sum -c -;k3d-checksums\.txt
#SECURITY.md;CI 增量 lint;196-211;^      # Lint only the code this PR/commit introduces;^          args: --new-from-rev=;--new-from-rev
#SECURITY.md;是 CI 侧收尾;601-603;^      - name: Purge CI development environment;^        run: make dev-purge CONFIRM=1$;
#SECURITY.md;门禁命令与 CI;37-48;^  sdk-check:$;^        run: go run \./cmd/sdkcheck/;
#docs/cli.md;test\.yml:421` 的 `if:`;421;^    if: github\.event_name;^    if: github\.event_name;== 'push'
#docs/cli.md;^命令 `:485`;485;^        run: make e2e-prerequisite-ci$;^        run: make e2e-prerequisite-ci$;
#docs/cli.md;test\.yml:519;519;^  e2e:$;^  e2e:$;
#docs/cli.md;test\.yml:519;590;^        run: make e2e-all$;^        run: make e2e-all$;
#docs/cli.md;test-sdkcheck` job;332;^  test-sdkcheck:$;^  test-sdkcheck:$;
#docs/cli.md;test\.yml:37`;37;^  sdk-check:$;^  sdk-check:$;
#docs/cli.md;operator-image-sdk-only` job;104;^  operator-image-sdk-only:$;^  operator-image-sdk-only:$;
#docs/api.md;setup buf 后跑;139-147;^      - uses: bufbuild/buf-setup-action;^        run: make proto$;
#docs/api.md;setup buf 后跑;235-241;^      - uses: bufbuild/buf-setup-action;^        run: make proto$;
#docs/api.md;setup buf 后跑;627-661;^  proto-check:$;^          fi$;make lint-proto
#migrations/README.md;test-sqlite` \*\*job\*\*;273-314;^  test-sqlite:$;^        run: go test -race -coverprofile=coverage\.out \./internal/store/sqlite/;
#deploy/README.md;kustomize` 需在 PATH;454;^          go install sigs\.k8s\.io/kustomize/kustomize/v5@v5\.8\.1$;^          go install sigs\.k8s\.io/kustomize/kustomize/v5@v5\.8\.1$;
#deploy/README.md;kustomize` 需在 PATH;576;^          go install sigs\.k8s\.io/kustomize/kustomize/v5@v5\.8\.1$;^          go install sigs\.k8s\.io/kustomize/kustomize/v5@v5\.8\.1$;
#docs/testing.md;test\.yml:423;423;^    timeout-minutes: 45$;^    timeout-minutes: 45$;
#docs/testing.md;test\.yml:500-508;500-508;^      - name: Upload Playwright HTML report;^          if-no-files-found: ignore$;^        if: always\(\)$
#docs/testing.md;test\.yml:487-493;487-493;^      - name: Upload smoke result;^          if-no-files-found: ignore$;
#web/README.md;workflow_dispatch 触发,PR 上不跑;420-508;^  e2e-prerequisite:$;^          if-no-files-found: ignore$;
#web/README.md;workflow_dispatch 触发,PR 上不跑;477;playwright install --with-deps chromium;playwright install --with-deps chromium;
#web/README.md;workflow_dispatch 触发,PR 上不跑;487-493;^      - name: Upload smoke result;^          if-no-files-found: ignore$;
#web/README.md;workflow_dispatch 触发,PR 上不跑;500-508;^      - name: Upload Playwright HTML report;^          if-no-files-found: ignore$;
#web/README.md;；② `e2e-prerequisite` job;420-508;^  e2e-prerequisite:$;^          if-no-files-found: ignore$;
#web/README.md;；② `e2e-prerequisite` job;61-68;^      # The checker classifies the license text;^        run: make check-licenses$;
#web/README.md;make lint-proto.{0,3}test\.yml:627-661;627-661;^  proto-check:$;^          fi$;
#.github/SECRETS.md;逐个 job 核对步骤;37-48;^  sdk-check:$;^        run: go run \./cmd/sdkcheck/;
#.github/SECRETS.md;逐个 job 核对步骤;50-68;^  license-check:$;^        run: make check-licenses$;
#.github/SECRETS.md;逐个 job 核对步骤;70-85;^  install-sdk:$;^        run: make test-install-sdk$;
#.github/SECRETS.md;逐个 job 核对步骤;87-102;^  upgrade-sdk:$;^        run: make test-upgrade-sdk$;
#.github/SECRETS.md;逐个 job 核对步骤;104-116;^  operator-image-sdk-only:$;^        run: make test-operator-image-sdk-only$;
#.github/SECRETS.md;逐个 job 核对步骤;118-211;^  test:$;^          args: --new-from-rev=;
#.github/SECRETS.md;逐个 job 核对步骤;273-314;^  test-sqlite:$;^        run: go test -race -coverprofile=coverage\.out \./internal/store/sqlite/;
#.github/SECRETS.md;逐个 job 核对步骤;332-369;^  test-sdkcheck:$;^        run: go test -race -coverprofile=coverage\.out \./internal/quality/sdkcheck/;
#.github/SECRETS.md;逐个 job 核对步骤;611-625;^  docs-check:$;^        run: make check-docs$;
#.github/SECRETS.md;逐个 job 核对步骤;627-661;^  proto-check:$;^          fi$;
#.github/SECRETS.md;^\| `e2e` \| job \|;523-524;^    permissions:$;^      contents: read$;
#.github/SECRETS.md;^\| `e2e-prerequisite` \| job \|;420;^  e2e-prerequisite:$;^  e2e-prerequisite:$;
#.github/SECRETS.md;^\| `e2e-prerequisite` \| job \|;424-425;^    permissions:$;^      contents: read$;
#.github/SECRETS.md;^\| `e2e-prerequisite` \| job \|;487-493;^      - name: Upload smoke result;^          if-no-files-found: ignore$;
#.github/SECRETS.md;^\| `test` \| job \|;118;^  test:$;^  test:$;
#.github/SECRETS.md;^\| `test` \| job \|;144;^          github_token: \$\{\{ secrets\.GITHUB_TOKEN \}\}$;^          github_token: \$\{\{ secrets\.GITHUB_TOKEN \}\}$;
#.github/SECRETS.md;^\| `test-postgres-integration` \| job \|;222;^  test-postgres-integration:$;^  test-postgres-integration:$;
#.github/SECRETS.md;^\| `test-postgres-integration` \| job \|;238;^          github_token: \$\{\{ secrets\.GITHUB_TOKEN \}\}$;^          github_token: \$\{\{ secrets\.GITHUB_TOKEN \}\}$;
#.github/SECRETS.md;^\| `test-sqlite` \| job \|;273;^  test-sqlite:$;^  test-sqlite:$;
#.github/SECRETS.md;^\| `test-sqlite` \| job \|;290;^          github_token: \$\{\{ secrets\.GITHUB_TOKEN \}\}$;^          github_token: \$\{\{ secrets\.GITHUB_TOKEN \}\}$;
#.github/SECRETS.md;^\| `docs-check` \| job \|;611;^  docs-check:$;^  docs-check:$;
#.github/SECRETS.md;^\| `proto-check` \| job \|;627;^  proto-check:$;^  proto-check:$;
#.github/SECRETS.md;^\| `proto-check` \| job \|;638;^          github_token: \$\{\{ secrets\.GITHUB_TOKEN \}\}$;^          github_token: \$\{\{ secrets\.GITHUB_TOKEN \}\}$;
#.github/SECRETS.md;^\| `web` \| job \|;673;^  web:$;^  web:$;
#.github/SECRETS.md;^\| `store-surface` \| job \|;717;^  store-surface:$;^  store-surface:$;
#.github/SECRETS.md;^\| `dead-methods` \| job \|;734;^  dead-methods:$;^  dead-methods:$;
#.github/SECRETS.md;注释见 `\.github/workflows/test\.yml:142-143;142-143;^          # Authenticate API calls to avoid GitHub API rate limits;buf release lookups;
#.github/SECRETS.md;^`on:` 触发范围;3-13;^on:$;^        type: boolean$;
#.github/SECRETS.md;^\| `GITHUB_TOKEN`;15-16;^permissions:$;^  contents: read$;
#.github/SECRETS.md;^\| `GITHUB_TOKEN`;139-144;^      - uses: bufbuild/buf-setup-action;^          github_token:;version: "1\.71\.0"
#.github/SECRETS.md;^\| `GITHUB_TOKEN`;235-238;^      - uses: bufbuild/buf-setup-action;^          github_token:;version: "1\.71\.0"
#.github/SECRETS.md;^\| `GITHUB_TOKEN`;285-290;^      - uses: bufbuild/buf-setup-action;^          github_token:;version: "1\.71\.0"
#.github/SECRETS.md;^\| `GITHUB_TOKEN`;633-638;^      - uses: bufbuild/buf-setup-action;^          github_token:;version: "1\.71\.0"
#.github/SECRETS.md;^\| `E2E_RUNNER_PASSWORD`;532;secrets\.E2E_RUNNER_PASSWORD;secrets\.E2E_RUNNER_PASSWORD;
#.github/SECRETS.md;^\| `E2E_RUNNER_PASSWORD`;588;^        env:$;^        env:$;
#.github/SECRETS.md;^\| `DEV_ADMIN_PASSWORD` \| `e2e` job 级 env;533;secrets\.DEV_ADMIN_PASSWORD;secrets\.DEV_ADMIN_PASSWORD;
#.github/SECRETS.md;^\| `DEV_ADMIN_PASSWORD` \| `e2e` job 级 env;584-585;^      - name: Seed CI development fixture$;^        run: make dev-seed$;
#.github/SECRETS.md;^\| `DEV_DEPLOYER_PASSWORD`;534;secrets\.DEV_DEPLOYER_PASSWORD;secrets\.DEV_DEPLOYER_PASSWORD;
#.github/SECRETS.md;^\| `DEV_READER_PASSWORD`;535;secrets\.DEV_READER_PASSWORD;secrets\.DEV_READER_PASSWORD;
#.github/SECRETS.md;^\| `DEV_JWT_PRIVATE_KEY` \| `\.github/workflows/test\.yml:538`;538;secrets\.DEV_JWT_PRIVATE_KEY;secrets\.DEV_JWT_PRIVATE_KEY;
#.github/SECRETS.md;^\| `DEV_WEBHOOK_SERVICE_TOKEN` \| `\.github/workflows/test\.yml:539`;539;secrets\.DEV_WEBHOOK_SERVICE_TOKEN;secrets\.DEV_WEBHOOK_SERVICE_TOKEN;
#.github/SECRETS.md;^\| `DEV_CI_API_KEY`;543;secrets\.DEV_CI_API_KEY;secrets\.DEV_CI_API_KEY;
#.github/SECRETS.md;^\| `DEV_HARBOR_SERVICE_TOKEN`;544;secrets\.DEV_HARBOR_SERVICE_TOKEN;secrets\.DEV_HARBOR_SERVICE_TOKEN;
#.github/SECRETS.md;^\| `DEV_NOTIFIER_SERVICE_TOKEN`;551;secrets\.DEV_NOTIFIER_SERVICE_TOKEN;secrets\.DEV_NOTIFIER_SERVICE_TOKEN;
#.github/SECRETS.md;^\| `DEV_M_TLS_CA_KEY` \| `\.github/workflows/test\.yml:552`;552;secrets\.DEV_M_TLS_CA_KEY;secrets\.DEV_M_TLS_CA_KEY;
#.github/SECRETS.md;^\| `DEV_M_TLS_CA_CERT`;553;secrets\.DEV_M_TLS_CA_CERT;secrets\.DEV_M_TLS_CA_CERT;
#.github/SECRETS.md;^\| `DEV_TRUST_ROOT_PRIVATE_KEY` \| `\.github/workflows/test\.yml:554`;554;secrets\.DEV_TRUST_ROOT_PRIVATE_KEY;secrets\.DEV_TRUST_ROOT_PRIVATE_KEY;
#.github/SECRETS.md;^合计:\*\*18 处;528-554;^    env:$;^      DEV_TRUST_ROOT_PRIVATE_KEY:;
#.github/SECRETS.md;^\| `RUNS_ON`;24-34;^#$;gh variable delete RUNS_ON;
#.github/SECRETS.md;^\| `RUNS_ON`;26;vars\.RUNS_ON;vars\.RUNS_ON;
#.github/SECRETS.md;^\| `RUNS_ON`;38;^    runs-on: \$\{\{ vars\.RUNS_ON;^    runs-on: \$\{\{ vars\.RUNS_ON;
#.github/SECRETS.md;^\| `RUNS_ON`;45;^          cache: \$\{\{ vars\.RUNS_ON;^          cache: \$\{\{ vars\.RUNS_ON;
#.github/SECRETS.md;^\| `RUNS_ON`;166,175,298,306,347,355;^        if: vars\.RUNS_ON != 'self-hosted'$;^        if: vars\.RUNS_ON != 'self-hosted'$;
#.github/SECRETS.md;^\| `RUNS_ON`;388,473;vars\.RUNS_ON.*self-hosted;vars\.RUNS_ON.*self-hosted;
#.github/SECRETS.md;^\| `RUNS_ON`;404;WORKLOAD_IMAGE;WORKLOAD_IMAGE;
#.github/SECRETS.md;^\| `RUNS_ON`;735;^    runs-on: \$\{\{ vars\.RUNS_ON;^    runs-on: \$\{\{ vars\.RUNS_ON;
#.github/SECRETS.md;^切换命令\(;29-34;^#   failed or your spending limit;gh variable delete RUNS_ON;
#.github/SECRETS.md;^\*\*不是 secret 的固定值;384;KIND_SHA256;KIND_SHA256;
#.github/SECRETS.md;^\*\*不是 secret 的固定值;404;WORKLOAD_IMAGE;WORKLOAD_IMAGE;
#.github/SECRETS.md;^\*\*不是 secret 的固定值;529-531;^      DEV_PROFILE: ci$;^      E2E_ENVIRONMENT: ci$;E2E_RUN_ID
#.github/SECRETS.md;^1\. \*\*`e2e`\(;4-5;^  push:$;^    branches: \[main\]$;
#.github/SECRETS.md;^1\. \*\*`e2e`\(;510-512;^  # TASK-066 formal E2E gate;^  # demand\.$;
#.github/SECRETS.md;^1\. \*\*`e2e`\(;520;^    if: github\.event_name;^    if: github\.event_name;inputs\.run-e2e
#.github/SECRETS.md;^3\. \*\*`e2e-prerequisite`\(;420-508;^  e2e-prerequisite:$;^          if-no-files-found: ignore$;
#.github/SECRETS.md;^3\. \*\*`e2e-prerequisite`\(;487-493;^      - name: Upload smoke result;^          if-no-files-found: ignore$;
#.github/SECRETS.md;^3\. \*\*`e2e-prerequisite`\(;500-508;^      - name: Upload Playwright HTML report;^          if-no-files-found: ignore$;
#.github/SECRETS.md;^- \*\*纪律\*\*:这些值只用于 ephemeral;592-599;^      - name: Upload E2E artifacts;^          retention-days: 7$;
#.github/SECRETS.md;^- `POSTGRES_TEST_DSN`;222-271;^  test-postgres-integration:$;docker rm -f ci-postgres;
#.github/SECRETS.md;^- `POSTGRES_TEST_DSN`;265;POSTGRES_TEST_DSN;POSTGRES_TEST_DSN;
#.github/SECRETS.md;^- `POSTGRES_TEST_DSN`;267;go test -tags integration -count=1;go test -tags integration -count=1;
#.github/SECRETS.md;^1\. \*\*现状:两个文件里没有任何写权限 job;15-16;^permissions:$;^  contents: read$;
#.github/SECRETS.md;^1\. \*\*现状:两个文件里没有任何写权限 job;523-524;^    permissions:$;^      contents: read$;
#.github/SECRETS.md;^3\. \*\*表达式内插进 shell 脚本的注入面;211;^          args: --new-from-rev=;^          args: --new-from-rev=;
#.github/SECRETS.md;^3\. \*\*表达式内插进 shell 脚本的注入面;388,473;vars\.RUNS_ON.*self-hosted;vars\.RUNS_ON.*self-hosted;
#.github/SECRETS.md;^3\. \*\*表达式内插进 shell 脚本的注入面;404;WORKLOAD_IMAGE;WORKLOAD_IMAGE;
#.github/SECRETS.md;^5\. \*\*下载物完整性;384-387;KIND_SHA256;sha256sum --check;
#.github/SECRETS.md;^5\. \*\*下载物完整性;404;WORKLOAD_IMAGE;WORKLOAD_IMAGE;
#.github/SECRETS.md;^5\. \*\*下载物完整性;450-451;^          curl -fsSL -o /tmp/k3d-checksums.txt;sha256sum -c -;
#.github/SECRETS.md;^6\. \*\*制品与日志;487-493;^      - name: Upload smoke result;^          if-no-files-found: ignore$;
#.github/SECRETS.md;^6\. \*\*制品与日志;592-599;^      - name: Upload E2E artifacts;^          retention-days: 7$;
#.github/SECRETS.md;^6\. \*\*制品与日志;500-508;^      - name: Upload Playwright HTML report;^          if-no-files-found: ignore$;
#.github/SECRETS.md;^\| live-PostgreSQL 门禁在 CI 的执行;222-271;^  test-postgres-integration:$;docker rm -f ci-postgres;
#.github/SECRETS.md;^\| live-PostgreSQL 门禁在 CI 的执行;265;POSTGRES_TEST_DSN;POSTGRES_TEST_DSN;
#.github/SECRETS.md;^\| live-PostgreSQL 门禁在 CI 的执行;267;go test -tags integration -count=1;go test -tags integration -count=1;
#.github/SECRETS.md;^\| 自托管 runner 的标签与主机名口径;30-32;arch-dev-1;gh variable set RUNS_ON;actions-runner-release-manager
#SECURITY.md;^\| 已发布版本 \|;3-13;^on:$;^        type: boolean$;
#SECURITY.md;^\| 受支持分支 \|;3-13;^on:$;^        type: boolean$;pull_request
#SECURITY.md;^\| 制品分发 \|;104-116;^  operator-image-sdk-only:$;^        run: make test-operator-image-sdk-only$;
#SECURITY.md;imagecheck\.operator\.yaml:2-5;104-116;^  operator-image-sdk-only:$;^        run: make test-operator-image-sdk-only$;
#SECURITY.md;test\.yml:50-68.*license-check;50-68;^  license-check:$;^        run: make check-licenses$;
#SECURITY.md;sha256/checksum\(`\.github;24-34;^#$;gh variable delete RUNS_ON;
#deploy/README.md;CI job test\.yml:104-116;104-116;^  operator-image-sdk-only:$;^        run: make test-operator-image-sdk-only$;
#docs/api.md;^命名豁免有 2 处;645;^        run: make lint-proto$;^        run: make lint-proto$;
#.github/SECRETS.md;^\| `test\.yml` \| workflow 级;15-16;^permissions:$;^  contents: read$;
#.github/SECRETS.md;^\| `test\.yml` → `sdk-check`;37;^  sdk-check:$;^  sdk-check:$;
#.github/SECRETS.md;^\| `license-check` \| job \|;50;^  license-check:$;^  license-check:$;
#.github/SECRETS.md;^\| `install-sdk` \| job \|;70;^  install-sdk:$;^  install-sdk:$;
#.github/SECRETS.md;^\| `upgrade-sdk` \| job \|;87;^  upgrade-sdk:$;^  upgrade-sdk:$;
#.github/SECRETS.md;^\| `operator-image-sdk-only` \| job \|;104;^  operator-image-sdk-only:$;^  operator-image-sdk-only:$;
#.github/SECRETS.md;^\| `vulncheck` \| job \|;316;^  vulncheck:$;^  vulncheck:$;
#.github/SECRETS.md;^\| `test-sdkcheck` \| job \|;332;^  test-sdkcheck:$;^  test-sdkcheck:$;
#.github/SECRETS.md;^\| `rollout-watch` \| job \|;371;^  rollout-watch:$;^  rollout-watch:$;
#.github/SECRETS.md;^5\. \*\*下载物完整性;572-573;^          curl -fsSL -o /tmp/k3d-checksums.txt;sha256sum -c -;
#.github/SECRETS.md;^4\. \*\*下列 10 个 job;421,520;^    if: github\.event_name;^    if: github\.event_name;github\.event_name
#test/e2e/README.md;^\| `e2e`\(test\.yml;9-13;^      run-e2e:$;^        type: boolean$;default: true
#RULES-END

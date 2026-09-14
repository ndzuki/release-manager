#!/usr/bin/env bash
#
# check-licenses.sh — audit the licenses of every dependency that ships here.
#
#   scripts/check-licenses.sh                       gate: exit 1 on a license we cannot ship
#   scripts/check-licenses.sh --write FILE          gate + regenerate the inventory
#   scripts/check-licenses.sh --write-notice FILE   gate + regenerate the bundled notices
#
# Scope
#   * Go: the modules that contribute to the default build (`go list -deps ./...`).
#     Test-only modules never reach a shipped binary and are out of scope.
#   * Frontend: the production half of `web/package-lock.json`, because dev
#     dependencies are not bundled into the console asset.
#
# Only bash, the Go toolchain and jq are needed: the module graph already lists
# every dependency and each module ships its license text next to its source, so
# no scanner has to be installed and no network access beyond the module
# download is required.
set -euo pipefail

# Generated artifacts must be byte-identical on every machine, so pin the
# collation. Developer machines commonly run en_US.UTF-8 while CI runs C.UTF-8,
# and that alone reorders the module sections of NOTICE (go.yaml.in before
# gopkg.in under one locale, after it under the other), which made the freshness
# check fail on the runner while passing locally.
export LC_ALL=C

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

OUT=""
NOTICE_OUT=""
while [ $# -gt 0 ]; do
  case "$1" in
    --write)
      [ $# -ge 2 ] || { echo "check-licenses: --write needs an output path" >&2; exit 2; }
      OUT=$2
      shift 2
      ;;
    --write-notice)
      [ $# -ge 2 ] || { echo "check-licenses: --write-notice needs an output path" >&2; exit 2; }
      NOTICE_OUT=$2
      shift 2
      ;;
    -h | --help)
      sed -n '3,20p' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *)
      echo "check-licenses: unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

EXCEPTIONS=license-exceptions.tsv
NOTICE=NOTICE

# Licenses this project may ship. MPL-2.0 is file-level copyleft and never
# reaches our code; everything in is_denied does reach it, because Go links the
# whole module into the binary.
ALLOWED_RE='^(Apache-2\.0|MIT|BSD-2-Clause|BSD-3-Clause|ISC|MPL-2\.0|Unlicense|0BSD|CC0-1\.0|Zlib|PostgreSQL|BlueOak-1\.0\.0|Python-2\.0|CC-BY-4\.0)$'

is_denied() {
  case "$1" in
    *GPL* | *SSPL* | *BUSL* | *lastic*) return 0 ;;
    *) return 1 ;;
  esac
}

is_allowed() { [[ $1 =~ $ALLOWED_RE ]]; }

license_files() {
  find "$1" -maxdepth 1 -type f \
    \( -iname 'LICENSE*' -o -iname 'LICENCE*' -o -iname 'COPYING*' -o -iname 'UNLICENSE*' \) \
    -print 2>/dev/null | sort
}

# classify_file maps license text to an SPDX-ish identifier, or UNRECOGNISED.
# Order matters: AGPL/LGPL texts quote "GNU GENERAL PUBLIC LICENSE" as well.
classify_file() {
  local t
  t=$(tr -d '\r' | tr '\n' ' ')
  case "$t" in
    *"GNU AFFERO GENERAL PUBLIC LICENSE"*)
      echo AGPL-3.0
      ;;
    *"GNU LESSER GENERAL PUBLIC LICENSE"*)
      case "$t" in
        *"Version 3"*) echo LGPL-3.0 ;;
        *) echo LGPL-2.1 ;;
      esac
      ;;
    *"GNU GENERAL PUBLIC LICENSE"*)
      case "$t" in
        *"Version 3"*) echo GPL-3.0 ;;
        *) echo GPL-2.0 ;;
      esac
      ;;
    *"Server Side Public License"*)
      echo SSPL-1.0
      ;;
    *"Business Source License"*)
      echo BUSL-1.1
      ;;
    *"Apache License"*)
      case "$t" in
        *"Version 2.0"*) echo Apache-2.0 ;;
        *) echo UNRECOGNISED ;;
      esac
      ;;
    *"Mozilla Public License"*"2.0"*)
      echo MPL-2.0
      ;;
    *"Permission is hereby granted, free of charge"*)
      echo MIT
      ;;
    *"Redistribution and use in source and binary forms"*)
      case "$t" in
        *"Neither the name"*) echo BSD-3-Clause ;;
        *) echo BSD-2-Clause ;;
      esac
      ;;
    *"Permission to use, copy, modify, and/or distribute this software"*)
      echo ISC
      ;;
    *"free and unencumbered software released into the public domain"*)
      echo Unlicense
      ;;
    *"Creative Commons"*"CC0 1.0"*)
      echo CC0-1.0
      ;;
    *"PostgreSQL License"* | *"without fee, and without a written agreement"*)
      echo PostgreSQL
      ;;
    *"altered source versions must be plainly marked"*)
      echo Zlib
      ;;
    *"Blue Oak Model License"*)
      echo BlueOak-1.0.0
      ;;
    *)
      echo UNRECOGNISED
      ;;
  esac
}

# license_ids prints the licenses found in a module directory joined with "|",
# or the sentinel NO-LICENSE / UNRECOGNISED.
license_ids() {
  local dir=$1 f ids="" id found=0
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    found=1
    id=$(classify_file <"$f")
    if [ "$id" != UNRECOGNISED ]; then ids="$ids $id"; fi
  done < <(license_files "$dir")

  if [ -n "${ids// /}" ]; then
    printf '%s\n' $ids | sort -u | paste -sd'|' -
  elif [ "$found" -eq 1 ]; then
    echo UNRECOGNISED
  else
    echo NO-LICENSE
  fi
}

# exception_reason prints the recorded justification for a module or package.
# A key ending in "/" matches every path below that prefix.
exception_reason() {
  [ -f "$EXCEPTIONS" ] || return 1
  awk -F'\t' -v key="$1" '
    /^[[:space:]]*#/ { next }
    NF >= 2 {
      if ($1 == key || (substr($1, length($1)) == "/" && index(key, $1) == 1)) {
        print $2
        found = 1
      }
    }
    END { exit(found ? 0 : 1) }
  ' "$EXCEPTIONS"
}

# verdict reduces a license id list or SPDX expression to OK / DENIED / REVIEW / MISSING.
verdict() {
  local spec=$1 id any_review=0
  case "$spec" in
    NO-LICENSE) echo MISSING; return ;;
    UNRECOGNISED) echo REVIEW; return ;;
  esac

  spec=${spec//(/}
  spec=${spec//)/}
  spec=${spec// AND /|}
  spec=${spec// OR /|}
  spec=${spec//, /|}
  local IFS='|'
  for id in $spec; do
    id=${id#"${id%%[![:space:]]*}"}
    id=${id%"${id##*[![:space:]]}"}
    [ -n "$id" ] || continue
    if is_denied "$id"; then
      echo DENIED
      return
    fi
    if ! is_allowed "$id"; then any_review=1; fi
  done
  if [ "$any_review" -eq 1 ]; then echo REVIEW; else echo OK; fi
}

# write_notice reproduces, verbatim, the NOTICE file of every bundled module that
# ships one: Apache-2.0 §4(d) requires those attribution notices to travel with
# the distribution, and copying them keeps the text byte-identical to upstream.
write_notice() {
  local out=$1 kind name version spec has_notice dir
  {
    echo "release-manager"
    echo "Copyright 2026 Nero Yang"
    echo
    echo "This product includes software developed by third parties. The notices below are"
    echo "reproduced verbatim from the NOTICE files shipped with the Go modules linked into"
    echo "this project's binaries. The complete dependency list, with license identifiers, is"
    echo "in docs/dependencies.md; this project's own license is in LICENSE."
    echo
    echo "Generated by scripts/check-licenses.sh --write-notice NOTICE — do not edit by hand."
    echo
    while IFS=$'\t' read -r kind name version spec has_notice dir; do
      [ "$has_notice" = yes ] || continue
      echo "--------------------------------------------------------------------------------"
      echo "$name $version"
      echo
      cat "$dir/NOTICE"
      echo
    done <"$rows"
  } >"$out"
}

main_module=$(go list -m)
go mod download

rows=$(mktemp)
go_table=$(mktemp)
fe_table=$(mktemp)
notices=$(mktemp)
failures=$(mktemp)
trap 'rm -f "$rows" "$go_table" "$fe_table" "$notices" "$failures"' EXIT

go_total=0 go_ok=0 go_review=0 go_denied=0 go_missing=0 go_exempt=0
fe_total=0 fe_ok=0 fe_review=0 fe_denied=0 fe_missing=0 fe_exempt=0 fe_skipped=0

while IFS=$'\t' read -r path version dir; do
  [ -n "${dir:-}" ] || continue
  [ "$path" = "$main_module" ] && continue
  printf 'go\t%s\t%s\t%s\t%s\t%s\n' "$path" "${version:-local}" "$(license_ids "$dir")" \
    "$([ -f "$dir/NOTICE" ] && echo yes || echo no)" "$dir" >>"$rows"
done < <(go list -deps -f '{{with .Module}}{{.Path}}{{"\t"}}{{.Version}}{{"\t"}}{{.Dir}}{{end}}' ./... | sort -u)

if command -v jq >/dev/null 2>&1 && [ -f web/package-lock.json ]; then
  while IFS=$'\t' read -r name version spec; do
    [ -n "$name" ] || continue
    printf 'npm\t%s\t%s\t%s\tno\t\n' "$name" "$version" "$spec" >>"$rows"
  done < <(jq -r '
      .packages // {}
      | to_entries[]
      | select(.key | startswith("node_modules/"))
      | select(.value.dev != true)
      | [(.key | sub("^node_modules/"; "")), (.value.version // "-"), (.value.license // "NO-LICENSE")]
      | @tsv' web/package-lock.json)
else
  fe_skipped=1
  echo "check-licenses: jq or web/package-lock.json unavailable — frontend licenses skipped" >&2
fi

while IFS=$'\t' read -r kind name version spec has_notice dir; do
  [ -n "$kind" ] || continue
  status=$(verdict "$spec")
  reason=$(exception_reason "$name" || true)
  effective=$status
  if [ -n "$reason" ] && [ "$status" != OK ]; then effective=EXEMPT; fi

  note=""
  case "$status" in
    MISSING) note="no license file — no redistribution right" ;;
    REVIEW) note="license not recognised" ;;
    DENIED) note="strong copyleft — cannot ship in a binary" ;;
  esac
  if [ -n "$reason" ]; then note="exception: $reason"; fi

  if [ "$kind" = go ]; then
    go_total=$((go_total + 1))
    case "$effective" in
      OK) go_ok=$((go_ok + 1)) ;;
      EXEMPT)
        go_exempt=$((go_exempt + 1))
        case "$status" in MISSING) go_missing=$((go_missing + 1)) ;; DENIED) go_denied=$((go_denied + 1)) ;; *) go_review=$((go_review + 1)) ;; esac
        ;;
      MISSING)
        go_missing=$((go_missing + 1))
        printf 'missing %s\n' "$name" >>"$failures"
        ;;
      DENIED)
        go_denied=$((go_denied + 1))
        printf 'denied  %s [%s]\n' "$name" "$spec" >>"$failures"
        ;;
      *)
        go_review=$((go_review + 1))
        printf 'review  %s [%s]\n' "$name" "$spec" >>"$failures"
        ;;
    esac
    printf '| `%s` | %s | %s | %s |\n' "$name" "$version" "$spec" "$note" >>"$go_table"
    [ "$has_notice" = yes ] && printf '| `%s` |\n' "$name" >>"$notices"
  else
    fe_total=$((fe_total + 1))
    case "$effective" in
      OK) fe_ok=$((fe_ok + 1)) ;;
      EXEMPT)
        fe_exempt=$((fe_exempt + 1))
        case "$status" in MISSING) fe_missing=$((fe_missing + 1)) ;; DENIED) fe_denied=$((fe_denied + 1)) ;; *) fe_review=$((fe_review + 1)) ;; esac
        ;;
      MISSING)
        fe_missing=$((fe_missing + 1))
        printf 'missing %s (npm)\n' "$name" >>"$failures"
        ;;
      DENIED)
        fe_denied=$((fe_denied + 1))
        printf 'denied  %s [%s] (npm)\n' "$name" "$spec" >>"$failures"
        ;;
      *)
        fe_review=$((fe_review + 1))
        printf 'review  %s [%s] (npm)\n' "$name" "$spec" >>"$failures"
        ;;
    esac
    printf '| `%s` | %s | %s | %s |\n' "$name" "$version" "$spec" "$note" >>"$fe_table"
  fi
done <"$rows"

if [ -n "$NOTICE_OUT" ]; then
  write_notice "$NOTICE_OUT"
  echo "check-licenses: wrote $NOTICE_OUT"
fi

# NOTICE is a release artifact: a dependency bump that adds or drops an upstream
# NOTICE must not leave it silently stale.
if [ -f "$NOTICE" ]; then
  fresh=$(mktemp)
  write_notice "$fresh"
  if ! diff -q "$NOTICE" "$fresh" >/dev/null; then
    printf 'stale   %s (regenerate: scripts/check-licenses.sh --write-notice %s)\n' "$NOTICE" "$NOTICE" >>"$failures"
  fi
  rm -f "$fresh"
fi

if [ -n "$OUT" ]; then
  notice_count=$(wc -l <"$notices" | tr -d ' ')
  {
    echo "<!-- Generated by scripts/check-licenses.sh --write $OUT — do not edit by hand. -->"
    echo
    echo "# 依赖许可清单"
    echo
    echo "本清单列出**会随产物分发**的依赖及其许可证，是根目录 \`LICENSE\`（Apache-2.0）之外的第三方声明来源。"
    echo "本地与 CI 都通过 \`make check-licenses\` 强制校验，规则见文末「政策与维护」。"
    echo
    echo "## 摘要"
    echo
    echo "| 范围 | 数量 | 通过 | 待复核 | 强 copyleft | 无许可证 | 例外 |"
    echo "| --- | --- | --- | --- | --- | --- | --- |"
    printf '| Go 模块（默认构建闭包） | %d | %d | %d | %d | %d | %d |\n' \
      "$go_total" "$go_ok" "$go_review" "$go_denied" "$go_missing" "$go_exempt"
    printf '| 前端运行时依赖 | %d | %d | %d | %d | %d | %d |\n' \
      "$fe_total" "$fe_ok" "$fe_review" "$fe_denied" "$fe_missing" "$fe_exempt"
    printf '| 自带 NOTICE 的 Go 模块（正文已汇总到仓库根目录 `NOTICE`） | %s | — | — | — | — | — |\n' "$notice_count"
    echo
    echo "## Go 模块"
    echo
    echo "范围：\`go list -deps ./...\` 的模块集合，即会被链接进服务与 CLI 的依赖。仅测试使用的模块不在其中。"
    echo
    echo "| 模块 | 版本 | 许可证 | 备注 |"
    echo "| --- | --- | --- | --- |"
    cat "$go_table"
    echo
    echo "## 前端运行时依赖"
    echo
    echo "范围：\`web/package-lock.json\` 中 \`dev != true\` 的包，即会被打进控制台产物的依赖。"
    echo
    echo "| 包 | 版本 | 许可证 | 备注 |"
    echo "| --- | --- | --- | --- |"
    cat "$fe_table"
    echo
    echo "## 政策与维护"
    echo
    echo "- **允许**：Apache-2.0、MIT、BSD-2/3-Clause、ISC、MPL-2.0（文件级 copyleft，不触及本项目代码）、Unlicense、0BSD、CC0-1.0、Zlib、PostgreSQL、BlueOak-1.0.0、Python-2.0、CC-BY-4.0。"
    echo "- **拒绝**：GPL / AGPL / LGPL、SSPL、BUSL、Elastic License —— Go 会把整个模块静态链接进二进制，等于以那些条款分发本项目。"
    echo "- **无许可证文件同样拒绝**：没有许可证就不存在分发授权。"
    echo "- 新增依赖若带来清单外的许可证，\`make check-licenses\` 会失败。确有必要时在 \`license-exceptions.tsv\` 中登记模块与理由，该文件会随 PR 被审阅。"
    echo "- 重新生成本清单：\`bash scripts/check-licenses.sh --write docs/dependencies.md\`。"
  } >"$OUT"
  echo "check-licenses: wrote $OUT"
fi

printf 'check-licenses: %d Go modules (build closure), %d frontend packages\n' "$go_total" "$fe_total"
printf '  Go       ok=%d review=%d denied=%d no-license=%d exception=%d\n' \
  "$go_ok" "$go_review" "$go_denied" "$go_missing" "$go_exempt"
if [ "$fe_skipped" -eq 1 ]; then
  echo "  frontend NOT CHECKED (no jq or no workspace lockfile) — install jq to close the gap"
else
  printf '  frontend ok=%d review=%d denied=%d no-license=%d exception=%d\n' \
    "$fe_ok" "$fe_review" "$fe_denied" "$fe_missing" "$fe_exempt"
fi

if [ -s "$failures" ]; then
  echo "check-licenses: FAIL"
  sed 's/^/  /' "$failures"
  cat <<'EOF'
  A strong-copyleft or unlicensed dependency cannot be shipped in a binary.
  An unrecognised license must be identified by a human first. When an entry is
  genuinely acceptable, record it in license-exceptions.tsv as
  `<module-or-package><TAB><reason>` and re-run.
EOF
  exit 1
fi

echo "check-licenses: PASS"

#!/usr/bin/env bash
# test-backup-script.sh — failure matrix for the backup CronJob's inline script
# (TASK-343).
#
# The backup script lives as a YAML literal block in
# deploy/kustomize/production/backup-cronjob.yaml, runs as `/bin/sh -c` inside
# postgres:16 (Debian, i.e. dash, no `pipefail`), and writes each archive
# atomically: `<db>-<stamp>.dump.tmp` is renamed to `<db>-<stamp>.dump` only
# after pg_dump succeeded, the archive is non-empty, and it has been synced.
# Every failure branch is supposed to delete the temporary file, so a failed
# step never publishes anything under the final name (TASK-342, PR #417).
#
# Those branches have no other automated coverage: a refactor could turn
# "failed dump leaves no archive" back into "failed dump leaves a 0-byte
# *.dump" and every existing gate would stay green. This script pins the
# behaviour:
#
#   * it extracts the script FROM THE MANIFEST (rendered with `kubectl
#     kustomize` when available, otherwise the file itself) — there is no
#     second copy of the program here, so a manifest edit cannot drift away
#     from the test;
#   * it runs it with the real postgres:16 dash, not bash;
#   * it injects every failure through `PATH` shims (pg_dump/psql/pg_isready/
#     sync/mv), because the manifest runs as uid 999 and permission-based
#     injection would not be deterministic;
#   * it asserts the exit code AND the exact set of file names left in
#     /backups (a name/size check, not a bare `ls`), so a stray `.tmp`, a
#     0-byte archive, or a published archive after a failed step all fail.
#
# Requirements: docker + postgres:16 (pulled automatically when missing),
# python3 (stdlib only), kubectl or the manifest alone. No cluster, no
# kubeconfig, no PostgreSQL server, no credentials.
#
# Usage:
#   bash scripts/test-backup-script.sh
#   BACKUP_TEST_IMAGE=postgres:16 DOCKER=docker bash scripts/test-backup-script.sh
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

MANIFEST=deploy/kustomize/production/backup-cronjob.yaml
OVERLAY=deploy/kustomize/production
IMAGE=${BACKUP_TEST_IMAGE:-postgres:16}
DOCKER=${DOCKER:-docker}

# The manifest runs the Job as uid/gid 999 (`runAsUser: 999`). Running the
# matrix as the same user keeps "the script can write /backups" honest; the
# case directories are world-writable because they are host-created, while the
# failure modes are injected through shims, never through permissions.
RUN_USER=999:999

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT INT TERM

SHIM_DIR=$WORK/shim
SCRIPT_FILE=$WORK/backup-script.sh
CASE_LOG=

fail() {
  printf 'test-backup-script: FAIL: %s\n' "$1" >&2
  if [ -n "$CASE_LOG" ] && [ -f "$CASE_LOG" ]; then
    printf -- '--- container log ---\n' >&2
    cat "$CASE_LOG" >&2
    printf -- '----------------------\n' >&2
  fi
  exit 1
}

pass() { printf 'ok   %s\n' "$1"; }

# ---------------------------------------------------------------------------
# Extract the script from the manifest.
#
# The rendered overlay is preferred: it is what `kubectl apply -k` would run.
# Rendering is a local, server-side-free transform; when kubectl is missing or
# a kustomize build fails, the manifest file is read directly — the script is
# inline there and no patch rewrites it — instead of silently skipping the
# matrix.
# ---------------------------------------------------------------------------
manifest_source() {
  local err=$WORK/kustomize.err
  if command -v kubectl >/dev/null 2>&1; then
    if kubectl kustomize "$OVERLAY" >"$WORK/rendered.yaml" 2>"$err"; then
      printf '%s\n' "$WORK/rendered.yaml"
      return 0
    fi
    printf 'note: kubectl kustomize failed, reading %s directly: %s\n' \
      "$MANIFEST" "$(tr '\n' ' ' <"$err" | cut -c1-160)" >&2
  else
    printf 'note: kubectl not found, reading %s directly\n' "$MANIFEST" >&2
  fi
  printf '%s\n' "$MANIFEST"
}

# The parser is deliberately not PyYAML: the only structure needed is "the
# first container's args:[0] literal block of the CronJob document", and a
# stdlib implementation works on every runner without an extra package. It
# fails loudly (never silently empty) when the manifest shape changes.
extract_script() {
  python3 - "$1" <<'PY'
import re
import sys


def documents(text):
    cur = []
    for line in text.splitlines():
        if re.match(r'^---\s*$', line):
            yield cur
            cur = []
        else:
            cur.append(line)
    yield cur


def literal_block(lines, start):
    """Return the block scalar that starts after index `start`, or None."""
    i = start
    while i < len(lines) and not lines[i].strip():
        i += 1
    if i == len(lines):
        return None
    m = re.match(r'^(\s*)-\s+\|[-+]?\s*$', lines[i])
    if not m:
        return None
    item_indent = len(m.group(1))
    body = []
    j = i + 1
    while j < len(lines):
        line = lines[j]
        if not line.strip():
            body.append('')
            j += 1
            continue
        if len(line) - len(line.lstrip(' ')) <= item_indent:
            break
        body.append(line)
        j += 1
    content_indent = next(
        (len(l) - len(l.lstrip(' ')) for l in body if l.strip()), None)
    if content_indent is None:
        return None
    out = []
    for line in body:
        if not line.strip():
            out.append('')
        elif len(line) - len(line.lstrip(' ')) >= content_indent:
            out.append(line[content_indent:])
        else:
            return None
    return '\n'.join(out).strip('\n') + '\n'


def extract(text):
    for doc in documents(text):
        if not any(re.match(r'^\s*kind:\s*CronJob\s*$', l) for l in doc):
            continue
        containers = next(
            (i for i, l in enumerate(doc) if re.match(r'^\s*containers:\s*$', l)),
            None)
        if containers is None:
            continue
        for i in range(containers, len(doc)):
            # The raw manifest writes `args:` as a mapping key; a kustomize
            # render writes it as `- args:` because the container itself is a
            # sequence item. Both forms are followed by the `- |` literal block.
            if re.match(r'^\s*(?:-\s+)?args:\s*$', doc[i]):
                script = literal_block(doc, i + 1)
                if script:
                    return script
    return None


path = sys.argv[1]
try:
    with open(path, encoding='utf-8') as handle:
        text = handle.read()
except OSError as exc:
    sys.exit('extract-backup-script: cannot read %s: %s' % (path, exc))

script = extract(text)
if script is None:
    sys.exit('extract-backup-script: %s has no CronJob with a literal '
             'containers[0].args[0] block — update this extractor, do not '
             'copy the script into the test' % path)
for marker in ('set -eu', 'dump_database()', 'pg_dump --format=custom'):
    if marker not in script:
        sys.exit('extract-backup-script: the block extracted from %s does not '
                 'look like the backup script (missing %r)' % (path, marker))
sys.stdout.write(script)
PY
}

SOURCE_FILE=$(manifest_source)
printf 'extracting the backup script from %s\n' "$SOURCE_FILE"
extract_script "$SOURCE_FILE" >"$SCRIPT_FILE"
printf 'extracted %s lines / %s bytes\n' \
  "$(wc -l <"$SCRIPT_FILE" | tr -d ' ')" "$(wc -c <"$SCRIPT_FILE" | tr -d ' ')"

# ---------------------------------------------------------------------------
# PATH shims. Each stub is the only copy of that command name on PATH; real
# work is delegated to /bin/mv and /bin/sync where the case does not inject a
# failure. Behaviour is selected per case through SHIM_* environment variables:
#
#   SHIM_PG_DUMP_EXIT    exit status of pg_dump (default 0)
#   SHIM_PG_DUMP_BYTES   bytes written to --file= (default 4096)
#   SHIM_PSQL_EXIT       exit status of psql (default 0)
#   SHIM_PSQL_EXISTS     output of the release_notifier existence probe
#   SHIM_PSQL_TABLES     output of the base-table count probe
#   SHIM_SYNC_EXIT       exit status of sync (default 0)
#   SHIM_MV_EXIT         exit status of mv (default 0)
# ---------------------------------------------------------------------------
mkdir -p "$SHIM_DIR"

cat >"$SHIM_DIR/pg_isready" <<'SHIM'
#!/bin/sh
# The readiness wait is not what this matrix covers: report ready.
exit "${SHIM_PG_ISREADY_EXIT:-0}"
SHIM

cat >"$SHIM_DIR/pg_dump" <<'SHIM'
#!/bin/sh
# Write the requested number of bytes to --file= and exit with the injected
# status. Exiting non-zero AFTER writing bytes is the shape of a real failed
# dump (a 0-byte file or a non-empty fragment), so both cleanup paths are
# reachable without a PostgreSQL server.
file=
for arg in "$@"; do
  case "$arg" in
    --file=*) file=${arg#--file=} ;;
  esac
done
if [ -z "$file" ]; then
  echo "shim pg_dump: no --file= argument: $*" >&2
  exit 99
fi
: >"$file"
bytes=${SHIM_PG_DUMP_BYTES:-4096}
if [ "$bytes" -gt 0 ]; then
  head -c "$bytes" /dev/zero | tr '\0' 'A' >"$file"
fi
exit "${SHIM_PG_DUMP_EXIT:-0}"
SHIM

cat >"$SHIM_DIR/psql" <<'SHIM'
#!/bin/sh
# Answer the two release_notifier probes (existence, base-table count) from the
# environment; fail the whole call when the case asks for an unknown state.
status=${SHIM_PSQL_EXIT:-0}
if [ "$status" -ne 0 ]; then
  echo "shim psql: injected failure" >&2
  exit "$status"
fi
query=
for arg in "$@"; do
  case "$arg" in
    --command=*) query=${arg#--command=} ;;
  esac
done
case "$query" in
  *pg_database*) printf '%s\n' "${SHIM_PSQL_EXISTS:-}" ;;
  *information_schema.tables) printf '%s\n' "${SHIM_PSQL_TABLES:-0}" ;;
  *) echo "shim psql: unexpected query: $query" >&2; exit 99 ;;
esac
SHIM

cat >"$SHIM_DIR/sync" <<'SHIM'
#!/bin/sh
status=${SHIM_SYNC_EXIT:-0}
if [ "$status" -ne 0 ]; then
  echo "shim sync: injected failure" >&2
  exit "$status"
fi
exec /bin/sync "$@"
SHIM

cat >"$SHIM_DIR/mv" <<'SHIM'
#!/bin/sh
status=${SHIM_MV_EXIT:-0}
if [ "$status" -ne 0 ]; then
  echo "shim mv: injected failure" >&2
  exit "$status"
fi
exec /bin/mv "$@"
SHIM

chmod 0755 "$SHIM_DIR"/*
SHIM_PATH=/shim:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# ---------------------------------------------------------------------------
# Case runner and assertions.
# ---------------------------------------------------------------------------
CASE_DIR=
CASE_NAME=
RC=0

case_start() { # $1 number, $2 description
  CASE_NAME="case $1 ($2)"
  CASE_DIR=$WORK/case$1
  CASE_LOG=$WORK/case$1.log
  rm -rf "$CASE_DIR"
  mkdir -p "$CASE_DIR"
  chmod 0777 "$CASE_DIR"
}

run_backup() { # extra `-e K=V` arguments for the shims
  RC=0
  "$DOCKER" run --rm --user "$RUN_USER" --entrypoint /bin/sh \
    -v "$SHIM_DIR:/shim:ro" \
    -v "$CASE_DIR:/backups" \
    -e "PATH=$SHIM_PATH" \
    -e PGHOST=postgres -e PGPORT=5432 \
    -e POSTGRES_DB=release_manager -e POSTGRES_USER=postgres \
    -e PGPASSWORD=not-used \
    "$@" \
    "$IMAGE" -c "$(cat "$SCRIPT_FILE")" >"$CASE_LOG" 2>&1 || RC=$?
  # `docker run` exits 125 when the CLI/daemon itself refused the run (image
  # absent, bad mount, ...). The failure cases expect a non-zero status and an
  # empty /backups, so a refused run would otherwise look exactly like a passing
  # failure-injection case. Treat it as a test-harness error, never as evidence.
  [ "$RC" -ne 125 ] || fail "$CASE_NAME: docker run itself failed (exit 125) — is $IMAGE available?"
}

list_names() {
  find "$CASE_DIR" -mindepth 1 -maxdepth 1 -type f -printf '%f\n' \
    | sed -E 's/^release_manager-[0-9]{8}T[0-9]{6}Z\.dump$/release_manager-<stamp>.dump/' \
    | sort
}

expect_rc_zero() {
  [ "$RC" -eq 0 ] || fail "$CASE_NAME: expected exit 0, got $RC"
}

expect_rc_nonzero() {
  [ "$RC" -ne 0 ] || fail "$CASE_NAME: expected a non-zero exit, got 0"
}

expect_names() { # expected file names (canonicalised); none means an empty dir
  local expected actual
  if [ "$#" -eq 0 ]; then
    expected=
  else
    expected=$(printf '%s\n' "$@" | sort)
  fi
  actual=$(list_names)
  [ "$actual" = "$expected" ] || fail "$CASE_NAME: /backups file set mismatch
--- expected ---
${expected}
--- actual ---
${actual}"
}

expect_log() { # fixed string the container log must contain
  grep -qF -- "$1" "$CASE_LOG" \
    || fail "$CASE_NAME: expected the log to contain: $1"
}

# ---------------------------------------------------------------------------
# The matrix.
# ---------------------------------------------------------------------------

# 1. pg_dump fails and leaves a 0-byte temporary file — the shape a wrong
#    password takes in the real cluster before TASK-342.
case_start 1 'pg_dump fails, 0-byte tmp left behind'
run_backup -e SHIM_PG_DUMP_EXIT=1 -e SHIM_PG_DUMP_BYTES=0
expect_rc_nonzero
expect_names
expect_log 'pg_dump failed for release_manager'
pass "$CASE_NAME"

# 2. pg_dump fails after writing a non-empty fragment: the fragment must not
#    be published, not even under the temporary name.
case_start 2 'pg_dump fails, non-empty fragment left behind'
run_backup -e SHIM_PG_DUMP_EXIT=1 -e SHIM_PG_DUMP_BYTES=4096
expect_rc_nonzero
expect_names
expect_log 'pg_dump failed for release_manager'
pass "$CASE_NAME"

# 3. pg_dump exits 0 but wrote nothing: `[ ! -s ]` is the only thing between a
#    0-byte file and a *.dump that looks like a backup.
case_start 3 'pg_dump exits 0 with a 0-byte archive'
run_backup -e SHIM_PG_DUMP_BYTES=0
expect_rc_nonzero
expect_names
expect_log 'pg_dump produced an empty archive for release_manager'
pass "$CASE_NAME"

# 4. sync fails: an archive whose bytes may not be on disk must not be renamed.
case_start 4 'sync fails'
run_backup -e SHIM_SYNC_EXIT=1
expect_rc_nonzero
expect_names
expect_log 'sync failed for release_manager'
pass "$CASE_NAME"

# 5. mv fails: the temporary file stays unpublished and gets cleaned up.
case_start 5 'mv fails'
run_backup -e SHIM_MV_EXIT=1
expect_rc_nonzero
expect_names
expect_log 'failed to rename'
pass "$CASE_NAME"

# 6. The happy path: exactly one release_manager-<stamp>.dump, non-empty, and
#    no *.tmp anywhere (release_notifier is absent, so it is skipped).
case_start 6 'success'
run_backup
expect_rc_zero
expect_names 'release_manager-<stamp>.dump'
dump=$(find "$CASE_DIR" -mindepth 1 -maxdepth 1 -type f \
  -name 'release_manager-*.dump' -print -quit)
[ -n "$dump" ] && [ -s "$dump" ] \
  || fail "$CASE_NAME: the published archive is missing or empty"
pass "$CASE_NAME"

# 7. Orphaned temp-file cleanup: a *.dump.tmp older than a day is removed by
#    the trailing `find -mtime +1 -delete`, while a fresh one and a valid
#    archive from an earlier run survive (retention only drops >7 days).
case_start 7 'orphaned *.tmp cleanup'
printf 'old orphan' >"$CASE_DIR/orphan-old.dump.tmp"
touch -d '3 days ago' "$CASE_DIR/orphan-old.dump.tmp"
printf 'fresh orphan' >"$CASE_DIR/orphan-fresh.dump.tmp"
printf 'earlier archive' >"$CASE_DIR/release_notifier-20250101T000000Z.dump"
touch -d '2 days ago' "$CASE_DIR/release_notifier-20250101T000000Z.dump"
run_backup
expect_rc_zero
expect_names 'release_manager-<stamp>.dump' \
  'release_notifier-20250101T000000Z.dump' 'orphan-fresh.dump.tmp'
[ ! -e "$CASE_DIR/orphan-old.dump.tmp" ] \
  || fail "$CASE_NAME: the 3-day-old orphan survived"
pass "$CASE_NAME"

# 8. The release_notifier probe fails (unknown state): the Job fails as a
#    whole, the already-published release_manager archive from this run stays
#    (documented per-database guarantee), and no notifier dump is produced.
case_start 8 'release_notifier probe fails'
run_backup -e SHIM_PSQL_EXIT=1
expect_rc_nonzero
expect_names 'release_manager-<stamp>.dump'
expect_log 'psql failed while probing release_notifier'
pass "$CASE_NAME"

printf '\ntest-backup-script: PASS — 8 cases\n'

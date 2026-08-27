#!/usr/bin/env bash
# lock.sh — environment flock with holder tracking.
#
# The lock file doubles as a stage record: after acquiring the lock the
# caller writes operation/PID/started_at into dev-stage.json, and a
# conflicting acquirer reads that record to report the holder (REQ-065:
# stderr must include the holder PID and started_at). The stage record is
# intentionally left in place on release — the next acquirer overwrites it,
# and stale records are harmless because they are only read when the flock
# itself is held.
set -euo pipefail

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=lib/errors.sh
source "$SCRIPT_DIR/lib/errors.sh"

DEV_DATA_DIR="${DEV_DATA_DIR:-$SCRIPT_DIR/../../data}"
DEV_LOCK_FILE="${DEV_LOCK_FILE:-$DEV_DATA_DIR/dev.lock}"
DEV_STAGE_FILE="${DEV_STAGE_FILE:-$DEV_DATA_DIR/dev-stage.json}"

# ensure_lock_file — create the data dir and lock file once.
ensure_lock_file() {
  mkdir -p "$DEV_DATA_DIR"
  if [ ! -f "$DEV_LOCK_FILE" ]; then
    : > "$DEV_LOCK_FILE"
  fi
}

# holder_record — print the JSON stage record of the current lock holder.
holder_record() {
  if [ -f "$DEV_STAGE_FILE" ]; then
    cat "$DEV_STAGE_FILE"
  else
    printf '{}'
  fi
}

# acquire_lock <operation> [shared] — non-blocking flock acquisition.
#   shared: dev-status uses LOCK_SH; all lifecycle operations use LOCK_EX.
# On conflict: prints `environment_locked: ...` with the holder PID and
# started_at, and exits 3 (AC-065-11).
acquire_lock() {
  local operation="$1"
  local mode="${2:-exclusive}"
  local lock_args=(-n)
  if [ "$mode" = "shared" ]; then
    lock_args+=(-s)
  fi

  ensure_lock_file
  # Open the lock file on fd 9; the flock applies to that descriptor and is
  # released automatically when the process (and its children) exit.
  exec 9>"$DEV_LOCK_FILE"
  if ! flock "${lock_args[@]}" 9; then
    local holder
    holder="$(holder_record)"
    local holder_pid holder_started
    holder_pid="$(printf '%s' "$holder" | sed -nE 's/.*"pid"[[:space:]]*:[[:space:]]*([0-9]+).*/\1/p')"
    holder_started="$(printf '%s' "$holder" | sed -nE 's/.*"started_at"[[:space:]]*:[[:space:]]*"([^"]*)".*/\1/p')"
    printf '%s: environment is locked by another dev operation (pid=%s started_at=%s)\n' \
      "$ERR_ENVIRONMENT_LOCKED" "${holder_pid:-unknown}" "${holder_started:-unknown}" >&2
    exit "$EXIT_ENVIRONMENT_LOCKED"
  fi

  # Record the holder for conflicting acquirers (AC-065-11).
  printf '{"operation":"%s","pid":%s,"started_at":"%s"}\n' \
    "$operation" "$$" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$DEV_STAGE_FILE"
}

# release_lock — called by trap handlers. Does not clear the stage record
# (see file header); closing fd 9 releases the flock.
release_lock() {
  flock -u 9 2>/dev/null || true
  # NOTE: no `2>/dev/null` here — `exec` redirections are permanent for the
  # shell, so `exec 9>&- 2>/dev/null` would silently redirect every later
  # stderr write in the trap chain (dev_purge_failed residual manifest,
  # diagnostics summary) into /dev/null. Closing an already-closed fd is a
  # silent no-op in bash, so no error suppression is needed.
  exec 9>&- || true
}

#!/usr/bin/env bash
# errors.sh — stable error codes and exit-code mapping for the dev environment.
#
# Every failure is reported as `CODE: message` on stderr with the process
# exit code derived from the failure class (REQ-065 exit code table):
#   0 success, 1 operation failure, 2 confirmation required, 3 environment locked.
set -euo pipefail

# Guard against double sourcing: dev.sh sources every lib, and libs source
# errors.sh themselves — the readonly declarations must run exactly once.
if [ "${RM_DEV_ERRORS_SOURCED:-}" = "1" ]; then
  return 0 2>/dev/null || true
fi
RM_DEV_ERRORS_SOURCED=1


readonly ERR_K3D_UNAVAILABLE="k3d_unavailable"
readonly ERR_DOCKER_UNAVAILABLE="docker_unavailable"
readonly ERR_PORT_CONFLICT="port_conflict"
readonly ERR_DOCKER_BUILD_FAILED="docker_build_failed"
readonly ERR_DOCKER_PUSH_FAILED="docker_push_failed"
readonly ERR_REGISTRY_UNREACHABLE="registry_unreachable"
readonly ERR_KUSTOMIZE_BUILD_FAILED="kustomize_build_failed"
readonly ERR_CLUSTER_CREATE_FAILED="cluster_create_failed"
readonly ERR_SERVICE_UNHEALTHY="service_unhealthy"
readonly ERR_OPERATOR_NOT_ONLINE="operator_not_online"
readonly ERR_SEED_WRITE_FAILED="seed_write_failed"
readonly ERR_ENVIRONMENT_LOCKED="environment_locked"
readonly ERR_CONFIRM_REQUIRED="confirm_required"
readonly ERR_HOST_MEMORY_INSUFFICIENT="host_memory_insufficient"
readonly ERR_HOST_DISK_INSUFFICIENT="host_disk_insufficient"
readonly ERR_RESOURCE_CONFLICT="resource_conflict"
readonly ERR_DEV_PURGE_CONFIRM_REQUIRED="dev_purge_confirm_required"
readonly ERR_FIXTURE_CONFLICT="fixture_conflict"
readonly ERR_DEV_PURGE_FAILED="dev_purge_failed"
readonly ERR_E2E_RUN_ID_INVALID="e2e_run_id_invalid"

# Exit codes (REQ-065).
readonly EXIT_OK=0
readonly EXIT_FAILURE=1
readonly EXIT_CONFIRM_REQUIRED=2
readonly EXIT_ENVIRONMENT_LOCKED=3

# fail <error-code> <message...> — print `CODE: message` to stderr and exit.
# The exit code is derived from the error class: confirmation gates exit 2,
# lock conflicts exit 3, everything else exits 1.
fail() {
  local code="$1"
  shift
  printf '%s: %s\n' "$code" "$*" >&2
  case "$code" in
    "$ERR_CONFIRM_REQUIRED" | "$ERR_DEV_PURGE_CONFIRM_REQUIRED")
      exit "$EXIT_CONFIRM_REQUIRED"
      ;;
    "$ERR_ENVIRONMENT_LOCKED")
      exit "$EXIT_ENVIRONMENT_LOCKED"
      ;;
    *)
      exit "$EXIT_FAILURE"
      ;;
  esac
}

# exit_code_for <error-code> — pure mapping used by callers that must not exit
# immediately (e.g. trap handlers preserving the primary error code).
exit_code_for() {
  case "$1" in
    "$ERR_CONFIRM_REQUIRED" | "$ERR_DEV_PURGE_CONFIRM_REQUIRED") printf '%s' "$EXIT_CONFIRM_REQUIRED" ;;
    "$ERR_ENVIRONMENT_LOCKED") printf '%s' "$EXIT_ENVIRONMENT_LOCKED" ;;
    *) printf '%s' "$EXIT_FAILURE" ;;
  esac
}

# require_confirm <error-code> — enforce the CONFIRM=1 gate for destructive
# operations. Uses the operation-specific code so dev-purge reports its own
# code (AC-065-23) while dev-reset-data reports confirm_required (AC-065-05).
require_confirm() {
  local code="$1"
  if [ "${CONFIRM:-}" != "1" ]; then
    fail "$code" "set CONFIRM=1 to proceed"
  fi
}

# require_command <command> <error-code> <hint> — fail when a CLI binary is
# missing from PATH.
require_command() {
  local command="$1"
  local code="$2"
  local hint="$3"
  if ! command -v "$command" >/dev/null 2>&1; then
    fail "$code" "$hint"
  fi
}

#!/usr/bin/env bash
# ownership.sh — managed-resource ownership manifest (dev-ownership.json).
#
# REQ-065 Docker ownership contract:
#   - every managed container/network carries labels
#     io.release-manager.dev.managed=true and io.release-manager.dev.profile=<p>
#   - a same-named resource WITHOUT the managed label is a resource_conflict
#   - dev-down deletes only k3d_clusters[] entries; dev-purge deletes every
#     resource listed in the manifest (registry included)
# The manifest is the single deletion whitelist — nothing outside it is ever
# removed by the lifecycle module.
set -euo pipefail

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=lib/errors.sh
source "$SCRIPT_DIR/lib/errors.sh"

DEV_DATA_DIR="${DEV_DATA_DIR:-$SCRIPT_DIR/../../data}"
OWNERSHIP_FILE="${OWNERSHIP_FILE:-$DEV_DATA_DIR/dev-ownership.json}"
MANAGED_LABEL="io.release-manager.dev.managed"
PROFILE_LABEL="io.release-manager.dev.profile"
FIXTURE_VERSION="${FIXTURE_VERSION:-v2}"

# ownership_init — create an empty manifest for the active profile. Never
# overwrites an existing manifest (the previous profile's resources are the
# previous owner's responsibility).
ownership_init() {
  mkdir -p "$DEV_DATA_DIR"
  if [ ! -f "$OWNERSHIP_FILE" ]; then
    printf '{"profile":"%s","created_at":"%s","fixture_version":"%s","k3d_clusters":[],"docker_containers":[],"docker_networks":[]}\n' \
      "${DEV_PROFILE:-local}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$FIXTURE_VERSION" > "$OWNERSHIP_FILE"
  fi
}

# ownership_read — print the raw manifest (empty object when absent).
ownership_read() {
  if [ -f "$OWNERSHIP_FILE" ]; then
    cat "$OWNERSHIP_FILE"
  else
    printf '{}'
  fi
}

# ownership_contains <json-array-key> <name> — 1 when name is in the array.
ownership_contains() {
  local key="$1"
  local name="$2"
  ownership_read | grep -q "\"$name\"" && [ "$(ownership_read | sed -nE "s/.*\"$key\"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p" | grep -c "\"$name\"")" -ge 1 ]
}

# ownership_add <json-array-key> <name> — append idempotently. The manifest
# is single-line JSON with flat string arrays; the array body is extracted,
# rebuilt, and spliced back so empty arrays stay valid JSON.
ownership_add() {
  local key="$1"
  local name="$2"
  local manifest rest new_array
  manifest="$(ownership_read)"
  if ownership_contains "$key" "$name"; then
    return 0
  fi
  rest="$(printf '%s' "$manifest" | sed -nE "s/.*\"$key\"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p")"
  if [ -z "$rest" ] || [ -z "$(printf '%s' "$rest" | tr -d '[:space:]')" ]; then
    new_array="\"$name\""
  else
    new_array="${rest}, \"$name\""
  fi
  # sed -n suppresses auto-print, so the write-back must NOT use -n: the
  # substitution result is the only line and would be dropped silently
  # (observed: ownership manifest truncated to zero bytes).
  printf '%s\n' "$manifest" | sed -E "s/(\"$key\"[[:space:]]*:[[:space:]]*)\[[^]]*\]/\1[$new_array]/" > "$OWNERSHIP_FILE"
}

# ownership_remove <json-array-key> <name> — remove one entry.
ownership_remove() {
  local key="$1"
  local name="$2"
  local manifest rest new_array
  manifest="$(ownership_read)"
  if ! ownership_contains "$key" "$name"; then
    return 0
  fi
  rest="$(printf '%s' "$manifest" | sed -nE "s/.*\"$key\"[[:space:]]*:[[:space:]]*\[([^]]*)\].*/\1/p")"
  # Remove the exact quoted name plus a neighbouring comma, then normalize.
  new_array="$(printf '%s' "$rest" | sed -E "s/\"$name\"[[:space:]]*,[[:space:]]*//; s/[[:space:]]*\"$name\"//; s/^[[:space:]]*,[[:space:]]*//; s/[[:space:]]*,[[:space:]]*$//")"
  printf '%s\n' "$manifest" | sed -E "s/(\"$key\"[[:space:]]*:[[:space:]]*)\[[^]]*\]/\1[$new_array]/" > "$OWNERSHIP_FILE"
}


# docker_managed <type> <name> <label-filter...> — check that a Docker object
# carries the managed label. `type` is container or network (explicit object
# typing is a project anti-pattern guard — no untyped docker inspect).
docker_managed() {
  local type="$1"
  local name="$2"
  shift 2
  docker "$type" inspect --format '{{ index .Config.Labels "io.release-manager.dev.managed" }}' "$name" 2>/dev/null | grep -q '^true$'
}

# docker_profile <type> <name> — print the profile label value.
docker_profile() {
  local type="$1"
  local name="$2"
  docker "$type" inspect --format '{{ index .Config.Labels "io.release-manager.dev.profile" }}' "$name" 2>/dev/null || true
}

# require_no_conflict <type> <name> — fail resource_conflict when a
# same-named object exists without the managed label (AC-065-22).
require_no_conflict() {
  local type="$1"
  local name="$2"
  if docker "$type" inspect "$name" >/dev/null 2>&1; then
    if ! docker_managed "$type" "$name"; then
      fail "$ERR_RESOURCE_CONFLICT" \
        "$type '$name' exists without label $MANAGED_LABEL=true; remove or label it manually, then retry"
    fi
  fi
}

# registry_managed_name — the k3d-managed registry container name.
registry_managed_name() {
  printf 'k3d-release-manager-registry'
}

# cluster_managed_name <cluster> — the k3d-managed network for a cluster.
cluster_network_name() {
  printf 'k3d-%s' "$1"
}

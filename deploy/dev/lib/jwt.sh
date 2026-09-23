#!/usr/bin/env bash
# jwt.sh — JWT header inspection + downgrade probe for the dev smoke.
#
# REQ-065 AC-065-01 / TASK-065 D1=A: the management-plane JWT is EdDSA
# (Ed25519). That is a *contract*, so the smoke must assert it instead of
# leaving it to manual observation — the access-token length only differs
# (450 vs 407 under the old HS256) and nothing else in the smoke would fail if
# auth were reverted to symmetric signing.
#
# Reading the algorithm needs no secret and no crypto library: base64url-decode
# the header segment and read its `alg` claim. The downgrade probe builds a
# well-formed HS256 token with the legacy default secret, which the service
# must refuse.
#
# deploy/dev/dev_test.go unit-tests these helpers against crafted EdDSA/HS256
# tokens, so the assertion cannot silently lose its teeth.
set -euo pipefail

# b64url_decode — decode base64url (unpadded) from stdin-argument to stdout.
b64url_decode() {
  local data="$1"
  [ -n "$data" ] || return 0
  # base64url uses '-'/'_' and drops padding; GNU base64 wants '+'/'=' padding.
  data="$(printf '%s' "$data" | tr -- '_-' '/+')"
  case $((${#data} % 4)) in
    2) data="${data}==" ;;
    3) data="${data}=" ;;
  esac
  printf '%s' "$data" | base64 -d 2>/dev/null || true
}

# jwt_alg <token> — print the `alg` claim of a JWT header. Prints nothing for a
# malformed token or a header without `alg`; callers must treat empty as
# "not EdDSA" rather than as a pass.
jwt_alg() {
  local token="$1" header
  # A compact JWT has exactly three dot-separated segments.
  case "$token" in
    *.*.*) ;;
    *) return 0 ;;
  esac
  header="$(b64url_decode "${token%%.*}")"
  [ -n "$header" ] || return 0
  printf '%s' "$header" \
    | sed -nE 's/.*"alg"[[:space:]]*:[[:space:]]*"([^"]*)".*/\1/p'
}

# jwt_signing_input <token> — the header.payload prefix a signature covers.
jwt_signing_input() {
  local token="$1"
  case "$token" in
    *.*.*) printf '%s' "${token%.*}" ;;
    *) printf '%s' "" ;;
  esac
}

# jwt_hs256_token <secret> [claims-json] — build a well-formed HS256 JWT signed
# with <secret>. This is the *downgrade probe*: signing with the legacy default
# secret ("change-me-in-production", the pre-Ed25519 cmd/auth flag default) is
# exactly what an attacker — or an accidental revert to HMAC signing — would
# present. The smoke asserts the service refuses it.
#
# Requires openssl (HMAC-SHA256); callers must skip the probe when it is absent
# instead of recording a pass.
jwt_hs256_token() {
  local secret="$1" claims="${2:-{\"sub\":\"downgrade-probe\"}}"
  local header='{"alg":"HS256","typ":"JWT"}'
  local head_b64 payload_b64 signing sig
  head_b64="$(printf '%s' "$header" | base64 | tr -d '\n' | tr -- '+/' '-_' | tr -d '=')"
  payload_b64="$(printf '%s' "$claims" | base64 | tr -d '\n' | tr -- '+/' '-_' | tr -d '=')"
  signing="$head_b64.$payload_b64"
  sig="$(printf '%s' "$signing" | openssl dgst -sha256 -hmac "$secret" -binary \
    | base64 | tr -d '\n' | tr -- '+/' '-_' | tr -d '=')"
  printf '%s.%s' "$signing" "$sig"
}

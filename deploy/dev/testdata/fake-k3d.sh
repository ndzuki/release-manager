#!/usr/bin/env bash
# fake-k3d.sh — stateful k3d shim for deploy/dev tests (dev_test.go).
#
# `cluster list` reflects clusters recorded in $STATE/clusters.txt;
# `cluster create` appends the cluster and logs one line per invocation
# (`<name>|<argv>`) to $STATE/k3d-creates.log; `cluster delete` removes;
# `registry list/create` report/manage the registry; `kubeconfig get/merge`
# write minimal outputs. Tests run dev.sh up/down end-to-end without Docker
# or k3d on the host.
set -euo pipefail

STATE="${FAKE_K3D_STATE:?FAKE_K3D_STATE is required}"

case "$1" in
  version)
    printf 'k3d version v5.8.3 k3s1\n'
    ;;
  cluster)
    case "$2" in
      list)
        if [ -f "$STATE/clusters.txt" ]; then
          printf '['
          first=1
          while IFS= read -r c; do
            [ -n "$c" ] || continue
            [ "$first" -eq 1 ] || printf ','
            printf '{"name":"%s"}' "$c"
            first=0
          done < "$STATE/clusters.txt"
          printf ']\n'
        else
          printf '[]\n'
        fi
        ;;
      create)
        shift 2
        # One line per create invocation: name followed by the full argv.
        argv="$*"
        name=""
        while [ "$#" -gt 0 ]; do
          if [ "$1" = "--name" ]; then name="$2"; shift 2; continue; fi
          # Real k3d v5 takes the cluster name positionally: `k3d cluster
          # create <name> [flags]`. Fall back to the first positional arg
          # (the name precedes any flag values in dev.sh's invocation).
          if [ -z "$name" ] && [ "$1" != "--" ]; then name="$1"; fi
          shift
        done
        printf '%s|%s\n' "$name" "$argv" >> "$STATE/k3d-creates.log"
        printf '%s\n' "$name" >> "$STATE/clusters.txt"
        ;;
      delete)
        shift 2
        name=""
        while [ "$#" -gt 0 ]; do
          if [ "$1" = "--name" ]; then name="$2"; shift 2; continue; fi
          # Real k3d accepts the cluster name positionally: `k3d cluster
          # delete <name>`. Fall back to the positional arg when no --name.
          if [ -z "$name" ] && [ "$1" != "--" ]; then name="$1"; fi
          shift
        done
        [ -n "$name" ] || exit 0
        [ -f "$STATE/clusters.txt" ] || exit 0
        grep -v "^${name}$" "$STATE/clusters.txt" > "$STATE/clusters.tmp" || true
        mv "$STATE/clusters.tmp" "$STATE/clusters.txt"
        ;;
    esac
    ;;
  registry)
    case "$2" in
      list)
        printf 'release-manager-registry\n'
        ;;
      create)
        printf '%s\n' "registry create" >> "$STATE/k3d-creates.log"
        ;;
    esac
    ;;
  kubeconfig)
    case "$2" in
      merge)
        shift 2
        out=""
        while [ "$#" -gt 0 ]; do
          if [ "$1" = "-o" ]; then out="$2"; fi
          shift
        done
        printf 'apiVersion: v1\nkind: Config\n' > "$out"
        ;;
      get)
        printf 'apiVersion: v1\nkind: Config\n'
        ;;
    esac
    ;;
esac
exit 0

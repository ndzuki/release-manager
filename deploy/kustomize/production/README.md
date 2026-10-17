# Production overlay (TASK-337 batch 1)

Kustomize overlay for a single-cluster production deployment of the
management plane. It turns the dev-shaped manifests under
`deploy/kustomize/{base,services,postgres,redis}` into a persisted,
ingress-isolated deployment: the PostgreSQL and Redis bare Deployments become
StatefulSets with PVCs, web runs two replicas, the four request-serving
components get PodDisruptionBudgets, and a default-deny NetworkPolicy replaces
the implicit "everything can reach everything" model.

## Render and verify (no cluster writes)

```bash
kubectl kustomize deploy/kustomize/production
kubectl apply --dry-run=client -k deploy/kustomize/production
```

`kubectl kustomize` is a pure local transform. `--dry-run=client` still asks the
current kubeconfig for API discovery, so point `KUBECONFIG` at the target
cluster (or at any cluster that knows the built-in API groups) before running
it.

The render is 39 objects: 1 Namespace, 7 ConfigMaps, 6 Deployments, 2
StatefulSets, 4 PodDisruptionBudgets, 9 Services, 4 PVCs (2 standalone + 2 from
the StatefulSet claim templates) and 8 NetworkPolicies — the default-deny one
plus 7 allow rules, including `allow-kubelet-probes`.

Apply only after the Secrets in the next section exist and the image tag is set
(see [Image tags](#image-tags)); the image gate is the last local check:

```bash
make check-production-images   # fails while the committed ':dev' default is in effect
kubectl apply -k deploy/kustomize/production
```

## Required Secrets (provisioned out of band, TASK-337 batch 4)

The overlay deliberately does not create these. The dev overlay's plaintext
credentials Secret and its namespace are deleted by the overlay's patches, so a
missing production Secret fails closed instead of booting on shared dev
credentials.

| Secret | Keys | Consumed by |
| --- | --- | --- |
| `release-manager-credentials` | `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`, `DATABASE_DSN`, `NOTIFIER_DATABASE_DSN` | PostgreSQL (`envFrom`), auth/orchestrator (`DATABASE_DSN`), notifier (`DATABASE_DSN`, sourced from the `NOTIFIER_DATABASE_DSN` key) |
| `release-manager-jwt-private` | `JWT_PRIVATE_KEY` | auth |
| `release-manager-jwt-public` | `JWT_PUBLIC_KEY` | api, orchestrator |
| `release-manager-mtls-ca` | `ca.key`, `ca.crt` | orchestrator agent gateway |
| `release-manager-webhook-service-token` | `WEBHOOK_SERVICE_TOKEN` (optional `WEBHOOK_SERVICE_TOKEN_PREVIOUS`) | webhook (sender), orchestrator (verifier) |
| `release-manager-ci-api-key` | `CI_API_KEY` | webhook CI ingress |
| `release-manager-harbor-service-token` | `HARBOR_SERVICE_TOKEN` (optional `HARBOR_SERVICE_TOKEN_PREVIOUS`) | webhook, orchestrator |
| `release-manager-notifier-service-token` | `NOTIFIER_SERVICE_TOKEN` (optional `NOTIFIER_SERVICE_TOKEN_PREVIOUS`) | notifier |

`DATABASE_DSN` / `NOTIFIER_DATABASE_DSN` must be complete `postgres://` URLs.
The notifier's Deployment exposes the `NOTIFIER_DATABASE_DSN` key under the
`DATABASE_DSN` environment variable, because that is the only database DSN
name `internal/config` binds.
`init.sql` creates the notifier's separate database on the first PostgreSQL
boot, so `NOTIFIER_DATABASE_DSN` should point at `release_notifier`.

The service manifests still read some credentials through environment variable
names prefixed with `DEV_` (for example `DEV_WEBHOOK_SERVICE_TOKEN`). Those are
code-level identifiers in `cmd/*` and `internal/auth`, not dev-only values;
renaming them is a code change outside this batch. Their values come from the
production Secrets above.

## Image tags

Kustomize does not expand shell environment variables. The committed
`images.newTag` default is `dev`, which keeps `kubectl kustomize`
self-contained; the release render must rewrite it to the immutable image tag
published by TASK-337 batch 2:

```bash
IMAGE_TAG=$(git rev-parse --short=12 HEAD)
for svc in release-api release-auth release-orchestrator release-webhook \
           release-notifier release-web; do
  kustomize edit set image \
    localhost:5001/$svc=localhost:5001/$svc:$IMAGE_TAG
done
```

Never apply the `dev` tags to production. `kustomize edit` rewrites this
directory's `kustomization.yaml` in place; commit only the `dev` defaults.

The check is mechanical, not a matter of remembering: run
`make check-production-images` before `kubectl apply`. It renders the overlay
locally (no cluster contact) and exits non-zero, printing the offending lines,
if any `image:` value still ends in `:dev`. It passes only after
`kustomize edit set image` has rewritten all six tags.

## Storage class

`production-settings.yaml` holds the single `storageClassName` value used by
all four claims: the PostgreSQL 10Gi and Redis 5Gi volume claim templates, the
release-api 1Gi PVC and the orchestrator 1Gi PVC inherited from `base`. Change
that one value before rendering for a cluster whose provisioner is not
`local-path` (editing the applied ConfigMap does not resize or rebind an
existing PVC). The inherited orchestrator claim ships without a class, so the
replacement writes the field into it (`create: true`) rather than leaving that
one claim on the cluster default — one overlay must not span two storage
classes. `local-path` volumes are node-bound: this overlay targets the
single-node k3d production cluster; a multi-node cluster needs a network volume
class.

## Database migrations

`migrations/` is the schema authority. The explicit migration step (also used
by first-boot and restore runbooks) is:

```bash
make migrate DATABASE_DSN='postgres://release_manager:...@postgres:5432/release_manager?sslmode=disable'
```

## Runbook notes (read before operating)

- **`make migrate` (CLI) is not equivalent to the service's startup
  migration.** The service path in `internal/postgres` first calls
  `repairHistoricalMigrationVersion()`, which rewinds `schema_migrations` to
  replay the idempotent migrations that independently developed branches
  reused on versions 7/8; the golang-migrate CLI does not. On a database with
  pre-existing history (a restored dump, a long-lived dev database) start the
  service once and let its startup migration repair and move the version, then
  use `make migrate` for subsequent explicit steps. On a fresh database both
  paths converge.
- **PDB drain trap.** api/auth/orchestrator run one replica with
  `minAvailable: 1`, so `kubectl drain <node>` on a node hosting them blocks
  forever on voluntary eviction. That is deliberate (a second replica has not
  been proven safe), not a bug to work around by deleting the PDB. Plan for
  `--disable-eviction` with the resulting downtime, or scale the service to two
  replicas first and let the PDB hold one available.

## Deliberate exclusions

- `notification-sink` is a dev-only local webhook receiver; it is deleted by
  the overlay and the notifier ships an empty egress allowlist.
- The dev service configs (debug logging, SQLite, disabled Secure cookies,
  raised login rate limit) are replaced by `configs/*.production.yaml`.
- NodePorts 30082-30088 are removed; every Service is ClusterIP and only the
  Ingress controller may enter the namespace.
- The inherited orchestrator PVC remains 1Gi. Growing it is a storage-class
  and capacity decision for a later batch.

## Known gaps (not fixed in batch 1)

- `release-api` still opens a SQLite store (`cmd/api`), so its audit reads do
  not see the PostgreSQL data written by auth/orchestrator. The PVC only keeps
  that SQLite file durable. Fixing the engine split needs a code change.
- Only `web` has two replicas. api/auth/orchestrator keep one replica, so their
  `minAvailable: 1` PDB blocks voluntary eviction until multi-replica safety is
  proven (startup migrations, gateway CA, background workers).
- The NetworkPolicy assumes the ingress controller runs in `kube-system`
  (k3s Traefik) or `ingress-nginx`. A host-network ingress controller may
  present a node address instead; re-check this when TASK-337 batch 3 wires
  the Ingress.
- Kubelet probes are **explicitly allowed** (not a gap left to fix):
  `allow-kubelet-probes` admits `nodeCidr` (`10.42.0.0/16` by default, the k3s
  Pod CIDR) to ports 8082-8088. It is required because k3s' default kube-router
  evaluates NetworkPolicies in the filter INPUT chain: kubelet -> pod probe
  packets from the node traverse that chain, hit `default-deny-ingress` and are
  dropped, so without this rule every application pod would stay non-Ready
  (k3s issue #10030). Set `nodeCidr` in `production-settings.yaml` to the
  cluster's Pod CIDR when it is not `10.42.0.0/16`. A real cluster still has to
  confirm the rule for a host-network ingress controller or a non-k3s CNI that
  does not evaluate policies on the node path.
- Redis has no AUTH; it is reachable only from pods in this namespace by
  NetworkPolicy. Add AUTH in the hardening batch if Redis leaves that boundary.
- No backup/restore CronJob and no Prometheus scrape configuration yet
  (TASK-337 batch 4).
- Ingress, TLS and the self-signed CA are TASK-337 batch 3; until then
  `secure_cookies: true` (the auth default) means the console will not keep a
  session over plain HTTP.

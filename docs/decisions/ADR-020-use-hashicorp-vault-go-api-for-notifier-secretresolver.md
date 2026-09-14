# ADR-020：Use HashiCorp Vault Go API for notifier SecretResolver

- Status: accepted
- Date: created 2026-08-05 / updated 2026-08-05
- Scope: project
- Related: REQ-031 (Requirements); TASK-031 (Tasks)

## Status
accepted

## Context
The notifier optionally authenticates outbound webhook delivery with a bearer secret. ADR-007 requires secret material to stay out of persisted jobs, API payloads, errors, and logs. REQ-002 selects HashiCorp Vault for production secret management, but the notifier currently has only a consumer-side SecretResolver seam and no production adapter or authentication lifecycle. A concrete SDK choice is required so reads honor context cancellation, Kubernetes workload identity, Vault token renewal, and KV v2 path semantics without introducing plaintext configuration fallbacks.

## Decision
Use the official HashiCorp Vault Go API module github.com/hashicorp/vault/api for the notifier production SecretResolver adapter. The adapter authenticates with the Vault Kubernetes auth method using the notifier service-account JWT, reads the configured KV v2 mount/path/key with the caller context, and returns only the selected string value through the existing narrow SecretResolver interface. Vault address, namespace, auth mount, role, service-account token path, KV mount, secret path, and key are references/configuration only; secret values must never enter application configuration, notification_jobs, outbox payloads, last_error, logs, metrics, or traces. Missing or invalid Vault configuration fails closed when the resolver is enabled. When no resolver is configured, webhook delivery remains intentionally unauthenticated as specified by REQ-031.

## Alternatives Considered
- Use direct net/http calls to Vault: rejected because it would duplicate authentication, token lifecycle, request construction, error handling, and API compatibility already provided by the official client.
- Read a static bearer token from YAML or environment variables: rejected because it creates a plaintext secret fallback and violates ADR-007.
- Use Vault Agent file injection as the only integration: rejected because it turns secret material into a filesystem artifact and removes context-aware per-delivery resolution from the notifier seam.
- Add a generic multi-provider secret manager abstraction now: rejected because REQ-002 already selects Vault and a speculative abstraction would widen the interface without a second adapter.

## Consequences
The notifier gains a supported, context-aware production adapter and Kubernetes-authenticated secret reads while keeping callers dependent only on SecretResolver. A new MPL-2.0 dependency and Vault runtime configuration are introduced. Deployments enabling the resolver must provision Vault auth roles, policies, and service-account access; resolver failures classify as credential_invalid and dead-letter without leaking Vault responses or secret values. Token renewal and adapter shutdown become part of notifier lifecycle management, and tests must cover cancellation, missing path/key, redaction, and the no-resolver path.

# ADR-022：制品签名校验保持 Ed25519 信任根主路径

- Status: accepted
- Date: created 2026-09-17 / updated 2026-09-17
- Scope: project
- Related: REQ-043 (Requirements); TASK-106 (Tasks)

## Status
accepted

## Context
REQ-043 already defines artifact trust as a managed key lifecycle: Ed25519 trust roots are created, rotated through a grace window, retired and revoked, and a versioned trust policy names the issuers whose signatures are acceptable. That path is implemented (`internal/trust`, trust-root state machine, `TrustService`) and enforced during preflight: a verification failure is rejected when the environment policy says `FailClosed` (`internal/trust/policy.go`, `internal/orchestrator/service.go`).

TASK-100 raised the question of whether to adopt cosign/sigstore instead, or alongside it. The question matters because sigstore's keyless model (OIDC identity plus a transparency log) is the ecosystem default for OCI artifacts, and "we sign with Ed25519 roots" is not by itself an argument against it. The constraints that decide it are specific to this repository:

- ADR-004 forbids `os/exec` on runtime paths, so a cosign *binary* cannot be invoked; adoption means importing sigstore's Go modules (fulcio/rekor client stacks, TUF metadata resolution) and their transitive licence surface, all of which `make check-licenses` and the module-size budget must accept.
- Keyless verification needs network access to Fulcio/Rekor (or a private equivalent) at verification time. Release admission must not depend on a third-party service being reachable; today trust verification is a local computation over a policy plus stored roots.
- Nothing in the current requirement set asks for keyless: REQ-043's issuers are the project's own CI identity, which holds a key.

## Decision
The Ed25519 trust-root path stays the single signature-verification mechanism, and no sigstore dependency is introduced. Concretely:

- `internal/trust` remains authoritative for "who may sign", including rotation, grace, retirement and revocation; the trust policy stays versioned and fail-closed in production via `FailClosed`.
- keyless verification is **not** implemented. If a future requirement needs to accept signatures from third-party artifacts signed through Fulcio/Rekor, it enters as an **adapter behind the existing trust-policy interface** — one more root/verifier type evaluated by the same admission step — not as a replacement for the Ed25519 path and not as a second, parallel admission mechanism.
- the boundary is recorded in `SECURITY.md` so the absence is a documented limitation rather than an untested expectation.

## Alternatives Considered
- **Replace the Ed25519 path with sigstore keyless verification**: rejected. It trades a locally verifiable, offline-capable check for one that needs Fulcio and Rekor to be reachable during admission, and it pulls a large transitive dependency set into a module whose licence and size are gated. The gain — verifying signatures made by identities we do not manage — is not a current requirement.
- **Add sigstore alongside the Ed25519 path as a second admission mechanism**: rejected for now. Two admission paths with independent failure modes invite the "which one decided?" ambiguity that makes an incident hard to reconstruct; the adapter form above keeps exactly one decision point.
- **Do nothing and leave the choice implicit**: rejected. TASK-100 could not be closed while the option stayed open, and an undocumented absence is indistinguishable from an oversight during review.

## Consequences
- Release admission stays offline-capable and its failure modes stay local (missing root, revoked issuer, policy unavailable).
- Third-party keyless signatures cannot be verified; onboarding such an artifact today requires adding its signer as a managed Ed25519 root (or re-signing it in CI).
- If keyless support is later required, the cost is scoped to one adapter plus its dependency-licence review, and the admission step, evidence and metrics do not change shape.
- TASK-106 is closed by this decision; no code change accompanies it.

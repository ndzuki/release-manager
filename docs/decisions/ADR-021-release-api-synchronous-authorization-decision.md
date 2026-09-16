# ADR-021：release-api 审计授权经同步判定 RPC（透传用户 JWT）

- Status: accepted
- Date: created 2026-09-16 / updated 2026-09-16
- Scope: project
- Related: REQ-027, REQ-029 (Requirements); TASK-103 (Tasks)

## Status
accepted

## Context
release-api (`cmd/api`) serves `audit.v1.AuditService` (`Emit`, `QueryAuditEvents`, `ExportAuditEvents`) behind its own JWT-only interceptor (`internal/jwtauth` + `internal/audit`). It runs no Casbin enforcer, and its database holds audit events only: no organization membership, no role rows, no policy. ADR-015 makes exactly one authority own each database, so release-api cannot read release-auth's membership or `casbin_rule` tables; ADR-006 requires every authorization decision to be made server-side against persistent membership and an active session, so a JWT `roles` claim is not a sufficient basis (it is a merged cross-organization list that cannot answer "what is this user's role in this organization", and it cannot observe session revocation).

REQ-029 nonetheless requires role-dependent behavior on this surface: `platform_admin` may query across organizations with up to a 366-day window, while `release_admin`/`deployer`/`viewer` are forced to their own organization with a 31-day window, and an over-wide range must fail with `range_too_large` (AC-029-01/02). TASK-095 delivered only the organization-scoping half (the principal organization is the only readable scope) and deliberately left the role-dependent half unimplemented, so release-api currently cannot distinguish `platform_admin` from any other role.

## Decision
release-api does not embed Casbin and does not replicate authorization state. It forwards the caller's access token to release-auth through a new authorization-decision RPC on `auth.v1.AuthorizationService` (working name `AuthorizeAccess`; the exact name and field shape are fixed by the REQ-027 contract change), and release-auth — the single authority for organization-domain authorization — verifies the token, loads the persistent membership role and the versioned policy, confirms the caller's session is still active, and returns the decision plus the effective scope: allow/deny, reason code, policy version, whether the caller may override the organization scope, and the maximum query window in days.

release-api applies the returned scope (forced or permitted organization, window limit) and fails closed with `CodeUnavailable` when release-auth cannot answer within its deadline. The RPC carries the user's own bearer token — the existing `authctx.WithAuthorizationHeader` precedent — rather than a service-identity assertion, so release-auth proves the identity instead of trusting the caller. release-api must never derive roles from JWT claims for an authorization decision.

## Alternatives Considered
- **Embed Casbin in release-api and synchronize versioned policy projections from release-auth** (the pattern REQ-027 built for release-orchestrator): rejected. It duplicates the decision implementation and adds a staleness protocol, while the audit surface — low-frequency reads plus a buffered write — gains no latency benefit that justifies a second enforcement point.
- **Read release-auth's membership/`casbin_rule` tables directly**: rejected. It breaks the single-authority boundary of ADR-015 and cannot work in production, where the services use separate PostgreSQL instances.
- **Authorize locally from the JWT `roles` claim**: rejected. ADR-006 requires persistent membership plus an active session; the claim is a merged cross-organization role list, cannot express the per-organization role, and cannot observe session revocation.
- **Keep the status quo (organization scoping only) and drop the role-dependent REQ-029 rules**: rejected. AC-029-01/02 would stay unimplemented and `platform_admin` would lose the cross-organization view the requirement grants.

## Consequences
release-api gains server-authoritative, role-aware audit authorization and inherits session-revocation checking without storing membership data; the three audit procedures gain a synchronous dependency on release-auth and must fail closed when it is unavailable or slow. A new RPC and its error model become part of the `auth.v1` contract (ADR-002: contract change through `api/proto` + buf), and the audit surface's authorization rules live in exactly one place. Authorization latency for audit reads becomes release-auth's p99 plus one round trip; because audit queries are interactive and low-frequency the decision is deliberately not cached, so revocation stays immediate. REQ-027's contract section and REQ-029's authorization rules must reference this decision; TASK-103 implements it, including the previously unimplemented `platform_admin` cross-organization branch and the 31/366-day window enforcement.

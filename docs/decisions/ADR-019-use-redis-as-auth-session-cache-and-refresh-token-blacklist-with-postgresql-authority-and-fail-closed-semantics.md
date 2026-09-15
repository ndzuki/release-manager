# ADR-019：Use Redis as auth session cache and refresh-token blacklist with PostgreSQL authority and fail-closed semantics

- Status: accepted
- Date: created 2026-08-05 / updated 2026-08-05
- Scope: project
- Related: REQ-025 (Requirements); TASK-073 (Tasks)

## Status
accepted

## Context
Auth sessions are durably stored in PostgreSQL or SQLite, but production authentication topology also requires Redis for low-latency refresh-session lookup and immediate refresh-token replay rejection. Redis is an additional failure domain, so cache failures must never turn into authentication bypasses or silently change the authoritative session state. The design must preserve ADR-014's single PostgreSQL authority and ADR-008's fail-closed safety principle while allowing rollback to the existing database-only mode by removing Redis configuration.

## Decision
Use `github.com/redis/go-redis/v9` to decorate `store.AuthSessionStore` when `redis.address` is configured. PostgreSQL or SQLite remains authoritative. Redis stores refresh-hash session cache entries, refresh-token blacklist entries, and per-user refresh-hash indexes with TTLs bounded by the underlying session expiry. Reads check the blacklist before cache and database backfill. Any Redis command or pipeline failure returns `store.ErrUnavailable`; authentication maps that sentinel to Connect `Unavailable` with the stable client message `verification_unavailable`. Redis is disabled when no address is configured, preserving the database-only rollback path. `HasActiveByUserID`, direct ID/family reads, and expired-row cleanup continue to use the authoritative database.

## Alternatives Considered
- Make Redis the authoritative session store: rejected because it would weaken restart durability and contradict the existing PostgreSQL/SQLite session contract.
- Treat Redis as best-effort and fall back to PostgreSQL after Redis errors: rejected because an unavailable blacklist could allow a revoked or replayed refresh token.
- Cache every session query including active-user checks: rejected because it expands invalidation complexity without being required by TASK-073.
- Store only cached sessions without a blacklist: rejected because stale cache entries cannot provide immediate replay rejection after rotation or logout.

## Consequences
Authentication session lifecycle requests depend on Redis availability when the adapter is enabled and fail closed during outages. Database writes may commit before a Redis write fails; retries remain safe because revocation is idempotent and orphaned sessions cannot be returned while Redis is unavailable. Key naming and TTL behavior become compatibility contracts. Operators can restore the previous database-only topology by removing Redis configuration, without a schema migration or session-data loss.

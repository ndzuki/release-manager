-- TASK-361: backfill sessions.customer_id / cluster_id for legacy rows.
--
-- The columns were added by 000014_operator_management with DEFAULT '', so every
-- row that existed at that point and every row written without the fields kept
-- the empty string. Only Enroll set them (internal/store/sqlite/operator_management.go
-- and its postgres twin copy the operator's scope onto the session); the row
-- CommandStream establishes on the mTLS gateway path did not
-- (internal/operator/service.go:488 now writes both). Consequence: filtering
-- sessions by cluster required joining operators, and a direct
-- sessions.cluster_id lookup silently missed the live session.
--
-- operator_id is NOT NULL REFERENCES operators(id) ON DELETE CASCADE, so the
-- authoritative scope is recoverable for every row; the EXISTS guard keeps the
-- statement a no-op if a database ever contains a dangling operator_id (SQLite
-- only enforces the reference when foreign_keys is on). Forward-only and
-- idempotent: the WHERE clause makes a second run a no-op. Rows that carry both
-- values are never touched; a row missing either one takes the operator's
-- authoritative pair (operators are never moved between clusters, so this can
-- only repair an incomplete row, never replace meaningful scope).
UPDATE sessions
SET customer_id = (SELECT o.customer_id FROM operators AS o WHERE o.id = sessions.operator_id),
    cluster_id  = (SELECT o.cluster_id  FROM operators AS o WHERE o.id = sessions.operator_id)
WHERE (customer_id = '' OR cluster_id = '')
  AND EXISTS (SELECT 1 FROM operators AS o WHERE o.id = sessions.operator_id);

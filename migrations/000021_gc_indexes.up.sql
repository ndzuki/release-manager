CREATE INDEX IF NOT EXISTS idx_pl_terminal_created
    ON preflight_lifecycles(operation_terminal_at, created_at);

CREATE INDEX IF NOT EXISTS idx_pl_opid
    ON preflight_lifecycles(operation_id);

CREATE INDEX IF NOT EXISTS idx_ci_created
    ON cleanup_idempotency(created_at);

-- Backfill legacy lifecycle rows from the authoritative terminal operation timestamp.
UPDATE preflight_lifecycles AS pl
SET operation_terminal_at = o.terminal_at
FROM operations AS o
WHERE pl.operation_id = o.id
  AND pl.operation_terminal_at IS NULL
  AND o.terminal_at IS NOT NULL;

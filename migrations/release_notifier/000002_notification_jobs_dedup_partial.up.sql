-- REQ-031 AC-031-10: the idempotency key dedupes only NON-terminal jobs, so a job
-- that reached dead_letter no longer blocks a new one for the same
-- (operation_id, channel, recipient). That is the prerequisite for a replay chain.
--
-- 000001 declared the key as an inline table-level UNIQUE, which means PostgreSQL
-- generated the constraint name. Look it up by type instead of guessing that name:
-- a wrong guess would leave the old constraint in place and the dedup unchanged.
DO $$
DECLARE constraint_name text;
BEGIN
    SELECT conname INTO constraint_name
      FROM pg_constraint
     WHERE conrelid = 'notification_jobs'::regclass
       AND contype = 'u';
    IF constraint_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE notification_jobs DROP CONSTRAINT %I', constraint_name);
    END IF;
END $$;

CREATE UNIQUE INDEX idx_notification_jobs_dedup
    ON notification_jobs (operation_id, channel, recipient)
    WHERE status NOT IN ('delivered', 'dead_letter');

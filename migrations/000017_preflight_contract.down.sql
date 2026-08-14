-- TASK-019 down: restore the legacy append-only lifecycle shape.
-- Note: stages is restored as a JSONB array of stage-name strings, not the
-- original object array; structural rollback only.

ALTER TABLE preflight_lifecycles DROP CONSTRAINT IF EXISTS preflight_lifecycles_overall_check;

UPDATE preflight_lifecycles SET overall = 'timeout' WHERE overall = 'cancelled';

ALTER TABLE preflight_lifecycles ALTER COLUMN stages DROP DEFAULT;

ALTER TABLE preflight_lifecycles ALTER COLUMN stages TYPE JSONB
    USING CASE WHEN stages = '' THEN '[]'::jsonb ELSE to_jsonb(stages) END;

ALTER TABLE preflight_lifecycles ALTER COLUMN stages SET DEFAULT '[]'::jsonb;

ALTER TABLE preflight_lifecycles ADD COLUMN error_code TEXT NOT NULL DEFAULT '';

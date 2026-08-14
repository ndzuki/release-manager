-- TASK-019 down: restore the legacy append-only lifecycle shape.
-- Canonical comma-separated stages round-trips back to the legacy JSONB
-- object array ([{"stage":"artifact"}, ...]) via a staging column, so a
-- down+up cycle loses no stage entries or order. ALTER TYPE ... USING
-- cannot contain subqueries, hence the two-phase conversion (mirrors up).

ALTER TABLE preflight_lifecycles DROP CONSTRAINT IF EXISTS preflight_lifecycles_overall_check;

UPDATE preflight_lifecycles SET overall = 'timeout' WHERE overall = 'cancelled';

ALTER TABLE preflight_lifecycles ADD COLUMN stages_legacy JSONB NOT NULL DEFAULT '[]'::jsonb;

UPDATE preflight_lifecycles
SET stages_legacy = CASE
    WHEN stages = '' THEN '[]'::jsonb
    ELSE (
        SELECT jsonb_agg(jsonb_build_object('stage', s) ORDER BY ord)
        FROM unnest(string_to_array(stages, ',')) WITH ORDINALITY AS t(s, ord)
    )
END;

ALTER TABLE preflight_lifecycles DROP COLUMN stages;
ALTER TABLE preflight_lifecycles RENAME COLUMN stages_legacy TO stages;

ALTER TABLE preflight_lifecycles ALTER COLUMN stages SET DEFAULT '[]'::jsonb;

ALTER TABLE preflight_lifecycles ADD COLUMN error_code TEXT NOT NULL DEFAULT '';

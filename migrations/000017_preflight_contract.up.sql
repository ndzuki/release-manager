-- TASK-019: converge preflight_lifecycles to the REQ-019 two-phase contract.
-- JSONB stages → canonical comma-separated stage names, drop error_code, add a
-- four-value overall constraint, and map the legacy 'timeout' overall to 'cancelled'.
-- Uses a staging column because ALTER TYPE ... USING cannot contain subqueries.

ALTER TABLE preflight_lifecycles ADD COLUMN stages_canonical TEXT NOT NULL DEFAULT '';

UPDATE preflight_lifecycles
SET stages_canonical = CASE
    WHEN stages IS NULL OR stages = '[]'::jsonb THEN ''
    WHEN jsonb_typeof(stages) = 'array' THEN (
        SELECT string_agg(elem->>'stage', ',' ORDER BY ord)
        FROM jsonb_array_elements(stages) WITH ORDINALITY AS t(elem, ord)
    )
    ELSE ''
END;

ALTER TABLE preflight_lifecycles DROP COLUMN stages;
ALTER TABLE preflight_lifecycles RENAME COLUMN stages_canonical TO stages;

ALTER TABLE preflight_lifecycles DROP COLUMN IF EXISTS error_code;

UPDATE preflight_lifecycles SET overall = 'cancelled' WHERE overall = 'timeout';

ALTER TABLE preflight_lifecycles
    ADD CONSTRAINT preflight_lifecycles_overall_check
    CHECK (overall IN ('running', 'passed', 'failed', 'cancelled'));

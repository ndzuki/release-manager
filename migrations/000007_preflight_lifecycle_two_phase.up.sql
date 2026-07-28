UPDATE preflight_lifecycles
SET overall = 'cancelled'
WHERE overall = 'timeout';

ALTER TABLE preflight_lifecycles
    ADD COLUMN stages_text TEXT NOT NULL DEFAULT '';

UPDATE preflight_lifecycles
SET stages_text = CASE
    WHEN stages IS NULL OR stages = '[]'::jsonb THEN ''
    WHEN jsonb_typeof(stages) = 'array' THEN COALESCE((
        SELECT string_agg(element->>'stage', ',' ORDER BY ordinal)
        FROM jsonb_array_elements(stages) WITH ORDINALITY AS entries(element, ordinal)
        WHERE element ? 'stage'
    ), '')
    ELSE trim(both '"' from stages::text)
END;
ALTER TABLE preflight_lifecycles
    DROP COLUMN stages;

ALTER TABLE preflight_lifecycles
    RENAME COLUMN stages_text TO stages;

ALTER TABLE preflight_lifecycles
    ALTER COLUMN stages SET DEFAULT '',
    ALTER COLUMN overall SET DEFAULT 'running',
    ADD CONSTRAINT chk_preflight_lifecycles_overall
        CHECK (overall IN ('running', 'passed', 'failed', 'cancelled')),
    DROP COLUMN error_code;

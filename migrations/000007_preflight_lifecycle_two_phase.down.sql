ALTER TABLE preflight_lifecycles
    DROP CONSTRAINT chk_preflight_lifecycles_overall,
    ADD COLUMN error_code TEXT NOT NULL DEFAULT '',
    ADD COLUMN stages_json JSONB NOT NULL DEFAULT '[]'::jsonb;

UPDATE preflight_lifecycles
SET stages_json = CASE
    WHEN stages = '' THEN '[]'::jsonb
    ELSE (
        SELECT jsonb_agg(jsonb_build_object('stage', stage_name))
        FROM unnest(string_to_array(stages, ',')) AS stage_name
    )
END;

ALTER TABLE preflight_lifecycles
    DROP COLUMN stages;

ALTER TABLE preflight_lifecycles
    RENAME COLUMN stages_json TO stages;

ALTER TABLE preflight_lifecycles
    ALTER COLUMN stages SET DEFAULT '[]'::jsonb;

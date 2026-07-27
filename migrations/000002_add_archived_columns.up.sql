ALTER TABLE release_bundles ADD COLUMN archived_at TIMESTAMPTZ;
ALTER TABLE release_bundles ADD COLUMN archived_from_status TEXT NOT NULL DEFAULT '';

-- TASK-216: record who submitted a bundle, so an unclaimed bundle can no longer be adopted by
-- an unrelated organization and then block the submitter's own first install.
--
-- Additive and defaulted on purpose. Existing rows keep '' (attribution unknown) and therefore
-- keep the first-come-first-served adoption rule; only bundles submitted after this migration
-- carry an owner. The column is TEXT NOT NULL DEFAULT '' rather than NULL to avoid the
-- three-valued-logic trap already paid for in TASK-215.
ALTER TABLE release_bundles
    ADD COLUMN submitted_by_organization_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN submitted_by_user_id TEXT NOT NULL DEFAULT '';

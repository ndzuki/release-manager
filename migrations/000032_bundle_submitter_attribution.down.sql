-- Rollback of 000032. Dropping the columns loses the attribution written since the migration;
-- the schema returns to its previous shape, which is exactly what a rollback restores.
ALTER TABLE release_bundles
    DROP COLUMN IF EXISTS submitted_by_user_id,
    DROP COLUMN IF EXISTS submitted_by_organization_id;

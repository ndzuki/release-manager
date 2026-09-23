-- TASK-168 (REQ-058 C1/R1): observed workload field projection on
-- release_inventory. The operator reports the live container names, their image
-- refs, the replica count and the observation time alongside the authoritative
-- workload identity (REQ-085); the orchestrator persists them here so the
-- emergency read model can derive real current values (D7=A) instead of the
-- hardcoded "unavailable" sentinels.
--
-- All columns are additive and defaulted, so existing rows and inventory syncs
-- stay compatible: an empty observed_containers / observed_image_refs and a
-- NULL observed_replicas / observed_at mean "not observed" (fail closed).
-- Migration 000024 is the precedent for the same additive-boundary shape.
ALTER TABLE release_inventory ADD COLUMN IF NOT EXISTS observed_containers JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE release_inventory ADD COLUMN IF NOT EXISTS observed_image_refs JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE release_inventory ADD COLUMN IF NOT EXISTS observed_replicas BIGINT;
ALTER TABLE release_inventory ADD COLUMN IF NOT EXISTS observed_at TIMESTAMPTZ;

-- REQ-085 / TASK-085: authoritative Emergency workload identity on
-- release_inventory (D-110=A additive boundary). The operator reads the live
-- workload (kind/name/namespace/uid) and reports it over the Connect control
-- stream; the orchestrator persists it here and fills EmergencyTarget /
-- EmergencyIntent / EmergencyCommand from it. All columns are additive
-- TEXT NOT NULL DEFAULT '' so existing rows and syncs stay compatible.

ALTER TABLE release_inventory ADD COLUMN workload_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE release_inventory ADD COLUMN workload_name TEXT NOT NULL DEFAULT '';
ALTER TABLE release_inventory ADD COLUMN workload_namespace TEXT NOT NULL DEFAULT '';
ALTER TABLE release_inventory ADD COLUMN workload_uid TEXT NOT NULL DEFAULT '';

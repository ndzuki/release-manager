-- TASK-148 (REQ-058 AC-058-33): record the REVERT reconciliation outcome.
-- awaiting_standard_release was the only revert_status ever produced; there was
-- nowhere to record that a later standard operation actually converged the
-- cluster back to the approved rendered value, nor which operation did it.
ALTER TABLE emergency_intents ADD COLUMN IF NOT EXISTS revert_status TEXT NOT NULL DEFAULT '';
ALTER TABLE emergency_intents ADD COLUMN IF NOT EXISTS reconciled_by_operation_id TEXT NOT NULL DEFAULT '';

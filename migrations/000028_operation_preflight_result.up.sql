-- TASK-149 (REQ-056 AC-056-03): persist the preflight stage results on the
-- operation. The coordinator already computes them
-- (internal/orchestrator/preflight/result.go AggregateResult) but only the error
-- code reached the flat last_error column, so a failed preflight could not show
-- which stage failed or what its checks said.
ALTER TABLE operations ADD COLUMN IF NOT EXISTS preflight_result_json JSONB NOT NULL DEFAULT '{}'::jsonb;

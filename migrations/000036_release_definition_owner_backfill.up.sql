-- TASK-359: backfill release_definitions.owner_organization_id for the rows the
-- pre-TASK-357 creation path left NULL.
--
-- TASK-357 made creation derive the owner from the verified request identity
-- (internal/orchestrator/definition.go releaseDefinitionOwner); it did not touch
-- rows already stored. On the production database that left two unapprovable
-- definitions: acme-web (active) and release-fixture in e2e-plan-scratch
-- (disabled). Approving a values revision of either failed with
-- failed_precondition / release_definition_owner_unresolved
-- (internal/orchestrator/values_approval.go checkApprovalAuth).
--
-- Why this is a recovery, not a guess: REQ-068's migration strategy is
-- "backfill from the unique active binding". The owner organization is only used
-- as an authorization subject: checkApprovalAuth requires the actor's
-- organization to equal owner_organization_id, that organization to be active,
-- and an ACTIVE org_customer_bindings row for this definition's customer. A
-- customer with exactly one active binding therefore has exactly one
-- organization that could ever authorize its definitions, so the update grants
-- no authorization the organization did not already hold through that binding.
--
-- The statement is intentionally not scoped to production UUIDs:
--   * owner_organization_id IS NULL makes it idempotent (a second run matches no
--     row) and leaves definitions that already carry an owner untouched;
--   * the "exactly one active binding" guard makes it a no-op when the answer is
--     ambiguous (zero or several active bindings) -- those rows keep NULL and
--     stay unapprovable by design (release_definition_owner_unresolved);
--   * the joined organizations.status = 'active' guard skips a disabled owner,
--     which could never pass the authorization check either.
--
-- NOT NULL is deliberately NOT added here (see TASK-359 delivery record):
-- REQ-068 requires the management flow first, and no RPC/store path can assign
-- or repair owner_organization_id after creation, so a NOT NULL column would
-- make an unbackfillable row permanently unfixable in-product. The SQLite twin
-- of this statement lives in internal/store/sqlite/db.go migrationStatements;
-- internal/store/sqlite/definition_owner_backfill_test.go covers both the
-- backfill and the guards, and the PostgreSQL SQL is covered by
-- internal/store/postgres/definition_owner_backfill_integration_test.go.
UPDATE release_definitions AS d
SET owner_organization_id = b.org_id
FROM org_customer_bindings AS b
JOIN organizations AS o ON o.id = b.org_id
WHERE d.owner_organization_id IS NULL
  AND b.customer_id = d.customer_id
  AND b.status = 'active'
  AND o.status = 'active'
  AND (SELECT count(*) FROM org_customer_bindings AS other
       WHERE other.customer_id = d.customer_id AND other.status = 'active') = 1;

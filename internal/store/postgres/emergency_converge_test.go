//go:build integration

package postgres_test

// PostgreSQL convergence tests for ConvergeEmergencyResult / ListStuckLocks /
// ReleaseLock (REQ-089 AC-089-01..04). Each test is the black-box mirror of
// its SQLite counterpart in internal/store/sqlite/emergency_converge_test.go
// through the same store public seams — a cross-engine consistency guarantee
// that the postgres engine converges every non-terminal arrival order to the
// same terminal state + effect with the same state_version sequence, and no
// longer returns the postgres-only ErrInvalidState for the queued/running race
// (REQ-089 §1 / D-112).
//
// Knowledge refs applied:
//   - core/go/state-machine-pattern.md: state_version CAS + single-transaction
//     terminal convergence; a result that beats the queued→running migration
//     advances queued→running→terminal inside one transaction, never writing a
//     QUEUED→SUCCEEDED row/timeline entry (ADR-009).
//   - core/containers/docker-test-container-management.md: these integration
//     tests run against a script-managed temporary postgres container via
//     POSTGRES_TEST_DSN (per-test schema + migrations via setupStore); without
//     the DSN they skip.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
)

// pgConvergeCommand builds a ConvergeEmergencyResult command mirroring what
// finishEmergencyResult would send for a succeeded/failed operator result
// (sqlite convergeCmd counterpart).
func pgConvergeCommand(t *testing.T, result *store.OperationCreationResult, terminal store.OperationStatus, effect store.EmergencyEffectStatus, version int, lastError string) store.EmergencyConvergeCommand {
	t.Helper()
	before, err := json.Marshal(map[string]any{"replicas": 1})
	require.NoError(t, err)
	after, err := json.Marshal(map[string]any{"replicas": 3})
	require.NoError(t, err)
	return store.EmergencyConvergeCommand{
		IntentID:             result.Intent.ID,
		OperationID:          result.Operation.ID,
		ExpectedStateVersion: version,
		Status:               terminal,
		EffectStatus:         effect,
		LastError:            lastError,
		BeforeSnapshot:       before,
		AfterSnapshot:        after,
		RequestID:            result.Intent.CommandID,
	}
}

// pgSeedEmergencyDefinition creates an active release definition with the
// emergency replicas cap enabled (sqlite seedEmergencyDefinition counterpart).
func pgSeedEmergencyDefinition(t *testing.T, st *postgresstore.Store, id string) {
	t.Helper()
	require.NoError(t, st.Definitions().Create(t.Context(), &store.ReleaseDefinition{
		ID: id, Name: id, CustomerID: "customer", ClusterID: "cluster", Namespace: "default",
		ReleaseName: id, Status: store.DefStatusActive, MaxEmergencyReplicas: 100,
	}, nil))
}

// pgCreateEmergencyViaUOW routes an emergency creation through the shared
// OperationCreationUnitOfWork seam (REQ-079 canonical creation path).
func pgCreateEmergencyViaUOW(t *testing.T, st *postgresstore.Store, command store.EmergencyCreateCommand) *store.OperationCreationResult {
	t.Helper()
	result, err := st.OperationCreationUnitOfWork()(t.Context(), store.OperationCreationRequest{
		Operation: command.Operation,
		Emergency: &command,
	})
	require.NoError(t, err)
	return result
}

// pgEmergencyCreateCommand builds a canonical SET_REPLICAS EMERGENCY create
// command (REQUIRE_PROMOTION with an atomic convergence task) on the given
// definition — every convergence scenario in this file exercises the replicas
// action (sqlite emergencyCreateCommand counterpart with
// store.EmergencySetReplicas).
func pgEmergencyCreateCommand(t *testing.T, definitionID, idempotencyKey, requestHash string) store.EmergencyCreateCommand {
	t.Helper()
	now := time.Now().UTC()
	opID := uuid.NewString()
	replicas := int32(3)
	intent := &store.EmergencyIntent{
		ID: uuid.NewString(), ReleaseDefinitionID: definitionID, OperationID: opID, CommandID: uuid.NewString(),
		Action: store.EmergencySetReplicas, WorkloadKind: "DEPLOYMENT", WorkloadName: "api", WorkloadNamespace: "default", WorkloadUID: "uid-api",
		TargetReplicas: &replicas, Convergence: store.EmergencyRequirePromotion,
	}
	paths, err := json.Marshal([]string{"image.digest"})
	require.NoError(t, err)
	intent.PromotionPaths = paths
	task := &store.ConvergenceTask{
		ID: uuid.NewString(), OperationID: opID, ReleaseDefinitionID: definitionID, Action: store.EmergencySetReplicas,
		TargetSummary: "Deployment/api", Reason: "incident", PromotionPaths: paths,
	}
	hash := sha256.Sum256([]byte(idempotencyKey))
	return store.EmergencyCreateCommand{
		Operation: &store.Operation{
			ID: opID, OperationType: store.OperationEmergency, Status: store.StatusPending,
			ReleaseDefinitionID: definitionID, IdempotencyKey: hex.EncodeToString(hash[:]), RequestHash: requestHash,
			CreatedAt: now, UpdatedAt: now,
		},
		Intent: intent, ConvergenceTask: task,
		IdempotencyScope: "org:" + definitionID, IdempotencyKeyHash: hex.EncodeToString(hash[:]),
		RequestHash: requestHash, IdempotencyExpiresAt: now.Add(time.Hour),
	}
}

// pgEmergencyRevertCommand builds a REVERT_ON_NEXT_RECONCILE emergency command
// (no convergence task, so releases are not blocked by a pending_promotion
// obligation) targeting the given workload name (sqlite
// emergencyRevertCommand counterpart).
func pgEmergencyRevertCommand(t *testing.T, definitionID, idempotencyKey, requestHash, workloadName string) store.EmergencyCreateCommand {
	t.Helper()
	cmd := pgEmergencyCreateCommand(t, definitionID, idempotencyKey, requestHash)
	cmd.Intent.WorkloadName = workloadName
	cmd.Intent.Convergence = store.EmergencyRevertOnNextReconcile
	cmd.ConvergenceTask = nil
	return cmd
}

// pgSetEmergencyTerminalAt back-dates the operation terminal_at column so the
// stuck-lock observe window elapses deterministically (sqlite
// setEmergencyTerminalAt counterpart; TIMESTAMPTZ takes a time.Time).
func pgSetEmergencyTerminalAt(t *testing.T, st *postgresstore.Store, operationID string, when time.Time) {
	t.Helper()
	_, err := st.SQLDB().ExecContext(context.Background(),
		`UPDATE operations SET terminal_at = $1 WHERE id = $2`, when.UTC(), operationID)
	require.NoError(t, err)
}

// assertLegalStateTransitions asserts every STATE_TRANSITION timeline entry is
// a legal CanTransitionTo hop and that no transition ever starts from queued
// and lands on a terminal state (the D1 race invariant, AC-089-01).
func assertLegalStateTransitions(ctx context.Context, t *testing.T, st *postgresstore.Store, operationID string) {
	t.Helper()
	entries, err := st.Timeline().List(ctx, operationID, 0, 1<<30)
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Kind != string(store.TimelineEntryStateTransition) {
			continue
		}
		var data store.StateTransitionTimelineData
		require.NoError(t, json.Unmarshal(entry.Data, &data))
		// The D1 race must never write a queued→terminal migration.
		if data.FromState == "queued" {
			assert.False(t, store.OperationStatus(data.ToState).IsTerminal(),
				"illegal queued→%s terminal hop must never be recorded", data.ToState)
		}
		assert.True(t, store.OperationStatus(data.FromState).CanTransitionTo(store.OperationStatus(data.ToState)),
			"illegal transition %s→%s", data.FromState, data.ToState)
	}
}

// TestConvergeEmergencyResult_ResultBeatsRunningTransition (REQ-089 AC-089-01,
// counterpart sqlite TestConvergeEmergencyResult_ResultBeatsRunningTransition):
// the operator result lands while the operation is still queued (the
// queued→running ACK migration has not happened yet). ConvergeEmergencyResult
// must advance queued→running→succeeded inside ONE transaction, without ever
// producing a QUEUED→SUCCEEDED state-transition record. Before the REQ-089 fix
// this was the postgres-only ErrInvalidState drift (the CanTransitionTo(running)
// guard stopped the loop at queued).
func TestConvergeEmergencyResult_ResultBeatsRunningTransition(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	pgSeedEmergencyDefinition(t, st, "pg-def-converge-race")
	created := pgCreateEmergencyViaUOW(t, st, pgEmergencyCreateCommand(t, "pg-def-converge-race", "pg-idem-race", "pg-hash-race"))
	// created op is pending with state_version=1; orchestrator marks queued (v2).
	queued, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, created.Operation.StateVersion, "")
	require.NoError(t, err)
	require.Equal(t, store.StatusQueued, queued.Status)
	require.Equal(t, 2, queued.StateVersion)

	cmd := pgConvergeCommand(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, queued.StateVersion, "")
	result, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Resolved)
	assert.Equal(t, store.StatusSucceeded, result.Operation.Status)
	assert.True(t, result.Operation.Status.IsTerminal())
	assert.NotNil(t, result.Operation.TerminalAt)
	// Two intermediate hops (queued→running, running→succeeded).
	assert.Equal(t, queued.StateVersion+2, result.Operation.StateVersion)
	assert.Equal(t, store.EmergencyEffectApplied, result.Intent.EffectStatus)
	assert.Equal(t, store.EmergencyEffectApplied, result.Operation.EffectStatus)

	// No illegal queued→terminal row or timeline entry.
	assertLegalStateTransitions(ctx, t, st, created.Operation.ID)

	// AC-087-02 (order independence): converge on the same op must be an
	// idempotent no-op (Resolved=false).
	replay, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, pgConvergeCommand(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, result.Operation.StateVersion, ""))
	require.NoError(t, err)
	assert.False(t, replay.Resolved)
	assert.Equal(t, result.Operation.StateVersion, replay.Operation.StateVersion)
}

// TestConvergeEmergencyResult_RunningAlreadyAdvanced (counterpart sqlite
// TestConvergeEmergencyResult_RunningAlreadyAdvanced): the ACK migration
// already moved the op to running before the result arrived — only the
// running→succeeded terminal hop is applied (AC-089-03 order independence).
func TestConvergeEmergencyResult_RunningAlreadyAdvanced(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	pgSeedEmergencyDefinition(t, st, "pg-def-converge-running")
	created := pgCreateEmergencyViaUOW(t, st, pgEmergencyCreateCommand(t, "pg-def-converge-running", "pg-idem-running", "pg-hash-running"))
	queued, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, created.Operation.StateVersion, "")
	require.NoError(t, err)
	running, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusRunning, queued.StateVersion, "")
	require.NoError(t, err)
	require.Equal(t, 3, running.StateVersion)

	result, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, pgConvergeCommand(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, running.StateVersion, ""))
	require.NoError(t, err)
	assert.True(t, result.Resolved)
	assert.Equal(t, store.StatusSucceeded, result.Operation.Status)
	assert.Equal(t, running.StateVersion+1, result.Operation.StateVersion)
	assert.Equal(t, store.EmergencyEffectApplied, result.Intent.EffectStatus)
}

// TestConvergeEmergencyResult_FromPending (counterpart sqlite
// TestConvergeEmergencyResult_FromPending): the result lands while the op is
// still pending — the chain pending→queued→running→succeeded is applied
// (AC-089-02).
func TestConvergeEmergencyResult_FromPending(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	pgSeedEmergencyDefinition(t, st, "pg-def-converge-pending")
	created := pgCreateEmergencyViaUOW(t, st, pgEmergencyCreateCommand(t, "pg-def-converge-pending", "pg-idem-pending", "pg-hash-pending"))
	require.Equal(t, 1, created.Operation.StateVersion)

	result, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, pgConvergeCommand(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, created.Operation.StateVersion, ""))
	require.NoError(t, err)
	assert.True(t, result.Resolved)
	assert.Equal(t, store.StatusSucceeded, result.Operation.Status)
	assert.Equal(t, created.Operation.StateVersion+3, result.Operation.StateVersion)
	assert.Equal(t, store.EmergencyEffectApplied, result.Intent.EffectStatus)
}

// TestConvergeEmergencyResult_FromPreflight (counterpart sqlite
// TestConvergeEmergencyResult_FromPending, preflight variant): the result
// lands while the op sits at preflight — the defensive hop chain
// preflight→queued→running→succeeded is applied (REQ-089 §2). EMERGENCY
// production never lands in preflight; the branch covers the legal-hop
// advance through nextEmergencyHop(preflight)=queued.
func TestConvergeEmergencyResult_FromPreflight(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	pgSeedEmergencyDefinition(t, st, "pg-def-converge-preflight")
	created := pgCreateEmergencyViaUOW(t, st, pgEmergencyCreateCommand(t, "pg-def-converge-preflight", "pg-idem-preflight", "pg-hash-preflight"))
	preflight, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusPreflight, created.Operation.StateVersion, "")
	require.NoError(t, err)
	require.Equal(t, store.StatusPreflight, preflight.Status)
	require.Equal(t, 2, preflight.StateVersion)

	result, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, pgConvergeCommand(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, preflight.StateVersion, ""))
	require.NoError(t, err)
	assert.True(t, result.Resolved)
	assert.Equal(t, store.StatusSucceeded, result.Operation.Status)
	assert.Equal(t, preflight.StateVersion+3, result.Operation.StateVersion)
	assert.Equal(t, store.EmergencyEffectApplied, result.Intent.EffectStatus)
	assertLegalStateTransitions(ctx, t, st, created.Operation.ID)
}

// TestConvergeEmergencyResult_FailedWhileQueued (counterpart sqlite
// TestConvergeEmergencyResult_FailedWhileQueued): a failed authoritative
// result beating the running migration records failed + NOT_APPLIED (lock
// release matrix: failed/NOT_APPLIED releases).
func TestConvergeEmergencyResult_FailedWhileQueued(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	pgSeedEmergencyDefinition(t, st, "pg-def-converge-failed")
	created := pgCreateEmergencyViaUOW(t, st, pgEmergencyCreateCommand(t, "pg-def-converge-failed", "pg-idem-failed", "pg-hash-failed"))
	queued, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, created.Operation.StateVersion, "")
	require.NoError(t, err)

	result, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, pgConvergeCommand(t, created, store.StatusFailed, store.EmergencyEffectNotApplied, queued.StateVersion, "helm_failed"))
	require.NoError(t, err)
	assert.True(t, result.Resolved)
	assert.Equal(t, store.StatusFailed, result.Operation.Status)
	assert.Equal(t, queued.StateVersion+2, result.Operation.StateVersion)
	assert.Equal(t, store.EmergencyEffectNotApplied, result.Intent.EffectStatus)
	assert.NotNil(t, result.Operation.TerminalAt)
	assertLegalStateTransitions(ctx, t, st, created.Operation.ID)
}

// TestConvergeEmergencyResult_FailedLastErrorWritesErrorTimeline (counterpart
// sqlite TestConvergeEmergencyResult_FailedLastErrorWritesErrorTimeline): a
// failed converge records the sanitized ERROR timeline entry.
func TestConvergeEmergencyResult_FailedLastErrorWritesErrorTimeline(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	pgSeedEmergencyDefinition(t, st, "pg-def-converge-err")
	created := pgCreateEmergencyViaUOW(t, st, pgEmergencyCreateCommand(t, "pg-def-converge-err", "pg-idem-err", "pg-hash-err"))
	queued, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, created.Operation.StateVersion, "")
	require.NoError(t, err)
	running, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusRunning, queued.StateVersion, "")
	require.NoError(t, err)
	result, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, pgConvergeCommand(t, created, store.StatusFailed, store.EmergencyEffectNotApplied, running.StateVersion, "helm_failed: boom"))
	require.NoError(t, err)
	require.True(t, result.Resolved)
	assert.Equal(t, store.StatusFailed, result.Operation.Status)

	entries, err := st.Timeline().List(ctx, created.Operation.ID, 0, 1<<30)
	require.NoError(t, err)
	found := false
	for _, entry := range entries {
		if entry.Kind == string(store.TimelineEntryError) {
			found = true
			assert.Contains(t, string(entry.Data), "helm_failed")
		}
	}
	assert.True(t, found, "expected an ERROR timeline entry for the failed result")
}

// TestConvergeEmergencyResult_TerminalUnknownResolvesOnce (counterpart sqlite
// TestConvergeEmergencyResult_TerminalUnknownResolvesOnce): a terminal op
// whose effect is still UNKNOWN accepts exactly one UNKNOWN→APPLIED/NOT_APPLIED
// resolution; a repeat resolve is an idempotent no-op and a different-effect
// result is rejected.
func TestConvergeEmergencyResult_TerminalUnknownResolvesOnce(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	pgSeedEmergencyDefinition(t, st, "pg-def-converge-resolve")
	created := pgCreateEmergencyViaUOW(t, st, pgEmergencyCreateCommand(t, "pg-def-converge-resolve", "pg-idem-resolve", "pg-hash-resolve"))
	queued, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, created.Operation.StateVersion, "")
	require.NoError(t, err)
	// Time out while still queued → terminal timeout + effect UNKNOWN.
	finished, err := st.EmergencyIntents().Finish(ctx, created.Intent.ID, created.Operation.ID, queued.StateVersion, store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	require.Equal(t, store.StatusTimeout, finished.Status)
	require.Equal(t, 3, finished.StateVersion)

	resolve, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, pgConvergeCommand(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, finished.StateVersion, ""))
	require.NoError(t, err)
	require.True(t, resolve.Resolved)
	assert.Equal(t, store.StatusTimeout, resolve.Operation.Status, "terminal status must not change on effect resolution")
	assert.Equal(t, finished.StateVersion+1, resolve.Operation.StateVersion)
	assert.Equal(t, store.EmergencyEffectApplied, resolve.Intent.EffectStatus)

	// The EMERGENCY_EFFECT_RESOLVED timeline entry was written exactly once.
	timeline, err := st.Timeline().List(ctx, created.Operation.ID, 0, 1<<30)
	require.NoError(t, err)
	resolvedCount := 0
	for _, entry := range timeline {
		if entry.Kind == string(store.TimelineEntryEmergencyEffectResolved) {
			resolvedCount++
		}
	}
	assert.Equal(t, 1, resolvedCount)

	// Same-effect replay: idempotent no-op.
	replay, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, pgConvergeCommand(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, resolve.Operation.StateVersion, ""))
	require.NoError(t, err)
	assert.False(t, replay.Resolved)

	// Different effect after resolution: protocol conflict.
	_, err = st.EmergencyIntents().ConvergeEmergencyResult(ctx, pgConvergeCommand(t, created, store.StatusFailed, store.EmergencyEffectNotApplied, resolve.Operation.StateVersion, "x"))
	require.ErrorIs(t, err, store.ErrInvalidState)
}

// TestConvergeEmergencyResult_StaleVersion (counterpart sqlite
// TestConvergeEmergencyResult_StaleVersion): an optimistic-lock conflict is
// surfaced instead of silently dropping the result.
func TestConvergeEmergencyResult_StaleVersion(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	pgSeedEmergencyDefinition(t, st, "pg-def-converge-stale")
	created := pgCreateEmergencyViaUOW(t, st, pgEmergencyCreateCommand(t, "pg-def-converge-stale", "pg-idem-stale", "pg-hash-stale"))
	queued, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, created.Operation.StateVersion, "")
	require.NoError(t, err)

	// Simulate a concurrent ACK that advanced to running between the
	// operator's read (queued, v2) and its converge call (still v2).
	running, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusRunning, queued.StateVersion, "")
	require.NoError(t, err)
	require.Equal(t, 3, running.StateVersion)

	_, err = st.EmergencyIntents().ConvergeEmergencyResult(ctx, pgConvergeCommand(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, queued.StateVersion, ""))
	require.ErrorIs(t, err, store.ErrOptimisticLock)
}

// TestConvergeEmergencyResult_RetryAfterOptimisticLock (recovery-path
// coverage): after an ErrOptimisticLock on a stale version, reloading the
// operation and retrying with the current state version converges cleanly —
// the recovery is not polluted by the earlier failed attempt.
func TestConvergeEmergencyResult_RetryAfterOptimisticLock(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	pgSeedEmergencyDefinition(t, st, "pg-def-converge-retry")
	created := pgCreateEmergencyViaUOW(t, st, pgEmergencyCreateCommand(t, "pg-def-converge-retry", "pg-idem-retry", "pg-hash-retry"))
	queued, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, created.Operation.StateVersion, "")
	require.NoError(t, err)
	running, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusRunning, queued.StateVersion, "")
	require.NoError(t, err)

	// First attempt races on a stale (queued v2) version.
	_, err = st.EmergencyIntents().ConvergeEmergencyResult(ctx, pgConvergeCommand(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, queued.StateVersion, ""))
	require.ErrorIs(t, err, store.ErrOptimisticLock)

	// Recovery: reload the current version and retry (the operator's bounded
	// reload loop in finishEmergencyResult does exactly this).
	fresh, err := st.Operations().Get(ctx, created.Operation.ID)
	require.NoError(t, err)
	require.Equal(t, store.StatusRunning, fresh.Status)
	require.Equal(t, running.StateVersion, fresh.StateVersion)
	result, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, pgConvergeCommand(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, fresh.StateVersion, ""))
	require.NoError(t, err)
	assert.True(t, result.Resolved)
	assert.Equal(t, store.StatusSucceeded, result.Operation.Status)
	assert.Equal(t, fresh.StateVersion+1, result.Operation.StateVersion)
	assert.Equal(t, store.EmergencyEffectApplied, result.Intent.EffectStatus)
}

// TestConvergeEmergencyResult_NonEmergencyRejected (counterpart sqlite
// TestConvergeEmergencyResult_NonEmergencyRejected): the method only serves
// EMERGENCY operations — a non-EMERGENCY op is a protocol violation rejected
// with ErrInvalidState on both engines (cross-engine parity; the intent-row
// lock must not surface as ErrNotFound first).
func TestConvergeEmergencyResult_NonEmergencyRejected(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	pgSeedEmergencyDefinition(t, st, "pg-def-converge-non-emergency")
	// Create a plain INSTALL operation directly (skip the emergency UOW).
	now := time.Now().UTC()
	op := &store.Operation{
		ID: "pg-op-standard", OperationType: store.OperationInstall, Status: store.StatusPending,
		ReleaseDefinitionID: "pg-def-converge-non-emergency", IdempotencyKey: "pg-k", IdempotencyScope: "org:def",
		RequestHash: "pg-h", CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, st.Operations().Create(ctx, op))
	queued, err := st.Operations().UpdateStatus(ctx, op.ID, store.StatusQueued, op.StateVersion, "")
	require.NoError(t, err)

	cmd := store.EmergencyConvergeCommand{
		IntentID: "intent-x", OperationID: op.ID, ExpectedStateVersion: queued.StateVersion,
		Status: store.StatusSucceeded, EffectStatus: store.EmergencyEffectApplied,
	}
	_, err = st.EmergencyIntents().ConvergeEmergencyResult(ctx, cmd)
	require.ErrorIs(t, err, store.ErrInvalidState)
}

// TestConvergeEmergencyResult_ValidationErrors covers the command-level input
// validation surface (missing ids, non-terminal status, invalid effect,
// invalid expected version, unknown operation).
func TestConvergeEmergencyResult_ValidationErrors(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	pgSeedEmergencyDefinition(t, st, "pg-def-converge-validation")
	created := pgCreateEmergencyViaUOW(t, st, pgEmergencyCreateCommand(t, "pg-def-converge-validation", "pg-idem-val", "pg-hash-val"))
	base := pgConvergeCommand(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, created.Operation.StateVersion, "")

	// Missing operation id.
	missingOp := base
	missingOp.OperationID = ""
	_, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, missingOp)
	require.Error(t, err)

	// Missing intent id.
	missingIntent := base
	missingIntent.IntentID = ""
	_, err = st.EmergencyIntents().ConvergeEmergencyResult(ctx, missingIntent)
	require.Error(t, err)

	// Non-terminal target status.
	nonTerminal := base
	nonTerminal.Status = store.StatusRunning
	_, err = st.EmergencyIntents().ConvergeEmergencyResult(ctx, nonTerminal)
	require.Error(t, err)

	// Invalid effect status.
	invalidEffect := base
	invalidEffect.EffectStatus = store.EmergencyEffectUnknown
	_, err = st.EmergencyIntents().ConvergeEmergencyResult(ctx, invalidEffect)
	require.Error(t, err)

	// Expected state version below 1.
	badVersion := base
	badVersion.ExpectedStateVersion = 0
	_, err = st.EmergencyIntents().ConvergeEmergencyResult(ctx, badVersion)
	require.Error(t, err)

	// Unknown operation.
	unknown := base
	unknown.OperationID = "does-not-exist"
	_, err = st.EmergencyIntents().ConvergeEmergencyResult(ctx, unknown)
	require.ErrorIs(t, err, store.ErrNotFound)
}

// TestConvergeEmergencyResult_ConcurrentCalls (counterpart sqlite
// TestConvergeEmergencyResult_ConcurrentCalls): many concurrent authoritative
// results for the same operation converge exactly once — the CAS + single-tx
// hop advance guarantees a single terminal state, no duplicate timeline rows
// and no lost result (REQ-087 D2 concurrency / AC-087-12; AC-089-04 lock
// regression on the FOR UPDATE serialization).
func TestConvergeEmergencyResult_ConcurrentCalls(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	pgSeedEmergencyDefinition(t, st, "pg-def-converge-concurrent")
	created := pgCreateEmergencyViaUOW(t, st, pgEmergencyCreateCommand(t, "pg-def-converge-concurrent", "pg-idem-concurrent", "pg-hash-concurrent"))
	queued, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, created.Operation.StateVersion, "")
	require.NoError(t, err)
	cmd := pgConvergeCommand(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, queued.StateVersion, "")

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	resolved := make(chan bool, workers)
	for range workers {
		go func() {
			<-start
			outcome, convErr := st.EmergencyIntents().ConvergeEmergencyResult(ctx, cmd)
			errs <- convErr
			if convErr == nil {
				resolved <- outcome.Resolved
			}
		}()
	}
	close(start)
	winCount := 0
	noopCount := 0
	optLock := 0
	for range workers {
		convErr := <-errs
		switch {
		case convErr == nil:
			if <-resolved {
				winCount++
			} else {
				noopCount++
			}
		case errors.Is(convErr, store.ErrOptimisticLock):
			optLock++
		default:
			t.Fatalf("unexpected concurrent converge error: %v", convErr)
		}
	}
	assert.Equal(t, 1, winCount, "exactly one concurrent caller converges to the terminal state")
	assert.Equal(t, workers-1, noopCount+optLock, "the rest see the terminal state as no-op or lose the CAS")

	op, err := st.Operations().Get(ctx, created.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusSucceeded, op.Status)
	assert.Equal(t, queued.StateVersion+2, op.StateVersion, "terminal state version reflects exactly one converged hop sequence")

	// Only one running→succeeded timeline row exists.
	stateTransitions := 0
	entries, err := st.Timeline().List(ctx, created.Operation.ID, 0, 1<<30)
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Kind == string(store.TimelineEntryStateTransition) {
			stateTransitions++
		}
	}
	assert.Equal(t, 3, stateTransitions, "pending→queued (seed) + queued→running + running→succeeded must appear once each")
}

// TestEmergencyStuckLocks_DerivedQuery (REQ-087 AC-087-07, counterpart sqlite
// TestEmergencyStuckLocks_DerivedQuery; AC-089-04 lock-matrix regression): a
// terminal op with UNKNOWN effect past the observe window surfaces as a stuck
// lock; a young one, a resolved one and a released one do not.
func TestEmergencyStuckLocks_DerivedQuery(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	pgSeedEmergencyDefinition(t, st, "pg-def-stuck")
	observe := 24 * time.Hour
	now := time.Now().UTC()

	// terminal timeout + UNKNOWN (stuck candidate).
	old := pgCreateEmergencyViaUOW(t, st, pgEmergencyRevertCommand(t, "pg-def-stuck", "pg-idem-stuck-1", "pg-hash-stuck-1", "api-1"))
	queued, err := st.Operations().UpdateStatus(ctx, old.Operation.ID, store.StatusQueued, old.Operation.StateVersion, "")
	require.NoError(t, err)
	finished, err := st.EmergencyIntents().Finish(ctx, old.Intent.ID, old.Operation.ID, queued.StateVersion, store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	pgSetEmergencyTerminalAt(t, st, finished.ID, now.Add(-25*time.Hour))

	// fresh terminal UNKNOWN (younger than the window → not stuck yet).
	young := pgCreateEmergencyViaUOW(t, st, pgEmergencyRevertCommand(t, "pg-def-stuck", "pg-idem-stuck-2", "pg-hash-stuck-2", "api-2"))
	queued2, err := st.Operations().UpdateStatus(ctx, young.Operation.ID, store.StatusQueued, young.Operation.StateVersion, "")
	require.NoError(t, err)
	youngFinished, err := st.EmergencyIntents().Finish(ctx, young.Intent.ID, young.Operation.ID, queued2.StateVersion, store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	pgSetEmergencyTerminalAt(t, st, youngFinished.ID, now.Add(-time.Hour))

	// resolved terminal (effect APPLIED → not stuck).
	resolved := pgCreateEmergencyViaUOW(t, st, pgEmergencyRevertCommand(t, "pg-def-stuck", "pg-idem-stuck-3", "pg-hash-stuck-3", "api-3"))
	queued3, err := st.Operations().UpdateStatus(ctx, resolved.Operation.ID, store.StatusQueued, resolved.Operation.StateVersion, "")
	require.NoError(t, err)
	applyResult, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, pgConvergeCommand(t, resolved, store.StatusSucceeded, store.EmergencyEffectApplied, queued3.StateVersion, ""))
	require.NoError(t, err)
	require.True(t, applyResult.Resolved)
	pgSetEmergencyTerminalAt(t, st, resolved.Operation.ID, now.Add(-25*time.Hour))

	// released (AUDITED_OVERRIDE) terminal UNKNOWN → not stuck.
	released := pgCreateEmergencyViaUOW(t, st, pgEmergencyRevertCommand(t, "pg-def-stuck", "pg-idem-stuck-4", "pg-hash-stuck-4", "api-4"))
	queued4, err := st.Operations().UpdateStatus(ctx, released.Operation.ID, store.StatusQueued, released.Operation.StateVersion, "")
	require.NoError(t, err)
	releasedFinished, err := st.EmergencyIntents().Finish(ctx, released.Intent.ID, released.Operation.ID, queued4.StateVersion, store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	pgSetEmergencyTerminalAt(t, st, releasedFinished.ID, now.Add(-25*time.Hour))
	_, err = st.EmergencyIntents().ReleaseLock(ctx, store.ReleaseLockCommand{
		IntentID: released.Intent.ID, Mode: store.EmergencyReleaseAuditedOverride,
		Reason: "takeover", ObserveTimeout: observe,
	})
	require.NoError(t, err)

	locks, err := st.EmergencyIntents().ListStuckLocks(ctx, store.StuckLockFilter{
		DefinitionID: "pg-def-stuck", ObserveTimeout: observe,
	})
	require.NoError(t, err)
	require.Len(t, locks, 1, "only the old UNKNOWN unreleased lock is stuck")
	assert.Equal(t, old.Intent.ID, locks[0].Intent.ID)
	assert.Equal(t, store.EmergencyEffectUnknown, locks[0].Intent.EffectStatus)
	assert.Contains(t, locks[0].LockPathSummary, "DEPLOYMENT/api-1")
}

// TestEmergencyReleaseLock_ModesAndGuards (REQ-087 AC-087-08/09, counterpart
// sqlite TestEmergencyReleaseLock_ModesAndGuards; AC-089-04 lock-matrix
// regression): NOT_APPLIED_PROVEN records NOT_APPLIED + releases;
// AUDITED_OVERRIDE keeps UNKNOWN; non-terminal / young / already-released
// intents are rejected.
func TestEmergencyReleaseLock_ModesAndGuards(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	pgSeedEmergencyDefinition(t, st, "pg-def-release")
	observe := 24 * time.Hour

	// ── NOT_APPLIED_PROVEN ── (revert command: no convergence task)
	proven := pgCreateEmergencyViaUOW(t, st, pgEmergencyRevertCommand(t, "pg-def-release", "pg-idem-release-1", "pg-hash-release-1", "api-1"))
	queued, err := st.Operations().UpdateStatus(ctx, proven.Operation.ID, store.StatusQueued, proven.Operation.StateVersion, "")
	require.NoError(t, err)
	finished, err := st.EmergencyIntents().Finish(ctx, proven.Intent.ID, proven.Operation.ID, queued.StateVersion, store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	pgSetEmergencyTerminalAt(t, st, finished.ID, time.Now().UTC().Add(-25*time.Hour))
	versionBefore := finished.StateVersion

	released, err := st.EmergencyIntents().ReleaseLock(ctx, store.ReleaseLockCommand{
		IntentID: proven.Intent.ID, Mode: store.EmergencyReleaseNotAppliedProven,
		Reason: "command never ACK_PERSISTED; operator offline", ObserveTimeout: observe,
	})
	require.NoError(t, err)
	assert.Equal(t, store.EmergencyEffectNotApplied, released.Intent.EffectStatus)
	require.NotNil(t, released.Intent.LockReleasedAt)
	assert.Equal(t, versionBefore+1, released.Operation.StateVersion)

	// Already released / resolved → lock_not_stuck.
	_, err = st.EmergencyIntents().ReleaseLock(ctx, store.ReleaseLockCommand{
		IntentID: proven.Intent.ID, Mode: store.EmergencyReleaseAuditedOverride, Reason: "again", ObserveTimeout: observe,
	})
	require.ErrorIs(t, err, store.ErrLockNotStuck)

	// ── AUDITED_OVERRIDE keeps effect UNKNOWN ──
	override := pgCreateEmergencyViaUOW(t, st, pgEmergencyRevertCommand(t, "pg-def-release", "pg-idem-release-2", "pg-hash-release-2", "api-2"))
	queued2, err := st.Operations().UpdateStatus(ctx, override.Operation.ID, store.StatusQueued, override.Operation.StateVersion, "")
	require.NoError(t, err)
	overrideFinished, err := st.EmergencyIntents().Finish(ctx, override.Intent.ID, override.Operation.ID, queued2.StateVersion, store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	pgSetEmergencyTerminalAt(t, st, overrideFinished.ID, time.Now().UTC().Add(-25*time.Hour))

	overridden, err := st.EmergencyIntents().ReleaseLock(ctx, store.ReleaseLockCommand{
		IntentID: override.Intent.ID, Mode: store.EmergencyReleaseAuditedOverride,
		Reason: "manual takeover verified in cluster", ObserveTimeout: observe,
	})
	require.NoError(t, err)
	assert.Equal(t, store.EmergencyEffectUnknown, overridden.Intent.EffectStatus)
	require.NotNil(t, overridden.Intent.LockReleasedAt)
	// AUDITED_OVERRIDE does not bump the operation state version.
	assert.Equal(t, overrideFinished.StateVersion, overridden.Operation.StateVersion)

	// A released-but-UNKNOWN intent no longer blocks new operations
	// (D7/AUDITED_OVERRIDE release semantics): it is excluded from
	// GetActiveLocksForDefinition and HasUnresolvedForDefinition.
	active, err := st.EmergencyIntents().GetActiveLocksForDefinition(ctx, "pg-def-release")
	require.NoError(t, err)
	releasedIDs := make([]string, 0)
	for _, lock := range active {
		if lock.ID == override.Intent.ID || lock.ID == proven.Intent.ID {
			releasedIDs = append(releasedIDs, lock.ID)
		}
	}
	assert.Empty(t, releasedIDs, "released intents must not hold the target lock")
	hasUnresolved, ids, err := st.EmergencyIntents().HasUnresolvedForDefinition(ctx, "pg-def-release")
	require.NoError(t, err)
	if hasUnresolved {
		for _, id := range ids {
			assert.NotEqual(t, override.Intent.ID, id)
			assert.NotEqual(t, proven.Intent.ID, id)
		}
	}

	// Late result still resolves a released UNKNOWN intent (AC-032-31).
	late, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, pgConvergeCommand(t, override, store.StatusSucceeded, store.EmergencyEffectApplied, overrideFinished.StateVersion, ""))
	require.NoError(t, err)
	assert.True(t, late.Resolved)
	assert.Equal(t, store.EmergencyEffectApplied, late.Intent.EffectStatus)

	// ── Guards ── (the release-global EMERGENCY mutex serializes in-flight
	// ops per definition, so each guard scenario uses its own definition)
	// Non-terminal op → operation_not_terminal.
	pgSeedEmergencyDefinition(t, st, "pg-def-release-nt")
	nonTerminal := pgCreateEmergencyViaUOW(t, st, pgEmergencyRevertCommand(t, "pg-def-release-nt", "pg-idem-release-3", "pg-hash-release-3", "api"))
	_, err = st.Operations().UpdateStatus(ctx, nonTerminal.Operation.ID, store.StatusQueued, nonTerminal.Operation.StateVersion, "")
	require.NoError(t, err)
	_, err = st.EmergencyIntents().ReleaseLock(ctx, store.ReleaseLockCommand{
		IntentID: nonTerminal.Intent.ID, Mode: store.EmergencyReleaseAuditedOverride, Reason: "x", ObserveTimeout: observe,
	})
	require.ErrorIs(t, err, store.ErrInvalidState)

	// Young lock (still inside observe window) → lock_not_stuck.
	pgSeedEmergencyDefinition(t, st, "pg-def-release-young")
	young := pgCreateEmergencyViaUOW(t, st, pgEmergencyRevertCommand(t, "pg-def-release-young", "pg-idem-release-4", "pg-hash-release-4", "api"))
	queued4, err := st.Operations().UpdateStatus(ctx, young.Operation.ID, store.StatusQueued, young.Operation.StateVersion, "")
	require.NoError(t, err)
	youngFinished, err := st.EmergencyIntents().Finish(ctx, young.Intent.ID, young.Operation.ID, queued4.StateVersion, store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	_ = youngFinished
	_, err = st.EmergencyIntents().ReleaseLock(ctx, store.ReleaseLockCommand{
		IntentID: young.Intent.ID, Mode: store.EmergencyReleaseAuditedOverride, Reason: "x", ObserveTimeout: observe,
	})
	require.ErrorIs(t, err, store.ErrLockNotStuck)

	// Release of a REQUIRE_PROMOTION op with a pending_promotion task is
	// rejected as convergence_pending (its operation, if terminal+UNKNOWN,
	// must converge first).
	pgSeedEmergencyDefinition(t, st, "pg-def-release-pending")
	pendingCmd := pgEmergencyCreateCommand(t, "pg-def-release-pending", "pg-idem-release-5", "pg-hash-release-5")
	pending := pgCreateEmergencyViaUOW(t, st, pendingCmd)
	require.NotNil(t, pending.ConvergenceTask)
	queued5, err := st.Operations().UpdateStatus(ctx, pending.Operation.ID, store.StatusQueued, pending.Operation.StateVersion, "")
	require.NoError(t, err)
	pendingFinished, err := st.EmergencyIntents().Finish(ctx, pending.Intent.ID, pending.Operation.ID, queued5.StateVersion, store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	pgSetEmergencyTerminalAt(t, st, pendingFinished.ID, time.Now().UTC().Add(-25*time.Hour))
	_, err = st.EmergencyIntents().ReleaseLock(ctx, store.ReleaseLockCommand{
		IntentID: pending.Intent.ID, Mode: store.EmergencyReleaseAuditedOverride, Reason: "x", ObserveTimeout: observe,
	})
	require.ErrorIs(t, err, store.ErrConvergencePending)

	// Invalid mode → error.
	_, err = st.EmergencyIntents().ReleaseLock(ctx, store.ReleaseLockCommand{
		IntentID: pending.Intent.ID, Mode: "UNSPECIFIED", Reason: "x", ObserveTimeout: observe,
	})
	require.Error(t, err)
}

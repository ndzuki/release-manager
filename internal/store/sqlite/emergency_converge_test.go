package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// convergeCmd builds a ConvergeEmergencyResult command mirroring what
// finishEmergencyResult would send for a succeeded/failed operator result.
func convergeCmd(t *testing.T, result *store.OperationCreationResult, terminal store.OperationStatus, effect store.EmergencyEffectStatus, version int, lastError string) store.EmergencyConvergeCommand {
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

// TestConvergeEmergencyResult_ResultBeatsRunningTransition (REQ-087 AC-087-01,
// D1=D2): the operator result lands while the operation is still queued (the
// queued→running ACK migration has not happened yet). ConvergeEmergencyResult
// must advance queued→running→succeeded inside ONE transaction, without ever
// producing a QUEUED→SUCCEEDED state-transition record.
func TestConvergeEmergencyResult_ResultBeatsRunningTransition(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()
	seedEmergencyDefinition(t, st, "def-converge-race")
	created := createEmergencyViaUOW(t, st, emergencyCreateCommand(t, "def-converge-race", "idem-race", "hash-race", store.EmergencySetReplicas))
	// created op is pending with state_version=1; orchestrator marks queued (v2).
	queued, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	require.Equal(t, store.StatusQueued, queued.Status)
	require.Equal(t, 2, queued.StateVersion)

	cmd := convergeCmd(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, queued.StateVersion, "")
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

	// No illegal queued→succeeded row: read all state transitions and assert
	// each entry's From/To is a legal hop.
	entries, err := st.Timeline().List(ctx, created.Operation.ID, 0, 1<<30)
	require.NoError(t, err)
	sawRunning := false
	sawSucceeded := false
	for _, entry := range entries {
		if entry.Kind != string(store.TimelineEntryStateTransition) {
			continue
		}
		var data store.StateTransitionTimelineData
		require.NoError(t, json.Unmarshal(entry.Data, &data))
		if data.FromState == "queued" && data.ToState == "running" {
			sawRunning = true
		}
		if data.FromState == "running" && data.ToState == "succeeded" {
			sawSucceeded = true
		}
		// The D1 race must never write a queued→terminal migration.
		if data.FromState == "queued" {
			assert.False(t, store.OperationStatus(data.ToState).IsTerminal(),
				"illegal queued→%s terminal hop must never be recorded", data.ToState)
		}
		assert.True(t, store.OperationStatus(data.FromState).CanTransitionTo(store.OperationStatus(data.ToState)),
			"illegal transition %s→%s", data.FromState, data.ToState)
	}
	assert.True(t, sawRunning, "expected a queued→running hop entry")
	assert.True(t, sawSucceeded, "expected a running→succeeded terminal entry")

	// AC-087-02 (order independence): converge on the same op must be an
	// idempotent no-op (Resolved=false) — the store treats a same-effect
	// terminal result as a replay.
	replay, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, convergeCmd(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, result.Operation.StateVersion, ""))
	require.NoError(t, err)
	assert.False(t, replay.Resolved)
	assert.Equal(t, result.Operation.StateVersion, replay.Operation.StateVersion)
}

// TestConvergeEmergencyResult_RunningAlreadyAdvanced: the ACK migration
// already moved the op to running before the result arrived — only the
// running→succeeded terminal hop is applied.
func TestConvergeEmergencyResult_RunningAlreadyAdvanced(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()
	seedEmergencyDefinition(t, st, "def-converge-running")
	created := createEmergencyViaUOW(t, st, emergencyCreateCommand(t, "def-converge-running", "idem-running", "hash-running", store.EmergencySetReplicas))
	queued, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	running, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusRunning, queued.StateVersion, "")
	require.NoError(t, err)
	require.Equal(t, 3, running.StateVersion)

	result, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, convergeCmd(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, running.StateVersion, ""))
	require.NoError(t, err)
	assert.True(t, result.Resolved)
	assert.Equal(t, store.StatusSucceeded, result.Operation.Status)
	assert.Equal(t, running.StateVersion+1, result.Operation.StateVersion)
	assert.Equal(t, store.EmergencyEffectApplied, result.Intent.EffectStatus)
}

// TestConvergeEmergencyResult_FromPending: the result lands while the op is
// still pending (orchestrator queued write raced the stream) — the chain
// pending→queued→running→succeeded is applied.
func TestConvergeEmergencyResult_FromPending(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()
	seedEmergencyDefinition(t, st, "def-converge-pending")
	created := createEmergencyViaUOW(t, st, emergencyCreateCommand(t, "def-converge-pending", "idem-pending", "hash-pending", store.EmergencySetReplicas))
	require.Equal(t, 1, created.Operation.StateVersion)

	result, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, convergeCmd(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, created.Operation.StateVersion, ""))
	require.NoError(t, err)
	assert.True(t, result.Resolved)
	assert.Equal(t, store.StatusSucceeded, result.Operation.Status)
	assert.Equal(t, created.Operation.StateVersion+3, result.Operation.StateVersion)
	assert.Equal(t, store.EmergencyEffectApplied, result.Intent.EffectStatus)
}

// TestConvergeEmergencyResult_FailedWhileQueued: a failed authoritative
// result beating the running migration records failed + NOT_APPLIED (lock
// release matrix D3: failed/NOT_APPLIED releases).
func TestConvergeEmergencyResult_FailedWhileQueued(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()
	seedEmergencyDefinition(t, st, "def-converge-failed")
	created := createEmergencyViaUOW(t, st, emergencyCreateCommand(t, "def-converge-failed", "idem-failed", "hash-failed", store.EmergencySetReplicas))
	queued, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)

	result, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, convergeCmd(t, created, store.StatusFailed, store.EmergencyEffectNotApplied, queued.StateVersion, "helm_failed"))
	require.NoError(t, err)
	assert.True(t, result.Resolved)
	assert.Equal(t, store.StatusFailed, result.Operation.Status)
	assert.Equal(t, store.EmergencyEffectNotApplied, result.Intent.EffectStatus)
	assert.NotNil(t, result.Operation.TerminalAt)
}

// TestConvergeEmergencyResult_TerminalUnknownResolvesOnce (REQ-087 AC-087-03):
// a terminal op whose effect is still UNKNOWN accepts exactly one
// UNKNOWN→APPLIED/NOT_APPLIED resolution; a repeat resolve is an idempotent
// no-op and a different-effect result is rejected.
func TestConvergeEmergencyResult_TerminalUnknownResolvesOnce(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()
	seedEmergencyDefinition(t, st, "def-converge-resolve")
	created := createEmergencyViaUOW(t, st, emergencyCreateCommand(t, "def-converge-resolve", "idem-resolve", "hash-resolve", store.EmergencySetReplicas))
	queued, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	// Time out while still queued → terminal timeout + effect UNKNOWN.
	finished, err := st.EmergencyIntents().Finish(ctx, created.Intent.ID, created.Operation.ID, queued.StateVersion, store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	require.Equal(t, store.StatusTimeout, finished.Status)
	require.Equal(t, 3, finished.StateVersion)

	resolve, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, convergeCmd(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, finished.StateVersion, ""))
	require.NoError(t, err)
	require.True(t, resolve.Resolved)
	assert.Equal(t, store.StatusTimeout, resolve.Operation.Status, "terminal status must not change on effect resolution")
	assert.Equal(t, finished.StateVersion+1, resolve.Operation.StateVersion)
	assert.Equal(t, store.EmergencyEffectApplied, resolve.Intent.EffectStatus)

	// The EMERGENCY_EFFECT_RESOLVED timeline entry was written.
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
	replay, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, convergeCmd(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, resolve.Operation.StateVersion, ""))
	require.NoError(t, err)
	assert.False(t, replay.Resolved)

	// Different effect after resolution: protocol conflict.
	_, err = st.EmergencyIntents().ConvergeEmergencyResult(ctx, convergeCmd(t, created, store.StatusFailed, store.EmergencyEffectNotApplied, resolve.Operation.StateVersion, "x"))
	require.ErrorIs(t, err, store.ErrInvalidState)
}

// TestConvergeEmergencyResult_StaleVersion: an optimistic-lock conflict is
// surfaced instead of silently dropping the result.
func TestConvergeEmergencyResult_StaleVersion(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()
	seedEmergencyDefinition(t, st, "def-converge-stale")
	created := createEmergencyViaUOW(t, st, emergencyCreateCommand(t, "def-converge-stale", "idem-stale", "hash-stale", store.EmergencySetReplicas))
	queued, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)

	// Simulate a concurrent ACK that advanced to running between the
	// operator's read (queued, v2) and its converge call (still v2).
	running, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusRunning, queued.StateVersion, "")
	require.NoError(t, err)
	require.Equal(t, 3, running.StateVersion)

	_, err = st.EmergencyIntents().ConvergeEmergencyResult(ctx, convergeCmd(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, queued.StateVersion, ""))
	require.ErrorIs(t, err, store.ErrOptimisticLock)
}

// TestConvergeEmergencyResult_NonEmergencyRejected: the method only serves
// EMERGENCY operations.
func TestConvergeEmergencyResult_NonEmergencyRejected(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()
	seedEmergencyDefinition(t, st, "def-converge-non-emergency")
	// Create a plain INSTALL operation directly (skip the emergency UOW).
	now := time.Now().UTC()
	op := &store.Operation{
		ID: "op-standard", OperationType: store.OperationInstall, Status: store.StatusPending,
		ReleaseDefinitionID: "def-converge-non-emergency", IdempotencyKey: "k", IdempotencyScope: "org:def",
		RequestHash: "h", CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, st.Operations().Create(ctx, op))
	queued, err := st.Operations().UpdateStatus(ctx, op.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)

	cmd := store.EmergencyConvergeCommand{
		IntentID: "intent-x", OperationID: op.ID, ExpectedStateVersion: queued.StateVersion,
		Status: store.StatusSucceeded, EffectStatus: store.EmergencyEffectApplied,
	}
	_, err = st.EmergencyIntents().ConvergeEmergencyResult(ctx, cmd)
	require.ErrorIs(t, err, store.ErrInvalidState)
}

// setEmergencyTerminalAt back-dates the operation terminal_at column so the
// stuck-lock observe window elapses deterministically.
func setEmergencyTerminalAt(t *testing.T, st *Store, operationID string, when time.Time) {
	t.Helper()
	_, err := st.db.ExecContext(context.Background(),
		`UPDATE operations SET terminal_at = ? WHERE id = ?`, when.UTC().Format(time.RFC3339), operationID)
	require.NoError(t, err)
}

// emergencyRevertCommand builds a REVERT_ON_NEXT_RECONCILE emergency command
// (no convergence task, so releases are not blocked by a pending_promotion
// obligation) targeting the given workload name.
func emergencyRevertCommand(t *testing.T, definitionID, idempotencyKey, requestHash, workloadName string) store.EmergencyCreateCommand {
	t.Helper()
	cmd := emergencyCreateCommand(t, definitionID, idempotencyKey, requestHash, store.EmergencySetReplicas)
	cmd.Intent.WorkloadName = workloadName
	cmd.Intent.Convergence = store.EmergencyRevertOnNextReconcile
	cmd.ConvergenceTask = nil
	return cmd
}

// TestEmergencyStuckLocks_DerivedQuery (REQ-087 AC-087-07): a terminal op
// with UNKNOWN effect past the observe window surfaces as a stuck lock; a
// young one, a resolved one and a released one do not.
func TestEmergencyStuckLocks_DerivedQuery(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()
	seedEmergencyDefinition(t, st, "def-stuck")
	observe := 24 * time.Hour
	now := time.Now().UTC()

	// terminal timeout + UNKNOWN (stuck candidate).
	old := createEmergencyViaUOW(t, st, emergencyRevertCommand(t, "def-stuck", "idem-stuck-1", "hash-stuck-1", "api-1"))
	queued, err := st.Operations().UpdateStatus(ctx, old.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	finished, err := st.EmergencyIntents().Finish(ctx, old.Intent.ID, old.Operation.ID, queued.StateVersion, store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	setEmergencyTerminalAt(t, st, finished.ID, now.Add(-25*time.Hour))

	// fresh terminal UNKNOWN (younger than the window → not stuck yet).
	young := createEmergencyViaUOW(t, st, emergencyRevertCommand(t, "def-stuck", "idem-stuck-2", "hash-stuck-2", "api-2"))
	queued2, err := st.Operations().UpdateStatus(ctx, young.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	youngFinished, err := st.EmergencyIntents().Finish(ctx, young.Intent.ID, young.Operation.ID, queued2.StateVersion, store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	setEmergencyTerminalAt(t, st, youngFinished.ID, now.Add(-time.Hour))

	// resolved terminal (effect APPLIED → not stuck).
	resolved := createEmergencyViaUOW(t, st, emergencyRevertCommand(t, "def-stuck", "idem-stuck-3", "hash-stuck-3", "api-3"))
	queued3, err := st.Operations().UpdateStatus(ctx, resolved.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	applyResult, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, convergeCmd(t, resolved, store.StatusSucceeded, store.EmergencyEffectApplied, queued3.StateVersion, ""))
	require.NoError(t, err)
	require.True(t, applyResult.Resolved)
	setEmergencyTerminalAt(t, st, resolved.Operation.ID, now.Add(-25*time.Hour))

	// released (AUDITED_OVERRIDE) terminal UNKNOWN → not stuck.
	released := createEmergencyViaUOW(t, st, emergencyRevertCommand(t, "def-stuck", "idem-stuck-4", "hash-stuck-4", "api-4"))
	queued4, err := st.Operations().UpdateStatus(ctx, released.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	releasedFinished, err := st.EmergencyIntents().Finish(ctx, released.Intent.ID, released.Operation.ID, queued4.StateVersion, store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	setEmergencyTerminalAt(t, st, releasedFinished.ID, now.Add(-25*time.Hour))
	_, err = st.EmergencyIntents().ReleaseLock(ctx, store.ReleaseLockCommand{
		IntentID: released.Intent.ID, Mode: store.EmergencyReleaseAuditedOverride,
		Reason: "takeover", ObserveTimeout: observe,
	})
	require.NoError(t, err)

	locks, err := st.EmergencyIntents().ListStuckLocks(ctx, store.StuckLockFilter{
		DefinitionID: "def-stuck", ObserveTimeout: observe,
	})
	require.NoError(t, err)
	require.Len(t, locks, 1, "only the old UNKNOWN unreleased lock is stuck")
	assert.Equal(t, old.Intent.ID, locks[0].Intent.ID)
	assert.Equal(t, store.EmergencyEffectUnknown, locks[0].Intent.EffectStatus)
	assert.Contains(t, locks[0].LockPathSummary, "DEPLOYMENT/api-1")
}

// TestEmergencyReleaseLock_ModesAndGuards (REQ-087 AC-087-08/09, §10):
// NOT_APPLIED_PROVEN records NOT_APPLIED + releases; AUDITED_OVERRIDE keeps
// UNKNOWN; non-terminal / young / already-released intents are rejected.
func TestEmergencyReleaseLock_ModesAndGuards(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()
	seedEmergencyDefinition(t, st, "def-release")
	observe := 24 * time.Hour

	// ── NOT_APPLIED_PROVEN ── (revert command: no convergence task)
	proven := createEmergencyViaUOW(t, st, emergencyRevertCommand(t, "def-release", "idem-release-1", "hash-release-1", "api-1"))
	queued, err := st.Operations().UpdateStatus(ctx, proven.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	finished, err := st.EmergencyIntents().Finish(ctx, proven.Intent.ID, proven.Operation.ID, queued.StateVersion, store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	setEmergencyTerminalAt(t, st, finished.ID, time.Now().UTC().Add(-25*time.Hour))
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
	override := createEmergencyViaUOW(t, st, emergencyRevertCommand(t, "def-release", "idem-release-2", "hash-release-2", "api-2"))
	queued2, err := st.Operations().UpdateStatus(ctx, override.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	overrideFinished, err := st.EmergencyIntents().Finish(ctx, override.Intent.ID, override.Operation.ID, queued2.StateVersion, store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	setEmergencyTerminalAt(t, st, overrideFinished.ID, time.Now().UTC().Add(-25*time.Hour))

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
	active, err := st.EmergencyIntents().GetActiveLocksForDefinition(ctx, "def-release")
	require.NoError(t, err)
	releasedIDs := make([]string, 0)
	for _, lock := range active {
		if lock.ID == override.Intent.ID || lock.ID == proven.Intent.ID {
			releasedIDs = append(releasedIDs, lock.ID)
		}
	}
	assert.Empty(t, releasedIDs, "released intents must not hold the target lock")
	hasUnresolved, ids, err := st.EmergencyIntents().HasUnresolvedForDefinition(ctx, "def-release")
	require.NoError(t, err)
	if hasUnresolved {
		for _, id := range ids {
			assert.NotEqual(t, override.Intent.ID, id)
			assert.NotEqual(t, proven.Intent.ID, id)
		}
	}

	// Late result still resolves a released UNKNOWN intent (AC-032-31).
	late, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, convergeCmd(t, override, store.StatusSucceeded, store.EmergencyEffectApplied, overrideFinished.StateVersion, ""))
	require.NoError(t, err)
	assert.True(t, late.Resolved)
	assert.Equal(t, store.EmergencyEffectApplied, late.Intent.EffectStatus)

	// ── Guards ── (the release-global EMERGENCY mutex serializes in-flight
	// ops per definition, so each guard scenario uses its own definition)
	// Non-terminal op → operation_not_terminal.
	seedEmergencyDefinition(t, st, "def-release-nt")
	nonTerminal := createEmergencyViaUOW(t, st, emergencyRevertCommand(t, "def-release-nt", "idem-release-3", "hash-release-3", "api"))
	_, err = st.Operations().UpdateStatus(ctx, nonTerminal.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	_, err = st.EmergencyIntents().ReleaseLock(ctx, store.ReleaseLockCommand{
		IntentID: nonTerminal.Intent.ID, Mode: store.EmergencyReleaseAuditedOverride, Reason: "x", ObserveTimeout: observe,
	})
	require.ErrorIs(t, err, store.ErrInvalidState)

	// Young lock (still inside observe window) → lock_not_stuck.
	seedEmergencyDefinition(t, st, "def-release-young")
	young := createEmergencyViaUOW(t, st, emergencyRevertCommand(t, "def-release-young", "idem-release-4", "hash-release-4", "api"))
	queued4, err := st.Operations().UpdateStatus(ctx, young.Operation.ID, store.StatusQueued, 1, "")
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
	seedEmergencyDefinition(t, st, "def-release-pending")
	pendingCmd := emergencyCreateCommand(t, "def-release-pending", "idem-release-5", "hash-release-5", store.EmergencySetReplicas)
	pending := createEmergencyViaUOW(t, st, pendingCmd)
	require.NotNil(t, pending.ConvergenceTask)
	queued5, err := st.Operations().UpdateStatus(ctx, pending.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	pendingFinished, err := st.EmergencyIntents().Finish(ctx, pending.Intent.ID, pending.Operation.ID, queued5.StateVersion, store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	setEmergencyTerminalAt(t, st, pendingFinished.ID, time.Now().UTC().Add(-25*time.Hour))
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

// TestConvergeEmergencyResult_FailedLastErrorWritesErrorTimeline: a failed
// converge records the sanitized ERROR timeline entry (lock-release matrix
// evidence for the "failed result" path).
func TestConvergeEmergencyResult_FailedLastErrorWritesErrorTimeline(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()
	seedEmergencyDefinition(t, st, "def-converge-err")
	created := createEmergencyViaUOW(t, st, emergencyCreateCommand(t, "def-converge-err", "idem-err", "hash-err", store.EmergencySetReplicas))
	queued, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	running, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusRunning, queued.StateVersion, "")
	require.NoError(t, err)
	result, err := st.EmergencyIntents().ConvergeEmergencyResult(ctx, convergeCmd(t, created, store.StatusFailed, store.EmergencyEffectNotApplied, running.StateVersion, "helm_failed: boom"))
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
	_ = strings.TrimSpace
}

// TestConvergeEmergencyResult_ConcurrentCalls: many concurrent authoritative
// results for the same operation converge exactly once — the CAS + single-tx
// hop advance guarantees a single terminal state, no duplicate timeline rows
// and no lost result (REQ-087 D2 concurrency / AC-087-12).
func TestConvergeEmergencyResult_ConcurrentCalls(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()
	seedEmergencyDefinition(t, st, "def-converge-concurrent")
	created := createEmergencyViaUOW(t, st, emergencyCreateCommand(t, "def-converge-concurrent", "idem-concurrent", "hash-concurrent", store.EmergencySetReplicas))
	queued, err := st.Operations().UpdateStatus(ctx, created.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	cmd := convergeCmd(t, created, store.StatusSucceeded, store.EmergencyEffectApplied, queued.StateVersion, "")

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

// TestActiveLockFactSourceTerminalEffectAuthority (REQ-087 AC-087-11 / D7):
// a dispatch-failed op finished with effect NOT_APPLIED while delivery_status
// is still pending no longer counts as holding the target lock, and the read
// projection surfaces the DB effect instead of NOT_STARTED.
func TestActiveLockFactSourceTerminalEffectAuthority(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()
	seedEmergencyDefinition(t, st, "def-lock-fact")
	created := createEmergencyViaUOW(t, st, emergencyCreateCommand(t, "def-lock-fact", "idem-fact", "hash-fact", store.EmergencySetReplicas))
	// Orchestrator dispatch failure: Finish(failed, NOT_APPLIED) while the
	// intent delivery_status never left pending.
	finished, err := st.EmergencyIntents().Finish(ctx, created.Intent.ID, created.Operation.ID, 1,
		store.StatusFailed, store.EmergencyEffectNotApplied, "delivery_failed", nil, nil)
	require.NoError(t, err)
	require.Equal(t, store.StatusFailed, finished.Status)
	intent, err := st.EmergencyIntents().GetByOperationID(ctx, created.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", intent.DeliveryStatus, "delivery never advanced (dispatch failed)")

	// Read projection: terminal op surfaces DB effect, not NOT_STARTED.
	op, err := st.Operations().Get(ctx, created.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, store.EmergencyEffectNotApplied, op.EffectStatus)

	// Lock fact source: the intent no longer holds the target lock.
	active, err := st.EmergencyIntents().GetActiveLocksForDefinition(ctx, "def-lock-fact")
	require.NoError(t, err)
	for _, lock := range active {
		assert.NotEqual(t, created.Intent.ID, lock.ID, "resolved terminal intent must not hold the lock")
	}
	hasUnresolved, ids, err := st.EmergencyIntents().HasUnresolvedForDefinition(ctx, "def-lock-fact")
	require.NoError(t, err)
	assert.False(t, hasUnresolved)
	assert.Empty(t, ids)

	// A new emergency on the same target is accepted.
	next := emergencyCreateCommand(t, "def-lock-fact", "idem-fact-2", "hash-fact-2", store.EmergencySetReplicas)
	result, err := st.OperationCreationUnitOfWork()(ctx, store.OperationCreationRequest{Operation: next.Operation, Emergency: &next})
	require.NoError(t, err)
	assert.False(t, result.Replayed)
}

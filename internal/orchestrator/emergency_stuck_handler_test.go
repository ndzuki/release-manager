package orchestrator

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/trust"
)

// stuckServiceWithAudit builds an orchestrator Service over the given store
// with an audit emitter and the store-backed authorizer so the stuck-lock RPC
// handlers can be exercised (authorization + audit assertions).
func stuckServiceWithAudit(t *testing.T, st store.Store) (*Service, *audit.Emitter) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	emitter := audit.NewEmitter(st.AuditEvents(), logger, audit.EmitterConfig{
		BufferSize: 16, FlushInterval: time.Hour, BatchSize: 16, SpoolPath: t.TempDir() + "/audit.jsonl",
	})
	uowStore, ok := st.(interface {
		store.Store
		OperationCreationUnitOfWork() store.OperationCreationUnitOfWork
	})
	require.True(t, ok)
	svc := NewService(st, trust.NewStubVerifier(st.Verifications(), nil, logger), "staging",
		emitter, &recordingEmergencyDispatcher{}, authorization.NewStoreAuthorizer(st),
		uowStore.OperationCreationUnitOfWork(), logger)
	return svc, emitter
}

// seedStuckLockIdentity seeds the release-admin / viewer members for the
// def-001 fixture (seedDefinition already created the customer/binding/def).
// seedStuckLockIdentity seeds the release-admin / viewer members for the
// def-001 fixture (seedDefinition already created the customer/binding/def
// and the user-viewer user; user-001 is already a release_admin member).
func seedStuckLockIdentity(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, st.Users().Create(ctx, &store.User{ID: "release-admin", Username: "release-admin", Status: store.UserActive}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: "org-001", UserID: "release-admin", Role: store.RoleReleaseAdmin}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: "org-001", UserID: "user-viewer", Role: store.RoleViewer}))
}

func viewerContext() context.Context {
	return authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-viewer", OrganizationID: "org-001", Roles: []string{string(store.RoleViewer)},
	})
}

// makeStuckLock creates a terminal-timeout EMERGENCY intent on def-001 whose
// effect stays UNKNOWN and back-dates terminal_at past the observe window so
// the lock is derived as stuck.
// makeStuckLockOn creates a terminal-timeout EMERGENCY intent on the given
// definition whose effect stays UNKNOWN and back-dates terminal_at past the
// observe window so the lock is derived as stuck.
func makeStuckLockOn(t *testing.T, st store.Store, definitionID, workloadName, key string) *store.OperationCreationResult {
	t.Helper()
	created := createEmergencyPendingOn(t, st, definitionID, workloadName, key)
	queued, err := st.Operations().UpdateStatus(t.Context(), created.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	finished, err := st.EmergencyIntents().Finish(t.Context(), created.Intent.ID, created.Operation.ID, queued.StateVersion,
		store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	_, err = sqliteExec(t, st).ExecContext(t.Context(), `UPDATE operations SET terminal_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-25*time.Hour).Format(time.RFC3339), finished.ID)
	require.NoError(t, err)
	return created
}

func makeStuckLock(t *testing.T, st store.Store, key string) *store.OperationCreationResult {
	t.Helper()
	return makeStuckLockOn(t, st, "def-001", "api", key)
}

// seedExtraDefinition inserts an additional active release definition on the
// existing cust-001 customer (bindings are per customer, so org-001 keeps its
// authorization scope).
func seedExtraDefinition(t *testing.T, st store.Store, id string) {
	t.Helper()
	require.NoError(t, st.Definitions().Create(t.Context(), &store.ReleaseDefinition{
		ID: id, Name: id, CustomerID: "cust-001", ClusterID: "cls-001", Namespace: "default",
		ReleaseName: id, Status: store.DefStatusActive, CreatedBy: "test",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, nil))
}

// TestListStuckLocksHandler (REQ-087 AC-087-07/10): release_admin lists the
// derived stuck locks; viewers are denied.
func TestListStuckLocksHandler(t *testing.T) {
	svc, st, _ := setupService(t)
	seedDefinition(t, st)
	seedStuckLockIdentity(t, st)
	stuck := makeStuckLockOn(t, st, "def-001", "api", "stuck-list-1")
	makeStuckLockOn(t, st, "def-001", "web", "stuck-list-2") // second stuck lock (different target)

	// Scope to the definition — both stuck locks are on def-001.
	resp, err := svc.ListStuckLocks(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.ListStuckLocksRequest{
		ReleaseDefinitionId: "def-001",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetLocks(), 2)
	intentIDs := make(map[string]bool, 2)
	for _, lock := range resp.Msg.GetLocks() {
		intentIDs[lock.GetIntentId()] = true
		assert.Equal(t, "def-001", lock.GetReleaseDefinitionId())
		assert.Equal(t, orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_UNKNOWN, lock.GetEffectStatus())
		assert.NotEmpty(t, lock.GetLockPathSummary())
		assert.NotNil(t, lock.GetTerminalAt())
		assert.NotNil(t, lock.GetStuckSince())
		assert.Equal(t, "24h0m0s", lock.GetObserveTimeoutDisplay())
	}
	assert.Len(t, intentIDs, 2)
	assert.True(t, intentIDs[stuck.Intent.ID])

	// Viewers are denied (AC-087-10).
	_, err = svc.ListStuckLocks(viewerContext(), connect.NewRequest(&orchestratorv1.ListStuckLocksRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// Unauthenticated is rejected.
	_, err = svc.ListStuckLocks(context.Background(), connect.NewRequest(&orchestratorv1.ListStuckLocksRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestReleaseEmergencyLockHandler_NotAppliedProven (REQ-087 AC-087-08): an
// operator proves the command never took effect → effect NOT_APPLIED + lock
// released + audited.
func TestReleaseEmergencyLockHandler_NotAppliedProven(t *testing.T) {
	baseSvc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedStuckLockIdentity(t, st)
	stuck := makeStuckLock(t, st, "stuck-release-proven")

	svc, emitter := stuckServiceWithAudit(t, st)
	_ = baseSvc
	resp, err := svc.ReleaseEmergencyLock(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.ReleaseEmergencyLockRequest{
		IntentId: stuck.Intent.ID,
		Mode:     orchestratorv1.ReleaseMode_NOT_APPLIED_PROVEN,
		Reason:   "command was never ACK_PERSISTED; operator offline",
		Evidence: "operator session last seen 3h ago; no in-flight command",
	}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetLockReleased())
	assert.Equal(t, orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_NOT_APPLIED, resp.Msg.GetEffectStatus())
	require.NotEmpty(t, resp.Msg.GetAuditEventId())

	intent, err := st.EmergencyIntents().GetByID(t.Context(), stuck.Intent.ID)
	require.NoError(t, err)
	assert.Equal(t, store.EmergencyEffectNotApplied, intent.EffectStatus)
	require.NotNil(t, intent.LockReleasedAt)

	// The released lock no longer appears in the stuck list.
	listResp, err := svc.ListStuckLocks(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.ListStuckLocksRequest{}))
	require.NoError(t, err)
	for _, lock := range listResp.Msg.GetLocks() {
		assert.NotEqual(t, stuck.Intent.ID, lock.GetIntentId())
	}

	// Audit event with actor/mode/evidence/reason/before-after effect.
	require.NoError(t, emitter.Shutdown(t.Context()))
	events, err := st.AuditEvents().ListByResource(t.Context(), "operation", stuck.Operation.ID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "emergency_lock_release", events[0].Action)
	assert.Equal(t, "NOT_APPLIED_PROVEN", events[0].Status)
	assert.Equal(t, "release-admin", events[0].ActorID)
	assert.Contains(t, events[0].ChangeSummary, "reason=command was never ACK_PERSISTED")
	assert.Equal(t, "UNKNOWN", events[0].Metadata["effect_before"])
	assert.Equal(t, "NOT_APPLIED", events[0].Metadata["effect_after"])
}

// TestReleaseEmergencyLockHandler_AuditedOverride (REQ-087 AC-087-09): an
// override releases the lock while the effect stays UNKNOWN and is audited;
// a late result can still resolve the effect afterwards.
func TestReleaseEmergencyLockHandler_AuditedOverride(t *testing.T) {
	svc, st, _ := setupService(t)
	seedDefinition(t, st)
	seedStuckLockIdentity(t, st)
	stuck := makeStuckLock(t, st, "stuck-release-override")

	service, emitter := stuckServiceWithAudit(t, st)
	_ = svc
	resp, err := service.ReleaseEmergencyLock(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.ReleaseEmergencyLockRequest{
		IntentId: stuck.Intent.ID,
		Mode:     orchestratorv1.ReleaseMode_AUDITED_OVERRIDE,
		Reason:   "manual takeover after on-call verification",
	}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetLockReleased())
	assert.Equal(t, orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_UNKNOWN, resp.Msg.GetEffectStatus())

	intent, err := st.EmergencyIntents().GetByID(t.Context(), stuck.Intent.ID)
	require.NoError(t, err)
	assert.Equal(t, store.EmergencyEffectUnknown, intent.EffectStatus)
	require.NotNil(t, intent.LockReleasedAt)

	require.NoError(t, emitter.Shutdown(t.Context()))
	events, err := st.AuditEvents().ListByResource(t.Context(), "operation", stuck.Operation.ID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "AUDITED_OVERRIDE", events[0].Status)
	assert.Equal(t, "UNKNOWN", events[0].Metadata["effect_after"])
}

// TestReleaseEmergencyLockHandler_ErrorModel (REQ-087 AC-087-10 / §10): every
// documented failure maps to its connect code.
func TestReleaseEmergencyLockHandler_ErrorModel(t *testing.T) {
	svc, st, _ := setupService(t)
	seedDefinition(t, st)
	seedStuckLockIdentity(t, st)
	stuck := makeStuckLock(t, st, "stuck-release-errors")

	service, _ := stuckServiceWithAudit(t, st)
	_ = svc

	adminCtx := emergencyAdminContext()
	// Missing reason → InvalidArgument reason_required.
	_, err := service.ReleaseEmergencyLock(adminCtx, connect.NewRequest(&orchestratorv1.ReleaseEmergencyLockRequest{
		IntentId: stuck.Intent.ID, Mode: orchestratorv1.ReleaseMode_AUDITED_OVERRIDE,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// UNSPECIFIED mode → InvalidArgument release_mode_unspecified.
	_, err = service.ReleaseEmergencyLock(adminCtx, connect.NewRequest(&orchestratorv1.ReleaseEmergencyLockRequest{
		IntentId: stuck.Intent.ID, Mode: orchestratorv1.ReleaseMode_RELEASE_MODE_UNSPECIFIED, Reason: "x",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// Unknown intent → NotFound intent_not_found.
	_, err = service.ReleaseEmergencyLock(adminCtx, connect.NewRequest(&orchestratorv1.ReleaseEmergencyLockRequest{
		IntentId: "missing-intent", Mode: orchestratorv1.ReleaseMode_AUDITED_OVERRIDE, Reason: "x",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	// Viewer (non-admin) → PermissionDenied.
	_, err = service.ReleaseEmergencyLock(viewerContext(), connect.NewRequest(&orchestratorv1.ReleaseEmergencyLockRequest{
		IntentId: stuck.Intent.ID, Mode: orchestratorv1.ReleaseMode_AUDITED_OVERRIDE, Reason: "x",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// Non-terminal intent → FailedPrecondition operation_not_terminal.
	seedExtraDefinition(t, st, "def-live")
	live := createEmergencyPendingOn(t, st, "def-live", "api", "stuck-release-errors-live")
	queued, err := st.Operations().UpdateStatus(t.Context(), live.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	_ = queued
	_, err = service.ReleaseEmergencyLock(adminCtx, connect.NewRequest(&orchestratorv1.ReleaseEmergencyLockRequest{
		IntentId: live.Intent.ID, Mode: orchestratorv1.ReleaseMode_AUDITED_OVERRIDE, Reason: "x",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	// Young lock (inside the observe window) → FailedPrecondition lock_not_stuck.
	seedExtraDefinition(t, st, "def-young")
	young := createEmergencyPendingOn(t, st, "def-young", "api", "stuck-release-errors-young")
	youngQueued, err := st.Operations().UpdateStatus(t.Context(), young.Operation.ID, store.StatusQueued, 1, "")
	require.NoError(t, err)
	_, err = st.EmergencyIntents().Finish(t.Context(), young.Intent.ID, young.Operation.ID, youngQueued.StateVersion,
		store.StatusTimeout, store.EmergencyEffectUnknown, "operation_timeout", nil, nil)
	require.NoError(t, err)
	_, err = service.ReleaseEmergencyLock(adminCtx, connect.NewRequest(&orchestratorv1.ReleaseEmergencyLockRequest{
		IntentId: young.Intent.ID, Mode: orchestratorv1.ReleaseMode_AUDITED_OVERRIDE, Reason: "x",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// TestScanStuckEmergencyLocksAlertsOnce (REQ-087 AC-087-07): the background
// scan derives stuck locks across the whole system, alerts (audit) each new
// stuck lock exactly once, and forgets locks that stop being stuck.
func TestScanStuckEmergencyLocksAlertsOnce(t *testing.T) {
	svc, st, _ := setupService(t)
	seedDefinition(t, st)
	seedStuckLockIdentity(t, st)
	stuck := makeStuckLock(t, st, "scan-stuck-1")

	service, emitter := stuckServiceWithAudit(t, st)
	_ = svc
	alerted := NewAlertedStuckLocks()
	require.Equal(t, 1, service.ScanStuckEmergencyLocks(t.Context(), alerted))
	require.Equal(t, 1, service.ScanStuckEmergencyLocks(t.Context(), alerted), "second scan must not re-alert the same lock")
	require.NoError(t, emitter.Shutdown(t.Context()))

	events, err := st.AuditEvents().ListByResource(t.Context(), "operation", stuck.Operation.ID)
	require.NoError(t, err)
	require.Len(t, events, 1, "only one lock_stuck audit per stuck lock")
	assert.Equal(t, "emergency_lock_stuck", events[0].Action)
	assert.Equal(t, "stuck", events[0].Status)
	assert.Equal(t, store.AuditActorSystem, events[0].ActorKind)

	// After the lock is released it stops being stuck; the alert set forgets it.
	_, err = service.ReleaseEmergencyLock(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.ReleaseEmergencyLockRequest{
		IntentId: stuck.Intent.ID, Mode: orchestratorv1.ReleaseMode_AUDITED_OVERRIDE, Reason: "cleanup",
	}))
	require.NoError(t, err)
	require.Equal(t, 0, service.ScanStuckEmergencyLocks(t.Context(), alerted))
}

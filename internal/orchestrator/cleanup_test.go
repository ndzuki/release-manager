package orchestrator

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestCleanupServiceDeletesOnlyPrepareSessionMetadataPastRetention(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	ctx := context.Background()
	now := time.Now().UTC()

	oldDefinition := createCleanupDefinition(t, st, "cleanup-old")
	recentDefinition := createCleanupDefinition(t, st, "cleanup-recent")
	for _, session := range []*store.PrepareSession{
		{
			TokenHash: "cleanup-expired-old", ActorUserID: "user", OrganizationID: "org",
			ReleaseDefinitionID: oldDefinition.ID, TaskIDs: []string{"task-old"},
			LockedPaths: []string{"/replicas"}, LockedPathHash: "old",
			ExpiresAt: now.Add(-25 * time.Hour), CreatedAt: now.Add(-26 * time.Hour),
		},
		{
			TokenHash: "cleanup-expired-recent", ActorUserID: "user", OrganizationID: "org",
			ReleaseDefinitionID: recentDefinition.ID, TaskIDs: []string{"task-recent"},
			LockedPaths: []string{"/replicas"}, LockedPathHash: "recent",
			ExpiresAt: now.Add(-time.Hour), CreatedAt: now.Add(-2 * time.Hour),
		},
	} {
		require.NoError(t, st.PrepareSessions().Create(ctx, session, 0))
	}

	service := NewCleanupService(st, DefaultRetentionConfig(), slog.New(slog.DiscardHandler))
	// Prepare-session cleanup runs on its own ticker (v16 Step 5), not inside
	// the artifact GC phases.
	deleted, err := service.runPrepareSessionGC(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	_, err = st.PrepareSessions().Get(ctx, "cleanup-expired-old")
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.PrepareSessions().Get(ctx, "cleanup-expired-recent")
	require.NoError(t, err)
	_, err = st.Definitions().Get(ctx, oldDefinition.ID)
	require.NoError(t, err)
}

func TestDefaultRetentionConfigValidates(t *testing.T) {
	require.NoError(t, DefaultRetentionConfig().Validate())
	assert.Equal(t, 6, DefaultRetentionConfig().GCIntervalHours)
	assert.Equal(t, 24, DefaultRetentionConfig().PrepareSessionHours)
	assert.Equal(t, 10, DefaultRetentionConfig().PrepareSessionGCIntervalMinutes)
}

func createCleanupDefinition(t *testing.T, st *sqlitestore.Store, id string) *store.ReleaseDefinition {
	t.Helper()
	definition := &store.ReleaseDefinition{
		ID: id, Name: id, CustomerID: "customer-" + id, ClusterID: "cluster-" + id,
		ReleaseName: id, Status: store.DefStatusActive,
	}
	require.NoError(t, st.Definitions().Create(context.Background(), definition, nil))
	return definition
}

func createCleanupBundle(ctx context.Context, t *testing.T, st *sqlitestore.Store, name string, status store.BundleStatus) *store.ReleaseBundle {
	t.Helper()
	bundle := &store.ReleaseBundle{
		ID: name + "-" + uuid.NewString(), Name: name,
		DigestAlg: "sha256", DigestValue: uuid.NewString(), Status: status,
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))
	return bundle
}

// ageBundle ages a bundle past the 90-day retention window (100 days).
func ageBundle(t *testing.T, st *sqlitestore.Store, id string) {
	t.Helper()
	_, err := st.DB().ExecContext(context.Background(),
		`UPDATE release_bundles SET created_at=? WHERE id=?`,
		time.Now().UTC().Add(-100*24*time.Hour).Format(time.RFC3339), id)
	require.NoError(t, err)
}

func ageArchivedBundle(t *testing.T, st *sqlitestore.Store, id string, age time.Duration) {
	t.Helper()
	_, err := st.DB().ExecContext(context.Background(),
		`UPDATE release_bundles SET archived_at=? WHERE id=?`,
		time.Now().UTC().Add(-age).Format(time.RFC3339), id)
	require.NoError(t, err)
}

func newCleanupService(t *testing.T, st *sqlitestore.Store) *CleanupService {
	t.Helper()
	return NewCleanupService(st, DefaultRetentionConfig(), slog.New(slog.DiscardHandler))
}

// AC-069-01/02/03/04: Phase 1 archives expired bundles with no active
// Definition and no non-terminal Operation references; draft/disabled
// Definition references do not protect.
func TestRunGCPhase1ArchiveAndReferenceProtection(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	ctx := context.Background()
	service := newCleanupService(t, st)

	// Eligible: expired, unreferenced.
	expired := createCleanupBundle(ctx, t, st, "gc-p1-expired", store.BundleValidated)
	ageBundle(t, st, expired.ID)

	// Protected: referenced by an active definition.
	protectedByDef := createCleanupBundle(ctx, t, st, "gc-p1-active-def", store.BundleValidated)
	ageBundle(t, st, protectedByDef.ID)
	def := createCleanupDefinition(t, st, "gc-p1-def")
	_, err := st.Definitions().SetCurrentBundle(ctx, def.ID, protectedByDef.ID)
	require.NoError(t, err)

	// Protected: referenced by a non-terminal operation.
	protectedByOp := createCleanupBundle(ctx, t, st, "gc-p1-running-op", store.BundleValidated)
	ageBundle(t, st, protectedByOp.ID)
	opDef := createCleanupDefinition(t, st, "gc-p1-op-def")
	require.NoError(t, st.Operations().Create(ctx, &store.Operation{
		ID: uuid.NewString(), OperationType: store.OperationInstall,
		Status: store.StatusRunning, ReleaseDefinitionID: opDef.ID,
		IdempotencyKey: uuid.NewString(), RequestHash: "p1-running",
		BundleID: protectedByOp.ID, StateVersion: 1,
	}))

	// Draft definition reference does not protect (AC-069-04).
	draftRef := createCleanupBundle(ctx, t, st, "gc-p1-draft-def", store.BundleValidated)
	ageBundle(t, st, draftRef.ID)
	err = st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: "gc-p1-draft-def", Name: "gc-p1-draft-def", CustomerID: "customer-gc-p1-draft-def",
		ClusterID: "cluster-gc-p1-draft-def", ReleaseName: "gc-p1-draft-def",
		Status: store.DefStatusDraft,
	}, nil)
	require.NoError(t, err)
	_, err = st.Definitions().SetCurrentBundle(ctx, "gc-p1-draft-def", draftRef.ID)
	require.NoError(t, err)

	// Too recent: not archived.
	recent := createCleanupBundle(ctx, t, st, "gc-p1-recent", store.BundleValidated)

	terminalStates := []store.OperationStatus{
		store.StatusSucceeded, store.StatusFailed, store.StatusCancelled, store.StatusTimeout,
	}
	ids, err := st.Bundles().ListForArchive(ctx, 90, terminalStates)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{expired.ID, draftRef.ID}, ids)

	resp, errs, err := service.runGC(ctx)
	require.NoError(t, err)
	require.Empty(t, errs)

	got, err := st.Bundles().Get(ctx, expired.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleArchived, got.Status)
	require.NotNil(t, got.ArchivedFromStatus)
	assert.Equal(t, store.BundleValidated, *got.ArchivedFromStatus)

	got, err = st.Bundles().Get(ctx, protectedByDef.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleValidated, got.Status)

	got, err = st.Bundles().Get(ctx, protectedByOp.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleValidated, got.Status)

	got, err = st.Bundles().Get(ctx, draftRef.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleArchived, got.Status)

	got, err = st.Bundles().Get(ctx, recent.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleValidated, got.Status)

	assert.Equal(t, int64(0), resp.GetDeletedBundles())
	assert.Empty(t, resp.GetErrors())
}

// AC-069-05/47: Phase 2 physically deletes archived bundles past the archive
// grace; definition references are NULLed, orphaned candidates are marked, and
// operation bundle_id history is preserved.
func TestRunGCPhase2PhysicalDeleteClearsReferences(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	ctx := context.Background()
	service := newCleanupService(t, st)

	bundle := createCleanupBundle(ctx, t, st, "gc-p2-delete", store.BundleValidated)
	ageBundle(t, st, bundle.ID)
	// A draft definition reference does not protect the bundle from archival
	// (AC-069-04) but its current_bundle_id must be NULLed on delete (AC-069-47).
	def := createCleanupDefinition(t, st, "gc-p2-def")
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: "gc-p2-draft-def", Name: "gc-p2-draft-def", CustomerID: "c", ClusterID: "k",
		ReleaseName: "gc-p2-draft-def", Status: store.DefStatusDraft,
	}, nil))
	_, err := st.Definitions().SetCurrentBundle(ctx, def.ID, bundle.ID)
	require.NoError(t, err)
	_, err = st.Bundles().Archive(ctx, []string{bundle.ID})
	require.NoError(t, err)
	ageArchivedBundle(t, st, bundle.ID, 40*24*time.Hour) // past 30d grace

	candidate := &store.CandidateArtifact{
		ArtifactType: store.ArtifactImage, Ref: "gc-p2-ref", Digest: uuid.NewString(),
	}
	require.NoError(t, st.CandidateArtifacts().Create(ctx, candidate))
	require.NoError(t, st.CandidateArtifacts().LinkToBundle(ctx, candidate.ID, bundle.ID))

	op := &store.Operation{
		ID: uuid.NewString(), OperationType: store.OperationInstall,
		Status: store.StatusSucceeded, ReleaseDefinitionID: def.ID,
		IdempotencyKey: uuid.NewString(), RequestHash: "p2-history",
		BundleID: bundle.ID, StateVersion: 1,
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	resp, errs, err := service.runGC(ctx)
	require.NoError(t, err)
	require.Empty(t, errs)
	assert.Equal(t, int64(1), resp.GetDeletedBundles())

	_, err = st.Bundles().Get(ctx, bundle.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)

	gotDef, err := st.Definitions().Get(ctx, def.ID)
	require.NoError(t, err)
	assert.Empty(t, gotDef.CurrentBundleID)

	gotCandidate, err := st.CandidateArtifacts().Get(ctx, candidate.ID)
	require.NoError(t, err)
	require.NotNil(t, gotCandidate.OrphanedAt, "candidate without remaining links must be marked orphaned")

	gotOp, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, bundle.ID, gotOp.BundleID, "operation history must keep its bundle_id")
}

// AC-069-15/16: Phase 3 deletes orphaned candidates past their TTL and keeps
// candidates with active bundle links.
func TestRunGCPhase3OrphanCandidates(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	ctx := context.Background()
	service := newCleanupService(t, st)

	oldOrphan := &store.CandidateArtifact{ArtifactType: store.ArtifactImage, Ref: "gc-p3-old", Digest: uuid.NewString()}
	require.NoError(t, st.CandidateArtifacts().Create(ctx, oldOrphan))
	oldTime := time.Now().UTC().Add(-40 * 24 * time.Hour).Format(time.RFC3339)
	_, err := st.DB().ExecContext(ctx,
		`UPDATE candidate_artifacts SET created_at=?, orphaned_at=? WHERE id=?`,
		oldTime, oldTime, oldOrphan.ID)
	require.NoError(t, err)

	linked := &store.CandidateArtifact{ArtifactType: store.ArtifactChart, Ref: "gc-p3-linked", Digest: uuid.NewString()}
	require.NoError(t, st.CandidateArtifacts().Create(ctx, linked))
	bundle := createCleanupBundle(ctx, t, st, "gc-p3-linked-bundle", store.BundleValidated)
	require.NoError(t, st.CandidateArtifacts().LinkToBundle(ctx, linked.ID, bundle.ID))

	resp, errs, err := service.runGC(ctx)
	require.NoError(t, err)
	require.Empty(t, errs)
	assert.Equal(t, int64(1), resp.GetDeletedCandidates())

	_, err = st.CandidateArtifacts().Get(ctx, oldOrphan.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.CandidateArtifacts().Get(ctx, linked.ID)
	require.NoError(t, err)
}

// AC-069-23/24/25/26: Phase 4 deletes expired preflight lifecycle rows
// (operation-terminal TTL, orphan TTL, JOIN fallback) and preserves rows linked
// to non-terminal operations.
func TestRunGCPhase4PreflightLifecycle(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	ctx := context.Background()
	service := newCleanupService(t, st)
	now := time.Now().UTC()
	// Default retention: preflight_retention_days=90, orphan TTL=7d.
	terminalOld := now.Add(-100 * 24 * time.Hour).Format(time.RFC3339)
	orphanOld := now.Add(-10 * 24 * time.Hour).Format(time.RFC3339)

	// Terminal operation with backfilled operation_terminal_at -> deleted.
	terminalOpID := uuid.NewString()
	_, err := st.PreflightLifecycles().CreateOrReset(ctx, terminalOpID)
	require.NoError(t, err)
	_, err = st.DB().ExecContext(ctx,
		`UPDATE preflight_lifecycles SET operation_terminal_at=?, created_at=?, updated_at=? WHERE operation_id=?`,
		terminalOld, terminalOld, terminalOld, terminalOpID)
	require.NoError(t, err)

	// Orphan row (operation_id IS NULL) past orphan TTL -> deleted.
	orphanID := uuid.NewString()
	_, err = st.DB().ExecContext(ctx,
		`INSERT INTO preflight_lifecycles (id, operation_id, operation_terminal_at, stages, overall, created_at, updated_at)
		 VALUES (?, NULL, NULL, '', 'passed', ?, ?)`, orphanID, orphanOld, orphanOld)
	require.NoError(t, err)

	// JOIN fallback: operation is terminal with terminal_at but the lifecycle
	// row has no operation_terminal_at -> deleted via operations.terminal_at.
	fallbackOpID := uuid.NewString()
	fallbackDef := createCleanupDefinition(t, st, "gc-p4-fallback-def")
	require.NoError(t, st.Operations().Create(ctx, &store.Operation{
		ID: fallbackOpID, OperationType: store.OperationInstall,
		Status: store.StatusSucceeded, ReleaseDefinitionID: fallbackDef.ID,
		IdempotencyKey: uuid.NewString(), RequestHash: "p4-fallback",
		StateVersion: 1,
	}))
	_, err = st.DB().ExecContext(ctx, `UPDATE operations SET terminal_at=? WHERE id=?`, terminalOld, fallbackOpID)
	require.NoError(t, err)
	_, err = st.PreflightLifecycles().CreateOrReset(ctx, fallbackOpID)
	require.NoError(t, err)

	// Non-terminal operation -> preserved.
	runningOpID := uuid.NewString()
	runningDef := createCleanupDefinition(t, st, "gc-p4-running-def")
	require.NoError(t, st.Operations().Create(ctx, &store.Operation{
		ID: runningOpID, OperationType: store.OperationInstall,
		Status: store.StatusRunning, ReleaseDefinitionID: runningDef.ID,
		IdempotencyKey: uuid.NewString(), RequestHash: "p4-running",
		StateVersion: 1,
	}))
	_, err = st.PreflightLifecycles().CreateOrReset(ctx, runningOpID)
	require.NoError(t, err)

	resp, errs, err := service.runGC(ctx)
	require.NoError(t, err)
	require.Empty(t, errs)
	assert.Equal(t, int64(3), resp.GetDeletedPreflights())

	_, err = st.PreflightLifecycles().GetByOperationID(ctx, terminalOpID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.PreflightLifecycles().GetByOperationID(ctx, fallbackOpID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.PreflightLifecycles().GetByOperationID(ctx, runningOpID)
	require.NoError(t, err)

	var orphanCount int
	require.NoError(t, st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM preflight_lifecycles WHERE id=?`, orphanID).Scan(&orphanCount))
	assert.Zero(t, orphanCount)
}

// AC-069-35: a pre-canceled context triggers the Phase 0 guard, skips every
// phase, and must not update last_success_at.
func TestRunGCTimeoutGuardSkipsAllPhases(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	service := newCleanupService(t, st)

	bundle := createCleanupBundle(context.Background(), t, st, "gc-timeout-bundle", store.BundleValidated)
	ageBundle(t, st, bundle.ID)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, errs, err := service.runGC(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0], "Phase0")

	got, err := st.Bundles().Get(context.Background(), bundle.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleValidated, got.Status, "no phase may run after the timeout guard")
	assert.Empty(t, resp.GetDeletedBundles())
	assert.True(t, service.lastSuccess.IsZero(), "last_success_at must not update on a skipped cycle")
}

// AC-069-40: a positive interval triggers one startup GC cycle before the
// ticker loop starts.
func TestStartupGCRunsImmediately(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	service := NewCleanupService(st, DefaultRetentionConfig(), slog.New(slog.DiscardHandler))
	bundle := createCleanupBundle(ctx, t, st, "gc-startup-bundle", store.BundleValidated)
	ageBundle(t, st, bundle.ID)

	done := make(chan struct{})
	go func() {
		defer close(done)
		service.StartTicker(ctx)
	}()
	require.Eventually(t, func() bool {
		got, err := st.Bundles().Get(context.Background(), bundle.ID)
		return err == nil && got.Status == store.BundleArchived
	}, 5*time.Second, 50*time.Millisecond)
	cancel()
	<-done
}

// AC-069-36: RunCleanup returns counted results and a sanitized error list.
func TestRunCleanupRPCResponse(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	ctx := context.Background()
	service := newCleanupService(t, st)

	bundle := createCleanupBundle(ctx, t, st, "gc-rpc-bundle", store.BundleValidated)
	ageBundle(t, st, bundle.ID)

	resp, err := service.RunCleanup(ctx, connect.NewRequest(&orchestratorv1.RunCleanupRequest{
		IdempotencyKey: "rpc-response-key",
	}))
	require.NoError(t, err)
	assert.NotNil(t, resp.Msg)
	assert.Equal(t, int64(0), resp.Msg.GetDeletedBundles(), "archive happens first; deletion needs a second cycle")
	require.NotNil(t, resp.Msg.GetErrors())

	got, err := st.Bundles().Get(ctx, bundle.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleArchived, got.Status)
}

// AC-069-08/09/10/11/55: UnarchiveBundle restores only validated archived
// bundles, is idempotent for validated, and maps rejected/received/not-found
// to the Connect error contract.
func TestUnarchiveBundleRPC(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	ctx := context.Background()
	service := newCleanupService(t, st)

	archive := func(bundle *store.ReleaseBundle) {
		t.Helper()
		_, err := st.Bundles().Archive(ctx, []string{bundle.ID})
		require.NoError(t, err)
	}

	t.Run("validated archived bundle restores with previous_status", func(t *testing.T) {
		bundle := createCleanupBundle(ctx, t, st, "unarchive-validated", store.BundleValidated)
		archive(bundle)
		resp, err := service.UnarchiveBundle(ctx, connect.NewRequest(&orchestratorv1.UnarchiveBundleRequest{BundleId: bundle.ID}))
		require.NoError(t, err)
		assert.Equal(t, "validated", resp.Msg.GetPreviousStatus())
		got, err := st.Bundles().Get(ctx, bundle.ID)
		require.NoError(t, err)
		assert.Equal(t, store.BundleValidated, got.Status)
		assert.Nil(t, got.ArchivedAt)
	})

	t.Run("validated bundle is idempotent", func(t *testing.T) {
		bundle := createCleanupBundle(ctx, t, st, "unarchive-idempotent", store.BundleValidated)
		resp, err := service.UnarchiveBundle(ctx, connect.NewRequest(&orchestratorv1.UnarchiveBundleRequest{BundleId: bundle.ID}))
		require.NoError(t, err)
		assert.Equal(t, "validated", resp.Msg.GetPreviousStatus())
	})

	t.Run("rejected archived bundle fails precondition", func(t *testing.T) {
		bundle := createCleanupBundle(ctx, t, st, "unarchive-rejected", store.BundleRejected)
		archive(bundle)
		_, err := service.UnarchiveBundle(ctx, connect.NewRequest(&orchestratorv1.UnarchiveBundleRequest{BundleId: bundle.ID}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	})

	t.Run("received archived bundle fails precondition", func(t *testing.T) {
		bundle := createCleanupBundle(ctx, t, st, "unarchive-received", store.BundleReceived)
		archive(bundle)
		_, err := service.UnarchiveBundle(ctx, connect.NewRequest(&orchestratorv1.UnarchiveBundleRequest{BundleId: bundle.ID}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	})

	t.Run("missing bundle is not found", func(t *testing.T) {
		_, err := service.UnarchiveBundle(ctx, connect.NewRequest(&orchestratorv1.UnarchiveBundleRequest{BundleId: "no-such-bundle"}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})

	t.Run("empty bundle id is invalid argument", func(t *testing.T) {
		_, err := service.UnarchiveBundle(ctx, connect.NewRequest(&orchestratorv1.UnarchiveBundleRequest{}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})
}

// fakeAuditSink records emitted events for audit assertions.
type fakeAuditSink struct {
	mu     sync.Mutex
	events []*store.AuditEvent
}

func (f *fakeAuditSink) Emit(event *store.AuditEvent) audit.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return audit.Result{EventID: event.ID, Accepted: true}
}

func (f *fakeAuditSink) find(resourceType, action string) *store.AuditEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, event := range f.events {
		if event.ResourceType == resourceType && event.Action == action {
			return event
		}
	}
	return nil
}

func (f *fakeAuditSink) findStatus(resourceType, action, status string) *store.AuditEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, event := range f.events {
		if event.ResourceType == resourceType && event.Action == action && event.Status == status {
			return event
		}
	}
	return nil
}

// REQ-069 审计: GC cycles and unarchive operations emit AuditEvents through
// the shared sink; failures stay sanitized.
func TestCleanupAuditEvents(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	ctx := context.Background()
	sink := &fakeAuditSink{}
	service := NewCleanupService(st, DefaultRetentionConfig(), slog.New(slog.DiscardHandler), sink)

	bundle := createCleanupBundle(ctx, t, st, "audit-bundle", store.BundleValidated)
	ageBundle(t, st, bundle.ID)

	_, err := service.RunCleanup(ctx, connect.NewRequest(&orchestratorv1.RunCleanupRequest{IdempotencyKey: "audit-gc"}))
	require.NoError(t, err)
	event := sink.find("gc_cycle", "cleanup.gc")
	require.NotNil(t, event, "GC completion must emit a gc_cycle audit event")

	_, err = service.UnarchiveBundle(ctx, connect.NewRequest(&orchestratorv1.UnarchiveBundleRequest{BundleId: bundle.ID}))
	require.NoError(t, err)
	unarchived := sink.find("bundle", "bundle.unarchived")
	require.NotNil(t, unarchived, "unarchive must emit a bundle audit event")
	assert.Equal(t, "succeeded", unarchived.Status)
	assert.Equal(t, "validated", unarchived.Metadata["previous_status"])

	_, err = service.UnarchiveBundle(ctx, connect.NewRequest(&orchestratorv1.UnarchiveBundleRequest{BundleId: "missing"}))
	require.Error(t, err)
	failed := sink.findStatus("bundle", "bundle.unarchived", "failed")
	require.NotNil(t, failed)
	assert.Equal(t, "failed", failed.Status)
}

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// ── Bundle lifecycle tests (AC-069-01, AC-069-02, AC-069-13) ──────

func TestBundleArchiveAndUnarchive(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	b := &store.ReleaseBundle{
		ID:          uuid.New().String(),
		Name:        "test-bundle",
		DigestAlg:   "sha256",
		DigestValue: "abc123",
		Status:      store.BundleValidated,
	}
	require.NoError(t, st.Bundles().Create(ctx, b))

	// Archive the bundle.
	n, err := st.Bundles().Archive(ctx, []string{b.ID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	got, err := st.Bundles().Get(ctx, b.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleArchived, got.Status)
	assert.NotNil(t, got.ArchivedAt)

	// Unarchive the bundle.
	require.NoError(t, st.Bundles().Unarchive(ctx, b.ID))

	got, err = st.Bundles().Get(ctx, b.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleValidated, got.Status)
	assert.Nil(t, got.ArchivedAt)
}

func TestBundleArchiveIdempotent(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	b := &store.ReleaseBundle{
		ID:          uuid.New().String(),
		Name:        "idem-bundle",
		DigestAlg:   "sha256",
		DigestValue: "idem123",
		Status:      store.BundleValidated,
	}
	require.NoError(t, st.Bundles().Create(ctx, b))

	// Archive twice — second call is no-op for already-archived.
	n1, err := st.Bundles().Archive(ctx, []string{b.ID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n1)

	n2, err := st.Bundles().Archive(ctx, []string{b.ID})
	require.NoError(t, err)
	assert.Equal(t, int64(0), n2) // already archived
}

func TestBundleDeleteExpired(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Create an archived bundle with old archived_at.
	old := &store.ReleaseBundle{
		ID:          uuid.New().String(),
		Name:        "old-bundle",
		DigestAlg:   "sha256",
		DigestValue: "old123",
		Status:      store.BundleValidated,
	}
	require.NoError(t, st.Bundles().Create(ctx, old))

	// Manually set the bundle as archived with an old timestamp.
	_, err := st.DB().ExecContext(ctx,
		`UPDATE release_bundles SET status='archived', archived_at=? WHERE id=?`,
		now.Add(-31*24*time.Hour).Format(time.RFC3339), old.ID)
	require.NoError(t, err)

	// Create a recently archived bundle (should survive).
	recent := &store.ReleaseBundle{
		ID:          uuid.New().String(),
		Name:        "recent-bundle",
		DigestAlg:   "sha256",
		DigestValue: "recent123",
		Status:      store.BundleValidated,
	}
	require.NoError(t, st.Bundles().Create(ctx, recent))

	_, err = st.DB().ExecContext(ctx,
		`UPDATE release_bundles SET status='archived', archived_at=? WHERE id=?`,
		now.Add(-15*24*time.Hour).Format(time.RFC3339), recent.ID)
	require.NoError(t, err)

	// Create a rejected bundle with old created_at (AC-069-13).
	rejected := &store.ReleaseBundle{
		ID:          uuid.New().String(),
		Name:        "rejected-bundle",
		DigestAlg:   "sha256",
		DigestValue: "rej123",
		Status:      store.BundleRejected,
	}
	require.NoError(t, st.Bundles().Create(ctx, rejected))

	_, err = st.DB().ExecContext(ctx,
		`UPDATE release_bundles SET created_at=? WHERE id=?`,
		now.Add(-100*24*time.Hour).Format(time.RFC3339), rejected.ID)
	require.NoError(t, err)

	// DeleteBefore: cutoff = now - 30 days.
	// Old archived (31d ago) → deleted. Recent archived (15d ago) → kept.
	// Rejected with old created_at → deleted.
	cutoff := now.Add(-30 * 24 * time.Hour)
	n, err := st.Bundles().DeleteBefore(ctx, cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n) // old + rejected

	_, err = st.Bundles().Get(ctx, old.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	recentGot, err := st.Bundles().Get(ctx, recent.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleArchived, recentGot.Status)

	_, err = st.Bundles().Get(ctx, rejected.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestBundleListForArchive_ActiveDefinitionProtects(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	b := &store.ReleaseBundle{
		ID:          uuid.New().String(),
		Name:        "protected-bundle",
		DigestAlg:   "sha256",
		DigestValue: "prot123",
		Status:      store.BundleValidated,
	}
	require.NoError(t, st.Bundles().Create(ctx, b))

	// Make it look old.
	_, err := st.DB().ExecContext(ctx,
		`UPDATE release_bundles SET created_at=? WHERE id=?`,
		now.Add(-100*24*time.Hour).Format(time.RFC3339), b.ID)
	require.NoError(t, err)

	// Create an active definition and link the bundle via SetCurrentBundle.
	def := &store.ReleaseDefinition{
		ID:                uuid.New().String(),
		Name:              "active-def",
		CustomerID:        "cust-1",
		ClusterID:         "cls-1",
		ReleaseName:       "active-rel",
		Status:            store.DefStatusActive,
		OptimisticVersion: 1,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))
	_, err = st.Definitions().SetCurrentBundle(ctx, def.ID, b.ID)
	require.NoError(t, err)

	terminalStates := []store.OperationStatus{
		store.StatusSucceeded, store.StatusFailed,
		store.StatusCancelled, store.StatusTimeout,
	}

	ids, err := st.Bundles().ListForArchive(ctx, 90, terminalStates)
	require.NoError(t, err)
	assert.Empty(t, ids) // protected by active definition
}

func TestBundleListForArchive_NonTerminalOperationProtects(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	b := &store.ReleaseBundle{
		ID:          uuid.New().String(),
		Name:        "op-protected-bundle",
		DigestAlg:   "sha256",
		DigestValue: "opprot123",
		Status:      store.BundleValidated,
	}
	require.NoError(t, st.Bundles().Create(ctx, b))

	_, err := st.DB().ExecContext(ctx,
		`UPDATE release_bundles SET created_at=? WHERE id=?`,
		now.Add(-100*24*time.Hour).Format(time.RFC3339), b.ID)
	require.NoError(t, err)

	// Create a non-terminal operation referencing this bundle.
	def := &store.ReleaseDefinition{
		ID:                uuid.New().String(),
		Name:              "op-def",
		CustomerID:        "cust-2",
		ClusterID:         "cls-2",
		ReleaseName:       "op-rel",
		Status:            store.DefStatusActive,
		OptimisticVersion: 1,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))

	op := &store.Operation{
		ID:                  uuid.New().String(),
		OperationType:       store.OperationInstall,
		Status:              store.StatusPreflight,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      uuid.New().String(),
		RequestHash:         "hash",
		BundleID:            b.ID,
		StateVersion:        1,
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	terminalStates := []store.OperationStatus{
		store.StatusSucceeded, store.StatusFailed,
		store.StatusCancelled, store.StatusTimeout,
	}

	ids, err := st.Bundles().ListForArchive(ctx, 90, terminalStates)
	require.NoError(t, err)
	assert.Empty(t, ids) // protected by non-terminal operation
}

func TestBundleListForArchive_EligibleAfterTerminal(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	b := &store.ReleaseBundle{
		ID:          uuid.New().String(),
		Name:        "terminal-bundle",
		DigestAlg:   "sha256",
		DigestValue: "term123",
		Status:      store.BundleValidated,
	}
	require.NoError(t, st.Bundles().Create(ctx, b))

	_, err := st.DB().ExecContext(ctx,
		`UPDATE release_bundles SET created_at=? WHERE id=?`,
		now.Add(-100*24*time.Hour).Format(time.RFC3339), b.ID)
	require.NoError(t, err)

	def := &store.ReleaseDefinition{
		ID:                uuid.New().String(),
		Name:              "term-def",
		CustomerID:        "cust-3",
		ClusterID:         "cls-3",
		ReleaseName:       "term-rel",
		Status:            store.DefStatusActive,
		OptimisticVersion: 1,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))

	op := &store.Operation{
		ID:                  uuid.New().String(),
		OperationType:       store.OperationInstall,
		Status:              store.StatusSucceeded, // terminal
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      uuid.New().String(),
		RequestHash:         "hash",
		BundleID:            b.ID,
		StateVersion:        1,
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	terminalStates := []store.OperationStatus{
		store.StatusSucceeded, store.StatusFailed,
		store.StatusCancelled, store.StatusTimeout,
	}

	ids, err := st.Bundles().ListForArchive(ctx, 90, terminalStates)
	require.NoError(t, err)
	assert.Contains(t, ids, b.ID) // now eligible
}

// ── Definition SetCurrentBundle (AC-069-02) ──────────────────────

func TestSetCurrentBundle_AutoUnarchive(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	b := &store.ReleaseBundle{
		ID:          uuid.New().String(),
		Name:        "unarchive-bundle",
		DigestAlg:   "sha256",
		DigestValue: "unarch123",
		Status:      store.BundleArchived,
	}
	require.NoError(t, st.Bundles().Create(ctx, b))

	// Manually set archived_at (Create doesn't set it automatically).
	_, err := st.DB().ExecContext(ctx,
		`UPDATE release_bundles SET archived_at=?, status='archived' WHERE id=?`,
		time.Now().UTC().Format(time.RFC3339), b.ID)
	require.NoError(t, err)

	def := &store.ReleaseDefinition{
		ID:                uuid.New().String(),
		Name:              "unarch-def",
		CustomerID:        "cust-4",
		ClusterID:         "cls-4",
		ReleaseName:       "unarch-rel",
		Status:            store.DefStatusActive,
		OptimisticVersion: 1,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))

	unarchived, err := st.Definitions().SetCurrentBundle(ctx, def.ID, b.ID)
	require.NoError(t, err)
	assert.True(t, unarchived)

	got, err := st.Bundles().Get(ctx, b.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleValidated, got.Status)
	assert.Nil(t, got.ArchivedAt)
}

func TestSetCurrentBundle_AlreadyValidated(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	b := &store.ReleaseBundle{
		ID:          uuid.New().String(),
		Name:        "validated-bundle",
		DigestAlg:   "sha256",
		DigestValue: "val123",
		Status:      store.BundleValidated,
	}
	require.NoError(t, st.Bundles().Create(ctx, b))

	def := &store.ReleaseDefinition{
		ID:                uuid.New().String(),
		Name:              "val-def",
		CustomerID:        "cust-5",
		ClusterID:         "cls-5",
		ReleaseName:       "val-rel",
		Status:            store.DefStatusActive,
		OptimisticVersion: 1,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))

	unarchived, err := st.Definitions().SetCurrentBundle(ctx, def.ID, b.ID)
	require.NoError(t, err)
	assert.False(t, unarchived) // wasn't archived
}

// ── Candidate Artifact lifecycle (AC-069-08) ────────────────────

func TestCandidateArtifactCreateAndLink(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	ca := &store.CandidateArtifact{
		ArtifactType: store.ArtifactImage,
		Ref:          "harbor.example.com/app:v1.0",
		Digest:       "sha256:candidate123",
	}
	require.NoError(t, st.CandidateArtifacts().Create(ctx, ca))
	assert.NotEmpty(t, ca.ID)

	// Idempotent: same digest + type → ON CONFLICT DO NOTHING.
	ca2 := &store.CandidateArtifact{
		ArtifactType: store.ArtifactImage,
		Ref:          "harbor.example.com/app:v1.0",
		Digest:       "sha256:candidate123",
	}
	require.NoError(t, st.CandidateArtifacts().Create(ctx, ca2))

	// Link to a bundle.
	b := &store.ReleaseBundle{
		ID:          uuid.New().String(),
		Name:        "link-bundle",
		DigestAlg:   "sha256",
		DigestValue: "link123",
		Status:      store.BundleValidated,
	}
	require.NoError(t, st.Bundles().Create(ctx, b))

	require.NoError(t, st.CandidateArtifacts().LinkToBundle(ctx, ca.ID, b.ID))
}

func TestCandidateArtifactDeleteOrphan(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Orphan candidate — old.
	oldOrphan := &store.CandidateArtifact{
		ID:           uuid.New().String(),
		ArtifactType: store.ArtifactImage,
		Ref:          "old-orphan:v1",
		Digest:       "sha256:oldorphan",
	}
	require.NoError(t, st.CandidateArtifacts().Create(ctx, oldOrphan))

	_, err := st.DB().ExecContext(ctx,
		`UPDATE candidate_artifacts SET created_at=? WHERE id=?`,
		now.Add(-35*24*time.Hour).Format(time.RFC3339), oldOrphan.ID)
	require.NoError(t, err)

	// Linked candidate — should survive (AC-069-08).
	b := &store.ReleaseBundle{
		ID:          uuid.New().String(),
		Name:        "linked-bundle",
		DigestAlg:   "sha256",
		DigestValue: "linked123",
		Status:      store.BundleValidated,
	}
	require.NoError(t, st.Bundles().Create(ctx, b))

	linked := &store.CandidateArtifact{
		ID:           uuid.New().String(),
		ArtifactType: store.ArtifactImage,
		Ref:          "linked-artifact:v1",
		Digest:       "sha256:linked",
		BundleID:     &b.ID,
	}
	require.NoError(t, st.CandidateArtifacts().Create(ctx, linked))

	_, err = st.DB().ExecContext(ctx,
		`UPDATE candidate_artifacts SET created_at=? WHERE id=?`,
		now.Add(-35*24*time.Hour).Format(time.RFC3339), linked.ID)
	require.NoError(t, err)

	// Recent orphan — should survive.
	recentOrphan := &store.CandidateArtifact{
		ID:           uuid.New().String(),
		ArtifactType: store.ArtifactImage,
		Ref:          "recent-orphan:v1",
		Digest:       "sha256:recent",
	}
	require.NoError(t, st.CandidateArtifacts().Create(ctx, recentOrphan))

	cutoff := now.Add(-30 * 24 * time.Hour)
	n, err := st.CandidateArtifacts().DeleteOrphanBefore(ctx, cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n) // only old orphan deleted
}

// ── Preflight Lifecycle (AC-019-05, AC-019-06, AC-069-10) ──────

func TestPreflightLifecycleCreateOrReset(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	def := &store.ReleaseDefinition{
		ID: "preflight-lifecycle-def", Name: "Preflight Lifecycle", CustomerID: "cust-preflight",
		ClusterID: "cluster-preflight", ReleaseName: "release-preflight", Status: store.DefStatusActive,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))
	op := &store.Operation{
		ID: "preflight-lifecycle-op", OperationType: store.OperationInstall, Status: store.StatusPreflight,
		ReleaseDefinitionID: def.ID, IdempotencyKey: "preflight-lifecycle-key", RequestHash: "hash",
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	require.NoError(t, st.PreflightLifecycles().CreateOrReset(ctx, op.ID))
	var firstID, stages, overall, createdAt, updatedAt string
	var terminalAt *string
	require.NoError(t, st.DB().QueryRowContext(ctx, `
		SELECT id, operation_terminal_at, stages, overall, created_at, updated_at
		FROM preflight_lifecycles WHERE operation_id = ?`, op.ID,
	).Scan(&firstID, &terminalAt, &stages, &overall, &createdAt, &updatedAt))
	assert.Nil(t, terminalAt)
	assert.Empty(t, stages)
	assert.Equal(t, "running", overall)
	assert.Equal(t, createdAt, updatedAt)

	require.NoError(t, st.PreflightLifecycles().UpdateResult(ctx, op.ID, "failed", "artifact,render"))
	require.NoError(t, st.PreflightLifecycles().CreateOrReset(ctx, op.ID))
	var resetID string
	require.NoError(t, st.DB().QueryRowContext(ctx, `
		SELECT id, stages, overall FROM preflight_lifecycles WHERE operation_id = ?`, op.ID,
	).Scan(&resetID, &stages, &overall))
	assert.Equal(t, firstID, resetID)
	assert.Empty(t, stages)
	assert.Equal(t, "running", overall)
}

func TestPreflightLifecycleCreateOrResetPreservesTerminalAt(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	def := &store.ReleaseDefinition{
		ID: "preflight-terminal-def", Name: "Preflight Terminal", CustomerID: "cust-terminal",
		ClusterID: "cluster-terminal", ReleaseName: "release-terminal", Status: store.DefStatusActive,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))
	op := &store.Operation{
		ID: "preflight-terminal-op", OperationType: store.OperationInstall, Status: store.StatusPreflight,
		ReleaseDefinitionID: def.ID, IdempotencyKey: "preflight-terminal-key", RequestHash: "hash",
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	require.NoError(t, st.PreflightLifecycles().CreateOrReset(ctx, op.ID))
	updated, err := st.Operations().Transition(ctx, op.ID, store.StatusCancelled, op.StateVersion, "cancelled")
	require.NoError(t, err)
	require.NotNil(t, updated.TerminalAt)
	require.NoError(t, st.PreflightLifecycles().CreateOrReset(ctx, op.ID))

	var terminalAt string
	require.NoError(t, st.DB().QueryRowContext(ctx,
		`SELECT operation_terminal_at FROM preflight_lifecycles WHERE operation_id = ?`, op.ID,
	).Scan(&terminalAt))
	assert.Equal(t, updated.TerminalAt.UTC().Format(time.RFC3339), terminalAt)
}

func TestPreflightLifecycleUpdateResult(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	opID := "preflight-result-op"
	require.NoError(t, st.PreflightLifecycles().CreateOrReset(ctx, opID))
	require.NoError(t, st.PreflightLifecycles().UpdateResult(ctx, opID, "passed", "artifact,render,dryrun"))

	var stages, overall string
	require.NoError(t, st.DB().QueryRowContext(ctx,
		`SELECT stages, overall FROM preflight_lifecycles WHERE operation_id = ?`, opID,
	).Scan(&stages, &overall))
	assert.Equal(t, "artifact,render,dryrun", stages)
	assert.Equal(t, "passed", overall)
}

func TestPreflightLifecycleDeleteExpired(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	now := time.Now().UTC()

	oldExploratoryID := uuid.NewString()
	_, err := st.DB().ExecContext(ctx, `
		INSERT INTO preflight_lifecycles (id, operation_id, stages, overall, created_at, updated_at)
		VALUES (?, NULL, '', 'passed', ?, ?)`,
		oldExploratoryID, now.Add(-8*24*time.Hour).Format(time.RFC3339), now.Add(-8*24*time.Hour).Format(time.RFC3339),
	)
	require.NoError(t, err)

	activeOperationID := "preflight-active-op"
	require.NoError(t, st.PreflightLifecycles().CreateOrReset(ctx, activeOperationID))
	_, err = st.DB().ExecContext(ctx, `UPDATE preflight_lifecycles SET created_at = ?, updated_at = ? WHERE operation_id = ?`,
		now.Add(-8*24*time.Hour).Format(time.RFC3339), now.Add(-8*24*time.Hour).Format(time.RFC3339), activeOperationID)
	require.NoError(t, err)

	terminalOperationID := "preflight-terminal-gc-op"
	require.NoError(t, st.PreflightLifecycles().CreateOrReset(ctx, terminalOperationID))
	_, err = st.DB().ExecContext(ctx, `UPDATE preflight_lifecycles SET operation_terminal_at = ? WHERE operation_id = ?`,
		now.Add(-8*24*time.Hour).Format(time.RFC3339), terminalOperationID)
	require.NoError(t, err)

	deleted, err := st.PreflightLifecycles().DeleteExpired(ctx, 7*24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)
}

func TestOperationTransitionSetsPreflightTerminal(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	opID := uuid.NewString()
	require.NoError(t, st.PreflightLifecycles().CreateOrReset(ctx, opID))
	def := &store.ReleaseDefinition{
		ID: uuid.NewString(), Name: "term-cb-def", CustomerID: "cust-6", ClusterID: "cls-6",
		ReleaseName: "term-cb-rel", Status: store.DefStatusActive, OptimisticVersion: 1,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))
	op := &store.Operation{
		ID: opID, OperationType: store.OperationInstall, Status: store.StatusRunning,
		ReleaseDefinitionID: def.ID, IdempotencyKey: uuid.NewString(), RequestHash: "hash", StateVersion: 3,
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	updated, err := st.Operations().Transition(ctx, op.ID, store.StatusSucceeded, op.StateVersion, "")
	require.NoError(t, err)
	require.NotNil(t, updated.TerminalAt)

	var operationTerminalAt, lifecycleTerminalAt string
	require.NoError(t, st.DB().QueryRowContext(ctx, `SELECT terminal_at FROM operations WHERE id = ?`, opID).Scan(&operationTerminalAt))
	require.NoError(t, st.DB().QueryRowContext(ctx,
		`SELECT operation_terminal_at FROM preflight_lifecycles WHERE operation_id = ?`, opID,
	).Scan(&lifecycleTerminalAt))
	assert.Equal(t, operationTerminalAt, lifecycleTerminalAt)
}

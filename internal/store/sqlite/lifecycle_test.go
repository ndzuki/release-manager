package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
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

func TestBundleArchive_StoresPreviousStatus(t *testing.T) {
	tests := []struct {
		name   string
		status store.BundleStatus
	}{
		{name: "received", status: store.BundleReceived},
		{name: "validated", status: store.BundleValidated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := setupStore(t)
			bundle := &store.ReleaseBundle{
				ID:          uuid.New().String(),
				Name:        "archive-source-" + tt.name,
				DigestAlg:   "sha256",
				DigestValue: uuid.New().String(),
				Status:      tt.status,
			}
			require.NoError(t, st.Bundles().Create(t.Context(), bundle))

			count, err := st.Bundles().Archive(t.Context(), []string{bundle.ID})
			require.NoError(t, err)
			assert.Equal(t, int64(1), count)

			archived, err := st.Bundles().Get(t.Context(), bundle.ID)
			require.NoError(t, err)
			assert.Equal(t, store.BundleArchived, archived.Status)
			assert.Equal(t, tt.status, *archived.ArchivedFromStatus)
		})
	}
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

func TestSetCurrentBundle_ArchivedValidatedRestored(t *testing.T) {
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

	// Manually set archival metadata (Create doesn't set it automatically).
	_, err := st.DB().ExecContext(ctx,
		`UPDATE release_bundles SET archived_at=?, status='archived', archived_from_status='validated' WHERE id=?`,
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

func TestSetCurrentBundle_ArchivedReceivedRejected(t *testing.T) {
	tests := []struct {
		name       string
		fromStatus store.BundleStatus
		wantErr    error
	}{
		{name: "received", fromStatus: store.BundleReceived, wantErr: store.ErrBundleNotReady},
		{name: "rejected", fromStatus: store.BundleRejected, wantErr: store.ErrBundleRejected},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := setupStore(t)
			bundle := &store.ReleaseBundle{
				ID:          uuid.New().String(),
				Name:        "archived-" + tt.name,
				DigestAlg:   "sha256",
				DigestValue: uuid.New().String(),
				Status:      store.BundleArchived,
			}
			require.NoError(t, st.Bundles().Create(t.Context(), bundle))
			_, err := st.DB().ExecContext(t.Context(), `
				UPDATE release_bundles
				SET archived_at=?, archived_from_status=?
				WHERE id=?
			`, time.Now().UTC().Format(time.RFC3339), string(tt.fromStatus), bundle.ID)
			require.NoError(t, err)

			definition := &store.ReleaseDefinition{
				ID: uuid.New().String(), Name: "definition-" + tt.name,
				CustomerID: "cust-" + tt.name, ClusterID: "cluster-" + tt.name,
				ReleaseName: "release-" + tt.name, Status: store.DefStatusActive,
			}
			require.NoError(t, st.Definitions().Create(t.Context(), definition, nil))

			_, err = st.Definitions().SetCurrentBundle(t.Context(), definition.ID, bundle.ID)
			require.ErrorIs(t, err, tt.wantErr)

			storedDefinition, err := st.Definitions().Get(t.Context(), definition.ID)
			require.NoError(t, err)
			assert.Nil(t, storedDefinition.CurrentBundleID)
			storedBundle, err := st.Bundles().Get(t.Context(), bundle.ID)
			require.NoError(t, err)
			assert.Equal(t, store.BundleArchived, storedBundle.Status)
			assert.Equal(t, tt.fromStatus, *storedBundle.ArchivedFromStatus)
		})
	}
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
	assert.NotEqual(t, ca.ID, ca2.ID)
	var persistedID string
	require.NoError(t, st.DB().QueryRowContext(ctx, `
		SELECT id FROM candidate_artifacts WHERE digest = ? AND artifact_type = ?
	`, ca.Digest, string(ca.ArtifactType)).Scan(&persistedID))
	assert.Equal(t, ca.ID, persistedID)

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

func TestLinkCandidateArtifacts_Batch(t *testing.T) {
	st := setupStore(t)
	bundle := &store.ReleaseBundle{
		ID: uuid.New().String(), Name: "batch-link-bundle", DigestAlg: "sha256",
		DigestValue: uuid.New().String(), Status: store.BundleValidated,
	}
	require.NoError(t, st.Bundles().Create(t.Context(), bundle))

	digests := []string{"sha256:image-one", "sha256:image-two", "sha256:chart"}
	for i, digest := range digests {
		artifactType := store.ArtifactImage
		if i == len(digests)-1 {
			artifactType = store.ArtifactChart
		}
		artifact := &store.CandidateArtifact{
			ArtifactType: artifactType,
			Ref:          fmt.Sprintf("artifact-%d", i),
			Digest:       digest,
		}
		require.NoError(t, st.CandidateArtifacts().Create(t.Context(), artifact))
	}

	const workers = 8
	type linkResult struct {
		linked int64
		err    error
	}
	results := make(chan linkResult, workers)
	for range workers {
		go func() {
			linked, err := st.CandidateArtifacts().LinkCandidateArtifacts(t.Context(), bundle.ID, digests)
			results <- linkResult{linked: linked, err: err}
		}()
	}

	var totalLinked int64
	for range workers {
		result := <-results
		require.NoError(t, result.err)
		totalLinked += result.linked
	}
	assert.Equal(t, int64(len(digests)), totalLinked)

	var count int
	require.NoError(t, st.DB().QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM bundle_candidate_artifacts WHERE bundle_id = ?
	`, bundle.ID).Scan(&count))
	assert.Equal(t, len(digests), count)
	var claimed int
	require.NoError(t, st.DB().QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM candidate_artifacts
		WHERE digest IN (?, ?, ?) AND orphaned_at IS NULL
	`, digests[0], digests[1], digests[2]).Scan(&claimed))
	assert.Equal(t, len(digests), claimed)
}

func TestLinkCandidateArtifacts_NoMatch(t *testing.T) {
	st := setupStore(t)
	bundle := &store.ReleaseBundle{
		ID: uuid.New().String(), Name: "no-match-bundle", DigestAlg: "sha256",
		DigestValue: uuid.New().String(), Status: store.BundleValidated,
	}
	require.NoError(t, st.Bundles().Create(t.Context(), bundle))

	linked, err := st.CandidateArtifacts().LinkCandidateArtifacts(t.Context(), bundle.ID, []string{"sha256:missing"})
	require.NoError(t, err)
	assert.Zero(t, linked)
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
		`UPDATE candidate_artifacts SET created_at=?, orphaned_at=? WHERE id=?`,
		now.Add(-35*24*time.Hour).Format(time.RFC3339), now.Add(-35*24*time.Hour).Format(time.RFC3339), oldOrphan.ID)
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
func TestSQLiteUnitOfWork_AtomicCommit(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	bundle := &store.ReleaseBundle{
		ID: uuid.New().String(), Name: "uow-bundle", DigestAlg: "sha256",
		DigestValue: uuid.New().String(), Status: store.BundleArchived,
		ChartDigest: "sha256:chart-uow",
		Images:      []store.BundleImage{{Ref: "app:v1", Digest: "sha256:image-uow"}},
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))
	_, err := st.DB().ExecContext(ctx, `
		UPDATE release_bundles
		SET archived_at=?, archived_from_status='validated'
		WHERE id=?
	`, time.Now().UTC().Format(time.RFC3339), bundle.ID)
	require.NoError(t, err)

	definition := &store.ReleaseDefinition{
		ID: uuid.New().String(), Name: "uow-definition", CustomerID: "uow-customer",
		ClusterID: "uow-cluster", ReleaseName: "uow-release", Status: store.DefStatusActive,
	}
	require.NoError(t, st.Definitions().Create(ctx, definition, nil))

	for artifactType, digest := range map[store.ArtifactType]string{
		store.ArtifactChart: bundle.ChartDigest,
		store.ArtifactImage: bundle.Images[0].Digest,
	} {
		require.NoError(t, st.CandidateArtifacts().Create(ctx, &store.CandidateArtifact{
			ArtifactType: artifactType, Ref: string(artifactType), Digest: digest,
		}))
	}

	now := time.Now().UTC()
	operationRecord := &store.Operation{
		ID: uuid.New().String(), OperationType: store.OperationInstall, Status: store.StatusPending,
		ReleaseDefinitionID: definition.ID, IdempotencyKey: uuid.New().String(), IdempotencyScope: "org:" + definition.ID,
		RequestHash: uuid.New().String(), BundleID: bundle.ID, CreatedAt: now, UpdatedAt: now,
	}
	dispatch := &store.OutboxEntry{
		ID: uuid.New().String(), CommandID: operationRecord.ID + ":artifact", OperationID: operationRecord.ID,
		OperationType: string(operationRecord.OperationType), Payload: []byte(`{}`),
	}

	result, err := st.OperationCreationUnitOfWork()(ctx, store.OperationCreationRequest{
		Operation: operationRecord, Dispatch: dispatch,
		CandidateArtifactDigests: []string{bundle.ChartDigest, bundle.Images[0].Digest},
	})
	require.NoError(t, err)
	assert.True(t, result.BundleRestored)
	assert.Equal(t, int64(2), result.LinkedCandidateCount)

	storedOperation, err := st.Operations().Get(ctx, operationRecord.ID)
	require.NoError(t, err)
	assert.Equal(t, bundle.ID, storedOperation.BundleID)
	storedDefinition, err := st.Definitions().Get(ctx, definition.ID)
	require.NoError(t, err)
	require.NotNil(t, storedDefinition.CurrentBundleID)
	assert.Equal(t, bundle.ID, *storedDefinition.CurrentBundleID)
	storedBundle, err := st.Bundles().Get(ctx, bundle.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleValidated, storedBundle.Status)
	assert.Nil(t, storedBundle.ArchivedFromStatus)
	_, err = st.Outbox().GetByCommandID(ctx, dispatch.CommandID)
	require.NoError(t, err)
	var linked int
	require.NoError(t, st.DB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM bundle_candidate_artifacts WHERE bundle_id = ?
	`, bundle.ID).Scan(&linked))
	assert.Equal(t, 2, linked)
}

func TestSQLiteUnitOfWork_AuthorizationFence(t *testing.T) {
	// AC-067-22: a stale authorization snapshot is rejected inside the
	// transaction with no writes (expected version never matches).
	st := setupStore(t)
	ctx := t.Context()

	definition := &store.ReleaseDefinition{
		ID: uuid.New().String(), Name: "fence-definition", CustomerID: "fence-customer",
		ClusterID: "fence-cluster", ReleaseName: "fence-release", Status: store.DefStatusActive,
	}
	require.NoError(t, st.Definitions().Create(ctx, definition, nil))
	bundle := &store.ReleaseBundle{
		ID: uuid.New().String(), Name: "fence-bundle", DigestAlg: "sha256",
		DigestValue: uuid.New().String(), Status: store.BundleValidated,
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))

	now := time.Now().UTC()
	operationRecord := &store.Operation{
		ID: uuid.New().String(), OperationType: store.OperationInstall, Status: store.StatusPending,
		ReleaseDefinitionID: definition.ID, IdempotencyKey: uuid.New().String(), IdempotencyScope: "org:" + definition.ID,
		RequestHash: uuid.New().String(), BundleID: bundle.ID, CreatedAt: now, UpdatedAt: now,
	}
	dispatch := &store.OutboxEntry{
		ID: uuid.New().String(), CommandID: operationRecord.ID + ":artifact", OperationID: operationRecord.ID,
		OperationType: string(operationRecord.OperationType), Payload: []byte(`{}`),
	}

	_, err := st.OperationCreationUnitOfWork()(ctx, store.OperationCreationRequest{
		Operation:                    operationRecord,
		Dispatch:                     dispatch,
		CandidateArtifactDigests:     []string{},
		ExpectedAuthorizationVersion: 42, // never matches the durable version
	})
	require.ErrorIs(t, err, store.ErrAuthorizationStale)

	_, err = st.Operations().Get(ctx, operationRecord.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.Outbox().GetByCommandID(ctx, dispatch.CommandID)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestSQLiteUnitOfWork_ArchivedReceivedRejected(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	bundle := &store.ReleaseBundle{
		ID: uuid.New().String(), Name: "archived-received-uow", DigestAlg: "sha256",
		DigestValue: uuid.New().String(), Status: store.BundleArchived,
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))
	_, err := st.DB().ExecContext(ctx, `
		UPDATE release_bundles SET archived_at=?, archived_from_status='received' WHERE id=?
	`, time.Now().UTC().Format(time.RFC3339), bundle.ID)
	require.NoError(t, err)

	definition := &store.ReleaseDefinition{
		ID: uuid.New().String(), Name: "archived-received-definition", CustomerID: "archived-received-customer",
		ClusterID: "archived-received-cluster", ReleaseName: "archived-received-release", Status: store.DefStatusActive,
	}
	require.NoError(t, st.Definitions().Create(ctx, definition, nil))
	const artifactDigest = "sha256:archived-received-artifact"
	require.NoError(t, st.CandidateArtifacts().Create(ctx, &store.CandidateArtifact{
		ArtifactType: store.ArtifactImage, Ref: "archived-received-artifact", Digest: artifactDigest,
	}))

	operationRecord := &store.Operation{
		ID: uuid.New().String(), OperationType: store.OperationInstall, Status: store.StatusPending,
		ReleaseDefinitionID: definition.ID, IdempotencyKey: uuid.New().String(), IdempotencyScope: "org:" + definition.ID,
		RequestHash: uuid.New().String(), BundleID: bundle.ID,
	}
	dispatch := &store.OutboxEntry{
		ID: uuid.New().String(), CommandID: operationRecord.ID + ":artifact", OperationID: operationRecord.ID,
		OperationType: string(operationRecord.OperationType), Payload: []byte(`{}`),
	}

	_, err = st.OperationCreationUnitOfWork()(ctx, store.OperationCreationRequest{
		Operation: operationRecord, Dispatch: dispatch, CandidateArtifactDigests: []string{artifactDigest},
	})
	require.ErrorIs(t, err, store.ErrBundleNotReady)

	_, err = st.Operations().Get(ctx, operationRecord.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.Outbox().GetByCommandID(ctx, dispatch.CommandID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	storedDefinition, err := st.Definitions().Get(ctx, definition.ID)
	require.NoError(t, err)
	assert.Nil(t, storedDefinition.CurrentBundleID)
	storedBundle, err := st.Bundles().Get(ctx, bundle.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleArchived, storedBundle.Status)
	assert.Equal(t, store.BundleReceived, *storedBundle.ArchivedFromStatus)
	var linked int
	require.NoError(t, st.DB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM bundle_candidate_artifacts WHERE bundle_id = ?
	`, bundle.ID).Scan(&linked))
	assert.Zero(t, linked)
	var orphaned int
	require.NoError(t, st.DB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM candidate_artifacts WHERE digest = ? AND orphaned_at IS NOT NULL
	`, artifactDigest).Scan(&orphaned))
	assert.Equal(t, 1, orphaned)
}

func TestSQLiteUnitOfWork_RollbackOnPartialFailure(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	bundle := &store.ReleaseBundle{
		ID: uuid.New().String(), Name: "rollback-bundle", DigestAlg: "sha256",
		DigestValue: uuid.New().String(), Status: store.BundleArchived,
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))
	_, err := st.DB().ExecContext(ctx, `
		UPDATE release_bundles SET archived_at=?, archived_from_status='validated' WHERE id=?
	`, time.Now().UTC().Format(time.RFC3339), bundle.ID)
	require.NoError(t, err)

	definition := &store.ReleaseDefinition{
		ID: uuid.New().String(), Name: "rollback-definition", CustomerID: "rollback-customer",
		ClusterID: "rollback-cluster", ReleaseName: "rollback-release", Status: store.DefStatusActive,
	}
	require.NoError(t, st.Definitions().Create(ctx, definition, nil))
	const artifactDigest = "sha256:rollback-artifact"
	require.NoError(t, st.CandidateArtifacts().Create(ctx, &store.CandidateArtifact{
		ArtifactType: store.ArtifactImage, Ref: "rollback-artifact", Digest: artifactDigest,
	}))

	// Force the final link step to fail after operation, dispatch, bundle restore,
	// and definition update have all executed inside the transaction.
	_, err = st.DB().ExecContext(ctx, `DROP TABLE bundle_candidate_artifacts`)
	require.NoError(t, err)

	operationRecord := &store.Operation{
		ID: uuid.New().String(), OperationType: store.OperationInstall, Status: store.StatusPending,
		ReleaseDefinitionID: definition.ID, IdempotencyKey: uuid.New().String(), IdempotencyScope: "org:" + definition.ID,
		RequestHash: uuid.New().String(), BundleID: bundle.ID,
	}
	dispatch := &store.OutboxEntry{
		ID: uuid.New().String(), CommandID: operationRecord.ID + ":artifact", OperationID: operationRecord.ID,
		OperationType: string(operationRecord.OperationType), Payload: []byte(`{}`),
	}

	_, err = st.OperationCreationUnitOfWork()(ctx, store.OperationCreationRequest{
		Operation: operationRecord, Dispatch: dispatch, CandidateArtifactDigests: []string{artifactDigest},
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "bundle_candidate_artifacts")

	_, err = st.Operations().Get(ctx, operationRecord.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.Outbox().GetByCommandID(ctx, dispatch.CommandID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	storedBundle, err := st.Bundles().Get(ctx, bundle.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleArchived, storedBundle.Status)
	assert.Equal(t, store.BundleValidated, *storedBundle.ArchivedFromStatus)
	storedDefinition, err := st.Definitions().Get(ctx, definition.ID)
	require.NoError(t, err)
	assert.Nil(t, storedDefinition.CurrentBundleID)
	var orphaned int
	require.NoError(t, st.DB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM candidate_artifacts WHERE digest = ? AND orphaned_at IS NOT NULL
	`, artifactDigest).Scan(&orphaned))
	assert.Equal(t, 1, orphaned)
}

// ── Preflight Lifecycle two-phase contract (AC-019-05/06/07) ─────

func TestPreflightLifecycleTwoPhaseWrite(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	// Phase start: first insert records running with empty stages (AC-019-05).
	opID := uuid.New().String()
	pl, err := st.PreflightLifecycles().CreateOrReset(ctx, opID)
	require.NoError(t, err)
	assert.NotEmpty(t, pl.ID)
	assert.Equal(t, "running", pl.Overall)
	assert.Equal(t, "", pl.Stages)
	assert.False(t, pl.CreatedAt.IsZero())
	assert.False(t, pl.UpdatedAt.IsZero())

	// Retry for the same operation reuses and resets the row (AC-019-05).
	pl2, err := st.PreflightLifecycles().CreateOrReset(ctx, opID)
	require.NoError(t, err)
	assert.Equal(t, pl.ID, pl2.ID, "retry must reuse the existing row")
	assert.Equal(t, "running", pl2.Overall)

	// Phase complete: final result persisted (AC-019-06).
	require.NoError(t, st.PreflightLifecycles().UpdateResult(ctx, opID, "passed", "artifact,render,dryrun"))
	got, err := st.PreflightLifecycles().GetByOperationID(ctx, opID)
	require.NoError(t, err)
	assert.Equal(t, "passed", got.Overall)
	assert.Equal(t, "artifact,render,dryrun", got.Stages)
	assert.False(t, got.UpdatedAt.Before(got.CreatedAt))

	// UpdateResult on a missing row reports not found.
	err = st.PreflightLifecycles().UpdateResult(ctx, uuid.New().String(), "failed", "artifact")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// ── Preflight Lifecycle GC (AC-069-09, AC-069-10) ──────────────────

// TestMigrateLegacyPreflightLifecycleSchema validates the REQ-019 two-phase
// schema rebuild against a real legacy-format database: duplicate operation
// rows, JSON stages, timeout overall, and terminal_at preservation.
func TestMigrateLegacyPreflightLifecycleSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE TABLE preflight_lifecycles (
		id                    TEXT PRIMARY KEY,
		operation_id          TEXT,
		operation_terminal_at TEXT,
		stages                TEXT NOT NULL DEFAULT '[]',
		overall               TEXT NOT NULL DEFAULT '',
		error_code            TEXT NOT NULL DEFAULT '',
		created_at            TEXT NOT NULL
	)`)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE INDEX idx_preflight_lifecycles_operation ON preflight_lifecycles(operation_id)`)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE INDEX idx_preflight_lifecycles_terminal ON preflight_lifecycles(operation_terminal_at)`)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	early := now.Add(-2 * time.Hour).Format(time.RFC3339)
	late := now.Add(-1 * time.Hour).Format(time.RFC3339)
	terminal := now.Add(-30 * time.Minute).Format(time.RFC3339)

	seed := []string{
		// op-dup: two rows for the same operation — dedupe keeps the earliest
		// created_at while carrying the latest result and the non-null terminal.
		`INSERT INTO preflight_lifecycles (id, operation_id, operation_terminal_at, stages, overall, error_code, created_at) VALUES ('dup-1', 'op-dup', NULL, '[]', 'running', '', '` + early + `')`,
		`INSERT INTO preflight_lifecycles (id, operation_id, operation_terminal_at, stages, overall, error_code, created_at) VALUES ('dup-2', 'op-dup', '` + terminal + `', '[{"stage":"artifact","status":"passed"},{"stage":"render","status":"passed"}]', 'passed', '', '` + late + `')`,
		// op-timeout: JSON stages + timeout overall → cancelled.
		`INSERT INTO preflight_lifecycles (id, operation_id, operation_terminal_at, stages, overall, error_code, created_at) VALUES ('timeout-1', 'op-timeout', NULL, '[]', 'timeout', 'stage_timeout', '` + late + `')`,
		// exploratory (no operation) survives.
		`INSERT INTO preflight_lifecycles (id, operation_id, operation_terminal_at, stages, overall, error_code, created_at) VALUES ('expl-1', NULL, NULL, '[]', 'passed', '', '` + late + `')`,
	}
	for _, stmt := range seed {
		_, err = raw.Exec(stmt)
		require.NoError(t, err)
	}
	require.NoError(t, raw.Close())

	// Open through the store so the migration hook rebuilds the legacy table.
	st, err := sqlitestore.Open(path)
	require.NoError(t, err)
	defer st.Close()
	ctx := context.Background()

	var count int
	require.NoError(t, st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM preflight_lifecycles WHERE operation_id = 'op-dup'`).Scan(&count))
	assert.Equal(t, 1, count, "duplicate operation rows must be deduplicated")

	var stages, overall, createdAt, updatedAt, terminalAt string
	require.NoError(t, st.DB().QueryRowContext(ctx, `
		SELECT stages, overall, created_at, updated_at, COALESCE(operation_terminal_at, '')
		FROM preflight_lifecycles WHERE operation_id = 'op-dup'`).Scan(&stages, &overall, &createdAt, &updatedAt, &terminalAt))
	assert.Equal(t, "artifact,render", stages, "JSON stages must convert to canonical names")
	assert.Equal(t, "passed", overall, "latest result must win")
	assert.Equal(t, early, createdAt, "earliest created_at must be preserved")
	assert.Equal(t, early, updatedAt, "updated_at must be backfilled from created_at")
	assert.Equal(t, terminal, terminalAt, "terminal_at must be preserved")

	require.NoError(t, st.DB().QueryRowContext(ctx, `SELECT overall FROM preflight_lifecycles WHERE operation_id = 'op-timeout'`).Scan(&overall))
	assert.Equal(t, "cancelled", overall, "legacy timeout must map to cancelled")

	require.NoError(t, st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM preflight_lifecycles WHERE operation_id IS NULL`).Scan(&count))
	assert.Equal(t, 1, count, "exploratory rows without operation must survive")

	// New shape: updated_at present, error_code gone, operation_id unique.
	rows, err := st.DB().QueryContext(ctx, `PRAGMA table_info(preflight_lifecycles)`)
	require.NoError(t, err)
	cols := map[string]bool{}
	for rows.Next() {
		var pos, notNull, pk int
		var name, colType string
		var def any
		require.NoError(t, rows.Scan(&pos, &name, &colType, &notNull, &def, &pk))
		cols[name] = true
	}
	require.NoError(t, rows.Close())
	assert.True(t, cols["updated_at"], "updated_at column must exist")
	assert.False(t, cols["error_code"], "error_code column must be removed")

	_, err = st.DB().ExecContext(ctx, `
		INSERT INTO preflight_lifecycles (id, operation_id, stages, overall, created_at, updated_at)
		VALUES ('dup-3', 'op-dup', '', 'running', ?, ?)`, late, late)
	require.Error(t, err, "operation_id uniqueness must be enforced")
}

func TestPreflightLifecycleDeleteExpired(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	old := now.Add(-8 * 24 * time.Hour).Format(time.RFC3339)

	// Exploratory preflight (no operation) — old → deleted (AC-069-09). Inserted
	// directly: the two-phase contract no longer creates exploratory rows.
	_, err := st.DB().ExecContext(ctx, `
		INSERT INTO preflight_lifecycles (id, operation_id, operation_terminal_at, stages, overall, created_at, updated_at)
		VALUES (?, NULL, NULL, '', 'passed', ?, ?)`,
		"exploratory", old, old)
	require.NoError(t, err)

	// Preflight without terminal_at — should survive (AC-069-10).
	opID := uuid.New().String()
	pl, err := st.PreflightLifecycles().CreateOrReset(ctx, opID)
	require.NoError(t, err)
	_, err = st.DB().ExecContext(ctx,
		`UPDATE preflight_lifecycles SET created_at=?, updated_at=? WHERE id=?`,
		old, old, pl.ID)
	require.NoError(t, err)

	// Preflight with terminal_at set — old → deleted.
	opID2 := uuid.New().String()
	pl2, err := st.PreflightLifecycles().CreateOrReset(ctx, opID2)
	require.NoError(t, err)
	_, err = st.DB().ExecContext(ctx,
		`UPDATE preflight_lifecycles SET operation_terminal_at=?, created_at=?, updated_at=? WHERE id=?`,
		old, old, old, pl2.ID)
	require.NoError(t, err)

	// GC: TTL = 7 days.
	n, err := st.PreflightLifecycles().DeleteExpired(ctx, 7*24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n) // exploratory + terminal (both > 7d)
}

// ── Operation Terminal Callback (REQ-069 integration) ───────────

func TestOperationTransition_SetsPreflightTerminal(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	// Create a preflight lifecycle record first.
	opID := uuid.New().String()
	_, err := st.PreflightLifecycles().CreateOrReset(ctx, opID)
	require.NoError(t, err)

	// Create an operation in running state.
	def := &store.ReleaseDefinition{
		ID:                uuid.New().String(),
		Name:              "term-cb-def",
		CustomerID:        "cust-6",
		ClusterID:         "cls-6",
		ReleaseName:       "term-cb-rel",
		Status:            store.DefStatusActive,
		OptimisticVersion: 1,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))

	op := &store.Operation{
		ID:                  opID,
		OperationType:       store.OperationInstall,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      uuid.New().String(),
		RequestHash:         "hash",
		BundleID:            "",
		StateVersion:        3,
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	// Transition to succeeded (terminal).
	updated, err := st.Operations().Transition(ctx, op.ID, store.StatusSucceeded, op.StateVersion, "")
	require.NoError(t, err)
	assert.Equal(t, store.StatusSucceeded, updated.Status)

	// AC-023-08: operation.terminal_at is set in the same transaction.
	require.NotNil(t, updated.TerminalAt, "returned Operation.TerminalAt must be non-nil")
	assert.False(t, updated.TerminalAt.IsZero())

	// Directly verify operations.terminal_at in the database (no sleep/indirection).
	var opTerminalAt *string
	err = st.DB().QueryRowContext(ctx, `SELECT terminal_at FROM operations WHERE id = ?`, opID).Scan(&opTerminalAt)
	require.NoError(t, err)
	require.NotNil(t, opTerminalAt, "operations.terminal_at must be non-nil after terminal transition")
	assert.NotEmpty(t, *opTerminalAt)

	// AC-023-09: preflight_lifecycles.operation_terminal_at is backfilled in the same transaction.
	var plTerminalAt *string
	err = st.DB().QueryRowContext(ctx, `SELECT operation_terminal_at FROM preflight_lifecycles WHERE operation_id = ?`, opID).Scan(&plTerminalAt)
	require.NoError(t, err)
	require.NotNil(t, plTerminalAt, "preflight_lifecycles.operation_terminal_at must be backfilled")
	assert.NotEmpty(t, *plTerminalAt)
	assert.Equal(t, *opTerminalAt, *plTerminalAt,
		"preflight_lifecycles.operation_terminal_at must match operations.terminal_at (same transaction timestamp)")
}

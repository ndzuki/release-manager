//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// TASK-218: the writer the read side always assumed. Until it existed, validated_at was
// never written in production, so ListValidated (and with it the emergency artifact
// selection) was permanently empty. This drives the real PostgreSQL path: create a bundle,
// link candidate artifacts to it the way the submission unit of work does, mark them, and
// assert the read model sees exactly the linked ones.
func TestMarkValidatedForBundleStampsLinkedCandidates(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	bundle := &store.ReleaseBundle{
		ID: uuid.NewString(), Name: "validation-bundle", DigestAlg: "sha256",
		DigestValue: uuid.NewString(), Status: store.BundleValidated, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))

	linked := &store.CandidateArtifact{
		ID: uuid.NewString(), ArtifactType: store.ArtifactImage,
		Ref: "registry.example.com/team/api@" + bundle.DigestValue, Digest: uuid.NewString(),
		CreatedAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(),
	}
	unlinked := &store.CandidateArtifact{
		ID: uuid.NewString(), ArtifactType: store.ArtifactImage,
		Ref: "registry.example.com/team/other@" + bundle.DigestValue, Digest: uuid.NewString(),
		CreatedAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(),
	}
	require.NoError(t, st.CandidateArtifacts().Create(ctx, linked))
	require.NoError(t, st.CandidateArtifacts().Create(ctx, unlinked))

	// Same call the submission unit of work makes: link by digest + artifact type, inside
	// the transaction that unit of work owns.
	linkTx := st.GORM().Begin()
	require.NoError(t, linkTx.Error)
	// Rollback hygiene: a transaction that is only committed on the happy path leaks when an
	// assertion aborts, and its row locks then block the schema cleanup (an independent
	// review caught exactly that: assertion failures showed up as 600s timeouts).
	defer func() { _ = linkTx.Rollback() }()
	require.NoError(t, st.CandidateArtifacts().LinkToBundleTx(
		linkTx, bundle.ID, []store.ArtifactDigest{{Digest: linked.Digest, ArtifactType: store.ArtifactImage}},
	))
	require.NoError(t, linkTx.Commit().Error)

	markedAt := time.Now().UTC().Truncate(time.Second)

	// The validation worker runs this inside the transaction that flips the bundle to
	// validated; here the same call runs in an equivalent manual transaction.
	tx := st.GORM().Begin()
	require.NoError(t, tx.Error)
	defer func() { _ = tx.Rollback() }()
	affected, err := st.CandidateArtifacts().MarkValidatedForBundleTx(tx, bundle.ID, markedAt)
	require.NoError(t, err)
	require.NoError(t, tx.Commit().Error)
	assert.EqualValues(t, 1, affected, "exactly the linked candidate is stamped")

	validated, err := st.CandidateArtifacts().ListValidated(ctx)
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, artifact := range validated {
		ids[artifact.ID] = true
	}
	assert.True(t, ids[linked.ID], "the linked candidate is now a candidate for emergency changes")
	assert.False(t, ids[unlinked.ID], "an unlinked artifact must not be stamped")

	// Re-running must not move an existing timestamp forward.
	again := st.GORM().Begin()
	require.NoError(t, again.Error)
	defer func() { _ = again.Rollback() }()
	affectedAgain, err := st.CandidateArtifacts().MarkValidatedForBundleTx(again, bundle.ID, markedAt.Add(time.Hour))
	require.NoError(t, err)
	require.NoError(t, again.Commit().Error)
	assert.EqualValues(t, 0, affectedAgain, "an already-validated artifact keeps its timestamp")

	stored, err := st.CandidateArtifacts().Get(ctx, linked.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.ValidatedAt)
	assert.WithinDuration(t, markedAt, *stored.ValidatedAt, time.Second)

	// The documented failure modes stay honest.
	_, err = st.CandidateArtifacts().MarkValidatedForBundleTx(nil, bundle.ID, markedAt)
	assert.ErrorContains(t, err, "nil transaction")
	_, err = st.CandidateArtifacts().MarkValidatedForBundleTx(st.GORM(), "", markedAt)
	assert.ErrorContains(t, err, "empty bundle id")
}

// TASK-220: production binds a candidate artifact to its bundle through
// bundle_candidate_artifacts (the artifact row has no bundle column on this engine), so
// anything resolving trust through the delivering bundle has to read that table.
func TestBundlesForArtifactReadsTheLinkTable(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	bundle := &store.ReleaseBundle{
		ID: uuid.NewString(), Name: "link-bundle", DigestAlg: "sha256",
		DigestValue: uuid.NewString(), Status: store.BundleValidated, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))

	linked := &store.CandidateArtifact{
		ID: uuid.NewString(), ArtifactType: store.ArtifactImage, Digest: uuid.NewString(),
		Ref:       "registry.example.com/team/api@" + uuid.NewString(),
		CreatedAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(),
	}
	unlinked := &store.CandidateArtifact{
		ID: uuid.NewString(), ArtifactType: store.ArtifactImage, Digest: uuid.NewString(),
		Ref:       "registry.example.com/team/other@" + uuid.NewString(),
		CreatedAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(),
	}
	require.NoError(t, st.CandidateArtifacts().Create(ctx, linked))
	require.NoError(t, st.CandidateArtifacts().Create(ctx, unlinked))
	require.NoError(t, st.CandidateArtifacts().LinkToBundle(ctx, linked.ID, bundle.ID))

	bundleIDs, err := st.CandidateArtifacts().BundlesForArtifact(ctx, linked.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{bundle.ID}, bundleIDs)

	none, err := st.CandidateArtifacts().BundlesForArtifact(ctx, unlinked.ID)
	require.NoError(t, err)
	assert.Empty(t, none, "an artifact no bundle delivered resolves to no bundle")

	unknown, err := st.CandidateArtifacts().BundlesForArtifact(ctx, uuid.NewString())
	require.NoError(t, err)
	assert.Empty(t, unknown)
}

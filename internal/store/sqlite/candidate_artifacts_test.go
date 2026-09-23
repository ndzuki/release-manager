package sqlite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// candidateArtifactIDs maps a validated list to its ids in returned order.
func candidateArtifactIDs(artifacts []*store.CandidateArtifact) []string {
	ids := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		ids = append(ids, artifact.ID)
	}
	return ids
}

// G11 (TASK-168 follow-up): ListValidated is defined by validation, not by
// fetchability. An artifact that passed validation is listed even when it has
// no fetch address (empty ref — SQLite has no location table at all), and an
// artifact that never passed validation is never listed. This engine is the
// reference definition; internal/store/postgres must match it.
func TestListValidatedIsValidationNotFetchability(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	older := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)

	require.NoError(t, st.CandidateArtifacts().Create(ctx, &store.CandidateArtifact{
		ID: "artifact-older", ArtifactType: store.ArtifactImage,
		Ref: "registry.example.com/team/api:1.0.0", Digest: "sha256:older",
		ValidatedAt: &older,
	}))
	// Validated but with no fetch address: must still be listed.
	require.NoError(t, st.CandidateArtifacts().Create(ctx, &store.CandidateArtifact{
		ID: "artifact-no-location", ArtifactType: store.ArtifactImage,
		Digest:      "sha256:nolocation",
		ValidatedAt: &newer,
	}))
	// Never validated: must never be listed.
	require.NoError(t, st.CandidateArtifacts().Create(ctx, &store.CandidateArtifact{
		ID: "artifact-unvalidated", ArtifactType: store.ArtifactImage,
		Ref: "registry.example.com/team/api:2.0.0", Digest: "sha256:unvalidated",
	}))

	got, err := st.CandidateArtifacts().ListValidated(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2, "only the two validated artifacts are listed")
	assert.Equal(t, []string{"artifact-no-location", "artifact-older"}, candidateArtifactIDs(got),
		"validated_at DESC: the newer validation comes first")
	require.NotNil(t, got[0].ValidatedAt)
	assert.True(t, got[0].ValidatedAt.Equal(newer), "validated_at must round-trip: got %s want %s", got[0].ValidatedAt, newer)
	require.NotNil(t, got[1].ValidatedAt)
	assert.True(t, got[1].ValidatedAt.Equal(older))
}

// The ordering (validated_at DESC, id ASC) is part of the cross-engine
// contract: equal validation timestamps must not leave the order up to the
// engine.
func TestListValidatedOrderingIsDeterministic(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	at := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

	for _, id := range []string{"artifact-b", "artifact-a"} {
		require.NoError(t, st.CandidateArtifacts().Create(ctx, &store.CandidateArtifact{
			ID: id, ArtifactType: store.ArtifactImage,
			Ref: "registry.example.com/team/api:1.0.0", Digest: "sha256:" + id,
			ValidatedAt: &at,
		}))
	}

	got, err := st.CandidateArtifacts().ListValidated(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"artifact-a", "artifact-b"}, candidateArtifactIDs(got))
}

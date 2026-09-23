//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// candidateParityFixture builds the identical G11 fixture for both engines.
// Fresh structs are returned per call because Create mutates its argument.
func candidateParityFixture(base time.Time) []*store.CandidateArtifact {
	createdAt := base.Add(-time.Hour)
	older := base
	newer := base.Add(time.Minute)
	return []*store.CandidateArtifact{
		{
			ID: "artifact-older", ArtifactType: store.ArtifactImage,
			Ref: "registry.example.com/team/api:1.0.0", Digest: "sha256:parity-older",
			CreatedAt: createdAt, LastSeenAt: createdAt, ValidatedAt: &older,
		},
		{
			// Validated with no fetch address: no candidate_artifact_locations
			// row on PostgreSQL (empty ref), no location concept at all on
			// SQLite. The old PostgreSQL predicate dropped this row.
			ID: "artifact-no-location", ArtifactType: store.ArtifactImage,
			Digest:    "sha256:parity-nolocation",
			CreatedAt: createdAt, LastSeenAt: createdAt, ValidatedAt: &newer,
		},
		{
			// Never validated but fetchable: the old PostgreSQL predicate
			// wrongly listed it.
			ID: "artifact-unvalidated", ArtifactType: store.ArtifactImage,
			Ref: "registry.example.com/team/api:2.0.0", Digest: "sha256:parity-unvalidated",
			CreatedAt: createdAt, LastSeenAt: createdAt,
		},
	}
}

// TestListValidatedParityAcrossEngines is the G11 gate: one fixture, two
// engines, one answer. PostgreSQL is compared against SQLite (the engine
// dev/e2e actually runs, and the reference definition of the predicate).
func TestListValidatedParityAcrossEngines(t *testing.T) {
	pg := setupStore(t)
	sq := sqlitestore.OpenTest(t)
	ctx := t.Context()
	base := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

	for _, candidate := range candidateParityFixture(base) {
		require.NoError(t, pg.CandidateArtifacts().Create(ctx, candidate))
	}
	for _, candidate := range candidateParityFixture(base) {
		require.NoError(t, sq.CandidateArtifacts().Create(ctx, candidate))
	}

	sqliteList, err := sq.CandidateArtifacts().ListValidated(ctx)
	require.NoError(t, err)
	postgresList, err := pg.CandidateArtifacts().ListValidated(ctx)
	require.NoError(t, err)

	want := []string{"artifact-no-location", "artifact-older"}
	assert.Equal(t, want, candidateArtifactIDs(sqliteList), "SQLite is the reference definition")
	assert.Equal(t, want, candidateArtifactIDs(postgresList),
		"PostgreSQL must return the same validated set in the same order")

	// The validation timestamps must survive on both engines: the orchestrator
	// filters on ValidatedAt and renders it, so a nil timestamp is a silent
	// empty list.
	for name, list := range map[string][]*store.CandidateArtifact{"sqlite": sqliteList, "postgres": postgresList} {
		require.Len(t, list, len(want), name)
		for _, artifact := range list {
			require.NotNil(t, artifact.ValidatedAt, "%s: %s must carry validated_at", name, artifact.ID)
		}
	}
}

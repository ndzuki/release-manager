package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// TASK-163 / AGENTS.md hard constraint 4: the SQLite engine used to store only the
// artifact *refs* while PostgreSQL also stored their digests
// (migrations/000007_bundle_services.up.sql), so a submitted signature/SBOM/provenance
// digest silently vanished on the SQLite engine. This pins the round trip.
func TestBundleArtifactDigestsRoundTrip(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	bundle := &store.ReleaseBundle{
		ID:               uuid.New().String(),
		Name:             "digest-bundle",
		DigestAlg:        "sha256",
		DigestValue:      "bundle-digest",
		Status:           store.BundleValidated,
		SignatureRef:     "registry.example.com/sig@sha256:abc",
		SignatureDigest:  "sha256:signature",
		SBOMRef:          "registry.example.com/sbom@sha256:def",
		SBOMDigest:       "sha256:sbom",
		ProvenanceRef:    "registry.example.com/prov@sha256:ghi",
		ProvenanceDigest: "sha256:provenance",
		CreatedAt:        time.Now().UTC().Truncate(time.Second),
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))

	got, err := st.Bundles().Get(ctx, bundle.ID)
	require.NoError(t, err)
	assert.Equal(t, bundle.SignatureDigest, got.SignatureDigest)
	assert.Equal(t, bundle.SBOMDigest, got.SBOMDigest)
	assert.Equal(t, bundle.ProvenanceDigest, got.ProvenanceDigest)
	assert.Equal(t, bundle.SignatureRef, got.SignatureRef)
	assert.Equal(t, bundle.SBOMRef, got.SBOMRef)
	assert.Equal(t, bundle.ProvenanceRef, got.ProvenanceRef)
}

// candidate_artifacts.last_seen_at existed only on PostgreSQL
// (migrations/000003_create_lifecycle_tables.up.sql): the SQLite engine neither stored
// nor returned it, so a re-observation could not be told from the first one.
func TestCandidateArtifactLastSeenAtRoundTrip(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	created := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	seen := created.Add(90 * time.Minute)
	artifact := &store.CandidateArtifact{
		ID:           uuid.New().String(),
		ArtifactType: store.ArtifactImage,
		Ref:          "registry.example.com/team/api:1.0.0",
		Digest:       "sha256:last-seen",
		CreatedAt:    created,
		LastSeenAt:   seen,
	}
	require.NoError(t, st.CandidateArtifacts().Create(ctx, artifact))

	got, err := st.CandidateArtifacts().Get(ctx, artifact.ID)
	require.NoError(t, err)
	assert.WithinDuration(t, seen, got.LastSeenAt, time.Second)

	// A later observation advances last_seen_at without touching created_at: that is the
	// behaviour the column exists for.
	later := seen.Add(30 * time.Minute)
	again := &store.CandidateArtifact{
		ID:           artifact.ID,
		ArtifactType: artifact.ArtifactType,
		Ref:          artifact.Ref,
		Digest:       artifact.Digest,
		CreatedAt:    created,
		LastSeenAt:   later,
	}
	require.NoError(t, st.CandidateArtifacts().Create(ctx, again))
	updated, err := st.CandidateArtifacts().Get(ctx, artifact.ID)
	require.NoError(t, err)
	assert.WithinDuration(t, later, updated.LastSeenAt, time.Second)
	assert.WithinDuration(t, created, updated.CreatedAt, time.Second)
}

// The column is added without a default, so rows written before this change kept NULL --
// and PostgreSQL declares candidate_artifacts.last_seen_at TIMESTAMPTZ NOT NULL, so a
// SQLite→PostgreSQL cutover (internal/migration) would fail inserting them. Reopening a
// legacy database must backfill from created_at. This drives the real migration list: the
// store re-runs it on every Open.
func TestCandidateArtifactLastSeenAtBackfillsLegacyRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	created, err := sqlitestore.Open(path)
	require.NoError(t, err)
	require.NoError(t, created.Close())

	// Bring the table back to the pre-TASK-235 shape, where last_seen_at is nullable and
	// a row written before the column existed holds NULL.
	downgradeCandidateArtifactsNullable(t, path)

	ctx := context.Background()
	createdAt := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	artifactID := uuid.New().String()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		INSERT INTO candidate_artifacts (id, artifact_type, ref, digest, created_at)
		VALUES (?, 'image', 'registry.example.com/team/api:1.0.0', 'sha256:legacy-null', ?)`,
		artifactID, createdAt.Format(time.RFC3339Nano))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	reopened, err := sqlitestore.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	var nulls int
	require.NoError(t, reopened.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM candidate_artifacts WHERE last_seen_at IS NULL`).Scan(&nulls))
	assert.Zero(t, nulls, "every legacy row must be backfilled or the cutover hits NOT NULL")

	got, err := reopened.CandidateArtifacts().Get(ctx, artifactID)
	require.NoError(t, err)
	assert.WithinDuration(t, createdAt, got.LastSeenAt, time.Second)
}

//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// TASK-163 / AGENTS.md hard constraint 4: the artifact-reference digests are part of the
// bundle record (common.v1.ArtifactReference carries ref + digest). PostgreSQL always
// stored them; SQLite did not, so the same submission read back differently per engine.
// This is the cross-engine contract that keeps them aligned.
func TestBundleArtifactDigestsParityAcrossEngines(t *testing.T) {
	pg := setupStore(t)
	sq := sqlitestore.OpenTest(t)
	ctx := t.Context()

	newBundle := func() *store.ReleaseBundle {
		return &store.ReleaseBundle{
			ID:               uuid.New().String(),
			Name:             "parity-bundle",
			DigestAlg:        "sha256",
			DigestValue:      "sha256:parity-bundle",
			Status:           store.BundleValidated,
			ChartRef:         "registry.example.com/charts/api",
			ChartVersion:     "1.2.3",
			ChartDigest:      "sha256:chart",
			GitCommit:        "deadbeef",
			PipelineID:       "pipeline-1",
			SignatureRef:     "registry.example.com/sig@sha256:aaa",
			SignatureDigest:  "sha256:signature",
			SBOMRef:          "registry.example.com/sbom@sha256:bbb",
			SBOMDigest:       "sha256:sbom",
			ProvenanceRef:    "registry.example.com/prov@sha256:ccc",
			ProvenanceDigest: "sha256:provenance",
			CreatedAt:        time.Date(2026, 9, 29, 7, 0, 0, 0, time.UTC),
		}
	}

	// The same record on both engines; Create mutates its argument, so each engine gets
	// its own copy of the identical bundle (same id).
	want := newBundle()
	pgCopy, sqCopy := *want, *want
	require.NoError(t, pg.Bundles().Create(ctx, &pgCopy))
	require.NoError(t, sq.Bundles().Create(ctx, &sqCopy))

	pgBundle, err := pg.Bundles().Get(ctx, want.ID)
	require.NoError(t, err)
	sqBundle, err := sq.Bundles().Get(ctx, want.ID)
	require.NoError(t, err)

	for name, field := range map[string]func(*store.ReleaseBundle) string{
		"signature_digest":  func(b *store.ReleaseBundle) string { return b.SignatureDigest },
		"sbom_digest":       func(b *store.ReleaseBundle) string { return b.SBOMDigest },
		"provenance_digest": func(b *store.ReleaseBundle) string { return b.ProvenanceDigest },
	} {
		assert.Equal(t, field(want), field(pgBundle), "postgres %s", name)
		assert.Equal(t, field(want), field(sqBundle), "sqlite %s", name)
	}
	assert.Equal(t, pgBundle.SignatureRef, sqBundle.SignatureRef)
	assert.Equal(t, pgBundle.SBOMDigest, sqBundle.SBOMDigest)
	assert.Equal(t, pgBundle.ProvenanceDigest, sqBundle.ProvenanceDigest)
}

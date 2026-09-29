//go:build integration

package orchestrator

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
)

// TASK-218: the validation worker is the ONLY production writer of
// candidate_artifacts.validated_at, and an independent review found the wiring had no test
// at all -- the store method was covered, the call site was not. This drives the worker's
// real outbox cycle: candidate artifacts linked to a received bundle must come out
// validated once the bundle is validated, in the same transaction.
func TestValidationWorkerMarksCandidateArtifacts(t *testing.T) {
	st := bundleServiceStore(t)
	pgStore, ok := st.(*postgresstore.Store)
	if !ok {
		t.Fatalf("the validation worker needs the PostgreSQL store, got %T", st)
	}
	ctx := t.Context()

	bundle := &store.ReleaseBundle{
		ID: "bundle-worker-validated", Name: "worker", DigestAlg: "sha256",
		DigestValue: "worker-digest", Status: store.BundleReceived, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))

	image := &store.CandidateArtifact{
		ID: "ca-worker-image", ArtifactType: store.ArtifactImage, Digest: "sha256:worker-image",
		Ref:       "registry.example.com/team/api@sha256:worker-image",
		CreatedAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(),
	}
	require.NoError(t, st.CandidateArtifacts().Create(ctx, image))

	linkTx := pgStore.GORM()
	require.NoError(t, st.CandidateArtifacts().LinkToBundleTx(linkTx, bundle.ID,
		[]store.ArtifactDigest{{Digest: image.Digest, ArtifactType: store.ArtifactImage}}))

	entry := &store.ValidationOutboxEntry{
		ID: "vo-worker-validated", BundleID: bundle.ID, Status: store.ValidationPending,
		NextAttemptAt: time.Now().UTC(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, st.ValidationOutbox().CreateTx(pgStore.GORM(), entry))

	worker := NewValidationWorker(st, pgStore.GORM(), slog.New(slog.NewTextHandler(io.Discard, nil)), DefaultValidationWorkerConfig())
	worker.processOutbox(ctx)

	validated, err := st.Bundles().Get(ctx, bundle.ID)
	require.NoError(t, err)
	if validated.Status != store.BundleValidated {
		t.Fatalf("bundle status = %q, want %q", validated.Status, store.BundleValidated)
	}

	stored, err := st.CandidateArtifacts().Get(ctx, image.ID)
	require.NoError(t, err)
	if stored.ValidatedAt == nil {
		t.Fatal("the linked candidate artifact was not marked validated by the worker")
	}

	// The read side the emergency flow uses must see it.
	validatedCandidates, err := st.CandidateArtifacts().ListValidated(ctx)
	require.NoError(t, err)
	found := false
	for _, candidate := range validatedCandidates {
		if candidate.ID == image.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("ListValidated does not report the artifact the worker validated")
	}
}

// The rollback half: if marking fails, the bundle must NOT be validated and the outbox
// entry must not be completed -- the whole unit of work is one transaction.
func TestValidationWorkerRollsBackWhenMarkingFails(t *testing.T) {
	st := bundleServiceStore(t)
	pgStore, ok := st.(*postgresstore.Store)
	if !ok {
		t.Fatalf("the validation worker needs the PostgreSQL store, got %T", st)
	}
	ctx := t.Context()

	bundle := &store.ReleaseBundle{
		ID: "bundle-worker-rollback", Name: "worker-rollback", DigestAlg: "sha256",
		DigestValue: "worker-rollback-digest", Status: store.BundleReceived, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))
	entry := &store.ValidationOutboxEntry{
		ID: "vo-worker-rollback", BundleID: bundle.ID, Status: store.ValidationPending,
		NextAttemptAt: time.Now().UTC(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, st.ValidationOutbox().CreateTx(pgStore.GORM(), entry))

	worker := NewValidationWorker(failingMarkStore{Store: st}, pgStore.GORM(),
		slog.New(slog.NewTextHandler(io.Discard, nil)), DefaultValidationWorkerConfig())
	worker.processOutbox(ctx)

	after, err := st.Bundles().Get(ctx, bundle.ID)
	require.NoError(t, err)
	if after.Status != store.BundleReceived {
		t.Fatalf("bundle status = %q, want it rolled back to %q", after.Status, store.BundleReceived)
	}
}

// failingMarkStore delegates everything to the real store except the marking call.
type failingMarkStore struct {
	store.Store
}

func (f failingMarkStore) CandidateArtifacts() store.CandidateArtifactStore {
	return failingMarkCandidates{CandidateArtifactStore: f.Store.CandidateArtifacts()}
}

type failingMarkCandidates struct {
	store.CandidateArtifactStore
}

func (f failingMarkCandidates) MarkValidatedForBundleTx(*gorm.DB, string, time.Time) (int64, error) {
	return 0, errors.New("injected mark failure")
}

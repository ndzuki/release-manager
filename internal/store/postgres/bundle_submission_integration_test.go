//go:build integration

package postgres_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// bundleSubmission builds the submission the orchestrator hands to Submit.
func bundleSubmission(digest string, idempotency *store.IdempotencyRecord) store.BundleSubmission {
	now := time.Now().UTC()
	return store.BundleSubmission{
		Bundle: &store.ReleaseBundle{
			ID: uuid.NewString(), Name: "bundle-" + digest, DigestAlg: "sha256", DigestValue: digest,
			Status: store.BundleReceived, CreatedAt: now,
		},
		Idempotency: idempotency,
	}
}

// AC-011-02: submitting the same digest twice returns the original bundle
// rather than creating a second row.
func TestSubmitBundleSameDigestReturnsTheOriginal(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	digest := uuid.NewString()

	first, created, err := st.BundleSubmissions().Submit(ctx, bundleSubmission(digest, nil))
	require.NoError(t, err)
	require.True(t, created)

	second, created, err := st.BundleSubmissions().Submit(ctx, bundleSubmission(digest, nil))
	require.NoError(t, err)
	assert.False(t, created, "AC-011-02: the second submission must not create a row")
	assert.Equal(t, first.ID, second.ID, "AC-011-02: both submissions return the same bundle_id")
	// PostgreSQL TIMESTAMPTZ keeps microseconds, so the round-tripped value is the
	// in-memory one truncated -- compare at storage precision.
	assert.Equal(t, first.CreatedAt.UTC().Truncate(time.Microsecond), second.CreatedAt.UTC().Truncate(time.Microsecond),
		"AC-011-02: created_at is the original's")
	assert.Equal(t, store.BundleReceived, second.Status)
}

// AC-011-10: the idempotency key replays an identical request and rejects a
// different body under the same key.
func TestSubmitBundleIdempotencyKey(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	digest := uuid.NewString()
	scope := "identity:org-1:SubmitBundle"
	record := func(hash string) *store.IdempotencyRecord {
		return &store.IdempotencyRecord{Scope: scope, Key: "key-1", RequestHash: hash, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	}

	first, created, err := st.BundleSubmissions().Submit(ctx, bundleSubmission(digest, record("hash-a")))
	require.NoError(t, err)
	require.True(t, created)

	replay, created, err := st.BundleSubmissions().Submit(ctx, bundleSubmission(digest, record("hash-a")))
	require.NoError(t, err)
	assert.False(t, created, "AC-011-10: a same-key same-content replay returns the original")
	assert.Equal(t, first.ID, replay.ID)

	_, _, err = st.BundleSubmissions().Submit(ctx, bundleSubmission(uuid.NewString(), record("hash-b")))
	require.Error(t, err, "AC-011-10: the same key with different content must conflict")
	assert.ErrorIs(t, err, store.ErrIdempotencyConflict)
}

// AC-011-11: two concurrent submissions of identical content produce one bundle;
// the loser reads the winner back instead of failing.
func TestSubmitBundleConcurrentSameContent(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	digest := uuid.NewString()

	var wg sync.WaitGroup
	type outcome struct {
		bundle  *store.ReleaseBundle
		created bool
		err     error
	}
	results := make([]outcome, 2)
	for index := range results {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			bundle, created, err := st.BundleSubmissions().Submit(ctx, bundleSubmission(digest, nil))
			results[slot] = outcome{bundle: bundle, created: created, err: err}
		}(index)
	}
	wg.Wait()

	for _, result := range results {
		require.NoError(t, result.err, "AC-011-11: the loser must read the winner back, not fail")
	}
	assert.Equal(t, results[0].bundle.ID, results[1].bundle.ID, "AC-011-11: both see one bundle")
	createdCount := 0
	for _, result := range results {
		if result.created {
			createdCount++
		}
	}
	assert.Equal(t, 1, createdCount, "AC-011-11: exactly one submission creates the bundle")
}

// AC-011-12 / AC-011-13: replaying an artifact event is idempotent, and the same
// (source_id, event_id) with a different payload hash is a conflict.
func TestRecordArtifactEventIdempotency(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	sourceID, eventID := "harbor", uuid.NewString()

	submission := func(hash string) store.ArtifactEventSubmission {
		return store.ArtifactEventSubmission{
			Event: &store.ArtifactEvent{
				ID: uuid.NewString(), SourceID: sourceID, EventID: eventID, EventType: "harbor.artifact.pushed",
				OccurredAt: now, ReceivedAt: now, RawPayload: "{}", PayloadSHA256: hash,
				ArtifactType: store.ArtifactImage, Repository: "reg/app",
			},
			Candidates: []*store.CandidateArtifact{{
				ID: uuid.NewString(), ArtifactType: store.ArtifactImage, Ref: "reg/app:v1",
				Digest: "sha256:" + uuid.NewString(), SourceID: sourceID, CreatedAt: now, LastSeenAt: now,
			}},
		}
	}

	first, err := st.ArtifactEventSubmissions().Record(ctx, submission("hash-a"))
	require.NoError(t, err)
	require.True(t, first.Created)

	replay, err := st.ArtifactEventSubmissions().Record(ctx, submission("hash-a"))
	require.NoError(t, err)
	assert.False(t, replay.Created, "AC-011-12: the same payload hash is a replay")
	assert.EqualValues(t, 0, replay.NewCandidates, "AC-011-12: no duplicate candidate rows")

	_, err = st.ArtifactEventSubmissions().Record(ctx, submission("hash-b"))
	require.Error(t, err, "AC-011-13: the same event with a different hash must conflict")
	assert.ErrorIs(t, err, store.ErrIdempotencyConflict)
}

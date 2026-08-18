//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

func TestArtifactLifecycleMigrationContract(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	bundle := &store.ReleaseBundle{
		ID: uuid.NewString(), Name: "migration-contract", DigestAlg: "sha256", DigestValue: uuid.NewString(),
		Status: store.BundleValidated,
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))
	_, err := st.SQLDB().ExecContext(ctx, "UPDATE release_bundles SET status = 'invalid' WHERE id = $1", bundle.ID)
	require.Error(t, err, "release_bundles status CHECK must reject invalid values")

	var indexCount int
	require.NoError(t, st.SQLDB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pg_indexes
		WHERE schemaname = current_schema()
		  AND indexname IN ('idx_pl_terminal_created', 'idx_pl_opid', 'idx_ci_created')`).Scan(&indexCount))
	assert.Equal(t, 3, indexCount)
}

func TestRejectedBundleIsEligibleForArchive(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	bundle := &store.ReleaseBundle{
		ID: uuid.NewString(), Name: "rejected-archive", DigestAlg: "sha256", DigestValue: uuid.NewString(),
		Status: store.BundleRejected,
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))
	_, err := st.SQLDB().ExecContext(ctx, "UPDATE release_bundles SET created_at = $1 WHERE id = $2", time.Now().UTC().Add(-100*24*time.Hour), bundle.ID)
	require.NoError(t, err)

	ids, err := st.Bundles().ListForArchive(ctx, 90, []store.OperationStatus{
		store.StatusSucceeded, store.StatusFailed, store.StatusCancelled, store.StatusTimeout,
	})
	require.NoError(t, err)
	assert.Contains(t, ids, bundle.ID)
}

func TestPreflightDeleteExpiredJoinFallback(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	definition := createTestDefinition(t, st)
	opID := uuid.NewString()
	old := time.Now().UTC().Add(-10 * 24 * time.Hour)
	require.NoError(t, st.Operations().Create(ctx, &store.Operation{
		ID: opID, OperationType: store.OperationInstall, Status: store.StatusSucceeded,
		ReleaseDefinitionID: definition.ID, IdempotencyKey: uuid.NewString(), RequestHash: "join-fallback",
	}))
	_, err := st.SQLDB().ExecContext(ctx, "UPDATE operations SET terminal_at = $1 WHERE id = $2", old, opID)
	require.NoError(t, err)
	_, err = st.SQLDB().ExecContext(ctx, `
		INSERT INTO preflight_lifecycles (id, operation_id, operation_terminal_at, stages, overall, created_at, updated_at)
		VALUES ($1, $2, NULL, '', 'passed', $3, $3)`, uuid.NewString(), opID, old)
	require.NoError(t, err)

	deleted, err := st.PreflightLifecycles().DeleteExpired(ctx, 7*24*time.Hour, 7*24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
}

func TestCleanupIdempotencyRetentionWindow(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	key := "integration-cleanup-" + uuid.NewString()
	idem := st.CleanupIdempotency()
	require.NoError(t, idem.TryCreate(ctx, key, 24*time.Hour))
	err := idem.TryCreate(ctx, key, 24*time.Hour)
	require.Error(t, err)
	assert.True(t, errors.Is(err, store.ErrCleanupAlreadyRequested))
	_, err = st.SQLDB().ExecContext(ctx, "UPDATE cleanup_idempotency SET created_at = $1 WHERE idempotency_key = $2", time.Now().UTC().Add(-48*time.Hour), key)
	require.NoError(t, err)
	require.NoError(t, idem.TryCreate(ctx, key, 24*time.Hour))
}

// AC-069-06/07/29: the store-level Unarchive CAS serializes concurrent restores
// on the same row (FOR UPDATE), restores only validated-archived bundles, and
// leaves received/rejected archives untouched.
func TestUnarchiveCASConcurrentRestores(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	bundle := &store.ReleaseBundle{
		ID: uuid.NewString(), Name: "cas-concurrent", DigestAlg: "sha256", DigestValue: uuid.NewString(),
		Status: store.BundleValidated,
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))
	_, err := st.Bundles().Archive(ctx, []string{bundle.ID})
	require.NoError(t, err)

	const workers = 4
	start := make(chan struct{})
	results := make(chan error, workers)
	for range workers {
		go func() {
			<-start
			_, err := st.Bundles().Unarchive(ctx, bundle.ID)
			results <- err
		}()
	}
	close(start)
	var successes int
	for range workers {
		if err := <-results; err == nil {
			successes++
		}
	}
	assert.Equal(t, workers, successes, "validated-archived unarchive must be idempotent under concurrency")

	got, err := st.Bundles().Get(ctx, bundle.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleValidated, got.Status)
	assert.Nil(t, got.ArchivedAt)

	// received-archived bundles must not be restorable by the CAS.
	received := &store.ReleaseBundle{
		ID: uuid.NewString(), Name: "cas-received", DigestAlg: "sha256", DigestValue: uuid.NewString(),
		Status: store.BundleReceived,
	}
	require.NoError(t, st.Bundles().Create(ctx, received))
	_, err = st.Bundles().Archive(ctx, []string{received.ID})
	require.NoError(t, err)
	_, err = st.Bundles().Unarchive(ctx, received.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, store.ErrBundleNotReady))
	got, err = st.Bundles().Get(ctx, received.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleArchived, got.Status, "received archive must stay archived")
}

var _ *sql.DB

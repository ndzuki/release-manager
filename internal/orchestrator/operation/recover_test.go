package operation

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestRecoverNonTerminal(t *testing.T) {
	st, err := sqlitestore.Open(t.TempDir() + "/recover.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	ctx := context.Background()
	def := &store.ReleaseDefinition{
		ID:                "recover-definition",
		Name:              "recover-definition",
		CustomerID:        "recover-customer",
		ClusterID:         "recover-cluster",
		Status:            store.DefStatusActive,
		OptimisticVersion: 1,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))

	now := time.Now().UTC()
	deadline := now.Add(-time.Minute)
	operations := []*store.Operation{
		{
			ID:                  "expired-running",
			OperationType:       store.OperationUpgrade,
			Status:              store.StatusRunning,
			ReleaseDefinitionID: def.ID,
			IdempotencyKey:      "expired-running-key",
			RequestHash:         "expired-running-hash",
			StateVersion:        3,
			Deadline:            &deadline,
			CreatedAt:           now.Add(-time.Hour),
			UpdatedAt:           now.Add(-time.Hour),
		},
		{
			ID:                  "stale-cancelling",
			OperationType:       store.OperationEmergency,
			Status:              store.StatusCancelling,
			ReleaseDefinitionID: def.ID,
			IdempotencyKey:      "stale-cancelling-key",
			RequestHash:         "stale-cancelling-hash",
			StateVersion:        7,
			CreatedAt:           now.Add(-time.Hour),
			UpdatedAt:           now.Add(-time.Hour),
		},
		{
			ID:                  "active-queued",
			OperationType:       store.OperationEmergency,
			Status:              store.StatusQueued,
			ReleaseDefinitionID: def.ID,
			IdempotencyKey:      "active-queued-key",
			RequestHash:         "active-queued-hash",
			StateVersion:        2,
			CreatedAt:           now,
			UpdatedAt:           now,
		},
	}
	for _, op := range operations {
		require.NoError(t, st.Operations().Create(ctx, op))
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recovered := RecoverNonTerminal(ctx, st, logger, RecoverOptions{
		DeadlineGracePeriod: 0,
		CancellingTimeout:   time.Minute,
	})
	assert.Equal(t, 2, recovered)

	expired, err := st.Operations().Get(ctx, "expired-running")
	require.NoError(t, err)
	assert.Equal(t, store.StatusTimeout, expired.Status)
	assert.Equal(t, 4, expired.StateVersion)
	assert.Contains(t, expired.LastError, "deadline exceeded")

	stale, err := st.Operations().Get(ctx, "stale-cancelling")
	require.NoError(t, err)
	assert.Equal(t, store.StatusFailed, stale.Status)
	assert.Equal(t, 8, stale.StateVersion)
	assert.Contains(t, stale.LastError, "did not acknowledge")

	active, err := st.Operations().Get(ctx, "active-queued")
	require.NoError(t, err)
	assert.Equal(t, store.StatusQueued, active.Status)
	assert.Equal(t, 2, active.StateVersion)
}

func TestRecoverOne_SkipsStaleStateVersion(t *testing.T) {
	st, err := sqlitestore.Open(t.TempDir() + "/recover-cas.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	ctx := context.Background()
	def := &store.ReleaseDefinition{
		ID: "recover-cas-definition", Name: "recover-cas-definition",
		CustomerID: "recover-cas-customer", ClusterID: "recover-cas-cluster",
		Status: store.DefStatusActive, OptimisticVersion: 1,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))

	now := time.Now().UTC()
	deadline := now.Add(-time.Minute)
	op := &store.Operation{
		ID: "recover-cas-operation", OperationType: store.OperationUpgrade,
		Status: store.StatusRunning, ReleaseDefinitionID: def.ID,
		IdempotencyKey: "recover-cas-key", RequestHash: "recover-cas-hash",
		StateVersion: 3, Deadline: &deadline,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	stale, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	_, err = st.DB().ExecContext(ctx, `UPDATE operations SET state_version = 4 WHERE id = ?`, op.ID)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recovered := recoverOne(ctx, st, logger, stale, RecoverOptions{}, now)
	assert.Zero(t, recovered)

	persisted, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusRunning, persisted.Status)
	assert.Equal(t, 4, persisted.StateVersion)
}

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

// TestPostgresUnitOfWork_AtomicCommit covers AC-067-19 on the PostgreSQL
// adapter: operation, dispatch, archived bundle CAS restore, definition
// current_bundle_id, and candidate artifact links commit atomically.
func TestPostgresUnitOfWork_AtomicCommit(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	bundle := &store.ReleaseBundle{
		ID: uuid.New().String(), Name: "pg-uow-bundle", DigestAlg: "sha256",
		DigestValue: uuid.New().String(), Status: store.BundleArchived,
		ChartDigest: "sha256:chart-pg-uow",
		Images:      []store.BundleImage{{Ref: "app:v1", Digest: "sha256:image-pg-uow", ValueKind: store.ImageValueFullReference}},
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))
	_, err := st.SQLDB().ExecContext(ctx, `
		UPDATE release_bundles
		SET archived_at=now(), archived_from_status='validated'
		WHERE id=$1
	`, bundle.ID)
	require.NoError(t, err)

	definition := &store.ReleaseDefinition{
		ID: uuid.New().String(), Name: "pg-uow-definition", CustomerID: "pg-uow-customer",
		ClusterID: "pg-uow-cluster", ReleaseName: "pg-uow-release", Status: store.DefStatusActive,
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
	_, err = st.Outbox().GetByCommandID(ctx, dispatch.CommandID)
	require.NoError(t, err)
}

// TestPostgresUnitOfWork_AuthorizationFence covers AC-067-22 on the
// PostgreSQL adapter: a stale authorization snapshot aborts the transaction
// with no writes.
func TestPostgresUnitOfWork_AuthorizationFence(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	definition := &store.ReleaseDefinition{
		ID: uuid.New().String(), Name: "pg-fence-definition", CustomerID: "pg-fence-customer",
		ClusterID: "pg-fence-cluster", ReleaseName: "pg-fence-release", Status: store.DefStatusActive,
	}
	require.NoError(t, st.Definitions().Create(ctx, definition, nil))
	bundle := &store.ReleaseBundle{
		ID: uuid.New().String(), Name: "pg-fence-bundle", DigestAlg: "sha256",
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
		ExpectedAuthorizationVersion: 42,
	})
	require.ErrorIs(t, err, store.ErrAuthorizationStale)

	_, err = st.Operations().Get(ctx, operationRecord.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.Outbox().GetByCommandID(ctx, dispatch.CommandID)
	require.ErrorIs(t, err, store.ErrNotFound)
}

// TestPostgresUnitOfWork_NilDispatchSkipsOutbox covers TASK-082 AC-082-02 on
// the PostgreSQL adapter: a nil Dispatch (UPGRADE carries no preflight stages;
// runUpgrade builds :execute itself) commits the operation with no outbox row.
func TestPostgresUnitOfWork_NilDispatchSkipsOutbox(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	definition := &store.ReleaseDefinition{
		ID: uuid.New().String(), Name: "pg-nil-dispatch-definition", CustomerID: "pg-nil-dispatch-customer",
		ClusterID: "pg-nil-dispatch-cluster", ReleaseName: "pg-nil-dispatch-release", Status: store.DefStatusActive,
	}
	require.NoError(t, st.Definitions().Create(ctx, definition, nil))
	bundle := &store.ReleaseBundle{
		ID: uuid.New().String(), Name: "pg-nil-dispatch-bundle", DigestAlg: "sha256",
		DigestValue: uuid.New().String(), Status: store.BundleValidated,
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))

	now := time.Now().UTC()
	operationRecord := &store.Operation{
		ID: uuid.New().String(), OperationType: store.OperationUpgrade, Status: store.StatusPending,
		ReleaseDefinitionID: definition.ID, IdempotencyKey: uuid.New().String(), IdempotencyScope: "org:" + definition.ID,
		RequestHash: uuid.New().String(), BundleID: bundle.ID, CreatedAt: now, UpdatedAt: now,
	}

	result, err := st.OperationCreationUnitOfWork()(ctx, store.OperationCreationRequest{
		Operation: operationRecord, Dispatch: nil, CandidateArtifactDigests: []string{},
	})
	require.NoError(t, err)
	assert.NotNil(t, result.Operation)

	storedOperation, err := st.Operations().Get(ctx, operationRecord.ID)
	require.NoError(t, err)
	assert.Equal(t, bundle.ID, storedOperation.BundleID)

	var outboxRows int
	require.NoError(t, st.SQLDB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM outbox WHERE operation_id = $1
	`, operationRecord.ID).Scan(&outboxRows))
	assert.Zero(t, outboxRows, "nil dispatch must not create any outbox row")

	_, err = st.Outbox().GetByCommandID(ctx, operationRecord.ID+":artifact")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// TestPostgresUnitOfWork_RollbackOnPartialFailure verifies that a failure
// mid-transaction leaves no partial writes (AC-067-19 atomicity).
func TestPostgresUnitOfWork_RollbackOnPartialFailure(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	definition := &store.ReleaseDefinition{
		ID: uuid.New().String(), Name: "pg-rollback-definition", CustomerID: "pg-rollback-customer",
		ClusterID: "pg-rollback-cluster", ReleaseName: "pg-rollback-release", Status: store.DefStatusActive,
	}
	require.NoError(t, st.Definitions().Create(ctx, definition, nil))
	// A received bundle makes setCurrentBundle fail after the operation row
	// and outbox row were written inside the transaction.
	bundle := &store.ReleaseBundle{
		ID: uuid.New().String(), Name: "pg-rollback-bundle", DigestAlg: "sha256",
		DigestValue: uuid.New().String(), Status: store.BundleReceived,
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
		Operation: operationRecord, Dispatch: dispatch,
	})
	require.ErrorIs(t, err, store.ErrBundleNotReady)

	_, err = st.Operations().Get(ctx, operationRecord.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.Outbox().GetByCommandID(ctx, dispatch.CommandID)
	require.ErrorIs(t, err, store.ErrNotFound)
}

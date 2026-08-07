package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func setupPreflightFixture(t *testing.T, st *sqlitestore.Store) string {
	t.Helper()
	ctx := context.Background()

	cust := &store.Customer{ID: uuid.New().String(), Name: "PreflightCust", Slug: "pf-cust"}
	require.NoError(t, st.Customers().Create(ctx, cust))

	def := &store.ReleaseDefinition{
		ID:          uuid.New().String(),
		Name:        "pf-def",
		CustomerID:  cust.ID,
		ClusterID:   "cls-pf",
		Namespace:   "default",
		ReleaseName: "pf-release",
		ChartName:   "nginx",
		Status:      store.DefStatusActive,
		CreatedBy:   "test",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))

	op := &store.Operation{
		ID:                  uuid.New().String(),
		OperationType:       store.OperationInstall,
		Status:              store.StatusPending,
		ReleaseDefinitionID: def.ID,
		CreatedAt:           time.Now().UTC(),
		UpdatedAt:           time.Now().UTC(),
		Actor:               store.ActorContext{UserID: "user-001"},
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	return op.ID
}

func TestPreflightCreateAndGetByKey(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	opID := setupPreflightFixture(t, st)

	key := store.PreflightCacheKey{
		OperationID:        opID,
		RoutingVersion:     "sha256:rv1",
		BundleDigest:       "sha256:bd1",
		TrustPolicyVersion: "v1",
		SBOMPolicyVersion:  "v1",
	}

	rec := &store.PreflightRecord{
		Key:        key,
		ResultJSON: []byte(`{"passed":true}`),
	}
	require.NoError(t, st.PreflightResults().Create(ctx, rec))
	assert.NotEmpty(t, rec.ID)

	got, err := st.PreflightResults().GetByKey(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, rec.ID, got.ID)
	assert.Equal(t, key.OperationID, got.Key.OperationID)
	assert.Equal(t, key.RoutingVersion, got.Key.RoutingVersion)
	assert.Equal(t, key.BundleDigest, got.Key.BundleDigest)
	assert.Equal(t, []byte(`{"passed":true}`), got.ResultJSON)
	assert.False(t, got.CreatedAt.IsZero())
}

func TestPreflightCreateIdempotent(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	opID := setupPreflightFixture(t, st)

	key := store.PreflightCacheKey{
		OperationID:        opID,
		RoutingVersion:     "sha256:rv2",
		BundleDigest:       "sha256:bd2",
		TrustPolicyVersion: "v1",
		SBOMPolicyVersion:  "v1",
	}

	rec1 := &store.PreflightRecord{
		Key:        key,
		ResultJSON: []byte(`{"passed":true}`),
	}
	require.NoError(t, st.PreflightResults().Create(ctx, rec1))

	// Second create with same key must succeed silently.
	rec2 := &store.PreflightRecord{
		Key:        key,
		ResultJSON: []byte(`{"passed":false}`),
	}
	require.NoError(t, st.PreflightResults().Create(ctx, rec2))

	// The stored result must be the first one.
	got, err := st.PreflightResults().GetByKey(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, rec1.ID, got.ID)
	assert.Equal(t, []byte(`{"passed":true}`), got.ResultJSON)
}

func TestPreflightGetByKeyNotFound(t *testing.T) {
	st := setupStore(t)

	_, err := st.PreflightResults().GetByKey(context.Background(), store.PreflightCacheKey{
		OperationID:        "nonexistent",
		RoutingVersion:     "v0",
		BundleDigest:       "bd0",
		TrustPolicyVersion: "v1",
		SBOMPolicyVersion:  "v1",
	})
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestPreflightDifferentVersionsProduceDistinctRows(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	opID := setupPreflightFixture(t, st)

	key1 := store.PreflightCacheKey{
		OperationID:        opID,
		RoutingVersion:     "sha256:rv3",
		BundleDigest:       "sha256:bd3",
		TrustPolicyVersion: "v1",
		SBOMPolicyVersion:  "v1",
	}
	key2 := store.PreflightCacheKey{
		OperationID:        opID,
		RoutingVersion:     "sha256:rv3",
		BundleDigest:       "sha256:bd3",
		TrustPolicyVersion: "v2", // different policy version
		SBOMPolicyVersion:  "v1",
	}

	rec1 := &store.PreflightRecord{Key: key1, ResultJSON: []byte(`"v1"`)}
	rec2 := &store.PreflightRecord{Key: key2, ResultJSON: []byte(`"v2"`)}
	require.NoError(t, st.PreflightResults().Create(ctx, rec1))
	require.NoError(t, st.PreflightResults().Create(ctx, rec2))

	got1, err := st.PreflightResults().GetByKey(ctx, key1)
	require.NoError(t, err)
	assert.Equal(t, []byte(`"v1"`), got1.ResultJSON)

	got2, err := st.PreflightResults().GetByKey(ctx, key2)
	require.NoError(t, err)
	assert.Equal(t, []byte(`"v2"`), got2.ResultJSON)
	assert.NotEqual(t, got1.ID, got2.ID)
}

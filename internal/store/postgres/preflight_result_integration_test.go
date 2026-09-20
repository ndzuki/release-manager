//go:build integration

package postgres_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// TASK-149 / REQ-056 AC-056-03: the SQLite counterpart is
// TestSaveAndGetPreflightResult. Both engines must round-trip the preflight
// stage results, because only the error code used to reach last_error and the
// detail page could not show which stage failed.
func TestSaveAndGetPreflightResult(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	now := time.Now().UTC()
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: "cust-1", Name: "cust-1", Slug: "cust-1", Status: store.CustomerActive}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: "cls-1", CustomerID: "cust-1", Name: "cls-1", Status: store.ClusterActive}))
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: "def-preflight", Name: "def-preflight", CustomerID: "cust-1", ClusterID: "cls-1",
		Namespace: "default", ReleaseName: "app", ChartName: "app", Status: store.DefStatusActive, CreatedAt: now,
	}, nil))
	operation := &store.Operation{
		ID: "op-preflight", OperationType: store.OperationInstall, Status: store.StatusPreflight,
		ReleaseDefinitionID: "def-preflight", IdempotencyKey: "k-preflight", IdempotencyScope: "org:def",
		RequestHash: "h-preflight", CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, st.Operations().Create(ctx, operation))

	result, err := st.Operations().GetPreflightResult(ctx, operation.ID)
	require.NoError(t, err)
	assert.Nil(t, result, "an operation that has not finished preflight has no result")

	payload := json.RawMessage(`{"overall":"failed","failed_stage":"render","stages":[{"stage":"render","status":"failed","detail":"render_failed"}]}`)
	require.NoError(t, st.Operations().SavePreflightResult(ctx, operation.ID, payload))

	stored, err := st.Operations().GetPreflightResult(ctx, operation.ID)
	require.NoError(t, err)
	assert.JSONEq(t, string(payload), string(stored))

	assert.ErrorIs(t, st.Operations().SavePreflightResult(ctx, "missing", payload), store.ErrNotFound)
}

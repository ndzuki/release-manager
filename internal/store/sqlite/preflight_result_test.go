package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// TASK-149 / REQ-056 AC-056-03: the preflight stage results must survive on the
// operation, so a failed preflight can show which stage failed and what its
// checks said. Only the error code used to reach last_error.
func TestSaveAndGetPreflightResult(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()
	seedEmergencyDefinition(t, st, "def-preflight-result")
	now := time.Now().UTC()
	created := &store.Operation{
		ID: "op-preflight-result", OperationType: store.OperationInstall, Status: store.StatusPreflight,
		ReleaseDefinitionID: "def-preflight-result", IdempotencyKey: "k-preflight", IdempotencyScope: "org:def",
		RequestHash: "h-preflight", CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, st.Operations().Create(ctx, created))

	// An operation that has not finished preflight has no result.
	result, err := st.Operations().GetPreflightResult(ctx, created.ID)
	require.NoError(t, err)
	assert.Nil(t, result)

	payload := json.RawMessage(`{"overall":"failed","failed_stage":"render","stages":[{"stage":"render","status":"failed","detail":"render_failed"}]}`)
	require.NoError(t, st.Operations().SavePreflightResult(ctx, created.ID, payload))

	stored, err := st.Operations().GetPreflightResult(ctx, created.ID)
	require.NoError(t, err)
	assert.JSONEq(t, string(payload), string(stored), "the stage results must round-trip")

	// An unknown operation is a not-found, not a silent success.
	assert.ErrorIs(t, st.Operations().SavePreflightResult(ctx, "missing", payload), store.ErrNotFound)
}

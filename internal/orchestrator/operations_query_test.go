package orchestrator

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// TestListOperationsPaginatesAndFilters replaces the previous handler that
// always answered CodeUnimplemented (TASK-095): REQ-056 needs the operation
// history to pick a successful ROLLBACK target.
func TestListOperationsPaginatesAndFilters(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	base := time.Date(2026, time.September, 16, 9, 0, 0, 0, time.UTC)
	seed := []*store.Operation{
		{
			ID: "op-install", OperationType: store.OperationInstall, Status: store.StatusSucceeded,
			ReleaseDefinitionID: "def-001", IdempotencyKey: uuid.NewString(), RequestHash: "hash-install",
			ExpectedRevision: 0, CreatedAt: base, UpdatedAt: base,
		},
		{
			ID: "op-failed", OperationType: store.OperationUpgrade, Status: store.StatusFailed,
			ReleaseDefinitionID: "def-001", IdempotencyKey: uuid.NewString(), RequestHash: "hash-failed",
			ExpectedRevision: 1, CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute),
		},
		{
			ID: "op-rollback", OperationType: store.OperationRollback, Status: store.StatusSucceeded,
			ReleaseDefinitionID: "def-001", IdempotencyKey: uuid.NewString(), RequestHash: "hash-rollback",
			ExpectedRevision: 2, TargetRevision: 1, CreatedAt: base.Add(2 * time.Minute), UpdatedAt: base.Add(2 * time.Minute),
		},
	}
	for _, operation := range seed {
		require.NoError(t, st.Operations().Create(t.Context(), operation))
	}

	first, err := svc.ListOperations(deployerCtx(), connect.NewRequest(&orchestratorv1.ListOperationsRequest{
		ReleaseDefinitionId: "def-001", Limit: 2,
	}))
	require.NoError(t, err)
	require.Len(t, first.Msg.GetOperations(), 2)
	// Newest first.
	assert.Equal(t, "op-rollback", first.Msg.GetOperations()[0].GetOperationId())
	assert.Equal(t, string(store.OperationRollback), first.Msg.GetOperations()[0].GetOperationType())
	assert.Equal(t, string(store.StatusSucceeded), first.Msg.GetOperations()[0].GetState())
	// revision: ROLLBACK reports its target revision.
	assert.Equal(t, int32(1), first.Msg.GetOperations()[0].GetRevision())
	assert.Equal(t, base.Add(2*time.Minute), first.Msg.GetOperations()[0].GetCreatedAt().AsTime())
	assert.Equal(t, "op-failed", first.Msg.GetOperations()[1].GetOperationId())
	require.NotEmpty(t, first.Msg.GetNextCursor())

	second, err := svc.ListOperations(deployerCtx(), connect.NewRequest(&orchestratorv1.ListOperationsRequest{
		ReleaseDefinitionId: "def-001", Limit: 2, Cursor: first.Msg.GetNextCursor(),
	}))
	require.NoError(t, err)
	require.Len(t, second.Msg.GetOperations(), 1)
	assert.Equal(t, "op-install", second.Msg.GetOperations()[0].GetOperationId())
	// revision: an INSTALL reports the revision it was computed against.
	assert.Equal(t, int32(0), second.Msg.GetOperations()[0].GetRevision())
	assert.Empty(t, second.Msg.GetNextCursor())

	succeeded, err := svc.ListOperations(deployerCtx(), connect.NewRequest(&orchestratorv1.ListOperationsRequest{
		ReleaseDefinitionId: "def-001", StatusFilter: string(store.StatusSucceeded),
	}))
	require.NoError(t, err)
	require.Len(t, succeeded.Msg.GetOperations(), 2)
	assert.Equal(t, "op-rollback", succeeded.Msg.GetOperations()[0].GetOperationId())
	assert.Equal(t, "op-install", succeeded.Msg.GetOperations()[1].GetOperationId())
}

func TestListOperationsValidatesRequest(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	tests := []struct {
		name     string
		request  *orchestratorv1.ListOperationsRequest
		wantCode connect.Code
		wantReas string
	}{
		{
			name:     "missing release definition",
			request:  &orchestratorv1.ListOperationsRequest{},
			wantCode: connect.CodeInvalidArgument, wantReas: "release_definition_id_required",
		},
		{
			name:     "unknown release definition",
			request:  &orchestratorv1.ListOperationsRequest{ReleaseDefinitionId: "def-missing"},
			wantCode: connect.CodeNotFound, wantReas: "release_definition_not_found",
		},
		{
			name:     "unknown status filter",
			request:  &orchestratorv1.ListOperationsRequest{ReleaseDefinitionId: "def-001", StatusFilter: "exploded"},
			wantCode: connect.CodeInvalidArgument, wantReas: "invalid_status_filter",
		},
		{
			name:     "negative limit",
			request:  &orchestratorv1.ListOperationsRequest{ReleaseDefinitionId: "def-001", Limit: -1},
			wantCode: connect.CodeInvalidArgument, wantReas: "invalid_page_size",
		},
		{
			name:     "malformed cursor",
			request:  &orchestratorv1.ListOperationsRequest{ReleaseDefinitionId: "def-001", Cursor: "not-a-cursor"},
			wantCode: connect.CodeInvalidArgument, wantReas: "invalid_cursor",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.ListOperations(deployerCtx(), connect.NewRequest(tt.request))
			require.Error(t, err)
			assert.Equal(t, tt.wantCode, connect.CodeOf(err))
			assert.Equal(t, tt.wantReas, connectErrorReason(err))
		})
	}
}

// TestListOperationsRequiresActiveBinding keeps the tenancy gate: an
// organization without a customer binding cannot read another tenant's
// operation history.
func TestListOperationsRequiresActiveBinding(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	require.NoError(t, st.Customers().Create(t.Context(), &store.Customer{
		ID: "cust-unbound", Name: "Unbound", Slug: "unbound",
	}))
	require.NoError(t, st.Definitions().Create(t.Context(), &store.ReleaseDefinition{
		ID: "def-unbound", Name: "unbound", CustomerID: "cust-unbound", ClusterID: "cls-001",
		Namespace: "default", ReleaseName: "unbound", Status: store.DefStatusActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, nil))

	_, err := svc.ListOperations(deployerCtx(), connect.NewRequest(&orchestratorv1.ListOperationsRequest{
		ReleaseDefinitionId: "def-unbound",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Equal(t, "binding_revoked", connectErrorReason(err))
}

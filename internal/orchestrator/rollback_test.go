package orchestrator

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

func TestRollbackRelease_Success(t *testing.T) {
	// AC-022-03: successful rollback creates operation and returns from/to revision
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	resp, err := svc.RollbackRelease(context.Background(), connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-001",
		TargetRevision:          1,
		ExpectedCurrentRevision: 3,
		Reason:                  "rolling back due to config error",
		IdempotencyKey:          "idem-rb-001",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Msg.OperationId)
	assert.Equal(t, int32(3), resp.Msg.FromRevision)
	assert.Equal(t, int32(4), resp.Msg.ToRevision) // rollback creates rev 4
	assert.Equal(t, "preflight", resp.Msg.State)

	// Verify operation persisted correctly
	op, err := st.Operations().Get(context.Background(), resp.Msg.OperationId)
	require.NoError(t, err)
	assert.Equal(t, store.OperationRollback, op.OperationType)
	assert.Equal(t, 3, op.ExpectedRevision)
	assert.Equal(t, 1, op.TargetRevision)
	assert.Equal(t, store.StatusPreflight, op.Status)
}

func TestRollbackRelease_TargetRevisionInvalid(t *testing.T) {
	// AC-022-01: invalid target/expected revision → rejected with no write
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	tests := []struct {
		name      string
		targetRev int32
		expected  int32
		wantCode  connect.Code
		wantMsg   string
	}{
		{
			name:      "target_revision zero",
			targetRev: 0,
			expected:  3,
			wantCode:  connect.CodeInvalidArgument,
			wantMsg:   "target_revision must be >= 1",
		},
		{
			name:      "expected_current_revision zero",
			targetRev: 1,
			expected:  0,
			wantCode:  connect.CodeInvalidArgument,
			wantMsg:   "expected_current_revision must be >= 1",
		},
		{
			name:      "target >= expected (same)",
			targetRev: 3,
			expected:  3,
			wantCode:  connect.CodeInvalidArgument,
			wantMsg:   "target_revision 3 must be < expected_current_revision 3",
		},
		{
			name:      "target > expected",
			targetRev: 5,
			expected:  3,
			wantCode:  connect.CodeInvalidArgument,
			wantMsg:   "target_revision 5 must be < expected_current_revision 3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.RollbackRelease(context.Background(), connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
				ReleaseDefinitionId:     "def-001",
				TargetRevision:          tt.targetRev,
				ExpectedCurrentRevision: tt.expected,
				Reason:                  "test",
				IdempotencyKey:          "idem-" + tt.name,
				Actor: &commonv1.ActorContext{
					UserId:       "user-001",
					Organization: "org-001",
				},
			}))
			require.Error(t, err)
			assert.Equal(t, tt.wantCode, connect.CodeOf(err))
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}

	// AC-022-01: no operation created for invalid requests
	ops, err := st.Operations().List(context.Background(), "def-001")
	require.NoError(t, err)
	assert.Empty(t, ops, "no operations should be created for invalid rollback requests")
}

func TestRollbackRelease_DefinitionNotFound(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	_, err := svc.RollbackRelease(context.Background(), connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "nonexistent",
		TargetRevision:          1,
		ExpectedCurrentRevision: 3,
		Reason:                  "test",
		IdempotencyKey:          "idem-rb-nf",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "release_definition not found")
}

func TestRollbackRelease_ReleaseBusy(t *testing.T) {
	// AC-022-03: concurrent operation → release_busy
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	// First rollback succeeds
	_, err := svc.RollbackRelease(context.Background(), connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-001",
		TargetRevision:          1,
		ExpectedCurrentRevision: 3,
		Reason:                  "first rollback",
		IdempotencyKey:          "idem-rb-busy-1",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}))
	require.NoError(t, err)

	// Second rollback on same definition → release_busy
	_, err = svc.RollbackRelease(context.Background(), connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-001",
		TargetRevision:          2,
		ExpectedCurrentRevision: 3,
		Reason:                  "second rollback",
		IdempotencyKey:          "idem-rb-busy-2",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "release_busy")
}

func TestRollbackRelease_MissingReason(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	_, err := svc.RollbackRelease(context.Background(), connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-001",
		TargetRevision:          1,
		ExpectedCurrentRevision: 3,
		Reason:                  "",
		IdempotencyKey:          "idem-rb-no-reason",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "reason is required")
}

func TestRollbackRelease_Idempotency(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	req := &orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-001",
		TargetRevision:          1,
		ExpectedCurrentRevision: 3,
		Reason:                  "idempotent rollback",
		IdempotencyKey:          "idem-rb-idem",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}

	// First call creates operation
	resp1, err := svc.RollbackRelease(context.Background(), connect.NewRequest(req))
	require.NoError(t, err)

	// Second call with same idempotency key returns existing operation
	resp2, err := svc.RollbackRelease(context.Background(), connect.NewRequest(req))
	require.NoError(t, err)

	assert.Equal(t, resp1.Msg.OperationId, resp2.Msg.OperationId)
	assert.Equal(t, resp1.Msg.State, resp2.Msg.State)

	// Verify only one operation exists
	ops, err := st.Operations().List(context.Background(), "def-001")
	require.NoError(t, err)
	assert.Len(t, ops, 1)
}

func TestRollbackRelease_DefinitionNotActive(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()

	// Create a draft (non-active) definition with its customer.
	cust := &store.Customer{
		ID:   "cust-001",
		Name: "Test Customer",
		Slug: "test-customer",
	}
	require.NoError(t, st.Customers().Create(context.Background(), cust))

	def := &store.ReleaseDefinition{
		ID:          "def-draft",
		Name:        "draft-release",
		CustomerID:  "cust-001",
		ClusterID:   "cls-001",
		Namespace:   "default",
		ReleaseName: "draft-release",
		ChartName:   "nginx",
		Status:      store.DefStatusDraft,
		CreatedBy:   "test",
	}
	require.NoError(t, st.Definitions().Create(context.Background(), def))

	_, err := svc.RollbackRelease(context.Background(), connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-draft",
		TargetRevision:          1,
		ExpectedCurrentRevision: 3,
		Reason:                  "test",
		IdempotencyKey:          "idem-rb-draft",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "draft")
}

func TestRollbackRelease_CustomerDisabled(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	// Disable the customer
	cust, err := st.Customers().Get(context.Background(), "cust-001")
	require.NoError(t, err)
	cust.Status = store.CustomerDisabled
	err = st.Customers().Update(context.Background(), cust)
	require.NoError(t, err)

	_, err = svc.RollbackRelease(context.Background(), connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-001",
		TargetRevision:          1,
		ExpectedCurrentRevision: 3,
		Reason:                  "test",
		IdempotencyKey:          "idem-rb-cust-disabled",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "disabled")
}

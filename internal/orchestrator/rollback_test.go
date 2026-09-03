package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// seedRollbackInventory creates an active inventory entry for def-001 at
// revision 3, satisfying the ROLLBACK inventory precondition (REQ-067 rule 13).
func seedRollbackInventory(t *testing.T, st store.Store) {
	t.Helper()
	require.NoError(t, st.Inventories().Upsert(t.Context(), &store.ReleaseInventory{
		ReleaseDefinitionID: "def-001",
		CustomerID:          "cust-001",
		ClusterID:           "cls-001",
		Namespace:           "default",
		ReleaseName:         "my-release",
		InventoryStatus:     store.InventoryActive,
		Revision:            3,
	}))
}

func TestRollbackRelease_Success(t *testing.T) {
	// AC-022-03: successful rollback creates operation and returns from/to revision
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedRollbackInventory(t, st)
	// Preflight runs asynchronously (REQ-019); an active operator keeps the
	// operation in preflight while the artifact stage awaits its result.
	// Without one the coordinator fail-closes the operation to a terminal
	// state, making the transient preflight assertion racy.
	require.NoError(t, st.Operators().Create(context.Background(), &store.Operator{
		ID: "operator-rollback-success", Name: "operator-rollback-success", CustomerID: "cust-001", ClusterID: "cls-001",
		CertSerial: "serial-rollback-success", Status: store.OperatorActive,
	}))

	resp, err := svc.RollbackRelease(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-001",
		TargetRevision:          1,
		ExpectedCurrentRevision: 3,
		Reason:                  "rolling back due to config error",
	}), "idem-rb-001"))
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
			_, err := svc.RollbackRelease(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
				ReleaseDefinitionId:     "def-001",
				TargetRevision:          tt.targetRev,
				ExpectedCurrentRevision: tt.expected,
				Reason:                  "test",
			}), "idem-"+tt.name))
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

	_, err := svc.RollbackRelease(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "nonexistent",
		TargetRevision:          1,
		ExpectedCurrentRevision: 3,
		Reason:                  "test",
	}), "idem-rb-nf"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "definition_not_found")
}

func TestRollbackRelease_ReleaseBusy(t *testing.T) {
	// AC-022-03: concurrent operation → release_busy
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedRollbackInventory(t, st)

	// First rollback succeeds
	_, err := svc.RollbackRelease(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-001",
		TargetRevision:          1,
		ExpectedCurrentRevision: 3,
		Reason:                  "first rollback",
	}), "idem-rb-busy-1"))
	require.NoError(t, err)

	// Second rollback on same definition → release_busy
	_, err = svc.RollbackRelease(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-001",
		TargetRevision:          2,
		ExpectedCurrentRevision: 3,
		Reason:                  "second rollback",
	}), "idem-rb-busy-2"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "release_busy")
}

func TestRollbackRelease_ConcurrentOnlyOneAccepted(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedRollbackInventory(t, st)
	// An active operator keeps the accepted operation in preflight (polling for
	// a stage result); without one the coordinator fail-closes it to terminal,
	// which legitimately allows a subsequent accept (ADR-008).
	require.NoError(t, st.Operators().Create(context.Background(), &store.Operator{
		ID: "operator-rollback", Name: "operator-rollback", CustomerID: "cust-001", ClusterID: "cls-001",
		CertSerial: "serial-rollback", Status: store.OperatorActive,
	}))

	const requests = 8
	errorsCh := make(chan error, requests)
	for i := range requests {
		go func(i int) {
			_, err := svc.RollbackRelease(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
				ReleaseDefinitionId:     "def-001",
				TargetRevision:          1,
				ExpectedCurrentRevision: 3,
				Reason:                  "concurrent rollback",
			}), fmt.Sprintf("idem-rb-concurrent-%d", i)))
			errorsCh <- err
		}(i)
	}

	accepted := 0
	busy := 0
	for range requests {
		err := <-errorsCh
		switch {
		case err == nil:
			accepted++
		case connect.CodeOf(err) == connect.CodeFailedPrecondition && strings.Contains(err.Error(), "release_busy"):
			busy++
		default:
			t.Fatalf("unexpected concurrent rollback error: %v", err)
		}
	}
	assert.Equal(t, 1, accepted)
	assert.Equal(t, requests-1, busy)

	ops, err := st.Operations().List(context.Background(), "def-001")
	require.NoError(t, err)
	nonTerminal := 0
	for _, op := range ops {
		if !op.Status.IsTerminal() {
			nonTerminal++
		}
	}
	assert.Equal(t, 1, nonTerminal)
}

func TestRollbackRelease_MissingReason(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	_, err := svc.RollbackRelease(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-001",
		TargetRevision:          1,
		ExpectedCurrentRevision: 3,
		Reason:                  "",
	}), "idem-rb-no-reason"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "reason is required")
}

func TestRollbackRelease_Idempotency(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedRollbackInventory(t, st)
	// The idempotent replay returns the operation's live state; without an
	// operator the async preflight coordinator fail-closes the operation
	// between the two calls, making the state comparison racy (same shape as
	// TestRollbackRelease_ConcurrentOnlyOneAccepted).
	require.NoError(t, st.Operators().Create(context.Background(), &store.Operator{
		ID: "operator-rollback-idem", Name: "operator-rollback-idem", CustomerID: "cust-001", ClusterID: "cls-001",
		CertSerial: "serial-rollback-idem", Status: store.OperatorActive,
	}))

	req := &orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-001",
		TargetRevision:          1,
		ExpectedCurrentRevision: 3,
		Reason:                  "idempotent rollback",
	}

	// First call creates operation
	resp1, err := svc.RollbackRelease(adminCtx(), withIdempotencyKey(connect.NewRequest(req), "idem-rb-idem"))
	require.NoError(t, err)

	// Second call with same idempotency key returns existing operation
	resp2, err := svc.RollbackRelease(adminCtx(), withIdempotencyKey(connect.NewRequest(req), "idem-rb-idem"))
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
	seedDefinition(t, st)

	// Create a draft (non-active) definition under the seeded customer/org.
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
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	require.NoError(t, st.Definitions().Create(context.Background(), def, nil))

	_, err := svc.RollbackRelease(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-draft",
		TargetRevision:          1,
		ExpectedCurrentRevision: 3,
		Reason:                  "test",
	}), "idem-rb-draft"))
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
	err = st.Customers().Update(context.Background(), cust, cust.Version)
	require.NoError(t, err)

	_, err = svc.RollbackRelease(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-001",
		TargetRevision:          1,
		ExpectedCurrentRevision: 3,
		Reason:                  "test",
	}), "idem-rb-cust-disabled"))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// AC-067-16: ROLLBACK must not carry values; the deprecated request fields
// remain the server-side rejection detection point.
func TestRollbackRelease_ValuesNotAllowed(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	_, err := svc.RollbackRelease(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     "def-001",
		TargetRevision:          1,
		ExpectedCurrentRevision: 3,
		Reason:                  "test",
		ValuesRevisionId:        "vr-001",
	}), "idem-rb-values"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "rollback_values_not_allowed")

	operations, listErr := st.Operations().List(context.Background(), "def-001")
	require.NoError(t, listErr)
	assert.Empty(t, operations, "rollback with values must not write an operation")
}

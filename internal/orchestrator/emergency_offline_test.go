package orchestrator

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
)

// TestExecuteEmergencyChangeRequiresFreshHeartbeat covers REQ-032 AC-032-20 with
// the TASK-098 tightening: a persisted session row that still says online is not
// enough — after an orchestrator restart the in-process stream is gone while the
// row survives, so a stale heartbeat must answer operator_offline instead of
// failing later with an internal-looking delivery error.
func TestExecuteEmergencyChangeRequiresFreshHeartbeat(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedEmergencyImageIdentity(t, st)
	svc, _, dispatcher := emergencyTestServiceFromExisting(t, svc, st)

	// Any heartbeat age is older than this: the row is online but not fresh.
	svc.lifecyclePolicy.SessionOfflineAfter = time.Nanosecond

	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("stale-heartbeat"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.Equal(t, "operator_offline", connectErrorReason(err))
	assert.Empty(t, dispatcher.commands, "a stale session must not be dispatched to")

	// Zero side effects: the rejected attempt leaves no non-terminal operation.
	operations, listErr := st.Operations().ListNonTerminal(t.Context())
	require.NoError(t, listErr)
	assert.Empty(t, operations, "a rejected emergency must not leave a non-terminal operation")
}

// TestExecuteEmergencyChangeMapsDispatchFailureToOperatorOffline covers the
// in-process dispatcher leg: the stream map is empty after a restart even
// though the session row may still be fresh, and the caller must still see the
// one documented retryable reason.
func TestExecuteEmergencyChangeMapsDispatchFailureToOperatorOffline(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedEmergencyImageIdentity(t, st)
	svc, _, dispatcher := emergencyTestServiceFromExisting(t, svc, st)
	dispatcher.err = errors.New("operator stream is offline")

	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("dispatch-offline"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.Equal(t, "operator_offline", connectErrorReason(err))

	operations, listErr := st.Operations().ListNonTerminal(t.Context())
	require.NoError(t, listErr)
	assert.Empty(t, operations, "a failed dispatch must not leave a non-terminal operation")
}

// TestCreateOperationPersistsADeadline covers REQ-023/TASK-098: standard
// operations are bounded, so a stuck INSTALL/UPGRADE is swept to timeout by the
// recovery loop instead of staying non-terminal forever.
func TestCreateOperationPersistsADeadline(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	svc.lifecyclePolicy.OperationDeadline = 17 * time.Minute
	response, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
	}), "deadline-bound"))
	require.NoError(t, err)

	operation, err := st.Operations().Get(t.Context(), response.Msg.GetOperationId())
	require.NoError(t, err)
	require.NotNil(t, operation.Deadline, "a standard operation must carry a deadline")
	elapsed := operation.Deadline.Sub(operation.CreatedAt)
	assert.InDelta(t, (17 * time.Minute).Seconds(), elapsed.Seconds(), 5,
		"the deadline must follow the configured bound")
}

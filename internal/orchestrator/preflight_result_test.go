package orchestrator

import (
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
)

// TASK-149 / AC-056-03: the persisted preflight stage results are exposed on
// GetOperation, so the detail page can highlight the failed stage and expand its
// checks instead of only showing the flat last_error.
func TestGetOperationExposesThePreflightResult(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	created, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("preflight-result"))
	require.NoError(t, err)
	operationID := created.Msg.GetOperationId()

	// Before preflight concludes there is nothing to show.
	before, err := svc.GetOperation(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.GetOperationRequest{OperationId: operationID}))
	require.NoError(t, err)
	assert.Nil(t, before.Msg.GetPreflightResult(), "an operation that has not finished preflight has no result")

	payload := json.RawMessage(`{"operation_id":"` + operationID + `","overall":"failed","failed_stage":"render","error_code":"render_failed","stages":[{"stage":"artifact","status":"passed"},{"stage":"render","status":"failed","detail":"render_failed"}]}`)
	require.NoError(t, st.Operations().SavePreflightResult(t.Context(), operationID, payload))

	after, err := svc.GetOperation(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.GetOperationRequest{OperationId: operationID}))
	require.NoError(t, err)
	preflight := after.Msg.GetPreflightResult()
	require.NotNil(t, preflight, "the persisted stage results must be exposed")
	assert.Equal(t, "failed", preflight.GetOverall())
	assert.Equal(t, "render", preflight.GetFailedStage())
	assert.Equal(t, "render_failed", preflight.GetErrorCode())
	require.Len(t, preflight.GetStages(), 2)
	assert.Equal(t, "artifact", preflight.GetStages()[0].GetStage())
	assert.Equal(t, "passed", preflight.GetStages()[0].GetStatus())
	assert.Equal(t, "render", preflight.GetStages()[1].GetStage())
	assert.Equal(t, "render_failed", preflight.GetStages()[1].GetDetail())
}

// A stored payload the read model cannot decode must not fail the detail page:
// the operation and its flat last_error are still served.
func TestGetOperationSurvivesAnUndecodablePreflightResult(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	created, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("preflight-bad-json"))
	require.NoError(t, err)
	operationID := created.Msg.GetOperationId()

	require.NoError(t, st.Operations().SavePreflightResult(t.Context(), operationID, json.RawMessage(`{"overall":`)))

	resp, err := svc.GetOperation(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.GetOperationRequest{OperationId: operationID}))
	require.NoError(t, err, "a bad diagnostic payload must not fail the detail page")
	assert.Nil(t, resp.Msg.GetPreflightResult())
	assert.NotNil(t, resp.Msg.GetOperation())
}

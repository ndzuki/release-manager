package orchestrator

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// REQ-021:217 promises the GetOperation response an optional UpgradeResult read from
// operation_execution_results. The column is written by the operator's terminal transition and
// nothing read it, so the typed outcome of an upgrade never left the database.
func TestGetOperationExposesTheStoredUpgradeResult(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	created, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("upgrade-result"))
	require.NoError(t, err)
	operationID := created.Msg.GetOperationId()

	before, err := svc.GetOperation(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.GetOperationRequest{OperationId: operationID}))
	require.NoError(t, err)
	assert.Nil(t, before.Msg.GetUpgradeResult(), "an operation with no terminal upgrade result has none to show")

	// The operator writes canonical protojson into result_payload; store the same shape.
	payload, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(&operatorv1.UpgradeResult{
		From:              &operatorv1.ReleaseSnapshot{HelmRevision: 3, BundleDigest: "sha256:from", Status: "deployed"},
		Attempted:         &operatorv1.ReleaseSnapshot{HelmRevision: 4, BundleDigest: "sha256:attempted", Status: "failed"},
		Active:            &operatorv1.ReleaseSnapshot{HelmRevision: 3, BundleDigest: "sha256:from", Status: "deployed"},
		RollbackSucceeded: true,
		ResourceSummary:   &operatorv1.ResourceSummary{ManifestDigest: "sha256:manifest", ResourceCount: 7},
	})
	require.NoError(t, err)
	// Write it through the production terminal transition, which is the only writer of this row
	// (BOTH engines implement it: sqlite operations.go, postgres upgrade.go). Seeding by hand
	// would leave the write side of the contract untested here.
	op, err := st.Operations().Get(t.Context(), operationID)
	require.NoError(t, err)
	require.NoError(t, st.UpgradeResults().FinalizeUpgrade(t.Context(), &store.UpgradeTerminalInput{
		OperationID:          operationID,
		ExpectedStateVersion: op.StateVersion,
		Status:               store.StatusSucceeded,
		ResultPayload:        payload,
		ReleaseDefinitionID:  op.ReleaseDefinitionID,
	}))

	after, err := svc.GetOperation(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.GetOperationRequest{OperationId: operationID}))
	require.NoError(t, err)
	upgrade := after.Msg.GetUpgradeResult()
	require.NotNil(t, upgrade, "the persisted terminal result must be exposed")
	assert.Equal(t, uint64(3), upgrade.GetFrom().GetHelmRevision())
	assert.Equal(t, "sha256:from", upgrade.GetFrom().GetBundleDigest())
	assert.Equal(t, uint64(4), upgrade.GetAttempted().GetHelmRevision())
	assert.Equal(t, "failed", upgrade.GetAttempted().GetStatus())
	assert.Equal(t, uint64(3), upgrade.GetActive().GetHelmRevision())
	assert.True(t, upgrade.GetRollbackSucceeded())
	require.NotNil(t, upgrade.GetResourceSummary())
	assert.Equal(t, int32(7), upgrade.GetResourceSummary().GetResourceCount())
	assert.Equal(t, "sha256:manifest", upgrade.GetResourceSummary().GetManifestDigest())
}

// A payload the read model cannot decode must not fail the detail page: the operation is still
// served, exactly like an undecodable preflight result.
func TestGetOperationSurvivesAnUndecodableUpgradeResult(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	created, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("upgrade-result-bad"))
	require.NoError(t, err)
	operationID := created.Msg.GetOperationId()

	op, err := st.Operations().Get(t.Context(), operationID)
	require.NoError(t, err)
	require.NoError(t, st.UpgradeResults().FinalizeUpgrade(t.Context(), &store.UpgradeTerminalInput{
		OperationID:          operationID,
		ExpectedStateVersion: op.StateVersion,
		Status:               store.StatusSucceeded,
		ResultPayload:        []byte(`{"from":`),
		ReleaseDefinitionID:  op.ReleaseDefinitionID,
	}))

	resp, err := svc.GetOperation(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.GetOperationRequest{OperationId: operationID}))
	require.NoError(t, err, "a bad result payload must not fail the detail page")
	assert.Nil(t, resp.Msg.GetUpgradeResult())
	assert.NotNil(t, resp.Msg.GetOperation())
}

// The stored payload is a durable protojson document written by whichever operator version ran
// the upgrade. A newer operator may add fields, so the reader must tolerate unknowns instead of
// failing the detail page -- behaviour that had no case in the package until review pointed it
// out.
func TestGetOperationToleratesUnknownUpgradeResultFields(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	created, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("upgrade-result-future"))
	require.NoError(t, err)
	operationID := created.Msg.GetOperationId()

	op, err := st.Operations().Get(t.Context(), operationID)
	require.NoError(t, err)
	require.NoError(t, st.UpgradeResults().FinalizeUpgrade(t.Context(), &store.UpgradeTerminalInput{
		OperationID:          operationID,
		ExpectedStateVersion: op.StateVersion,
		Status:               store.StatusSucceeded,
		ResultPayload:        []byte(`{"from":{"helm_revision":"5","status":"deployed"},"future_field":{"anything":true}}`),
		ReleaseDefinitionID:  op.ReleaseDefinitionID,
	}))

	resp, err := svc.GetOperation(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.GetOperationRequest{OperationId: operationID}))
	require.NoError(t, err, "an unknown field from a newer operator must not fail the read")
	require.NotNil(t, resp.Msg.GetUpgradeResult())
	assert.Equal(t, uint64(5), resp.Msg.GetUpgradeResult().GetFrom().GetHelmRevision())
}

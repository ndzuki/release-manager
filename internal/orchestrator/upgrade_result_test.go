package orchestrator

import (
	"database/sql"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
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
	// The upgrade terminal writer only exists for PostgreSQL; the test store is the concrete
	// SQLite engine, which exposes its handle for exactly this kind of seeding.
	sqliteStore, ok := st.(interface{ DB() *sql.DB })
	require.True(t, ok, "the test store must expose its database handle")
	_, err = sqliteStore.DB().ExecContext(t.Context(), `
		INSERT INTO operation_execution_results (operation_id, result_type, result_payload, created_at)
		VALUES (?, 'upgrade', ?, ?)
	`, operationID, string(payload), "2026-09-30T12:00:00Z")
	require.NoError(t, err)

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

	sqliteStore, ok := st.(interface{ DB() *sql.DB })
	require.True(t, ok, "the test store must expose its database handle")
	_, err = sqliteStore.DB().ExecContext(t.Context(), `
		INSERT INTO operation_execution_results (operation_id, result_type, result_payload, created_at)
		VALUES (?, 'upgrade', ?, ?)
	`, operationID, `{"from":`, "2026-09-30T12:00:00Z")
	require.NoError(t, err)

	resp, err := svc.GetOperation(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.GetOperationRequest{OperationId: operationID}))
	require.NoError(t, err, "a bad result payload must not fail the detail page")
	assert.Nil(t, resp.Msg.GetUpgradeResult())
	assert.NotNil(t, resp.Msg.GetOperation())
}

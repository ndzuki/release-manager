package orchestrator

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// AC-032-06: the canonical request requires a container for the image branch.
func TestExecuteEmergencyChangeContainerRequired(t *testing.T) {
	svc, _, _ := emergencyTestService(t)
	req := emergencyImageRequest("container-required")
	req.Msg.Container = ""
	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, "container_required", connectErrorReason(err))
}

// AC-032-23: REVERT_ON_NEXT_RECONCILE must not create a ConvergenceTask --
// only REQUIRE_PROMOTION does (ADR-011).
func TestExecuteEmergencyChangeRevertCreatesNoConvergenceTask(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	req := emergencyImageRequest("revert-no-task")
	req.Msg.ConvergenceStrategy = orchestratorv1.ConvergenceStrategy_REVERT_ON_NEXT_RECONCILE
	resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
	require.NoError(t, err)

	intent, err := st.EmergencyIntents().GetByOperationID(t.Context(), resp.Msg.GetOperationId())
	require.NoError(t, err)
	assert.Equal(t, store.EmergencyRevertOnNextReconcile, intent.Convergence)
	assert.Empty(t, resp.Msg.GetResult().GetConvergenceTasks())
	_, taskErr := st.ConvergenceTasks().GetByOperationID(t.Context(), resp.Msg.GetOperationId())
	assert.ErrorIs(t, taskErr, store.ErrNotFound, "AC-032-23: REVERT must not create a ConvergenceTask")
}

// AC-032-04: a disabled customer is rejected with its own reason code.
func TestExecuteEmergencyChangeCustomerDisabled(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	customer, err := st.Customers().Get(t.Context(), "cust-001")
	require.NoError(t, err)
	version := customer.Version
	customer.Status = store.CustomerDisabled
	require.NoError(t, st.Customers().Update(t.Context(), customer, version))

	_, err = svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("customer-disabled"))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Equal(t, "customer_disabled", connectErrorReason(err))
}

// AC-032-08: a disabled release definition is rejected with its own reason code.
func TestExecuteEmergencyChangeDefinitionDisabled(t *testing.T) {
	svc, _, _ := emergencyTestService(t)
	_, err := svc.DisableReleaseDefinition(emergencyAdminContext(), connect.NewRequest(
		&orchestratorv1.DisableReleaseDefinitionRequest{DefinitionId: "def-001"},
	))
	require.NoError(t, err)

	_, err = svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("definition-disabled"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Equal(t, "release_definition_disabled", connectErrorReason(err))
}

// AC-032-17: an unvalidated candidate must not be offered as a candidate.
func TestExecuteEmergencyChangeUnvalidatedArtifactRejected(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	require.NoError(t, st.CandidateArtifacts().Create(t.Context(), &store.CandidateArtifact{
		ID: "artifact-unvalidated", ArtifactType: store.ArtifactImage,
		Ref: "registry.example/team/api:2.0.0", Digest: "sha256:def", SourceID: "source-1",
	}))

	req := emergencyImageRequest("unvalidated-artifact")
	req.Msg.ArtifactRef = "artifact-unvalidated"
	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	detail := emergencyDetailFrom(t, err)
	assert.Equal(t, orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_NO_CANDIDATE_ARTIFACT, detail.GetReasonCode())
}

// AC-032-03: an organization without an active binding for the definition's
// customer cannot execute an emergency change.
func TestExecuteEmergencyChangeRequiresActiveBinding(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	// Remove the binding the fixture seeded, if any.
	binding, err := st.Bindings().GetByOrgAndCustomer(t.Context(), "org-001", "cust-001")
	if err == nil {
		require.NoError(t, st.Bindings().SetStatus(t.Context(), binding.ID, store.BindingRevoked))
	}

	_, err = svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequest("no-binding"))
	require.Error(t, err, "AC-032-03: an org without an active binding must be rejected")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// AC-032-19: a pending promotion on the same path blocks another promotion.
func TestExecuteEmergencyChangeLockedPathRejected(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedEmergencyImageIdentity(t, st)
	svc, _, _ = emergencyTestServiceFromExisting(t, svc, st)

	// The promotion path must exist on the definition for the lock to resolve.
	definition, err := st.Definitions().Get(t.Context(), "def-001")
	require.NoError(t, err)
	definition.PromotionMappings = []store.PromotionMapping{{
		WorkloadKind: workloadDeployment, WorkloadName: "api", Container: "api",
		Field: "image_digest", ValuesPath: "api.image.digest",
	}}
	_, err = st.Definitions().Update(t.Context(), definition, nil)
	require.NoError(t, err)

	promotion := func(key string) *connect.Request[orchestratorv1.ExecuteEmergencyChangeRequest] {
		req := emergencyImageRequest(key)
		req.Msg.ConvergenceStrategy = orchestratorv1.ConvergenceStrategy_REQUIRE_PROMOTION
		req.Msg.TargetLocks = []string{"api.image.digest"}
		return req
	}

	_, err = svc.ExecuteEmergencyChange(emergencyAdminContext(), promotion("locked-path-first"))
	require.NoError(t, err)

	_, err = svc.ExecuteEmergencyChange(emergencyAdminContext(), promotion("locked-path-second"))
	require.Error(t, err, "AC-032-19: a pending promotion on the path must block another")
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Equal(t, "LOCKED_PATH", connectErrorReason(err))
}

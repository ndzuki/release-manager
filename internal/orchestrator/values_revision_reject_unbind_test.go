package orchestrator

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// AC-058-40 (TASK-325 W8): rejecting a bound pending revision clears the active
// binding, leaves the convergence task pending_promotion, records why, and makes
// the task preparable again.
//
// The pre-reject half is the negative control: while the revision is bound,
// CreatePrepareSession must refuse the same task with convergence_conflict
// (internal/orchestrator/prepare_sessions.go:245). If the binding were never
// real the re-Prepare after Reject would prove nothing, so both halves are
// asserted in one test.
func TestRejectValuesRevision_UnbindsConvergenceTaskAndAllowsReprepare(t *testing.T) {
	f := newPrepareFixture(t)
	prepareTask := func(parentVersion int64) *connect.Request[orchestratorv1.CreatePrepareSessionRequest] {
		return connect.NewRequest(&orchestratorv1.CreatePrepareSessionRequest{
			ReleaseDefinitionId:   f.defID,
			TaskIds:               []string{"task-vr-1"},
			ExpectedParentVersion: parentVersion,
		})
	}

	prepared, err := f.svc.CreatePrepareSession(f.ctx, prepareTask(0))
	require.NoError(t, err)

	create := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `image: null`,
		PrepareToken:        prepared.Msg.GetPrepareToken(),
	})
	create.Header().Set("Idempotency-Key", "reject-unbind-create")
	created, err := f.svc.CreateValuesRevision(f.ctx, create)
	require.NoError(t, err)
	revisionID := created.Msg.GetRevision().GetId()
	require.NotEmpty(t, revisionID)

	// Negative control: the revision now owns the task, so a second prepare of
	// the same task is refused.
	_, err = f.svc.CreatePrepareSession(f.ctx, prepareTask(1))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "convergence_conflict")

	submit := connect.NewRequest(&orchestratorv1.SubmitValuesRevisionRequest{
		RevisionId: revisionID, ExpectedStateVersion: 1, Comment: "ready",
	})
	submit.Header().Set("Idempotency-Key", "reject-unbind-submit")
	_, err = f.svc.SubmitValuesRevision(f.ctx, submit)
	require.NoError(t, err)

	const rejectionReason = "does not cover the incident"
	reject := connect.NewRequest(&orchestratorv1.RejectValuesRevisionRequest{
		RevisionId: revisionID, ExpectedStateVersion: 2, Reason: rejectionReason,
	})
	reject.Header().Set("Idempotency-Key", "reject-unbind-reject")
	rejected, err := f.svc.RejectValuesRevision(f.adminContext(), reject)
	require.NoError(t, err)
	assert.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_REJECTED, rejected.Msg.GetNewState())

	revision, err := f.st.Values().Get(f.ctx, revisionID)
	require.NoError(t, err)
	assert.Equal(t, store.ValuesStatusRejected, revision.Status)

	task, err := f.st.ConvergenceTasks().Get(f.ctx, "task-vr-1")
	require.NoError(t, err)
	assert.Equal(t, "pending_promotion", task.Status, "AC-058-40: the task must stay pending_promotion")
	assert.Nil(t, task.ActiveRevisionID, "AC-058-40: the active binding must be cleared")
	assert.Nil(t, task.ActiveRevisionStatus)
	require.NotNil(t, task.LastRejectionReason)
	assert.Equal(t, rejectionReason, *task.LastRejectionReason)

	// AC-058-40: the unbound task can be prepared again.
	reprepared, err := f.svc.CreatePrepareSession(f.ctx, prepareTask(1))
	require.NoError(t, err, "AC-058-40: rejecting the bound revision must make the task preparable again")
	assert.NotEmpty(t, reprepared.Msg.GetPrepareToken())
	assert.NotEmpty(t, reprepared.Msg.GetLockedPaths())
}

package preflight

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestErrorCodeFromStatus(t *testing.T) {
	tests := []struct {
		name   string
		result StageResult
		want   string
	}{
		{name: "preserves stable detail code", result: StageResult{Status: StageFailed, Detail: "render_failed: invalid manifest"}, want: "render_failed"},
		{name: "uses timeout code", result: StageResult{Status: StageTimeout}, want: "stage_timeout"},
		{name: "uses generic failed code", result: StageResult{Status: StageFailed}, want: "preflight_failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, errorCodeFromStatus(tt.result))
		})
	}
}

// seedPreflightFixture creates a definition with an active operator so stage
// commands can be dispatched and driven to results.
func seedPreflightFixture(t *testing.T, st *sqlitestore.Store) *store.Operation {
	return seedPreflightFixtureWithOperator(t, st, true)
}

// seedPreflightFixtureWithOperator controls operator seeding: with an operator
// stages dispatch and poll; without one the required stage fails closed
// (AC-019-02 stage_unavailable).
func seedPreflightFixtureWithOperator(t *testing.T, st *sqlitestore.Store, withOperator bool) *store.Operation {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	cust := &store.Customer{ID: "cust-preflight", Name: "Preflight Customer", Slug: "preflight-cust", Status: store.CustomerActive}
	require.NoError(t, st.Customers().Create(ctx, cust))
	cluster := &store.Cluster{ID: "cluster-preflight", Name: "Preflight Cluster", CustomerID: cust.ID}
	require.NoError(t, st.Clusters().Create(ctx, cluster))
	def := &store.ReleaseDefinition{
		ID: "def-preflight", Name: "Preflight Definition", CustomerID: cust.ID, ClusterID: cluster.ID,
		Namespace: "default", ReleaseName: "preflight-rel", Status: store.DefStatusActive, OptimisticVersion: 1,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))
	if withOperator {
		op := &store.Operator{
			ID: "operator-preflight", Name: "preflight-operator", CustomerID: cust.ID, ClusterID: cluster.ID,
			CertSerial: "serial-preflight", Status: store.OperatorActive,
		}
		require.NoError(t, st.Operators().Create(ctx, op))
	}
	operation := &store.Operation{
		ID: uuid.NewString(), OperationType: store.OperationInstall, Status: store.StatusPreflight,
		ReleaseDefinitionID: def.ID, IdempotencyKey: uuid.NewString(), RequestHash: "hash",
		BundleID: "bundle-preflight", StateVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, st.Operations().Create(ctx, operation))
	return operation
}

// newTestCoordinator returns a coordinator with a fast poll interval so stage
// driving tests complete quickly without real minute-scale timeouts.
func newTestCoordinator(t *testing.T, st *sqlitestore.Store) *Coordinator {
	t.Helper()
	c := NewCoordinator(st.Outbox(), st.Operations(), st.Operators(), st.Definitions(), st.Values(), st.Bundles(), st.PreflightLifecycles(), st.Inventories(), slog.New(slog.DiscardHandler))
	c.pollInterval = 20 * time.Millisecond
	return c
}

// seedRollbackFixture seeds the same preflight fixture as
// seedPreflightFixture but with a ROLLBACK operation, so the coordinator can
// be driven through the same stage pipeline. TASK-114/U-1: the stages are
// checks; the real helm rollback is dispatched as a separate non-stage
// :execute command after they pass.
func seedRollbackFixture(t *testing.T, st *sqlitestore.Store) *store.Operation {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	cust := &store.Customer{ID: "cust-preflight", Name: "Preflight Customer", Slug: "preflight-cust", Status: store.CustomerActive}
	require.NoError(t, st.Customers().Create(ctx, cust))
	cluster := &store.Cluster{ID: "cluster-preflight", Name: "Preflight Cluster", CustomerID: cust.ID}
	require.NoError(t, st.Clusters().Create(ctx, cluster))
	def := &store.ReleaseDefinition{
		ID: "def-preflight", Name: "Preflight Definition", CustomerID: cust.ID, ClusterID: cluster.ID,
		Namespace: "default", ReleaseName: "preflight-rel", Status: store.DefStatusActive, OptimisticVersion: 1,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))
	op := &store.Operator{
		ID: "operator-preflight", Name: "preflight-operator", CustomerID: cust.ID, ClusterID: cluster.ID,
		CertSerial: "serial-preflight", Status: store.OperatorActive,
	}
	require.NoError(t, st.Operators().Create(ctx, op))
	operation := &store.Operation{
		ID: uuid.NewString(), OperationType: store.OperationRollback, Status: store.StatusPreflight,
		ReleaseDefinitionID: def.ID, IdempotencyKey: uuid.NewString(), RequestHash: "hash",
		BundleID: "bundle-preflight", StateVersion: 1,
		ExpectedRevision: 3, TargetRevision: 1, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, st.Operations().Create(ctx, operation))
	return operation
}

func waitForCommand(t *testing.T, st *sqlitestore.Store, commandID string) *store.OutboxEntry {
	t.Helper()
	var entry *store.OutboxEntry
	require.Eventually(t, func() bool {
		e, err := st.Outbox().GetByCommandID(context.Background(), commandID)
		if err != nil {
			return false
		}
		entry = e
		return true
	}, 5*time.Second, 20*time.Millisecond)
	return entry
}

// driveOperatorStages drives the three operator-side preflight stages
// (render/cluster/runtime_pull) to a passed result. The artifact stage is
// consumed by the coordinator itself, so it has no operator command to drive
// (TASK-114/U-1).
func driveOperatorStages(t *testing.T, st *sqlitestore.Store, opID string) {
	t.Helper()
	for _, stage := range []string{"render", "cluster", "runtime_pull"} {
		entry := waitForCommand(t, st, opID+":"+stage)
		require.NoError(t, st.Outbox().UpdateStatus(
			context.Background(), entry.ID, store.CommandPersisted, `{"status":"passed"}`))
	}
}

// D-87: the artifact command pre-created by the operation creation transaction
// is consumed, not duplicated, and restarts stay idempotent on the identity.
// TASK-114/U-1: the artifact row is a durable record — the coordinator consumes
// it locally and the release write is a separate non-stage :execute command.
func TestCoordinatorRun_ConsumesPreCreatedArtifactDispatch(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	op := seedPreflightFixture(t, st)
	c := newTestCoordinator(t, st)
	ctx := context.Background()

	// Simulate the OperationCreationUnitOfWork first dispatch row.
	first := &store.OutboxEntry{
		ID: uuid.NewString(), CommandID: op.ID + ":artifact", OperationID: op.ID,
		OperationType: string(op.OperationType), OperatorID: "operator-preflight",
		Payload: []byte(`{"stage":"artifact","operation_id":"` + op.ID + `"}`),
	}
	require.NoError(t, st.Outbox().Create(ctx, first))

	done := make(chan struct{})
	go func() { c.Run(ctx, op); close(done) }()

	// The artifact stage is consumed locally; the operator stages follow.
	driveOperatorStages(t, st, op.ID)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}

	var count int
	require.NoError(t, st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE command_id = ?`, op.ID+":artifact").Scan(&count))
	assert.Equal(t, 1, count, "the pre-created dispatch must be consumed, not duplicated")

	// The artifact record is closed as succeeded, and the release write is a
	// separate command that carries no stage.
	artifact, err := st.Outbox().GetByCommandID(ctx, op.ID+":artifact")
	require.NoError(t, err)
	assert.Equal(t, store.CommandSucceeded, artifact.Status, "the record-only artifact row is closed by the coordinator")

	execute, err := st.Outbox().GetByCommandID(ctx, op.ID+":execute")
	require.NoError(t, err, "the release write must be dispatched after preflight passes")
	payload, err := UnmarshalCommandPayload(execute.Payload)
	require.NoError(t, err)
	assert.Empty(t, payload.Stage, "the release write must not carry a preflight stage")

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusQueued, got.Status, "preflight passed == the release write is queued, not already done")
}

// TASK-114/U-1: the artifact dispatch row is a durable record, never a
// deliverable command. Delivering it would send an operator a stage=artifact
// command, which before this change ran a real Helm install for it.
func TestCoordinatorDispatch_ArtifactRowIsRecordOnly(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	op := seedPreflightFixture(t, st)
	c := newTestCoordinator(t, st)
	ctx := context.Background()

	entry, err := c.Dispatch(ctx, op, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, op.ID+":artifact", entry.CommandID)
	assert.Empty(t, entry.OperatorID, "the artifact row must not be dispatchable to an operator")

	_, err = st.Outbox().GetNextPending(ctx, "operator-preflight")
	assert.ErrorIs(t, err, store.ErrNotFound, "the record-only artifact row must never be delivered")
}

// D-87 restart replay: a Run over an outbox that already carries the
// pre-created artifact dispatch and later stage rows must reuse them instead
// of recreating duplicates (idempotent resume on the stable command identity).
func TestCoordinatorRun_RestartReusesExistingDispatches(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	op := seedPreflightFixture(t, st)
	c := newTestCoordinator(t, st)
	ctx := context.Background()

	// Rows left behind by a previous Run before an interruption: the UOW
	// first dispatch plus the render stage already dispatched.
	for _, stage := range []string{"artifact", "render"} {
		require.NoError(t, st.Outbox().Create(ctx, &store.OutboxEntry{
			ID: uuid.NewString(), CommandID: op.ID + ":" + stage, OperationID: op.ID,
			OperationType: string(op.OperationType), OperatorID: "operator-preflight",
			Payload: []byte(`{"stage":"` + stage + `","operation_id":"` + op.ID + `"}`),
		}))
	}

	done := make(chan struct{})
	go func() { c.Run(ctx, op); close(done) }()

	// Drive all stages through the pre-existing rows.
	driveOperatorStages(t, st, op.ID)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}

	for _, stage := range []string{"artifact", "render", "cluster", "runtime_pull"} {
		var count int
		require.NoError(t, st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE command_id = ?`, op.ID+":"+stage).Scan(&count))
		assert.Equal(t, 1, count, "resumed run must not duplicate command %s", stage)
	}

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusQueued, got.Status, "resumed run dispatches the release write instead of finalizing it")
}

// AC-019-02: a required stage with no available operator fail-closes the
// operation with stage_unavailable (production semantics).
func TestCoordinatorRun_StageUnavailableFailsClosed(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	op := seedPreflightFixtureWithOperator(t, st, false)
	c := newTestCoordinator(t, st)
	ctx := context.Background()

	done := make(chan struct{})
	go func() { c.Run(ctx, op); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusFailed, got.Status, "AC-019-02: fail closed with no operator")

	// The lifecycle records the failure and the attempted stage. The artifact
	// stage is consumed locally, so the first operator stage is the one that
	// reports the missing operator.
	pl, err := st.PreflightLifecycles().GetByOperationID(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", pl.Overall)
	assert.Equal(t, "artifact,render", pl.Stages)

	// TASK-149 / AC-056-03: the stage results are persisted on the operation, not
	// only logged, so the detail page can name the failed stage and its error.
	stored, err := st.Operations().GetPreflightResult(ctx, op.ID)
	require.NoError(t, err)
	require.NotNil(t, stored, "the preflight stage results must be persisted")
	var aggregate struct {
		Overall   string `json:"overall"`
		ErrorCode string `json:"error_code"`
	}
	require.NoError(t, json.Unmarshal(stored, &aggregate))
	assert.Equal(t, "failed", aggregate.Overall)
	assert.Equal(t, "stage_unavailable", aggregate.ErrorCode)
}

// AC-019-02 regression: a cluster whose operators are all revoked must fail
// closed — revoked-only operators never receive commands (restored from v2,
// lost in the TASK-067 baseline rewrite).
func TestCoordinatorRun_RevokedOnlyFailsClosed(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	cust := &store.Customer{ID: "cust-revoked", Name: "Revoked Customer", Slug: "revoked-cust", Status: store.CustomerActive}
	require.NoError(t, st.Customers().Create(ctx, cust))
	cluster := &store.Cluster{ID: "cluster-revoked", Name: "Revoked Cluster", CustomerID: cust.ID}
	require.NoError(t, st.Clusters().Create(ctx, cluster))
	def := &store.ReleaseDefinition{
		ID: "def-revoked", Name: "Revoked Definition", CustomerID: cust.ID, ClusterID: cluster.ID,
		Namespace: "default", ReleaseName: "revoked-rel", Status: store.DefStatusActive, OptimisticVersion: 1,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))
	require.NoError(t, st.Operators().Create(ctx, &store.Operator{
		ID: "operator-revoked", Name: "revoked-operator", CustomerID: cust.ID, ClusterID: cluster.ID,
		CertSerial: "serial-revoked", Status: store.OperatorRevoked,
	}))
	op := &store.Operation{
		ID: uuid.NewString(), OperationType: store.OperationInstall, Status: store.StatusPreflight,
		ReleaseDefinitionID: def.ID, IdempotencyKey: uuid.NewString(), RequestHash: "hash",
		BundleID: "bundle-revoked", StateVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, st.Operations().Create(ctx, op))
	c := newTestCoordinator(t, st)

	done := make(chan struct{})
	go func() { c.Run(ctx, op); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusFailed, got.Status, "revoked-only cluster must fail closed")
	pl, err := st.PreflightLifecycles().GetByOperationID(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", pl.Overall)
}

// REQ-019: preflight timeout is a cancellation — the operation must end
// cancelled (not failed) and the lifecycle must agree.
func TestCoordinatorRun_StageTimeoutCancelsOperation(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	op := seedPreflightFixture(t, st)
	c := newTestCoordinator(t, st)
	ctx := context.Background()

	done := make(chan struct{})
	go func() { c.Run(ctx, op); close(done) }()

	// The artifact stage is consumed locally; let the render stage time out
	// (short StageDef).
	entry := waitForCommand(t, st, op.ID+":render")
	require.NoError(t, st.Outbox().UpdateStatus(ctx, entry.ID, store.CommandPersisted, `{"status":"timeout"}`))
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusCancelled, got.Status, "preflight timeout must cancel the operation")
	pl, err := st.PreflightLifecycles().GetByOperationID(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, "cancelled", pl.Overall)
	assert.Equal(t, "artifact,render", pl.Stages)
}

// AC-019-04/06: all required stages pass → operation CAS to queued and the
// lifecycle records passed with canonical stages.
func TestCoordinatorRun_AllPassedFinalizesLifecycle(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	op := seedPreflightFixture(t, st)
	c := newTestCoordinator(t, st)
	ctx := context.Background()

	done := make(chan struct{})
	go func() { c.Run(ctx, op); close(done) }()

	driveOperatorStages(t, st, op.ID)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusQueued, got.Status, "AC-019-04: preflight passed queues the release write; the operation is not terminal yet")

	// TASK-114/U-1: the release write is a separate command that carries no
	// stage, so the operator runs an INSTALL and never a check.
	execute, err := st.Outbox().GetByCommandID(ctx, op.ID+":execute")
	require.NoError(t, err, "the release write must be dispatched")
	assert.Equal(t, string(store.OperationInstall), execute.OperationType)
	payload, err := UnmarshalCommandPayload(execute.Payload)
	require.NoError(t, err)
	assert.Empty(t, payload.Stage, "the release write must not carry a preflight stage")

	pl, err := st.PreflightLifecycles().GetByOperationID(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, "passed", pl.Overall)
	assert.Equal(t, "artifact,render,dryrun,runtime_pull", pl.Stages, "canonical stage names in execution order")
}

// AC-090-01: a ROLLBACK operation queues its real helm rollback as a separate
// command (INSTALL-symmetric). Before TASK-114 the first stage ran the rollback
// itself and casQueued drove the operation to succeeded without a separate
// execution; now a stage is a check, so the rollback must be its own non-stage
// command.
//
// A rollback carries no bundle, so it has no chart to render: its stage set is
// the artifact stage only (D-V / V-1), and the rollback runs as :execute.
func TestCoordinatorRun_RollbackPassedFinalizesSucceeded(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	op := seedRollbackFixture(t, st)
	c := newTestCoordinator(t, st)
	ctx := context.Background()

	done := make(chan struct{})
	go func() { c.Run(ctx, op); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}

	// The chart-dependent stages must not be dispatched for a bundle-less
	// rollback: they would fail a required stage on an operation with no chart.
	for _, stage := range []string{"render", "cluster", "runtime_pull"} {
		_, err := st.Outbox().GetByCommandID(ctx, op.ID+":"+stage)
		assert.ErrorIs(t, err, store.ErrNotFound,
			"a bundle-less ROLLBACK must not dispatch the %s stage", stage)
	}

	execute, err := st.Outbox().GetByCommandID(ctx, op.ID+":execute")
	require.NoError(t, err, "AC-090-01: ROLLBACK must dispatch its own release write")
	assert.Equal(t, string(store.OperationRollback), execute.OperationType)
	payload, err := UnmarshalCommandPayload(execute.Payload)
	require.NoError(t, err)
	assert.Empty(t, payload.Stage, "the rollback write must not carry a preflight stage")
	assert.Equal(t, int64(1), payload.TargetRevision)

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusQueued, got.Status, "the rollback write is queued, not already done")

	pl, err := st.PreflightLifecycles().GetByOperationID(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, "artifact", pl.Stages, "a bundle-less rollback has only the artifact stage")
}

// AC-090-01 negative: only INSTALL/ROLLBACK get a post-preflight release write
// — an operation of any other type that somehow passes every stage stays queued
// instead of being mis-dispatched (ADR-009 legal hops only; no accidental
// QUEUED→SUCCEEDED direct transition).
func TestCoordinatorRun_NonInstallRollbackTypeStaysQueued(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	op := seedRollbackFixture(t, st)
	op.OperationType = "NOT_INSTALL_OR_ROLLBACK"
	c := newTestCoordinator(t, st)
	ctx := context.Background()

	done := make(chan struct{})
	go func() { c.Run(ctx, op); close(done) }()

	driveOperatorStages(t, st, op.ID)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusQueued, got.Status, "non-INSTALL/ROLLBACK type must not be driven past queued")

	_, err = st.Outbox().GetByCommandID(ctx, op.ID+":execute")
	assert.ErrorIs(t, err, store.ErrNotFound, "a non-INSTALL/ROLLBACK type must not get a release write")
}

// AC-019-01/06: a required stage failure stops the pipeline and records failed.
// The operator's stable `detail` code survives into the persisted result, so a
// fail-closed stage is attributable (TASK-114 AC 4).
func TestCoordinatorRun_RequiredFailureStopsPipeline(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	op := seedPreflightFixture(t, st)
	c := newTestCoordinator(t, st)
	ctx := context.Background()

	done := make(chan struct{})
	go func() { c.Run(ctx, op); close(done) }()

	// The artifact stage passes locally; the render stage fails closed with the
	// operator's stable code (the shape the agent now reports for a stage).
	entry := waitForCommand(t, st, op.ID+":render")
	require.NoError(t, st.Outbox().UpdateStatus(ctx, entry.ID, store.CommandFailed,
		`{"status":"failed","code":"preflight_stage_failed","detail":"preflight_stage_failed"}`))
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}

	_, err := st.Outbox().GetByCommandID(ctx, op.ID+":cluster")
	assert.ErrorIs(t, err, store.ErrNotFound, "AC-019-01: later stages must not run")
	_, err = st.Outbox().GetByCommandID(ctx, op.ID+":execute")
	assert.ErrorIs(t, err, store.ErrNotFound, "a failed preflight must not dispatch the release write")

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusFailed, got.Status)

	pl, err := st.PreflightLifecycles().GetByOperationID(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", pl.Overall)
	assert.Equal(t, "artifact,render", pl.Stages)

	stored, err := st.Operations().GetPreflightResult(ctx, op.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	var aggregate struct {
		Overall     string `json:"overall"`
		FailedStage string `json:"failed_stage"`
		ErrorCode   string `json:"error_code"`
	}
	require.NoError(t, json.Unmarshal(stored, &aggregate))
	assert.Equal(t, "failed", aggregate.Overall)
	assert.Equal(t, "render", aggregate.FailedStage)
	assert.Equal(t, "preflight_stage_failed", aggregate.ErrorCode,
		"the operator's stable stage code must survive into the persisted result")
}

// TestCoordinatorRun_SucceededCommandResultPassesStage locks the operator
// result contract (real smoke 2026-08-27): the operator writes its command
// result as CommandSucceeded with a helm-shaped JSON (`{"status":"succeeded",
// ...}`). pollStage must consume that as a passed stage — previously only
// CommandPersisted was handled and a CommandSucceeded race left the stage
// polling until timeout.
func TestCoordinatorRun_SucceededCommandResultPassesStage(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	op := seedPreflightFixture(t, st)
	c := newTestCoordinator(t, st)
	ctx := context.Background()

	done := make(chan struct{})
	go func() { c.Run(ctx, op); close(done) }()

	// The artifact stage is consumed locally; the operator stages report the
	// operator's helm-shaped succeeded JSON.
	for _, stage := range []string{"render", "cluster", "runtime_pull"} {
		entry := waitForCommand(t, st, op.ID+":"+stage)
		require.NoError(t, st.Outbox().UpdateStatus(ctx, entry.ID, store.CommandSucceeded,
			`{"operation_id":"`+op.ID+`","status":"succeeded","release":{"name":"preflight-rel"}}`))
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusQueued, got.Status, "all stages passed via succeeded results must queue the release write")
	_, err = st.Outbox().GetByCommandID(ctx, op.ID+":execute")
	require.NoError(t, err, "the release write must be dispatched once every stage passed")
}

// TestCoordinatorRun_StageCommandsCarryBundle locks the stage-command
// contract (real smoke 2026-08-27): every precheck stage command (render/
// cluster/runtime_pull) must carry the operation's bundle — the wire Command
// does not convey the stage and the operator executes each INSTALL-typed
// command against the bundle; a nil bundle fails `chart_ref is required`.
func TestCoordinatorRun_StageCommandsCarryBundle(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	op := seedPreflightFixture(t, st)
	// Seed the bundle referenced by the fixture operation.
	require.NoError(t, st.Bundles().Create(t.Context(), &store.ReleaseBundle{
		ID: op.BundleID, Name: "bundle-preflight", ChartRef: "oci://registry.example.com/charts/example",
		ChartVersion: "1.0.0", ChartDigest: "sha256:chart", Status: store.BundleValidated,
		Images: []store.BundleImage{{Ref: "localhost:5001/release-fixture:dev", Digest: "sha256:img", ValuesPath: "image.repository", ValueKind: store.ImageValueFullReference}},
	}))
	c := newTestCoordinator(t, st)
	ctx := context.Background()

	done := make(chan struct{})
	go func() { c.Run(ctx, op); close(done) }()

	// Drive each operator stage to passed and decode its payload: every command
	// must carry the bundle (chart_ref + image), and every stage command must
	// carry its stage name.
	for _, stage := range []string{"render", "cluster", "runtime_pull"} {
		entry := waitForCommand(t, st, op.ID+":"+stage)
		payload, err := UnmarshalCommandPayload(entry.Payload)
		require.NoError(t, err)
		require.NotNil(t, payload.Bundle, "stage %s command must carry the bundle", stage)
		assert.Equal(t, "oci://registry.example.com/charts/example", payload.Bundle.GetChartRef())
		assert.Len(t, payload.Bundle.GetImages(), 1)
		assert.Equal(t, StageName(stage), payload.Stage)
		require.NoError(t, st.Outbox().UpdateStatus(ctx, entry.ID, store.CommandPersisted, `{"status":"passed"}`))
	}

	// TASK-114/U-1: the release write carries the same execution context but no
	// stage — a stage-typed command is a check and must never install.
	execute := waitForCommand(t, st, op.ID+":execute")
	payload, err := UnmarshalCommandPayload(execute.Payload)
	require.NoError(t, err)
	require.NotNil(t, payload.Bundle, "the release write must carry the bundle")
	assert.Equal(t, "oci://registry.example.com/charts/example", payload.Bundle.GetChartRef())
	assert.Len(t, payload.Bundle.GetImages(), 1)
	assert.Empty(t, payload.Stage, "the release write must not carry a preflight stage")
	assert.True(t, payload.CreateNamespace, "INSTALL still creates the target namespace")

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}
}

// AC-019-03/07: cancelling the run context terminates polling and records the
// lifecycle as cancelled without overwriting the operation via a stale CAS.
func TestCoordinatorRun_CancelFinalizesCancelledLifecycle(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	op := seedPreflightFixture(t, st)
	c := newTestCoordinator(t, st)

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(runCtx, op); close(done) }()

	// Cancel while an operator stage is polling for a result. The artifact
	// stage is consumed locally, so the first polled command is render.
	waitForCommand(t, st, op.ID+":render")
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not exit after cancel")
	}

	pl, err := st.PreflightLifecycles().GetByOperationID(context.Background(), op.ID)
	require.NoError(t, err)
	assert.Equal(t, "cancelled", pl.Overall, "AC-019-07: cancel records cancelled")
	assert.Contains(t, pl.Stages, "artifact")

	// The coordinator must not CAS the operation to failed with its stale
	// state_version after a cancellation (AC-019-07).
	got, err := st.Operations().Get(context.Background(), op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusPreflight, got.Status)
}

// ADR-025 (Plan A): runtime_pull is the only statically-optional stage, and
// "required" is now decided at runtime. A stage that RAN and failed must block
// even though Required is false; a stage that could not run reports skipped and
// the pipeline continues. Restoring the old `!stage.Required -> continue`
// shortcut makes the first test fail.
func TestCoordinatorRun_OptionalStageThatRanAndFailedBlocks(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	op := seedPreflightFixture(t, st)
	c := newTestCoordinator(t, st)
	ctx := context.Background()

	done := make(chan struct{})
	go func() { c.Run(ctx, op); close(done) }()

	for _, stage := range []string{"render", "cluster"} {
		entry := waitForCommand(t, st, op.ID+":"+stage)
		require.NoError(t, st.Outbox().UpdateStatus(ctx, entry.ID, store.CommandPersisted, `{"status":"passed"}`))
	}
	pull := waitForCommand(t, st, op.ID+":runtime_pull")
	require.NoError(t, st.Outbox().UpdateStatus(ctx, pull.ID, store.CommandFailed,
		`{"status":"failed","code":"runtime_pull_failed","detail":"runtime_pull_failed"}`))

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}

	_, err := st.Outbox().GetByCommandID(ctx, op.ID+":execute")
	assert.ErrorIs(t, err, store.ErrNotFound, "a failed runtime_pull must not dispatch the release write")

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusFailed, got.Status)
}

func TestCoordinatorRun_SkippedOptionalStageContinues(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	op := seedPreflightFixture(t, st)
	c := newTestCoordinator(t, st)
	ctx := context.Background()

	done := make(chan struct{})
	go func() { c.Run(ctx, op); close(done) }()

	for _, stage := range []string{"render", "cluster"} {
		entry := waitForCommand(t, st, op.ID+":"+stage)
		require.NoError(t, st.Outbox().UpdateStatus(ctx, entry.ID, store.CommandPersisted, `{"status":"passed"}`))
	}
	pull := waitForCommand(t, st, op.ID+":runtime_pull")
	require.NoError(t, st.Outbox().UpdateStatus(ctx, pull.ID, store.CommandPersisted,
		`{"status":"skipped","detail":"runtime_pull_disabled"}`))

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}

	_, err := st.Outbox().GetByCommandID(ctx, op.ID+":execute")
	require.NoError(t, err, "a skipped stage must not block the release write")

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusQueued, got.Status)
}

// D-γ / γ-1a: the queued transition and the :execute dispatch commit in ONE
// transaction, so a failed CAS must leave no dispatch row. The previous shape
// created the dispatch first, which is what let a release write be delivered for
// an operation that never reached queued. Splitting them back into two writes
// makes this test fail.
func TestQueueOperationLeavesNoDispatchWhenTheCasFails(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	op := seedPreflightFixture(t, st)
	ctx := context.Background()

	dispatch := &store.OutboxEntry{
		ID: "entry-queue-fail", CommandID: op.ID + ":execute", OperationID: op.ID,
		OperationType: string(store.OperationInstall), OperatorID: "operator-preflight",
		Payload: []byte(`{}`),
	}
	// A stale state version: the CAS must reject the transition.
	err := st.Operations().QueueOperation(ctx, store.OperationQueueRequest{
		OperationID:  op.ID,
		NextStatus:   store.StatusQueued,
		StateVersion: op.StateVersion + 99,
		Dispatch:     dispatch,
	})
	require.Error(t, err, "a stale state version must fail the CAS")

	_, err = st.Outbox().GetByCommandID(ctx, op.ID+":execute")
	assert.ErrorIs(t, err, store.ErrNotFound,
		"a failed CAS must not leave a dispatch row behind")

	// The happy path commits both: the transition and the (normalized) row.
	require.NoError(t, st.Operations().QueueOperation(ctx, store.OperationQueueRequest{
		OperationID:  op.ID,
		NextStatus:   store.StatusQueued,
		StateVersion: op.StateVersion,
		Dispatch: &store.OutboxEntry{
			ID: "entry-queue-ok", CommandID: op.ID + ":execute", OperationID: op.ID,
			OperationType: string(store.OperationInstall), OperatorID: "operator-preflight",
			Payload: []byte(`{}`),
		},
	}))
	queued, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusQueued, queued.Status)

	entry, err := st.Outbox().GetByCommandID(ctx, op.ID+":execute")
	require.NoError(t, err)
	assert.Equal(t, store.CommandPending, entry.Status,
		"the dispatch must be normalized like Create does, or GetNextPending never sees it")
}

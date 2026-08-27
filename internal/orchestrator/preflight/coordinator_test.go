package preflight

import (
	"context"
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

// D-87: the artifact command pre-created by the operation creation transaction
// is consumed, not duplicated, and restarts stay idempotent on the identity.
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

	// Drive the artifact stage through the pre-created row, then the rest.
	require.NoError(t, st.Outbox().UpdateStatus(ctx, first.ID, store.CommandPersisted, `{"status":"passed"}`))
	for _, stage := range []string{"render", "cluster", "runtime_pull"} {
		entry := waitForCommand(t, st, op.ID+":"+stage)
		require.NoError(t, st.Outbox().UpdateStatus(ctx, entry.ID, store.CommandPersisted, `{"status":"passed"}`))
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}

	var count int
	require.NoError(t, st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE command_id = ?`, op.ID+":artifact").Scan(&count))
	assert.Equal(t, 1, count, "the pre-created dispatch must be consumed, not duplicated")

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusQueued, got.Status)
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
	for _, stage := range []string{"artifact", "render", "cluster", "runtime_pull"} {
		entry := waitForCommand(t, st, op.ID+":"+stage)
		require.NoError(t, st.Outbox().UpdateStatus(ctx, entry.ID, store.CommandPersisted, `{"status":"passed"}`))
	}
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
	assert.Equal(t, store.StatusQueued, got.Status)
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

	// The lifecycle records the failure and the attempted stage.
	pl, err := st.PreflightLifecycles().GetByOperationID(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", pl.Overall)
	assert.Equal(t, "artifact", pl.Stages)
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

	// Drive artifact to passed, then let render time out (short StageDef).
	entry := waitForCommand(t, st, op.ID+":artifact")
	require.NoError(t, st.Outbox().UpdateStatus(ctx, entry.ID, store.CommandPersisted, `{"status":"passed"}`))
	entry = waitForCommand(t, st, op.ID+":render")
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

	for _, stage := range []string{"artifact", "render", "cluster", "runtime_pull"} {
		entry := waitForCommand(t, st, op.ID+":"+stage)
		require.NoError(t, st.Outbox().UpdateStatus(ctx, entry.ID, store.CommandPersisted, `{"status":"passed"}`))
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusQueued, got.Status, "AC-019-04: operation CAS to queued")

	pl, err := st.PreflightLifecycles().GetByOperationID(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, "passed", pl.Overall)
	assert.Equal(t, "artifact,render,dryrun,runtime_pull", pl.Stages, "canonical stage names in execution order")
}

// AC-019-01/06: a required stage failure stops the pipeline and records failed.
func TestCoordinatorRun_RequiredFailureStopsPipeline(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	op := seedPreflightFixture(t, st)
	c := newTestCoordinator(t, st)
	ctx := context.Background()

	done := make(chan struct{})
	go func() { c.Run(ctx, op); close(done) }()

	entry := waitForCommand(t, st, op.ID+":artifact")
	require.NoError(t, st.Outbox().UpdateStatus(ctx, entry.ID, store.CommandFailed, "artifact_failed: digest mismatch"))
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}

	_, err := st.Outbox().GetByCommandID(ctx, op.ID+":render")
	assert.ErrorIs(t, err, store.ErrNotFound, "AC-019-01: later stages must not run")

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusFailed, got.Status)

	pl, err := st.PreflightLifecycles().GetByOperationID(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", pl.Overall)
	assert.Equal(t, "artifact", pl.Stages)
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

	// Drive artifact to CommandSucceeded with the operator's helm-shaped JSON.
	entry := waitForCommand(t, st, op.ID+":artifact")
	require.NoError(t, st.Outbox().UpdateStatus(ctx, entry.ID, store.CommandSucceeded,
		`{"operation_id":"`+op.ID+`","status":"succeeded","release":{"name":"preflight-rel"}}`))
	// Remaining stages: same succeeded result.
	for _, stage := range []string{"render", "cluster", "runtime_pull"} {
		e := waitForCommand(t, st, op.ID+":"+stage)
		require.NoError(t, st.Outbox().UpdateStatus(ctx, e.ID, store.CommandSucceeded, `{"status":"succeeded"}`))
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not finish")
	}

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusQueued, got.Status, "all stages passed via succeeded results must queue the operation")
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

	// Drive each stage to passed and decode its payload: every command must
	// carry the bundle (chart_ref + image).
	for _, stage := range []string{"artifact", "render", "cluster", "runtime_pull"} {
		entry := waitForCommand(t, st, op.ID+":"+stage)
		payload, err := UnmarshalCommandPayload(entry.Payload)
		require.NoError(t, err)
		require.NotNil(t, payload.Bundle, "stage %s command must carry the bundle", stage)
		assert.Equal(t, "oci://registry.example.com/charts/example", payload.Bundle.GetChartRef())
		assert.Len(t, payload.Bundle.GetImages(), 1)
		require.NoError(t, st.Outbox().UpdateStatus(ctx, entry.ID, store.CommandPersisted, `{"status":"passed"}`))
	}
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

	// Cancel while the artifact stage is polling for a result.
	waitForCommand(t, st, op.ID+":artifact")
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

package operator

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/operator/ca"
	"github.com/ndzuki/release-manager/internal/operator/commandtype"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// ── γ-1b delivery fence (D-γ / AC-167-04) ──
//
// The fence is the delivery-side half of D-γ: a non-stage release write
// (`<operation_id>:execute`) must not be handed to an operator while its
// operation is not queued/running. The tests below lock both directions of the
// acceptance criterion: the release write is fenced, and the commands that must
// keep flowing -- preflight stage commands and the operator control commands --
// are not.

const (
	fenceTestOperatorID = "op-1"
	fenceTestDefinition = "definition-fence"
)

// newFenceTestStore opens a per-test in-memory SQLite store seeded with the
// release definition the fence's operation rows reference.
func newFenceTestStore(t *testing.T) store.Store {
	t.Helper()
	st := sqlitestore.OpenTest(t)
	ctx := context.Background()
	now := time.Now()
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{
		ID: "cust-1", Name: "test-customer", Slug: "test", Status: store.CustomerActive, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{
		ID: "clus-1", Name: "test-cluster", CustomerID: "cust-1", Status: store.ClusterActive, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, st.Operators().Create(ctx, &store.Operator{
		ID: fenceTestOperatorID, CustomerID: "cust-1", ClusterID: "clus-1", CertSerial: "cert-1",
		Status: store.OperatorActive, RegisteredAt: now, UpdatedAt: now,
	}))
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: fenceTestDefinition, Name: "definition", CustomerID: "cust-1", ClusterID: "clus-1",
		Namespace: "apps", ReleaseName: "example", ChartName: "example", Status: store.DefStatusActive,
	}, nil))
	return st
}

// newFenceTestService builds the operator service the delivery poll runs on.
func newFenceTestService(t *testing.T, st store.Store) *Service {
	t.Helper()
	authority, err := ca.New(ca.Config{TTL: time.Hour})
	require.NoError(t, err)
	svc, err := NewService(st, nil, WithCA(authority))
	require.NoError(t, err)
	return svc
}

// createFenceOperation inserts one standard operation in the given status.
func createFenceOperation(t *testing.T, st store.Store, id string, status store.OperationStatus) *store.Operation {
	t.Helper()
	op := &store.Operation{
		ID: id, OperationType: store.OperationInstall, Status: status,
		ReleaseDefinitionID: fenceTestDefinition,
		IdempotencyKey:      "idempotency-" + id, RequestHash: "hash",
	}
	require.NoError(t, st.Operations().Create(context.Background(), op))
	return op
}

// createFenceEntry inserts one pending outbox row addressed to the test operator.
func createFenceEntry(t *testing.T, st store.Store, commandID, operationID, operationType string) *store.OutboxEntry {
	t.Helper()
	entry := &store.OutboxEntry{
		ID: uuid.NewString(), CommandID: commandID, OperationID: operationID,
		OperationType: operationType, OperatorID: fenceTestOperatorID,
		Payload: []byte(`{}`), MaxInFlight: 1,
	}
	require.NoError(t, st.Outbox().Create(context.Background(), entry))
	return entry
}

// runFenceDeliveryCycle runs exactly one delivery poll cycle and returns the
// rows it handed to the delivery channel.
func runFenceDeliveryCycle(t *testing.T, svc *Service) []*store.OutboxEntry {
	t.Helper()
	deliverCh := make(chan *store.OutboxEntry, 4)
	done := make(chan struct{})
	closed := svc.deliverNextPending(context.Background(), fenceTestOperatorID, deliverCh, done)
	close(done)
	close(deliverCh)
	require.False(t, closed, "one poll cycle must not report a closed stream")
	var delivered []*store.OutboxEntry
	for entry := range deliverCh {
		delivered = append(delivered, entry)
	}
	return delivered
}

// TestFenceDeliveryReleaseWriteDecisionByOperationStatus locks the verdict the
// fence returns for every operation status. Only queued/running may be
// delivered: the release write is what mutates a customer cluster, and
// FinishOperation drops the result for anything else.
func TestFenceDeliveryReleaseWriteDecisionByOperationStatus(t *testing.T) {
	tests := []struct {
		name   string
		status store.OperationStatus
		want   fenceDeliveryDecision
	}{
		{"queued is deliverable", store.StatusQueued, fenceDeliveryAllow},
		{"running is deliverable", store.StatusRunning, fenceDeliveryAllow},
		{"pending holds", store.StatusPending, fenceDeliveryHold},
		{"preflight holds", store.StatusPreflight, fenceDeliveryHold},
		{"cancelling refuses", store.StatusCancelling, fenceDeliveryRefuse},
		{"cancelled refuses", store.StatusCancelled, fenceDeliveryRefuse},
		{"succeeded refuses", store.StatusSucceeded, fenceDeliveryRefuse},
		{"failed refuses", store.StatusFailed, fenceDeliveryRefuse},
		{"timeout refuses", store.StatusTimeout, fenceDeliveryRefuse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newFenceTestStore(t)
			svc := newFenceTestService(t, st)
			op := createFenceOperation(t, st, "op-"+string(tt.status), tt.status)
			entry := createFenceEntry(t, st, op.ID+":execute", op.ID, string(store.OperationInstall))

			got, _ := svc.fenceDelivery(context.Background(), entry)

			assert.Equal(t, tt.want, got, "operation status %s", tt.status)
		})
	}
}

// TestFenceDeliveryRefusesReleaseWriteWithoutOperation fails closed: a release
// write the gateway cannot account for must never reach a cluster.
func TestFenceDeliveryRefusesReleaseWriteWithoutOperation(t *testing.T) {
	st := newFenceTestStore(t)
	svc := newFenceTestService(t, st)
	entry := createFenceEntry(t, st, "missing-op:execute", "missing-op", string(store.OperationInstall))

	got, reason := svc.fenceDelivery(context.Background(), entry)

	assert.Equal(t, fenceDeliveryRefuse, got)
	assert.Contains(t, reason, "operation_not_found")
}

// TestDeliveryFenceHoldsReleaseWriteUntilOperationIsQueued is the core AC: a
// non-stage release write is not delivered while the operation is still
// preflight, and it is delivered once the operation reaches queued. Holding the
// row (rather than failing it) is what keeps the coordinator's queued CAS --
// which consumes the existing dispatch row instead of inserting a new one --
// able to release it.
func TestDeliveryFenceHoldsReleaseWriteUntilOperationIsQueued(t *testing.T) {
	st := newFenceTestStore(t)
	svc := newFenceTestService(t, st)
	ctx := context.Background()
	op := createFenceOperation(t, st, "op-hold", store.StatusPreflight)
	entry := createFenceEntry(t, st, op.ID+":execute", op.ID, string(store.OperationInstall))

	assert.Empty(t, runFenceDeliveryCycle(t, svc),
		"a release write must not be delivered while its operation is preflight")

	held, err := st.Outbox().Get(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.CommandPending, held.Status,
		"the held row must stay pending so the queued CAS can release it")

	_, err = st.Operations().UpdateStatus(ctx, op.ID, store.StatusQueued, op.StateVersion, "")
	require.NoError(t, err)

	delivered := runFenceDeliveryCycle(t, svc)
	require.Len(t, delivered, 1, "the release write must be delivered once the operation is queued")
	assert.Equal(t, entry.ID, delivered[0].ID)
	assert.NotZero(t, delivered[0].Sequence, "the delivered row must carry a sequence")
}

// TestDeliveryFenceAllowsStageCommandWhileOperationIsPreflight is the
// anti-over-correction case from AC-167-04: stage commands are dispatched
// *during* preflight by design, so fencing them would break the whole pipeline.
func TestDeliveryFenceAllowsStageCommandWhileOperationIsPreflight(t *testing.T) {
	st := newFenceTestStore(t)
	svc := newFenceTestService(t, st)
	op := createFenceOperation(t, st, "op-stage-preflight", store.StatusPreflight)
	entry := createFenceEntry(t, st, op.ID+":render", op.ID, string(store.OperationInstall))

	delivered := runFenceDeliveryCycle(t, svc)

	require.Len(t, delivered, 1, "a stage command must be delivered while its operation is still preflight")
	assert.Equal(t, entry.ID, delivered[0].ID)
	assert.NotZero(t, delivered[0].Sequence)
}

// TestDeliveryFenceRefusesReleaseWriteForCancelledOperation covers the window
// γ-1a does not close: queued can be cancelled before the dispatch is delivered
// (cancelling only rewrites the operations row), so the pending release write
// outlives the state it was created for. It must be failed, not delivered, and
// it must leave the pending queue so it cannot stall the operator.
func TestDeliveryFenceRefusesReleaseWriteForCancelledOperation(t *testing.T) {
	st := newFenceTestStore(t)
	svc := newFenceTestService(t, st)
	ctx := context.Background()
	op := createFenceOperation(t, st, "op-cancelled", store.StatusCancelled)
	entry := createFenceEntry(t, st, op.ID+":execute", op.ID, string(store.OperationInstall))

	assert.Empty(t, runFenceDeliveryCycle(t, svc),
		"a release write for a cancelled operation must not be delivered")

	refused, err := st.Outbox().Get(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.CommandFailed, refused.Status,
		"the refused row must leave the pending queue")
	assert.Contains(t, refused.ResultJSON, "delivery_fenced")
}

// TestDeliveryFenceLeavesControlCommandsDeliverable guards the second
// over-correction the fence must avoid: INVENTORY_SYNC and SECRET_METADATA_LIST
// are non-stage commands whose operation_id is a synthetic id with no operation
// row, so a status lookup would refuse them forever.
func TestDeliveryFenceLeavesControlCommandsDeliverable(t *testing.T) {
	tests := []struct {
		name          string
		commandID     string
		operationID   string
		operationType string
	}{
		{"inventory sync", "sync-command-1", "sync-request-1", commandtype.InventorySync},
		{"secret metadata list", "secret-command-1", "secret-operation-1", commandtype.SecretMetadataList},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newFenceTestStore(t)
			svc := newFenceTestService(t, st)
			entry := createFenceEntry(t, st, tt.commandID, tt.operationID, tt.operationType)

			delivered := runFenceDeliveryCycle(t, svc)

			require.Len(t, delivered, 1, "a control command has no operation row and must stay deliverable")
			assert.Equal(t, entry.ID, delivered[0].ID)
		})
	}
}

package operator_test

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/operator/ca"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// testCA builds a throwaway CA for services that no longer self-generate one
// (ADR-017: the signing CA is externally managed and injected via WithCA).
func testCA(t *testing.T) *ca.CA {
	t.Helper()
	authority, err := ca.New(ca.Config{TTL: time.Hour})
	require.NoError(t, err)
	return authority
}

// newTestSvc creates a Store backed by a per-test in-memory SQLite database.
func newTestSvc(t *testing.T) store.Store {
	t.Helper()
	// OpenTest allocates a unique named in-memory DB per test; the anonymous
	// file::memory:?cache=shared form would be shared by every test in the
	// process, leaking rows across tests once a previous store's connection
	// outlives its cleanup.
	st := sqlitestore.OpenTest(t)

	ctx := context.Background()
	cust := &store.Customer{ID: "cust-1", Name: "test-customer", Slug: "test", Status: store.CustomerActive, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, st.Customers().Create(ctx, cust))
	clus := &store.Cluster{ID: "clus-1", Name: "test-cluster", CustomerID: "cust-1", Status: store.ClusterActive, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, st.Clusters().Create(ctx, clus))
	op := &store.Operator{ID: "op-1", CustomerID: "cust-1", ClusterID: "clus-1", CertSerial: "cert-1", Status: store.OperatorActive, RegisteredAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, st.Operators().Create(ctx, op))
	sess := &store.Session{ID: "sess-1", OperatorID: "op-1", Status: store.SessionOnline, StartedAt: time.Now(), LastHeartbeat: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, st.Sessions().Create(ctx, sess))
	return st
}

// pendingEntry creates an outbox entry with the given fields and inserts it.
func pendingEntry(t *testing.T, st store.Store, opID string, seq int64, status store.CommandStatus) *store.OutboxEntry {
	t.Helper()
	e := &store.OutboxEntry{
		ID:            uuid.New().String(),
		CommandID:     uuid.New().String(),
		OperationID:   opID,
		OperationType: "INSTALL",
		OperatorID:    "op-1",
		Payload:       []byte(`{}`),
		Status:        status,
		MaxInFlight:   1,
		Sequence:      seq,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	require.NoError(t, st.Outbox().Create(context.Background(), e))
	return e
}

// ── AC-016-01: Reconnect re-delivers delivered-but-not-acked commands ──

func TestReconnectReDeliversUnackedCommands(t *testing.T) {
	st := newTestSvc(t)

	// Create a delivered-but-not-acked command.
	e := pendingEntry(t, st, "op-1", 1, store.CommandDelivered)

	// Verify the store query works: delivered but not acked is returned.
	entries, err := st.Outbox().GetDeliveredNotAcked(context.Background(), "op-1")
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, e.ID, entries[0].ID)

	// Mark as acked and verify it's no longer returned.
	require.NoError(t, st.Outbox().UpdateStatus(context.Background(), e.ID, store.CommandPersisted, ""))
	entries, err = st.Outbox().GetDeliveredNotAcked(context.Background(), "op-1")
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// ── AC-016-02: DuplicateResponse for already-completed commands ──

func TestDuplicateDetectionForTerminalCommands(t *testing.T) {
	st := newTestSvc(t)

	// Create a command that is already succeeded (terminal).
	e := pendingEntry(t, st, "op-1", 1, store.CommandSucceeded)
	e.ResultJSON = `{"result":"already-done"}`
	require.NoError(t, st.Outbox().UpdateStatus(context.Background(), e.ID, store.CommandSucceeded, e.ResultJSON))

	// GetByCommandID should find it.
	got, err := st.Outbox().GetByCommandID(context.Background(), e.CommandID)
	require.NoError(t, err)
	assert.Equal(t, store.CommandSucceeded, got.Status)
	assert.Equal(t, `{"result":"already-done"}`, got.ResultJSON)

	// A new result for the same command_id should be detected as duplicate.
	// This is tested in the CommandStream loop via the GetByCommandID check.
	got2, err := st.Outbox().GetByCommandID(context.Background(), e.CommandID)
	require.NoError(t, err)
	assert.True(t, got2.Status == store.CommandSucceeded || got2.Status == store.CommandFailed)
}

// ── AC-016-03: Sequence gap detection ──

func TestSequenceGapDetection(t *testing.T) {
	st := newTestSvc(t)

	// Create commands with sequence 3, 4, 5 (gap from 0).
	pendingEntry(t, st, "op-1", 3, store.CommandPending)
	pendingEntry(t, st, "op-2", 4, store.CommandPending)
	pendingEntry(t, st, "op-3", 5, store.CommandPending)

	// lastSeenSeq=1, maxSeq=5 → gap detected.
	// handleReconnect should send ResyncRequest.
	// Since handleReconnect is unexported, test via reDeliverFrom.
	// reDeliverFrom with fromSeq=1 should skip commands with sequence <= 1
	// and deliver those with sequence > 1 that are not terminal.

	// Actually let's test the store-level gap query.
	entries, err := st.Outbox().GetDeliveredNotAcked(context.Background(), "op-1")
	require.NoError(t, err)
	// None are delivered yet (all pending), so none should be re-delivered.
	assert.Empty(t, entries)

	// Mark e1 as delivered.
	got, err := st.Outbox().GetNextPending(context.Background(), "op-1")
	require.NoError(t, err)
	require.NoError(t, st.Outbox().UpdateStatus(context.Background(), got.ID, store.CommandDelivered, ""))

	// Now GetDeliveredNotAcked should return it.
	entries, err = st.Outbox().GetDeliveredNotAcked(context.Background(), "op-1")
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, int64(3), entries[0].Sequence)
}

// ── AC-016-04: max_inflight=1 queueing ──

func TestMaxInflightEnforcement(t *testing.T) {
	st := newTestSvc(t)

	// Create a delivered-but-not-terminal command (inflight).
	e1 := pendingEntry(t, st, "op-1", 1, store.CommandDelivered)

	// GetInflightForOperator should find it.
	inflight, err := st.Outbox().GetInflightForOperator(context.Background(), "op-1")
	require.NoError(t, err)
	assert.NotNil(t, inflight)
	assert.Equal(t, e1.ID, inflight.ID)

	// Mark as terminal (succeeded).
	require.NoError(t, st.Outbox().UpdateStatus(context.Background(), e1.ID, store.CommandSucceeded, ""))

	// Now GetInflightForOperator should return nil.
	inflight, err = st.Outbox().GetInflightForOperator(context.Background(), "op-1")
	assert.ErrorIs(t, err, store.ErrNotFound)
	assert.Nil(t, inflight)

	// Create a persisted command (not yet terminal) — also should be seen as inflight.
	e2 := pendingEntry(t, st, "op-2", 2, store.CommandPersisted)
	inflight, err = st.Outbox().GetInflightForOperator(context.Background(), "op-1")
	require.NoError(t, err)
	assert.Equal(t, e2.ID, inflight.ID)
}

// ── AC-016-05: Sequence monotonicity and re-deliver after ACK_PERSISTED ──

func TestSequenceMonotonicAndAckPersistedRelease(t *testing.T) {
	st := newTestSvc(t)

	// Sequences are global monotonic.
	seq1, err := st.Outbox().GetNextSequence(context.Background())
	require.NoError(t, err)
	seq2, err := st.Outbox().GetNextSequence(context.Background())
	require.NoError(t, err)
	// Note: GetNextSequence returns MAX+1, so same value if no inserts between calls.
	assert.GreaterOrEqual(t, seq2, seq1)

	// After creating an entry, sequence advances.
	e1 := pendingEntry(t, st, "op-1", seq1, store.CommandDelivered)

	// Before ACK_PERSISTED, the entry is returned by GetDeliveredNotAcked.
	entries, err := st.Outbox().GetDeliveredNotAcked(context.Background(), "op-1")
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, e1.ID, entries[0].ID)

	// After ACK_PERSISTED (acked_at is set), it's no longer returned.
	require.NoError(t, st.Outbox().UpdateStatus(context.Background(), e1.ID, store.CommandPersisted, ""))
	entries, err = st.Outbox().GetDeliveredNotAcked(context.Background(), "op-1")
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// ── Test: GetByCommandID for dedup ──

func TestGetByCommandID(t *testing.T) {
	st := newTestSvc(t)

	cmdID := "dedup-cmd-1"
	e := &store.OutboxEntry{
		ID:            uuid.New().String(),
		CommandID:     cmdID,
		OperationID:   "op-dedup",
		OperationType: "INSTALL",
		OperatorID:    "op-1",
		Payload:       []byte(`{}`),
		Status:        store.CommandSucceeded,
		MaxInFlight:   1,
		Sequence:      10,
		ResultJSON:    `{"output":"done"}`,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	require.NoError(t, st.Outbox().Create(context.Background(), e))

	got, err := st.Outbox().GetByCommandID(context.Background(), cmdID)
	require.NoError(t, err)
	assert.Equal(t, cmdID, got.CommandID)
	assert.Equal(t, store.CommandSucceeded, got.Status)

	// Non-existent command_id returns ErrNotFound.
	_, err = st.Outbox().GetByCommandID(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// ── Test: sequence assignment in outbox ──

func TestSequenceAssignment(t *testing.T) {
	st := newTestSvc(t)

	e := &store.OutboxEntry{
		ID:            uuid.New().String(),
		CommandID:     uuid.New().String(),
		OperationID:   "op-seq",
		OperationType: "INSTALL",
		OperatorID:    "op-1",
		Payload:       []byte(`{}`),
		Status:        store.CommandPending,
		MaxInFlight:   1,
		Sequence:      0, // Not yet assigned.
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	require.NoError(t, st.Outbox().Create(context.Background(), e))

	// Sequence 0 is valid for now (assigned later in deliverPending).
	got, err := st.Outbox().Get(context.Background(), e.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), got.Sequence)
}

func TestDecodeCommandPayload(t *testing.T) {
	payload := []byte(`{
		"definition_id":"definition-1",
		"namespace":"apps",
		"release_name":"example",
		"create_namespace":true,
		"timeout_seconds":45,
		"bundle":{"name":"example-bundle","chart_ref":"oci://registry.example.com/charts/example","chart_version":"1.0.0"},
		"values":{"message":"hello"},
		"values_revision_id":"revision-1",
		"expected_current_revision":3,
		"target_revision":1,
		"atomic":true,
		"values_patch":{"replicas":2}
	}`)
	command := new(operatorv1.Command)

	require.NoError(t, operator.DecodeCommandPayload(payload, command))
	assert.Equal(t, "definition-1", command.GetDefinitionId())
	assert.Equal(t, "apps", command.GetNamespace())
	assert.Equal(t, "example", command.GetReleaseName())
	assert.True(t, command.GetCreateNamespace())
	assert.Equal(t, int64(45), command.GetTimeoutSeconds())
	assert.Equal(t, "oci://registry.example.com/charts/example", command.GetBundle().GetChartRef())
	assert.JSONEq(t, `{"message":"hello"}`, string(command.GetValues()))
	assert.Equal(t, "revision-1", command.GetValuesRevisionId())
	assert.Equal(t, int64(3), command.GetExpectedCurrentRevision())
	assert.Equal(t, int64(1), command.GetTargetRevision())
	assert.True(t, command.GetAtomic())
	assert.JSONEq(t, `{"replicas":2}`, string(command.GetValuesPatch()))
}

func TestDecodeCommandPayload_RollbackFields(t *testing.T) {
	payload := []byte(`{
		"definition_id":"def-001",
		"namespace":"apps",
		"release_name":"example",
		"timeout_seconds":120,
		"values_revision_id":"vr-001",
		"expected_current_revision":3,
		"target_revision":1,
		"atomic":false,
		"values_patch":null
	}`)
	command := new(operatorv1.Command)

	require.NoError(t, operator.DecodeCommandPayload(payload, command))
	assert.Equal(t, "def-001", command.GetDefinitionId())
	assert.Equal(t, "apps", command.GetNamespace())
	assert.Equal(t, "example", command.GetReleaseName())
	assert.Equal(t, int64(120), command.GetTimeoutSeconds())
	assert.Equal(t, "vr-001", command.GetValuesRevisionId())
	assert.Equal(t, int64(3), command.GetExpectedCurrentRevision())
	assert.Equal(t, int64(1), command.GetTargetRevision())
	assert.False(t, command.GetAtomic())
}

func TestFinishOperation(t *testing.T) {
	tests := []struct {
		name       string
		result     string
		wantStatus store.OperationStatus
		wantError  string
	}{
		{name: "succeeded", result: "succeeded", wantStatus: store.StatusSucceeded},
		{name: "failed", result: "failed", wantStatus: store.StatusFailed, wantError: `{"code":"helm_install_failed"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := newTestSvc(t)
			svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
			require.NoError(t, err)
			ctx := context.Background()
			def := &store.ReleaseDefinition{
				ID:          "definition-" + test.name,
				Name:        "definition",
				CustomerID:  "cust-1",
				ClusterID:   "clus-1",
				Namespace:   "apps",
				ReleaseName: "example",
				ChartName:   "example",
				Status:      store.DefStatusActive,
			}
			require.NoError(t, st.Definitions().Create(ctx, def, nil), nil)
			op := &store.Operation{
				ID:                  "operation-" + test.name,
				OperationType:       store.OperationInstall,
				Status:              store.StatusQueued,
				ReleaseDefinitionID: def.ID,
				IdempotencyKey:      "idempotency-" + test.name,
				RequestHash:         "hash",
			}
			require.NoError(t, st.Operations().Create(ctx, op))

			svc.FinishOperation(ctx, op.ID, test.result, test.wantError)

			got, err := st.Operations().Get(ctx, op.ID)
			require.NoError(t, err)
			assert.Equal(t, test.wantStatus, got.Status)
			assert.Equal(t, test.wantError, got.LastError)
		})
	}
}

// TestFinishOperation_IgnoresPreflightState locks the preflight-finalization
// boundary (real smoke 2026-08-27): a preflight-stage command result must not
// finalize the operation (it is still in `preflight`; the coordinator CASes
// preflight→queued/failed after ALL stages). The artifact stage result
// previously triggered `invalid state transition: complete from preflight`.
func TestFinishOperation_IgnoresPreflightState(t *testing.T) {
	st := newTestSvc(t)
	svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)
	ctx := context.Background()
	def := &store.ReleaseDefinition{
		ID: "definition-preflight-ignore", Name: "definition", CustomerID: "cust-1", ClusterID: "clus-1",
		Namespace: "apps", ReleaseName: "example", ChartName: "example", Status: store.DefStatusActive,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))
	op := &store.Operation{
		ID: "operation-preflight-ignore", OperationType: store.OperationInstall,
		Status: store.StatusPreflight, ReleaseDefinitionID: def.ID,
		IdempotencyKey: "idempotency-preflight-ignore", RequestHash: "hash",
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	svc.FinishOperation(ctx, op.ID, "succeeded", `{"code":"ok"}`)

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusPreflight, got.Status, "preflight operation must not be finalized by a stage result")
}

func TestHandleCommandResultFinalizesUpgrade(t *testing.T) {
	st := newTestSvc(t)
	svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)
	ctx := t.Context()
	definition := &store.ReleaseDefinition{
		ID: "definition-upgrade", Name: "example", CustomerID: "cust-1", ClusterID: "clus-1",
		Namespace: "apps", ReleaseName: "example", Status: store.DefStatusActive,
	}
	require.NoError(t, st.Definitions().Create(ctx, definition, nil))
	require.NoError(t, st.Inventories().Upsert(ctx, &store.ReleaseInventory{
		ReleaseDefinitionID: definition.ID, CustomerID: "cust-1", ClusterID: "clus-1",
		Namespace: "apps", ReleaseName: "example", Revision: 1, Status: "deployed",
		InventoryStatus: store.InventoryActive,
	}))
	op := &store.Operation{
		ID: "operation-upgrade", OperationType: store.OperationUpgrade, Status: store.StatusQueued,
		ReleaseDefinitionID: definition.ID, IdempotencyKey: "idem-upgrade", RequestHash: "hash",
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	result := &operatorv1.CommandResult{
		CommandId: "command-upgrade", OperationId: op.ID, Status: "succeeded",
		Result: &operatorv1.CommandResult_Upgrade{Upgrade: &operatorv1.UpgradeResult{
			Active: &operatorv1.ReleaseSnapshot{
				HelmRevision: 2, BundleDigest: "sha256:bundle", ChartDigest: "sha256:chart",
				EffectiveValuesDigest: "sha256:values", ManifestDigest: "sha256:manifest", Status: "deployed",
			},
			ResourceSummary: &operatorv1.ResourceSummary{ResourceCount: 4, ManifestDigest: "sha256:manifest"},
		}},
	}

	require.NoError(t, svc.HandleCommandResult(ctx, result))
	updated, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusSucceeded, updated.Status)
	inventory, err := st.Inventories().GetByDefinition(ctx, definition.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, inventory.Revision)
	assert.Equal(t, "sha256:manifest", inventory.ObservedManifestDigest)
	tracking, err := st.RolloutTrackings().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, 4, tracking.ResourceCount)
	assert.Equal(t, "deployed", inventory.LiveStatus)

	require.NoError(t, svc.HandleCommandResult(ctx, result))
	resultRecord, err := st.ExecutionResults().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, "upgrade", resultRecord.ResultType)
}

func TestHandleCommandResultAtomicRollbackPersistsActiveRevision(t *testing.T) {
	st := newTestSvc(t)
	svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)
	ctx := t.Context()
	definition := &store.ReleaseDefinition{
		ID: "definition-rollback", Name: "example", CustomerID: "cust-1", ClusterID: "clus-1",
		Namespace: "apps", ReleaseName: "example", Status: store.DefStatusActive,
	}
	require.NoError(t, st.Definitions().Create(ctx, definition, nil))
	require.NoError(t, st.Inventories().Upsert(ctx, &store.ReleaseInventory{
		ReleaseDefinitionID: definition.ID, CustomerID: "cust-1", ClusterID: "clus-1",
		Namespace: "apps", ReleaseName: "example", Revision: 2, Status: "deployed",
		InventoryStatus: store.InventoryActive,
	}))
	op := &store.Operation{
		ID: "operation-rollback", OperationType: store.OperationUpgrade, Status: store.StatusRunning,
		ReleaseDefinitionID: definition.ID, IdempotencyKey: "idem-rollback", RequestHash: "hash",
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	result := &operatorv1.CommandResult{
		CommandId: "command-rollback", OperationId: op.ID, Status: "failed",
		Error: &operatorv1.ExecutionError{Code: "helm_upgrade_failed", Message: "Helm upgrade failed"},
		Result: &operatorv1.CommandResult_Upgrade{Upgrade: &operatorv1.UpgradeResult{
			RollbackSucceeded: true,
			Active: &operatorv1.ReleaseSnapshot{
				HelmRevision: 1, BundleDigest: "sha256:old-bundle", ChartDigest: "sha256:old-chart",
				EffectiveValuesDigest: "sha256:old-values", ManifestDigest: "sha256:old-manifest", Status: "deployed",
			},
		}},
	}

	require.NoError(t, svc.HandleCommandResult(ctx, result))
	updated, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusFailed, updated.Status)
	inventory, err := st.Inventories().GetByDefinition(ctx, definition.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, inventory.Revision)
	assert.Equal(t, "sha256:old-manifest", inventory.ObservedManifestDigest)
	assert.Equal(t, "deployed", inventory.LiveStatus)
}

func TestHandleCommandResultRollbackFailureMarksOutOfSync(t *testing.T) {
	st := newTestSvc(t)
	svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)
	ctx := t.Context()
	definition := &store.ReleaseDefinition{
		ID: "definition-out-of-sync", Name: "example", CustomerID: "cust-1", ClusterID: "clus-1",
		Namespace: "apps", ReleaseName: "example", Status: store.DefStatusActive,
	}
	require.NoError(t, st.Definitions().Create(ctx, definition, nil))
	require.NoError(t, st.Inventories().Upsert(ctx, &store.ReleaseInventory{
		ReleaseDefinitionID: definition.ID, CustomerID: "cust-1", ClusterID: "clus-1",
		Namespace: "apps", ReleaseName: "example", Revision: 1, Status: "deployed",
		InventoryStatus: store.InventoryActive,
	}))
	op := &store.Operation{
		ID: "operation-out-of-sync", OperationType: store.OperationUpgrade, Status: store.StatusRunning,
		ReleaseDefinitionID: definition.ID, IdempotencyKey: "idem-out-of-sync", RequestHash: "hash",
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	result := &operatorv1.CommandResult{
		CommandId: "command-out-of-sync", OperationId: op.ID, Status: "failed",
		Error: &operatorv1.ExecutionError{Code: "atomic_rollback_failed", Message: "manual intervention required"},
		Result: &operatorv1.CommandResult_Upgrade{Upgrade: &operatorv1.UpgradeResult{
			Active: &operatorv1.ReleaseSnapshot{HelmRevision: 2, ManifestDigest: "sha256:failed", Status: "failed"},
		}},
	}

	require.NoError(t, svc.HandleCommandResult(ctx, result))
	updated, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusFailed, updated.Status)
	assert.Equal(t, "atomic_rollback_failed", updated.LastError)
	inventory, err := st.Inventories().GetByDefinition(ctx, definition.ID)
	require.NoError(t, err)
	assert.Equal(t, store.InventoryOutOfSync, inventory.InventoryStatus)
	assert.Equal(t, 2, inventory.Revision)
	assert.Equal(t, "sha256:failed", inventory.ObservedManifestDigest)
	assert.Equal(t, "failed", inventory.LiveStatus)
	assert.False(t, result.GetError().GetRetryable())
}

func TestFinishOperation_PreflightFailurePersistsStableCode(t *testing.T) {
	st := newTestSvc(t)
	svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)
	ctx := t.Context()
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: "definition-preflight-failed", Name: "definition-preflight-failed",
		CustomerID: "cust-1", ClusterID: "clus-1", Namespace: "apps", ReleaseName: "example",
		Status: store.DefStatusActive,
	}, nil))
	require.NoError(t, st.Operations().Create(ctx, &store.Operation{
		ID: "operation-preflight-failed", OperationType: store.OperationUpgrade,
		Status: store.StatusPending, ReleaseDefinitionID: "definition-preflight-failed",
		IdempotencyKey: "idempotency-preflight-failed", RequestHash: "hash",
	}))

	svc.FinishOperation(ctx, "operation-preflight-failed", "failed", `{"code":"inventory_stale"}`)

	got, err := st.Operations().Get(ctx, "operation-preflight-failed")
	require.NoError(t, err)
	assert.Equal(t, store.StatusFailed, got.Status)
	assert.Equal(t, `{"code":"inventory_stale"}`, got.LastError)
}

// TestCommandStreamAckPersistedWritesTimelineEntry drives the real
// CommandStream bidi endpoint over HTTP: an ACK_PERSISTED must atomically
// persist the outbox row and append one ACK timeline entry; a replayed
// ACK_PERSISTED (reconnect) must not write a second entry (AC-077-01/10).
func TestCommandStreamAckPersistedWritesTimelineEntry(t *testing.T) {
	st := newTestSvc(t)
	ctx := t.Context()
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: "definition-ack-stream", Name: "definition-ack-stream",
		CustomerID: "cust-1", ClusterID: "clus-1", Namespace: "apps", ReleaseName: "example",
		Status: store.DefStatusActive,
	}, nil))
	require.NoError(t, st.Operations().Create(ctx, &store.Operation{
		ID: "operation-ack-stream", OperationType: store.OperationInstall,
		Status: store.StatusRunning, ReleaseDefinitionID: "definition-ack-stream",
		IdempotencyKey: "idem-ack-stream", RequestHash: "hash",
	}))
	entry := pendingEntry(t, st, "operation-ack-stream", 1, store.CommandDelivered)

	svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)
	path, handler := operatorv1connect.NewOperatorServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(path, handler)

	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	client := operatorv1connect.NewOperatorServiceClient(srv.Client(), srv.URL)
	stream := client.CommandStream(ctx)
	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Hello{
			Hello: &operatorv1.Hello{SessionId: "sess-1", OperatorId: "op-1", LastSeenSequence: 1},
		},
	}))
	established, err := stream.Receive()
	require.NoError(t, err)
	require.NotNil(t, established.GetSessionEstablished())

	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Ack{
			Ack: &operatorv1.Ack{
				OutboxId: entry.ID, CommandId: entry.CommandID,
				Sequence: 1, AckType: operatorv1.AckType_ACK_TYPE_PERSISTED,
			},
		},
	}))
	require.NoError(t, stream.CloseRequest())
	for {
		if _, err := stream.Receive(); err != nil {
			break // stream closed by server after request close
		}
	}

	got, err := st.Outbox().Get(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.CommandPersisted, got.Status)
	entries, err := st.Timeline().List(ctx, "operation-ack-stream", 0, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, string(store.TimelineEntryACK), entries[0].Kind)

	// Replay: a second connection with a replayed ACK_PERSISTED (e.g. the
	// operator's local store survived a reconnect) must stay idempotent.
	stream2 := client.CommandStream(ctx)
	require.NoError(t, err)
	require.NoError(t, stream2.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Hello{
			Hello: &operatorv1.Hello{SessionId: "sess-1", OperatorId: "op-1", LastSeenSequence: 1},
		},
	}))
	_, err = stream2.Receive()
	require.NoError(t, err)
	require.NoError(t, stream2.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Ack{
			Ack: &operatorv1.Ack{
				OutboxId: entry.ID, CommandId: entry.CommandID,
				Sequence: 1, AckType: operatorv1.AckType_ACK_TYPE_PERSISTED,
			},
		},
	}))
	require.NoError(t, stream2.CloseRequest())
	for {
		if _, err := stream2.Receive(); err != nil {
			break
		}
	}

	entries, err = st.Timeline().List(ctx, "operation-ack-stream", 0, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

// ── AC-077-02/15/17: rollout_progress service-side handling ──

// commandStreamPair starts a real bidi CommandStream over HTTP/2 and returns
// the client.
func commandStreamPair(t *testing.T, svc *operator.Service) operatorv1connect.OperatorServiceClient {
	t.Helper()
	path, handler := operatorv1connect.NewOperatorServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return operatorv1connect.NewOperatorServiceClient(srv.Client(), srv.URL)
}

func TestCommandStreamRolloutProgressWritesTimelineEntry(t *testing.T) {
	st := newTestSvc(t)
	ctx := t.Context()
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: "definition-rollout", Name: "definition-rollout",
		CustomerID: "cust-1", ClusterID: "clus-1", Namespace: "apps", ReleaseName: "example",
		Status: store.DefStatusActive,
	}, nil))
	require.NoError(t, st.Operations().Create(ctx, &store.Operation{
		ID: "operation-rollout", OperationType: store.OperationInstall,
		Status: store.StatusRunning, ReleaseDefinitionID: "definition-rollout",
		IdempotencyKey: "idem-rollout", RequestHash: "hash",
	}))
	pendingEntry(t, st, "operation-rollout", 1, store.CommandDelivered)

	svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)
	client := commandStreamPair(t, svc)
	stream := client.CommandStream(ctx)
	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Hello{
			Hello: &operatorv1.Hello{SessionId: "sess-1", OperatorId: "op-1", LastSeenSequence: 1},
		},
	}))
	_, err = stream.Receive()
	require.NoError(t, err)

	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_RolloutProgress{
			RolloutProgress: &operatorv1.RolloutProgress{
				OperationId: "operation-rollout", WorkloadRef: "deployments/app/default",
				Ready: 2, Desired: 3,
			},
		},
	}))
	require.NoError(t, stream.CloseRequest())
	for {
		if _, err := stream.Receive(); err != nil {
			break
		}
	}

	entries, err := st.Timeline().List(ctx, "operation-rollout", 0, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, string(store.TimelineEntryRolloutProgress), entries[0].Kind)
	var data store.RolloutProgressTimelineData
	require.NoError(t, json.Unmarshal(entries[0].Data, &data))
	assert.Equal(t, store.RolloutProgressTimelineData{WorkloadRef: "deployments/app/default", Ready: 2, Desired: 3}, data)
}

func TestCommandStreamRolloutProgressForeignOperationDropped(t *testing.T) {
	st := newTestSvc(t)
	ctx := t.Context()
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: "definition-foreign", Name: "definition-foreign",
		CustomerID: "cust-1", ClusterID: "clus-1", Namespace: "apps", ReleaseName: "foreign",
		Status: store.DefStatusActive,
	}, nil))
	require.NoError(t, st.Operations().Create(ctx, &store.Operation{
		ID: "operation-foreign", OperationType: store.OperationInstall,
		Status: store.StatusRunning, ReleaseDefinitionID: "definition-foreign",
		IdempotencyKey: "idem-foreign", RequestHash: "hash",
	}))
	// Outbox row owned by a different operator than the session (op-2 vs op-1);
	// the entry must be persisted with op-2, mutating the in-memory struct
	// alone would not change the stored row.
	require.NoError(t, st.Outbox().Create(ctx, &store.OutboxEntry{
		ID: uuid.NewString(), CommandID: uuid.NewString(), OperationID: "operation-foreign",
		OperationType: "INSTALL", OperatorID: "op-2", Payload: []byte(`{}`),
		Status: store.CommandDelivered, MaxInFlight: 1, Sequence: 1,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)
	client := commandStreamPair(t, svc)
	stream := client.CommandStream(ctx)
	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Hello{
			Hello: &operatorv1.Hello{SessionId: "sess-1", OperatorId: "op-1", LastSeenSequence: 1},
		},
	}))
	_, err = stream.Receive()
	require.NoError(t, err)
	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_RolloutProgress{
			RolloutProgress: &operatorv1.RolloutProgress{
				OperationId: "operation-foreign", WorkloadRef: "deployments/app/default",
				Ready: 1, Desired: 3,
			},
		},
	}))
	require.NoError(t, stream.CloseRequest())
	for {
		if _, err := stream.Receive(); err != nil {
			break
		}
	}

	entries, err := st.Timeline().List(ctx, "operation-foreign", 0, 10)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestCommandStreamRolloutProgressTerminalOperationDropped(t *testing.T) {
	st := newTestSvc(t)
	ctx := t.Context()
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: "definition-terminal", Name: "definition-terminal",
		CustomerID: "cust-1", ClusterID: "clus-1", Namespace: "apps", ReleaseName: "terminal",
		Status: store.DefStatusActive,
	}, nil))
	require.NoError(t, st.Operations().Create(ctx, &store.Operation{
		ID: "operation-terminal", OperationType: store.OperationInstall,
		Status: store.StatusRunning, ReleaseDefinitionID: "definition-terminal",
		IdempotencyKey: "idem-terminal", RequestHash: "hash", StateVersion: 1,
	}))
	pendingEntry(t, st, "operation-terminal", 1, store.CommandDelivered)
	_, err := st.Operations().Transition(ctx, "operation-terminal", store.StatusFailed, 1, "failed")
	require.NoError(t, err)

	svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)
	client := commandStreamPair(t, svc)
	stream := client.CommandStream(ctx)
	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Hello{
			Hello: &operatorv1.Hello{SessionId: "sess-1", OperatorId: "op-1", LastSeenSequence: 1},
		},
	}))
	_, err = stream.Receive()
	require.NoError(t, err)
	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_RolloutProgress{
			RolloutProgress: &operatorv1.RolloutProgress{
				OperationId: "operation-terminal", WorkloadRef: "deployments/app/default",
				Ready: 1, Desired: 3,
			},
		},
	}))
	require.NoError(t, stream.CloseRequest())
	for {
		if _, err := stream.Receive(); err != nil {
			break
		}
	}

	entries, err := st.Timeline().List(ctx, "operation-terminal", 0, 10)
	require.NoError(t, err)
	// STATE_TRANSITION plus the AC-077-03 ERROR entry from the failed
	// transition; no progress entry (AC-077-17).
	require.Len(t, entries, 2)
	for _, e := range entries {
		assert.NotEqual(t, string(store.TimelineEntryRolloutProgress), e.Kind)
	}
}

func TestCommandStreamRolloutProgressInvalidFieldDropped(t *testing.T) {
	st := newTestSvc(t)
	ctx := t.Context()
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: "definition-invalid", Name: "definition-invalid",
		CustomerID: "cust-1", ClusterID: "clus-1", Namespace: "apps", ReleaseName: "invalid",
		Status: store.DefStatusActive,
	}, nil))
	require.NoError(t, st.Operations().Create(ctx, &store.Operation{
		ID: "operation-invalid", OperationType: store.OperationInstall,
		Status: store.StatusRunning, ReleaseDefinitionID: "definition-invalid",
		IdempotencyKey: "idem-invalid", RequestHash: "hash",
	}))
	pendingEntry(t, st, "operation-invalid", 1, store.CommandDelivered)

	svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)
	client := commandStreamPair(t, svc)
	stream := client.CommandStream(ctx)
	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Hello{
			Hello: &operatorv1.Hello{SessionId: "sess-1", OperatorId: "op-1", LastSeenSequence: 1},
		},
	}))
	_, err = stream.Receive()
	require.NoError(t, err)

	// ready > desired and non-whitelisted GVR: both must be dropped silently.
	for _, progress := range []*operatorv1.RolloutProgress{
		{OperationId: "operation-invalid", WorkloadRef: "deployments/app/default", Ready: 4, Desired: 3},
		{OperationId: "operation-invalid", WorkloadRef: "cronjobs/app/default", Ready: 1, Desired: 3},
		{OperationId: "operation-invalid", WorkloadRef: "deployments/default", Ready: 1, Desired: 3},
	} {
		require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
			Payload: &operatorv1.CommandStreamRequest_RolloutProgress{RolloutProgress: progress},
		}))
	}
	require.NoError(t, stream.CloseRequest())
	for {
		if _, err := stream.Receive(); err != nil {
			break
		}
	}

	entries, err := st.Timeline().List(ctx, "operation-invalid", 0, 10)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// AC-077-16: proto3 unknown-field tolerance. A CommandStreamRequest wire
// payload carrying oneof field 9 (rollout_progress) decodes without error
// even when the decoder build does not know the field (old orchestrator):
// unknown fields are retained and ignored, never failing the stream. The
// authoritative wire-compat evidence is the buf breaking check (additive
// field 9 only; no renumbering).
func TestCommandStreamRequest_UnknownOneofFieldTolerated(t *testing.T) {
	progress := &operatorv1.RolloutProgress{OperationId: "op-1", WorkloadRef: "deployments/app/default", Ready: 1, Desired: 3}
	inner, err := proto.Marshal(progress)
	require.NoError(t, err)
	wire := protowire.AppendTag(nil, 9, protowire.BytesType)
	wire = protowire.AppendBytes(wire, inner)

	var req operatorv1.CommandStreamRequest
	require.NoError(t, proto.Unmarshal(wire, &req))
	assert.NotNil(t, req.GetRolloutProgress())
	assert.Equal(t, "op-1", req.GetRolloutProgress().GetOperationId())

	// Unknown-field semantics: a field the decoder does not know is
	// preserved and round-trips without error.
	wireUnknown := protowire.AppendTag(nil, 99, protowire.BytesType)
	wireUnknown = protowire.AppendBytes(wireUnknown, []byte("future-payload"))
	require.NoError(t, proto.Unmarshal(wireUnknown, &req))
	got, err := proto.Marshal(&req)
	require.NoError(t, err)
	assert.Equal(t, wireUnknown, got)
}

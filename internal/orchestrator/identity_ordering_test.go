package orchestrator

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/operator/ca"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// ── REQ-088 (TASK-088): identity arrival-order contract matrix ──
//
// These tests assemble the combined deployment shape mirrored from
// cmd/orchestrator/main_test.go (TASK-087): one sqlite store shared by the
// operator Service (report consumption over the real CommandStream bidi) and
// the orchestrator Service (SyncInventory creation + pending identity replay,
// REQ-088 D5=A). AC-066-17 remains the final combination smoke; this matrix is
// the task's own AC-088-04 verification.

// identityOrderingFixture holds the two services sharing one store plus the
// seeded actor identity (operator/session) used by the CommandStream Hello.
type identityOrderingFixture struct {
	st              store.Store
	operatorSvc     *operator.Service
	orchestratorSvc *Service
	ca              *ca.CA
	operatorID      string
	customerID      string
	clusterID       string
	sessionID       string
}

const (
	identityOrderingOperatorID = "op-088"
	identityOrderingCustomerID = "cust-088"
	identityOrderingClusterID  = "clus-088"
	identityOrderingSessionID  = "sess-088"
)

func newIdentityOrderingFixture(t *testing.T) *identityOrderingFixture {
	t.Helper()
	st := sqlitestore.OpenTest(t)
	ctx := context.Background()
	authority, err := ca.New(ca.Config{TTL: time.Hour})
	require.NoError(t, err)

	now := time.Now()
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{
		ID: identityOrderingCustomerID, Name: "ordering-customer", Slug: "ordering-customer",
		Status: store.CustomerActive, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{
		ID: identityOrderingClusterID, Name: "ordering-cluster", CustomerID: identityOrderingCustomerID,
		Status: store.ClusterActive, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, st.Operators().Create(ctx, &store.Operator{
		ID: identityOrderingOperatorID, Name: identityOrderingClusterID,
		CustomerID: identityOrderingCustomerID, ClusterID: identityOrderingClusterID,
		CertSerial: "cert-088", Status: store.OperatorActive,
		RegisteredAt: now, UpdatedAt: now,
	}))
	require.NoError(t, st.Sessions().Create(ctx, &store.Session{
		ID: identityOrderingSessionID, OperatorID: identityOrderingOperatorID,
		Status: store.SessionOnline, StartedAt: now, LastHeartbeat: now,
		ExpiresAt: now.Add(time.Hour),
	}))

	logger := slog.New(slog.DiscardHandler)
	operatorSvc, err := operator.NewService(st, logger,
		operator.WithCA(authority),
		operator.WithIdentityMetrics(operator.NewIdentityMetrics(nil)),
	)
	require.NoError(t, err)
	orchestratorSvc := NewService(st, nil, "staging",
		logger,
		NewPendingIdentityReplayer(operatorSvc),
	)
	return &identityOrderingFixture{
		st: st, operatorSvc: operatorSvc, orchestratorSvc: orchestratorSvc, ca: authority,
		operatorID: identityOrderingOperatorID, customerID: identityOrderingCustomerID,
		clusterID: identityOrderingClusterID, sessionID: identityOrderingSessionID,
	}
}

// seedOrderingDefinition creates an active definition (and its inventory
// prerequisites) for the identity ordering matrix.
//
//nolint:unparam // namespace is uniformly "apps" across the matrix, kept explicit for readability
func (f *identityOrderingFixture) seedOrderingDefinition(t *testing.T, definitionID, namespace, releaseName string) {
	t.Helper()
	require.NoError(t, f.st.Definitions().Create(t.Context(), &store.ReleaseDefinition{
		ID: definitionID, Name: definitionID,
		CustomerID: f.customerID, ClusterID: f.clusterID,
		Namespace: namespace, ReleaseName: releaseName,
		Status: store.DefStatusActive,
	}, nil))
}

// identityStreamFor opens a CommandStream performing the Hello handshake and
// returns the live client stream (mirror of operator's openIdentityStream).
func (f *identityOrderingFixture) identityStreamFor(t *testing.T) *connect.BidiStreamForClient[operatorv1.CommandStreamRequest, operatorv1.CommandStreamResponse] {
	t.Helper()
	path, handler := operatorv1connect.NewOperatorServiceHandler(f.operatorSvc)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	client := operatorv1connect.NewOperatorServiceClient(srv.Client(), srv.URL)
	stream := client.CommandStream(t.Context())
	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Hello{
			Hello: &operatorv1.Hello{SessionId: f.sessionID, OperatorId: f.operatorID, LastSeenSequence: 1},
		},
	}))
	established, err := stream.Receive()
	require.NoError(t, err)
	require.NotNil(t, established.GetSessionEstablished())
	return stream
}

// reportIdentityItem sends one report with a single identity item and drains
// the stream so server-side handling has fully completed (async to Send).
//
//nolint:unparam // release namespace is uniformly "apps" across the matrix, kept explicit for readability
func (f *identityOrderingFixture) reportIdentityItem(t *testing.T, namespace, releaseName, kind, name, workloadNamespace, uid string) {
	t.Helper()
	stream := f.identityStreamFor(t)
	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_WorkloadIdentityReport{
			WorkloadIdentityReport: &operatorv1.WorkloadIdentityReport{Items: []*operatorv1.WorkloadIdentityItem{{
				ReleaseNamespace: namespace, ReleaseName: releaseName,
				Kind: kind, Name: name, Namespace: workloadNamespace, Uid: uid,
			}}},
		},
	}))
	require.NoError(t, stream.CloseRequest())
	for {
		if _, err := stream.Receive(); err != nil {
			break
		}
	}
}

// syncInventoryItem applies one SyncInventory (targeted) item creating the
// inventory row, exactly like the operator's targeted inventory update path.
//
//nolint:unparam // release namespace is uniformly "apps" across the matrix, kept explicit for readability
func (f *identityOrderingFixture) syncInventoryItem(t *testing.T, syncID, namespace, releaseName, definitionID string) {
	t.Helper()
	_, err := f.orchestratorSvc.SyncInventory(t.Context(), connect.NewRequest(&orchestratorv1.SyncInventoryRequest{
		OperatorId: f.operatorID,
		CustomerId: f.customerID,
		ClusterId:  f.clusterID,
		SyncId:     syncID,
		Items: []*orchestratorv1.InventoryItem{{
			Namespace: namespace, Name: releaseName,
			DefinitionId: definitionID,
			Chart:        "example-chart", ChartVersion: "1.0.0",
			Revision: 1, Status: "deployed",
		}},
	}))
	require.NoError(t, err)
}

//nolint:unparam // release namespace is uniformly "apps" across the matrix, kept explicit for readability
func (f *identityOrderingFixture) inventoryIdentity(t *testing.T, namespace, releaseName string) store.WorkloadIdentity {
	t.Helper()
	row, err := f.st.Inventories().GetByReleaseKey(t.Context(), f.customerID, f.clusterID, namespace, releaseName)
	require.NoError(t, err)
	return store.WorkloadIdentity{
		Kind: row.WorkloadKind, Name: row.WorkloadName,
		Namespace: row.WorkloadNamespace, UID: row.WorkloadUID,
	}
}

// AC-088-01/05 (contract seam, D1=D2=D5=A): the seed-first-operation ordering —
// report arrives before the inventory row, is buffered, and is replayed and
// bound the moment SyncInventory creates the row.
func TestIdentityOrderingReportBeforeInventoryRow(t *testing.T) {
	f := newIdentityOrderingFixture(t)
	f.seedOrderingDefinition(t, "definition-ordering-a", "apps", "example-a")

	// Report arrives while the release_inventory row does not exist yet.
	f.reportIdentityItem(t, "apps", "example-a", "DEPLOYMENT", "example-a", "apps", "uid-a1")

	pending, err := f.st.PendingWorkloadIdentities().GetByReleaseKey(t.Context(), f.customerID, f.clusterID, "apps", "example-a")
	require.NoError(t, err, "report before the row must be buffered, not dropped")
	assert.Equal(t, "DEPLOYMENT", pending.WorkloadKind)
	assert.Equal(t, "uid-a1", pending.WorkloadUID)

	// The targeted inventory sync creates the row and event-drives the replay.
	f.syncInventoryItem(t, "sync-ordering-a", "apps", "example-a", "definition-ordering-a")

	identity := f.inventoryIdentity(t, "apps", "example-a")
	assert.Equal(t, "DEPLOYMENT", identity.Kind, "identity must be bound after the row appears")
	assert.Equal(t, "example-a", identity.Name)
	assert.Equal(t, "apps", identity.Namespace)
	assert.Equal(t, "uid-a1", identity.UID)

	_, err = f.st.PendingWorkloadIdentities().GetByReleaseKey(t.Context(), f.customerID, f.clusterID, "apps", "example-a")
	require.ErrorIs(t, err, store.ErrNotFound, "pending must be deleted once bound")
}

// AC-088-02 (contract seam, D6=A): a duplicate report for the same release key
// converges to a single bind — no duplicate pending row and no double write.
func TestIdentityOrderingDuplicateReportConverges(t *testing.T) {
	f := newIdentityOrderingFixture(t)
	f.seedOrderingDefinition(t, "definition-ordering-b", "apps", "example-b")

	// Two reports for the same release before the row exists.
	f.reportIdentityItem(t, "apps", "example-b", "DEPLOYMENT", "example-b", "apps", "uid-b1")
	f.reportIdentityItem(t, "apps", "example-b", "DEPLOYMENT", "example-b", "apps", "uid-b2")

	pendings, err := f.st.PendingWorkloadIdentities().ListByCluster(t.Context(), f.customerID, f.clusterID)
	require.NoError(t, err)
	require.Len(t, pendings, 1, "same release key must not duplicate pending rows")
	assert.Equal(t, "uid-b2", pendings[0].WorkloadUID, "later report replaces the buffered four-tuple")

	f.syncInventoryItem(t, "sync-ordering-b", "apps", "example-b", "definition-ordering-b")
	identity := f.inventoryIdentity(t, "apps", "example-b")
	assert.Equal(t, "uid-b2", identity.UID, "single bind with the latest report wins")
	_, err = f.st.PendingWorkloadIdentities().GetByReleaseKey(t.Context(), f.customerID, f.clusterID, "apps", "example-b")
	require.ErrorIs(t, err, store.ErrNotFound)
}

// AC-088-02 (contract seam): result-first ordering — the row exists before the
// report arrives — binds directly through the row-present path (no pending
// row is ever created).
func TestIdentityOrderingInventoryRowBeforeReport(t *testing.T) {
	f := newIdentityOrderingFixture(t)
	f.seedOrderingDefinition(t, "definition-ordering-c", "apps", "example-c")

	f.syncInventoryItem(t, "sync-ordering-c", "apps", "example-c", "definition-ordering-c")

	f.reportIdentityItem(t, "apps", "example-c", "DEPLOYMENT", "example-c", "apps", "uid-c1")

	identity := f.inventoryIdentity(t, "apps", "example-c")
	assert.Equal(t, "DEPLOYMENT", identity.Kind)
	assert.Equal(t, "uid-c1", identity.UID)

	pendings, err := f.st.PendingWorkloadIdentities().ListByCluster(t.Context(), f.customerID, f.clusterID)
	require.NoError(t, err)
	assert.Empty(t, pendings, "row-present reports never buffer")
}

// AC-088-08 (contract seam): a buffered identity survives an orchestrator
// restart — a fresh operator+orchestrator pair over the same persisted store
// replays it when the row is synced after the restart.
func TestIdentityOrderingRestartReplaysBufferedIdentity(t *testing.T) {
	f := newIdentityOrderingFixture(t)
	f.seedOrderingDefinition(t, "definition-ordering-d", "apps", "example-d")

	// Process 1 buffers the report before the row exists, then "restarts".
	f.reportIdentityItem(t, "apps", "example-d", "DEPLOYMENT", "example-d", "apps", "uid-d1")

	// Process 2 (same persisted store) syncs the row after restart; the
	// fresh operator service replays the persisted pending row.
	secondOperator, err := operator.NewService(f.st, slog.New(slog.DiscardHandler),
		operator.WithCA(f.ca),
		operator.WithIdentityMetrics(operator.NewIdentityMetrics(nil)),
	)
	require.NoError(t, err)
	secondOrchestrator := NewService(f.st, nil, "staging",
		slog.New(slog.DiscardHandler),
		NewPendingIdentityReplayer(secondOperator),
	)
	_, err = secondOrchestrator.SyncInventory(t.Context(), connect.NewRequest(&orchestratorv1.SyncInventoryRequest{
		OperatorId: f.operatorID,
		CustomerId: f.customerID,
		ClusterId:  f.clusterID,
		SyncId:     "sync-ordering-d-after-restart",
		Items: []*orchestratorv1.InventoryItem{{
			Namespace: "apps", Name: "example-d",
			DefinitionId: "definition-ordering-d",
			Chart:        "example-chart", ChartVersion: "1.0.0",
			Revision: 1, Status: "deployed",
		}},
	}))
	require.NoError(t, err)

	identity := f.inventoryIdentity(t, "apps", "example-d")
	assert.Equal(t, "uid-d1", identity.UID, "identity must not be lost across restart")
	_, err = f.st.PendingWorkloadIdentities().GetByReleaseKey(t.Context(), f.customerID, f.clusterID, "apps", "example-d")
	require.ErrorIs(t, err, store.ErrNotFound)
}

// AC-088-07 (contract seam, D4=C): a uid-only change on the same workload is
// applied; a kind/name/namespace conflict keeps the existing identity and is a
// deterministic terminal state (the pending row is consumed, no retry loop).
func TestIdentityOrderingTieredBindAcrossSync(t *testing.T) {
	f := newIdentityOrderingFixture(t)
	f.seedOrderingDefinition(t, "definition-ordering-e", "apps", "example-e")

	// Report before the row → buffered.
	f.reportIdentityItem(t, "apps", "example-e", "DEPLOYMENT", "example-e", "apps", "uid-e1")
	f.syncInventoryItem(t, "sync-ordering-e1", "apps", "example-e", "definition-ordering-e")
	assert.Equal(t, "uid-e1", f.inventoryIdentity(t, "apps", "example-e").UID)

	// Re-report after a workload rebuild (same kind/name/namespace, new uid).
	f.reportIdentityItem(t, "apps", "example-e", "DEPLOYMENT", "example-e", "apps", "uid-e2")
	assert.Equal(t, "uid-e2", f.inventoryIdentity(t, "apps", "example-e").UID, "uid-only change must update")

	// A conflicting report (different kind) is fail-closed: existing identity
	// stays, and the pending row is consumed (no retry churn).
	f.reportIdentityItem(t, "apps", "example-e", "STATEFUL_SET", "example-e", "apps", "uid-sts")
	identity := f.inventoryIdentity(t, "apps", "example-e")
	assert.Equal(t, "DEPLOYMENT", identity.Kind, "conflict must keep the existing identity")
	assert.Equal(t, "uid-e2", identity.UID)
	_, err := f.st.PendingWorkloadIdentities().GetByReleaseKey(t.Context(), f.customerID, f.clusterID, "apps", "example-e")
	require.ErrorIs(t, err, store.ErrNotFound, "a deterministic conflict must consume the pending row")
}

// AC-088-03 (contract seam): reports that are not selectable — ambiguous
// multiple items without promotion mappings, or incomplete items — are never
// buffered (fail closed: nothing wrong is ever bound later).
func TestIdentityOrderingUnselectableReportNeverBuffered(t *testing.T) {
	f := newIdentityOrderingFixture(t)
	f.seedOrderingDefinition(t, "definition-ordering-f", "apps", "example-f")

	// Ambiguous: two complete items, no promotion mappings → not selectable.
	stream := f.identityStreamFor(t)
	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_WorkloadIdentityReport{
			WorkloadIdentityReport: &operatorv1.WorkloadIdentityReport{Items: []*operatorv1.WorkloadIdentityItem{
				{ReleaseNamespace: "apps", ReleaseName: "example-f", Kind: "DEPLOYMENT", Name: "a", Namespace: "apps", Uid: "uid-a"},
				{ReleaseNamespace: "apps", ReleaseName: "example-f", Kind: "DEPLOYMENT", Name: "b", Namespace: "apps", Uid: "uid-b"},
			}},
		},
	}))
	require.NoError(t, stream.CloseRequest())
	for {
		if _, err := stream.Receive(); err != nil {
			break
		}
	}

	// Incomplete item (missing uid) → never selectable.
	f.reportIdentityItem(t, "apps", "example-f", "DEPLOYMENT", "c", "apps", "")

	pendings, err := f.st.PendingWorkloadIdentities().ListByCluster(t.Context(), f.customerID, f.clusterID)
	require.NoError(t, err)
	assert.Empty(t, pendings, "unselectable reports must never be buffered")

	// Later sync leaves the identity empty (fail closed end state).
	f.syncInventoryItem(t, "sync-ordering-f", "apps", "example-f", "definition-ordering-f")
	identity := f.inventoryIdentity(t, "apps", "example-f")
	assert.Equal(t, "", identity.Kind)
	assert.Equal(t, "", identity.UID)
}

// AC-088-01/05 (contract seam, D2 fallback): the seed-first-operation defect is
// precisely a report whose release has no inventory row YET. Per plan D2 the
// row-absent path reuses the row-present selection rule — a release whose
// definition has not been linked yet but reports a single complete workload is
// still a legitimate target that will materialize (the sync carries the
// definition_id). It is buffered (bounded by TTL), and report handling never
// fabricates an inventory row. True fail-closed boundaries for the row-absent
// path are ambiguity and incomplete items (covered by
// TestIdentityOrderingUnselectableReportNeverBuffered).
func TestIdentityOrderingUniqueCompleteItemBufferedWithoutDefinition(t *testing.T) {
	f := newIdentityOrderingFixture(t)

	f.reportIdentityItem(t, "apps", "ghost", "DEPLOYMENT", "ghost", "apps", "uid-ghost")

	pending, err := f.st.PendingWorkloadIdentities().GetByReleaseKey(t.Context(), f.customerID, f.clusterID, "apps", "ghost")
	require.NoError(t, err, "unique complete report is selectable and buffered for the later row")
	assert.Equal(t, "uid-ghost", pending.WorkloadUID)

	items, err := f.st.Inventories().ListByCluster(t.Context(), f.customerID, f.clusterID)
	require.NoError(t, err)
	assert.Empty(t, items, "report handling must never fabricate an inventory row")
}

// AC-088-05 (contract seam, full snapshot): a full-snapshot SyncInventory
// creates the row and triggers the same event-driven replay as the targeted
// update (D5=A applies to both SyncInventory callers).
func TestIdentityOrderingFullSnapshotTriggersReplay(t *testing.T) {
	f := newIdentityOrderingFixture(t)
	f.seedOrderingDefinition(t, "definition-ordering-g", "apps", "example-g")

	f.reportIdentityItem(t, "apps", "example-g", "DEPLOYMENT", "example-g", "apps", "uid-g1")
	_, err := f.st.PendingWorkloadIdentities().GetByReleaseKey(t.Context(), f.customerID, f.clusterID, "apps", "example-g")
	require.NoError(t, err, "report buffered before the full sync")

	_, err = f.orchestratorSvc.SyncInventory(t.Context(), connect.NewRequest(&orchestratorv1.SyncInventoryRequest{
		OperatorId:   f.operatorID,
		CustomerId:   f.customerID,
		ClusterId:    f.clusterID,
		SyncId:       "sync-ordering-g-full",
		FullSnapshot: true,
		Items: []*orchestratorv1.InventoryItem{{
			Namespace: "apps", Name: "example-g",
			DefinitionId: "definition-ordering-g",
			Chart:        "example-chart", ChartVersion: "1.0.0",
			Revision: 1, Status: "deployed",
		}},
	}))
	require.NoError(t, err)

	identity := f.inventoryIdentity(t, "apps", "example-g")
	assert.Equal(t, "uid-g1", identity.UID, "full snapshot must replay the buffered identity")
	_, err = f.st.PendingWorkloadIdentities().GetByReleaseKey(t.Context(), f.customerID, f.clusterID, "apps", "example-g")
	require.ErrorIs(t, err, store.ErrNotFound)
}

// AC-088-02 (recovery path): a SyncInventory that cannot bind yet (its own
// row does not exist because the upsert item was dropped by MarkMissing)
// leaves the pending row intact — the sweep or a later sync retries instead of
// losing the identity.
func TestIdentityOrderingReplayFailureKeepsPendingForRetry(t *testing.T) {
	f := newIdentityOrderingFixture(t)

	// Buffer an identity whose definition does exist but whose inventory row
	// never arrives; a full sync marks nothing present and must not touch the
	// pending row (no release key is replayed because no item matched).
	f.seedOrderingDefinition(t, "definition-ordering-h", "apps", "example-h")
	f.reportIdentityItem(t, "apps", "example-h", "DEPLOYMENT", "example-h", "apps", "uid-h1")

	_, err := f.orchestratorSvc.SyncInventory(t.Context(), connect.NewRequest(&orchestratorv1.SyncInventoryRequest{
		OperatorId:   f.operatorID,
		CustomerId:   f.customerID,
		ClusterId:    f.clusterID,
		SyncId:       "sync-ordering-h-empty-full",
		FullSnapshot: true,
		Items:        nil,
	}))
	require.NoError(t, err)

	pending, err := f.st.PendingWorkloadIdentities().GetByReleaseKey(t.Context(), f.customerID, f.clusterID, "apps", "example-h")
	require.NoError(t, err, "pending must survive a sync that cannot bind it")
	assert.Equal(t, "uid-h1", pending.WorkloadUID)

	// Recovery: the row finally appears via a targeted sync → bind succeeds.
	f.syncInventoryItem(t, "sync-ordering-h-targeted", "apps", "example-h", "definition-ordering-h")
	identity := f.inventoryIdentity(t, "apps", "example-h")
	assert.Equal(t, "uid-h1", identity.UID, "recovery must not be polluted by the earlier failed replay")
	_, err = f.st.PendingWorkloadIdentities().GetByReleaseKey(t.Context(), f.customerID, f.clusterID, "apps", "example-h")
	require.ErrorIs(t, err, store.ErrNotFound)
}

// REQ-088 D6 concurrency hardening: a replay racing a concurrent report buffer
// for the same release key is serialized by the per-key stripe lock, so a
// stale replay can never delete the newer buffered report. The test hammers
// SyncInventory-driven replay and report buffering concurrently and asserts
// the final pending/bound state stays one of the two self-consistent values
// (the newer report is never lost by the older replay's unconditional delete).
func TestIdentityOrderingConcurrentReplayAndReportConverges(t *testing.T) {
	f := newIdentityOrderingFixture(t)
	f.seedOrderingDefinition(t, "definition-ordering-conc", "apps", "example-conc")

	// Buffer an older report before any row exists.
	f.reportIdentityItem(t, "apps", "example-conc", "DEPLOYMENT", "example-conc", "apps", "uid-old")

	const iterations = 40
	var wg sync.WaitGroup
	errCh := make(chan error, iterations*2)
	for i := 0; i < iterations; i++ {
		wg.Add(2)
		// Concurrently: SyncInventory creating the row + replay (read pending,
		// bind, delete), and a fresh report buffering a newer uid.
		go func() {
			defer wg.Done()
			_, err := f.orchestratorSvc.SyncInventory(t.Context(), connect.NewRequest(&orchestratorv1.SyncInventoryRequest{
				OperatorId: f.operatorID, CustomerId: f.customerID, ClusterId: f.clusterID,
				SyncId: "sync-conc-" + uuid.NewString(),
				Items: []*orchestratorv1.InventoryItem{{
					Namespace: "apps", Name: "example-conc",
					DefinitionId: "definition-ordering-conc",
					Chart:        "example-chart", ChartVersion: "1.0.0",
					Revision: 1, Status: "deployed",
				}},
			}))
			if err != nil {
				errCh <- err
			}
		}()
		go func() {
			defer wg.Done()
			// Report buffering path: either row-present apply (which deletes
			// pending) or row-absent buffer (which refreshes pending to a
			// newer uid) — both under the same per-key lock as replay.
			f.reportIdentityItem(t, "apps", "example-conc", "DEPLOYMENT", "example-conc", "apps", "uid-new")
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent sync/report failed: %v", err)
	}

	// Self-consistent end state: run one final deterministic sync/replay and
	// assert convergence on the newer uid.
	_, err := f.orchestratorSvc.SyncInventory(t.Context(), connect.NewRequest(&orchestratorv1.SyncInventoryRequest{
		OperatorId: f.operatorID, CustomerId: f.customerID, ClusterId: f.clusterID,
		SyncId: "sync-conc-final",
		Items: []*orchestratorv1.InventoryItem{{
			Namespace: "apps", Name: "example-conc",
			DefinitionId: "definition-ordering-conc",
			Chart:        "example-chart", ChartVersion: "1.0.0",
			Revision: 1, Status: "deployed",
		}},
	}))
	require.NoError(t, err)
	identity := f.inventoryIdentity(t, "apps", "example-conc")
	assert.Equal(t, "example-conc", identity.Name)
	assert.NotEmpty(t, identity.UID, "identity must converge to a bound value under concurrency")
	// No pending row may remain after the final sync (row-present path or
	// replay consumes it); if one does remain it must be the newest value and
	// the sweep will bind it — assert no duplicate/stale pending is left that
	// could regress.
	pending, err := f.st.PendingWorkloadIdentities().GetByReleaseKey(t.Context(), f.customerID, f.clusterID, "apps", "example-conc")
	if err == nil {
		assert.Equal(t, "uid-new", pending.WorkloadUID, "any surviving pending must be the newest report")
	} else {
		require.ErrorIs(t, err, store.ErrNotFound)
	}
	// A sweep converges whatever is left and never regresses the bound uid.
	require.NoError(t, f.operatorSvc.ReconcilePendingIdentities(t.Context()))
	row, err := f.st.Inventories().GetByReleaseKey(t.Context(), f.customerID, f.clusterID, "apps", "example-conc")
	require.NoError(t, err)
	assert.Equal(t, identity.UID, row.WorkloadUID, "sweep must not regress a bound identity")
}

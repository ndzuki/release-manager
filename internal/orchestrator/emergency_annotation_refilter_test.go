package orchestrator

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/operator/ca"
	"github.com/ndzuki/release-manager/internal/store"
)

// TestListEmergencyTargetsDropsAnnotationRemovedFromDefinition is the TASK-247
// end-to-end regression: an operator whose persisted annotation whitelist is
// stale still reports a key the definition has since removed (no later release
// write refreshed it), and the emergency read model must never show it. The
// operator report goes through the real CommandStream ingest path, the center
// re-filters against the current definition, and ListEmergencyTargets reads the
// washed row: the still-approved key survives, the removed key is gone.
// Removing the re-filter from applyWorkloadObservation makes this test fail.
func TestListEmergencyTargetsDropsAnnotationRemovedFromDefinition(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	// The definition approves `team` only; `legacy` is the key the center
	// removed while the stale operator keeps reporting it.
	definition, err := st.Definitions().Get(t.Context(), "def-001")
	require.NoError(t, err)
	definition.ApprovedAnnotationKeys = []store.ApprovedAnnotationKey{
		{Key: "team", Scope: "WORKLOAD_METADATA"},
	}
	_, err = st.Definitions().Update(t.Context(), definition, nil)
	require.NoError(t, err)

	// The inventory row the ingest binds to. Production creates it through
	// SyncInventory; the direct upsert keeps this test focused on the annotation
	// data plane, mirroring seedObservationDefinition in the operator tests.
	require.NoError(t, st.Inventories().Upsert(t.Context(), &store.ReleaseInventory{
		ReleaseDefinitionID: "def-001", CustomerID: "cust-001", ClusterID: "cls-001",
		Namespace: "default", ReleaseName: "my-release", Status: "deployed", InventoryStatus: store.InventoryActive,
	}))

	// The reporting operator identity the CommandStream Hello resolves; its
	// customer/cluster must match the definition.
	now := time.Now()
	require.NoError(t, st.Operators().Create(t.Context(), &store.Operator{
		ID: "op-247", CustomerID: "cust-001", ClusterID: "cls-001",
		CertSerial: "cert-247", Status: store.OperatorActive, RegisteredAt: now, UpdatedAt: now,
	}))
	require.NoError(t, st.Sessions().Establish(t.Context(), &store.Session{
		ID: "sess-247", OperatorID: "op-247", Status: store.SessionOnline,
		StartedAt: now, LastHeartbeat: now, ExpiresAt: now.Add(time.Hour),
	}))
	authority, err := ca.New(ca.Config{TTL: time.Hour})
	require.NoError(t, err)
	operatorSvc, err := operator.NewService(st, slog.New(slog.DiscardHandler), operator.WithCA(authority))
	require.NoError(t, err)

	path, handler := operatorv1connect.NewOperatorServiceHandler(operatorSvc)
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
			Hello: &operatorv1.Hello{SessionId: "sess-247", OperatorId: "op-247", LastSeenSequence: 1},
		},
	}))
	established, err := stream.Receive()
	require.NoError(t, err)
	require.NotNil(t, established.GetSessionEstablished())

	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_WorkloadIdentityReport{
			WorkloadIdentityReport: &operatorv1.WorkloadIdentityReport{Items: []*operatorv1.WorkloadIdentityItem{{
				ReleaseNamespace: "default", ReleaseName: "my-release",
				Kind: "DEPLOYMENT", Name: "my-release", Namespace: "default", Uid: "uid-refilter",
				CurrentAnnotations: []*operatorv1.ScopedAnnotations{{
					Scope: "WORKLOAD_METADATA",
					Entries: []*operatorv1.AnnotationEntry{
						{Key: "team", Value: "platform"},
						{Key: "legacy", Value: "stale"},
					},
				}},
				ObservedAt: timestamppb.Now(),
			}}},
		},
	}))
	require.NoError(t, stream.CloseRequest())
	for {
		if _, err := stream.Receive(); err != nil {
			break
		}
	}

	resp, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
		ReleaseDefinitionId: "def-001",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTargets(), 1)
	annotations := resp.Msg.GetTargets()[0].GetCurrentAnnotations()
	assert.Equal(t, "platform", annotations["WORKLOAD_METADATA/team"], "the approved key must survive")
	assert.NotContains(t, annotations, "WORKLOAD_METADATA/legacy",
		"a key removed from the definition must not reach the emergency read model")
}

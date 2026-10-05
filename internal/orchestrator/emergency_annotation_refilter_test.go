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

// TestListEmergencyTargetsFiltersAnnotationRemovedWithoutNewIngest closes the
// window TASK-247's ingest filter alone leaves open: a key that was persisted
// while the definition still approved it stays on release_inventory until the
// next *ingest*, and the center does not send the release a new command when a
// key is removed from the definition. The operator therefore keeps reporting it
// from its stale local whitelist — or, in this test, no report ever arrives —
// and the read model must still not advertise it. The definition shrinks with
// no command and no new ingest; ListEmergencyTargets re-applies the current
// whitelist to the already-stored projection.
//
// Removing the read-side filter (the filterApprovedAnnotations call in
// ListEmergencyTargets) makes the `legacy`/empty-whitelist assertions fail.
func TestListEmergencyTargetsFiltersAnnotationRemovedWithoutNewIngest(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	ctx := t.Context()
	observedAt := time.Now().UTC().Truncate(time.Second)

	// The definition approves both keys when the observation is written, and the
	// projection reaches the row through the real store write path.
	seedApprovedAnnotationKeys(t, st,
		store.ApprovedAnnotationKey{Key: "team", Scope: "WORKLOAD_METADATA"},
		store.ApprovedAnnotationKey{Key: "legacy", Scope: "WORKLOAD_METADATA"},
	)
	require.NoError(t, st.Inventories().Upsert(ctx, &store.ReleaseInventory{
		ReleaseDefinitionID: "def-001", CustomerID: "cust-001", ClusterID: "cls-001",
		Namespace: "default", ReleaseName: "my-release", Status: "deployed", InventoryStatus: store.InventoryActive,
	}))
	require.NoError(t, st.Inventories().UpdateWorkloadObservation(ctx, "cust-001", "cls-001", "default", "my-release", store.WorkloadObservation{
		Containers: []string{"api"},
		ImageRefs:  map[string]string{"api": "registry.example/team/api:1.0.0"},
		Annotations: map[string]map[string]string{
			"WORKLOAD_METADATA": {"team": "platform", "legacy": "stale"},
		},
		ObservedAt: observedAt,
	}))

	// Precondition: with both keys still approved the row's projection is visible.
	pre := listTarget(t, svc)
	require.Equal(t, map[string]string{
		"WORKLOAD_METADATA/team":   "platform",
		"WORKLOAD_METADATA/legacy": "stale",
	}, pre.GetCurrentAnnotations(), "precondition: the stored row carries both keys")

	// The definition drops `legacy`; no release command is sent and no new ingest
	// happens, so the row still carries both keys. Only the read model can hide it.
	seedApprovedAnnotationKeys(t, st, store.ApprovedAnnotationKey{Key: "team", Scope: "WORKLOAD_METADATA"})

	target := listTarget(t, svc)
	assert.Equal(t, map[string]string{
		"WORKLOAD_METADATA/team": "platform",
	}, target.GetCurrentAnnotations(),
		"a definition-removed key must vanish from the read model without a new ingest")
	assert.NotContains(t, target.GetCurrentAnnotations(), "WORKLOAD_METADATA/legacy")
	assert.Contains(t, target.GetSupportedOperations(), orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_APPROVED_ANNOTATION)

	// Variant: the definition shrinks to an empty whitelist. Every stored key must
	// disappear and the annotation action must no longer be offered.
	seedApprovedAnnotationKeys(t, st)
	target = listTarget(t, svc)
	assert.Empty(t, target.GetCurrentAnnotations(), "an empty whitelist must hide every stored key")
	assert.NotContains(t, target.GetSupportedOperations(), orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_APPROVED_ANNOTATION)

	// The filter only removes visible keys: freshness still comes from
	// observed_at, so an annotation-independent operation stays available.
	assert.Contains(t, target.GetSupportedOperations(), orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_CONTAINER_IMAGE,
		"filtering annotations must not change the freshness semantics")
}

// TestFilterApprovedAnnotations pins the read-side matching semantics to the
// ingest-side filter (TASK-247): exact (scope, key) after trimming, unknown
// scopes and unlisted keys dropped, and an all-unapproved projection collapsed
// to nil while freshness is left to the caller.
func TestFilterApprovedAnnotations(t *testing.T) {
	workloadTeam := store.ApprovedAnnotationKey{Key: "team", Scope: "WORKLOAD_METADATA"}
	podScrape := store.ApprovedAnnotationKey{Key: "prometheus.io/scrape", Scope: "POD_TEMPLATE_METADATA"}
	definition := func(keys ...store.ApprovedAnnotationKey) *store.ReleaseDefinition {
		return &store.ReleaseDefinition{ApprovedAnnotationKeys: keys}
	}

	tests := []struct {
		name       string
		definition *store.ReleaseDefinition
		reported   map[string]map[string]string
		want       map[string]map[string]string
	}{
		{
			name:       "approved subset survives, removed key is dropped",
			definition: definition(workloadTeam, podScrape),
			reported: map[string]map[string]string{
				"WORKLOAD_METADATA": {"team": "platform", "removed": "stale"},
			},
			want: map[string]map[string]string{"WORKLOAD_METADATA": {"team": "platform"}},
		},
		{
			name:       "unknown scope is dropped even for an approved key",
			definition: definition(workloadTeam),
			reported: map[string]map[string]string{
				"WORKLOAD_METADATA": {"team": "platform"},
				"UNKNOWN_SCOPE":     {"team": "wrong-scope"},
			},
			want: map[string]map[string]string{"WORKLOAD_METADATA": {"team": "platform"}},
		},
		{
			name:       "all reported keys unapproved collapses to nil",
			definition: definition(workloadTeam),
			reported: map[string]map[string]string{
				"WORKLOAD_METADATA": {"legacy": "stale"},
			},
			want: nil,
		},
		{
			name:       "empty whitelist approves nothing",
			definition: definition(),
			reported: map[string]map[string]string{
				"WORKLOAD_METADATA": {"team": "platform"},
			},
			want: nil,
		},
		{
			name:       "nil definition approves nothing (fail closed)",
			definition: nil,
			reported: map[string]map[string]string{
				"WORKLOAD_METADATA": {"team": "platform"},
			},
			want: nil,
		},
		{
			name:       "nil projection stays nil",
			definition: definition(workloadTeam),
			reported:   nil,
			want:       nil,
		},
		{
			name:       "matching trims scope and key on both sides",
			definition: definition(store.ApprovedAnnotationKey{Key: " team ", Scope: " WORKLOAD_METADATA "}),
			reported: map[string]map[string]string{
				" WORKLOAD_METADATA ": {" team ": "platform"},
			},
			want: map[string]map[string]string{"WORKLOAD_METADATA": {"team": "platform"}},
		},
		{
			name:       "blank approved entries never match",
			definition: definition(store.ApprovedAnnotationKey{Key: " ", Scope: "WORKLOAD_METADATA"}, store.ApprovedAnnotationKey{Key: "team", Scope: " "}),
			reported: map[string]map[string]string{
				"WORKLOAD_METADATA": {"team": "platform"},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, filterApprovedAnnotations(tt.definition, tt.reported))
		})
	}
}

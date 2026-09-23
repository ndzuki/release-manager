package operator_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/store"
)

// AC-085-04: the manifest→enum kind normalization is the operator-side
// contract for identity reports (REQ-085): the three emergency whitelist
// kinds map to their enum spellings, everything else (incl. Job) is out of
// scope.
func TestNormalizeWorkloadKind(t *testing.T) {
	tests := []struct {
		name string
		kind string
		want string
		ok   bool
	}{
		{name: "deployment", kind: "Deployment", want: "DEPLOYMENT", ok: true},
		{name: "statefulset", kind: "StatefulSet", want: "STATEFUL_SET", ok: true},
		{name: "daemonset", kind: "DaemonSet", want: "DAEMON_SET", ok: true},
		{name: "job excluded", kind: "Job", want: "", ok: false},
		{name: "unknown excluded", kind: "CronJob", want: "", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := operator.NormalizeWorkloadKind(tt.kind)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// AC-085-01 (operator read boundary): WorkloadUID returns the live object
// UID for the typed kind; unwhitelisted kinds and missing objects fail
// closed with an error (identity never fabricated).
func TestWorkloadUID(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps", UID: types.UID("uid-live-1")},
	})

	t.Run("deployment uid", func(t *testing.T) {
		uid, err := operator.WorkloadUID(t.Context(), client, "DEPLOYMENT", "apps", "api")
		require.NoError(t, err)
		assert.Equal(t, "uid-live-1", uid)
	})
	t.Run("missing object", func(t *testing.T) {
		_, err := operator.WorkloadUID(t.Context(), client, "DEPLOYMENT", "apps", "ghost")
		require.Error(t, err)
	})
	t.Run("unwhitelisted kind", func(t *testing.T) {
		_, err := operator.WorkloadUID(t.Context(), client, "JOB", "apps", "api")
		require.Error(t, err)
	})
	t.Run("nil client", func(t *testing.T) {
		_, err := operator.WorkloadUID(context.Background(), nil, "DEPLOYMENT", "apps", "api")
		require.Error(t, err)
	})
}

// ── REQ-088 (TASK-088): D4=C grading and selection ──

// AC-088-03/07 (pure seam): ResolveWorkloadIdentity grades a reported
// identity against the current row identity — bind on empty, no-op on equal,
// uid-only update on the same workload, conflict (fail closed) otherwise.
func TestResolveWorkloadIdentity(t *testing.T) {
	empty := store.WorkloadIdentity{}
	bound := store.WorkloadIdentity{Kind: "DEPLOYMENT", Name: "example", Namespace: "apps", UID: "uid-1"}
	sameWorkloadNewUID := store.WorkloadIdentity{Kind: "DEPLOYMENT", Name: "example", Namespace: "apps", UID: "uid-2"}
	differentName := store.WorkloadIdentity{Kind: "DEPLOYMENT", Name: "other", Namespace: "apps", UID: "uid-3"}
	differentKind := store.WorkloadIdentity{Kind: "STATEFUL_SET", Name: "example", Namespace: "apps", UID: "uid-4"}
	differentNamespace := store.WorkloadIdentity{Kind: "DEPLOYMENT", Name: "example", Namespace: "prod", UID: "uid-5"}
	incomplete := store.WorkloadIdentity{Kind: "DEPLOYMENT", Name: "example", Namespace: "apps"} // no uid

	tests := []struct {
		name    string
		current store.WorkloadIdentity
		report  store.WorkloadIdentity
		want    operator.WorkloadIdentityResolution
	}{
		{name: "empty row binds full report", current: empty, report: bound, want: operator.WorkloadIdentityBind},
		{name: "identical no-op", current: bound, report: bound, want: operator.WorkloadIdentityNoop},
		{name: "same workload new uid updates uid", current: bound, report: sameWorkloadNewUID, want: operator.WorkloadIdentityUpdateUID},
		{name: "name mismatch conflicts", current: bound, report: differentName, want: operator.WorkloadIdentityConflict},
		{name: "kind mismatch conflicts", current: bound, report: differentKind, want: operator.WorkloadIdentityConflict},
		{name: "namespace mismatch conflicts", current: bound, report: differentNamespace, want: operator.WorkloadIdentityConflict},
		{name: "incomplete report on empty row conflicts", current: empty, report: incomplete, want: operator.WorkloadIdentityConflict},
		{name: "incomplete report on bound row conflicts", current: bound, report: incomplete, want: operator.WorkloadIdentityConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, operator.ResolveWorkloadIdentity(tt.current, tt.report))
		})
	}
}

// TASK-168 W2 (operator projection): WorkloadObservation reads the live object
// once and returns the UID plus the observed fields, reusing the same
// kind→resource dispatch as WorkloadUID (D-110 ①). A DaemonSet has no replica
// count, so the pointer stays nil ("not observed") rather than becoming a
// fabricated 0; a Deployment explicitly scaled to 0 keeps a non-nil 0.
func TestWorkloadObservation(t *testing.T) {
	three := int32(3)
	zero := int32(0)
	client := kubernetesfake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps", UID: "uid-deploy"},
			Spec: appsv1.DeploymentSpec{
				Replicas: &three,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
					{Name: "api", Image: "registry.example/team/api:1.0.0"},
					{Name: "sidecar", Image: "registry.example/team/sidecar:2.0.0"},
				}}},
			},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "scaled", Namespace: "apps", UID: "uid-scaled"},
			Spec: appsv1.DeploymentSpec{
				Replicas: &zero,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
					{Name: "api", Image: "registry.example/team/api:1.0.0"},
				}}},
			},
		},
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "apps", UID: "uid-stateful"},
			Spec: appsv1.StatefulSetSpec{
				Replicas: &three,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
					{Name: "postgres", Image: "registry.example/team/postgres:16"},
				}}},
			},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "node-agent", Namespace: "apps", UID: "uid-daemon"},
			Spec: appsv1.DaemonSetSpec{
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
					{Name: "agent", Image: "registry.example/team/agent:3.0.0"},
				}}},
			},
		},
	)

	t.Run("deployment projects containers images and replicas", func(t *testing.T) {
		uid, observation, err := operator.WorkloadObservation(t.Context(), client, "DEPLOYMENT", "apps", "api")
		require.NoError(t, err)
		assert.Equal(t, "uid-deploy", uid)
		assert.Equal(t, []string{"api", "sidecar"}, observation.Containers)
		assert.Equal(t, map[string]string{
			"api":     "registry.example/team/api:1.0.0",
			"sidecar": "registry.example/team/sidecar:2.0.0",
		}, observation.ImageRefs)
		require.NotNil(t, observation.Replicas)
		assert.Equal(t, int32(3), *observation.Replicas)
		assert.False(t, observation.ObservedAt.IsZero(), "the read must be timestamped")
	})
	t.Run("real zero replicas stays a non-nil zero", func(t *testing.T) {
		_, observation, err := operator.WorkloadObservation(t.Context(), client, "DEPLOYMENT", "apps", "scaled")
		require.NoError(t, err)
		require.NotNil(t, observation.Replicas, "a real 0 is not 'not observed'")
		assert.Equal(t, int32(0), *observation.Replicas)
	})
	t.Run("statefulset projects replicas", func(t *testing.T) {
		_, observation, err := operator.WorkloadObservation(t.Context(), client, "STATEFUL_SET", "apps", "db")
		require.NoError(t, err)
		assert.Equal(t, []string{"postgres"}, observation.Containers)
		require.NotNil(t, observation.Replicas)
		assert.Equal(t, int32(3), *observation.Replicas)
	})
	t.Run("daemonset leaves replicas absent", func(t *testing.T) {
		_, observation, err := operator.WorkloadObservation(t.Context(), client, "DAEMON_SET", "apps", "node-agent")
		require.NoError(t, err)
		assert.Equal(t, []string{"agent"}, observation.Containers)
		assert.Nil(t, observation.Replicas, "a DaemonSet has no replica count: absent, never 0")
	})
	t.Run("missing object fails closed", func(t *testing.T) {
		_, _, err := operator.WorkloadObservation(t.Context(), client, "DEPLOYMENT", "apps", "ghost")
		require.Error(t, err)
	})
	t.Run("nil client fails closed", func(t *testing.T) {
		_, _, err := operator.WorkloadObservation(context.Background(), nil, "DEPLOYMENT", "apps", "api")
		require.Error(t, err)
	})
}

// TASK-168 W2 (central persistence): the observed field projection must reach
// the release_inventory row through UpdateWorkloadObservation — without this
// the observed_* columns stay NULL and the emergency read model keeps answering
// "current fields not observed" forever. A real observed 0 must survive as a
// non-nil zero, and a report without observed_at must never clobber an existing
// observation with the zero value.
func TestCommandStreamWorkloadIdentityReportPersistsObservation(t *testing.T) {
	st := newTestSvc(t)
	ctx := t.Context()
	seedObservationDefinition(t, st, "definition-observation", "example")

	observedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	three := int32(3)
	sendIdentityReport(t, st, []*operatorv1.WorkloadIdentityItem{{
		ReleaseNamespace: "apps", ReleaseName: "example",
		Kind: "DEPLOYMENT", Name: "example", Namespace: "apps", Uid: "uid-observation",
		Containers:       []string{"api", "sidecar"},
		CurrentImageRefs: map[string]string{"api": "registry.example/team/api:1.0.0", "sidecar": "registry.example/team/sidecar:2.0.0"},
		CurrentReplicas:  &three,
		ObservedAt:       timestamppb.New(observedAt),
	}})

	row, err := st.Inventories().GetByReleaseKey(ctx, "cust-1", "clus-1", "apps", "example")
	require.NoError(t, err)
	assert.Equal(t, "uid-observation", row.WorkloadUID, "identity and observation come from the same item")
	assert.Equal(t, []string{"api", "sidecar"}, row.ObservedContainers)
	assert.Equal(t, map[string]string{
		"api":     "registry.example/team/api:1.0.0",
		"sidecar": "registry.example/team/sidecar:2.0.0",
	}, row.ObservedImageRefs)
	require.NotNil(t, row.ObservedReplicas)
	assert.Equal(t, int32(3), *row.ObservedReplicas)
	assert.WithinDuration(t, observedAt, row.ObservedAt, time.Second)

	// A later report from an operator that carries the identity but no
	// observation (observed_at absent) must leave the persisted observation
	// untouched: overwriting it with the zero value would make the row look
	// stale for no reason.
	sendIdentityReport(t, st, []*operatorv1.WorkloadIdentityItem{{
		ReleaseNamespace: "apps", ReleaseName: "example",
		Kind: "DEPLOYMENT", Name: "example", Namespace: "apps", Uid: "uid-observation",
	}})
	row, err = st.Inventories().GetByReleaseKey(ctx, "cust-1", "clus-1", "apps", "example")
	require.NoError(t, err)
	assert.Equal(t, []string{"api", "sidecar"}, row.ObservedContainers, "an observation-less report must not clear a stored observation")
	require.NotNil(t, row.ObservedReplicas)
	assert.Equal(t, int32(3), *row.ObservedReplicas)
}

// TASK-168 W2: a Deployment observed at zero replicas reports a real zero, and
// the row must keep it as a non-nil pointer so the read model can tell
// "scaled to zero" from "never observed".
func TestCommandStreamWorkloadIdentityReportPersistsObservedZeroReplicas(t *testing.T) {
	st := newTestSvc(t)
	ctx := t.Context()
	seedObservationDefinition(t, st, "definition-zero", "example")

	zero := int32(0)
	sendIdentityReport(t, st, []*operatorv1.WorkloadIdentityItem{{
		ReleaseNamespace: "apps", ReleaseName: "example",
		Kind: "DEPLOYMENT", Name: "example", Namespace: "apps", Uid: "uid-zero",
		Containers:       []string{"api"},
		CurrentImageRefs: map[string]string{"api": "registry.example/team/api:1.0.0"},
		CurrentReplicas:  &zero,
		ObservedAt:       timestamppb.Now(),
	}})

	row, err := st.Inventories().GetByReleaseKey(ctx, "cust-1", "clus-1", "apps", "example")
	require.NoError(t, err)
	require.NotNil(t, row.ObservedReplicas, "observed 0 must be stored as a non-nil pointer")
	assert.Equal(t, int32(0), *row.ObservedReplicas)
}

// TASK-168 W2 (selection scoping): when the definition maps one workload, the
// persisted observation must come from the selected item — never from an
// unmapped workload the emergency target cannot address.
func TestCommandStreamWorkloadIdentityReportPersistsSelectedItemObservation(t *testing.T) {
	st := newTestSvc(t)
	ctx := t.Context()
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: "definition-observation-mapped", Name: "definition-observation-mapped",
		CustomerID: "cust-1", ClusterID: "clus-1", Namespace: "apps", ReleaseName: "example",
		Status: store.DefStatusActive,
	}, nil))
	definition, err := st.Definitions().Get(ctx, "definition-observation-mapped")
	require.NoError(t, err)
	definition.PromotionMappings = []store.PromotionMapping{{
		WorkloadKind: "DEPLOYMENT", WorkloadName: "api", Field: "replicas", ValuesPath: "replicaCount",
	}}
	_, err = st.Definitions().Update(ctx, definition, nil)
	require.NoError(t, err)
	require.NoError(t, st.Inventories().Upsert(ctx, &store.ReleaseInventory{
		ReleaseDefinitionID: "definition-observation-mapped", CustomerID: "cust-1", ClusterID: "clus-1",
		Namespace: "apps", ReleaseName: "example", Status: "deployed", InventoryStatus: store.InventoryActive,
	}))

	apiReplicas := int32(2)
	webReplicas := int32(9)
	sendIdentityReport(t, st, []*operatorv1.WorkloadIdentityItem{
		{
			ReleaseNamespace: "apps", ReleaseName: "example",
			Kind: "DEPLOYMENT", Name: "api", Namespace: "apps", Uid: "uid-api",
			Containers:       []string{"api"},
			CurrentImageRefs: map[string]string{"api": "registry.example/team/api:1.0.0"},
			CurrentReplicas:  &apiReplicas,
			ObservedAt:       timestamppb.Now(),
		},
		{
			ReleaseNamespace: "apps", ReleaseName: "example",
			Kind: "DEPLOYMENT", Name: "web", Namespace: "apps", Uid: "uid-web",
			Containers:       []string{"web"},
			CurrentImageRefs: map[string]string{"web": "registry.example/team/web:9.9.9"},
			CurrentReplicas:  &webReplicas,
			ObservedAt:       timestamppb.Now(),
		},
	})

	row, err := st.Inventories().GetByReleaseKey(ctx, "cust-1", "clus-1", "apps", "example")
	require.NoError(t, err)
	assert.Equal(t, "uid-api", row.WorkloadUID)
	assert.Equal(t, []string{"api"}, row.ObservedContainers, "observation must come from the selected item")
	assert.Equal(t, map[string]string{"api": "registry.example/team/api:1.0.0"}, row.ObservedImageRefs)
	require.NotNil(t, row.ObservedReplicas)
	assert.Equal(t, int32(2), *row.ObservedReplicas)
}

// seedObservationDefinition seeds the definition + inventory row pair the
// observation persistence tests need.
func seedObservationDefinition(t *testing.T, st store.Store, definitionID, releaseName string) {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: definitionID, Name: definitionID,
		CustomerID: "cust-1", ClusterID: "clus-1", Namespace: "apps", ReleaseName: releaseName,
		Status: store.DefStatusActive,
	}, nil))
	require.NoError(t, st.Inventories().Upsert(ctx, &store.ReleaseInventory{
		ReleaseDefinitionID: definitionID, CustomerID: "cust-1", ClusterID: "clus-1",
		Namespace: "apps", ReleaseName: releaseName, Status: "deployed", InventoryStatus: store.InventoryActive,
	}))
}

// sendIdentityReport drives one WorkloadIdentityReport through the real control
// stream and waits for the service to drain it.
func sendIdentityReport(t *testing.T, st store.Store, items []*operatorv1.WorkloadIdentityItem) {
	t.Helper()
	stream := openIdentityStream(t.Context(), t, st)
	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_WorkloadIdentityReport{
			WorkloadIdentityReport: &operatorv1.WorkloadIdentityReport{Items: items},
		},
	}))
	require.NoError(t, stream.CloseRequest())
	for {
		if _, err := stream.Receive(); err != nil {
			break
		}
	}
}

// AC-088-03 (pure seam): SelectWorkloadIdentity picks the authoritative item —
// promotion-mapped when the definition carries mappings, the unique complete
// item otherwise — and drops incomplete or ambiguous reports fail closed.
func TestSelectWorkloadIdentity(t *testing.T) {
	mappedDef := &store.ReleaseDefinition{PromotionMappings: []store.PromotionMapping{
		{WorkloadKind: "DEPLOYMENT", WorkloadName: "api", Field: "replicas", ValuesPath: "replicaCount"},
	}}
	item := func(kind, name, namespace, uid string) *operatorv1.WorkloadIdentityItem {
		return &operatorv1.WorkloadIdentityItem{
			ReleaseNamespace: "apps", ReleaseName: "example",
			Kind: kind, Name: name, Namespace: namespace, Uid: uid,
		}
	}
	apiItem := item("DEPLOYMENT", "api", "apps", "uid-api")
	webItem := item("DEPLOYMENT", "web", "apps", "uid-web")
	uniqueItem := item("DEPLOYMENT", "example", "apps", "uid-example")

	tests := []struct {
		name       string
		definition *store.ReleaseDefinition
		items      []*operatorv1.WorkloadIdentityItem
		want       store.WorkloadIdentity
		ok         bool
	}{
		{name: "mapping selects mapped item", definition: mappedDef, items: []*operatorv1.WorkloadIdentityItem{apiItem, webItem},
			want: store.WorkloadIdentity{Kind: "DEPLOYMENT", Name: "api", Namespace: "apps", UID: "uid-api"}, ok: true},
		{name: "mapping present but no item matches", definition: mappedDef, items: []*operatorv1.WorkloadIdentityItem{webItem},
			want: store.WorkloadIdentity{}, ok: false},
		{name: "no definition unique complete item", definition: nil, items: []*operatorv1.WorkloadIdentityItem{uniqueItem},
			want: store.WorkloadIdentity{Kind: "DEPLOYMENT", Name: "example", Namespace: "apps", UID: "uid-example"}, ok: true},
		{name: "no definition ambiguous multiple items", definition: nil, items: []*operatorv1.WorkloadIdentityItem{apiItem, webItem},
			want: store.WorkloadIdentity{}, ok: false},
		{name: "incomplete item never selectable", definition: nil, items: []*operatorv1.WorkloadIdentityItem{item("DEPLOYMENT", "example", "apps", "")},
			want: store.WorkloadIdentity{}, ok: false},
		{name: "no items", definition: nil, items: nil, want: store.WorkloadIdentity{}, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := operator.SelectWorkloadIdentity(tt.definition, tt.items)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

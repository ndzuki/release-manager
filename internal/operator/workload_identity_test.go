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
		uid, observation, err := operator.WorkloadObservation(t.Context(), client, "DEPLOYMENT", "apps", "api", nil)
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
		_, observation, err := operator.WorkloadObservation(t.Context(), client, "DEPLOYMENT", "apps", "scaled", nil)
		require.NoError(t, err)
		require.NotNil(t, observation.Replicas, "a real 0 is not 'not observed'")
		assert.Equal(t, int32(0), *observation.Replicas)
	})
	t.Run("statefulset projects replicas", func(t *testing.T) {
		_, observation, err := operator.WorkloadObservation(t.Context(), client, "STATEFUL_SET", "apps", "db", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"postgres"}, observation.Containers)
		require.NotNil(t, observation.Replicas)
		assert.Equal(t, int32(3), *observation.Replicas)
	})
	t.Run("daemonset leaves replicas absent", func(t *testing.T) {
		_, observation, err := operator.WorkloadObservation(t.Context(), client, "DAEMON_SET", "apps", "node-agent", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"agent"}, observation.Containers)
		assert.Nil(t, observation.Replicas, "a DaemonSet has no replica count: absent, never 0")
	})
	t.Run("missing object fails closed", func(t *testing.T) {
		_, _, err := operator.WorkloadObservation(t.Context(), client, "DEPLOYMENT", "apps", "ghost", nil)
		require.Error(t, err)
	})
	t.Run("nil client fails closed", func(t *testing.T) {
		_, _, err := operator.WorkloadObservation(context.Background(), nil, "DEPLOYMENT", "apps", "api", nil)
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
//
//nolint:unparam // releaseName is uniformly "example" across the ingest tests; the helper mirrors the production release shape and stays explicit.
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

// grantObservationAnnotations sets the center-approved annotation whitelist on
// an already-seeded definition (TASK-247). The center re-filters every reported
// annotation against this whitelist before it is persisted, so an ingest test
// that expects annotations on the row must grant them here.
func grantObservationAnnotations(t *testing.T, st store.Store, definitionID string, approved ...store.ApprovedAnnotationKey) {
	t.Helper()
	ctx := t.Context()
	definition, err := st.Definitions().Get(ctx, definitionID)
	require.NoError(t, err)
	definition.ApprovedAnnotationKeys = approved
	_, err = st.Definitions().Update(ctx, definition, nil)
	require.NoError(t, err)
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

// TASK-241 W-b (operator projection): WorkloadObservation reads the workload
// and pod-template annotations, keeps only the center-approved (key, scope)
// pairs and groups them by scope. An unknown whitelist (nil) reports nothing —
// the unapproved `unapproved` value present in the cluster must never appear.
func TestWorkloadObservationApprovedAnnotations(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api", Namespace: "apps", UID: "uid-annotation",
			Annotations: map[string]string{"team": "platform", "tier": "web", "unapproved": "nope"},
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"prometheus.io/scrape": "true"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "registry.example/team/api:1.0.0"}}},
			},
		},
	})

	approved := []store.ApprovedAnnotationKey{
		{Key: "tier", Scope: operator.AnnotationScopeWorkloadMetadata},
		{Key: "team", Scope: operator.AnnotationScopeWorkloadMetadata},
		{Key: "prometheus.io/scrape", Scope: operator.AnnotationScopePodTemplateMetadata},
		{Key: "missing", Scope: operator.AnnotationScopeWorkloadMetadata},
		{Key: "team", Scope: "UNKNOWN_SCOPE"},
	}
	_, observation, err := operator.WorkloadObservation(t.Context(), client, "DEPLOYMENT", "apps", "api", approved)
	require.NoError(t, err)
	assert.Equal(t, map[string]map[string]string{
		"WORKLOAD_METADATA":     {"team": "platform", "tier": "web"},
		"POD_TEMPLATE_METADATA": {"prometheus.io/scrape": "true"},
	}, observation.Annotations)

	// Negative control: an unknown whitelist never projects anything, even
	// though the live object carries annotations.
	_, unknown, err := operator.WorkloadObservation(t.Context(), client, "DEPLOYMENT", "apps", "api", nil)
	require.NoError(t, err)
	assert.Nil(t, unknown.Annotations, "unknown whitelist must report no annotations (fail closed)")
}

// TASK-241 W-c (central projection): the wire's scope-grouped
// current_annotations must reach the release_inventory row through
// UpdateWorkloadObservation, flattened to scope → key → value. Every reported
// key is granted on the definition first (TASK-247): the center re-filters
// ingest against the current whitelist, so an ungranted key is dropped. A
// report whose observation carries no annotation group stores "not observed"
// (nil), never a present-but-empty map, and an observation-less redelivery
// leaves the stored projection untouched.
func TestCommandStreamWorkloadIdentityReportPersistsApprovedAnnotations(t *testing.T) {
	st := newTestSvc(t)
	ctx := t.Context()
	seedObservationDefinition(t, st, "definition-annotations", "example")
	grantObservationAnnotations(t, st, "definition-annotations",
		store.ApprovedAnnotationKey{Key: "team", Scope: "WORKLOAD_METADATA"},
		store.ApprovedAnnotationKey{Key: "tier", Scope: "WORKLOAD_METADATA"},
		store.ApprovedAnnotationKey{Key: "prometheus.io/scrape", Scope: "POD_TEMPLATE_METADATA"},
	)

	observedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	sendIdentityReport(t, st, []*operatorv1.WorkloadIdentityItem{{
		ReleaseNamespace: "apps", ReleaseName: "example",
		Kind: "DEPLOYMENT", Name: "example", Namespace: "apps", Uid: "uid-annotations",
		CurrentAnnotations: []*operatorv1.ScopedAnnotations{
			{Scope: "WORKLOAD_METADATA", Entries: []*operatorv1.AnnotationEntry{
				{Key: "team", Value: "platform"}, {Key: "tier", Value: "web"},
			}},
			{Scope: "POD_TEMPLATE_METADATA", Entries: []*operatorv1.AnnotationEntry{
				{Key: "prometheus.io/scrape", Value: "true"},
			}},
		},
		ObservedAt: timestamppb.New(observedAt),
	}})

	row, err := st.Inventories().GetByReleaseKey(ctx, "cust-1", "clus-1", "apps", "example")
	require.NoError(t, err)
	assert.Equal(t, map[string]map[string]string{
		"WORKLOAD_METADATA":     {"team": "platform", "tier": "web"},
		"POD_TEMPLATE_METADATA": {"prometheus.io/scrape": "true"},
	}, row.ObservedAnnotations)

	// A later observation with observed_at but no annotation group means the
	// whitelist now yields nothing: it clears the stored projection rather than
	// leaving a stale value behind.
	sendIdentityReport(t, st, []*operatorv1.WorkloadIdentityItem{{
		ReleaseNamespace: "apps", ReleaseName: "example",
		Kind: "DEPLOYMENT", Name: "example", Namespace: "apps", Uid: "uid-annotations",
		ObservedAt: timestamppb.New(observedAt.Add(time.Minute)),
	}})
	row, err = st.Inventories().GetByReleaseKey(ctx, "cust-1", "clus-1", "apps", "example")
	require.NoError(t, err)
	assert.Nil(t, row.ObservedAnnotations, "an empty projection must be stored as not-observed")

	// An observation-less redelivery (no observed_at) must not clobber it with
	// the zero value.
	require.NoError(t, st.Inventories().UpdateWorkloadObservation(ctx, "cust-1", "clus-1", "apps", "example", store.WorkloadObservation{
		Annotations: map[string]map[string]string{"WORKLOAD_METADATA": {"team": "platform"}},
		ObservedAt:  observedAt,
	}))
	sendIdentityReport(t, st, []*operatorv1.WorkloadIdentityItem{{
		ReleaseNamespace: "apps", ReleaseName: "example",
		Kind: "DEPLOYMENT", Name: "example", Namespace: "apps", Uid: "uid-annotations",
	}})
	row, err = st.Inventories().GetByReleaseKey(ctx, "cust-1", "clus-1", "apps", "example")
	require.NoError(t, err)
	assert.Equal(t, map[string]map[string]string{"WORKLOAD_METADATA": {"team": "platform"}}, row.ObservedAnnotations,
		"an observation-less report must not clear stored annotations")
}

// TASK-247 case (a): the center re-applies its own annotation whitelist at
// ingest. An operator whose persisted whitelist is stale reports a key the
// definition no longer approves, and only the approved subset may be persisted
// — the removed key cannot survive. An unknown scope is dropped too. Removing
// the filter from applyWorkloadObservation makes the `removed`/`UNKNOWN_SCOPE`
// assertions fail.
func TestCommandStreamAnnotationRefilterKeepsApprovedSubsetOnly(t *testing.T) {
	st := newTestSvc(t)
	ctx := t.Context()
	seedObservationDefinition(t, st, "definition-refilter-subset", "example")
	grantObservationAnnotations(t, st, "definition-refilter-subset",
		store.ApprovedAnnotationKey{Key: "team", Scope: "WORKLOAD_METADATA"},
	)

	reportedAt := time.Now().UTC().Truncate(time.Second)
	sendIdentityReport(t, st, []*operatorv1.WorkloadIdentityItem{{
		ReleaseNamespace: "apps", ReleaseName: "example",
		Kind: "DEPLOYMENT", Name: "example", Namespace: "apps", Uid: "uid-refilter-subset",
		Containers:       []string{"api"},
		CurrentImageRefs: map[string]string{"api": "registry.example/team/api:1.0.0"},
		CurrentAnnotations: []*operatorv1.ScopedAnnotations{
			{Scope: "WORKLOAD_METADATA", Entries: []*operatorv1.AnnotationEntry{
				{Key: "team", Value: "platform"},
				{Key: "removed", Value: "stale"},
			}},
			{Scope: "POD_TEMPLATE_METADATA", Entries: []*operatorv1.AnnotationEntry{
				{Key: "prometheus.io/scrape", Value: "true"},
			}},
			{Scope: "UNKNOWN_SCOPE", Entries: []*operatorv1.AnnotationEntry{
				{Key: "team", Value: "wrong-scope"},
			}},
		},
		ObservedAt: timestamppb.New(reportedAt),
	}})

	row, err := st.Inventories().GetByReleaseKey(ctx, "cust-1", "clus-1", "apps", "example")
	require.NoError(t, err)
	assert.Equal(t, map[string]map[string]string{
		"WORKLOAD_METADATA": {"team": "platform"},
	}, row.ObservedAnnotations,
		"only the (scope, key) the definition approves may be persisted")
	assert.Equal(t, []string{"api"}, row.ObservedContainers,
		"the annotation filter must not disturb the other observed fields")
	assert.WithinDuration(t, reportedAt, row.ObservedAt, time.Second)
}

// TASK-247 case (b): the definition shrinks while the operator keeps its stale
// whitelist (no later release write reaches it), so the operator still reports
// the removed key. The removed key must be dropped from the row, and once the
// definition approves no annotation at all the filtered-empty projection must
// clear the column instead of leaving the old key behind.
func TestCommandStreamAnnotationRefilterDropsRemovedKeyWhenDefinitionShrinks(t *testing.T) {
	st := newTestSvc(t)
	ctx := t.Context()
	seedObservationDefinition(t, st, "definition-refilter-shrink", "example")
	grantObservationAnnotations(t, st, "definition-refilter-shrink",
		store.ApprovedAnnotationKey{Key: "team", Scope: "WORKLOAD_METADATA"},
		store.ApprovedAnnotationKey{Key: "legacy", Scope: "WORKLOAD_METADATA"},
	)

	reportBoth := func(observedAt time.Time) {
		t.Helper()
		sendIdentityReport(t, st, []*operatorv1.WorkloadIdentityItem{{
			ReleaseNamespace: "apps", ReleaseName: "example",
			Kind: "DEPLOYMENT", Name: "example", Namespace: "apps", Uid: "uid-refilter-shrink",
			CurrentAnnotations: []*operatorv1.ScopedAnnotations{
				{Scope: "WORKLOAD_METADATA", Entries: []*operatorv1.AnnotationEntry{
					{Key: "team", Value: "platform"},
					{Key: "legacy", Value: "stale"},
				}},
			},
			ObservedAt: timestamppb.New(observedAt),
		}})
	}

	base := time.Now().UTC().Truncate(time.Second)
	reportBoth(base)
	row, err := st.Inventories().GetByReleaseKey(ctx, "cust-1", "clus-1", "apps", "example")
	require.NoError(t, err)
	require.Equal(t, map[string]map[string]string{
		"WORKLOAD_METADATA": {"team": "platform", "legacy": "stale"},
	}, row.ObservedAnnotations, "precondition: both keys were approved and persisted")

	// The center removes `legacy` without sending the release a new command, so
	// the operator's persisted whitelist stays stale and it keeps reporting it.
	grantObservationAnnotations(t, st, "definition-refilter-shrink",
		store.ApprovedAnnotationKey{Key: "team", Scope: "WORKLOAD_METADATA"},
	)
	reportBoth(base.Add(time.Minute))
	row, err = st.Inventories().GetByReleaseKey(ctx, "cust-1", "clus-1", "apps", "example")
	require.NoError(t, err)
	assert.Equal(t, map[string]map[string]string{
		"WORKLOAD_METADATA": {"team": "platform"},
	}, row.ObservedAnnotations, "the center-removed key must not survive ingest")

	// The definition now approves no annotation at all while the stale operator
	// still reports both: the filtered-empty result must drop them all.
	grantObservationAnnotations(t, st, "definition-refilter-shrink")
	reportBoth(base.Add(2 * time.Minute))
	row, err = st.Inventories().GetByReleaseKey(ctx, "cust-1", "clus-1", "apps", "example")
	require.NoError(t, err)
	assert.Nil(t, row.ObservedAnnotations, "an all-unapproved report must clear the projection")
}

// TASK-247 case (c): a report that carries no annotations at all (an operator
// with no whitelist) is not evidence about the definition, and the filter must
// not fabricate one. The pre-existing ingest semantics are preserved: the
// other observed fields still refresh, while the annotation column keeps the
// store's "empty projection is not observed" rule so the read model stops
// advertising the old value. Preserving the stored projection here would let a
// definition-removed key survive whenever a fresh operator (empty local store)
// reports the identity without annotations — exactly the window this card
// closes — so clearing is deliberate (filterApprovedAnnotations, case c).
func TestCommandStreamAnnotationRefilterWithoutAnnotationsKeepsExistingSemantics(t *testing.T) {
	st := newTestSvc(t)
	ctx := t.Context()
	seedObservationDefinition(t, st, "definition-refilter-none", "example")
	grantObservationAnnotations(t, st, "definition-refilter-none",
		store.ApprovedAnnotationKey{Key: "team", Scope: "WORKLOAD_METADATA"},
	)

	base := time.Now().UTC().Truncate(time.Second)
	sendIdentityReport(t, st, []*operatorv1.WorkloadIdentityItem{{
		ReleaseNamespace: "apps", ReleaseName: "example",
		Kind: "DEPLOYMENT", Name: "example", Namespace: "apps", Uid: "uid-refilter-none",
		CurrentAnnotations: []*operatorv1.ScopedAnnotations{
			{Scope: "WORKLOAD_METADATA", Entries: []*operatorv1.AnnotationEntry{{Key: "team", Value: "platform"}}},
		},
		ObservedAt: timestamppb.New(base),
	}})
	row, err := st.Inventories().GetByReleaseKey(ctx, "cust-1", "clus-1", "apps", "example")
	require.NoError(t, err)
	require.Equal(t, map[string]map[string]string{"WORKLOAD_METADATA": {"team": "platform"}}, row.ObservedAnnotations)

	// The operator now reports the identity with observed_at but no annotation
	// group: its whitelist is unknown/empty. Containers and observed_at still
	// refresh; annotations keep the existing "not observed" semantics rather
	// than retaining the previous value.
	sendIdentityReport(t, st, []*operatorv1.WorkloadIdentityItem{{
		ReleaseNamespace: "apps", ReleaseName: "example",
		Kind: "DEPLOYMENT", Name: "example", Namespace: "apps", Uid: "uid-refilter-none",
		Containers:       []string{"api"},
		CurrentImageRefs: map[string]string{"api": "registry.example/team/api:2.0.0"},
		ObservedAt:       timestamppb.New(base.Add(time.Minute)),
	}})
	row, err = st.Inventories().GetByReleaseKey(ctx, "cust-1", "clus-1", "apps", "example")
	require.NoError(t, err)
	assert.Nil(t, row.ObservedAnnotations, "no annotation payload keeps the existing not-observed semantics")
	assert.Equal(t, []string{"api"}, row.ObservedContainers, "the rest of the observation still updates")
	assert.WithinDuration(t, base.Add(time.Minute), row.ObservedAt, time.Second)
}

package operator_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
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

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

	"github.com/ndzuki/release-manager/internal/operator"
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

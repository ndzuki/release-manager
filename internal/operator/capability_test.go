package operator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakediscovery "k8s.io/client-go/discovery/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

// ADR-026 V1: the capability version must change when the served resource set
// changes -- a CRD appearing or disappearing is exactly what has to invalidate a
// cached dry-run, and the audit's fail-open was replaying a "passed" after one
// had been removed. Returning a constant makes this test fail.
func TestDiscoveryCapabilityVersionChangesWithServedResources(t *testing.T) {
	t.Parallel()

	clientset := kubefake.NewSimpleClientset()
	fakeDiscovery, ok := clientset.Discovery().(*fakediscovery.FakeDiscovery)
	require.True(t, ok)

	probe := NewDiscoveryCapabilityVersioner(fakeDiscovery)
	before, err := probe.CapabilityVersion(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, before)

	fakeDiscovery.Resources = []*metav1.APIResourceList{{
		GroupVersion: "example.com/v1",
		APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget"}},
	}}
	after, err := probe.CapabilityVersion(t.Context())
	require.NoError(t, err)

	assert.NotEqual(t, before, after, "a changed resource set must change the capability version")

	again, err := probe.CapabilityVersion(t.Context())
	require.NoError(t, err)
	assert.Equal(t, after, again, "the version must be stable for the same cluster state")
}

// A probe without a client is a configuration error, not a silent empty version.
func TestDiscoveryCapabilityVersionRequiresAClient(t *testing.T) {
	t.Parallel()

	_, err := NewDiscoveryCapabilityVersioner(nil).CapabilityVersion(t.Context())
	assert.Error(t, err)
}

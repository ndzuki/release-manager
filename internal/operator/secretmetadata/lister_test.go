package secretmetadata

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKubernetesListerListReturnsOnlySortedMetadata(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "z-last", Namespace: "apps"}, Data: map[string][]byte{"token": []byte("super-secret"), "ca.crt": []byte("certificate")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "a-first", Namespace: "apps"}, Data: map[string][]byte{"username": []byte("admin")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "ignored", Namespace: "other"}, Data: map[string][]byte{"password": []byte("hidden")}},
	)

	secrets, err := New(client).List(t.Context(), "apps")
	require.NoError(t, err)
	require.Len(t, secrets, 2)
	assert.Equal(t, "a-first", secrets[0].Name)
	assert.Equal(t, []string{"username"}, secrets[0].Keys)
	assert.Equal(t, "z-last", secrets[1].Name)
	assert.Equal(t, []string{"ca.crt", "token"}, secrets[1].Keys)
	encoded, err := json.Marshal(secrets)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "super-secret")
	assert.NotContains(t, string(encoded), "certificate")
	assert.NotContains(t, string(encoded), "admin")
	assert.NotContains(t, string(encoded), "hidden")
}

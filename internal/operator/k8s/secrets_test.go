package k8s

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestResolveInjectsMatchingSecret(t *testing.T) {
	value := []byte("secret-value")
	client := kubernetesfake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "database",
			Namespace:       "apps",
			UID:             types.UID("uid-1"),
			ResourceVersion: "7",
		},
		Data: map[string][]byte{"password": value},
	})
	values := map[string]any{"database": map[string]any{"password": "placeholder"}}
	ref := &operatorv1.SecretRef{
		Path:            "database.password",
		Name:            "database",
		Key:             "password",
		Uid:             "uid-1",
		ResourceVersion: "7",
		ValueDigest:     digest(value),
	}

	snapshotDigest, err := Resolve(t.Context(), client.CoreV1(), "apps", []*operatorv1.SecretRef{ref}, values)
	require.NoError(t, err)
	assert.NotEmpty(t, snapshotDigest)
	database, ok := values["database"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "secret-value", database["password"])
}

func TestResolveRejectsChangedSecret(t *testing.T) {
	client := kubernetesfake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "database",
			Namespace:       "apps",
			UID:             types.UID("uid-new"),
			ResourceVersion: "8",
		},
		Data: map[string][]byte{"password": []byte("new")},
	})
	values := map[string]any{"database": map[string]any{}}
	ref := &operatorv1.SecretRef{
		Path:            "database.password",
		Name:            "database",
		Key:             "password",
		Uid:             "uid-old",
		ResourceVersion: "7",
		ValueDigest:     digest([]byte("old")),
	}

	_, err := Resolve(context.Background(), client.CoreV1(), "apps", []*operatorv1.SecretRef{ref}, values)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret_ref_changed")
	database, ok := values["database"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, database, "password")
}

func TestResolveSupportsArrayPath(t *testing.T) {
	value := []byte("pull-secret")
	client := kubernetesfake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pull", Namespace: "apps"},
		Data:       map[string][]byte{"name": value},
	})
	values := map[string]any{"imagePullSecrets": []any{map[string]any{"name": "placeholder"}}}
	ref := &operatorv1.SecretRef{Path: "imagePullSecrets[0].name", Name: "pull", Key: "name"}

	_, err := Resolve(t.Context(), client.CoreV1(), "apps", []*operatorv1.SecretRef{ref}, values)
	require.NoError(t, err)
	items, ok := values["imagePullSecrets"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, items)
	entry, ok := items[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "pull-secret", entry["name"])
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return fmt.Sprintf("%x", sum)
}

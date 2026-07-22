//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const installTimeout = 90 * time.Second

func TestInstallSDK(t *testing.T) {
	t.Parallel()

	adminKubeconfig, adminConfig, adminClient := loadCluster(t)
	assertNoHelmOrKubectl(t)

	t.Run("installs without helm or kubectl", func(t *testing.T) {
		t.Parallel()

		namespace, releaseName := isolatedTarget(t, adminClient, "success")
		engine := newEngine(adminKubeconfig)

		release, err := engine.Install(t.Context(), helmengine.InstallOptions{
			Namespace:   namespace,
			ReleaseName: releaseName,
			ChartPath:   chartPath(t, "install-success"),
			Values:      map[string]interface{}{"message": "installed-without-cli"},
			Atomic:      true,
			Timeout:     installTimeout,
		})
		require.NoError(t, err)
		assert.Equal(t, releaseName, release.Name)
		assert.Equal(t, namespace, release.Namespace)
		assert.Equal(t, 1, release.Revision)
		assert.Equal(t, "deployed", release.Status)
		assert.NotEmpty(t, release.ManifestDigest)

		configMap, err := adminClient.CoreV1().ConfigMaps(namespace).Get(
			t.Context(), releaseName+"-payload", metav1.GetOptions{},
		)
		require.NoError(t, err)
		assert.Equal(t, "installed-without-cli", configMap.Data["message"])

		items, err := engine.List(t.Context(), namespace)
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, releaseName, items[0].Name)
		assert.Equal(t, 1, items[0].Revision)
		assert.Equal(t, "deployed", items[0].Status)
	})

	t.Run("atomic failure removes release and resources", func(t *testing.T) {
		t.Parallel()

		namespace, releaseName := isolatedTarget(t, adminClient, "atomic")
		engine := newEngine(adminKubeconfig)

		_, err := engine.Install(t.Context(), helmengine.InstallOptions{
			Namespace:   namespace,
			ReleaseName: releaseName,
			ChartPath:   chartPath(t, "install-failing-hook"),
			Atomic:      true,
			Timeout:     installTimeout,
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, helmengine.ErrActionFailed)

		_, statusErr := engine.Status(t.Context(), helmengine.StatusOptions{
			Namespace:   namespace,
			ReleaseName: releaseName,
		})
		assert.ErrorIs(t, statusErr, helmengine.ErrNotFound)

		items, listErr := engine.List(t.Context(), namespace)
		require.NoError(t, listErr)
		assert.Empty(t, items)

		_, resourceErr := adminClient.CoreV1().ConfigMaps(namespace).Get(
			t.Context(), releaseName+"-payload", metav1.GetOptions{},
		)
		assert.True(t, apierrors.IsNotFound(resourceErr), "payload ConfigMap remains after atomic cleanup: %v", resourceErr)
	})

	t.Run("forbidden is returned without privilege escalation", func(t *testing.T) {
		t.Parallel()

		namespace, releaseName := isolatedTarget(t, adminClient, "forbidden")
		restrictedConfig := createRestrictedIdentity(t, adminConfig, adminClient, namespace)
		engine := newEngine(writeKubeconfig(t, restrictedConfig, "restricted"))

		before, err := adminClient.RbacV1().ClusterRoleBindings().List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err)

		_, installErr := engine.Install(t.Context(), helmengine.InstallOptions{
			Namespace:   namespace,
			ReleaseName: releaseName,
			ChartPath:   chartPath(t, "install-success"),
			Atomic:      true,
			Timeout:     installTimeout,
		})
		require.Error(t, installErr)
		assert.ErrorIs(t, installErr, helmengine.ErrForbidden)
		assert.Contains(t, strings.ToLower(installErr.Error()), "forbidden")

		after, err := adminClient.RbacV1().ClusterRoleBindings().List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err)
		assert.Equal(t, clusterRoleBindingNames(before.Items), clusterRoleBindingNames(after.Items))

		secrets, err := adminClient.CoreV1().Secrets(namespace).List(t.Context(), metav1.ListOptions{
			LabelSelector: "owner=helm,name=" + releaseName,
		})
		require.NoError(t, err)
		assert.Empty(t, secrets.Items)
	})
}

func loadCluster(t *testing.T) (string, *rest.Config, kubernetes.Interface) {
	t.Helper()

	kubeconfig := os.Getenv("KUBECONFIG")
	require.NotEmpty(t, kubeconfig, "KUBECONFIG must point to the isolated integration cluster")

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	require.NoError(t, err)
	config.Timeout = 30 * time.Second

	client, err := kubernetes.NewForConfig(config)
	require.NoError(t, err)
	_, err = client.Discovery().ServerVersion()
	require.NoError(t, err)
	return kubeconfig, config, client
}

func assertNoHelmOrKubectl(t *testing.T) {
	t.Helper()

	for _, binary := range []string{"helm", "kubectl"} {
		for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
			path := filepath.Join(directory, binary)
			_, err := os.Stat(path)
			if err == nil {
				t.Fatalf("forbidden CLI %q is available in integration PATH", binary)
			}
			if !os.IsNotExist(err) {
				t.Fatalf("check integration PATH for %q: %v", binary, err)
			}
		}
	}
}

func isolatedTarget(t *testing.T, client kubernetes.Interface, suffix string) (string, string) {
	t.Helper()

	unique := fmt.Sprintf("%d", time.Now().UnixNano())
	namespace := "sdk-install-" + suffix + "-" + unique
	releaseName := "release-" + suffix + "-" + unique

	_, err := client.CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cleanupErr := client.CoreV1().Namespaces().Delete(cleanupCtx, namespace, metav1.DeleteOptions{}); cleanupErr != nil && !apierrors.IsNotFound(cleanupErr) {
			t.Errorf("delete namespace %s: %v", namespace, cleanupErr)
		}
	})

	return namespace, releaseName
}

func newEngine(kubeconfig string) *helmengine.RealEngine {
	return helmengine.NewRealEngine(
		kubeconfig,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}

func createRestrictedIdentity(
	t *testing.T,
	adminConfig *rest.Config,
	adminClient kubernetes.Interface,
	namespace string,
) *rest.Config {
	t.Helper()

	const serviceAccountName = "restricted-installer"
	_, err := adminClient.CoreV1().ServiceAccounts(namespace).Create(t.Context(), &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = adminClient.RbacV1().Roles(namespace).Create(t.Context(), &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = adminClient.RbacV1().RoleBindings(namespace).Create(t.Context(), &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: serviceAccountName, Namespace: namespace},
		},
		RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: serviceAccountName},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	token, err := adminClient.CoreV1().ServiceAccounts(namespace).CreateToken(
		t.Context(), serviceAccountName, &authenticationv1.TokenRequest{}, metav1.CreateOptions{},
	)
	require.NoError(t, err)

	restricted := rest.CopyConfig(adminConfig)
	restricted.BearerToken = token.Status.Token
	restricted.BearerTokenFile = ""
	restricted.Username = ""
	restricted.Password = ""
	restricted.ExecProvider = nil
	restricted.AuthProvider = nil
	return restricted
}

func writeKubeconfig(t *testing.T, config *rest.Config, name string) string {
	t.Helper()

	clusterName := name + "-cluster"
	userName := name + "-user"
	contextName := name + "-context"
	kubeconfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			clusterName: {
				Server:                   config.Host,
				CertificateAuthorityData: append([]byte(nil), config.CAData...),
				InsecureSkipTLSVerify:    config.Insecure,
				TLSServerName:            config.ServerName,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			userName: {Token: config.BearerToken},
		},
		Contexts: map[string]*clientcmdapi.Context{
			contextName: {Cluster: clusterName, AuthInfo: userName},
		},
		CurrentContext: contextName,
	}

	path := filepath.Join(t.TempDir(), name+"-kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(kubeconfig, path))
	return path
}

func chartPath(t *testing.T, name string) string {
	t.Helper()

	path, err := filepath.Abs(filepath.Join("testdata", name))
	require.NoError(t, err)
	return path
}

func clusterRoleBindingNames(items []rbacv1.ClusterRoleBinding) []string {
	names := make([]string, len(items))
	for i := range items {
		names[i] = items[i].Name
	}
	sort.Strings(names)
	return names
}

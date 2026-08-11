//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
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

const installTimeout = 30 * time.Second

func TestInstallSDK(t *testing.T) {
	t.Parallel()

	_, adminConfig, adminClient := loadCluster(t)
	assertNoHelmOrKubectl(t)

	t.Run("installs without helm or kubectl", func(t *testing.T) {
		t.Parallel()

		namespace, releaseName := isolatedTarget(t, adminClient, "success")
		installerConfig := createMinimalInstaller(t, adminConfig, adminClient, namespace)
		engine := newEngine(writeKubeconfig(t, installerConfig, "minimal"))

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
		installerConfig := createMinimalInstaller(t, adminConfig, adminClient, namespace)
		engine := newEngine(writeKubeconfig(t, installerConfig, "atomic"))

		_, err := engine.Install(t.Context(), helmengine.InstallOptions{
			Namespace:   namespace,
			ReleaseName: releaseName,
			ChartPath:   chartPath(t, "install-failing-hook"),
			Values:      map[string]interface{}{"message": "must-be-removed"},
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

		secrets, secretErr := adminClient.CoreV1().Secrets(namespace).List(t.Context(), metav1.ListOptions{
			LabelSelector: "owner=helm,name=" + releaseName,
		})
		require.NoError(t, secretErr)
		assert.Empty(t, secrets.Items)

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
		before := captureRBACSnapshot(t, adminClient, namespace)

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

		after := captureRBACSnapshot(t, adminClient, namespace)
		assert.Equal(t, before, after)

		secrets, err := adminClient.CoreV1().Secrets(namespace).List(t.Context(), metav1.ListOptions{
			LabelSelector: "owner=helm,name=" + releaseName,
		})
		require.NoError(t, err)
		assert.Empty(t, secrets.Items)

		_, resourceErr := adminClient.CoreV1().ConfigMaps(namespace).Get(
			t.Context(), releaseName+"-payload", metav1.GetOptions{},
		)
		assert.True(t, apierrors.IsNotFound(resourceErr), "payload ConfigMap exists after forbidden install: %v", resourceErr)
	})

	t.Run("concurrent installs remain isolated", func(t *testing.T) {
		t.Parallel()

		namespaceA, releaseA := isolatedTarget(t, adminClient, "conc-a")
		namespaceB, releaseB := isolatedTarget(t, adminClient, "conc-b")
		installerConfig := createMinimalInstaller(t, adminConfig, adminClient, namespaceA, namespaceB)
		engine := newEngine(writeKubeconfig(t, installerConfig, "concurrent"))

		cases := []struct {
			namespace   string
			releaseName string
			message     string
		}{
			{namespace: namespaceA, releaseName: releaseA, message: "message-a"},
			{namespace: namespaceB, releaseName: releaseB, message: "message-b"},
		}
		results := make([]struct {
			release *helmengine.Release
			err     error
		}, len(cases))

		var waitGroup sync.WaitGroup
		for i, installCase := range cases {
			waitGroup.Add(1)
			go func() {
				defer waitGroup.Done()
				results[i].release, results[i].err = engine.Install(t.Context(), helmengine.InstallOptions{
					Namespace:   installCase.namespace,
					ReleaseName: installCase.releaseName,
					ChartPath:   chartPath(t, "install-success"),
					Values:      map[string]interface{}{"message": installCase.message},
					Atomic:      true,
					Timeout:     installTimeout,
				})
			}()
		}
		waitGroup.Wait()

		for i, installCase := range cases {
			require.NoError(t, results[i].err)
			require.NotNil(t, results[i].release)
			assert.Equal(t, 1, results[i].release.Revision)
			assert.Equal(t, "deployed", results[i].release.Status)

			configMap, err := adminClient.CoreV1().ConfigMaps(installCase.namespace).Get(
				t.Context(), installCase.releaseName+"-payload", metav1.GetOptions{},
			)
			require.NoError(t, err)
			assert.Equal(t, installCase.message, configMap.Data["message"])

			items, err := engine.List(t.Context(), installCase.namespace)
			require.NoError(t, err)
			require.Len(t, items, 1)
			assert.Equal(t, installCase.releaseName, items[0].Name)
		}
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
			return
		}

		for {
			_, getErr := client.CoreV1().Namespaces().Get(cleanupCtx, namespace, metav1.GetOptions{})
			if apierrors.IsNotFound(getErr) {
				return
			}
			if getErr != nil {
				t.Errorf("verify namespace %s deletion: %v", namespace, getErr)
				return
			}
			select {
			case <-cleanupCtx.Done():
				t.Errorf("namespace %s still exists after cleanup: %v", namespace, cleanupCtx.Err())
				return
			case <-time.After(100 * time.Millisecond):
			}
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
	return createInstallerIdentity(
		t,
		adminConfig,
		adminClient,
		"restricted-installer",
		[]string{namespace},
		[]string{"secrets"},
	)
}

func createMinimalInstaller(
	t *testing.T,
	adminConfig *rest.Config,
	adminClient kubernetes.Interface,
	namespaces ...string,
) *rest.Config {
	t.Helper()
	return createInstallerIdentity(
		t,
		adminConfig,
		adminClient,
		"minimal-installer",
		namespaces,
		[]string{"secrets", "configmaps"},
	)
}

func createInstallerIdentity(
	t *testing.T,
	adminConfig *rest.Config,
	adminClient kubernetes.Interface,
	serviceAccountName string,
	namespaces []string,
	resources []string,
) *rest.Config {
	t.Helper()
	require.NotEmpty(t, namespaces)

	identityNamespace := namespaces[0]
	_, err := adminClient.CoreV1().ServiceAccounts(identityNamespace).Create(t.Context(), &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	for _, namespace := range namespaces {
		_, err = adminClient.RbacV1().Roles(namespace).Create(t.Context(), &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: resources,
					Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		_, err = adminClient.RbacV1().RoleBindings(namespace).Create(t.Context(), &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName},
			Subjects: []rbacv1.Subject{
				{Kind: "ServiceAccount", Name: serviceAccountName, Namespace: identityNamespace},
			},
			RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: serviceAccountName},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	token, err := adminClient.CoreV1().ServiceAccounts(identityNamespace).CreateToken(
		t.Context(), serviceAccountName, &authenticationv1.TokenRequest{}, metav1.CreateOptions{},
	)
	require.NoError(t, err)

	installer := rest.CopyConfig(adminConfig)
	installer.BearerToken = token.Status.Token
	installer.BearerTokenFile = ""
	installer.Username = ""
	installer.Password = ""
	installer.ExecProvider = nil
	installer.AuthProvider = nil
	return installer
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

	directory, err := os.MkdirTemp("", "install-sdk-"+name+"-")
	require.NoError(t, err)
	path := filepath.Join(directory, name+"-kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(kubeconfig, path))
	t.Cleanup(func() {
		if cleanupErr := os.RemoveAll(directory); cleanupErr != nil {
			t.Errorf("remove kubeconfig directory %s: %v", directory, cleanupErr)
			return
		}
		if _, statErr := os.Stat(directory); !os.IsNotExist(statErr) {
			t.Errorf("kubeconfig directory %s remains after cleanup: %v", directory, statErr)
		}
	})
	return path
}

func chartPath(t *testing.T, name string) string {
	t.Helper()

	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "locate integration test source")
	return filepath.Join(filepath.Dir(sourceFile), "testdata", name)
}

type rbacSnapshot struct {
	Roles               []string
	RoleBindings        []string
	ClusterRoles        []string
	ClusterRoleBindings []string
}

func captureRBACSnapshot(
	t *testing.T,
	client kubernetes.Interface,
	namespace string,
) rbacSnapshot {
	t.Helper()

	roles, err := client.RbacV1().Roles(namespace).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	roleBindings, err := client.RbacV1().RoleBindings(namespace).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	clusterRoles, err := client.RbacV1().ClusterRoles().List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	clusterRoleBindings, err := client.RbacV1().ClusterRoleBindings().List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)

	return rbacSnapshot{
		Roles:               objectNames(roles.Items, func(item rbacv1.Role) string { return item.Name }),
		RoleBindings:        objectNames(roleBindings.Items, func(item rbacv1.RoleBinding) string { return item.Name }),
		ClusterRoles:        objectNames(clusterRoles.Items, func(item rbacv1.ClusterRole) string { return item.Name }),
		ClusterRoleBindings: objectNames(clusterRoleBindings.Items, func(item rbacv1.ClusterRoleBinding) string { return item.Name }),
	}
}

func objectNames[T any](items []T, name func(T) string) []string {
	names := make([]string, len(items))
	for i, item := range items {
		names[i] = name(item)
	}
	sort.Strings(names)
	return names
}

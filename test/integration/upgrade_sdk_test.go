//go:build integration

package integration_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
)

// upgradeTimeout allows the engine's render preflight + real upgrade sequence
// (with atomic wait) to complete on a freshly created kind cluster.
const upgradeTimeout = 90 * time.Second

// TestUpgradeSDK exercises the real Helm SDK Upgrade path against a real
// Kubernetes API server (REQ-086 AC-086-03/04, REQ-062 AC-062-05/06):
//   - a revision-2 upgrade succeeds and persists a 63-char rm_input_digest
//     label (the D-111 defect made Helm write a 64-char hex digest, which the
//     API server rejects because label values cap at 63 chars);
//   - replaying the same command is idempotent (no revision increment);
//   - the API server gate for over-long label values is real (negative
//     sentinel), which is the failure the engine now encodes away.
func TestUpgradeSDK(t *testing.T) {
	t.Parallel()

	_, adminConfig, adminClient := loadCluster(t)
	assertNoHelmOrKubectl(t)

	t.Run("revision 2 upgrade persists valid 63-char rm_input_digest label", func(t *testing.T) {
		t.Parallel()

		namespace, releaseName := isolatedTarget(t, adminClient, "upgrade")
		installerConfig := createMinimalInstaller(t, adminConfig, adminClient, namespace)
		engine := newEngine(writeKubeconfig(t, installerConfig, "upgrade"))
		chartArchive, chartDigest := packageChart(t, chartPath(t, "install-success"))

		// Revision 1 (INSTALL). Install writes no rm_input_digest label.
		installed, err := engine.Install(t.Context(), helmengine.InstallOptions{
			Namespace:   namespace,
			ReleaseName: releaseName,
			ChartPath:   chartArchive,
			Values:      map[string]interface{}{"message": "rev1"},
			Atomic:      true,
			Timeout:     upgradeTimeout,
		})
		require.NoError(t, err)
		require.Equal(t, 1, installed.Revision)
		require.Empty(t, installed.Labels["rm_input_digest"], "install must not carry the digest label")

		// Upgrade to revision 2 with all four input digests non-empty.
		opts := helmengine.UpgradeOptions{
			Namespace: namespace, ReleaseName: releaseName, ChartPath: chartArchive,
			Values:           map[string]interface{}{"message": "upgraded-v2"},
			ExpectedRevision: 1, Atomic: true, Timeout: upgradeTimeout,
			OperationID: "operation-upgrade-label", CommandID: "command-upgrade-label",
			BundleDigest: "sha256:bundle-aaa", ChartDigest: chartDigest,
			EffectiveValuesDigest: "sha256:values-aaa", SecretSnapshotDigest: "sha256:secret-aaa",
		}
		_, fullDigest := expectedInputDigest(opts)
		expectedLabel := fullDigest[:63]

		rel, err := engine.Upgrade(t.Context(), opts)
		require.NoError(t, err)
		require.Equal(t, 2, rel.Revision)
		assert.Equal(t, "deployed", rel.Status)
		assert.NotEmpty(t, rel.ManifestDigest)
		assert.NotEqual(t, installed.ManifestDigest, rel.ManifestDigest, "revision 2 must render a changed manifest")
		assert.Equal(t, expectedLabel, rel.Labels["rm_input_digest"],
			"returned model label must be the 63-char encoded digest")

		// AC-062-05: the target ConfigMap reflects the new effective values.
		configMap, err := adminClient.CoreV1().ConfigMaps(namespace).Get(
			t.Context(), releaseName+"-payload", metav1.GetOptions{},
		)
		require.NoError(t, err)
		assert.Equal(t, "upgraded-v2", configMap.Data["message"])

		// AC-086-03: the persisted revision-2 release Secret carries the
		// encoded label and satisfies Kubernetes label value rules.
		secret := helmReleaseSecret(t, adminClient, namespace, releaseName, "2")
		label := secret.Labels["rm_input_digest"]
		assert.Equal(t, expectedLabel, label, "release Secret label must be the 63-char encoded digest")
		assert.Len(t, label, 63)
		assert.Empty(t, validation.IsValidLabelValue(label), "persisted label must satisfy K8s label value rules")
		assert.NotContains(t, label, string(opts.BundleDigest), "label must not leak raw input segments")

		items, err := engine.List(t.Context(), namespace)
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, 2, items[0].Revision)
	})

	t.Run("replayed upgrade command is idempotent", func(t *testing.T) {
		t.Parallel()

		namespace, releaseName := isolatedTarget(t, adminClient, "upgrade-replay")
		installerConfig := createMinimalInstaller(t, adminConfig, adminClient, namespace)
		engine := newEngine(writeKubeconfig(t, installerConfig, "upgrade-replay"))
		chartArchive, chartDigest := packageChart(t, chartPath(t, "install-success"))

		_, err := engine.Install(t.Context(), helmengine.InstallOptions{
			Namespace: namespace, ReleaseName: releaseName, ChartPath: chartArchive,
			Values: map[string]interface{}{"message": "rev1"}, Atomic: true, Timeout: upgradeTimeout,
		})
		require.NoError(t, err)

		opts := helmengine.UpgradeOptions{
			Namespace: namespace, ReleaseName: releaseName, ChartPath: chartArchive,
			Values:           map[string]interface{}{"message": "upgraded-v2"},
			ExpectedRevision: 1, Atomic: true, Timeout: upgradeTimeout,
			OperationID: "operation-upgrade-replay", CommandID: "command-upgrade-replay",
			BundleDigest: "sha256:bundle-aaa", ChartDigest: chartDigest,
			EffectiveValuesDigest: "sha256:values-aaa", SecretSnapshotDigest: "sha256:secret-aaa",
		}

		first, err := engine.Upgrade(t.Context(), opts)
		require.NoError(t, err)
		require.Equal(t, 2, first.Revision)

		// AC-062-06 / AC-086-02: same frozen command replayed → no revision
		// increment and no new release Secret; the label stays stable.
		replayed, err := engine.Upgrade(t.Context(), opts)
		require.NoError(t, err)
		assert.Equal(t, 2, replayed.Revision, "replay must not increment the revision")
		assert.Equal(t, first.Description, replayed.Description)
		assert.Equal(t, first.Labels["rm_input_digest"], replayed.Labels["rm_input_digest"])

		secrets, err := adminClient.CoreV1().Secrets(namespace).List(t.Context(), metav1.ListOptions{
			LabelSelector: "owner=helm,name=" + releaseName,
		})
		require.NoError(t, err)
		require.Len(t, secrets.Items, 2, "replay must not create a revision-3 Secret")

		configMap, err := adminClient.CoreV1().ConfigMaps(namespace).Get(
			t.Context(), releaseName+"-payload", metav1.GetOptions{},
		)
		require.NoError(t, err)
		assert.Equal(t, "upgraded-v2", configMap.Data["message"], "replay must not re-apply resources")
	})

	t.Run("api server rejects an over-long rm_input_digest label", func(t *testing.T) {
		t.Parallel()

		// Negative sentinel (REQ-086 AC-086-04): bypassing the engine, writing
		// a release Secret whose rm_input_digest label is a full 64-char hex
		// digest must be rejected by the API server (label value > 63). This is
		// the real gate the D-111 defect tripped; the engine's encodeLabelDigest
		// makes such a value constructively unreachable on the success path.
		namespace, _ := isolatedTarget(t, adminClient, "upgrade-neg")
		overLong := strings.Repeat("ab", 32) // 64-char hex digest
		require.Len(t, overLong, 64)

		_, err := adminClient.CoreV1().Secrets(namespace).Create(t.Context(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "neg-label-" + namespace,
				Namespace: namespace,
				Labels: map[string]string{
					"owner": "helm", "name": "neg-release",
					"version": "1", "status": "deployed",
					"rm_input_digest": overLong,
				},
			},
		}, metav1.CreateOptions{})
		require.Error(t, err)
		assert.True(t, apierrors.IsInvalid(err), "over-long label value must surface as an Invalid API error: %v", err)
		// The API server rejects at the label-value field: the message carries
		// the offending value and the 63-byte cap. (The label key is not part
		// of the value-validation message.)
		assert.Contains(t, err.Error(), "metadata.labels")
		assert.Contains(t, err.Error(), "no more than 63 bytes")
		assert.Contains(t, err.Error(), overLong[:8], "message should echo the offending label value")
	})
}

// helmReleaseSecret returns the Helm release Secret for the given revision
// (identified by the owner/name/version system labels Helm writes).
func helmReleaseSecret(
	t *testing.T,
	client kubernetes.Interface,
	namespace, releaseName, version string,
) corev1.Secret {
	t.Helper()

	secrets, err := client.CoreV1().Secrets(namespace).List(t.Context(), metav1.ListOptions{
		LabelSelector: "owner=helm,name=" + releaseName,
	})
	require.NoError(t, err)
	for _, secret := range secrets.Items {
		if secret.Labels["version"] == version {
			return secret
		}
	}
	t.Fatalf("release Secret %s/%s version %s not found (found %d)", namespace, releaseName, version, len(secrets.Items))
	return corev1.Secret{}
}

// expectedInputDigest reproduces the engine's input digest over the four
// frozen upgrade digests and returns both the full 64-char hex and its
// 63-char label encoding (REQ-086 D1: sha256 hex truncated to 63 chars).
func expectedInputDigest(opts helmengine.UpgradeOptions) (fullHex, encoded string) {
	fullHex = fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join([]string{
		opts.BundleDigest, opts.ChartDigest, opts.EffectiveValuesDigest, opts.SecretSnapshotDigest,
	}, "|"))))
	return fullHex, fullHex[:63]
}

// packageChart tars and gzips a chart directory into a standard
// `<name>-<version>` archive (the layout `helm package` produces and
// helm's LoadArchive expects) and returns the archive path plus its sha256
// hex digest, mirroring the engine's local-chart digest verification path.
func packageChart(t *testing.T, chartDir string) (path, digest string) {
	t.Helper()

	var buf bytes.Buffer
	gzipWriter := gzip.NewWriter(&buf)
	tarWriter := tar.NewWriter(gzipWriter)

	root := "install-success-0.1.0"
	err := filepath.Walk(chartDir, func(current string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == chartDir {
			return nil
		}
		rel, relErr := filepath.Rel(chartDir, current)
		if relErr != nil {
			return relErr
		}
		header, headerErr := tar.FileInfoHeader(info, "")
		if headerErr != nil {
			return headerErr
		}
		header.Name = filepath.ToSlash(filepath.Join(root, rel))
		if info.IsDir() {
			header.Name += "/"
		}
		if writeErr := tarWriter.WriteHeader(header); writeErr != nil {
			return writeErr
		}
		if info.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(current)
		if readErr != nil {
			return readErr
		}
		_, writeErr := io.Copy(tarWriter, bytes.NewReader(data))
		return writeErr
	})
	require.NoError(t, err)
	require.NoError(t, tarWriter.Close())
	require.NoError(t, gzipWriter.Close())

	archive := buf.Bytes()
	path = filepath.Join(t.TempDir(), "install-success-0.1.0.tgz")
	require.NoError(t, os.WriteFile(path, archive, 0o644))
	return path, fmt.Sprintf("%x", sha256.Sum256(archive))
}

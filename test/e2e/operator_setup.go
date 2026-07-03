//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// deployOperator builds the operator image, loads it into kind, and deploys via Helm.
//
// customerID identifies the customer for this operator instance.
// notifEndpoint is the manager's gRPC address that the operator reports back to.
// caFile, certFile, keyFile are mTLS certificate paths.
//
// Returns a cleanup function that removes the operator namespace.
func deployOperator(ctx context.Context, clientset kubernetes.Interface, clusterName, customerID, notifEndpoint, caFile, certFile, keyFile string) (func(), error) {
	ns := fmt.Sprintf("release-operator-%s", customerID)

	// Create namespace
	if _, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
		log.Printf("warning: create namespace %s: %v", ns, err)
	}

	// Helper to run kubectl commands
	kubectl := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "kubectl", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("kubectl %v: %w\n%s", args, err, string(out))
		}
		return nil
	}

	// Create TLS secret (kubernetes.io/tls type) via kubectl.
	// Delete first for idempotency.
	_ = kubectl("-n", ns, "delete", "secret", "release-operator-tls", "--ignore-not-found")
	if err := kubectl("-n", ns, "create", "secret", "tls", "release-operator-tls",
		fmt.Sprintf("--cert=%s", certFile), fmt.Sprintf("--key=%s", keyFile)); err != nil {
		return nil, fmt.Errorf("create TLS secret: %w", err)
	}

	// Create CA secret (Opaque type with ca.crt key) via kubectl.
	_ = kubectl("-n", ns, "delete", "secret", "release-operator-ca", "--ignore-not-found")
	if err := kubectl("-n", ns, "create", "secret", "generic", "release-operator-ca",
		fmt.Sprintf("--from-file=ca.crt=%s", caFile)); err != nil {
		return nil, fmt.Errorf("create CA secret: %w", err)
	}

	// Build and load image (if SKIP_BUILD is not set)
	if os.Getenv("SKIP_BUILD") != "1" {
		if err := buildAndLoadImage(ctx, clusterName, "release-operator", "release-operator:dev", "Dockerfile.operator"); err != nil {
			return nil, err
		}
	}

	// Deploy via Helm
	helmCmd := exec.CommandContext(ctx, "helm", "upgrade", "--install",
		fmt.Sprintf("release-operator-%s", customerID),
		filepath.Join(projectRoot(), "deployments/release-operator"),
		"-n", ns,
		"--set", fmt.Sprintf("customerID=%s", customerID),
		"--set", fmt.Sprintf("notificationEndpoint=%s", notifEndpoint),
		"--set", "image.repository=release-operator",
		"--set", "image.tag=dev",
		"--set", "image.pullPolicy=IfNotPresent",
		"--set", "tls.enabled=true",
		"--set", "tls.existingCertSecret=release-operator-tls",
		"--set", "tls.existingCaSecret=release-operator-ca",
		"--set", "harbor.insecureSkipVerify=true",
		"--set", "rbac.managedNamespaces[0]=default",
		"--set", fmt.Sprintf("serviceAccount.name=release-operator-%s", customerID),
		"--set", "networkPolicy.enabled=false",
		"--timeout", "2m",
	)
	if out, err := helmCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("helm install operator: %w\n%s", err, string(out))
	}

	// Wait for operator pod ready
	if err := waitForPodReady(ctx, clientset, ns, "app.kubernetes.io/name=release-operator", 2*time.Minute); err != nil {
		return nil, fmt.Errorf("wait for operator pod: %w", err)
	}

	// Patch operator configmap to disable TLS. The chart hardcodes cert_file/key_file;
	// we clear them so serveGRPC starts without TLS, matching the manager's insecure mode.
	cmName := fmt.Sprintf("release-operator-%s-config", customerID)
	getCmd := exec.CommandContext(ctx, "kubectl", "-n", ns, "get", "configmap", cmName, "-o", "yaml")
	cmYAML, err := getCmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("get operator configmap: %w\n%s", err, string(cmYAML))
	}
	// Replace TLS file paths with empty strings and disable client cert requirement
	patched := strings.ReplaceAll(string(cmYAML),
		`ca_file: "/etc/release-operator/tls/ca.crt"`, `ca_file: ""`)
	patched = strings.ReplaceAll(patched,
		`cert_file: "/etc/release-operator/tls/tls.crt"`, `cert_file: ""`)
	patched = strings.ReplaceAll(patched,
		`key_file: "/etc/release-operator/tls/tls.key"`, `key_file: ""`)
	patched = strings.ReplaceAll(patched,
		`require_client_cert: true`, `require_client_cert: false`)
	applyCmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	applyCmd.Stdin = strings.NewReader(patched)
	if out, err := applyCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("apply patched operator configmap: %w\n%s", err, string(out))
	}
	// Restart operator to pick up new config
	restartCmd := exec.CommandContext(ctx, "kubectl", "-n", ns, "rollout", "restart",
		fmt.Sprintf("deployment/release-operator-%s", customerID))
	if out, err := restartCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("restart operator: %w\n%s", err, string(out))
	}
	// Wait for new operator pod ready
	if err := waitForPodReady(ctx, clientset, ns, "app.kubernetes.io/name=release-operator", 2*time.Minute); err != nil {
		return nil, fmt.Errorf("wait for operator pod after restart: %w", err)
	}

	cleanup := func() {
		_ = clientset.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
	}

	return cleanup, nil
}
